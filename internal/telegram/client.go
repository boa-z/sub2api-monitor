package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	ReplyMarkup         any // InlineKeyboardMarkup or nil
}

// InlineKeyboardButton is a single button.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// InlineKeyboardMarkup is a grid of buttons.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// Row builds a single-row keyboard.
func Row(buttons ...InlineKeyboardButton) []InlineKeyboardButton {
	return buttons
}

// Btn creates a callback button.
func Btn(text, data string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, CallbackData: data}
}

// ----- inbound update types (subset) -----

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type InMessage struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string     `json:"id"`
	From    *User      `json:"from"`
	Message *InMessage `json:"message"`
	Data    string     `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *InMessage     `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
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

	mu       sync.Mutex
	lastSend time.Time
	minGap   time.Duration
}

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
		minGap = 50 * time.Millisecond
	}
	return &Client{
		token:               cfg.BotToken,
		defaultChatID:       strings.TrimSpace(cfg.ChatID),
		extraChatIDs:        normalizeChatIDs(cfg.ExtraChatIDs),
		parseMode:           parseMode,
		disableNotification: cfg.DisableNotification,
		apiBase:             base,
		http:                &http.Client{Timeout: 60 * time.Second},
		minGap:              minGap,
	}, nil
}

func (c *Client) DefaultChatID() string { return c.defaultChatID }
func (c *Client) ParseMode() string     { return c.parseMode }

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

func (c *Client) Send(ctx context.Context, text string) error {
	return c.SendTo(ctx, nil, text, c.disableNotification)
}

func (c *Client) SendSilent(ctx context.Context, text string) error {
	return c.SendTo(ctx, nil, text, true)
}

func (c *Client) SendTo(ctx context.Context, chatIDs []string, text string, silent bool) error {
	targets := c.ResolveChatIDs(chatIDs)
	if len(targets) == 0 {
		return fmt.Errorf("no telegram chat_id configured")
	}
	chunks := splitRunes(text, 4000)
	var firstErr error
	for _, chatID := range targets {
		for _, chunk := range chunks {
			if err := c.sendOne(ctx, chatID, chunk, silent, nil); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("chat %s: %w", chatID, err)
				}
			}
		}
	}
	return firstErr
}

// SendChat sends text to a single chat (by int64 or string id).
func (c *Client) SendChat(ctx context.Context, chatID any, text string, markup *InlineKeyboardMarkup) error {
	id := chatIDString(chatID)
	if id == "" {
		return fmt.Errorf("empty chat id")
	}
	chunks := splitRunes(text, 4000)
	for i, chunk := range chunks {
		var m *InlineKeyboardMarkup
		if i == len(chunks)-1 {
			m = markup
		}
		if err := c.sendOne(ctx, id, chunk, c.disableNotification, m); err != nil {
			return err
		}
	}
	return nil
}

// EditMessage edits an existing message text + optional markup.
func (c *Client) EditMessage(ctx context.Context, chatID any, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	c.throttle(ctx)
	payload := map[string]any{
		"chat_id":                  chatIDString(chatID),
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               c.parseMode,
		"disable_web_page_preview": true,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return c.apiCall(ctx, "editMessageText", payload, nil)
}

// AnswerCallback answers a callback query (dismiss loading spinner).
func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string, showAlert bool) error {
	payload := map[string]any{
		"callback_query_id": callbackID,
	}
	if text != "" {
		payload["text"] = text
		payload["show_alert"] = showAlert
	}
	return c.apiCall(ctx, "answerCallbackQuery", payload, nil)
}

// BotCommand is a Telegram bot command shown in the menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommands registers the bot command menu (private chats).
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	if commands == nil {
		commands = []BotCommand{}
	}
	return c.apiCall(ctx, "setMyCommands", map[string]any{
		"commands": commands,
		"scope":    map[string]any{"type": "all_private_chats"},
	}, nil)
}

// DeleteMessage best-effort deletes a chat message (e.g. API key input).
func (c *Client) DeleteMessage(ctx context.Context, chatID any, messageID int64) error {
	if messageID <= 0 {
		return nil
	}
	return c.apiCall(ctx, "deleteMessage", map[string]any{
		"chat_id":    chatIDString(chatID),
		"message_id": messageID,
	}, nil)
}

// GetUpdates long-polls for updates.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	q := url.Values{}
	q.Set("offset", strconv.FormatInt(offset, 10))
	q.Set("timeout", strconv.Itoa(timeoutSec))
	q.Set("allowed_updates", `["message","callback_query"]`)

	// Use longer HTTP timeout than long-poll
	u := fmt.Sprintf("%s/bot%s/getUpdates?%s", c.apiBase, c.token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var ar struct {
		OK          bool     `json:"ok"`
		Description string   `json:"description"`
		Result      []Update `json:"result"`
	}
	if err := json.Unmarshal(raw, &ar); err != nil {
		return nil, fmt.Errorf("getUpdates decode: %w", err)
	}
	if !ar.OK {
		return nil, fmt.Errorf("getUpdates: %s", ar.Description)
	}
	return ar.Result, nil
}

// DeleteWebhook clears any webhook so getUpdates works.
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.apiCall(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

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
	_ = mode
	silent := msg.DisableNotification
	var markup *InlineKeyboardMarkup
	if m, ok := msg.ReplyMarkup.(*InlineKeyboardMarkup); ok {
		markup = m
	}
	chunks := splitRunes(msg.Text, 4000)
	for i, chunk := range chunks {
		var m *InlineKeyboardMarkup
		if i == len(chunks)-1 {
			m = markup
		}
		if err := c.sendOneWithMode(ctx, chatID, chunk, c.parseMode, silent, m); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendOne(ctx context.Context, chatID, text string, silent bool, markup *InlineKeyboardMarkup) error {
	return c.sendOneWithMode(ctx, chatID, text, c.parseMode, silent, markup)
}

func (c *Client) sendOneWithMode(ctx context.Context, chatID, text, parseMode string, silent bool, markup *InlineKeyboardMarkup) error {
	c.throttle(ctx)
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_notification":     silent,
		"disable_web_page_preview": true,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	if err := c.apiCall(ctx, "sendMessage", payload, nil); err != nil {
		if parseMode != "" && strings.Contains(strings.ToLower(err.Error()), "parse") {
			delete(payload, "parse_mode")
			return c.apiCall(ctx, "sendMessage", payload, nil)
		}
		if ra, ok := asRetryAfter(err); ok && ra > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(ra) * time.Second):
			}
			return c.apiCall(ctx, "sendMessage", payload, nil)
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

func (c *Client) apiCall(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/bot%s/%s", c.apiBase, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var ar struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
		Parameters  *struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &ar); err != nil {
		return fmt.Errorf("telegram %s decode: %w body=%s", method, err, string(raw))
	}
	if !ar.OK {
		if resp.StatusCode == 429 || strings.Contains(strings.ToLower(ar.Description), "too many") {
			ra := 1
			if ar.Parameters != nil && ar.Parameters.RetryAfter > 0 {
				ra = ar.Parameters.RetryAfter
			}
			return &retryAfterError{seconds: ra, msg: ar.Description}
		}
		// editMessage "message is not modified" is benign
		if strings.Contains(strings.ToLower(ar.Description), "message is not modified") {
			return nil
		}
		return fmt.Errorf("telegram %s: %s", method, ar.Description)
	}
	if result != nil && len(ar.Result) > 0 {
		return json.Unmarshal(ar.Result, result)
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

func chatIDString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
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

func EscapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func Code(s string) string {
	return "<code>" + EscapeHTML(s) + "</code>"
}

func Bold(s string) string {
	return "<b>" + EscapeHTML(s) + "</b>"
}
