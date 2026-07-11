package sub2api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDashboardStatsUnmarshal(t *testing.T) {
	raw := []byte(`{
		"total_users":11,"today_new_users":1,"active_users":5,
		"total_accounts":19,"normal_accounts":11,"error_accounts":5,
		"ratelimit_accounts":0,"overload_accounts":0,
		"today_requests":18046,"today_tokens":2515837291,"today_cost":2324.6,
		"total_requests":562502,"total_tokens":66067944440,"total_cost":67229.8,
		"rpm":0,"tpm":0,"uptime":80395
	}`)
	var s DashboardStats
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.ErrorAccounts != 5 || s.TodayRequests != 18046 || s.TodayCost < 2000 {
		t.Fatalf("%+v", s)
	}
}

func TestAvailabilityUnmarshal(t *testing.T) {
	raw := []byte(`{
		"enabled":true,
		"platform":{"openai":{"platform":"openai","total_accounts":10,"available_count":3,"rate_limit_count":0,"error_count":5}},
		"group":{"10":{"group_id":10,"group_name":"demo","total_accounts":4,"available_count":1,"error_count":2}},
		"account":{"12":{"account_id":12,"account_name":"a","platform":"openai","status":"active","is_available":true,"has_error":false}},
		"timestamp":"2026-07-11T15:16:40.8700329Z"
	}`)
	var av AccountAvailability
	if err := json.Unmarshal(raw, &av); err != nil {
		t.Fatal(err)
	}
	p := av.Platform["openai"]
	if p.TotalNum() != 10 || p.AvailableNum() != 3 || p.ErrorNum() != 5 {
		t.Fatalf("platform bucket %+v", p)
	}
	if st, ok := av.Account["12"]; !ok || !st.IsAvailable {
		t.Fatalf("account map %+v", av.Account)
	}
}

func TestTrafficSummaryNested(t *testing.T) {
	raw := []byte(`{"enabled":true,"summary":{"window":"5min","qps":{"current":1.5,"peak":2,"avg":1.2},"tps":{"current":0}},"timestamp":"2026-07-11T15:16:29Z"}`)
	var tr TrafficSummary
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.CurrentQPS() != 1.5 {
		t.Fatalf("qps=%v", tr.CurrentQPS())
	}
	if tr.WindowLabel() != "5min" {
		t.Fatalf("window=%s", tr.WindowLabel())
	}
}

func TestAlertEventHelpers(t *testing.T) {
	ev := AlertEvent{
		Title: "P1: 错误率过高", Description: "error_rate > 5", Severity: "P1",
		MetricValue: 100, ThresholdValue: 5, Status: "firing",
		FiredAt: time.Now(),
	}
	if ev.DisplayTitle() != "P1: 错误率过高" {
		t.Fatal(ev.DisplayTitle())
	}
	if ev.Metric() != 100 || ev.ThresholdVal() != 5 {
		t.Fatal(ev.Metric(), ev.ThresholdVal())
	}
}

func TestOpsErrorPageUnmarshal(t *testing.T) {
	raw := []byte(`{"items":[{"id":1,"severity":"P1","status_code":502,"message":"up","account_name":"x"}],"total":1,"page":1,"page_size":10}`)
	var p OpsErrorPage
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].StatusCode != 502 {
		t.Fatalf("%+v", p)
	}
}

func TestParseNamedListUsersShape(t *testing.T) {
	raw := []byte(`{"items":[{"id":1,"email":"a@b.c","role":"user","balance":10,"concurrency":5,"status":"active"}],"total":1}`)
	items, total, err := parseNamedList[User](raw)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Email != "a@b.c" {
		t.Fatalf("%+v %d", items, total)
	}
}

func TestTrafficSummaryHelpers(t *testing.T) {
	tr := TrafficSummary{}
	tr.Summary.QPS.Current = 1.5
	tr.Summary.QPS.Peak = 3.2
	tr.Summary.TPS.Current = 0.7
	tr.Summary.Window = "5min"
	if tr.CurrentQPS() != 1.5 || tr.CurrentTPS() != 0.7 || tr.PeakQPS() != 3.2 {
		t.Fatalf("%v %v %v", tr.CurrentQPS(), tr.CurrentTPS(), tr.PeakQPS())
	}
	if tr.WindowLabel() != "5min" {
		t.Fatal(tr.WindowLabel())
	}
}

func TestNormalizeWindow(t *testing.T) {
	cases := map[string]string{
		"5h":         "five_hour",
		"5_hour":     "five_hour",
		"Five_Hour":  "five_hour",
		"7d":         "seven_day",
		"seven-day":  "seven_day",
		"five_hour":  "five_hour",
		"custom_win": "custom_win",
		"  7D  ":     "seven_day",
	}
	for in, want := range cases {
		if got := NormalizeWindow(in); got != want {
			t.Fatalf("NormalizeWindow(%q)=%q want %q", in, got, want)
		}
	}
}

func TestThresholdHit(t *testing.T) {
	th := map[string]float64{
		"five_hour": 70,
		"seven_day": 80,
	}
	if !ThresholdHit("5h", 70, th) {
		t.Fatal("expected hit at boundary")
	}
	if ThresholdHit("5h", 69.9, th) {
		t.Fatal("expected miss below threshold")
	}
	if !ThresholdHit("seven_day", 90, th) {
		t.Fatal("expected seven_day hit")
	}
	if ThresholdHit("unknown", 100, th) {
		t.Fatal("unknown window should miss")
	}
	if ThresholdHit("5h", 100, nil) {
		t.Fatal("nil map should miss")
	}
}

func TestCompactUsageSummary(t *testing.T) {
	u := &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 72},
		SevenDay: &UsageProgress{Utilization: 41.2},
	}
	th := map[string]float64{"five_hour": 70, "seven_day": 80}
	line, hit := u.CompactUsageSummary(th, 3)
	if !hit {
		t.Fatalf("expected hit, line=%q", line)
	}
	if !strings.Contains(line, "5h 72%⚠") {
		t.Fatalf("expected 5h warning in %q", line)
	}
	if !strings.Contains(line, "7d 41%") {
		t.Fatalf("expected 7d part in %q", line)
	}

	// priority: five_hour / seven_day before lower-priority windows
	u2 := &UsageInfo{
		GeminiFlashDaily: &UsageProgress{Utilization: 99},
		FiveHour:         &UsageProgress{Utilization: 10},
		SevenDay:         &UsageProgress{Utilization: 20},
	}
	line2, hit2 := u2.CompactUsageSummary(map[string]float64{"gemini_flash_daily": 90}, 2)
	if hit2 {
		t.Fatalf("gemini hit should be truncated out of maxParts=2, line=%q", line2)
	}
	if !strings.HasPrefix(line2, "5h ") || !strings.Contains(line2, "7d ") {
		t.Fatalf("expected primary windows first, got %q", line2)
	}
	line3, hit3 := u2.CompactUsageSummary(map[string]float64{"gemini_flash_daily": 90}, 3)
	if !hit3 || !strings.Contains(line3, "g-fl 99%⚠") {
		t.Fatalf("expected gemini hit when included, got %q hit=%v", line3, hit3)
	}

	// empty / error
	if line, hit := (*UsageInfo)(nil).CompactUsageSummary(nil, 3); line != "" || hit {
		t.Fatalf("nil usage: %q %v", line, hit)
	}
	uErr := &UsageInfo{Error: "timeout"}
	if line, hit := uErr.CompactUsageSummary(nil, 3); line != "err" || !hit {
		t.Fatalf("error-only usage: %q %v", line, hit)
	}
}
