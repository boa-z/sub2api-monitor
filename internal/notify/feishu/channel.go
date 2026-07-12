package feishu

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

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/notify"
)

// Channel delivers alerts to Feishu/Lark via:
//  1. Incoming webhook (group custom bot) — simplest
//  2. Optional app credentials reserved for future IM message API
//
// Current implementation focuses on webhook; app token path is stubbed for extension.
type Channel struct {
	webhookURL string
	secret     string // optional sign secret for custom bot
	// future:
	appID     string
	appSecret string
	// default receive ids when message has no recipients (open_id / chat_id / user_id)
	defaultIDs []string

	http *http.Client
	mu   sync.Mutex
	// reserved for tenant access token cache
	token    string
	tokenExp time.Time
}

var _ notify.Channel = (*Channel)(nil)

func New(cfg config.FeishuConfig) (*Channel, error) {
	if !cfg.Enabled {
		return &Channel{}, nil
	}
	if strings.TrimSpace(cfg.WebhookURL) == "" && (cfg.AppID == "" || cfg.AppSecret == "") {
		return nil, fmt.Errorf("feishu: webhook_url or app_id+app_secret required when enabled")
	}
	return &Channel{
		webhookURL: strings.TrimSpace(cfg.WebhookURL),
		secret:     cfg.WebhookSecret,
		appID:      cfg.AppID,
		appSecret:  cfg.AppSecret,
		defaultIDs: append([]string(nil), cfg.DefaultReceiveIDs...),
		http:       &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (c *Channel) Name() string { return "feishu" }

func (c *Channel) Enabled() bool {
	return c != nil && (c.webhookURL != "" || (c.appID != "" && c.appSecret != ""))
}

func (c *Channel) Send(ctx context.Context, msg notify.Message) error {
	if !c.Enabled() {
		return nil
	}
	// Prefer webhook when configured (covers group bots).
	if c.webhookURL != "" {
		return c.sendWebhook(ctx, msg)
	}
	// App API path: not fully implemented yet — fail clearly so Multi can report.
	return fmt.Errorf("feishu app messaging API not implemented yet; configure webhook_url")
}

type webhookPayload struct {
	MsgType string         `json:"msg_type"`
	Content map[string]any `json:"content,omitempty"`
	Card    map[string]any `json:"card,omitempty"`
	// Timestamp/Sign for secured bots
	Timestamp string `json:"timestamp,omitempty"`
	Sign      string `json:"sign,omitempty"`
}

func (c *Channel) sendWebhook(ctx context.Context, msg notify.Message) error {
	title := msg.Title
	if title == "" {
		title = "sub2api-monitor"
	}
	// Use interactive card for better readability
	headerTemplate := "red"
	if msg.Resolved {
		headerTemplate = "green"
	} else {
		switch msg.Severity {
		case notify.SevP0:
			headerTemplate = "red"
		case notify.SevP1:
			headerTemplate = "orange"
		case notify.SevP2:
			headerTemplate = "yellow"
		default:
			headerTemplate = "blue"
		}
	}
	body := msg.Body(notify.FormatMD)
	if body == "" {
		body = msg.Text
	}
	// Feishu markdown in cards uses a subset; plain is safer in text element.
	card := map[string]any{
		"header": map[string]any{
			"template": headerTemplate,
			"title": map[string]any{
				"tag":     "plain_text",
				"content": trim(fmt.Sprintf("[%s] %s", msg.Severity, title), 100),
			},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": trim(body, 4000),
				},
			},
		},
	}
	payload := webhookPayload{
		MsgType: "interactive",
		Card:    card,
	}
	if c.secret != "" {
		ts := fmt.Sprintf("%d", time.Now().Unix())
		payload.Timestamp = ts
		payload.Sign = sign(ts, c.secret)
	}
	return c.postJSON(ctx, c.webhookURL, payload)
}

func (c *Channel) postJSON(ctx context.Context, url string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu webhook status %d: %s", resp.StatusCode, trim(string(body), 200))
	}
	// Feishu returns {"code":0,...} even on HTTP 200
	var wr struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &wr); err == nil && wr.Code != 0 {
		return fmt.Errorf("feishu webhook code=%d msg=%s", wr.Code, wr.Msg)
	}
	return nil
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// sign implements Feishu custom bot signature:
// sign = base64(hmac_sha256(timestamp + "\n" + secret))
func sign(timestamp, secret string) string {
	// local import to keep file deps clear
	return feishuSign(timestamp, secret)
}
