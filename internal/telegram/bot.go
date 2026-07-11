package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/boa/sub2api-monitor/internal/config"
)

type Bot struct {
	token               string
	chatID              string
	parseMode           string
	disableNotification bool
	apiBase             string
	http                *http.Client
}

func NewBot(cfg config.TelegramConfig) (*Bot, error) {
	if cfg.BotToken == "" || cfg.ChatID == "" {
		return nil, fmt.Errorf("telegram bot_token and chat_id required")
	}
	base := strings.TrimRight(cfg.APIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	return &Bot{
		token:               cfg.BotToken,
		chatID:              cfg.ChatID,
		parseMode:           cfg.ParseMode,
		disableNotification: cfg.DisableNotification,
		apiBase:             base,
		http:                &http.Client{Timeout: 20 * time.Second},
	}, nil
}

type sendMessageReq struct {
	ChatID              string `json:"chat_id"`
	Text                string `json:"text"`
	ParseMode           string `json:"parse_mode,omitempty"`
	DisableNotification bool   `json:"disable_notification,omitempty"`
	DisableWebPagePreview bool `json:"disable_web_page_preview"`
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (b *Bot) Send(ctx context.Context, text string) error {
	return b.SendWithOptions(ctx, text, b.disableNotification)
}

func (b *Bot) SendWithOptions(ctx context.Context, text string, silent bool) error {
	// Split long messages by rune count (Telegram 4096 limit)
	chunks := splitRunes(text, 4000)
	for _, chunk := range chunks {
		if err := b.sendOne(ctx, chunk, silent); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) sendOne(ctx context.Context, text string, silent bool) error {
	payload := sendMessageReq{
		ChatID:                b.chatID,
		Text:                  text,
		ParseMode:             b.parseMode,
		DisableNotification:   silent,
		DisableWebPagePreview: true,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/bot%s/sendMessage", b.apiBase, b.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
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
		// retry without parse_mode if HTML/Markdown fails
		if b.parseMode != "" && strings.Contains(strings.ToLower(ar.Description), "parse") {
			payload.ParseMode = ""
			body2, _ := json.Marshal(payload)
			req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body2))
			req2.Header.Set("Content-Type", "application/json")
			resp2, err2 := b.http.Do(req2)
			if err2 != nil {
				return err2
			}
			defer resp2.Body.Close()
			raw2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
			var ar2 apiResp
			if err := json.Unmarshal(raw2, &ar2); err != nil {
				return err
			}
			if !ar2.OK {
				return fmt.Errorf("telegram: %s", ar2.Description)
			}
			return nil
		}
		return fmt.Errorf("telegram: %s", ar.Description)
	}
	return nil
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
