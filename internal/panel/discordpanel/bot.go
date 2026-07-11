// Package discordpanel implements an interactive Discord control panel
// (slash commands + message components) with the same multi-user role model
// as the Telegram panel.
package discordpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/discord"
	"github.com/boa/sub2api-monitor/internal/panel/browse"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

const (
	awaitNone        = ""
	awaitBaseURL     = "base_url"
	awaitAPIKey      = "api_key"
	awaitAddAcc      = "add_account"
	awaitThrPct      = "set_threshold_pct"
	awaitSearch      = "search_account"
	awaitUserSearch  = "search_user"
	awaitGroupSearch = "search_group"
	awaitRename      = "rename_account"
)

type session struct {
	Await     string
	UpdatedAt time.Time
	AccountID int64
	Window    string
	// OpsErrorKind/Page remember last errors view for resolve refresh.
	OpsErrorKind string
	OpsErrorPage int
	// ManageBack is custom_id to return from account manage.
	ManageBack   string
	BrowseStatus string
	BrowsePage   int
	// UserSearch/GroupSearch remember last instance user/group list query.
	UserSearch  string
	UserStatus  string
	GroupSearch string
}

// Bot is the Discord interactive panel.
type Bot struct {
	dc       *discord.Client
	users    *userstore.Store
	cfg      *config.Config
	logger   *slog.Logger
	defaults []config.UsageThreshold

	mu       sync.Mutex
	sessions map[int64]*session
}

func New(dc *discord.Client, users *userstore.Store, cfg *config.Config, logger *slog.Logger) *Bot {
	defs := cfg.Checks.AccountUsage.DefaultThresholds
	if len(defs) == 0 {
		defs = []config.UsageThreshold{
			{Window: "five_hour", UtilizationGTE: 80, Severity: "P2"},
			{Window: "seven_day", UtilizationGTE: 90, Severity: "P1"},
		}
	}
	return &Bot{
		dc:       dc,
		users:    users,
		cfg:      cfg,
		logger:   logger,
		defaults: defs,
		sessions: make(map[int64]*session),
	}
}

// Run registers slash commands and listens on the Discord gateway.
func (b *Bot) Run(ctx context.Context) error {
	dmTrue := true
	cmds := []discord.ApplicationCommand{
		{Name: "panel", Description: "打开 Sub2API 监控面板", Type: 1, DMPermission: &dmTrue},
		{Name: "status", Description: "查看配置摘要", Type: 1, DMPermission: &dmTrue},
		{Name: "check", Description: "立即检查用量", Type: 1, DMPermission: &dmTrue},
		{Name: "ops", Description: "运维视图（管理员）", Type: 1, DMPermission: &dmTrue},
		{Name: "manage", Description: "账号管理（管理员）", Type: 1, DMPermission: &dmTrue},
		{Name: "help", Description: "帮助", Type: 1, DMPermission: &dmTrue},
		{
			Name:         "setbase",
			Description:  "设置 Sub2API Base URL",
			Type:         1,
			DMPermission: &dmTrue,
			Options: []discord.ApplicationCommandOption{
				{Type: 3, Name: "url", Description: "例如 http://host:8080", Required: true},
			},
		},
		{
			Name:         "setkey",
			Description:  "设置 Admin API Key",
			Type:         1,
			DMPermission: &dmTrue,
			Options: []discord.ApplicationCommandOption{
				{Type: 3, Name: "key", Description: "Admin API Key", Required: true},
			},
		},
		{
			Name:         "addaccount",
			Description:  "添加监控账号 ID",
			Type:         1,
			DMPermission: &dmTrue,
			Options: []discord.ApplicationCommandOption{
				{Type: 4, Name: "id", Description: "账号数字 ID", Required: true},
			},
		},
		{
			Name:         "search",
			Description:  "搜索账号（管理员）",
			Type:         1,
			DMPermission: &dmTrue,
			Options: []discord.ApplicationCommandOption{
				{Type: 3, Name: "q", Description: "名称/关键词", Required: true},
			},
		},
	}
	if err := b.dc.RegisterCommands(ctx, cmds); err != nil {
		b.logger.Warn("discord register commands", "err", err)
	} else {
		b.logger.Info("discord slash commands registered")
	}

	gw := discord.NewGateway(b.dc, b.logger, b.handleInteraction)
	b.logger.Info("discord panel gateway listening")
	return gw.Run(ctx)
}

func (b *Bot) handleInteraction(ctx context.Context, it *discord.Interaction) error {
	user := it.User
	if user == nil && it.Member != nil {
		user = it.Member.User
	}
	if user == nil {
		return nil
	}
	uid, err := discord.ParseSnowflake(user.ID)
	if err != nil {
		return err
	}
	if !b.allowed(uid) {
		return b.respond(ctx, it, "无权限使用此面板。请联系管理员将你加入 allow_user_ids / admin_user_ids。", nil, true)
	}
	display := user.Display()
	if _, err := b.users.GetOrCreatePlatform(uid, userstore.PlatformDiscord, user.ID, user.Username, display); err != nil {
		return b.respond(ctx, it, "内部错误: "+err.Error(), nil, true)
	}

	switch it.Type {
	case 2: // application command
		return b.handleCommand(ctx, it, uid, user)
	case 3: // message component
		return b.handleComponent(ctx, it, uid, user)
	case 5: // modal submit
		return b.handleModal(ctx, it, uid, user)
	default:
		return nil
	}
}

func (b *Bot) handleCommand(ctx context.Context, it *discord.Interaction, uid int64, user *discord.User) error {
	if it.Data == nil {
		return nil
	}
	name := it.Data.Name
	switch name {
	case "panel":
		return b.respond(ctx, it, b.homeText(uid), b.homeComponents(uid), false)
	case "status":
		text, comps := b.statusView(ctx, uid)
		return b.respond(ctx, it, text, comps, false)
	case "help":
		return b.respond(ctx, it, helpText(), b.homeComponents(uid), false)
	case "check":
		_ = b.respond(ctx, it, "⏳ 正在检查用量…", nil, false)
		msg, comps := b.forceCheckView(ctx, uid)
		return b.followupEdit(ctx, it, msg, comps)
	case "ops":
		if !b.canOpsRead(uid) {
			return b.respond(ctx, it, "⛔ 运维视图仅管理员或只读运维可用。", b.homeComponents(uid), true)
		}
		return b.respond(ctx, it, b.opsMenuText(ctx, uid), b.opsComponents(uid), false)
	case "manage":
		if !b.canOpsRead(uid) {
			return b.respond(ctx, it, "⛔ 账号管理/浏览仅管理员或只读运维可用。", b.homeComponents(uid), true)
		}
		text, comps := b.manageMenuView(ctx, uid)
		return b.respond(ctx, it, text, comps, false)
	case "setbase":
		url := optionString(it, "url")
		msg := b.setBaseURL(uid, url)
		return b.respond(ctx, it, msg, b.connComponents(uid), false)
	case "setkey":
		key := optionString(it, "key")
		msg := b.setAPIKey(uid, key)
		return b.respond(ctx, it, msg, b.connComponents(uid), true) // ephemeral for secrets
	case "addaccount":
		id := optionInt(it, "id")
		msg := b.addAccount(ctx, uid, strconv.FormatInt(id, 10))
		return b.respond(ctx, it, msg, b.accountsComponents(uid), false)
	case "search":
		if !b.canOpsRead(uid) {
			return b.respond(ctx, it, "⛔ 搜索账号仅管理员或只读运维可用。", b.homeComponents(uid), true)
		}
		q := strings.TrimSpace(optionString(it, "q"))
		if q == "" {
			return b.respond(ctx, it, "请提供关键词，例如 `/search q:openai`", manageComponents(), true)
		}
		text, comps := b.accountBrowser(ctx, uid, "search:"+q, 0)
		return b.respond(ctx, it, text, comps, false)
	default:
		return b.respond(ctx, it, "未知命令", nil, true)
	}
}

func (b *Bot) handleComponent(ctx context.Context, it *discord.Interaction, uid int64, user *discord.User) error {
	if it.Data == nil {
		return nil
	}
	data := it.Data.CustomID
	// select menu value
	if len(it.Data.Values) > 0 && strings.HasPrefix(data, "select:") {
		data = it.Data.Values[0]
	}

	switch {
	case data == "home":
		return b.update(ctx, it, b.homeText(uid), b.homeComponents(uid))
	case data == "status":
		text, comps := b.statusView(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "cfg_conn":
		return b.update(ctx, it, b.connText(uid), b.connComponents(uid))
	case data == "cfg_acc":
		b.syncWatchAccountNames(ctx, uid)
		return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
	case data == "cfg_thr":
		return b.update(ctx, it, b.thresholdsText(uid), b.thrComponents(uid))
	case data == "help":
		return b.update(ctx, it, helpText(), b.homeComponents(uid))
	case data == "check_now":
		_ = b.respondUpdate(ctx, it, "⏳ 正在检查用量…", nil)
		msg, comps := b.forceCheckView(ctx, uid)
		return b.followupEdit(ctx, it, msg, comps)
	case data == "toggle_mon":
		_, _ = b.users.Update(uid, func(p *userstore.Profile) error {
			p.Enabled = !p.Enabled
			return nil
		})
		return b.update(ctx, it, b.homeText(uid), b.homeComponents(uid))
	case data == "toggle_src":
		_, _ = b.users.Update(uid, func(p *userstore.Profile) error {
			if p.Source == "active" {
				p.Source = "passive"
			} else {
				p.Source = "active"
			}
			return nil
		})
		return b.update(ctx, it, b.homeText(uid), b.homeComponents(uid))
	case data == "test_conn":
		return b.update(ctx, it, b.testConnection(ctx, uid)+"\n\n"+b.connText(uid), b.connComponents(uid))
	case data == "clear_conn":
		_, _ = b.users.Update(uid, func(p *userstore.Profile) error {
			p.BaseURL = ""
			p.AdminAPIKey = ""
			p.JWT = ""
			return nil
		})
		return b.update(ctx, it, "✅ 已清除连接\n\n"+b.connText(uid), b.connComponents(uid))
	case data == "seed_conn":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		return b.update(ctx, it, b.seedConnection(uid), b.connComponents(uid))
	case data == "set_base_prompt":
		return b.openModal(ctx, it, discord.NewModal(
			"modal_base", "设置 Base URL", "base_url", "Base URL",
			"http://127.0.0.1:8080", 200,
		))
	case data == "set_key_prompt":
		return b.openModal(ctx, it, discord.NewModal(
			"modal_key", "设置 Admin API Key", "api_key", "API Key",
			"sk-...", 200,
		))
	case data == "add_acc_prompt":
		return b.openModal(ctx, it, discord.NewModal(
			"modal_addacc", "添加监控账号", "account_id", "账号数字 ID",
			"12345", 32,
		))
	case data == "pick_acc" || strings.HasPrefix(data, "pick_acc:"):
		status, page := "all", 0
		if strings.HasPrefix(data, "pick_acc:") {
			status, page = browse.ParseCallback(strings.TrimPrefix(data, "pick_acc:"))
		}
		text, comps := b.accountPickerView(ctx, uid, status, page)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "pick:"):
		idStr := strings.TrimPrefix(data, "pick:")
		msg := b.addAccount(ctx, uid, idStr)
		// stay on picker if failure? refresh accounts on success
		if strings.HasPrefix(msg, "✅") {
			return b.update(ctx, it, msg+"\n\n"+b.accountsText(uid), b.accountsComponents(uid))
		}
		text, comps := b.accountPickerView(ctx, uid, "all", 0)
		return b.update(ctx, it, msg+"\n\n"+text, comps)
	case strings.HasPrefix(data, "acc:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc:"), 10, 64)
		text, comps := b.accountDetailView(ctx, uid, id)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "rename:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "rename:"), 10, 64)
		if id <= 0 {
			return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
		}
		return b.openModal(ctx, it, discord.NewModal(
			fmt.Sprintf("modal_rename:%d", id), "重命名监控账号", "name", "显示名称",
			fmt.Sprintf("#%d", id), 64,
		))
	case strings.HasPrefix(data, "del_acc:"):
		idStr := strings.TrimPrefix(data, "del_acc:")
		msg := b.delAccount(uid, idStr)
		return b.update(ctx, it, msg+"\n\n"+b.accountsText(uid), b.accountsComponents(uid))
	case strings.HasPrefix(data, "tog_acc:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "tog_acc:"), 10, 64)
		_, _ = b.users.Update(uid, func(p *userstore.Profile) error {
			for i := range p.Accounts {
				if p.Accounts[i].ID == id {
					en := !p.Accounts[i].IsEnabled()
					p.Accounts[i].Enabled = &en
					return nil
				}
			}
			return fmt.Errorf("not found")
		})
		// if coming from detail-ish context, prefer detail when still watched
		if p, ok := b.users.Get(uid); ok {
			for _, a := range p.Accounts {
				if a.ID == id {
					text, comps := b.accountDetailView(ctx, uid, id)
					return b.update(ctx, it, text, comps)
				}
			}
		}
		return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
	case data == "ops_menu":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		return b.update(ctx, it, b.opsMenuText(ctx, uid), b.opsComponents(uid))
	case data == "ops_dash":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.showDashboardView(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "ops_avail":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.showAvailabilityView(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "ops_alerts":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.showAlertsView(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "ops_conc":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.showConcurrencyView(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "ops_channels":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.showChannelsView(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "ops_traf" || strings.HasPrefix(data, "ops_traf:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		win := "5min"
		if strings.HasPrefix(data, "ops_traf:") {
			win = strings.TrimPrefix(data, "ops_traf:")
		}
		text, comps := b.showTrafficView(ctx, uid, win)
		return b.update(ctx, it, text, comps)
	case data == "ops_errors" || strings.HasPrefix(data, "ops_errors:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		kind, page := "all", 0
		if strings.HasPrefix(data, "ops_errors:") {
			rest := strings.TrimPrefix(data, "ops_errors:")
			parts := strings.Split(rest, ":")
			if len(parts) >= 1 && parts[0] != "" {
				kind = parts[0]
			}
			if len(parts) >= 2 {
				page, _ = strconv.Atoi(parts[1])
			}
		}
		text, comps := b.showErrorsView(ctx, uid, kind, page, "")
		return b.update(ctx, it, text, comps)
	case data == "ops_badacc" || strings.HasPrefix(data, "ops_badacc:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		rest := ""
		if strings.HasPrefix(data, "ops_badacc:") {
			rest = strings.TrimPrefix(data, "ops_badacc:")
		}
		kind, page := browse.ParseBadAccCallback(rest)
		text, comps := b.showBadAccountsView(ctx, uid, kind, page, "")
		return b.update(ctx, it, text, comps)
	case data == "ops_watch_errors":
		if !b.canOpsWrite(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		_ = b.respondUpdate(ctx, it, "⏳ 正在添加账号到监控…", nil)
		text, comps := b.watchAccountsByScope(ctx, uid, "error")
		return b.followupEdit(ctx, it, text, comps)
	case strings.HasPrefix(data, "ops_watch:"):
		if !b.canOpsWrite(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		scope := strings.TrimPrefix(data, "ops_watch:")
		_ = b.respondUpdate(ctx, it, "⏳ 正在添加账号到监控…", nil)
		text, comps := b.watchAccountsByScope(ctx, uid, scope)
		return b.followupEdit(ctx, it, text, comps)
	case data == "oe:resolve_all:u":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.resolveAllOpsErrors(ctx, uid, "upstream", "上游")
		return b.update(ctx, it, text, comps)
	case data == "oe:resolve_all:r":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.resolveAllOpsErrors(ctx, uid, "request", "请求")
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "oe:r:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		rest := strings.TrimPrefix(data, "oe:r:")
		kind, idStr, ok := strings.Cut(rest, ":")
		if !ok {
			text, comps := b.showErrors(ctx, uid)
			return b.update(ctx, it, text, comps)
		}
		eid, _ := strconv.ParseInt(idStr, 10, 64)
		text, comps := b.resolveOpsError(ctx, uid, kind, eid)
		return b.update(ctx, it, text, comps)
	case data == "mgr_menu":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.manageMenuView(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "mgr_users" || strings.HasPrefix(data, "mgr_users:") || strings.HasPrefix(data, "mgr_users|"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		page, search := parseUsersCallback(data)
		text, comps := b.showUsersView(ctx, uid, page, search)
		return b.update(ctx, it, text, comps)
	case data == "mgr_user_search":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		return b.openModal(ctx, it, discord.NewModal(
			"modal_user_search", "搜索实例用户", "q", "邮箱/用户名/ID",
			"alice@example.com", 80,
		))
	case data == "mgr_user_clear":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.showUsersView(ctx, uid, 0, "")
		return b.update(ctx, it, text, comps)
	case data == "mgr_ust" || strings.HasPrefix(data, "mgr_ust:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		st := ""
		if strings.HasPrefix(data, "mgr_ust:") {
			st = strings.TrimPrefix(data, "mgr_ust:")
		}
		b.setUserStatus(uid, st)
		text, comps := b.showUsersView(ctx, uid, 0, b.getUserSearch(uid))
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "mgr_user:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "mgr_user:"), 10, 64)
		text, comps := b.showUserDetailView(ctx, uid, id)
		return b.update(ctx, it, text, comps)
	case data == "mgr_groups" || strings.HasPrefix(data, "mgr_groups:") || strings.HasPrefix(data, "mgr_groups|"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		page, search := parseGroupsCallback(data)
		text, comps := b.showGroupsView(ctx, uid, page, search)
		return b.update(ctx, it, text, comps)
	case data == "mgr_group_search":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		return b.openModal(ctx, it, discord.NewModal(
			"modal_group_search", "搜索分组", "q", "名称/平台/ID",
			"openai", 80,
		))
	case data == "mgr_group_clear":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		text, comps := b.showGroupsView(ctx, uid, 0, "")
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "mgr_group:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "mgr_group:"), 10, 64)
		text, comps := b.showGroupDetailView(ctx, uid, id)
		return b.update(ctx, it, text, comps)
	case data == "mgr_browse" || strings.HasPrefix(data, "mgr_browse:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		status, page := "all", 0
		if strings.HasPrefix(data, "mgr_browse:") {
			status, page = browse.ParseCallback(strings.TrimPrefix(data, "mgr_browse:"))
		} else {
			status, page = b.getBrowseView(uid)
		}
		b.setBrowseView(uid, status, page)
		text, comps := b.accountBrowser(ctx, uid, status, page)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "mgr_acc:"):
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "mgr_acc:"), 10, 64)
		text, comps := b.manageAccount(ctx, uid, id, "")
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "mgr_act:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		rest := strings.TrimPrefix(data, "mgr_act:")
		// mgr_act:<action...>:<accountID> (action may contain colons, e.g. temp:15m)
		idx := strings.LastIndex(rest, ":")
		if idx <= 0 {
			text, comps := b.manageMenuView(ctx, uid)
			return b.update(ctx, it, text, comps)
		}
		action := rest[:idx]
		id, _ := strconv.ParseInt(rest[idx+1:], 10, 64)
		if action == "temp_menu" {
			return b.update(ctx, it, fmt.Sprintf("选择账号 #%d 临时停调度时长：", id), tempMenuComponents(id))
		}
		if action == "temp_custom" {
			return b.openModal(ctx, it, discord.NewModal(
				fmt.Sprintf("modal_temp:%d", id), "临时停调度时长", "dur", "时长 如 30m/2h/1d",
				"30m", 16,
			))
		}
		notice := b.doManageAction(ctx, uid, action, id)
		if action == "confirm_unsched" || action == "confirm_disable" || action == "confirm_reset_quota" {
			return b.update(ctx, it, notice, confirmComponents(action, id))
		}
		text, comps := b.manageAccount(ctx, uid, id, notice)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "acc_live:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_live:"), 10, 64)
		text, comps := b.showAccountLive(ctx, uid, id, "")
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "live_act:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		rest := strings.TrimPrefix(data, "live_act:")
		action, idStr, ok := strings.Cut(rest, ":")
		if !ok {
			text, comps := b.manageMenuView(ctx, uid)
			return b.update(ctx, it, text, comps)
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		text, comps := b.handleLiveAction(ctx, uid, action, id)
		return b.update(ctx, it, text, comps)
	case data == "mgr_bulk_clear":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.bulkActionPrompt(ctx, uid, "clear_err", "批量清错", "mgr_bulk_clear_go")
		return b.update(ctx, it, text, comps)
	case data == "mgr_bulk_clear_go":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		_ = b.update(ctx, it, "⏳ 批量清错处理中…", nil)
		text, comps := b.bulkAccountActionExecute(ctx, uid, "clear_err")
		return b.followupEdit(ctx, it, text, comps)
	case data == "mgr_bulk_recover":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.bulkActionPrompt(ctx, uid, "recover", "批量恢复", "mgr_bulk_recover_go")
		return b.update(ctx, it, text, comps)
	case data == "mgr_bulk_recover_go":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		_ = b.update(ctx, it, "⏳ 批量恢复处理中…", nil)
		text, comps := b.bulkAccountActionExecute(ctx, uid, "recover")
		return b.followupEdit(ctx, it, text, comps)
	case data == "mgr_bulk_sched_on":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.bulkActionPrompt(ctx, uid, "sched_on", "批量开调度", "mgr_bulk_sched_on_go")
		return b.update(ctx, it, text, comps)
	case data == "mgr_bulk_sched_on_go":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		_ = b.update(ctx, it, "⏳ 批量开调度处理中…", nil)
		text, comps := b.bulkAccountActionExecute(ctx, uid, "sched_on")
		return b.followupEdit(ctx, it, text, comps)
	case data == "mgr_bulk_clear_rl":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.bulkActionPrompt(ctx, uid, "clear_rl", "批量清限速", "mgr_bulk_clear_rl_go")
		return b.update(ctx, it, text, comps)
	case data == "mgr_bulk_clear_rl_go":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		_ = b.update(ctx, it, "⏳ 批量清限速处理中…", nil)
		text, comps := b.bulkAccountActionExecute(ctx, uid, "clear_rl")
		return b.followupEdit(ctx, it, text, comps)
	case data == "mgr_bulk_heal":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.bulkActionPrompt(ctx, uid, "heal", "批量一键修复", "mgr_bulk_heal_go")
		return b.update(ctx, it, text, comps)
	case data == "mgr_bulk_heal_go":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		_ = b.update(ctx, it, "⏳ 批量一键修复处理中…", nil)
		text, comps := b.bulkAccountActionExecute(ctx, uid, "heal")
		return b.followupEdit(ctx, it, text, comps)
	case data == "mgr_search":
		if !b.canOpsRead(uid) {
			return b.update(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid))
		}
		return b.openModal(ctx, it, discord.NewModal(
			"modal_search", "搜索账号", "q", "关键词",
			"名称 / 邮箱片段", 100,
		))
	case data == "pnl_users" || strings.HasPrefix(data, "pnl_users:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		page := 0
		if strings.HasPrefix(data, "pnl_users:") {
			page, _ = strconv.Atoi(strings.TrimPrefix(data, "pnl_users:"))
		}
		text, comps := b.showPanelUsers(uid, page, "")
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "pnl_user:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "pnl_user:"), 10, 64)
		text, comps := b.showPanelUserDetail(uid, id, "")
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "pnl_role:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		rest := strings.TrimPrefix(data, "pnl_role:")
		role, idStr, ok := strings.Cut(rest, ":")
		if !ok {
			text, comps := b.showPanelUsers(uid, 0, "")
			return b.update(ctx, it, text, comps)
		}
		tid, _ := strconv.ParseInt(idStr, 10, 64)
		text, comps := b.setPanelUserRole(uid, tid, role)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "pnl_mon:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		tid, _ := strconv.ParseInt(strings.TrimPrefix(data, "pnl_mon:"), 10, 64)
		text, comps := b.togglePanelUserMonitor(uid, tid)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "pnl_src:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		tid, _ := strconv.ParseInt(strings.TrimPrefix(data, "pnl_src:"), 10, 64)
		text, comps := b.togglePanelUserSource(uid, tid)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "acc_thr:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr:"), 10, 64)
		text, comps := b.accountThresholdsView(uid, id)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "acc_thr_add:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr_add:"), 10, 64)
		return b.update(ctx, it, fmt.Sprintf("账号 #%d — 选择窗口，再输入自定义百分比（或用快捷预设）：", id), thrWindowPickComponentsForAccount(id))
	case strings.HasPrefix(data, "acc_thr_presets:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr_presets:"), 10, 64)
		return b.update(ctx, it, fmt.Sprintf("账号 #%d — 快捷预设：", id), thrWindowComponentsForAccount(id))
	case strings.HasPrefix(data, "acc_thr_win:"):
		rest := strings.TrimPrefix(data, "acc_thr_win:")
		idStr, win, ok := strings.Cut(rest, ":")
		if !ok {
			return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		win = sub2api.NormalizeWindow(win)
		title := "账号阈值 %"
		if len(win) > 0 {
			title = truncate(win, 20) + " %"
		}
		return b.openModal(ctx, it, discord.NewModal(
			fmt.Sprintf("modal_acc_thr:%d:%s", id, win), title, "pct", "阈值百分比 1-100",
			"80", 8,
		))
	case strings.HasPrefix(data, "acc_thr_set:"):
		// acc_thr_set:id:window:pct
		rest := strings.TrimPrefix(data, "acc_thr_set:")
		parts := strings.Split(rest, ":")
		if len(parts) < 3 {
			return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
		}
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		win := strings.Join(parts[1:len(parts)-1], ":")
		pct, _ := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err := b.setAccountThreshold(uid, id, win, pct, "P2"); err != nil {
			text, comps := b.accountThresholdsView(uid, id)
			return b.update(ctx, it, "❌ "+err.Error()+"\n\n"+text, comps)
		}
		text, comps := b.accountThresholdsView(uid, id)
		return b.update(ctx, it, fmt.Sprintf("✅ 已设置 `%s` ≥ `%.0f%%`\n\n", win, pct)+text, comps)
	case strings.HasPrefix(data, "acc_thr_del:"):
		rest := strings.TrimPrefix(data, "acc_thr_del:")
		idStr, win, ok := strings.Cut(rest, ":")
		if !ok {
			return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		_ = b.deleteAccountThreshold(uid, id, win)
		text, comps := b.accountThresholdsView(uid, id)
		return b.update(ctx, it, "✅ 已删除\n\n"+text, comps)
	case strings.HasPrefix(data, "acc_thr_clear:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr_clear:"), 10, 64)
		_ = b.clearAccountThresholds(uid, id)
		text, comps := b.accountThresholdsView(uid, id)
		return b.update(ctx, it, "✅ 已清除账号专属阈值\n\n"+text, comps)
	case strings.HasPrefix(data, "acc_thr_copy:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr_copy:"), 10, 64)
		_ = b.copyDefaultsToAccount(uid, id)
		text, comps := b.accountThresholdsView(uid, id)
		return b.update(ctx, it, "✅ 已复制默认阈值到该账号\n\n"+text, comps)
	case data == "thr_add":
		return b.update(ctx, it, "选择窗口后输入自定义百分比，或用「快捷预设」一键写入：", thrWindowPickComponents())
	case data == "thr_presets":
		return b.update(ctx, it, "快捷预设（窗口 + 百分比）：", thrWindowComponents())
	case strings.HasPrefix(data, "thr_win:"):
		win := sub2api.NormalizeWindow(strings.TrimPrefix(data, "thr_win:"))
		if win == "" {
			return b.update(ctx, it, b.thresholdsText(uid), b.thrComponents(uid))
		}
		title := truncate(win, 20) + " %"
		return b.openModal(ctx, it, discord.NewModal(
			"modal_thr:"+win, title, "pct", "阈值百分比 1-100",
			"80", 8,
		))
	case strings.HasPrefix(data, "thr_set:"):
		// thr_set:window:pct
		rest := strings.TrimPrefix(data, "thr_set:")
		win, pctStr, ok := strings.Cut(rest, ":")
		if !ok {
			return b.update(ctx, it, b.thresholdsText(uid), b.thrComponents(uid))
		}
		pct, _ := strconv.ParseFloat(pctStr, 64)
		_ = b.setThreshold(uid, win, pct, "P2")
		return b.update(ctx, it, "✅ 已设置\n\n"+b.thresholdsText(uid), b.thrComponents(uid))
	case strings.HasPrefix(data, "thr_del:"):
		win := strings.TrimPrefix(data, "thr_del:")
		_ = b.deleteThreshold(uid, win)
		return b.update(ctx, it, "✅ 已删除\n\n"+b.thresholdsText(uid), b.thrComponents(uid))
	case data == "thr_apply_defs":
		_, _ = b.users.Update(uid, func(p *userstore.Profile) error {
			p.Thresholds = append([]config.UsageThreshold(nil), b.defaults...)
			return nil
		})
		return b.update(ctx, it, "✅ 已写入系统默认阈值\n\n"+b.thresholdsText(uid), b.thrComponents(uid))
	case data == "thr_reset":
		_, _ = b.users.Update(uid, func(p *userstore.Profile) error {
			p.Thresholds = nil
			return nil
		})
		return b.update(ctx, it, "✅ 已重置为系统默认\n\n"+b.thresholdsText(uid), b.thrComponents(uid))
	default:
		return b.update(ctx, it, "未知操作: "+data, b.homeComponents(uid))
	}
}

func (b *Bot) openModal(ctx context.Context, it *discord.Interaction, modal discord.Modal) error {
	if b.dc == nil {
		return fmt.Errorf("discord client nil")
	}
	return b.dc.RespondModal(ctx, it.ID, it.Token, modal)
}

func (b *Bot) handleModal(ctx context.Context, it *discord.Interaction, uid int64, user *discord.User) error {
	if it.Data == nil {
		return nil
	}
	switch it.Data.CustomID {
	case "modal_search":
		if !b.canOpsRead(uid) {
			return b.respond(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid), true)
		}
		q := strings.TrimSpace(it.ModalValue("q"))
		if q == "" {
			return b.respond(ctx, it, "关键词不能为空。可再点「搜索」重试，或使用 `/search q:<关键词>`。", manageComponents(), true)
		}
		text, comps := b.accountBrowser(ctx, uid, "search:"+q, 0)
		return b.respond(ctx, it, text, comps, false)
	case "modal_user_search":
		if !b.canOpsRead(uid) {
			return b.respond(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid), true)
		}
		q := strings.TrimSpace(it.ModalValue("q"))
		if q == "" {
			return b.respond(ctx, it, "关键词不能为空。可再点「搜索用户」重试。", manageComponents(), true)
		}
		text, comps := b.showUsersView(ctx, uid, 0, q)
		return b.respond(ctx, it, text, comps, false)
	case "modal_group_search":
		if !b.canOpsRead(uid) {
			return b.respond(ctx, it, "⛔ 需要运维查看权限", b.homeComponents(uid), true)
		}
		q := strings.TrimSpace(it.ModalValue("q"))
		if q == "" {
			return b.respond(ctx, it, "关键词不能为空。可再点「搜索分组」重试。", manageComponents(), true)
		}
		text, comps := b.showGroupsView(ctx, uid, 0, q)
		return b.respond(ctx, it, text, comps, false)
	case "modal_base":
		url := strings.TrimSpace(it.ModalValue("base_url"))
		msg := b.setBaseURL(uid, url)
		return b.respond(ctx, it, msg+"\n\n"+b.connText(uid), b.connComponents(uid), false)
	case "modal_key":
		key := strings.TrimSpace(it.ModalValue("api_key"))
		msg := b.setAPIKey(uid, key)
		// ephemeral for secrets
		return b.respond(ctx, it, msg+"\n\n"+b.connText(uid), b.connComponents(uid), true)
	case "modal_addacc":
		idRaw := strings.TrimSpace(it.ModalValue("account_id"))
		msg := b.addAccount(ctx, uid, idRaw)
		return b.respond(ctx, it, msg+"\n\n"+b.accountsText(uid), b.accountsComponents(uid), false)
	default:
		if strings.HasPrefix(it.Data.CustomID, "modal_thr:") {
			win := sub2api.NormalizeWindow(strings.TrimPrefix(it.Data.CustomID, "modal_thr:"))
			pct, err := parsePct(it.ModalValue("pct"))
			if err != nil {
				return b.respond(ctx, it, "❌ "+err.Error()+"\n请输入 1-100 的数字。", b.thrComponents(uid), true)
			}
			if err := b.setThreshold(uid, win, pct, ""); err != nil {
				return b.respond(ctx, it, "❌ "+err.Error(), b.thrComponents(uid), true)
			}
			return b.respond(ctx, it, fmt.Sprintf("✅ 已设置 `%s` ≥ `%.0f%%`\n\n", win, pct)+b.thresholdsText(uid), b.thrComponents(uid), false)
		}
		if strings.HasPrefix(it.Data.CustomID, "modal_acc_thr:") {
			rest := strings.TrimPrefix(it.Data.CustomID, "modal_acc_thr:")
			idStr, win, ok := strings.Cut(rest, ":")
			if !ok {
				return b.respond(ctx, it, "参数无效", b.accountsComponents(uid), true)
			}
			id, _ := strconv.ParseInt(idStr, 10, 64)
			win = sub2api.NormalizeWindow(win)
			pct, err := parsePct(it.ModalValue("pct"))
			if err != nil {
				text, comps := b.accountThresholdsView(uid, id)
				return b.respond(ctx, it, "❌ "+err.Error()+"\n\n"+text, comps, true)
			}
			if err := b.setAccountThreshold(uid, id, win, pct, ""); err != nil {
				text, comps := b.accountThresholdsView(uid, id)
				return b.respond(ctx, it, "❌ "+err.Error()+"\n\n"+text, comps, true)
			}
			text, comps := b.accountThresholdsView(uid, id)
			return b.respond(ctx, it, fmt.Sprintf("✅ 已设置 `%s` ≥ `%.0f%%`\n\n", win, pct)+text, comps, false)
		}
		if strings.HasPrefix(it.Data.CustomID, "modal_temp:") {
			if !b.isAdmin(uid) {
				return b.respond(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid), true)
			}
			id, _ := strconv.ParseInt(strings.TrimPrefix(it.Data.CustomID, "modal_temp:"), 10, 64)
			sec, label, err := parseFlexibleDuration(it.ModalValue("dur"))
			if err != nil {
				return b.respond(ctx, it, "❌ "+err.Error()+"\n示例: `30m` / `2h` / `1d` / `90`", tempMenuComponents(id), true)
			}
			_ = sec
			notice := b.doManageAction(ctx, uid, "temp:"+label, id)
			text, comps := b.manageAccount(ctx, uid, id, notice)
			return b.respond(ctx, it, text, comps, false)
		}
		if strings.HasPrefix(it.Data.CustomID, "modal_rename:") {

			id, _ := strconv.ParseInt(strings.TrimPrefix(it.Data.CustomID, "modal_rename:"), 10, 64)
			name := strings.TrimSpace(it.ModalValue("name"))
			msg := b.renameWatchAccount(uid, id, name)
			if strings.HasPrefix(msg, "✅") {
				text, comps := b.accountDetailView(ctx, uid, id)
				return b.respond(ctx, it, msg+"\n\n"+text, comps, false)
			}
			return b.respond(ctx, it, msg, b.accountsComponents(uid), true)
		}
		return b.respond(ctx, it, "未知表单: "+it.Data.CustomID, b.homeComponents(uid), true)
	}
}

func (b *Bot) respond(ctx context.Context, it *discord.Interaction, content string, comps []discord.Component, ephemeral bool) error {
	return b.dc.RespondInteraction(ctx, it.ID, it.Token, 4, content, comps, ephemeral)
}

func (b *Bot) update(ctx context.Context, it *discord.Interaction, content string, comps []discord.Component) error {
	// type 7 = UPDATE_MESSAGE for components
	return b.dc.RespondInteraction(ctx, it.ID, it.Token, 7, content, comps, false)
}

func (b *Bot) respondUpdate(ctx context.Context, it *discord.Interaction, content string, comps []discord.Component) error {
	return b.dc.RespondInteraction(ctx, it.ID, it.Token, 7, content, comps, false)
}

func (b *Bot) followupEdit(ctx context.Context, it *discord.Interaction, content string, comps []discord.Component) error {
	appID, err := b.dc.ApplicationID(ctx)
	if err != nil {
		return err
	}
	return b.dc.UpdateInteraction(ctx, appID, it.Token, content, comps)
}

func parsePct(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("百分比不能为空")
	}
	pct, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("百分比格式无效")
	}
	if pct <= 0 || pct > 100 {
		return 0, fmt.Errorf("百分比需在 1-100")
	}
	return pct, nil
}

func optionString(it *discord.Interaction, name string) string {
	if it.Data == nil {
		return ""
	}
	for _, o := range it.Data.Options {
		if o.Name == name {
			var s string
			_ = json.Unmarshal(o.Value, &s)
			return s
		}
	}
	return ""
}

func optionInt(it *discord.Interaction, name string) int64 {
	if it.Data == nil {
		return 0
	}
	for _, o := range it.Data.Options {
		if o.Name == name {
			var n float64
			if err := json.Unmarshal(o.Value, &n); err == nil {
				return int64(n)
			}
			var s string
			if err := json.Unmarshal(o.Value, &s); err == nil {
				v, _ := strconv.ParseInt(s, 10, 64)
				return v
			}
		}
	}
	return 0
}

func (b *Bot) panelCfg() config.DiscordPanelConfig {
	return b.cfg.Discord.Panel
}

func (b *Bot) allowed(userID int64) bool {
	if b.isAdmin(userID) {
		return true
	}
	pc := b.panelCfg()
	if len(pc.AllowUserIDs) > 0 {
		for _, id := range pc.AllowUserIDs {
			if id == userID {
				return true
			}
		}
		return false
	}
	if pc.AllowAll || pc.OpenRegistration {
		return true
	}
	return false
}

func (b *Bot) isAdmin(userID int64) bool {
	if p, ok := b.users.Get(userID); ok {
		switch p.EffectiveRole() {
		case userstore.RoleAdmin:
			return true
		case userstore.RoleViewer, userstore.RoleUser:
			return false
		}
	}
	for _, id := range b.panelCfg().AdminUserIDs {
		if id == userID {
			return true
		}
	}
	// fallback: if no admin list, nobody is admin unless role set — avoid open admin
	// (unlike telegram chat_id fallback; discord uses explicit admin_user_ids)
	return false
}

// isViewer reports explicit profile.role=viewer (not admin).
func (b *Bot) isViewer(userID int64) bool {
	if b.isAdmin(userID) {
		return false
	}
	if p, ok := b.users.Get(userID); ok {
		return p.EffectiveRole() == userstore.RoleViewer
	}
	return false
}

func (b *Bot) canOpsRead(userID int64) bool {
	return b.isAdmin(userID) || b.isViewer(userID)
}

func (b *Bot) canOpsWrite(userID int64) bool {
	return b.isAdmin(userID)
}

func (b *Bot) roleLabel(userID int64) string {
	if b.isAdmin(userID) {
		return "管理员"
	}
	if b.isViewer(userID) {
		return "只读运维"
	}
	return "用户"
}

func (b *Bot) setAwait(userID int64, kind string, accountID int64, window string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		s = &session{}
		b.sessions[userID] = s
	}
	s.Await = kind
	s.UpdatedAt = time.Now()
	s.AccountID = accountID
	s.Window = window
}

func (b *Bot) setOpsErrorView(userID int64, kind string, page int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		s = &session{}
		b.sessions[userID] = s
	}
	s.OpsErrorKind = kind
	s.OpsErrorPage = page
	s.UpdatedAt = time.Now()
}

func (b *Bot) getOpsErrorView(userID int64) (kind string, page int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		return "all", 0
	}
	kind = s.OpsErrorKind
	page = s.OpsErrorPage
	if kind == "" {
		kind = "all"
	}
	if page < 0 {
		page = 0
	}
	return kind, page
}

func (b *Bot) setUserSearch(userID int64, search string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		s = &session{}
		b.sessions[userID] = s
	}
	s.UserSearch = strings.TrimSpace(search)
	s.UpdatedAt = time.Now()
}

func (b *Bot) getUserSearch(userID int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		return ""
	}
	return s.UserSearch
}

func (b *Bot) setUserStatus(userID int64, status string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		s = &session{}
		b.sessions[userID] = s
	}
	s.UserStatus = strings.ToLower(strings.TrimSpace(status))
	s.UpdatedAt = time.Now()
}

func (b *Bot) getUserStatus(userID int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		return ""
	}
	return s.UserStatus
}

func (b *Bot) setGroupSearch(userID int64, search string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		s = &session{}
		b.sessions[userID] = s
	}
	s.GroupSearch = strings.TrimSpace(search)
	s.UpdatedAt = time.Now()
}

func (b *Bot) getGroupSearch(userID int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		return ""
	}
	return s.GroupSearch
}

// usersCallback / parseUsersCallback keep search across pages (shared with TG forms).
func usersCallback(page int, search string) string {
	search = strings.TrimSpace(search)
	if search == "" {
		if page <= 0 {
			return "mgr_users"
		}
		return fmt.Sprintf("mgr_users:%d", page)
	}
	safe := strings.ReplaceAll(search, ":", " ")
	safe = strings.ReplaceAll(safe, "|", " ")
	return fmt.Sprintf("mgr_users|%s:%d", safe, page)
}

func parseUsersCallback(data string) (page int, search string) {
	data = strings.TrimSpace(data)
	if data == "mgr_users" {
		return 0, ""
	}
	if strings.HasPrefix(data, "mgr_users|") {
		rest := strings.TrimPrefix(data, "mgr_users|")
		if i := strings.LastIndex(rest, ":"); i >= 0 {
			search = strings.TrimSpace(rest[:i])
			page, _ = strconv.Atoi(rest[i+1:])
			return page, search
		}
		return 0, strings.TrimSpace(rest)
	}
	if strings.HasPrefix(data, "mgr_users:") {
		page, _ = strconv.Atoi(strings.TrimPrefix(data, "mgr_users:"))
		return page, ""
	}
	return 0, ""
}

func groupsCallback(page int, search string) string {
	search = strings.TrimSpace(search)
	if search == "" {
		if page <= 0 {
			return "mgr_groups"
		}
		return fmt.Sprintf("mgr_groups:%d", page)
	}
	safe := strings.ReplaceAll(search, ":", " ")
	safe = strings.ReplaceAll(safe, "|", " ")
	return fmt.Sprintf("mgr_groups|%s:%d", safe, page)
}

func parseGroupsCallback(data string) (page int, search string) {
	data = strings.TrimSpace(data)
	if data == "mgr_groups" {
		return 0, ""
	}
	if strings.HasPrefix(data, "mgr_groups|") {
		rest := strings.TrimPrefix(data, "mgr_groups|")
		if i := strings.LastIndex(rest, ":"); i >= 0 {
			search = strings.TrimSpace(rest[:i])
			page, _ = strconv.Atoi(rest[i+1:])
			return page, search
		}
		return 0, strings.TrimSpace(rest)
	}
	if strings.HasPrefix(data, "mgr_groups:") {
		page, _ = strconv.Atoi(strings.TrimPrefix(data, "mgr_groups:"))
		return page, ""
	}
	return 0, ""
}

func (b *Bot) setManageBack(userID int64, data string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		s = &session{}
		b.sessions[userID] = s
	}
	s.ManageBack = data
	s.UpdatedAt = time.Now()
}

func (b *Bot) getManageBack(userID int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil || s.ManageBack == "" {
		return "mgr_menu"
	}
	return s.ManageBack
}

func (b *Bot) setBrowseView(userID int64, status string, page int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil {
		s = &session{}
		b.sessions[userID] = s
	}
	if status == "" {
		status = "all"
	}
	if page < 0 {
		page = 0
	}
	s.BrowseStatus = status
	s.BrowsePage = page
	s.ManageBack = fmt.Sprintf("mgr_browse:%s:%d", browse.Token(status), page)
	s.UpdatedAt = time.Now()
}

func (b *Bot) getBrowseView(userID int64) (status string, page int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[userID]
	if s == nil || s.BrowseStatus == "" {
		return "all", 0
	}
	return s.BrowseStatus, s.BrowsePage
}

func (b *Bot) manageBackLabel(userID int64) (label, data string) {
	data = b.getManageBack(userID)
	label = "« 返回"
	switch {
	case data == "mgr_menu":
		label = "« 管理"
	case data == "ops_menu":
		label = "« 运维"
	case data == "ops_avail":
		label = "« 可用性"
	case data == "ops_conc":
		label = "« 并发"
	case data == "ops_dash":
		label = "« 看板"
	case data == "ops_alerts":
		label = "« 告警"
	case data == "ops_channels":
		label = "« 渠道"
	case data == "ops_traf" || strings.HasPrefix(data, "ops_traf:"):
		label = "« 流量"
	case strings.HasPrefix(data, "ops_badacc"):
		label = "« 异常账号"
	case strings.HasPrefix(data, "mgr_browse"):
		label = "« 浏览"
	case strings.HasPrefix(data, "ops_errors"):
		label = "« 错误"
	case data == "mgr_users" || strings.HasPrefix(data, "mgr_users:") || strings.HasPrefix(data, "mgr_users|"):
		label = "« 实例用户"
	case strings.HasPrefix(data, "mgr_user:"):
		label = "« 用户详情"
	case data == "mgr_groups" || strings.HasPrefix(data, "mgr_groups:") || strings.HasPrefix(data, "mgr_groups|"):
		label = "« 分组"
	case strings.HasPrefix(data, "mgr_group:"):
		label = "« 分组详情"
	}
	return label, data
}

func (b *Bot) renameWatchAccount(userID, id int64, name string) string {
	name = strings.TrimSpace(name)
	if id <= 0 {
		return "账号 ID 无效"
	}
	if name == "" {
		return "名称不能为空"
	}
	if len([]rune(name)) > 64 {
		name = string([]rune(name)[:64])
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		for i := range p.Accounts {
			if p.Accounts[i].ID == id {
				p.Accounts[i].Name = name
				return nil
			}
		}
		return fmt.Errorf("账号 #%d 不在监控列表", id)
	})
	if err != nil {
		return "重命名失败: " + err.Error()
	}
	return fmt.Sprintf("✅ 已重命名 #%d → `%s`", id, name)
}

func (b *Bot) userClient(userID int64, timeout time.Duration) (*sub2api.Client, *userstore.Profile, error) {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() {
		return nil, p, fmt.Errorf("请先配置连接（/setbase + /setkey）")
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL:     p.BaseURL,
		AdminAPIKey: p.AdminAPIKey,
		JWT:         p.JWT,
		Timeout:     timeout,
	})
	if err != nil {
		return nil, p, err
	}
	return cli, p, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func schedLabel(v bool) string {
	if v {
		return "调度开"
	}
	return "调度关"
}
