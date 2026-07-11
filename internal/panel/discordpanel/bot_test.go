package discordpanel

import (
	"context"
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

func TestBadAccountTabLabels(t *testing.T) {
	if errorTabLabel("限速", "rl", "rl") != "• 限速" {
		t.Fatal(errorTabLabel("限速", "rl", "rl"))
	}
	if errorTabLabel("error", "rl", "error") != "error" {
		t.Fatal(errorTabLabel("error", "rl", "error"))
	}
}

func TestOpsComponentsBadAccCallback(t *testing.T) {
	found := false
	for _, row := range opsComponents() {
		for _, c := range row.Components {
			if c.CustomID == "ops_badacc:error:0" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("ops components missing badacc error:0")
	}
}

func TestOpsErrorViewMemory(t *testing.T) {
	b, _ := testBot(t)
	kind, page := b.getOpsErrorView(42)
	if kind != "all" || page != 0 {
		t.Fatalf("default %s %d", kind, page)
	}
	b.setOpsErrorView(42, "u", 2)
	kind, page = b.getOpsErrorView(42)
	if kind != "u" || page != 2 {
		t.Fatalf("got %s %d", kind, page)
	}
	// setAwait should not wipe ops memory
	b.setAwait(42, awaitSearch, 0, "")
	kind, page = b.getOpsErrorView(42)
	if kind != "u" || page != 2 {
		t.Fatalf("after setAwait %s %d", kind, page)
	}
}

func TestDashboardComponents(t *testing.T) {
	comps := dashboardComponents(&sub2api.DashboardStats{ErrorAccounts: 4, RatelimitAccounts: 1})
	if len(comps) < 2 {
		t.Fatal("expected jump rows")
	}
	if !containsCustomID(comps, "ops_badacc:error:0") {
		t.Fatal("missing error jump")
	}
	if !containsCustomID(comps, "ops_badacc:rl:0") {
		t.Fatal("missing rl jump")
	}
	comps2 := dashboardComponents(nil)
	if !containsCustomID(comps2, "ops_badacc:error:0") {
		t.Fatal("nil stats fallback")
	}
}

func TestManageBackAndBrowseMemory(t *testing.T) {
	b, _ := testBot(t)
	if b.getManageBack(7) != "mgr_menu" {
		t.Fatal(b.getManageBack(7))
	}
	b.setManageBack(7, "ops_conc")
	label, data := b.manageBackLabel(7)
	if data != "ops_conc" || label != "« 并发" {
		t.Fatalf("%s %s", label, data)
	}
	b.setBrowseView(7, "rate_limited", 1)
	st, page := b.getBrowseView(7)
	if st != "rate_limited" || page != 1 {
		t.Fatalf("%s %d", st, page)
	}
	if b.getManageBack(7) != "mgr_browse:rate_limited:1" {
		t.Fatal(b.getManageBack(7))
	}
	b.setAwait(7, awaitSearch, 0, "")
	if b.getManageBack(7) != "mgr_browse:rate_limited:1" {
		t.Fatal("await wiped manage back")
	}
}

func TestPanelExtractAccountIDs(t *testing.T) {
	ids := panelExtractAccountIDs("account_id=42", "账号 #99", "rate 15 only")
	if len(ids) != 2 || ids[0] != 42 || ids[1] != 99 {
		t.Fatalf("got %v", ids)
	}
}

func TestDiscordHomeAdminShortcuts(t *testing.T) {
	b, _ := testBot(t)
	admin := b.homeComponents(100)
	foundDash, foundBad, foundAlert := false, false, false
	for _, row := range admin {
		for _, c := range row.Components {
			switch c.CustomID {
			case "ops_dash":
				foundDash = true
			case "ops_badacc:error:0":
				foundBad = true
			case "ops_alerts":
				foundAlert = true
			}
		}
	}
	if !foundDash || !foundBad || !foundAlert {
		t.Fatalf("admin home missing shortcuts dash=%v bad=%v alert=%v", foundDash, foundBad, foundAlert)
	}
	user := b.homeComponents(42)
	for _, row := range user {
		for _, c := range row.Components {
			if c.CustomID == "ops_dash" || c.CustomID == "ops_alerts" {
				t.Fatal("user should not see ops shortcuts")
			}
		}
	}
}

func TestManageBackAlertsChannels(t *testing.T) {
	b, _ := testBot(t)
	b.setManageBack(7, "ops_alerts")
	label, data := b.manageBackLabel(7)
	if data != "ops_alerts" || label != "« 告警" {
		t.Fatalf("%s %s", label, data)
	}
	b.setManageBack(7, "ops_channels")
	label, data = b.manageBackLabel(7)
	if data != "ops_channels" || label != "« 渠道" {
		t.Fatalf("%s %s", label, data)
	}
}

func TestChannelIsBad(t *testing.T) {
	if channelIsBad(sub2api.ChannelMonitor{Enabled: true, PrimaryStatus: "healthy"}) {
		t.Fatal("healthy")
	}
	if !channelIsBad(sub2api.ChannelMonitor{Enabled: true, PrimaryStatus: "timeout"}) {
		t.Fatal("timeout")
	}
}

func TestAlertsComponents(t *testing.T) {
	comps := alertsComponents([]int64{5}, 1)
	if !containsCustomID(comps, "mgr_acc:5") {
		t.Fatal("missing manage")
	}
	if !containsCustomID(comps, "ops_dash") {
		t.Fatal("missing dash for firing")
	}
}

func TestManageComponentsForHealth(t *testing.T) {
	comps := manageComponentsFor(&sub2api.DashboardStats{ErrorAccounts: 2, RatelimitAccounts: 1})
	if !containsCustomID(comps, "mgr_bulk_heal") || !containsCustomID(comps, "ops_badacc:error:0") {
		t.Fatalf("%+v", comps)
	}
	// healthy path still has bulk clear
	comps2 := manageComponentsFor(nil)
	if !containsCustomID(comps2, "mgr_bulk_clear") {
		t.Fatal("nil stats missing bulk clear")
	}
}

func TestForceCheckViewEmpty(t *testing.T) {
	b, _ := testBot(t)
	text, comps := b.forceCheckView(context.Background(), 42)
	if text == "" || len(comps) == 0 {
		t.Fatal("empty force check view")
	}
}
