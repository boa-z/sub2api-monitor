// Package browse provides shared account-browser filters for Telegram/Discord panels.
package browse

import (
	"context"
	"strconv"
	"strings"
	"sync"

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
	if s == "unsched" || s == "rate_limited" || s == "overload" {
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
	case "problem":
		return ListProblemAccounts(ctx, cli, page, pageSize)
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
	case "overload":
		// prefer API status when supported; fall back to scan on OverloadUntil/status
		items, total, err := cli.ListAccountsEx(ctx, page+1, pageSize, sub2api.AccountListFilter{Status: "overload"})
		if err == nil && (total > 0 || len(items) > 0) {
			return items, total, nil
		}
		return ScanPage(ctx, cli, page, pageSize, IsOverloaded)
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

// IsOverloaded reports overload-specific state (subset of rate-limit-ish issues).
func IsOverloaded(a sub2api.Account) bool {
	if a.OverloadUntil != nil {
		return true
	}
	st := strings.ToLower(a.Status)
	return strings.Contains(st, "overload")
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
	case title == "overload":
		return "过载"
	case title == "problem":
		return "异常汇总"
	case title == "error":
		return "异常"
	case title == "active":
		return "正常"
	case title == "disabled":
		return "已禁用"
	default:
		return title
	}
}

// LoadBulkTargets selects accounts for bulk manage actions (global defaults).
func LoadBulkTargets(ctx context.Context, cli *sub2api.Client, action string, maxOps int) ([]sub2api.Account, int64, string, error) {
	return LoadBulkTargetsScoped(ctx, cli, action, maxOps, "")
}

// LoadBulkTargetsScoped selects bulk targets, preferring the current browser filter
// (status/platform/search/unsched/...) when provided and non-empty.
func LoadBulkTargetsScoped(ctx context.Context, cli *sub2api.Client, action string, maxOps int, browseStatus string) ([]sub2api.Account, int64, string, error) {
	if maxOps <= 0 {
		maxOps = 20
	}
	browseStatus = strings.TrimSpace(browseStatus)
	if browseStatus == "all" {
		browseStatus = ""
	}

	// Prefer the operator's current browser filter when it is a useful scope.
	if browseStatus != "" && bulkScopeCompatible(action, browseStatus) {
		var items []sub2api.Account
		var total int64
		var err error
		if browseStatus == "problem" {
			items, total, err = ListProblemAccounts(ctx, cli, 0, maxOps)
		} else {
			items, total, err = ListAccounts(ctx, cli, browseStatus, 0, maxOps)
		}
		if err == nil {
			label := "当前筛选: " + Title(browseStatus)
			return items, total, label, nil
		}
		// fall through to action defaults on list error
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

// bulkScopeCompatible reports whether a browser filter should override the
// action's default account set. Incompatible scopes fall back to defaults so
// e.g. bulk clear-RL from an "active" list still targets rate-limited accounts.
func bulkScopeCompatible(action, status string) bool {
	status = strings.TrimSpace(status)
	if status == "" || status == "all" {
		return false
	}
	// search / platform / problem-summary filters always scope
	if strings.HasPrefix(status, "search:") || strings.HasPrefix(status, "plat:") || status == "problem" {
		return true
	}
	switch action {
	case "clear_rl":
		return status == "rate_limited" || status == "overload"
	case "sched_on":
		return status == "unsched" || status == "error"
	case "clear_err", "recover", "heal":
		// error tab or other problem tabs; also allow overload/rate_limited for heal
		switch status {
		case "error", "rate_limited", "overload", "unsched":
			return true
		default:
			// active/disabled etc. are not safe defaults for destructive bulk
			return false
		}
	default:
		return status == "error" || status == "rate_limited" || status == "overload" || status == "unsched"
	}
}

// NormalizeBadKind maps callback kind tokens to error|rl|ol|unsched|all.
func NormalizeBadKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "rl", "ol", "unsched", "all":
		return kind
	default:
		return "error"
	}
}

// StatusFromBadKind maps a bad-account tab kind to a browser status filter so
// bulk manage actions opened from that tab reuse the same account set.
// "all" maps to "problem" (merged error+限速+过载+停调度) for bulk/heal.
func StatusFromBadKind(kind string) string {
	switch NormalizeBadKind(kind) {
	case "rl":
		return "rate_limited"
	case "ol":
		return "overload"
	case "unsched":
		return "unsched"
	case "all":
		return "problem"
	default:
		return "error"
	}
}

// ListProblemAccounts merges error + rate_limited + overload + unsched accounts
// (unique by ID, capped scan) and returns one page for bulk/summary views.
func ListProblemAccounts(ctx context.Context, cli *sub2api.Client, page, pageSize int) ([]sub2api.Account, int64, error) {
	if page < 0 {
		page = 0
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	errItems, _, e1 := ListAccounts(ctx, cli, "error", 0, 40)
	rlItems, _, e2 := ListAccounts(ctx, cli, "rate_limited", 0, 40)
	olItems, _, e4 := ListAccounts(ctx, cli, "overload", 0, 40)
	unItems, _, e3 := ListAccounts(ctx, cli, "unsched", 0, 40)
	if e1 != nil && e2 != nil && e3 != nil && e4 != nil {
		return nil, 0, e1
	}
	seen := map[int64]struct{}{}
	var merged []sub2api.Account
	for _, a := range append(append(append(errItems, rlItems...), olItems...), unItems...) {
		if _, ok := seen[a.ID]; ok {
			continue
		}
		seen[a.ID] = struct{}{}
		merged = append(merged, a)
	}
	total := int64(len(merged))
	start := page * pageSize
	if start >= len(merged) {
		return []sub2api.Account{}, total, nil
	}
	end := start + pageSize
	if end > len(merged) {
		end = len(merged)
	}
	return merged[start:end], total, nil
}

// LoadBadAccountsPage returns one page of problematic accounts.
// kind: error|rl|unsched|all (page is 0-based).
func LoadBadAccountsPage(ctx context.Context, cli *sub2api.Client, kind string, page, pageSize int) (items []sub2api.Account, total int64, title, scope string, err error) {
	if page < 0 {
		page = 0
	}
	if pageSize <= 0 {
		pageSize = 8
	}
	kind = NormalizeBadKind(kind)
	switch kind {
	case "rl":
		items, total, err = ListAccounts(ctx, cli, "rate_limited", page, pageSize)
		return items, total, "限速/过载账号", "rate_limited", err
	case "ol":
		items, total, err = ListAccounts(ctx, cli, "overload", page, pageSize)
		return items, total, "过载账号", "overload", err
	case "unsched":
		items, total, err = ListAccounts(ctx, cli, "unsched", page, pageSize)
		return items, total, "停调度账号", "unsched", err
	case "all":
		items, total, err = ListProblemAccounts(ctx, cli, page, pageSize)
		return items, total, "异常汇总", "error+rl+ol+unsched", err
	default:
		items, total, err = ListAccounts(ctx, cli, "error", page, pageSize)
		return items, total, "异常账号 (status=error)", "error", err
	}
}

// ParseBadAccCallback parses rest after ops_badacc: → kind, page.
// Forms: "" | "error" | "error:2" | "rl:0"
func ParseBadAccCallback(rest string) (kind string, page int) {
	kind, page = "error", 0
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return
	}
	parts := strings.Split(rest, ":")
	if len(parts) >= 1 && parts[0] != "" {
		kind = NormalizeBadKind(parts[0])
	}
	if len(parts) >= 2 {
		page, _ = strconv.Atoi(parts[1])
		if page < 0 {
			page = 0
		}
	}
	return
}

// WatchTarget is a watched account identity for status snapshots.
type WatchTarget struct {
	ID   int64
	Name string
}

// AccountSnap holds concurrent-fetched account status + usage for one watch target.
type AccountSnap struct {
	ID         int64
	Name       string
	Account    *sub2api.Account
	AccountErr error
	Usage      *sub2api.UsageInfo
	UsageErr   error
	Today      *sub2api.WindowStats
	TodayErr   error
}

// SnapOpts controls FetchAccountSnaps.
type SnapOpts struct {
	Source      string
	Force       bool
	WithToday   bool
	MaxShow     int
	Concurrency int
}

// FetchAccountSnaps loads GetAccount + GetAccountUsage (and optional today stats)
// for up to MaxShow targets with bounded concurrency. Order matches input targets.
func FetchAccountSnaps(ctx context.Context, cli *sub2api.Client, targets []WatchTarget, opts SnapOpts) []AccountSnap {
	if cli == nil || len(targets) == 0 {
		return nil
	}
	maxShow := opts.MaxShow
	if maxShow <= 0 || maxShow > len(targets) {
		maxShow = len(targets)
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > maxShow {
		concurrency = maxShow
	}
	out := make([]AccountSnap, maxShow)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < maxShow; i++ {
		i, t := i, targets[i]
		out[i] = AccountSnap{ID: t.ID, Name: t.Name}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				out[i].AccountErr = ctx.Err()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			name := t.Name
			acc, err := cli.GetAccount(ctx, t.ID)
			out[i].Account, out[i].AccountErr = acc, err
			if err == nil && acc != nil && (name == "" || name == fmtID(t.ID)) && acc.Name != "" {
				name = acc.Name
			}
			out[i].Name = name

			usage, uerr := cli.GetAccountUsage(ctx, t.ID, opts.Source, opts.Force)
			out[i].Usage, out[i].UsageErr = usage, uerr

			if opts.WithToday {
				today, terr := cli.GetAccountTodayStats(ctx, t.ID)
				out[i].Today, out[i].TodayErr = today, terr
			}
		}()
	}
	wg.Wait()
	return out
}

func fmtID(id int64) string {
	return "#" + strconv.FormatInt(id, 10)
}
