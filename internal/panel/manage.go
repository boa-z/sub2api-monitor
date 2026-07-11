package panel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

func manageKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("📚 账号浏览", "mgr_browse"), telegram.Btn("🔎 搜索账号", "mgr_search")},
			{telegram.Btn("🧹 批量清错", "mgr_bulk_clear"), telegram.Btn("♻️ 批量恢复", "mgr_bulk_recover")},
			{telegram.Btn("▶️ 批量开调度", "mgr_bulk_sched_on"), telegram.Btn("📋 异常账号", "ops_badacc")},
			{telegram.Btn("👥 用户", "mgr_users"), telegram.Btn("🏷 分组", "mgr_groups")},
			{telegram.Btn("« 返回主面板", "home")},
		},
	}
}

func (b *Bot) manageMenuText() string {
	return telegram.Bold("账号管理") + `

用你的 Admin API 管理实例（只对你配置的连接生效）：

• 账号浏览 — 状态/平台筛选、搜索、分页
• 批量清错 / 批量恢复 / 批量开调度 — 对 error 账号批量处理（需确认）
• 用户 / 分组 — 只读列表
• 异常账号 — error 列表，点进管理 / 一键监控

进入账号后可执行：
切换调度 · 启停状态 · 测试连通 · 清错误/限速 · 恢复/刷新
临时停调度 · 重置额度 · 加入监控 · 实时用量
`
}

func (b *Bot) showManageMenu(ctx context.Context, chatID, msgID int64) error {
	return b.editOrSend(ctx, chatID, msgID, b.manageMenuText(), manageKeyboard())
}

// parseBrowseFilter decodes browser status tokens.
// Forms: all|active|error|... | search:kw | plat:openai | plat:openai:active
func parseBrowseFilter(status string) sub2api.AccountListFilter {
	f := sub2api.AccountListFilter{}
	s := strings.TrimSpace(status)
	if s == "" || s == "all" {
		return f
	}
	if strings.HasPrefix(s, "search:") {
		f.Search = strings.TrimPrefix(s, "search:")
		return f
	}
	if strings.HasPrefix(s, "plat:") {
		rest := strings.TrimPrefix(s, "plat:")
		parts := strings.SplitN(rest, ":", 2)
		f.Platform = parts[0]
		if len(parts) == 2 && parts[1] != "" && parts[1] != "all" {
			f.Status = parts[1]
		}
		return f
	}
	if s == "unsched" || s == "rate_limited" {
		// special client-side filters
		return f
	}
	f.Status = s
	return f
}

func (b *Bot) showAccountBrowser(ctx context.Context, chatID, msgID, userID int64, status string, page int) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	if page < 0 {
		page = 0
	}
	const pageSize = 8
	filterToken := status
	if filterToken == "" {
		filterToken = "all"
	}

	var items []sub2api.Account
	var total int64

	switch {
	case status == "unsched":
		all, tot, err := cli.ListAccountsEx(ctx, page+1, pageSize, sub2api.AccountListFilter{})
		if err != nil {
			return b.editOrSend(ctx, chatID, msgID, "列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
		}
		total = tot
		for _, a := range all {
			if !a.Schedulable {
				items = append(items, a)
			}
		}
	case status == "rate_limited":
		all, tot, err := cli.ListAccountsEx(ctx, page+1, 30, sub2api.AccountListFilter{})
		if err != nil {
			return b.editOrSend(ctx, chatID, msgID, "列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
		}
		total = tot
		for _, a := range all {
			if a.RateLimitedAt != nil || a.OverloadUntil != nil || strings.Contains(strings.ToLower(a.Status), "rate") {
				items = append(items, a)
			}
		}
		if len(items) > pageSize {
			items = items[:pageSize]
		}
	default:
		f := parseBrowseFilter(status)
		var err error
		items, total, err = cli.ListAccountsEx(ctx, page+1, pageSize, f)
		if err != nil {
			return b.editOrSend(ctx, chatID, msgID, "列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
		}
	}

	var bld strings.Builder
	title := filterToken
	switch {
	case title == "" || title == "all":
		title = "全部"
	case strings.HasPrefix(title, "search:"):
		title = "搜索:" + strings.TrimPrefix(title, "search:")
	case strings.HasPrefix(title, "plat:"):
		title = "平台:" + strings.TrimPrefix(title, "plat:")
	}
	bld.WriteString(telegram.Bold("账号浏览") + " · " + telegram.Code(title) + "\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共约 %s\n点账号进入管理\n\n", page+1, telegram.Code(itoa(total)))
	if len(items) == 0 {
		bld.WriteString("本页无账号。")
	}

	// status filter row
	kbRows := [][]telegram.InlineKeyboardButton{
		{
			telegram.Btn(filterLabel("全部", status, "all"), "mgr_browse:all:0"),
			telegram.Btn(filterLabel("active", status, "active"), "mgr_browse:active:0"),
			telegram.Btn(filterLabel("error", status, "error"), "mgr_browse:error:0"),
		},
		{
			telegram.Btn(filterLabel("停调度", status, "unsched"), "mgr_browse:unsched:0"),
			telegram.Btn(filterLabel("限速", status, "rate_limited"), "mgr_browse:rate_limited:0"),
			telegram.Btn("🔎 搜索", "mgr_search"),
		},
		{
			telegram.Btn(filterLabel("openai", status, "plat:openai"), "mgr_browse:plat|openai:0"),
			telegram.Btn(filterLabel("anthropic", status, "plat:anthropic"), "mgr_browse:plat|anthropic:0"),
		},
	}

	// Note: platform callbacks use mgr_browse:plat:openai:0 — need custom parse in bot.go
	// For nav we encode statusOrAll carefully.

	for _, a := range items {
		label := fmt.Sprintf("#%d [%s] %s", a.ID, a.Status, truncateRunes(a.Name, 12))
		if a.Platform != "" {
			label = fmt.Sprintf("#%d %s/%s %s", a.ID, truncateRunes(a.Platform, 6), a.Status, truncateRunes(a.Name, 10))
		}
		kbRows = append(kbRows, []telegram.InlineKeyboardButton{
			telegram.Btn(label, fmt.Sprintf("mgr_acc:%d", a.ID)),
		})
		fmt.Fprintf(&bld, "• #%d %s [%s/%s] %s\n",
			a.ID,
			telegram.EscapeHTML(truncateRunes(a.Name, 16)),
			telegram.EscapeHTML(a.Platform),
			telegram.EscapeHTML(a.Status),
			schedLabel(a.Schedulable),
		)
	}
	nav := []telegram.InlineKeyboardButton{}
	token := browseToken(status)
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("mgr_browse:%s:%d", token, page-1)))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("mgr_browse:%s:%d", token, page+1)))
	}
	if len(nav) > 0 {
		kbRows = append(kbRows, nav)
	}
	kbRows = append(kbRows, []telegram.InlineKeyboardButton{
		telegram.Btn("« 管理菜单", "mgr_menu"),
		telegram.Btn("« 主面板", "home"),
	})
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: kbRows})
}

// browseToken encodes status for callback_data (avoid extra colons for search).
func browseToken(status string) string {
	s := strings.TrimSpace(status)
	if s == "" {
		return "all"
	}
	// search:kw uses colon — encode as search|kw for callback
	if strings.HasPrefix(s, "search:") {
		return "search|" + strings.TrimPrefix(s, "search:")
	}
	if strings.HasPrefix(s, "plat:") {
		// plat:openai or plat:openai:active
		return strings.ReplaceAll(s, ":", "|")
	}
	return s
}

// parseBrowseCallback parses rest after mgr_browse:
// formats: all:0 | active:1 | search|kw:0 | plat|openai:0 | plat|openai|active:0
func parseBrowseCallback(rest string) (status string, page int) {
	status = "all"
	page = 0
	if rest == "" {
		return
	}
	// page is always last segment after final ':'
	// but search|kw may contain no extra colon if we use form search|kw:page
	parts := strings.Split(rest, ":")
	if len(parts) == 1 {
		status = decodeBrowseToken(parts[0])
		return
	}
	// last is page
	page, _ = strconv.Atoi(parts[len(parts)-1])
	token := strings.Join(parts[:len(parts)-1], ":")
	// also support plat:openai:0 (colon-based platform without pipe)
	status = decodeBrowseToken(token)
	return
}

func decodeBrowseToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "all"
	}
	if strings.HasPrefix(token, "search|") {
		return "search:" + strings.TrimPrefix(token, "search|")
	}
	if strings.HasPrefix(token, "plat|") {
		return "plat:" + strings.ReplaceAll(strings.TrimPrefix(token, "plat|"), "|", ":")
	}
	// already plat:openai form from buttons
	if strings.HasPrefix(token, "plat:") {
		return token
	}
	return token
}

func filterLabel(label, cur, val string) string {
	curN := normalizeFilterForLabel(cur)
	valN := normalizeFilterForLabel(val)
	if curN == valN || (curN == "" && valN == "all") {
		return "• " + label
	}
	return label
}

func normalizeFilterForLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "all"
	}
	if strings.HasPrefix(s, "plat:") {
		// highlight by platform only
		rest := strings.TrimPrefix(s, "plat:")
		return "plat:" + strings.Split(rest, ":")[0]
	}
	return s
}

func statusOrAll(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

func (b *Bot) showManageAccount(ctx context.Context, chatID, msgID, userID, accountID int64, notice string) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	acc, err := cli.GetAccount(ctx, accountID)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "读取账号失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString(telegram.Bold(fmt.Sprintf("管理账号 #%d", accountID)) + "\n\n")
	fmt.Fprintf(&bld, "名称: %s\n", telegram.Code(acc.Name))
	fmt.Fprintf(&bld, "平台/类型: %s / %s\n", telegram.Code(acc.Platform), telegram.Code(acc.Type))
	fmt.Fprintf(&bld, "状态: %s\n", telegram.Code(acc.Status))
	fmt.Fprintf(&bld, "可调度: %s\n", telegram.Code(fmt.Sprintf("%v", acc.Schedulable)))
	if acc.ErrorMessage != "" {
		fmt.Fprintf(&bld, "错误: %s\n", telegram.EscapeHTML(truncateRunes(acc.ErrorMessage, 160)))
	}
	if acc.RateLimitedAt != nil {
		fmt.Fprintf(&bld, "限速于: %s\n", telegram.Code(acc.RateLimitedAt.Local().Format("01-02 15:04")))
	}
	if acc.RateLimitResetAt != nil {
		fmt.Fprintf(&bld, "限速重置: %s\n", telegram.Code(acc.RateLimitResetAt.Local().Format("01-02 15:04")))
	}
	if acc.OverloadUntil != nil {
		fmt.Fprintf(&bld, "过载至: %s\n", telegram.Code(acc.OverloadUntil.Local().Format("01-02 15:04")))
	}
	if acc.TempUnschedulableUntil != nil {
		fmt.Fprintf(&bld, "临时停调度至: %s\n", telegram.Code(acc.TempUnschedulableUntil.Local().Format("01-02 15:04")))
		if acc.TempUnschedulableReason != "" {
			fmt.Fprintf(&bld, "原因: %s\n", telegram.EscapeHTML(truncateRunes(acc.TempUnschedulableReason, 80)))
		}
	}
	if temp, err := cli.GetTempUnschedulable(ctx, accountID); err == nil && temp != nil && temp.Active {
		bld.WriteString("临时停调度: " + telegram.Code("active") + "\n")
	}

	watched := false
	if p, ok := b.users.Get(userID); ok {
		for _, a := range p.Accounts {
			if a.ID == accountID {
				watched = true
				break
			}
		}
	}
	fmt.Fprintf(&bld, "面板监控: %s\n", telegram.Code(map[bool]string{true: "已添加", false: "未添加"}[watched]))

	schedBtn := "⏸ 停止调度"
	schedData := fmt.Sprintf("mgr_act:confirm_unsched:%d", accountID)
	if !acc.Schedulable {
		schedBtn = "▶️ 开启调度"
		schedData = fmt.Sprintf("mgr_act:sched:%d", accountID)
	}
	watchBtn := "➕ 加入监控"
	watchData := fmt.Sprintf("mgr_act:watch:%d", accountID)
	if watched {
		watchBtn = "🗑 移出监控"
		watchData = fmt.Sprintf("mgr_act:unwatch:%d", accountID)
	}

	// enable/disable based on status
	statusBtn := "🚫 禁用账号"
	statusData := fmt.Sprintf("mgr_act:confirm_disable:%d", accountID)
	if strings.EqualFold(acc.Status, "disabled") {
		statusBtn = "✅ 启用账号"
		statusData = fmt.Sprintf("mgr_act:enable:%d", accountID)
	}

	// enrich with quick usage snapshot (passive)
	if usage, err := cli.GetAccountUsage(ctx, accountID, "passive", false); err == nil && usage != nil {
		bld.WriteString("\n" + telegram.Bold("用量快照") + "\n")
		for _, w := range usage.Windows() {
			line := fmt.Sprintf("• %s %s%%", telegram.Code(w.Window), telegram.Code(fmt.Sprintf("%.1f", w.Utilization)))
			if w.ResetsAt != nil {
				line += " · 重置 " + telegram.Code(w.ResetsAt.Local().Format("01-02 15:04"))
			}
			bld.WriteString(line + "\n")
		}
		if today, err := cli.GetAccountTodayStats(ctx, accountID); err == nil && today != nil {
			fmt.Fprintf(&bld, "今日: req %s · token %s · cost %s\n",
				telegram.Code(itoa(today.Requests)),
				telegram.Code(formatCompactInt(today.Tokens)),
				telegram.Code(fmt.Sprintf("%.2f", today.Cost)),
			)
		}
	}

	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn(schedBtn, schedData), telegram.Btn(watchBtn, watchData)},
			{
				telegram.Btn(statusBtn, statusData),
				telegram.Btn("🧪 测试连通", fmt.Sprintf("mgr_act:test:%d", accountID)),
			},
			{
				telegram.Btn("🧹 清错误", fmt.Sprintf("mgr_act:clear_err:%d", accountID)),
				telegram.Btn("⏱ 清限速", fmt.Sprintf("mgr_act:clear_rl:%d", accountID)),
			},
			{
				telegram.Btn("♻️ 恢复状态", fmt.Sprintf("mgr_act:recover:%d", accountID)),
				telegram.Btn("🔄 刷新凭据", fmt.Sprintf("mgr_act:refresh:%d", accountID)),
			},
			{
				telegram.Btn("⏳ 临时停调度", fmt.Sprintf("mgr_act:temp_menu:%d", accountID)),
				telegram.Btn("🚫 清临时停", fmt.Sprintf("mgr_act:clear_temp:%d", accountID)),
			},
			{
				telegram.Btn("📊 重置额度", fmt.Sprintf("mgr_act:confirm_reset_quota:%d", accountID)),
				telegram.Btn("📡 实时用量", fmt.Sprintf("acc_live:%d", accountID)),
			},
			{telegram.Btn("« 账号浏览", "mgr_browse"), telegram.Btn("« 管理菜单", "mgr_menu")},
		},
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
}

func (b *Bot) handleManageAction(ctx context.Context, chatID, msgID, userID int64, action string, accountID int64) error {
	// confirmation / menu steps (no API call yet)
	switch action {
	case "confirm_unsched":
		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{
					telegram.Btn("✅ 确认停止调度", fmt.Sprintf("mgr_act:unsched:%d", accountID)),
					telegram.Btn("取消", fmt.Sprintf("mgr_acc:%d", accountID)),
				},
			},
		}
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("确认停止账号 #%d 的调度？\n停止后新请求将不再分配到该账号。", accountID),
			kb)
	case "confirm_disable":
		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{
					telegram.Btn("✅ 确认禁用", fmt.Sprintf("mgr_act:disable:%d", accountID)),
					telegram.Btn("取消", fmt.Sprintf("mgr_acc:%d", accountID)),
				},
			},
		}
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("确认禁用账号 #%d？\n禁用后账号将不可用，直到重新启用。", accountID),
			kb)
	case "confirm_reset_quota":
		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{
					telegram.Btn("✅ 确认重置额度", fmt.Sprintf("mgr_act:reset_quota:%d", accountID)),
					telegram.Btn("取消", fmt.Sprintf("mgr_acc:%d", accountID)),
				},
			},
		}
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("确认重置账号 #%d 的额度计数？\n部分实例可能不可逆，请谨慎。", accountID),
			kb)
	case "temp_menu":
		kb := &telegram.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegram.InlineKeyboardButton{
				{
					telegram.Btn("15 分钟", fmt.Sprintf("mgr_act:temp:15m:%d", accountID)),
					telegram.Btn("1 小时", fmt.Sprintf("mgr_act:temp:1h:%d", accountID)),
				},
				{
					telegram.Btn("6 小时", fmt.Sprintf("mgr_act:temp:6h:%d", accountID)),
					telegram.Btn("24 小时", fmt.Sprintf("mgr_act:temp:24h:%d", accountID)),
				},
				{telegram.Btn("取消", fmt.Sprintf("mgr_acc:%d", accountID))},
			},
		}
		return b.editOrSend(ctx, chatID, msgID,
			fmt.Sprintf("为账号 #%d 设置临时停调度时长：", accountID),
			kb)
	}

	cli, _, err := b.userClient(userID, 25*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	notice := ""
	switch action {
	case "sched":
		if _, err := cli.SetSchedulable(ctx, accountID, true); err != nil {
			notice = "❌ 开启调度失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已开启调度"
		}
	case "unsched":
		if _, err := cli.SetSchedulable(ctx, accountID, false); err != nil {
			notice = "❌ 停止调度失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已停止调度"
		}
	case "enable":
		if _, err := cli.SetAccountStatus(ctx, accountID, "active"); err != nil {
			notice = "❌ 启用失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已启用（status=active）"
		}
	case "disable":
		if _, err := cli.SetAccountStatus(ctx, accountID, "disabled"); err != nil {
			notice = "❌ 禁用失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已禁用（status=disabled）"
		}
	case "test":
		raw, err := cli.TestAccount(ctx, accountID)
		if err != nil {
			notice = "❌ 测试失败: " + telegram.EscapeHTML(err.Error())
		} else {
			s := string(raw)
			if len(s) > 280 {
				s = s[:280] + "…"
			}
			notice = "✅ 测试完成: " + telegram.Code(truncateRunes(s, 200))
		}
	case "clear_err":
		if _, err := cli.ClearAccountError(ctx, accountID); err != nil {
			notice = "❌ 清除错误失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已清除错误状态"
		}
	case "clear_rl":
		if _, err := cli.ClearAccountRateLimit(ctx, accountID); err != nil {
			notice = "❌ 清除限速失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已清除限速"
		}
	case "recover":
		if _, err := cli.RecoverAccountState(ctx, accountID); err != nil {
			notice = "❌ 恢复状态失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已请求恢复状态"
		}
	case "refresh":
		if _, err := cli.RefreshAccount(ctx, accountID); err != nil {
			notice = "❌ 刷新失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已刷新账号/凭据"
		}
	case "clear_temp":
		if err := cli.ClearTempUnschedulable(ctx, accountID); err != nil {
			notice = "❌ 清除临时停调度失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已清除临时停调度"
		}
	case "watch":
		if label, err := b.addAccountMutate(ctx, chatID, userID, strconv.FormatInt(accountID, 10)); err != nil {
			notice = "❌ 加入监控失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已加入监控 " + telegram.Code(label)
		}
	case "unwatch":
		if _, err := b.delAccountMutate(userID, strconv.FormatInt(accountID, 10)); err != nil {
			notice = "❌ 移出监控失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已移出监控"
		}
	case "reset_quota":
		if _, err := cli.ResetAccountQuota(ctx, accountID); err != nil {
			notice = "❌ 重置额度失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已请求重置额度"
		}
	default:
		notice = "未知操作"
	}
	return b.showManageAccount(ctx, chatID, msgID, userID, accountID, notice)
}

// bulkClearErrors prompts confirmation then clears error state for up to N error accounts.
func (b *Bot) bulkClearErrors(ctx context.Context, chatID, msgID, userID int64) error {
	return b.bulkClearErrorsPrompt(ctx, chatID, msgID, userID)
}

func (b *Bot) bulkClearErrorsPrompt(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, total, err := cli.ListAccountsEx(ctx, 1, 30, sub2api.AccountListFilter{Status: "error"})
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取 error 账号失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	if len(items) == 0 {
		return b.editOrSend(ctx, chatID, msgID, "✅ 当前没有 status=error 的账号。", manageKeyboard())
	}
	const maxOps = 20
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
	var sample strings.Builder
	for i := 0; i < n && i < 8; i++ {
		a := items[i]
		fmt.Fprintf(&sample, "• #%d %s\n", a.ID, telegram.EscapeHTML(truncateRunes(a.Name, 16)))
	}
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn(fmt.Sprintf("✅ 确认清错 %d 个", n), "mgr_bulk_clear_go")},
			{telegram.Btn("取消", "mgr_menu")},
		},
	}
	msg := fmt.Sprintf("%s\n\n将清除约 %s 个 error 账号中的前 %s 个：\n%s\n共约 %s 个 error。",
		telegram.Bold("批量清错确认"),
		telegram.Code(itoa(total)),
		telegram.Code(strconv.Itoa(n)),
		sample.String(),
		telegram.Code(itoa(total)),
	)
	return b.editOrSend(ctx, chatID, msgID, msg, kb)
}

func (b *Bot) bulkClearErrorsExecute(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 30*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, total, err := cli.ListAccountsEx(ctx, 1, 30, sub2api.AccountListFilter{Status: "error"})
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取 error 账号失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	if len(items) == 0 {
		return b.editOrSend(ctx, chatID, msgID, "✅ 当前没有 status=error 的账号。", manageKeyboard())
	}
	okN, failN := 0, 0
	var fails []string
	const maxOps = 20
	for i, a := range items {
		if i >= maxOps {
			break
		}
		if _, err := cli.ClearAccountError(ctx, a.ID); err != nil {
			failN++
			if len(fails) < 5 {
				fails = append(fails, fmt.Sprintf("#%d %s", a.ID, truncateRunes(err.Error(), 40)))
			}
		} else {
			okN++
		}
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("批量清错结果") + "\n\n")
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
	fmt.Fprintf(&bld, "error 账号约 %s 个（本次处理 %d）\n", telegram.Code(itoa(total)), n)
	fmt.Fprintf(&bld, "✅ 成功 %s · ❌ 失败 %s\n", telegram.Code(strconv.Itoa(okN)), telegram.Code(strconv.Itoa(failN)))
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + telegram.EscapeHTML(f) + "\n")
		}
	}
	if total > int64(maxOps) {
		bld.WriteString("\n还有更多 error 账号，可再次执行「批量清错」。")
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), manageKeyboard())
}

// applyTempUnschedulable sets a temporary unschedulable hold for duration.
func (b *Bot) applyTempUnschedulable(ctx context.Context, chatID, msgID, userID, accountID int64, durLabel string) error {
	sec := parseDurationLabel(durLabel)
	if sec <= 0 {
		return b.showManageAccount(ctx, chatID, msgID, userID, accountID, "❌ 无效时长")
	}
	cli, _, err := b.userClient(userID, 20*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	notice := ""
	if _, err := cli.SetTempUnschedulable(ctx, accountID, sec, "panel:"+durLabel); err != nil {
		notice = "❌ 设置临时停调度失败: " + telegram.EscapeHTML(err.Error())
	} else {
		notice = fmt.Sprintf("✅ 已设置临时停调度 %s", telegram.Code(durLabel))
	}
	return b.showManageAccount(ctx, chatID, msgID, userID, accountID, notice)
}

func parseDurationLabel(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "15m":
		return 15 * 60
	case "1h":
		return 60 * 60
	case "6h":
		return 6 * 60 * 60
	case "24h":
		return 24 * 60 * 60
	default:
		return 0
	}
}

// bulkAccountActionPrompt previews error accounts then asks confirm for bulk action.
func (b *Bot) bulkAccountActionPrompt(ctx context.Context, chatID, msgID, userID int64, action, title, confirmData string) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, total, err := cli.ListAccountsEx(ctx, 1, 30, sub2api.AccountListFilter{Status: "error"})
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取 error 账号失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	if len(items) == 0 {
		return b.editOrSend(ctx, chatID, msgID, "✅ 当前没有 status=error 的账号。", manageKeyboard())
	}
	const maxOps = 20
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
	var sample strings.Builder
	for i := 0; i < n && i < 8; i++ {
		a := items[i]
		fmt.Fprintf(&sample, "• #%d %s\n", a.ID, telegram.EscapeHTML(truncateRunes(a.Name, 16)))
	}
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn(fmt.Sprintf("✅ 确认处理 %d 个", n), confirmData)},
			{telegram.Btn("取消", "mgr_menu")},
		},
	}
	msg := fmt.Sprintf("%s\n\n将对约 %s 个 error 账号中的前 %s 个执行「%s」：\n%s\n共约 %s 个 error。",
		telegram.Bold(title),
		telegram.Code(itoa(total)),
		telegram.Code(strconv.Itoa(n)),
		telegram.EscapeHTML(action),
		sample.String(),
		telegram.Code(itoa(total)),
	)
	return b.editOrSend(ctx, chatID, msgID, msg, kb)
}

func (b *Bot) bulkRecoverPrompt(ctx context.Context, chatID, msgID, userID int64) error {
	return b.bulkAccountActionPrompt(ctx, chatID, msgID, userID, "恢复状态", "批量恢复确认", "mgr_bulk_recover_go")
}

func (b *Bot) bulkSchedOnPrompt(ctx context.Context, chatID, msgID, userID int64) error {
	return b.bulkAccountActionPrompt(ctx, chatID, msgID, userID, "开启调度", "批量开调度确认", "mgr_bulk_sched_on_go")
}

func (b *Bot) bulkAccountActionExecute(ctx context.Context, chatID, msgID, userID int64, action string) error {
	cli, _, err := b.userClient(userID, 40*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, total, err := cli.ListAccountsEx(ctx, 1, 30, sub2api.AccountListFilter{Status: "error"})
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取 error 账号失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	if len(items) == 0 {
		return b.editOrSend(ctx, chatID, msgID, "✅ 当前没有 status=error 的账号。", manageKeyboard())
	}
	okN, failN := 0, 0
	var fails []string
	const maxOps = 20
	for i, a := range items {
		if i >= maxOps {
			break
		}
		var opErr error
		switch action {
		case "recover":
			_, opErr = cli.RecoverAccountState(ctx, a.ID)
		case "sched_on":
			_, opErr = cli.SetSchedulable(ctx, a.ID, true)
		default:
			opErr = fmt.Errorf("unknown action")
		}
		if opErr != nil {
			failN++
			if len(fails) < 5 {
				fails = append(fails, fmt.Sprintf("#%d %s", a.ID, truncateRunes(opErr.Error(), 40)))
			}
		} else {
			okN++
		}
	}
	title := map[string]string{"recover": "批量恢复结果", "sched_on": "批量开调度结果"}[action]
	if title == "" {
		title = "批量操作结果"
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold(title) + "\n\n")
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
	fmt.Fprintf(&bld, "error 账号约 %s 个（本次处理 %d）\n", telegram.Code(itoa(total)), n)
	fmt.Fprintf(&bld, "✅ 成功 %s · ❌ 失败 %s\n", telegram.Code(strconv.Itoa(okN)), telegram.Code(strconv.Itoa(failN)))
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + telegram.EscapeHTML(f) + "\n")
		}
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), manageKeyboard())
}

// seedConnectionFromGlobal copies global sub2api config into the user's panel profile.
// Admin-only. Shares the global Admin key with this Telegram user — use carefully.
func (b *Bot) seedConnectionFromGlobal(ctx context.Context, chatID, msgID, userID int64) error {
	base := strings.TrimSpace(b.cfg.Sub2API.BaseURL)
	key := strings.TrimSpace(b.cfg.Sub2API.AdminAPIKey)
	jwt := strings.TrimSpace(b.cfg.Sub2API.JWT)
	if base == "" || (key == "" && jwt == "") {
		return b.editOrSend(ctx, chatID, msgID,
			"❌ 全局 sub2api 未配置完整（需要 base_url + admin_api_key/jwt）。\n请手动设置连接，或在 config.yaml 填写全局凭证。",
			connKeyboardFor(true))
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.BaseURL = strings.TrimRight(base, "/")
		p.AdminAPIKey = key
		p.JWT = jwt
		return nil
	})
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "写入失败: "+telegram.EscapeHTML(err.Error()), connKeyboardFor(true))
	}
	msg := "✅ 已导入全局连接配置\n\n" + b.connText(userID) + "\n\n⚠️ 共享 Admin Key 拥有完整管理权限，请仅用于可信管理员。"
	return b.editOrSend(ctx, chatID, msgID, msg, connKeyboardFor(true))
}

func (b *Bot) showUsers(ctx context.Context, chatID, msgID, userID int64, page int) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	if page < 0 {
		page = 0
	}
	const pageSize = 12
	items, total, err := cli.ListUsers(ctx, page+1, pageSize)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "用户列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("用户列表") + "\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共 %s\n\n", page+1, telegram.Code(itoa(total)))
	for _, u := range items {
		fmt.Fprintf(&bld, "• #%d %s [%s] %s\n",
			u.ID,
			telegram.EscapeHTML(truncateRunes(u.Username, 16)),
			telegram.EscapeHTML(u.Role),
			telegram.EscapeHTML(u.Status),
		)
	}
	if len(items) == 0 {
		bld.WriteString("无用户。")
	}
	nav := []telegram.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("mgr_users:%d", page-1)))
	}
	if int64((page+1)*pageSize) < total {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("mgr_users:%d", page+1)))
	}
	rows := [][]telegram.InlineKeyboardButton{}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 管理菜单", "mgr_menu")})
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showGroups(ctx context.Context, chatID, msgID, userID int64, page int) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	if page < 0 {
		page = 0
	}
	const pageSize = 12
	items, total, err := cli.ListGroups(ctx, page+1, pageSize)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "分组列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("分组列表") + "\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共 %s\n\n", page+1, telegram.Code(itoa(total)))
	for _, g := range items {
		fmt.Fprintf(&bld, "• #%d %s [%s/%s] ×%.2f\n",
			g.ID,
			telegram.EscapeHTML(truncateRunes(g.Name, 20)),
			telegram.EscapeHTML(g.Platform),
			telegram.EscapeHTML(g.Status),
			g.RateMultiplier,
		)
	}
	if len(items) == 0 {
		bld.WriteString("无分组。")
	}
	nav := []telegram.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("mgr_groups:%d", page-1)))
	}
	if int64((page+1)*pageSize) < total {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("mgr_groups:%d", page+1)))
	}
	rows := [][]telegram.InlineKeyboardButton{}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 管理菜单", "mgr_menu")})
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func schedLabel(v bool) string {
	if v {
		return "可调度"
	}
	return "停调度"
}

func watchedLabel(v bool) string {
	if v {
		return "已添加"
	}
	return "未添加"
}
