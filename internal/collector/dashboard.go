package collector

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

type dashboardCollector struct {
	cfg    *config.Config
	client *sub2api.Client
	engine *alerter.Engine
	logger *slog.Logger
}

func (d *dashboardCollector) Run(ctx context.Context) error {
	stats, err := d.client.GetDashboardStats(ctx)
	if err != nil {
		return err
	}
	d.logger.Debug("dashboard stats",
		"error_accounts", stats.ErrorAccounts,
		"overload", stats.OverloadAccounts,
		"ratelimit", stats.RatelimitAccounts,
		"total", stats.TotalAccounts,
	)

	type thr struct {
		fp    string
		count int64
		max   int
		sev   alerter.Severity
		title string
	}
	checks := []thr{
		{"dashboard:error_accounts", stats.ErrorAccounts, d.cfg.Checks.Dashboard.MaxErrorAccounts, alerter.SevP1, "异常账号数超阈值"},
		{"dashboard:overload_accounts", stats.OverloadAccounts, d.cfg.Checks.Dashboard.MaxOverloadAccounts, alerter.SevP2, "过载账号数超阈值"},
		{"dashboard:ratelimit_accounts", stats.RatelimitAccounts, d.cfg.Checks.Dashboard.MaxRatelimitAccounts, alerter.SevP3, "限速账号数超阈值"},
	}
	for _, c := range checks {
		if int(c.count) > c.max {
			if err := d.engine.Emit(ctx, alerter.Event{
				Fingerprint: c.fp,
				Severity:    c.sev,
				Title:       c.title,
				Body: line("当前", fmt.Sprintf("%d", c.count)) +
					line("阈值", fmt.Sprintf("%d", c.max)) +
					line("总账号", fmt.Sprintf("%d", stats.TotalAccounts)) +
					line("正常", fmt.Sprintf("%d", stats.NormalAccounts)),
			}); err != nil {
				return err
			}
		} else if d.engine.Seen(c.fp) {
			if err := d.engine.Emit(ctx, alerter.Event{
				Fingerprint: c.fp,
				Severity:    c.sev,
				Title:       c.title + " · 已恢复",
				Body:        line("当前", fmt.Sprintf("%d", c.count)),
				Resolved:    true,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
