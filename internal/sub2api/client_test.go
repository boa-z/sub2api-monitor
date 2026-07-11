package sub2api

import (
	"encoding/json"
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
