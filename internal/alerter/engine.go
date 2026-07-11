package alerter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/notify"
	"github.com/boa/sub2api-monitor/internal/state"
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
	// Body is HTML fragment for rich channels; Text is optional plain override.
	Body string
	Text string
	// Lines optional structured fields (preferred for multi-channel).
	Lines []notify.KV

	Resolved bool
	Silent   bool
	// Recipients routes to channels:
	//   - empty: each channel default
	//   - "123" or "telegram:123" for telegram
	//   - "feishu:oc_xxx" for feishu (future app path)
	// ChatIDs is a legacy alias for Recipients (telegram-oriented).
	Recipients []string
	ChatIDs    []string
	// Cooldown overrides global alert.cooldown when > 0.
	Cooldown time.Duration
	// Force bypasses cooldown (use sparingly).
	Force bool
	// Labels free-form metadata.
	Labels map[string]string
}

// Notifier is the outbound messaging port.
// Implemented by *notify.Multi (preferred) and legacy telegram.Client via SendTo.
type Notifier interface {
	SendTo(ctx context.Context, chatIDs []string, text string, silent bool) error
}

// MessageNotifier is the richer multi-channel port.
type MessageNotifier interface {
	Send(ctx context.Context, msg notify.Message) error
}

type Engine struct {
	cfg      *config.Config
	store    state.Store
	notifier Notifier
	// multi is optional; when set, Emit uses structured Message fan-out.
	multi  MessageNotifier
	logger *slog.Logger
}

func New(cfg *config.Config, store state.Store, notifier Notifier, logger *slog.Logger) *Engine {
	e := &Engine{cfg: cfg, store: store, notifier: notifier, logger: logger}
	if mn, ok := notifier.(MessageNotifier); ok {
		e.multi = mn
	}
	return e
}

// SetMulti attaches a multi-channel sender (optional enhancement).
func (e *Engine) SetMulti(m MessageNotifier) { e.multi = m }

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

	recipients := ev.Recipients
	if len(recipients) == 0 {
		recipients = ev.ChatIDs
	}

	if ev.Resolved {
		if !seen {
			return nil
		}
		if !e.cfg.Alert.SendResolved {
			_ = e.store.ClearAlert(ev.Fingerprint)
			return nil
		}
		if err := e.dispatch(ctx, ev, recipients, true); err != nil {
			// soft-log partial failures
			e.logger.Warn("notify resolved", "err", err, "fp", ev.Fingerprint)
			if !isPartial(err) {
				return err
			}
		}
		return e.store.ClearAlert(ev.Fingerprint)
	}

	if seen && !ev.Force && now.Sub(last) < cooldown {
		e.logger.Debug("cooldown", "fp", ev.Fingerprint, "left", cooldown-now.Sub(last))
		return nil
	}

	if err := e.dispatch(ctx, ev, recipients, ev.Silent); err != nil {
		e.logger.Warn("notify fire", "err", err, "fp", ev.Fingerprint)
		if !isPartial(err) {
			return err
		}
	}
	return e.store.MarkAlert(ev.Fingerprint, now)
}

func (e *Engine) dispatch(ctx context.Context, ev Event, recipients []string, silent bool) error {
	msg := e.buildMessage(ev, silent)
	msg.Recipients = recipients

	if e.multi != nil {
		return e.multi.Send(ctx, msg)
	}
	// legacy path: HTML body via SendTo
	body := msg.HTML
	if body == "" {
		body = msg.Text
	}
	return e.notifier.SendTo(ctx, recipients, body, silent)
}

func (e *Engine) buildMessage(ev Event, silent bool) notify.Message {
	// structured path when Lines present
	if len(ev.Lines) > 0 {
		view := notify.AlertView{
			Instance: e.cfg.Instance,
			Severity: notify.Severity(ev.Severity),
			Title:    ev.Title,
			Resolved: ev.Resolved,
			Lines:    ev.Lines,
			Time:     time.Now().Format("2006-01-02 15:04:05 MST"),
		}
		msg := view.Message(e.cfg.Alert.MaxMessageRunes)
		msg.Silent = silent
		msg.Labels = ev.Labels
		return msg
	}

	// legacy HTML body from collectors
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
	case SevP3:
		if !ev.Resolved {
			icon = "🟡"
		}
	}

	var h strings.Builder
	fmt.Fprintf(&h, "%s %s · %s\n",
		icon,
		notify.BoldHTML("["+string(ev.Severity)+"] "+label),
		notify.EscapeHTML(ev.Title),
	)
	fmt.Fprintf(&h, "实例: %s\n", notify.CodeHTML(e.cfg.Instance))
	if ev.Body != "" {
		h.WriteString(ev.Body)
		if !strings.HasSuffix(ev.Body, "\n") {
			h.WriteByte('\n')
		}
	}
	fmt.Fprintf(&h, "时间: %s", notify.CodeHTML(time.Now().Format("2006-01-02 15:04:05 MST")))
	htmlBody := h.String()
	if max := e.cfg.Alert.MaxMessageRunes; max > 0 {
		r := []rune(htmlBody)
		if len(r) > max {
			htmlBody = string(r[:max]) + "…"
		}
	}
	text := ev.Text
	if text == "" {
		text = notify.StripHTML(htmlBody)
	}
	return notify.Message{
		Title:    ev.Title,
		Text:     text,
		HTML:     htmlBody,
		Markdown: text,
		Severity: notify.Severity(ev.Severity),
		Resolved: ev.Resolved,
		Silent:   silent,
		Labels:   ev.Labels,
	}
}

func isPartial(err error) bool {
	return err != nil && strings.Contains(err.Error(), "partial notify")
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
