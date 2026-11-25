package notify

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

type cardTheme struct {
	template string
	bgStyle  string
	tagColor string
	icon     string
	title    string
}

// BuildFeishuCard constructs a clean Feishu/Lark Card 2.0 interactive card payload.
func BuildFeishuCard(n Notification) map[string]any {
	th := resolveTheme(n)

	header := map[string]any{
		"template": th.template,
		"padding":  "12px 16px 12px 16px",
		"title": map[string]any{
			"tag":     "plain_text",
			"content": th.title,
		},
		"icon": map[string]any{
			"tag":   "standard_icon",
			"token": th.icon,
		},
	}

	if sub := formatHeaderLocation(n); sub != "" {
		header["subtitle"] = map[string]any{
			"tag":     "plain_text",
			"content": sub,
		}
	}

	var tags []map[string]any
	if n.Kind != "" {
		tags = append(tags, makeTag(n.Kind, th.tagColor))
	}
	if dur := FormatDuration(n.Duration); dur != "" {
		tags = append(tags, makeTag(dur, th.tagColor))
	} else if n.Progress != nil {
		tags = append(tags, makeTag(fmt.Sprintf("%d%%", *n.Progress), th.tagColor))
	}
	if len(tags) > 0 {
		header["text_tag_list"] = tags
	}

	elements := []any{}

	// 1. Primary status callout (the single focal block)
	primaryText := escapeMarkdownInline(n.Msg)
	if primaryText == "" {
		primaryText = th.title
	}
	elements = append(elements, map[string]any{
		"tag":    "column_set",
		"margin": "0px 0px 8px 0px",
		"columns": []any{
			map[string]any{
				"tag":              "column",
				"width":            "weighted",
				"weight":           1,
				"background_style": th.bgStyle,
				"padding":          "10px 12px",
				"elements": []any{
					map[string]any{
						"tag":     "markdown",
						"content": fmt.Sprintf("**%s**", primaryText),
					},
				},
			},
		},
	})

	// 2. Compact metadata strip (3 balanced columns, grey-50 background)
	cols := []any{}
	if dir := shortenPath(n.Cwd, 28); dir != "" {
		cols = append(cols, metaColumn("目录", "`"+dir+"`", 2))
	}
	if prog := formatProgram(n); prog != "" {
		cols = append(cols, metaColumn("程序", prog, 1))
	}
	if right := formatHostAndTime(n); right != "" {
		cols = append(cols, metaColumn("节点", right, 1))
	}
	if len(cols) > 0 {
		strip := map[string]any{
			"tag":                "column_set",
			"flex_mode":          "none",
			"horizontal_spacing": "8px",
			"background_style":   "grey-50",
			"columns":            cols,
		}
		if cleanedTail := CleanTerminalTail(n.Tail, 8); cleanedTail != "" || n.State == "blocked" {
			strip["margin"] = "0px 0px 8px 0px"
		}
		elements = append(elements, strip)
	}

	// 3. Collapsed terminal tail (hidden by default so card stays clean at a glance)
	if cleanedTail := CleanTerminalTail(n.Tail, 8); cleanedTail != "" {
		fence := "```"
		if strings.Contains(cleanedTail, "```") {
			fence = "````"
		}
		panel := map[string]any{
			"tag":              "collapsible_panel",
			"expanded":         false,
			"background_color": "grey-50",
			"border": map[string]any{
				"color":         "grey",
				"corner_radius": "8px",
			},
			"padding": "8px 12px",
			"header": map[string]any{
				"title": map[string]any{
					"tag":     "plain_text",
					"content": "终端最近输出（点击展开）",
				},
				"icon": map[string]any{
					"tag":   "standard_icon",
					"token": "info_outlined",
					"color": "grey",
				},
				"icon_position": "right",
			},
			"elements": []any{
				map[string]any{
					"tag":       "markdown",
					"text_size": "notation",
					"content":   fmt.Sprintf("%s\n%s\n%s", fence, cleanedTail, fence),
				},
			},
		}
		if n.State == "blocked" {
			panel["margin"] = "0px 0px 8px 0px"
		}
		elements = append(elements, panel)
	}

	// 4. Subtle action hint only when blocked
	if n.State == "blocked" && n.BlockID != "" {
		elements = append(elements, map[string]any{
			"tag":       "markdown",
			"text_size": "notation",
			"content":   "<font color='grey'>打开 Rex (Mac / iPhone) → Cmd+K → Beacon: Reply to Blocked Pane</font>",
		})
	}

	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":   true,
			"width_mode":     "default",
			"enable_forward": true,
		},
		"header": header,
		"body": map[string]any{
			"direction": "vertical",
			"padding":   "12px 16px 16px 16px",
			"elements":  elements,
		},
	}
}

func makeTag(text, color string) map[string]any {
	return map[string]any{
		"tag":   "text_tag",
		"color": color,
		"text": map[string]any{
			"tag":     "plain_text",
			"content": text,
		},
	}
}

func metaColumn(label, value string, weight int) map[string]any {
	return map[string]any{
		"tag":              "column",
		"width":            "weighted",
		"weight":           weight,
		"padding":          "8px 10px",
		"vertical_spacing": "2px",
		"elements": []any{
			map[string]any{
				"tag":       "markdown",
				"text_size": "notation",
				"content":   fmt.Sprintf("<font color='grey'>%s</font>", label),
			},
			map[string]any{
				"tag":       "markdown",
				"text_size": "notation",
				"content":   value,
			},
		},
	}
}

func resolveTheme(n Notification) cardTheme {
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
		label := "等待处理"
		switch n.Kind {
		case "permission":
			label = "需要授权确认"
		case "question":
			label = "等待输入回复"
		case "auth":
			label = "需要认证凭据"
		}
		return cardTheme{
			template: "orange",
			bgStyle:  "orange-50",
			tagColor: "orange",
			icon:     "warning_outlined",
			title:    fmt.Sprintf("%s · %s", subject, label),
		}
	case "error":
		return cardTheme{
			template: "red",
			bgStyle:  "red-50",
			tagColor: "red",
			icon:     "close_outlined",
			title:    fmt.Sprintf("%s · 执行失败", subject),
		}
	case "done":
		return cardTheme{
			template: "green",
			bgStyle:  "green-50",
			tagColor: "green",
			icon:     "done_outlined",
			title:    fmt.Sprintf("%s · 已完成", subject),
		}
	case "working":
		return cardTheme{
			template: "blue",
			bgStyle:  "blue-50",
			tagColor: "blue",
			icon:     "time_outlined",
			title:    fmt.Sprintf("%s · 运行中", subject),
		}
	default:
		if n.EventKind == "ports" {
			return cardTheme{
				template: "wathet",
				bgStyle:  "wathet-50",
				tagColor: "wathet",
				icon:     "bell_outlined",
				title:    fmt.Sprintf("%s · 端口已监听", subject),
			}
		}
		return cardTheme{
			template: "blue",
			bgStyle:  "blue-50",
			tagColor: "blue",
			icon:     "bell_outlined",
			title:    fmt.Sprintf("%s · 终端提醒", subject),
		}
	}
}

func formatHeaderLocation(n Notification) string {
	var parts []string
	if n.SessionLabel != "" {
		parts = append(parts, n.SessionLabel)
	} else if n.SessionID != "" {
		parts = append(parts, ShortID(n.SessionID))
	}
	if n.WindowLabel != "" && !strings.HasPrefix(n.WindowLabel, "Window ") {
		parts = append(parts, n.WindowLabel)
	}
	if n.BlockLabel != "" && n.BlockLabel != "shell" {
		parts = append(parts, n.BlockLabel)
	}
	return strings.Join(parts, " · ")
}

func formatProgram(n Notification) string {
	prog := n.App
	if prog == "" {
		prog = n.ProcessName
	}
	if prog == "" {
		return ""
	}
	if n.RecordID != "" {
		return fmt.Sprintf("%s (%s)", prog, n.RecordID)
	}
	return prog
}

func formatHostAndTime(n Notification) string {
	host := strings.TrimSuffix(n.Host, ".local")
	if idx := strings.Index(host, "."); idx > 0 {
		host = host[:idx]
	}
	var parts []string
	if host != "" {
		parts = append(parts, host)
	}
	if !n.Timestamp.IsZero() {
		parts = append(parts, n.Timestamp.Format("15:04"))
	}
	return strings.Join(parts, " · ")
}

func shortenPath(p string, maxLen int) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + p[len(home):]
	}
	if len(p) <= maxLen {
		return p
	}
	parts := strings.Split(p, "/")
	if len(parts) >= 3 {
		return parts[0] + "/…/" + parts[len(parts)-1]
	}
	return "…" + p[len(p)-maxLen+1:]
}

// CleanTerminalTail strips noisy TUI box-drawing rules, blank lines, and keeps at most maxLines.
func CleanTerminalTail(tail string, maxLines int) string {
	if strings.TrimSpace(tail) == "" || maxLines <= 0 {
		return ""
	}
	rawLines := strings.Split(strings.ReplaceAll(tail, "\r\n", "\n"), "\n")
	cleaned := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isDecorativeLine(trimmed) {
			continue
		}
		runes := []rune(trimmed)
		if len(runes) > 96 {
			trimmed = string(runes[:93]) + "..."
		}
		cleaned = append(cleaned, trimmed)
	}
	if len(cleaned) == 0 {
		return ""
	}
	if len(cleaned) > maxLines {
		cleaned = cleaned[len(cleaned)-maxLines:]
	}
	return strings.Join(cleaned, "\n")
}

func isDecorativeLine(s string) bool {
	hasContent := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasContent = true
			break
		}
	}
	return !hasContent
}

func escapeMarkdownInline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// BuildPlainSummary formats a concise multi-line text summary (for macOS or fallback).
func BuildPlainSummary(n Notification) (title string, body string) {
	title = resolveTheme(n).title
	var parts []string
	if n.Msg != "" {
		parts = append(parts, n.Msg)
	}
	if loc := formatHeaderLocation(n); loc != "" {
		parts = append(parts, loc)
	}
	if dur := FormatDuration(n.Duration); dur != "" {
		parts = append(parts, dur)
	}
	if len(parts) == 0 {
		return title, shortenPath(n.Cwd, 40)
	}
	return title, strings.Join(parts, " — ")
}
