package browse

import "github.com/boa/sub2api-monitor/internal/sub2api"

// HotLoadThreshold is the load percentage at which a concurrency bucket is
// treated as needing operator attention.
const HotLoadThreshold = 80.0

// ConcurrencyLoadScore ranks a concurrency bucket. Higher is hotter.
// Waiting queue is weighted above pure percentage so queueing is surfaced first.
func ConcurrencyLoadScore(loadPct float64, waiting int) int {
	if loadPct < 0 {
		loadPct = 0
	}
	if waiting < 0 {
		waiting = 0
	}
	// Waiting dominates pure percentage so queued buckets surface first.
	// loadPct is typically 0..100 (+headroom); waiting*1000 always outranks pct-only.
	return int(loadPct) + waiting*1000
}

// IsHotLoad reports whether a bucket is operationally hot.
func IsHotLoad(loadPct float64, waiting int) bool {
	return waiting > 0 || loadPct >= HotLoadThreshold
}

// HotConcurrencyAccounts returns unique account IDs from hot account buckets,
// sorted by load score descending.
func HotConcurrencyAccounts(snap *sub2api.ConcurrencySnapshot, maxN int) []int64 {
	if snap == nil || maxN <= 0 {
		return nil
	}
	type item struct {
		id    int64
		score int
	}
	var items []item
	seen := map[int64]struct{}{}
	for _, b := range snap.Account {
		id := b.AccountID
		if id <= 0 {
			continue
		}
		if !IsHotLoad(b.LoadPercentage, b.WaitingInQueue) && b.CurrentInUse <= 0 {
			// include only hot or active load for shortcuts
			continue
		}
		if !IsHotLoad(b.LoadPercentage, b.WaitingInQueue) {
			// still allow high-ish load shortcuts for top used accounts via caller;
			// this helper focuses on hot.
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		items = append(items, item{id, ConcurrencyLoadScore(b.LoadPercentage, b.WaitingInQueue)})
	}
	// sort by score desc
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > maxN {
		items = items[:maxN]
	}
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.id)
	}
	return out
}

// HotConcurrencyPlatforms returns platform keys sorted by heat.
func HotConcurrencyPlatforms(snap *sub2api.ConcurrencySnapshot, maxN int) []string {
	if snap == nil || maxN <= 0 {
		return nil
	}
	type item struct {
		key   string
		score int
	}
	var items []item
	for k, b := range snap.Platform {
		name := k
		if b.Platform != "" {
			name = b.Platform
		}
		if name == "" || !IsHotLoad(b.LoadPercentage, b.WaitingInQueue) {
			continue
		}
		items = append(items, item{name, ConcurrencyLoadScore(b.LoadPercentage, b.WaitingInQueue)})
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > maxN {
		items = items[:maxN]
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.key)
	}
	return out
}

// HotConcurrencyGroups returns group IDs sorted by heat.
func HotConcurrencyGroups(snap *sub2api.ConcurrencySnapshot, maxN int) []int64 {
	if snap == nil || maxN <= 0 {
		return nil
	}
	type item struct {
		id    int64
		score int
	}
	var items []item
	seen := map[int64]struct{}{}
	for _, b := range snap.Group {
		id := b.GroupID
		if id <= 0 || !IsHotLoad(b.LoadPercentage, b.WaitingInQueue) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		items = append(items, item{id, ConcurrencyLoadScore(b.LoadPercentage, b.WaitingInQueue)})
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].score > items[i].score {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > maxN {
		items = items[:maxN]
	}
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.id)
	}
	return out
}
