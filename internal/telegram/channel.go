package telegram

import (
	"context"
	"log/slog"
	"strings"

	"github.com/boa/sub2api-monitor/internal/notify"
)

// Channel adapts Client to notify.Channel without colliding with Client.Send helpers.
type Channel struct {
	Client *Client
	Logger *slog.Logger
}

var _ notify.Channel = (*Channel)(nil)

func (ch *Channel) Name() string { return "telegram" }

func (ch *Channel) Enabled() bool {
	return ch != nil && ch.Client != nil && ch.Client.token != ""
}

func (ch *Channel) Send(ctx context.Context, msg notify.Message) error {
	if !ch.Enabled() {
		return nil
	}
	body := msg.Body(notify.FormatHTML)
	if body == "" {
		body = msg.Title
	}
	silent := msg.Silent || msg.Resolved
	var err error
	if len(msg.Recipients) == 0 {
		err = ch.Client.SendTo(ctx, nil, body, silent)
	} else {
		err = ch.Client.SendTo(ctx, msg.Recipients, body, silent)
	}
	if err != nil && ch.Logger != nil {
		ch.Logger.Warn("telegram send failed", "err", err, "recipients", strings.Join(msg.Recipients, ","))
	}
	return err
}

// AsChannel wraps a client as notify.Channel.
func (c *Client) AsChannel(logger *slog.Logger) *Channel {
	return &Channel{Client: c, Logger: logger}
}
