// Package discord provides a lightweight Discord REST + Gateway client for
// outbound alerts and interactive panel commands (slash + DM buttons).
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
)

const apiBase = "https://discord.com/api/v10"

// Client talks to Discord REST. Gateway is optional (panel).
type Client struct {
	token             string
	defaultChannelID  string
	extraChannelIDs   []string
	guildID           string
	http              *http.Client
	applicationID     string
	applicationIDOnce sync.Once
	applicationIDErr  error

	mu       sync.Mutex
	lastSend time.Time
	minGap   time.Duration
}

func New(cfg config.DiscordConfig) (*Client, error) {
	token := strings.TrimSpace(cfg.BotToken)
	if token == "" {
		return nil, fmt.Errorf("discord.bot_token is required")
	}
	// Accept tokens with or without "Bot " prefix
	if !strings.HasPrefix(token, "Bot ") {
		token = "Bot " + token
	}
	return &Client{
		token:            token,
		defaultChannelID: strings.TrimSpace(cfg.DefaultChannelID),
		extraChannelIDs:  normalizeIDs(cfg.ExtraChannelIDs),
		guildID:          strings.TrimSpace(cfg.GuildID),
		http:             &http.Client{Timeout: 30 * time.Second},
		minGap:           50 * time.Millisecond,
	}, nil
}

func NewFromNotify(cfg config.NotifyDiscordConfig, top config.DiscordConfig) (*Client, error) {
	merged := top
	if cfg.BotToken != "" {
		merged.BotToken = cfg.BotToken
	}
	if cfg.DefaultChannelID != "" {
		merged.DefaultChannelID = cfg.DefaultChannelID
	}
	if len(cfg.ExtraChannelIDs) > 0 {
		merged.ExtraChannelIDs = cfg.ExtraChannelIDs
	}
	return New(merged)
}

func (c *Client) Token() string            { return c.token }
func (c *Client) GuildID() string          { return c.guildID }
func (c *Client) DefaultChannelID() string { return c.defaultChannelID }

func (c *Client) ResolveChannelIDs(overrides []string) []string {
	if len(overrides) > 0 {
		return normalizeIDs(overrides)
	}
	out := make([]string, 0, 1+len(c.extraChannelIDs))
	if c.defaultChannelID != "" {
		out = append(out, c.defaultChannelID)
	}
	out = append(out, c.extraChannelIDs...)
	return normalizeIDs(out)
}

func (c *Client) throttle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	wait := c.minGap - time.Since(c.lastSend)
	if wait > 0 {
		time.Sleep(wait)
	}
	c.lastSend = time.Now()
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	c.throttle()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sub2api-monitor (discord-panel)")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == 429 {
		// basic retry once
		var rr struct {
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal(raw, &rr)
		d := time.Duration(rr.RetryAfter * float64(time.Second))
		if d <= 0 {
			d = time.Second
		}
		time.Sleep(d)
		return c.do(ctx, method, path, body, out)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord %s %s: status %d body=%s", method, path, resp.StatusCode, truncate(string(raw), 240))
	}
	if out == nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) ApplicationID(ctx context.Context) (string, error) {
	c.applicationIDOnce.Do(func() {
		var me struct {
			ID string `json:"id"`
		}
		c.applicationIDErr = c.do(ctx, http.MethodGet, "/oauth2/applications/@me", nil, &me)
		if c.applicationIDErr == nil {
			c.applicationID = me.ID
		}
		if c.applicationID == "" && c.applicationIDErr == nil {
			// fallback: /users/@me for bot user id == application id for classic bots
			var u struct {
				ID string `json:"id"`
			}
			if err := c.do(ctx, http.MethodGet, "/users/@me", nil, &u); err == nil {
				c.applicationID = u.ID
			}
		}
	})
	return c.applicationID, c.applicationIDErr
}

// CreateDM opens (or reuses) a DM channel with a user.
func (c *Client) CreateDM(ctx context.Context, userID string) (string, error) {
	var ch struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/users/@me/channels", map[string]any{
		"recipient_id": userID,
	}, &ch); err != nil {
		return "", err
	}
	return ch.ID, nil
}

// SendChannel sends a message to a channel id (guild or DM).
func (c *Client) SendChannel(ctx context.Context, channelID, content string, components []Component) error {
	if channelID == "" {
		return fmt.Errorf("empty channel id")
	}
	if len([]rune(content)) > 2000 {
		content = string([]rune(content)[:1990]) + "…"
	}
	body := map[string]any{"content": content}
	if len(components) > 0 {
		body["components"] = components
	}
	return c.do(ctx, http.MethodPost, "/channels/"+channelID+"/messages", body, nil)
}

// SendToUser DMs a user by snowflake id.
func (c *Client) SendToUser(ctx context.Context, userID, content string) error {
	ch, err := c.CreateDM(ctx, userID)
	if err != nil {
		return err
	}
	return c.SendChannel(ctx, ch, content, nil)
}

// SendDefaults sends to configured default channels.
func (c *Client) SendDefaults(ctx context.Context, content string) error {
	ids := c.ResolveChannelIDs(nil)
	if len(ids) == 0 {
		return fmt.Errorf("no discord default channel configured")
	}
	var first error
	for _, id := range ids {
		if err := c.SendChannel(ctx, id, content, nil); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// EditChannelMessage edits an existing message.
func (c *Client) EditChannelMessage(ctx context.Context, channelID, messageID, content string, components []Component) error {
	body := map[string]any{"content": content}
	if components != nil {
		body["components"] = components
	}
	return c.do(ctx, http.MethodPatch, "/channels/"+channelID+"/messages/"+messageID, body, nil)
}

// RespondInteraction answers a slash/component interaction.
// typ: 4=channel message, 7=update message
func (c *Client) RespondInteraction(ctx context.Context, interactionID, interactionToken string, typ int, content string, components []Component, ephemeral bool) error {
	data := map[string]any{"content": content}
	if len(components) > 0 {
		data["components"] = components
	}
	if ephemeral {
		data["flags"] = 64
	}
	body := map[string]any{
		"type": typ,
		"data": data,
	}
	return c.do(ctx, http.MethodPost, "/interactions/"+interactionID+"/"+interactionToken+"/callback", body, nil)
}

// UpdateInteraction updates original deferred/response message.
func (c *Client) UpdateInteraction(ctx context.Context, applicationID, interactionToken, content string, components []Component) error {
	body := map[string]any{"content": content}
	if components != nil {
		body["components"] = components
	}
	path := "/webhooks/" + applicationID + "/" + interactionToken + "/messages/@original"
	return c.do(ctx, http.MethodPatch, path, body, nil)
}

// RegisterCommands upserts global or guild slash commands.
func (c *Client) RegisterCommands(ctx context.Context, cmds []ApplicationCommand) error {
	appID, err := c.ApplicationID(ctx)
	if err != nil || appID == "" {
		return fmt.Errorf("application id: %w", err)
	}
	path := "/applications/" + appID + "/commands"
	if c.guildID != "" {
		path = "/applications/" + appID + "/guilds/" + c.guildID + "/commands"
	}
	// bulk overwrite
	return c.do(ctx, http.MethodPut, path, cmds, nil)
}

// Component types for Discord message components.
type Component struct {
	Type        int         `json:"type"` // 1 action row, 2 button, 3 select
	CustomID    string      `json:"custom_id,omitempty"`
	Label       string      `json:"label,omitempty"`
	Style       int         `json:"style,omitempty"` // buttons 1-5
	Disabled    bool        `json:"disabled,omitempty"`
	Emoji       *Emoji      `json:"emoji,omitempty"`
	Options     []SelectOpt `json:"options,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
	Components  []Component `json:"components,omitempty"`
}

type Emoji struct {
	Name string `json:"name,omitempty"`
}

type SelectOpt struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

func ActionRow(children ...Component) Component {
	return Component{Type: 1, Components: children}
}

func Button(label, customID string, style int) Component {
	if style == 0 {
		style = 2 // secondary
	}
	return Component{Type: 2, Style: style, Label: label, CustomID: customID}
}

func PrimaryButton(label, customID string) Component { return Button(label, customID, 1) }
func DangerButton(label, customID string) Component  { return Button(label, customID, 4) }
func SuccessButton(label, customID string) Component { return Button(label, customID, 3) }

// StringSelect builds a type-3 string select menu (max 25 options).
func StringSelect(customID, placeholder string, opts ...SelectOpt) Component {
	if len(opts) > 25 {
		opts = opts[:25]
	}
	return Component{Type: 3, CustomID: customID, Placeholder: placeholder, Options: opts}
}

func SelectOption(label, value, desc string) SelectOpt {
	if len(label) > 100 {
		label = label[:97] + "…"
	}
	if len(value) > 100 {
		value = value[:100]
	}
	if len(desc) > 100 {
		desc = desc[:97] + "…"
	}
	return SelectOpt{Label: label, Value: value, Description: desc}
}

type ApplicationCommand struct {
	Name         string                     `json:"name"`
	Description  string                     `json:"description"`
	Type         int                        `json:"type,omitempty"` // 1 CHAT_INPUT
	Options      []ApplicationCommandOption `json:"options,omitempty"`
	DMPermission *bool                      `json:"dm_permission,omitempty"`
}

type ApplicationCommandOption struct {
	Type        int    `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}

func normalizeIDs(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ParseSnowflake converts a Discord snowflake string to int64.
func ParseSnowflake(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty snowflake")
	}
	return strconv.ParseInt(s, 10, 64)
}

func FormatSnowflake(id int64) string {
	return strconv.FormatInt(id, 10)
}
