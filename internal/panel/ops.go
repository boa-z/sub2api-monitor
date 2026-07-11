package panel

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/panel/browse"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

func (b *Bot) userClient(userID int64, timeout time.Duration) (*sub2api.Client, *userstore.Profile, error) {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() {
		return nil, p, fmt.Errorf("请先配置连接（Base URL + API Key）")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: timeout,
	})
	if err != nil {
		return nil, p, err
	}
	return cli, p, nil
}

func opsKeyboard() *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("📈 看板", "ops_dash"), telegram.Btn("✅ 可用性", "ops_avail")},
			{telegram.Btn("🚨 告警", "ops_alerts"), telegram.Btn("❌ 错误", "ops_errors:all:0")},
			{telegram.Btn("⚙️ 并发", "ops_conc"), telegram.Btn("📡 渠道探测", "ops_channels")},
			{telegram.Btn("📋 异常账号", "ops_badacc:error:0"), telegram.Btn("🧰 账号管理", "mgr_menu")},
			{telegram.Btn("🔄 刷新菜单", "ops_menu"), telegram.Btn("« 主面板", "home")},
		},
	}
}

// opsViewKeyboard is ops menu plus a self-refresh button for the current view.
func opsViewKeyboard(refreshData string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("🔄 刷新", refreshData), telegram.Btn("« 运维菜单", "ops_menu")},
			{telegram.Btn("📈 看板", "ops_dash"), telegram.Btn("✅ 可用性", "ops_avail")},
			{telegram.Btn("🚨 告警", "ops_alerts"), telegram.Btn("❌ 错误", "ops_errors:all:0")},
			{telegram.Btn("⚙️ 并发", "ops_conc"), telegram.Btn("📋 异常账号", "ops_badacc:error:0")},
			{telegram.Btn("« 主面板", "home")},
		},
	}
}

// dashboardKeyboard builds contextual shortcuts from live stats.
func dashboardKeyboard(stats *sub2api.DashboardStats) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新", "ops_dash"), telegram.Btn("« 运维菜单", "ops_menu")},
	}
	var jump []telegram.InlineKeyboardButton
	if stats != nil {
		if stats.ErrorAccounts > 0 {
			jump = append(jump, telegram.Btn(fmt.Sprintf("📋 异常 %v", stats.ErrorAccounts), "ops_badacc:error:0"))
		}
		if stats.RatelimitAccounts > 0 {
			jump = append(jump, telegram.Btn(fmt.Sprintf("⏱ 限速 %v", stats.RatelimitAccounts), "ops_badacc:rl:0"))
		}
		if stats.OverloadAccounts > 0 && len(jump) < 3 {
			// overload often overlaps rate_limited; still offer rl tab
			if stats.RatelimitAccounts == 0 {
				jump = append(jump, telegram.Btn(fmt.Sprintf("过载 %v", stats.OverloadAccounts), "ops_badacc:rl:0"))
			}
		}
	}
	if len(jump) == 0 {
		jump = append(jump, telegram.Btn("📋 异常账号", "ops_badacc:error:0"))
	}
	rows = append(rows, jump)
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("❌ 错误列表", "ops_errors:all:0"),
			telegram.Btn("🧰 账号管理", "mgr_menu"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("✅ 可用性", "ops_avail"),
			telegram.Btn("🚨 告警", "ops_alerts"),
		},
		[]telegram.InlineKeyboardButton{telegram.Btn("« 主面板", "home")},
	)
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// opsMenuText builds the ops hub text; when cli is available includes a live health line.
func (b *Bot) opsMenuText(ctx context.Context, userID int64) string {
	var bld strings.Builder
	bld.WriteString(telegram.Bold("运维视图") + "\n\n")
	// live snapshot (best-effort, never blocks the menu)
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil && st != nil {
			fmt.Fprintf(&bld, "健康: 正常 %s · 异常 %s · 限速 %s · 过载 %s\n",
				telegram.Code(itoa(st.NormalAccounts)),
				telegram.Code(itoa(st.ErrorAccounts)),
				telegram.Code(itoa(st.RatelimitAccounts)),
				telegram.Code(itoa(st.OverloadAccounts)))
			if st.RPM > 0 {
				fmt.Fprintf(&bld, "RPM %s · 今日请求 %s\n",
					telegram.Code(fmt.Sprintf("%.1f", st.RPM)),
					telegram.Code(itoa(st.TodayRequests)))
			}
			if rt, err := cli.GetRealtimeDashboard(ctx); err == nil && rt != nil {
				fmt.Fprintf(&bld, "实时: 活跃 %s · 错误率 %s%%\n",
					telegram.Code(itoa(rt.ActiveRequests)),
					telegram.Code(fmt.Sprintf("%.2f", rt.ErrorRate)))
			}
			bld.WriteString("\n")
		}
	}
	bld.WriteString(`基于当前连接的 Admin API：

• 看板 — 账号/用量/实时流量
• 可用性 — 平台/分组可用率
• 告警 — 内置 alert-events
• 错误 — 请求/上游（分页·解决·修复·实时）
• 并发 / 渠道探测
• 异常账号 — error/限速/停调度/汇总（分页·管理/实时/修复）

点下方按钮查看；数据实时拉取。`)
	return bld.String()
}

func (b *Bot) showOpsMenu(ctx context.Context, chatID, msgID, userID int64) error {
	return b.editOrSend(ctx, chatID, msgID, b.opsMenuText(ctx, userID), opsKeyboard())
}

func (b *Bot) showDashboard(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	stats, err := cli.GetDashboardStats(ctx)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "看板失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	rt, _ := cli.GetRealtimeDashboard(ctx)
	traf, _ := cli.GetRealtimeTraffic(ctx, "5min")

	var bld strings.Builder
	bld.WriteString(telegram.Bold("实例看板") + "\n")
	fmt.Fprintf(&bld, "更新: %s\n\n", telegram.Code(time.Now().Local().Format("15:04:05")))
	fmt.Fprintf(&bld, "账号: 总 %s · 正常 %s\n",
		telegram.Code(itoa(stats.TotalAccounts)), telegram.Code(itoa(stats.NormalAccounts)))
	fmt.Fprintf(&bld, "异常 %s · 限速 %s · 过载 %s\n",
		telegram.Code(itoa(stats.ErrorAccounts)),
		telegram.Code(itoa(stats.RatelimitAccounts)),
		telegram.Code(itoa(stats.OverloadAccounts)))
	fmt.Fprintf(&bld, "用户: 总 %s · 活跃 %s · 今日新增 %s\n",
		telegram.Code(itoa(stats.TotalUsers)),
		telegram.Code(itoa(stats.ActiveUsers)),
		telegram.Code(itoa(stats.TodayNewUsers)))
	fmt.Fprintf(&bld, "API Key: 总 %s · 活跃 %s\n",
		telegram.Code(itoa(stats.TotalAPIKeys)), telegram.Code(itoa(stats.ActiveAPIKeys)))
	fmt.Fprintf(&bld, "今日: 请求 %s · Token %s · 费用 %s\n",
		telegram.Code(itoa(stats.TodayRequests)),
		telegram.Code(formatCompactInt(stats.TodayTokens)),
		telegram.Code(fmt.Sprintf("%.2f", stats.TodayCost)))
	fmt.Fprintf(&bld, "累计: 请求 %s · Token %s · 费用 %s\n",
		telegram.Code(formatCompactInt(stats.TotalRequests)),
		telegram.Code(formatCompactInt(stats.TotalTokens)),
		telegram.Code(fmt.Sprintf("%.2f", stats.TotalCost)))
	if stats.RPM > 0 || stats.TPM > 0 {
		fmt.Fprintf(&bld, "RPM/TPM: %s / %s\n",
			telegram.Code(fmt.Sprintf("%.2f", stats.RPM)),
			telegram.Code(fmt.Sprintf("%.0f", stats.TPM)))
	}
	if stats.Uptime > 0 {
		fmt.Fprintf(&bld, "Uptime: %s\n", telegram.Code(formatDurationSec(stats.Uptime)))
	}
	if rt != nil {
		fmt.Fprintf(&bld, "\n实时: 活跃请求 %s · RPM %s · 错误率 %s%%\n",
			telegram.Code(itoa(rt.ActiveRequests)),
			telegram.Code(fmt.Sprintf("%.2f", rt.RequestsPerMinute)),
			telegram.Code(fmt.Sprintf("%.2f", rt.ErrorRate)))
	}
	if traf != nil {
		fmt.Fprintf(&bld, "流量(%s): QPS %s\n",
			telegram.EscapeHTML(traf.WindowLabel()),
			telegram.Code(fmt.Sprintf("%.3f", traf.CurrentQPS())))
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), dashboardKeyboard(stats))
}

func (b *Bot) showAvailability(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	av, err := cli.GetAccountAvailability(ctx)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "可用性失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("账号可用性") + "\n")
	if av != nil && !av.Enabled {
		bld.WriteString("服务端实时监控未启用。\n")
		return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
	}
	// platform summary
	type row struct {
		key string
		tot int
		av  int
		err int
		rl  int
	}
	var plats []row
	for k, bucket := range av.Platform {
		plats = append(plats, row{k, bucket.TotalNum(), bucket.AvailableNum(), bucket.ErrorNum(), bucket.RateLimitNum()})
	}
	sort.Slice(plats, func(i, j int) bool { return plats[i].key < plats[j].key })
	bld.WriteString(telegram.Bold("平台") + "\n")
	if len(plats) == 0 {
		bld.WriteString("(无数据)\n")
	}
	for _, r := range plats {
		ratio := 0.0
		if r.tot > 0 {
			ratio = float64(r.av) / float64(r.tot) * 100
		}
		fmt.Fprintf(&bld, "• %s: %s/%s (%.0f%%) err=%s rl=%s\n",
			telegram.EscapeHTML(r.key),
			telegram.Code(strconv.Itoa(r.av)),
			telegram.Code(strconv.Itoa(r.tot)),
			ratio,
			telegram.Code(strconv.Itoa(r.err)),
			telegram.Code(strconv.Itoa(r.rl)),
		)
	}
	// groups
	var groups []row
	for k, bucket := range av.Group {
		name := bucket.GroupName
		if name == "" {
			name = "group#" + k
		}
		groups = append(groups, row{name, bucket.TotalNum(), bucket.AvailableNum(), bucket.ErrorNum(), bucket.RateLimitNum()})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].err > groups[j].err || groups[i].key < groups[j].key })
	bld.WriteString("\n" + telegram.Bold("分组") + "\n")
	limit := 12
	for i, r := range groups {
		if i >= limit {
			fmt.Fprintf(&bld, "… 另有 %d 个分组\n", len(groups)-limit)
			break
		}
		ratio := 0.0
		if r.tot > 0 {
			ratio = float64(r.av) / float64(r.tot) * 100
		}
		fmt.Fprintf(&bld, "• %s: %s/%s (%.0f%%) err=%s\n",
			telegram.EscapeHTML(truncateRunes(r.key, 16)),
			telegram.Code(strconv.Itoa(r.av)),
			telegram.Code(strconv.Itoa(r.tot)),
			ratio,
			telegram.Code(strconv.Itoa(r.err)),
		)
	}
	// problem accounts
	var bad []sub2api.AccountRuntimeStatus
	for _, st := range av.Account {
		if st.HasError || st.IsRateLimited || st.IsOverloaded || !st.IsAvailable {
			bad = append(bad, st)
		}
	}
	sort.Slice(bad, func(i, j int) bool {
		if bad[i].HasError != bad[j].HasError {
			return bad[i].HasError
		}
		return bad[i].AccountID < bad[j].AccountID
	})
	bld.WriteString("\n" + telegram.Bold("异常/不可用账号") + "\n")
	if len(bad) == 0 {
		bld.WriteString("无\n")
	}
	for i, st := range bad {
		if i >= 10 {
			fmt.Fprintf(&bld, "… 另有 %d 个\n", len(bad)-10)
			break
		}
		flags := []string{}
		if st.HasError {
			flags = append(flags, "error")
		}
		if st.IsRateLimited {
			flags = append(flags, "rl")
		}
		if st.IsOverloaded {
			flags = append(flags, "ol")
		}
		if !st.IsAvailable {
			flags = append(flags, "unavail")
		}
		msg := st.ErrorMessage
		if msg == "" {
			msg = st.Status
		}
		fmt.Fprintf(&bld, "• #%d %s [%s] %s\n",
			st.AccountID,
			telegram.EscapeHTML(truncateRunes(st.AccountName, 14)),
			telegram.EscapeHTML(strings.Join(flags, ",")),
			telegram.EscapeHTML(truncateRunes(msg, 40)),
		)
	}
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新", "ops_avail"), telegram.Btn("« 运维菜单", "ops_menu")},
	}
	// top problem accounts → manage
	var accRow []telegram.InlineKeyboardButton
	for i, st := range bad {
		if i >= 4 || st.AccountID <= 0 {
			break
		}
		accRow = append(accRow, telegram.Btn(fmt.Sprintf("管理 #%d", st.AccountID), fmt.Sprintf("mgr_acc:%d", st.AccountID)))
		if len(accRow) == 2 {
			rows = append(rows, accRow)
			accRow = nil
		}
	}
	if len(accRow) > 0 {
		rows = append(rows, accRow)
	}
	b.setManageBack(userID, "ops_avail")
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
			telegram.Btn("⏱ 限速", "ops_badacc:rl:0"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("❌ 错误", "ops_errors:all:0"),
			telegram.Btn("📈 看板", "ops_dash"),
		},
		[]telegram.InlineKeyboardButton{telegram.Btn("« 主面板", "home")},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showAlerts(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	events, err := cli.ListAlertEvents(ctx, 1, 20)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "告警失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("内置告警事件") + "\n\n")
	if len(events) == 0 {
		bld.WriteString("暂无事件。")
		return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
	}
	// prefer firing first
	sort.SliceStable(events, func(i, j int) bool {
		si, sj := strings.ToLower(events[i].Status), strings.ToLower(events[j].Status)
		fi, fj := si == "firing" || si == "open" || si == "active", sj == "firing" || sj == "open" || sj == "active"
		if fi != fj {
			return fi
		}
		return events[i].FiredAt.After(events[j].FiredAt)
	})
	shown := 0
	for _, ev := range events {
		if shown >= 12 {
			break
		}
		st := strings.ToLower(ev.Status)
		icon := "⚪"
		switch {
		case st == "firing" || st == "open" || st == "active":
			icon = "🔴"
		case st == "resolved" || st == "ok" || st == "closed":
			icon = "🟢"
		}
		title := ev.DisplayTitle()
		fmt.Fprintf(&bld, "%s [%s] %s\n", icon, telegram.EscapeHTML(strings.ToUpper(ev.Severity)), telegram.EscapeHTML(truncateRunes(title, 40)))
		if msg := ev.DisplayMessage(); msg != "" {
			fmt.Fprintf(&bld, "  %s\n", telegram.EscapeHTML(truncateRunes(msg, 80)))
		}
		mv, tv := ev.Metric(), ev.ThresholdVal()
		if mv != 0 || tv != 0 {
			fmt.Fprintf(&bld, "  值 %s / 阈值 %s\n",
				telegram.Code(fmt.Sprintf("%.4g", mv)),
				telegram.Code(fmt.Sprintf("%.4g", tv)))
		}
		if !ev.FiredAt.IsZero() {
			fmt.Fprintf(&bld, "  时间 %s · %s\n",
				telegram.Code(ev.FiredAt.Local().Format("01-02 15:04")),
				telegram.EscapeHTML(ev.Status))
		}
		shown++
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), opsViewKeyboard("ops_alerts"))
}

func (b *Bot) showErrors(ctx context.Context, chatID, msgID, userID int64) error {
	return b.showErrorsView(ctx, chatID, msgID, userID, "all", 0, "")
}

func (b *Bot) showErrorsNotice(ctx context.Context, chatID, msgID, userID int64, notice string) error {
	kind, page := b.getOpsErrorView(userID)
	return b.showErrorsView(ctx, chatID, msgID, userID, kind, page, notice)
}

func (b *Bot) showErrorsNoticeKind(ctx context.Context, chatID, msgID, userID int64, kind string, page int, notice string) error {
	return b.showErrorsView(ctx, chatID, msgID, userID, kind, page, notice)
}

// showErrorsView renders ops errors.
// kind: all | u (upstream) | r (request). page is 0-based for u/r tabs.
func (b *Bot) showErrorsView(ctx context.Context, chatID, msgID, userID int64, kind string, page int, notice string) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	if page < 0 {
		page = 0
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "u" && kind != "r" {
		kind = "all"
	}
	b.setOpsErrorView(userID, kind, page)
	b.setManageBack(userID, fmt.Sprintf("ops_errors:%s:%d", kind, page))

	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString(telegram.Bold("近期错误") + "（优先未解决）\n")
	rows := [][]telegram.InlineKeyboardButton{
		{
			telegram.Btn(errorTabLabel("全部", kind, "all"), "ops_errors:all:0"),
			telegram.Btn(errorTabLabel("上游", kind, "u"), "ops_errors:u:0"),
			telegram.Btn(errorTabLabel("请求", kind, "r"), "ops_errors:r:0"),
		},
	}

	switch kind {
	case "u":
		pageData, err1 := cli.ListUpstreamErrors(ctx, page+1, 10)
		fmt.Fprintf(&bld, "标签: %s · 第 %d 页\n\n", telegram.Code("上游"), page+1)
		if err1 != nil {
			fmt.Fprintf(&bld, "拉取失败: %s\n", telegram.EscapeHTML(err1.Error()))
		} else {
			writeErrorItems(&bld, pageData, "u", 8, &rows)
			rows = append(rows, errorPageNav("u", page, pageData)...)
		}
	case "r":
		pageData, err2 := cli.ListRequestErrors(ctx, page+1, 10)
		fmt.Fprintf(&bld, "标签: %s · 第 %d 页\n\n", telegram.Code("请求"), page+1)
		if err2 != nil {
			fmt.Fprintf(&bld, "拉取失败: %s\n", telegram.EscapeHTML(err2.Error()))
		} else {
			writeErrorItems(&bld, pageData, "r", 8, &rows)
			rows = append(rows, errorPageNav("r", page, pageData)...)
		}
	default:
		up, err1 := cli.ListUpstreamErrors(ctx, 1, 15)
		req, err2 := cli.ListRequestErrors(ctx, 1, 10)
		bld.WriteString("\n")
		if err1 != nil && err2 != nil {
			return b.editOrSend(ctx, chatID, msgID, "错误列表失败: "+telegram.EscapeHTML(err1.Error()), opsKeyboard())
		}
		bld.WriteString(telegram.Bold("上游错误") + "\n")
		if err1 != nil {
			fmt.Fprintf(&bld, "拉取失败: %s\n", telegram.EscapeHTML(err1.Error()))
		} else {
			writeErrorItems(&bld, up, "u", 5, &rows)
		}
		bld.WriteString("\n" + telegram.Bold("请求错误") + "\n")
		if err2 != nil {
			fmt.Fprintf(&bld, "拉取失败: %s\n", telegram.EscapeHTML(err2.Error()))
		} else {
			writeErrorItems(&bld, req, "r", 3, &rows)
		}
	}

	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("✅ 解决上游", "oe:resolve_all:u"),
			telegram.Btn("✅ 解决请求", "oe:resolve_all:r"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("🔄 刷新", fmt.Sprintf("ops_errors:%s:%d", kind, page)),
			telegram.Btn("« 运维菜单", "ops_menu"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("« 主面板", "home"),
		},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func errorTabLabel(label, cur, val string) string {
	if cur == val {
		return "• " + label
	}
	return label
}

func errorPageNav(kind string, page int, pageData *sub2api.OpsErrorPage) [][]telegram.InlineKeyboardButton {
	if pageData == nil {
		return nil
	}
	nav := []telegram.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("ops_errors:%s:%d", kind, page-1)))
	}
	// next if this page looks full or total indicates more
	pageSize := pageData.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}
	hasMore := false
	if pageData.Total > 0 && int64((page+1)*pageSize) < pageData.Total {
		hasMore = true
	} else if len(pageData.Items) >= pageSize {
		hasMore = true
	}
	if hasMore {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("ops_errors:%s:%d", kind, page+1)))
	}
	if len(nav) == 0 {
		return nil
	}
	return [][]telegram.InlineKeyboardButton{nav}
}

// writeErrorItems renders error lines and appends resolve/manage/live/heal buttons.
// kind is "u" (upstream) or "r" (request) for compact callback_data.
func writeErrorItems(bld *strings.Builder, page *sub2api.OpsErrorPage, kind string, maxShow int, rows *[][]telegram.InlineKeyboardButton) {
	if page == nil || len(page.Items) == 0 {
		bld.WriteString("无\n")
		return
	}
	items := append([]sub2api.OpsError(nil), page.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Resolved != items[j].Resolved {
			return !items[i].Resolved && items[j].Resolved
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	shown := 0
	for _, e := range items {
		if e.Resolved {
			continue
		}
		if shown >= maxShow {
			break
		}
		name := e.AccountName
		if name == "" && e.AccountID > 0 {
			name = fmt.Sprintf("#%d", e.AccountID)
		}
		if name == "" {
			name = "(无账号)"
		}
		model := e.Model
		if model == "" {
			model = e.RequestedModel
		}
		when := ""
		if !e.CreatedAt.IsZero() {
			when = e.CreatedAt.Local().Format("01-02 15:04")
		}
		fmt.Fprintf(bld, "• #%d [%s] %s %s\n  %s · %s",
			e.ID,
			telegram.EscapeHTML(e.Severity),
			telegram.Code(strconv.Itoa(e.StatusCode)),
			telegram.EscapeHTML(truncateRunes(name, 14)),
			telegram.EscapeHTML(e.Platform),
			telegram.EscapeHTML(truncateRunes(model, 18)),
		)
		if when != "" {
			fmt.Fprintf(bld, " · %s", telegram.Code(when))
		}
		bld.WriteString("\n")
		fmt.Fprintf(bld, "  %s\n", telegram.EscapeHTML(truncateRunes(e.Message, 70)))
		btnRow := []telegram.InlineKeyboardButton{
			telegram.Btn(fmt.Sprintf("✅ #%d", e.ID), fmt.Sprintf("oe:r:%s:%d", kind, e.ID)),
		}
		if e.AccountID > 0 {
			btnRow = append(btnRow,
				telegram.Btn("修复", fmt.Sprintf("live_act:heal:%d", e.AccountID)),
				telegram.Btn("实时", fmt.Sprintf("acc_live:%d", e.AccountID)),
				telegram.Btn("管理", fmt.Sprintf("mgr_acc:%d", e.AccountID)),
			)
		}
		*rows = append(*rows, btnRow)
		shown++
	}
	if shown == 0 {
		bld.WriteString("无未解决项。\n")
	}
	if page.Total > 0 {
		fmt.Fprintf(bld, "列表共约 %s 条\n", telegram.Code(itoa(page.Total)))
	}
}

// resolveOpsError marks one ops error resolved and refreshes the list.
func (b *Bot) resolveOpsError(ctx context.Context, chatID, msgID, userID int64, kind string, errorID int64) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	apiKind := "upstream"
	if kind == "r" {
		apiKind = "request"
	}
	tab := kind
	if tab != "u" && tab != "r" {
		tab = "all"
	}
	memKind, memPage := b.getOpsErrorView(userID)
	page := 0
	if memKind == tab {
		page = memPage
	}
	if err := cli.ResolveOpsError(ctx, apiKind, errorID); err != nil {
		return b.showErrorsNoticeKind(ctx, chatID, msgID, userID, tab, page, "❌ 标记失败: "+telegram.EscapeHTML(err.Error()))
	}
	return b.showErrorsNoticeKind(ctx, chatID, msgID, userID, tab, page, fmt.Sprintf("✅ 已标记错误 #%d 为已解决", errorID))
}

// resolveAllUpstreamErrors resolves up to N unresolved upstream errors.
func (b *Bot) resolveAllUpstreamErrors(ctx context.Context, chatID, msgID, userID int64) error {
	return b.resolveAllOpsErrors(ctx, chatID, msgID, userID, "upstream", "上游")
}

// resolveAllRequestErrors resolves up to N unresolved request errors.
func (b *Bot) resolveAllRequestErrors(ctx context.Context, chatID, msgID, userID int64) error {
	return b.resolveAllOpsErrors(ctx, chatID, msgID, userID, "request", "请求")
}

// resolveAllOpsErrors marks unresolved ops errors of the given kind as resolved.
func (b *Bot) resolveAllOpsErrors(ctx context.Context, chatID, msgID, userID int64, apiKind, label string) error {
	cli, _, err := b.userClient(userID, 30*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	var page *sub2api.OpsErrorPage
	switch apiKind {
	case "request":
		page, err = cli.ListRequestErrors(ctx, 1, 20)
	default:
		page, err = cli.ListUpstreamErrors(ctx, 1, 20)
		apiKind = "upstream"
	}
	tab := "u"
	if apiKind == "request" {
		tab = "r"
	}
	memKind, memPage := b.getOpsErrorView(userID)
	pageNo := 0
	if memKind == tab {
		pageNo = memPage
	}
	if err != nil {
		return b.showErrorsNoticeKind(ctx, chatID, msgID, userID, tab, pageNo, "❌ 拉取失败: "+telegram.EscapeHTML(err.Error()))
	}
	okN, failN, n := 0, 0, 0
	const maxOps = 15
	for _, e := range page.Items {
		if e.Resolved {
			continue
		}
		if n >= maxOps {
			break
		}
		n++
		if err := cli.ResolveOpsError(ctx, apiKind, e.ID); err != nil {
			failN++
		} else {
			okN++
		}
	}
	if n == 0 {
		return b.showErrorsNoticeKind(ctx, chatID, msgID, userID, tab, pageNo, "✅ 没有未解决的"+label+"错误。")
	}
	return b.showErrorsNoticeKind(ctx, chatID, msgID, userID, tab, pageNo,
		fmt.Sprintf("✅ 批量标记%s错误：成功 %d · 失败 %d", label, okN, failN))
}

func (b *Bot) showConcurrency(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	snap, err := cli.GetConcurrency(ctx)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "并发失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("并发占用") + "\n")
	if snap != nil && !snap.Enabled {
		bld.WriteString("服务端并发监控未启用。\n")
		return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
	}
	// platforms
	type crow struct {
		name string
		b    sub2api.ConcurrencyBucket
	}
	var plats []crow
	for k, v := range snap.Platform {
		name := k
		if v.Platform != "" {
			name = v.Platform
		}
		plats = append(plats, crow{name, v})
	}
	sort.Slice(plats, func(i, j int) bool { return plats[i].b.LoadPercentage > plats[j].b.LoadPercentage })
	bld.WriteString(telegram.Bold("平台") + "\n")
	for _, r := range plats {
		fmt.Fprintf(&bld, "• %s: %s/%s (%.0f%%) wait=%s\n",
			telegram.EscapeHTML(r.name),
			telegram.Code(strconv.Itoa(r.b.CurrentInUse)),
			telegram.Code(strconv.Itoa(r.b.MaxCapacity)),
			r.b.LoadPercentage,
			telegram.Code(strconv.Itoa(r.b.WaitingInQueue)),
		)
	}
	// top loaded accounts
	var accs []crow
	for _, v := range snap.Account {
		name := v.AccountName
		if name == "" {
			name = fmt.Sprintf("#%d", v.AccountID)
		}
		if v.CurrentInUse > 0 || v.LoadPercentage > 0 || v.WaitingInQueue > 0 {
			accs = append(accs, crow{name, v})
		}
	}
	sort.Slice(accs, func(i, j int) bool { return accs[i].b.LoadPercentage > accs[j].b.LoadPercentage })
	bld.WriteString("\n" + telegram.Bold("有负载账号") + "\n")
	if len(accs) == 0 {
		bld.WriteString("当前无占用。\n")
	}
	for i, r := range accs {
		if i >= 10 {
			fmt.Fprintf(&bld, "… 另有 %d 个\n", len(accs)-10)
			break
		}
		fmt.Fprintf(&bld, "• #%d %s: %s/%s (%.0f%%)\n",
			r.b.AccountID,
			telegram.EscapeHTML(truncateRunes(r.name, 14)),
			telegram.Code(strconv.Itoa(r.b.CurrentInUse)),
			telegram.Code(strconv.Itoa(r.b.MaxCapacity)),
			r.b.LoadPercentage,
		)
	}
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新", "ops_conc"), telegram.Btn("« 运维菜单", "ops_menu")},
	}
	var accRow []telegram.InlineKeyboardButton
	for i, r := range accs {
		if i >= 4 || r.b.AccountID <= 0 {
			break
		}
		accRow = append(accRow, telegram.Btn(fmt.Sprintf("管理 #%d", r.b.AccountID), fmt.Sprintf("mgr_acc:%d", r.b.AccountID)))
		if len(accRow) == 2 {
			rows = append(rows, accRow)
			accRow = nil
		}
	}
	if len(accRow) > 0 {
		rows = append(rows, accRow)
	}
	b.setManageBack(userID, "ops_conc")
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
			telegram.Btn("📈 看板", "ops_dash"),
		},
		[]telegram.InlineKeyboardButton{telegram.Btn("« 主面板", "home")},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showChannels(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, err := cli.ListChannelMonitors(ctx)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "渠道探测失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("渠道探测") + "\n\n")
	if len(items) == 0 {
		bld.WriteString("无探测任务。")
		return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
	}
	for _, m := range items {
		en := "OFF"
		if m.Enabled {
			en = "ON"
		}
		last := "-"
		if m.LastCheckedAt != nil {
			last = m.LastCheckedAt.Local().Format("01-02 15:04")
		}
		fmt.Fprintf(&bld, "• [%s] #%d %s\n  %s / %s · %s · %sms\n  上次 %s\n",
			en,
			m.ID,
			telegram.EscapeHTML(truncateRunes(m.Name, 18)),
			telegram.EscapeHTML(m.Provider),
			telegram.EscapeHTML(truncateRunes(m.PrimaryModel, 16)),
			telegram.EscapeHTML(m.PrimaryStatus),
			telegram.Code(itoa(m.PrimaryLatencyMS)),
			telegram.Code(last),
		)
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), opsViewKeyboard("ops_channels"))
}

func (b *Bot) showBadAccounts(ctx context.Context, chatID, msgID, userID int64) error {
	return b.showBadAccountsView(ctx, chatID, msgID, userID, "error", 0, "")
}

// showBadAccountsView lists problematic accounts.
// kind: error|rl|unsched|all; page is 0-based.
func (b *Bot) showBadAccountsView(ctx context.Context, chatID, msgID, userID int64, kind string, page int, notice string) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	if page < 0 {
		page = 0
	}
	kind = browse.NormalizeBadKind(kind)
	const pageSize = 8
	b.setManageBack(userID, fmt.Sprintf("ops_badacc:%s:%d", kind, page))

	items, total, title, scope, err := browse.LoadBadAccountsPage(ctx, cli, kind, page, pageSize)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "账号列表失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}

	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString(telegram.Bold(title) + "\n")
	fmt.Fprintf(&bld, "范围: %s · 第 %s 页 · 共约 %s\n\n",
		telegram.Code(scope), telegram.Code(strconv.Itoa(page+1)), telegram.Code(itoa(total)))
	if len(items) == 0 {
		bld.WriteString("当前无匹配账号。")
	}
	for _, a := range items {
		msg := a.ErrorMessage
		if msg == "" {
			msg = a.Status
		}
		fmt.Fprintf(&bld, "• #%d %s [%s/%s] %s\n  %s\n",
			a.ID,
			telegram.EscapeHTML(truncateRunes(a.Name, 16)),
			telegram.EscapeHTML(a.Platform),
			telegram.EscapeHTML(a.Status),
			schedLabel(a.Schedulable),
			telegram.EscapeHTML(truncateRunes(msg, 70)),
		)
	}

	rows := [][]telegram.InlineKeyboardButton{
		{
			telegram.Btn(errorTabLabel("error", kind, "error"), "ops_badacc:error:0"),
			telegram.Btn(errorTabLabel("限速", kind, "rl"), "ops_badacc:rl:0"),
			telegram.Btn(errorTabLabel("停调度", kind, "unsched"), "ops_badacc:unsched:0"),
		},
		{telegram.Btn(errorTabLabel("汇总", kind, "all"), "ops_badacc:all:0")},
	}
	for _, a := range items {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(fmt.Sprintf("管理 #%d %s", a.ID, truncateRunes(a.Name, 8)), fmt.Sprintf("mgr_acc:%d", a.ID)),
			telegram.Btn("实时", fmt.Sprintf("acc_live:%d", a.ID)),
			telegram.Btn("修复", fmt.Sprintf("live_act:heal:%d", a.ID)),
		})
	}
	// pagination
	nav := []telegram.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, telegram.Btn("« 上页", fmt.Sprintf("ops_badacc:%s:%d", kind, page-1)))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, telegram.Btn("下页 »", fmt.Sprintf("ops_badacc:%s:%d", kind, page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	switch kind {
	case "rl":
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("⏱ 批量清限速", "mgr_bulk_clear_rl"),
			telegram.Btn("🛠 一键修复", "mgr_bulk_heal"),
		})
	case "unsched":
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("▶️ 批量开调度", "mgr_bulk_sched_on"),
		})
	default:
		rows = append(rows,
			[]telegram.InlineKeyboardButton{
				telegram.Btn("🧹 批量清错", "mgr_bulk_clear"),
				telegram.Btn("♻️ 批量恢复", "mgr_bulk_recover"),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn("🛠 一键修复", "mgr_bulk_heal"),
				telegram.Btn("▶️ 批量开调度", "mgr_bulk_sched_on"),
			},
		)
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("➕ 一键监控 error", "ops_watch_errors"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("🔄 刷新", fmt.Sprintf("ops_badacc:%s:%d", kind, page)),
			telegram.Btn("« 运维菜单", "ops_menu"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("« 主面板", "home"),
		},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) watchErrorAccounts(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 20*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, _, err := cli.ListAccounts(ctx, 1, 50, "error")
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	added := 0
	skipped := 0
	for _, a := range items {
		_, err := b.addAccountMutate(ctx, chatID, userID, strconv.FormatInt(a.ID, 10))
		if err != nil {
			if strings.Contains(err.Error(), "已在列表") {
				skipped++
			}
			continue
		}
		added++
	}
	msg := fmt.Sprintf("✅ 已添加 %d 个异常账号到监控（跳过已存在 %d）\n\n%s",
		added, skipped, b.accountsText(userID))
	return b.editOrSend(ctx, chatID, msgID, msg, b.accountsKeyboard(userID))
}

func itoa(v any) string {
	switch n := v.(type) {
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case float64:
		return strconv.FormatInt(int64(n), 10)
	default:
		return fmt.Sprint(v)
	}
}

func formatCompactInt(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	if n < 1_000_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%.2fB", float64(n)/1_000_000_000)
}

func formatDurationSec(sec int64) string {
	d := time.Duration(sec) * time.Second
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

// handleLiveAction runs a quick manage action then refreshes the live account view.
func (b *Bot) handleLiveAction(ctx context.Context, chatID, msgID, userID int64, action string, accountID int64) error {
	cli, _, err := b.userClient(userID, 25*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	notice := ""
	switch action {
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
	default:
		notice = "未知操作"
	}
	return b.showAccountLiveWithNotice(ctx, chatID, msgID, userID, accountID, notice)
}

func (b *Bot) showAccountLive(ctx context.Context, chatID, msgID, userID, accountID int64) error {
	return b.showAccountLiveWithNotice(ctx, chatID, msgID, userID, accountID, "")
}

func (b *Bot) showAccountLiveWithNotice(ctx context.Context, chatID, msgID, userID, accountID int64, notice string) error {
	cli, p, err := b.userClient(userID, 20*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString(telegram.Bold(fmt.Sprintf("账号 #%d 实时", accountID)) + "\n\n")

	if acc, err := cli.GetAccount(ctx, accountID); err == nil && acc != nil {
		fmt.Fprintf(&bld, "名称: %s\n", telegram.Code(acc.Name))
		fmt.Fprintf(&bld, "平台/类型: %s / %s\n", telegram.Code(acc.Platform), telegram.Code(acc.Type))
		fmt.Fprintf(&bld, "状态: %s · 可调度: %s\n",
			telegram.Code(acc.Status),
			telegram.Code(fmt.Sprintf("%v", acc.Schedulable)))
		if acc.ErrorMessage != "" {
			fmt.Fprintf(&bld, "错误: %s\n", telegram.EscapeHTML(truncateRunes(acc.ErrorMessage, 120)))
		}
		if acc.RateLimitResetAt != nil {
			fmt.Fprintf(&bld, "限速重置: %s\n", telegram.Code(acc.RateLimitResetAt.Local().Format(time.RFC3339)))
		}
		if acc.OverloadUntil != nil {
			fmt.Fprintf(&bld, "过载至: %s\n", telegram.Code(acc.OverloadUntil.Local().Format(time.RFC3339)))
		}
	} else if err != nil {
		fmt.Fprintf(&bld, "账号详情失败: %s\n", telegram.EscapeHTML(err.Error()))
	}

	src := "passive"
	if p != nil {
		src = p.EffectiveSource()
	}
	fmt.Fprintf(&bld, "\n用量数据源: %s\n", telegram.Code(src))
	if usage, err := cli.GetAccountUsage(ctx, accountID, src, false); err != nil {
		fmt.Fprintf(&bld, "用量: %s\n", telegram.EscapeHTML(err.Error()))
	} else {
		wins := usage.Windows()
		if len(wins) == 0 {
			bld.WriteString("用量窗口: (无数据)\n")
		}
		for _, w := range wins {
			reset := ""
			if w.ResetsAt != nil {
				reset = " · " + w.ResetsAt.Local().Format("01-02 15:04")
			}
			fmt.Fprintf(&bld, "• %s: %s%s\n",
				telegram.EscapeHTML(w.Window),
				telegram.Code(fmt.Sprintf("%.1f%%", w.Utilization)),
				telegram.EscapeHTML(reset),
			)
		}
		if usage.Error != "" {
			fmt.Fprintf(&bld, "提示: %s\n", telegram.EscapeHTML(usage.Error))
		}
	}
	if today, err := cli.GetAccountTodayStats(ctx, accountID); err == nil && today != nil {
		fmt.Fprintf(&bld, "\n今日: req=%s tok=%s cost=%s\n",
			telegram.Code(strconv.FormatInt(today.Requests, 10)),
			telegram.Code(strconv.FormatInt(today.Tokens, 10)),
			telegram.Code(fmt.Sprintf("%.4f", today.Cost)),
		)
	}

	if av, err := cli.GetAccountAvailability(ctx); err == nil && av != nil {
		if st, ok := av.Account[strconv.FormatInt(accountID, 10)]; ok {
			fmt.Fprintf(&bld, "\n运行态: available=%v error=%v rl=%v ol=%v\n",
				st.IsAvailable, st.HasError, st.IsRateLimited, st.IsOverloaded)
		}
	}

	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新", fmt.Sprintf("acc_live:%d", accountID))},
	}
	if b.isAdmin(userID) {
		rows = append(rows,
			[]telegram.InlineKeyboardButton{
				telegram.Btn("🛠 一键修复", fmt.Sprintf("live_act:heal:%d", accountID)),
				telegram.Btn("🧹 清错误", fmt.Sprintf("live_act:clear_err:%d", accountID)),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn("⏱ 清限速", fmt.Sprintf("live_act:clear_rl:%d", accountID)),
				telegram.Btn("♻️ 恢复", fmt.Sprintf("live_act:recover:%d", accountID)),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn("🧰 完整管理", fmt.Sprintf("mgr_acc:%d", accountID)),
			},
		)
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("« 账号详情", fmt.Sprintf("acc:%d", accountID)),
			telegram.Btn("« 列表", "cfg_acc"),
		},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}
