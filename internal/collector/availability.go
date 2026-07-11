package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

type availabilityCollector struct {
	cfg    *config.Config
	client *sub2api.Client
	engine *alerter.Engine
	logger *slog.Logger
}

func (a *availabilityCollector) Run(ctx context.Context) error {
	av, err := a.client.GetAccountAvailability(ctx)
	if err != nil {
		return err
	}
	if av != nil && !av.Enabled {
		a.logger.Debug("realtime monitoring disabled on server; skip availability")
		return nil
	}

	check := func(scope, key string, bucket sub2api.AvailabilityBucket) error {
		total := bucket.TotalNum()
		avail := bucket.AvailableNum()
		if total == 0 {
			return nil
		}
		ratio := float64(avail) / float64(total)
		fp := fmt.Sprintf("availability:%s:%s", scope, key)
		below := avail < a.cfg.Checks.Availability.MinAvailableAccounts ||
			ratio < a.cfg.Checks.Availability.MinAvailableRatio

		if below {
			return a.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    alerter.SevP1,
				Title:       "账号可用率过低",
				Body: line("范围", scope+":"+key) +
					line("可用/总数", fmt.Sprintf("%d/%d", avail, total)) +
					line("可用率", fmt.Sprintf("%.1f%%", ratio*100)) +
					line("阈值", fmt.Sprintf("count>=%d ratio>=%.0f%%",
						a.cfg.Checks.Availability.MinAvailableAccounts,
						a.cfg.Checks.Availability.MinAvailableRatio*100)),
			})
		}
		if a.engine.Seen(fp) {
			return a.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    alerter.SevP1,
				Title:       "账号可用率已恢复",
				Body:        line("范围", scope+":"+key) + line("可用/总数", fmt.Sprintf("%d/%d", avail, total)),
				Resolved:    true,
			})
		}
		return nil
	}

	for k, b := range av.Platform {
		if err := check("platform", k, b); err != nil {
			return err
		}
	}
	for k, b := range av.Group {
		// group keys may be numeric strings
		key := k
		if _, err := strconv.ParseInt(k, 10, 64); err == nil {
			key = "group#" + k
		}
		if err := check("group", key, b); err != nil {
			return err
		}
	}
	return nil
}
