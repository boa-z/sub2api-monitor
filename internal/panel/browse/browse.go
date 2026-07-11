// Package browse provides shared account-browser filters for Telegram/Discord panels.
package browse

import (
	"context"
	"strconv"
	"strings"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

// ParseFilter decodes browser status tokens.
// Forms: all|active|error|... | search:kw | plat:openai | plat:openai:active
func ParseFilter(status string) sub2api.AccountListFilter {
	f := sub2api.AccountListFilter{}
	s := strings.TrimSpace(status)
	if s == "" || s == "all" {
		return f
	}
	if strings.HasPrefix(s, "search:") {
		f.Search = strings.TrimPrefix(s, "search:")
		return f
	}
	if strings.HasPrefix(s, "plat:") {
		rest := strings.TrimPrefix(s, "plat:")
		parts := strings.SplitN(rest, ":", 2)
		f.Platform = parts[0]
		if len(parts) == 2 && parts[1] != "" && parts[1] != "all" {
			f.Status = parts[1]
		}
		return f
	}
	if s == "unsched" || s == "rate_limited" {
		// special client-side / API hybrid filters
		return f
	}
	f.Status = s
	return f
}

// ListAccounts returns one page of accounts for the manage browser.
// Special filters unsched/rate_limited prefer API query params, with client-side fallback scan.
func ListAccounts(ctx context.Context, cli *sub2api.Client, status string, page, pageSize int) ([]sub2api.Account, int64, error) {
	if page < 0 {
		page = 0
	}
	if pageSize <= 0 {
		pageSize = 8
	}
	switch status {
	case "unsched":
		falseV := false
		items, total, err := cli.ListAccountsEx(ctx, page+1, pageSize, sub2api.AccountListFilter{Schedulable: &falseV})
		if err == nil {
			filtered := make([]sub2api.Account, 0, len(items))
			for _, a := range items {
				if !a.Schedulable {
					filtered = append(filtered, a)
				}
			}
			if len(items) == 0 || len(filtered) == len(items) {
				return filtered, total, nil
			}
		}
		return ScanPage(ctx, cli, page, pageSize, func(a sub2api.Account) bool { return !a.Schedulable })
	case "rate_limited":
		items, total, err := cli.ListAccountsEx(ctx, page+1, pageSize, sub2api.AccountListFilter{Status: "rate_limited"})
		if err == nil && (total > 0 || len(items) > 0) {
			return items, total, nil
		}
		return ScanPage(ctx, cli, page, pageSize, IsRateLimited)
	default:
		f := ParseFilter(status)
		return cli.ListAccountsEx(ctx, page+1, pageSize, f)
	}
}

// IsRateLimited reports whether an account looks rate-limited or overloaded.
func IsRateLimited(a sub2api.Account) bool {
	if a.RateLimitedAt != nil || a.OverloadUntil != nil {
		return true
	}
	st := strings.ToLower(a.Status)
	return strings.Contains(st, "rate") || strings.Contains(st, "limit") || strings.Contains(st, "overload")
}

// ScanPage walks account list pages and returns the requested slice of matches.
func ScanPage(ctx context.Context, cli *sub2api.Client, page, pageSize int, match func(sub2api.Account) bool) ([]sub2api.Account, int64, error) {
	const scanPage = 50
	const maxScanPages = 10 // up to 500 accounts
	var matched []sub2api.Account
	for p := 1; p <= maxScanPages; p++ {
		items, tot, err := cli.ListAccountsEx(ctx, p, scanPage, sub2api.AccountListFilter{})
		if err != nil {
			return nil, 0, err
		}
		for _, a := range items {
			if match(a) {
				matched = append(matched, a)
			}
		}
		if len(items) < scanPage || int64(p*scanPage) >= tot {
			break
		}
	}
	start := page * pageSize
	if start >= len(matched) {
		return []sub2api.Account{}, int64(len(matched)), nil
	}
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}
	return matched[start:end], int64(len(matched)), nil
}

// Token encodes status for callback_data (avoid extra colons for search).
func Token(status string) string {
	s := strings.TrimSpace(status)
	if s == "" {
		return "all"
	}
	if strings.HasPrefix(s, "search:") {
		return "search|" + strings.TrimPrefix(s, "search:")
	}
	if strings.HasPrefix(s, "plat:") {
		return strings.ReplaceAll(s, ":", "|")
	}
	return s
}

// ParseCallback parses rest after mgr_browse:
// formats: all:0 | active:1 | search|kw:0 | plat|openai:0 | plat|openai|active:0
func ParseCallback(rest string) (status string, page int) {
	status = "all"
	page = 0
	if rest == "" {
		return
	}
	parts := strings.Split(rest, ":")
	if len(parts) == 1 {
		status = DecodeToken(parts[0])
		return
	}
	page, _ = strconv.Atoi(parts[len(parts)-1])
	token := strings.Join(parts[:len(parts)-1], ":")
	status = DecodeToken(token)
	return
}

// DecodeToken maps encoded callback tokens back to filter status.
func DecodeToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "all"
	}
	if strings.HasPrefix(token, "search|") {
		return "search:" + strings.TrimPrefix(token, "search|")
	}
	if strings.HasPrefix(token, "plat|") {
		return "plat:" + strings.ReplaceAll(strings.TrimPrefix(token, "plat|"), "|", ":")
	}
	if strings.HasPrefix(token, "plat:") {
		return token
	}
	return token
}

// Title returns a short human label for a filter token.
func Title(status string) string {
	title := strings.TrimSpace(status)
	switch {
	case title == "" || title == "all":
		return "全部"
	case strings.HasPrefix(title, "search:"):
		return "搜索:" + strings.TrimPrefix(title, "search:")
	case strings.HasPrefix(title, "plat:"):
		return "平台:" + strings.TrimPrefix(title, "plat:")
	case title == "unsched":
		return "停调度"
	case title == "rate_limited":
		return "限速"
	default:
		return title
	}
}

// LoadBulkTargets selects accounts for bulk manage actions.
func LoadBulkTargets(ctx context.Context, cli *sub2api.Client, action string, maxOps int) ([]sub2api.Account, int64, string, error) {
	if maxOps <= 0 {
		maxOps = 20
	}
	switch action {
	case "clear_rl":
		items, total, err := ListAccounts(ctx, cli, "rate_limited", 0, maxOps)
		return items, total, "限速账号", err
	case "sched_on":
		items, total, err := ListAccounts(ctx, cli, "unsched", 0, maxOps)
		if err == nil && len(items) > 0 {
			return items, total, "停调度账号", nil
		}
		items, total, err = ListAccounts(ctx, cli, "error", 0, maxOps)
		return items, total, "error 账号（无停调度时回退）", err
	default:
		items, total, err := ListAccounts(ctx, cli, "error", 0, maxOps)
		return items, total, "status=error 账号", err
	}
}
