package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"astudio2api/internal/astron"
	"astudio2api/internal/store"
)

// 手机号验证码登录的 HTTP 面。
//
// 流程（三步，验证码必须在浏览器完成）：
//   GET  /admin/api/login/geetest   取 GeeTest 配置
//   POST /admin/api/login/sms       手机号 + 已解题的验证码 -> 发送短信
//   POST /admin/api/login/verify    手机号 + 短信码 -> 登录并落库账号

func (s *Server) adminLoginGeetest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()
	config, err := s.Client().FetchGeetest(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"geetest": config})
}

func (s *Server) adminLoginSms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		Mobile    string `json:"mobile"`
		Challenge string `json:"geetest_challenge"`
		Validate  string `json:"geetest_validate"`
		Seccode   string `json:"geetest_seccode"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
		return
	}

	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	err := s.Client().SendSmsCode(ctx, body.Mobile, astron.GeetestCaptcha{
		Challenge: body.Challenge,
		Validate:  body.Validate,
		Seccode:   body.Seccode,
	})
	if err != nil {
		// A rejected captcha is a client-side problem, not a gateway failure.
		status := http.StatusBadRequest
		var envErr *astron.EnvelopeError
		if asEnvelope(err, &envErr) && strings.Contains(envErr.Code, "20009") {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"error": friendlySmsError(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminLoginVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Use POST."})
		return
	}
	var body struct {
		Mobile     string `json:"mobile"`
		VerifyCode string `json:"verify_code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON."})
		return
	}

	ctx, cancel := contextWithTimeout(r, 90*time.Second)
	defer cancel()
	result, err := s.Client().LoginWithSms(ctx, body.Mobile, body.VerifyCode)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyLoginError(err)})
		return
	}
	if result.Banned {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "该账号已被封禁，无法使用。"})
		return
	}

	account := &store.Account{
		Name:         firstNonEmpty(result.Nickname, result.Mobile, result.AccountID),
		AccountID:    result.AccountID,
		UID:          result.UID,
		Token:        result.Token,
		SSOSessionID: result.SSOSessionID,
		ModelBearer:  result.ModelBearer,
		Mobile:       result.Mobile,
		Nickname:     result.Nickname,
		Source:       "sms-login",
	}
	saved, err := s.store.UpsertAccount(account)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// Warm the new account so it is immediately usable.
	_, _ = s.RefreshAccountStatus(ctx, saved.ID)
	_ = s.SyncModels(ctx)

	name := firstNonEmpty(saved.Nickname, saved.Mobile, saved.AccountID)
	s.recordBenefit(name, "手机号登录", "ok",
		"accountId="+saved.AccountID+" uid="+saved.UID+" 新账号="+boolLabel(result.IsNew))

	writeJSON(w, http.StatusOK, map[string]any{
		"account": saved,
		"is_new":  result.IsNew,
	})
}

// contextWithTimeout derives a bounded context from the request.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func friendlySmsError(err error) string {
	var envErr *astron.EnvelopeError
	if asEnvelope(err, &envErr) {
		switch {
		case strings.Contains(envErr.Code, "20009"):
			return "安全验证未通过，请重新完成滑块验证后再试。"
		case strings.Contains(envErr.Desc, "频繁") || strings.Contains(strings.ToLower(envErr.Desc), "frequent"):
			return "发送过于频繁，请稍后再试。"
		}
		return envErr.Desc
	}
	if err == astron.ErrInvalidMobile || strings.Contains(err.Error(), "格式不正确") {
		return err.Error()
	}
	return err.Error()
}

// asEnvelope unwraps an *astron.EnvelopeError.
func asEnvelope(err error, target **astron.EnvelopeError) bool {
	return errors.As(err, target)
}

func friendlyLoginError(err error) string {
	var envErr *astron.EnvelopeError
	if asEnvelope(err, &envErr) {
		desc := envErr.Desc
		if strings.Contains(desc, "验证码") || strings.Contains(strings.ToLower(desc), "code") {
			return "验证码错误或已过期，请重新获取。"
		}
		return desc
	}
	return err.Error()
}

func boolLabel(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
