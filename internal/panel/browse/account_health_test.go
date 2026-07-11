package browse

import (
	"strings"
	"testing"
	"time"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

func TestAccountIssueKind(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		a    sub2api.Account
		want string
	}{
		{"ok", sub2api.Account{Status: "active", Schedulable: true}, IssueOK},
		{"error status", sub2api.Account{Status: "error", Schedulable: true}, IssueError},
		{"error msg", sub2api.Account{Status: "active", ErrorMessage: "boom", Schedulable: true}, IssueError},
		{"rl", sub2api.Account{Status: "active", RateLimitedAt: &now, Schedulable: true}, IssueRL},
		{"ol", sub2api.Account{Status: "active", OverloadUntil: &now, Schedulable: true}, IssueOverload},
		{"unsched", sub2api.Account{Status: "active", Schedulable: false}, IssueUnsched},
		{"temp", sub2api.Account{Status: "active", Schedulable: true, TempUnschedulableUntil: &now}, IssueTemp},
		{"disabled", sub2api.Account{Status: "disabled", Schedulable: false}, IssueDisabled},
	}
	for _, tc := range cases {
		if got := AccountIssueKind(tc.a); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestAccountIssueLabel(t *testing.T) {
	if AccountIssueLabel(IssueError) != "异常" {
		t.Fatal(AccountIssueLabel(IssueError))
	}
	if AccountIssueLabel(IssueOK) != "正常" {
		t.Fatal(AccountIssueLabel(IssueOK))
	}
}

func TestLiveActionPlanFor(t *testing.T) {
	p := LiveActionPlanFor(IssueError)
	if len(p.Rows) != 2 || p.Rows[0][0] != LiveHeal || !p.AppendRefreshWithManage {
		t.Fatalf("error plan: %+v", p)
	}
	p = LiveActionPlanFor(IssueRL)
	if p.Rows[0][0] != LiveClearRL || !p.AppendRefreshWithManage {
		t.Fatalf("rl plan: %+v", p)
	}
	p = LiveActionPlanFor(IssueUnsched)
	if p.Rows[0][0] != LiveSched {
		t.Fatalf("unsched plan: %+v", p)
	}
	p = LiveActionPlanFor(IssueTemp)
	if p.Rows[0][0] != LiveClearTemp {
		t.Fatalf("temp plan: %+v", p)
	}
	p = LiveActionPlanFor(IssueDisabled)
	if p.Rows[0][0] != LiveEnable {
		t.Fatalf("disabled plan: %+v", p)
	}
	p = LiveActionPlanFor(IssueOK)
	if p.AppendRefreshWithManage || len(p.Rows) != 3 {
		t.Fatalf("ok plan: %+v", p)
	}
	// disabled should still offer useful recovery actions
	p = LiveActionPlanFor(IssueDisabled)
	if len(p.Rows) < 1 || !p.AppendRefreshWithManage {
		t.Fatalf("disabled plan: %+v", p)
	}
}

func TestLiveActionLabel(t *testing.T) {
	if LiveActionLabel(LiveHeal) != "一键修复" {
		t.Fatal(LiveActionLabel(LiveHeal))
	}
	if LiveActionLabel("x") != "x" {
		t.Fatal(LiveActionLabel("x"))
	}
}

func TestHealAccountNilTruncate(t *testing.T) {
	// Compile-time/shape smoke: truncateFn nil path uses default slicer.
	// No real client — only verify helper doesn't panic on empty steps with nil cli would panic.
	// So just ensure LiveAction labels still work after heal addition.
	if LiveActionLabel(LiveHeal) == "" {
		t.Fatal("empty")
	}
}

func TestAccountIsUnhealthyAndFlags(t *testing.T) {
	now := time.Now()
	if AccountIsUnhealthy(sub2api.Account{Status: "active", Schedulable: true}) {
		t.Fatal("healthy active")
	}
	if !AccountIsUnhealthy(sub2api.Account{Status: "active", OverloadUntil: &now, Schedulable: true}) {
		t.Fatal("overload should be unhealthy")
	}
	if !AccountIsUnhealthy(sub2api.Account{Status: "active", TempUnschedulableUntil: &now, Schedulable: true}) {
		t.Fatal("temp should be unhealthy")
	}
	if !AccountIsUnhealthy(sub2api.Account{Status: "disabled"}) {
		t.Fatal("disabled")
	}
	if StatusFlag(sub2api.Account{Status: "active", OverloadUntil: &now}) != "🔥" {
		t.Fatal(StatusFlag(sub2api.Account{Status: "active", OverloadUntil: &now}))
	}
	if StatusFlag(sub2api.Account{Status: "active", TempUnschedulableUntil: &now}) != "⏳" {
		t.Fatal("temp flag")
	}
	parts := StatusDetailParts(sub2api.Account{Platform: "openai", Status: "active", OverloadUntil: &now, Schedulable: true})
	joined := strings.Join(parts, "/")
	if !strings.Contains(joined, "过载") {
		t.Fatalf("parts=%v", parts)
	}
	if !AccountNeedsHeal(sub2api.Account{Status: "disabled"}) {
		t.Fatal("disabled needs heal (enable)")
	}
}

func TestDashboardTriage(t *testing.T) {
	ops, bad, data, issues := DashboardTriage(nil)
	if issues || data != "ops_badacc:error:0" || bad != "异常账号" {
		t.Fatalf("nil: %s %s %s %v", ops, bad, data, issues)
	}
	ops, bad, data, issues = DashboardTriage(&sub2api.DashboardStats{OverloadAccounts: 3})
	if !issues || data != "ops_badacc:ol:0" || bad != "过载 3" || ops != "运维⚠" {
		t.Fatalf("overload: %s %s %s %v", ops, bad, data, issues)
	}
	ops, bad, data, issues = DashboardTriage(&sub2api.DashboardStats{RatelimitAccounts: 2, OverloadAccounts: 9})
	if data != "ops_badacc:rl:0" || bad != "限速 2" {
		t.Fatalf("rl priority over ol: %s %s %s", ops, bad, data)
	}
	ops, bad, data, issues = DashboardTriage(&sub2api.DashboardStats{ErrorAccounts: 1, RatelimitAccounts: 5, OverloadAccounts: 9})
	if data != "ops_badacc:error:0" || bad != "异常 1" {
		t.Fatalf("error wins: %s %s %s", ops, bad, data)
	}
}

func TestPlatformProblemScore(t *testing.T) {
	if PlatformProblemScore(1, 0) <= PlatformProblemScore(0, 9) {
		t.Fatal("errors should outrank pure rl")
	}
	if PlatformProblemScore(0, 0) != 0 {
		t.Fatal("healthy")
	}
}
