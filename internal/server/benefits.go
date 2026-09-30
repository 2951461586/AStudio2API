package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"astudio2api/internal/astron"
	"astudio2api/internal/store"
)

// 权益中心 / 多账号轮转保活。
//
// 星辰侧的"签到"不是 Qoder 那种 campaign 接口，而是由运营弹窗下发的：
//   GET  client-popups/pending   -> [{componentType: DAILY_REWARD_DIALOG, popupId, instanceKey, ...}]
//   POST client-popups/complete  -> {popupId, instanceKey}   完成即领取
// 官方客户端在用户关闭弹窗时调用 complete，这里代替用户完成同样的动作。
//
// 保活（keepalive）则是定期用会话 cookie 重新换取模型凭据并拉一次账号状态，
// 既刷新 Bearer，也让 cookie 保持活跃。

// claimablePopupTypes mirrors SUPPORTED_COMPONENT_TYPES in the AStudio bundle.
var claimablePopupTypes = map[string]bool{
	"DAILY_REWARD_DIALOG":           true,
	"NEW_USER_DIALOG":               true,
	"CLIENT_DOWNLOAD_REWARD_DIALOG": true,
}

// popupLabel renders a human-readable action name for the event log.
func popupLabel(componentType string) string {
	switch componentType {
	case "DAILY_REWARD_DIALOG":
		return "签到（每日奖励）"
	case "NEW_USER_DIALOG":
		return "新人奖励"
	case "CLIENT_DOWNLOAD_REWARD_DIALOG":
		return "客户端下载奖励"
	}
	return "弹窗:" + componentType
}

// CheckinResult reports what happened for one account.
type CheckinResult struct {
	AccountID   string   `json:"account_id"`
	Name        string   `json:"name"`
	Points      int64    `json:"points_balance"`
	Spark       int64    `json:"spark_balance"`
	PointsDelta int64    `json:"points_delta"`
	Plan        string   `json:"plan,omitempty"`
	Popups      int      `json:"pending_popups"`
	Actions     []string `json:"actions,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func (s *Server) accountByID(id string) *store.Account {
	for _, a := range s.store.Accounts() {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// sessionOf rebuilds an upstream session from a persisted account.
func sessionOf(a *store.Account) *astron.Session {
	return astron.SessionFrom(a.AccountID, a.UID, a.Token, a.SSOSessionID, a.ModelBearer)
}

func (s *Server) recordBenefit(account, action, result, detail string) {
	s.store.RecordBenefit(&store.BenefitEvent{
		Time:    time.Now().Unix(),
		Account: account,
		Action:  action,
		Result:  result,
		Detail:  truncate(detail, 400),
	})
}

// RefreshAccountStatus pulls balance / membership / popups / beta eligibility and
// persists the result. It never claims anything.
func (s *Server) RefreshAccountStatus(ctx context.Context, id string) (*store.Account, error) {
	account := s.accountByID(id)
	if account == nil {
		return nil, errors.New("account not found")
	}
	if account.Token == "" {
		return nil, errors.New("account has no session cookie; re-import from AStudio")
	}
	snap := s.Client().Snapshot(ctx, sessionOf(account))

	var sessionErr error
	if snap.Balance == nil && snap.Membership == nil && snap.Popups == nil && snap.Beta == nil {
		// Everything failed: surface the aggregated reason.
		sessionErr = errors.New(firstNonEmpty(snap.Problem(), "account status unavailable"))
	}

	now := time.Now().Unix()

	// Self-heal: accounts created before uid normalization may store "0".
	uid := astron.NormalizeUID(account.UID)
	if uid == "" && account.Token != "" {
		if info, err := s.Client().FetchUserInfo(ctx, sessionOf(account)); err == nil && info != nil {
			uid = astron.NormalizeUID(info.UID)
		}
	}

	err := s.store.UpdateAccount(id, func(a *store.Account) {
		a.LastCheckedAt = now
		a.StatusNote = truncate(snap.Problem(), 400)
		if uid != "" {
			a.UID = uid
		}
		if snap.Balance != nil {
			a.PointsKnown = true
			a.PointsBalance = snap.Balance.TotalBalance
			a.SparkBalance = snap.Balance.SparkTotalBalance
			a.PointsExpireAt = snap.Balance.ActivityNextExpireTime
		}
		if snap.Membership != nil {
			a.PlanCode = snap.Membership.PlanCode
			a.PlanName = snap.Membership.PlanName
			a.PlanActive = snap.Membership.Active
		}
		if snap.Popups != nil {
			a.PendingPopups = countClaimable(snap.Popups)
		}
		if sessionErr != nil {
			a.SessionOK = false
			a.LastError = truncate(sessionErr.Error(), 500)
		} else {
			a.SessionOK = true
			if a.LastError == "" || strings.Contains(a.LastError, "session") {
				a.LastError = ""
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if sessionErr != nil {
		return s.accountByID(id), sessionErr
	}
	return s.accountByID(id), nil
}

func countClaimable(popups []astron.Popup) int {
	n := 0
	for _, p := range popups {
		if claimablePopupTypes[p.ComponentType] {
			n++
		}
	}
	return n
}

// CheckinAccount refreshes status and then performs the enabled claim actions.
func (s *Server) CheckinAccount(ctx context.Context, id string) *CheckinResult {
	settings := s.store.Settings()
	result := &CheckinResult{}

	account, err := s.RefreshAccountStatus(ctx, id)
	if account == nil {
		result.AccountID = id
		result.Error = errString(err)
		return result
	}
	result.AccountID = account.AccountID
	result.Name = firstNonEmpty(account.Nickname, account.Name, account.AccountID)
	result.Points = account.PointsBalance
	result.Spark = account.SparkBalance
	result.Plan = firstNonEmpty(account.PlanName, account.PlanCode)
	result.Popups = account.PendingPopups
	// Snapshot the starting balance so the final delta reflects what actually
	// landed, not merely which upstream calls returned success.
	balanceBefore := account.PointsBalance + account.SparkBalance

	session := sessionOf(account)
	if err != nil {
		result.Error = errString(err)
		s.recordBenefit(result.Name, "status", "failed", result.Error)
		return result
	}
	s.recordBenefit(result.Name, "status", "ok",
		fmt.Sprintf("积分 %d / Spark %d / 会员 %s", account.PointsBalance, account.SparkBalance, result.Plan))

	// 1) 运营弹窗（含每日签到 DAILY_REWARD_DIALOG）
	if settings.CheckinCompletePopups {
		popups, popupErr := s.Client().PendingPopups(ctx, session)
		if popupErr != nil {
			result.Error = errString(popupErr)
			s.recordBenefit(result.Name, "签到", "failed", result.Error)
			return result
		}
		claimed := 0
		for _, p := range popups {
			if !claimablePopupTypes[p.ComponentType] {
				continue
			}
			label := popupLabel(p.ComponentType)
			if err := s.Client().CompletePopup(ctx, session, p.PopupID, p.InstanceKey); err != nil {
				s.recordBenefit(result.Name, label, "failed", errString(err))
				continue
			}
			claimed++
			result.Actions = append(result.Actions, label)
			s.recordBenefit(result.Name, label, "ok",
				fmt.Sprintf("popupId=%d code=%s", p.PopupID, p.InstanceKey))
		}
		if claimed > 0 {
			_ = s.store.UpdateAccount(id, func(a *store.Account) {
				a.LastClaimAt = time.Now().Unix()
			})
		}
	}

	// 2) 客户端下载奖励（一次性；已领过会返回业务错误，属正常）
	if settings.CheckinClaimDownload {
		if err := s.Client().ClaimDownloadReward(ctx, session); err != nil {
			if !isAlreadyClaimed(err) {
				s.recordBenefit(result.Name, "客户端下载奖励", "skipped", errString(err))
			}
		} else {
			result.Actions = append(result.Actions, "客户端下载奖励")
			s.recordBenefit(result.Name, "客户端下载奖励", "ok", "")
			_ = s.store.UpdateAccount(id, func(a *store.Account) { a.LastClaimAt = time.Now().Unix() })
		}
	}

	// 3) Beta 资格（需要 domainAccount，未配置则跳过）
	if settings.CheckinClaimBeta && strings.TrimSpace(account.DomainAccount) != "" {
		elig, eligErr := s.Client().BetaEligibility(ctx, session)
		switch {
		case eligErr != nil:
			s.recordBenefit(result.Name, "Beta 领取", "failed", errString(eligErr))
		case elig != nil && elig.CanClaim:
			if err := s.Client().ClaimBeta(ctx, session, account.DomainAccount); err != nil {
				s.recordBenefit(result.Name, "Beta 领取", "failed", errString(err))
			} else {
				result.Actions = append(result.Actions, "Beta 领取")
				s.recordBenefit(result.Name, "Beta 领取", "ok", account.DomainAccount)
				// 官方在 beta 领取成功后会重换凭据，这里对齐该行为。
				_ = s.RefreshAccount(ctx, id)
				_ = s.store.UpdateAccount(id, func(a *store.Account) { a.LastClaimAt = time.Now().Unix() })
			}
		default:
			s.recordBenefit(result.Name, "Beta 领取", "skipped", "不具备领取资格")
		}
	}

	// 结算后再取一次余额，让面板显示领取后的真实数字。
	if refreshed, err := s.RefreshAccountStatus(ctx, id); err == nil && refreshed != nil {
		result.Points = refreshed.PointsBalance
		result.Spark = refreshed.SparkBalance
		result.Popups = refreshed.PendingPopups
		result.PointsDelta = (refreshed.PointsBalance + refreshed.SparkBalance) - balanceBefore
	}
	if len(result.Actions) > 0 && result.PointsDelta == 0 {
		// The calls succeeded but nothing was credited: almost always "already
		// claimed today". Say so instead of implying a fresh win.
		s.recordBenefit(result.Name, "结算", "skipped", "调用成功但积分无变化（可能今日已领）")
	}
	return result
}

// CheckinAll runs the check-in flow for every enabled account.
func (s *Server) CheckinAll(ctx context.Context) []*CheckinResult {
	accounts := s.store.Accounts()
	results := make([]*CheckinResult, 0, len(accounts))
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		results = append(results, s.CheckinAccount(ctx, a.ID))
	}
	return results
}

// KeepaliveAccount refreshes the model credential and the account status,
// keeping both the Bearer token and the session cookie warm.
func (s *Server) KeepaliveAccount(ctx context.Context, id string) error {
	account := s.accountByID(id)
	if account == nil {
		return errors.New("account not found")
	}
	if !account.Enabled {
		return nil
	}
	name := firstNonEmpty(account.Nickname, account.Name, account.AccountID)

	if account.Token != "" {
		if err := s.RefreshAccount(ctx, id); err != nil {
			s.recordBenefit(name, "保活", "failed", errString(err))
			return err
		}
	}
	if _, err := s.RefreshAccountStatus(ctx, id); err != nil {
		s.recordBenefit(name, "保活", "failed", errString(err))
		return err
	}
	s.recordBenefit(name, "保活", "ok", "")
	return nil
}

// KeepaliveAll sweeps the whole pool.
func (s *Server) KeepaliveAll(ctx context.Context) []error {
	var errs []error
	for _, a := range s.store.Accounts() {
		if !a.Enabled {
			continue
		}
		if err := s.KeepaliveAccount(ctx, a.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// RedeemForAccount redeems a code against one account.
func (s *Server) RedeemForAccount(ctx context.Context, id, code string) (*astron.RedeemResult, error) {
	account := s.accountByID(id)
	if account == nil {
		return nil, errors.New("account not found")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("兑换码不能为空")
	}
	name := firstNonEmpty(account.Nickname, account.Name, account.AccountID)
	result, err := s.Client().RedeemCode(ctx, sessionOf(account), code)
	if err != nil {
		s.recordBenefit(name, "兑换码", "failed", errString(err))
		return nil, err
	}
	s.recordBenefit(name, "兑换码", "ok",
		fmt.Sprintf("+%d 积分，到期 %s", result.Points, result.ExpireTime))
	_, _ = s.RefreshAccountStatus(ctx, id)
	return result, nil
}

// isAlreadyClaimed reports whether an upstream error means "nothing left to claim".
func isAlreadyClaimed(err error) bool {
	var envErr *astron.EnvelopeError
	if errors.As(err, &envErr) {
		switch envErr.Code {
		case "11003002", "11003001": // conflict / not found
			return true
		}
		desc := strings.ToLower(envErr.Desc)
		return strings.Contains(desc, "already") || strings.Contains(desc, "已领") || strings.Contains(desc, "重复")
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), 400)
}
