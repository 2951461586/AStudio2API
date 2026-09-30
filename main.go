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

	"astudio2api/internal/astron"
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

	settings := st.Settings()
	client := astron.NewClient(settings.UpstreamBase, settings.ModelsBase, settings.WorkspaceAPI, settings.StudioVersion)
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

	_ = client

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
		}
		if v := firstNonEmpty(astronDir, envAstron); v != "" {
			s.AstronDataDir = v
		}
	})
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

	var lastCheckinDay string
	var lastKeepalive time.Time

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

			if settings.AutoCheckin && now.Hour() == settings.CheckinHour {
				if day := now.Format("2006-01-02"); lastCheckinDay != day {
					lastCheckinDay = day
					checkCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
					results := srv.CheckinAll(checkCtx)
					cancel()
					for _, r := range results {
						if r.Error != "" {
							log.Printf("签到失败 %s: %s", r.Name, r.Error)
							continue
						}
						log.Printf("签到完成 %s: 积分=%d Spark=%d 领取=%v", r.Name, r.Points, r.Spark, r.Actions)
					}
				}
			}

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
