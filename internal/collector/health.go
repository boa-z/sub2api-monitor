package collector

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

type healthCollector struct {
	cfg      *config.Config
	client   *sub2api.Client
	engine   *alerter.Engine
	logger   *slog.Logger
	failures int
}

func (h *healthCollector) Run(ctx context.Context) error {
	fp := "health:down"
	err := h.client.Health(ctx)
	if err != nil {
		h.failures++
		h.logger.Warn("health check failed", "failures", h.failures, "err", err)
		if h.failures >= h.cfg.Checks.Health.FailThreshold {
			return h.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    alerter.SevP0,
				Title:       "Sub2API 健康检查失败",
				Body:        line("错误", err.Error()) + line("连续失败", fmt.Sprintf("%d", h.failures)),
			})
		}
		return nil
	}
	if h.failures > 0 {
		h.failures = 0
		return h.engine.Emit(ctx, alerter.Event{
			Fingerprint: fp,
			Severity:    alerter.SevP0,
			Title:       "Sub2API 已恢复",
			Resolved:    true,
		})
	}
	h.failures = 0
	return nil
}
