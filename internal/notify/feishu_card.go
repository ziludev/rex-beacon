package notify

import (
	"fmt"
	"os"
	"strings"
)

type cardTheme struct {
	template string
	bgStyle  string
	tagColor string
	icon     string
	title    string
}

// BuildFeishuCard constructs a clean Feishu/Lark Card 2.0 interactive card payload
// that puts the full conclusion front-and-center while keeping metadata compact.
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
	conclusion := ExtractTerminalConclusion(n.Tail, 45, 3200)
	explicitMsg := escapeMarkdownInline(n.Msg)

	// 1. Explicit status message banner (when provided by OSC 7501 msg= or wrapper)
	if explicitMsg != "" {
		banner := map[string]any{
			"tag": "column_set",
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
							"content": fmt.Sprintf("**%s**", explicitMsg),
						},
					},
				},
			},
		}
		if conclusion != "" {
			banner["margin"] = "0px 0px 8px 0px"
		} else {
			banner["margin"] = "0px 0px 8px 0px"
		}
		elements = append(elements, banner)
	}

	// 2. Full conclusion / terminal output directly visible in card body
	if conclusion != "" {
		elements = append(elements, buildConclusionElements(conclusion)...)
	} else if explicitMsg == "" {
		// Fallback when neither msg nor terminal output is available
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
							"content": fmt.Sprintf("**%s**", th.title),
						},
					},
				},
			},
		})
	}

	// 3. Compact metadata footer (3 balanced columns, grey-50 background)
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
		if n.State == "blocked" && n.BlockID != "" {
			strip["margin"] = "0px 0px 8px 0px"
		}
		elements = append(elements, strip)
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

const clippedConclusionPrefix = "> *…（前文已省略）*"

// ExtractTerminalConclusion strips trailing TUI input boxes/status footers (such as
// Antigravity, Pi, and Claude Code chrome) and retains the full recent conclusion
// up to maxLines lines and maxChars bytes/runes cleanly.
func ExtractTerminalConclusion(raw string, maxLines, maxChars int) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if maxLines <= 0 {
		maxLines = 45
	}
	if maxChars <= 0 {
		maxChars = 3200
	}

	alreadyClipped := false
	if strings.HasPrefix(raw, clippedConclusionPrefix) {
		alreadyClipped = true
		raw = strings.TrimPrefix(raw, clippedConclusionPrefix)
		raw = strings.TrimLeft(raw, "\r\n")
	}

	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}

	// Strip TUI bottom chrome: look for the bottommost input box framed by two horizontal rule lines.
	lines = stripBottomTUIChrome(lines)

	// Drop leading blank lines and decorative startup banner lines if captured.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return ""
	}

	// Normalize internal lines: collapse consecutive blank lines and replace long box rules with "---".
	norm := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if isStartupArt(trimmed) {
			continue
		}
		if trimmed == "" {
			if !prevBlank && len(norm) > 0 {
				norm = append(norm, "")
				prevBlank = true
			}
			continue
		}
		prevBlank = false
		if isHorizontalRule(trimmed) {
			norm = append(norm, "---")
			continue
		}
		norm = append(norm, strings.TrimRight(l, " \t"))
	}
	for len(norm) > 0 && (norm[len(norm)-1] == "" || norm[len(norm)-1] == "---") {
		norm = norm[:len(norm)-1]
	}
	if len(norm) == 0 {
		return ""
	}

	// Stop at the most recent user prompt line if present above the answer so we don't bleed into older turns.
	if start := findLatestTurnStart(norm); start > 0 && start < len(norm) {
		norm = norm[start:]
	}

	// Apply maxLines from the bottom.
	clipped := false
	if len(norm) > maxLines {
		norm = norm[len(norm)-maxLines:]
		clipped = true
	}

	// Apply maxChars from the bottom while preserving whole lines.
	var selected []string
	totalRunes := 0
	for i := len(norm) - 1; i >= 0; i-- {
		rLen := len([]rune(norm[i])) + 1
		if len(selected) > 0 && totalRunes+rLen > maxChars {
			clipped = true
			break
		}
		selected = append(selected, norm[i])
		totalRunes += rLen
	}
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	for len(selected) > 0 && (strings.TrimSpace(selected[0]) == "" || selected[0] == "---") {
		selected = selected[1:]
	}
	if len(selected) == 0 {
		return ""
	}

	out := strings.Join(selected, "\n")
	if clipped || alreadyClipped {
		out = clippedConclusionPrefix + "\n\n" + out
	}
	return out
}

func stripBottomTUIChrome(lines []string) []string {
	n := len(lines)
	searchFrom := n - 14
	if searchFrom < 0 {
		searchFrom = 0
	}

	// Find bottommost pair of horizontal rules framing an input prompt within the tail window.
	bottomRule := -1
	topRule := -1
	for i := n - 1; i >= searchFrom; i-- {
		if isHorizontalRule(strings.TrimSpace(lines[i])) {
			if bottomRule == -1 {
				bottomRule = i
			} else if bottomRule-i <= 5 {
				topRule = i
				break
			} else {
				bottomRule = i
			}
		}
	}

	if topRule >= 0 && bottomRule > topRule {
		// Verify between topRule and bottomRule looks like an input box (empty or prompt line).
		looksLikeInput := true
		for i := topRule + 1; i < bottomRule; i++ {
			t := strings.TrimSpace(lines[i])
			if t != "" && t != ">" && t != "❯" && !strings.HasPrefix(t, "> ") && !strings.HasPrefix(t, "❯ ") {
				looksLikeInput = false
				break
			}
		}
		if looksLikeInput {
			cut := topRule
			// Pi renders a model/telemetry line immediately above the top rule.
			if cut > 0 && isTelemetryBar(strings.TrimSpace(lines[cut-1])) {
				cut--
			}
			lines = lines[:cut]
		}
	}

	// Trim any remaining trailing bare shell prompts or status bars.
	for len(lines) > 0 {
		t := strings.TrimSpace(lines[len(lines)-1])
		if t == "" || t == ">" || t == "❯" || t == "$" || t == "%" || isTelemetryBar(t) || strings.HasPrefix(t, "🔌 MCP:") {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}
	return lines
}

func findLatestTurnStart(lines []string) int {
	// Look backward up to 100 lines for a clear TUI user prompt marker (e.g. "❯ ..." or "> ..." surrounded by blank/rule)
	// without cutting inside short answers.
	limit := len(lines) - 100
	if limit < 0 {
		limit = 0
	}
	for i := len(lines) - 1; i >= limit; i-- {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "❯ ") || strings.HasPrefix(t, "› ") {
			return i + 1
		}
		if strings.HasPrefix(t, "> ") && i > 0 && (lines[i-1] == "---" || strings.TrimSpace(lines[i-1]) == "") {
			// Skip markdown blockquotes if followed immediately by non-rule lines; TUI prompt usually precedes blank/rule or answer start.
			if i+1 < len(lines) && (lines[i+1] == "---" || strings.TrimSpace(lines[i+1]) == "") {
				return i + 1
			}
		}
	}
	return 0
}

func isHorizontalRule(s string) bool {
	if len([]rune(s)) < 6 || strings.ContainsRune(s, ' ') {
		return false
	}
	for _, r := range s {
		switch r {
		case '─', '━', '═', '-', '_':
		default:
			return false
		}
	}
	return true
}

func isTelemetryBar(s string) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, " │ ") || strings.Contains(s, " | ") {
		if strings.Contains(s, "think:") ||
			strings.Contains(s, "idle") ||
			strings.Contains(s, "tokens") ||
			strings.Contains(s, "cache") ||
			strings.Contains(s, "Gemini") ||
			strings.Contains(s, "Claude") ||
			strings.Contains(s, "DeepSeek") ||
			strings.Contains(s, "1.0M") ||
			strings.Contains(s, "auto") {
			return true
		}
	}
	return false
}

func isStartupArt(s string) bool {
	for _, r := range s {
		if r == '▄' || r == '▀' || r == '█' {
			return true
		}
	}
	return false
}

func buildConclusionElements(s string) []any {
	segments := parseConclusionSegments(s)
	if len(segments) == 0 {
		return nil
	}

	elements := make([]any, 0, len(segments))
	tableCount := 0
	for _, seg := range segments {
		if seg.table != nil {
			if tableCount < maxFeishuCardTables {
				elements = append(elements, buildFeishuTableElement(seg.table, tableCount))
				tableCount++
				continue
			}
			// Feishu Card 2.0 allows at most 5 native table components per card.
			seg.markdown = formatTableFallbackMarkdown(seg.table)
		}
		if strings.TrimSpace(seg.markdown) == "" {
			continue
		}
		elements = append(elements, map[string]any{
			"tag":    "column_set",
			"margin": "0px 0px 8px 0px",
			"columns": []any{
				map[string]any{
					"tag":              "column",
					"width":            "weighted",
					"weight":           1,
					"background_style": "default",
					"padding":          "4px 2px",
					"elements": []any{
						map[string]any{
							"tag":     "markdown",
							"content": seg.markdown,
						},
					},
				},
			},
		})
	}
	return elements
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
	} else if c := ExtractTerminalConclusion(n.Tail, 3, 180); c != "" {
		parts = append(parts, escapeMarkdownInline(c))
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
