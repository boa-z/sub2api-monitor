package browse

import (
	"testing"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

func TestUserConcurrencyPct(t *testing.T) {
	if UserConcurrencyPct(0, 0) != 0 {
		t.Fatal("unlimited")
	}
	if UserConcurrencyPct(4, 10) != 40 {
		t.Fatalf("got %v", UserConcurrencyPct(4, 10))
	}
}

func TestUserIsHot(t *testing.T) {
	if UserIsHot(5, 0) {
		t.Fatal("unlimited should not be hot")
	}
	if !UserIsHot(10, 10) {
		t.Fatal("full quota")
	}
	if !UserIsHot(8, 10) {
		t.Fatal("80% should be hot")
	}
	if UserIsHot(7, 10) {
		t.Fatal("70% not hot")
	}
}

func TestUserStatusNeedsAttention(t *testing.T) {
	if !UserStatusNeedsAttention("Disabled") {
		t.Fatal("disabled")
	}
	if UserStatusNeedsAttention("active") {
		t.Fatal("active")
	}
}

func TestCountUserOpsErrors(t *testing.T) {
	items := []sub2api.OpsError{
		{UserID: 1, Resolved: false, AccountID: 10},
		{UserID: 1, Resolved: true, AccountID: 11},
		{UserID: 2, Resolved: false, AccountID: 12},
		{UserID: 1, Resolved: false, AccountID: 10},
		{UserID: 1, Resolved: false, AccountID: 13},
	}
	n, accs := CountUserOpsErrors(items, 1)
	if n != 3 {
		t.Fatalf("count=%d", n)
	}
	if len(accs) != 2 {
		t.Fatalf("accs=%v", accs)
	}
}
