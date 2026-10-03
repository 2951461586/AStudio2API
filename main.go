// Command astudio2api exposes the AStudio (讯飞星辰 / Astron Studio) model
// upstream as an OpenAI-compatible gateway.
//
// It reuses the login session the desktop app already stores on disk, so no
// extra credentials are required beyond having AStudio installed and signed in.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"astudio2api/internal/registry"
	"astudio2api/internal/server"
	"astudio2api/internal/store"
)

// version is overridable at build time: -ldflags "-X main.version=1.0.0"
var version = "1.0.0"

func main() {
	var (
		hostFlag     = flag.String("host", "", "listen address (env ASTUDIO_HOST)")
		portFlag     = flag.Int("port", 0, "listen port (env ASTUDIO_PORT)")
		dataFlag     = flag.String("data", "", "state file path (env ASTUDIO_DATA_PATH)")
		passwordFlag = flag.String("password", "", "panel password (env ASTUDIO_ADMIN_PASSWORD)")
		astronFlag   = flag.String("astron-dir", "", "AStudio data directory to import from (env ASTUDIO_DATA_DIR)")
		onceFlag     = flag.Bool("sync-once", false, "refresh the model directory and exit")
		versionFlag  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *versionFlag {
		fmt.Println("astudio2api", version)
		return
	}

	dataPath := firstNonEmpty(*dataFlag, os.Getenv("ASTUDIO_DATA_PATH"), defaultDataPath())
	st, err := store.Open(dataPath)
	if err != nil {
		log.Fatalf("open state %s: %v", dataPath, err)
	}

	applyEnvOverrides(st, *hostFlag, *portFlag, *passwordFlag, *astronFlag)
	ensureAdminPassword(st, firstNonEmpty(*passwordFlag, os.Getenv("ASTUDIO_ADMIN_PASSWORD")))

	settings := st.Settings()
	reg := registry.New()

	srv := server.New(st, reg, version)

	// Best-effort bootstrap: import the local desktop session and warm the
	// directory so the first client request does not pay for discovery.
	bootstrap(srv, st, settings.AstronDataDir)

	if *onceFlag {
		if err := srv.SyncModels(context.Background()); err != nil {
			log.Fatalf("sync failed: %v", err)
		}
		log.Printf("model directory refreshed")
		return
	}

	// Periodic maintenance: flush state and keep the directory reasonably fresh.
	stop := make(chan struct{})
	go maintain(srv, st, stop)

	settings = st.Settings()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(settings.Host, settings.Port) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		if err != nil {
			log.Printf("server stopped: %v", err)
		}
	}
	close(stop)
	if err := st.Save(); err != nil {
		log.Printf("final state flush failed: %v", err)
	}
}

// applyEnvOverrides lets environment variables and CLI flags win over the
// persisted settings.
func applyEnvOverrides(st *store.Store, host string, port int, password, astronDir string) {
	envHost := os.Getenv("ASTUDIO_HOST")
	envPort := os.Getenv("ASTUDIO_PORT")
	envPassword := os.Getenv("ASTUDIO_ADMIN_PASSWORD")
	envAstron := os.Getenv("ASTUDIO_DATA_DIR")

	_ = st.UpdateSettings(func(s *store.Settings) {
		if v := firstNonEmpty(host, envHost); v != "" {
			s.Host = v
		}
		if port > 0 {
			s.Port = port
		} else if v := strings.TrimSpace(envPort); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
				s.Port = n
			}
		}
		if v := firstNonEmpty(password, envPassword); v != "" {
			s.Password = v
			s.PasswordGenerated = false
		}
		if v := firstNonEmpty(astronDir, envAstron); v != "" {
			s.AstronDataDir = v
		}
	})
}

// legacyDefaultPassword is the guessable password older releases shipped.
// A state file still holding it is treated as unconfigured on upgrade.
const legacyDefaultPassword = "admin"

// ensureAdminPassword guarantees the panel never runs on a guessable default.
// An explicitly configured password (flag or env) always wins; otherwise the
// first run, or an upgrade still carrying the legacy "admin" value, generates a
// random password, persists it and prints it once.
func ensureAdminPassword(st *store.Store, explicit string) {
	if explicit != "" {
		return // applyEnvOverrides already stored the operator's choice
	}
	s := st.Settings()
	if pw := strings.TrimSpace(s.Password); pw != "" && pw != legacyDefaultPassword {
		return
	}
	password := store.RandomPassword()
	if err := st.UpdateSettings(func(x *store.Settings) {
		x.Password = password
		x.PasswordGenerated = true
	}); err != nil {
		log.Printf("could not persist the initial panel password: %v", err)
		return
	}
	log.Printf("──────────────────────────────────────────────────────────")
	log.Printf(" 首次启动：已生成控制台密码  %s", password)
	log.Printf(" 登录 http://127.0.0.1:%d/ 后请在「设置」中修改", s.Port)
	log.Printf("──────────────────────────────────────────────────────────")
}

// bootstrap imports the desktop session (if any) and refreshes the directory.
func bootstrap(srv *server.Server, st *store.Store, dataDir string) {
	accounts := st.Accounts()
	if len(accounts) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		acct, err := srv.ImportFromAStudio(ctx, dataDir)
		if err != nil {
			log.Printf("no AStudio session imported yet: %v", err)
			log.Printf("→ open the panel at http://127.0.0.1:%d/ and click 「一键导入」", st.Settings().Port)
			return
		}
		log.Printf("imported AStudio account %s (%s)", acct.AccountID, acct.UID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := srv.SyncModels(ctx); err != nil {
		log.Printf("model directory refresh failed: %v", err)
	}
}

// maintain periodically flushes state, keeps credentials warm, refreshes the
// model directory, and drives the 签到 / 保活 schedule.
func maintain(srv *server.Server, st *store.Store, stop <-chan struct{}) {
	flush := time.NewTicker(30 * time.Second)
	defer flush.Stop()
	directory := time.NewTicker(6 * time.Hour)
	defer directory.Stop()
	credentials := time.NewTicker(6 * time.Hour)
	defer credentials.Stop()
	// Settings are read on every tick so the panel takes effect without a restart.
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()

	// One base context for all background work, cancelled on shutdown so an
	// in-flight check-in or keepalive sweep stops promptly.
	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()
	go func() {
		<-stop
		cancelAll()
	}()

	var lastKeepalive time.Time

	// 启动即补签：不必等到下一个整点，也能补上关机 / 睡眠期间错过的签到。
	maybeCheckin(ctx, srv, st)

	for {
		select {
		case <-stop:
			return
		case <-flush.C:
			if err := st.Save(); err != nil {
				log.Printf("state flush failed: %v", err)
			}
		case <-directory.C:
			syncCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			if err := srv.SyncModels(syncCtx); err != nil {
				log.Printf("scheduled model refresh failed: %v", err)
			}
			cancel()
		case <-credentials.C:
			if st.Settings().KeepaliveMinutes > 0 {
				continue // the keepalive tick already refreshes credentials
			}
			credCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			for _, err := range srv.KeepaliveAll(credCtx) {
				log.Printf("credential refresh failed: %v", err)
			}
			cancel()
		case <-tick.C:
			settings := st.Settings()
			now := time.Now()

			// 过点即补：哪怕进程整点不在（关机 / 睡眠 / 晚启动），只要已经过了
			// checkin_hour 且今天还没跑过，就补一次。
			maybeCheckin(ctx, srv, st)

			if settings.KeepaliveMinutes > 0 && now.Sub(lastKeepalive) >= time.Duration(settings.KeepaliveMinutes)*time.Minute {
				lastKeepalive = now
				keepCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				errs := srv.KeepaliveAll(keepCtx)
				cancel()
				if len(errs) > 0 {
					log.Printf("保活完成，%d 个账号失败（首个：%v）", len(errs), errs[0])
				} else {
					log.Printf("保活完成，全部账号正常")
				}
			}
		}
	}
}

// maybeCheckin runs the daily check-in once per day, as soon as the configured
// hour has been reached. It is called on start-up and on every tick, so a machine
// that was off or asleep over the configured hour still catches up. The run date
// is persisted, so restarts do not repeat a completed check-in.
func maybeCheckin(ctx context.Context, srv *server.Server, st *store.Store) {
	settings := st.Settings()
	now := time.Now()
	if !checkinDue(settings.AutoCheckin, settings.CheckinHour, st.LastCheckinDay(), now) {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	results := srv.CheckinAll(checkCtx)
	st.SetLastCheckinDay(now.Format("2006-01-02"))
	// 立即落盘：否则进程在 30s 周期刷盘前退出时，重启会重复跑一次签到。
	if err := st.Save(); err != nil {
		log.Printf("签到日期落盘失败: %v", err)
	}
	for _, r := range results {
		if r.Error != "" {
			log.Printf("签到失败 %s: %s", r.Name, r.Error)
			continue
		}
		log.Printf("签到完成 %s: 积分=%d Spark=%d 增量=%d 领取=%v", r.Name, r.Points, r.Spark, r.PointsDelta, r.Actions)
	}
}

// checkinDue reports whether the automatic check-in should run now: enabled, the
// configured hour reached, and not already run today.
func checkinDue(auto bool, hour int, lastDay string, now time.Time) bool {
	if !auto {
		return false
	}
	if hour >= 0 && now.Hour() < hour {
		return false
	}
	return lastDay != now.Format("2006-01-02")
}

func defaultDataPath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "astudio2api-data.json")
	}
	return "astudio2api-data.json"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
