package discordpanel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/discord"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

// collectAlertAccountIDs extracts account IDs from alert events, preferring firing ones.
func collectAlertAccountIDs(events []sub2api.AlertEvent) []int64 {
	var firing, all []string
	for _, ev := range events {
		blob := strings.Join([]string{ev.DisplayTitle(), ev.DisplayMessage(), ev.MetricType, ev.Name}, " ")
		all = append(all, blob)
		st := strings.ToLower(ev.Status)
		if st == "firing" || st == "open" || st == "active" {
			firing = append(firing, blob)
		}
	}
	ids := panelExtractAccountIDs(firing...)
	if len(ids) == 0 {
		ids = panelExtractAccountIDs(all...)
	}
	return ids
}

func (b *Bot) healRelatedFromAlerts(ctx context.Context, userID int64) (string, []discord.Component) {
	cli, _, err := b.userClient(userID, 45*time.Second)
	if err != nil {
		return "❌ " + err.Error(), opsComponents()
	}
	events, err := cli.ListAlertEvents(ctx, 1, 30)
	if err != nil {
		return "告警失败: " + err.Error(), opsComponents()
	}
	ids := collectAlertAccountIDs(events)
	if len(ids) == 0 {
		return "✅ 当前告警文本中未解析到关联账号。可改用批量一键修复或异常账号列表。", alertsComponents(nil, 0, true)
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
	bld.WriteString("**告警关联一键修复结果**\n\n")
	fmt.Fprintf(&bld, "关联账号 `%d` 个\n✅ 成功 `%d` · ❌ 失败 `%d`\n", len(ids), okN, failN)
	if len(fails) > 0 {
		bld.WriteString("\n失败样例:\n")
		for _, f := range fails {
			bld.WriteString("• " + f + "\n")
		}
	}
	comps := []discord.Component{
		discord.ActionRow(
			discord.Button("告警", "ops_alerts", 2),
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

// channelProviderPlatform maps channel provider labels to account browser platforms.
func channelProviderPlatform(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch {
	case p == "":
		return ""
	case strings.Contains(p, "openai") || p == "gpt" || strings.Contains(p, "chatgpt"):
		return "openai"
	case strings.Contains(p, "anthropic") || strings.Contains(p, "claude"):
		return "anthropic"
	case strings.Contains(p, "gemini") || strings.Contains(p, "google"):
		return "gemini"
	case strings.Contains(p, "grok") || strings.Contains(p, "xai"):
		return "grok"
	case strings.Contains(p, "antigravity"):
		return "antigravity"
	default:
		switch p {
		case "openai", "anthropic", "gemini", "grok", "antigravity":
			return p
		}
		return ""
	}
}
