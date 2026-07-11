package sub2api

import (
	"bytes"
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

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, out any) error {
	return c.doBody(ctx, method, path, query, nil, out)
}

func (c *Client) doBody(ctx context.Context, method, path string, query url.Values, payload any, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var bodyReader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	} else if c.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+c.jwt)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

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
	var env envelope
	if err := json.Unmarshal(body, &env); err == nil && (env.Data != nil || env.Code != 0 || env.Message != "") {
		if env.Code != 0 && env.Code != 200 && env.Code != http.StatusOK {
			if env.Data == nil {
				return fmt.Errorf("%s %s: api code=%d message=%s", method, path, env.Code, env.Message)
			}
		}
		if len(env.Data) > 0 && string(env.Data) != "null" {
			return json.Unmarshal(env.Data, out)
		}
		// success with empty data
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, out)
}

func (c *Client) post(ctx context.Context, path string, payload any, out any) error {
	return c.doBody(ctx, http.MethodPost, path, nil, payload, out)
}

func (c *Client) put(ctx context.Context, path string, payload any, out any) error {
	return c.doBody(ctx, http.MethodPut, path, nil, payload, out)
}

func (c *Client) delete(ctx context.Context, path string, out any) error {
	return c.doBody(ctx, http.MethodDelete, path, nil, nil, out)
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
	var env envelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Data) > 0 && string(env.Data) != "null" {
		return env.Data, nil
	}
	return body, nil
}

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

type DashboardStats struct {
	TotalUsers        int64   `json:"total_users"`
	TodayNewUsers     int64   `json:"today_new_users"`
	ActiveUsers       int64   `json:"active_users"`
	HourlyActiveUsers int64   `json:"hourly_active_users"`
	TotalAPIKeys      int64   `json:"total_api_keys"`
	ActiveAPIKeys     int64   `json:"active_api_keys"`
	TotalAccounts     int64   `json:"total_accounts"`
	NormalAccounts    int64   `json:"normal_accounts"`
	ErrorAccounts     int64   `json:"error_accounts"`
	RatelimitAccounts int64   `json:"ratelimit_accounts"`
	OverloadAccounts  int64   `json:"overload_accounts"`
	TotalRequests     int64   `json:"total_requests"`
	TodayRequests     int64   `json:"today_requests"`
	TodayTokens       int64   `json:"today_tokens"`
	TodayCost         float64 `json:"today_cost"`
	TodayActualCost   float64 `json:"today_actual_cost"`
	TotalTokens       int64   `json:"total_tokens"`
	TotalCost         float64 `json:"total_cost"`
	RPM               float64 `json:"rpm"`
	TPM               float64 `json:"tpm"`
	AverageDurationMS float64 `json:"average_duration_ms"`
	Uptime            int64   `json:"uptime"`
	StatsStale        bool    `json:"stats_stale"`
}

func (c *Client) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	var out DashboardStats
	if err := c.get(ctx, "/api/v1/admin/dashboard/stats", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type Account struct {
	ID                      int64      `json:"id"`
	Name                    string     `json:"name"`
	Platform                string     `json:"platform"`
	Type                    string     `json:"type"`
	Status                  string     `json:"status"`
	ErrorMessage            string     `json:"error_message"`
	Schedulable             bool       `json:"schedulable"`
	RateLimitedAt           *time.Time `json:"rate_limited_at"`
	RateLimitResetAt        *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil           *time.Time `json:"overload_until"`
	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason"`
}

type PageMeta struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

// AccountListFilter controls admin accounts list query params.
type AccountListFilter struct {
	Status      string // active|error|...
	Search      string // name/email keyword (API: search)
	Platform    string // openai|anthropic|...
	Schedulable *bool
}

func (c *Client) ListAccounts(ctx context.Context, page, pageSize int, status string) ([]Account, int64, error) {
	return c.ListAccountsEx(ctx, page, pageSize, AccountListFilter{Status: status})
}

func (c *Client) ListAccountsEx(ctx context.Context, page, pageSize int, f AccountListFilter) ([]Account, int64, error) {
	return c.listAccountsRaw(ctx, page, pageSize, f)
}

func (c *Client) listAccountsRaw(ctx context.Context, page, pageSize int, f AccountListFilter) ([]Account, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.Search != "" {
		q.Set("search", f.Search)
	}
	if f.Platform != "" {
		q.Set("platform", f.Platform)
	}
	if f.Schedulable != nil {
		if *f.Schedulable {
			q.Set("schedulable", "true")
		} else {
			q.Set("schedulable", "false")
		}
	}
	body, err := c.getRaw(ctx, "/api/v1/admin/accounts", q)
	if err != nil {
		return nil, 0, err
	}
	return parseAccountList(body)
}

func parseAccountList(body []byte) ([]Account, int64, error) {
	var a struct {
		Items []Account `json:"items"`
		Total int64     `json:"total"`
	}
	if err := json.Unmarshal(body, &a); err == nil && a.Items != nil {
		return a.Items, a.Total, nil
	}
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
	var arr []Account
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, int64(len(arr)), nil
	}
	return nil, 0, fmt.Errorf("unrecognized accounts list shape: %s", truncate(string(body), 200))
}

func (c *Client) ListAllAccounts(ctx context.Context, pageSize int, status string) ([]Account, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	var all []Account
	page := 1
	for {
		items, total, err := c.listAccountsRaw(ctx, page, pageSize, AccountListFilter{Status: status})
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if int64(len(all)) >= total || len(items) == 0 {
			break
		}
		page++
		if page > 1000 {
			break
		}
	}
	return all, nil
}

func (c *Client) GetAccount(ctx context.Context, id int64) (*Account, error) {
	var out Account
	if err := c.get(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type UsageProgress struct {
	Utilization      float64      `json:"utilization"`
	ResetsAt         *time.Time   `json:"resets_at"`
	RemainingSeconds int          `json:"remaining_seconds"`
	UsedRequests     int64        `json:"used_requests,omitempty"`
	LimitRequests    int64        `json:"limit_requests,omitempty"`
	WindowStats      *WindowStats `json:"window_stats,omitempty"`
}

type WindowStats struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	StandardCost float64 `json:"standard_cost"`
	UserCost     float64 `json:"user_cost"`
}

type AntigravityModelQuota struct {
	Utilization int    `json:"utilization"`
	ResetTime   string `json:"reset_time"`
}

type UsageInfo struct {
	Source            string                            `json:"source,omitempty"`
	UpdatedAt         *time.Time                        `json:"updated_at,omitempty"`
	FiveHour          *UsageProgress                    `json:"five_hour"`
	SevenDay          *UsageProgress                    `json:"seven_day,omitempty"`
	SevenDaySonnet    *UsageProgress                    `json:"seven_day_sonnet,omitempty"`
	SevenDayFable     *UsageProgress                    `json:"seven_day_fable,omitempty"`
	GeminiSharedDaily *UsageProgress                    `json:"gemini_shared_daily,omitempty"`
	GeminiProDaily    *UsageProgress                    `json:"gemini_pro_daily,omitempty"`
	GeminiFlashDaily  *UsageProgress                    `json:"gemini_flash_daily,omitempty"`
	AntigravityQuota  map[string]*AntigravityModelQuota `json:"antigravity_quota,omitempty"`
	Error             string                            `json:"error,omitempty"`
	ErrorCode         string                            `json:"error_code,omitempty"`
}

type WindowValue struct {
	Window      string
	Utilization float64
	ResetsAt    *time.Time
	Remaining   int
	Stats       *WindowStats
}

func (u *UsageInfo) Windows() []WindowValue {
	if u == nil {
		return nil
	}
	out := make([]WindowValue, 0, 8)
	add := func(name string, p *UsageProgress) {
		if p == nil {
			return
		}
		out = append(out, WindowValue{
			Window:      name,
			Utilization: p.Utilization,
			ResetsAt:    p.ResetsAt,
			Remaining:   p.RemainingSeconds,
			Stats:       p.WindowStats,
		})
	}
	add("five_hour", u.FiveHour)
	add("seven_day", u.SevenDay)
	add("seven_day_sonnet", u.SevenDaySonnet)
	add("seven_day_fable", u.SevenDayFable)
	add("gemini_shared_daily", u.GeminiSharedDaily)
	add("gemini_pro_daily", u.GeminiProDaily)
	add("gemini_flash_daily", u.GeminiFlashDaily)
	for model, q := range u.AntigravityQuota {
		if q == nil {
			continue
		}
		out = append(out, WindowValue{
			Window:      "antigravity:" + model,
			Utilization: float64(q.Utilization),
		})
	}
	return out
}

func (u *UsageInfo) Window(name string) (WindowValue, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return WindowValue{}, false
	}
	if name == "max" {
		var best WindowValue
		found := false
		for _, w := range u.Windows() {
			if !found || w.Utilization > best.Utilization {
				best = w
				found = true
			}
		}
		if found {
			best.Window = "max(" + best.Window + ")"
		}
		return best, found
	}
	for _, w := range u.Windows() {
		if strings.EqualFold(w.Window, name) {
			return w, true
		}
		if name == "5h" && w.Window == "five_hour" {
			return w, true
		}
		if name == "7d" && w.Window == "seven_day" {
			return w, true
		}
	}
	return WindowValue{}, false
}

func (c *Client) GetAccountUsage(ctx context.Context, id int64, source string, force bool) (*UsageInfo, error) {
	q := url.Values{}
	if source != "" {
		q.Set("source", source)
	}
	if force {
		q.Set("force", "true")
	}
	var out UsageInfo
	if err := c.get(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/usage", id), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetAccountTodayStats(ctx context.Context, id int64) (*WindowStats, error) {
	var out WindowStats
	if err := c.get(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/today-stats", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type AvailabilityBucket struct {
	// legacy / alternate shapes
	Total          int `json:"total"`
	Available      int `json:"available"`
	Error          int `json:"error"`
	RateLimit      int `json:"rate_limit"`
	Overload       int `json:"overload"`
	Disabled       int `json:"disabled"`
	AvailableCount int `json:"available_count"`
	TotalCount     int `json:"total_count"`
	// current Sub2API ops shape
	TotalAccounts  int `json:"total_accounts"`
	RateLimitCount int `json:"rate_limit_count"`
	ErrorCount     int `json:"error_count"`
	// labels (optional)
	Platform  string `json:"platform,omitempty"`
	GroupID   int64  `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

func (b AvailabilityBucket) AvailableNum() int {
	if b.Available > 0 {
		return b.Available
	}
	if b.AvailableCount > 0 {
		return b.AvailableCount
	}
	return b.AvailableCount
}

func (b AvailabilityBucket) TotalNum() int {
	if b.Total > 0 {
		return b.Total
	}
	if b.TotalAccounts > 0 {
		return b.TotalAccounts
	}
	return b.TotalCount
}

func (b AvailabilityBucket) ErrorNum() int {
	if b.ErrorCount > 0 {
		return b.ErrorCount
	}
	return b.Error
}

func (b AvailabilityBucket) RateLimitNum() int {
	if b.RateLimitCount > 0 {
		return b.RateLimitCount
	}
	return b.RateLimit
}

// AccountRuntimeStatus is per-account availability detail from ops endpoint.
type AccountRuntimeStatus struct {
	AccountID             int64  `json:"account_id"`
	AccountName           string `json:"account_name"`
	Platform              string `json:"platform"`
	GroupID               int64  `json:"group_id"`
	GroupName             string `json:"group_name"`
	Status                string `json:"status"`
	IsAvailable           bool   `json:"is_available"`
	IsRateLimited         bool   `json:"is_rate_limited"`
	IsOverloaded          bool   `json:"is_overloaded"`
	HasError              bool   `json:"has_error"`
	RateLimitRemainingSec *int64 `json:"rate_limit_remaining_sec"`
	OverloadRemainingSec  *int64 `json:"overload_remaining_sec"`
	ErrorMessage          string `json:"error_message"`
}

type AccountAvailability struct {
	Enabled   bool                            `json:"enabled"`
	Platform  map[string]AvailabilityBucket   `json:"platform"`
	Group     map[string]AvailabilityBucket   `json:"group"`
	Account   map[string]AccountRuntimeStatus `json:"account"`
	Timestamp time.Time                       `json:"timestamp"`
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

type AlertEvent struct {
	ID             int64      `json:"id"`
	RuleID         int64      `json:"rule_id"`
	RuleName       string     `json:"rule_name"`
	Name           string     `json:"name"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	MetricType     string     `json:"metric_type"`
	Severity       string     `json:"severity"`
	Status         string     `json:"status"`
	Message        string     `json:"message"`
	Value          float64    `json:"value"`
	Threshold      float64    `json:"threshold"`
	MetricValue    float64    `json:"metric_value"`
	ThresholdValue float64    `json:"threshold_value"`
	FiredAt        time.Time  `json:"fired_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (e AlertEvent) DisplayTitle() string {
	for _, s := range []string{e.Title, e.RuleName, e.Name, e.MetricType} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return "alert"
}

func (e AlertEvent) DisplayMessage() string {
	for _, s := range []string{e.Description, e.Message} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func (e AlertEvent) Metric() float64 {
	if e.MetricValue != 0 {
		return e.MetricValue
	}
	return e.Value
}

func (e AlertEvent) ThresholdVal() float64 {
	if e.ThresholdValue != 0 {
		return e.ThresholdValue
	}
	return e.Threshold
}

func (c *Client) ListAlertEvents(ctx context.Context, page, pageSize int) ([]AlertEvent, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	body, err := c.getRaw(ctx, "/api/v1/admin/ops/alert-events", q)
	if err != nil {
		return nil, err
	}
	// live API often returns a bare array after envelope unwrap
	var arr []AlertEvent
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, nil
	}
	var a struct {
		Items []AlertEvent `json:"items"`
		Data  []AlertEvent `json:"data"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, err
	}
	if a.Items != nil {
		return a.Items, nil
	}
	return a.Data, nil
}

type TrafficMetric struct {
	Current float64 `json:"current"`
	Peak    float64 `json:"peak"`
	Avg     float64 `json:"avg"`
}

type TrafficSummary struct {
	Enabled bool   `json:"enabled"`
	Window  string `json:"window"`
	// flat / legacy fields
	QPS     float64 `json:"qps"`
	Current struct {
		QPS float64 `json:"qps"`
		TPS float64 `json:"tps"`
	} `json:"current"`
	Avg struct {
		QPS float64 `json:"qps"`
	} `json:"avg"`
	// nested ops shape: data.summary.qps.current
	Summary struct {
		Window string        `json:"window"`
		QPS    TrafficMetric `json:"qps"`
		TPS    TrafficMetric `json:"tps"`
	} `json:"summary"`
	Timestamp time.Time `json:"timestamp"`
}

func (t TrafficSummary) CurrentQPS() float64 {
	if t.Summary.QPS.Current > 0 {
		return t.Summary.QPS.Current
	}
	if t.Summary.QPS.Avg > 0 {
		return t.Summary.QPS.Avg
	}
	if t.QPS > 0 {
		return t.QPS
	}
	if t.Current.QPS > 0 {
		return t.Current.QPS
	}
	return t.Avg.QPS
}

func (t TrafficSummary) WindowLabel() string {
	if t.Summary.Window != "" {
		return t.Summary.Window
	}
	return t.Window
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
