package panel

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
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
