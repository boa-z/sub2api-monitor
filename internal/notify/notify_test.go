package notify

import (
	"context"
	"sync/atomic"
	"testing"
)

type stub struct {
	name    string
	enabled bool
	n       atomic.Int32
}

func (s *stub) Name() string  { return s.name }
func (s *stub) Enabled() bool { return s.enabled }
func (s *stub) Send(ctx context.Context, msg Message) error {
	s.n.Add(1)
	return nil
}

func TestMultiFanout(t *testing.T) {
	a := &stub{name: "telegram", enabled: true}
	b := &stub{name: "feishu", enabled: true}
	c := &stub{name: "off", enabled: false}
	m := NewMulti(a, b, c)
	if err := m.Send(context.Background(), Message{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if a.n.Load() != 1 || b.n.Load() != 1 || c.n.Load() != 0 {
		t.Fatalf("counts a=%d b=%d c=%d", a.n.Load(), b.n.Load(), c.n.Load())
	}
}

func TestRecipientFilter(t *testing.T) {
	a := &stub{name: "telegram", enabled: true}
	b := &stub{name: "feishu", enabled: true}
	m := NewMulti(a, b)
	if err := m.Send(context.Background(), Message{Text: "x", Recipients: []string{"telegram:1"}}); err != nil {
		t.Fatal(err)
	}
	if a.n.Load() != 1 || b.n.Load() != 0 {
		t.Fatalf("expected only telegram, a=%d b=%d", a.n.Load(), b.n.Load())
	}
}
