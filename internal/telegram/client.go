package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/boa/sub2api-monitor/internal/config"
)

// Message is a fully-formed outbound Telegram message.
type Message struct {
	ChatID              string
	Text                string
	ParseMode           string
	DisableNotification bool
}

// Client talks to the Telegram Bot API with multi-chat support and basic rate limiting.
type Client struct {
	token               string
	defaultChatID       string
	extraChatIDs        []string
	parseMode           string
	disableNotification bool
	apiBase             string
	http                *http.Client

	// simple global rate limit: min interval between sendMessage calls
	mu       sync.Mutex
	lastSend time.Time
	minGap   time.Duration
}

// New creates a Telegram client. bot_token is required; default chat_id is recommended
// but optional if every alert supplies its own chat_ids.
func New(cfg config.TelegramConfig) (*Client, error) {
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, fmt.Errorf("telegram.bot_token is required")
	}
	base := strings.TrimRight(cfg.APIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	parseMode := cfg.ParseMode
	if parseMode == "" {
		parseMode = "HTML"
	}
	minGap := cfg.MinSendInterval
	if minGap <= 0 {
		minGap = 50 * time.Millisecond // ~20 msg/s soft limit; Telegram allows ~30/s to different chats
	}
	return &Client{
		token:               cfg.BotToken,
		defaultChatID:       strings.TrimSpace(cfg.ChatID),
		extraChatIDs:        normalizeChatIDs(cfg.ExtraChatIDs),
		parseMode:           parseMode,
		disableNotification: cfg.DisableNotification,
		apiBase:             base,
		http:                &http.Client{Timeout: 20 * time.Second},
		minGap:              minGap,
	}, nil
}

// DefaultChatID returns the primary chat.
func (c *Client) DefaultChatID() string { return c.defaultChatID }

// ResolveChatIDs merges per-event chat overrides with defaults.
// If overrides is empty, returns default + extra.
// Deduplicates while preserving order.
func (c *Client) ResolveChatIDs(overrides []string) []string {
	if len(overrides) > 0 {
		return normalizeChatIDs(overrides)
	}
	out := make([]string, 0, 1+len(c.extraChatIDs))
	if c.defaultChatID != "" {
		out = append(out, c.defaultChatID)
	}
	out = append(out, c.extraChatIDs...)
	return normalizeChatIDs(out)
}

// Send sends text to the default recipient set (default + extra).
func (c *Client) Send(ctx context.Context, text string) error {
	return c.SendTo(ctx, nil, text, c.disableNotification)
}

// SendSilent sends a quiet notification to the default recipient set.
func (c *Client) SendSilent(ctx context.Context, text string) error {
	return c.SendTo(ctx, nil, text, true)
}

// SendTo delivers text to the given chat IDs (or defaults when empty).
func (c *Client) SendTo(ctx context.Context, chatIDs []string, text string, silent bool) error {
	targets := c.ResolveChatIDs(chatIDs)
	if len(targets) == 0 {
		return fmt.Errorf("no telegram chat_id configured")
	}
	chunks := splitRunes(text, 4000)
	var firstErr error
	for _, chatID := range targets {
		for _, chunk := range chunks {
			if err := c.sendOne(ctx, chatID, chunk, silent); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("chat %s: %w", chatID, err)
				}
			}
		}
	}
	return firstErr
}

// SendMessage sends a pre-built Message (single chat).
func (c *Client) SendMessage(ctx context.Context, msg Message) error {
	chatID := strings.TrimSpace(msg.ChatID)
	if chatID == "" {
		chatID = c.defaultChatID
	}
	if chatID == "" {
		return fmt.Errorf("message chat_id empty and no default")
	}
	mode := msg.ParseMode
	if mode == "" {
		mode = c.parseMode
	}
	silent := msg.DisableNotification
	chunks := splitRunes(msg.Text, 4000)
	for _, chunk := range chunks {
		if err := c.sendOneWithMode(ctx, chatID, chunk, mode, silent); err != nil {
			return err
		}
	}
	return nil
}

type sendMessageReq struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode,omitempty"`
	DisableNotification   bool   `json:"disable_notification,omitempty"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	// Parameters may include retry_after on 429
	Parameters *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) sendOne(ctx context.Context, chatID, text string, silent bool) error {
	return c.sendOneWithMode(ctx, chatID, text, c.parseMode, silent)
}

func (c *Client) sendOneWithMode(ctx context.Context, chatID, text, parseMode string, silent bool) error {
	c.throttle(ctx)

	payload := sendMessageReq{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             parseMode,
		DisableNotification:   silent,
		DisableWebPagePreview: true,
	}
	if err := c.postSend(ctx, payload); err != nil {
		// retry without parse_mode on parse errors
		if parseMode != "" && strings.Contains(strings.ToLower(err.Error()), "parse") {
			payload.ParseMode = ""
			return c.postSend(ctx, payload)
		}
		// retry_after handling
		if ra, ok := asRetryAfter(err); ok && ra > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(ra) * time.Second):
			}
			return c.postSend(ctx, payload)
		}
		return err
	}
	return nil
}

type retryAfterError struct {
	seconds int
	msg     string
}

func (e *retryAfterError) Error() string { return e.msg }

func asRetryAfter(err error) (int, bool) {
	if e, ok := err.(*retryAfterError); ok {
		return e.seconds, true
	}
	return 0, false
}

func (c *Client) postSend(ctx context.Context, payload sendMessageReq) error {
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/bot%s/sendMessage", c.apiBase, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var ar apiResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		return fmt.Errorf("telegram decode: %w body=%s", err, string(raw))
	}
	if !ar.OK {
		if resp.StatusCode == 429 || strings.Contains(strings.ToLower(ar.Description), "too many") {
			ra := 1
			if ar.Parameters != nil && ar.Parameters.RetryAfter > 0 {
				ra = ar.Parameters.RetryAfter
			}
			return &retryAfterError{seconds: ra, msg: ar.Description}
		}
		return fmt.Errorf("telegram: %s", ar.Description)
	}
	return nil
}

func (c *Client) throttle(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	wait := c.minGap - time.Since(c.lastSend)
	if wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	c.lastSend = time.Now()
}

func normalizeChatIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
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

func splitRunes(s string, max int) []string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return []string{s}
	}
	var out []string
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count >= max {
			out = append(out, b.String())
			b.Reset()
			count = 0
		}
		b.WriteRune(r)
		count++
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// EscapeHTML escapes text for Telegram HTML parse mode.
func EscapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// Code wraps s as <code>…</code> with escaping.
func Code(s string) string {
	return "<code>" + EscapeHTML(s) + "</code>"
}

// Bold wraps s as <b>…</b> with escaping.
func Bold(s string) string {
	return "<b>" + EscapeHTML(s) + "</b>"
}
