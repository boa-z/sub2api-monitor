package browse

import (
	"strings"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

// UserConcurrencyPct returns the user's concurrency utilization percentage.
// When max <= 0 the quota is treated as unlimited and returns 0.
func UserConcurrencyPct(current, max int) float64 {
	if max <= 0 {
		return 0
	}
	if current < 0 {
		current = 0
	}
	return float64(current) / float64(max) * 100
}

// UserIsHot reports whether an instance user's concurrency quota looks saturated.
// Unlimited quotas (max <= 0) are never considered hot from percentage alone.
func UserIsHot(current, max int) bool {
	if max <= 0 {
		return false
	}
	if current >= max {
		return true
	}
	return UserConcurrencyPct(current, max) >= HotLoadThreshold
}

// UserStatusNeedsAttention reports disabled/suspended-like statuses.
func UserStatusNeedsAttention(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "disabled", "suspended", "banned", "inactive", "blocked":
		return true
	default:
		return false
	}
}

// CountUserOpsErrors counts unresolved ops errors for a Sub2API user id.
func CountUserOpsErrors(items []sub2api.OpsError, userID int64) (count int, accountIDs []int64) {
	if userID <= 0 || len(items) == 0 {
		return 0, nil
	}
	seen := map[int64]struct{}{}
	for _, e := range items {
		if e.UserID != userID || e.Resolved {
			continue
		}
		count++
		if e.AccountID > 0 {
			if _, ok := seen[e.AccountID]; !ok {
				seen[e.AccountID] = struct{}{}
				accountIDs = append(accountIDs, e.AccountID)
			}
		}
	}
	return count, accountIDs
}

// CountAccountOpsErrors counts unresolved ops errors for a Sub2API account id.
func CountAccountOpsErrors(items []sub2api.OpsError, accountID int64) int {
	if accountID <= 0 || len(items) == 0 {
		return 0
	}
	n := 0
	for _, e := range items {
		if e.AccountID == accountID && !e.Resolved {
			n++
		}
	}
	return n
}

// FilterHotUsers keeps only users whose concurrency quota looks saturated.
func FilterHotUsers(items []sub2api.User) []sub2api.User {
	if len(items) == 0 {
		return nil
	}
	out := make([]sub2api.User, 0, len(items))
	for _, u := range items {
		if UserIsHot(u.CurrentConcurrency, u.Concurrency) {
			out = append(out, u)
		}
	}
	return out
}
