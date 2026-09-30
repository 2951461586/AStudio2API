package astron

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Account-side ("权益 / 积分 / 会员") upstream calls.
//
// These live on the workspace API host (agent.xfyun.cn/xingchen-studio) and are
// authenticated with the same session cookie as the model-credential exchange.
// Every response is wrapped in an envelope {flag, code, desc, data}; code 1002
// and 1005 mean the session itself was rejected.

// Envelope error codes that mean "the session is no longer valid".
const (
	codeSessionExpired = "1002"
	codeSessionInvalid = "1005"
)

// ErrSessionExpired is returned when the workspace API rejects the session
// cookie, meaning the operator must re-import the AStudio login.
var ErrSessionExpired = errors.New("astron session expired; re-import the AStudio login")

// PointsBalance mirrors GET points/balance.
type PointsBalance struct {
	TotalAmount            int64  `json:"totalAmount"`
	TotalBalance           int64  `json:"totalBalance"`
	MemberTotal            int64  `json:"memberTotal"`
	MemberBalance          int64  `json:"memberBalance"`
	BuyTotal               int64  `json:"buyTotal"`
	BuyBalance             int64  `json:"buyBalance"`
	ActivityTotal          int64  `json:"activityTotal"`
	ActivityBalance        int64  `json:"activityBalance"`
	ActivityNextExpireTime string `json:"activityNextExpireTime"`
	BuyNextExpireTime      string `json:"buyNextExpireTime"`
	MemberNextExpireTime   string `json:"memberNextExpireTime"`
	SparkTotalAmount       int64  `json:"sparkTotalAmount"`
	SparkTotalBalance      int64  `json:"sparkTotalBalance"`
	SparkActivityTotal     int64  `json:"sparkActivityTotal"`
	SparkActivityBalance   int64  `json:"sparkActivityBalance"`
}

// PointsSummary mirrors GET points/summary.
type PointsSummary struct {
	TotalBalance      int64  `json:"totalBalance"`
	SparkTotalBalance int64  `json:"sparkTotalBalance"`
	PurchaseTarget    string `json:"purchaseTarget"`
}

// FlexibleString accepts either a JSON string or a JSON number. The workspace
// API is inconsistent about scalar types: for example membership/me returns
// "uid" as a number while the session file stores the same value as a string.
type FlexibleString string

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexibleString) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*f = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*f = FlexibleString(s)
		return nil
	}
	*f = FlexibleString(strings.TrimSpace(string(trimmed)))
	return nil
}

// String returns the underlying value.
func (f FlexibleString) String() string { return string(f) }

// Membership mirrors GET membership/me.
type Membership struct {
	UID         FlexibleString `json:"uid"`
	Active      bool           `json:"active"`
	PlanCode    string         `json:"planCode"`
	PlanName    string         `json:"planName"`
	BillingMode string         `json:"billingMode"`
	AutoRenew   bool           `json:"autoRenew"`
	EndTime     string         `json:"endTime"`
	NextRenew   string         `json:"nextRenewTime"`
	Source      string         `json:"source"`
}

// Popup is one pending client popup from GET client-popups/pending.
type Popup struct {
	PopupID       int64          `json:"popupId"`
	InstanceKey   string         `json:"instanceKey"`
	ComponentType string         `json:"componentType"`
	Raw           map[string]any `json:"-"`
}

// BetaEligibility mirrors GET beta/claim/eligibility.
type BetaEligibility struct {
	CanClaim       bool   `json:"canClaim"`
	AlreadyClaimed bool   `json:"alreadyClaimed"`
	ShowPopup      bool   `json:"showPopup"`
	Reason         string `json:"reason"`
}

// RedeemResult mirrors a successful POST redeem-codes/redeem.
type RedeemResult struct {
	Points     int64  `json:"points"`
	ExpireTime string `json:"expireTime"`
}

// AccountSnapshot is the aggregated per-account status the panel shows. Errors
// records which sub-fetches failed, so a partial outage is visible rather than
// silently producing empty fields.
type AccountSnapshot struct {
	Balance    *PointsBalance
	Membership *Membership
	Popups     []Popup
	Beta       *BetaEligibility
	Errors     map[string]string
}

// envelope is the shared response shape of the workspace API.
type envelope struct {
	Flag    bool            `json:"flag"`
	Code    any             `json:"code"`
	Desc    string          `json:"desc"`
	TraceID string          `json:"traceId"`
	Data    json.RawMessage `json:"data"`
}

// accountRequest performs one cookie-authenticated JSON workspace API call and
// unwraps the envelope. out may be nil when the payload is irrelevant.
func (c *Client) accountRequest(ctx context.Context, session *Session, method, path string, payload any, out any) error {
	if !session.Valid() {
		return ErrSessionExpired
	}
	var body []byte
	contentType := ""
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = raw
		contentType = "application/json"
	}
	return c.envelopeRequest(ctx, method, path, session, contentType, body, out)
}

// envelopeRequest is the shared transport for every workspace API call. session
// may be nil for the pre-login endpoints. out may be nil when the payload is
// irrelevant.
func (c *Client) envelopeRequest(ctx context.Context, method, path string, session *Session, contentType string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Workspace+"/"+strings.TrimLeft(path, "/"), reader)
	if err != nil {
		return err
	}
	for k, v := range c.clientHeaders() {
		req.Header.Set(k, v)
	}
	if session != nil {
		req.Header.Set("Cookie", session.Cookie())
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: upstream returned unreadable JSON (HTTP %d)", path, resp.StatusCode)
	}
	code := codeStr(env.Code)
	sessionInvalid := resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		code == codeSessionExpired || code == codeSessionInvalid
	if sessionInvalid {
		return ErrSessionExpired
	}
	if !respOk(resp.StatusCode) || !env.Flag || code != "0" {
		desc := strings.TrimSpace(env.Desc)
		if desc == "" {
			desc = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return &EnvelopeError{Path: path, Code: code, Desc: desc, TraceID: env.TraceID}
	}
	if out == nil {
		return nil
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// EnvelopeError is a business-level rejection from the workspace API.
type EnvelopeError struct {
	Path    string
	Code    string
	Desc    string
	TraceID string
}

func (e *EnvelopeError) Error() string {
	return fmt.Sprintf("%s rejected (code=%s): %s", e.Path, e.Code, e.Desc)
}

func respOk(status int) bool { return status >= 200 && status < 300 }

// PointsBalance fetches GET points/balance.
func (c *Client) PointsBalance(ctx context.Context, s *Session) (*PointsBalance, error) {
	var out PointsBalance
	if err := c.accountRequest(ctx, s, http.MethodGet, "points/balance", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PointsSummary fetches GET points/summary.
func (c *Client) PointsSummary(ctx context.Context, s *Session) (*PointsSummary, error) {
	var out PointsSummary
	if err := c.accountRequest(ctx, s, http.MethodGet, "points/summary", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Membership fetches GET membership/me.
func (c *Client) Membership(ctx context.Context, s *Session) (*Membership, error) {
	var out Membership
	if err := c.accountRequest(ctx, s, http.MethodGet, "membership/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PendingPopups fetches GET client-popups/pending.
func (c *Client) PendingPopups(ctx context.Context, s *Session) ([]Popup, error) {
	var out []Popup
	if err := c.accountRequest(ctx, s, http.MethodGet, "client-popups/pending", nil, &out); err != nil {
		return nil, err
	}
	for i := range out {
		var raw map[string]any
		if b, err := json.Marshal(out[i]); err == nil {
			_ = json.Unmarshal(b, &raw)
			out[i].Raw = raw
		}
	}
	return out, nil
}

// BetaEligibility fetches GET beta/claim/eligibility.
func (c *Client) BetaEligibility(ctx context.Context, s *Session) (*BetaEligibility, error) {
	var out BetaEligibility
	if err := c.accountRequest(ctx, s, http.MethodGet, "beta/claim/eligibility", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimBeta performs POST beta/claim for the given domain account.
func (c *Client) ClaimBeta(ctx context.Context, s *Session, domainAccount string) error {
	return c.accountRequest(ctx, s, http.MethodPost, "beta/claim", map[string]string{"domainAccount": domainAccount}, nil)
}

// ClaimDownloadReward performs POST client-download-reward/claim.
func (c *Client) ClaimDownloadReward(ctx context.Context, s *Session) error {
	return c.accountRequest(ctx, s, http.MethodPost, "client-download-reward/claim", nil, nil)
}

// CompletePopup performs POST client-popups/complete.
func (c *Client) CompletePopup(ctx context.Context, s *Session, popupID int64, instanceKey string) error {
	return c.accountRequest(ctx, s, http.MethodPost, "client-popups/complete", map[string]any{
		"popupId":     popupID,
		"instanceKey": instanceKey,
	}, nil)
}

// RedeemCode performs POST redeem-codes/redeem.
func (c *Client) RedeemCode(ctx context.Context, s *Session, code string) (*RedeemResult, error) {
	var out struct {
		Reward struct {
			Points     int64  `json:"points"`
			ExpireTime string `json:"expireTime"`
		} `json:"reward"`
	}
	if err := c.accountRequest(ctx, s, http.MethodPost, "redeem-codes/redeem", map[string]string{"code": code}, &out); err != nil {
		return nil, err
	}
	return &RedeemResult{Points: out.Reward.Points, ExpireTime: out.Reward.ExpireTime}, nil
}

// Snapshot gathers the read-only account status in one pass. Individual
// failures are tolerated so a partial outage still yields useful data; the
// failures are reported through Snapshot.Errors.
func (c *Client) Snapshot(ctx context.Context, s *Session) *AccountSnapshot {
	snap := &AccountSnapshot{Errors: map[string]string{}}
	if b, err := c.PointsBalance(ctx, s); err == nil {
		snap.Balance = b
	} else {
		snap.Errors["points"] = err.Error()
	}
	if m, err := c.Membership(ctx, s); err == nil {
		snap.Membership = m
	} else {
		snap.Errors["membership"] = err.Error()
	}
	if p, err := c.PendingPopups(ctx, s); err == nil {
		snap.Popups = p
	} else {
		snap.Errors["popups"] = err.Error()
	}
	if e, err := c.BetaEligibility(ctx, s); err == nil {
		snap.Beta = e
	} else {
		snap.Errors["beta"] = err.Error()
	}
	return snap
}

// Problem renders the aggregated partial failures, or "" when everything worked.
func (s *AccountSnapshot) Problem() string {
	if len(s.Errors) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s.Errors))
	for k := range s.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+s.Errors[k])
	}
	return strings.Join(parts, "; ")
}

// SessionFrom builds a Session from persisted account fields.
func SessionFrom(accountID, uid, token, ssoSessionID, bearer string) *Session {
	return &Session{
		AccountID:        accountID,
		UID:              uid,
		Token:            token,
		SSOSessionID:     ssoSessionID,
		ModelBearerToken: bearer,
	}
}
