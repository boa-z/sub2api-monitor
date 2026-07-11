package browse

import (
	"fmt"
	"strings"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

// ErrorTally is a counted dimension (platform / user / account) for ops triage.
type ErrorTally struct {
	Key   string
	ID    int64 // optional numeric id (user/account)
	Count int
}

// CollectUnresolvedOpsErrors returns only unresolved items.
func CollectUnresolvedOpsErrors(pages ...*sub2api.OpsErrorPage) []sub2api.OpsError {
	var out []sub2api.OpsError
	for _, p := range pages {
		if p == nil {
			continue
		}
		for _, e := range p.Items {
			if !e.Resolved {
				out = append(out, e)
			}
		}
	}
	return out
}

// TopUnresolvedErrorPlatforms ranks unresolved errors by platform.
func TopUnresolvedErrorPlatforms(items []sub2api.OpsError, maxN int) []ErrorTally {
	return topErrorDimension(items, maxN, func(e sub2api.OpsError) (string, int64) {
		p := strings.TrimSpace(e.Platform)
		if p == "" {
			return "", 0
		}
		return strings.ToLower(p), 0
	})
}

// TopUnresolvedErrorUsers ranks unresolved errors by Sub2API user id.
func TopUnresolvedErrorUsers(items []sub2api.OpsError, maxN int) []ErrorTally {
	return topErrorDimension(items, maxN, func(e sub2api.OpsError) (string, int64) {
		if e.UserID <= 0 {
			return "", 0
		}
		label := strings.TrimSpace(e.UserEmail)
		if label == "" {
			label = fmt.Sprintf("user#%d", e.UserID)
		}
		return label, e.UserID
	})
}

// TopUnresolvedErrorAccounts ranks unresolved errors by account id.
func TopUnresolvedErrorAccounts(items []sub2api.OpsError, maxN int) []ErrorTally {
	return topErrorDimension(items, maxN, func(e sub2api.OpsError) (string, int64) {
		if e.AccountID <= 0 {
			return "", 0
		}
		label := strings.TrimSpace(e.AccountName)
		if label == "" {
			label = fmt.Sprintf("#%d", e.AccountID)
		}
		return label, e.AccountID
	})
}

func topErrorDimension(items []sub2api.OpsError, maxN int, keyFn func(sub2api.OpsError) (string, int64)) []ErrorTally {
	if maxN <= 0 || len(items) == 0 {
		return nil
	}
	type acc struct {
		label string
		id    int64
		n     int
	}
	by := map[string]*acc{}
	for _, e := range items {
		if e.Resolved {
			continue
		}
		label, id := keyFn(e)
		if label == "" && id <= 0 {
			continue
		}
		k := label
		if id > 0 {
			k = fmt.Sprintf("id:%d", id)
		}
		a := by[k]
		if a == nil {
			a = &acc{label: label, id: id}
			by[k] = a
		}
		a.n++
		if a.label == "" {
			a.label = label
		}
	}
	out := make([]ErrorTally, 0, len(by))
	for _, a := range by {
		out = append(out, ErrorTally{Key: a.label, ID: a.id, Count: a.n})
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[i].Count || (out[j].Count == out[i].Count && out[j].Key < out[i].Key) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > maxN {
		out = out[:maxN]
	}
	return out
}
