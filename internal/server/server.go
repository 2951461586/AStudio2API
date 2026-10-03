// Package server exposes the OpenAI-compatible gateway surface and the
// operator panel.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"astudio2api/internal/astron"
	"astudio2api/internal/registry"
	"astudio2api/internal/store"
	"astudio2api/internal/web"
)

// Server wires the store, model registry and upstream client behind one mux.
type Server struct {
	store    *store.Store
	registry *registry.Registry
	web      *web.Panel

	mu     sync.RWMutex
	client *astron.Client

	cursor    int32
	admission chan struct{}

	sessionsMu sync.Mutex
	sessions   map[string]int64
	loginGuard map[string]*attempts

	syncMu      sync.Mutex
	lastSyncAt  time.Time
	lastSyncErr string

	Version string
}

type attempts struct {
	count int
	until time.Time
}

// New builds a server from persisted state.
func New(st *store.Store, reg *registry.Registry, version string) *Server {
	s := &Server{
		store:      st,
		registry:   reg,
		Version:    version,
		sessions:   map[string]int64{},
		loginGuard: map[string]*attempts{},
	}
	s.refreshClient()
	settings := st.Settings()
	s.admission = make(chan struct{}, maxInt(1, settings.MaxConcurrency))
	s.web = web.NewPanel(version)
	reg.SetAliases(settings.ModelAliases)
	return s
}

// Client returns the upstream client built from current settings.
func (s *Server) Client() *astron.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client
}

func (s *Server) refreshClient() {
	settings := s.store.Settings()
	s.mu.Lock()
	s.client = astron.NewClient(settings.UpstreamBase, settings.ModelsBase, settings.WorkspaceAPI, settings.StudioVersion)
	s.mu.Unlock()
	s.resizeAdmission(settings.MaxConcurrency)
	s.registry.SetAliases(settings.ModelAliases)
}

func (s *Server) resizeAdmission(n int) {
	n = maxInt(1, n)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cap(s.admission) == n {
		return
	}
	s.admission = make(chan struct{}, n)
}

// Reload re-reads settings after a configuration change.
func (s *Server) Reload() { s.refreshClient() }

// Store exposes the backing store to the panel.
func (s *Server) Store() *store.Store { return s.store }

// Registry exposes the model directory to the panel.
func (s *Server) Registry() *registry.Registry { return s.registry }

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/v1/models", s.auth(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.auth(s.handleChatCompletions))
	mux.HandleFunc("/v1/responses", s.auth(s.handleResponses))
	mux.HandleFunc("/v1/messages", s.auth(s.handleAnthropicMessages))
	mux.HandleFunc("/admin/api/", s.handleAdminAPI)
	return withRecovery(s.withCORS(mux))
}

// --- middleware -------------------------------------------------------------

// withCORS adds CORS headers only for origins the operator explicitly allowed.
// The default (empty list) permits loopback origins only, which keeps the
// browser panel working while refusing cross-site requests from other hosts.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		allowed := origin != "" && s.originAllowed(origin)
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version, anthropic-beta")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			if origin != "" && !allowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed compares the request Origin against the configured allow-list,
// defaulting to loopback origins when the list is empty.
func (s *Server) originAllowed(origin string) bool {
	configured := s.store.Settings().CORSOrigins
	if len(configured) == 0 {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		host := u.Hostname()
		if host == "localhost" {
			return true
		}
		if ip := net.ParseIP(host); ip != nil {
			return ip.IsLoopback()
		}
		return false
	}
	for _, candidate := range configured {
		if strings.EqualFold(strings.TrimRight(candidate, "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic serving %s %s: %v", r.Method, r.URL.Path, rec)
				writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type ctxKey string

const keyCtxKey ctxKey = "apikey"

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if token == "" {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "Missing API key. Pass it as `Authorization: Bearer <key>` or `x-api-key`.")
			return
		}
		// Bootstrap: if the operator has not created a key yet, accept the panel
		// password so the gateway is usable immediately after install.
		if !s.store.HasKeys() {
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.store.Settings().Password)) == 1 {
				next(w, r)
				return
			}
		}
		key, ok := s.store.Authenticate(token)
		if !ok {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "Invalid API key.")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), keyCtxKey, key)))
	}
}

func extractToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("x-api-key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("api-key")); v != "" {
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return ""
	}
	lower := strings.ToLower(auth)
	for _, prefix := range []string{"bearer ", "token "} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(auth[len(prefix):])
		}
	}
	return auth
}

func currentKey(r *http.Request) *store.APIKey {
	if k, ok := r.Context().Value(keyCtxKey).(*store.APIKey); ok {
		return k
	}
	return nil
}

// --- basic endpoints --------------------------------------------------------

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.web.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	accounts := s.store.Accounts()
	ready := 0
	for _, a := range accounts {
		if a.Enabled && strings.TrimSpace(a.ModelBearer) != "" {
			ready++
		}
	}
	count, source, refreshedAt, lastErr := s.registry.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"version":             s.Version,
		"accounts":            len(accounts),
		"accounts_ready":      ready,
		"models":              count,
		"models_source":       source,
		"models_refreshed_at": refreshedAt,
		"models_error":        lastErr,
		"workspace":           s.store.Settings().WorkspaceAPI,
		"upstream":            s.store.Settings().UpstreamBase,
	})
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "pong")
}

// --- model directory --------------------------------------------------------

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Use GET.")
		return
	}
	if n, _, _, _ := s.registry.Status(); n == 0 {
		_ = s.SyncModels(r.Context())
	}
	models := s.registry.List()
	data := make([]map[string]any, 0, len(models))
	now := time.Now().Unix()
	for _, m := range models {
		entry := map[string]any{
			"id":       m.Slug,
			"object":   "model",
			"created":  now,
			"owned_by": "astron",
		}
		extra := map[string]any{
			"display_name":   m.Name,
			"context_window": m.ContextWindow,
			"source":         m.Source,
		}
		if m.DirectoryID != "" {
			extra["directory_id"] = m.DirectoryID
		}
		if m.Provider != "" {
			extra["provider"] = m.Provider
		}
		if m.PointMultiplier != "" {
			extra["point_multiplier"] = m.PointMultiplier
		}
		if len(m.ReasoningEfforts) > 0 {
			extra["reasoning_efforts"] = m.ReasoningEfforts
		}
		if m.Description != "" {
			extra["description"] = m.Description
		}
		entry["astron"] = extra
		data = append(data, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// SyncModels refreshes the directory from upstream.
func (s *Server) SyncModels(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	accounts := s.store.Accounts()
	var session *astron.Session
	var bearer string
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		if bearer == "" && strings.TrimSpace(a.ModelBearer) != "" {
			bearer = a.ModelBearer
		}
		if session == nil && a.Token != "" {
			session = &astron.Session{
				AccountID: a.AccountID, UID: a.UID, Token: a.Token,
				SSOSessionID: a.SSOSessionID, ModelBearerToken: a.ModelBearer,
			}
		}
	}
	err := s.registry.Sync(ctx, s.Client(), session, bearer)
	s.lastSyncAt = time.Now()
	if err != nil {
		s.lastSyncErr = err.Error()
		return err
	}
	s.lastSyncErr = ""
	return nil
}

// ImportFromAStudio reads the desktop app session from disk and adds it to the
// pool, refreshing the model credential in the same pass.
func (s *Server) ImportFromAStudio(ctx context.Context, dataDir string) (*store.Account, error) {
	session, path, err := astron.LoadSession(dataDir)
	if err != nil {
		bearer, uid, fallbackErr := astron.FindAcodeBearerToken(dataDir)
		if fallbackErr != nil || bearer == "" {
			return nil, err
		}
		acct := &store.Account{
			Name:        "AStudio (acode config)",
			AccountID:   uid,
			UID:         uid,
			ModelBearer: bearer,
			Source:      "acode-config",
		}
		saved, saveErr := s.store.UpsertAccount(acct)
		if saveErr == nil {
			_ = s.SyncModels(ctx)
		}
		return saved, saveErr
	}

	bearer := session.ModelBearerToken
	if fresh, refreshErr := s.Client().RefreshCredential(ctx, session); refreshErr == nil && fresh != "" {
		bearer = fresh
	} else if bearer == "" {
		return nil, fmt.Errorf("could not obtain a model credential: %w", refreshErr)
	}

	acct := &store.Account{
		Name:         firstNonEmpty(session.Nickname, session.Mobile, session.AccountID),
		AccountID:    session.AccountID,
		UID:          session.UID,
		Token:        session.Token,
		SSOSessionID: session.SSOSessionID,
		ModelBearer:  bearer,
		Mobile:       session.Mobile,
		Nickname:     session.Nickname,
		Source:       path,
	}
	saved, err := s.store.UpsertAccount(acct)
	if err != nil {
		return nil, err
	}
	_ = s.SyncModels(ctx)
	return saved, nil
}

// --- account selection ------------------------------------------------------

// pickAccount returns the next usable account, refreshing its credential when
// the stored bearer is missing.
func (s *Server) pickAccount(ctx context.Context, skip map[string]bool) (*store.Account, error) {
	now := time.Now().Unix()
	accounts := s.store.Accounts()
	n := len(accounts)
	if n == 0 {
		return nil, errors.New("no upstream account configured; import the AStudio session from the panel")
	}
	start := int(atomic.AddInt32(&s.cursor, 1))

	// Two passes: prefer accounts with points left, then fall back to any usable
	// account so a stale or zero balance never hard-blocks a request.
	passes := []bool{false}
	if s.store.Settings().BalanceAwareRotation {
		passes = []bool{true, false}
	}
	for _, requireBalance := range passes {
		for i := 0; i < n; i++ {
			a := accounts[(start+i)%n]
			if !a.Enabled || a.InCooldown(now) || skip[a.ID] {
				continue
			}
			if requireBalance && a.PointsKnown && a.PointsBalance <= 0 && a.SparkBalance <= 0 {
				continue
			}
			if strings.TrimSpace(a.ModelBearer) == "" {
				if err := s.RefreshAccount(ctx, a.ID); err != nil {
					continue
				}
				refreshed := s.store.Accounts()
				for _, cand := range refreshed {
					if cand.ID == a.ID {
						a = cand
						break
					}
				}
				if strings.TrimSpace(a.ModelBearer) == "" {
					continue
				}
			}
			return a, nil
		}
	}
	return nil, errors.New("no usable upstream account (all disabled, cooling down, or missing credentials)")
}

// RefreshAccount re-exchanges the session cookie for a fresh model credential.
func (s *Server) RefreshAccount(ctx context.Context, id string) error {
	var target *store.Account
	for _, a := range s.store.Accounts() {
		if a.ID == id {
			target = a
			break
		}
	}
	if target == nil {
		return errors.New("account not found")
	}
	if target.Token == "" {
		return errors.New("account has no session cookie; re-import from AStudio")
	}
	session := sessionOf(target)
	bearer, err := s.Client().RefreshCredential(ctx, session)
	if err != nil {
		_ = s.store.UpdateAccount(id, func(a *store.Account) {
			a.LastError = err.Error()
			a.CooldownUntil = time.Now().Add(60 * time.Second).Unix()
		})
		return err
	}
	return s.store.UpdateAccount(id, func(a *store.Account) {
		a.ModelBearer = bearer
		a.LastError = ""
		a.CooldownUntil = 0
	})
}

func (s *Server) markAccountFailed(id string, err error) {
	if id == "" {
		return
	}
	_ = s.store.UpdateAccount(id, func(a *store.Account) {
		a.Failures++
		a.LastError = truncate(err.Error(), 500)
		a.CooldownUntil = time.Now().Add(20 * time.Second).Unix()
	})
}

func (s *Server) markAccountUsed(id string) {
	if id == "" {
		return
	}
	_ = s.store.UpdateAccount(id, func(a *store.Account) {
		a.Successes++
		a.LastUsedAt = time.Now().Unix()
		a.LastError = ""
	})
}

// --- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeOpenAIError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    kind,
			"code":    nil,
		},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// ListenAndServe starts the gateway and blocks until the process exits.
func (s *Server) ListenAndServe(host string, port int) error {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("AStudio2API listening on http://%s", addr)
	return srv.ListenAndServe()
}
