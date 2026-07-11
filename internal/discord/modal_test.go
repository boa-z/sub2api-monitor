package discord

import (
	"encoding/json"
	"testing"
)

func TestNewModalShape(t *testing.T) {
	m := NewModal("modal_search", "搜索账号", "q", "关键词", "name", 100)
	if m.CustomID != "modal_search" || m.Title != "搜索账号" {
		t.Fatalf("meta %+v", m)
	}
	if len(m.Components) != 1 || m.Components[0].Type != 1 {
		t.Fatalf("expected one action row, got %+v", m.Components)
	}
	if len(m.Components[0].Components) != 1 {
		t.Fatal("expected one text input")
	}
	ti := m.Components[0].Components[0]
	if ti.Type != 4 || ti.CustomID != "q" || ti.Style != 1 || ti.MaxLength != 100 {
		t.Fatalf("text input %+v", ti)
	}
	if ti.Required == nil || !*ti.Required {
		t.Fatal("required")
	}
	// ensure JSON omits empty fields cleanly
	raw, err := json.Marshal(m.Components)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatal(string(raw))
	}
}

func TestModalValueNested(t *testing.T) {
	it := &Interaction{
		Type: 5,
		Data: &struct {
			CustomID string `json:"custom_id"`
			Name     string `json:"name"`
			Options  []struct {
				Name  string          `json:"name"`
				Type  int             `json:"type"`
				Value json.RawMessage `json:"value"`
			} `json:"options"`
			ComponentType int         `json:"component_type"`
			Values        []string    `json:"values"`
			Components    []Component `json:"components"`
		}{
			CustomID: "modal_search",
			Components: []Component{
				{
					Type: 1,
					Components: []Component{
						{Type: 4, CustomID: "q", Value: "  openai  "},
					},
				},
			},
		},
	}
	if got := it.ModalValue("q"); got != "openai" {
		t.Fatalf("got %q", got)
	}
	if got := it.ModalValue("missing"); got != "" {
		t.Fatalf("missing %q", got)
	}
}

func TestModalValueFromJSON(t *testing.T) {
	payload := `{
		"id":"1","token":"t","type":5,
		"data":{
			"custom_id":"modal_base",
			"components":[
				{"type":1,"components":[
					{"type":4,"custom_id":"base_url","value":"http://127.0.0.1:8080"}
				]}
			]
		}
	}`
	var it Interaction
	if err := json.Unmarshal([]byte(payload), &it); err != nil {
		t.Fatal(err)
	}
	if it.Type != 5 {
		t.Fatalf("type %d", it.Type)
	}
	if it.Data.CustomID != "modal_base" {
		t.Fatalf("custom %s", it.Data.CustomID)
	}
	if got := it.ModalValue("base_url"); got != "http://127.0.0.1:8080" {
		t.Fatalf("got %q", got)
	}
}
