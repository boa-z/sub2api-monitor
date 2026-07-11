package browse

import (
	"testing"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

func TestConcurrencyLoadScoreAndHot(t *testing.T) {
	if ConcurrencyLoadScore(90, 0) <= ConcurrencyLoadScore(50, 0) {
		t.Fatal("higher pct should score higher")
	}
	if ConcurrencyLoadScore(10, 1) <= ConcurrencyLoadScore(99, 0) {
		t.Fatal("waiting should outrank pure pct")
	}
	if !IsHotLoad(80, 0) || !IsHotLoad(10, 1) || IsHotLoad(50, 0) {
		t.Fatal("hot thresholds")
	}
}

func TestHotConcurrencySelectors(t *testing.T) {
	snap := &sub2api.ConcurrencySnapshot{
		Platform: map[string]sub2api.ConcurrencyBucket{
			"openai":    {Platform: "openai", LoadPercentage: 95},
			"anthropic": {Platform: "anthropic", LoadPercentage: 10},
			"gemini":    {Platform: "gemini", LoadPercentage: 20, WaitingInQueue: 2},
		},
		Group: map[string]sub2api.ConcurrencyBucket{
			"1": {GroupID: 1, LoadPercentage: 90},
			"2": {GroupID: 2, LoadPercentage: 10},
			"3": {GroupID: 3, WaitingInQueue: 1, LoadPercentage: 5},
		},
		Account: map[string]sub2api.ConcurrencyBucket{
			"a": {AccountID: 11, LoadPercentage: 99},
			"b": {AccountID: 12, LoadPercentage: 20},
			"c": {AccountID: 13, WaitingInQueue: 3, LoadPercentage: 1},
		},
	}
	plats := HotConcurrencyPlatforms(snap, 2)
	if len(plats) != 2 || plats[0] != "gemini" || plats[1] != "openai" {
		t.Fatalf("plats=%v", plats)
	}
	groups := HotConcurrencyGroups(snap, 3)
	if len(groups) != 2 || groups[0] != 3 || groups[1] != 1 {
		t.Fatalf("groups=%v", groups)
	}
	accs := HotConcurrencyAccounts(snap, 3)
	if len(accs) != 2 || accs[0] != 13 || accs[1] != 11 {
		t.Fatalf("accs=%v", accs)
	}
}
