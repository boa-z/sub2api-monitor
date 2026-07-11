package browse

import (
	"context"
	"testing"
	"time"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

func TestParseCallbackAndToken(t *testing.T) {
	cases := []struct {
		rest string
		want string
		page int
	}{
		{"all:0", "all", 0},
		{"error:2", "error", 2},
		{"search|foo:1", "search:foo", 1},
		{"plat|openai:0", "plat:openai", 0},
		{"plat|openai|active:3", "plat:openai:active", 3},
		{"unsched:1", "unsched", 1},
		{"rate_limited:0", "rate_limited", 0},
	}
	for _, c := range cases {
		got, page := ParseCallback(c.rest)
		if got != c.want || page != c.page {
			t.Fatalf("rest=%q got=(%q,%d) want=(%q,%d)", c.rest, got, page, c.want, c.page)
		}
	}
	if Token("search:abc") != "search|abc" {
		t.Fatal(Token("search:abc"))
	}
	if Token("plat:gemini") != "plat|gemini" {
		t.Fatal(Token("plat:gemini"))
	}
	if Title("rate_limited") != "限速" {
		t.Fatal(Title("rate_limited"))
	}
}

func TestIsRateLimited(t *testing.T) {
	now := time.Now()
	if !IsRateLimited(sub2api.Account{RateLimitedAt: &now}) {
		t.Fatal("rate limited at")
	}
	if !IsRateLimited(sub2api.Account{Status: "rate_limited"}) {
		t.Fatal("status")
	}
	if IsRateLimited(sub2api.Account{Status: "active"}) {
		t.Fatal("active")
	}
}

func TestParseFilter(t *testing.T) {
	f := ParseFilter("plat:openai:active")
	if f.Platform != "openai" || f.Status != "active" {
		t.Fatalf("%+v", f)
	}
	f = ParseFilter("search:hello")
	if f.Search != "hello" {
		t.Fatalf("%+v", f)
	}
}

func TestParseBadAccAndNormalize(t *testing.T) {
	if NormalizeBadKind("RL") != "rl" {
		t.Fatal(NormalizeBadKind("RL"))
	}
	if NormalizeBadKind("x") != "error" {
		t.Fatal(NormalizeBadKind("x"))
	}
	k, p := ParseBadAccCallback("all:3")
	if k != "all" || p != 3 {
		t.Fatalf("%s %d", k, p)
	}
	k, p = ParseBadAccCallback("")
	if k != "error" || p != 0 {
		t.Fatalf("%s %d", k, p)
	}
}

func TestIsOverloaded(t *testing.T) {
	if !IsOverloaded(sub2api.Account{Status: "overload"}) {
		t.Fatal("status overload")
	}
	until := sub2api.Account{}
	// OverloadUntil via RateLimited helper still true for IsRateLimited; IsOverloaded needs field
	if IsOverloaded(sub2api.Account{Status: "active"}) {
		t.Fatal("active")
	}
	if Title("overload") != "过载" {
		t.Fatal(Title("overload"))
	}
	if NormalizeBadKind("ol") != "ol" {
		t.Fatal(NormalizeBadKind("ol"))
	}
	_ = until
}

func TestFetchAccountSnapsEmpty(t *testing.T) {
	opts := SnapOpts{Source: "passive", MaxShow: 8, Concurrency: 4}
	if got := FetchAccountSnaps(context.Background(), nil, nil, opts); got != nil {
		t.Fatalf("nil client: %+v", got)
	}
	if got := FetchAccountSnaps(context.Background(), nil, []WatchTarget{{ID: 1}}, opts); got != nil {
		t.Fatalf("nil client with targets: %+v", got)
	}
}

func TestFmtID(t *testing.T) {
	if got := fmtID(42); got != "#42" {
		t.Fatalf("got %s", got)
	}
}

func TestBulkScopeCompatible(t *testing.T) {
	if !bulkScopeCompatible("clear_err", "error") {
		t.Fatal("error clear")
	}
	if bulkScopeCompatible("clear_err", "active") {
		t.Fatal("active should not scope clear_err")
	}
	if !bulkScopeCompatible("clear_rl", "rate_limited") {
		t.Fatal("rl")
	}
	if bulkScopeCompatible("clear_rl", "error") {
		t.Fatal("error should not scope clear_rl")
	}
	if !bulkScopeCompatible("heal", "search:foo") {
		t.Fatal("search scopes heal")
	}
	if !bulkScopeCompatible("heal", "plat:openai") {
		t.Fatal("plat scopes heal")
	}
	if bulkScopeCompatible("sched_on", "all") {
		t.Fatal("all not scoped")
	}
	if !bulkScopeCompatible("heal", "problem") {
		t.Fatal("problem should scope heal")
	}
	if !bulkScopeCompatible("clear_rl", "problem") {
		t.Fatal("problem should scope clear_rl")
	}
}

func TestStatusFromBadKind(t *testing.T) {
	cases := map[string]string{
		"error":   "error",
		"rl":      "rate_limited",
		"ol":      "overload",
		"unsched": "unsched",
		"all":     "problem",
		"":        "error",
		"weird":   "error",
	}
	for in, want := range cases {
		if got := StatusFromBadKind(in); got != want {
			t.Fatalf("StatusFromBadKind(%q)=%q want %q", in, got, want)
		}
	}
}

func TestTitleProblem(t *testing.T) {
	if Title("problem") != "异常汇总" {
		t.Fatalf("title=%s", Title("problem"))
	}
	if Token("problem") != "problem" {
		t.Fatalf("token=%s", Token("problem"))
	}
	st, pg := ParseCallback("problem:2")
	if st != "problem" || pg != 2 {
		t.Fatalf("parse %s %d", st, pg)
	}
}

func TestTitleDisabledTemp(t *testing.T) {
	if Title("disabled") != "已禁用" {
		t.Fatal(Title("disabled"))
	}
	if Title("temp") != "临时停调度" {
		t.Fatal(Title("temp"))
	}
}

func TestBulkScopeEnableClearTemp(t *testing.T) {
	if !bulkScopeCompatible("enable", "disabled") {
		t.Fatal("enable+disabled")
	}
	if bulkScopeCompatible("enable", "error") {
		t.Fatal("enable should not scope error")
	}
	if !bulkScopeCompatible("clear_temp", "temp") {
		t.Fatal("clear_temp+temp")
	}
	if !bulkScopeCompatible("sched_on", "temp") {
		t.Fatal("sched_on+temp")
	}
}

func TestIsTempUnschedulable(t *testing.T) {
	now := time.Now()
	if !IsTempUnschedulable(sub2api.Account{TempUnschedulableUntil: &now}) {
		t.Fatal("until")
	}
	if !IsTempUnschedulable(sub2api.Account{TempUnschedulableReason: "x"}) {
		t.Fatal("reason")
	}
	if IsTempUnschedulable(sub2api.Account{}) {
		t.Fatal("empty")
	}
}

func TestNormalizeBadKindTempDisabled(t *testing.T) {
	if NormalizeBadKind("temp") != "temp" {
		t.Fatal(NormalizeBadKind("temp"))
	}
	if NormalizeBadKind("disabled") != "disabled" {
		t.Fatal(NormalizeBadKind("disabled"))
	}
	if StatusFromBadKind("temp") != "temp" || StatusFromBadKind("disabled") != "disabled" {
		t.Fatal(StatusFromBadKind("temp"), StatusFromBadKind("disabled"))
	}
	if StatusFromBadKind("all") != "problem" {
		t.Fatal(StatusFromBadKind("all"))
	}
}
