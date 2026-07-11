package browse

import (
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

// AccountNeedsHeal reports whether heal/clear/recover is likely useful.
func AccountNeedsHeal(a sub2api.Account) bool {
	switch AccountIssueKind(a) {
	case IssueError, IssueRL, IssueOverload, IssueUnsched, IssueTemp:
		return true
	default:
		return false
	}
}

// Live quick-action tokens shared by Telegram / Discord live views.
const (
	LiveHeal     = "heal"
	LiveClearErr = "clear_err"
	LiveClearRL  = "clear_rl"
	LiveRecover  = "recover"
	LiveSched    = "sched"
	LiveRefresh  = "refresh"
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
	case IssueUnsched, IssueTemp:
		return LiveActionPlan{
			Rows: [][]string{
				{LiveSched, LiveHeal},
				{LiveClearRL, LiveRecover},
			},
			AppendRefreshWithManage: true,
		}
	case IssueDisabled:
		// Live has no "enable"; keep recover/sched/heal useful and push refresh to manage row.
		return LiveActionPlan{
			Rows: [][]string{
				{LiveRecover, LiveSched},
				{LiveHeal, LiveClearErr},
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
	default:
		return action
	}
}
