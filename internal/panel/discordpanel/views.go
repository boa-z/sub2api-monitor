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
• **管理员**：运维写操作 + 账号管理（调度/清错/恢复/批量/一键修复/临时停调度/搜索/面板用户）
• 角色由 admin_user_ids 或 profile.role=admin|viewer|user 控制
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
	if b.canOpsRead(userID) {
		if cli, _, err := b.userClient(userID, 5*time.Second); err == nil && cli != nil {
			if st, err := cli.GetDashboardStats(context.Background()); err == nil && st != nil {
				bld.WriteString("**运维快照**\n")
				fmt.Fprintf(&bld, "正常 `%v` · 异常 `%v` · 限速 `%v` · 过载 `%v`\n",
					st.NormalAccounts, st.ErrorAccounts, st.RatelimitAccounts, st.OverloadAccounts)
				if st.ErrorAccounts > 0 || st.RatelimitAccounts > 0 {
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
	if b.isAdmin(userID) {
		return []discord.Component{
			discord.ActionRow(
				discord.PrimaryButton("状态", "status"),
				discord.Button("运维", "ops_menu", 2),
				discord.Button("管理", "mgr_menu", 2),
			),
			discord.ActionRow(
				discord.Button("看板", "ops_dash", 2),
				discord.Button("异常账号", "ops_badacc:error:0", 2),
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
				discord.Button("运维", "ops_menu", 2),
				discord.Button("看板", "ops_dash", 2),
			),
			discord.ActionRow(
				discord.Button("异常账号", "ops_badacc:error:0", 2),
				discord.Button("账号浏览", "mgr_menu", 2),
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
				if st.ErrorAccounts > 0 || st.RatelimitAccounts > 0 {
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
			}
			if strings.EqualFold(acc.Status, "disabled") {
				flag = "🚫"
				statusBad = true
			}
			fmt.Fprintf(&bld, "%s `#%d` %s · `%s`\n", flag, snap.ID, truncate(name, 14), strings.Join(parts, "/"))
			if flag == "❌" && acc.ErrorMessage != "" {
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
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新状态", "status", 2),
			discord.SuccessButton("立即检查", "check_now"),
			discord.Button("监控账号", "cfg_acc", 2),
		),
	}
	if len(issues) > 0 {
		var row []discord.Component
		for i, id := range issues {
			if i >= 4 {
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
		discord.ActionRow(discord.Button("« 阈值", "cfg_thr", 2)),
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
			bld.WriteString("\n")
		}
	}
	bld.WriteString("基于当前连接的 Admin API：\n• 看板 / 可用性 / 告警 / 并发 / 渠道\n• 错误（分标签分页，解决后保留页码 · 修复/实时）\n• 异常账号（error/限速/停调度/汇总分标签分页 + 管理/实时/修复）")
	return bld.String()
}

func (b *Bot) opsComponents(userID int64) []discord.Component {
	_ = userID
	return opsComponents()
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
			discord.Button("异常账号", "ops_badacc:error:0", 2),
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

func (b *Bot) manageMenuText(ctx context.Context, userID int64) string {
	var bld strings.Builder
	bld.WriteString("**账号管理**\n\n")
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil && st != nil {
			fmt.Fprintf(&bld, "健康: 正常 `%v` · 异常 `%v` · 限速 `%v` · 过载 `%v`\n",
				st.NormalAccounts, st.ErrorAccounts, st.RatelimitAccounts, st.OverloadAccounts)
			if st.ErrorAccounts > 0 || st.RatelimitAccounts > 0 {
				bld.WriteString("建议优先处理异常/限速，或使用批量操作。\n")
			}
			bld.WriteString("\n")
		}
	}
	bld.WriteString("浏览（状态/平台/停调度/限速）、搜索、切换调度、清错/恢复/一键修复、临时停调度、批量处理、实例用户/分组只读列表、面板用户角色（Admin API / Bot 权限）。")
	return bld.String()
}

func manageComponents() []discord.Component {
	return manageComponentsFor(nil, true)
}

func manageComponentsFor(stats *sub2api.DashboardStats, canWrite bool) []discord.Component {
	badLabel := "异常账号"
	clearLabel := "批量清错"
	rlLabel := "批量清限速"
	if stats != nil {
		if stats.ErrorAccounts > 0 {
			badLabel = fmt.Sprintf("异常 %v", stats.ErrorAccounts)
			clearLabel = fmt.Sprintf("清错 %v", stats.ErrorAccounts)
		}
		if stats.RatelimitAccounts > 0 {
			rlLabel = fmt.Sprintf("清限速 %v", stats.RatelimitAccounts)
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
			discord.Button(badLabel, "ops_badacc:error:0", 2),
		),
	}
	if canWrite {
		if stats != nil && (stats.ErrorAccounts > 0 || stats.RatelimitAccounts > 0) {
			comps = append(comps,
				discord.ActionRow(
					discord.Button("一键修复", "mgr_bulk_heal", 1),
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
					discord.Button("一键修复", "mgr_bulk_heal", 1),
					discord.Button("搜索", "mgr_search", 2),
				),
			)
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
	var stats *sub2api.DashboardStats
	if cli, _, err := b.userClient(userID, 6*time.Second); err == nil && cli != nil {
		if st, err := cli.GetDashboardStats(ctx); err == nil {
			stats = st
		}
	}
	return b.manageMenuText(ctx, userID), manageComponentsFor(stats, b.canOpsWrite(userID))
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
		if acc := snap.Account; acc != nil {
			if strings.EqualFold(acc.Status, "error") || acc.ErrorMessage != "" || acc.RateLimitedAt != nil || !acc.Schedulable {
				accBad = true
			}
		} else if snap.AccountErr != nil {
			accBad = true
		}
		if snap.UsageErr != nil {
			fmt.Fprintf(&bld, "• #%d 失败: %s\n", snap.ID, truncate(snap.UsageErr.Error(), 60))
			warnN++
			if len(issueIDs) < 4 {
				issueIDs = append(issueIDs, snap.ID)
			}
			continue
		}
		fmt.Fprintf(&bld, "**#%d %s**\n", snap.ID, name)
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
			for _, w := range usage.Windows() {
				mark := ""
				if sub2api.ThresholdHit(w.Window, w.Utilization, thMap) {
					mark = " ⚠"
					hitThr = true
				}
				fmt.Fprintf(&bld, "  `%s` %.1f%%%s", w.Window, w.Utilization, mark)
				if w.ResetsAt != nil {
					fmt.Fprintf(&bld, " · 重置 %s", w.ResetsAt.Local().Format("01-02 15:04"))
				}
				bld.WriteString("\n")
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
	comps := []discord.Component{
		discord.ActionRow(
			discord.SuccessButton("再检查", "check_now"),
			discord.Button("« 主面板", "home", 2),
		),
	}
	if len(issueIDs) > 0 {
		row := []discord.Component{}
		for _, id := range issueIDs {
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
		comps = append(comps, discord.ActionRow(
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("运维", "ops_menu", 2),
		))
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
	if rt, err := cli.GetRealtimeDashboard(ctx); err == nil && rt != nil {
		fmt.Fprintf(&bld, "实时: 活跃 `%v` · RPM `%.2f` · 错误率 `%.2f%%`\n",
			rt.ActiveRequests, rt.RequestsPerMinute, rt.ErrorRate)
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
	}
	return bld.String(), dashboardComponents(st)
}

func dashboardComponents(st *sub2api.DashboardStats) []discord.Component {
	jump := []discord.Component{}
	if st != nil {
		if st.ErrorAccounts > 0 {
			jump = append(jump, discord.Button(fmt.Sprintf("异常 %v", st.ErrorAccounts), "ops_badacc:error:0", 1))
		}
		if st.RatelimitAccounts > 0 {
			jump = append(jump, discord.Button(fmt.Sprintf("限速 %v", st.RatelimitAccounts), "ops_badacc:rl:0", 2))
		}
		if st.OverloadAccounts > 0 && st.RatelimitAccounts == 0 {
			jump = append(jump, discord.Button(fmt.Sprintf("过载 %v", st.OverloadAccounts), "ops_badacc:rl:0", 2))
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
	return []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_dash", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
		discord.ActionRow(jump...),
		discord.ActionRow(
			discord.Button("可用性", "ops_avail", 2),
			discord.Button("告警", "ops_alerts", 2),
			discord.Button("并发", "ops_conc", 2),
		),
	}
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
	b.setManageBack(userID, "ops_avail")
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_avail", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	var row []discord.Component
	for i, st := range bad {
		if i >= 4 || st.AccountID <= 0 {
			break
		}
		row = append(row, discord.Button(fmt.Sprintf("管理 #%d", st.AccountID), fmt.Sprintf("mgr_acc:%d", st.AccountID), 1))
		if len(row) == 2 {
			comps = append(comps, discord.ActionRow(row...))
			row = nil
		}
	}
	if len(row) > 0 {
		comps = append(comps, discord.ActionRow(row...))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("异常账号", "ops_badacc:error:0", 2),
		discord.Button("限速", "ops_badacc:rl:0", 2),
		discord.Button("错误", "ops_errors:all:0", 2),
	))
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
		return bld.String() + "无事件。", alertsComponents(nil, 0)
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
	accIDs := panelExtractAccountIDs(idTexts...)
	b.setManageBack(userID, "ops_alerts")
	return bld.String(), alertsComponents(accIDs, firingN)
}

func alertsComponents(accIDs []int64, firingN int) []discord.Component {
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
			if i >= 4 {
				break
			}
			row = append(row, discord.Button(fmt.Sprintf("管理 #%d", id), fmt.Sprintf("mgr_acc:%d", id), 1))
			if len(row) == 2 {
				comps = append(comps, discord.ActionRow(row...))
				row = nil
			}
		}
		if len(row) > 0 {
			comps = append(comps, discord.ActionRow(row...))
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
	b.setManageBack(userID, "ops_conc")
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("刷新", "ops_conc", 2),
			discord.Button("« 运维", "ops_menu", 2),
			discord.Button("« 主面板", "home", 2),
		),
	}
	var row []discord.Component
	for i, r := range accs {
		if i >= 4 || r.b.AccountID <= 0 {
			break
		}
		row = append(row, discord.Button(fmt.Sprintf("管理 #%d", r.b.AccountID), fmt.Sprintf("mgr_acc:%d", r.b.AccountID), 1))
		if len(row) == 2 {
			comps = append(comps, discord.ActionRow(row...))
			row = nil
		}
	}
	if len(row) > 0 {
		comps = append(comps, discord.ActionRow(row...))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("异常账号", "ops_badacc:error:0", 2),
		discord.Button("看板", "ops_dash", 2),
		discord.Button("分组列表", "mgr_groups", 2),
	))
	return bld.String(), comps
}

func (b *Bot) showChannels(ctx context.Context, userID int64) string {
	text, _ := b.showChannelsView(ctx, userID)
	return text
}

func (b *Bot) showChannelsView(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	items, err := cli.ListChannelMonitors(ctx)
	if err != nil {
		return "渠道探测失败: " + err.Error(), opsComponents()
	}
	var bld strings.Builder
	bld.WriteString("**渠道探测**\n\n")
	if len(items) == 0 {
		return bld.String() + "无探测任务。", channelsComponents(0)
	}
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
	fmt.Fprintf(&bld, "汇总: 启用 `%d` · 正常 `%d` · 异常状态 `%d` · 共 `%d`\n\n", onN, okN, badN, len(items))
	sort.SliceStable(items, func(i, j int) bool {
		bi, bj := channelIsBad(items[i]), channelIsBad(items[j])
		if bi != bj {
			return bi
		}
		return items[i].ID < items[j].ID
	})
	for i, m := range items {
		if i >= 12 {
			fmt.Fprintf(&bld, "… 另有 %d 个\n", len(items)-12)
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
		fmt.Fprintf(&bld, "• [%s]%s #%d %s\n  %s / %s · %s · `%dms`\n  上次 %s",
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
	}
	return bld.String(), channelsComponents(badN)
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
				kind      string
				id        int64
				accountID int64
			}{k, e.ID, e.AccountID})
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
				discord.Button("查看", fmt.Sprintf("mgr_acc:%d", r.accountID), 2),
			)
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

func (b *Bot) showBadAccounts(ctx context.Context, userID int64) (string, []discord.Component) {
	return b.showBadAccountsView(ctx, userID, "error", 0, "")
}

// showBadAccountsView lists problematic accounts.
// kind: error|rl|unsched|all; page is 0-based.
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
			msg = a.Status
		}
		fmt.Fprintf(&bld, "• #%d %s [%s/%s] %s\n  %s\n",
			a.ID, truncate(a.Name, 16), a.Platform, a.Status, schedLabel(a.Schedulable),
			truncate(msg, 60),
		)
	}

	comps := []discord.Component{
		discord.ActionRow(
			discord.Button(errorTabLabel("error", kind, "error"), "ops_badacc:error:0", 2),
			discord.Button(errorTabLabel("限速", kind, "rl"), "ops_badacc:rl:0", 2),
			discord.Button(errorTabLabel("过载", kind, "ol"), "ops_badacc:ol:0", 2),
		),
		discord.ActionRow(
			discord.Button(errorTabLabel("停调度", kind, "unsched"), "ops_badacc:unsched:0", 2),
			discord.Button(errorTabLabel("汇总", kind, "all"), "ops_badacc:all:0", 2),
		),
	}
	// account actions: manage / live / contextual quick act (up to 5 rows)
	for i, a := range items {
		if i >= 5 {
			break
		}
		row := []discord.Component{
			discord.Button(fmt.Sprintf("查看 #%d", a.ID), fmt.Sprintf("mgr_acc:%d", a.ID), 1),
			discord.Button("实时", fmt.Sprintf("acc_live:%d", a.ID), 2),
		}
		if canWrite {
			quick, quickData := "修复", fmt.Sprintf("live_act:heal:%d", a.ID)
			if kind == "rl" || kind == "ol" {
				quick, quickData = "清限速", fmt.Sprintf("live_act:clear_rl:%d", a.ID)
			} else if kind == "unsched" {
				quick, quickData = "开调度", fmt.Sprintf("live_act:sched:%d", a.ID)
			}
			row = append(row, discord.Button(quick, quickData, 1))
		}
		comps = append(comps, discord.ActionRow(row...))
	}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("ops_badacc:%s:%d", kind, page-1), 2))
	}
	if int64((page+1)*pageSize) < total || len(items) == pageSize {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("ops_badacc:%s:%d", kind, page+1), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
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
		default:
			comps = append(comps, discord.ActionRow(
				discord.DangerButton("批量清错", "mgr_bulk_clear"),
				discord.Button("批量恢复", "mgr_bulk_recover", 2),
				discord.Button("一键修复", "mgr_bulk_heal", 1),
			))
		}
	}
	comps = append(comps,
		discord.ActionRow(
			discord.Button("刷新", fmt.Sprintf("ops_badacc:%s:%d", kind, page), 2),
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
			discord.Button(filterBtn("过载", status, "overload"), "mgr_browse:overload:0", 2),
		),
		discord.ActionRow(
			discord.Button(filterBtn("openai", status, "plat:openai"), "mgr_browse:"+browse.Token("plat:openai")+":0", 2),
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
	if b.canOpsWrite(userID) {
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
	if snap, err := cli.GetConcurrency(ctx); err == nil && snap != nil && snap.Enabled {
		for _, v := range snap.Account {
			if v.AccountID == accountID {
				fmt.Fprintf(&bld, "并发: `%d/%d` (%.0f%%) wait=`%d`\n",
					v.CurrentInUse, v.MaxCapacity, v.LoadPercentage, v.WaitingInQueue)
				break
			}
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
	if usage, err := cli.GetAccountUsage(ctx, accountID, "passive", false); err == nil && usage != nil {
		bld.WriteString("\n**用量快照**\n")
		for _, w := range usage.Windows() {
			mark := ""
			if sub2api.ThresholdHit(w.Window, w.Utilization, thMap) {
				mark = " ⚠"
			}
			line := fmt.Sprintf("• `%s` `%.1f%%`%s", w.Window, w.Utilization, mark)
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
	backLabel, backData := b.manageBackLabel(userID)
	var comps []discord.Component
	if b.canOpsWrite(userID) {
		comps = []discord.Component{
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
				discord.Button(backLabel, backData, 2),
				discord.Button("« 管理", "mgr_menu", 2),
			),
		}
	} else {
		comps = []discord.Component{
			discord.ActionRow(
				discord.Button("实时用量", fmt.Sprintf("acc_live:%d", accountID), 1),
				discord.Button(backLabel, backData, 2),
				discord.Button("« 浏览", "mgr_menu", 2),
			),
		}
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
		wins := usage.Windows()
		if len(wins) == 0 {
			bld.WriteString("用量窗口: (无数据)\n")
		}
		for _, w := range wins {
			mark := ""
			if sub2api.ThresholdHit(w.Window, w.Utilization, thMap) {
				mark = " ⚠"
			}
			reset := ""
			if w.ResetsAt != nil {
				reset = " · " + w.ResetsAt.Local().Format("01-02 15:04")
			}
			fmt.Fprintf(&bld, "• %s: `%.1f%%`%s%s\n", w.Window, w.Utilization, reset, mark)
		}
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
	comps := []discord.Component{
		discord.ActionRow(discord.Button("刷新", fmt.Sprintf("acc_live:%d", accountID), 2)),
	}
	if b.canOpsWrite(userID) {
		comps = append(comps,
			discord.ActionRow(
				discord.Button("一键修复", fmt.Sprintf("live_act:heal:%d", accountID), 1),
				discord.Button("清错误", fmt.Sprintf("live_act:clear_err:%d", accountID), 2),
				discord.Button("清限速", fmt.Sprintf("live_act:clear_rl:%d", accountID), 2),
			),
			discord.ActionRow(
				discord.Button("恢复", fmt.Sprintf("live_act:recover:%d", accountID), 2),
				discord.Button("开调度", fmt.Sprintf("live_act:sched:%d", accountID), 2),
				discord.Button("刷新凭据", fmt.Sprintf("live_act:refresh:%d", accountID), 2),
			),
			discord.ActionRow(
				discord.Button("完整管理", fmt.Sprintf("mgr_acc:%d", accountID), 2),
			),
		)
	} else if b.canOpsRead(userID) {
		comps = append(comps, discord.ActionRow(
			discord.Button("账号详情", fmt.Sprintf("mgr_acc:%d", accountID), 2),
		))
	}
	backLabel, backData := b.manageBackLabel(userID)
	comps = append(comps, discord.ActionRow(
		discord.Button(backLabel, backData, 2),
		discord.Button("完整管理", fmt.Sprintf("mgr_acc:%d", accountID), 2),
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
	comps = append(comps,
		discord.ActionRow(
			discord.Button("异常账号", "ops_badacc:error:0", 2),
			discord.Button("浏览", "mgr_browse:error:0", 2),
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

func (b *Bot) showUsersView(ctx context.Context, userID int64, page int) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	if page < 0 {
		page = 0
	}
	const pageSize = 12
	items, total, err := cli.ListUsers(ctx, page+1, pageSize)
	if err != nil {
		return "用户列表失败: " + err.Error(), manageComponents()
	}
	var bld strings.Builder
	bld.WriteString("**实例用户**（Sub2API）\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共 `%d`\n\n", page+1, total)
	for _, u := range items {
		name := u.Username
		if name == "" {
			name = u.Email
		}
		fmt.Fprintf(&bld, "• `#%d` %s [%s] `%s`",
			u.ID, truncate(name, 16), u.Role, u.Status)
		if u.CurrentConcurrency > 0 || u.Concurrency > 0 {
			fmt.Fprintf(&bld, " · 并发 `%d/%d`", u.CurrentConcurrency, u.Concurrency)
		}
		if u.Balance != 0 {
			fmt.Fprintf(&bld, " · 余额 `%.2f`", u.Balance)
		}
		bld.WriteString("\n")
	}
	if len(items) == 0 {
		bld.WriteString("无用户。")
	}
	comps := []discord.Component{}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("mgr_users:%d", page-1), 2))
	}
	if int64((page+1)*pageSize) < total {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("mgr_users:%d", page+1), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("分组", "mgr_groups", 2),
		discord.Button("浏览账号", "mgr_browse:all:0", 2),
		discord.Button("« 管理", "mgr_menu", 2),
	))
	return bld.String(), comps
}

func (b *Bot) showGroupsView(ctx context.Context, userID int64, page int) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 12*time.Second)
	if err != nil {
		return "❌ " + err.Error(), manageComponents()
	}
	if page < 0 {
		page = 0
	}
	const pageSize = 12
	items, total, err := cli.ListGroups(ctx, page+1, pageSize)
	if err != nil {
		return "分组列表失败: " + err.Error(), manageComponents()
	}
	var bld strings.Builder
	bld.WriteString("**分组列表**（Sub2API）\n")
	fmt.Fprintf(&bld, "第 %d 页 · 共 `%d`\n\n", page+1, total)
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
	}
	if len(items) == 0 {
		bld.WriteString("无分组。")
	}
	comps := []discord.Component{}
	nav := []discord.Component{}
	if page > 0 {
		nav = append(nav, discord.Button("« 上页", fmt.Sprintf("mgr_groups:%d", page-1), 2))
	}
	if int64((page+1)*pageSize) < total {
		nav = append(nav, discord.Button("下页 »", fmt.Sprintf("mgr_groups:%d", page+1), 2))
	}
	if len(nav) > 0 {
		comps = append(comps, discord.ActionRow(nav...))
	}
	comps = append(comps, discord.ActionRow(
		discord.Button("实例用户", "mgr_users", 2),
		discord.Button("浏览账号", "mgr_browse:all:0", 2),
		discord.Button("« 管理", "mgr_menu", 2),
	))
	return bld.String(), comps
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
