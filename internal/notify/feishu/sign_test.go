package feishu

import "testing"

func TestSignStable(t *testing.T) {
	// Just ensure function runs and is deterministic
	a := feishuSign("1599360473", "test_secret")
	b := feishuSign("1599360473", "test_secret")
	if a == "" || a != b {
		t.Fatalf("sign unstable or empty: %q %q", a, b)
	}
	if a == feishuSign("1599360473", "other") {
		t.Fatal("sign should depend on secret")
	}
}
