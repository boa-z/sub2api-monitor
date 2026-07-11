package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// Intent bits
const (
	IntentGuilds         = 1 << 0
	IntentDirectMessages = 1 << 12
	IntentMessageContent = 1 << 15 // privileged; optional
)

// GatewayEvent is a decoded dispatch.
type GatewayEvent struct {
	Type string
	Data json.RawMessage
}

// Interaction is a subset of Discord interaction objects.
type Interaction struct {
	ID    string `json:"id"`
	Token string `json:"token"`
	Type  int    `json:"type"` // 2 application command, 3 message component
	Data  *struct {
		CustomID string `json:"custom_id"`
		Name     string `json:"name"`
		Options  []struct {
			Name  string          `json:"name"`
			Type  int             `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"options"`
		ComponentType int      `json:"component_type"`
		Values        []string `json:"values"`
	} `json:"data"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	Member    *struct {
		User *User `json:"user"`
	} `json:"member"`
	User    *User `json:"user"`
	Message *struct {
		ID string `json:"id"`
	} `json:"message"`
}

type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	GlobalName    string `json:"global_name"`
	Discriminator string `json:"discriminator"`
}

func (u *User) Display() string {
	if u == nil {
		return ""
	}
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// Handler processes gateway interactions.
type InteractionHandler func(ctx context.Context, it *Interaction) error

// Gateway maintains a Discord gateway connection for interactions.
type Gateway struct {
	client  *Client
	logger  *slog.Logger
	handler InteractionHandler
	intents int

	mu     sync.Mutex
	seq    *int64
	sessID string
}

func NewGateway(client *Client, logger *slog.Logger, handler InteractionHandler) *Gateway {
	return &Gateway{
		client:  client,
		logger:  logger,
		handler: handler,
		intents: IntentGuilds | IntentDirectMessages,
	}
}

func (g *Gateway) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := g.connectOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if g.logger != nil {
				g.logger.Warn("discord gateway reconnect", "err", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
	}
}

func (g *Gateway) connectOnce(ctx context.Context) error {
	// get gateway url
	var gw struct {
		URL string `json:"url"`
	}
	if err := g.client.do(ctx, http.MethodGet, "/gateway", nil, &gw); err != nil {
		return err
	}
	if gw.URL == "" {
		return fmt.Errorf("empty gateway url")
	}
	url := gw.URL + "/?v=10&encoding=json"
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: g.client.http,
	})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	// hello
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	var hello envelope
	if err := json.Unmarshal(data, &hello); err != nil {
		return err
	}
	var helloD struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	_ = json.Unmarshal(hello.D, &helloD)
	if helloD.HeartbeatInterval <= 0 {
		helloD.HeartbeatInterval = 41250
	}

	// identify
	identify := map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   stringsTrimBotPrefix(g.client.token),
			"intents": g.intents,
			"properties": map[string]string{
				"os":      "linux",
				"browser": "sub2api-monitor",
				"device":  "sub2api-monitor",
			},
		},
	}
	// token for identify must be raw without "Bot " prefix
	if err := writeJSON(ctx, conn, identify); err != nil {
		return err
	}

	// heartbeat loop
	hb := time.NewTicker(time.Duration(helloD.HeartbeatInterval) * time.Millisecond)
	defer hb.Stop()
	errCh := make(chan error, 1)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hb.C:
				g.mu.Lock()
				seq := g.seq
				g.mu.Unlock()
				payload := map[string]any{"op": 1, "d": seq}
				if err := writeJSON(ctx, conn, payload); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		default:
		}
		readCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		_, msg, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		var env envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.S != nil {
			g.mu.Lock()
			g.seq = env.S
			g.mu.Unlock()
		}
		switch env.Op {
		case 0: // dispatch
			if env.T == "INTERACTION_CREATE" {
				var it Interaction
				if err := json.Unmarshal(env.D, &it); err != nil {
					continue
				}
				if g.handler != nil {
					hctx, hcancel := context.WithTimeout(ctx, 12*time.Second)
					if err := g.handler(hctx, &it); err != nil && g.logger != nil {
						g.logger.Warn("interaction", "err", err)
					}
					hcancel()
				}
			}
		case 7: // reconnect
			return fmt.Errorf("gateway requested reconnect")
		case 9: // invalid session
			return fmt.Errorf("invalid session")
		case 11: // heartbeat ack
		case 10: // hello (already handled)
		}
	}
}

type envelope struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, raw)
}

func stringsTrimBotPrefix(token string) string {
	const p = "Bot "
	if len(token) > len(p) && token[:len(p)] == p {
		return token[len(p):]
	}
	return token
}
