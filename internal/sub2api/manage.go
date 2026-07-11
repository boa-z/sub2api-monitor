package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
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

func (c *Client) ListGroups(ctx context.Context, page, pageSize int) ([]Group, int64, error) {
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

func (c *Client) ListUsers(ctx context.Context, page, pageSize int) ([]User, int64, error) {
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
	return c.ListAccounts(ctx, page, pageSize, status)
}
