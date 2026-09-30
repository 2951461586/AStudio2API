package astron

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"
)

// 手机号验证码登录。
//
// 逆向自 AStudio bundle（apps/server/dist/index.mjs）中
// createAstronAccountGateway 的 getGeetestConfig / sendSmsCode / loginWithSms：
//
//	GET  {authBase}chat/gee-captcha                      -> GeeTest 配置
//	POST {authBase}login/mobile/send-verify-code         -> multipart，需要验证码
//	POST {authBase}login/phone-quick-login               -> multipart，不需要验证码
//	POST {authBase}tenant-app/v2/init-app                -> Cookie，返回 banned
//	GET  {authBase}userInfo                              -> Cookie，补齐 uid/mobile
//	GET  {workspace}bot/models/configs                   -> Cookie，换取 modelBearerToken
//
// 关键约束：send-verify-code 由服务端强制校验 GeeTest v3，空验证码会返回
// code=20009 "geetest verify failed"。因此验证码必须由浏览器端解题后回传，
// 网关不做（也不应做）自动解题。

// GeetestConfig mirrors GET chat/gee-captcha.
type GeetestConfig struct {
	GT         string `json:"gt"`
	Challenge  string `json:"challenge"`
	Success    any    `json:"success"`
	NewCaptcha bool   `json:"new_captcha"`
}

// GeetestCaptcha is the solution produced by the browser GeeTest widget.
type GeetestCaptcha struct {
	Challenge string `json:"geetest_challenge"`
	Validate  string `json:"geetest_validate"`
	Seccode   string `json:"geetest_seccode"`
}

// Complete reports whether every field the upstream expects is present.
func (g GeetestCaptcha) Complete() bool {
	return strings.TrimSpace(g.Challenge) != "" &&
		strings.TrimSpace(g.Validate) != "" &&
		strings.TrimSpace(g.Seccode) != ""
}

// LoginResult is the outcome of a successful SMS sign-in.
type LoginResult struct {
	AccountID    string
	UID          string
	Token        string
	SSOSessionID string
	Mobile       string
	Nickname     string
	Avatar       string
	IsNew        bool
	Banned       bool
	ModelBearer  string
}

// Session converts the login result into a persistable session.
func (r *LoginResult) Session() *Session {
	return &Session{
		AccountID:        r.AccountID,
		UID:              r.UID,
		Token:            r.Token,
		SSOSessionID:     r.SSOSessionID,
		Mobile:           r.Mobile,
		Nickname:         r.Nickname,
		Avatar:           r.Avatar,
		Banned:           r.Banned,
		ModelBearerToken: r.ModelBearer,
	}
}

// chinaMobilePattern matches the client-side check used by AStudio.
var chinaMobilePattern = regexp.MustCompile(`^1[3-9]\d{9}$`)

// uidPattern mirrors ASTRON_UID_PATTERN in the AStudio bundle.
var uidPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)

const maxUIDLength = 256

// NormalizeUID drops placeholder uids. The login endpoint returns `uid: 0` for
// accounts whose identity is only available from userInfo, and a bare "0" would
// otherwise be treated as a real value (and leaked as an upstream header).
func NormalizeUID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" || len(v) > maxUIDLength || !uidPattern.MatchString(v) {
		return ""
	}
	return v
}

// ErrInvalidMobile is returned for a phone number that is not a CN mobile.
var ErrInvalidMobile = fmt.Errorf("手机号格式不正确（需为 11 位中国大陆号码）")

// ValidateMobile enforces the same rule the desktop client applies.
func ValidateMobile(mobile string) error {
	if !chinaMobilePattern.MatchString(strings.TrimSpace(mobile)) {
		return ErrInvalidMobile
	}
	return nil
}

// FetchGeetest retrieves a fresh GeeTest v3 challenge.
func (c *Client) FetchGeetest(ctx context.Context) (*GeetestConfig, error) {
	var out GeetestConfig
	if err := c.envelopeRequest(ctx, http.MethodGet, "chat/gee-captcha", nil, "", nil, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.GT) == "" || strings.TrimSpace(out.Challenge) == "" {
		return nil, fmt.Errorf("GeeTest 配置无效（gt/challenge 为空）")
	}
	return &out, nil
}

// SendSmsCode requests a verification SMS. The captcha must already be solved.
func (c *Client) SendSmsCode(ctx context.Context, mobile string, captcha GeetestCaptcha) error {
	if err := ValidateMobile(mobile); err != nil {
		return err
	}
	if !captcha.Complete() {
		return fmt.Errorf("验证码未完成，请先在页面完成安全验证")
	}
	body, contentType := multipartFields([][2]string{
		{"geetest_challenge", captcha.Challenge},
		{"geetest_validate", captcha.Validate},
		// The upstream expects this field base64-encoded (see captchaFormData).
		{"geetest_seccode", base64.StdEncoding.EncodeToString([]byte(captcha.Seccode))},
		{"mobile", strings.TrimSpace(mobile)},
		{"countryCode", "86"},
	})
	return c.envelopeRequest(ctx, http.MethodPost, "login/mobile/send-verify-code", nil, contentType, body, nil)
}

// LoginWithSms completes the sign-in and resolves the model credential.
func (c *Client) LoginWithSms(ctx context.Context, mobile, verifyCode string) (*LoginResult, error) {
	mobile = strings.TrimSpace(mobile)
	verifyCode = strings.TrimSpace(verifyCode)
	if err := ValidateMobile(mobile); err != nil {
		return nil, err
	}
	if verifyCode == "" {
		return nil, fmt.Errorf("请填写短信验证码")
	}

	body, contentType := multipartFields([][2]string{
		{"mobile", mobile},
		{"verifyCode", verifyCode},
		{"countryCode", "86"},
	})
	var login struct {
		AccountID    FlexibleString `json:"account_id"`
		Token        string         `json:"token"`
		SSOSessionID string         `json:"ssoSessionId"`
		UID          FlexibleString `json:"uid"`
		Mobile       string         `json:"mobile"`
		PhoneNum     string         `json:"phoneNum"`
		Phone        string         `json:"phone"`
		Nickname     string         `json:"nickname"`
		DisplayName  string         `json:"displayName"`
		Username     string         `json:"username"`
		UserName     string         `json:"userName"`
		NickName     string         `json:"nickName"`
		Avatar       string         `json:"avatar"`
		AvatarURL    string         `json:"avatarUrl"`
		IsNew        any            `json:"isNew"`
	}
	if err := c.envelopeRequest(ctx, http.MethodPost, "login/phone-quick-login", nil, contentType, body, &login); err != nil {
		return nil, err
	}

	token := firstNonEmpty(login.Token, login.SSOSessionID)
	if login.AccountID.String() == "" || token == "" {
		return nil, fmt.Errorf("登录响应不完整（缺少 account_id / token）")
	}

	result := &LoginResult{
		AccountID:    login.AccountID.String(),
		UID:          NormalizeUID(login.UID.String()),
		Token:        token,
		SSOSessionID: firstNonEmpty(login.SSOSessionID, token),
		Mobile:       firstNonEmpty(login.Mobile, login.PhoneNum, login.Phone, mobile),
		Nickname:     firstNonEmpty(login.Nickname, login.DisplayName, login.Username, login.UserName, login.NickName),
		Avatar:       firstNonEmpty(login.Avatar, login.AvatarURL),
		IsNew:        isTruthy(login.IsNew),
	}
	session := result.Session()

	// tenant-app/v2/init-app reports whether the account is banned.
	var tenant struct {
		Banned bool `json:"banned"`
	}
	if err := c.envelopeRequest(ctx, http.MethodPost, "tenant-app/v2/init-app", session, "", nil, &tenant); err == nil {
		result.Banned = tenant.Banned
		session.Banned = tenant.Banned
	}

	// Fill in identity details the login payload may omit. `uid` is absent or 0
	// on this endpoint, so this branch is the normal path, not an edge case.
	if result.UID == "" || result.Mobile == "" {
		if info, err := c.FetchUserInfo(ctx, session); err == nil && info != nil {
			if result.UID == "" {
				result.UID = NormalizeUID(info.UID)
				session.UID = result.UID
			}
			if result.Mobile == "" {
				result.Mobile = info.Mobile
				session.Mobile = info.Mobile
			}
		}
	}
	if result.UID == "" {
		return nil, fmt.Errorf("登录成功但无法解析账号 uid（userInfo 未返回）")
	}

	// Exchange the session cookie for the model bearer credential.
	if _, bearer, err := c.FetchModelCredentials(ctx, session); err == nil {
		result.ModelBearer = bearer
		session.ModelBearerToken = bearer
	} else {
		return nil, fmt.Errorf("登录成功但获取模型凭据失败：%w", err)
	}

	return result, nil
}

func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t == 1
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	}
	return false
}

// multipartFields builds a multipart/form-data body, preserving field order.
func multipartFields(fields [][2]string) ([]byte, string) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, field := range fields {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name="%s"`, escapeQuotes(field[0])))
		header.Set("Content-Type", "text/plain; charset=utf-8")
		part, err := writer.CreatePart(header)
		if err != nil {
			continue
		}
		_, _ = part.Write([]byte(field[1]))
	}
	_ = writer.Close()
	return buf.Bytes(), writer.FormDataContentType()
}

// escapeQuotes matches net/http's multipart quoting rules.
func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s)
}

// MarshalJSON keeps GeetestConfig readable when echoed to the panel.
func (g GeetestConfig) MarshalJSON() ([]byte, error) {
	type alias GeetestConfig
	return json.Marshal(alias(g))
}
