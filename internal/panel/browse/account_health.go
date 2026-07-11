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
