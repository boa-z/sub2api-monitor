// Package notify defines pluggable outbound notification channels.
//
// Design:
//
//	alerter.Engine  ──formats──►  notify.Message
//	                              │
//	                     notify.Multi (fan-out)
//	                    /     |      \
//	            telegram   feishu   (webhook/email/...)
//
// Adding a channel: implement Channel, register in notify.BuildFromConfig.
package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Format is the preferred body markup for a message.
type Format string

const (
	FormatPlain Format = "plain"
	FormatHTML  Format = "html"
	FormatMD    Format = "markdown" // generic markdown; channels map as needed
)

// Severity mirrors alerter severities without importing alerter (avoid cycles).
type Severity string

const (
	SevP0 Severity = "P0"
	SevP1 Severity = "P1"
	SevP2 Severity = "P2"
	SevP3 Severity = "P3"
)

// Message is a channel-agnostic notification payload.
type Message struct {
	Title string
	// Text is plain-text body (always populated). Channels that need rich text
	// should prefer HTML/Markdown when set, else fall back to Text.
	Text     string
	HTML     string
	Markdown string
	Severity Severity
	Resolved bool
	Silent   bool
	// Labels are free-form metadata (instance, account_id, ...).
	Labels map[string]string
	// Recipients routes this message:
	//   - empty: each channel uses its own default recipients
	//   - "telegram:<chat_id>" or bare chat id for telegram
	//   - "feishu:<user_id|chat_id|open_id>"
	//   - "all" is not used; omit for defaults
	Recipients []string
}

// Channel is one outbound provider (Telegram, Feishu, webhook, ...).
type Channel interface {
	// Name is a stable id used in logs and recipient prefixes ("telegram", "feishu").
	Name() string
	// Enabled reports whether the channel is configured and ready.
	Enabled() bool
	// Send delivers a message. Implementations must be safe for concurrent use.
	Send(ctx context.Context, msg Message) error
}

// Multi fans out to all enabled channels. Partial failures are aggregated.
type Multi struct {
	channels []Channel
}

func NewMulti(channels ...Channel) *Multi {
	out := make([]Channel, 0, len(channels))
	for _, ch := range channels {
		if ch != nil {
			out = append(out, ch)
		}
	}
	return &Multi{channels: out}
}

func (m *Multi) Channels() []Channel { return append([]Channel(nil), m.channels...) }

func (m *Multi) EnabledNames() []string {
	var names []string
	for _, ch := range m.channels {
		if ch.Enabled() {
			names = append(names, ch.Name())
		}
	}
	return names
}

// Send implements a Notifier-like fan-out used by alerter.
func (m *Multi) Send(ctx context.Context, msg Message) error {
	if m == nil || len(m.channels) == 0 {
		return fmt.Errorf("no notification channels configured")
	}

	type result struct {
		name string
		err  error
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []result
		sent int
	)
	for _, ch := range m.channels {
		if !ch.Enabled() {
			continue
		}
		// If recipients are channel-scoped, skip channels that have no matching recipient
		// unless recipients empty (broadcast defaults).
		if len(msg.Recipients) > 0 && !recipientsTouchChannel(msg.Recipients, ch.Name()) {
			continue
		}
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ch.Send(ctx, filterRecipients(msg, ch.Name())); err != nil {
				mu.Lock()
				errs = append(errs, result{name: ch.Name(), err: err})
				mu.Unlock()
				return
			}
			mu.Lock()
			sent++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if sent == 0 && len(errs) == 0 {
		return fmt.Errorf("no enabled notification channel matched recipients")
	}
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%s: %v", e.name, e.err))
	}
	// If at least one channel succeeded, treat as soft error string but return error
	// so caller can log; alerter still marks alert to avoid storms on partial fail.
	if sent > 0 {
		return fmt.Errorf("partial notify failure (%d ok): %s", sent, strings.Join(parts, "; "))
	}
	return fmt.Errorf("notify failed: %s", strings.Join(parts, "; "))
}

// SendTo is a compatibility shim matching the old alerter.Notifier signature.
// chatIDs are treated as telegram recipients (or prefixed recipients).
func (m *Multi) SendTo(ctx context.Context, chatIDs []string, text string, silent bool) error {
	return m.Send(ctx, Message{
		Text:       text,
		HTML:       text, // historical callers already pass HTML
		Silent:     silent,
		Recipients: chatIDs,
	})
}

func recipientsTouchChannel(recipients []string, channel string) bool {
	channel = strings.ToLower(channel)
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if i := strings.IndexByte(r, ':'); i > 0 {
			if strings.EqualFold(r[:i], channel) {
				return true
			}
			continue
		}
		// bare id: only telegram historically
		if channel == "telegram" {
			return true
		}
	}
	return false
}

func filterRecipients(msg Message, channel string) Message {
	if len(msg.Recipients) == 0 {
		return msg
	}
	channel = strings.ToLower(channel)
	out := make([]string, 0, len(msg.Recipients))
	for _, r := range msg.Recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if i := strings.IndexByte(r, ':'); i > 0 {
			if strings.EqualFold(r[:i], channel) {
				out = append(out, r[i+1:])
			}
			continue
		}
		if channel == "telegram" {
			out = append(out, r)
		}
	}
	cp := msg
	cp.Recipients = out
	return cp
}

// Body picks the best body for a channel format preference.
func (m Message) Body(prefer Format) string {
	switch prefer {
	case FormatHTML:
		if m.HTML != "" {
			return m.HTML
		}
	case FormatMD:
		if m.Markdown != "" {
			return m.Markdown
		}
		if m.HTML != "" {
			// crude strip is caller's problem; prefer text
		}
	}
	if m.Text != "" {
		return m.Text
	}
	if m.HTML != "" {
		return StripHTML(m.HTML)
	}
	if m.Markdown != "" {
		return m.Markdown
	}
	return m.Title
}

// StripHTML is a minimal tag stripper for plain fallbacks.
func StripHTML(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	out := b.String()
	out = strings.ReplaceAll(out, "&lt;", "<")
	out = strings.ReplaceAll(out, "&gt;", ">")
	out = strings.ReplaceAll(out, "&amp;", "&")
	out = strings.ReplaceAll(out, "&quot;", "\"")
	return out
}

// Prefixed recipient helpers.
func TelegramRecipient(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(chatID), "telegram:") {
		return chatID
	}
	return "telegram:" + chatID
}

func FeishuRecipient(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(id), "feishu:") {
		return id
	}
	return "feishu:" + id
}
