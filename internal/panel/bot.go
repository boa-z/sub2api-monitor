package panel

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/panel/browse"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

// pending input kinds for multi-step wizards
const (
	awaitNone    = ""
	awaitBaseURL = "base_url"
	awaitAPIKey  = "api_key"
	awaitAddAcc  = "add_account"
	awaitThrPct  = "set_threshold_pct" // window stored in session.Window
	awaitRename  = "rename_account"    // account id in session.AccountID
	awaitSearch  = "search_account"
)

type session struct {
	Await     string
	UpdatedAt time.Time
	AccountID int64
	Window    string
	Page      int
	// OpsErrorKind/Page remember last errors view for resolve refresh.
	OpsErrorKind string
	OpsErrorPage int
	// ManageBack is callback_data to return from account manage (e.g. mgr_browse:error:1).
	ManageBack string
	// BrowseStatus/Page remember last account browser filter.
	BrowseStatus string
	BrowsePage   int
}

// Bot is the interactive Telegram control panel.
type Bot struct {
	tg       *telegram.Client
	users    *userstore.Store
	cfg      *config.Config
	logger   *slog.Logger
	defaults []config.UsageThreshold

	mu       sync.Mutex
	sessions map[int64]*session // telegram user id -> pending input
	offset   int64
}

func New(tg *telegram.Client, users *userstore.Store, cfg *config.Config, logger *slog.Logger) *Bot {
	defs := cfg.Checks.AccountUsage.DefaultThresholds
	if len(defs) == 0 {
		defs = []config.UsageThreshold{
			{Window: "five_hour", UtilizationGTE: 80, Severity: "P2"},
			{Window: "seven_day", UtilizationGTE: 90, Severity: "P1"},
		}
	}
	return &Bot{
		tg:       tg,
		users:    users,
		cfg:      cfg,
		logger:   logger,
		defaults: defs,
		sessions: make(map[int64]*session),
	}
}

// Run long-polls Telegram updates until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.tg.DeleteWebhook(ctx); err != nil {
		b.logger.Warn("deleteWebhook", "err", err)
	}
	if err := b.tg.SetMyCommands(ctx, defaultBotCommands()); err != nil {
		b.logger.Warn("setMyCommands", "err", err)
	}
	b.logger.Info("panel bot listening for updates")
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updates, err := b.tg.GetUpdates(ctx, b.offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.logger.Warn("getUpdates", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= b.offset {
				b.offset = u.UpdateID + 1
			}
			if err := b.handleUpdate(ctx, u); err != nil {
				b.logger.Warn("handle update", "err", err, "update_id", u.UpdateID)
			}
		}
	}
}

func defaultBotCommands() []telegram.BotCommand {
	return []telegram.BotCommand{
		{Command: "start", Description: "打开主面板"},
		{Command: "status", Description: "查看配置摘要"},
		{Command: "ops", Description: "运维视图（看板/告警/错误）"},
		{Command: "manage", Description: "账号管理（调度/清错/刷新）"},
		{Command: "search", Description: "搜索账号"},
		{Command: "check", Description: "立即检查用量"},
		{Command: "setbase", Description: "设置 Base URL"},
		{Command: "setkey", Description: "设置 Admin API Key"},
		{Command: "addaccount", Description: "添加监控账号 ID"},
		{Command: "thresholds", Description: "查看/管理阈值"},
		{Command: "id", Description: "显示你的 Telegram ID"},
		{Command: "help", Description: "帮助说明"},
		{Command: "cancel", Description: "取消当前输入"},
	}
}

func (b *Bot) handleUpdate(ctx context.Context, u telegram.Update) error {
	if u.CallbackQuery != nil {
		return b.handleCallback(ctx, u.CallbackQuery)
	}
	if u.Message != nil {
		return b.handleMessage(ctx, u.Message)
	}
	return nil
}

func (b *Bot) handleMessage(ctx context.Context, m *telegram.InMessage) error {
	if m.From == nil || m.From.IsBot {
		return nil
	}
	// only private chats for panel
	if m.Chat.Type != "private" {
		return b.tg.SendChat(ctx, m.Chat.ID, "请私聊 Bot 使用控制面板（群聊中不处理配置指令）。", nil)
	}
	if !b.allowed(m.From.ID) {
		return b.tg.SendChat(ctx, m.Chat.ID,
			"⛔ 你没有权限使用此 Bot。请联系管理员将你的 Telegram ID 加入 allowlist。\n你的 ID: "+
				telegram.Code(strconv.FormatInt(m.From.ID, 10)), nil)
	}

	display := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	if _, err := b.users.GetOrCreate(m.From.ID, strconv.FormatInt(m.Chat.ID, 10), m.From.Username, display); err != nil {
		return err
	}

	text := strings.TrimSpace(m.Text)
	if text == "" {
		return nil
	}

	// pending wizard input?
	if s := b.getSession(m.From.ID); s != nil && s.Await != awaitNone {
		return b.handleAwait(ctx, m, s, text)
	}

	cmd, arg, _ := strings.Cut(text, " ")
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	// strip @botname suffix
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	arg = strings.TrimSpace(arg)

	switch {
	case cmd == "/start" || cmd == "/menu" || cmd == "菜单":
		return b.sendHome(ctx, m.Chat.ID, m.From.ID)
	case cmd == "/status" || cmd == "状态":
		return b.sendStatus(ctx, m.Chat.ID, m.From.ID)
	case cmd == "/help" || cmd == "帮助":
		return b.tg.SendChat(ctx, m.Chat.ID, helpText(), b.homeKeyboardFor(m.From.ID))
	case cmd == "/id" || cmd == "/myid":
		return b.tg.SendChat(ctx, m.Chat.ID,
			"你的 Telegram ID: "+telegram.Code(strconv.FormatInt(m.From.ID, 10))+"\nChat ID: "+
				telegram.Code(strconv.FormatInt(m.Chat.ID, 10)), homeKeyboard())
	case cmd == "/cancel" || cmd == "取消":
		b.clearSession(m.From.ID)
		return b.tg.SendChat(ctx, m.Chat.ID, "已取消当前输入。", b.homeKeyboardFor(m.From.ID))
	case strings.HasPrefix(cmd, "/setbase") || cmd == "/baseurl":
		if arg != "" {
			return b.setBaseURL(ctx, m.Chat.ID, m.From.ID, arg)
		}
		b.setAwait(m.From.ID, awaitBaseURL, 0, "")
		return b.tg.SendChat(ctx, m.Chat.ID, "请发送 Sub2API 的 Base URL（例如 <code>http://192.168.1.10:8080</code>）\n发送 /cancel 取消。", cancelKeyboard())
	case strings.HasPrefix(cmd, "/setkey") || cmd == "/apikey":
		if arg != "" {
			// best-effort delete the message that contains the key
			_ = b.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
			return b.setAPIKey(ctx, m.Chat.ID, m.From.ID, arg)
		}
		b.setAwait(m.From.ID, awaitAPIKey, 0, "")
		return b.tg.SendChat(ctx, m.Chat.ID, "请发送 Admin API Key（发送后会尽量删除含密钥的消息）。\n发送 /cancel 取消。", cancelKeyboard())
	case strings.HasPrefix(cmd, "/addaccount") || cmd == "/add":
		if arg != "" {
			return b.addAccount(ctx, m.Chat.ID, m.From.ID, arg)
		}
		b.setAwait(m.From.ID, awaitAddAcc, 0, "")
		return b.tg.SendChat(ctx, m.Chat.ID, "请发送要监控的账号 ID（数字，可在 Sub2API 后台账号列表查看）。\n或点「从列表选择」。\n发送 /cancel 取消。", addAccountKeyboard())
	case strings.HasPrefix(cmd, "/delaccount") || cmd == "/del":
		if arg == "" {
			return b.tg.SendChat(ctx, m.Chat.ID, "用法: /delaccount &lt;id&gt;", nil)
		}
		return b.delAccount(ctx, m.Chat.ID, m.From.ID, arg)
	case cmd == "/thresholds" || cmd == "/threshold" || cmd == "阈值":
		return b.tg.SendChat(ctx, m.Chat.ID, b.thresholdsText(m.From.ID), thresholdsKeyboard(m.From.ID, b))
	case cmd == "/ops" || cmd == "/dashboard" || cmd == "运维":
		if !b.canOpsRead(m.From.ID) {
			return b.tg.SendChat(ctx, m.Chat.ID, "⛔ 运维视图仅管理员或只读运维可用。", b.homeKeyboardFor(m.From.ID))
		}
		var stats *sub2api.DashboardStats
		if cli, _, err := b.userClient(m.From.ID, 6*time.Second); err == nil && cli != nil {
			if st, err := cli.GetDashboardStats(ctx); err == nil {
				stats = st
			}
		}
		return b.tg.SendChat(ctx, m.Chat.ID, b.opsMenuText(ctx, m.From.ID), opsKeyboardFor(stats, b.canOpsWrite(m.From.ID)))
	case cmd == "/manage" || cmd == "/mgr" || cmd == "管理":
		if !b.canOpsRead(m.From.ID) {
			return b.tg.SendChat(ctx, m.Chat.ID, "⛔ 账号管理/浏览仅管理员或只读运维可用。", b.homeKeyboardFor(m.From.ID))
		}
		var stats *sub2api.DashboardStats
		if cli, _, err := b.userClient(m.From.ID, 6*time.Second); err == nil && cli != nil {
			if st, err := cli.GetDashboardStats(ctx); err == nil {
				stats = st
			}
		}
		return b.tg.SendChat(ctx, m.Chat.ID, b.manageMenuText(ctx, m.From.ID), manageKeyboardFor(stats, b.canOpsWrite(m.From.ID)))
	case cmd == "/search" || strings.HasPrefix(cmd, "/search"):
		if !b.canOpsRead(m.From.ID) {
			return b.tg.SendChat(ctx, m.Chat.ID, "⛔ 搜索账号仅管理员或只读运维可用。", b.homeKeyboardFor(m.From.ID))
		}
		if arg != "" {
			return b.showAccountBrowser(ctx, m.Chat.ID, 0, m.From.ID, "search:"+arg, 0)
		}
		b.setAwait(m.From.ID, awaitSearch, 0, "")
		return b.tg.SendChat(ctx, m.Chat.ID, "请发送要搜索的账号关键词（名称/邮箱片段）\n/cancel 取消", cancelKeyboard())
	case cmd == "/check" || cmd == "立即检查":
		return b.forceCheck(ctx, m.Chat.ID, m.From.ID)
	default:
		// free text when not awaiting → show menu
		return b.sendHome(ctx, m.Chat.ID, m.From.ID)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cq *telegram.CallbackQuery) error {
	if cq.From == nil {
		return nil
	}
	if !b.allowed(cq.From.ID) {
		_ = b.tg.AnswerCallback(ctx, cq.ID, "无权限", true)
		return nil
	}
	chatID := cq.From.ID
	msgID := int64(0)
	if cq.Message != nil {
		chatID = cq.Message.Chat.ID
		msgID = cq.Message.MessageID
	}
	display := strings.TrimSpace(cq.From.FirstName + " " + cq.From.LastName)
	if _, err := b.users.GetOrCreate(cq.From.ID, strconv.FormatInt(chatID, 10), cq.From.Username, display); err != nil {
		_ = b.tg.AnswerCallback(ctx, cq.ID, "内部错误", true)
		return err
	}

	data := cq.Data
	// answer early for snappy UI; long ops re-answer with text where needed
	_ = b.tg.AnswerCallback(ctx, cq.ID, "", false)

	switch {
	case data == "home":
		b.clearSession(cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), b.homeKeyboardFor(cq.From.ID))
	case data == "status":
		return b.showStatus(ctx, chatID, msgID, cq.From.ID)
	case data == "cfg_conn":
		return b.editOrSend(ctx, chatID, msgID, b.connText(cq.From.ID), connKeyboardFor(b.isAdmin(cq.From.ID)))
	case data == "cfg_acc":
		b.syncWatchAccountNames(ctx, cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID, b.accountsText(cq.From.ID), b.accountsKeyboard(cq.From.ID))
	case data == "cfg_thr":
		return b.editOrSend(ctx, chatID, msgID, b.thresholdsText(cq.From.ID), thresholdsKeyboard(cq.From.ID, b))
	case data == "ops_menu":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.showOpsMenu(ctx, chatID, msgID, cq.From.ID)
	case data == "ops_dash":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.showDashboard(ctx, chatID, msgID, cq.From.ID)
	case data == "ops_avail":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.showAvailability(ctx, chatID, msgID, cq.From.ID)
	case data == "ops_alerts":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.showAlerts(ctx, chatID, msgID, cq.From.ID)
	case data == "ops_errors" || strings.HasPrefix(data, "ops_errors:"):
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
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
		return b.showErrorsView(ctx, chatID, msgID, cq.From.ID, kind, page, "")
	case data == "ops_conc":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.showConcurrency(ctx, chatID, msgID, cq.From.ID)
	case data == "ops_channels":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.showChannels(ctx, chatID, msgID, cq.From.ID)
	case data == "ops_traf" || strings.HasPrefix(data, "ops_traf:"):
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		win := "5min"
		if strings.HasPrefix(data, "ops_traf:") {
			win = strings.TrimPrefix(data, "ops_traf:")
		}
		return b.showTraffic(ctx, chatID, msgID, cq.From.ID, win)
	case data == "ops_badacc" || strings.HasPrefix(data, "ops_badacc:"):
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		rest := ""
		if strings.HasPrefix(data, "ops_badacc:") {
			rest = strings.TrimPrefix(data, "ops_badacc:")
		}
		kind, page := browse.ParseBadAccCallback(rest)
		return b.showBadAccountsView(ctx, chatID, msgID, cq.From.ID, kind, page, "")
	case data == "ops_watch_errors":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.watchErrorAccounts(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_menu":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.showManageMenu(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_search":
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		b.setAwait(cq.From.ID, awaitSearch, 0, "")
		return b.editOrSend(ctx, chatID, msgID, "请发送要搜索的账号关键词（名称/邮箱片段）\n/cancel 取消", cancelKeyboard())
	case data == "mgr_bulk_clear":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkClearErrors(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_bulk_clear_go":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkClearErrorsExecute(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_bulk_recover":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkRecoverPrompt(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_bulk_recover_go":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkAccountActionExecute(ctx, chatID, msgID, cq.From.ID, "recover")
	case data == "mgr_bulk_sched_on":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkSchedOnPrompt(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_bulk_sched_on_go":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkAccountActionExecute(ctx, chatID, msgID, cq.From.ID, "sched_on")
	case data == "mgr_bulk_clear_rl":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkClearRLPrompt(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_bulk_clear_rl_go":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkAccountActionExecute(ctx, chatID, msgID, cq.From.ID, "clear_rl")
	case data == "mgr_bulk_heal":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkHealPrompt(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_bulk_heal_go":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.bulkAccountActionExecute(ctx, chatID, msgID, cq.From.ID, "heal")
	case data == "oe:resolve_all:u":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.resolveAllUpstreamErrors(ctx, chatID, msgID, cq.From.ID)
	case data == "oe:resolve_all:r":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.resolveAllRequestErrors(ctx, chatID, msgID, cq.From.ID)
	case strings.HasPrefix(data, "oe:r:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		// oe:r:<u|r>:<errorID>
		rest := strings.TrimPrefix(data, "oe:r:")
		kind, idStr, ok := strings.Cut(rest, ":")
		if !ok {
			return b.showErrors(ctx, chatID, msgID, cq.From.ID)
		}
		eid, _ := strconv.ParseInt(idStr, 10, 64)
		return b.resolveOpsError(ctx, chatID, msgID, cq.From.ID, kind, eid)
	case data == "seed_conn":
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		return b.seedConnectionFromGlobal(ctx, chatID, msgID, cq.From.ID)
	case data == "mgr_browse" || strings.HasPrefix(data, "mgr_browse:"):
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		status, page := "all", 0
		if strings.HasPrefix(data, "mgr_browse:") {
			status, page = parseBrowseCallback(strings.TrimPrefix(data, "mgr_browse:"))
		} else {
			status, page = b.getBrowseView(cq.From.ID)
		}
		return b.showAccountBrowser(ctx, chatID, msgID, cq.From.ID, status, page)
	case strings.HasPrefix(data, "mgr_acc:"):
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "mgr_acc:"), 10, 64)
		return b.showManageAccount(ctx, chatID, msgID, cq.From.ID, id, "")
	case strings.HasPrefix(data, "mgr_act:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		// mgr_act:<action...>:<accountID>
		// action may itself contain colons (e.g. temp:15m)
		rest := strings.TrimPrefix(data, "mgr_act:")
		idx := strings.LastIndex(rest, ":")
		if idx <= 0 {
			return b.showManageMenu(ctx, chatID, msgID, cq.From.ID)
		}
		action := rest[:idx]
		id, _ := strconv.ParseInt(rest[idx+1:], 10, 64)
		if strings.HasPrefix(action, "temp:") && action != "temp_menu" {
			// mgr_act:temp:15m:<id>
			dur := strings.TrimPrefix(action, "temp:")
			return b.applyTempUnschedulable(ctx, chatID, msgID, cq.From.ID, id, dur)
		}
		return b.handleManageAction(ctx, chatID, msgID, cq.From.ID, action, id)
	case data == "mgr_users" || strings.HasPrefix(data, "mgr_users:"):
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		page := 0
		if strings.HasPrefix(data, "mgr_users:") {
			page, _ = strconv.Atoi(strings.TrimPrefix(data, "mgr_users:"))
		}
		return b.showUsers(ctx, chatID, msgID, cq.From.ID, page)
	case data == "mgr_groups" || strings.HasPrefix(data, "mgr_groups:"):
		if b.denyIfNotOpsRead(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		page := 0
		if strings.HasPrefix(data, "mgr_groups:") {
			page, _ = strconv.Atoi(strings.TrimPrefix(data, "mgr_groups:"))
		}
		return b.showGroups(ctx, chatID, msgID, cq.From.ID, page)
	case data == "pnl_users" || strings.HasPrefix(data, "pnl_users:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		page := 0
		if strings.HasPrefix(data, "pnl_users:") {
			page, _ = strconv.Atoi(strings.TrimPrefix(data, "pnl_users:"))
		}
		return b.showPanelUsers(ctx, chatID, msgID, cq.From.ID, page, "")
	case strings.HasPrefix(data, "pnl_user:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "pnl_user:"), 10, 64)
		return b.showPanelUserDetail(ctx, chatID, msgID, cq.From.ID, id, "")
	case strings.HasPrefix(data, "pnl_role:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		// pnl_role:<admin|viewer|user|clear>:<targetUserID>
		rest := strings.TrimPrefix(data, "pnl_role:")
		role, idStr, ok := strings.Cut(rest, ":")
		if !ok {
			return b.showPanelUsers(ctx, chatID, msgID, cq.From.ID, 0, "")
		}
		tid, _ := strconv.ParseInt(idStr, 10, 64)
		return b.setPanelUserRole(ctx, chatID, msgID, cq.From.ID, tid, role)
	case strings.HasPrefix(data, "pnl_mon:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		tid, _ := strconv.ParseInt(strings.TrimPrefix(data, "pnl_mon:"), 10, 64)
		return b.togglePanelUserMonitor(ctx, chatID, msgID, cq.From.ID, tid)
	case strings.HasPrefix(data, "pnl_src:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		tid, _ := strconv.ParseInt(strings.TrimPrefix(data, "pnl_src:"), 10, 64)
		return b.togglePanelUserSource(ctx, chatID, msgID, cq.From.ID, tid)
	case data == "set_base":
		b.setAwait(cq.From.ID, awaitBaseURL, 0, "")
		return b.editOrSend(ctx, chatID, msgID, "请发送 Base URL（如 <code>http://host:8080</code>）\n/cancel 取消", cancelKeyboard())
	case data == "set_key":
		b.setAwait(cq.From.ID, awaitAPIKey, 0, "")
		return b.editOrSend(ctx, chatID, msgID, "请发送 Admin API Key\n发送后将尽量删除含密钥的消息\n/cancel 取消", cancelKeyboard())
	case data == "test_conn":
		msg := b.testConnection(ctx, cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID, msg+"\n\n"+b.connText(cq.From.ID), connKeyboardFor(b.isAdmin(cq.From.ID)))
	case data == "clear_conn":
		_, err := b.users.Update(cq.From.ID, func(p *userstore.Profile) error {
			p.BaseURL = ""
			p.AdminAPIKey = ""
			p.JWT = ""
			return nil
		})
		if err != nil {
			return b.tg.SendChat(ctx, chatID, "清除失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		return b.editOrSend(ctx, chatID, msgID, "✅ 已清除连接配置\n\n"+b.connText(cq.From.ID), connKeyboardFor(b.isAdmin(cq.From.ID)))
	case data == "add_acc":
		b.setAwait(cq.From.ID, awaitAddAcc, 0, "")
		return b.editOrSend(ctx, chatID, msgID, "请发送账号 ID（数字）\n或从列表选择。\n/cancel 取消", addAccountKeyboard())
	case data == "pick_acc" || strings.HasPrefix(data, "pick_acc:"):
		status, page := "all", 0
		if strings.HasPrefix(data, "pick_acc:") {
			status, page = browse.ParseCallback(strings.TrimPrefix(data, "pick_acc:"))
		}
		return b.showAccountPicker(ctx, chatID, msgID, cq.From.ID, status, page)
	case strings.HasPrefix(data, "pick:"):
		idStr := strings.TrimPrefix(data, "pick:")
		label, err := b.addAccountMutate(ctx, chatID, cq.From.ID, idStr)
		if err != nil {
			// already watched etc — show toast-ish via new message then refresh
			_ = b.tg.SendChat(ctx, chatID, "添加失败: "+telegram.EscapeHTML(err.Error()), nil)
			return b.editOrSend(ctx, chatID, msgID, b.accountsText(cq.From.ID), b.accountsKeyboard(cq.From.ID))
		}
		return b.editOrSend(ctx, chatID, msgID,
			"✅ 已添加 "+telegram.Code(label)+"\n\n"+b.accountsText(cq.From.ID),
			b.accountsKeyboard(cq.From.ID))
	case strings.HasPrefix(data, "del_acc:"):
		idStr := strings.TrimPrefix(data, "del_acc:")
		id, err := b.delAccountMutate(cq.From.ID, idStr)
		if err != nil {
			_ = b.tg.SendChat(ctx, chatID, "删除失败: "+telegram.EscapeHTML(err.Error()), nil)
			return b.editOrSend(ctx, chatID, msgID, b.accountsText(cq.From.ID), b.accountsKeyboard(cq.From.ID))
		}
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("✅ 已移除账号 #%d\n\n", id)+b.accountsText(cq.From.ID),
			b.accountsKeyboard(cq.From.ID))
	case strings.HasPrefix(data, "tog_acc:"):
		idStr := strings.TrimPrefix(data, "tog_acc:")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		_, err := b.users.Update(cq.From.ID, func(p *userstore.Profile) error {
			for i := range p.Accounts {
				if p.Accounts[i].ID == id {
					en := p.Accounts[i].IsEnabled()
					v := !en
					p.Accounts[i].Enabled = &v
					return nil
				}
			}
			return fmt.Errorf("account not found")
		})
		if err != nil {
			return b.tg.SendChat(ctx, chatID, "切换失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		// Prefer detail view when account still watched (toggle from detail)
		if p, ok := b.users.Get(cq.From.ID); ok {
			for _, a := range p.Accounts {
				if a.ID == id {
					return b.editOrSend(ctx, chatID, msgID, b.accountDetailText(ctx, cq.From.ID, id), b.accountDetailKeyboard(cq.From.ID, id))
				}
			}
		}
		return b.editOrSend(ctx, chatID, msgID, b.accountsText(cq.From.ID), b.accountsKeyboard(cq.From.ID))
	case strings.HasPrefix(data, "acc:"):
		// account detail
		idStr := strings.TrimPrefix(data, "acc:")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		return b.editOrSend(ctx, chatID, msgID, b.accountDetailText(ctx, cq.From.ID, id), b.accountDetailKeyboard(cq.From.ID, id))
	case strings.HasPrefix(data, "live_act:"):
		if b.denyIfNotAdmin(ctx, chatID, msgID, cq.From.ID, cq.ID) {
			return nil
		}
		// live_act:<action>:<accountID> — admin quick ops, stay on live view
		rest := strings.TrimPrefix(data, "live_act:")
		action, idStr, ok := strings.Cut(rest, ":")
		if !ok {
			return b.sendHome(ctx, chatID, cq.From.ID)
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		return b.handleLiveAction(ctx, chatID, msgID, cq.From.ID, action, id)
	case strings.HasPrefix(data, "acc_live:"):
		idStr := strings.TrimPrefix(data, "acc_live:")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		return b.showAccountLive(ctx, chatID, msgID, cq.From.ID, id)
	case strings.HasPrefix(data, "rename:"):
		idStr := strings.TrimPrefix(data, "rename:")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		b.setAwait(cq.From.ID, awaitRename, id, "")
		return b.editOrSend(ctx, chatID, msgID, fmt.Sprintf("请发送账号 #%d 的显示名称\n/cancel 取消", id), cancelKeyboard())
	case data == "toggle_mon":
		_, err := b.users.Update(cq.From.ID, func(p *userstore.Profile) error {
			p.Enabled = !p.Enabled
			return nil
		})
		if err != nil {
			return err
		}
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), b.homeKeyboardFor(cq.From.ID))
	case data == "toggle_src":
		_, err := b.users.Update(cq.From.ID, func(p *userstore.Profile) error {
			if p.Source == "active" {
				p.Source = "passive"
			} else {
				p.Source = "active"
			}
			return nil
		})
		if err != nil {
			return err
		}
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), b.homeKeyboardFor(cq.From.ID))
	case data == "check_now":
		_ = b.tg.SendChat(ctx, chatID, "⏳ 正在检查用量…", nil)
		return b.forceCheck(ctx, chatID, cq.From.ID)
	case data == "help":
		return b.editOrSend(ctx, chatID, msgID, helpText(), b.homeKeyboardFor(cq.From.ID))
	case data == "cancel":
		b.clearSession(cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID, "已取消。\n\n"+b.homeText(cq.From.ID), b.homeKeyboardFor(cq.From.ID))

	// per-account thresholds
	case strings.HasPrefix(data, "acc_thr:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr:"), 10, 64)
		return b.editOrSend(ctx, chatID, msgID, b.accountThresholdsText(cq.From.ID, id), b.accountThresholdsKeyboard(cq.From.ID, id))
	case strings.HasPrefix(data, "acc_thr_add:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr_add:"), 10, 64)
		return b.editOrSend(ctx, chatID, msgID, fmt.Sprintf("账号 #%d — 选择用量窗口：", id), thrWindowKeyboardForAccount(id))
	case strings.HasPrefix(data, "acc_thr_win:"):
		rest := strings.TrimPrefix(data, "acc_thr_win:")
		idStr, win, ok := strings.Cut(rest, ":")
		if !ok {
			return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), b.homeKeyboardFor(cq.From.ID))
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		b.setAwait(cq.From.ID, awaitThrPct, id, win)
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("账号 #%d · 窗口 <code>%s</code>\n选择阈值百分比，或直接发送数字（1-100）：", id, telegram.EscapeHTML(win)),
			thrPercentKeyboardScoped(id, win))
	case strings.HasPrefix(data, "acc_thr_pct:"):
		// acc_thr_pct:id:window:80
		rest := strings.TrimPrefix(data, "acc_thr_pct:")
		parts := strings.Split(rest, ":")
		if len(parts) < 3 {
			return b.editOrSend(ctx, chatID, msgID, "无效账号阈值参数", b.homeKeyboardFor(cq.From.ID))
		}
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		win := strings.Join(parts[1:len(parts)-1], ":")
		pct, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil {
			return b.editOrSend(ctx, chatID, msgID, "无效百分比", b.accountThresholdsKeyboard(cq.From.ID, id))
		}
		if err := b.setAccountThreshold(cq.From.ID, id, win, pct, ""); err != nil {
			return b.tg.SendChat(ctx, chatID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		b.clearSession(cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("✅ 账号 #%d 已设置 %s ≥ %.0f%%\n\n", id, telegram.Code(win), pct)+b.accountThresholdsText(cq.From.ID, id),
			b.accountThresholdsKeyboard(cq.From.ID, id))
	case strings.HasPrefix(data, "acc_thr_del:"):
		rest := strings.TrimPrefix(data, "acc_thr_del:")
		idStr, win, ok := strings.Cut(rest, ":")
		if !ok {
			return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), b.homeKeyboardFor(cq.From.ID))
		}
		id, _ := strconv.ParseInt(idStr, 10, 64)
		if err := b.deleteAccountThreshold(cq.From.ID, id, win); err != nil {
			return b.tg.SendChat(ctx, chatID, "删除失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		return b.editOrSend(ctx, chatID, msgID, b.accountThresholdsText(cq.From.ID, id), b.accountThresholdsKeyboard(cq.From.ID, id))
	case strings.HasPrefix(data, "acc_thr_clear:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr_clear:"), 10, 64)
		if err := b.clearAccountThresholds(cq.From.ID, id); err != nil {
			return b.tg.SendChat(ctx, chatID, "清除失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		return b.editOrSend(ctx, chatID, msgID, "✅ 已清除账号专属阈值（恢复继承）\n\n"+b.accountThresholdsText(cq.From.ID, id), b.accountThresholdsKeyboard(cq.From.ID, id))
	case strings.HasPrefix(data, "acc_thr_copy:"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(data, "acc_thr_copy:"), 10, 64)
		if err := b.copyDefaultsToAccount(cq.From.ID, id); err != nil {
			return b.tg.SendChat(ctx, chatID, "复制失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		return b.editOrSend(ctx, chatID, msgID, "✅ 已复制用户/系统默认到该账号\n\n"+b.accountThresholdsText(cq.From.ID, id), b.accountThresholdsKeyboard(cq.From.ID, id))

	// thresholds
	case data == "thr_add":
		return b.editOrSend(ctx, chatID, msgID, "选择用量窗口：", thrWindowKeyboard())
	case strings.HasPrefix(data, "thr_win:"):
		win := strings.TrimPrefix(data, "thr_win:")
		b.setAwait(cq.From.ID, awaitThrPct, 0, win)
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("窗口 <code>%s</code>\n选择阈值百分比，或直接发送数字（1-100）：", telegram.EscapeHTML(win)),
			thrPercentKeyboard(win))
	case strings.HasPrefix(data, "thr_pct:"):
		// thr_pct:window:80
		rest := strings.TrimPrefix(data, "thr_pct:")
		win, pctStr, ok := strings.Cut(rest, ":")
		if !ok {
			return b.editOrSend(ctx, chatID, msgID, "无效阈值参数", thresholdsKeyboard(cq.From.ID, b))
		}
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			return b.editOrSend(ctx, chatID, msgID, "无效百分比", thresholdsKeyboard(cq.From.ID, b))
		}
		if err := b.setThreshold(cq.From.ID, win, pct, ""); err != nil {
			return b.tg.SendChat(ctx, chatID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		b.clearSession(cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("✅ 已设置 %s ≥ %.0f%%\n\n", telegram.Code(win), pct)+b.thresholdsText(cq.From.ID),
			thresholdsKeyboard(cq.From.ID, b))
	case strings.HasPrefix(data, "thr_del:"):
		win := strings.TrimPrefix(data, "thr_del:")
		if err := b.deleteThreshold(cq.From.ID, win); err != nil {
			return b.tg.SendChat(ctx, chatID, "删除失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		return b.editOrSend(ctx, chatID, msgID, b.thresholdsText(cq.From.ID), thresholdsKeyboard(cq.From.ID, b))
	case data == "thr_reset":
		_, err := b.users.Update(cq.From.ID, func(p *userstore.Profile) error {
			p.Thresholds = nil
			return nil
		})
		if err != nil {
			return err
		}
		return b.editOrSend(ctx, chatID, msgID, "✅ 已恢复系统默认阈值\n\n"+b.thresholdsText(cq.From.ID), thresholdsKeyboard(cq.From.ID, b))
	case data == "thr_apply_defs":
		// copy system defaults into user profile as explicit thresholds
		_, err := b.users.Update(cq.From.ID, func(p *userstore.Profile) error {
			p.Thresholds = append([]config.UsageThreshold(nil), b.defaults...)
			return nil
		})
		if err != nil {
			return err
		}
		return b.editOrSend(ctx, chatID, msgID, "✅ 已把系统默认写入你的配置（可继续改）\n\n"+b.thresholdsText(cq.From.ID), thresholdsKeyboard(cq.From.ID, b))

	default:
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), b.homeKeyboardFor(cq.From.ID))
	}
}

func (b *Bot) handleAwait(ctx context.Context, m *telegram.InMessage, s *session, text string) error {
	if strings.EqualFold(text, "/cancel") || text == "取消" {
		b.clearSession(m.From.ID)
		return b.tg.SendChat(ctx, m.Chat.ID, "已取消。", b.homeKeyboardFor(m.From.ID))
	}
	switch s.Await {
	case awaitBaseURL:
		b.clearSession(m.From.ID)
		return b.setBaseURL(ctx, m.Chat.ID, m.From.ID, text)
	case awaitAPIKey:
		b.clearSession(m.From.ID)
		_ = b.tg.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
		return b.setAPIKey(ctx, m.Chat.ID, m.From.ID, text)
	case awaitAddAcc:
		b.clearSession(m.From.ID)
		return b.addAccount(ctx, m.Chat.ID, m.From.ID, text)
	case awaitThrPct:
		pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(text, "%")), 64)
		if err != nil || pct <= 0 || pct > 100 {
			kb := thrPercentKeyboardScoped(s.AccountID, s.Window)
			return b.tg.SendChat(ctx, m.Chat.ID, "请发送 1-100 之间的数字（如 80）", kb)
		}
		win := s.Window
		accID := s.AccountID
		b.clearSession(m.From.ID)
		if accID > 0 {
			if err := b.setAccountThreshold(m.From.ID, accID, win, pct, ""); err != nil {
				return b.tg.SendChat(ctx, m.Chat.ID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
			}
			return b.tg.SendChat(ctx, m.Chat.ID,
				fmt.Sprintf("✅ 账号 #%d 已设置 %s ≥ %.0f%%\n\n", accID, telegram.Code(win), pct)+b.accountThresholdsText(m.From.ID, accID),
				b.accountThresholdsKeyboard(m.From.ID, accID))
		}
		if err := b.setThreshold(m.From.ID, win, pct, ""); err != nil {
			return b.tg.SendChat(ctx, m.Chat.ID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		return b.tg.SendChat(ctx, m.Chat.ID,
			fmt.Sprintf("✅ 已设置 %s ≥ %.0f%%\n\n", telegram.Code(win), pct)+b.thresholdsText(m.From.ID),
			thresholdsKeyboard(m.From.ID, b))
	case awaitSearch:
		q := strings.TrimSpace(text)
		b.clearSession(m.From.ID)
		if q == "" {
			return b.tg.SendChat(ctx, m.Chat.ID, "关键词不能为空", manageKeyboard())
		}
		// reuse showAccountBrowser with search via special status prefix search:keyword
		return b.showAccountBrowser(ctx, m.Chat.ID, 0, m.From.ID, "search:"+q, 0)
	case awaitRename:
		name := strings.TrimSpace(text)
		if name == "" {
			return b.tg.SendChat(ctx, m.Chat.ID, "名称不能为空", cancelKeyboard())
		}
		id := s.AccountID
		b.clearSession(m.From.ID)
		_, err := b.users.Update(m.From.ID, func(p *userstore.Profile) error {
			for i := range p.Accounts {
				if p.Accounts[i].ID == id {
					p.Accounts[i].Name = name
					return nil
				}
			}
			return fmt.Errorf("账号 #%d 不存在", id)
		})
		if err != nil {
			return b.tg.SendChat(ctx, m.Chat.ID, "重命名失败: "+telegram.EscapeHTML(err.Error()), nil)
		}
		return b.tg.SendChat(ctx, m.Chat.ID, "✅ 已更新名称\n\n"+b.accountDetailText(ctx, m.From.ID, id), b.accountDetailKeyboard(m.From.ID, id))
	default:
		b.clearSession(m.From.ID)
		return b.sendHome(ctx, m.Chat.ID, m.From.ID)
	}
}

func (b *Bot) setBaseURL(ctx context.Context, chatID, userID int64, raw string) error {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return b.tg.SendChat(ctx, chatID, "URL 需以 http:// 或 https:// 开头", connKeyboard())
	}
	if _, err := b.users.GetOrCreate(userID, strconv.FormatInt(chatID, 10), "", ""); err != nil {
		return err
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.BaseURL = u
		return nil
	})
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
	}
	return b.tg.SendChat(ctx, chatID, "✅ Base URL 已保存: "+telegram.Code(u), connKeyboardFor(b.isAdmin(userID)))
}

func (b *Bot) setAPIKey(ctx context.Context, chatID, userID int64, raw string) error {
	key := strings.TrimSpace(raw)
	if key == "" {
		return b.tg.SendChat(ctx, chatID, "密钥不能为空", connKeyboard())
	}
	if _, err := b.users.GetOrCreate(userID, strconv.FormatInt(chatID, 10), "", ""); err != nil {
		return err
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.AdminAPIKey = key
		p.JWT = ""
		return nil
	})
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
	}
	return b.tg.SendChat(ctx, chatID, "✅ API Key 已保存: "+telegram.Code(userstore.MaskKey(key)), connKeyboardFor(b.isAdmin(userID)))
}

// addAccountMutate adds an account and returns a short success label or error.
func (b *Bot) addAccountMutate(ctx context.Context, chatID, userID int64, raw string) (string, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("无效的账号 ID，请输入正整数")
	}
	name := ""
	platform := ""
	if p, ok := b.users.Get(userID); ok && p.HasConnection() {
		if cli, err := sub2api.NewClient(config.Sub2APIConfig{
			BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 10 * time.Second,
		}); err == nil {
			if acc, err := cli.GetAccount(ctx, id); err == nil && acc != nil {
				name = acc.Name
				platform = acc.Platform
			}
		}
	}
	if _, err := b.users.GetOrCreate(userID, strconv.FormatInt(chatID, 10), "", ""); err != nil {
		return "", err
	}
	_, err = b.users.Update(userID, func(p *userstore.Profile) error {
		for _, a := range p.Accounts {
			if a.ID == id {
				return fmt.Errorf("账号 #%d 已在列表中", id)
			}
		}
		en := true
		p.Accounts = append(p.Accounts, userstore.AccountWatch{ID: id, Name: name, Enabled: &en})
		return nil
	})
	if err != nil {
		return "", err
	}
	label := fmt.Sprintf("#%d", id)
	if name != "" {
		label += " " + name
	}
	if platform != "" {
		label += " (" + platform + ")"
	}
	return label, nil
}

func (b *Bot) addAccount(ctx context.Context, chatID, userID int64, raw string) error {
	label, err := b.addAccountMutate(ctx, chatID, userID, raw)
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "添加失败: "+telegram.EscapeHTML(err.Error()), b.accountsKeyboard(userID))
	}
	return b.tg.SendChat(ctx, chatID, "✅ 已添加监控账号 "+telegram.Code(label), b.accountsKeyboard(userID))
}

func (b *Bot) delAccountMutate(userID int64, raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("无效的账号 ID")
	}
	_, err = b.users.Update(userID, func(p *userstore.Profile) error {
		out := p.Accounts[:0]
		found := false
		for _, a := range p.Accounts {
			if a.ID == id {
				found = true
				continue
			}
			out = append(out, a)
		}
		if !found {
			return fmt.Errorf("未找到账号 #%d", id)
		}
		p.Accounts = out
		return nil
	})
	return id, err
}

func (b *Bot) delAccount(ctx context.Context, chatID, userID int64, raw string) error {
	id, err := b.delAccountMutate(userID, raw)
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "删除失败: "+telegram.EscapeHTML(err.Error()), b.accountsKeyboard(userID))
	}
	return b.tg.SendChat(ctx, chatID, "✅ 已移除账号 "+telegram.Code(fmt.Sprintf("#%d", id)), b.accountsKeyboard(userID))
}

func (b *Bot) setThreshold(userID int64, window string, pct float64, severity string) error {
	window = normalizeWindow(window)
	if window == "" {
		return fmt.Errorf("无效窗口")
	}
	if pct <= 0 || pct > 100 {
		return fmt.Errorf("百分比需在 1-100")
	}
	if severity == "" {
		severity = "P2"
		if pct >= 90 {
			severity = "P1"
		}
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		// start from current effective list so first edit doesn't lose defaults
		ths := p.Thresholds
		if len(ths) == 0 {
			ths = append([]config.UsageThreshold(nil), b.defaults...)
		}
		found := false
		for i := range ths {
			if strings.EqualFold(normalizeWindow(ths[i].Window), window) {
				ths[i].Window = window
				ths[i].UtilizationGTE = pct
				ths[i].Severity = severity
				found = true
				break
			}
		}
		if !found {
			ths = append(ths, config.UsageThreshold{Window: window, UtilizationGTE: pct, Severity: severity})
		}
		p.Thresholds = ths
		return nil
	})
	return err
}

func (b *Bot) deleteThreshold(userID int64, window string) error {
	window = normalizeWindow(window)
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		ths := p.Thresholds
		if len(ths) == 0 {
			// materialize defaults then delete one, so user has an explicit list
			ths = append([]config.UsageThreshold(nil), b.defaults...)
		}
		out := ths[:0]
		found := false
		for _, t := range ths {
			if strings.EqualFold(normalizeWindow(t.Window), window) {
				found = true
				continue
			}
			out = append(out, t)
		}
		if !found {
			return fmt.Errorf("未找到窗口 %s", window)
		}
		p.Thresholds = out
		return nil
	})
	return err
}

func (b *Bot) setAccountThreshold(userID, accountID int64, window string, pct float64, severity string) error {
	window = normalizeWindow(window)
	if accountID <= 0 {
		return fmt.Errorf("无效账号")
	}
	if window == "" {
		return fmt.Errorf("无效窗口")
	}
	if pct <= 0 || pct > 100 {
		return fmt.Errorf("百分比需在 1-100")
	}
	if severity == "" {
		severity = "P2"
		if pct >= 90 {
			severity = "P1"
		}
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		for i := range p.Accounts {
			if p.Accounts[i].ID != accountID {
				continue
			}
			ths := p.Accounts[i].Thresholds
			found := false
			for j := range ths {
				if strings.EqualFold(normalizeWindow(ths[j].Window), window) {
					ths[j].Window = window
					ths[j].UtilizationGTE = pct
					ths[j].Severity = severity
					found = true
					break
				}
			}
			if !found {
				ths = append(ths, config.UsageThreshold{Window: window, UtilizationGTE: pct, Severity: severity})
			}
			p.Accounts[i].Thresholds = ths
			return nil
		}
		return fmt.Errorf("账号 #%d 不在监控列表", accountID)
	})
	return err
}

func (b *Bot) deleteAccountThreshold(userID, accountID int64, window string) error {
	window = normalizeWindow(window)
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		for i := range p.Accounts {
			if p.Accounts[i].ID != accountID {
				continue
			}
			out := p.Accounts[i].Thresholds[:0]
			found := false
			for _, t := range p.Accounts[i].Thresholds {
				if strings.EqualFold(normalizeWindow(t.Window), window) {
					found = true
					continue
				}
				out = append(out, t)
			}
			if !found {
				return fmt.Errorf("未找到窗口 %s", window)
			}
			p.Accounts[i].Thresholds = out
			return nil
		}
		return fmt.Errorf("账号 #%d 不在监控列表", accountID)
	})
	return err
}

func (b *Bot) clearAccountThresholds(userID, accountID int64) error {
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		for i := range p.Accounts {
			if p.Accounts[i].ID != accountID {
				continue
			}
			p.Accounts[i].Thresholds = nil
			return nil
		}
		return fmt.Errorf("账号 #%d 不在监控列表", accountID)
	})
	return err
}

func (b *Bot) copyDefaultsToAccount(userID, accountID int64) error {
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		src := p.Thresholds
		if len(src) == 0 {
			src = b.defaults
		}
		for i := range p.Accounts {
			if p.Accounts[i].ID != accountID {
				continue
			}
			p.Accounts[i].Thresholds = append([]config.UsageThreshold(nil), src...)
			return nil
		}
		return fmt.Errorf("账号 #%d 不在监控列表", accountID)
	})
	return err
}

func (b *Bot) testConnection(ctx context.Context, userID int64) string {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() {
		return "❌ 请先配置 Base URL 与 API Key"
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 10 * time.Second,
	})
	if err != nil {
		return "❌ 客户端创建失败: " + telegram.EscapeHTML(err.Error())
	}
	if err := cli.Health(ctx); err != nil {
		return "❌ /health 失败: " + telegram.EscapeHTML(err.Error())
	}
	// light auth check
	stats, err := cli.GetDashboardStats(ctx)
	if err != nil {
		return "⚠️ health 正常，但 Admin API 鉴权失败: " + telegram.EscapeHTML(err.Error())
	}
	extra := ""
	if stats != nil {
		extra = fmt.Sprintf("\n账号: total=%d error=%d overload=%d",
			stats.TotalAccounts, stats.ErrorAccounts, stats.OverloadAccounts)
	}
	return "✅ 连接成功（health + dashboard OK）" + extra
}

func (b *Bot) forceCheck(ctx context.Context, chatID, userID int64) error {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() {
		return b.tg.SendChat(ctx, chatID, "请先配置连接（Base URL + API Key）", b.homeKeyboardFor(userID))
	}
	if len(p.Accounts) == 0 {
		return b.tg.SendChat(ctx, chatID, "请先添加监控账号", b.homeKeyboardFor(userID))
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 25 * time.Second,
	})
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "错误: "+telegram.EscapeHTML(err.Error()), nil)
	}
	thsDefault := p.Thresholds
	if len(thsDefault) == 0 {
		thsDefault = b.defaults
	}

	src := p.EffectiveSource()
	force := strings.EqualFold(src, "active")
	var bld strings.Builder
	bld.WriteString(telegram.Bold("用量快照") + "\n")
	forceLabel := "缓存"
	if force {
		forceLabel = "强制刷新"
	}
	fmt.Fprintf(&bld, "数据源: %s · %s · %s\n\n", telegram.Code(src), telegram.Code(forceLabel), telegram.Code(time.Now().Local().Format("15:04:05")))
	checked := 0
	warnN := 0
	var issueIDs []int64
	var issueLabels []string
	var targets []browse.WatchTarget
	thByID := map[int64][]config.UsageThreshold{}
	for _, a := range p.Accounts {
		if !a.IsEnabled() {
			fmt.Fprintf(&bld, "• #%d %s %s\n", a.ID, telegram.EscapeHTML(displayName(a)), telegram.Code("已暂停"))
			continue
		}
		checked++
		targets = append(targets, browse.WatchTarget{ID: a.ID, Name: displayName(a)})
		thByID[a.ID] = a.Thresholds
	}
	snaps := browse.FetchAccountSnaps(ctx, cli, targets, browse.SnapOpts{
		Source: src, Force: force, WithToday: true, Concurrency: 4,
	})
	for _, snap := range snaps {
		name := snap.Name
		if name == "" {
			name = fmt.Sprintf("#%d", snap.ID)
		}
		statusLine := ""
		accBad := false
		if acc := snap.Account; acc != nil {
			statusLine = fmt.Sprintf(" [%s]", acc.Status)
			if acc.Platform != "" {
				statusLine = fmt.Sprintf(" [%s/%s]", acc.Platform, acc.Status)
			}
			if strings.EqualFold(acc.Status, "error") || acc.ErrorMessage != "" || acc.RateLimitedAt != nil || !acc.Schedulable {
				accBad = true
			}
		} else if snap.AccountErr != nil {
			accBad = true
			statusLine = " [详情失败]"
		}
		fmt.Fprintf(&bld, "• %s%s\n", telegram.EscapeHTML(fmt.Sprintf("#%d %s", snap.ID, name)), telegram.EscapeHTML(statusLine))
		hitThr := false
		if snap.UsageErr != nil {
			fmt.Fprintf(&bld, "  用量: %s\n", telegram.EscapeHTML(snap.UsageErr.Error()))
			accBad = true
		} else if usage := snap.Usage; usage != nil {
			ths := thByID[snap.ID]
			if len(ths) == 0 {
				ths = thsDefault
			}
			thMap := map[string]float64{}
			for _, th := range ths {
				thMap[sub2api.NormalizeWindow(th.Window)] = th.UtilizationGTE
			}
			sum, hit := usage.CompactUsageSummary(thMap, 4)
			if hit {
				hitThr = true
			}
			if sum == "" {
				bld.WriteString("  用量: (无窗口数据)\n")
			} else {
				fmt.Fprintf(&bld, "  用量: %s\n", telegram.EscapeHTML(sum))
			}
			if usage.Error != "" {
				fmt.Fprintf(&bld, "  提示: %s\n", telegram.EscapeHTML(usage.Error))
				accBad = true
			}
		}
		if today := snap.Today; today != nil {
			fmt.Fprintf(&bld, "  今日: req=%s tok=%s cost=%s\n",
				telegram.Code(strconv.FormatInt(today.Requests, 10)),
				telegram.Code(strconv.FormatInt(today.Tokens, 10)),
				telegram.Code(fmt.Sprintf("%.4f", today.Cost)),
			)
		}
		if hitThr || accBad {
			warnN++
			if len(issueIDs) < 6 {
				issueIDs = append(issueIDs, snap.ID)
				issueLabels = append(issueLabels, name)
			}
		}
	}
	if checked == 0 {
		bld.WriteString("\n没有启用的账号。")
	} else if warnN > 0 {
		fmt.Fprintf(&bld, "\n⚠️ 需关注 %s 个账号（超阈值或状态异常），可用下方按钮直达。\n", telegram.Code(strconv.Itoa(warnN)))
	} else {
		bld.WriteString("\n✅ 监控账号用量与状态正常。\n")
	}
	return b.tg.SendChat(ctx, chatID, bld.String(), checkResultKeyboard(b.canOpsWrite(userID), issueIDs, issueLabels))
}

// checkResultKeyboard offers per-account live/manage jumps after force check.
func checkResultKeyboard(admin bool, issueIDs []int64, issueLabels []string) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 再检查", "check_now"), telegram.Btn("« 主面板", "home")},
	}
	if len(issueIDs) > 0 {
		var row []telegram.InlineKeyboardButton
		for i, id := range issueIDs {
			if i >= 4 {
				break
			}
			label := fmt.Sprintf("#%d", id)
			if i < len(issueLabels) && issueLabels[i] != "" {
				label = fmt.Sprintf("#%d %s", id, truncateRunes(issueLabels[i], 8))
			}
			if admin {
				row = append(row, telegram.Btn("管理 "+label, fmt.Sprintf("mgr_acc:%d", id)))
			} else {
				row = append(row, telegram.Btn("实时 "+label, fmt.Sprintf("acc_live:%d", id)))
			}
			if len(row) == 2 {
				rows = append(rows, row)
				row = nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	if admin {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
			telegram.Btn("🛠 运维视图", "ops_menu"),
		})
	} else {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("👤 监控账号", "cfg_acc"),
			telegram.Btn("🎯 阈值", "cfg_thr"),
		})
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) showAccountPicker(ctx context.Context, chatID, msgID, userID int64, status string, page int) error {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() {
		return b.editOrSend(ctx, chatID, msgID, "❌ 请先配置连接后再从列表选择", connKeyboard())
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 15 * time.Second,
	})
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "客户端错误: "+telegram.EscapeHTML(err.Error()), b.accountsKeyboard(userID))
	}
	const pageSize = 8
	if page < 0 {
		page = 0
	}
	if status == "" {
		status = "all"
	}
	items, total, err := browse.ListAccounts(ctx, cli, status, page, pageSize)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取账号列表失败: "+telegram.EscapeHTML(err.Error()), b.accountsKeyboard(userID))
	}
	// build set of already watched
	watched := map[int64]bool{}
	for _, a := range p.Accounts {
		watched[a.ID] = true
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("选择账号添加监控") + "\n")
	fmt.Fprintf(&bld, "筛选: %s · 第 %d 页 · 共 %d 个\n点击按钮添加（已监控的标 ✓）\n",
		telegram.Code(status), page+1, total)
	token := browse.Token(status)
	rows := [][]telegram.InlineKeyboardButton{
		{
			telegram.Btn(pickFilterLabel(status, "all", "全部"), "pick_acc:all:0"),
			telegram.Btn(pickFilterLabel(status, "active", "active"), "pick_acc:active:0"),
			telegram.Btn(pickFilterLabel(status, "error", "error"), "pick_acc:error:0"),
		},
		{
			telegram.Btn(pickFilterLabel(status, "rate_limited", "限速"), "pick_acc:rate_limited:0"),
			telegram.Btn(pickFilterLabel(status, "unsched", "停调度"), "pick_acc:unsched:0"),
		},
	}
	for _, acc := range items {
		mark := ""
		if watched[acc.ID] {
			mark = "✓ "
		}
		label := fmt.Sprintf("%s#%d %s", mark, acc.ID, truncateRunes(acc.Name, 14))
		if acc.Platform != "" {
			label = fmt.Sprintf("%s#%d [%s] %s", mark, acc.ID, truncateRunes(acc.Platform, 8), truncateRunes(acc.Name, 10))
		}
		// callback_data max 64 bytes
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(label, fmt.Sprintf("pick:%d", acc.ID)),
		})
	}
	nav := []telegram.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("pick_acc:%s:%d", token, page-1)))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("pick_acc:%s:%d", token, page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{telegram.Btn("手动输入 ID", "add_acc")},
		[]telegram.InlineKeyboardButton{telegram.Btn("« 返回账号", "cfg_acc")},
	)
	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
}

func pickFilterLabel(cur, want, label string) string {
	if cur == want {
		return "· " + label
	}
	return label
}

// ----- views -----

func (b *Bot) sendHome(ctx context.Context, chatID, userID int64) error {
	return b.tg.SendChat(ctx, chatID, b.homeText(userID), b.homeKeyboardFor(userID))
}

func (b *Bot) sendStatus(ctx context.Context, chatID, userID int64) error {
	text, issueIDs := b.statusTextWithIssues(ctx, userID)
	return b.tg.SendChat(ctx, chatID, text, b.statusKeyboardFor(userID, issueIDs))
}

func (b *Bot) showStatus(ctx context.Context, chatID, msgID, userID int64) error {
	text, issueIDs := b.statusTextWithIssues(ctx, userID)
	return b.editOrSend(ctx, chatID, msgID, text, b.statusKeyboardFor(userID, issueIDs))
}

// statusText is a live status view: profile + watched accounts health (best-effort).
func (b *Bot) statusText(ctx context.Context, userID int64) string {
	text, _ := b.statusTextWithIssues(ctx, userID)
	return text
}

// statusTextWithIssues returns status text and watched account IDs that need attention.
func (b *Bot) statusTextWithIssues(ctx context.Context, userID int64) (string, []int64) {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString(telegram.Bold("运行状态") + "\n")
	bld.WriteString("实例: " + telegram.Code(b.cfg.Instance) + "\n")
	fmt.Fprintf(&bld, "角色: %s · 检查间隔: %s · 冷却: %s\n",
		telegram.Code(b.roleLabel(userID)),
		telegram.Code(b.cfg.Telegram.Panel.CheckInterval.String()),
		telegram.Code(b.cfg.Telegram.Panel.Cooldown.String()),
	)
	fmt.Fprintf(&bld, "时间: %s\n\n", telegram.Code(time.Now().Local().Format("01-02 15:04:05")))

	if b.canOpsRead(userID) {
		if cli, _, err := b.userClient(userID, 5*time.Second); err == nil && cli != nil {
			if line, issues := adminHealthSnapshot(ctx, cli); line != "" {
				bld.WriteString(telegram.Bold("实例健康") + "\n")
				bld.WriteString(line + "\n")
				if issues {
					bld.WriteString("可从下方运维入口查看异常（写操作需管理员）。\n")
				}
				bld.WriteString("\n")
			}
		}
	}

	if p == nil {
		bld.WriteString("尚未创建配置，点「主面板」开始。")
		return bld.String(), nil
	}
	mon := "关闭"
	if p.Enabled {
		mon = "开启"
	}
	fmt.Fprintf(&bld, "监控: %s · 数据源: %s\n", telegram.Code(mon), telegram.Code(p.EffectiveSource()))
	base := p.BaseURL
	if base == "" {
		base = "(未设置)"
	}
	fmt.Fprintf(&bld, "Base URL: %s\n", telegram.Code(base))
	fmt.Fprintf(&bld, "API Key: %s\n", telegram.Code(userstore.MaskKey(p.AdminAPIKey)))

	enabled := make([]userstore.AccountWatch, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		if a.IsEnabled() {
			enabled = append(enabled, a)
		}
	}
	fmt.Fprintf(&bld, "监控账号: %s 个（启用 %s）\n\n",
		telegram.Code(strconv.Itoa(len(p.Accounts))),
		telegram.Code(strconv.Itoa(len(enabled))),
	)

	// thresholds one-liner
	ths := p.Thresholds
	src := "系统默认"
	if len(ths) > 0 {
		src = "自定义"
	} else {
		ths = b.defaults
	}
	fmt.Fprintf(&bld, "阈值(%s): ", src)
	if len(ths) == 0 {
		bld.WriteString("(无)\n")
	} else {
		parts := make([]string, 0, len(ths))
		for _, t := range ths {
			parts = append(parts, fmt.Sprintf("%s≥%.0f%%", t.Window, t.UtilizationGTE))
		}
		bld.WriteString(telegram.Code(strings.Join(parts, ", ")) + "\n")
	}

	if !p.HasConnection() {
		bld.WriteString("\n⚠️ 请先配置连接信息")
		return bld.String(), nil
	}
	if len(p.Accounts) == 0 {
		bld.WriteString("\n⚠️ 请添加至少一个监控账号")
		return bld.String(), nil
	}

	bld.WriteString("\n" + telegram.Bold("启用账号快照") + "（含用量）\n")
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 12 * time.Second,
	})
	if err != nil {
		bld.WriteString("客户端错误: " + telegram.EscapeHTML(err.Error()))
		return bld.String(), nil
	}
	thsDefault := p.Thresholds
	if len(thsDefault) == 0 {
		thsDefault = b.defaults
	}
	usageSrc := p.EffectiveSource()
	warnN := 0
	usageHitN := 0
	var issueIDs []int64
	const maxShow = 8
	targets := make([]browse.WatchTarget, 0, len(enabled))
	thByID := map[int64][]config.UsageThreshold{}
	for _, a := range enabled {
		targets = append(targets, browse.WatchTarget{ID: a.ID, Name: displayName(a)})
		thByID[a.ID] = a.Thresholds
	}
	snaps := browse.FetchAccountSnaps(ctx, cli, targets, browse.SnapOpts{Source: usageSrc, MaxShow: maxShow, Concurrency: 4})
	for _, snap := range snaps {
		name := snap.Name
		if name == "" {
			name = fmt.Sprintf("#%d", snap.ID)
		}
		flag := "✅"
		detail := ""
		statusBad := false
		if snap.AccountErr != nil {
			flag = "❓"
			detail = telegram.EscapeHTML(truncateRunes(snap.AccountErr.Error(), 40))
			statusBad = true
			fmt.Fprintf(&bld, "%s #%d %s · %s\n", flag, snap.ID, telegram.EscapeHTML(truncateRunes(name, 14)), detail)
		} else if acc := snap.Account; acc != nil {
			parts := []string{acc.Status}
			if acc.Platform != "" {
				parts = []string{acc.Platform, acc.Status}
			}
			if !acc.Schedulable {
				parts = append(parts, "停调度")
				flag = "⏸"
				statusBad = true
			}
			if acc.RateLimitedAt != nil || strings.Contains(strings.ToLower(acc.Status), "rate") {
				parts = append(parts, "限速")
				flag = "⏱"
				statusBad = true
			}
			if strings.EqualFold(acc.Status, "error") || acc.ErrorMessage != "" {
				flag = "❌"
				statusBad = true
				if acc.ErrorMessage != "" {
					detail = telegram.EscapeHTML(truncateRunes(acc.ErrorMessage, 48))
				}
			}
			if strings.EqualFold(acc.Status, "disabled") {
				flag = "🚫"
				statusBad = true
			}
			detailLine := strings.Join(parts, "/")
			fmt.Fprintf(&bld, "%s #%d %s · %s\n",
				flag,
				snap.ID,
				telegram.EscapeHTML(truncateRunes(name, 14)),
				telegram.Code(detailLine),
			)
			if detail != "" && flag == "❌" {
				fmt.Fprintf(&bld, "   %s\n", detail)
			}
		} else {
			fmt.Fprintf(&bld, "%s #%d %s\n", flag, snap.ID, telegram.EscapeHTML(truncateRunes(name, 14)))
		}

		ths := thByID[snap.ID]
		if len(ths) == 0 {
			ths = thsDefault
		}
		thMap := map[string]float64{}
		for _, th := range ths {
			thMap[sub2api.NormalizeWindow(th.Window)] = th.UtilizationGTE
		}
		usageLine := ""
		usageHit := false
		if snap.UsageErr != nil {
			usageLine = "用量: " + telegram.EscapeHTML(truncateRunes(snap.UsageErr.Error(), 36))
			usageHit = true
		} else if usage := snap.Usage; usage != nil {
			sum, hit := usage.CompactUsageSummary(thMap, 3)
			usageHit = hit
			if sum == "" {
				sum = "(无窗口)"
			}
			usageLine = "用量: " + telegram.Code(sum)
			if usage.Error != "" {
				usageHit = true
			}
		}
		if usageLine != "" {
			fmt.Fprintf(&bld, "   %s\n", usageLine)
		}
		if statusBad || usageHit {
			warnN++
			if usageHit {
				usageHitN++
			}
			if len(issueIDs) < 6 {
				issueIDs = append(issueIDs, snap.ID)
			}
		}
	}
	if len(enabled) > maxShow {
		fmt.Fprintf(&bld, "… 另有 %s 个启用账号\n", telegram.Code(strconv.Itoa(len(enabled)-maxShow)))
	}
	if len(enabled) == 0 {
		bld.WriteString("(没有启用的监控账号)\n")
	} else if warnN > 0 {
		fmt.Fprintf(&bld, "\n⚠️ 需关注 %s 个账号", telegram.Code(strconv.Itoa(warnN)))
		if usageHitN > 0 {
			fmt.Fprintf(&bld, "（含 %s 个超阈值/用量异常）", telegram.Code(strconv.Itoa(usageHitN)))
		}
		bld.WriteString("；点下方账号或「立即检查」看详情。\n")
	} else {
		bld.WriteString("\n✅ 启用账号状态与用量正常。\n")
	}
	if !p.Enabled {
		bld.WriteString("\n⏸ 自动监控已关闭（不会后台告警）。")
	} else {
		bld.WriteString("\n✅ 自动监控开启中。")
	}
	return bld.String(), issueIDs
}

func (b *Bot) statusKeyboardFor(userID int64, issueIDs ...[]int64) *telegram.InlineKeyboardMarkup {
	var issues []int64
	if len(issueIDs) > 0 {
		issues = issueIDs[0]
	}
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新状态", "status"), telegram.Btn("▶️ 立即检查", "check_now")},
	}
	// watched issue shortcuts
	if len(issues) > 0 {
		var row []telegram.InlineKeyboardButton
		for i, id := range issues {
			if i >= 4 {
				break
			}
			if b.canOpsRead(userID) {
				row = append(row, telegram.Btn(fmt.Sprintf("查看 #%d", id), fmt.Sprintf("mgr_acc:%d", id)))
			} else {
				row = append(row, telegram.Btn(fmt.Sprintf("实时 #%d", id), fmt.Sprintf("acc_live:%d", id)))
			}
			if len(row) == 2 {
				rows = append(rows, row)
				row = nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	rows = append(rows, []telegram.InlineKeyboardButton{
		telegram.Btn("👤 监控账号", "cfg_acc"), telegram.Btn("🔌 连接", "cfg_conn"),
	})
	if b.canOpsRead(userID) {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("🛠 运维视图", "ops_menu"),
			telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
		})
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("📈 看板", "ops_dash"),
			telegram.Btn("🧰 账号浏览", "mgr_menu"),
		})
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 主面板", "home")})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) homeText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString(telegram.Bold("Sub2API 监控面板") + "\n")
	bld.WriteString("实例: " + telegram.Code(b.cfg.Instance) + "\n")
	fmt.Fprintf(&bld, "角色: %s · 检查间隔: %s · 冷却: %s\n\n",
		telegram.Code(b.roleLabel(userID)),
		telegram.Code(b.cfg.Telegram.Panel.CheckInterval.String()),
		telegram.Code(b.cfg.Telegram.Panel.Cooldown.String()),
	)
	if b.canOpsRead(userID) {
		if cli, _, err := b.userClient(userID, 5*time.Second); err == nil && cli != nil {
			if line, issues := adminHealthSnapshot(context.Background(), cli); line != "" {
				bld.WriteString(telegram.Bold("运维快照") + "\n")
				bld.WriteString(line + "\n")
				if issues {
					bld.WriteString("可从下方「运维视图 / 看板」查看异常（写操作需管理员）。\n")
				}
				bld.WriteString("\n")
			}
		}
	}
	if p == nil {
		bld.WriteString("尚未创建配置，点下方按钮开始。")
		return bld.String()
	}
	mon := "关闭"
	if p.Enabled {
		mon = "开启"
	}
	fmt.Fprintf(&bld, "监控: %s · 数据源: %s\n", telegram.Code(mon), telegram.Code(p.EffectiveSource()))
	base := p.BaseURL
	if base == "" {
		base = "(未设置)"
	}
	fmt.Fprintf(&bld, "Base URL: %s\n", telegram.Code(base))
	fmt.Fprintf(&bld, "API Key: %s\n", telegram.Code(userstore.MaskKey(p.AdminAPIKey)))
	enabledN := 0
	for _, a := range p.Accounts {
		if a.IsEnabled() {
			enabledN++
		}
	}
	fmt.Fprintf(&bld, "监控账号: %s 个（启用 %s）\n",
		telegram.Code(strconv.Itoa(len(p.Accounts))),
		telegram.Code(strconv.Itoa(enabledN)),
	)
	// thresholds summary
	ths := p.Thresholds
	src := "系统默认"
	if len(ths) > 0 {
		src = "自定义"
	} else {
		ths = b.defaults
	}
	fmt.Fprintf(&bld, "阈值(%s): ", src)
	if len(ths) == 0 {
		bld.WriteString("(无)")
	} else {
		parts := make([]string, 0, len(ths))
		for _, t := range ths {
			parts = append(parts, fmt.Sprintf("%s≥%.0f%%", t.Window, t.UtilizationGTE))
		}
		bld.WriteString(telegram.Code(strings.Join(parts, ", ")))
	}
	bld.WriteString("\n")

	if !p.HasConnection() {
		bld.WriteString("\n⚠️ 请先配置连接信息")
	} else if len(p.Accounts) == 0 {
		bld.WriteString("\n⚠️ 请添加至少一个监控账号")
	} else if p.Enabled {
		bld.WriteString("\n✅ 后台将按间隔检查用量并私聊告警")
	} else {
		bld.WriteString("\n⏸ 监控已关闭（不会自动检查）")
	}
	return bld.String()
}

func (b *Bot) connText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString(telegram.Bold("连接配置") + "\n\n")
	base, key := "(未设置)", "(未设置)"
	if p != nil {
		if p.BaseURL != "" {
			base = p.BaseURL
		}
		key = userstore.MaskKey(p.AdminAPIKey)
	}
	fmt.Fprintf(&bld, "Base URL: %s\n", telegram.Code(base))
	fmt.Fprintf(&bld, "API Key: %s\n", telegram.Code(key))
	bld.WriteString("\n密钥仅存于本机 users.json，不会写入 git。\n可用「测试连接」验证 Admin API。")
	return bld.String()
}

// syncWatchAccountNames best-effort fills empty AccountWatch.Name from Sub2API.
func (b *Bot) syncWatchAccountNames(ctx context.Context, userID int64) {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() || len(p.Accounts) == 0 {
		return
	}
	need := false
	for _, a := range p.Accounts {
		if strings.TrimSpace(a.Name) == "" {
			need = true
			break
		}
	}
	if !need {
		return
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 8 * time.Second,
	})
	if err != nil {
		return
	}
	_, _ = b.users.Update(userID, func(p *userstore.Profile) error {
		for i := range p.Accounts {
			if strings.TrimSpace(p.Accounts[i].Name) != "" {
				continue
			}
			if acc, err := cli.GetAccount(ctx, p.Accounts[i].ID); err == nil && acc != nil && acc.Name != "" {
				p.Accounts[i].Name = acc.Name
			}
		}
		return nil
	})
}

func (b *Bot) accountsText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString(telegram.Bold("监控账号") + "\n\n")
	if p == nil || len(p.Accounts) == 0 {
		bld.WriteString("列表为空。点击「添加账号」或「从列表选择」。")
		return bld.String()
	}
	for _, a := range p.Accounts {
		en := "ON"
		if !a.IsEnabled() {
			en = "OFF"
		}
		name := a.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(&bld, "• [%s] #%d %s\n", en, a.ID, telegram.EscapeHTML(name))
	}
	bld.WriteString("\n点击账号行可查看详情；⏸/▶️ 切换启用；🗑 删除。")
	return bld.String()
}

func (b *Bot) accountDetailText(ctx context.Context, userID int64, id int64) string {
	p, ok := b.users.Get(userID)
	if !ok {
		return "用户不存在"
	}
	var a *userstore.AccountWatch
	for i := range p.Accounts {
		if p.Accounts[i].ID == id {
			a = &p.Accounts[i]
			break
		}
	}
	if a == nil {
		return fmt.Sprintf("未找到账号 #%d", id)
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold(fmt.Sprintf("监控账号 #%d", id)) + "\n\n")
	fmt.Fprintf(&bld, "名称: %s\n", telegram.Code(displayName(*a)))
	en := "启用"
	if !a.IsEnabled() {
		en = "暂停"
	}
	fmt.Fprintf(&bld, "监控状态: %s\n", telegram.Code(en))
	ths := a.Thresholds
	if len(ths) == 0 {
		bld.WriteString("阈值: 继承用户/系统默认\n")
	} else {
		bld.WriteString("账号级阈值:\n")
		for _, t := range ths {
			fmt.Fprintf(&bld, "  • %s ≥ %.0f%% (%s)\n", t.Window, t.UtilizationGTE, t.Severity)
		}
	}

	// live enrich (best-effort)
	if p.HasConnection() {
		if cli, err := sub2api.NewClient(config.Sub2APIConfig{
			BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 12 * time.Second,
		}); err == nil {
			if acc, err := cli.GetAccount(ctx, id); err == nil && acc != nil {
				bld.WriteString("\n" + telegram.Bold("实例状态") + "\n")
				fmt.Fprintf(&bld, "平台/类型: %s / %s\n", telegram.Code(acc.Platform), telegram.Code(acc.Type))
				fmt.Fprintf(&bld, "状态: %s · 可调度: %s\n",
					telegram.Code(acc.Status),
					telegram.Code(fmt.Sprintf("%v", acc.Schedulable)))
				if acc.ErrorMessage != "" {
					fmt.Fprintf(&bld, "错误: %s\n", telegram.EscapeHTML(truncateRunes(acc.ErrorMessage, 100)))
				}
			} else if err != nil {
				fmt.Fprintf(&bld, "\n实例状态: %s\n", telegram.EscapeHTML(truncateRunes(err.Error(), 80)))
			}
			src := p.EffectiveSource()
			thMap := map[string]float64{}
			thsEff := ths
			if len(thsEff) == 0 {
				thsEff = p.Thresholds
				if len(thsEff) == 0 {
					thsEff = b.defaults
				}
			}
			for _, th := range thsEff {
				thMap[sub2api.NormalizeWindow(th.Window)] = th.UtilizationGTE
			}
			if usage, err := cli.GetAccountUsage(ctx, id, src, false); err == nil && usage != nil {
				sum, hit := usage.CompactUsageSummary(thMap, 4)
				if sum == "" {
					sum = "(无窗口)"
				}
				mark := ""
				if hit {
					mark = " ⚠️"
				}
				fmt.Fprintf(&bld, "\n用量(%s): %s%s\n", telegram.Code(src), telegram.Code(sum), mark)
			} else if err != nil {
				fmt.Fprintf(&bld, "\n用量: %s\n", telegram.EscapeHTML(truncateRunes(err.Error(), 60)))
			}
		}
	}
	return bld.String()
}

func (b *Bot) thresholdsText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString(telegram.Bold("用量阈值") + "\n\n")
	bld.WriteString("当某窗口使用率 ≥ 阈值时私聊告警；\n")
	bld.WriteString("恢复线默认阈值-5%。\n\n")

	var ths []config.UsageThreshold
	custom := false
	if p != nil && len(p.Thresholds) > 0 {
		ths = p.Thresholds
		custom = true
	} else {
		ths = b.defaults
	}
	if custom {
		bld.WriteString(telegram.Bold("当前: 自定义") + "\n")
	} else {
		bld.WriteString(telegram.Bold("当前: 系统默认") + "\n")
	}
	if len(ths) == 0 {
		bld.WriteString("(无阈值)\n")
	} else {
		for _, t := range ths {
			sev := t.Severity
			if sev == "" {
				sev = "P2"
			}
			fmt.Fprintf(&bld, "• %s ≥ %s%% · %s\n",
				telegram.Code(t.Window),
				telegram.Code(fmt.Sprintf("%.0f", t.UtilizationGTE)),
				telegram.Code(sev),
			)
		}
	}
	bld.WriteString("\n可添加/删除窗口阈值，或重置为系统默认。")
	return bld.String()
}

func (b *Bot) editOrSend(ctx context.Context, chatID, msgID int64, text string, kb *telegram.InlineKeyboardMarkup) error {
	// editMessageText cannot split; clamp to Telegram hard limit with ellipsis.
	const maxEdit = 3900
	editText := text
	if rn := []rune(editText); len(rn) > maxEdit {
		editText = string(rn[:maxEdit-1]) + "…"
	}
	if msgID > 0 {
		if err := b.tg.EditMessage(ctx, chatID, msgID, editText, kb); err == nil {
			return nil
		}
		// fall through to send (SendChat auto-splits)
	}
	return b.tg.SendChat(ctx, chatID, text, kb)
}

// ----- keyboards -----

func homeKeyboard() *telegram.InlineKeyboardMarkup {
	// default full keyboard (admin); prefer homeKeyboardFor when userID known
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("📊 状态", "status"), telegram.Btn("🛠 运维视图", "ops_menu")},
			{telegram.Btn("📈 看板", "ops_dash"), telegram.Btn("📋 异常账号", "ops_badacc:error:0")},
			{telegram.Btn("🧰 账号管理", "mgr_menu"), telegram.Btn("👤 监控账号", "cfg_acc")},
			{telegram.Btn("🔌 连接配置", "cfg_conn"), telegram.Btn("🎯 阈值", "cfg_thr")},
			{telegram.Btn("▶️ 立即检查", "check_now"), telegram.Btn("🔁 开关监控", "toggle_mon")},
			{telegram.Btn("📡 切换数据源", "toggle_src"), telegram.Btn("❓ 帮助", "help")},
		},
	}
}

func (b *Bot) homeKeyboardFor(userID int64) *telegram.InlineKeyboardMarkup {
	if b.isAdmin(userID) {
		return homeKeyboard()
	}
	if b.isViewer(userID) {
		// Viewer: ops read + self-service; no manage write hub
		return &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{telegram.Btn("📊 状态", "status"), telegram.Btn("🛠 运维视图", "ops_menu")},
				{telegram.Btn("📈 看板", "ops_dash"), telegram.Btn("📋 异常账号", "ops_badacc:error:0")},
				{telegram.Btn("👤 监控账号", "cfg_acc"), telegram.Btn("🔌 连接配置", "cfg_conn")},
				{telegram.Btn("🎯 阈值", "cfg_thr"), telegram.Btn("▶️ 立即检查", "check_now")},
				{telegram.Btn("🔁 开关监控", "toggle_mon"), telegram.Btn("📡 切换数据源", "toggle_src")},
				{telegram.Btn("❓ 帮助", "help")},
			},
		}
	}
	// Normal user: self-service monitoring only (no ops/manage write entry points)
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("📊 状态", "status"), telegram.Btn("👤 监控账号", "cfg_acc")},
			{telegram.Btn("🔌 连接配置", "cfg_conn"), telegram.Btn("🎯 阈值", "cfg_thr")},
			{telegram.Btn("▶️ 立即检查", "check_now"), telegram.Btn("🔁 开关监控", "toggle_mon")},
			{telegram.Btn("📡 切换数据源", "toggle_src"), telegram.Btn("❓ 帮助", "help")},
		},
	}
}

func connKeyboard() *telegram.InlineKeyboardMarkup {
	return connKeyboardFor(true)
}

func connKeyboardFor(admin bool) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("设置 Base URL", "set_base"), telegram.Btn("设置 API Key", "set_key")},
		{telegram.Btn("测试连接", "test_conn"), telegram.Btn("清除连接", "clear_conn")},
	}
	if admin {
		rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("📥 使用全局配置", "seed_conn")})
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 返回", "home")})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func addAccountKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("📋 从列表选择", "pick_acc")},
			{telegram.Btn("取消", "cancel")},
		},
	}
}

// accountsKeyboard builds dynamic account action buttons for a user.
func (b *Bot) accountsKeyboard(userID int64) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("➕ 添加账号", "add_acc"), telegram.Btn("📋 从列表选择", "pick_acc")},
	}
	if p, ok := b.users.Get(userID); ok {
		for _, a := range p.Accounts {
			label := fmt.Sprintf("#%d", a.ID)
			if a.Name != "" {
				label = fmt.Sprintf("#%d %s", a.ID, truncateRunes(a.Name, 12))
			}
			tog := "⏸"
			if !a.IsEnabled() {
				tog = "▶️"
			}
			rows = append(rows, []telegram.InlineKeyboardButton{
				telegram.Btn(label, fmt.Sprintf("acc:%d", a.ID)),
				telegram.Btn(tog, fmt.Sprintf("tog_acc:%d", a.ID)),
				telegram.Btn("🗑", fmt.Sprintf("del_acc:%d", a.ID)),
			})
		}
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 返回", "home")})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) accountDetailKeyboard(userID, id int64) *telegram.InlineKeyboardMarkup {
	tog := "⏸ 暂停监控"
	if p, ok := b.users.Get(userID); ok {
		for _, a := range p.Accounts {
			if a.ID == id && !a.IsEnabled() {
				tog = "▶️ 启用监控"
				break
			}
		}
	}
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("📡 实时状态/用量", fmt.Sprintf("acc_live:%d", id)), telegram.Btn("🔄 刷新", fmt.Sprintf("acc:%d", id))},
		{telegram.Btn(tog, fmt.Sprintf("tog_acc:%d", id)), telegram.Btn("🎯 账号阈值", fmt.Sprintf("acc_thr:%d", id))},
	}
	if b.canOpsRead(userID) {
		label := "🧰 管理操作"
		if b.isViewer(userID) {
			label = "👁 账号详情"
		}
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(label, fmt.Sprintf("mgr_acc:%d", id)),
		})
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("✏️ 重命名", fmt.Sprintf("rename:%d", id)),
			telegram.Btn("🗑 删除", fmt.Sprintf("del_acc:%d", id)),
		},
		[]telegram.InlineKeyboardButton{telegram.Btn("« 返回账号列表", "cfg_acc")},
	)
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func thresholdsKeyboard(userID int64, b *Bot) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("➕ 添加/修改阈值", "thr_add")},
	}
	// delete buttons for current thresholds
	var ths []config.UsageThreshold
	if p, ok := b.users.Get(userID); ok && len(p.Thresholds) > 0 {
		ths = p.Thresholds
	} else {
		ths = b.defaults
	}
	for _, t := range ths {
		w := normalizeWindow(t.Window)
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(fmt.Sprintf("🗑 %s", truncateRunes(w, 20)), "thr_del:"+w),
		})
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("写入系统默认", "thr_apply_defs"),
			telegram.Btn("重置默认", "thr_reset"),
		},
		[]telegram.InlineKeyboardButton{telegram.Btn("« 返回", "home")},
	)
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func thrWindowKeyboard() *telegram.InlineKeyboardMarkup {
	wins := []struct{ id, label string }{
		{"five_hour", "5 小时"},
		{"seven_day", "7 天"},
		{"seven_day_sonnet", "7d Sonnet"},
		{"seven_day_fable", "7d Fable"},
		{"gemini_shared_daily", "Gemini 共享日"},
		{"gemini_pro_daily", "Gemini Pro 日"},
		{"gemini_flash_daily", "Gemini Flash 日"},
		{"max", "最高窗口 max"},
	}
	rows := [][]telegram.InlineKeyboardButton{}
	var row []telegram.InlineKeyboardButton
	for i, w := range wins {
		row = append(row, telegram.Btn(w.label, "thr_win:"+w.id))
		if len(row) == 2 || i == len(wins)-1 {
			rows = append(rows, row)
			row = nil
		}
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 返回阈值", "cfg_thr")})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func thrPercentKeyboard(window string) *telegram.InlineKeyboardMarkup {
	return thrPercentKeyboardScoped(0, window)
}

func thrPercentKeyboardScoped(accountID int64, window string) *telegram.InlineKeyboardMarkup {
	pcts := []int{50, 60, 70, 80, 85, 90, 95, 100}
	rows := [][]telegram.InlineKeyboardButton{}
	var row []telegram.InlineKeyboardButton
	for i, p := range pcts {
		var data string
		if accountID > 0 {
			data = fmt.Sprintf("acc_thr_pct:%d:%s:%d", accountID, window, p)
		} else {
			data = fmt.Sprintf("thr_pct:%s:%d", window, p)
		}
		row = append(row, telegram.Btn(fmt.Sprintf("%d%%", p), data))
		if len(row) == 4 || i == len(pcts)-1 {
			rows = append(rows, row)
			row = nil
		}
	}
	back := "cfg_thr"
	backLabel := "« 返回阈值"
	if accountID > 0 {
		back = fmt.Sprintf("acc_thr:%d", accountID)
		backLabel = "« 账号阈值"
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn(backLabel, back)})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func thrWindowKeyboardForAccount(accountID int64) *telegram.InlineKeyboardMarkup {
	wins := []struct{ id, label string }{
		{"five_hour", "5 小时"},
		{"seven_day", "7 天"},
		{"seven_day_sonnet", "7d Sonnet"},
		{"seven_day_fable", "7d Fable"},
		{"gemini_shared_daily", "Gemini 共享日"},
		{"gemini_pro_daily", "Gemini Pro 日"},
		{"gemini_flash_daily", "Gemini Flash 日"},
		{"max", "最高窗口 max"},
	}
	rows := [][]telegram.InlineKeyboardButton{}
	var row []telegram.InlineKeyboardButton
	for i, w := range wins {
		row = append(row, telegram.Btn(w.label, fmt.Sprintf("acc_thr_win:%d:%s", accountID, w.id)))
		if len(row) == 2 || i == len(wins)-1 {
			rows = append(rows, row)
			row = nil
		}
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 账号阈值", fmt.Sprintf("acc_thr:%d", accountID))})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) accountThresholdsText(userID, accountID int64) string {
	p, ok := b.users.Get(userID)
	if !ok {
		return "用户不存在"
	}
	var a *userstore.AccountWatch
	for i := range p.Accounts {
		if p.Accounts[i].ID == accountID {
			a = &p.Accounts[i]
			break
		}
	}
	if a == nil {
		return fmt.Sprintf("未找到监控账号 #%d", accountID)
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold(fmt.Sprintf("账号 #%d 阈值", accountID)) + "\n")
	fmt.Fprintf(&bld, "名称: %s\n\n", telegram.Code(displayName(*a)))
	if len(a.Thresholds) == 0 {
		bld.WriteString("当前: " + telegram.Bold("继承用户/系统默认") + "\n")
		// show effective
		ths := p.Thresholds
		src := "用户默认"
		if len(ths) == 0 {
			ths = b.defaults
			src = "系统默认"
		}
		fmt.Fprintf(&bld, "生效来源: %s\n", telegram.Code(src))
		for _, t := range ths {
			sev := t.Severity
			if sev == "" {
				sev = "P2"
			}
			fmt.Fprintf(&bld, "• %s ≥ %s%% · %s\n",
				telegram.Code(t.Window),
				telegram.Code(fmt.Sprintf("%.0f", t.UtilizationGTE)),
				telegram.Code(sev),
			)
		}
		bld.WriteString("\n可设置账号专属阈值覆盖默认；清除后恢复继承。")
	} else {
		bld.WriteString("当前: " + telegram.Bold("账号专属") + "\n")
		for _, t := range a.Thresholds {
			sev := t.Severity
			if sev == "" {
				sev = "P2"
			}
			fmt.Fprintf(&bld, "• %s ≥ %s%% · %s\n",
				telegram.Code(t.Window),
				telegram.Code(fmt.Sprintf("%.0f", t.UtilizationGTE)),
				telegram.Code(sev),
			)
		}
		bld.WriteString("\n删除单项或清除全部后将继承用户/系统默认。")
	}
	return bld.String()
}

func (b *Bot) accountThresholdsKeyboard(userID, accountID int64) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("➕ 添加/修改", fmt.Sprintf("acc_thr_add:%d", accountID))},
	}
	if p, ok := b.users.Get(userID); ok {
		for _, a := range p.Accounts {
			if a.ID != accountID {
				continue
			}
			for _, t := range a.Thresholds {
				w := normalizeWindow(t.Window)
				rows = append(rows, []telegram.InlineKeyboardButton{
					telegram.Btn(fmt.Sprintf("🗑 %s", truncateRunes(w, 18)), fmt.Sprintf("acc_thr_del:%d:%s", accountID, w)),
				})
			}
			break
		}
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("📥 复制用户默认", fmt.Sprintf("acc_thr_copy:%d", accountID)),
			telegram.Btn("🧹 清除专属", fmt.Sprintf("acc_thr_clear:%d", accountID)),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("« 账号详情", fmt.Sprintf("acc:%d", accountID)),
			telegram.Btn("« 监控列表", "cfg_acc"),
		},
	)
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func cancelKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("取消", "cancel")},
		},
	}
}

func helpText() string {
	return telegram.Bold("帮助") + `

` + telegram.Bold("面板") + `
/start · /menu — 打开主面板
/status — 查看配置摘要
/ops — 运维视图（看板/可用性/告警/错误/并发）
/manage — 账号管理（调度/清错/一键修复/刷新/浏览）
/search &lt;关键词&gt; — 搜索账号
/check — 立即拉取用量快照
/id — 显示你的 Telegram ID
/thresholds — 管理用量阈值

` + telegram.Bold("连接") + `
/setbase &lt;url&gt; — 设置 Sub2API Base URL
/setkey &lt;key&gt; — 设置 Admin API Key

` + telegram.Bold("账号") + `
/addaccount &lt;id&gt; — 添加监控账号
/delaccount &lt;id&gt; — 删除监控账号

` + telegram.Bold("说明") + `
• 每位用户独立保存 base_url / key / 账号 / 阈值
• <b>普通用户</b>：自助连接 / 监控账号 / 阈值 / 立即检查
• <b>只读运维</b>：运维视图 / 看板 / 异常账号等只读，不能修复/调度/改角色
• <b>管理员</b>：运维视图 + 账号管理写操作（调度/启停/清错/一键修复/临时停调度/重置额度/批量/搜索/错误分页/异常账号/面板用户角色）
• 角色由 admin_user_ids 或 profile.role=admin|viewer|user 控制；菜单按角色显示
• 用量达到阈值时 Bot 会私聊提醒你（Telegram / Discord 按平台投递）
• 支持 passive（轻量缓存）与 active（刷新上游）数据源
• 配置按用户隔离，存于 data/users.json（跨平台共享）
• 发送 /cancel 取消当前输入
`
}

// ----- session + allowlist -----

func (b *Bot) allowed(userID int64) bool {
	// Admins always allowed
	if b.isAdmin(userID) {
		return true
	}
	list := b.cfg.Telegram.Panel.AllowUserIDs
	if len(list) > 0 {
		for _, id := range list {
			if id == userID {
				return true
			}
		}
		return false
	}
	// empty allowlist
	if b.cfg.Telegram.Panel.AllowAll || b.cfg.Telegram.Panel.OpenRegistration {
		return true
	}
	// fallback: default chat_id as sole owner when it's a private user id
	if b.cfg.Telegram.ChatID != "" {
		if id, err := strconv.ParseInt(b.cfg.Telegram.ChatID, 10, 64); err == nil && id == userID {
			return true
		}
	}
	return false
}

// isAdmin reports whether the Telegram user has full admin (write) privileges.
// Priority: Profile.Role override → admin_user_ids → numeric telegram.chat_id fallback.
// RoleViewer / RoleUser explicitly deny admin even if listed in admin_user_ids.
func (b *Bot) isAdmin(userID int64) bool {
	if p, ok := b.users.Get(userID); ok {
		switch p.EffectiveRole() {
		case userstore.RoleAdmin:
			return true
		case userstore.RoleViewer, userstore.RoleUser:
			return false
		}
	}
	for _, id := range b.cfg.Telegram.Panel.AdminUserIDs {
		if id == userID {
			return true
		}
	}
	// If no explicit admin list, treat private chat_id owner as sole admin.
	if len(b.cfg.Telegram.Panel.AdminUserIDs) == 0 && b.cfg.Telegram.ChatID != "" {
		if id, err := strconv.ParseInt(b.cfg.Telegram.ChatID, 10, 64); err == nil && id == userID {
			return true
		}
	}
	return false
}

// isViewer reports explicit profile.role=viewer (not admin). Admins supersede viewer.
func (b *Bot) isViewer(userID int64) bool {
	if b.isAdmin(userID) {
		return false
	}
	if p, ok := b.users.Get(userID); ok {
		return p.EffectiveRole() == userstore.RoleViewer
	}
	return false
}

// canOpsRead allows ops read views (dashboard/availability/errors/badacc/...).
func (b *Bot) canOpsRead(userID int64) bool {
	return b.isAdmin(userID) || b.isViewer(userID)
}

// canOpsWrite allows mutating manage/ops actions (heal/clear/bulk/resolve/roles).
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

// denyIfNotAdmin answers callback / sends message and returns true when denied.
// Alias for write-gated actions (admin only).
func (b *Bot) denyIfNotAdmin(ctx context.Context, chatID, msgID, userID int64, cqID string) bool {
	return b.denyIfNotWrite(ctx, chatID, msgID, userID, cqID)
}

// denyIfNotWrite denies non-admins for write actions.
func (b *Bot) denyIfNotWrite(ctx context.Context, chatID, msgID, userID int64, cqID string) bool {
	if b.canOpsWrite(userID) {
		return false
	}
	if cqID != "" {
		_ = b.tg.AnswerCallback(ctx, cqID, "需要管理员写权限", true)
	}
	msg := "⛔ 该操作需要管理员写权限。\n只读运维可查看运维视图，但不能执行修复/调度/角色变更。"
	if msgID > 0 {
		_ = b.editOrSend(ctx, chatID, msgID, msg, b.homeKeyboardFor(userID))
	} else {
		_ = b.tg.SendChat(ctx, chatID, msg, b.homeKeyboardFor(userID))
	}
	return true
}

// denyIfNotOpsRead denies users without ops read (admin or viewer).
func (b *Bot) denyIfNotOpsRead(ctx context.Context, chatID, msgID, userID int64, cqID string) bool {
	if b.canOpsRead(userID) {
		return false
	}
	if cqID != "" {
		_ = b.tg.AnswerCallback(ctx, cqID, "需要运维查看权限", true)
	}
	msg := "⛔ 该功能仅管理员或只读运维可用。\n普通用户可配置自己的连接、监控账号与阈值。"
	if msgID > 0 {
		_ = b.editOrSend(ctx, chatID, msgID, msg, b.homeKeyboardFor(userID))
	} else {
		_ = b.tg.SendChat(ctx, chatID, msg, b.homeKeyboardFor(userID))
	}
	return true
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

// manageBackButton returns a back button labeled from the remembered source.
func (b *Bot) manageBackButton(userID int64) telegram.InlineKeyboardButton {
	data := b.getManageBack(userID)
	label := "« 返回"
	switch {
	case data == "mgr_menu":
		label = "« 管理菜单"
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
		label = "« 账号浏览"
	case strings.HasPrefix(data, "ops_errors"):
		label = "« 错误列表"
	case data == "mgr_users" || strings.HasPrefix(data, "mgr_users:"):
		label = "« 实例用户"
	case data == "mgr_groups" || strings.HasPrefix(data, "mgr_groups:"):
		label = "« 分组"
	}
	return telegram.Btn(label, data)
}

func (b *Bot) getSession(userID int64) *session {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[userID]
	if !ok {
		return nil
	}
	if time.Since(s.UpdatedAt) > 10*time.Minute {
		delete(b.sessions, userID)
		return nil
	}
	// touch
	s.UpdatedAt = time.Now()
	return s
}

func (b *Bot) clearSession(userID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sessions, userID)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func displayName(a userstore.AccountWatch) string {
	if a.Name != "" {
		return a.Name
	}
	return fmt.Sprintf("#%d", a.ID)
}

func normalizeWindow(w string) string {
	return sub2api.NormalizeWindow(w)
}
