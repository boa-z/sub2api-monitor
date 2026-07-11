package panel

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
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
			{telegram.Btn("🚨 告警", "ops_alerts"), telegram.Btn("❌ 错误", "ops_errors")},
			{telegram.Btn("⚙️ 并发", "ops_conc"), telegram.Btn("📡 渠道探测", "ops_channels")},
			{telegram.Btn("📋 异常账号", "ops_badacc"), telegram.Btn("🧰 账号管理", "mgr_menu")},
			{telegram.Btn("🔄 刷新菜单", "ops_menu"), telegram.Btn("« 主面板", "home")},
		},
	}
}

func (b *Bot) opsMenuText() string {
	return telegram.Bold("运维视图") + `

基于你当前连接的 Sub2API Admin API，只读查看：

• 看板 — 账号/用量/实时 RPM
• 可用性 — 平台/分组可用率
• 告警 — 内置 alert-events
• 错误 — 请求/上游错误（可标记已解决）
• 并发 — 账号/分组负载
• 渠道探测 — channel monitors
• 异常账号 — status=error 列表

点下方按钮查看；数据实时拉取。`
}

func (b *Bot) showOpsMenu(ctx context.Context, chatID, msgID int64) error {
	return b.editOrSend(ctx, chatID, msgID, b.opsMenuText(), opsKeyboard())
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
	return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
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
	return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
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
	return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
}

func (b *Bot) showErrors(ctx context.Context, chatID, msgID, userID int64) error {
	return b.showErrorsNotice(ctx, chatID, msgID, userID, "")
}

func (b *Bot) showErrorsNotice(ctx context.Context, chatID, msgID, userID int64, notice string) error {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	up, err1 := cli.ListUpstreamErrors(ctx, 1, 15)
	req, err2 := cli.ListRequestErrors(ctx, 1, 10)
	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString(telegram.Bold("近期错误") + "（优先未解决）\n\n")
	if err1 != nil && err2 != nil {
		return b.editOrSend(ctx, chatID, msgID, "错误列表失败: "+telegram.EscapeHTML(err1.Error()), opsKeyboard())
	}

	rows := [][]telegram.InlineKeyboardButton{}

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

	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("✅ 解决上游", "oe:resolve_all:u"),
			telegram.Btn("✅ 解决请求", "oe:resolve_all:r"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("🔄 刷新", "ops_errors"),
			telegram.Btn("« 运维菜单", "ops_menu"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("« 主面板", "home"),
		},
	)
	return b.editOrSend(ctx, chatID, msgID, bld.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// writeErrorItems renders error lines and appends resolve/manage buttons.
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
		fmt.Fprintf(bld, "• #%d [%s] %s %s\n  %s · %s\n  %s\n",
			e.ID,
			telegram.EscapeHTML(e.Severity),
			telegram.Code(strconv.Itoa(e.StatusCode)),
			telegram.EscapeHTML(truncateRunes(name, 14)),
			telegram.EscapeHTML(e.Platform),
			telegram.EscapeHTML(truncateRunes(model, 18)),
			telegram.EscapeHTML(truncateRunes(e.Message, 70)),
		)
		btnRow := []telegram.InlineKeyboardButton{
			telegram.Btn(fmt.Sprintf("✅ 解决 #%d", e.ID), fmt.Sprintf("oe:r:%s:%d", kind, e.ID)),
		}
		if e.AccountID > 0 {
			btnRow = append(btnRow, telegram.Btn(fmt.Sprintf("管理 #%d", e.AccountID), fmt.Sprintf("mgr_acc:%d", e.AccountID)))
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
	if err := cli.ResolveOpsError(ctx, apiKind, errorID); err != nil {
		return b.showErrorsNotice(ctx, chatID, msgID, userID, "❌ 标记失败: "+telegram.EscapeHTML(err.Error()))
	}
	return b.showErrorsNotice(ctx, chatID, msgID, userID, fmt.Sprintf("✅ 已标记错误 #%d 为已解决", errorID))
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
	if err != nil {
		return b.showErrorsNotice(ctx, chatID, msgID, userID, "❌ 拉取失败: "+telegram.EscapeHTML(err.Error()))
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
		return b.showErrorsNotice(ctx, chatID, msgID, userID, "✅ 没有未解决的"+label+"错误。")
	}
	return b.showErrorsNotice(ctx, chatID, msgID, userID,
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
	return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
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
	return b.editOrSend(ctx, chatID, msgID, bld.String(), opsKeyboard())
}

func (b *Bot) showBadAccounts(ctx context.Context, chatID, msgID, userID int64) error {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	// list error accounts; also try rate-limited if supported by filter
	items, total, err := cli.ListAccounts(ctx, 1, 30, "error")
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "账号列表失败: "+telegram.EscapeHTML(err.Error()), opsKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold("异常账号 (status=error)") + "\n")
	fmt.Fprintf(&bld, "共 %s 个\n\n", telegram.Code(itoa(total)))
	if len(items) == 0 {
		bld.WriteString("当前无 error 状态账号。")
	}
	for i, a := range items {
		if i >= 15 {
			fmt.Fprintf(&bld, "… 另有 %d 个\n", len(items)-15)
			break
		}
		msg := a.ErrorMessage
		if msg == "" {
			msg = a.Status
		}
		fmt.Fprintf(&bld, "• #%d %s [%s]\n  %s\n",
			a.ID,
			telegram.EscapeHTML(truncateRunes(a.Name, 18)),
			telegram.EscapeHTML(a.Platform),
			telegram.EscapeHTML(truncateRunes(msg, 80)),
		)
	}
	rows := [][]telegram.InlineKeyboardButton{}
	// direct manage buttons for first few error accounts
	for i, a := range items {
		if i >= 8 {
			break
		}
		label := fmt.Sprintf("管理 #%d %s", a.ID, truncateRunes(a.Name, 10))
		rows = append(rows, []telegram.InlineKeyboardButton{
			telegram.Btn(label, fmt.Sprintf("mgr_acc:%d", a.ID)),
		})
	}
	rows = append(rows,
		[]telegram.InlineKeyboardButton{
			telegram.Btn("🧹 批量清错", "mgr_bulk_clear"),
			telegram.Btn("♻️ 批量恢复", "mgr_bulk_recover"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("🛠 一键修复", "mgr_bulk_heal"),
			telegram.Btn("▶️ 批量开调度", "mgr_bulk_sched_on"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("➕ 一键监控", "ops_watch_errors"),
		},
		[]telegram.InlineKeyboardButton{
			telegram.Btn("« 运维菜单", "ops_menu"),
			telegram.Btn("« 主面板", "home"),
		},
	)
	kb := &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
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

func (b *Bot) showAccountLive(ctx context.Context, chatID, msgID, userID, accountID int64) error {
	cli, p, err := b.userClient(userID, 20*time.Second)
	if err != nil {
		return b.editOrSend(ctx, chatID, msgID, "❌ "+telegram.EscapeHTML(err.Error()), connKeyboard())
	}
	var bld strings.Builder
	bld.WriteString(telegram.Bold(fmt.Sprintf("账号 #%d 实时", accountID)) + "\n\n")

	name := ""
	if acc, err := cli.GetAccount(ctx, accountID); err == nil && acc != nil {
		name = acc.Name
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

	// availability map lookup
	if av, err := cli.GetAccountAvailability(ctx); err == nil && av != nil {
		if st, ok := av.Account[strconv.FormatInt(accountID, 10)]; ok {
			fmt.Fprintf(&bld, "\n运行态: available=%v error=%v rl=%v ol=%v\n",
				st.IsAvailable, st.HasError, st.IsRateLimited, st.IsOverloaded)
		}
	}

	_ = name
	kb := &telegram.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegram.InlineKeyboardButton{
			{telegram.Btn("🔄 刷新", fmt.Sprintf("acc_live:%d", accountID)), telegram.Btn("🧰 管理", fmt.Sprintf("mgr_acc:%d", accountID))},
			{telegram.Btn("« 账号详情", fmt.Sprintf("acc:%d", accountID)), telegram.Btn("« 列表", "cfg_acc")},
		},
	}
	return b.editOrSend(ctx, chatID, msgID, bld.String(), kb)
}
