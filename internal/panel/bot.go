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
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

// pending input kinds for multi-step wizards
const (
	awaitNone = ""
	awaitBaseURL = "base_url"
	awaitAPIKey  = "api_key"
	awaitAddAcc  = "add_account"
	awaitSetThr  = "set_threshold" // format: window percent
)

type session struct {
	Await     string
	UpdatedAt time.Time
	// for threshold: which account id (0 = user default)
	AccountID int64
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
		return b.tg.SendChat(ctx, m.Chat.ID, "⛔ 你没有权限使用此 Bot。请联系管理员将你的 Telegram ID 加入 allowlist。\n你的 ID: "+telegram.Code(strconv.FormatInt(m.From.ID, 10)), nil)
	}

	display := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	p, err := b.users.GetOrCreate(m.From.ID, strconv.FormatInt(m.Chat.ID, 10), m.From.Username, display)
	if err != nil {
		return err
	}
	_ = p

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
	arg = strings.TrimSpace(arg)

	switch {
	case cmd == "/start" || cmd == "/menu" || cmd == "菜单":
		return b.sendHome(ctx, m.Chat.ID, m.From.ID)
	case cmd == "/status" || cmd == "状态":
		return b.sendStatus(ctx, m.Chat.ID, m.From.ID)
	case cmd == "/help" || cmd == "帮助":
		return b.tg.SendChat(ctx, m.Chat.ID, helpText(), homeKeyboard())
	case cmd == "/cancel" || cmd == "取消":
		b.clearSession(m.From.ID)
		return b.tg.SendChat(ctx, m.Chat.ID, "已取消当前输入。", homeKeyboard())
	case strings.HasPrefix(cmd, "/setbase") || cmd == "/baseurl":
		if arg != "" {
			return b.setBaseURL(ctx, m.Chat.ID, m.From.ID, arg)
		}
		b.setAwait(m.From.ID, awaitBaseURL, 0)
		return b.tg.SendChat(ctx, m.Chat.ID, "请发送 Sub2API 的 Base URL（例如 <code>http://192.168.1.10:8080</code>）\n发送 /cancel 取消。", nil)
	case strings.HasPrefix(cmd, "/setkey") || cmd == "/apikey":
		if arg != "" {
			return b.setAPIKey(ctx, m.Chat.ID, m.From.ID, arg)
		}
		b.setAwait(m.From.ID, awaitAPIKey, 0)
		return b.tg.SendChat(ctx, m.Chat.ID, "请发送 Admin API Key（消息不会回显完整密钥）。\n发送 /cancel 取消。", nil)
	case strings.HasPrefix(cmd, "/addaccount") || cmd == "/add":
		if arg != "" {
			return b.addAccount(ctx, m.Chat.ID, m.From.ID, arg)
		}
		b.setAwait(m.From.ID, awaitAddAcc, 0)
		return b.tg.SendChat(ctx, m.Chat.ID, "请发送要监控的账号 ID（数字，可在 Sub2API 后台账号列表查看）。\n发送 /cancel 取消。", nil)
	case strings.HasPrefix(cmd, "/delaccount") || cmd == "/del":
		if arg == "" {
			return b.tg.SendChat(ctx, m.Chat.ID, "用法: /delaccount &lt;id&gt;", nil)
		}
		return b.delAccount(ctx, m.Chat.ID, m.From.ID, arg)
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
	_, _ = b.users.GetOrCreate(cq.From.ID, strconv.FormatInt(chatID, 10), cq.From.Username, display)

	data := cq.Data
	_ = b.tg.AnswerCallback(ctx, cq.ID, "", false)

	switch {
	case data == "home":
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), homeKeyboard())
	case data == "status":
		text, kb := b.statusView(cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID, text, kb)
	case data == "cfg_conn":
		return b.editOrSend(ctx, chatID, msgID, b.connText(cq.From.ID), connKeyboard())
	case data == "cfg_acc":
		return b.editOrSend(ctx, chatID, msgID, b.accountsText(cq.From.ID), b.accountsKeyboard(cq.From.ID))
	case data == "set_base":
		b.setAwait(cq.From.ID, awaitBaseURL, 0)
		return b.editOrSend(ctx, chatID, msgID, "请发送 Base URL（如 <code>http://host:8080</code>）\n/cancel 取消", cancelKeyboard())
	case data == "set_key":
		b.setAwait(cq.From.ID, awaitAPIKey, 0)
		return b.editOrSend(ctx, chatID, msgID, "请发送 Admin API Key\n/cancel 取消", cancelKeyboard())
	case data == "test_conn":
		msg := b.testConnection(ctx, cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID, msg+"\n\n"+b.connText(cq.From.ID), connKeyboard())
	case data == "add_acc":
		b.setAwait(cq.From.ID, awaitAddAcc, 0)
		return b.editOrSend(ctx, chatID, msgID, "请发送账号 ID（数字）\n/cancel 取消", cancelKeyboard())
	case strings.HasPrefix(data, "del_acc:"):
		idStr := strings.TrimPrefix(data, "del_acc:")
		if err := b.delAccount(ctx, chatID, cq.From.ID, idStr); err != nil {
			return err
		}
		return b.editOrSend(ctx, chatID, msgID, b.accountsText(cq.From.ID), b.accountsKeyboard(cq.From.ID))
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
		return b.editOrSend(ctx, chatID, msgID, b.accountsText(cq.From.ID), b.accountsKeyboard(cq.From.ID))
	case data == "toggle_mon":
		_, err := b.users.Update(cq.From.ID, func(p *userstore.Profile) error {
			p.Enabled = !p.Enabled
			return nil
		})
		if err != nil {
			return err
		}
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), homeKeyboard())
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
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), homeKeyboard())
	case data == "check_now":
		_ = b.tg.SendChat(ctx, chatID, "⏳ 正在检查用量…", nil)
		return b.forceCheck(ctx, chatID, cq.From.ID)
	case data == "help":
		return b.editOrSend(ctx, chatID, msgID, helpText(), homeKeyboard())
	case data == "cancel":
		b.clearSession(cq.From.ID)
		return b.editOrSend(ctx, chatID, msgID, b.homeText(cq.From.ID), homeKeyboard())
	default:
		return nil
	}
}

func (b *Bot) handleAwait(ctx context.Context, m *telegram.InMessage, s *session, text string) error {
	if strings.EqualFold(text, "/cancel") || text == "取消" {
		b.clearSession(m.From.ID)
		return b.tg.SendChat(ctx, m.Chat.ID, "已取消。", homeKeyboard())
	}
	switch s.Await {
	case awaitBaseURL:
		b.clearSession(m.From.ID)
		return b.setBaseURL(ctx, m.Chat.ID, m.From.ID, text)
	case awaitAPIKey:
		b.clearSession(m.From.ID)
		// try delete user message to reduce key leakage in chat history (best-effort; may fail)
		return b.setAPIKey(ctx, m.Chat.ID, m.From.ID, text)
	case awaitAddAcc:
		b.clearSession(m.From.ID)
		return b.addAccount(ctx, m.Chat.ID, m.From.ID, text)
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
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.BaseURL = u
		return nil
	})
	if err != nil {
		// maybe first create path already done
		_, err = b.users.GetOrCreate(userID, strconv.FormatInt(chatID, 10), "", "")
		if err != nil {
			return err
		}
		_, err = b.users.Update(userID, func(p *userstore.Profile) error {
			p.BaseURL = u
			return nil
		})
	}
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
	}
	return b.tg.SendChat(ctx, chatID, "✅ Base URL 已保存: "+telegram.Code(u), connKeyboard())
}

func (b *Bot) setAPIKey(ctx context.Context, chatID, userID int64, raw string) error {
	key := strings.TrimSpace(raw)
	if key == "" {
		return b.tg.SendChat(ctx, chatID, "密钥不能为空", connKeyboard())
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.AdminAPIKey = key
		p.JWT = ""
		return nil
	})
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "保存失败: "+telegram.EscapeHTML(err.Error()), nil)
	}
	return b.tg.SendChat(ctx, chatID, "✅ API Key 已保存: "+telegram.Code(userstore.MaskKey(key)), connKeyboard())
}

func (b *Bot) addAccount(ctx context.Context, chatID, userID int64, raw string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return b.tg.SendChat(ctx, chatID, "无效的账号 ID，请输入正整数", b.accountsKeyboard(userID))
	}
	// try resolve name via user connection
	name := ""
	if p, ok := b.users.Get(userID); ok && p.HasConnection() {
		if cli, err := sub2api.NewClient(config.Sub2APIConfig{
			BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 10 * time.Second,
		}); err == nil {
			if acc, err := cli.GetAccount(ctx, id); err == nil && acc != nil {
				name = acc.Name
			}
		}
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
		return b.tg.SendChat(ctx, chatID, "添加失败: "+telegram.EscapeHTML(err.Error()), b.accountsKeyboard(userID))
	}
	label := fmt.Sprintf("#%d", id)
	if name != "" {
		label += " " + name
	}
	return b.tg.SendChat(ctx, chatID, "✅ 已添加监控账号 "+telegram.Code(label), b.accountsKeyboard(userID))
}

func (b *Bot) delAccount(ctx context.Context, chatID, userID int64, raw string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return b.tg.SendChat(ctx, chatID, "无效的账号 ID", b.accountsKeyboard(userID))
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
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "删除失败: "+telegram.EscapeHTML(err.Error()), b.accountsKeyboard(userID))
	}
	return b.tg.SendChat(ctx, chatID, "✅ 已移除账号 "+telegram.Code(fmt.Sprintf("#%d", id)), b.accountsKeyboard(userID))
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
	if _, err := cli.GetDashboardStats(ctx); err != nil {
		return "⚠️ health 正常，但 Admin API 鉴权失败: " + telegram.EscapeHTML(err.Error())
	}
	return "✅ 连接成功（health + dashboard OK）"
}

func (b *Bot) forceCheck(ctx context.Context, chatID, userID int64) error {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() {
		return b.tg.SendChat(ctx, chatID, "请先配置连接（Base URL + API Key）", homeKeyboard())
	}
	if len(p.Accounts) == 0 {
		return b.tg.SendChat(ctx, chatID, "请先添加监控账号", homeKeyboard())
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 20 * time.Second,
	})
	if err != nil {
		return b.tg.SendChat(ctx, chatID, "错误: "+telegram.EscapeHTML(err.Error()), nil)
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("用量快照") + "\n")
	src := p.EffectiveSource()
	for _, a := range p.Accounts {
		if !a.IsEnabled() {
			continue
		}
		usage, err := cli.GetAccountUsage(ctx, a.ID, src, false)
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("#%d", a.ID)
		}
		if err != nil {
			fmt.Fprintf(&bld, "• %s: %s\n", telegram.EscapeHTML(name), telegram.EscapeHTML(err.Error()))
			continue
		}
		fmt.Fprintf(&bld, "• %s\n", telegram.EscapeHTML(fmt.Sprintf("#%d %s", a.ID, name)))
		for _, w := range usage.Windows() {
			fmt.Fprintf(&bld, "  - %s: %s\n", telegram.EscapeHTML(w.Window), telegram.Code(fmt.Sprintf("%.1f%%", w.Utilization)))
		}
		if len(usage.Windows()) == 0 {
			bld.WriteString("  - (无窗口数据)\n")
		}
	}
	return b.tg.SendChat(ctx, chatID, bld.String(), homeKeyboard())
}

// ----- views -----

func (b *Bot) sendHome(ctx context.Context, chatID, userID int64) error {
	return b.tg.SendChat(ctx, chatID, b.homeText(userID), homeKeyboard())
}

func (b *Bot) sendStatus(ctx context.Context, chatID, userID int64) error {
	text, kb := b.statusView(userID)
	return b.tg.SendChat(ctx, chatID, text, kb)
}

func (b *Bot) homeText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString(telegram.Bold("Sub2API 监控面板") + "\n")
	bld.WriteString("实例: " + telegram.Code(b.cfg.Instance) + "\n\n")
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
	fmt.Fprintf(&bld, "监控账号: %s 个\n", telegram.Code(strconv.Itoa(len(p.Accounts))))
	if !p.HasConnection() {
		bld.WriteString("\n⚠️ 请先配置连接信息")
	} else if len(p.Accounts) == 0 {
		bld.WriteString("\n⚠️ 请添加至少一个监控账号")
	} else if p.Enabled {
		bld.WriteString("\n✅ 后台将按间隔检查用量并私聊告警")
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
	bld.WriteString("\n密钥仅存于本机 users.json，不会写入 git。")
	return bld.String()
}

func (b *Bot) accountsText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString(telegram.Bold("监控账号") + "\n\n")
	if p == nil || len(p.Accounts) == 0 {
		bld.WriteString("列表为空。点击「添加账号」。")
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
	return bld.String()
}

func (b *Bot) statusView(userID int64) (string, *telegram.InlineKeyboardMarkup) {
	return b.homeText(userID), homeKeyboard()
}

func (b *Bot) editOrSend(ctx context.Context, chatID, msgID int64, text string, kb *telegram.InlineKeyboardMarkup) error {
	if msgID > 0 {
		if err := b.tg.EditMessage(ctx, chatID, msgID, text, kb); err == nil {
			return nil
		}
	}
	return b.tg.SendChat(ctx, chatID, text, kb)
}

// ----- keyboards -----

func homeKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("📊 状态", "status"), telegram.Btn("🔌 连接配置", "cfg_conn")},
			{telegram.Btn("👤 监控账号", "cfg_acc"), telegram.Btn("▶️ 立即检查", "check_now")},
			{telegram.Btn("🔁 开关监控", "toggle_mon"), telegram.Btn("📡 切换数据源", "toggle_src")},
			{telegram.Btn("❓ 帮助", "help")},
		},
	}
}

func connKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("设置 Base URL", "set_base"), telegram.Btn("设置 API Key", "set_key")},
			{telegram.Btn("测试连接", "test_conn")},
			{telegram.Btn("« 返回", "home")},
		},
	}
}

// accountsKeyboard builds dynamic account action buttons for a user.
func (b *Bot) accountsKeyboard(userID int64) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("➕ 添加账号", "add_acc")},
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
				telegram.Btn(tog+" "+label, fmt.Sprintf("tog_acc:%d", a.ID)),
				telegram.Btn("🗑", fmt.Sprintf("del_acc:%d", a.ID)),
			})
		}
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 返回", "home")})
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
/check — 立即拉取用量快照

` + telegram.Bold("连接") + `
/setbase &lt;url&gt; — 设置 Sub2API Base URL
/setkey &lt;key&gt; — 设置 Admin API Key

` + telegram.Bold("账号") + `
/addaccount &lt;id&gt; — 添加监控账号
/delaccount &lt;id&gt; — 删除监控账号

` + telegram.Bold("说明") + `
• 每位用户独立保存 base_url / key / 账号列表
• 用量达到阈值时 Bot 会私聊提醒你
• 配置存于服务器 data/users.json
`
}

// ----- session + allowlist -----

func (b *Bot) allowed(userID int64) bool {
	list := b.cfg.Telegram.Panel.AllowUserIDs
	if len(list) == 0 {
		// empty allowlist = allow all (or only default chat owner)
		if b.cfg.Telegram.Panel.AllowAll {
			return true
		}
		// if default chat_id is a user id matching, allow that user
		if b.cfg.Telegram.ChatID != "" {
			if id, err := strconv.ParseInt(b.cfg.Telegram.ChatID, 10, 64); err == nil && id == userID {
				return true
			}
		}
		// no allowlist and not allow_all: still allow if open registration
		return b.cfg.Telegram.Panel.AllowAll || len(list) == 0 && b.cfg.Telegram.Panel.OpenRegistration
	}
	for _, id := range list {
		if id == userID {
			return true
		}
	}
	return false
}

func (b *Bot) setAwait(userID int64, kind string, accountID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[userID] = &session{Await: kind, UpdatedAt: time.Now(), AccountID: accountID}
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
