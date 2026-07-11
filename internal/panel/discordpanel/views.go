package discordpanel

import (
	"context"
	"fmt"
	"regexp"
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
• **只读运维**：运维视图 / 看板 / 异常账号等只读（不可修复/调度/改角色）
• **管理员**：运维写操作 + 账号管理（调度/清错/恢复/批量/一键修复/临时停调度/启用/账号与用户搜索/面板用户）
• 异常账号支持 tab：error / 限速 / 过载 / 停调度 / 临时停 / 禁用 / 汇总
• 批量操作优先使用当前「账号浏览 / 异常 tab」筛选范围
• 角色由 admin_user_ids 或 profile.role=admin|viewer|user 控制
• 配置按用户隔离，存于 users.json（可与 Telegram 共享）
• 按钮可弹出输入框（搜索 / Base URL / API Key / 添加账号 ID）；也可用斜杠命令
• 斜杠命令：` + "`/panel` `/status` `/check` `/setbase` `/setkey` `/addaccount` `/search` `/ops` `/manage`"
}

func (b *Bot) homeText(userID int64) string {
	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString("**Sub2API 监控面板 (Discord)**\n")
	fmt.Fprintf(&bld, "实例: `%s` · 角色: `%s`\n", b.cfg.Instance, b.roleLabel(userID))
	fmt.Fprintf(&bld, "检查间隔: `%s` · 冷却: `%s`\n\n",
		b.panelCfg().CheckInterval.String(), b.panelCfg().Cooldown.String())
	if b.canOpsRead(userID) {
		if cli, _, err := b.userClient(userID, 5*time.Second); err == nil && cli != nil {
			if st, err := cli.GetDashboardStats(context.Background()); err == nil && st != nil {
				bld.WriteString("**运维快照**\n")
				fmt.Fprintf(&bld, "正常 `%v` · 异常 `%v` · 限速 `%v` · 过载 `%v`\n",
					st.NormalAccounts, st.ErrorAccounts, st.RatelimitAccounts, st.OverloadAccounts)
				issues := st.ErrorAccounts > 0 || st.RatelimitAccounts > 0 || st.OverloadAccounts > 0
				if traf, err := cli.GetRealtimeTraffic(context.Background(), "5min"); err == nil && traf != nil && traf.Enabled {
					qps, peak := traf.CurrentQPS(), traf.PeakQPS()
					if browse.TrafficIsDropped(qps, peak) {
						fmt.Fprintf(&bld, "流量: ⚠ 相对峰值下降约 `%d%%`（QPS `%.2f` / 峰值 `%.2f`）\n",
							browse.TrafficDropPercent(qps, peak), qps, peak)
						issues = true
					}
				}
				if issues {
					bld.WriteString("可从下方运维/看板快速处理异常。\n")
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
	opsLabel := "运维"
	badLabel := "异常账号"
	badData := "ops_badacc:error:0"
	mgrLabel := "管理"
	if !b.isAdmin(userID) {
		mgrLabel = "账号浏览"
	}
	if b.canOpsRead(userID) {
		if cli, _, err := b.userClient(userID, 4*time.Second); err == nil && cli != nil {
			if st, err := cli.GetDashboardStats(context.Background()); err == nil && st != nil {
				opsL, badL, data, issues := browse.DashboardTriage(st)
				opsLabel = opsL
				badLabel = badL
				badData = data
				if b.isAdmin(userID) && issues {
					mgrLabel = "管理修复"
				}
			}
		}
	}
	if b.isAdmin(userID) {
		return []discord.Component{
			discord.ActionRow(
				discord.PrimaryButton("状态", "status"),
				discord.Button(opsLabel, "ops_menu", 2),
				discord.Button(mgrLabel, "mgr_menu", 2),
			),
			discord.ActionRow(
				discord.Button("看板", "ops_dash", 2),
				discord.Button(badLabel, badData, 2),
				discord.Button("告警", "ops_alerts", 2),
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
	if b.isViewer(userID) {
		return []discord.Component{
			discord.ActionRow(
				discord.PrimaryButton("状态", "status"),
				discord.Button(opsLabel, "ops_menu", 2),
				discord.Button("看板", "ops_dash", 2),
			),
			discord.ActionRow(
				discord.Button(badLabel, badData, 2),
				discord.Button(mgrLabel, "mgr_menu", 2),
				discord.Button("监控账号", "cfg_acc", 2),
			),
			discord.ActionRow(
				discord.Button("连接", "cfg_conn", 2),
				discord.Button("阈值", "cfg_thr", 2),
				discord.SuccessButton("立即检查", "check_now"),
			),
			discord.ActionRow(
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

func (b *Bot) statusText(ctx context.Context, userID int64) string {
	text, _ := b.statusTextWithIssues(ctx, userID)
	return text
}

func (b *Bot) statusTextWithIssues(ctx context.Context, userID int64) (string, []int64) {

	p, _ := b.users.Get(userID)
	var bld strings.Builder
	bld.WriteString("**运行状态**\n")
	fmt.Fprintf(&bld, "实例: `%s` · 角色: `%s`\n", b.cfg.Instance, b.roleLabel(userID))
	fmt.Fprintf(&bld, "检查间隔: `%s` · 冷却: `%s`\n",
		b.panelCfg().CheckInterval.String(), b.panelCfg().Cooldown.String())
	fmt.Fprintf(&bld, "时间: `%s`\n\n", time.Now().Local().Format("01-02 15:04:05"))

	if b.canOpsRead(userID) {
		if cli, _, err := b.userClient(userID, 5*time.Second); err == nil && cli != nil {
			if st, err := cli.GetDashboardStats(ctx); err == nil && st != nil {
				bld.WriteString("**实例健康**\n")
				fmt.Fprintf(&bld, "正常 `%v` · 异常 `%v` · 限速 `%v` · 过载 `%v`\n",
					st.NormalAccounts, st.ErrorAccounts, st.RatelimitAccounts, st.OverloadAccounts)
				issues := st.ErrorAccounts > 0 || st.RatelimitAccounts > 0 || st.OverloadAccounts > 0
				if traf, err := cli.GetRealtimeTraffic(ctx, "5min"); err == nil && traf != nil && traf.Enabled {
					qps, peak := traf.CurrentQPS(), traf.PeakQPS()
					if browse.TrafficIsDropped(qps, peak) {
						fmt.Fprintf(&bld, "流量: ⚠ 相对峰值下降约 `%d%%`（QPS `%.2f` / 峰值 `%.2f`）\n",
							browse.TrafficDropPercent(qps, peak), qps, peak)
						issues = true
					}
				}
				if rt, err := cli.GetRealtimeDashboard(ctx); err == nil && rt != nil {
					fmt.Fprintf(&bld, "实时: 活跃 `%d` · RPM `%.1f` · 错误率 `%.2f%%`\n",
						rt.ActiveRequests, rt.RequestsPerMinute, rt.ErrorRate)
					// ErrorRate is already percentage-like (e.g. 2.5 = 2.5%)
					if rt.ErrorRate >= 5 || (rt.ActiveRequests > 0 && rt.ErrorRate >= 2) {
						issues = true
					}
				}
				if issues {
					bld.WriteString("可从下方运维入口处理异常。\n")
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
	fmt.Fprintf(&bld, "监控: `%s` · 数据源: `%s`\n", mon, p.EffectiveSource())
	base := p.BaseURL
	if base == "" {
		base = "(未设置)"
	}
	fmt.Fprintf(&bld, "Base URL: `%s`\n", base)
	fmt.Fprintf(&bld, "API Key: `%s`\n", userstore.MaskKey(p.AdminAPIKey))
	enabled := make([]userstore.AccountWatch, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		if a.IsEnabled() {
			enabled = append(enabled, a)
		}
	}
	fmt.Fprintf(&bld, "监控账号: `%d` 个（启用 `%d`）\n", len(p.Accounts), len(enabled))
	thsLine := p.Thresholds
	srcLabel := "系统默认"
	if len(thsLine) > 0 {
		srcLabel = "自定义"
	} else {
		thsLine = b.defaults
	}
	fmt.Fprintf(&bld, "阈值(%s): ", srcLabel)
	if len(thsLine) == 0 {
		bld.WriteString("(无)\n\n")
	} else {
		parts := make([]string, 0, len(thsLine))
		for _, t := range thsLine {
			parts = append(parts, fmt.Sprintf("%s≥%.0f%%", t.Window, t.UtilizationGTE))
		}
		fmt.Fprintf(&bld, "`%s`\n\n", strings.Join(parts, ", "))
	}
	if !p.HasConnection() {
		bld.WriteString("⚠️ 请先配置连接信息")
		return bld.String(), nil
	}
	if len(p.Accounts) == 0 {
		bld.WriteString("⚠️ 请添加至少一个监控账号")
		return bld.String(), nil
	}
	bld.WriteString("**启用账号快照**（含用量）\n")
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 12 * time.Second,
	})
	if err != nil {
		bld.WriteString("客户端错误: " + err.Error())
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
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("#%d", a.ID)
		}
		targets = append(targets, browse.WatchTarget{ID: a.ID, Name: name})
		thByID[a.ID] = a.Thresholds
	}
	snaps := browse.FetchAccountSnaps(ctx, cli, targets, browse.SnapOpts{Source: usageSrc, MaxShow: maxShow, Concurrency: 4})
	for _, snap := range snaps {
		name := snap.Name
		if name == "" {
			name = fmt.Sprintf("#%d", snap.ID)
		}
		flag := "✅"
		statusBad := false
		if snap.AccountErr != nil {
			flag = "❓"
			statusBad = true
			fmt.Fprintf(&bld, "%s `#%d` %s · %s\n", flag, snap.ID, truncate(name, 14), truncate(snap.AccountErr.Error(), 40))
		} else if acc := snap.Account; acc != nil {
			flag = browse.StatusFlag(*acc)
			statusBad = browse.AccountIsUnhealthy(*acc)
			parts := browse.StatusDetailParts(*acc)
			fmt.Fprintf(&bld, "%s `#%d` %s · `%s`\n", flag, snap.ID, truncate(name, 14), strings.Join(parts, "/"))
			if acc.ErrorMessage != "" && browse.AccountIssueKind(*acc) == browse.IssueError {
				fmt.Fprintf(&bld, "   %s\n", truncate(acc.ErrorMessage, 48))
			}
		} else {
			fmt.Fprintf(&bld, "%s `#%d` %s\n", flag, snap.ID, truncate(name, 14))
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
			usageLine = "用量: " + truncate(snap.UsageErr.Error(), 36)
			usageHit = true
		} else if usage := snap.Usage; usage != nil {
			sum, hit := usage.CompactUsageSummary(thMap, 3)
			usageHit = hit
			if sum == "" {
				sum = "(无窗口)"
			}
			usageLine = "用量: `" + sum + "`"
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
		fmt.Fprintf(&bld, "… 另有 `%d` 个启用账号\n", len(enabled)-maxShow)
	}
	if len(enabled) == 0 {
		bld.WriteString("(没有启用的监控账号)\n")
	} else if warnN > 0 {
		fmt.Fprintf(&bld, "\n⚠️ 需关注 `%d` 个账号", warnN)
		if usageHitN > 0 {
			fmt.Fprintf(&bld, "（含 `%d` 个超阈值/用量异常）", usageHitN)
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

func (b *Bot) statusComponents(userID int64, issueIDs ...[]int64) []discord.Component {
	var issues []int64
	if len(issueIDs) > 0 {
		issues = issueIDs[0]
	}
	if len(issues) > 0 && b.canOpsWrite(userID) {
		b.setBrowseView(userID, "problem", 0)
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新状态", "status", 2),
			discord.SuccessButton("立即检查", "check_now"),
			discord.Button("监控账号", "cfg_acc", 2),
		),
	}
	if len(issues) > 0 {
		var row []discord.Component
		maxN := 4
		if b.canOpsRead(userID) {
			maxN = 2 // leave room for ops/heal rows (Discord max 5)
		}
		for i, id := range issues {
			if i >= maxN {
				break
			}
			if b.canOpsRead(userID) {
				row = append(row, discord.Button(fmt.Sprintf("查看 #%d", id), fmt.Sprintf("mgr_acc:%d", id), 1))
			} else {
				row = append(row, discord.Button(fmt.Sprintf("实时 #%d", id), fmt.Sprintf("acc_live:%d", id), 2))
			}
			if len(row) == 2 {
				comps = append(comps, discord.ActionRow(row...))
				row = nil
			}
		}
		if len(row) > 0 {
			comps = append(comps, discord.ActionRow(row...))
		}
	}
	if b.canOpsRead(userID) {
		mgrLabel := "账号管理"
		if b.isViewer(userID) {
			mgrLabel = "账号浏览"
		}
		if b.canOpsWrite(userID) && len(issues) > 0 {
			comps = append(comps,
				discord.ActionRow(
					discord.Button("一键修复", "mgr_bulk_heal", 1),
					discord.Button("异常账号", "ops_badacc:error:0", 2),
					discord.Button("异常汇总", "mgr_browse:problem:0", 2),
				),
				discord.ActionRow(
					discord.Button("运维", "ops_menu", 2),
					discord.Button(mgrLabel, "mgr_menu", 2),
					discord.Button("« 主面板", "home", 2),
				),
			)
		} else {
			comps = append(comps,
				discord.ActionRow(
					discord.Button("运维", "ops_menu", 2),
					discord.Button("异常账号", "ops_badacc:error:0", 2),
					discord.Button("看板", "ops_dash", 2),
				),
				discord.ActionRow(
					discord.Button(mgrLabel, "mgr_menu", 2),
					discord.Button("连接", "cfg_conn", 2),
					discord.Button("« 主面板", "home", 2),
				),
			)
		}
	} else {
		comps = append(comps, discord.ActionRow(
			discord.Button("连接", "cfg_conn", 2),
			discord.Button("« 主面板", "home", 2),
		))
	}
	// Discord max 5 action rows
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return comps
}

func (b *Bot) statusView(ctx context.Context, userID int64) (string, []discord.Component) {
	text, issues := b.statusTextWithIssues(ctx, userID)
	return text, b.statusComponents(userID, issues)
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
	bld.WriteString("\n点下方按钮弹出输入框，或用 `/setbase` `/setkey`。")
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
	cli, _, err := b.userClient(userID, 8*time.Second)
	if err != nil || cli == nil {
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
	bld.WriteString("**监控账号**\n\n")
	if p == nil || len(p.Accounts) == 0 {
		bld.WriteString("暂无账号。可「从列表选择」或 `/addaccount id:123`。")
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
	bld.WriteString("\n下拉选择账号查看详情；也可切换启用/删除。")
	return bld.String()
}

func (b *Bot) accountsComponents(userID int64) []discord.Component {
	rows := []discord.Component{
		discord.ActionRow(
			discord.PrimaryButton("从列表选择", "pick_acc"),
			discord.Button("手动添加", "add_acc_prompt", 2),
		),
	}
	if p, ok := b.users.Get(userID); ok && len(p.Accounts) > 0 {
		opts := make([]discord.SelectOpt, 0, len(p.Accounts))
		for _, a := range p.Accounts {
			if len(opts) >= 20 {
				break
			}
			name := a.Name
			if name == "" {
				name = fmt.Sprintf("#%d", a.ID)
			}
			en := "启用"
			if !a.IsEnabled() {
				en = "暂停"
			}
			opts = append(opts, discord.SelectOption(
				fmt.Sprintf("#%d %s", a.ID, truncate(name, 18)),
				fmt.Sprintf("acc:%d", a.ID),
				en,
			))
		}
		rows = append(rows, discord.ActionRow(discord.StringSelect("select:acc", "选择监控账号…", opts...)))
		// quick actions for first few accounts
		n := 0
		for _, a := range p.Accounts {
			if n >= 2 { // keep under 5 action rows total with nav
				break
			}
			tog := "暂停"
			if !a.IsEnabled() {
				tog = "启用"
			}
			rows = append(rows, discord.ActionRow(
				discord.Button(fmt.Sprintf("实时#%d", a.ID), fmt.Sprintf("acc_live:%d", a.ID), 1),
				discord.Button(fmt.Sprintf("%s#%d", tog, a.ID), fmt.Sprintf("tog_acc:%d", a.ID), 2),
				discord.DangerButton(fmt.Sprintf("删#%d", a.ID), fmt.Sprintf("del_acc:%d", a.ID)),
			))
			n++
		}
	}
	rows = append(rows, discord.ActionRow(discord.Button("« 主面板", "home", 2)))
	if len(rows) > 5 {
		rows = rows[:5]
	}
	return rows
}

// accountDetailView shows a watched account with live status/usage.
func (b *Bot) accountDetailView(ctx context.Context, userID, id int64) (string, []discord.Component) {
	p, ok := b.users.Get(userID)
	if !ok {
		return "用户不存在", b.accountsComponents(userID)
	}
	var a *userstore.AccountWatch
	for i := range p.Accounts {
		if p.Accounts[i].ID == id {
			a = &p.Accounts[i]
			break
		}
	}
	if a == nil {
		return fmt.Sprintf("未找到监控账号 #%d", id), b.accountsComponents(userID)
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "**监控账号 #%d**\n\n", id)
	name := a.Name
	if name == "" {
		name = fmt.Sprintf("#%d", id)
	}
	en := "启用"
	if !a.IsEnabled() {
		en = "暂停"
	}
	fmt.Fprintf(&bld, "名称: `%s`\n监控状态: `%s`\n", name, en)
	ths := a.Thresholds
	if len(ths) == 0 {
		bld.WriteString("阈值: 继承用户/系统默认\n")
	} else {
		bld.WriteString("账号级阈值:\n")
		for _, t := range ths {
			fmt.Fprintf(&bld, "  • `%s` ≥ `%.0f%%` (%s)\n", t.Window, t.UtilizationGTE, t.Severity)
		}
	}

	// live enrich
	if cli, _, err := b.userClient(userID, 12*time.Second); err == nil && cli != nil {
		if acc, err := cli.GetAccount(ctx, id); err == nil && acc != nil {
			bld.WriteString("\n**实例状态**\n")
			fmt.Fprintf(&bld, "平台/类型: `%s` / `%s`\n状态: `%s` · 可调度: `%v`\n",
				acc.Platform, acc.Type, acc.Status, acc.Schedulable)
			if acc.ErrorMessage != "" {
				fmt.Fprintf(&bld, "错误: %s\n", truncate(acc.ErrorMessage, 100))
			}
			if name == fmt.Sprintf("#%d", id) && acc.Name != "" {
				// keep display only
			}
		} else if err != nil {
			fmt.Fprintf(&bld, "\n实例状态: %s\n", truncate(err.Error(), 80))
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
				mark = " ⚠"
			}
			fmt.Fprintf(&bld, "\n用量(`%s`): `%s`%s\n", src, sum, mark)
		} else if err != nil {
			fmt.Fprintf(&bld, "\n用量: %s\n", truncate(err.Error(), 60))
		}
	}

	togLabel := "暂停监控"
	if !a.IsEnabled() {
		togLabel = "启用监控"
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.PrimaryButton("实时用量", fmt.Sprintf("acc_live:%d", id)),
			discord.Button(togLabel, fmt.Sprintf("tog_acc:%d", id), 2),
			discord.Button("账号阈值", fmt.Sprintf("acc_thr:%d", id), 2),
		),
		discord.ActionRow(
			discord.Button("重命名", fmt.Sprintf("rename:%d", id), 2),
			discord.DangerButton("移出监控", fmt.Sprintf("del_acc:%d", id)),
		),
	}
	if b.canOpsRead(userID) {
		label := "管理操作"
		if b.isViewer(userID) {
			label = "账号详情"
		}
		comps = append(comps, discord.ActionRow(
			discord.Button(label, fmt.Sprintf("mgr_acc:%d", id), 2),
		))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("« 监控账号", "cfg_acc", 2),
		discord.Button("« 主面板", "home", 2),
	))
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return bld.String(), comps
}

// accountPickerView lists Sub2API accounts to add to the watch list.
func (b *Bot) accountPickerView(ctx context.Context, userID int64, status string, page int) (string, []discord.Component) {
	p, ok := b.users.Get(userID)
	if !ok || !p.HasConnection() {
		return "❌ 请先配置连接后再从列表选择", b.connComponents(userID)
	}
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL: p.BaseURL, AdminAPIKey: p.AdminAPIKey, JWT: p.JWT, Timeout: 15 * time.Second,
	})
	if err != nil {
		return "客户端错误: " + err.Error(), b.accountsComponents(userID)
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
		return "拉取账号列表失败: " + err.Error(), b.accountsComponents(userID)
	}
	watched := map[int64]bool{}
	for _, a := range p.Accounts {
		watched[a.ID] = true
	}
	var bld strings.Builder
	bld.WriteString("**选择账号添加监控**\n")
	fmt.Fprintf(&bld, "筛选: `%s` · 第 %d 页 · 共 %d 个\n", status, page+1, total)
	bld.WriteString("已监控标 ✓；下拉或筛选后选择添加。\n\n")
	for _, acc := range items {
		mark := ""
		if watched[acc.ID] {
			mark = "✓ "
		}
		fmt.Fprintf(&bld, "%s`#%d` %s · `%s/%s`\n", mark, acc.ID, truncate(acc.Name, 16), acc.Platform, acc.Status)
	}
	if len(items) == 0 {
		bld.WriteString("(本页无账号)\n")
	}

	token := browse.Token(status)
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(pickFilterBtn(status, "all", "全部"), "pick_acc:all:0", 2),
			discord.Button(pickFilterBtn(status, "active", "active"), "pick_acc:active:0", 2),
			discord.Button(pickFilterBtn(status, "error", "error"), "pick_acc:error:0", 2),
			discord.Button(pickFilterBtn(status, "rate_limited", "限速"), "pick_acc:rate_limited:0", 2),
		),
		discord.ActionRow(
			discord.Button(pickFilterBtn(status, "unsched", "停调度"), "pick_acc:unsched:0", 2),
			discord.Button(pickFilterBtn(status, "overload", "过载"), "pick_acc:overload:0", 2),
		),
	}
	if len(items) > 0 {
		opts := make([]discord.SelectOpt, 0, len(items))
		for _, acc := range items {
			if len(opts) >= 8 {
				break
			}
			mark := ""
			if watched[acc.ID] {
				mark = "✓ "
			}
			opts = append(opts, discord.SelectOption(
				fmt.Sprintf("%s#%d %s", mark, acc.ID, truncate(acc.Name, 16)),
				fmt.Sprintf("pick:%d", acc.ID),
				fmt.Sprintf("%s/%s", acc.Platform, acc.Status),
			))
		}
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:pick", "选择添加…", opts...)))
	}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("pick_acc:%s:%d", token, page-1), 2))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("pick_acc:%s:%d", token, page+1), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("手动输入 ID", "add_acc_prompt", 2),
		discord.Button("« 监控账号", "cfg_acc", 2),
	))
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return bld.String(), comps
}

func pickFilterBtn(cur, want, label string) string {
	if cur == want {
		return "· " + label
	}
	return label
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

func (b *Bot) thrComponents(userID int64) []discord.Component {
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("添加/改阈值", "thr_add", 1),
			discord.Button("写入系统默认", "thr_apply_defs", 2),
			discord.Button("重置默认", "thr_reset", 2),
		),
	}
	// dynamic delete buttons for current effective thresholds
	var ths []config.UsageThreshold
	if p, ok := b.users.Get(userID); ok && len(p.Thresholds) > 0 {
		ths = p.Thresholds
	} else {
		ths = b.defaults
	}
	row := []discord.Component{}
	for _, t := range ths {
		w := sub2api.NormalizeWindow(t.Window)
		if w == "" {
			continue
		}
		label := "删 " + w
		switch w {
		case "five_hour":
			label = "删 5h"
		case "seven_day":
			label = "删 7d"
		case "seven_day_sonnet":
			label = "删 7d-s"
		case "seven_day_fable":
			label = "删 7d-f"
		case "gemini_shared_daily":
			label = "删 g-sh"
		case "gemini_pro_daily":
			label = "删 g-pro"
		case "gemini_flash_daily":
			label = "删 g-fl"
		}
		row = append(row, discord.DangerButton(truncate(label, 20), "thr_del:"+w))
		if len(row) == 3 {
			comps = append(comps, discord.ActionRow(row...))
			row = nil
		}
		if len(comps) >= 4 { // leave room for home row
			break
		}
	}
	if len(row) > 0 && len(comps) < 4 {
		comps = append(comps, discord.ActionRow(row...))
	}
	comps = append(comps, discord.ActionRow(discord.Button("« 主面板", "home", 2)))
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return comps
}

func thrWindowPickComponents() []discord.Component {
	// pick window first, then custom % via modal (or jump to presets)
	return []discord.Component{
		discord.ActionRow(
			discord.Button("5 小时", "thr_win:five_hour", 2),
			discord.Button("7 天", "thr_win:seven_day", 2),
			discord.Button("7d Sonnet", "thr_win:seven_day_sonnet", 2),
		),
		discord.ActionRow(
			discord.Button("7d Fable", "thr_win:seven_day_fable", 2),
			discord.Button("Gemini 共享", "thr_win:gemini_shared_daily", 2),
			discord.Button("Gemini Pro", "thr_win:gemini_pro_daily", 2),
		),
		discord.ActionRow(
			discord.Button("Gemini Flash", "thr_win:gemini_flash_daily", 2),
			discord.Button("max", "thr_win:max", 2),
			discord.Button("快捷预设", "thr_presets", 1),
		),
		discord.ActionRow(discord.Button("« 阈值", "cfg_thr", 2)),
	}
}

func thrWindowComponents() []discord.Component {
	// quick presets for common windows + percent combos
	return []discord.Component{
		discord.ActionRow(
			discord.Button("5h≥70%", "thr_set:five_hour:70", 2),
			discord.Button("5h≥80%", "thr_set:five_hour:80", 2),
			discord.Button("5h≥90%", "thr_set:five_hour:90", 2),
		),
		discord.ActionRow(
			discord.Button("7d≥70%", "thr_set:seven_day:70", 2),
			discord.Button("7d≥80%", "thr_set:seven_day:80", 2),
			discord.Button("7d≥90%", "thr_set:seven_day:90", 2),
		),
		discord.ActionRow(
			discord.Button("7d-s≥80%", "thr_set:seven_day_sonnet:80", 2),
			discord.Button("7d-f≥80%", "thr_set:seven_day_fable:80", 2),
			discord.Button("g-pro≥80%", "thr_set:gemini_pro_daily:80", 2),
		),
		discord.ActionRow(
			discord.Button("g-sh≥80%", "thr_set:gemini_shared_daily:80", 2),
			discord.Button("g-fl≥80%", "thr_set:gemini_flash_daily:80", 2),
			discord.Button("max≥90%", "thr_set:max:90", 2),
		),
		discord.ActionRow(
			discord.Button("自定义窗口", "thr_add", 2),
			discord.Button("« 阈值", "cfg_thr", 2),
		),
	}
}

// opsMenuText builds the ops hub with optional live health snapshot.
func (b *Bot) opsMenuText(ctx context.Context, userID int64) string {
	var bld strings.Builder
	bld.WriteString("**运维视图**\n\n")
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil && st != nil {
			fmt.Fprintf(&bld, "健康: 正常 `%v` · 异常 `%v` · 限速 `%v` · 过载 `%v`\n",
				st.NormalAccounts, st.ErrorAccounts, st.RatelimitAccounts, st.OverloadAccounts)
			if st.RPM > 0 {
				fmt.Fprintf(&bld, "RPM `%.1f` · 今日请求 `%v`\n", st.RPM, st.TodayRequests)
			}
			if rt, err := cli.GetRealtimeDashboard(ctx); err == nil && rt != nil {
				fmt.Fprintf(&bld, "实时: 活跃 `%v` · 错误率 `%.2f%%`\n", rt.ActiveRequests, rt.ErrorRate)
			}
			if traf, err := cli.GetRealtimeTraffic(ctx, "5min"); err == nil && traf != nil && traf.Enabled {
				qps, peak := traf.CurrentQPS(), traf.PeakQPS()
				line := fmt.Sprintf("流量(5min): QPS `%.3f`", qps)
				if traf.CurrentTPS() > 0 {
					line += fmt.Sprintf(" · TPS `%.3f`", traf.CurrentTPS())
				}
				if peak > 0 {
					line += fmt.Sprintf(" · 峰值 `%.3f`", peak)
				}
				bld.WriteString(line + "\n")
				if browse.TrafficIsDropped(qps, peak) {
					fmt.Fprintf(&bld, "⚠ 流量骤降约 `%d%%`（当前 ≤ 峰值 × %.0f%%）\n",
						browse.TrafficDropPercent(qps, peak), browse.TrafficDropRatio*100)
				}
			}
			bld.WriteString("\n")
		}
	}
	bld.WriteString("基于当前连接的 Admin API：\n• 看板 / 可用性 / 告警 / 并发 / 流量 / 渠道\n• 错误（分标签分页，解决后保留页码 · 修复/实时）\n• 异常账号（error/限速/停调度/汇总分标签分页 + 管理/实时/修复）")
	return bld.String()
}

func (b *Bot) opsComponents(userID int64) []discord.Component {
	var stats *sub2api.DashboardStats
	if cli, _, err := b.userClient(userID, 5*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(context.Background()); err == nil {
			stats = st
		}
	}
	return opsComponentsFor(stats, b.canOpsWrite(userID))
}

func opsComponents() []discord.Component {
	return opsComponentsFor(nil, true)
}

// opsComponentsFor builds the ops hub. When stats are present, labels include
// live error/rate-limit counts; canWrite controls bulk-heal visibility.
func opsComponentsFor(stats *sub2api.DashboardStats, canWrite bool) []discord.Component {
	badLabel := "异常账号"
	badData := "ops_badacc:error:0"
	rlLabel := "限速"
	errLabel := "错误"
	mgrLabel := "账号管理"
	if !canWrite {
		mgrLabel = "账号浏览"
	}
	olLabel := "过载"
	if stats != nil {
		_, bl, bd, _ := browse.DashboardTriage(stats)
		badLabel, badData = bl, bd
		if stats.RatelimitAccounts > 0 {
			rlLabel = fmt.Sprintf("限速 %v", stats.RatelimitAccounts)
		}
		if stats.OverloadAccounts > 0 {
			olLabel = fmt.Sprintf("过载 %v", stats.OverloadAccounts)
		}
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("看板", "ops_dash", 1),
			discord.Button("可用性", "ops_avail", 2),
			discord.Button("告警", "ops_alerts", 2),
		),
		discord.ActionRow(
			discord.Button(errLabel, "ops_errors:all:0", 2),
			discord.Button("并发", "ops_conc", 2),
			discord.Button("流量", "ops_traf", 2),
			discord.Button("渠道", "ops_channels", 2),
		),
	}
	hasIssues := stats != nil && (stats.ErrorAccounts > 0 || stats.RatelimitAccounts > 0 || stats.OverloadAccounts > 0)
	if hasIssues {
		row := []discord.Component{
			discord.Button(badLabel, badData, 1),
		}
		if stats.RatelimitAccounts > 0 {
			row = append(row, discord.Button(rlLabel, "ops_badacc:rl:0", 2))
		}
		if stats.OverloadAccounts > 0 && len(row) < 3 {
			row = append(row, discord.Button(olLabel, "ops_badacc:ol:0", 2))
		}
		if stats.RatelimitAccounts == 0 && stats.OverloadAccounts == 0 {
			row = append(row, discord.Button(rlLabel, "ops_badacc:rl:0", 2))
		}
		if canWrite {
			row = append(row, discord.Button("一键修复", "mgr_bulk_heal", 1))
		} else {
			row = append(row, discord.Button(mgrLabel, "mgr_menu", 2))
		}
		// Discord max 5 buttons/row
		if len(row) > 5 {
			row = row[:5]
		}
		comps = append(comps, discord.ActionRow(row...))
		if canWrite {
			comps = append(comps, discord.ActionRow(
				discord.Button(mgrLabel, "mgr_menu", 2),
				discord.Button("« 主面板", "home", 2),
			))
		} else {
			comps = append(comps, discord.ActionRow(
				discord.Button("« 主面板", "home", 2),
			))
		}
	} else {
		comps = append(comps, discord.ActionRow(
			discord.Button(badLabel, badData, 2),
			discord.Button(mgrLabel, "mgr_menu", 2),
			discord.Button("« 主面板", "home", 2),
		))
	}
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return comps
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

func (b *Bot) manageMenuText(ctx context.Context, userID int64) string {
	var bld strings.Builder
	bld.WriteString("**账号管理**\n\n")
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil && st != nil {
			fmt.Fprintf(&bld, "健康: 正常 `%v` · 异常 `%v` · 限速 `%v` · 过载 `%v`\n",
				st.NormalAccounts, st.ErrorAccounts, st.RatelimitAccounts, st.OverloadAccounts)
			issues := st.ErrorAccounts > 0 || st.RatelimitAccounts > 0 || st.OverloadAccounts > 0
			if traf, err := cli.GetRealtimeTraffic(ctx, "5min"); err == nil && traf != nil && traf.Enabled {
				qps, peak := traf.CurrentQPS(), traf.PeakQPS()
				if browse.TrafficIsDropped(qps, peak) {
					fmt.Fprintf(&bld, "流量: ⚠ 相对峰值下降约 `%d%%`（QPS `%.2f` / 峰值 `%.2f`）\n",
						browse.TrafficDropPercent(qps, peak), qps, peak)
					issues = true
				}
			}
			if issues {
				bld.WriteString("建议优先处理异常/限速/过载，或使用批量操作。\n")
			}
			bld.WriteString("\n")
		}
	}
	if st, _ := b.getBrowseView(userID); st != "" && st != "all" {
		fmt.Fprintf(&bld, "当前筛选: `%s`（批量操作优先此范围）\n\n", browse.Title(st))
	}
	bld.WriteString("浏览（状态/平台/停调度/限速）、搜索、切换调度、清错/恢复/一键修复、临时停调度、批量处理（优先当前浏览/异常 tab 筛选）、实例用户/分组（搜索+详情只读）、面板用户角色（Admin API / Bot 权限）。")
	return bld.String()
}

func manageComponents() []discord.Component {
	return manageComponentsFor(nil, true, "")
}

func manageComponentsFor(stats *sub2api.DashboardStats, canWrite bool, browseStatus string) []discord.Component {
	badLabel := "异常账号"
	badData := "ops_badacc:error:0"
	healLabel := "一键修复"
	clearLabel := "批量清错"
	rlLabel := "批量清限速"
	if stats != nil {
		_, bl, bd, _ := browse.DashboardTriage(stats)
		badLabel, badData = bl, bd
		if stats.ErrorAccounts > 0 {
			clearLabel = fmt.Sprintf("清错 %v", stats.ErrorAccounts)
		}
		if stats.RatelimitAccounts > 0 {
			rlLabel = fmt.Sprintf("清限速 %v", stats.RatelimitAccounts)
		}
	}
	if st := strings.TrimSpace(browseStatus); st != "" && st != "all" {
		scope := browse.Title(st)
		if r := []rune(scope); len(r) > 6 {
			scope = string(r[:6])
		}
		healLabel = "修复·" + scope
		if stats == nil || stats.ErrorAccounts == 0 {
			clearLabel = "清错·" + scope
		} else {
			clearLabel = fmt.Sprintf("清错 %v·%s", stats.ErrorAccounts, scope)
		}
		if stats == nil || stats.RatelimitAccounts == 0 {
			rlLabel = "限速·" + scope
		} else {
			rlLabel = fmt.Sprintf("限速 %v·%s", stats.RatelimitAccounts, scope)
		}
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("浏览全部", "mgr_browse:all:0", 1),
			discord.Button("error", "mgr_browse:error:0", 2),
			discord.Button("active", "mgr_browse:active:0", 2),
		),
		discord.ActionRow(
			discord.Button("停调度", "mgr_browse:unsched:0", 2),
			discord.Button("限速", "mgr_browse:rate_limited:0", 2),
			discord.Button(badLabel, badData, 2),
		),
	}
	if canWrite {
		switch strings.TrimSpace(browseStatus) {
		case "disabled":
			comps = append(comps,
				discord.ActionRow(
					discord.SuccessButton("批量启用", "mgr_bulk_enable"),
					discord.Button(healLabel, "mgr_bulk_heal", 1),
					discord.Button("搜索", "mgr_search", 2),
				),
				discord.ActionRow(
					discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
					discord.Button("批量恢复", "mgr_bulk_recover", 2),
				),
			)
		case "temp":
			comps = append(comps,
				discord.ActionRow(
					discord.Button("清临时停", "mgr_bulk_clear_temp", 2),
					discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
					discord.Button("搜索", "mgr_search", 2),
				),
				discord.ActionRow(
					discord.Button(healLabel, "mgr_bulk_heal", 1),
					discord.Button("批量恢复", "mgr_bulk_recover", 2),
				),
			)
		default:
			if stats != nil && (stats.ErrorAccounts > 0 || stats.RatelimitAccounts > 0 || stats.OverloadAccounts > 0) {
				comps = append(comps,
					discord.ActionRow(
						discord.Button(healLabel, "mgr_bulk_heal", 1),
						discord.DangerButton(clearLabel, "mgr_bulk_clear"),
						discord.Button(rlLabel, "mgr_bulk_clear_rl", 2),
					),
					discord.ActionRow(
						discord.Button("批量恢复", "mgr_bulk_recover", 2),
						discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
						discord.Button("搜索", "mgr_search", 2),
					),
				)
			} else {
				comps = append(comps,
					discord.ActionRow(
						discord.DangerButton(clearLabel, "mgr_bulk_clear"),
						discord.Button("批量恢复", "mgr_bulk_recover", 2),
						discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
					),
					discord.ActionRow(
						discord.Button(rlLabel, "mgr_bulk_clear_rl", 2),
						discord.Button(healLabel, "mgr_bulk_heal", 1),
						discord.Button("搜索", "mgr_search", 2),
					),
				)
			}
		}
		comps = append(comps, discord.ActionRow(
			discord.Button("实例用户", "mgr_users", 2),
			discord.Button("分组", "mgr_groups", 2),
			discord.Button("面板用户", "pnl_users", 2),
			discord.Button("« 主面板", "home", 2),
		))
	} else {
		comps = append(comps,
			discord.ActionRow(
				discord.Button("搜索", "mgr_search", 2),
				discord.Button("实例用户", "mgr_users", 2),
				discord.Button("分组", "mgr_groups", 2),
			),
			discord.ActionRow(
				discord.Button("« 主面板", "home", 2),
			),
		)
	}
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return comps
}

func (b *Bot) manageMenuView(ctx context.Context, userID int64) (string, []discord.Component) {
	// Hub entry: bulk return targets hub, not a previous triage tab.
	b.setManageBack(userID, "mgr_menu")
	var stats *sub2api.DashboardStats
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil {
			stats = st
		}
	}
	st, _ := b.getBrowseView(userID)
	return b.manageMenuText(ctx, userID), manageComponentsFor(stats, b.canOpsWrite(userID), st)
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

func (b *Bot) setAccountThreshold(userID, accountID int64, window string, pct float64, severity string) error {
	window = normalizeWindow(window)
	if accountID <= 0 {
		return fmt.Errorf("invalid account")
	}
	if pct <= 0 || pct > 100 {
		return fmt.Errorf("invalid pct")
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
		return fmt.Errorf("account not watched")
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
				return fmt.Errorf("window not found")
			}
			p.Accounts[i].Thresholds = out
			return nil
		}
		return fmt.Errorf("account not watched")
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
		return fmt.Errorf("account not watched")
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
		return fmt.Errorf("account not watched")
	})
	return err
}

func (b *Bot) accountThresholdsView(userID, accountID int64) (string, []discord.Component) {
	p, ok := b.users.Get(userID)
	if !ok {
		return "用户不存在", b.accountsComponents(userID)
	}
	var a *userstore.AccountWatch
	for i := range p.Accounts {
		if p.Accounts[i].ID == accountID {
			a = &p.Accounts[i]
			break
		}
	}
	if a == nil {
		return fmt.Sprintf("未找到监控账号 #%d", accountID), b.accountsComponents(userID)
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "**账号 #%d 阈值**\n", accountID)
	name := a.Name
	if name == "" {
		name = fmt.Sprintf("#%d", accountID)
	}
	fmt.Fprintf(&bld, "名称: `%s`\n\n", name)
	if len(a.Thresholds) == 0 {
		bld.WriteString("当前: **继承用户/系统默认**\n")
		ths := p.Thresholds
		src := "用户默认"
		if len(ths) == 0 {
			ths = b.defaults
			src = "系统默认"
		}
		fmt.Fprintf(&bld, "生效来源: `%s`\n", src)
		for _, t := range ths {
			sev := t.Severity
			if sev == "" {
				sev = "P2"
			}
			fmt.Fprintf(&bld, "• `%s` ≥ `%.0f%%` · `%s`\n", t.Window, t.UtilizationGTE, sev)
		}
	} else {
		bld.WriteString("当前: **账号专属**\n")
		for _, t := range a.Thresholds {
			sev := t.Severity
			if sev == "" {
				sev = "P2"
			}
			fmt.Fprintf(&bld, "• `%s` ≥ `%.0f%%` · `%s`\n", t.Window, t.UtilizationGTE, sev)
		}
	}

	comps := []discord.Component{
		discord.ActionRow(
			discord.PrimaryButton("添加/修改", fmt.Sprintf("acc_thr_add:%d", accountID)),
			discord.Button("复制默认", fmt.Sprintf("acc_thr_copy:%d", accountID), 2),
			discord.DangerButton("清除专属", fmt.Sprintf("acc_thr_clear:%d", accountID)),
		),
	}
	// delete existing account thresholds
	row := []discord.Component{}
	for _, t := range a.Thresholds {
		w := normalizeWindow(t.Window)
		label := "删 " + w
		switch w {
		case "five_hour":
			label = "删 5h"
		case "seven_day":
			label = "删 7d"
		}
		row = append(row, discord.DangerButton(truncate(label, 20), fmt.Sprintf("acc_thr_del:%d:%s", accountID, w)))
		if len(row) == 3 {
			comps = append(comps, discord.ActionRow(row...))
			row = nil
		}
		if len(comps) >= 4 {
			break
		}
	}
	if len(row) > 0 && len(comps) < 4 {
		comps = append(comps, discord.ActionRow(row...))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("« 账号详情", fmt.Sprintf("acc:%d", accountID), 2),
		discord.Button("« 监控账号", "cfg_acc", 2),
	))
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return bld.String(), comps
}

func thrWindowPickComponentsForAccount(accountID int64) []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("5 小时", fmt.Sprintf("acc_thr_win:%d:five_hour", accountID), 2),
			discord.Button("7 天", fmt.Sprintf("acc_thr_win:%d:seven_day", accountID), 2),
			discord.Button("7d Sonnet", fmt.Sprintf("acc_thr_win:%d:seven_day_sonnet", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("7d Fable", fmt.Sprintf("acc_thr_win:%d:seven_day_fable", accountID), 2),
			discord.Button("Gemini 共享", fmt.Sprintf("acc_thr_win:%d:gemini_shared_daily", accountID), 2),
			discord.Button("Gemini Pro", fmt.Sprintf("acc_thr_win:%d:gemini_pro_daily", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("Gemini Flash", fmt.Sprintf("acc_thr_win:%d:gemini_flash_daily", accountID), 2),
			discord.Button("max", fmt.Sprintf("acc_thr_win:%d:max", accountID), 2),
			discord.Button("快捷预设", fmt.Sprintf("acc_thr_presets:%d", accountID), 1),
		),
		discord.ActionRow(discord.Button("« 账号阈值", fmt.Sprintf("acc_thr:%d", accountID), 2)),
	}
}

func thrWindowComponentsForAccount(accountID int64) []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("5h≥70%", fmt.Sprintf("acc_thr_set:%d:five_hour:70", accountID), 2),
			discord.Button("5h≥80%", fmt.Sprintf("acc_thr_set:%d:five_hour:80", accountID), 2),
			discord.Button("5h≥90%", fmt.Sprintf("acc_thr_set:%d:five_hour:90", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("7d≥70%", fmt.Sprintf("acc_thr_set:%d:seven_day:70", accountID), 2),
			discord.Button("7d≥80%", fmt.Sprintf("acc_thr_set:%d:seven_day:80", accountID), 2),
			discord.Button("7d≥90%", fmt.Sprintf("acc_thr_set:%d:seven_day:90", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("7d-s≥80%", fmt.Sprintf("acc_thr_set:%d:seven_day_sonnet:80", accountID), 2),
			discord.Button("g-pro≥80%", fmt.Sprintf("acc_thr_set:%d:gemini_pro_daily:80", accountID), 2),
			discord.Button("max≥90%", fmt.Sprintf("acc_thr_set:%d:max:90", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("自定义窗口", fmt.Sprintf("acc_thr_add:%d", accountID), 2),
			discord.Button("« 账号阈值", fmt.Sprintf("acc_thr:%d", accountID), 2),
		),
	}
}

func normalizeWindow(w string) string {
	return sub2api.NormalizeWindow(w)
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
	text, _ := b.forceCheckView(ctx, userID)
	return text
}

func (b *Bot) forceCheckView(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, p, err := b.userClient(userID, 25*time.Second)
	if err != nil {
		return "❌ " + err.Error(), b.homeComponents(userID)
	}
	if p == nil || len(p.Accounts) == 0 {
		return "请先添加监控账号", b.homeComponents(userID)
	}
	src := p.EffectiveSource()
	force := strings.EqualFold(src, "active")
	thsDefault := p.Thresholds
	if len(thsDefault) == 0 {
		thsDefault = b.defaults
	}
	var bld strings.Builder
	forceLabel := "缓存"
	if force {
		forceLabel = "强制刷新"
	}
	fmt.Fprintf(&bld, "**立即检查** · `%s` · `%s`\n\n", src, forceLabel)
	warnN := 0
	var issueIDs []int64
	var targets []browse.WatchTarget
	thByID := map[int64][]config.UsageThreshold{}
	for _, a := range p.Accounts {
		if !a.IsEnabled() {
			fmt.Fprintf(&bld, "• #%d 已暂停\n", a.ID)
			continue
		}
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("#%d", a.ID)
		}
		targets = append(targets, browse.WatchTarget{ID: a.ID, Name: name})
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
		accBad := false
		statusTag := ""
		if acc := snap.Account; acc != nil {
			accBad = browse.AccountIsUnhealthy(*acc)
			if accBad {
				statusTag = fmt.Sprintf(" %s`%s`", browse.StatusFlag(*acc), strings.Join(browse.StatusDetailParts(*acc), "/"))
			}
		} else if snap.AccountErr != nil {
			accBad = true
			statusTag = " 详情失败"
		}
		if snap.UsageErr != nil {
			fmt.Fprintf(&bld, "• #%d 失败: %s\n", snap.ID, truncate(snap.UsageErr.Error(), 60))
			warnN++
			if len(issueIDs) < 4 {
				issueIDs = append(issueIDs, snap.ID)
			}
			continue
		}
		fmt.Fprintf(&bld, "**#%d %s**%s\n", snap.ID, name, statusTag)
		hitThr := false
		thMap := map[string]float64{}
		ths := thByID[snap.ID]
		if len(ths) == 0 {
			ths = thsDefault
		}
		for _, th := range ths {
			thMap[sub2api.NormalizeWindow(th.Window)] = th.UtilizationGTE
		}
		if usage := snap.Usage; usage != nil {
			sum, hit := usage.CompactUsageSummary(thMap, 4)
			if hit {
				hitThr = true
			}
			if sum == "" {
				bld.WriteString("  用量: (无窗口数据)\n")
			} else {
				fmt.Fprintf(&bld, "  用量: %s\n", sum)
			}
		}
		if today := snap.Today; today != nil {
			fmt.Fprintf(&bld, "  today: req=%d token=%d cost=%.2f\n", today.Requests, today.Tokens, today.Cost)
		}
		if hitThr || accBad {
			warnN++
			if len(issueIDs) < 4 {
				issueIDs = append(issueIDs, snap.ID)
			}
		}
	}
	if warnN > 0 {
		fmt.Fprintf(&bld, "\n⚠ 需关注 %d 个账号（超阈值或状态异常）。\n", warnN)
	} else {
		bld.WriteString("\n✅ 监控账号用量与状态正常。\n")
	}
	// Row budget (max 5): top nav + up to 2 issue rows + triage + optional ops
	comps := []discord.Component{
		discord.ActionRow(
			discord.SuccessButton("再检查", "check_now"),
			discord.Button("« 主面板", "home", 2),
		),
	}
	// Cap issue shortcuts so total rows stay ≤5 with footer.
	maxIssueBtns := 4
	if b.canOpsRead(userID) {
		maxIssueBtns = 2 // leave room for triage footer rows
	}
	if len(issueIDs) > 0 {
		row := []discord.Component{}
		shown := 0
		for _, id := range issueIDs {
			if shown >= maxIssueBtns {
				break
			}
			if b.canOpsRead(userID) {
				row = append(row, discord.Button(fmt.Sprintf("查看 #%d", id), fmt.Sprintf("mgr_acc:%d", id), 1))
			} else {
				row = append(row, discord.Button(fmt.Sprintf("实时 #%d", id), fmt.Sprintf("acc_live:%d", id), 2))
			}
			shown++
			if len(row) == 2 {
				comps = append(comps, discord.ActionRow(row...))
				row = nil
			}
		}
		if len(row) > 0 {
			comps = append(comps, discord.ActionRow(row...))
		}
	}
	if b.canOpsRead(userID) {
		if len(issueIDs) > 0 {
			// Scope subsequent bulk heal to problem accounts when user taps 一键修复.
			b.setBrowseView(userID, "problem", 0)
		}
		row := []discord.Component{
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("异常汇总", "mgr_browse:problem:0", 2),
			discord.Button("运维", "ops_menu", 2),
		}
		if b.canOpsWrite(userID) && len(issueIDs) > 0 {
			// Prefer heal over crowding a 4th button when space is tight.
			row = []discord.Component{
				discord.Button("异常账号", "ops_badacc:error:0", 2),
				discord.Button("异常汇总", "mgr_browse:problem:0", 2),
				discord.Button("一键修复", "mgr_bulk_heal", 1),
				discord.Button("运维", "ops_menu", 2),
			}
		}
		comps = append(comps, discord.ActionRow(row...))
	}
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return bld.String(), comps
}

func (b *Bot) showDashboard(ctx context.Context, userID int64) string {
	text, _ := b.showDashboardView(ctx, userID)
	return text
}

func (b *Bot) showDashboardView(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	st, err := cli.GetDashboardStats(ctx)
	if err != nil {
		return "看板失败: " + err.Error(), opsComponents()
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
	trafficDropped := false
	highErrorRate := false
	var rtErr float64
	if rt, err := cli.GetRealtimeDashboard(ctx); err == nil && rt != nil {
		fmt.Fprintf(&bld, "实时: 活跃 `%v` · RPM `%.2f` · 错误率 `%.2f%%`\n",
			rt.ActiveRequests, rt.RequestsPerMinute, rt.ErrorRate)
		if rt.ErrorRate >= 5 || (rt.ActiveRequests > 0 && rt.ErrorRate >= 2) {
			highErrorRate = true
			rtErr = rt.ErrorRate
		}
	}
	if traf, err := cli.GetRealtimeTraffic(ctx, "5min"); err == nil && traf != nil {
		qps, tps, peak := traf.CurrentQPS(), traf.CurrentTPS(), traf.PeakQPS()
		line := fmt.Sprintf("流量(%s): QPS `%.3f`", traf.WindowLabel(), qps)
		if tps > 0 {
			line += fmt.Sprintf(" · TPS `%.3f`", tps)
		}
		if peak > 0 {
			line += fmt.Sprintf(" · 峰值QPS `%.3f`", peak)
		}
		bld.WriteString(line + "\n")
		if browse.TrafficIsDropped(qps, peak) {
			trafficDropped = true
			fmt.Fprintf(&bld, "⚠ 流量骤降约 `%d%%`（当前 ≤ 峰值 × %.0f%%）\n",
				browse.TrafficDropPercent(qps, peak), browse.TrafficDropRatio*100)
		}
	}
	if highErrorRate {
		fmt.Fprintf(&bld, "⚠ 实时错误率偏高（`%.2f%%`），建议查错误/异常账号。\n", rtErr)
	}
	if trafficDropped || highErrorRate || (st != nil && (st.ErrorAccounts > 0 || st.RatelimitAccounts > 0 || st.OverloadAccounts > 0)) {
		bld.WriteString("\n下方可快速跳转到异常/流量/并发排查。")
	}
	return bld.String(), dashboardComponents(st, trafficDropped || highErrorRate)
}

func dashboardComponents(st *sub2api.DashboardStats, stress ...bool) []discord.Component {
	needStress := len(stress) > 0 && stress[0]
	jump := []discord.Component{}
	if st != nil {
		if st.ErrorAccounts > 0 {
			jump = append(jump, discord.Button(fmt.Sprintf("异常 %v", st.ErrorAccounts), "ops_badacc:error:0", 1))
		}
		if st.RatelimitAccounts > 0 {
			jump = append(jump, discord.Button(fmt.Sprintf("限速 %v", st.RatelimitAccounts), "ops_badacc:rl:0", 2))
		}
		if st.OverloadAccounts > 0 && len(jump) < 3 {
			jump = append(jump, discord.Button(fmt.Sprintf("过载 %v", st.OverloadAccounts), "ops_badacc:ol:0", 2))
		}
	}
	if len(jump) == 0 {
		jump = append(jump, discord.Button("异常账号", "ops_badacc:error:0", 2))
	}
	if len(jump) < 3 {
		jump = append(jump, discord.Button("错误列表", "ops_errors:all:0", 2))
	}
	if len(jump) < 3 {
		jump = append(jump, discord.Button("管理", "mgr_menu", 2))
	}
	rows := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_dash", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
		discord.ActionRow(jump...),
	}
	if needStress {
		rows = append(rows, discord.ActionRow(
			discord.Button("流量", "ops_traf", 2),
			discord.Button("并发", "ops_conc", 2),
			discord.Button("错误", "ops_errors:all:0", 2),
		))
		rows = append(rows, discord.ActionRow(
			discord.Button("可用性", "ops_avail", 2),
			discord.Button("告警", "ops_alerts", 2),
			discord.Button("管理", "mgr_menu", 2),
		))
	} else {
		rows = append(rows, discord.ActionRow(
			discord.Button("可用性", "ops_avail", 2),
			discord.Button("流量", "ops_traf", 2),
			discord.Button("并发", "ops_conc", 2),
		))
	}
	if len(rows) > 5 {
		rows = rows[:5]
	}
	return rows
}

func (b *Bot) showAvailability(ctx context.Context, userID int64) string {
	text, _ := b.showAvailabilityView(ctx, userID)
	return text
}

func (b *Bot) showAvailabilityView(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	av, err := cli.GetAccountAvailability(ctx)
	if err != nil {
		return "可用性失败: " + err.Error(), opsComponents()
	}
	var bld strings.Builder
	bld.WriteString("**账号可用性**\n\n")
	if av == nil {
		return bld.String() + "无数据。", opsViewComponents("ops_avail")
	}
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
		return "**可用性**\n```\n" + truncate(fmt.Sprintf("%+v", av), 900) + "\n```", opsViewComponents("ops_avail")
	}
	sort.Slice(plats, func(i, j int) bool { return plats[i].k < plats[j].k })
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
	if len(bad) > 0 {
		bld.WriteString("\n**异常/不可用账号**\n")
		for i, st := range bad {
			if i >= 8 {
				fmt.Fprintf(&bld, "… 另有 %d 个\n", len(bad)-8)
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
			fmt.Fprintf(&bld, "• #%d %s [%s]\n", st.AccountID, truncate(st.AccountName, 16), strings.Join(flags, ","))
		}
	}
	nErr, nRL, nOL := 0, 0, 0
	for _, st := range bad {
		if st.HasError {
			nErr++
		}
		if st.IsRateLimited {
			nRL++
		}
		if st.IsOverloaded {
			nOL++
		}
	}
	b.setManageBack(userID, "ops_avail")
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_avail", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	// keep account buttons to one row so platform jumps + footer fit Discord 5-row limit
	var row []discord.Component
	for i, st := range bad {
		if i >= 2 || st.AccountID <= 0 {
			break
		}
		row = append(row, discord.Button(fmt.Sprintf("管理 #%d", st.AccountID), fmt.Sprintf("mgr_acc:%d", st.AccountID), 1))
	}
	if len(row) > 0 {
		comps = append(comps, discord.ActionRow(row...))
	}
	type platJump struct {
		key   string
		score int
	}
	var pj []platJump
	for _, p := range plats {
		if strings.HasPrefix(p.k, "g:") {
			continue // group synthetic keys are not account browser platform filters
		}
		score := browse.PlatformProblemScore(p.v.ErrorNum(), p.v.RateLimitNum())
		if score <= 0 {
			continue
		}
		pj = append(pj, platJump{p.k, score})
	}
	sort.Slice(pj, func(i, j int) bool {
		if pj[i].score != pj[j].score {
			return pj[i].score > pj[j].score
		}
		return pj[i].key < pj[j].key
	})
	var platBtns []discord.Component
	for i, p := range pj {
		if i >= 3 {
			break
		}
		label := truncate(p.key, 10)
		platBtns = append(platBtns, discord.Button("🏷 "+label, "mgr_browse:"+browse.Token("plat:"+p.key)+":0", 2))
	}
	if len(platBtns) > 0 {
		comps = append(comps, discord.ActionRow(platBtns...))
	}
	badJumpLabel, badJumpData := "异常账号", "ops_badacc:error:0"
	switch {
	case nErr > 0:
		badJumpLabel, badJumpData = fmt.Sprintf("异常 %d", nErr), "ops_badacc:error:0"
	case nRL > 0:
		badJumpLabel, badJumpData = fmt.Sprintf("限速 %d", nRL), "ops_badacc:rl:0"
	case nOL > 0:
		badJumpLabel, badJumpData = fmt.Sprintf("过载 %d", nOL), "ops_badacc:ol:0"
	}
	footer := []discord.Component{
		discord.Button(badJumpLabel, badJumpData, 2),
		discord.Button("限速", "ops_badacc:rl:0", 2),
		discord.Button("异常汇总", "mgr_browse:problem:0", 2),
	}
	if b.canOpsWrite(userID) && len(bad) > 0 {
		footer = append(footer, discord.Button("修复本页", "av:heal_related", 1))
	} else {
		footer = append(footer, discord.Button("错误", "ops_errors:all:0", 2))
	}
	// Discord max 5 buttons/row
	if len(footer) > 5 {
		footer = footer[:5]
	}
	comps = append(comps, discord.ActionRow(footer...))
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return bld.String(), comps
}

func (b *Bot) showAlerts(ctx context.Context, userID int64) string {
	text, _ := b.showAlertsView(ctx, userID)
	return text
}

func (b *Bot) showAlertsView(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	events, err := cli.ListAlertEvents(ctx, 1, 20)
	if err != nil {
		return "告警失败: " + err.Error(), opsComponents()
	}
	var bld strings.Builder
	bld.WriteString("**内置告警**\n\n")
	if len(events) == 0 {
		return bld.String() + "无事件。", alertsComponents(nil, 0, b.canOpsWrite(userID))
	}
	sort.SliceStable(events, func(i, j int) bool {
		si, sj := strings.ToLower(events[i].Status), strings.ToLower(events[j].Status)
		fi, fj := si == "firing" || si == "open" || si == "active", sj == "firing" || sj == "open" || sj == "active"
		if fi != fj {
			return fi
		}
		return events[i].FiredAt.After(events[j].FiredAt)
	})
	firingN, resolvedN := 0, 0
	for _, ev := range events {
		st := strings.ToLower(ev.Status)
		switch {
		case st == "firing" || st == "open" || st == "active":
			firingN++
		case st == "resolved" || st == "ok" || st == "closed":
			resolvedN++
		}
	}
	fmt.Fprintf(&bld, "汇总: 🔴 触发 `%d` · 🟢 已恢复 `%d` · 共 `%d`\n\n", firingN, resolvedN, len(events))
	for i, e := range events {
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
	accIDs := collectAlertAccountIDs(events)
	b.setManageBack(userID, "ops_alerts")
	return bld.String(), alertsComponents(accIDs, firingN, b.canOpsWrite(userID))
}

func alertsComponents(accIDs []int64, firingN int, canWrite bool) []discord.Component {
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_alerts", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	if len(accIDs) > 0 {
		row := []discord.Component{}
		for i, id := range accIDs {
			if i >= 2 {
				break
			}
			row = append(row, discord.Button(fmt.Sprintf("管理 #%d", id), fmt.Sprintf("mgr_acc:%d", id), 1))
		}
		if len(row) > 0 {
			comps = append(comps, discord.ActionRow(row...))
		}
	}
	if canWrite && (len(accIDs) > 0 || firingN > 0) {
		if len(accIDs) > 0 {
			n := len(accIDs)
			if n > 10 {
				n = 10
			}
			comps = append(comps, discord.ActionRow(
				discord.Button(fmt.Sprintf("修复关联 %d", n), "al:heal_related", 1),
				discord.Button("批量修复", "mgr_bulk_heal", 1),
			))
		} else {
			comps = append(comps, discord.ActionRow(
				discord.Button("批量修复", "mgr_bulk_heal", 1),
				discord.Button("异常汇总", "mgr_browse:problem:0", 2),
			))
		}
	}
	jump := []discord.Component{
		discord.Button("错误", "ops_errors:all:0", 2),
		discord.Button("异常账号", "ops_badacc:error:0", 2),
	}
	if firingN > 0 {
		jump = append(jump, discord.Button("看板", "ops_dash", 2))
	} else {
		jump = append(jump, discord.Button("可用性", "ops_avail", 2))
	}
	comps = append(comps, discord.ActionRow(jump...))
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return comps
}

func (b *Bot) showConcurrency(ctx context.Context, userID int64) string {
	text, _ := b.showConcurrencyView(ctx, userID)
	return text
}

func (b *Bot) showConcurrencyView(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	snap, err := cli.GetConcurrency(ctx)
	if err != nil {
		return "并发失败: " + err.Error(), opsComponents()
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
	sort.Slice(plats, func(i, j int) bool { return plats[i].b.LoadPercentage > plats[j].b.LoadPercentage })
	bld.WriteString("**平台**\n")
	for _, r := range plats {
		fmt.Fprintf(&bld, "• %s: `%d/%d` (%.0f%%) wait=`%d`\n",
			r.name, r.b.CurrentInUse, r.b.MaxCapacity, r.b.LoadPercentage, r.b.WaitingInQueue)
	}
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
	bld.WriteString("\n**高负载分组**\n")
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
		fmt.Fprintf(&bld, "• %s%s: `%d/%d` (%.0f%%) wait=`%d`\n",
			idPart, truncate(r.name, 14), r.b.CurrentInUse, r.b.MaxCapacity, r.b.LoadPercentage, r.b.WaitingInQueue)
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
	hotPlats := browse.HotConcurrencyPlatforms(snap, 3)
	hotGroups := browse.HotConcurrencyGroups(snap, 2)
	hotAccIDs := browse.HotConcurrencyAccounts(snap, 2)
	if len(hotAccIDs) == 0 {
		for _, r := range accs {
			if r.b.AccountID <= 0 {
				continue
			}
			hotAccIDs = append(hotAccIDs, r.b.AccountID)
			if len(hotAccIDs) >= 2 {
				break
			}
		}
	}
	hotN := 0
	for _, r := range plats {
		if browse.IsHotLoad(r.b.LoadPercentage, r.b.WaitingInQueue) {
			hotN++
		}
	}
	for _, r := range groups {
		if browse.IsHotLoad(r.b.LoadPercentage, r.b.WaitingInQueue) {
			hotN++
		}
	}
	for _, r := range accs {
		if browse.IsHotLoad(r.b.LoadPercentage, r.b.WaitingInQueue) {
			hotN++
		}
	}
	if hotN > 0 {
		fmt.Fprintf(&bld, "\n提示: 高负载/排队项约 `%d` 个（≥%.0f%% 或 wait>0）\n", hotN, browse.HotLoadThreshold)
	}
	b.setManageBack(userID, "ops_conc")
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_conc", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	if len(hotAccIDs) > 0 {
		row := []discord.Component{}
		for _, id := range hotAccIDs {
			row = append(row, discord.Button(fmt.Sprintf("管理 #%d", id), fmt.Sprintf("mgr_acc:%d", id), 1))
		}
		comps = append(comps, discord.ActionRow(row...))
	}
	if len(hotPlats) > 0 {
		row := []discord.Component{}
		for _, plat := range hotPlats {
			row = append(row, discord.Button("🏷 "+truncate(plat, 10), "mgr_browse:"+browse.Token("plat:"+plat)+":0", 2))
		}
		comps = append(comps, discord.ActionRow(row...))
	}
	footer := []discord.Component{
		discord.Button("过载账号", "ops_badacc:ol:0", 2),
		discord.Button("看板", "ops_dash", 2),
	}
	if len(hotGroups) > 0 {
		footer = append([]discord.Component{
			discord.Button(fmt.Sprintf("分组 #%d", hotGroups[0]), fmt.Sprintf("mgr_group:%d", hotGroups[0]), 2),
		}, footer...)
	} else {
		footer = append(footer, discord.Button("分组列表", "mgr_groups", 2))
	}
	if len(footer) > 5 {
		footer = footer[:5]
	}
	comps = append(comps, discord.ActionRow(footer...))
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return bld.String(), comps
}

func (b *Bot) showTrafficView(ctx context.Context, userID int64, window string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	window = normalizeTrafficWindow(window)
	traf, err := cli.GetRealtimeTraffic(ctx, window)
	if err != nil {
		return "流量查询失败: " + err.Error(), opsComponents()
	}
	var bld strings.Builder
	bld.WriteString("**实时流量**\n")
	fmt.Fprintf(&bld, "更新: `%s`\n", time.Now().Local().Format("15:04:05"))
	if traf == nil {
		return bld.String() + "无流量数据。", trafficComponents(window)
	}
	if !traf.Enabled {
		bld.WriteString("服务端实时监控未启用（ops realtime-traffic disabled）。\n")
		return bld.String(), trafficComponents(window)
	}
	winLabel := traf.WindowLabel()
	if winLabel == "" {
		winLabel = window
	}
	qps, tps, peak := traf.CurrentQPS(), traf.CurrentTPS(), traf.PeakQPS()
	fmt.Fprintf(&bld, "窗口: `%s`\n", winLabel)
	fmt.Fprintf(&bld, "当前 QPS: `%.3f`\n", qps)
	if tps > 0 {
		fmt.Fprintf(&bld, "当前 TPS: `%.3f`\n", tps)
	}
	if peak > 0 {
		fmt.Fprintf(&bld, "峰值 QPS: `%.3f`\n", peak)
	}
	if !traf.Timestamp.IsZero() {
		fmt.Fprintf(&bld, "采样时间: `%s`\n", traf.Timestamp.Local().Format("01-02 15:04:05"))
	}
	dropped := browse.TrafficIsDropped(qps, peak)
	if dropped {
		fmt.Fprintf(&bld, "\n⚠ **流量骤降** 相对峰值下降约 `%d%%`（当前 ≤ 峰值 × %.0f%%）。建议检查并发/异常账号。\n",
			browse.TrafficDropPercent(qps, peak), browse.TrafficDropRatio*100)
	} else {
		bld.WriteString("\n切换下方窗口可对比不同时间尺度；QPS 骤降可结合看板/异常账号排查。")
	}
	return bld.String(), trafficComponents(window, dropped)
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

func trafficComponents(window string, dropped ...bool) []discord.Component {
	window = normalizeTrafficWindow(window)
	isDrop := len(dropped) > 0 && dropped[0]
	wins := []string{"1min", "5min", "15min", "1h"}
	var row []discord.Component
	for _, w := range wins {
		label := w
		if w == window {
			label = "· " + w
		}
		row = append(row, discord.Button(label, "ops_traf:"+w, 2))
	}
	comps := []discord.Component{
		discord.ActionRow(row...),
		discord.ActionRow(
			discord.Button("刷新", "ops_traf:"+window, 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	if isDrop {
		comps = append(comps,
			discord.ActionRow(
				discord.Button("并发", "ops_conc", 2),
				discord.Button("异常账号", "ops_badacc:error:0", 2),
				discord.Button("错误", "ops_errors:all:0", 2),
			),
			discord.ActionRow(
				discord.Button("可用性", "ops_avail", 2),
				discord.Button("看板", "ops_dash", 2),
			),
		)
	} else {
		comps = append(comps, discord.ActionRow(
			discord.Button("看板", "ops_dash", 2),
			discord.Button("并发", "ops_conc", 2),
			discord.Button("异常账号", "ops_badacc:error:0", 2),
		))
	}
	if len(comps) > 5 {
		comps = comps[:5]
	}
	return comps
}

func (b *Bot) showChannels(ctx context.Context, userID int64) string {
	text, _ := b.showChannelsView(ctx, userID, "all")
	return text
}

func (b *Bot) showChannelsView(ctx context.Context, userID int64, tab string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), b.homeComponents(userID)
	}
	items, err := cli.ListChannelMonitors(ctx)
	if err != nil {
		return "渠道探测失败: " + err.Error(), opsComponents()
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
	sort.SliceStable(filtered, func(i, j int) bool {
		bi := channelIsBad(filtered[i])
		bj := channelIsBad(filtered[j])
		if bi != bj {
			return bi
		}
		return filtered[i].ID < filtered[j].ID
	})

	var bld strings.Builder
	bld.WriteString("**渠道探测**\n")
	fmt.Fprintf(&bld, "汇总: 启用 `%d` · 正常 `%d` · 异常 `%d` · 共 `%d`\n", onN, okN, badN, len(items))
	fmt.Fprintf(&bld, "筛选: `%s` · 本页 `%d`\n点选任务查看详情\n\n", channelTabLabel(tab), len(filtered))

	opts := make([]discord.SelectOpt, 0, min(25, len(filtered)))
	for i, m := range filtered {
		if i >= 12 {
			fmt.Fprintf(&bld, "… 另有 %d 个（可换筛选）\n", len(filtered)-12)
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
		fmt.Fprintf(&bld, "• [%s]%s `#%d` %s\n  %s / %s · %s · `%dms`\n  上次 %s",
			en, flag, m.ID, truncate(m.Name, 18), m.Provider, truncate(m.PrimaryModel, 16),
			m.PrimaryStatus, m.PrimaryLatencyMS, last)
		if m.Availability7d > 0 {
			av := m.Availability7d
			if av <= 1 {
				av *= 100
			}
			fmt.Fprintf(&bld, " · 7d %.1f%%", av)
		}
		bld.WriteString("\n")
		if len(opts) < 25 {
			label := fmt.Sprintf("#%d %s", m.ID, truncate(m.Name, 12))
			if m.Name == "" {
				label = fmt.Sprintf("#%d", m.ID)
			}
			if channelIsBad(m) {
				label = "⚠ " + label
			}
			opts = append(opts, discord.SelectOption(label, fmt.Sprintf("ops_ch:%d", m.ID),
				fmt.Sprintf("%s · %s", m.Provider, m.PrimaryStatus)))
		}
	}
	if len(filtered) == 0 {
		bld.WriteString("无匹配探测任务。")
	}

	comps := []discord.Component{}
	if len(opts) > 0 {
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:ops_ch", "选择渠道任务…", opts...)))
	}
	tabRow := []discord.Component{}
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
		tabRow = append(tabRow, discord.Button(lab, "ops_channels:"+st.val, 2))
	}
	comps = append(comps, discord.ActionRow(tabRow...))
	comps = append(comps, discord.ActionRow(
		discord.Button("刷新", "ops_channels:"+tab, 2),
		discord.Button("« 运维", "ops_menu", 2),
		discord.Button("« 主面板", "home", 2),
	))
	nav2 := []discord.Component{
		discord.Button("看板", "ops_dash", 2),
		discord.Button("可用性", "ops_avail", 2),
		discord.Button("告警", "ops_alerts", 2),
	}
	if badN > 0 {
		nav2 = append(nav2, discord.Button("异常账号", "ops_badacc:error:0", 2))
	}
	comps = append(comps, discord.ActionRow(nav2...))
	return bld.String(), comps
}

func (b *Bot) showChannelDetailView(ctx context.Context, userID, channelID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), b.homeComponents(userID)
	}
	items, err := cli.ListChannelMonitors(ctx)
	if err != nil {
		return "渠道探测失败: " + err.Error(), opsComponents()
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
		return b.showChannelsView(ctx, userID, tab)
	}
	tab := normalizeChannelTab(b.getChannelTab(userID))
	if tab == "" {
		tab = "all"
	}
	b.setManageBack(userID, "ops_channels:"+tab)

	var bld strings.Builder
	fmt.Fprintf(&bld, "**渠道探测 `#%d`**\n\n", m.ID)
	name := m.Name
	if name == "" {
		name = "(未命名)"
	}
	fmt.Fprintf(&bld, "名称: `%s`\n", truncate(name, 40))
	en := "关闭"
	if m.Enabled {
		en = "启用"
	}
	fmt.Fprintf(&bld, "状态: `%s`", en)
	if channelIsBad(*m) {
		bld.WriteString(" · **异常**")
	}
	bld.WriteString("\n")
	fmt.Fprintf(&bld, "提供商: `%s`\n", m.Provider)
	fmt.Fprintf(&bld, "主模型: `%s`\n", truncate(m.PrimaryModel, 48))
	fmt.Fprintf(&bld, "探测结果: `%s` · 延迟 `%d` ms\n", m.PrimaryStatus, m.PrimaryLatencyMS)
	if m.IntervalSeconds > 0 {
		fmt.Fprintf(&bld, "间隔: `%d` s\n", m.IntervalSeconds)
	}
	last := "(无)"
	if m.LastCheckedAt != nil {
		last = m.LastCheckedAt.Local().Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(&bld, "上次检查: `%s`\n", last)
	if m.Availability7d > 0 {
		av := m.Availability7d
		if av <= 1 {
			av *= 100
		}
		fmt.Fprintf(&bld, "7 日可用率: `%.1f%%`\n", av)
	}
	bld.WriteString("\n只读详情；触发/启停需上游 Admin 写接口支持后再开放。")
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", fmt.Sprintf("ops_ch:%d", m.ID), 2),
			discord.Button("« 渠道列表", "ops_channels:"+tab, 2),
		),
	}
	if plat := channelProviderPlatform(m.Provider); plat != "" {
		comps = append(comps, discord.ActionRow(
			discord.Button("🏷 浏览 "+plat, "mgr_browse:"+browse.Token("plat:"+plat)+":0", 2),
			discord.Button("异常账号", "ops_badacc:error:0", 2),
		))
	} else {
		comps = append(comps, discord.ActionRow(
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("看板", "ops_dash", 2),
		))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("看板", "ops_dash", 2),
		discord.Button("« 运维", "ops_menu", 2),
		discord.Button("« 主面板", "home", 2),
	))
	return bld.String(), comps
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

func channelsComponents(badN int) []discord.Component {
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_channels", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
		discord.ActionRow(
			discord.Button("看板", "ops_dash", 2),
			discord.Button("可用性", "ops_avail", 2),
			discord.Button("告警", "ops_alerts", 2),
		),
	}
	if badN > 0 {
		comps = append(comps, discord.ActionRow(
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("错误", "ops_errors:all:0", 2),
		))
	}
	return comps
}

func (b *Bot) showErrors(ctx context.Context, userID int64) (string, []discord.Component) {
	return b.showErrorsView(ctx, userID, "all", 0, "")
}

func (b *Bot) showErrorsView(ctx context.Context, userID int64, kind string, page int, notice string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
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
	bld.WriteString("**近期错误**（优先未解决）\n")
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(errorTabLabel("全部", kind, "all"), "ops_errors:all:0", 2),
			discord.Button(errorTabLabel("上游", kind, "u"), "ops_errors:u:0", 2),
			discord.Button(errorTabLabel("请求", kind, "r"), "ops_errors:r:0", 2),
		),
	}

	var resolveIDs []struct {
		kind      string
		id        int64
		accountID int64
		userID    int64
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
		items := append([]sub2api.OpsError(nil), pageData.Items...)
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
			when := ""
			if !e.CreatedAt.IsZero() {
				when = " · " + e.CreatedAt.Local().Format("01-02 15:04")
			}
			model := e.Model
			if model == "" {
				model = e.RequestedModel
			}
			plat := e.Platform
			if plat == "" {
				plat = "-"
			}
			userHint := ""
			if e.UserID > 0 {
				ul := e.UserEmail
				if ul == "" {
					ul = fmt.Sprintf("user#%d", e.UserID)
				}
				userHint = " · 用户 `" + truncate(ul, 18) + "`"
			}
			fmt.Fprintf(&bld, "• #%d [%s] %d %s%s%s\n  %s · %s\n  %s\n",
				e.ID, e.Severity, e.StatusCode, truncate(name, 14), when, userHint,
				truncate(plat, 12), truncate(model, 18),
				truncate(e.Message, 70))
			resolveIDs = append(resolveIDs, struct {
				kind      string
				id        int64
				accountID int64
				userID    int64
			}{k, e.ID, e.AccountID, e.UserID})
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

	// Discord allows max 5 action rows: tabs + up to 2 error rows + resolve-all + footer.
	// Prefer first 2 unresolved with full shortcuts.
	for i, r := range resolveIDs {
		if i >= 2 {
			break
		}
		row := []discord.Component{}
		if canWrite {
			row = append(row, discord.SuccessButton(fmt.Sprintf("✅ #%d", r.id), fmt.Sprintf("oe:r:%s:%d", r.kind, r.id)))
		}
		if r.accountID > 0 {
			if canWrite {
				row = append(row, discord.Button("修复", fmt.Sprintf("live_act:heal:%d", r.accountID), 1))
			}
			row = append(row,
				discord.Button("实时", fmt.Sprintf("acc_live:%d", r.accountID), 2),
				discord.Button("账号", fmt.Sprintf("mgr_acc:%d", r.accountID), 2),
			)
		} else if r.userID > 0 {
			row = append(row, discord.Button("用户", fmt.Sprintf("mgr_user:%d", r.userID), 2))
		}
		if len(row) > 0 {
			comps = append(comps, discord.ActionRow(row...))
		}
	}
	// If there is a nav row already, drop it when we need room for resolve-all+footer.
	// Rebuild comps to keep: tabs (first), error rows, optional nav, then footer actions.
	// Simpler: always append resolve-all + compact footer (nav merged if present).
	footer := []discord.Component{
		discord.Button("刷新", fmt.Sprintf("ops_errors:%s:%d", kind, page), 2),
		discord.Button("« 运维", "ops_menu", 2),
		discord.Button("« 主面板", "home", 2),
	}
	if canWrite {
		comps = append(comps, discord.ActionRow(
			discord.SuccessButton("全解上游", "oe:resolve_all:u"),
			discord.SuccessButton("全解请求", "oe:resolve_all:r"),
			discord.Button("修复关联", "oe:heal_related", 1),
		))
	}
	comps = append(comps, discord.ActionRow(footer...))
	// Trim to 5 action rows if pagination was inserted earlier.
	if len(comps) > 5 {
		// Keep first (tabs), next 2 error rows if any, then last 2 (resolve-all + footer).
		kept := []discord.Component{comps[0]}
		// collect middle except last 2
		mid := comps[1 : len(comps)-2]
		for _, c := range mid {
			if len(kept) >= 3 {
				break
			}
			kept = append(kept, c)
		}
		kept = append(kept, comps[len(comps)-2], comps[len(comps)-1])
		comps = kept
	}
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
	memKind, memPage := b.getOpsErrorView(userID)
	page := 0
	if memKind == tab {
		page = memPage
	}
	if err := cli.ResolveOpsError(ctx, apiKind, errorID); err != nil {
		return b.showErrorsView(ctx, userID, tab, page, "❌ 标记失败: "+err.Error())
	}
	return b.showErrorsView(ctx, userID, tab, page, fmt.Sprintf("✅ 已标记错误 #%d 为已解决", errorID))
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
	memKind, memPage := b.getOpsErrorView(userID)
	pageNo := 0
	if memKind == tab {
		pageNo = memPage
	}
	if err != nil {
		return b.showErrorsView(ctx, userID, tab, pageNo, "❌ 拉取失败: "+err.Error())
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
		return b.showErrorsView(ctx, userID, tab, pageNo, "✅ 没有未解决的"+label+"错误。")
	}
	return b.showErrorsView(ctx, userID, tab, pageNo,
		fmt.Sprintf("✅ 批量标记%s错误：成功 %d · 失败 %d", label, okN, failN))
}

// collectUnresolvedErrorAccountIDs returns unique account IDs from unresolved ops errors.
func collectUnresolvedErrorAccountIDs(pages ...*sub2api.OpsErrorPage) []int64 {
	seen := map[int64]struct{}{}
	var ids []int64
	for _, page := range pages {
		if page == nil {
			continue
		}
		for _, e := range page.Items {
			if e.Resolved || e.AccountID <= 0 {
				continue
			}
			if _, ok := seen[e.AccountID]; ok {
				continue
			}
			seen[e.AccountID] = struct{}{}
			ids = append(ids, e.AccountID)
		}
	}
	return ids
}

func (b *Bot) healRelatedFromErrors(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 45*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	up, e1 := cli.ListUpstreamErrors(ctx, 1, 20)
	req, e2 := cli.ListRequestErrors(ctx, 1, 20)
	if e1 != nil && e2 != nil {
		return b.showErrorsView(ctx, userID, "all", 0, "❌ 拉取错误失败: "+e1.Error())
	}
	ids := collectUnresolvedErrorAccountIDs(up, req)
	if len(ids) == 0 {
		return b.showErrorsView(ctx, userID, "all", 0, "✅ 当前未解决错误没有关联账号可修复。")
	}
	const maxOps = 10
	if len(ids) > maxOps {
		ids = ids[:maxOps]
	}
	b.setBrowseView(userID, "problem", 0)
	okN, failN := 0, 0
	var fails []string
	for _, id := range ids {
		msg := b.healAccount(ctx, cli, id)
		if strings.HasPrefix(msg, "❌") {
			failN++
			if len(fails) < 5 {
				fails = append(fails, fmt.Sprintf("#%d %s", id, truncate(strings.TrimPrefix(msg, "❌ "), 40)))
			}
		} else {
			okN++
		}
	}
	var bld strings.Builder
	bld.WriteString("**错误关联一键修复结果**\n\n")
	fmt.Fprintf(&bld, "关联账号 `%d` 个\n✅ 成功 `%d` · ❌ 失败 `%d`\n", len(ids), okN, failN)
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + f + "\n")
		}
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("错误列表", "ops_errors:all:0", 2),
			discord.Button("批量修复", "mgr_bulk_heal", 1),
		),
		discord.ActionRow(
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	return bld.String(), comps
}

// collectUnavailableAccountIDs returns unique account IDs marked unavailable/error/rl/ol.
func collectUnavailableAccountIDs(statuses []sub2api.AccountRuntimeStatus) []int64 {
	seen := map[int64]struct{}{}
	var ids []int64
	for _, st := range statuses {
		if st.AccountID <= 0 {
			continue
		}
		if !(st.HasError || st.IsRateLimited || st.IsOverloaded || !st.IsAvailable) {
			continue
		}
		if _, ok := seen[st.AccountID]; ok {
			continue
		}
		seen[st.AccountID] = struct{}{}
		ids = append(ids, st.AccountID)
	}
	return ids
}

func (b *Bot) healRelatedFromAvailability(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 45*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	av, err := cli.GetAccountAvailability(ctx)
	if err != nil {
		return "可用性失败: " + err.Error(), opsComponents()
	}
	var list []sub2api.AccountRuntimeStatus
	if av != nil {
		for _, st := range av.Account {
			list = append(list, st)
		}
	}
	ids := collectUnavailableAccountIDs(list)
	if len(ids) == 0 {
		return b.showAvailabilityView(ctx, userID)
	}
	const maxOps = 10
	if len(ids) > maxOps {
		ids = ids[:maxOps]
	}
	b.setBrowseView(userID, "problem", 0)
	okN, failN := 0, 0
	var fails []string
	for _, id := range ids {
		msg := b.healAccount(ctx, cli, id)
		if strings.HasPrefix(msg, "❌") {
			failN++
			if len(fails) < 5 {
				fails = append(fails, fmt.Sprintf("#%d %s", id, truncate(strings.TrimPrefix(msg, "❌ "), 40)))
			}
		} else {
			okN++
		}
	}
	var bld strings.Builder
	bld.WriteString("**可用性关联一键修复结果**\n\n")
	fmt.Fprintf(&bld, "关联账号 `%d` 个\n✅ 成功 `%d` · ❌ 失败 `%d`\n", len(ids), okN, failN)
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + f + "\n")
		}
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("可用性", "ops_avail", 2),
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("批量修复", "mgr_bulk_heal", 1),
		),
		discord.ActionRow(
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	return bld.String(), comps
}

func (b *Bot) showBadAccounts(ctx context.Context, userID int64) (string, []discord.Component) {
	return b.showBadAccountsView(ctx, userID, "error", 0, "")
}

// showBadAccountsView lists problematic accounts.
// kind: error|rl|ol|unsched|temp|disabled|all; page is 0-based.
// Layout is capped at Discord's 5 action-row limit (tabs + select + bulk + nav).
func (b *Bot) showBadAccountsView(ctx context.Context, userID int64, kind string, page int, notice string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	canWrite := b.canOpsWrite(userID)
	if page < 0 {
		page = 0
	}
	kind = browse.NormalizeBadKind(kind)
	const pageSize = 8
	// Sync browser filter so bulk actions from this tab reuse the same scope.
	b.setBrowseView(userID, browse.StatusFromBadKind(kind), page)
	b.setManageBack(userID, fmt.Sprintf("ops_badacc:%s:%d", kind, page))

	items, total, title, scope, err := browse.LoadBadAccountsPage(ctx, cli, kind, page, pageSize)
	if err != nil {
		return "账号列表失败: " + err.Error(), opsComponents()
	}

	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	fmt.Fprintf(&bld, "**%s**\n范围: `%s` · 第 %d 页 · 共约 %d\n\n", title, scope, page+1, total)
	if len(items) == 0 {
		bld.WriteString("当前无匹配账号。")
	}
	for _, a := range items {
		msg := a.ErrorMessage
		if msg == "" {
			msg = browse.AccountIssueLabel(browse.AccountIssueKind(a))
			if msg == "正常" {
				msg = a.Status
			}
		}
		fmt.Fprintf(&bld, "%s `#%d` %s · `%s` · %s\n  %s\n",
			browse.StatusFlag(a), a.ID, truncate(a.Name, 16),
			strings.Join(browse.StatusDetailParts(a), "/"), schedLabel(a.Schedulable),
			truncate(msg, 60),
		)
	}
	bld.WriteString("\n下拉选择账号进入管理/实时操作。")

	// Row budget (max 5):
	// 1 tabs-A, 2 tabs-B+watch, 3 account select, 4 bulk (write), 5 nav/footer
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(errorTabLabel("error", kind, "error"), "ops_badacc:error:0", 2),
			discord.Button(errorTabLabel("限速", kind, "rl"), "ops_badacc:rl:0", 2),
			discord.Button(errorTabLabel("过载", kind, "ol"), "ops_badacc:ol:0", 2),
		),
	}
	tabB := []discord.Component{
		discord.Button(errorTabLabel("停调度", kind, "unsched"), "ops_badacc:unsched:0", 2),
		discord.Button(errorTabLabel("临时停", kind, "temp"), "ops_badacc:temp:0", 2),
		discord.Button(errorTabLabel("禁用", kind, "disabled"), "ops_badacc:disabled:0", 2),
		discord.Button(errorTabLabel("汇总", kind, "all"), "ops_badacc:all:0", 2),
	}
	if canWrite {
		watchLabel, watchData := "监控 error", "ops_watch:error"
		switch kind {
		case "rl":
			watchLabel, watchData = "监控限速", "ops_watch:rl"
		case "ol":
			watchLabel, watchData = "监控过载", "ops_watch:ol"
		case "unsched":
			watchLabel, watchData = "监控停调度", "ops_watch:unsched"
		case "temp":
			watchLabel, watchData = "监控临时停", "ops_watch:temp"
		case "disabled":
			watchLabel, watchData = "监控禁用", "ops_watch:disabled"
		case "all":
			watchLabel, watchData = "监控本页", "ops_watch:all"
		}
		// Discord max 5 buttons/row: tabs already use 4; append watch when room.
		if len(tabB) < 5 {
			tabB = append(tabB, discord.SuccessButton(watchLabel, watchData))
		}
	}
	comps = append(comps, discord.ActionRow(tabB...))

	if len(items) > 0 {
		opts := make([]discord.SelectOpt, 0, len(items))
		for _, a := range items {
			if len(opts) >= 25 {
				break
			}
			name := a.Name
			if name == "" {
				name = fmt.Sprintf("#%d", a.ID)
			}
			desc := strings.TrimSpace(a.Platform + " · " + a.Status)
			opts = append(opts, discord.SelectOption(
				fmt.Sprintf("#%d %s", a.ID, truncate(name, 18)),
				fmt.Sprintf("mgr_acc:%d", a.ID),
				truncate(desc, 50),
			))
		}
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:badacc", "选择账号管理…", opts...)))
	}

	if canWrite {
		switch kind {
		case "rl", "ol":
			comps = append(comps, discord.ActionRow(
				discord.Button("批量清限速", "mgr_bulk_clear_rl", 2),
				discord.Button("一键修复", "mgr_bulk_heal", 1),
			))
		case "unsched":
			comps = append(comps, discord.ActionRow(
				discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
			))
		case "temp":
			comps = append(comps, discord.ActionRow(
				discord.Button("清临时停", "mgr_bulk_clear_temp", 2),
				discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
			))
		case "disabled":
			comps = append(comps, discord.ActionRow(
				discord.SuccessButton("批量启用", "mgr_bulk_enable"),
			))
		default:
			comps = append(comps, discord.ActionRow(
				discord.DangerButton("批量清错", "mgr_bulk_clear"),
				discord.Button("批量恢复", "mgr_bulk_recover", 2),
				discord.Button("一键修复", "mgr_bulk_heal", 1),
			))
		}
	}

	nav := []discord.Component{
		discord.Button("刷新", fmt.Sprintf("ops_badacc:%s:%d", kind, page), 2),
	}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("ops_badacc:%s:%d", kind, page-1), 2))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("ops_badacc:%s:%d", kind, page+1), 2))
	}
	nav = append(nav,
		discord.Button("« 运维", "ops_menu", 2),
		discord.Button("« 主面板", "home", 2),
	)
	// Discord max 5 buttons per row
	if len(nav) > 5 {
		nav = nav[:5]
	}
	comps = append(comps, discord.ActionRow(nav...))

	if len(comps) > 5 {
		comps = comps[:5]
	}
	return bld.String(), comps
}

// watchErrorAccounts is a convenience wrapper for status=error bulk watch.
func (b *Bot) watchErrorAccounts(ctx context.Context, userID int64) (string, []discord.Component) {
	return b.watchAccountsByScope(ctx, userID, "error")
}

// watchAccountsByScope bulk-adds accounts from a bad-account scope into the watch list.
func (b *Bot) watchAccountsByScope(ctx context.Context, userID int64, scope string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 20*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	scope = browse.NormalizeBadKind(scope)
	items, total, title, _, err := browse.LoadBadAccountsPage(ctx, cli, scope, 0, 50)
	if err != nil {
		return "拉取失败: " + err.Error(), opsComponents()
	}
	added, skipped := 0, 0
	for _, a := range items {
		msg := b.addAccount(ctx, userID, strconv.FormatInt(a.ID, 10))
		if strings.Contains(msg, "已在监控") || strings.Contains(msg, "已在列表") {
			skipped++
			continue
		}
		if strings.HasPrefix(msg, "✅") {
			added++
			continue
		}
	}
	p, _ := b.users.Get(userID)
	watchN := 0
	if p != nil {
		watchN = len(p.Accounts)
	}
	notice := fmt.Sprintf("✅ %s：已添加 %d 个到监控（跳过已存在 %d · 本页/扫描 %d · 共约 %d）\n当前监控列表共 %d 个账号",
		title, added, skipped, len(items), total, watchN)
	return b.showBadAccountsView(ctx, userID, scope, 0, notice)
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
		switch {
		case status == "problem" || status == "error" || status == "rate_limited" || status == "overload" || status == "unsched" || status == "temp" || status == "disabled":
			bld.WriteString("\n当前筛选下没有问题账号，可切换「全部」或刷新。")
		case strings.HasPrefix(status, "search:"):
			bld.WriteString("\n未命中搜索，可换关键词或清除搜索。")
		case strings.HasPrefix(status, "plat:"):
			bld.WriteString("\n该平台筛选下无账号，可换平台或看全部。")
		}
	}
	for _, a := range items {
		fmt.Fprintf(&bld, "%s `#%d` %s · `%s` · sched=%v\n",
			browse.StatusFlag(a), a.ID, truncate(a.Name, 16),
			strings.Join(browse.StatusDetailParts(a), "/"), a.Schedulable)
	}

	token := browse.Token(status)
	// Discord max 5 rows: status filters | special | select | bulk | nav
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(filterBtn("全部", status, "all"), "mgr_browse:all:0", 2),
			discord.Button(filterBtn("error", status, "error"), "mgr_browse:error:0", 2),
			discord.Button(filterBtn("汇总", status, "problem"), "mgr_browse:problem:0", 2),
			discord.Button(filterBtn("限速", status, "rate_limited"), "mgr_browse:rate_limited:0", 2),
			discord.Button(filterBtn("停调度", status, "unsched"), "mgr_browse:unsched:0", 2),
		),
		discord.ActionRow(
			discord.Button(filterBtn("过载", status, "overload"), "mgr_browse:overload:0", 2),
			discord.Button(filterBtn("禁用", status, "disabled"), "mgr_browse:disabled:0", 2),
			discord.Button(filterBtn("临时停", status, "temp"), "mgr_browse:temp:0", 2),
			discord.Button("异常", "ops_badacc:error:0", 2),
			discord.Button("搜索", "mgr_search", 2),
		),
	}
	if len(items) > 0 {
		opts := make([]discord.SelectOpt, 0, len(items))
		for _, a := range items {
			if len(opts) >= 25 {
				break
			}
			name := a.Name
			if name == "" {
				name = fmt.Sprintf("#%d", a.ID)
			}
			opts = append(opts, discord.SelectOption(
				fmt.Sprintf("#%d %s", a.ID, truncate(name, 18)),
				fmt.Sprintf("mgr_acc:%d", a.ID),
				fmt.Sprintf("%s/%s", a.Platform, a.Status),
			))
		}
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:mgr_acc", "选择账号管理…", opts...)))
	}

	if b.canOpsWrite(userID) {
		switch {
		case status == "error" || strings.HasPrefix(status, "search:"):
			comps = append(comps, discord.ActionRow(
				discord.DangerButton("批量清错", "mgr_bulk_clear"),
				discord.Button("一键修复", "mgr_bulk_heal", 1),
				discord.SuccessButton("一键监控 error", "ops_watch_errors"),
			))
		case status == "rate_limited" || status == "overload":
			comps = append(comps, discord.ActionRow(
				discord.Button("批量清限速", "mgr_bulk_clear_rl", 2),
				discord.Button("一键修复", "mgr_bulk_heal", 1),
			))
		case status == "unsched":
			comps = append(comps, discord.ActionRow(
				discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
			))
		case status == "problem":
			comps = append(comps, discord.ActionRow(
				discord.DangerButton("批量清错", "mgr_bulk_clear"),
				discord.Button("批量恢复", "mgr_bulk_recover", 2),
				discord.Button("一键修复", "mgr_bulk_heal", 1),
			))
		case status == "disabled":
			comps = append(comps, discord.ActionRow(
				discord.SuccessButton("批量启用", "mgr_bulk_enable"),
			))
		case status == "temp":
			comps = append(comps, discord.ActionRow(
				discord.Button("清临时停", "mgr_bulk_clear_temp", 2),
				discord.Button("批量开调度", "mgr_bulk_sched_on", 2),
			))
		}
	}

	nav := []discord.Component{
		discord.Button("刷新", fmt.Sprintf("mgr_browse:%s:%d", token, page), 2),
	}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("mgr_browse:%s:%d", token, page-1), 2))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("mgr_browse:%s:%d", token, page+1), 2))
	}
	nav = append(nav,
		discord.Button("« 管理", "mgr_menu", 2),
		discord.Button("« 主面板", "home", 2),
	)
	if len(nav) > 5 {
		nav = nav[:5]
	}
	comps = append(comps, discord.ActionRow(nav...))
	if len(comps) > 5 {
		comps = comps[:5]
	}
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
	issue := browse.AccountIssueKind(*acc)
	if issue != browse.IssueOK {
		fmt.Fprintf(&bld, "诊断: **%s**\n", browse.AccountIssueLabel(issue))
	} else {
		fmt.Fprintf(&bld, "诊断: `正常`\n")
	}
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
	if snap, err := cli.GetConcurrency(ctx); err == nil && snap != nil && snap.Enabled {
		for _, v := range snap.Account {
			if v.AccountID == accountID {
				fmt.Fprintf(&bld, "并发: `%d/%d` (%.0f%%) wait=`%d`\n",
					v.CurrentInUse, v.MaxCapacity, v.LoadPercentage, v.WaitingInQueue)
				if browse.IsHotLoad(v.LoadPercentage, v.WaitingInQueue) {
					bld.WriteString("⚠ 该账号并发偏热，可查并发视图或考虑临时停调度。\n")
				}
				break
			}
		}
	}
	{
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		var items []sub2api.OpsError
		if page, err := cli.ListUpstreamErrors(cctx, 1, 20); err == nil && page != nil {
			items = append(items, page.Items...)
		}
		if page, err := cli.ListRequestErrors(cctx, 1, 20); err == nil && page != nil {
			items = append(items, page.Items...)
		}
		cancel()
		if n := browse.CountAccountOpsErrors(items, accountID); n > 0 {
			fmt.Fprintf(&bld, "近期未解决错误: 约 `%d` 条（见错误列表）\n", n)
		}
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

	thMap := map[string]float64{}
	if p, ok := b.users.Get(userID); ok {
		ths := p.Thresholds
		if len(ths) == 0 {
			ths = b.defaults
		}
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
	src := "passive"
	force := false
	if p, ok := b.users.Get(userID); ok {
		src = p.EffectiveSource()
		force = strings.EqualFold(src, "active")
	}
	if usage, err := cli.GetAccountUsage(ctx, accountID, src, force); err == nil && usage != nil {
		sum, hit := usage.CompactUsageSummary(thMap, 5)
		if sum == "" {
			sum = "(无窗口)"
		}
		mark := ""
		if hit {
			mark = " ⚠"
		}
		forceLabel := "缓存"
		if force {
			forceLabel = "强制"
		}
		fmt.Fprintf(&bld, "\n**用量** (`%s`/`%s`): `%s`%s\n", src, forceLabel, sum, mark)
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
	backLabel, backData := b.manageBackLabel(userID)
	var comps []discord.Component
	if b.canOpsWrite(userID) {
		kind := browse.AccountIssueKind(*acc)
		// Row budget max 5: triage + secondary + common + live/back
		switch kind {
		case browse.IssueError:
			comps = append(comps, discord.ActionRow(
				discord.Button("一键修复", fmt.Sprintf("mgr_act:heal:%d", accountID), 1),
				discord.Button("清错误", fmt.Sprintf("mgr_act:clear_err:%d", accountID), 2),
				discord.Button("恢复", fmt.Sprintf("mgr_act:recover:%d", accountID), 2),
				discord.Button(schedBtn, schedData, 1),
			))
		case browse.IssueRL, browse.IssueOverload:
			comps = append(comps, discord.ActionRow(
				discord.Button("清限速", fmt.Sprintf("mgr_act:clear_rl:%d", accountID), 2),
				discord.Button("一键修复", fmt.Sprintf("mgr_act:heal:%d", accountID), 1),
				discord.Button(schedBtn, schedData, 1),
				discord.Button("清错误", fmt.Sprintf("mgr_act:clear_err:%d", accountID), 2),
			))
		case browse.IssueUnsched:
			comps = append(comps, discord.ActionRow(
				discord.Button("开调度", fmt.Sprintf("mgr_act:sched:%d", accountID), 1),
				discord.Button("一键修复", fmt.Sprintf("mgr_act:heal:%d", accountID), 1),
				discord.Button("清临时停", fmt.Sprintf("mgr_act:clear_temp:%d", accountID), 2),
				discord.Button("临时停", fmt.Sprintf("mgr_act:temp_menu:%d", accountID), 2),
			))
		case browse.IssueTemp:
			comps = append(comps, discord.ActionRow(
				discord.Button("清临时停", fmt.Sprintf("mgr_act:clear_temp:%d", accountID), 1),
				discord.Button("开调度", fmt.Sprintf("mgr_act:sched:%d", accountID), 1),
				discord.Button("再设临时停", fmt.Sprintf("mgr_act:temp_menu:%d", accountID), 2),
				discord.Button("一键修复", fmt.Sprintf("mgr_act:heal:%d", accountID), 1),
			))
		case browse.IssueDisabled:
			comps = append(comps, discord.ActionRow(
				discord.Button("启用", fmt.Sprintf("mgr_act:enable:%d", accountID), 1),
				discord.Button("测试", fmt.Sprintf("mgr_act:test:%d", accountID), 2),
				discord.Button(schedBtn, schedData, 2),
				discord.Button("刷新", fmt.Sprintf("mgr_act:refresh:%d", accountID), 2),
			))
		default:
			comps = append(comps, discord.ActionRow(
				discord.Button(schedBtn, schedData, 1),
				discord.Button(watchBtn, watchData, 2),
				discord.Button(statusBtn, statusData, 2),
			))
		}
		// Secondary row
		if kind != browse.IssueOK && kind != browse.IssueDisabled {
			comps = append(comps, discord.ActionRow(
				discord.Button(watchBtn, watchData, 2),
				discord.Button(statusBtn, statusData, 2),
				discord.Button("测试", fmt.Sprintf("mgr_act:test:%d", accountID), 2),
				discord.Button("刷新", fmt.Sprintf("mgr_act:refresh:%d", accountID), 2),
			))
		} else if kind == browse.IssueOK {
			comps = append(comps, discord.ActionRow(
				discord.Button("一键修复", fmt.Sprintf("mgr_act:heal:%d", accountID), 1),
				discord.Button("清错误", fmt.Sprintf("mgr_act:clear_err:%d", accountID), 2),
				discord.Button("清限速", fmt.Sprintf("mgr_act:clear_rl:%d", accountID), 2),
			))
			comps = append(comps, discord.ActionRow(
				discord.Button("恢复", fmt.Sprintf("mgr_act:recover:%d", accountID), 2),
				discord.Button("刷新", fmt.Sprintf("mgr_act:refresh:%d", accountID), 2),
				discord.Button("测试", fmt.Sprintf("mgr_act:test:%d", accountID), 2),
			))
		}
		// Temp / quota row (when not already primary)
		if kind != browse.IssueTemp && kind != browse.IssueUnsched {
			comps = append(comps, discord.ActionRow(
				discord.Button("临时停调度", fmt.Sprintf("mgr_act:temp_menu:%d", accountID), 2),
				discord.Button("清临时停", fmt.Sprintf("mgr_act:clear_temp:%d", accountID), 2),
				discord.Button("重置额度", fmt.Sprintf("mgr_act:confirm_reset_quota:%d", accountID), 4),
			))
		} else {
			comps = append(comps, discord.ActionRow(
				discord.Button("重置额度", fmt.Sprintf("mgr_act:confirm_reset_quota:%d", accountID), 4),
				discord.Button("清限速", fmt.Sprintf("mgr_act:clear_rl:%d", accountID), 2),
			))
		}
		// Footer
		comps = append(comps, discord.ActionRow(
			discord.Button("实时用量", fmt.Sprintf("acc_live:%d", accountID), 1),
			discord.Button(backLabel, backData, 2),
			discord.Button("« 管理", "mgr_menu", 2),
		))
	} else {
		comps = []discord.Component{
			discord.ActionRow(
				discord.Button("实时用量", fmt.Sprintf("acc_live:%d", accountID), 1),
				discord.Button(backLabel, backData, 2),
				discord.Button("« 浏览", "mgr_menu", 2),
			),
		}
	}
	if len(comps) > 5 {
		comps = comps[:5]
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
	if sec := browse.ParseDurationLabel(label); sec > 0 {
		return sec
	}
	if sec, _, err := browse.ParseFlexibleDuration(label); err == nil {
		return sec
	}
	return 0
}

// parseFlexibleDuration accepts 30m / 2h / 1d / bare minutes (1..10080).
func parseFlexibleDuration(raw string) (sec int64, label string, err error) {
	return browse.ParseFlexibleDuration(raw)
}

func tempMenuComponents(accountID int64) []discord.Component {
	return []discord.Component{
		discord.ActionRow(
			discord.Button("15m", fmt.Sprintf("mgr_act:temp:15m:%d", accountID), 2),
			discord.Button("1h", fmt.Sprintf("mgr_act:temp:1h:%d", accountID), 2),
			discord.Button("6h", fmt.Sprintf("mgr_act:temp:6h:%d", accountID), 2),
			discord.Button("24h", fmt.Sprintf("mgr_act:temp:24h:%d", accountID), 2),
		),
		discord.ActionRow(
			discord.Button("自定义", fmt.Sprintf("mgr_act:temp_custom:%d", accountID), 1),
			discord.Button("取消", fmt.Sprintf("mgr_acc:%d", accountID), 2),
		),
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

	var liveAcc *sub2api.Account
	if acc, err := cli.GetAccount(ctx, accountID); err == nil && acc != nil {
		liveAcc = acc
		fmt.Fprintf(&bld, "名称: `%s`\n平台/类型: `%s` / `%s`\n状态: `%s` · 可调度: `%v`\n",
			acc.Name, acc.Platform, acc.Type, acc.Status, acc.Schedulable)
		if kind := browse.AccountIssueKind(*acc); kind != browse.IssueOK {
			fmt.Fprintf(&bld, "诊断: **%s**\n", browse.AccountIssueLabel(kind))
		}
		if acc.ErrorMessage != "" {
			fmt.Fprintf(&bld, "错误: %s\n", truncate(acc.ErrorMessage, 120))
		}
		if acc.RateLimitResetAt != nil {
			fmt.Fprintf(&bld, "限速重置: `%s`\n", acc.RateLimitResetAt.Local().Format(time.RFC3339))
		}
		if acc.OverloadUntil != nil {
			fmt.Fprintf(&bld, "过载至: `%s`\n", acc.OverloadUntil.Local().Format(time.RFC3339))
		}
		if acc.TempUnschedulableUntil != nil {
			fmt.Fprintf(&bld, "临时停调度至: `%s`\n", acc.TempUnschedulableUntil.Local().Format(time.RFC3339))
		}
	} else if err != nil {
		fmt.Fprintf(&bld, "账号详情失败: %s\n", err.Error())
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
	fmt.Fprintf(&bld, "\n用量数据源: `%s` · `%s`\n", src, forceLabel)
	thMap := map[string]float64{}
	if p != nil {
		ths := p.Thresholds
		if len(ths) == 0 {
			ths = b.defaults
		}
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
		fmt.Fprintf(&bld, "用量: %s\n", err.Error())
	} else {
		sum, hit := usage.CompactUsageSummary(thMap, 5)
		if sum == "" {
			sum = "(无数据)"
		}
		mark := ""
		if hit {
			mark = " ⚠"
		}
		fmt.Fprintf(&bld, "用量: `%s`%s\n", sum, mark)
		if usage.Error != "" {
			fmt.Fprintf(&bld, "提示: %s\n", truncate(usage.Error, 80))
		}
	}
	if today, err := cli.GetAccountTodayStats(ctx, accountID); err == nil && today != nil {
		fmt.Fprintf(&bld, "\n今日: req=`%d` tok=`%d` cost=`%.4f`\n", today.Requests, today.Tokens, today.Cost)
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
				fmt.Fprintf(&bld, "并发: `%d/%d` (%.0f%%) wait=`%d`\n",
					v.CurrentInUse, v.MaxCapacity, v.LoadPercentage, v.WaitingInQueue)
				break
			}
		}
	}

	// Discord max 5 action rows: refresh + up to 2 triage rows + manage + back.
	comps := []discord.Component{
		discord.ActionRow(discord.Button("刷新", fmt.Sprintf("acc_live:%d", accountID), 2)),
	}
	if b.canOpsWrite(userID) {
		kind := browse.IssueOK
		if liveAcc != nil {
			kind = browse.AccountIssueKind(*liveAcc)
		}
		plan := browse.LiveActionPlanFor(kind)
		// Prefer primary triage rows; keep room for manage + back (max 5 total).
		maxActionRows := 2
		if !plan.AppendRefreshWithManage {
			// healthy path has 3 action rows in plan but Discord budget is tight:
			// keep heal/clear + clear_rl/recover, fold sched into manage row with refresh.
			maxActionRows = 2
		}
		for i, row := range plan.Rows {
			if i >= maxActionRows {
				break
			}
			btns := make([]discord.Component, 0, len(row))
			for _, act := range row {
				style := 2
				if act == browse.LiveHeal || act == browse.LiveSched || act == browse.LiveEnable {
					style = 1
				}
				btns = append(btns, discord.Button(
					browse.LiveActionLabel(act),
					fmt.Sprintf("live_act:%s:%d", act, accountID),
					style,
				))
			}
			if len(btns) > 0 {
				comps = append(comps, discord.ActionRow(btns...))
			}
		}
		manageRow := []discord.Component{
			discord.Button("完整管理", fmt.Sprintf("mgr_acc:%d", accountID), 2),
		}
		if plan.AppendRefreshWithManage {
			// put refresh credentials next to manage (TG parity)
			manageRow = append([]discord.Component{
				discord.Button(browse.LiveActionLabel(browse.LiveRefresh), fmt.Sprintf("live_act:%s:%d", browse.LiveRefresh, accountID), 2),
			}, manageRow...)
		} else {
			// healthy: offer open-sched if not already in shown rows
			manageRow = append([]discord.Component{
				discord.Button(browse.LiveActionLabel(browse.LiveSched), fmt.Sprintf("live_act:%s:%d", browse.LiveSched, accountID), 1),
				discord.Button(browse.LiveActionLabel(browse.LiveRefresh), fmt.Sprintf("live_act:%s:%d", browse.LiveRefresh, accountID), 2),
			}, manageRow...)
		}
		comps = append(comps, discord.ActionRow(manageRow...))
	} else if b.canOpsRead(userID) {
		comps = append(comps, discord.ActionRow(
			discord.Button("账号详情", fmt.Sprintf("mgr_acc:%d", accountID), 2),
		))
	}
	backLabel, backData := b.manageBackLabel(userID)
	// Avoid duplicate "完整管理" — only back + watched-accounts list.
	comps = append(comps, discord.ActionRow(
		discord.Button(backLabel, backData, 2),
		discord.Button("« 监控", "cfg_acc", 2),
	))
	if len(comps) > 5 {
		comps = comps[:5]
	}
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
	case "sched":
		if _, err := cli.SetSchedulable(ctx, accountID, true); err != nil {
			notice = "❌ 开启调度失败: " + err.Error()
		} else {
			notice = "✅ 已开启调度"
		}
	case "refresh":
		if _, err := cli.RefreshAccount(ctx, accountID); err != nil {
			notice = "❌ 刷新凭据失败: " + err.Error()
		} else {
			notice = "✅ 已刷新账号/凭据"
		}
	case "clear_temp":
		if err := cli.ClearTempUnschedulable(ctx, accountID); err != nil {
			notice = "❌ 清除临时停失败: " + err.Error()
		} else {
			notice = "✅ 已清除临时停调度"
		}
	case "enable":
		if _, err := cli.SetAccountStatus(ctx, accountID, "active"); err != nil {
			notice = "❌ 启用失败: " + err.Error()
		} else {
			notice = "✅ 已启用账号"
		}
	default:
		notice = "未知操作"
	}
	return b.showAccountLive(ctx, userID, accountID, notice)
}

// bulkNavComponents builds cancel/back components after bulk empty/result.
func (b *Bot) bulkNavComponents(userID int64) []discord.Component {
	row := []discord.Component{}
	if back := b.getManageBack(userID); strings.HasPrefix(back, "ops_badacc") {
		row = append(row, discord.Button("« 异常列表", back, 2))
	} else if st, pg := b.getBrowseView(userID); st != "" && st != "all" {
		row = append(row, discord.Button("« 浏览", fmt.Sprintf("mgr_browse:%s:%d", browse.Token(st), pg), 2))
	}
	row = append(row,
		discord.Button("« 管理", "mgr_menu", 2),
		discord.Button("« 运维", "ops_menu", 2),
		discord.Button("« 主面板", "home", 2),
	)
	if len(row) > 5 {
		row = row[:5]
	}
	return []discord.Component{discord.ActionRow(row...)}
}

// loadDiscordBulkTargets selects accounts for bulk ops (scoped to browser filter when compatible).
func (b *Bot) loadDiscordBulkTargets(ctx context.Context, cli *sub2api.Client, userID int64, action string, maxOps int) ([]sub2api.Account, int64, string, error) {
	status, _ := b.getBrowseView(userID)
	return browse.LoadBulkTargetsScoped(ctx, cli, action, maxOps, status)
}

func (b *Bot) bulkActionPrompt(ctx context.Context, userID int64, action, title, confirmID string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 15*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	const maxOps = 20
	items, total, scope, err := b.loadDiscordBulkTargets(ctx, cli, userID, action, maxOps)
	if err != nil {
		return "拉取账号失败: " + err.Error(), manageComponents()
	}
	if len(items) == 0 {
		return "✅ 当前没有可处理的账号（" + scope + "）。", b.bulkNavComponents(userID)
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
	row := []discord.Component{
		discord.DangerButton(fmt.Sprintf("确认处理 %d 个", n), confirmID),
		discord.Button("取消", "mgr_menu", 2),
	}
	// Prefer return to the view that launched bulk (badacc / browse).
	if back := b.getManageBack(userID); strings.HasPrefix(back, "ops_badacc") {
		row = append(row, discord.Button("« 异常列表", back, 2))
	} else if st, pg := b.getBrowseView(userID); st != "" && st != "all" {
		tok := browse.Token(st)
		row = append(row, discord.Button("« 浏览", fmt.Sprintf("mgr_browse:%s:%d", tok, pg), 2))
	}
	if len(row) > 5 {
		row = row[:5]
	}
	comps := []discord.Component{discord.ActionRow(row...)}
	return bld.String(), comps
}

func (b *Bot) bulkAccountActionExecute(ctx context.Context, userID int64, action string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 45*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	const maxOps = 20
	items, total, scope, err := b.loadDiscordBulkTargets(ctx, cli, userID, action, maxOps)
	if err != nil {
		return "拉取失败: " + err.Error(), manageComponents()
	}
	if len(items) == 0 {
		return "✅ 当前没有可处理的账号（" + scope + "）", b.bulkNavComponents(userID)
	}
	n := len(items)
	if n > maxOps {
		n = maxOps
	}
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
		case "enable":
			_, opErr = cli.SetAccountStatus(ctx, a.ID, "active")
		case "clear_temp":
			opErr = cli.ClearTempUnschedulable(ctx, a.ID)
		default:
			opErr = fmt.Errorf("unknown action %s", action)
		}
		if opErr != nil {
			failN++
			if len(fails) < 5 {
				fails = append(fails, fmt.Sprintf("#%d %s", a.ID, truncate(opErr.Error(), 40)))
			}
			if len(failIDs) < 3 {
				failIDs = append(failIDs, a.ID)
			}
		} else {
			okN++
		}
	}
	title := map[string]string{
		"clear_err":  "批量清错",
		"recover":    "批量恢复",
		"sched_on":   "批量开调度",
		"clear_rl":   "批量清限速",
		"heal":       "批量一键修复",
		"enable":     "批量启用",
		"clear_temp": "批量清临时停",
	}[action]
	if title == "" {
		title = "批量操作"
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "**%s 结果**\n\n范围: %s · 约 %d 个（本次 %d）\n✅ 成功 %d · ❌ 失败 %d\n", title, scope, total, n, okN, failN)
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + f + "\n")
		}
	}
	comps := []discord.Component{}
	if len(failIDs) > 0 {
		row := []discord.Component{}
		for _, id := range failIDs {
			row = append(row, discord.Button(fmt.Sprintf("管理 #%d", id), fmt.Sprintf("mgr_acc:%d", id), 1))
		}
		comps = append(comps, discord.ActionRow(row...))
	}
	badBtn := discord.Button("异常账号", "ops_badacc:error:0", 2)
	if back := b.getManageBack(userID); strings.HasPrefix(back, "ops_badacc") {
		badBtn = discord.Button("« 异常列表", back, 2)
	}
	browseBtn := discord.Button("浏览", "mgr_browse:error:0", 2)
	if st, pg := b.getBrowseView(userID); st != "" && st != "all" {
		browseBtn = discord.Button("« 浏览", fmt.Sprintf("mgr_browse:%s:%d", browse.Token(st), pg), 2)
	}
	comps = append(comps,
		discord.ActionRow(
			badBtn,
			browseBtn,
			discord.Button("« 管理", "mgr_menu", 2),
		),
		discord.ActionRow(
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	)
	return bld.String(), comps
}

// healAccount best-effort: clear error, clear rate limit, recover, enable schedule.
func (b *Bot) healAccount(ctx context.Context, cli *sub2api.Client, accountID int64) string {
	return browse.HealAccount(ctx, cli, accountID, truncate)
}

func (b *Bot) showUsersView(ctx context.Context, userID int64, page int, search string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	if page < 0 {
		page = 0
	}
	search = strings.TrimSpace(search)
	b.setUserSearch(userID, search)
	status := b.getUserStatus(userID)
	const pageSize = 8
	items, total, err := cli.ListUsersEx(ctx, page+1, pageSize, sub2api.UserListFilter{Search: search, Status: status})
	if err != nil {
		return "用户列表失败: " + err.Error(), manageComponents()
	}
	var bld strings.Builder
	bld.WriteString("**实例用户**（Sub2API）\n")
	if search != "" {
		fmt.Fprintf(&bld, "搜索: `%s`\n", truncate(search, 40))
	}
	if status != "" {
		fmt.Fprintf(&bld, "状态筛选: `%s`\n", status)
	}
	fmt.Fprintf(&bld, "第 %d 页 · 共 `%d`\n点选用户查看详情\n\n", page+1, total)
	opts := make([]discord.SelectOpt, 0, len(items))
	for _, u := range items {
		name := u.Username
		if name == "" {
			name = u.Email
		}
		if name == "" {
			name = strconv.FormatInt(u.ID, 10)
		}
		fmt.Fprintf(&bld, "• `#%d` %s [%s] `%s`",
			u.ID, truncate(name, 16), u.Role, u.Status)
		if u.CurrentConcurrency > 0 || u.Concurrency > 0 {
			fmt.Fprintf(&bld, " · 并发 `%d/%d`", u.CurrentConcurrency, u.Concurrency)
			if browse.UserIsHot(u.CurrentConcurrency, u.Concurrency) {
				bld.WriteString(" 🔥")
			}
		}
		if u.Balance != 0 {
			fmt.Fprintf(&bld, " · 余额 `%.2f`", u.Balance)
		}
		bld.WriteString("\n")
		opts = append(opts, discord.SelectOption(
			fmt.Sprintf("#%d %s", u.ID, truncate(name, 12)),
			fmt.Sprintf("mgr_user:%d", u.ID),
			fmt.Sprintf("%s · %s", u.Role, u.Status),
		))
	}
	if len(items) == 0 {
		bld.WriteString("无用户。")
	}
	comps := []discord.Component{}
	if len(opts) > 0 {
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:mgr_user", "选择用户…", opts...)))
	}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", usersCallback(page-1, search), 2))
	}
	if int64((page+1)*pageSize) < total {
		nav = append(nav, discord.Button("下页 »", usersCallback(page+1, search), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
	}
	stRow := []discord.Component{}
	for _, st := range []struct {
		label, val string
	}{
		{"全部", ""},
		{"active", "active"},
		{"disabled", "disabled"},
	} {
		lab := st.label
		if st.val == status || (st.val == "" && status == "") {
			lab = "· " + lab
		}
		cb := "mgr_ust"
		if st.val != "" {
			cb = "mgr_ust:" + st.val
		}
		stRow = append(stRow, discord.Button(lab, cb, 2))
	}
	comps = append(comps, discord.ActionRow(stRow...))
	action := []discord.Component{discord.Button("🔎 搜索", "mgr_user_search", 2)}
	if search != "" {
		action = append(action, discord.Button("清除搜索", "mgr_user_clear", 2))
	}
	comps = append(comps, discord.ActionRow(action...))
	comps = append(comps, discord.ActionRow(
		discord.Button("分组", "mgr_groups", 2),
		discord.Button("浏览账号", "mgr_browse:all:0", 2),
		discord.Button("« 管理", "mgr_menu", 2),
	))
	return bld.String(), comps
}

func (b *Bot) showUserDetailView(ctx context.Context, userID, targetID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	u, err := cli.GetUser(ctx, targetID)
	if err != nil {
		return "用户详情失败: " + err.Error(), manageComponents()
	}
	b.setManageBack(userID, usersCallback(0, b.getUserSearch(userID)))
	var bld strings.Builder
	fmt.Fprintf(&bld, "**实例用户 `#%d`**\n\n", u.ID)
	name := u.Username
	if name == "" {
		name = "(无用户名)"
	}
	fmt.Fprintf(&bld, "用户名: `%s`\n", truncate(name, 40))
	email := u.Email
	if email == "" {
		email = "(无邮箱)"
	}
	fmt.Fprintf(&bld, "邮箱: `%s`\n", truncate(email, 48))
	fmt.Fprintf(&bld, "角色: `%s` · 状态: `%s`\n", u.Role, u.Status)
	fmt.Fprintf(&bld, "余额: `%.2f`", u.Balance)
	if u.FrozenBalance != 0 {
		fmt.Fprintf(&bld, " · 冻结 `%.2f`", u.FrozenBalance)
	}
	bld.WriteString("\n")
	pct := browse.UserConcurrencyPct(u.CurrentConcurrency, u.Concurrency)
	fmt.Fprintf(&bld, "并发: `%d/%d`", u.CurrentConcurrency, u.Concurrency)
	if u.Concurrency > 0 {
		fmt.Fprintf(&bld, " (%.0f%%)", pct)
	} else if u.Concurrency <= 0 && u.CurrentConcurrency > 0 {
		bld.WriteString(" (配额未限制)")
	}
	if u.RPMLimit > 0 {
		fmt.Fprintf(&bld, " · RPM `%d`", u.RPMLimit)
	}
	bld.WriteString("\n")
	if strings.TrimSpace(u.Notes) != "" {
		fmt.Fprintf(&bld, "备注: %s\n", truncate(u.Notes, 120))
	}

	hot := browse.UserIsHot(u.CurrentConcurrency, u.Concurrency)
	statusBad := browse.UserStatusNeedsAttention(u.Status)
	userErrN := 0
	var errAccIDs []int64
	{
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		var items []sub2api.OpsError
		if page, err := cli.ListUpstreamErrors(cctx, 1, 30); err == nil && page != nil {
			items = append(items, page.Items...)
		}
		if page, err := cli.ListRequestErrors(cctx, 1, 30); err == nil && page != nil {
			items = append(items, page.Items...)
		}
		cancel()
		userErrN, errAccIDs = browse.CountUserOpsErrors(items, u.ID)
	}
	if hot || statusBad || userErrN > 0 {
		bld.WriteString("\n")
		if statusBad {
			fmt.Fprintf(&bld, "⚠ 用户状态 `%s`，可能无法正常调用。\n", u.Status)
		}
		if hot {
			fmt.Fprintf(&bld, "⚠ 用户并发偏热 (%.0f%%)，可查全局并发/流量。\n", pct)
		}
		if userErrN > 0 {
			fmt.Fprintf(&bld, "⚠ 近期未解决错误约 `%d` 条", userErrN)
			if len(errAccIDs) > 0 {
				fmt.Fprintf(&bld, " · 关联账号 `%d`", len(errAccIDs))
			}
			bld.WriteString("\n")
		}
	}
	bld.WriteString("\n只读详情；写操作需上游 Admin API 支持后再开放。")
	back := usersCallback(0, b.getUserSearch(userID))
	rows := []discord.Component{
		discord.ActionRow(
			discord.Button("🔄 刷新", fmt.Sprintf("mgr_user:%d", u.ID), 2),
			discord.Button("并发", "ops_conc", 2),
			discord.Button("流量", "ops_traf", 2),
		),
		discord.ActionRow(
			discord.Button("错误", "ops_errors:all:0", 2),
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("异常汇总", "mgr_browse:problem:0", 2),
		),
		discord.ActionRow(
			discord.Button("« 用户列表", back, 2),
			discord.Button("分组", "mgr_groups", 2),
			discord.Button("« 管理", "mgr_menu", 2),
		),
	}
	if len(rows) > 5 {
		rows = rows[:5]
	}
	return bld.String(), rows
}

func (b *Bot) showGroupsView(ctx context.Context, userID int64, page int, search string) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	if page < 0 {
		page = 0
	}
	search = strings.TrimSpace(search)
	b.setGroupSearch(userID, search)
	platform := b.getGroupPlatform(userID)
	const pageSize = 8
	items, total, err := cli.ListGroupsEx(ctx, page+1, pageSize, sub2api.GroupListFilter{Search: search, Platform: platform})
	if err != nil {
		return "分组列表失败: " + err.Error(), manageComponents()
	}
	var bld strings.Builder
	bld.WriteString("**分组列表**（Sub2API）\n")
	if search != "" {
		fmt.Fprintf(&bld, "搜索: `%s`\n", truncate(search, 40))
	}
	if platform != "" {
		fmt.Fprintf(&bld, "平台: `%s`\n", platform)
	}
	fmt.Fprintf(&bld, "第 %d 页 · 共 `%d`\n点选分组查看详情\n\n", page+1, total)
	opts := make([]discord.SelectOpt, 0, len(items))
	for _, g := range items {
		excl := ""
		if g.IsExclusive {
			excl = " · 独占"
		}
		fmt.Fprintf(&bld, "• `#%d` %s [`%s`/`%s`] ×`%.2f`%s\n",
			g.ID, truncate(g.Name, 20), g.Platform, g.Status, g.RateMultiplier, excl)
		if g.Description != "" {
			fmt.Fprintf(&bld, "  %s\n", truncate(g.Description, 60))
		}
		label := g.Name
		if label == "" {
			label = strconv.FormatInt(g.ID, 10)
		}
		opts = append(opts, discord.SelectOption(
			fmt.Sprintf("#%d %s", g.ID, truncate(label, 12)),
			fmt.Sprintf("mgr_group:%d", g.ID),
			fmt.Sprintf("%s · %s", g.Platform, g.Status),
		))
	}
	if len(items) == 0 {
		bld.WriteString("无分组。")
	}
	comps := []discord.Component{}
	if len(opts) > 0 {
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:mgr_group", "选择分组…", opts...)))
	}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", groupsCallback(page-1, search), 2))
	}
	if int64((page+1)*pageSize) < total {
		nav = append(nav, discord.Button("下页 »", groupsCallback(page+1, search), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
	}
	platRow := []discord.Component{}
	for _, st := range []struct {
		label, val string
	}{
		{"全部", ""},
		{"openai", "openai"},
		{"anthropic", "anthropic"},
		{"gemini", "gemini"},
	} {
		lab := st.label
		if st.val == platform || (st.val == "" && platform == "") {
			lab = "· " + lab
		}
		cb := "mgr_gplat"
		if st.val != "" {
			cb = "mgr_gplat:" + st.val
		}
		platRow = append(platRow, discord.Button(lab, cb, 2))
	}
	comps = append(comps, discord.ActionRow(platRow...))
	action := []discord.Component{discord.Button("🔎 搜索", "mgr_group_search", 2)}
	if search != "" {
		action = append(action, discord.Button("清除搜索", "mgr_group_clear", 2))
	}
	comps = append(comps, discord.ActionRow(action...))
	comps = append(comps, discord.ActionRow(
		discord.Button("实例用户", "mgr_users", 2),
		discord.Button("浏览账号", "mgr_browse:all:0", 2),
		discord.Button("« 管理", "mgr_menu", 2),
	))
	return bld.String(), comps
}

func (b *Bot) showGroupDetailView(ctx context.Context, userID, groupID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	g, err := cli.GetGroup(ctx, groupID)
	if err != nil {
		return "分组详情失败: " + err.Error(), manageComponents()
	}
	b.setManageBack(userID, groupsCallback(0, b.getGroupSearch(userID)))
	var bld strings.Builder
	fmt.Fprintf(&bld, "**分组 `#%d`**\n\n", g.ID)
	name := g.Name
	if name == "" {
		name = "(未命名)"
	}
	fmt.Fprintf(&bld, "名称: `%s`\n", truncate(name, 40))
	fmt.Fprintf(&bld, "平台: `%s` · 状态: `%s`\n", g.Platform, g.Status)
	fmt.Fprintf(&bld, "倍率: `%.2f`", g.RateMultiplier)
	if g.IsExclusive {
		bld.WriteString(" · 独占")
	}
	bld.WriteString("\n")
	if strings.TrimSpace(g.Description) != "" {
		fmt.Fprintf(&bld, "描述: %s\n", truncate(g.Description, 160))
	}
	plat := strings.ToLower(strings.TrimSpace(g.Platform))
	hot := false
	if snap, err := cli.GetConcurrency(ctx); err == nil && snap != nil && snap.Enabled {
		var bucket *sub2api.ConcurrencyBucket
		for _, v := range snap.Group {
			if v.GroupID == g.ID {
				mm := v
				bucket = &mm
				break
			}
		}
		if bucket != nil {
			fmt.Fprintf(&bld, "并发: `%d/%d` (%.0f%%) wait=`%d`\n",
				bucket.CurrentInUse, bucket.MaxCapacity, bucket.LoadPercentage, bucket.WaitingInQueue)
			hot = browse.IsHotLoad(bucket.LoadPercentage, bucket.WaitingInQueue)
		} else if plat != "" {
			for k, v := range snap.Platform {
				name := strings.ToLower(k)
				if v.Platform != "" {
					name = strings.ToLower(v.Platform)
				}
				if name == plat {
					fmt.Fprintf(&bld, "平台并发(%s): `%d/%d` (%.0f%%) wait=`%d`\n",
						plat, v.CurrentInUse, v.MaxCapacity, v.LoadPercentage, v.WaitingInQueue)
					hot = browse.IsHotLoad(v.LoadPercentage, v.WaitingInQueue)
					break
				}
			}
		}
	}
	if av, err := cli.GetAccountAvailability(ctx); err == nil && av != nil && av.Enabled && plat != "" {
		for k, bucket := range av.Platform {
			if strings.EqualFold(k, plat) || strings.EqualFold(bucket.Platform, plat) {
				tot := bucket.TotalNum()
				avn := bucket.AvailableNum()
				rate := 0.0
				if tot > 0 {
					rate = float64(avn) / float64(tot) * 100
				}
				fmt.Fprintf(&bld, "可用性(%s): `%d/%d` (%.0f%%) err=`%d` rl=`%d`\n",
					plat, avn, tot, rate, bucket.ErrorNum(), bucket.RateLimitNum())
				if bucket.ErrorNum() > 0 || bucket.RateLimitNum() > 0 || rate < 80 {
					hot = true
				}
				break
			}
		}
	}
	if hot {
		bld.WriteString("\n⚠ 该分组/平台当前偏热或可用性偏低，可下方快速跳转。\n")
	}
	bld.WriteString("\n只读详情（分组本身无写接口）。")
	back := groupsCallback(0, b.getGroupSearch(userID))
	rows := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", fmt.Sprintf("mgr_group:%d", g.ID), 2),
			discord.Button("并发", "ops_conc", 2),
			discord.Button("« 分组", back, 2),
		),
	}
	if plat != "" {
		tok := browse.Token("plat:" + plat)
		b.setBrowseView(userID, "plat:"+plat, 0)
		rows = append(rows, discord.ActionRow(
			discord.Button("浏览 "+truncate(plat, 10), fmt.Sprintf("mgr_browse:%s:0", tok), 2),
			discord.Button("异常账号", "ops_badacc:error:0", 2),
		))
	} else {
		rows = append(rows, discord.ActionRow(
			discord.Button("浏览账号", "mgr_browse:all:0", 2),
			discord.Button("异常账号", "ops_badacc:error:0", 2),
		))
	}
	if b.canOpsWrite(userID) {
		rows = append(rows, discord.ActionRow(
			discord.Button("批量修复", "mgr_bulk_heal", 1),
			discord.Button("异常汇总", "mgr_browse:problem:0", 2),
		))
	} else {
		rows = append(rows, discord.ActionRow(
			discord.Button("异常汇总", "mgr_browse:problem:0", 2),
			discord.Button("可用性", "ops_avail", 2),
		))
	}
	rows = append(rows, discord.ActionRow(
		discord.Button("实例用户", "mgr_users", 2),
		discord.Button("« 管理", "mgr_menu", 2),
		discord.Button("« 主面板", "home", 2),
	))
	if len(rows) > 5 {
		rows = rows[:5]
	}
	return bld.String(), rows
}

func (b *Bot) showPanelUsers(userID int64, page int, notice string) (string, []discord.Component) {
	if page < 0 {
		page = 0
	}
	const pageSize = 8
	all := b.users.ListAll()
	sort.Slice(all, func(i, j int) bool {
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
	bld.WriteString("**面板用户**（Bot 侧，非 Sub2API 用户）\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共 %d\n\n", page+1, total)
	if len(pageItems) == 0 {
		bld.WriteString("暂无面板用户。")
	}
	opts := make([]discord.SelectOpt, 0, len(pageItems))
	for _, p := range pageItems {
		role := p.EffectiveRole()
		if role == "" {
			if b.isAdmin(p.UserID()) {
				role = "admin*"
			} else if b.isViewer(p.UserID()) {
				role = "viewer*"
			} else {
				role = "user*"
			}
		}
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
		fmt.Fprintf(&bld, "• `%d` %s [%s/%s]\n  %s · 监控%s · 账号%d\n",
			p.UserID(), truncate(name, 14), role, p.EffectivePlatform(), conn, mon, len(p.Accounts))
		opts = append(opts, discord.SelectOption(
			fmt.Sprintf("#%d %s", p.UserID(), truncate(name, 12)),
			fmt.Sprintf("pnl_user:%d", p.UserID()),
			fmt.Sprintf("%s · %s", role, p.EffectivePlatform()),
		))
	}
	comps := []discord.Component{}
	if len(opts) > 0 {
		comps = append(comps, discord.ActionRow(discord.StringSelect("select:pnl_user", "选择用户…", opts...)))
	}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("pnl_users:%d", page-1), 2))
	}
	if end < total {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("pnl_users:%d", page+1), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("« 管理菜单", "mgr_menu", 2),
		discord.Button("« 主面板", "home", 2),
	))
	return bld.String(), comps
}

func (b *Bot) showPanelUserDetail(adminID, targetID int64, notice string) (string, []discord.Component) {
	p, ok := b.users.Get(targetID)
	if !ok {
		return b.showPanelUsers(adminID, 0, "❌ 用户不存在")
	}
	var bld strings.Builder
	if notice != "" {
		bld.WriteString(notice + "\n\n")
	}
	fmt.Fprintf(&bld, "**面板用户 #%d**\n\n", targetID)
	name := p.DisplayName
	if name == "" {
		name = p.Username
	}
	fmt.Fprintf(&bld, "名称: `%s`\n", truncate(name, 24))
	fmt.Fprintf(&bld, "平台: `%s` · Chat: `%s`\n", p.EffectivePlatform(), p.ChatID)
	roleStored := strings.TrimSpace(p.Role)
	if roleStored == "" {
		roleStored = "(继承配置)"
	}
	fmt.Fprintf(&bld, "存储角色: `%s`\n", roleStored)
	fmt.Fprintf(&bld, "生效角色: `%s`\n", b.roleLabel(targetID))
	base := p.BaseURL
	if base == "" {
		base = "(未设置)"
	}
	fmt.Fprintf(&bld, "Base URL: `%s`\n", truncate(base, 40))
	fmt.Fprintf(&bld, "API Key: `%s`\n", userstore.MaskKey(p.AdminAPIKey))
	mon := "关闭"
	if p.Enabled {
		mon = "开启"
	}
	fmt.Fprintf(&bld, "监控: `%s` · 数据源: `%s` · 账号: `%d`\n", mon, p.EffectiveSource(), len(p.Accounts))
	if targetID == adminID {
		bld.WriteString("\n⚠️ 这是你自己的账号。")
	}
	bld.WriteString("\n\n角色覆盖仅影响本 Bot 面板权限，不改 Sub2API 权限。")

	monBtn := "关闭监控"
	if !p.Enabled {
		monBtn = "开启监控"
	}
	srcBtn := "源→active"
	if p.EffectiveSource() == "active" {
		srcBtn = "源→passive"
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("设为管理员", fmt.Sprintf("pnl_role:admin:%d", targetID), 1),
			discord.Button("设为只读运维", fmt.Sprintf("pnl_role:viewer:%d", targetID), 2),
		),
		discord.ActionRow(
			discord.Button("设为用户", fmt.Sprintf("pnl_role:user:%d", targetID), 2),
			discord.Button("清除角色覆盖", fmt.Sprintf("pnl_role:clear:%d", targetID), 2),
		),
		discord.ActionRow(
			discord.Button(monBtn, fmt.Sprintf("pnl_mon:%d", targetID), 2),
			discord.Button(srcBtn, fmt.Sprintf("pnl_src:%d", targetID), 2),
		),
		discord.ActionRow(
			discord.Button("« 面板用户", "pnl_users", 2),
			discord.Button("« 管理", "mgr_menu", 2),
		),
	}
	return bld.String(), comps
}

func (b *Bot) setPanelUserRole(adminID, targetID int64, role string) (string, []discord.Component) {
	role = strings.ToLower(strings.TrimSpace(role))
	var storeRole string
	switch role {
	case "admin":
		storeRole = userstore.RoleAdmin
	case "viewer", "readonly", "ro":
		storeRole = userstore.RoleViewer
	case "user":
		storeRole = userstore.RoleUser
	case "clear", "inherit", "default", "":
		storeRole = ""
	default:
		return b.showPanelUserDetail(adminID, targetID, "❌ 无效角色")
	}
	if _, err := b.users.Update(targetID, func(p *userstore.Profile) error {
		p.Role = storeRole
		return nil
	}); err != nil {
		return b.showPanelUserDetail(adminID, targetID, "❌ 保存失败: "+err.Error())
	}
	label := storeRole
	if label == "" {
		label = "继承配置"
	}
	return b.showPanelUserDetail(adminID, targetID, "✅ 已更新角色为 `"+label+"`")
}

func (b *Bot) togglePanelUserMonitor(adminID, targetID int64) (string, []discord.Component) {
	var enabled bool
	if _, err := b.users.Update(targetID, func(p *userstore.Profile) error {
		p.Enabled = !p.Enabled
		enabled = p.Enabled
		return nil
	}); err != nil {
		return b.showPanelUserDetail(adminID, targetID, "❌ 切换监控失败: "+err.Error())
	}
	state := "关闭"
	if enabled {
		state = "开启"
	}
	return b.showPanelUserDetail(adminID, targetID, "✅ 监控已`"+state+"`")
}

func (b *Bot) togglePanelUserSource(adminID, targetID int64) (string, []discord.Component) {
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
		return b.showPanelUserDetail(adminID, targetID, "❌ 切换数据源失败: "+err.Error())
	}
	return b.showPanelUserDetail(adminID, targetID, "✅ 数据源已设为 `"+src+"`")
}

// panelExtractAccountIDs mirrors panel.extractAccountIDs for Discord package.
var panelAccountIDRe = regexp.MustCompile(`(?i)(?:account[_\s-]?id|账号\s*(?:id|ID)?|acc(?:ount)?)\s*[#:=\s]\s*(\d{1,12})|(?:^|[^\d])#(\d{1,12})\b`)

func panelExtractAccountIDs(texts ...string) []int64 {
	seen := map[int64]struct{}{}
	var out []int64
	for _, t := range texts {
		for _, m := range panelAccountIDRe.FindAllStringSubmatch(t, -1) {
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
