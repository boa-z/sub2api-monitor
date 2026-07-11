package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
)

type accountUsageCollector struct {
	cfg    *config.Config
	client *sub2api.Client
	engine *alerter.Engine
	logger *slog.Logger
}

func (c *accountUsageCollector) Run(ctx context.Context) error {
	targets := c.cfg.Checks.AccountUsage.Accounts
	if len(targets) == 0 {
		return nil
	}

	concurrency := c.cfg.Checks.AccountUsage.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, t := range targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			if err := c.checkOne(ctx, t); err != nil {
				c.logger.Warn("account usage check failed", "account_id", t.ID, "err", err)
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func (c *accountUsageCollector) checkOne(ctx context.Context, t config.AccountUsageTarget) error {
	// meta for display
	label := t.Name
	platform := ""
	accType := ""
	if acc, err := c.client.GetAccount(ctx, t.ID); err == nil && acc != nil {
		if label == "" {
			label = acc.Name
		}
		platform = acc.Platform
		accType = acc.Type
	}
	if label == "" {
		label = fmt.Sprintf("#%d", t.ID)
	}

	source := t.Source
	if source == "" {
		source = c.cfg.Checks.AccountUsage.Source
	}
	force := c.cfg.Checks.AccountUsage.ForceActive && strings.EqualFold(source, "active")

	// usage windows
	if !t.DisableUsage {
		usage, err := c.client.GetAccountUsage(ctx, t.ID, source, force)
		if err != nil {
			// soft-fail: emit degraded notice only if we never saw data? skip to avoid noise
			return fmt.Errorf("get usage: %w", err)
		}
		if usage.Error != "" {
			c.logger.Debug("usage degraded", "account_id", t.ID, "error", usage.Error, "code", usage.ErrorCode)
		}
		if err := c.evalThresholds(ctx, t, label, platform, accType, usage); err != nil {
			return err
		}
	}

	// today stats
	today := t.ResolveToday(c.cfg.Checks.AccountUsage.DefaultToday)
	if today != nil && todayActive(today) {
		stats, err := c.client.GetAccountTodayStats(ctx, t.ID)
		if err != nil {
			return fmt.Errorf("get today-stats: %w", err)
		}
		if err := c.evalToday(ctx, t, label, platform, stats, today); err != nil {
			return err
		}
	}
	return nil
}

func todayActive(t *config.TodayThreshold) bool {
	if t == nil {
		return false
	}
	return t.CostGTE > 0 || t.TokensGTE > 0 || t.RequestsGTE > 0
}

func (c *accountUsageCollector) evalThresholds(
	ctx context.Context,
	t config.AccountUsageTarget,
	label, platform, accType string,
	usage *sub2api.UsageInfo,
) error {
	thresholds := t.ResolveThresholds(c.cfg.Checks.AccountUsage.DefaultThresholds)
	cooldown := c.cfg.Checks.AccountUsage.Cooldown

	for _, th := range thresholds {
		w, ok := usage.Window(th.Window)
		if !ok {
			c.logger.Debug("usage window missing", "account_id", t.ID, "window", th.Window)
			// if previously firing and window gone (reset), resolve
			fp := usageFingerprint(t.ID, th.Window, th.UtilizationGTE)
			if c.engine.Seen(fp) {
				if err := c.engine.Emit(ctx, alerter.Event{
					Fingerprint: fp,
					Severity:    parseSev(th.Severity, alerter.SevP2),
					Title:       "账号用量阈值 · 已恢复",
					Body: accountBody(t.ID, label, platform, accType) +
						line("窗口", th.Window) +
						line("说明", "窗口数据不可用（可能已重置）"),
					Resolved: true,
					ChatIDs:  t.ChatIDs,
					Cooldown: cooldown,
				}); err != nil {
					return err
				}
			}
			continue
		}

		fp := usageFingerprint(t.ID, th.Window, th.UtilizationGTE)
		resolveBelow := th.UtilizationGTE - 5
		if th.ResolveBelow != nil {
			resolveBelow = *th.ResolveBelow
		}
		if resolveBelow < 0 {
			resolveBelow = 0
		}

		sev := parseSev(th.Severity, alerter.SevP2)
		body := accountBody(t.ID, label, platform, accType) +
			line("窗口", w.Window) +
			line("使用率", fmt.Sprintf("%.1f%%", w.Utilization)) +
			line("阈值", fmt.Sprintf(">= %.1f%%", th.UtilizationGTE))
		if w.ResetsAt != nil {
			body += line("重置于", w.ResetsAt.Local().Format(time.RFC3339))
		}
		if w.Remaining > 0 {
			body += line("剩余", formatDuration(time.Duration(w.Remaining)*time.Second))
		}
		if w.Stats != nil {
			body += line("窗口统计", fmt.Sprintf("req=%d tokens=%d cost=%.4f",
				w.Stats.Requests, w.Stats.Tokens, w.Stats.Cost))
		}
		if usage.Source != "" {
			body += line("数据源", usage.Source)
		}

		if w.Utilization >= th.UtilizationGTE {
			if err := c.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    sev,
				Title:       "账号用量达到阈值",
				Body:        body,
				ChatIDs:     t.ChatIDs,
				Cooldown:    cooldown,
			}); err != nil {
				return err
			}
			continue
		}

		// resolve with hysteresis
		if w.Utilization < resolveBelow && c.engine.Seen(fp) {
			if err := c.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    sev,
				Title:       "账号用量阈值 · 已恢复",
				Body: accountBody(t.ID, label, platform, accType) +
					line("窗口", w.Window) +
					line("使用率", fmt.Sprintf("%.1f%%", w.Utilization)) +
					line("恢复线", fmt.Sprintf("< %.1f%%", resolveBelow)),
				Resolved: true,
				ChatIDs:  t.ChatIDs,
				Cooldown: cooldown,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *accountUsageCollector) evalToday(
	ctx context.Context,
	t config.AccountUsageTarget,
	label, platform string,
	stats *sub2api.WindowStats,
	th *config.TodayThreshold,
) error {
	if stats == nil || th == nil {
		return nil
	}
	cooldown := c.cfg.Checks.AccountUsage.Cooldown
	sev := parseSev(th.Severity, alerter.SevP2)

	type item struct {
		key   string
		title string
		fire  bool
		value string
		limit string
	}
	items := []item{
		{
			key: "cost", title: "今日费用达到阈值",
			fire:  th.CostGTE > 0 && stats.Cost >= th.CostGTE,
			value: fmt.Sprintf("%.4f", stats.Cost),
			limit: fmt.Sprintf("%.4f", th.CostGTE),
		},
		{
			key: "tokens", title: "今日 Token 达到阈值",
			fire:  th.TokensGTE > 0 && stats.Tokens >= th.TokensGTE,
			value: fmt.Sprintf("%d", stats.Tokens),
			limit: fmt.Sprintf("%d", th.TokensGTE),
		},
		{
			key: "requests", title: "今日请求数达到阈值",
			fire:  th.RequestsGTE > 0 && stats.Requests >= th.RequestsGTE,
			value: fmt.Sprintf("%d", stats.Requests),
			limit: fmt.Sprintf("%d", th.RequestsGTE),
		},
	}

	for _, it := range items {
		// skip disabled metrics
		if it.limit == "0" || it.limit == "0.0000" {
			// still need to know if threshold was 0 — check fields
		}
		switch it.key {
		case "cost":
			if th.CostGTE <= 0 {
				continue
			}
		case "tokens":
			if th.TokensGTE <= 0 {
				continue
			}
		case "requests":
			if th.RequestsGTE <= 0 {
				continue
			}
		}
		fp := fmt.Sprintf("usage:today:%d:%s", t.ID, it.key)
		body := accountBody(t.ID, label, platform, "") +
			line("指标", it.key) +
			line("当前", it.value) +
			line("阈值", ">="+it.limit) +
			line("今日汇总", fmt.Sprintf("req=%d tokens=%d cost=%.4f", stats.Requests, stats.Tokens, stats.Cost))

		if it.fire {
			if err := c.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    sev,
				Title:       it.title,
				Body:        body,
				ChatIDs:     t.ChatIDs,
				Cooldown:    cooldown,
			}); err != nil {
				return err
			}
		} else if c.engine.Seen(fp) {
			if err := c.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    sev,
				Title:       it.title + " · 已恢复",
				Body:        body,
				Resolved:    true,
				ChatIDs:     t.ChatIDs,
				Cooldown:    cooldown,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func usageFingerprint(accountID int64, window string, threshold float64) string {
	return fmt.Sprintf("usage:acc:%d:%s:gte:%.0f", accountID, strings.ToLower(window), threshold)
}

func accountBody(id int64, label, platform, accType string) string {
	b := line("账号", fmt.Sprintf("#%d %s", id, label))
	if platform != "" {
		b += line("平台", platform)
	}
	if accType != "" {
		b += line("类型", accType)
	}
	return b
}

func parseSev(s string, def alerter.Severity) alerter.Severity {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P0":
		return alerter.SevP0
	case "P1":
		return alerter.SevP1
	case "P2":
		return alerter.SevP2
	case "P3":
		return alerter.SevP3
	default:
		return def
	}
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// ensure telegram import used for Escape via line()
var _ = telegram.EscapeHTML
