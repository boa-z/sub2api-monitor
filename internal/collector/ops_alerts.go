package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

// opsAlertsCollector bridges Sub2API built-in ops alert-events to Telegram.
type opsAlertsCollector struct {
	cfg    *config.Config
	client *sub2api.Client
	engine *alerter.Engine
	logger *slog.Logger
}

func (o *opsAlertsCollector) Run(ctx context.Context) error {
	events, err := o.client.ListAlertEvents(ctx, 1, 50)
	if err != nil {
		return err
	}

	sevAllow := map[string]bool{}
	for _, s := range o.cfg.Checks.OpsAlerts.Severities {
		sevAllow[strings.ToUpper(s)] = true
	}
	statusAllow := map[string]bool{}
	for _, s := range o.cfg.Checks.OpsAlerts.Statuses {
		statusAllow[strings.ToLower(s)] = true
	}

	for _, ev := range events {
		sev := strings.ToUpper(strings.TrimSpace(ev.Severity))
		if sev == "" {
			sev = "P2"
		}
		if len(sevAllow) > 0 && !sevAllow[sev] {
			continue
		}
		st := strings.ToLower(strings.TrimSpace(ev.Status))
		if len(statusAllow) > 0 && st != "" && !statusAllow[st] {
			// also accept empty status
			if st != "firing" && st != "open" && st != "active" && st != "resolved" && st != "ok" {
				continue
			}
			if !statusAllow[st] {
				continue
			}
		}

		name := ev.RuleName
		if name == "" {
			name = ev.Name
		}
		if name == "" {
			name = ev.MetricType
		}
		id := ev.ID
		if id == 0 {
			id = ev.RuleID
		}
		fp := fmt.Sprintf("ops_alert:%d:%s", id, ev.MetricType)
		if id == 0 {
			fp = fmt.Sprintf("ops_alert:%s:%s", name, ev.MetricType)
		}

		resolved := st == "resolved" || st == "ok" || st == "closed"
		body := line("规则", name) +
			line("指标", ev.MetricType) +
			line("状态", ev.Status)
		if ev.Message != "" {
			body += line("消息", trim(ev.Message, 400))
		}
		if ev.Threshold != 0 || ev.Value != 0 {
			body += line("值/阈值", fmt.Sprintf("%.4g / %.4g", ev.Value, ev.Threshold))
		}

		severity := alerter.Severity(sev)
		switch severity {
		case alerter.SevP0, alerter.SevP1, alerter.SevP2, alerter.SevP3:
		default:
			severity = alerter.SevP2
		}

		if err := o.engine.Emit(ctx, alerter.Event{
			Fingerprint: fp,
			Severity:    severity,
			Title:       "Ops 内置告警",
			Body:        body,
			Resolved:    resolved,
		}); err != nil {
			return err
		}
	}
	return nil
}
