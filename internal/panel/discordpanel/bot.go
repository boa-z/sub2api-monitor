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
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

const (
	awaitNone    = ""
	awaitBaseURL = "base_url"
	awaitAPIKey  = "api_key"
	awaitAddAcc  = "add_account"
	awaitThrPct  = "set_threshold_pct"
	awaitSearch  = "search_account"
	awaitRename  = "rename_account"
)

type session struct {
	Await     string
	UpdatedAt time.Time
	AccountID int64
	Window    string
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
	case "panel", "status":
		return b.respond(ctx, it, b.homeText(uid), b.homeComponents(uid), false)
	case "help":
		return b.respond(ctx, it, helpText(), b.homeComponents(uid), false)
	case "check":
		_ = b.respond(ctx, it, "⏳ 正在检查用量…", nil, false)
		msg := b.forceCheck(ctx, uid)
		return b.followupEdit(ctx, it, msg, b.homeComponents(uid))
	case "ops":
		if !b.isAdmin(uid) {
			return b.respond(ctx, it, "⛔ 运维视图仅管理员可用。", b.homeComponents(uid), true)
		}
		return b.respond(ctx, it, opsMenuText(), opsComponents(), false)
	case "manage":
		if !b.isAdmin(uid) {
			return b.respond(ctx, it, "⛔ 账号管理仅管理员可用。", b.homeComponents(uid), true)
		}
		return b.respond(ctx, it, manageMenuText(), manageComponents(), false)
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
	case data == "home" || data == "status":
		return b.update(ctx, it, b.homeText(uid), b.homeComponents(uid))
	case data == "cfg_conn":
		return b.update(ctx, it, b.connText(uid), b.connComponents(uid))
	case data == "cfg_acc":
		return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
	case data == "cfg_thr":
		return b.update(ctx, it, b.thresholdsText(uid), thrComponents())
	case data == "help":
		return b.update(ctx, it, helpText(), b.homeComponents(uid))
	case data == "check_now":
		_ = b.respondUpdate(ctx, it, "⏳ 正在检查用量…", nil)
		msg := b.forceCheck(ctx, uid)
		return b.followupEdit(ctx, it, msg, b.homeComponents(uid))
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
		b.setAwait(uid, awaitBaseURL, 0, "")
		return b.update(ctx, it, "请使用 `/setbase url:<Base URL>` 设置，例如 `/setbase url:http://127.0.0.1:8080`", b.connComponents(uid))
	case data == "set_key_prompt":
		b.setAwait(uid, awaitAPIKey, 0, "")
		return b.update(ctx, it, "请使用 `/setkey key:<API Key>` 设置（建议在私密频道/DM）。", b.connComponents(uid))
	case data == "add_acc_prompt":
		return b.update(ctx, it, "请使用 `/addaccount id:<账号ID>` 添加监控账号。", b.accountsComponents(uid))
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
		return b.update(ctx, it, b.accountsText(uid), b.accountsComponents(uid))
	case data == "ops_menu":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		return b.update(ctx, it, opsMenuText(), opsComponents())
	case data == "ops_dash":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		return b.update(ctx, it, b.showDashboard(ctx, uid), opsComponents())
	case data == "ops_avail":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		return b.update(ctx, it, b.showAvailability(ctx, uid), opsComponents())
	case data == "ops_alerts":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		return b.update(ctx, it, b.showAlerts(ctx, uid), opsComponents())
	case data == "ops_errors":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.showErrors(ctx, uid)
		return b.update(ctx, it, text, comps)
	case data == "ops_badacc":
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		text, comps := b.showBadAccounts(ctx, uid)
		return b.update(ctx, it, text, comps)
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
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		return b.update(ctx, it, manageMenuText(), manageComponents())
	case data == "mgr_browse" || strings.HasPrefix(data, "mgr_browse:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		status, page := "all", 0
		if strings.HasPrefix(data, "mgr_browse:") {
			rest := strings.TrimPrefix(data, "mgr_browse:")
			parts := strings.Split(rest, ":")
			if len(parts) >= 1 && parts[0] != "" {
				status = parts[0]
			}
			if len(parts) >= 2 {
				page, _ = strconv.Atoi(parts[1])
			}
		}
		text, comps := b.accountBrowser(ctx, uid, status, page)
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "mgr_acc:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "mgr_acc:"), 10, 64)
		text, comps := b.manageAccount(ctx, uid, id, "")
		return b.update(ctx, it, text, comps)
	case strings.HasPrefix(data, "mgr_act:"):
		if !b.isAdmin(uid) {
			return b.update(ctx, it, "⛔ 需要管理员权限", b.homeComponents(uid))
		}
		rest := strings.TrimPrefix(data, "mgr_act:")
		action, idStr, ok := strings.Cut(rest, ":")
		if !ok {
			return b.update(ctx, it, manageMenuText(), manageComponents())
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		notice := b.doManageAction(ctx, uid, action, id)
		if action == "confirm_unsched" || action == "confirm_disable" {
			// notice is confirmation UI text handled inside
			return b.update(ctx, it, notice, confirmComponents(action, id))
		}
		text, comps := b.manageAccount(ctx, uid, id, notice)
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
		text, comps := b.bulkAccountActionExecute(ctx, uid, "clear_err")
		return b.update(ctx, it, text, comps)
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
		text, comps := b.bulkAccountActionExecute(ctx, uid, "recover")
		return b.update(ctx, it, text, comps)
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
		text, comps := b.bulkAccountActionExecute(ctx, uid, "sched_on")
		return b.update(ctx, it, text, comps)
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
		text, comps := b.bulkAccountActionExecute(ctx, uid, "clear_rl")
		return b.update(ctx, it, text, comps)
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
		text, comps := b.bulkAccountActionExecute(ctx, uid, "heal")
		return b.update(ctx, it, text, comps)
	case data == "thr_add":
		return b.update(ctx, it, "选择窗口后使用固定阈值，或之后可在配置中细化：", thrWindowComponents())
	case strings.HasPrefix(data, "thr_set:"):
		// thr_set:window:pct
		rest := strings.TrimPrefix(data, "thr_set:")
		win, pctStr, ok := strings.Cut(rest, ":")
		if !ok {
			return b.update(ctx, it, b.thresholdsText(uid), thrComponents())
		}
		pct, _ := strconv.ParseFloat(pctStr, 64)
		_ = b.setThreshold(uid, win, pct, "P2")
		return b.update(ctx, it, "✅ 已设置\n\n"+b.thresholdsText(uid), thrComponents())
	case strings.HasPrefix(data, "thr_del:"):
		win := strings.TrimPrefix(data, "thr_del:")
		_ = b.deleteThreshold(uid, win)
		return b.update(ctx, it, "✅ 已删除\n\n"+b.thresholdsText(uid), thrComponents())
	case data == "thr_reset":
		_, _ = b.users.Update(uid, func(p *userstore.Profile) error {
			p.Thresholds = nil
			return nil
		})
		return b.update(ctx, it, "✅ 已重置为系统默认\n\n"+b.thresholdsText(uid), thrComponents())
	default:
		return b.update(ctx, it, "未知操作: "+data, b.homeComponents(uid))
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
		case userstore.RoleUser:
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

func (b *Bot) roleLabel(userID int64) string {
	if b.isAdmin(userID) {
		return "管理员"
	}
	return "用户"
}

func (b *Bot) setAwait(userID int64, kind string, accountID int64, window string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[userID] = &session{Await: kind, UpdatedAt: time.Now(), AccountID: accountID, Window: window}
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
