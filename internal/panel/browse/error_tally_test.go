package browse

import (
	"testing"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

func TestTopUnresolvedErrorDimensions(t *testing.T) {
	items := []sub2api.OpsError{
		{Platform: "openai", UserID: 1, UserEmail: "a@x", AccountID: 10, Resolved: false},
		{Platform: "openai", UserID: 1, UserEmail: "a@x", AccountID: 10, Resolved: false},
		{Platform: "anthropic", UserID: 2, AccountID: 11, Resolved: false},
		{Platform: "openai", UserID: 3, AccountID: 12, Resolved: true},
	}
	plats := TopUnresolvedErrorPlatforms(items, 2)
	if len(plats) != 2 || plats[0].Key != "openai" || plats[0].Count != 2 {
		t.Fatalf("plats=%v", plats)
	}
	users := TopUnresolvedErrorUsers(items, 2)
	if len(users) < 1 || users[0].ID != 1 || users[0].Count != 2 {
		t.Fatalf("users=%v", users)
	}
	accs := TopUnresolvedErrorAccounts(items, 2)
	if len(accs) < 1 || accs[0].ID != 10 || accs[0].Count != 2 {
		t.Fatalf("accs=%v", accs)
	}
	// Ensure resolved filtered by Collect
	pages := CollectUnresolvedOpsErrors(&sub2api.OpsErrorPage{Items: items})
	if len(pages) != 3 {
		t.Fatalf("collect=%d", len(pages))
	}
}
