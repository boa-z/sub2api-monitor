package collector

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

type trafficCollector struct {
	cfg    *config.Config
	client *sub2api.Client
	engine *alerter.Engine
	logger *slog.Logger
}

func (t *trafficCollector) Run(ctx context.Context) error {
	sum, err := t.client.GetRealtimeTraffic(ctx, t.cfg.Checks.Traffic.Window)
	if err != nil {
		return err
	}
	if sum != nil && !sum.Enabled {
		t.logger.Debug("realtime monitoring disabled; skip traffic")
		return nil
	}
	qps := sum.CurrentQPS()
	fp := "traffic:low_qps"
	if qps < t.cfg.Checks.Traffic.MinQPS {
		return t.engine.Emit(ctx, alerter.Event{
			Fingerprint: fp,
			Severity:    alerter.SevP2,
			Title:       "实时 QPS 过低",
			Body: line("当前 QPS", fmt.Sprintf("%.3f", qps)) +
				line("阈值", fmt.Sprintf("%.3f", t.cfg.Checks.Traffic.MinQPS)) +
				line("窗口", t.cfg.Checks.Traffic.Window),
		})
	}
	if t.engine.Seen(fp) {
		return t.engine.Emit(ctx, alerter.Event{
			Fingerprint: fp,
			Severity:    alerter.SevP2,
			Title:       "实时 QPS 已恢复",
			Body:        line("当前 QPS", fmt.Sprintf("%.3f", qps)),
			Resolved:    true,
		})
	}
	return nil
}
