package discordpanel

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

func testBot(t *testing.T) (*Bot, *userstore.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := userstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Instance: "test",
		Discord: config.DiscordConfig{
			Panel: config.DiscordPanelConfig{
				Enabled:          true,
				OpenRegistration: true,
				AdminUserIDs:     []int64{100},
				UsersPath:        path,
				CheckInterval:    time.Minute,
				Cooldown:         time.Hour,
			},
		},
		Checks: config.ChecksConfig{
			AccountUsage: config.AccountUsageCheck{
				DefaultThresholds: []config.UsageThreshold{
					{Window: "five_hour", UtilizationGTE: 80, Severity: "P2"},
				},
			},
		},
	}
	b := New(nil, store, cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	return b, store
}

func TestDiscordAllowedAndAdmin(t *testing.T) {
	b, store := testBot(t)
	if !b.allowed(42) {
		t.Fatal("open registration should allow")
	}
	if b.isAdmin(42) {
		t.Fatal("normal user not admin")
	}
	if !b.isAdmin(100) {
		t.Fatal("admin_user_ids should grant admin")
	}
	if _, err := store.GetOrCreatePlatform(7, userstore.PlatformDiscord, "7", "u", "U"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(7, func(p *userstore.Profile) error {
		p.Role = userstore.RoleAdmin
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !b.isAdmin(7) {
		t.Fatal("profile role admin")
	}
}

func TestDiscordHomeComponentsRoleAware(t *testing.T) {
	b, _ := testBot(t)
	admin := b.homeComponents(100)
	user := b.homeComponents(42)
	adminHasOps := false
	for _, row := range admin {
		for _, c := range row.Components {
			if c.CustomID == "ops_menu" || c.CustomID == "mgr_menu" {
				adminHasOps = true
			}
		}
	}
	if !adminHasOps {
		t.Fatal("admin keyboard missing ops/manage")
	}
	for _, row := range user {
		for _, c := range row.Components {
			if c.CustomID == "ops_menu" || c.CustomID == "mgr_menu" {
				t.Fatal("user keyboard should hide ops/manage")
			}
		}
	}
}

func TestNotifyRecipientsDiscord(t *testing.T) {
	p := &userstore.Profile{TelegramUserID: 99, Platform: userstore.PlatformDiscord}
	rs := p.NotifyRecipients()
	if len(rs) != 1 || rs[0] != "discord:99" {
		t.Fatalf("got %v", rs)
	}
}
