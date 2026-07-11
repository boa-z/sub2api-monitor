package userstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
)

// AccountWatch is a user-configured account to monitor.
type AccountWatch struct {
	ID         int64                   `json:"id"`
	Name       string                  `json:"name,omitempty"`
	Thresholds []config.UsageThreshold `json:"thresholds,omitempty"`
	// Enabled defaults true when omitted in older profiles
	Enabled *bool `json:"enabled,omitempty"`
}

func (a AccountWatch) IsEnabled() bool {
	if a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

// Role constants for panel privileges.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// Profile is a Telegram user's monitoring configuration.
type Profile struct {
	TelegramUserID int64  `json:"telegram_user_id"`
	ChatID         string `json:"chat_id"`
	Username       string `json:"username,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	// Role is an optional per-profile override: "admin" | "user" | empty (derive from config).
	Role string `json:"role,omitempty"`

	// Sub2API connection (per-user)
	BaseURL     string `json:"base_url"`
	AdminAPIKey string `json:"admin_api_key,omitempty"`
	JWT         string `json:"jwt,omitempty"`

	// Monitoring
	Enabled    bool                    `json:"enabled"`
	Source     string                  `json:"source,omitempty"` // passive|active
	Accounts   []AccountWatch          `json:"accounts"`
	Thresholds []config.UsageThreshold `json:"thresholds,omitempty"` // defaults for this user

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EffectiveRole returns admin|user for this profile given explicit Role.
func (p *Profile) EffectiveRole() string {
	if p == nil {
		return RoleUser
	}
	switch strings.ToLower(strings.TrimSpace(p.Role)) {
	case RoleAdmin:
		return RoleAdmin
	case RoleUser:
		return RoleUser
	default:
		return ""
	}
}

func (p *Profile) HasConnection() bool {
	return p != nil && p.BaseURL != "" && (p.AdminAPIKey != "" || p.JWT != "")
}

func (p *Profile) EffectiveSource() string {
	if p != nil && (p.Source == "active" || p.Source == "passive") {
		return p.Source
	}
	return "passive"
}

// Store persists user profiles to a JSON file.
type Store struct {
	path string
	mu   sync.RWMutex
	byID map[int64]*Profile
}

func Open(path string) (*Store, error) {
	if path == "" {
		path = "./data/users.json"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		path: path,
		byID: make(map[int64]*Profile),
	}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		var list []*Profile
		// support {users:[...]} or bare array
		var wrap struct {
			Users []*Profile `json:"users"`
		}
		if err := json.Unmarshal(b, &wrap); err == nil && wrap.Users != nil {
			list = wrap.Users
		} else if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("parse users store: %w", err)
		}
		for _, p := range list {
			if p != nil && p.TelegramUserID > 0 {
				cp := *p
				s.byID[p.TelegramUserID] = &cp
			}
		}
	}
	return s, nil
}

func (s *Store) Get(userID int64) (*Profile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byID[userID]
	if !ok {
		return nil, false
	}
	cp := *p
	cp.Accounts = append([]AccountWatch(nil), p.Accounts...)
	cp.Thresholds = append([]config.UsageThreshold(nil), p.Thresholds...)
	return &cp, true
}

func (s *Store) GetOrCreate(userID int64, chatID, username, display string) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.byID[userID]; ok {
		changed := false
		if chatID != "" && p.ChatID != chatID {
			p.ChatID = chatID
			changed = true
		}
		if username != "" && p.Username != username {
			p.Username = username
			changed = true
		}
		if display != "" && p.DisplayName != display {
			p.DisplayName = display
			changed = true
		}
		if changed {
			p.UpdatedAt = time.Now().UTC()
			if err := s.persistLocked(); err != nil {
				return nil, err
			}
		}
		cp := *p
		cp.Accounts = append([]AccountWatch(nil), p.Accounts...)
		cp.Thresholds = append([]config.UsageThreshold(nil), p.Thresholds...)
		return &cp, nil
	}
	now := time.Now().UTC()
	p := &Profile{
		TelegramUserID: userID,
		ChatID:         chatID,
		Username:       username,
		DisplayName:    display,
		Enabled:        true,
		Source:         "passive",
		Accounts:       nil,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.byID[userID] = p
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	cp := *p
	return &cp, nil
}

// Update applies a mutator under lock and persists.
func (s *Store) Update(userID int64, fn func(*Profile) error) (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[userID]
	if !ok {
		return nil, fmt.Errorf("user %d not found", userID)
	}
	if err := fn(p); err != nil {
		return nil, err
	}
	p.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	cp := *p
	cp.Accounts = append([]AccountWatch(nil), p.Accounts...)
	cp.Thresholds = append([]config.UsageThreshold(nil), p.Thresholds...)
	return &cp, nil
}

// ListEnabled returns profiles that have monitoring enabled and a connection.
func (s *Store) ListEnabled() []*Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Profile, 0, len(s.byID))
	for _, p := range s.byID {
		if !p.Enabled || !p.HasConnection() || len(p.Accounts) == 0 {
			continue
		}
		cp := *p
		cp.Accounts = append([]AccountWatch(nil), p.Accounts...)
		cp.Thresholds = append([]config.UsageThreshold(nil), p.Thresholds...)
		out = append(out, &cp)
	}
	return out
}

func (s *Store) ListAll() []*Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Profile, 0, len(s.byID))
	for _, p := range s.byID {
		cp := *p
		cp.Accounts = append([]AccountWatch(nil), p.Accounts...)
		cp.Thresholds = append([]config.UsageThreshold(nil), p.Thresholds...)
		out = append(out, &cp)
	}
	return out
}

func (s *Store) persistLocked() error {
	list := make([]*Profile, 0, len(s.byID))
	for _, p := range s.byID {
		cp := *p
		list = append(list, &cp)
	}
	wrap := struct {
		Users []*Profile `json:"users"`
	}{Users: list}
	raw, err := json.MarshalIndent(wrap, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Close() error { return nil }

// MaskKey redacts secrets for display.
func MaskKey(s string) string {
	if s == "" {
		return "(未设置)"
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}
