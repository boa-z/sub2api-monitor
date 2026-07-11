package discordpanel

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/discord"
	"github.com/boa/sub2api-monitor/internal/panel/browse"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/userstore"
)

func helpText() string {
	return `**Sub2API Discord 面板**

• **普通用户**：连接 / 监控账号 / 阈值 / 立即检查
• **管理员**：运维视图 + 账号管理（调度/清错/恢复/批量/错误分页/一键修复/临时停调度）
• 管理员由 admin_user_ids 或 profile.role=admin 控制
• 配置按用户隔离，存于 users.json（可与 Telegram 共享）
• 斜杠命令：` + "`/panel` `/status` `/check` `/setbase` `/setkey` `/addaccount` `/ops` `/manage`"
}

func (b *Bot) homeText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString("**Sub2API 监控面板 (Discord)**\n")
	fmt.Fprintf(&bld, "实例: `%s` · 角色: `%s`\n", b.cfg.Instance, b.roleLabel(userID))
	fmt.Fprintf(&bld, "检查间隔: `%s` · 冷却: `%s`\n\n",
		b.panelCfg().CheckInterval.String(), b.panelCfg().Cooldown.String())
	if p == nil {
		bld.WriteString("尚未创建配置，点下方按钮开始。")
		return bld.String()
	}
	mon := "关闭"
	if p.Enabled {
		mon = "开启"
	}
	fmt.Fprintf(&bld, "监控: `%s` · 数据源: `%s`\n", mon, p.EffectiveSource())
	base := p.BaseURL
	if base == "" {
		base = "(未设置)"
	}
	fmt.Fprintf(&bld, "Base URL: `%s`\n", base)
	fmt.Fprintf(&bld, "API Key: `%s`\n", userstore.MaskKey(p.AdminAPIKey))
	enabledN := 0
	for _, a := range p.Accounts {
		if a.IsEnabled() {
			enabledN++
		}
	}
	fmt.Fprintf(&bld, "监控账号: `%d` 个（启用 `%d`）\n", len(p.Accounts), enabledN)
	return bld.String()
}

func (b *Bot) homeComponents(userID int64) []discord.Component {
	if b.isAdmin(userID) {
		return []discord.Component{
			discord.ActionRow(
				discord.PrimaryButton("状态", "status"),
				discord.Button("运维", "ops_menu", 2),
				discord.Button("管理", "mgr_menu", 2),
			),
			discord.ActionRow(
				discord.Button("监控账号", "cfg_acc", 2),
				discord.Button("连接", "cfg_conn", 2),
				discord.Button("阈值", "cfg_thr", 2),
			),
			discord.ActionRow(
				discord.SuccessButton("立即检查", "check_now"),
				discord.Button("开关监控", "toggle_mon", 2),
				discord.Button("数据源", "toggle_src", 2),
				discord.Button("帮助", "help", 2),
			),
		}
	}
	return []discord.Component{
		discord.ActionRow(
			discord.PrimaryButton("状态", "status"),
			discord.Button("监控账号", "cfg_acc", 2),
			discord.Button("连接", "cfg_conn", 2),
		),
		discord.ActionRow(
			discord.Button("阈值", "cfg_thr", 2),
			discord.SuccessButton("立即检查", "check_now"),
			discord.Button("开关监控", "toggle_mon", 2),
		),
		discord.ActionRow(
			discord.Button("数据源", "toggle_src", 2),
			discord.Button("帮助", "help", 2),
		),
	}
}

func (b *Bot) connText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString("**连接配置**\n\n")
	if p == nil {
		bld.WriteString("未创建。")
		return bld.String()
	}
	base := p.BaseURL
	if base == "" {
		base = "(未设置)"
	}
	fmt.Fprintf(&bld, "Base URL: `%s`\nAPI Key: `%s`\n", base, userstore.MaskKey(p.AdminAPIKey))
	bld.WriteString("\n用 `/setbase` `/setkey` 设置，或点下方按钮查看说明。")
	return bld.String()
}

func (b *Bot) connComponents(userID int64) []discord.Component {
	rows := []discord.Component{
		discord.ActionRow(
			discord.Button("设置 Base", "set_base_prompt", 2),
			discord.Button("设置 Key", "set_key_prompt", 2),
			discord.Button("测试连接", "test_conn", 1),
		),
		discord.ActionRow(
			discord.DangerButton("清除连接", "clear_conn"),
		),
	}
	if b.isAdmin(userID) {
		rows = append(rows, discord.ActionRow(discord.Button("导入全局配置", "seed_conn", 3)))
	}
	rows = append(rows, discord.ActionRow(discord.Button("« 主面板", "home", 2)))
	return rows
}

func (b *Bot) accountsText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString("**监控账号**\n\n")
	if p == nil || len(p.Accounts) == 0 {
		bld.WriteString("暂无账号。使用 `/addaccount id:123` 添加。")
		return bld.String()
	}
	for _, a := range p.Accounts {
		en := "启用"
		if !a.IsEnabled() {
			en = "暂停"
		}
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("#%d", a.ID)
		}
		fmt.Fprintf(&bld, "• `#%d` %s · `%s`\n", a.ID, name, en)
	}
	return bld.String()
}

func (b *Bot) accountsComponents(userID int64) []discord.Component {
	rows := []discord.Component{
		discord.ActionRow(discord.Button("添加账号", "add_acc_prompt", 1)),
	}
	if p, ok := b.users.Get(userID); ok {
		n := 0
		for _, a := range p.Accounts {
			if n >= 4 {
				break
			}
			label := fmt.Sprintf("删#%d", a.ID)
			tog := fmt.Sprintf("切#%d", a.ID)
			rows = append(rows, discord.ActionRow(
				discord.DangerButton(label, fmt.Sprintf("del_acc:%d", a.ID)),
				discord.Button(tog, fmt.Sprintf("tog_acc:%d", a.ID), 2),
			))
			n++
		}
	}
	rows = append(rows, discord.ActionRow(discord.Button("« 主面板", "home", 2)))
	return rows
}

func (b *Bot) thresholdsText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString("**用量阈值**\n\n")
	var ths []config.UsageThreshold
	src := "系统默认"
	if p != nil && len(p.Thresholds) > 0 {
		ths = p.Thresholds
		src = "自定义"
	} else {
		ths = b.defaults
	}
	fmt.Fprintf(&bld, "当前: **%s**\n", src)
	for _, t := range ths {
		sev := t.Severity
		if sev == "" {
			sev = "P2"
		}
		fmt.Fprintf(&bld, "• `%s` ≥ `%.0f%%` · `%s`\n", t.Window, t.UtilizationGTE, sev)
	}
	return bld.String()
}

func thrComponents() []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("添加/改阈值", "thr_add", 1),
			discord.Button("重置默认", "thr_reset", 2),
		),
		discord.ActionRow(
			discord.DangerButton("删 5h", "thr_del:five_hour"),
			discord.DangerButton("删 7d", "thr_del:seven_day"),
		),
		discord.ActionRow(discord.Button("« 主面板", "home", 2)),
	}
}

func thrWindowComponents() []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("5h≥80%", "thr_set:five_hour:80", 2),
			discord.Button("5h≥90%", "thr_set:five_hour:90", 2),
		),
		discord.ActionRow(
			discord.Button("7d≥80%", "thr_set:seven_day:80", 2),
			discord.Button("7d≥90%", "thr_set:seven_day:90", 2),
		),
		discord.ActionRow(discord.Button("« 阈值", "cfg_thr", 2)),
	}
}

func opsMenuText() string {
	return "**运维视图**\n\n基于当前连接的 Admin API：\n• 看板 / 可用性 / 告警 / 并发 / 渠道\n• 错误（分标签分页，可标记已解决）\n• 异常账号（跳转管理 / 批量处理）"
}

func opsComponents() []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("看板", "ops_dash", 1),
			discord.Button("可用性", "ops_avail", 2),
			discord.Button("告警", "ops_alerts", 2),
		),
		discord.ActionRow(
			discord.Button("错误", "ops_errors:all:0", 2),
			discord.Button("并发", "ops_conc", 2),
			discord.Button("渠道", "ops_channels", 2),
		),
		discord.ActionRow(
			discord.Button("异常账号", "ops_badacc", 2),
			discord.Button("账号管理", "mgr_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
}

func opsViewComponents(refresh string) []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", refresh, 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
}

func manageMenuText() string {
	return "**账号管理**\n\n浏览（状态/平台/停调度/限速）、切换调度、清错/恢复/一键修复、临时停调度、批量处理（Admin API，需确认）。"
}

func manageComponents() []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("浏览全部", "mgr_browse:all:0", 1),
			discord.Button("error", "mgr_browse:error:0", 2),
			discord.Button("active", "mgr_browse:active:0", 2),
		),
		discord.ActionRow(
			discord.Button("停调度", "mgr_browse:unsched:0", 2),
			discord.Button("限速", "mgr_browse:rate_limited:0", 2),
			discord.Button("异常账号", "ops_badacc", 2),
		),
		discord.ActionRow(
			discord.DangerButton("批量清错", "mgr_bulk_clear"),
			discord.Button("批量恢复", "mgr_bulk_recover", 2),
			discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
		),
		discord.ActionRow(
			discord.Button("批量清限速", "mgr_bulk_clear_rl", 2),
			discord.Button("一键修复", "mgr_bulk_heal", 1),
			discord.Button("« 主面板", "home", 2),
		),
	}
}

func confirmComponents(action string, accountID int64) []discord.Component {
	switch action {
	case "confirm_unsched":
		return []discord.Component{
			discord.ActionRow(
				discord.DangerButton("确认停调度", fmt.Sprintf("mgr_act:unsched:%d", accountID)),
				discord.Button("取消", fmt.Sprintf("mgr_acc:%d", accountID), 2),
			),
		}
	case "confirm_disable":
		return []discord.Component{
			discord.ActionRow(
				discord.DangerButton("确认禁用", fmt.Sprintf("mgr_act:disable:%d", accountID)),
				discord.Button("取消", fmt.Sprintf("mgr_acc:%d", accountID), 2),
			),
		}
	case "confirm_reset_quota":
		return []discord.Component{
			discord.ActionRow(
				discord.DangerButton("确认重置额度", fmt.Sprintf("mgr_act:reset_quota:%d", accountID)),
				discord.Button("取消", fmt.Sprintf("mgr_acc:%d", accountID), 2),
			),
		}
	default:
		return manageComponents()
	}
}

func (b *Bot) setBaseURL(userID int64, raw string) string {
	u := strings.TrimSpace(raw)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "URL 需以 http:// 或 https:// 开头"
	}
	u = strings.TrimRight(u, "/")
	if _, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.BaseURL = u
		return nil
	}); err != nil {
		return "保存失败: " + err.Error()
	}
	return "✅ Base URL 已保存: `" + u + "`"
}

func (b *Bot) setAPIKey(userID int64, raw string) string {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "密钥不能为空"
	}
	if _, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.AdminAPIKey = key
		return nil
	}); err != nil {
		return "保存失败: " + err.Error()
	}
	return "✅ API Key 已保存: `" + userstore.MaskKey(key) + "`"
}

func (b *Bot) addAccount(ctx context.Context, userID int64, raw string) string {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return "账号 ID 无效"
	}
	name := ""
	if cli, _, err := b.userClient(userID, 10*time.Second); err == nil {
		if acc, err := cli.GetAccount(ctx, id); err == nil && acc != nil {
			name = acc.Name
		}
	}
	en := true
	if _, err := b.users.Update(userID, func(p *userstore.Profile) error {
		for _, a := range p.Accounts {
			if a.ID == id {
				return fmt.Errorf("已在监控列表")
			}
		}
		p.Accounts = append(p.Accounts, userstore.AccountWatch{ID: id, Name: name, Enabled: &en})
		return nil
	}); err != nil {
		return "添加失败: " + err.Error()
	}
	label := name
	if label == "" {
		label = fmt.Sprintf("#%d", id)
	}
	return "✅ 已添加 " + label
}

func (b *Bot) delAccount(userID int64, raw string) string {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return "ID 无效"
	}
	if _, err := b.users.Update(userID, func(p *userstore.Profile) error {
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
			return fmt.Errorf("未找到")
		}
		p.Accounts = out
		return nil
	}); err != nil {
		return "删除失败: " + err.Error()
	}
	return fmt.Sprintf("✅ 已移除 #%d", id)
}

func (b *Bot) setThreshold(userID int64, window string, pct float64, severity string) error {
	window = normalizeWindow(window)
	if pct <= 0 || pct > 100 {
		return fmt.Errorf("invalid pct")
	}
	if severity == "" {
		severity = "P2"
	}
	_, err := b.users.Update(userID, func(p *userstore.Profile) error {
		ths := p.Thresholds
		if len(ths) == 0 {
			ths = append([]config.UsageThreshold(nil), b.defaults...)
		}
		found := false
		for i := range ths {
			if ths[i].Window == window {
				ths[i].UtilizationGTE = pct
				ths[i].Severity = severity
				found = true
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
		if len(p.Thresholds) == 0 {
			p.Thresholds = append([]config.UsageThreshold(nil), b.defaults...)
		}
		out := p.Thresholds[:0]
		for _, t := range p.Thresholds {
			if t.Window == window {
				continue
			}
			out = append(out, t)
		}
		p.Thresholds = out
		return nil
	})
	return err
}

func normalizeWindow(w string) string {
	w = strings.TrimSpace(strings.ToLower(w))
	switch w {
	case "5h", "5_hour", "5hour", "five-hour":
		return "five_hour"
	case "7d", "7_day", "7day", "seven-day":
		return "seven_day"
	default:
		return w
	}
}

func (b *Bot) testConnection(ctx context.Context, userID int64) string {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	if err := cli.Health(ctx); err != nil {
		return "❌ /health 失败: " + err.Error()
	}
	if _, err := cli.GetDashboardStats(ctx); err != nil {
		return "⚠️ health 正常，但 Admin API 失败: " + err.Error()
	}
	return "✅ 连接正常（health + dashboard）"
}

func (b *Bot) seedConnection(userID int64) string {
	base := strings.TrimSpace(b.cfg.Sub2API.BaseURL)
	key := strings.TrimSpace(b.cfg.Sub2API.AdminAPIKey)
	jwt := strings.TrimSpace(b.cfg.Sub2API.JWT)
	if base == "" || (key == "" && jwt == "") {
		return "❌ 全局 sub2api 未配置完整"
	}
	if _, err := b.users.Update(userID, func(p *userstore.Profile) error {
		p.BaseURL = strings.TrimRight(base, "/")
		p.AdminAPIKey = key
		p.JWT = jwt
		return nil
	}); err != nil {
		return "写入失败: " + err.Error()
	}
	return "✅ 已导入全局连接\n\n" + b.connText(userID) + "\n\n⚠️ 共享 Admin Key 请仅给可信管理员。"
}

func (b *Bot) forceCheck(ctx context.Context, userID int64) string {
	cli, p, err := b.userClient(userID, 25*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	if p == nil || len(p.Accounts) == 0 {
		return "请先添加监控账号"
	}
	src := p.EffectiveSource()
	var bld strings.Builder
	bld.WriteString("**立即检查** · `" + src + "`\n\n")
	for _, a := range p.Accounts {
		if !a.IsEnabled() {
			fmt.Fprintf(&bld, "• #%d 已暂停\n", a.ID)
			continue
		}
		usage, err := cli.GetAccountUsage(ctx, a.ID, src, false)
		if err != nil {
			fmt.Fprintf(&bld, "• #%d 失败: %s\n", a.ID, truncate(err.Error(), 60))
			continue
		}
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("#%d", a.ID)
		}
		fmt.Fprintf(&bld, "**#%d %s**\n", a.ID, name)
		for _, w := range usage.Windows() {
			fmt.Fprintf(&bld, "  `%s` %.1f%%", w.Window, w.Utilization)
			if w.ResetsAt != nil {
				fmt.Fprintf(&bld, " · 重置 %s", w.ResetsAt.Local().Format("01-02 15:04"))
			}
			bld.WriteString("\n")
		}
		if today, err := cli.GetAccountTodayStats(ctx, a.ID); err == nil && today != nil {
			fmt.Fprintf(&bld, "  today: req=%d token=%d cost=%.2f\n", today.Requests, today.Tokens, today.Cost)
		}
	}
	return bld.String()
}

func (b *Bot) showDashboard(ctx context.Context, userID int64) string {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	st, err := cli.GetDashboardStats(ctx)
	if err != nil {
		return "看板失败: " + err.Error()
	}
	var bld strings.Builder
	bld.WriteString("**实例看板**\n\n")
	fmt.Fprintf(&bld, "账号: 总 `%v` · 正常 `%v` · 异常 `%v` · 限速 `%v` · 过载 `%v`\n",
		st.TotalAccounts, st.NormalAccounts, st.ErrorAccounts, st.RatelimitAccounts, st.OverloadAccounts)
	fmt.Fprintf(&bld, "用户: 总 `%v` · 活跃 `%v` · 今日新增 `%v`\n",
		st.TotalUsers, st.ActiveUsers, st.TodayNewUsers)
	fmt.Fprintf(&bld, "今日: 请求 `%v` · Token `%v` · 费用 `%.2f`\n",
		st.TodayRequests, st.TodayTokens, st.TodayCost)
	fmt.Fprintf(&bld, "累计: 请求 `%v` · Token `%v` · 费用 `%.2f`\n",
		st.TotalRequests, st.TotalTokens, st.TotalCost)
	if st.RPM > 0 || st.TPM > 0 {
		fmt.Fprintf(&bld, "RPM/TPM: `%.2f` / `%.0f`\n", st.RPM, st.TPM)
	}
	if rt, err := cli.GetRealtimeDashboard(ctx); err == nil && rt != nil {
		fmt.Fprintf(&bld, "实时: 活跃 `%v` · RPM `%.2f` · 错误率 `%.2f%%`\n",
			rt.ActiveRequests, rt.RequestsPerMinute, rt.ErrorRate)
	}
	return bld.String()
}

func (b *Bot) showAvailability(ctx context.Context, userID int64) string {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	av, err := cli.GetAccountAvailability(ctx)
	if err != nil {
		return "可用性失败: " + err.Error()
	}
	var bld strings.Builder
	bld.WriteString("**账号可用性**\n\n")
	if av == nil {
		return bld.String() + "无数据。"
	}
	// Platform map
	type kv struct {
		k string
		v sub2api.AvailabilityBucket
	}
	var plats []kv
	for k, v := range av.Platform {
		plats = append(plats, kv{k, v})
	}
	if len(plats) == 0 && len(av.Group) > 0 {
		for k, v := range av.Group {
			plats = append(plats, kv{"g:" + k, v})
		}
	}
	if len(plats) == 0 {
		// fallback dump
		return "**可用性**\n```\n" + truncate(fmt.Sprintf("%+v", av), 900) + "\n```"
	}
	// simple sort by key
	for i := 0; i < len(plats); i++ {
		for j := i + 1; j < len(plats); j++ {
			if plats[j].k < plats[i].k {
				plats[i], plats[j] = plats[j], plats[i]
			}
		}
	}
	for i, p := range plats {
		if i >= 12 {
			break
		}
		tot := p.v.TotalNum()
		avn := p.v.AvailableNum()
		rate := 0.0
		if tot > 0 {
			rate = float64(avn) / float64(tot) * 100
		}
		fmt.Fprintf(&bld, "• `%s` 可用 %d/%d (%.0f%%) · err %d · rl %d\n",
			p.k, avn, tot, rate, p.v.ErrorNum(), p.v.RateLimitNum())
	}
	return bld.String()
}

func (b *Bot) showAlerts(ctx context.Context, userID int64) string {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	ev, err := cli.ListAlertEvents(ctx, 1, 20)
	if err != nil {
		return "告警失败: " + err.Error()
	}
	var bld strings.Builder
	bld.WriteString("**内置告警**\n\n")
	if len(ev) == 0 {
		bld.WriteString("无事件。")
		return bld.String()
	}
	for i, e := range ev {
		if i >= 10 {
			break
		}
		title := e.DisplayTitle()
		if title == "" {
			title = e.Status
		}
		fmt.Fprintf(&bld, "• [%s] %s — %s\n", strings.ToUpper(e.Severity), truncate(title, 40), e.Status)
		if msg := e.DisplayMessage(); msg != "" {
			fmt.Fprintf(&bld, "  %s\n", truncate(msg, 80))
		}
	}
	return bld.String()
}

func (b *Bot) showConcurrency(ctx context.Context, userID int64) string {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	snap, err := cli.GetConcurrency(ctx)
	if err != nil {
		return "并发失败: " + err.Error()
	}
	var bld strings.Builder
	bld.WriteString("**并发负载**\n\n")
	type crow struct {
		name string
		b    sub2api.ConcurrencyBucket
	}
	var plats []crow
	for k, v := range snap.Platform {
		plats = append(plats, crow{k, v})
	}
	// simple order by load desc without sort import if missing - check imports
	// use sort
	sort.Slice(plats, func(i, j int) bool { return plats[i].b.LoadPercentage > plats[j].b.LoadPercentage })
	bld.WriteString("**平台**\n")
	for _, r := range plats {
		fmt.Fprintf(&bld, "• %s: `%d/%d` (%.0f%%) wait=`%d`\n",
			r.name, r.b.CurrentInUse, r.b.MaxCapacity, r.b.LoadPercentage, r.b.WaitingInQueue)
	}
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
	bld.WriteString("\n**有负载账号**\n")
	if len(accs) == 0 {
		bld.WriteString("当前无占用。\n")
	}
	for i, r := range accs {
		if i >= 10 {
			fmt.Fprintf(&bld, "… 另有 %d 个\n", len(accs)-10)
			break
		}
		fmt.Fprintf(&bld, "• #%d %s: `%d/%d` (%.0f%%)\n",
			r.b.AccountID, truncate(r.name, 14), r.b.CurrentInUse, r.b.MaxCapacity, r.b.LoadPercentage)
	}
	return bld.String()
}

func (b *Bot) showChannels(ctx context.Context, userID int64) string {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	items, err := cli.ListChannelMonitors(ctx)
	if err != nil {
		return "渠道探测失败: " + err.Error()
	}
	var bld strings.Builder
	bld.WriteString("**渠道探测**\n\n")
	if len(items) == 0 {
		bld.WriteString("无探测任务。")
		return bld.String()
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
		fmt.Fprintf(&bld, "• [%s] #%d %s\n  %s / %s · %s · `%dms`\n  上次 %s\n",
			en, m.ID, truncate(m.Name, 18), m.Provider, truncate(m.PrimaryModel, 16),
			m.PrimaryStatus, m.PrimaryLatencyMS, last)
	}
	return bld.String()
}

func (b *Bot) showErrors(ctx context.Context, userID int64) (string, []discord.Component) {
	return b.showErrorsView(ctx, userID, "all", 0, "")
}

func (b *Bot) showErrorsView(ctx context.Context, userID int64, kind string, page int, notice string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	if page < 0 {
		page = 0
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "u" && kind != "r" {
		kind = "all"
	}

	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	bld.WriteString("**近期错误**（优先未解决）\n")
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(errorTabLabel("全部", kind, "all"), "ops_errors:all:0", 2),
			discord.Button(errorTabLabel("上游", kind, "u"), "ops_errors:u:0", 2),
			discord.Button(errorTabLabel("请求", kind, "r"), "ops_errors:r:0", 2),
		),
	}

	var resolveIDs []struct {
		kind string
		id   int64
	}
	writePage := func(label, k string, pageData *sub2api.OpsErrorPage, pullErr error, maxShow int) {
		fmt.Fprintf(&bld, "\n**%s**\n", label)
		if pullErr != nil {
			fmt.Fprintf(&bld, "拉取失败: %s\n", pullErr.Error())
			return
		}
		if pageData == nil || len(pageData.Items) == 0 {
			bld.WriteString("无\n")
			return
		}
		shown := 0
		for _, e := range pageData.Items {
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
			when := ""
			if !e.CreatedAt.IsZero() {
				when = " · " + e.CreatedAt.Local().Format("01-02 15:04")
			}
			fmt.Fprintf(&bld, "• #%d [%s] %d %s%s\n  %s\n",
				e.ID, e.Severity, e.StatusCode, truncate(name, 14), when,
				truncate(e.Message, 70))
			resolveIDs = append(resolveIDs, struct {
				kind string
				id   int64
			}{k, e.ID})
			shown++
		}
		if shown == 0 {
			bld.WriteString("无未解决项。\n")
		}
		if pageData.Total > 0 {
			fmt.Fprintf(&bld, "列表共约 %d 条\n", pageData.Total)
		}
	}

	switch kind {
	case "u":
		pageData, err1 := cli.ListUpstreamErrors(ctx, page+1, 10)
		fmt.Fprintf(&bld, "标签: `上游` · 第 %d 页\n", page+1)
		writePage("上游错误", "u", pageData, err1, 8)
		comps = append(comps, errorPageNav("u", page, pageData)...)
	case "r":
		pageData, err2 := cli.ListRequestErrors(ctx, page+1, 10)
		fmt.Fprintf(&bld, "标签: `请求` · 第 %d 页\n", page+1)
		writePage("请求错误", "r", pageData, err2, 8)
		comps = append(comps, errorPageNav("r", page, pageData)...)
	default:
		up, err1 := cli.ListUpstreamErrors(ctx, 1, 15)
		req, err2 := cli.ListRequestErrors(ctx, 1, 10)
		if err1 != nil && err2 != nil {
			return "错误列表失败: " + err1.Error(), opsComponents()
		}
		writePage("上游错误", "u", up, err1, 4)
		writePage("请求错误", "r", req, err2, 3)
	}

	var row []discord.Component
	for i, r := range resolveIDs {
		if i >= 4 {
			break
		}
		row = append(row, discord.Button(fmt.Sprintf("解决 #%d", r.id), fmt.Sprintf("oe:r:%s:%d", r.kind, r.id), 3))
		if len(row) == 2 {
			comps = append(comps, discord.ActionRow(row...))
			row = nil
		}
	}
	if len(row) > 0 {
		comps = append(comps, discord.ActionRow(row...))
	}
	comps = append(comps,
		discord.ActionRow(
			discord.SuccessButton("全部解决上游", "oe:resolve_all:u"),
			discord.SuccessButton("全部解决请求", "oe:resolve_all:r"),
		),
		discord.ActionRow(
			discord.Button("刷新", fmt.Sprintf("ops_errors:%s:%d", kind, page), 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	)
	return bld.String(), comps
}

func errorTabLabel(label, cur, val string) string {
	if cur == val {
		return "• " + label
	}
	return label
}

func errorPageNav(kind string, page int, pageData *sub2api.OpsErrorPage) []discord.Component {
	if pageData == nil {
		return nil
	}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("ops_errors:%s:%d", kind, page-1), 2))
	}
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
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("ops_errors:%s:%d", kind, page+1), 2))
	}
	if len(nav) == 0 {
		return nil
	}
	return []discord.Component{discord.ActionRow(nav...)}
}

func (b *Bot) resolveOpsError(ctx context.Context, userID int64, kind string, errorID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	apiKind := "upstream"
	tab := kind
	if kind == "r" {
		apiKind = "request"
	}
	if tab != "u" && tab != "r" {
		tab = "all"
	}
	if err := cli.ResolveOpsError(ctx, apiKind, errorID); err != nil {
		return b.showErrorsView(ctx, userID, tab, 0, "❌ 标记失败: "+err.Error())
	}
	return b.showErrorsView(ctx, userID, tab, 0, fmt.Sprintf("✅ 已标记错误 #%d 为已解决", errorID))
}

func (b *Bot) resolveAllOpsErrors(ctx context.Context, userID int64, apiKind, label string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 30*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	tab := "u"
	var page *sub2api.OpsErrorPage
	switch apiKind {
	case "request":
		page, err = cli.ListRequestErrors(ctx, 1, 20)
		tab = "r"
	default:
		page, err = cli.ListUpstreamErrors(ctx, 1, 20)
		apiKind = "upstream"
		tab = "u"
	}
	if err != nil {
		return b.showErrorsView(ctx, userID, tab, 0, "❌ 拉取失败: "+err.Error())
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
		return b.showErrorsView(ctx, userID, tab, 0, "✅ 没有未解决的"+label+"错误。")
	}
	return b.showErrorsView(ctx, userID, tab, 0,
		fmt.Sprintf("✅ 批量标记%s错误：成功 %d · 失败 %d", label, okN, failN))
}

func (b *Bot) showBadAccounts(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	items, total, err := cli.ListAccountsEx(ctx, 1, 15, sub2api.AccountListFilter{Status: "error"})
	if err != nil {
		return "列表失败: " + err.Error(), opsComponents()
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "**异常账号** · 约 %d\n\n", total)
	for _, a := range items {
		fmt.Fprintf(&bld, "• #%d %s [%s] %s\n", a.ID, truncate(a.Name, 16), a.Status, truncate(a.ErrorMessage, 40))
	}
	if len(items) == 0 {
		bld.WriteString("无 error 账号。")
	}
	comps := []discord.Component{}
	var row []discord.Component
	for i, a := range items {
		if i >= 4 {
			break
		}
		row = append(row, discord.Button(fmt.Sprintf("管理 #%d", a.ID), fmt.Sprintf("mgr_acc:%d", a.ID), 1))
		if len(row) == 2 {
			comps = append(comps, discord.ActionRow(row...))
			row = nil
		}
	}
	if len(row) > 0 {
		comps = append(comps, discord.ActionRow(row...))
	}
	comps = append(comps,
		discord.ActionRow(
			discord.DangerButton("批量清错", "mgr_bulk_clear"),
			discord.Button("批量恢复", "mgr_bulk_recover", 2),
			discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
		),
		discord.ActionRow(
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 管理", "mgr_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	)
	return bld.String(), comps
}

func (b *Bot) accountBrowser(ctx context.Context, userID int64, status string, page int) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	if page < 0 {
		page = 0
	}
	const pageSize = 8
	if status == "" {
		status = "all"
	}
	items, total, err := browse.ListAccounts(ctx, cli, status, page, pageSize)
	if err != nil {
		return "列表失败: " + err.Error(), manageComponents()
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "**账号浏览** · `%s` · 第 %d 页 · 约 %d\n点账号进入管理\n\n", browse.Title(status), page+1, total)
	if len(items) == 0 {
		bld.WriteString("本页无账号。")
	}
	for _, a := range items {
		fmt.Fprintf(&bld, "• #%d %s [%s/%s] sched=%v\n", a.ID, truncate(a.Name, 16), a.Platform, a.Status, a.Schedulable)
	}

	token := browse.Token(status)
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(filterBtn("全部", status, "all"), "mgr_browse:all:0", 2),
			discord.Button(filterBtn("active", status, "active"), "mgr_browse:active:0", 2),
			discord.Button(filterBtn("error", status, "error"), "mgr_browse:error:0", 2),
		),
		discord.ActionRow(
			discord.Button(filterBtn("停调度", status, "unsched"), "mgr_browse:unsched:0", 2),
			discord.Button(filterBtn("限速", status, "rate_limited"), "mgr_browse:rate_limited:0", 2),
			discord.Button(filterBtn("openai", status, "plat:openai"), "mgr_browse:"+browse.Token("plat:openai")+":0", 2),
		),
		discord.ActionRow(
			discord.Button(filterBtn("anthropic", status, "plat:anthropic"), "mgr_browse:"+browse.Token("plat:anthropic")+":0", 2),
			discord.Button(filterBtn("gemini", status, "plat:gemini"), "mgr_browse:"+browse.Token("plat:gemini")+":0", 2),
			discord.Button(filterBtn("grok", status, "plat:grok"), "mgr_browse:"+browse.Token("plat:grok")+":0", 2),
		),
	}
	// account select (up to 8) keeps button count low
	if len(items) > 0 {
		opts := make([]discord.SelectOpt, 0, len(items))
		for _, a := range items {
			if len(opts) >= 8 {
				break
			}
			opts = append(opts, discord.SelectOption(
				fmt.Sprintf("#%d %s", a.ID, truncate(a.Name, 18)),
				fmt.Sprintf("mgr_acc:%d", a.ID),
				fmt.Sprintf("%s/%s", a.Platform, a.Status),
			))
		}
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:mgr_acc", "选择账号管理…", opts...)))
	}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("mgr_browse:%s:%d", token, page-1), 2))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("mgr_browse:%s:%d", token, page+1), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
	}
	switch status {
	case "error":
		comps = append(comps, discord.ActionRow(
			discord.DangerButton("批量清错", "mgr_bulk_clear"),
			discord.Button("一键修复", "mgr_bulk_heal", 1),
			discord.Button("批量恢复", "mgr_bulk_recover", 2),
		))
	case "rate_limited":
		comps = append(comps, discord.ActionRow(discord.Button("批量清限速", "mgr_bulk_clear_rl", 2)))
	case "unsched":
		comps = append(comps, discord.ActionRow(discord.Button("批量开调度", "mgr_bulk_sched_on", 2)))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("« 管理菜单", "mgr_menu", 2),
		discord.Button("« 主面板", "home", 2),
	))
	return bld.String(), comps
}

func filterBtn(label, cur, val string) string {
	curN := cur
	if curN == "" {
		curN = "all"
	}
	if strings.HasPrefix(curN, "plat:") && strings.HasPrefix(val, "plat:") {
		// compare platform only
		if strings.Split(strings.TrimPrefix(curN, "plat:"), ":")[0] == strings.TrimPrefix(val, "plat:") {
			return "• " + label
		}
		return label
	}
	if curN == val {
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

func (b *Bot) manageAccount(ctx context.Context, userID, accountID int64, notice string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	acc, err := cli.GetAccount(ctx, accountID)
	if err != nil {
		return "读取失败: " + err.Error(), manageComponents()
	}
	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	fmt.Fprintf(&bld, "**管理账号 #%d**\n\n", accountID)
	fmt.Fprintf(&bld, "名称: `%s`\n平台/类型: `%s` / `%s`\n状态: `%s`\n可调度: `%v`\n",
		acc.Name, acc.Platform, acc.Type, acc.Status, acc.Schedulable)
	if acc.ErrorMessage != "" {
		fmt.Fprintf(&bld, "错误: %s\n", truncate(acc.ErrorMessage, 120))
	}
	if acc.RateLimitedAt != nil {
		fmt.Fprintf(&bld, "限速于: `%s`\n", acc.RateLimitedAt.Local().Format("01-02 15:04"))
	}
	if acc.RateLimitResetAt != nil {
		fmt.Fprintf(&bld, "限速重置: `%s`\n", acc.RateLimitResetAt.Local().Format("01-02 15:04"))
	}
	if acc.OverloadUntil != nil {
		fmt.Fprintf(&bld, "过载至: `%s`\n", acc.OverloadUntil.Local().Format("01-02 15:04"))
	}
	if acc.TempUnschedulableUntil != nil {
		fmt.Fprintf(&bld, "临时停调度至: `%s`\n", acc.TempUnschedulableUntil.Local().Format("01-02 15:04"))
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
	fmt.Fprintf(&bld, "面板监控: `%s`\n", map[bool]string{true: "已添加", false: "未添加"}[watched])

	if usage, err := cli.GetAccountUsage(ctx, accountID, "passive", false); err == nil && usage != nil {
		bld.WriteString("\n**用量快照**\n")
		for _, w := range usage.Windows() {
			line := fmt.Sprintf("• `%s` `%.1f%%`", w.Window, w.Utilization)
			if w.ResetsAt != nil {
				line += " · 重置 `" + w.ResetsAt.Local().Format("01-02 15:04") + "`"
			}
			bld.WriteString(line + "\n")
		}
		if today, err := cli.GetAccountTodayStats(ctx, accountID); err == nil && today != nil {
			fmt.Fprintf(&bld, "今日: req `%d` · token `%d` · cost `%.2f`\n", today.Requests, today.Tokens, today.Cost)
		}
	}

	schedBtn := "停调度"
	schedData := fmt.Sprintf("mgr_act:confirm_unsched:%d", accountID)
	if !acc.Schedulable {
		schedBtn = "开调度"
		schedData = fmt.Sprintf("mgr_act:sched:%d", accountID)
	}
	watchBtn := "加入监控"
	watchData := fmt.Sprintf("mgr_act:watch:%d", accountID)
	if watched {
		watchBtn = "移出监控"
		watchData = fmt.Sprintf("mgr_act:unwatch:%d", accountID)
	}
	statusBtn := "禁用"
	statusData := fmt.Sprintf("mgr_act:confirm_disable:%d", accountID)
	if strings.EqualFold(acc.Status, "disabled") {
		statusBtn = "启用"
		statusData = fmt.Sprintf("mgr_act:enable:%d", accountID)
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(schedBtn, schedData, 1),
			discord.Button(watchBtn, watchData, 2),
			discord.Button(statusBtn, statusData, 2),
		),
		discord.ActionRow(
			discord.Button("一键修复", fmt.Sprintf("mgr_act:heal:%d", accountID), 1),
			discord.Button("清错误", fmt.Sprintf("mgr_act:clear_err:%d", accountID), 2),
			discord.Button("清限速", fmt.Sprintf("mgr_act:clear_rl:%d", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("恢复", fmt.Sprintf("mgr_act:recover:%d", accountID), 2),
			discord.Button("刷新", fmt.Sprintf("mgr_act:refresh:%d", accountID), 2),
			discord.Button("测试", fmt.Sprintf("mgr_act:test:%d", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("临时停调度", fmt.Sprintf("mgr_act:temp_menu:%d", accountID), 2),
			discord.Button("清临时停", fmt.Sprintf("mgr_act:clear_temp:%d", accountID), 2),
			discord.Button("重置额度", fmt.Sprintf("mgr_act:confirm_reset_quota:%d", accountID), 4),
		),
		discord.ActionRow(
			discord.Button("实时用量", fmt.Sprintf("acc_live:%d", accountID), 1),
			discord.Button("« 浏览", "mgr_browse:all:0", 2),
			discord.Button("« 管理", "mgr_menu", 2),
		),
	}
	return bld.String(), comps
}

func (b *Bot) doManageAction(ctx context.Context, userID int64, action string, accountID int64) string {
	if action == "confirm_unsched" {
		return fmt.Sprintf("确认停止账号 #%d 的调度？", accountID)
	}
	if action == "confirm_disable" {
		return fmt.Sprintf("确认禁用账号 #%d？", accountID)
	}
	if action == "confirm_reset_quota" {
		return fmt.Sprintf("确认重置账号 #%d 额度？此操作可能不可逆。", accountID)
	}
	if action == "temp_menu" {
		return fmt.Sprintf("选择账号 #%d 临时停调度时长：", accountID)
	}
	cli, p, err := b.userClient(userID, 25*time.Second)
	if err != nil {
		return "❌ " + err.Error()
	}
	switch action {
	case "sched":
		if _, err := cli.SetSchedulable(ctx, accountID, true); err != nil {
			return "❌ 开启调度失败: " + err.Error()
		}
		return "✅ 已开启调度"
	case "unsched":
		if _, err := cli.SetSchedulable(ctx, accountID, false); err != nil {
			return "❌ 停止调度失败: " + err.Error()
		}
		return "✅ 已停止调度"
	case "enable":
		if _, err := cli.SetAccountStatus(ctx, accountID, "active"); err != nil {
			return "❌ 启用失败: " + err.Error()
		}
		return "✅ 已启用"
	case "disable":
		if _, err := cli.SetAccountStatus(ctx, accountID, "disabled"); err != nil {
			return "❌ 禁用失败: " + err.Error()
		}
		return "✅ 已禁用"
	case "test":
		raw, err := cli.TestAccount(ctx, accountID)
		if err != nil {
			return "❌ 测试失败: " + err.Error()
		}
		return "✅ 测试: " + truncate(string(raw), 150)
	case "clear_err":
		if _, err := cli.ClearAccountError(ctx, accountID); err != nil {
			return "❌ 清错误失败: " + err.Error()
		}
		return "✅ 已清错误"
	case "clear_rl":
		if _, err := cli.ClearAccountRateLimit(ctx, accountID); err != nil {
			return "❌ 清限速失败: " + err.Error()
		}
		return "✅ 已清限速"
	case "heal":
		return b.healAccount(ctx, cli, accountID)
	case "recover":
		if _, err := cli.RecoverAccountState(ctx, accountID); err != nil {
			return "❌ 恢复失败: " + err.Error()
		}
		return "✅ 已请求恢复"
	case "refresh":
		if _, err := cli.RefreshAccount(ctx, accountID); err != nil {
			return "❌ 刷新失败: " + err.Error()
		}
		return "✅ 已刷新"
	case "clear_temp":
		if err := cli.ClearTempUnschedulable(ctx, accountID); err != nil {
			return "❌ 清除临时停失败: " + err.Error()
		}
		return "✅ 已清除临时停调度"
	case "reset_quota":
		if _, err := cli.ResetAccountQuota(ctx, accountID); err != nil {
			return "❌ 重置额度失败: " + err.Error()
		}
		return "✅ 已重置额度"
	case "watch":
		if p == nil {
			return "❌ 用户配置不存在"
		}
		name := ""
		if acc, err := cli.GetAccount(ctx, accountID); err == nil && acc != nil {
			name = acc.Name
		}
		_, err := b.users.Update(userID, func(pr *userstore.Profile) error {
			for _, a := range pr.Accounts {
				if a.ID == accountID {
					return fmt.Errorf("already watched")
				}
			}
			en := true
			pr.Accounts = append(pr.Accounts, userstore.AccountWatch{ID: accountID, Name: name, Enabled: &en})
			return nil
		})
		if err != nil {
			return "❌ 加入监控失败: " + err.Error()
		}
		return "✅ 已加入面板监控"
	case "unwatch":
		_, err := b.users.Update(userID, func(pr *userstore.Profile) error {
			out := pr.Accounts[:0]
			found := false
			for _, a := range pr.Accounts {
				if a.ID == accountID {
					found = true
					continue
				}
				out = append(out, a)
			}
			if !found {
				return fmt.Errorf("not watched")
			}
			pr.Accounts = out
			return nil
		})
		if err != nil {
			return "❌ 移出监控失败: " + err.Error()
		}
		return "✅ 已移出面板监控"
	default:
		if strings.HasPrefix(action, "temp:") {
			dur := strings.TrimPrefix(action, "temp:")
			sec := parseTempDur(dur)
			if sec <= 0 {
				return "❌ 无效时长"
			}
			if _, err := cli.SetTempUnschedulable(ctx, accountID, sec, "discord-panel"); err != nil {
				return "❌ 临时停调度失败: " + err.Error()
			}
			return fmt.Sprintf("✅ 已临时停调度 %s", dur)
		}
		return "未知操作"
	}
}

func parseTempDur(label string) int64 {
	switch strings.ToLower(strings.TrimSpace(label)) {
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

func tempMenuComponents(accountID int64) []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("15m", fmt.Sprintf("mgr_act:temp:15m:%d", accountID), 2),
			discord.Button("1h", fmt.Sprintf("mgr_act:temp:1h:%d", accountID), 2),
			discord.Button("6h", fmt.Sprintf("mgr_act:temp:6h:%d", accountID), 2),
			discord.Button("24h", fmt.Sprintf("mgr_act:temp:24h:%d", accountID), 2),
		),
		discord.ActionRow(discord.Button("取消", fmt.Sprintf("mgr_acc:%d", accountID), 2)),
	}
}

func (b *Bot) showAccountLive(ctx context.Context, userID, accountID int64, notice string) (string, []discord.Component) {
	cli, p, err := b.userClient(userID, 20*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	fmt.Fprintf(&bld, "**账号 #%d 实时**\n\n", accountID)
	if acc, err := cli.GetAccount(ctx, accountID); err == nil && acc != nil {
		fmt.Fprintf(&bld, "名称: `%s`\n平台/类型: `%s` / `%s`\n状态: `%s` · 可调度: `%v`\n",
			acc.Name, acc.Platform, acc.Type, acc.Status, acc.Schedulable)
		if acc.ErrorMessage != "" {
			fmt.Fprintf(&bld, "错误: %s\n", truncate(acc.ErrorMessage, 120))
		}
	} else if err != nil {
		fmt.Fprintf(&bld, "账号详情失败: %s\n", err.Error())
	}
	src := "passive"
	if p != nil {
		src = p.EffectiveSource()
	}
	fmt.Fprintf(&bld, "\n用量数据源: `%s`\n", src)
	if usage, err := cli.GetAccountUsage(ctx, accountID, src, false); err != nil {
		fmt.Fprintf(&bld, "用量: %s\n", err.Error())
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
			fmt.Fprintf(&bld, "• %s: `%.1f%%`%s\n", w.Window, w.Utilization, reset)
		}
	}
	if today, err := cli.GetAccountTodayStats(ctx, accountID); err == nil && today != nil {
		fmt.Fprintf(&bld, "\n今日: req=`%d` tok=`%d` cost=`%.4f`\n", today.Requests, today.Tokens, today.Cost)
	}
	comps := []discord.Component{
		discord.ActionRow(discord.Button("刷新", fmt.Sprintf("acc_live:%d", accountID), 2)),
	}
	if b.isAdmin(userID) {
		comps = append(comps,
			discord.ActionRow(
				discord.Button("一键修复", fmt.Sprintf("live_act:heal:%d", accountID), 1),
				discord.Button("清错误", fmt.Sprintf("live_act:clear_err:%d", accountID), 2),
				discord.Button("清限速", fmt.Sprintf("live_act:clear_rl:%d", accountID), 2),
			),
			discord.ActionRow(
				discord.Button("恢复", fmt.Sprintf("live_act:recover:%d", accountID), 2),
				discord.Button("完整管理", fmt.Sprintf("mgr_acc:%d", accountID), 2),
			),
		)
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("« 管理", fmt.Sprintf("mgr_acc:%d", accountID), 2),
		discord.Button("« 浏览", "mgr_browse:all:0", 2),
	))
	return bld.String(), comps
}

func (b *Bot) handleLiveAction(ctx context.Context, userID int64, action string, accountID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 25*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	notice := ""
	switch action {
	case "heal":
		notice = b.healAccount(ctx, cli, accountID)
	case "clear_err":
		if _, err := cli.ClearAccountError(ctx, accountID); err != nil {
			notice = "❌ 清除错误失败: " + err.Error()
		} else {
			notice = "✅ 已清除错误状态"
		}
	case "clear_rl":
		if _, err := cli.ClearAccountRateLimit(ctx, accountID); err != nil {
			notice = "❌ 清除限速失败: " + err.Error()
		} else {
			notice = "✅ 已清除限速"
		}
	case "recover":
		if _, err := cli.RecoverAccountState(ctx, accountID); err != nil {
			notice = "❌ 恢复状态失败: " + err.Error()
		} else {
			notice = "✅ 已请求恢复状态"
		}
	default:
		notice = "未知操作"
	}
	return b.showAccountLive(ctx, userID, accountID, notice)
}

// loadDiscordBulkTargets selects accounts for bulk ops.
func loadDiscordBulkTargets(ctx context.Context, cli *sub2api.Client, action string, maxOps int) ([]sub2api.Account, int64, string, error) {
	return browse.LoadBulkTargets(ctx, cli, action, maxOps)
}

func (b *Bot) bulkActionPrompt(ctx context.Context, userID int64, action, title, confirmID string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	const maxOps = 20
	items, total, scope, err := loadDiscordBulkTargets(ctx, cli, action, maxOps)
	if err != nil {
		return "拉取账号失败: " + err.Error(), manageComponents()
	}
	if len(items) == 0 {
		return "✅ 当前没有可处理的账号（" + scope + "）。", manageComponents()
	}
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "**%s**\n\n范围: %s\n将对约 %d 个中的前 %d 个执行「%s」：\n", title, scope, total, n, action)
	for i := 0; i < n && i < 8; i++ {
		a := items[i]
		fmt.Fprintf(&bld, "• #%d %s\n", a.ID, truncate(a.Name, 16))
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.DangerButton(fmt.Sprintf("确认处理 %d 个", n), confirmID),
			discord.Button("取消", "mgr_menu", 2),
		),
	}
	return bld.String(), comps
}

func (b *Bot) bulkAccountActionExecute(ctx context.Context, userID int64, action string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 45*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	const maxOps = 20
	items, total, scope, err := loadDiscordBulkTargets(ctx, cli, action, maxOps)
	if err != nil {
		return "拉取失败: " + err.Error(), manageComponents()
	}
	if len(items) == 0 {
		return "✅ 当前没有可处理的账号（" + scope + "）", manageComponents()
	}
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
	okN, failN := 0, 0
	var fails []string
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
			opErr = fmt.Errorf("unknown action %s", action)
		}
		if opErr != nil {
			failN++
			if len(fails) < 5 {
				fails = append(fails, fmt.Sprintf("#%d %s", a.ID, truncate(opErr.Error(), 40)))
			}
		} else {
			okN++
		}
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
	var bld strings.Builder
	fmt.Fprintf(&bld, "**%s**\n\nerror 账号约 %d 个（本次处理 %d）\n✅ 成功 %d · ❌ 失败 %d\n", title, total, n, okN, failN)
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + f + "\n")
		}
	}
	return bld.String(), manageComponents()
}

// healAccount best-effort: clear error, clear rate limit, recover, enable schedule.
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
			fail = append(fail, s.name+": "+truncate(err.Error(), 40))
		} else {
			ok = append(ok, s.name)
		}
	}
	if len(ok) == 0 {
		return "❌ 一键修复全部失败: " + strings.Join(fail, "; ")
	}
	msg := "✅ 一键修复完成: " + strings.Join(ok, " · ")
	if len(fail) > 0 {
		msg += "\n⚠️ 部分失败: " + strings.Join(fail, "; ")
	}
	return msg
}
