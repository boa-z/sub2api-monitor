package browse

import (
	"context"
	"fmt"
	"strings"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

// Account issue kinds for triage UI prioritization.
const (
	IssueOK       = "ok"
	IssueError    = "error"
	IssueRL       = "rate_limited"
	IssueOverload = "overload"
	IssueUnsched  = "unsched"
	IssueTemp     = "temp"
	IssueDisabled = "disabled"
)

// AccountIssueKind returns the primary issue token for an account.
// Priority: disabled > error > overload > rate_limited > temp > unsched > ok.
func AccountIssueKind(a sub2api.Account) string {
	st := strings.ToLower(strings.TrimSpace(a.Status))
	if st == "disabled" {
		return IssueDisabled
	}
	if st == "error" || strings.TrimSpace(a.ErrorMessage) != "" {
		return IssueError
	}
	if a.OverloadUntil != nil || strings.Contains(st, "overload") {
		return IssueOverload
	}
	if a.RateLimitedAt != nil || strings.Contains(st, "rate") || strings.Contains(st, "limit") {
		return IssueRL
	}
	if a.TempUnschedulableUntil != nil || strings.TrimSpace(a.TempUnschedulableReason) != "" {
		return IssueTemp
	}
	if !a.Schedulable {
		return IssueUnsched
	}
	return IssueOK
}

// AccountIssueLabel is a short Chinese label for the primary issue.
func AccountIssueLabel(kind string) string {
	switch kind {
	case IssueError:
		return "异常"
	case IssueRL:
		return "限速"
	case IssueOverload:
		return "过载"
	case IssueUnsched:
		return "停调度"
	case IssueTemp:
		return "临时停调度"
	case IssueDisabled:
		return "已禁用"
	default:
		return "正常"
	}
}

// AccountNeedsHeal reports whether heal/clear/recover/enable is likely useful.
func AccountNeedsHeal(a sub2api.Account) bool {
	switch AccountIssueKind(a) {
	case IssueError, IssueRL, IssueOverload, IssueUnsched, IssueTemp, IssueDisabled:
		return true
	default:
		return false
	}
}

// AccountIsUnhealthy reports status-level problems (not usage thresholds).
func AccountIsUnhealthy(a sub2api.Account) bool {
	return AccountIssueKind(a) != IssueOK
}

// StatusFlag returns a short emoji/status marker for triage lists.
func StatusFlag(a sub2api.Account) string {
	switch AccountIssueKind(a) {
	case IssueError:
		return "❌"
	case IssueRL:
		return "⏱"
	case IssueOverload:
		return "🔥"
	case IssueUnsched:
		return "⏸"
	case IssueTemp:
		return "⏳"
	case IssueDisabled:
		return "🚫"
	default:
		return "✅"
	}
}

// StatusDetailParts builds compact status tokens for a one-line summary
// (platform/status + secondary issue tags when useful).
func StatusDetailParts(a sub2api.Account) []string {
	parts := []string{a.Status}
	if a.Platform != "" {
		parts = []string{a.Platform, a.Status}
	}
	kind := AccountIssueKind(a)
	// Annotate secondary signals that may not be obvious from status alone.
	switch kind {
	case IssueTemp:
		parts = append(parts, "临时停")
	case IssueUnsched:
		parts = append(parts, "停调度")
	case IssueRL:
		if !strings.Contains(strings.ToLower(a.Status), "rate") && !strings.Contains(strings.ToLower(a.Status), "limit") {
			parts = append(parts, "限速")
		}
	case IssueOverload:
		if !strings.Contains(strings.ToLower(a.Status), "overload") {
			parts = append(parts, "过载")
		}
	case IssueDisabled:
		// status already carries disabled
	case IssueError:
		// status/error already shown
	}
	return parts
}

// Live quick-action tokens shared by Telegram / Discord live views.
const (
	LiveHeal      = "heal"
	LiveClearErr  = "clear_err"
	LiveClearRL   = "clear_rl"
	LiveRecover   = "recover"
	LiveSched     = "sched"
	LiveRefresh   = "refresh"
	LiveClearTemp = "clear_temp"
	LiveEnable    = "enable"
)

// LiveActionPlan describes prioritized live-view action buttons for an issue kind.
// Rows hold action tokens only (not navigation / manage). When AppendRefreshWithManage
// is true, the UI should place "refresh" next to "完整管理" instead of in Rows.
type LiveActionPlan struct {
	Rows                    [][]string
	AppendRefreshWithManage bool
}

// LiveActionPlanFor returns a context-aware action layout for the account issue kind.
// Mirrors triage priority used by the Telegram live keyboard.
func LiveActionPlanFor(kind string) LiveActionPlan {
	switch kind {
	case IssueError:
		return LiveActionPlan{
			Rows: [][]string{
				{LiveHeal, LiveClearErr},
				{LiveRecover, LiveSched},
			},
			AppendRefreshWithManage: true,
		}
	case IssueRL, IssueOverload:
		return LiveActionPlan{
			Rows: [][]string{
				{LiveClearRL, LiveHeal},
				{LiveSched, LiveClearErr},
			},
			AppendRefreshWithManage: true,
		}
	case IssueUnsched:
		return LiveActionPlan{
			Rows: [][]string{
				{LiveSched, LiveHeal},
				{LiveClearTemp, LiveRecover},
			},
			AppendRefreshWithManage: true,
		}
	case IssueTemp:
		return LiveActionPlan{
			Rows: [][]string{
				{LiveClearTemp, LiveSched},
				{LiveHeal, LiveRecover},
			},
			AppendRefreshWithManage: true,
		}
	case IssueDisabled:
		return LiveActionPlan{
			Rows: [][]string{
				{LiveEnable, LiveSched},
				{LiveHeal, LiveRecover},
			},
			AppendRefreshWithManage: true,
		}
	default:
		return LiveActionPlan{
			Rows: [][]string{
				{LiveHeal, LiveClearErr},
				{LiveClearRL, LiveRecover},
				{LiveSched, LiveRefresh},
			},
			AppendRefreshWithManage: false,
		}
	}
}

// LiveActionLabel is a short Chinese label for a live action token.
func LiveActionLabel(action string) string {
	switch action {
	case LiveHeal:
		return "一键修复"
	case LiveClearErr:
		return "清错误"
	case LiveClearRL:
		return "清限速"
	case LiveRecover:
		return "恢复"
	case LiveSched:
		return "开调度"
	case LiveRefresh:
		return "刷新凭据"
	case LiveClearTemp:
		return "清临时停"
	case LiveEnable:
		return "启用"
	default:
		return action
	}
}

// HealAccount runs a recovery pipeline:
// clear error → clear rate limit → clear temp unsched → recover → enable (active) → enable schedulable.
// Returns a human-readable summary (no HTML/markdown escaping).
// Individual step failures are collected so partially-healed accounts still report progress.
func HealAccount(ctx context.Context, cli *sub2api.Client, accountID int64, truncateFn func(string, int) string) string {
	if truncateFn == nil {
		truncateFn = func(s string, n int) string {
			if n <= 0 || len(s) <= n {
				return s
			}
			// rune-safe-ish for ASCII errors; callers can pass richer truncators
			if len(s) > n {
				return s[:n]
			}
			return s
		}
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{"清错误", func() error { _, err := cli.ClearAccountError(ctx, accountID); return err }},
		{"清限速", func() error { _, err := cli.ClearAccountRateLimit(ctx, accountID); return err }},
		{"清临时停", func() error { return cli.ClearTempUnschedulable(ctx, accountID) }},
		{"恢复", func() error { _, err := cli.RecoverAccountState(ctx, accountID); return err }},
		{"启用", func() error { _, err := cli.SetAccountStatus(ctx, accountID, "active"); return err }},
		{"开调度", func() error { _, err := cli.SetSchedulable(ctx, accountID, true); return err }},
	}
	var ok, fail []string
	for _, s := range steps {
		if err := s.fn(); err != nil {
			fail = append(fail, s.name+": "+truncateFn(err.Error(), 40))
		} else {
			ok = append(ok, s.name)
		}
	}
	if len(ok) == 0 {
		return "❌ 一键修复全部失败: " + strings.Join(fail, "; ")
	}
	msg := "✅ 一键修复完成: " + strings.Join(ok, " · ")
	if len(fail) > 0 {
		msg += "\n⚠️ 部分失败: " + strings.Join(fail, "; ")
	}
	return msg
}

// DashboardTriage returns home/ops shortcut labels for the dominant failure mode.
// Priority: error > rate_limited > overload. badData is a callback token suitable
// for both Telegram and Discord (ops_badacc:<kind>:0).
func DashboardTriage(st *sub2api.DashboardStats) (opsLabel, badLabel, badData string, issues bool) {
	opsLabel = "运维"
	badLabel = "异常账号"
	badData = "ops_badacc:error:0"
	if st == nil {
		return opsLabel, badLabel, badData, false
	}
	if st.ErrorAccounts > 0 {
		return "运维⚠", fmt.Sprintf("异常 %v", st.ErrorAccounts), "ops_badacc:error:0", true
	}
	if st.RatelimitAccounts > 0 {
		return "运维⚠", fmt.Sprintf("限速 %v", st.RatelimitAccounts), "ops_badacc:rl:0", true
	}
	if st.OverloadAccounts > 0 {
		return "运维⚠", fmt.Sprintf("过载 %v", st.OverloadAccounts), "ops_badacc:ol:0", true
	}
	return opsLabel, badLabel, badData, false
}
