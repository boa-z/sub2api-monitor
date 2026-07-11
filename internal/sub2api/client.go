package sub2api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
)

type Client struct {
	baseURL string
	apiKey  string
	jwt     string
	http    *http.Client
}

func NewClient(cfg config.Sub2APIConfig) (*Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.AdminAPIKey,
		jwt:     cfg.JWT,
		http: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: tr,
		},
	}, nil
}

// ----- generic envelope -----

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	} else if c.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+c.jwt)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d body=%s", method, path, resp.StatusCode, truncate(string(body), 300))
	}
	if out == nil {
		return nil
	}
	// Try envelope first: {code, message, data}
	var env envelope
	if err := json.Unmarshal(body, &env); err == nil && (env.Data != nil || env.Code != 0 || env.Message != "") {
		if env.Code != 0 && env.Code != 200 && env.Code != http.StatusOK {
			// some APIs use code=0 for success; others use 200
			if env.Data == nil {
				return fmt.Errorf("%s %s: api code=%d message=%s", method, path, env.Code, env.Message)
			}
		}
		if len(env.Data) > 0 && string(env.Data) != "null" {
			return json.Unmarshal(env.Data, out)
		}
	}
	// Fallback: raw JSON body
	return json.Unmarshal(body, out)
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, out)
}

// Health hits /health without auth.
func (c *Client) Health(ctx context.Context) error {
	u := c.baseURL + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

// ----- dashboard -----

type DashboardStats struct {
	TotalUsers         int64 `json:"total_users"`
	TodayNewUsers      int64 `json:"today_new_users"`
	ActiveUsers        int64 `json:"active_users"`
	TotalAPIKeys       int64 `json:"total_api_keys"`
	ActiveAPIKeys      int64 `json:"active_api_keys"`
	TotalAccounts      int64 `json:"total_accounts"`
	NormalAccounts     int64 `json:"normal_accounts"`
	ErrorAccounts      int64 `json:"error_accounts"`
	RatelimitAccounts  int64 `json:"ratelimit_accounts"`
	OverloadAccounts   int64 `json:"overload_accounts"`
	TotalRequests      int64 `json:"total_requests"`
	Uptime             int64 `json:"uptime"`
}

func (c *Client) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	var out DashboardStats
	if err := c.get(ctx, "/api/v1/admin/dashboard/stats", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- accounts -----

type Account struct {
	ID                     int64      `json:"id"`
	Name                   string     `json:"name"`
	Platform               string     `json:"platform"`
	Type                   string     `json:"type"`
	Status                 string     `json:"status"`
	ErrorMessage           string     `json:"error_message"`
	Schedulable            bool       `json:"schedulable"`
	RateLimitedAt          *time.Time `json:"rate_limited_at"`
	RateLimitResetAt       *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil          *time.Time `json:"overload_until"`
	TempUnschedulableUntil *time.Time `json:"temp_unschedulable_until"`
	TempUnschedulableReason string    `json:"temp_unschedulable_reason"`
}

type PageMeta struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

type AccountList struct {
	Items      []Account `json:"items"`
	Total      int64     `json:"total"`
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
	// alternate shapes
	Data       []Account `json:"data"`
	Pagination *PageMeta `json:"pagination"`
}

func (c *Client) ListAccounts(ctx context.Context, page, pageSize int, status string) ([]Account, int64, error) {
	return c.listAccountsRaw(ctx, page, pageSize, status)
}

func (c *Client) listAccountsRaw(ctx context.Context, page, pageSize int, status string) ([]Account, int64, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	if status != "" {
		q.Set("status", status)
	}

	// Use internal request returning body
	body, err := c.getRaw(ctx, "/api/v1/admin/accounts", q)
	if err != nil {
		return nil, 0, err
	}
	return parseAccountList(body)
}

func (c *Client) getRaw(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	} else if c.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+c.jwt)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d body=%s", path, resp.StatusCode, truncate(string(body), 300))
	}
	// unwrap envelope if present
	var env envelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Data) > 0 && string(env.Data) != "null" {
		return env.Data, nil
	}
	return body, nil
}

func parseAccountList(body []byte) ([]Account, int64, error) {
	// shape A: {items:[], total, page, page_size}
	var a struct {
		Items    []Account `json:"items"`
		Total    int64     `json:"total"`
		Page     int       `json:"page"`
		PageSize int       `json:"page_size"`
	}
	if err := json.Unmarshal(body, &a); err == nil && a.Items != nil {
		return a.Items, a.Total, nil
	}
	// shape B: {data:[], pagination:{total,...}}
	var b struct {
		Data       []Account `json:"data"`
		Pagination *PageMeta `json:"pagination"`
	}
	if err := json.Unmarshal(body, &b); err == nil && b.Data != nil {
		total := int64(len(b.Data))
		if b.Pagination != nil {
			total = b.Pagination.Total
		}
		return b.Data, total, nil
	}
	// shape C: bare array
	var arr []Account
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, int64(len(arr)), nil
	}
	return nil, 0, fmt.Errorf("unrecognized accounts list shape: %s", truncate(string(body), 200))
}

// ListAllAccounts pages through accounts.
func (c *Client) ListAllAccounts(ctx context.Context, pageSize int, status string) ([]Account, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	var all []Account
	page := 1
	for {
		items, total, err := c.listAccountsRaw(ctx, page, pageSize, status)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if int64(len(all)) >= total || len(items) == 0 {
			break
		}
		page++
		if page > 1000 { // safety
			break
		}
	}
	return all, nil
}

// ----- availability -----

type AvailabilityBucket struct {
	Total      int `json:"total"`
	Available  int `json:"available"`
	Error      int `json:"error"`
	RateLimit  int `json:"rate_limit"`
	Overload   int `json:"overload"`
	Disabled   int `json:"disabled"`
	// alternate field names
	AvailableCount int `json:"available_count"`
	TotalCount     int `json:"total_count"`
}

func (b AvailabilityBucket) AvailableNum() int {
	if b.Available > 0 {
		return b.Available
	}
	return b.AvailableCount
}

func (b AvailabilityBucket) TotalNum() int {
	if b.Total > 0 {
		return b.Total
	}
	return b.TotalCount
}

type AccountAvailability struct {
	Enabled   bool                          `json:"enabled"`
	Platform  map[string]AvailabilityBucket `json:"platform"`
	Group     map[string]AvailabilityBucket `json:"group"`
	Timestamp time.Time                     `json:"timestamp"`
}

func (c *Client) GetAccountAvailability(ctx context.Context) (*AccountAvailability, error) {
	body, err := c.getRaw(ctx, "/api/v1/admin/ops/account-availability", nil)
	if err != nil {
		return nil, err
	}
	var out AccountAvailability
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- ops alert events -----

type AlertEvent struct {
	ID         int64     `json:"id"`
	RuleID     int64     `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	Name       string    `json:"name"`
	MetricType string    `json:"metric_type"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"`
	Message    string    `json:"message"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	FiredAt    time.Time `json:"fired_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (c *Client) ListAlertEvents(ctx context.Context, page, pageSize int) ([]AlertEvent, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	body, err := c.getRaw(ctx, "/api/v1/admin/ops/alert-events", q)
	if err != nil {
		return nil, err
	}
	// flexible
	var a struct {
		Items []AlertEvent `json:"items"`
		Data  []AlertEvent `json:"data"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		var arr []AlertEvent
		if err2 := json.Unmarshal(body, &arr); err2 == nil {
			return arr, nil
		}
		return nil, err
	}
	if a.Items != nil {
		return a.Items, nil
	}
	return a.Data, nil
}

// ----- traffic -----

type TrafficSummary struct {
	Enabled bool    `json:"enabled"`
	Window  string  `json:"window"`
	QPS     float64 `json:"qps"`
	// nested variants
	Current struct {
		QPS float64 `json:"qps"`
		TPS float64 `json:"tps"`
	} `json:"current"`
	Avg struct {
		QPS float64 `json:"qps"`
	} `json:"avg"`
}

func (t TrafficSummary) CurrentQPS() float64 {
	if t.QPS > 0 {
		return t.QPS
	}
	if t.Current.QPS > 0 {
		return t.Current.QPS
	}
	return t.Avg.QPS
}

func (c *Client) GetRealtimeTraffic(ctx context.Context, window string) (*TrafficSummary, error) {
	q := url.Values{}
	if window != "" {
		q.Set("window", window)
	}
	body, err := c.getRaw(ctx, "/api/v1/admin/ops/realtime-traffic", q)
	if err != nil {
		return nil, err
	}
	var out TrafficSummary
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
