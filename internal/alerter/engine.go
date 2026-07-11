package alerter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/state"
	"github.com/boa/sub2api-monitor/internal/telegram"
)

type Severity string

const (
	SevP0 Severity = "P0"
	SevP1 Severity = "P1"
	SevP2 Severity = "P2"
	SevP3 Severity = "P3"
)

// Event is a deduplicated alert/notification.
type Event struct {
	Fingerprint string
	Severity    Severity
	Title       string
	Body        string // HTML fragments (values should already be escaped)
	Resolved    bool
	Silent      bool
	// ChatIDs overrides default telegram recipients for this event.
	ChatIDs []string
	// Cooldown overrides global alert.cooldown when > 0.
	Cooldown time.Duration
	// Force bypasses cooldown (use sparingly).
	Force bool
}

// Notifier is the outbound messaging port (implemented by telegram.Client).
type Notifier interface {
	SendTo(ctx context.Context, chatIDs []string, text string, silent bool) error
}

type Engine struct {
	cfg      *config.Config
	store    state.Store
	notifier Notifier
	logger   *slog.Logger
}

func New(cfg *config.Config, store state.Store, notifier Notifier, logger *slog.Logger) *Engine {
	return &Engine{cfg: cfg, store: store, notifier: notifier, logger: logger}
}

func (e *Engine) Emit(ctx context.Context, ev Event) error {
	if ev.Fingerprint == "" {
		return fmt.Errorf("empty fingerprint")
	}
	if ev.Severity == "" {
		ev.Severity = SevP2
	}

	if !ev.Resolved && e.inQuietHours(ev.Severity) {
		e.logger.Debug("suppressed by quiet hours", "fp", ev.Fingerprint, "sev", ev.Severity)
		return nil
	}

	now := time.Now()
	last, seen := e.store.LastAlert(ev.Fingerprint)
	cooldown := e.cfg.Alert.Cooldown
	if ev.Cooldown > 0 {
		cooldown = ev.Cooldown
	}

	if ev.Resolved {
		if !seen {
			return nil
		}
		if !e.cfg.Alert.SendResolved {
			_ = e.store.ClearAlert(ev.Fingerprint)
			return nil
		}
		msg := e.format(ev)
		if err := e.notifier.SendTo(ctx, ev.ChatIDs, msg, true); err != nil {
			return err
		}
		return e.store.ClearAlert(ev.Fingerprint)
	}

	if seen && !ev.Force && now.Sub(last) < cooldown {
		e.logger.Debug("cooldown", "fp", ev.Fingerprint, "left", cooldown-now.Sub(last))
		return nil
	}

	msg := e.format(ev)
	if err := e.notifier.SendTo(ctx, ev.ChatIDs, msg, ev.Silent); err != nil {
		return err
	}
	return e.store.MarkAlert(ev.Fingerprint, now)
}

func (e *Engine) format(ev Event) string {
	icon := "🔴"
	label := "FIRING"
	if ev.Resolved {
		icon = "🟢"
		label = "RESOLVED"
	}
	switch ev.Severity {
	case SevP0:
		if !ev.Resolved {
			icon = "🚨"
		}
	case SevP1:
		if !ev.Resolved {
			icon = "🔴"
		}
	case SevP3:
		if !ev.Resolved {
			icon = "🟡"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s · %s\n",
		icon,
		telegram.Bold("["+string(ev.Severity)+"] "+label),
		telegram.EscapeHTML(ev.Title),
	)
	fmt.Fprintf(&b, "实例: %s\n", telegram.Code(e.cfg.Instance))
	if ev.Body != "" {
		b.WriteString(ev.Body)
		if !strings.HasSuffix(ev.Body, "\n") {
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "时间: %s", telegram.Code(time.Now().Format("2006-01-02 15:04:05 MST")))

	msg := b.String()
	runes := []rune(msg)
	if max := e.cfg.Alert.MaxMessageRunes; max > 0 && len(runes) > max {
		msg = string(runes[:max]) + "…"
	}
	return msg
}

func (e *Engine) inQuietHours(sev Severity) bool {
	qh := e.cfg.Alert.QuietHours
	if qh == nil || qh.Start == "" || qh.End == "" {
		return false
	}
	for _, a := range qh.AllowSeverities {
		if strings.EqualFold(a, string(sev)) {
			return false
		}
	}
	now := time.Now()
	start, err1 := parseHHMM(qh.Start, now)
	end, err2 := parseHHMM(qh.End, now)
	if err1 != nil || err2 != nil {
		return false
	}
	if start.Equal(end) {
		return false
	}
	if start.Before(end) {
		return !now.Before(start) && now.Before(end)
	}
	return !now.Before(start) || now.Before(end)
}

func parseHHMM(s string, ref time.Time) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return time.Time{}, err
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day(), h, m, 0, 0, ref.Location()), nil
}

// Seen reports whether fingerprint is currently active.
func (e *Engine) Seen(fp string) bool {
	_, ok := e.store.LastAlert(fp)
	return ok
}
