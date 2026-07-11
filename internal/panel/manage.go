package panel

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/panel/browse"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

func manageKeyboard() *telegram.InlineKeyboardMarkup {
	return manageKeyboardFor(nil)
}

// manageKeyboardFor builds manage hub buttons. With stats, prioritizes triage actions.
func manageKeyboardFor(stats *sub2api.DashboardStats) *telegram.InlineKeyboardMarkup {
	badLabel := "📋 异常账号"
	healLabel := "🛠 批量一键修复"
	clearLabel := "🧹 批量清错"
	rlLabel := "⏱ 批量清限速"
	if stats != nil {
		if stats.ErrorAccounts > 0 {
			badLabel = fmt.Sprintf("📋 异常 %v", stats.ErrorAccounts)
			clearLabel = fmt.Sprintf("🧹 清错 %v", stats.ErrorAccounts)
		}
		if stats.RatelimitAccounts > 0 {
			rlLabel = fmt.Sprintf("⏱ 清限速 %v", stats.RatelimitAccounts)
		}
	}
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("📚 账号浏览", "mgr_browse"), telegram.Btn("🔎 搜索账号", "mgr_search")},
	}
	if stats != nil && (stats.ErrorAccounts > 0 || stats.RatelimitAccounts > 0) {
		rows = append(rows,
			[]telegram.InlineKeyboardButton{
				telegram.Btn(badLabel, "ops_badacc:error:0"),
				telegram.Btn(healLabel, "mgr_bulk_heal"),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn(clearLabel, "mgr_bulk_clear"),
				telegram.Btn(rlLabel, "mgr_bulk_clear_rl"),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn("♻️ 批量恢复", "mgr_bulk_recover"),
				telegram.Btn("▶️ 批量开调度", "mgr_bulk_sched_on"),
			},
		)
	} else {
		rows = append(rows,
			[]telegram.InlineKeyboardButton{
				telegram.Btn(clearLabel, "mgr_bulk_clear"),
				telegram.Btn("♻️ 批量恢复", "mgr_bulk_recover"),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn("▶️ 批量开调度", "mgr_bulk_sched_on"),
				telegram.Btn(rlLabel, "mgr_bulk_clear_rl"),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn(badLabel, "ops_badacc:error:0"),
				telegram.Btn(healLabel, "mgr_bulk_heal"),
			},
		)
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("👥 实例用户", "mgr_users"),
			telegram.Btn("🏷 分组", "mgr_groups"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("👤 面板用户", "pnl_users"),
			telegram.Btn("« 返回主面板", "home"),
		},
	)
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) manageMenuText(ctx context.Context, userID int64) string {
	var bld strings.Builder
	bld.WriteString(telegram.Bold("账号管理") + "\n\n")
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if line, issues := adminHealthSnapshot(ctx, cli); line != "" {
			bld.WriteString(line + "\n")
			if issues {
				bld.WriteString("建议优先处理异常/限速账号，或使用下方批量操作。\n")
			}
			bld.WriteString("\n")
		}
	}
	bld.WriteString(`用你的 Admin API 管理实例（只对你配置的连接生效）：

• 账号浏览 — 状态/平台筛选、搜索、分页
• 批量清错 / 恢复 / 开调度 / 清限速 / 一键修复 — 批量处理（需确认）
• 实例用户 / 分组 — Sub2API 只读列表
• 面板用户 — 本 Bot 多用户与角色（admin/user）
• 异常账号 — error/限速/停调度/汇总分标签分页，管理/实时/修复 / 一键监控

进入账号后可执行：
切换调度 · 启停状态 · 测试连通 · 清错误/限速 · 恢复/刷新
临时停调度 · 重置额度 · 加入监控 · 实时用量
`)
	return bld.String()
}

func (b *Bot) showManageMenu(ctx context.Context, chatID, msgID, userID int64) error {
	var stats *sub2api.DashboardStats
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil {
			stats = st
		}
	}
	return b.editOrSend(ctx, chatID, msgID, b.manageMenuText(ctx, userID), manageKeyboardFor(stats))
}

func (b *Bot) showAccountBrowser(ctx context.Context, chatID, msgID, userID int64, status string, page int) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	if page < 0 {
		page = 0
	}
	if status == "" {
		status = "all"
	}
	b.setBrowseView(userID, status, page)
	const pageSize = 8
	filterToken := status
	if filterToken == "" {
		filterToken = "all"
	}

	items, total, err := listBrowserAccounts(ctx, cli, status, page, pageSize)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}

	var bld strings.Builder
	title := browse.Title(filterToken)
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
			telegram.Btn(filterLabel("gemini", status, "plat:gemini"), "mgr_browse:plat|gemini:0"),
		},
		{
			telegram.Btn(filterLabel("grok", status, "plat:grok"), "mgr_browse:plat|grok:0"),
			telegram.Btn(filterLabel("antigravity", status, "plat:antigravity"), "mgr_browse:plat|antigravity:0"),
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
	// context actions for common problem filters
	switch status {
	case "error":
		kbRows = append(kbRows, []telegram.InlineKeyboardButton{
			telegram.Btn("🧹 批量清错", "mgr_bulk_clear"),
			telegram.Btn("🛠 一键修复", "mgr_bulk_heal"),
			telegram.Btn("♻️ 批量恢复", "mgr_bulk_recover"),
		})
	case "rate_limited":
		kbRows = append(kbRows, []telegram.InlineKeyboardButton{
			telegram.Btn("⏱ 批量清限速", "mgr_bulk_clear_rl"),
		})
	case "unsched":
		kbRows = append(kbRows, []telegram.InlineKeyboardButton{
			telegram.Btn("▶️ 批量开调度", "mgr_bulk_sched_on"),
		})
	}
	kbRows = append(kbRows, []telegram.InlineKeyboardButton{
		telegram.Btn("« 管理菜单", "mgr_menu"),
		telegram.Btn("« 主面板", "home"),
	})
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: kbRows})
}

// Thin wrappers keep existing call sites and tests on package panel.
func parseBrowseFilter(status string) sub2api.AccountListFilter {
	return browse.ParseFilter(status)
}

func listBrowserAccounts(ctx context.Context, cli *sub2api.Client, status string, page, pageSize int) ([]sub2api.Account, int64, error) {
	return browse.ListAccounts(ctx, cli, status, page, pageSize)
}

func isRateLimitedAccount(a sub2api.Account) bool {
	return browse.IsRateLimited(a)
}

func browseToken(status string) string { return browse.Token(status) }

func parseBrowseCallback(rest string) (string, int) { return browse.ParseCallback(rest) }

func decodeBrowseToken(token string) string { return browse.DecodeToken(token) }

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
				telegram.Btn("🛠 一键修复", fmt.Sprintf("mgr_act:heal:%d", accountID)),
				telegram.Btn("♻️ 恢复状态", fmt.Sprintf("mgr_act:recover:%d", accountID)),
			},
			{
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
			{b.manageBackButton(userID), telegram.Btn("« 管理菜单", "mgr_menu")},
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
	case "heal":
		notice = b.healAccount(ctx, cli, accountID)
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
	return b.bulkAccountActionExecute(ctx, chatID, msgID, userID, "clear_err")
}

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
	actionKey := inferBulkActionKey(confirmData)
	const maxOps = 20
	items, total, scopeLabel, err := b.loadBulkTargets(ctx, cli, actionKey, maxOps)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取账号失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	if len(items) == 0 {
		return b.editOrSend(ctx, chatID, msgID, "✅ 当前没有可处理的账号（"+telegram.EscapeHTML(scopeLabel)+"）。", manageKeyboard())
	}
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
	msg := fmt.Sprintf("%s\n\n范围: %s\n将对约 %s 个中的前 %s 个执行「%s」：\n%s",
		telegram.Bold(title),
		telegram.EscapeHTML(scopeLabel),
		telegram.Code(itoa(total)),
		telegram.Code(strconv.Itoa(n)),
		telegram.EscapeHTML(action),
		sample.String(),
	)
	return b.editOrSend(ctx, chatID, msgID, msg, kb)
}

func inferBulkActionKey(confirmData string) string {
	switch confirmData {
	case "mgr_bulk_clear_go":
		return "clear_err"
	case "mgr_bulk_recover_go":
		return "recover"
	case "mgr_bulk_sched_on_go":
		return "sched_on"
	case "mgr_bulk_clear_rl_go":
		return "clear_rl"
	case "mgr_bulk_heal_go":
		return "heal"
	default:
		return "clear_err"
	}
}

func (b *Bot) bulkRecoverPrompt(ctx context.Context, chatID, msgID, userID int64) error {
	return b.bulkAccountActionPrompt(ctx, chatID, msgID, userID, "恢复状态", "批量恢复确认", "mgr_bulk_recover_go")
}

func (b *Bot) bulkSchedOnPrompt(ctx context.Context, chatID, msgID, userID int64) error {
	return b.bulkAccountActionPrompt(ctx, chatID, msgID, userID, "开启调度", "批量开调度确认", "mgr_bulk_sched_on_go")
}

func (b *Bot) bulkAccountActionExecute(ctx context.Context, chatID, msgID, userID int64, action string) error {
	cli, _, err := b.userClient(userID, 45*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	const maxOps = 20
	items, total, scopeLabel, err := b.loadBulkTargets(ctx, cli, action, maxOps)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取账号失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	if len(items) == 0 {
		return b.editOrSend(ctx, chatID, msgID, "✅ 当前没有可处理的账号（"+telegram.EscapeHTML(scopeLabel)+"）。", manageKeyboard())
	}
	title := map[string]string{
		"clear_err": "批量清错",
		"recover":   "批量恢复",
		"sched_on":  "批量开调度",
		"clear_rl":  "批量清限速",
		"heal":      "批量一键修复",
	}[action]
	if title == "" {
		title = "批量操作"
	}
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
	// progress kickoff
	_ = b.editOrSend(ctx, chatID, msgID,
		fmt.Sprintf("%s\n\n⏳ 处理中 0/%d …\n范围: %s · 约 %s 个",
			telegram.Bold(title), n,
			telegram.EscapeHTML(scopeLabel), telegram.Code(itoa(total))),
		nil)

	okN, failN := 0, 0
	var fails []string
	var failIDs []int64
	for i := 0; i < n; i++ {
		a := items[i]
		var opErr error
		switch action {
		case "clear_err":
			_, opErr = cli.ClearAccountError(ctx, a.ID)
		case "recover":
			_, opErr = cli.RecoverAccountState(ctx, a.ID)
		case "sched_on":
			_, opErr = cli.SetSchedulable(ctx, a.ID, true)
		case "clear_rl":
			_, opErr = cli.ClearAccountRateLimit(ctx, a.ID)
		case "heal":
			msg := b.healAccount(ctx, cli, a.ID)
			if strings.HasPrefix(msg, "❌") {
				opErr = fmt.Errorf("%s", strings.TrimPrefix(msg, "❌ "))
			}
		default:
			opErr = fmt.Errorf("unknown action")
		}
		if opErr != nil {
			failN++
			if len(fails) < 5 {
				fails = append(fails, fmt.Sprintf("#%d %s", a.ID, truncateRunes(opErr.Error(), 40)))
			}
			if len(failIDs) < 3 {
				failIDs = append(failIDs, a.ID)
			}
		} else {
			okN++
		}
		// mid progress every 3 items or last
		if (i+1)%3 == 0 || i+1 == n {
			_ = b.editOrSend(ctx, chatID, msgID,
				fmt.Sprintf("%s\n\n⏳ 处理中 %d/%d\n✅ %d · ❌ %d\n当前 #%d %s",
					telegram.Bold(title), i+1, n, okN, failN,
					a.ID, telegram.EscapeHTML(truncateRunes(a.Name, 16))),
				nil)
		}
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold(title+"结果") + "\n\n")
	fmt.Fprintf(&bld, "范围: %s · 约 %s 个（本次 %d）\n",
		telegram.EscapeHTML(scopeLabel), telegram.Code(itoa(total)), n)
	fmt.Fprintf(&bld, "✅ 成功 %s · ❌ 失败 %s\n", telegram.Code(strconv.Itoa(okN)), telegram.Code(strconv.Itoa(failN)))
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + telegram.EscapeHTML(f) + "\n")
		}
	}
	rows := [][]telegram.InlineKeyboardButton{}
	if len(failIDs) > 0 {
		var failRow []telegram.InlineKeyboardButton
		for _, id := range failIDs {
			failRow = append(failRow, telegram.Btn(fmt.Sprintf("管理 #%d", id), fmt.Sprintf("mgr_acc:%d", id)))
		}
		rows = append(rows, failRow)
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
			telegram.Btn("📚 账号浏览", "mgr_browse"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("« 管理菜单", "mgr_menu"),
			telegram.Btn("« 运维", "ops_menu"),
		},
		[]telegram.InlineKeyboardButton{telegram.Btn("« 主面板", "home")},
	)
	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
}

// loadBulkTargets picks accounts for bulk actions.
func (b *Bot) loadBulkTargets(ctx context.Context, cli *sub2api.Client, action string, maxOps int) ([]sub2api.Account, int64, string, error) {
	return browse.LoadBulkTargets(ctx, cli, action, maxOps)
}

func (b *Bot) healAccount(ctx context.Context, cli *sub2api.Client, accountID int64) string {
	steps := []struct {
		name string
		fn   func() error
	}{
		{"清错误", func() error { _, err := cli.ClearAccountError(ctx, accountID); return err }},
		{"清限速", func() error { _, err := cli.ClearAccountRateLimit(ctx, accountID); return err }},
		{"恢复", func() error { _, err := cli.RecoverAccountState(ctx, accountID); return err }},
		{"开调度", func() error { _, err := cli.SetSchedulable(ctx, accountID, true); return err }},
	}
	var ok, fail []string
	for _, s := range steps {
		if err := s.fn(); err != nil {
			fail = append(fail, s.name+": "+truncateRunes(err.Error(), 40))
		} else {
			ok = append(ok, s.name)
		}
	}
	if len(ok) == 0 {
		return "❌ 一键修复全部失败: " + telegram.EscapeHTML(strings.Join(fail, "; "))
	}
	msg := "✅ 一键修复完成: " + telegram.Code(strings.Join(ok, " · "))
	if len(fail) > 0 {
		msg += "\n⚠️ 部分失败: " + telegram.EscapeHTML(strings.Join(fail, "; "))
	}
	return msg
}

func (b *Bot) bulkClearRLPrompt(ctx context.Context, chatID, msgID, userID int64) error {
	return b.bulkAccountActionPrompt(ctx, chatID, msgID, userID, "清除限速", "批量清限速确认", "mgr_bulk_clear_rl_go")
}

func (b *Bot) bulkHealPrompt(ctx context.Context, chatID, msgID, userID int64) error {
	return b.bulkAccountActionPrompt(ctx, chatID, msgID, userID, "一键修复(清错+清限速+恢复+开调度)", "批量一键修复确认", "mgr_bulk_heal_go")
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
		name := u.Username
		if name == "" {
			name = u.Email
		}
		fmt.Fprintf(&bld, "• #%d %s [%s] %s",
			u.ID,
			telegram.EscapeHTML(truncateRunes(name, 16)),
			telegram.EscapeHTML(u.Role),
			telegram.EscapeHTML(u.Status),
		)
		if u.CurrentConcurrency > 0 || u.Concurrency > 0 {
			fmt.Fprintf(&bld, " · 并发 %s/%s",
				telegram.Code(itoa(u.CurrentConcurrency)),
				telegram.Code(itoa(u.Concurrency)))
		}
		if u.Balance != 0 {
			fmt.Fprintf(&bld, " · 余额 %s", telegram.Code(fmt.Sprintf("%.2f", u.Balance)))
		}
		bld.WriteString("\n")
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
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("📚 账号浏览", "mgr_browse"),
			telegram.Btn("« 管理菜单", "mgr_menu"),
		},
	)
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
		excl := ""
		if g.IsExclusive {
			excl = " · 独占"
		}
		fmt.Fprintf(&bld, "• #%d %s [%s/%s] ×%.2f%s\n",
			g.ID,
			telegram.EscapeHTML(truncateRunes(g.Name, 20)),
			telegram.EscapeHTML(g.Platform),
			telegram.EscapeHTML(g.Status),
			g.RateMultiplier,
			excl,
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
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("📚 账号浏览", "mgr_browse"),
			telegram.Btn("« 管理菜单", "mgr_menu"),
		},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// showPanelUsers lists local monitor panel profiles (not Sub2API users).
func (b *Bot) showPanelUsers(ctx context.Context, chatID, msgID, adminID int64, page int, notice string) error {
	if page < 0 {
		page = 0
	}
	const pageSize = 8
	all := b.users.ListAll()
	// stable-ish sort by id
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].UserID() < all[j].UserID()
	})
	total := len(all)
	start := page * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageItems := all[start:end]

	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString(telegram.Bold("面板用户") + "（本监控 Bot）\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共 %s\n点用户可改角色\n\n", page+1, telegram.Code(strconv.Itoa(total)))
	if len(pageItems) == 0 {
		bld.WriteString("暂无面板用户。")
	}
	rows := [][]telegram.InlineKeyboardButton{}
	for _, p := range pageItems {
		role := p.EffectiveRole()
		if role == "" {
			if b.isAdmin(p.UserID()) {
				role = "admin*"
			} else {
				role = "user*"
			}
		}
		plat := p.EffectivePlatform()
		name := p.DisplayName
		if name == "" {
			name = p.Username
		}
		if name == "" {
			name = strconv.FormatInt(p.UserID(), 10)
		}
		conn := "未连接"
		if p.HasConnection() {
			conn = "已连接"
		}
		mon := "关"
		if p.Enabled {
			mon = "开"
		}
		fmt.Fprintf(&bld, "• %s %s [%s/%s]\n  %s · 监控%s · 账号%d\n",
			telegram.Code(strconv.FormatInt(p.UserID(), 10)),
			telegram.EscapeHTML(truncateRunes(name, 14)),
			telegram.EscapeHTML(role),
			telegram.EscapeHTML(plat),
			telegram.EscapeHTML(conn),
			telegram.EscapeHTML(mon),
			len(p.Accounts),
		)
		label := fmt.Sprintf("#%d %s", p.UserID(), truncateRunes(name, 10))
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(label, fmt.Sprintf("pnl_user:%d", p.UserID())),
		})
	}
	nav := []telegram.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("pnl_users:%d", page-1)))
	}
	if end < total {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("pnl_users:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []telegram.InlineKeyboardButton{
		telegram.Btn("« 管理菜单", "mgr_menu"),
		telegram.Btn("« 主面板", "home"),
	})
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showPanelUserDetail(ctx context.Context, chatID, msgID, adminID, targetID int64, notice string) error {
	p, ok := b.users.Get(targetID)
	if !ok {
		return b.showPanelUsers(ctx, chatID, msgID, adminID, 0, "❌ 用户不存在")
	}
	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString(telegram.Bold(fmt.Sprintf("面板用户 #%d", targetID)) + "\n\n")
	name := p.DisplayName
	if name == "" {
		name = p.Username
	}
	fmt.Fprintf(&bld, "名称: %s\n", telegram.Code(truncateRunes(name, 24)))
	fmt.Fprintf(&bld, "平台: %s · Chat: %s\n", telegram.Code(p.EffectivePlatform()), telegram.Code(p.ChatID))
	roleStored := strings.TrimSpace(p.Role)
	if roleStored == "" {
		roleStored = "(继承配置)"
	}
	fmt.Fprintf(&bld, "存储角色: %s\n", telegram.Code(roleStored))
	fmt.Fprintf(&bld, "生效角色: %s\n", telegram.Code(b.roleLabel(targetID)))
	base := p.BaseURL
	if base == "" {
		base = "(未设置)"
	}
	fmt.Fprintf(&bld, "Base URL: %s\n", telegram.Code(truncateRunes(base, 40)))
	fmt.Fprintf(&bld, "API Key: %s\n", telegram.Code(userstore.MaskKey(p.AdminAPIKey)))
	mon := "关闭"
	if p.Enabled {
		mon = "开启"
	}
	fmt.Fprintf(&bld, "监控: %s · 数据源: %s · 账号: %d\n",
		telegram.Code(mon), telegram.Code(p.EffectiveSource()), len(p.Accounts))
	if targetID == adminID {
		bld.WriteString("\n⚠️ 这是你自己的账号。")
	}
	bld.WriteString("\n\n角色覆盖仅影响本 Bot 面板权限，不改 Sub2API 权限。")

	monBtn := "⏸ 关闭监控"
	if !p.Enabled {
		monBtn = "▶️ 开启监控"
	}
	srcBtn := "数据源→active"
	if p.EffectiveSource() == "active" {
		srcBtn = "数据源→passive"
	}
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{
				telegram.Btn("设为管理员", fmt.Sprintf("pnl_role:admin:%d", targetID)),
				telegram.Btn("设为用户", fmt.Sprintf("pnl_role:user:%d", targetID)),
			},
			{
				telegram.Btn("清除角色覆盖", fmt.Sprintf("pnl_role:clear:%d", targetID)),
			},
			{
				telegram.Btn(monBtn, fmt.Sprintf("pnl_mon:%d", targetID)),
				telegram.Btn(srcBtn, fmt.Sprintf("pnl_src:%d", targetID)),
			},
			{
				telegram.Btn("« 面板用户", "pnl_users"),
				telegram.Btn("« 管理菜单", "mgr_menu"),
			},
		},
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
}

func (b *Bot) setPanelUserRole(ctx context.Context, chatID, msgID, adminID, targetID int64, role string) error {
	role = strings.ToLower(strings.TrimSpace(role))
	if targetID == adminID && (role == "user" || role == "clear") {
		// allow but warn — still allow demote self to avoid lockout only if other admins exist is complex; just warn
	}
	var storeRole string
	switch role {
	case "admin":
		storeRole = userstore.RoleAdmin
	case "user":
		storeRole = userstore.RoleUser
	case "clear", "inherit", "default", "":
		storeRole = ""
	default:
		return b.showPanelUserDetail(ctx, chatID, msgID, adminID, targetID, "❌ 无效角色")
	}
	if _, err := b.users.Update(targetID, func(p *userstore.Profile) error {
		p.Role = storeRole
		return nil
	}); err != nil {
		return b.showPanelUserDetail(ctx, chatID, msgID, adminID, targetID, "❌ 保存失败: "+telegram.EscapeHTML(err.Error()))
	}
	label := storeRole
	if label == "" {
		label = "继承配置"
	}
	return b.showPanelUserDetail(ctx, chatID, msgID, adminID, targetID,
		fmt.Sprintf("✅ 已更新角色为 %s", telegram.Code(label)))
}

func (b *Bot) togglePanelUserMonitor(ctx context.Context, chatID, msgID, adminID, targetID int64) error {
	var enabled bool
	if _, err := b.users.Update(targetID, func(p *userstore.Profile) error {
		p.Enabled = !p.Enabled
		enabled = p.Enabled
		return nil
	}); err != nil {
		return b.showPanelUserDetail(ctx, chatID, msgID, adminID, targetID, "❌ 切换监控失败: "+telegram.EscapeHTML(err.Error()))
	}
	state := "关闭"
	if enabled {
		state = "开启"
	}
	return b.showPanelUserDetail(ctx, chatID, msgID, adminID, targetID,
		fmt.Sprintf("✅ 监控已%s", telegram.Code(state)))
}

func (b *Bot) togglePanelUserSource(ctx context.Context, chatID, msgID, adminID, targetID int64) error {
	var src string
	if _, err := b.users.Update(targetID, func(p *userstore.Profile) error {
		if p.EffectiveSource() == "active" {
			p.Source = "passive"
		} else {
			p.Source = "active"
		}
		src = p.EffectiveSource()
		return nil
	}); err != nil {
		return b.showPanelUserDetail(ctx, chatID, msgID, adminID, targetID, "❌ 切换数据源失败: "+telegram.EscapeHTML(err.Error()))
	}
	return b.showPanelUserDetail(ctx, chatID, msgID, adminID, targetID,
		fmt.Sprintf("✅ 数据源已设为 %s", telegram.Code(src)))
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
