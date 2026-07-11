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
	"github.com/boa/sub2api-monitor/internal/userstore"
)

// UserUsageCollector polls each panel user's configured accounts and alerts that user.
type UserUsageCollector struct {
	cfg      *config.Config
	users    *userstore.Store
	engine   *alerter.Engine
	logger   *slog.Logger
	defaults []config.UsageThreshold
}

func NewUserUsageCollector(cfg *config.Config, users *userstore.Store, engine *alerter.Engine, logger *slog.Logger) *UserUsageCollector {
	defs := cfg.Checks.AccountUsage.DefaultThresholds
	if len(defs) == 0 {
		defs = []config.UsageThreshold{
			{Window: "five_hour", UtilizationGTE: 80, Severity: "P2"},
			{Window: "seven_day", UtilizationGTE: 90, Severity: "P1"},
		}
	}
	return &UserUsageCollector{cfg: cfg, users: users, engine: engine, logger: logger, defaults: defs}
}

func (c *UserUsageCollector) Interval() time.Duration {
	d := c.cfg.Telegram.Panel.CheckInterval
	if d <= 0 {
		d = 5 * time.Minute
	}
	return d
}

func (c *UserUsageCollector) Run(ctx context.Context) error {
	profiles := c.users.ListEnabled()
	if len(profiles) == 0 {
		c.logger.Debug("user usage: no enabled profiles")
		return nil
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, p := range profiles {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			if err := c.checkProfile(ctx, p); err != nil {
				c.logger.Warn("user usage check", "user", p.TelegramUserID, "err", err)
			}
		}()
	}
	wg.Wait()
	return nil
}

func (c *UserUsageCollector) checkProfile(ctx context.Context, p *userstore.Profile) error {
	cli, err := sub2api.NewClient(config.Sub2APIConfig{
		BaseURL:     p.BaseURL,
		AdminAPIKey: p.AdminAPIKey,
		JWT:         p.JWT,
		Timeout:     20 * time.Second,
	})
	if err != nil {
		return err
	}
	src := p.EffectiveSource()
	thsDefault := p.Thresholds
	if len(thsDefault) == 0 {
		thsDefault = c.defaults
	}
	cooldown := c.cfg.Telegram.Panel.Cooldown
	if cooldown <= 0 {
		cooldown = 2 * time.Hour
	}
	chatIDs := []string{p.ChatID}

	for _, a := range p.Accounts {
		if !a.IsEnabled() {
			continue
		}
		usage, err := cli.GetAccountUsage(ctx, a.ID, src, false)
		if err != nil {
			c.logger.Debug("usage fetch failed", "user", p.TelegramUserID, "account", a.ID, "err", err)
			continue
		}
		label := a.Name
		if label == "" {
			label = fmt.Sprintf("#%d", a.ID)
		}
		ths := a.Thresholds
		if len(ths) == 0 {
			ths = thsDefault
		}
		for _, th := range ths {
			w, ok := usage.Window(th.Window)
			fp := fmt.Sprintf("user:%d:usage:acc:%d:%s:gte:%.0f", p.TelegramUserID, a.ID, strings.ToLower(th.Window), th.UtilizationGTE)
			sev := parseSev(th.Severity, alerter.SevP2)
			if !ok {
				if c.engine.Seen(fp) {
					_ = c.engine.Emit(ctx, alerter.Event{
						Fingerprint: fp,
						Severity:    sev,
						Title:       "账号用量阈值 · 已恢复",
						Body:        line("账号", fmt.Sprintf("#%d %s", a.ID, label)) + line("窗口", th.Window),
						Resolved:    true,
						ChatIDs:     chatIDs,
						Cooldown:    cooldown,
					})
				}
				continue
			}
			resolveBelow := th.UtilizationGTE - 5
			if th.ResolveBelow != nil {
				resolveBelow = *th.ResolveBelow
			}
			body := line("账号", fmt.Sprintf("#%d %s", a.ID, label)) +
				line("窗口", w.Window) +
				line("使用率", fmt.Sprintf("%.1f%%", w.Utilization)) +
				line("阈值", fmt.Sprintf(">= %.1f%%", th.UtilizationGTE))
			if w.ResetsAt != nil {
				body += line("重置于", w.ResetsAt.Local().Format(time.RFC3339))
			}
			if w.Utilization >= th.UtilizationGTE {
				if err := c.engine.Emit(ctx, alerter.Event{
					Fingerprint: fp,
					Severity:    sev,
					Title:       "账号用量达到阈值",
					Body:        body,
					ChatIDs:     chatIDs,
					Cooldown:    cooldown,
				}); err != nil {
					return err
				}
			} else if w.Utilization < resolveBelow && c.engine.Seen(fp) {
				if err := c.engine.Emit(ctx, alerter.Event{
					Fingerprint: fp,
					Severity:    sev,
					Title:       "账号用量阈值 · 已恢复",
					Body:        body,
					Resolved:    true,
					ChatIDs:     chatIDs,
					Cooldown:    cooldown,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// RunLoop ticks until ctx done.
func (c *UserUsageCollector) RunLoop(ctx context.Context) error {
	// initial delay small
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			_ = c.Run(ctx)
			t.Reset(c.Interval())
		}
	}
}
