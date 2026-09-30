package astron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Default upstream endpoints, extracted from the AStudio bundle.
const (
	DefaultUpstreamBase = "https://maas-api.cn-huabei-1.xf-yun.com/v1"
	DefaultModelsBase   = "https://astronstudio-api-volces-prod.xf-yun.com/api/v1/model-manager"
	DefaultWorkspaceAPI = "https://agent.xfyun.cn/xingchen-studio"
	DefaultStudioVer    = "3.4.4"
)

// ModelConfig is one entry of GET {workspace}/bot/models/configs. It is the
// authoritative directory of models the signed-in Astron account may call.
type ModelConfig struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Model           string `json:"model"`
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	PointMultiplier any    `json:"point_multiplier"`
	IsDefault       bool   `json:"is_default"`
	IsCurrent       bool   `json:"is_current"`
}

// CatalogModel is one entry of GET {modelsBase}/models.
type CatalogModel struct {
	Slug             string          `json:"slug"`
	DisplayName      string          `json:"display_name"`
	Description      string          `json:"description"`
	ContextWindow    int             `json:"context_window"`
	Priority         int             `json:"priority"`
	Visibility       string          `json:"visibility"`
	SupportedInAPI   bool            `json:"supported_in_api"`
	InputModalities  []string        `json:"input_modalities"`
	ReasoningLevels  []ReasoningSpec `json:"supported_reasoning_levels"`
	DefaultReasoning string          `json:"default_reasoning_level"`
	ServiceTiers     []string        `json:"service_tiers"`
	Upgrade          json.RawMessage `json:"upgrade"`
	Raw              map[string]any  `json:"-"`
}

// ReasoningSpec describes one selectable reasoning effort.
type ReasoningSpec struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// Client is a thin HTTP client for the Astron upstreams.
type Client struct {
	HTTP      *http.Client
	Upstream  string // inference base, e.g. https://maas-api.cn-huabei-1.xf-yun.com/v1
	Models    string // model-manager base
	Workspace string // workspace API base (agent.xfyun.cn/xingchen-studio)
	Version   string // studioVersion header
}

// NewClient builds an upstream client with sensible timeouts.
func NewClient(upstream, models, workspace, version string) *Client {
	if upstream == "" {
		upstream = DefaultUpstreamBase
	}
	if models == "" {
		models = DefaultModelsBase
	}
	if workspace == "" {
		workspace = DefaultWorkspaceAPI
	}
	if version == "" {
		version = DefaultStudioVer
	}
	return &Client{
		HTTP: &http.Client{
			Timeout: 0, // streaming requests manage their own deadlines
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
		Upstream:  strings.TrimRight(upstream, "/"),
		Models:    strings.TrimRight(models, "/"),
		Workspace: strings.TrimRight(workspace, "/"),
		Version:   version,
	}
}

func (c *Client) clientHeaders() map[string]string {
	return map[string]string{
		"clientType":    ClientType(),
		"studioVersion": c.Version,
		"Accept":        "application/json",
		"User-Agent":    "AStudio/" + c.Version,
	}
}

// FetchModelCredentials performs GET {workspace}/bot/models/configs with the
// session cookie. It returns the fresh entry list plus the credential the
// account currently uses (is_current -> is_default -> first).
func (c *Client) FetchModelCredentials(ctx context.Context, s *Session) ([]ModelConfig, string, error) {
	if !s.Valid() {
		return nil, "", fmt.Errorf("astron session is missing accountId/token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Workspace+"/bot/models/configs", nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range c.clientHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Cookie", s.Cookie())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("model configuration request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("model configuration request failed: HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	var payload struct {
		Code  any           `json:"code"`
		Desc  string        `json:"desc"`
		Data  []ModelConfig `json:"data"`
		Flag  bool          `json:"flag"`
		Count any           `json:"count"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", fmt.Errorf("model configuration response is not JSON: %w", err)
	}
	if codeStr(payload.Code) != "0" {
		return nil, "", fmt.Errorf("model configuration rejected (code=%s): %s", codeStr(payload.Code), payload.Desc)
	}
	if len(payload.Data) == 0 {
		return nil, "", fmt.Errorf("astron account has no available model credential")
	}
	token := ""
	for _, prefer := range []func(ModelConfig) bool{
		func(m ModelConfig) bool { return m.IsCurrent },
		func(m ModelConfig) bool { return m.IsDefault },
	} {
		for _, m := range payload.Data {
			if prefer(m) && strings.TrimSpace(m.APIKey) != "" {
				token = strings.TrimSpace(m.APIKey)
				break
			}
		}
		if token != "" {
			break
		}
	}
	if token == "" {
		token = strings.TrimSpace(payload.Data[0].APIKey)
	}
	return payload.Data, token, nil
}

// RefreshCredential returns only the current model bearer token.
func (c *Client) RefreshCredential(ctx context.Context, s *Session) (string, error) {
	_, token, err := c.FetchModelCredentials(ctx, s)
	return token, err
}

// FetchCatalog performs GET {modelsBase}/models with the bearer credential. The
// response carries the full agent-facing model directory (context window,
// reasoning levels, base instructions, ...).
func (c *Client) FetchCatalog(ctx context.Context, bearer string) ([]CatalogModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Models+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	for k, v := range c.clientHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model catalog request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("model catalog request failed: HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var payload struct {
		Code any    `json:"code"`
		Msg  string `json:"message"`
		Data struct {
			Models []CatalogModel `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("model catalog response is not JSON: %w", err)
	}
	if codeStr(payload.Code) != "0" {
		return nil, fmt.Errorf("model catalog rejected (code=%s): %s", codeStr(payload.Code), payload.Msg)
	}
	for i := range payload.Data.Models {
		var raw map[string]any
		if err := json.Unmarshal(mustJSON(payload.Data.Models[i]), &raw); err == nil {
			payload.Data.Models[i].Raw = raw
		}
	}
	return payload.Data.Models, nil
}

// AccountInfo is the subset of GET {workspace}/userInfo the gateway surfaces.
type AccountInfo struct {
	UID      string `json:"uid"`
	Mobile   string `json:"mobile"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
	Raw      map[string]any
}

// FetchUserInfo performs GET {workspace}/userInfo with the session cookie.
// FetchUserInfo performs GET {workspace}/userInfo with the session cookie.
//
// The payload nests as {flag, code, data: {userInfo: {...}}}, so it must go
// through the envelope unwrapper; reading the raw body at the top level yields
// a silently empty result.
func (c *Client) FetchUserInfo(ctx context.Context, s *Session) (*AccountInfo, error) {
	var payload struct {
		UserInfo struct {
			UID       FlexibleString `json:"uid"`
			Mobile    string         `json:"mobile"`
			Telephone string         `json:"telephone"`
			Nickname  string         `json:"nickname"`
			Avatar    string         `json:"avatar"`
		} `json:"userInfo"`
	}
	if err := c.envelopeRequest(ctx, http.MethodGet, "userInfo", s, "", nil, &payload); err != nil {
		return nil, err
	}
	info := &AccountInfo{
		UID:      NormalizeUID(payload.UserInfo.UID.String()),
		Mobile:   firstNonEmpty(payload.UserInfo.Mobile, payload.UserInfo.Telephone),
		Nickname: payload.UserInfo.Nickname,
		Avatar:   payload.UserInfo.Avatar,
	}
	if info.UID == "" {
		info.UID = NormalizeUID(s.UID)
	}
	return info, nil
}

// InferenceURL joins the configured inference base with a sub path.
func (c *Client) InferenceURL(path string) string {
	return c.Upstream + "/" + strings.TrimLeft(path, "/")
}

func codeStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "0"
	case string:
		return t
	case float64:
		return fmt.Sprintf("%d", int64(t))
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// CloneBody returns a re-readable copy of a request body.
func CloneBody(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }
