package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/boa/sub2api-monitor/internal/config"
)

// Store tracks alert fingerprints and collector snapshots.
type Store interface {
	// LastAlert returns last fire time for fingerprint (zero if never).
	LastAlert(fingerprint string) (time.Time, bool)
	// MarkAlert records a fire.
	MarkAlert(fingerprint string, t time.Time) error
	// ClearAlert removes fingerprint (on resolve).
	ClearAlert(fingerprint string) error
	// GetJSON loads a named blob into dest; returns false if missing.
	GetJSON(key string, dest any) (bool, error)
	// PutJSON stores a named blob.
	PutJSON(key string, v any) error
	Close() error
}

func New(cfg config.StateConfig) (Store, error) {
	switch cfg.Driver {
	case "", "memory":
		return newMemory(), nil
	case "sqlite":
		// lightweight file-backed JSON store (no cgo sqlite dependency)
		return newFileStore(cfg.SQLitePath)
	default:
		return nil, fmt.Errorf("unknown state driver: %s", cfg.Driver)
	}
}

// ----- memory -----

type memoryStore struct {
	mu     sync.Mutex
	alerts map[string]time.Time
	blobs  map[string]json.RawMessage
}

func newMemory() *memoryStore {
	return &memoryStore{
		alerts: make(map[string]time.Time),
		blobs:  make(map[string]json.RawMessage),
	}
}

func (m *memoryStore) LastAlert(fp string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.alerts[fp]
	return t, ok
}

func (m *memoryStore) MarkAlert(fp string, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts[fp] = t
	return nil
}

func (m *memoryStore) ClearAlert(fp string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.alerts, fp)
	return nil
}

func (m *memoryStore) GetJSON(key string, dest any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.blobs[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dest)
}

func (m *memoryStore) PutJSON(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = raw
	return nil
}

func (m *memoryStore) Close() error { return nil }

// ----- file-backed (JSON on disk, misnamed sqlite for simplicity) -----

type fileStore struct {
	path string
	mu   sync.Mutex
	data fileData
}

type fileData struct {
	Alerts map[string]time.Time       `json:"alerts"`
	Blobs  map[string]json.RawMessage `json:"blobs"`
}

func newFileStore(path string) (*fileStore, error) {
	if path == "" {
		path = "./data/state.db"
	}
	// use .json extension internally even if path says .db
	if filepath.Ext(path) == ".db" {
		path = path[:len(path)-3] + "json"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	fs := &fileStore{
		path: path,
		data: fileData{
			Alerts: make(map[string]time.Time),
			Blobs:  make(map[string]json.RawMessage),
		},
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &fs.data)
		if fs.data.Alerts == nil {
			fs.data.Alerts = make(map[string]time.Time)
		}
		if fs.data.Blobs == nil {
			fs.data.Blobs = make(map[string]json.RawMessage)
		}
	}
	return fs, nil
}

func (f *fileStore) persist() error {
	raw, err := json.MarshalIndent(f.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileStore) LastAlert(fp string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.data.Alerts[fp]
	return t, ok
}

func (f *fileStore) MarkAlert(fp string, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.Alerts[fp] = t
	return f.persist()
}

func (f *fileStore) ClearAlert(fp string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data.Alerts, fp)
	return f.persist()
}

func (f *fileStore) GetJSON(key string, dest any) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.data.Blobs[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dest)
}

func (f *fileStore) PutJSON(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.Blobs[key] = raw
	return f.persist()
}

func (f *fileStore) Close() error { return nil }
