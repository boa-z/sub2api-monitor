package browse

import (
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
