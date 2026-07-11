package panel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
)

func manageKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("📚 账号浏览", "mgr_browse"), telegram.Btn("👥 用户", "mgr_users")},
			{telegram.Btn("🏷 分组", "mgr_groups"), telegram.Btn("📋 异常账号", "ops_badacc")},
			{telegram.Btn("« 返回主面板", "home")},
		},
	}
}

func (b *Bot) manageMenuText() string {
	return telegram.Bold("账号管理") + `

用你的 Admin API 管理实例（只对你配置的连接生效）：

• 账号浏览 — 按状态分页查看、点进管理
• 用户 / 分组 — 只读列表
• 异常账号 — error 列表与一键监控

进入账号后可执行：
切换调度 · 清除错误 · 清除限速 · 恢复状态 · 刷新凭据
`
}

func (b *Bot) showManageMenu(ctx context.Context, chatID, msgID int64) error {
	return b.editOrSend(ctx, chatID, msgID, b.manageMenuText(), manageKeyboard())
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
	filter := status
	if filter == "all" || filter == "" {
		filter = ""
	}
	// special: unsched => list all then filter client-side for !schedulable
	var items []sub2api.Account
	var total int64
	if status == "unsched" {
		// pull a larger page of active+error isn't perfect; list without status
		all, tot, err := cli.ListAccounts(ctx, page+1, pageSize, "")
		if err != nil {
			return b.editOrSend(ctx, chatID, msgID, "列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
		}
		for _, a := range all {
			if !a.Schedulable {
				items = append(items, a)
			}
		}
		total = tot
	} else if status == "rate_limited" {
		// API may not support; fetch and filter
		all, tot, err := cli.ListAccounts(ctx, page+1, 30, "")
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
	} else {
		var err error
		items, total, err = cli.ListAccounts(ctx, page+1, pageSize, filter)
		if err != nil {
			return b.editOrSend(ctx, chatID, msgID, "列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
		}
	}

	var bld strings.Builder
	title := status
	if title == "" || title == "all" {
		title = "全部"
	}
	bld.WriteString(telegram.Bold("账号浏览") + " · " + telegram.Code(title) + "\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共约 %s\n点账号进入管理\n\n", page+1, telegram.Code(itoa(total)))
	if len(items) == 0 {
		bld.WriteString("本页无账号。")
	}

	kbRows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn(filterLabel("全部", status, "all"), "mgr_browse:all:0"),
			telegram.Btn(filterLabel("active", status, "active"), "mgr_browse:active:0"),
			telegram.Btn(filterLabel("error", status, "error"), "mgr_browse:error:0")},
	}
	for _, a := range items {
		sched := "可调度"
		if !a.Schedulable {
			sched = "停调度"
		}
		label := fmt.Sprintf("#%d [%s] %s", a.ID, a.Status, truncateRunes(a.Name, 12))
		if a.Platform != "" {
			label = fmt.Sprintf("#%d %s/%s %s", a.ID, truncateRunes(a.Platform, 6), a.Status, truncateRunes(a.Name, 10))
		}
		_ = sched
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
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("mgr_browse:%s:%d", statusOrAll(status), page-1)))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("mgr_browse:%s:%d", statusOrAll(status), page+1)))
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

func filterLabel(label, cur, val string) string {
	if cur == val || (cur == "" && val == "all") {
		return "• " + label
	}
	return label
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

	// watched?
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

	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn(schedBtn, schedData), telegram.Btn(watchBtn, watchData)},
			{
				telegram.Btn("🧹 清错误", fmt.Sprintf("mgr_act:clear_err:%d", accountID)),
				telegram.Btn("⏱ 清限速", fmt.Sprintf("mgr_act:clear_rl:%d", accountID)),
			},
			{
				telegram.Btn("♻️ 恢复状态", fmt.Sprintf("mgr_act:recover:%d", accountID)),
				telegram.Btn("🔄 刷新凭据", fmt.Sprintf("mgr_act:refresh:%d", accountID)),
			},
			{
				telegram.Btn("🚫 清临时停调度", fmt.Sprintf("mgr_act:clear_temp:%d", accountID)),
				telegram.Btn("📡 实时用量", fmt.Sprintf("acc_live:%d", accountID)),
			},
			{telegram.Btn("« 账号浏览", "mgr_browse"), telegram.Btn("« 管理菜单", "mgr_menu")},
		},
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
}

func (b *Bot) handleManageAction(ctx context.Context, chatID, msgID, userID int64, action string, accountID int64) error {
	// confirmation step before stopping schedule
	if action == "confirm_unsched" {
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
	default:
		notice = "未知操作"
	}
	return b.showManageAccount(ctx, chatID, msgID, userID, accountID, notice)
}

func (b *Bot) showUsers(ctx context.Context, chatID, msgID, userID int64, page int) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	if page < 0 {
		page = 0
	}
	const pageSize = 10
	items, total, err := cli.ListUsers(ctx, page+1, pageSize)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "用户列表失败: "+telegram.EscapeHTML(err.Error()), manageKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("用户列表") + "\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共 %s\n\n", page+1, telegram.Code(itoa(total)))
	for _, u := range items {
		email := u.Email
		if email == "" {
			email = u.Username
		}
		fmt.Fprintf(&bld, "• #%d %s [%s]\n  余额 %s · 并发 %s/%s · %s\n",
			u.ID,
			telegram.EscapeHTML(truncateRunes(email, 24)),
			telegram.EscapeHTML(u.Role),
			telegram.Code(fmt.Sprintf("%.2f", u.Balance)),
			telegram.Code(strconv.Itoa(u.CurrentConcurrency)),
			telegram.Code(strconv.Itoa(u.Concurrency)),
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
