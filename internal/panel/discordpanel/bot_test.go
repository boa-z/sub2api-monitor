package discordpanel

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/discord"
	"github.com/boa/sub2api-monitor/internal/sub2api"
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

func TestErrorTabLabelAndPageNav(t *testing.T) {
	if errorTabLabel("上游", "u", "u") != "• 上游" {
		t.Fatal(errorTabLabel("上游", "u", "u"))
	}
	if errorTabLabel("请求", "u", "r") != "请求" {
		t.Fatal(errorTabLabel("请求", "u", "r"))
	}
	nav := errorPageNav("u", 0, &sub2api.OpsErrorPage{Total: 25, PageSize: 10, Items: make([]sub2api.OpsError, 10)})
	if len(nav) != 1 || len(nav[0].Components) != 1 {
		t.Fatalf("expected next only, got %+v", nav)
	}
	if nav[0].Components[0].CustomID != "ops_errors:u:1" {
		t.Fatal(nav[0].Components[0].CustomID)
	}
}

func TestFilterBtn(t *testing.T) {
	if filterBtn("全部", "all", "all") != "• 全部" {
		t.Fatal(filterBtn("全部", "all", "all"))
	}
	if filterBtn("error", "active", "error") != "error" {
		t.Fatal(filterBtn("error", "active", "error"))
	}
	if filterBtn("openai", "plat:openai", "plat:openai") != "• openai" {
		t.Fatal(filterBtn("openai", "plat:openai", "plat:openai"))
	}
}

func TestParseTempDur(t *testing.T) {
	if parseTempDur("15m") != 15*60 {
		t.Fatal(parseTempDur("15m"))
	}
	if parseTempDur("1h") != 3600 {
		t.Fatal(parseTempDur("1h"))
	}
	if parseTempDur("bad") != 0 {
		t.Fatal(parseTempDur("bad"))
	}
}

func TestOpsComponentsIncludeConcChannels(t *testing.T) {
	foundConc, foundCh, foundErr := false, false, false
	for _, row := range opsComponents() {
		for _, c := range row.Components {
			switch c.CustomID {
			case "ops_conc":
				foundConc = true
			case "ops_channels":
				foundCh = true
			case "ops_errors:all:0":
				foundErr = true
			}
		}
	}
	if !foundConc || !foundCh || !foundErr {
		t.Fatalf("ops components missing: conc=%v ch=%v err=%v", foundConc, foundCh, foundErr)
	}
}

func TestShowPanelUsersAndRole(t *testing.T) {
	b, store := testBot(t)
	if _, err := store.GetOrCreatePlatform(9001, userstore.PlatformDiscord, "9001", "u", "U"); err != nil {
		t.Fatal(err)
	}
	text, comps := b.showPanelUsers(100, 0, "")
	if text == "" || len(comps) == 0 {
		t.Fatal("empty panel users")
	}
	text, comps = b.setPanelUserRole(100, 9001, "admin")
	if !b.isAdmin(9001) {
		t.Fatal("role not applied")
	}
	if !containsCustomID(comps, "pnl_role:user:9001") && !containsCustomID(comps, "pnl_mon:9001") {
		// detail view should have mon/src/role buttons
		t.Fatalf("detail comps missing toggles: %+v", comps)
	}
	text, comps = b.togglePanelUserMonitor(100, 9001)
	p, _ := store.Get(9001)
	if p.Enabled {
		// started enabled true by default; toggle should flip to false
		// GetOrCreate defaults Enabled true, first toggle -> false
	}
	if p.Enabled {
		t.Fatal("expected monitor disabled after toggle")
	}
	_ = text
}

func containsCustomID(comps []discord.Component, id string) bool {
	for _, row := range comps {
		for _, c := range row.Components {
			if c.CustomID == id {
				return true
			}
		}
	}
	return false
}
