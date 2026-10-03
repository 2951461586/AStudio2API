package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"astudio2api/internal/astron"
	"astudio2api/internal/store"
)

const sessionCookie = "astudio2api_session"
const sessionTTL = 12 * time.Hour

const maxLoginAttempts = 5
const loginLockout = 5 * time.Minute

func (s *Server) handleAdminAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/")
	switch path {
	case "login":
		s.adminLogin(w, r)
		return
	case "logout":
		s.adminLogout(w, r)
		return
	case "state":
		if !s.requireSession(w, r) {
			return
		}
		s.adminState(w, r)
		return
	}
	if !s.requireSession(w, r) {
		return
	}
	switch path {
	case "keys":
		s.adminKeys(w, r)
	case "accounts":
		s.adminAccounts(w, r)
	case "settings":
		s.adminSettings(w, r)
	case "models":
		s.adminModels(w, r)
	case "logs":
		s.adminLogs(w, r)
	case "stats":
		s.adminStats(w, r)
	case "benefits":
		s.adminBenefits(w, r)
	case "login/geetest":
		s.adminLoginGeetest(w, r)
	case "login/sms":
		s.adminLoginSms(w, r)
	case "login/verify":
		s.adminLoginVerify(w, r)
	case "checkin":
		s.adminCheckin(w, r)
	case "redeem":
		s.adminRedeem(w, r)
	case "keepalive":
		s.adminKeepalive(w, r)
	case "password":
		s.adminPassword(w, r)
	default:
		http.NotFound(w, r)
	}
}

// --- sessions ---------------------------------------------------------------

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	ip := clientIP(r)
	if retryAfter, blocked := s.loginBlocked(ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "Too many failed attempts. Try again later."})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
		return
	}
	expected := s.store.Settings().Password
	// An unset/empty password must never authenticate, even if the panel
	// password was never configured (defence in depth for a failed first run).
	if expected == "" || subtle.ConstantTimeCompare([]byte(body.Password), []byte(expected)) != 1 {
		s.recordLoginFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Incorrect password."})
		return
	}
	s.clearLoginFailures(ip)

	token := randomToken()
	s.sessionsMu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL).Unix()
	s.sessionsMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessionsMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Not signed in."})
		return false
	}
	now := time.Now().Unix()
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	expiry, ok := s.sessions[cookie.Value]
	if !ok || expiry < now {
		delete(s.sessions, cookie.Value)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Session expired."})
		return false
	}
	s.sessions[cookie.Value] = now + int64(sessionTTL.Seconds())
	return true
}

func (s *Server) loginBlocked(ip string) (int, bool) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	a := s.loginGuard[ip]
	if a == nil {
		return 0, false
	}
	if a.count >= maxLoginAttempts && time.Now().Before(a.until) {
		return int(time.Until(a.until).Seconds()) + 1, true
	}
	if time.Now().After(a.until) {
		delete(s.loginGuard, ip)
	}
	return 0, false
}

func (s *Server) recordLoginFailure(ip string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	a := s.loginGuard[ip]
	if a == nil {
		a = &attempts{}
		s.loginGuard[ip] = a
	}
	a.count++
	backoff := time.Duration(a.count) * 2 * time.Second
	if backoff > loginLockout {
		backoff = loginLockout
	}
	a.until = time.Now().Add(backoff)
}

func (s *Server) clearLoginFailures(ip string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	delete(s.loginGuard, ip)
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// --- state ------------------------------------------------------------------

func (s *Server) adminState(w http.ResponseWriter, r *http.Request) {
	settings := s.store.Settings()
	settings.Password = "" // never send the password
	count, source, refreshedAt, lastErr := s.registry.Status()

	accounts := s.store.Accounts()
	safeAccounts := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		safeAccounts = append(safeAccounts, map[string]any{
			"id":               a.ID,
			"name":             a.Name,
			"account_id":       a.AccountID,
			"uid":              a.UID,
			"mobile":           a.Mobile,
			"nickname":         a.Nickname,
			"domain_account":   a.DomainAccount,
			"source":           a.Source,
			"enabled":          a.Enabled,
			"has_credential":   strings.TrimSpace(a.ModelBearer) != "",
			"credential_hint":  maskSecret(a.ModelBearer),
			"last_error":       a.LastError,
			"cooldown_until":   a.CooldownUntil,
			"successes":        a.Successes,
			"failures":         a.Failures,
			"last_used_at":     a.LastUsedAt,
			"created_at":       a.CreatedAt,
			"points_known":     a.PointsKnown,
			"points_balance":   a.PointsBalance,
			"spark_balance":    a.SparkBalance,
			"points_expire_at": a.PointsExpireAt,
			"plan_code":        a.PlanCode,
			"plan_name":        a.PlanName,
			"plan_active":      a.PlanActive,
			"pending_popups":   a.PendingPopups,
			"last_checked_at":  a.LastCheckedAt,
			"last_claim_at":    a.LastClaimAt,
			"session_ok":       a.SessionOK,
			"status_note":      a.StatusNote,
		})
	}

	logs := s.store.Logs(200)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.Version,
		"settings": settings,
		"keys":     s.store.Keys(),
		"accounts": safeAccounts,
		"models": map[string]any{
			"count":        count,
			"source":       source,
			"refreshed_at": refreshedAt,
			"error":        lastErr,
			"items":        s.registry.List(),
		},
		"stats":    s.store.StatsSnapshot(),
		"logs":     logs,
		"benefits": s.store.Benefits(200),
		"autodetect": map[string]any{
			"data_dir":      settings.AstronDataDir,
			"session_found": detectSession(settings.AstronDataDir),
			"default_hint":  astron.DefaultWorkspaceAPI,
		},
	})
}

func detectSession(dataDir string) bool {
	_, err := astron.LocateSessionFile(dataDir)
	return err == nil
}

func maskSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 12 {
		return strings.Repeat("•", len(v))
	}
	return v[:8] + "…" + v[len(v)-4:]
}

// --- keys -------------------------------------------------------------------

func (s *Server) adminKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"keys": s.store.Keys()})
	case http.MethodPost:
		var body struct {
			Key     string `json:"key"`
			Note    string `json:"note"`
			Action  string `json:"action"`
			ID      string `json:"id"`
			Enabled *bool  `json:"enabled"`
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 32<<10))
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
			return
		}
		switch body.Action {
		case "toggle":
			if body.Enabled == nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "`enabled` is required."})
				return
			}
			if err := s.store.SetKeyEnabled(body.ID, *body.Enabled); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			key, err := s.store.AddKey(strings.TrimSpace(body.Key), strings.TrimSpace(body.Note))
			if err != nil {
				status := http.StatusInternalServerError
				if err == store.ErrDuplicate {
					status = http.StatusConflict
				}
				writeJSON(w, status, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"key": key})
		}
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if err := s.store.RemoveKey(id); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Unsupported method."})
	}
}

// --- accounts ---------------------------------------------------------------

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		Action        string `json:"action"`
		ID            string `json:"id"`
		Enabled       *bool  `json:"enabled"`
		DataDir       string `json:"data_dir"`
		AccountID     string `json:"account_id"`
		Token         string `json:"token"`
		SSOSessionID  string `json:"sso_session_id"`
		BearerToken   string `json:"bearer_token"`
		Name          string `json:"name"`
		DomainAccount string `json:"domain_account"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
		return
	}

	switch body.Action {
	case "import":
		dataDir := strings.TrimSpace(body.DataDir)
		acct, err := s.ImportFromAStudio(r.Context(), dataDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if dataDir != "" {
			_ = s.store.UpdateSettings(func(st *store.Settings) { st.AstronDataDir = dataDir })
		}
		writeJSON(w, http.StatusOK, map[string]any{"account": acct})
	case "manual":
		if strings.TrimSpace(body.BearerToken) == "" && strings.TrimSpace(body.Token) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Provide a model bearer token, or a session cookie (account_id + token)."})
			return
		}
		acct := &store.Account{
			Name:          firstNonEmpty(body.Name, body.AccountID),
			AccountID:     body.AccountID,
			Token:         body.Token,
			SSOSessionID:  body.SSOSessionID,
			ModelBearer:   body.BearerToken,
			DomainAccount: strings.TrimSpace(body.DomainAccount),
			Source:        "manual",
		}
		if acct.AccountID == "" {
			acct.AccountID = "manual-" + store.NewID()
		}
		saved, err := s.store.UpsertAccount(acct)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// A cookie-only import still needs the exchange to obtain a bearer.
		if strings.TrimSpace(saved.ModelBearer) == "" {
			_ = s.RefreshAccount(r.Context(), saved.ID)
		}
		_ = s.SyncModels(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"account": saved})
	case "refresh":
		if err := s.RefreshAccount(r.Context(), body.ID); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "toggle":
		if body.Enabled == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "`enabled` is required."})
			return
		}
		if err := s.store.SetAccountEnabled(body.ID, *body.Enabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "domain":
		if err := s.store.UpdateAccount(body.ID, func(a *store.Account) {
			a.DomainAccount = strings.TrimSpace(body.DomainAccount)
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "remove":
		if err := s.store.RemoveAccount(body.ID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Unknown action."})
	}
}

// --- settings ---------------------------------------------------------------

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		UpstreamBase     *string            `json:"upstream_base"`
		ModelsBase       *string            `json:"models_base"`
		WorkspaceAPI     *string            `json:"workspace_api"`
		StudioVersion    *string            `json:"studio_version"`
		AstronDataDir    *string            `json:"astron_data_dir"`
		RequestTimeoutS  *int               `json:"request_timeout_seconds"`
		IdleTimeoutS     *int               `json:"idle_timeout_seconds"`
		LogRetentionDays *int               `json:"log_retention_days"`
		LogMaxEntries    *int               `json:"log_max_entries"`
		MaxConcurrency   *int               `json:"max_concurrency"`
		CORSOrigins      *[]string          `json:"cors_origins"`
		ModelAliases     *map[string]string `json:"model_aliases"`

		AutoCheckin           *bool `json:"auto_checkin"`
		CheckinHour           *int  `json:"checkin_hour"`
		KeepaliveMinutes      *int  `json:"keepalive_minutes"`
		BalanceAwareRotation  *bool `json:"balance_aware_rotation"`
		CheckinCompletePopups *bool `json:"checkin_complete_popups"`
		CheckinClaimDownload  *bool `json:"checkin_claim_download_reward"`
		CheckinClaimBeta      *bool `json:"checkin_claim_beta"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
		return
	}
	err := s.store.UpdateSettings(func(st *store.Settings) {
		if body.UpstreamBase != nil {
			st.UpstreamBase = strings.TrimRight(strings.TrimSpace(*body.UpstreamBase), "/")
		}
		if body.ModelsBase != nil {
			st.ModelsBase = strings.TrimRight(strings.TrimSpace(*body.ModelsBase), "/")
		}
		if body.WorkspaceAPI != nil {
			st.WorkspaceAPI = strings.TrimRight(strings.TrimSpace(*body.WorkspaceAPI), "/")
		}
		if body.StudioVersion != nil {
			st.StudioVersion = strings.TrimSpace(*body.StudioVersion)
		}
		if body.AstronDataDir != nil {
			st.AstronDataDir = strings.TrimSpace(*body.AstronDataDir)
		}
		if body.RequestTimeoutS != nil && *body.RequestTimeoutS > 0 && *body.RequestTimeoutS <= 3600 {
			st.RequestTimeoutS = *body.RequestTimeoutS
		}
		if body.IdleTimeoutS != nil && *body.IdleTimeoutS > 0 && *body.IdleTimeoutS <= 3600 {
			st.IdleTimeoutS = *body.IdleTimeoutS
		}
		if body.LogRetentionDays != nil && *body.LogRetentionDays > 0 && *body.LogRetentionDays <= 3650 {
			st.LogRetentionDays = *body.LogRetentionDays
		}
		if body.LogMaxEntries != nil && *body.LogMaxEntries >= 100 && *body.LogMaxEntries <= 100000 {
			st.LogMaxEntries = *body.LogMaxEntries
		}
		if body.MaxConcurrency != nil && *body.MaxConcurrency > 0 && *body.MaxConcurrency <= 64 {
			st.MaxConcurrency = *body.MaxConcurrency
		}
		if body.CORSOrigins != nil {
			st.CORSOrigins = *body.CORSOrigins
		}
		if body.ModelAliases != nil {
			st.ModelAliases = *body.ModelAliases
		}
		if body.AutoCheckin != nil {
			st.AutoCheckin = *body.AutoCheckin
		}
		if body.CheckinHour != nil && *body.CheckinHour >= 0 && *body.CheckinHour <= 23 {
			st.CheckinHour = *body.CheckinHour
		}
		if body.KeepaliveMinutes != nil && *body.KeepaliveMinutes >= 0 && *body.KeepaliveMinutes <= 1440 {
			st.KeepaliveMinutes = *body.KeepaliveMinutes
		}
		if body.BalanceAwareRotation != nil {
			st.BalanceAwareRotation = *body.BalanceAwareRotation
		}
		if body.CheckinCompletePopups != nil {
			st.CheckinCompletePopups = *body.CheckinCompletePopups
		}
		if body.CheckinClaimDownload != nil {
			st.CheckinClaimDownload = *body.CheckinClaimDownload
		}
		if body.CheckinClaimBeta != nil {
			st.CheckinClaimBeta = *body.CheckinClaimBeta
		}
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.Reload()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Current), []byte(s.store.Settings().Password)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Current password is incorrect."})
		return
	}
	if len(strings.TrimSpace(body.Next)) < 4 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "New password must be at least 4 characters."})
		return
	}
	if err := s.store.UpdateSettings(func(st *store.Settings) {
		st.Password = body.Next
		st.PasswordGenerated = false
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- models, stats, logs ----------------------------------------------------

func (s *Server) adminModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		count, source, refreshedAt, lastErr := s.registry.Status()
		writeJSON(w, http.StatusOK, map[string]any{
			"count": count, "source": source, "refreshed_at": refreshedAt, "error": lastErr,
			"items": s.registry.List(),
		})
		return
	}
	if err := s.SyncModels(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "items": s.registry.List()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": s.registry.List()})
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := s.store.ResetStats(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	stats := s.store.StatsSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":  stats,
		"hourly": hourlyTrend(stats),
	})
}

// hourlyTrend renders the last 24 hourly buckets in chronological order.
func hourlyTrend(stats store.Stats) []map[string]any {
	type bucket struct {
		key string
		*store.BucketStat
	}
	buckets := make([]bucket, 0, len(stats.ByHour))
	for k, v := range stats.ByHour {
		buckets = append(buckets, bucket{k, v})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].key < buckets[j].key })
	if len(buckets) > 24 {
		buckets = buckets[len(buckets)-24:]
	}
	out := make([]map[string]any, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, map[string]any{
			"hour":              b.key,
			"requests":          b.Requests,
			"prompt_tokens":     b.PromptTokens,
			"completion_tokens": b.CompletionTokens,
		})
	}
	return out
}

// --- 权益中心 --------------------------------------------------------------

func (s *Server) adminBenefits(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		if err := s.store.ClearBenefits(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"benefits": s.store.Benefits(300)})
}

// adminCheckin runs the check-in / claim flow for one account, or all of them.
func (s *Server) adminCheckin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if strings.TrimSpace(body.ID) != "" {
		result := s.CheckinAccount(r.Context(), body.ID)
		if result.Error != "" {
			writeJSON(w, http.StatusBadGateway, map[string]any{"results": []any{result}, "error": result.Error})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{result}})
		return
	}
	results := s.CheckinAll(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// adminRedeem redeems a code against one account.
func (s *Server) adminRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
		return
	}
	if body.ID == "" {
		accounts := s.store.Accounts()
		if len(accounts) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有可用账号。"})
			return
		}
		body.ID = accounts[0].ID
	}
	result, err := s.RedeemForAccount(r.Context(), body.ID, body.Code)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

// adminKeepalive refreshes credentials and status for one account or the pool.
func (s *Server) adminKeepalive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if strings.TrimSpace(body.ID) != "" {
		if err := s.KeepaliveAccount(r.Context(), body.ID); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	errs := s.KeepaliveAll(r.Context())
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		messages = append(messages, err.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": len(errs) == 0, "errors": messages})
}

func (s *Server) adminLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": s.store.Logs(limit)})
}
