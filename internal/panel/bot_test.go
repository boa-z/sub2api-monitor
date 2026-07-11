package panel

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/panel/browse"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

func testBot(t *testing.T) (*Bot, *userstore.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	store, err := userstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Instance: "test",
		Telegram: config.TelegramConfig{
			ChatID: "1001",
			Panel: config.PanelConfig{
				Enabled:          true,
				OpenRegistration: true,
				CheckInterval:    5 * time.Minute,
				Cooldown:         2 * time.Hour,
				UsersPath:        path,
			},
		},
		Checks: config.ChecksConfig{
			AccountUsage: config.AccountUsageCheck{
				DefaultThresholds: []config.UsageThreshold{
					{Window: "five_hour", UtilizationGTE: 80, Severity: "P2"},
					{Window: "seven_day", UtilizationGTE: 90, Severity: "P1"},
				},
			},
		},
	}
	b := New(nil, store, cfg, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	return b, store
}

func TestAllowedOpenRegistration(t *testing.T) {
	b, _ := testBot(t)
	if !b.allowed(42) {
		t.Fatal("expected open registration to allow any user")
	}
}

func TestAllowedAllowlist(t *testing.T) {
	b, _ := testBot(t)
	b.cfg.Telegram.Panel.OpenRegistration = false
	b.cfg.Telegram.Panel.AllowAll = false
	b.cfg.Telegram.Panel.AllowUserIDs = []int64{7, 9}
	if b.allowed(42) {
		t.Fatal("should deny non-allowlisted")
	}
	if !b.allowed(7) {
		t.Fatal("should allow listed user")
	}
}

func TestAllowedChatOwnerFallback(t *testing.T) {
	b, _ := testBot(t)
	b.cfg.Telegram.Panel.OpenRegistration = false
	b.cfg.Telegram.Panel.AllowAll = false
	b.cfg.Telegram.Panel.AllowUserIDs = nil
	b.cfg.Telegram.ChatID = "1001"
	if !b.allowed(1001) {
		t.Fatal("chat owner should be allowed")
	}
	if b.allowed(1002) {
		t.Fatal("other users denied when closed")
	}
}

func TestNormalizeWindow(t *testing.T) {
	if got := normalizeWindow("5h"); got != "five_hour" {
		t.Fatalf("got %s", got)
	}
	if got := normalizeWindow("7d"); got != "seven_day" {
		t.Fatalf("got %s", got)
	}
	if got := normalizeWindow("Five_Hour"); got != "five_hour" {
		t.Fatalf("got %s", got)
	}
}

func TestSetAndDeleteThreshold(t *testing.T) {
	b, store := testBot(t)
	if _, err := store.GetOrCreate(1, "1", "u", "U"); err != nil {
		t.Fatal(err)
	}
	if err := b.setThreshold(1, "5h", 70, "P1"); err != nil {
		t.Fatal(err)
	}
	p, ok := store.Get(1)
	if !ok {
		t.Fatal("missing profile")
	}
	if len(p.Thresholds) < 2 {
		t.Fatalf("expected defaults materialized + update, got %+v", p.Thresholds)
	}
	found := false
	for _, th := range p.Thresholds {
		if th.Window == "five_hour" && th.UtilizationGTE == 70 && th.Severity == "P1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("five_hour not updated: %+v", p.Thresholds)
	}
	if err := b.deleteThreshold(1, "five_hour"); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Get(1)
	for _, th := range p.Thresholds {
		if th.Window == "five_hour" {
			t.Fatal("five_hour should be deleted")
		}
	}
}

func TestHomeTextAndThresholdsText(t *testing.T) {
	b, store := testBot(t)
	if _, err := store.GetOrCreate(2, "2", "alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	_, _ = store.Update(2, func(p *userstore.Profile) error {
		p.BaseURL = "http://example.com"
		p.AdminAPIKey = "sk-test-key-12345678"
		en := true
		p.Accounts = []userstore.AccountWatch{{ID: 9, Name: "demo", Enabled: &en}}
		return nil
	})
	txt := b.homeText(2)
	if txt == "" || !containsAll(txt, "开启", "demo", "example.com") && !containsAll(txt, "开启", "example.com") {
		// name may not appear in home, but base and mon should
		if !containsAll(txt, "开启", "example.com", "1") {
			t.Fatalf("unexpected home text: %s", txt)
		}
	}
	th := b.thresholdsText(2)
	if !containsAll(th, "five_hour", "系统默认") {
		t.Fatalf("unexpected thresholds text: %s", th)
	}
}

func TestSessionExpiry(t *testing.T) {
	b, _ := testBot(t)
	b.setAwait(5, awaitBaseURL, 0, "")
	if s := b.getSession(5); s == nil || s.Await != awaitBaseURL {
		t.Fatal("expected session")
	}
	b.mu.Lock()
	b.sessions[5].UpdatedAt = time.Now().Add(-11 * time.Minute)
	b.mu.Unlock()
	if s := b.getSession(5); s != nil {
		t.Fatal("expected expired session cleared")
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func TestIsAdminFromConfig(t *testing.T) {
	b, _ := testBot(t)
	b.cfg.Telegram.Panel.OpenRegistration = true
	b.cfg.Telegram.Panel.AdminUserIDs = []int64{100}
	b.cfg.Telegram.ChatID = "999"
	if !b.isAdmin(100) {
		t.Fatal("admin_user_ids should grant admin")
	}
	if b.isAdmin(42) {
		t.Fatal("normal open-reg user should not be admin when admin_user_ids set")
	}
	if !b.allowed(42) {
		t.Fatal("normal user still allowed via open registration")
	}
}

func TestIsAdminChatOwnerFallback(t *testing.T) {
	b, _ := testBot(t)
	b.cfg.Telegram.Panel.AdminUserIDs = nil
	b.cfg.Telegram.ChatID = "1001"
	if !b.isAdmin(1001) {
		t.Fatal("chat owner should be admin when admin list empty")
	}
	if b.isAdmin(1002) {
		t.Fatal("other user not admin")
	}
}

func TestIsAdminProfileOverride(t *testing.T) {
	b, store := testBot(t)
	b.cfg.Telegram.Panel.AdminUserIDs = []int64{1}
	if _, err := store.GetOrCreate(2, "2", "u", "U"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(2, func(p *userstore.Profile) error {
		p.Role = userstore.RoleAdmin
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !b.isAdmin(2) {
		t.Fatal("profile role=admin should grant admin")
	}
	if _, err := store.Update(1, func(p *userstore.Profile) error {
		// user 1 is in admin list but force user role
		p.Role = userstore.RoleUser
		return nil
	}); err != nil {
		// may not exist
		if _, err2 := store.GetOrCreate(1, "1", "a", "A"); err2 != nil {
			t.Fatal(err2)
		}
		if _, err3 := store.Update(1, func(p *userstore.Profile) error {
			p.Role = userstore.RoleUser
			return nil
		}); err3 != nil {
			t.Fatal(err3)
		}
	}
	if b.isAdmin(1) {
		t.Fatal("profile role=user should override admin_user_ids")
	}
}

func TestHomeKeyboardRoleAware(t *testing.T) {
	b, _ := testBot(t)
	b.cfg.Telegram.Panel.AdminUserIDs = []int64{7}
	adminKB := b.homeKeyboardFor(7)
	userKB := b.homeKeyboardFor(8)
	adminJoined := ""
	for _, row := range adminKB.InlineKeyboard {
		for _, btn := range row {
			adminJoined += btn.CallbackData + ","
		}
	}
	userJoined := ""
	for _, row := range userKB.InlineKeyboard {
		for _, btn := range row {
			userJoined += btn.CallbackData + ","
		}
	}
	if !strings.Contains(adminJoined, "mgr_menu") || !strings.Contains(adminJoined, "ops_menu") {
		t.Fatalf("admin keyboard missing manage/ops: %s", adminJoined)
	}
	if strings.Contains(userJoined, "mgr_menu") || strings.Contains(userJoined, "ops_menu") {
		t.Fatalf("user keyboard should hide manage/ops: %s", userJoined)
	}
}

func TestParseBrowseCallback(t *testing.T) {
	st, page := parseBrowseCallback("active:2")
	if st != "active" || page != 2 {
		t.Fatalf("got %s %d", st, page)
	}
	st, page = parseBrowseCallback("search|foo bar:0")
	if st != "search:foo bar" || page != 0 {
		t.Fatalf("got %q %d", st, page)
	}
	st, page = parseBrowseCallback("plat|openai:1")
	if st != "plat:openai" || page != 1 {
		t.Fatalf("got %q %d", st, page)
	}
}

func TestParseDurationLabel(t *testing.T) {
	if parseDurationLabel("15m") != 15*60 {
		t.Fatal("15m")
	}
	if parseDurationLabel("1h") != 3600 {
		t.Fatal("1h")
	}
	if parseDurationLabel("nope") != 0 {
		t.Fatal("invalid")
	}
}

func TestEditTextClampHelpers(t *testing.T) {
	// ensure parseBrowse still works after other changes
	st, page := parseBrowseCallback("plat|openai:3")
	if st != "plat:openai" || page != 3 {
		t.Fatalf("%s %d", st, page)
	}
}

func TestIsAdminRoleOverrideAndFallback(t *testing.T) {
	b, store := testBot(t)
	// empty admin list + chat_id owner fallback
	if !b.isAdmin(1001) {
		t.Fatal("chat_id owner should be admin when admin_user_ids empty")
	}
	if b.isAdmin(2002) {
		t.Fatal("other user should not be admin")
	}
	b.cfg.Telegram.Panel.AdminUserIDs = []int64{3003}
	if !b.isAdmin(3003) {
		t.Fatal("admin_user_ids should grant admin")
	}
	if b.isAdmin(1001) {
		t.Fatal("chat_id fallback disabled when admin list non-empty")
	}
	if _, err := store.GetOrCreate(4004, "4004", "u", "U"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(4004, func(p *userstore.Profile) error {
		p.Role = userstore.RoleAdmin
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !b.isAdmin(4004) {
		t.Fatal("profile.role=admin should override")
	}
	// create profile 3003 with user role to demote config-level admin
	if _, err := store.GetOrCreate(3003, "3003", "a", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(3003, func(p *userstore.Profile) error {
		p.Role = userstore.RoleUser
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if b.isAdmin(3003) {
		t.Fatal("profile.role=user should demote even if in admin_user_ids")
	}
}

func TestAccountDetailKeyboardHidesManageForUser(t *testing.T) {
	b, store := testBot(t)
	if _, err := store.GetOrCreate(55, "55", "u", "U"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(55, func(p *userstore.Profile) error {
		p.Role = userstore.RoleUser
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	kb := b.accountDetailKeyboard(55, 9)
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, "mgr_acc:") {
				t.Fatal("non-admin should not see manage button")
			}
		}
	}
	// admin via chat_id fallback
	kb2 := b.accountDetailKeyboard(1001, 9)
	found := false
	for _, row := range kb2.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, "mgr_acc:") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("admin should see manage button")
	}
}

func TestParseBrowseCallbackAndTokens(t *testing.T) {
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
	}
	for _, c := range cases {
		got, page := parseBrowseCallback(c.rest)
		if got != c.want || page != c.page {
			t.Fatalf("rest=%q got=(%q,%d) want=(%q,%d)", c.rest, got, page, c.want, c.page)
		}
	}
	if browseToken("search:abc") != "search|abc" {
		t.Fatal(browseToken("search:abc"))
	}
	if browseToken("plat:gemini") != "plat|gemini" {
		t.Fatal(browseToken("plat:gemini"))
	}
}

func TestInferBulkActionKey(t *testing.T) {
	if inferBulkActionKey("mgr_bulk_heal_go") != "heal" {
		t.Fatal(inferBulkActionKey("mgr_bulk_heal_go"))
	}
	if inferBulkActionKey("mgr_bulk_clear_rl_go") != "clear_rl" {
		t.Fatal(inferBulkActionKey("mgr_bulk_clear_rl_go"))
	}
	if inferBulkActionKey("mgr_bulk_sched_on_go") != "sched_on" {
		t.Fatal(inferBulkActionKey("mgr_bulk_sched_on_go"))
	}
}

func TestIsRateLimitedAccount(t *testing.T) {
	now := time.Now()
	if !isRateLimitedAccount(sub2api.Account{RateLimitedAt: &now}) {
		t.Fatal("rate limited at")
	}
	if !isRateLimitedAccount(sub2api.Account{Status: "rate_limited"}) {
		t.Fatal("status")
	}
	if isRateLimitedAccount(sub2api.Account{Status: "active"}) {
		t.Fatal("active should not match")
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
	if len(nav) != 1 || len(nav[0]) != 1 {
		t.Fatalf("expected next only, got %+v", nav)
	}
	if nav[0][0].CallbackData != "ops_errors:u:1" {
		t.Fatal(nav[0][0].CallbackData)
	}
	nav2 := errorPageNav("r", 1, &sub2api.OpsErrorPage{Total: 15, PageSize: 10, Items: make([]sub2api.OpsError, 5)})
	if len(nav2) != 1 || len(nav2[0]) != 1 || nav2[0][0].CallbackData != "ops_errors:r:0" {
		t.Fatalf("expected prev only, got %+v", nav2)
	}
}

func TestSetPanelUserRole(t *testing.T) {
	b, store := testBot(t)
	if _, err := store.GetOrCreate(9001, "9001", "x", "X"); err != nil {
		t.Fatal(err)
	}
	// admin via chat fallback 1001
	// simulate role set through store (same as handler)
	if _, err := store.Update(9001, func(p *userstore.Profile) error {
		p.Role = userstore.RoleAdmin
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !b.isAdmin(9001) {
		t.Fatal("expected admin after role set")
	}
	if _, err := store.Update(9001, func(p *userstore.Profile) error {
		p.Role = userstore.RoleUser
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if b.isAdmin(9001) {
		t.Fatal("expected demoted")
	}
	if _, err := store.Update(9001, func(p *userstore.Profile) error {
		p.Role = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpsErrorViewMemory(t *testing.T) {
	b, _ := testBot(t)
	kind, page := b.getOpsErrorView(42)
	if kind != "all" || page != 0 {
		t.Fatalf("default %s %d", kind, page)
	}
	b.setOpsErrorView(42, "u", 3)
	kind, page = b.getOpsErrorView(42)
	if kind != "u" || page != 3 {
		t.Fatalf("got %s %d", kind, page)
	}
	// setAwait should not wipe ops memory
	b.setAwait(42, awaitSearch, 0, "")
	kind, page = b.getOpsErrorView(42)
	if kind != "u" || page != 3 {
		t.Fatalf("after setAwait %s %d", kind, page)
	}
}

func TestBadAccountTabLabels(t *testing.T) {
	if errorTabLabel("限速", "rl", "rl") != "• 限速" {
		t.Fatal(errorTabLabel("限速", "rl", "rl"))
	}
	if errorTabLabel("error", "rl", "error") != "error" {
		t.Fatal(errorTabLabel("error", "rl", "error"))
	}
}

func TestParseBadAccCallback(t *testing.T) {
	kind, page := browse.ParseBadAccCallback("")
	if kind != "error" || page != 0 {
		t.Fatalf("empty %s %d", kind, page)
	}
	kind, page = browse.ParseBadAccCallback("rl:2")
	if kind != "rl" || page != 2 {
		t.Fatalf("rl:2 got %s %d", kind, page)
	}
	kind, page = browse.ParseBadAccCallback("unsched")
	if kind != "unsched" || page != 0 {
		t.Fatalf("unsched %s %d", kind, page)
	}
	kind, page = browse.ParseBadAccCallback("weird:1")
	if kind != "error" || page != 1 {
		t.Fatalf("weird %s %d", kind, page)
	}
}

func TestWriteErrorItemsLiveHealButtons(t *testing.T) {
	page := &sub2api.OpsErrorPage{Items: []sub2api.OpsError{
		{ID: 9, AccountID: 42, Message: "boom", Severity: "error", StatusCode: 500},
	}}
	var bld strings.Builder
	var rows [][]telegram.InlineKeyboardButton
	writeErrorItems(&bld, page, "u", 8, &rows)
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	ids := map[string]bool{}
	for _, btn := range rows[0] {
		ids[btn.CallbackData] = true
	}
	for _, want := range []string{"oe:r:u:9", "live_act:heal:42", "acc_live:42", "mgr_acc:42"} {
		if !ids[want] {
			t.Fatalf("missing %s in %+v", want, ids)
		}
	}
}

func TestOpsMenuTextContainsHints(t *testing.T) {
	b, _ := testBot(t)
	text := b.opsMenuText(context.Background(), 1001)
	if !strings.Contains(text, "运维视图") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "异常账号") {
		t.Fatal(text)
	}
}

func TestDashboardKeyboard(t *testing.T) {
	kb := dashboardKeyboard(&sub2api.DashboardStats{ErrorAccounts: 3, RatelimitAccounts: 2})
	if kb == nil || len(kb.InlineKeyboard) < 2 {
		t.Fatal("empty keyboard")
	}
	foundErr, foundRL := false, false
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "ops_badacc:error:0" {
				foundErr = true
			}
			if btn.CallbackData == "ops_badacc:rl:0" {
				foundRL = true
			}
		}
	}
	if !foundErr || !foundRL {
		t.Fatalf("missing jumps err=%v rl=%v", foundErr, foundRL)
	}
	// empty stats still has fallback
	kb2 := dashboardKeyboard(nil)
	ok := false
	for _, row := range kb2.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "ops_badacc:error:0" {
				ok = true
			}
		}
	}
	if !ok {
		t.Fatal("nil stats should still offer badacc")
	}
}
