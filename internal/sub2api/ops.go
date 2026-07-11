package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// RealtimeDashboard is a lightweight live snapshot.
type RealtimeDashboard struct {
	ActiveRequests      int64   `json:"active_requests"`
	AverageResponseTime float64 `json:"average_response_time"`
	ErrorRate           float64 `json:"error_rate"`
	RequestsPerMinute   float64 `json:"requests_per_minute"`
}

func (c *Client) GetRealtimeDashboard(ctx context.Context) (*RealtimeDashboard, error) {
	var out RealtimeDashboard
	if err := c.get(ctx, "/api/v1/admin/dashboard/realtime", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConcurrencyBucket is load for a platform/group/account.
type ConcurrencyBucket struct {
	Platform       string  `json:"platform,omitempty"`
	GroupID        int64   `json:"group_id,omitempty"`
	GroupName      string  `json:"group_name,omitempty"`
	AccountID      int64   `json:"account_id,omitempty"`
	AccountName    string  `json:"account_name,omitempty"`
	CurrentInUse   int     `json:"current_in_use"`
	MaxCapacity    int     `json:"max_capacity"`
	LoadPercentage float64 `json:"load_percentage"`
	WaitingInQueue int     `json:"waiting_in_queue"`
}

type ConcurrencySnapshot struct {
	Enabled   bool                         `json:"enabled"`
	Platform  map[string]ConcurrencyBucket `json:"platform"`
	Group     map[string]ConcurrencyBucket `json:"group"`
	Account   map[string]ConcurrencyBucket `json:"account"`
	Timestamp time.Time                    `json:"timestamp"`
}

func (c *Client) GetConcurrency(ctx context.Context) (*ConcurrencySnapshot, error) {
	body, err := c.getRaw(ctx, "/api/v1/admin/ops/concurrency", nil)
	if err != nil {
		return nil, err
	}
	var out ConcurrencySnapshot
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OpsError is a request/upstream error event from ops APIs.
type OpsError struct {
	ID             int64      `json:"id"`
	CreatedAt      time.Time  `json:"created_at"`
	Phase          string     `json:"phase"`
	Type           string     `json:"type"`
	ErrorOwner     string     `json:"error_owner"`
	ErrorSource    string     `json:"error_source"`
	Severity       string     `json:"severity"`
	StatusCode     int        `json:"status_code"`
	Platform       string     `json:"platform"`
	Model          string     `json:"model"`
	Resolved       bool       `json:"resolved"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	Message        string     `json:"message"`
	UserID         int64      `json:"user_id"`
	UserEmail      string     `json:"user_email"`
	APIKeyID       int64      `json:"api_key_id"`
	APIKeyName     string     `json:"api_key_name"`
	AccountID      int64      `json:"account_id"`
	AccountName    string     `json:"account_name"`
	GroupID        int64      `json:"group_id"`
	GroupName      string     `json:"group_name"`
	RequestPath    string     `json:"request_path"`
	RequestID      string     `json:"request_id"`
	RequestedModel string     `json:"requested_model"`
}

type OpsErrorPage struct {
	Items    []OpsError `json:"items"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

func (c *Client) ListRequestErrors(ctx context.Context, page, pageSize int) (*OpsErrorPage, error) {
	return c.listOpsErrors(ctx, "/api/v1/admin/ops/request-errors", page, pageSize)
}

func (c *Client) ListUpstreamErrors(ctx context.Context, page, pageSize int) (*OpsErrorPage, error) {
	return c.listOpsErrors(ctx, "/api/v1/admin/ops/upstream-errors", page, pageSize)
}

func (c *Client) listOpsErrors(ctx context.Context, path string, page, pageSize int) (*OpsErrorPage, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	body, err := c.getRaw(ctx, path, q)
	if err != nil {
		return nil, err
	}
	// shapes: {items,total,...} | [] | {data:[]}
	var pageOut OpsErrorPage
	if err := json.Unmarshal(body, &pageOut); err == nil && (pageOut.Items != nil || pageOut.Total > 0) {
		return &pageOut, nil
	}
	var arr []OpsError
	if err := json.Unmarshal(body, &arr); err == nil {
		return &OpsErrorPage{Items: arr, Total: int64(len(arr)), Page: page, PageSize: pageSize}, nil
	}
	var wrap struct {
		Items []OpsError `json:"items"`
		Data  []OpsError `json:"data"`
		Total int64      `json:"total"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("parse ops errors: %w body=%s", err, truncate(string(body), 200))
	}
	items := wrap.Items
	if items == nil {
		items = wrap.Data
	}
	return &OpsErrorPage{Items: items, Total: wrap.Total, Page: page, PageSize: pageSize}, nil
}

// ChannelMonitor is an active channel probe task.
type ChannelMonitor struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Provider         string     `json:"provider"`
	APIMode          string     `json:"api_mode"`
	Endpoint         string     `json:"endpoint"`
	PrimaryModel     string     `json:"primary_model"`
	Enabled          bool       `json:"enabled"`
	IntervalSeconds  int        `json:"interval_seconds"`
	LastCheckedAt    *time.Time `json:"last_checked_at"`
	PrimaryStatus    string     `json:"primary_status"`
	PrimaryLatencyMS int64      `json:"primary_latency_ms"`
	Availability7d   float64    `json:"availability_7d"`
}

func (c *Client) ListChannelMonitors(ctx context.Context) ([]ChannelMonitor, error) {
	body, err := c.getRaw(ctx, "/api/v1/admin/channel-monitors", nil)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Items []ChannelMonitor `json:"items"`
		Data  []ChannelMonitor `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err == nil {
		if wrap.Items != nil {
			return wrap.Items, nil
		}
		if wrap.Data != nil {
			return wrap.Data, nil
		}
	}
	var arr []ChannelMonitor
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("unrecognized channel-monitors shape: %s", truncate(string(body), 200))
}

// ListAlertEventsPage returns events with robust parsing for array-or-page payloads.
func (c *Client) ListAlertEventsPage(ctx context.Context, page, pageSize int) ([]AlertEvent, error) {
	return c.ListAlertEvents(ctx, page, pageSize)
}
