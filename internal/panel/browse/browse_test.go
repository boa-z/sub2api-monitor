package browse

import (
	"testing"
	"time"

	"github.com/boa/sub2api-monitor/internal/sub2api"
)

func TestParseCallbackAndToken(t *testing.T) {
	cases := []struct {
		rest string
		want string
		page int
	}{
		{"all:0", "all", 0},
		{"error:2", "error", 2},
		{"search|foo:1", "search:foo", 1},
		{"plat|openai:0", "plat:openai", 0},
		{"plat|openai|active:3", "plat:openai:active", 3},
		{"unsched:1", "unsched", 1},
		{"rate_limited:0", "rate_limited", 0},
	}
	for _, c := range cases {
		got, page := ParseCallback(c.rest)
		if got != c.want || page != c.page {
			t.Fatalf("rest=%q got=(%q,%d) want=(%q,%d)", c.rest, got, page, c.want, c.page)
		}
	}
	if Token("search:abc") != "search|abc" {
		t.Fatal(Token("search:abc"))
	}
	if Token("plat:gemini") != "plat|gemini" {
		t.Fatal(Token("plat:gemini"))
	}
	if Title("rate_limited") != "限速" {
		t.Fatal(Title("rate_limited"))
	}
}

func TestIsRateLimited(t *testing.T) {
	now := time.Now()
	if !IsRateLimited(sub2api.Account{RateLimitedAt: &now}) {
		t.Fatal("rate limited at")
	}
	if !IsRateLimited(sub2api.Account{Status: "rate_limited"}) {
		t.Fatal("status")
	}
	if IsRateLimited(sub2api.Account{Status: "active"}) {
		t.Fatal("active")
	}
}

func TestParseFilter(t *testing.T) {
	f := ParseFilter("plat:openai:active")
	if f.Platform != "openai" || f.Status != "active" {
		t.Fatalf("%+v", f)
	}
	f = ParseFilter("search:hello")
	if f.Search != "hello" {
		t.Fatalf("%+v", f)
	}
}
