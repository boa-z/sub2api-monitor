package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/sub2api"
)

type accountsCollector struct {
	cfg    *config.Config
	client *sub2api.Client
	engine *alerter.Engine
	logger *slog.Logger
}

func (a *accountsCollector) Run(ctx context.Context) error {
	accounts, err := a.client.ListAllAccounts(ctx, a.cfg.Checks.Accounts.PageSize, "")
	if err != nil {
		return err
	}

	watchStatus := map[string]bool{}
	for _, s := range a.cfg.Checks.Accounts.WatchStatuses {
		watchStatus[strings.ToLower(s)] = true
	}

	now := time.Now()

	for _, acc := range accounts {
		st := strings.ToLower(acc.Status)
		if watchStatus[st] {
			fp := fmt.Sprintf("account:status:%d:%s", acc.ID, st)
			body := line("账号", fmt.Sprintf("#%d %s", acc.ID, acc.Name)) +
				line("平台", acc.Platform) +
				line("类型", acc.Type) +
				line("状态", acc.Status)
			if acc.ErrorMessage != "" {
				body += line("原因", trim(acc.ErrorMessage, 400))
			}
			if err := a.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    alerter.SevP1,
				Title:       "账号状态异常",
				Body:        body,
			}); err != nil {
				return err
			}
		}

		if a.cfg.Checks.Accounts.WatchRateLimited && acc.RateLimitResetAt != nil && acc.RateLimitResetAt.After(now) {
			fp := fmt.Sprintf("account:ratelimit:%d", acc.ID)
			body := line("账号", fmt.Sprintf("#%d %s", acc.ID, acc.Name)) +
				line("平台", acc.Platform) +
				line("解除时间", acc.RateLimitResetAt.Local().Format(time.RFC3339))
			if err := a.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    alerter.SevP2,
				Title:       "账号触发限速",
				Body:        body,
			}); err != nil {
				return err
			}
		}

		if a.cfg.Checks.Accounts.WatchOverload && acc.OverloadUntil != nil && acc.OverloadUntil.After(now) {
			fp := fmt.Sprintf("account:overload:%d", acc.ID)
			body := line("账号", fmt.Sprintf("#%d %s", acc.ID, acc.Name)) +
				line("平台", acc.Platform) +
				line("过载至", acc.OverloadUntil.Local().Format(time.RFC3339))
			if err := a.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    alerter.SevP2,
				Title:       "账号过载",
				Body:        body,
			}); err != nil {
				return err
			}
		}

		if a.cfg.Checks.Accounts.WatchTempUnschedulable && acc.TempUnschedulableUntil != nil && acc.TempUnschedulableUntil.After(now) {
			fp := fmt.Sprintf("account:temp_unsched:%d", acc.ID)
			body := line("账号", fmt.Sprintf("#%d %s", acc.ID, acc.Name)) +
				line("平台", acc.Platform) +
				line("至", acc.TempUnschedulableUntil.Local().Format(time.RFC3339))
			if acc.TempUnschedulableReason != "" {
				body += line("原因", trim(acc.TempUnschedulableReason, 300))
			}
			if err := a.engine.Emit(ctx, alerter.Event{
				Fingerprint: fp,
				Severity:    alerter.SevP2,
				Title:       "账号临时不可调度",
				Body:        body,
			}); err != nil {
				return err
			}
		}

		candidates := []struct {
			fp    string
			cond  bool
			title string
			sev   alerter.Severity
		}{
			{fmt.Sprintf("account:status:%d:error", acc.ID), strings.EqualFold(acc.Status, "error"), "账号状态异常", alerter.SevP1},
			{fmt.Sprintf("account:ratelimit:%d", acc.ID), acc.RateLimitResetAt != nil && acc.RateLimitResetAt.After(now), "账号触发限速", alerter.SevP2},
			{fmt.Sprintf("account:overload:%d", acc.ID), acc.OverloadUntil != nil && acc.OverloadUntil.After(now), "账号过载", alerter.SevP2},
			{fmt.Sprintf("account:temp_unsched:%d", acc.ID), acc.TempUnschedulableUntil != nil && acc.TempUnschedulableUntil.After(now), "账号临时不可调度", alerter.SevP2},
		}
		for _, c := range candidates {
			if !c.cond && a.engine.Seen(c.fp) {
				if err := a.engine.Emit(ctx, alerter.Event{
					Fingerprint: c.fp,
					Severity:    c.sev,
					Title:       c.title + " · 已恢复",
					Body:        line("账号", fmt.Sprintf("#%d %s", acc.ID, acc.Name)),
					Resolved:    true,
				}); err != nil {
					return err
				}
			}
		}
	}

	a.logger.Debug("accounts scanned", "count", len(accounts))
	return nil
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
