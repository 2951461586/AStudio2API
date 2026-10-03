// Package store persists gateway state: API keys, upstream accounts, runtime
// settings, usage statistics and request logs. Everything lives in a single
// JSON document written atomically, mirroring the single-binary deployment
// model of the reference 2api projects.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Account is one signed-in Astron account used as an upstream credential.
type Account struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	AccountID     string `json:"account_id"`
	UID           string `json:"uid"`
	Token         string `json:"token"`
	SSOSessionID  string `json:"sso_session_id"`
	ModelBearer   string `json:"model_bearer_token"`
	Mobile        string `json:"mobile,omitempty"`
	Nickname      string `json:"nickname,omitempty"`
	DomainAccount string `json:"domain_account,omitempty"`
	Source        string `json:"source,omitempty"`
	Enabled       bool   `json:"enabled"`
	LastError     string `json:"last_error,omitempty"`
	LastUsedAt    int64  `json:"last_used_at,omitempty"`
	CooldownUntil int64  `json:"cooldown_until,omitempty"`
	Successes     int64  `json:"successes"`
	Failures      int64  `json:"failures"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`

	// 权益 / 积分状态（由 checkin 与保活任务刷新）
	PointsKnown    bool   `json:"points_known"`
	PointsBalance  int64  `json:"points_balance"`
	SparkBalance   int64  `json:"spark_balance"`
	PointsExpireAt string `json:"points_expire_at,omitempty"`
	PlanCode       string `json:"plan_code,omitempty"`
	PlanName       string `json:"plan_name,omitempty"`
	PlanActive     bool   `json:"plan_active,omitempty"`
	PendingPopups  int    `json:"pending_popups"`
	LastCheckedAt  int64  `json:"last_checked_at,omitempty"`
	LastClaimAt    int64  `json:"last_claim_at,omitempty"`
	SessionOK      bool   `json:"session_ok"`
	StatusNote     string `json:"status_note,omitempty"`
}

// InCooldown reports whether the account is temporarily benched.
func (a *Account) InCooldown(now int64) bool { return a.CooldownUntil > now }

// APIKey is a client-facing gateway key.
type APIKey struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Note      string `json:"note"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// Settings are the operator-tunable runtime parameters.
type Settings struct {
	Host             string            `json:"host"`
	Port             int               `json:"port"`
	Password         string            `json:"password"`
	UpstreamBase     string            `json:"upstream_base"`
	ModelsBase       string            `json:"models_base"`
	WorkspaceAPI     string            `json:"workspace_api"`
	StudioVersion    string            `json:"studio_version"`
	AstronDataDir    string            `json:"astron_data_dir"`
	RequestTimeoutS  int               `json:"request_timeout_seconds"`
	IdleTimeoutS     int               `json:"idle_timeout_seconds"`
	LogRetentionDays int               `json:"log_retention_days"`
	LogMaxEntries    int               `json:"log_max_entries"`
	MaxConcurrency   int               `json:"max_concurrency"`
	CORSOrigins      []string          `json:"cors_origins"`
	ModelAliases     map[string]string `json:"model_aliases"`

	// 权益 / 保活调度
	AutoCheckin          bool `json:"auto_checkin"`
	CheckinHour          int  `json:"checkin_hour"`
	KeepaliveMinutes     int  `json:"keepalive_minutes"`
	BalanceAwareRotation bool `json:"balance_aware_rotation"`

	// 签到（弹窗领取）子开关
	CheckinCompletePopups bool `json:"checkin_complete_popups"`
	CheckinClaimDownload  bool `json:"checkin_claim_download_reward"`
	CheckinClaimBeta      bool `json:"checkin_claim_beta"`

	// PasswordGenerated marks a first-run random password that the operator has
	// not changed yet. The panel uses it to force a password change.
	PasswordGenerated bool `json:"password_generated"`
}

// ModelStat aggregates usage for one model slug.
type ModelStat struct {
	Requests         int64 `json:"requests"`
	Successes        int64 `json:"successes"`
	Failures         int64 `json:"failures"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// BucketStat aggregates one hour of traffic for trend charts.
type BucketStat struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

// Stats holds all counters.
type Stats struct {
	Requests         int64                  `json:"requests"`
	Successes        int64                  `json:"successes"`
	Failures         int64                  `json:"failures"`
	PromptTokens     int64                  `json:"prompt_tokens"`
	CompletionTokens int64                  `json:"completion_tokens"`
	CachedTokens     int64                  `json:"cached_tokens"`
	ReasoningTokens  int64                  `json:"reasoning_tokens"`
	FirstTokenMsSum  int64                  `json:"first_token_ms_sum"`
	FirstTokenCount  int64                  `json:"first_token_count"`
	ByModel          map[string]*ModelStat  `json:"by_model"`
	ByHour           map[string]*BucketStat `json:"by_hour"`
}

// LogEntry is one request record. Conversation content is never stored.
type LogEntry struct {
	Time             int64  `json:"time"`
	Model            string `json:"model"`
	UpstreamModel    string `json:"upstream_model"`
	KeyNote          string `json:"key_note"`
	Account          string `json:"account"`
	Stream           bool   `json:"stream"`
	Status           int    `json:"status"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CachedTokens     int64  `json:"cached_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	DurationMs       int64  `json:"duration_ms"`
	FirstTokenMs     int64  `json:"first_token_ms"`
	Error            string `json:"error,omitempty"`
}

// BenefitEvent records one account-maintenance action (checkin, claim,
// keepalive). Conversation content is never involved.
type BenefitEvent struct {
	Time    int64  `json:"time"`
	Account string `json:"account"`
	Action  string `json:"action"`
	Result  string `json:"result"`
	Detail  string `json:"detail,omitempty"`
}

// Data is the persisted document.
type Data struct {
	SchemaVersion int             `json:"schema_version"`
	Settings      Settings        `json:"settings"`
	APIKeys       []*APIKey       `json:"api_keys"`
	Accounts      []*Account      `json:"accounts"`
	Stats         Stats           `json:"stats"`
	Logs          []*LogEntry     `json:"logs"`
	Benefits      []*BenefitEvent `json:"benefits"`
	// LastCheckinDay is the local date (YYYY-MM-DD) of the last automatic
	// check-in run, so restarts do not repeat it and a machine that was off or
	// asleep over the configured hour can still catch up.
	LastCheckinDay string `json:"last_checkin_day,omitempty"`
}

// Store wraps Data with a mutex and atomic persistence.
type Store struct {
	mu       sync.RWMutex
	path     string
	data     *Data
	dirty    bool
	lastSave time.Time
}

// DefaultSettings returns the out-of-the-box configuration.
func DefaultSettings() Settings {
	return Settings{
		Host: "0.0.0.0",
		Port: 10086,
		// Password is intentionally empty: main generates a random one on first
		// run instead of shipping a guessable default.
		Password:         "",
		UpstreamBase:     "https://maas-api.cn-huabei-1.xf-yun.com/v1",
		ModelsBase:       "https://astronstudio-api-volces-prod.xf-yun.com/api/v1/model-manager",
		WorkspaceAPI:     "https://agent.xfyun.cn/xingchen-studio",
		StudioVersion:    "3.4.4",
		RequestTimeoutS:  300,
		IdleTimeoutS:     300,
		LogRetentionDays: 30,
		LogMaxEntries:    2000,
		MaxConcurrency:   4,
		ModelAliases:     map[string]string{},

		AutoCheckin:          false,
		CheckinHour:          9,
		KeepaliveMinutes:     0,
		BalanceAwareRotation: true,

		CheckinCompletePopups: true,
		CheckinClaimDownload:  true,
		CheckinClaimBeta:      true,
	}
}

// Open loads (or creates) the data document at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: &Data{
		SchemaVersion: 1,
		Settings:      DefaultSettings(),
		Stats: Stats{
			ByModel: map[string]*ModelStat{},
			ByHour:  map[string]*BucketStat{},
		},
	}}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var d Data
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		s.data = &d
	case errors.Is(err, os.ErrNotExist):
		// fresh install
	default:
		return nil, err
	}
	s.normalize()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) normalize() {
	d := s.data
	def := DefaultSettings()
	applyDefaults(&d.Settings, def)
	if d.Stats.ByModel == nil {
		d.Stats.ByModel = map[string]*ModelStat{}
	}
	if d.Stats.ByHour == nil {
		d.Stats.ByHour = map[string]*BucketStat{}
	}
	if d.Settings.ModelAliases == nil {
		d.Settings.ModelAliases = map[string]string{}
	}
	if d.SchemaVersion == 0 {
		d.SchemaVersion = 1
	}
	// Persisted collections must serialise as [] rather than null, so external
	// tooling (and the example file) sees a consistent shape.
	if d.APIKeys == nil {
		d.APIKeys = []*APIKey{}
	}
	if d.Accounts == nil {
		d.Accounts = []*Account{}
	}
	if d.Logs == nil {
		d.Logs = []*LogEntry{}
	}
	if d.Benefits == nil {
		d.Benefits = []*BenefitEvent{}
	}
	for _, a := range d.Accounts {
		if a.ID == "" {
			a.ID = NewID()
		}
	}
}

func applyDefaults(s *Settings, def Settings) {
	if s.Host == "" {
		s.Host = def.Host
	}
	if s.Port == 0 {
		s.Port = def.Port
	}
	if s.Password == "" {
		s.Password = def.Password
	}
	if s.UpstreamBase == "" {
		s.UpstreamBase = def.UpstreamBase
	}
	if s.ModelsBase == "" {
		s.ModelsBase = def.ModelsBase
	}
	if s.WorkspaceAPI == "" {
		s.WorkspaceAPI = def.WorkspaceAPI
	}
	if s.StudioVersion == "" {
		s.StudioVersion = def.StudioVersion
	}
	if s.RequestTimeoutS <= 0 {
		s.RequestTimeoutS = def.RequestTimeoutS
	}
	if s.IdleTimeoutS <= 0 {
		s.IdleTimeoutS = def.IdleTimeoutS
	}
	if s.LogRetentionDays <= 0 {
		s.LogRetentionDays = def.LogRetentionDays
	}
	if s.LogMaxEntries <= 0 {
		s.LogMaxEntries = def.LogMaxEntries
	}
	if s.MaxConcurrency <= 0 {
		s.MaxConcurrency = def.MaxConcurrency
	}
	if s.CheckinHour < 0 || s.CheckinHour > 23 {
		s.CheckinHour = def.CheckinHour
	}
	if s.KeepaliveMinutes < 0 {
		s.KeepaliveMinutes = def.KeepaliveMinutes
	}
}

// Settings returns a copy of the current settings.
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.data.Settings
	if s.data.Settings.ModelAliases != nil {
		out.ModelAliases = make(map[string]string, len(s.data.Settings.ModelAliases))
		for k, v := range s.data.Settings.ModelAliases {
			out.ModelAliases[k] = v
		}
	}
	return out
}

// UpdateSettings applies fn to the settings and persists the result.
func (s *Store) UpdateSettings(fn func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data.Settings)
	s.normalize()
	return s.saveLocked()
}

// --- API keys ---------------------------------------------------------------

// Keys returns a snapshot of the configured API keys.
func (s *Store) Keys() []*APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*APIKey, len(s.data.APIKeys))
	for i, k := range s.data.APIKeys {
		cp := *k
		out[i] = &cp
	}
	return out
}

// AddKey stores a new API key. An empty key value is generated.
func (s *Store) AddKey(key, note string) (*APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		key = "sk-" + randomHex(24)
	}
	for _, k := range s.data.APIKeys {
		if k.Key == key {
			return nil, ErrDuplicate
		}
	}
	entry := &APIKey{ID: NewID(), Key: key, Note: note, Enabled: true, CreatedAt: time.Now().Unix()}
	s.data.APIKeys = append(s.data.APIKeys, entry)
	return entry, s.saveLocked()
}

// RemoveKey deletes an API key by id.
func (s *Store) RemoveKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.data.APIKeys {
		if k.ID == id {
			s.data.APIKeys = append(s.data.APIKeys[:i], s.data.APIKeys[i+1:]...)
			return s.saveLocked()
		}
	}
	return nil
}

// SetKeyEnabled toggles an API key.
func (s *Store) SetKeyEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.data.APIKeys {
		if k.ID == id {
			k.Enabled = enabled
			return s.saveLocked()
		}
	}
	return nil
}

// Authenticate checks a client key. When no keys are configured the gateway is
// closed (fail-safe), matching the reference implementations.
func (s *Store) Authenticate(key string) (*APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.data.APIKeys {
		if k.Key == key && k.Enabled {
			cp := *k
			return &cp, true
		}
	}
	return nil, false
}

// HasKeys reports whether at least one client key exists.
func (s *Store) HasKeys() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.APIKeys) > 0
}

// --- Accounts ---------------------------------------------------------------

// Accounts returns a snapshot of the upstream account pool.
func (s *Store) Accounts() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, len(s.data.Accounts))
	for i, a := range s.data.Accounts {
		cp := *a
		out[i] = &cp
	}
	return out
}

// UpsertAccount inserts or updates an account keyed by account id (uid).
func (s *Store) UpsertAccount(a *Account) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	for i, existing := range s.data.Accounts {
		if existing.AccountID == a.AccountID && a.AccountID != "" {
			merged := *existing
			merged.Token = a.Token
			merged.SSOSessionID = a.SSOSessionID
			if a.ModelBearer != "" {
				merged.ModelBearer = a.ModelBearer
			}
			if a.Name != "" {
				merged.Name = a.Name
			}
			if a.UID != "" {
				merged.UID = a.UID
			}
			if a.Mobile != "" {
				merged.Mobile = a.Mobile
			}
			if a.Nickname != "" {
				merged.Nickname = a.Nickname
			}
			if a.Source != "" {
				merged.Source = a.Source
			}
			merged.Enabled = true
			merged.LastError = ""
			merged.CooldownUntil = 0
			merged.UpdatedAt = now
			s.data.Accounts[i] = &merged
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
			cp := merged
			return &cp, nil
		}
	}
	if a.ID == "" {
		a.ID = NewID()
	}
	a.Enabled = true
	a.CreatedAt = now
	a.UpdatedAt = now
	s.data.Accounts = append(s.data.Accounts, a)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	cp := *a
	return &cp, nil
}

// RemoveAccount deletes an account by id.
func (s *Store) RemoveAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.data.Accounts {
		if a.ID == id {
			s.data.Accounts = append(s.data.Accounts[:i], s.data.Accounts[i+1:]...)
			return s.saveLocked()
		}
	}
	return nil
}

// SetAccountEnabled toggles an account.
func (s *Store) SetAccountEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.data.Accounts {
		if a.ID == id {
			a.Enabled = enabled
			return s.saveLocked()
		}
	}
	return nil
}

// UpdateAccount applies fn to the account with the given id.
func (s *Store) UpdateAccount(id string, fn func(*Account)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.data.Accounts {
		if a.ID == id {
			fn(a)
			return s.saveLocked()
		}
	}
	return nil
}

// PickAccount chooses the next upstream account using round-robin over the
// enabled, non-cooling pool.
func (s *Store) PickAccount(now int64, cursor int) (*Account, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.data.Accounts)
	if n == 0 {
		return nil, cursor
	}
	for i := 0; i < n; i++ {
		idx := (cursor + i) % n
		a := s.data.Accounts[idx]
		if a.Enabled && !a.InCooldown(now) {
			cp := *a
			return &cp, idx + 1
		}
	}
	return nil, cursor
}

// --- Stats and logs ---------------------------------------------------------

// RecordRequest appends a log entry and folds the counters.
func (s *Store) RecordRequest(entry *LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.data
	d.Stats.Requests++
	if entry.Status >= 200 && entry.Status < 400 && entry.Error == "" {
		d.Stats.Successes++
	} else {
		d.Stats.Failures++
	}
	d.Stats.PromptTokens += entry.PromptTokens
	d.Stats.CompletionTokens += entry.CompletionTokens
	d.Stats.CachedTokens += entry.CachedTokens
	d.Stats.ReasoningTokens += entry.ReasoningTokens
	if entry.FirstTokenMs > 0 {
		d.Stats.FirstTokenMsSum += entry.FirstTokenMs
		d.Stats.FirstTokenCount++
	}
	if entry.Model != "" {
		ms := d.Stats.ByModel[entry.Model]
		if ms == nil {
			ms = &ModelStat{}
			d.Stats.ByModel[entry.Model] = ms
		}
		ms.Requests++
		if entry.Status >= 200 && entry.Status < 400 && entry.Error == "" {
			ms.Successes++
		} else {
			ms.Failures++
		}
		ms.PromptTokens += entry.PromptTokens
		ms.CompletionTokens += entry.CompletionTokens
		ms.CachedTokens += entry.CachedTokens
		ms.ReasoningTokens += entry.ReasoningTokens
	}
	bucket := time.Unix(entry.Time, 0).UTC().Format("2006-01-02T15")
	bs := d.Stats.ByHour[bucket]
	if bs == nil {
		bs = &BucketStat{}
		d.Stats.ByHour[bucket] = bs
	}
	bs.Requests++
	bs.PromptTokens += entry.PromptTokens
	bs.CompletionTokens += entry.CompletionTokens

	d.Logs = append([]*LogEntry{entry}, d.Logs...)
	s.pruneLocked()
	s.dirty = true
}

func (s *Store) pruneLocked() {
	d := s.data
	max := d.Settings.LogMaxEntries
	if max > 0 && len(d.Logs) > max {
		d.Logs = d.Logs[:max]
	}
	if days := d.Settings.LogRetentionDays; days > 0 {
		cutoff := time.Now().AddDate(0, 0, -days).Unix()
		kept := d.Logs[:0]
		for _, e := range d.Logs {
			if e.Time >= cutoff {
				kept = append(kept, e)
			}
		}
		d.Logs = kept
	}
	if len(d.Stats.ByHour) > 24*45 {
		keys := make([]string, 0, len(d.Stats.ByHour))
		for k := range d.Stats.ByHour {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-24*45] {
			delete(d.Stats.ByHour, k)
		}
	}
}

// StatsSnapshot returns a deep-ish copy of the counters.
func (s *Store) StatsSnapshot() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.data.Stats
	out.ByModel = make(map[string]*ModelStat, len(s.data.Stats.ByModel))
	for k, v := range s.data.Stats.ByModel {
		cp := *v
		out.ByModel[k] = &cp
	}
	out.ByHour = make(map[string]*BucketStat, len(s.data.Stats.ByHour))
	for k, v := range s.data.Stats.ByHour {
		cp := *v
		out.ByHour[k] = &cp
	}
	return out
}

// ResetStats clears counters and logs.
func (s *Store) ResetStats() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Stats = Stats{ByModel: map[string]*ModelStat{}, ByHour: map[string]*BucketStat{}}
	s.data.Logs = nil
	return s.saveLocked()
}

// Logs returns up to limit recent entries, newest first.
func (s *Store) Logs(limit int) []*LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.data.Logs) {
		limit = len(s.data.Logs)
	}
	out := make([]*LogEntry, limit)
	for i := 0; i < limit; i++ {
		cp := *s.data.Logs[i]
		out[i] = &cp
	}
	return out
}

// --- benefit events ---------------------------------------------------------

const maxBenefitEvents = 500

// RecordBenefit appends one account-maintenance event, newest first.
func (s *Store) RecordBenefit(e *BenefitEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Benefits = append([]*BenefitEvent{e}, s.data.Benefits...)
	if len(s.data.Benefits) > maxBenefitEvents {
		s.data.Benefits = s.data.Benefits[:maxBenefitEvents]
	}
	s.dirty = true
}

// Benefits returns up to limit recent events, newest first.
func (s *Store) Benefits(limit int) []*BenefitEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.data.Benefits) {
		limit = len(s.data.Benefits)
	}
	out := make([]*BenefitEvent, limit)
	for i := 0; i < limit; i++ {
		cp := *s.data.Benefits[i]
		out[i] = &cp
	}
	return out
}

// ClearBenefits drops the event history.
func (s *Store) ClearBenefits() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Benefits = nil
	return s.saveLocked()
}

// LastCheckinDay returns the local date of the last automatic check-in run.
func (s *Store) LastCheckinDay() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LastCheckinDay
}

// SetLastCheckinDay records the local date of the last automatic check-in run.
func (s *Store) SetLastCheckinDay(day string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.LastCheckinDay == day {
		return
	}
	s.data.LastCheckinDay = day
	s.dirty = true
}

// --- persistence ------------------------------------------------------------

// Save flushes pending changes to disk.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty && time.Since(s.lastSave) < 30*time.Second {
		return nil
	}
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	s.lastSave = time.Now()
	return nil
}

// ErrDuplicate is returned when a key already exists.
var ErrDuplicate = errors.New("duplicate entry")

// NewID returns a short random identifier.
func NewID() string { return randomHex(8) }

// RandomPassword returns a random password for first-run panel setup.
func RandomPassword() string { return randomHex(16) }

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))[:n*2]
	}
	return hex.EncodeToString(buf)
}
