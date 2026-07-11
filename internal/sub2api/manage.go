package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// SetSchedulable enables/disables account scheduling.
func (c *Client) SetSchedulable(ctx context.Context, id int64, schedulable bool) (*Account, error) {
	var out Account
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/schedulable", id), map[string]any{
		"schedulable": schedulable,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearAccountError clears sticky error state on an account.
func (c *Client) ClearAccountError(ctx context.Context, id int64) (*Account, error) {
	var out Account
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/clear-error", id), map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearAccountRateLimit clears rate-limit hold on an account.
func (c *Client) ClearAccountRateLimit(ctx context.Context, id int64) (*Account, error) {
	var out Account
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/clear-rate-limit", id), map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RecoverAccountState attempts to recover account runtime state.
func (c *Client) RecoverAccountState(ctx context.Context, id int64) (*Account, error) {
	var out Account
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/recover-state", id), map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefreshAccount refreshes credentials / runtime info from upstream where supported.
func (c *Client) RefreshAccount(ctx context.Context, id int64) (*Account, error) {
	var out Account
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/refresh", id), map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearTempUnschedulable removes temporary unschedulable hold.
func (c *Client) ClearTempUnschedulable(ctx context.Context, id int64) error {
	return c.delete(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/temp-unschedulable", id), nil)
}

// SetTempUnschedulable marks an account temporarily unschedulable until the given time/reason.
// durationSec > 0 preferred; until may be used by servers that accept absolute time.
func (c *Client) SetTempUnschedulable(ctx context.Context, id int64, durationSec int64, reason string) (*TempUnschedulableInfo, error) {
	payload := map[string]any{}
	if durationSec > 0 {
		payload["duration_seconds"] = durationSec
		payload["duration"] = durationSec
	}
	if reason != "" {
		payload["reason"] = reason
	}
	var out TempUnschedulableInfo
	// Sub2API commonly uses POST for temp hold
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/temp-unschedulable", id), payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TempUnschedulableInfo reports temporary unschedulable status.
type TempUnschedulableInfo struct {
	Active bool `json:"active"`
}

func (c *Client) GetTempUnschedulable(ctx context.Context, id int64) (*TempUnschedulableInfo, error) {
	var out TempUnschedulableInfo
	if err := c.get(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/temp-unschedulable", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Group is a scheduling/billing group.
type Group struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	Platform       string  `json:"platform"`
	Status         string  `json:"status"`
	RateMultiplier float64 `json:"rate_multiplier"`
	IsExclusive    bool    `json:"is_exclusive"`
}

// User is an end-user of Sub2API.
type User struct {
	ID                 int64   `json:"id"`
	Email              string  `json:"email"`
	Username           string  `json:"username"`
	Role               string  `json:"role"`
	Balance            float64 `json:"balance"`
	FrozenBalance      float64 `json:"frozen_balance"`
	Concurrency        int     `json:"concurrency"`
	CurrentConcurrency int     `json:"current_concurrency"`
	Status             string  `json:"status"`
	RPMLimit           int     `json:"rpm_limit"`
	Notes              string  `json:"notes"`
}

// UserListFilter controls admin users list query params.
type UserListFilter struct {
	Search string // email/username/id keyword (API: search / q)
	Status string // active|disabled|...
	Role   string // admin|user|...
}

// GroupListFilter controls admin groups list query params.
type GroupListFilter struct {
	Search   string
	Platform string
	Status   string
}

func (c *Client) ListGroups(ctx context.Context, page, pageSize int) ([]Group, int64, error) {
	return c.ListGroupsEx(ctx, page, pageSize, GroupListFilter{})
}

func (c *Client) ListGroupsEx(ctx context.Context, page, pageSize int, f GroupListFilter) ([]Group, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	if f.Search != "" {
		q.Set("search", f.Search)
		q.Set("q", f.Search)
		q.Set("keyword", f.Search)
	}
	if f.Platform != "" {
		q.Set("platform", f.Platform)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	body, err := c.getRaw(ctx, "/api/v1/admin/groups", q)
	if err != nil {
		return nil, 0, err
	}
	items, total, err := parseNamedList[Group](body)
	if err != nil {
		return nil, 0, err
	}
	if f.Search == "" {
		return items, total, nil
	}
	if listLooksGroupSearchAware(items, f.Search) {
		return items, total, nil
	}
	// API ignored search — limited client-side scan.
	return c.searchGroupsLocal(ctx, page, pageSize, f.Search)
}

func (c *Client) ListUsers(ctx context.Context, page, pageSize int) ([]User, int64, error) {
	return c.ListUsersEx(ctx, page, pageSize, UserListFilter{})
}

func (c *Client) ListUsersEx(ctx context.Context, page, pageSize int, f UserListFilter) ([]User, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	if f.Search != "" {
		q.Set("search", f.Search)
		q.Set("q", f.Search)
		q.Set("keyword", f.Search)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.Role != "" {
		q.Set("role", f.Role)
	}
	body, err := c.getRaw(ctx, "/api/v1/admin/users", q)
	if err != nil {
		return nil, 0, err
	}
	items, total, err := parseNamedList[User](body)
	if err != nil {
		return nil, 0, err
	}
	if f.Search == "" {
		return items, total, nil
	}
	if listLooksUserSearchAware(items, f.Search) {
		return items, total, nil
	}
	// API ignored search — limited client-side scan.
	return c.searchUsersLocal(ctx, page, pageSize, f.Search)
}

// GetUser fetches one instance user. Tries detail endpoint, then list/search fallback.
func (c *Client) GetUser(ctx context.Context, id int64) (*User, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid user id")
	}
	var out User
	if err := c.get(ctx, fmt.Sprintf("/api/v1/admin/users/%d", id), nil, &out); err == nil && out.ID != 0 {
		return &out, nil
	}
	// Fallback: search by id string then exact match.
	items, _, err := c.ListUsersEx(ctx, 1, 50, UserListFilter{Search: strconv.FormatInt(id, 10)})
	if err == nil {
		for i := range items {
			if items[i].ID == id {
				u := items[i]
				return &u, nil
			}
		}
	}
	// Last resort: scan first pages without search.
	for page := 1; page <= 5; page++ {
		items, total, err := c.ListUsers(ctx, page, 50)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if items[i].ID == id {
				u := items[i]
				return &u, nil
			}
		}
		if int64(page*50) >= total || len(items) == 0 {
			break
		}
	}
	return nil, fmt.Errorf("user #%d not found", id)
}

// GetGroup fetches one group. Tries detail endpoint, then list/search fallback.
func (c *Client) GetGroup(ctx context.Context, id int64) (*Group, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid group id")
	}
	var out Group
	if err := c.get(ctx, fmt.Sprintf("/api/v1/admin/groups/%d", id), nil, &out); err == nil && out.ID != 0 {
		return &out, nil
	}
	items, _, err := c.ListGroupsEx(ctx, 1, 50, GroupListFilter{Search: strconv.FormatInt(id, 10)})
	if err == nil {
		for i := range items {
			if items[i].ID == id {
				g := items[i]
				return &g, nil
			}
		}
	}
	for page := 1; page <= 5; page++ {
		items, total, err := c.ListGroups(ctx, page, 50)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if items[i].ID == id {
				g := items[i]
				return &g, nil
			}
		}
		if int64(page*50) >= total || len(items) == 0 {
			break
		}
	}
	return nil, fmt.Errorf("group #%d not found", id)
}

func userMatchesSearch(u User, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	if strconv.FormatInt(u.ID, 10) == q {
		return true
	}
	blob := strings.ToLower(strings.Join([]string{u.Email, u.Username, u.Role, u.Status, u.Notes}, " "))
	return strings.Contains(blob, q)
}

func groupMatchesSearch(g Group, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	if strconv.FormatInt(g.ID, 10) == q {
		return true
	}
	blob := strings.ToLower(strings.Join([]string{g.Name, g.Description, g.Platform, g.Status}, " "))
	return strings.Contains(blob, q)
}

func listLooksUserSearchAware(items []User, q string) bool {
	if len(items) == 0 {
		return true
	}
	for _, u := range items {
		if !userMatchesSearch(u, q) {
			return false
		}
	}
	return true
}

func listLooksGroupSearchAware(items []Group, q string) bool {
	if len(items) == 0 {
		return true
	}
	for _, g := range items {
		if !groupMatchesSearch(g, q) {
			return false
		}
	}
	return true
}

func (c *Client) searchUsersLocal(ctx context.Context, page, pageSize int, q string) ([]User, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var matched []User
	const maxScan = 10
	for p := 1; p <= maxScan; p++ {
		items, total, err := c.listUsersRawNoSearch(ctx, p, 50)
		if err != nil {
			return nil, 0, err
		}
		for _, u := range items {
			if userMatchesSearch(u, q) {
				matched = append(matched, u)
			}
		}
		if int64(p*50) >= total || len(items) == 0 {
			break
		}
	}
	total := int64(len(matched))
	start := (page - 1) * pageSize
	if start >= len(matched) {
		return []User{}, total, nil
	}
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}
	return matched[start:end], total, nil
}

func (c *Client) searchGroupsLocal(ctx context.Context, page, pageSize int, q string) ([]Group, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var matched []Group
	const maxScan = 10
	for p := 1; p <= maxScan; p++ {
		items, total, err := c.listGroupsRawNoSearch(ctx, p, 50)
		if err != nil {
			return nil, 0, err
		}
		for _, g := range items {
			if groupMatchesSearch(g, q) {
				matched = append(matched, g)
			}
		}
		if int64(p*50) >= total || len(items) == 0 {
			break
		}
	}
	total := int64(len(matched))
	start := (page - 1) * pageSize
	if start >= len(matched) {
		return []Group{}, total, nil
	}
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}
	return matched[start:end], total, nil
}

func (c *Client) listUsersRawNoSearch(ctx context.Context, page, pageSize int) ([]User, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	body, err := c.getRaw(ctx, "/api/v1/admin/users", q)
	if err != nil {
		return nil, 0, err
	}
	return parseNamedList[User](body)
}

func (c *Client) listGroupsRawNoSearch(ctx context.Context, page, pageSize int) ([]Group, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	body, err := c.getRaw(ctx, "/api/v1/admin/groups", q)
	if err != nil {
		return nil, 0, err
	}
	return parseNamedList[Group](body)
}

func parseNamedList[T any](body []byte) ([]T, int64, error) {
	var a struct {
		Items []T   `json:"items"`
		Total int64 `json:"total"`
		Data  []T   `json:"data"`
	}
	if err := json.Unmarshal(body, &a); err == nil {
		if a.Items != nil {
			total := a.Total
			if total == 0 {
				total = int64(len(a.Items))
			}
			return a.Items, total, nil
		}
		if a.Data != nil {
			total := a.Total
			if total == 0 {
				total = int64(len(a.Data))
			}
			return a.Data, total, nil
		}
	}
	var arr []T
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, int64(len(arr)), nil
	}
	return nil, 0, fmt.Errorf("unrecognized list shape: %s", truncate(string(body), 200))
}

// ListAccountsFiltered lists accounts with optional status filter.
func (c *Client) ListAccountsFiltered(ctx context.Context, page, pageSize int, status string) ([]Account, int64, error) {
	return c.ListAccountsEx(ctx, page, pageSize, AccountListFilter{Status: status})
}

// SetAccountStatus updates account status via PUT /accounts/:id {status}.
// Common values: active, disabled, error (server-defined).
func (c *Client) SetAccountStatus(ctx context.Context, id int64, status string) (*Account, error) {
	var out Account
	if err := c.put(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d", id), map[string]any{
		"status": status,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TestAccount runs an account connectivity test and returns raw result data.
func (c *Client) TestAccount(ctx context.Context, id int64) (json.RawMessage, error) {
	var m any
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/test", id), map[string]any{}, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return json.RawMessage(`{"ok":true}`), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ResetAccountQuota resets account quota counters when supported.
func (c *Client) ResetAccountQuota(ctx context.Context, id int64) (*Account, error) {
	var out Account
	if err := c.post(ctx, fmt.Sprintf("/api/v1/admin/accounts/%d/reset-quota", id), map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveOpsError marks an ops error resolved.
func (c *Client) ResolveOpsError(ctx context.Context, kind string, id int64) error {
	// kind: request|upstream|errors
	path := ""
	switch kind {
	case "request", "request-errors":
		path = fmt.Sprintf("/api/v1/admin/ops/request-errors/%d/resolve", id)
	case "upstream", "upstream-errors":
		path = fmt.Sprintf("/api/v1/admin/ops/upstream-errors/%d/resolve", id)
	default:
		path = fmt.Sprintf("/api/v1/admin/ops/errors/%d/resolve", id)
	}
	return c.post(ctx, path, map[string]any{}, nil)
}
