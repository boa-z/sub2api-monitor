package panel

import (
	"context"
	"fmt"
	"regexp"
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
	return opsKeyboardFor(nil, true)
}

// opsKeyboardFor builds the ops hub keyboard. When stats are present, labels
// include live error/rate-limit counts for faster triage.
// canWrite controls bulk heal / write entry visibility for viewers.
func opsKeyboardFor(stats *sub2api.DashboardStats, canWrite bool) *telegram.InlineKeyboardMarkup {
	badLabel := "📋 异常账号"
	rlLabel := "⏱ 限速"
	errLabel := "❌ 错误"
	mgrLabel := "🧰 账号管理"
	if !canWrite {
		mgrLabel = "🧰 账号浏览"
	}
	if stats != nil {
		if stats.ErrorAccounts > 0 {
			badLabel = fmt.Sprintf("📋 异常 %v", stats.ErrorAccounts)
		}
		if stats.RatelimitAccounts > 0 {
			rlLabel = fmt.Sprintf("⏱ 限速 %v", stats.RatelimitAccounts)
		}
	}
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("📈 看板", "ops_dash"), telegram.Btn("✅ 可用性", "ops_avail")},
		{telegram.Btn("🚨 告警", "ops_alerts"), telegram.Btn(errLabel, "ops_errors:all:0")},
		{telegram.Btn("⚙️ 并发", "ops_conc"), telegram.Btn("📉 流量", "ops_traf")},
		{telegram.Btn("📡 渠道探测", "ops_channels")},
	}
	// health-aware second action row
	if stats != nil && (stats.ErrorAccounts > 0 || stats.RatelimitAccounts > 0) {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(badLabel, "ops_badacc:error:0"),
			telegram.Btn(rlLabel, "ops_badacc:rl:0"),
		})
		if canWrite {
			rows = append(rows, []telegram.InlineKeyboardButton{
				telegram.Btn("🛠 批量一键修复", "mgr_bulk_heal"),
				telegram.Btn(mgrLabel, "mgr_menu"),
			})
		} else {
			rows = append(rows, []telegram.InlineKeyboardButton{
				telegram.Btn(mgrLabel, "mgr_menu"),
			})
		}
	} else {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(badLabel, "ops_badacc:error:0"),
			telegram.Btn(mgrLabel, "mgr_menu"),
		})
	}
	rows = append(rows, []telegram.InlineKeyboardButton{
		telegram.Btn("🔄 刷新菜单", "ops_menu"), telegram.Btn("« 主面板", "home"),
	})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// opsViewKeyboard is ops menu plus a self-refresh button for the current view.
func opsViewKeyboard(refreshData string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("🔄 刷新", refreshData), telegram.Btn("« 运维菜单", "ops_menu")},
			{telegram.Btn("📈 看板", "ops_dash"), telegram.Btn("✅ 可用性", "ops_avail")},
			{telegram.Btn("🚨 告警", "ops_alerts"), telegram.Btn("❌ 错误", "ops_errors:all:0")},
			{telegram.Btn("⚙️ 并发", "ops_conc"), telegram.Btn("📉 流量", "ops_traf")},
			{telegram.Btn("📋 异常账号", "ops_badacc:error:0"), telegram.Btn("📡 渠道", "ops_channels")},
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
			jump = append(jump, telegram.Btn(fmt.Sprintf("过载 %v", stats.OverloadAccounts), "ops_badacc:ol:0"))
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
			telegram.Btn("📉 流量", "ops_traf"),
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
			if traf, err := cli.GetRealtimeTraffic(ctx, "5min"); err == nil && traf != nil && traf.Enabled {
				fmt.Fprintf(&bld, "流量(5min): QPS %s", telegram.Code(fmt.Sprintf("%.3f", traf.CurrentQPS())))
				if traf.CurrentTPS() > 0 {
					fmt.Fprintf(&bld, " · TPS %s", telegram.Code(fmt.Sprintf("%.3f", traf.CurrentTPS())))
				}
				bld.WriteString("\n")
			}
			bld.WriteString("\n")
		}
	}
	bld.WriteString(`基于当前连接的 Admin API：

• 看板 — 账号/用量/实时流量
• 可用性 — 平台/分组可用率
• 告警 — 内置 alert-events
• 错误 — 请求/上游（分页·解决·修复·实时）
• 并发 / 流量 / 渠道探测
• 异常账号 — error/限速/停调度/汇总（分页·管理/实时/修复）

点下方按钮查看；数据实时拉取。`)
	return bld.String()
}

func (b *Bot) showOpsMenu(ctx context.Context, chatID, msgID, userID int64) error {
	var stats *sub2api.DashboardStats
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil {
			stats = st
		}
	}
	return b.editOrSend(ctx, chatID, msgID, b.opsMenuText(ctx, userID), opsKeyboardFor(stats, b.canOpsWrite(userID)))
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
		qps := traf.CurrentQPS()
		tps := traf.CurrentTPS()
		peak := traf.PeakQPS()
		line := fmt.Sprintf("流量(%s): QPS %s",
			telegram.EscapeHTML(traf.WindowLabel()),
			telegram.Code(fmt.Sprintf("%.3f", qps)))
		if tps > 0 {
			line += " · TPS " + telegram.Code(fmt.Sprintf("%.3f", tps))
		}
		if peak > 0 {
			line += " · 峰值QPS " + telegram.Code(fmt.Sprintf("%.3f", peak))
		}
		bld.WriteString(line + "\n")
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
		return b.editOrSend(ctx, chatID, msgID, bld.String(), alertsKeyboard(nil, 0, 0))
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
	firingN, resolvedN := 0, 0
	var idTexts []string
	for _, ev := range events {
		st := strings.ToLower(ev.Status)
		switch {
		case st == "firing" || st == "open" || st == "active":
			firingN++
		case st == "resolved" || st == "ok" || st == "closed":
			resolvedN++
		}
		idTexts = append(idTexts, ev.DisplayTitle(), ev.DisplayMessage(), ev.MetricType, ev.Name)
	}
	fmt.Fprintf(&bld, "汇总: 🔴 触发 %s · 🟢 已恢复 %s · 共 %s\n\n",
		telegram.Code(strconv.Itoa(firingN)),
		telegram.Code(strconv.Itoa(resolvedN)),
		telegram.Code(strconv.Itoa(len(events))),
	)
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
	accIDs := extractAccountIDs(idTexts...)
	b.setManageBack(userID, "ops_alerts")
	return b.editOrSend(ctx, chatID, msgID, bld.String(), alertsKeyboard(accIDs, firingN, resolvedN))
}

func alertsKeyboard(accIDs []int64, firingN, resolvedN int) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新", "ops_alerts"), telegram.Btn("« 运维菜单", "ops_menu")},
	}
	if len(accIDs) > 0 {
		var row []telegram.InlineKeyboardButton
		for i, id := range accIDs {
			if i >= 4 {
				break
			}
			row = append(row, telegram.Btn(fmt.Sprintf("管理 #%d", id), fmt.Sprintf("mgr_acc:%d", id)))
			if len(row) == 2 {
				rows = append(rows, row)
				row = nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	jump := []telegram.InlineKeyboardButton{
		telegram.Btn("❌ 错误", "ops_errors:all:0"),
		telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
	}
	if firingN > 0 {
		jump = append(jump, telegram.Btn("📈 看板", "ops_dash"))
	} else {
		jump = append(jump, telegram.Btn("✅ 可用性", "ops_avail"))
	}
	rows = append(rows, jump)
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 主面板", "home")})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
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
	canWrite := b.canOpsWrite(userID)
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
			writeErrorItems(&bld, pageData, "u", 8, canWrite, &rows)
			rows = append(rows, errorPageNav("u", page, pageData)...)
		}
	case "r":
		pageData, err2 := cli.ListRequestErrors(ctx, page+1, 10)
		fmt.Fprintf(&bld, "标签: %s · 第 %d 页\n\n", telegram.Code("请求"), page+1)
		if err2 != nil {
			fmt.Fprintf(&bld, "拉取失败: %s\n", telegram.EscapeHTML(err2.Error()))
		} else {
			writeErrorItems(&bld, pageData, "r", 8, canWrite, &rows)
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
			writeErrorItems(&bld, up, "u", 5, canWrite, &rows)
		}
		bld.WriteString("\n" + telegram.Bold("请求错误") + "\n")
		if err2 != nil {
			fmt.Fprintf(&bld, "拉取失败: %s\n", telegram.EscapeHTML(err2.Error()))
		} else {
			writeErrorItems(&bld, req, "r", 3, canWrite, &rows)
		}
	}

	if canWrite {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("✅ 解决上游", "oe:resolve_all:u"),
			telegram.Btn("✅ 解决请求", "oe:resolve_all:r"),
		})
	}
	rows = append(rows,
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
func writeErrorItems(bld *strings.Builder, page *sub2api.OpsErrorPage, kind string, maxShow int, canWrite bool, rows *[][]telegram.InlineKeyboardButton) {
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
		btnRow := []telegram.InlineKeyboardButton{}
		if canWrite {
			btnRow = append(btnRow, telegram.Btn(fmt.Sprintf("✅ #%d", e.ID), fmt.Sprintf("oe:r:%s:%d", kind, e.ID)))
		}
		if e.AccountID > 0 {
			if canWrite {
				btnRow = append(btnRow, telegram.Btn("修复", fmt.Sprintf("live_act:heal:%d", e.AccountID)))
			}
			btnRow = append(btnRow,
				telegram.Btn("实时", fmt.Sprintf("acc_live:%d", e.AccountID)),
				telegram.Btn("查看", fmt.Sprintf("mgr_acc:%d", e.AccountID)),
			)
		}
		if len(btnRow) == 0 {
			continue
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
	// top loaded groups
	var groups []crow
	for k, v := range snap.Group {
		name := v.GroupName
		if name == "" {
			name = k
		}
		if name == "" && v.GroupID > 0 {
			name = fmt.Sprintf("#%d", v.GroupID)
		}
		if v.CurrentInUse > 0 || v.LoadPercentage > 0 || v.WaitingInQueue > 0 {
			groups = append(groups, crow{name, v})
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].b.LoadPercentage > groups[j].b.LoadPercentage })
	bld.WriteString("\n" + telegram.Bold("高负载分组") + "\n")
	if len(groups) == 0 {
		bld.WriteString("当前无分组占用。\n")
	}
	for i, r := range groups {
		if i >= 6 {
			fmt.Fprintf(&bld, "… 另有 %d 个\n", len(groups)-6)
			break
		}
		idPart := ""
		if r.b.GroupID > 0 {
			idPart = fmt.Sprintf("#%d ", r.b.GroupID)
		}
		fmt.Fprintf(&bld, "• %s%s: %s/%s (%.0f%%) wait=%s\n",
			idPart,
			telegram.EscapeHTML(truncateRunes(r.name, 14)),
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
		[]telegram.InlineKeyboardButton{
			telegram.Btn("🏷 分组列表", "mgr_groups"),
			telegram.Btn("« 主面板", "home"),
		},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showTraffic(ctx context.Context, chatID, msgID, userID int64, window string) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	window = normalizeTrafficWindow(window)
	traf, err := cli.GetRealtimeTraffic(ctx, window)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "流量查询失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("实时流量") + "\n")
	fmt.Fprintf(&bld, "更新: %s\n", telegram.Code(time.Now().Local().Format("15:04:05")))
	if traf == nil {
		bld.WriteString("无流量数据。")
		return b.editOrSend(ctx, chatID, msgID, bld.String(), trafficKeyboard(window))
	}
	if !traf.Enabled {
		bld.WriteString("服务端实时监控未启用（ops realtime-traffic disabled）。\n")
		return b.editOrSend(ctx, chatID, msgID, bld.String(), trafficKeyboard(window))
	}
	winLabel := traf.WindowLabel()
	if winLabel == "" {
		winLabel = window
	}
	qps, tps, peak := traf.CurrentQPS(), traf.CurrentTPS(), traf.PeakQPS()
	fmt.Fprintf(&bld, "窗口: %s\n", telegram.Code(winLabel))
	fmt.Fprintf(&bld, "当前 QPS: %s\n", telegram.Code(fmt.Sprintf("%.3f", qps)))
	if tps > 0 {
		fmt.Fprintf(&bld, "当前 TPS: %s\n", telegram.Code(fmt.Sprintf("%.3f", tps)))
	}
	if peak > 0 {
		fmt.Fprintf(&bld, "峰值 QPS: %s\n", telegram.Code(fmt.Sprintf("%.3f", peak)))
	}
	if !traf.Timestamp.IsZero() {
		fmt.Fprintf(&bld, "采样时间: %s\n", telegram.Code(traf.Timestamp.Local().Format("01-02 15:04:05")))
	}
	bld.WriteString("\n切换下方窗口可对比不同时间尺度；QPS 骤降可结合看板/异常账号排查。")
	return b.editOrSend(ctx, chatID, msgID, bld.String(), trafficKeyboard(window))
}

func trafficWindows() []string {
	return []string{"1min", "5min", "15min", "1h"}
}

func normalizeTrafficWindow(w string) string {
	w = strings.TrimSpace(strings.ToLower(w))
	switch w {
	case "", "default", "traf", "ops_traf":
		return "5min"
	case "1m", "1min", "60s":
		return "1min"
	case "5m", "5min":
		return "5min"
	case "15m", "15min":
		return "15min"
	case "1h", "60m", "60min":
		return "1h"
	default:
		return w
	}
}

func trafficKeyboard(window string) *telegram.InlineKeyboardMarkup {
	window = normalizeTrafficWindow(window)
	var winRow []telegram.InlineKeyboardButton
	for _, w := range trafficWindows() {
		label := w
		if w == window {
			label = "· " + w
		}
		winRow = append(winRow, telegram.Btn(label, "ops_traf:"+w))
	}
	return &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			winRow,
			{telegram.Btn("🔄 刷新", "ops_traf:"+window), telegram.Btn("« 运维菜单", "ops_menu")},
			{telegram.Btn("📈 看板", "ops_dash"), telegram.Btn("⚙️ 并发", "ops_conc"), telegram.Btn("📋 异常账号", "ops_badacc:error:0")},
			{telegram.Btn("« 主面板", "home")},
		},
	}
}

func (b *Bot) showChannels(ctx context.Context, chatID, msgID, userID int64, tab string) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, err := cli.ListChannelMonitors(ctx)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "渠道探测失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	tab = normalizeChannelTab(tab)
	if tab == "" {
		tab = normalizeChannelTab(b.getChannelTab(userID))
	}
	if tab == "" {
		tab = "all"
	}
	b.setChannelTab(userID, tab)

	onN, okN, badN := 0, 0, 0
	for _, m := range items {
		if m.Enabled {
			onN++
		}
		if channelIsBad(m) {
			badN++
		} else if m.Enabled {
			okN++
		}
	}
	filtered := filterChannelMonitors(items, tab)

	var bld strings.Builder
	bld.WriteString(telegram.Bold("渠道探测") + "\n")
	fmt.Fprintf(&bld, "汇总: 启用 %s · 正常 %s · 异常 %s · 共 %s\n",
		telegram.Code(strconv.Itoa(onN)),
		telegram.Code(strconv.Itoa(okN)),
		telegram.Code(strconv.Itoa(badN)),
		telegram.Code(strconv.Itoa(len(items))),
	)
	fmt.Fprintf(&bld, "筛选: %s · 本页 %s\n点任务查看详情\n\n",
		telegram.Code(channelTabLabel(tab)),
		telegram.Code(strconv.Itoa(len(filtered))),
	)
	if len(filtered) == 0 {
		bld.WriteString("无匹配探测任务。")
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		bi := channelIsBad(filtered[i])
		bj := channelIsBad(filtered[j])
		if bi != bj {
			return bi
		}
		return filtered[i].ID < filtered[j].ID
	})
	rows := [][]telegram.InlineKeyboardButton{}
	for i, m := range filtered {
		if i >= 10 {
			fmt.Fprintf(&bld, "… 另有 %d 个（可换筛选）\n", len(filtered)-10)
			break
		}
		en := "OFF"
		if m.Enabled {
			en = "ON"
		}
		last := "-"
		if m.LastCheckedAt != nil {
			last = m.LastCheckedAt.Local().Format("01-02 15:04")
		}
		flag := ""
		if channelIsBad(m) {
			flag = " ⚠"
		}
		fmt.Fprintf(&bld, "• [%s]%s #%d %s\n  %s / %s · %s · %sms\n  上次 %s",
			en,
			flag,
			m.ID,
			telegram.EscapeHTML(truncateRunes(m.Name, 18)),
			telegram.EscapeHTML(m.Provider),
			telegram.EscapeHTML(truncateRunes(m.PrimaryModel, 16)),
			telegram.EscapeHTML(m.PrimaryStatus),
			telegram.Code(itoa(m.PrimaryLatencyMS)),
			telegram.Code(last),
		)
		if m.Availability7d > 0 {
			av := m.Availability7d
			if av <= 1 {
				av *= 100
			}
			fmt.Fprintf(&bld, " · 7d %.1f%%", av)
		}
		bld.WriteString("\n")
		label := fmt.Sprintf("#%d %s", m.ID, truncateRunes(m.Name, 10))
		if m.Name == "" {
			label = fmt.Sprintf("#%d", m.ID)
		}
		if channelIsBad(m) {
			label = "⚠ " + label
		}
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(label, fmt.Sprintf("ops_ch:%d", m.ID)),
		})
	}
	// filter tabs
	tabRow := []telegram.InlineKeyboardButton{}
	for _, st := range []struct{ label, val string }{
		{"全部", "all"},
		{"启用", "on"},
		{"正常", "ok"},
		{"异常", "bad"},
	} {
		lab := st.label
		if st.val == tab {
			lab = "· " + lab
		}
		tabRow = append(tabRow, telegram.Btn(lab, "ops_channels:"+st.val))
	}
	rows = append(rows, tabRow)
	rows = append(rows, []telegram.InlineKeyboardButton{
		telegram.Btn("🔄 刷新", "ops_channels:"+tab),
		telegram.Btn("« 运维菜单", "ops_menu"),
	})
	rows = append(rows, []telegram.InlineKeyboardButton{
		telegram.Btn("📈 看板", "ops_dash"),
		telegram.Btn("✅ 可用性", "ops_avail"),
		telegram.Btn("🚨 告警", "ops_alerts"),
	})
	if badN > 0 {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
			telegram.Btn("错误", "ops_errors:all:0"),
		})
	}
	rows = append(rows, []telegram.InlineKeyboardButton{
		telegram.Btn("« 主面板", "home"),
	})
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showChannelDetail(ctx context.Context, chatID, msgID, userID, channelID int64) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	items, err := cli.ListChannelMonitors(ctx)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "渠道探测失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	var m *sub2api.ChannelMonitor
	for i := range items {
		if items[i].ID == channelID {
			mm := items[i]
			m = &mm
			break
		}
	}
	if m == nil {
		tab := normalizeChannelTab(b.getChannelTab(userID))
		if tab == "" {
			tab = "all"
		}
		return b.showChannels(ctx, chatID, msgID, userID, tab)
	}
	tab := normalizeChannelTab(b.getChannelTab(userID))
	if tab == "" {
		tab = "all"
	}
	b.setManageBack(userID, "ops_channels:"+tab)

	var bld strings.Builder
	bld.WriteString(telegram.Bold(fmt.Sprintf("渠道探测 #%d", m.ID)) + "\n\n")
	name := m.Name
	if name == "" {
		name = "(未命名)"
	}
	fmt.Fprintf(&bld, "名称: %s\n", telegram.Code(truncateRunes(name, 40)))
	en := "关闭"
	if m.Enabled {
		en = "启用"
	}
	fmt.Fprintf(&bld, "状态: %s", telegram.Code(en))
	if channelIsBad(*m) {
		bld.WriteString(" · " + telegram.Bold("异常"))
	}
	bld.WriteString("\n")
	fmt.Fprintf(&bld, "提供商: %s\n", telegram.Code(m.Provider))
	fmt.Fprintf(&bld, "主模型: %s\n", telegram.Code(truncateRunes(m.PrimaryModel, 48)))
	fmt.Fprintf(&bld, "探测结果: %s · 延迟 %s ms\n",
		telegram.Code(m.PrimaryStatus),
		telegram.Code(itoa(m.PrimaryLatencyMS)),
	)
	if m.IntervalSeconds > 0 {
		fmt.Fprintf(&bld, "间隔: %s s\n", telegram.Code(itoa(m.IntervalSeconds)))
	}
	last := "(无)"
	if m.LastCheckedAt != nil {
		last = m.LastCheckedAt.Local().Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(&bld, "上次检查: %s\n", telegram.Code(last))
	if m.Availability7d > 0 {
		av := m.Availability7d
		if av <= 1 {
			av *= 100
		}
		fmt.Fprintf(&bld, "7 日可用率: %s%%\n", telegram.Code(fmt.Sprintf("%.1f", av)))
	}
	bld.WriteString("\n只读详情；触发/启停需上游 Admin 写接口支持后再开放。")

	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("🔄 刷新", fmt.Sprintf("ops_ch:%d", m.ID))},
			{
				telegram.Btn("« 渠道列表", "ops_channels:"+tab),
				telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
			},
			{
				telegram.Btn("📈 看板", "ops_dash"),
				telegram.Btn("« 运维菜单", "ops_menu"),
			},
		},
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
}

func normalizeChannelTab(tab string) string {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case "", "all", "全部":
		return "all"
	case "on", "enabled", "启用":
		return "on"
	case "ok", "healthy", "正常":
		return "ok"
	case "bad", "error", "fail", "异常":
		return "bad"
	default:
		return "all"
	}
}

func channelTabLabel(tab string) string {
	switch normalizeChannelTab(tab) {
	case "on":
		return "启用"
	case "ok":
		return "正常"
	case "bad":
		return "异常"
	default:
		return "全部"
	}
}

func filterChannelMonitors(items []sub2api.ChannelMonitor, tab string) []sub2api.ChannelMonitor {
	tab = normalizeChannelTab(tab)
	if tab == "all" {
		out := make([]sub2api.ChannelMonitor, len(items))
		copy(out, items)
		return out
	}
	out := make([]sub2api.ChannelMonitor, 0, len(items))
	for _, m := range items {
		switch tab {
		case "on":
			if m.Enabled {
				out = append(out, m)
			}
		case "ok":
			if m.Enabled && !channelIsBad(m) {
				out = append(out, m)
			}
		case "bad":
			if channelIsBad(m) {
				out = append(out, m)
			}
		}
	}
	return out
}

func channelIsBad(m sub2api.ChannelMonitor) bool {
	if !m.Enabled {
		return false
	}
	st := strings.ToLower(strings.TrimSpace(m.PrimaryStatus))
	if st == "" || st == "ok" || st == "success" || st == "up" || st == "healthy" || st == "pass" {
		return false
	}
	return true
}

func channelsKeyboard(onN, okN, badN int) *telegram.InlineKeyboardMarkup {
	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新", "ops_channels"), telegram.Btn("« 运维菜单", "ops_menu")},
		{
			telegram.Btn("📈 看板", "ops_dash"),
			telegram.Btn("✅ 可用性", "ops_avail"),
			telegram.Btn("🚨 告警", "ops_alerts"),
		},
	}
	if badN > 0 {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("📋 异常账号", "ops_badacc:error:0"),
			telegram.Btn("❌ 错误", "ops_errors:all:0"),
		})
	}
	rows = append(rows, []telegram.InlineKeyboardButton{telegram.Btn("« 主面板", "home")})
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
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
	canWrite := b.canOpsWrite(userID)
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
			telegram.Btn(errorTabLabel("过载", kind, "ol"), "ops_badacc:ol:0"),
		},
		{
			telegram.Btn(errorTabLabel("停调度", kind, "unsched"), "ops_badacc:unsched:0"),
			telegram.Btn(errorTabLabel("汇总", kind, "all"), "ops_badacc:all:0"),
		},
	}
	for _, a := range items {
		row := []telegram.InlineKeyboardButton{
			telegram.Btn(fmt.Sprintf("查看 #%d %s", a.ID, truncateRunes(a.Name, 8)), fmt.Sprintf("mgr_acc:%d", a.ID)),
			telegram.Btn("实时", fmt.Sprintf("acc_live:%d", a.ID)),
		}
		if canWrite {
			quick := "修复"
			quickData := fmt.Sprintf("live_act:heal:%d", a.ID)
			if kind == "rl" || kind == "ol" {
				quick = "清限速"
				quickData = fmt.Sprintf("live_act:clear_rl:%d", a.ID)
			} else if kind == "unsched" {
				quick = "开调度"
				quickData = fmt.Sprintf("live_act:sched:%d", a.ID)
			}
			row = append(row, telegram.Btn(quick, quickData))
		}
		rows = append(rows, row)
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
	if canWrite {
		switch kind {
		case "rl", "ol":
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
		// contextual one-tap watch for current triage tab
		watchLabel, watchData := "➕ 一键监控 error", "ops_watch:error"
		switch kind {
		case "rl":
			watchLabel, watchData = "➕ 一键监控限速", "ops_watch:rl"
		case "ol":
			watchLabel, watchData = "➕ 一键监控过载", "ops_watch:ol"
		case "unsched":
			watchLabel, watchData = "➕ 一键监控停调度", "ops_watch:unsched"
		case "all":
			watchLabel, watchData = "➕ 一键监控本页异常", "ops_watch:all"
		}
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(watchLabel, watchData),
		})
	}
	rows = append(rows,
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
	return b.watchAccountsByScope(ctx, chatID, msgID, userID, "error")
}

// watchAccountsByScope bulk-adds accounts from a bad-account scope into the watch list.
// scope: error|rl|ol|unsched|all (same tokens as ops_badacc).
func (b *Bot) watchAccountsByScope(ctx context.Context, chatID, msgID, userID int64, scope string) error {
	cli, _, err := b.userClient(userID, 20*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	scope = browse.NormalizeBadKind(scope)
	// pull first pages of matching accounts
	items, total, title, _, err := browse.LoadBadAccountsPage(ctx, cli, scope, 0, 50)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "拉取失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	added, skipped := 0, 0
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
	watchN := 0
	if p, ok := b.users.Get(userID); ok {
		watchN = len(p.Accounts)
	}
	notice := fmt.Sprintf("✅ %s：已添加 %d 个到监控（跳过已存在 %d · 本页/扫描 %d · 共约 %s）\n当前监控列表共 %d 个账号",
		title, added, skipped, len(items), itoa(total), watchN)
	return b.showBadAccountsView(ctx, chatID, msgID, userID, scope, 0, notice)
}

// adminHealthLine returns a short instance health summary for admins (best-effort).
// adminHealthSnapshot returns a one-line dashboard health summary and whether
// error/rate-limit accounts need attention. Single API fetch for home panels.
func adminHealthSnapshot(ctx context.Context, cli *sub2api.Client) (line string, issues bool) {
	if cli == nil {
		return "", false
	}
	st, err := cli.GetDashboardStats(ctx)
	if err != nil || st == nil {
		return "", false
	}
	line = fmt.Sprintf("实例健康: 正常 %s · 异常 %s · 限速 %s · 过载 %s",
		itoa(st.NormalAccounts), itoa(st.ErrorAccounts), itoa(st.RatelimitAccounts), itoa(st.OverloadAccounts))
	issues = st.ErrorAccounts > 0 || st.RatelimitAccounts > 0
	return line, issues
}

// adminHealthLine is a thin wrapper when only the text is needed.
func adminHealthLine(ctx context.Context, cli *sub2api.Client) string {
	line, _ := adminHealthSnapshot(ctx, cli)
	return line
}

var accountIDRe = regexp.MustCompile(`(?i)(?:account[_\s-]?id|账号\s*(?:id|ID)?|acc(?:ount)?)\s*[#:=\s]\s*(\d{1,12})|(?:^|[^\d])#(\d{1,12})\b`)

// extractAccountIDs pulls likely account IDs from free text (alerts etc).
// Prefers labeled forms (account_id / 账号 / #id) over bare numbers to reduce false positives.
func extractAccountIDs(texts ...string) []int64 {
	seen := map[int64]struct{}{}
	var out []int64
	for _, t := range texts {
		for _, m := range accountIDRe.FindAllStringSubmatch(t, -1) {
			raw := ""
			if len(m) >= 2 && m[1] != "" {
				raw = m[1]
			} else if len(m) >= 3 && m[2] != "" {
				raw = m[2]
			}
			if raw == "" {
				continue
			}
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
			if len(out) >= 6 {
				return out
			}
		}
	}
	return out
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
	case "sched":
		if _, err := cli.SetSchedulable(ctx, accountID, true); err != nil {
			notice = "❌ 开启调度失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已开启调度"
		}
	case "refresh":
		if _, err := cli.RefreshAccount(ctx, accountID); err != nil {
			notice = "❌ 刷新凭据失败: " + telegram.EscapeHTML(err.Error())
		} else {
			notice = "✅ 已刷新账号/凭据"
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
	force := strings.EqualFold(src, "active")
	forceLabel := "缓存"
	if force {
		forceLabel = "强制刷新"
	}
	fmt.Fprintf(&bld, "\n用量数据源: %s · %s\n", telegram.Code(src), telegram.Code(forceLabel))
	thMap := map[string]float64{}
	if p != nil {
		ths := p.Thresholds
		if len(ths) == 0 {
			ths = b.defaults
		}
		// account-level overrides if watched
		for _, a := range p.Accounts {
			if a.ID == accountID && len(a.Thresholds) > 0 {
				ths = a.Thresholds
				break
			}
		}
		for _, th := range ths {
			thMap[sub2api.NormalizeWindow(th.Window)] = th.UtilizationGTE
		}
	}
	if usage, err := cli.GetAccountUsage(ctx, accountID, src, force); err != nil {
		fmt.Fprintf(&bld, "用量: %s\n", telegram.EscapeHTML(err.Error()))
	} else {
		sum, hit := usage.CompactUsageSummary(thMap, 5)
		if sum == "" {
			sum = "(无数据)"
		}
		mark := ""
		if hit {
			mark = " ⚠️"
		}
		fmt.Fprintf(&bld, "用量: %s%s\n", telegram.EscapeHTML(sum), mark)
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
	if snap, err := cli.GetConcurrency(ctx); err == nil && snap != nil && snap.Enabled {
		for _, v := range snap.Account {
			if v.AccountID == accountID {
				fmt.Fprintf(&bld, "并发: %s/%s (%.0f%%) wait=%s\n",
					telegram.Code(strconv.Itoa(v.CurrentInUse)),
					telegram.Code(strconv.Itoa(v.MaxCapacity)),
					v.LoadPercentage,
					telegram.Code(strconv.Itoa(v.WaitingInQueue)),
				)
				break
			}
		}
	}

	rows := [][]telegram.InlineKeyboardButton{
		{telegram.Btn("🔄 刷新", fmt.Sprintf("acc_live:%d", accountID))},
	}
	if b.canOpsWrite(userID) {
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
				telegram.Btn("▶️ 开调度", fmt.Sprintf("live_act:sched:%d", accountID)),
				telegram.Btn("🔄 刷新凭据", fmt.Sprintf("live_act:refresh:%d", accountID)),
			},
			[]telegram.InlineKeyboardButton{
				telegram.Btn("🧰 完整管理", fmt.Sprintf("mgr_acc:%d", accountID)),
			},
		)
	} else if b.canOpsRead(userID) {
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn("👁 账号详情", fmt.Sprintf("mgr_acc:%d", accountID)),
		})
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			b.manageBackButton(userID),
			telegram.Btn("« 列表", "cfg_acc"),
		},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}
