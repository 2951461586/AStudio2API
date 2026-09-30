// Package astron implements the upstream protocol used by the AStudio desktop
// app (a.k.a. Astron Studio / 讯飞星辰). It covers three things:
//
//  1. Reading the on-disk login session the desktop app persists
//     (<AStudio Data>/userdata/astron-session.json).
//  2. Exchanging that session cookie for the model bearer credential used by
//     the MaaS inference gateway (GET {workspace}/bot/models/configs).
//  3. Reading the live model directory (GET {modelsBase}/models).
//
// The protocol was reconstructed from the packaged AStudio bundle
// (apps/server/dist/index.mjs) and verified against the live upstream.
package astron

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Session mirrors the astron-session.json produced by the AStudio desktop app.
type Session struct {
	AccountID        string `json:"accountId"`
	UID              string `json:"uid"`
	Token            string `json:"token"`
	SSOSessionID     string `json:"ssoSessionId"`
	ModelBearerToken string `json:"modelBearerToken,omitempty"`
	Mobile           string `json:"mobile,omitempty"`
	Nickname         string `json:"nickname,omitempty"`
	Avatar           string `json:"avatar,omitempty"`
	Banned           bool   `json:"banned,omitempty"`
	LoginMethod      string `json:"loginMethod,omitempty"`
	LoggedInAt       string `json:"loggedInAt,omitempty"`
}

// Cookie renders the cookie header the desktop app sends to the workspace API.
// Mirrors cookieHeader() in the AStudio bundle.
func (s *Session) Cookie() string {
	parts := make([]string, 0, 4)
	if s.SSOSessionID != "" {
		parts = append(parts, "ssoSessionId="+s.SSOSessionID, "sso_sessionid="+s.SSOSessionID)
	}
	parts = append(parts, "account_id="+s.AccountID, "token="+s.Token)
	return strings.Join(parts, "; ")
}

// Valid reports whether the session carries enough data to be refreshed.
func (s *Session) Valid() bool {
	return s != nil && s.AccountID != "" && s.Token != ""
}

// ClientType is the value the desktop app sends in the "clientType" header.
// Mirrors resolveAstronStudioClientType() in the AStudio bundle.
func ClientType() string {
	if runtime.GOOS == "windows" {
		return "21"
	}
	return "22"
}

// ErrNoSession is returned when no AStudio session could be located on disk.
var ErrNoSession = errors.New("no AStudio session found")

// LoadSession reads astron-session.json from an explicit AStudio data directory
// (the parent of "userdata"). An empty dir triggers auto-discovery.
func LoadSession(dataDir string) (*Session, string, error) {
	path, err := LocateSessionFile(dataDir)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if !s.Valid() {
		return nil, path, fmt.Errorf("%s does not contain a usable accountId/token", path)
	}
	return &s, path, nil
}

// LocateSessionFile finds the astron-session.json on disk. When dataDir is set
// only that directory tree is checked; otherwise a list of well-known AStudio
// install locations is probed.
func LocateSessionFile(dataDir string) (string, error) {
	if dataDir != "" {
		for _, candidate := range sessionCandidates(dataDir) {
			if fileExists(candidate) {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("%w under %s", ErrNoSession, dataDir)
	}
	for _, dir := range defaultDataDirs() {
		for _, candidate := range sessionCandidates(dir) {
			if fileExists(candidate) {
				return candidate, nil
			}
		}
	}
	return "", ErrNoSession
}

// sessionCandidates lists every layout AStudio has used for its state file.
func sessionCandidates(dataDir string) []string {
	return []string{
		filepath.Join(dataDir, "userdata", "astron-session.json"),
		filepath.Join(dataDir, "astron-session.json"),
	}
}

// defaultDataDirs returns the AStudio data roots worth probing. AStudio places
// "<install>/../AStudio Data" next to the executable, so the parent of the
// running binary's directory is the most reliable anchor.
func defaultDataDirs() []string {
	dirs := []string{}
	if v := strings.TrimSpace(os.Getenv("ASTUDIO_DATA_DIR")); v != "" {
		dirs = append(dirs, v)
	}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(filepath.Dir(exe))
		dirs = append(dirs, filepath.Join(base, "AStudio Data"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, "AStudio Data"),
			filepath.Join(home, ".astudio"),
		)
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		dirs = append(dirs, filepath.Join(appData, "AStudio Data"), filepath.Join(appData, "AStudio"))
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		dirs = append(dirs, filepath.Join(localAppData, "AStudio Data"))
	}
	for _, drive := range []string{"C:", "D:", "E:", "F:", "G:"} {
		dirs = append(dirs, filepath.Join(drive+string(filepath.Separator), "IDE", "AStudio Data"))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// FindAcodeBearerToken reads the bearer credential AStudio projects into the
// Acode kernel config (<data>/runtime/acode-home/config.toml). It is a useful
// fallback when astron-session.json has not been written yet.
func FindAcodeBearerToken(dataDir string) (bearer, uid string, err error) {
	candidates := []string{}
	if dataDir != "" {
		candidates = append(candidates, filepath.Join(dataDir, "runtime", "acode-home", "config.toml"))
	} else {
		for _, dir := range defaultDataDirs() {
			candidates = append(candidates, filepath.Join(dir, "runtime", "acode-home", "config.toml"))
		}
	}
	for _, path := range candidates {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		text := string(raw)
		if !strings.Contains(text, "[model_providers.astron-spark]") {
			continue
		}
		bearer = tomlStringValue(text, "experimental_bearer_token")
		uid = tomlStringValue(text, "uid")
		if bearer != "" {
			return bearer, uid, nil
		}
	}
	return "", "", ErrNoSession
}

// tomlStringValue extracts `key = "value"` from a small TOML document.
func tomlStringValue(doc, key string) string {
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, key))
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "="))
		rest = strings.Trim(rest, `"'`)
		if i := strings.IndexAny(rest, `"'`); i >= 0 {
			rest = rest[:i]
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
