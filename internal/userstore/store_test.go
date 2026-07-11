package userstore

import (
	"path/filepath"
	"testing"
)

func TestMaskKey(t *testing.T) {
	if MaskKey("") != "(未设置)" {
		t.Fatal(MaskKey(""))
	}
	if MaskKey("short") != "****" {
		t.Fatal(MaskKey("short"))
	}
	got := MaskKey("sk-test-key-12345678")
	if got == "" || got == "sk-test-key-12345678" {
		t.Fatalf("expected masked, got %s", got)
	}
}

func TestPersistAndListEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetOrCreate(1, "1", "u", "U"); err != nil {
		t.Fatal(err)
	}
	if len(s.ListEnabled()) != 0 {
		t.Fatal("should not list without connection/accounts")
	}
	en := true
	if _, err := s.Update(1, func(p *Profile) error {
		p.BaseURL = "http://x"
		p.AdminAPIKey = "k"
		p.Enabled = true
		p.Accounts = []AccountWatch{{ID: 9, Enabled: &en}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.ListEnabled()); n != 1 {
		t.Fatalf("expected 1 enabled, got %d", n)
	}
	// reopen
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s2.Get(1)
	if !ok || p.BaseURL != "http://x" || len(p.Accounts) != 1 {
		t.Fatalf("reload failed: %+v", p)
	}
}
