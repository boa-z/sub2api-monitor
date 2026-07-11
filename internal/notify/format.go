package notify

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"
)

// RenderAlert builds a standard multi-format alert body from structured fields.
type AlertView struct {
	Instance string
	Severity Severity
	Title    string
	Resolved bool
	// Lines are key/value pairs shown in the body.
	Lines []KV
	Time  string
}

type KV struct {
	Key   string
	Value string
}

func (v AlertView) Message(maxRunes int) Message {
	icon := "🔴"
	label := "FIRING"
	if v.Resolved {
		icon = "🟢"
		label = "RESOLVED"
	}
	switch v.Severity {
	case SevP0:
		if !v.Resolved {
			icon = "🚨"
		}
	case SevP3:
		if !v.Resolved {
			icon = "🟡"
		}
	}

	// plain
	var plain strings.Builder
	fmt.Fprintf(&plain, "%s [%s] %s · %s\n", icon, v.Severity, label, v.Title)
	if v.Instance != "" {
		fmt.Fprintf(&plain, "实例: %s\n", v.Instance)
	}
	for _, kv := range v.Lines {
		fmt.Fprintf(&plain, "%s: %s\n", kv.Key, kv.Value)
	}
	if v.Time != "" {
		fmt.Fprintf(&plain, "时间: %s", v.Time)
	}

	// html
	var h strings.Builder
	fmt.Fprintf(&h, "%s <b>[%s] %s</b> · %s\n", icon, html.EscapeString(string(v.Severity)), html.EscapeString(label), html.EscapeString(v.Title))
	if v.Instance != "" {
		fmt.Fprintf(&h, "实例: <code>%s</code>\n", html.EscapeString(v.Instance))
	}
	for _, kv := range v.Lines {
		fmt.Fprintf(&h, "%s: <code>%s</code>\n", html.EscapeString(kv.Key), html.EscapeString(kv.Value))
	}
	if v.Time != "" {
		fmt.Fprintf(&h, "时间: <code>%s</code>", html.EscapeString(v.Time))
	}

	// feishu/lark interactive-friendly markdown (simple)
	var md strings.Builder
	fmt.Fprintf(&md, "%s **[%s] %s** · %s\n", icon, v.Severity, label, v.Title)
	if v.Instance != "" {
		fmt.Fprintf(&md, "实例: `%s`\n", v.Instance)
	}
	for _, kv := range v.Lines {
		fmt.Fprintf(&md, "%s: `%s`\n", kv.Key, kv.Value)
	}
	if v.Time != "" {
		fmt.Fprintf(&md, "时间: `%s`", v.Time)
	}

	plainS := trimRunes(plain.String(), maxRunes)
	htmlS := trimRunes(h.String(), maxRunes)
	mdS := trimRunes(md.String(), maxRunes)

	return Message{
		Title:    v.Title,
		Text:     plainS,
		HTML:     htmlS,
		Markdown: mdS,
		Severity: v.Severity,
		Resolved: v.Resolved,
		Labels: map[string]string{
			"instance": v.Instance,
		},
	}
}

// LineHTML is used by collectors that still assemble HTML bodies.
// Prefer structured AlertView for new code.
func LineHTML(k, v string) string {
	return fmt.Sprintf("%s: <code>%s</code>\n", html.EscapeString(k), html.EscapeString(v))
}

// LinePlain formats a plain key/value line.
func LinePlain(k, v string) string {
	return fmt.Sprintf("%s: %s\n", k, v)
}

func EscapeHTML(s string) string { return html.EscapeString(s) }

func CodeHTML(s string) string {
	return "<code>" + html.EscapeString(s) + "</code>"
}

func BoldHTML(s string) string {
	return "<b>" + html.EscapeString(s) + "</b>"
}

func trimRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}
