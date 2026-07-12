package collector

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/notify"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

type Runner struct {
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context) error
}

func Build(cfg *config.Config, client *sub2api.Client, engine *alerter.Engine, logger *slog.Logger) []Runner {
	var out []Runner
	if cfg.Checks.Health.Enabled {
		h := &healthCollector{cfg: cfg, client: client, engine: engine, logger: logger}
		out = append(out, Runner{Name: "health", Interval: cfg.Checks.Health.Interval, Run: h.Run})
	}
	if cfg.Checks.Dashboard.Enabled {
		d := &dashboardCollector{cfg: cfg, client: client, engine: engine, logger: logger}
		out = append(out, Runner{Name: "dashboard", Interval: cfg.Checks.Dashboard.Interval, Run: d.Run})
	}
	if cfg.Checks.Accounts.Enabled {
		a := &accountsCollector{cfg: cfg, client: client, engine: engine, logger: logger}
		out = append(out, Runner{Name: "accounts", Interval: cfg.Checks.Accounts.Interval, Run: a.Run})
	}
	if cfg.Checks.Availability.Enabled {
		a := &availabilityCollector{cfg: cfg, client: client, engine: engine, logger: logger}
		out = append(out, Runner{Name: "availability", Interval: cfg.Checks.Availability.Interval, Run: a.Run})
	}
	if cfg.Checks.OpsAlerts.Enabled {
		o := &opsAlertsCollector{cfg: cfg, client: client, engine: engine, logger: logger}
		out = append(out, Runner{Name: "ops_alerts", Interval: cfg.Checks.OpsAlerts.Interval, Run: o.Run})
	}
	if cfg.Checks.Traffic.Enabled {
		t := &trafficCollector{cfg: cfg, client: client, engine: engine, logger: logger}
		out = append(out, Runner{Name: "traffic", Interval: cfg.Checks.Traffic.Interval, Run: t.Run})
	}
	if cfg.Checks.AccountUsage.Enabled {
		u := &accountUsageCollector{cfg: cfg, client: client, engine: engine, logger: logger}
		out = append(out, Runner{Name: "account_usage", Interval: cfg.Checks.AccountUsage.Interval, Run: u.Run})
	}
	return out
}

func RunAll(ctx context.Context, runners []Runner, poll config.PollConfig, logger *slog.Logger) error {
	var wg sync.WaitGroup
	for _, r := range runners {
		r := r
		if r.Interval <= 0 {
			r.Interval = poll.Interval
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if poll.Jitter > 0 {
				j := time.Duration(rand.Int63n(int64(poll.Jitter)))
				timer := time.NewTimer(j)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			runOnce(ctx, r, logger)
			t := time.NewTicker(r.Interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					runOnce(ctx, r, logger)
				}
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

func runOnce(ctx context.Context, r Runner, logger *slog.Logger) {
	start := time.Now()
	if err := r.Run(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		logger.Warn("collector failed", "name", r.Name, "err", err, "dur", time.Since(start))
		return
	}
	logger.Debug("collector ok", "name", r.Name, "dur", time.Since(start))
}

func line(k, v string) string {
	return notify.LineHTML(k, v)
}
