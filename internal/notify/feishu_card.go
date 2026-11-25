package notify

import (
	"fmt"
	"strings"
)

// BuildFeishuCard constructs a Feishu/Lark interactive card payload map.
func BuildFeishuCard(n Notification) map[string]any {
	template, headerTitle := cardHeader(n)

	elements := []any{}

	// Primary summary / message
	if n.Msg != "" {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**%s**", escapeMarkdownInline(n.Msg)),
			},
		})
	}

	// Metadata grid
	fields := []map[string]any{}
	addField := func(label, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		fields = append(fields, map[string]any{
			"is_short": true,
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**%s**\n%s", label, val),
			},
		})
	}

	loc := n.SessionLabel
	if loc == "" {
		loc = ShortID(n.SessionID)
	}
	if n.WindowLabel != "" {
		loc = loc + " / " + n.WindowLabel
	}
	if n.BlockLabel != "" {
		loc = loc + " / " + n.BlockLabel
	}
	addField("Rex Location", loc)

	appVal := n.App
	if appVal == "" {
		appVal = n.ProcessName
	} else if n.ProcessName != "" && n.ProcessName != n.App {
		appVal = fmt.Sprintf("%s (%s)", n.App, n.ProcessName)
	}
	if n.RecordID != "" {
		appVal = fmt.Sprintf("%s [%s]", appVal, n.RecordID)
	}
	addField("Program", appVal)

	if n.Cwd != "" {
		addField("Directory", "`"+n.Cwd+"`")
	}
	if dur := FormatDuration(n.Duration); dur != "" {
		addField("Duration", dur)
	} else if n.Progress != nil {
		addField("Progress", fmt.Sprintf("%d%%", *n.Progress))
	}
	if n.Host != "" {
		addField("Host", n.Host)
	}
	if n.Kind != "" {
		addField("Waiting For", n.Kind)
	}

	if len(fields) > 0 {
		elements = append(elements, map[string]any{
			"tag":    "div",
			"fields": fields,
		})
	}

	// Terminal output tail snippet
	if tail := strings.TrimSpace(n.Tail); tail != "" {
		fence := "```"
		if strings.Contains(tail, "```") {
			fence = "````"
		}
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**Terminal Tail**\n%s\n%s\n%s", fence, tail, fence),
			},
		})
	}

	// Footer hint
	var noteParts []string
	if n.BlockID != "" {
		noteParts = append(noteParts, "block: "+ShortID(n.BlockID))
	}
	if !n.Timestamp.IsZero() {
		noteParts = append(noteParts, n.Timestamp.Format("15:04:05"))
	}
	if n.State == "blocked" && n.BlockID != "" {
		noteParts = append(noteParts, "Open Rex (Mac/iOS) → Beacon: Reply to Blocked Pane")
	}
	if len(noteParts) > 0 {
		elements = append(elements, map[string]any{
			"tag": "note",
			"elements": []any{
				map[string]any{
					"tag":     "plain_text",
					"content": strings.Join(noteParts, "  ·  "),
				},
			},
		})
	}

	return map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
			"enable_forward":   true,
		},
		"header": map[string]any{
			"template": template,
			"title": map[string]any{
				"tag":     "plain_text",
				"content": headerTitle,
			},
		},
		"elements": elements,
	}
}

func cardHeader(n Notification) (template string, title string) {
	subject := n.Title
	if subject == "" {
		subject = n.App
	}
	if subject == "" {
		subject = n.SessionLabel
	}
	if subject == "" {
		subject = "Rex Terminal"
	}

	switch n.State {
	case "blocked":
		prefix := "✋ Blocked"
		switch n.Kind {
		case "permission":
			prefix = "🔐 Permission Needed"
		case "question":
			prefix = "❓ Input Needed"
		case "auth":
			prefix = "🔑 Auth Required"
		}
		return "orange", fmt.Sprintf("%s · %s", prefix, subject)
	case "error":
		return "red", fmt.Sprintf("❌ Error · %s", subject)
	case "done":
		return "green", fmt.Sprintf("✅ Done · %s", subject)
	case "working":
		return "blue", fmt.Sprintf("⚙️ Working · %s", subject)
	default:
		if n.EventKind == "ports" {
			return "wathet", fmt.Sprintf("🔌 Port Listening · %s", subject)
		}
		return "purple", fmt.Sprintf("🔔 Rex · %s", subject)
	}
}

func escapeMarkdownInline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// BuildPlainSummary formats a concise multi-line text summary (for macOS or lark-cli fallback).
func BuildPlainSummary(n Notification) (title string, body string) {
	_, title = cardHeader(n)
	var parts []string
	if n.Msg != "" {
		parts = append(parts, n.Msg)
	}
	var loc []string
	if n.SessionLabel != "" {
		loc = append(loc, n.SessionLabel)
	}
	if n.WindowLabel != "" {
		loc = append(loc, n.WindowLabel)
	}
	if n.BlockLabel != "" {
		loc = append(loc, n.BlockLabel)
	}
	if len(loc) > 0 {
		parts = append(parts, strings.Join(loc, " / "))
	}
	if dur := FormatDuration(n.Duration); dur != "" {
		parts = append(parts, "duration "+dur)
	}
	if len(parts) == 0 {
		return title, n.Cwd
	}
	return title, strings.Join(parts, " — ")
}
