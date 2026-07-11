package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/boa/sub2api-monitor/internal/notify"
)

// Channel adapts Client to notify.Channel.
type Channel struct {
	client *Client
	logger *slog.Logger
}

func (c *Client) AsChannel(logger *slog.Logger) *Channel {
	return &Channel{client: c, logger: logger}
}

func (ch *Channel) Name() string { return "discord" }

func (ch *Channel) Enabled() bool {
	return ch != nil && ch.client != nil && ch.client.token != ""
}

func (ch *Channel) Send(ctx context.Context, msg notify.Message) error {
	if !ch.Enabled() {
		return fmt.Errorf("discord channel disabled")
	}
	body := msg.Body(notify.FormatPlain)
	if body == "" {
		body = msg.Text
	}
	if msg.Title != "" {
		prefix := msg.Title
		if msg.Resolved {
			prefix = "✅ " + prefix
		} else if msg.Severity != "" {
			prefix = string(msg.Severity) + " " + prefix
		}
		if !strings.HasPrefix(body, prefix) {
			body = prefix + "\n" + body
		}
	}
	// strip crude HTML tags if HTML slipped in
	body = stripSimpleHTML(body)

	recipients := msg.Recipients
	if len(recipients) == 0 {
		return ch.client.SendDefaults(ctx, body)
	}
	var first error
	sent := 0
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		// user: vs channel: optional hints; default treat as user id for panel alerts,
		// channel id for ops defaults that are channel snowflakes — we try DM first if looks like user routing.
		// Convention from panel: discord:<userID> already stripped to userID by filterRecipients.
		// For default channel alerts recipients are channel IDs.
		var err error
		if strings.HasPrefix(r, "channel:") {
			err = ch.client.SendChannel(ctx, strings.TrimPrefix(r, "channel:"), body, nil)
		} else if strings.HasPrefix(r, "user:") {
			err = ch.client.SendToUser(ctx, strings.TrimPrefix(r, "user:"), body)
		} else {
			// Heuristic: if default channel list contains id, send to channel; else DM user.
			isDefaultCh := false
			for _, id := range ch.client.ResolveChannelIDs(nil) {
				if id == r {
					isDefaultCh = true
					break
				}
			}
			if isDefaultCh {
				err = ch.client.SendChannel(ctx, r, body, nil)
			} else {
				// panel user alerts → DM
				err = ch.client.SendToUser(ctx, r, body)
			}
		}
		if err != nil {
			if first == nil {
				first = err
			}
			if ch.logger != nil {
				ch.logger.Warn("discord send", "to", r, "err", err)
			}
			continue
		}
		sent++
	}
	if sent == 0 {
		if first != nil {
			return first
		}
		return fmt.Errorf("discord: no recipients delivered")
	}
	return nil
}

func stripSimpleHTML(s string) string {
	// minimal replacements for Telegram HTML used in alerter
	repl := []struct{ a, b string }{
		{"<b>", "**"}, {"</b>", "**"},
		{"<code>", "`"}, {"</code>", "`"},
		{"<pre>", "```\n"}, {"</pre>", "\n```"},
		{"&lt;", "<"}, {"&gt;", ">"}, {"&amp;", "&"},
		{"<br>", "\n"}, {"<br/>", "\n"}, {"<br />", "\n"},
	}
	for _, r := range repl {
		s = strings.ReplaceAll(s, r.a, r.b)
	}
	// drop remaining simple tags
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+1:]
	}
	return s
}
