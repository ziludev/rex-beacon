package control

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/ziludev/rex-beacon/internal/notify"
	"github.com/ziludev/rex-beacon/internal/rexcli"
)

// Controller handles bidirectional chat commands and interactive button callbacks,
// translating them into Rex API calls (`BlockWrite`, `BlockFormatText`, inbox queries).
type Controller struct {
	rex  rexcli.API
	refs *notify.RefStore
}

// NewController creates a Controller backed by Rex CLI and a notification reference store.
func NewController(rex rexcli.API, refs *notify.RefStore) *Controller {
	return &Controller{rex: rex, refs: refs}
}

// InboxEntry represents one active status-reporting Rex terminal pane.
type InboxEntry struct {
	Rank         int
	SessionID    string
	SessionLabel string
	WindowLabel  string
	BlockID      string
	BlockLabel   string
	State        string
	Kind         string
	App          string
	Title        string
	Msg          string
	UpdatedAt    string
}

// ActionResult is the outcome of a card or inline-keyboard button interaction.
type ActionResult struct {
	Toast      string
	DetailText string
	BlockID    string
	IsError    bool
}

// HandleButtonAction executes a callback button action ("reply" or "peek").
func (c *Controller) HandleButtonAction(ctx context.Context, action, blockID, text, label string) ActionResult {
	fullID, blockLabel, err := c.ResolveBlockID(ctx, blockID)
	if err != nil {
		return ActionResult{Toast: err.Error(), IsError: true}
	}
	if blockLabel == "" {
		blockLabel = notify.ShortID(fullID)
	}

	switch strings.ToLower(strings.TrimSpace(action)) {
	case "reply":
		if text == "" {
			text = "\r"
		}
		if err := c.rex.BlockWrite(ctx, fullID, text); err != nil {
			return ActionResult{Toast: "发送失败: " + err.Error(), BlockID: fullID, IsError: true}
		}
		shown := strings.TrimSpace(label)
		if shown == "" {
			shown = formatSentPreview(text)
		}
		return ActionResult{
			Toast:   fmt.Sprintf("已发送 %s → %s", shown, blockLabel),
			BlockID: fullID,
		}

	case "peek":
		out, err := c.PeekOutput(ctx, fullID, 35)
		if err != nil {
			return ActionResult{Toast: "读取输出失败: " + err.Error(), BlockID: fullID, IsError: true}
		}
		if out == "" {
			out = "（终端暂无可显示输出）"
		}
		return ActionResult{
			Toast:      "已获取最新输出",
			DetailText: fmt.Sprintf("📟 %s (%s)\n%s", blockLabel, notify.ShortID(fullID), out),
			BlockID:    fullID,
		}

	default:
		return ActionResult{Toast: "不支持的操作: " + action, BlockID: fullID, IsError: true}
	}
}

// HandleTextCommand parses a chat command or quoted reply and returns a user-facing response text.
func (c *Controller) HandleTextCommand(ctx context.Context, rawText, replyToMsgID string) string {
	text := cleanIncomingText(rawText)
	if text == "" {
		return commandHelpText()
	}

	// 1. Quoted reply to an earlier notification card -> route directly to that pane.
	if replyToMsgID != "" && c.refs != nil {
		if ref, ok := c.refs.LookupMessage(replyToMsgID); ok {
			return c.handleQuotedReply(ctx, ref, text)
		}
	}

	// 2. Explicit bot commands (/inbox, /peek, /reply, /y, /1, /n, /enter, /ctrlc, /help).
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/help", "help", "帮助", "?":
		return commandHelpText()

	case "/inbox", "inbox", "状态":
		return c.FormatInbox(ctx)

	case "/peek", "peek":
		target := ""
		if len(fields) >= 2 {
			target = fields[1]
		}
		fullID, label, err := c.ResolveBlockID(ctx, target)
		if err != nil {
			return "❌ " + err.Error()
		}
		out, err := c.PeekOutput(ctx, fullID, 35)
		if err != nil {
			return "❌ 读取输出失败: " + err.Error()
		}
		if out == "" {
			out = "（终端暂无可显示输出）"
		}
		return fmt.Sprintf("📟 %s (%s)\n\n%s", label, notify.ShortID(fullID), out)

	case "/y", "/yes":
		return c.handleQuickCommand(ctx, fields[1:], "y\r", "y")
	case "/1":
		return c.handleQuickCommand(ctx, fields[1:], "1\r", "1")
	case "/n", "/no":
		return c.handleQuickCommand(ctx, fields[1:], "n\r", "n")
	case "/enter":
		return c.handleQuickCommand(ctx, fields[1:], "\r", "Enter")
	case "/ctrlc", "/stop":
		return c.handleQuickCommand(ctx, fields[1:], "\x03", "Ctrl+C")

	case "/reply", "/send", "reply", "send":
		if len(fields) < 3 {
			return "用法: /reply <分屏ID> <内容>\n例如: /reply 01a18b97 y  或  /reply 01a18b97 继续执行"
		}
		target := fields[1]
		body := extractArgTail(text, 2)
		fullID, label, err := c.ResolveBlockID(ctx, target)
		if err != nil {
			return "❌ " + err.Error()
		}
		if err := c.rex.BlockWrite(ctx, fullID, body+"\r"); err != nil {
			return "❌ 发送失败: " + err.Error()
		}
		return fmt.Sprintf("✅ 已发送到 %s (%s): %s", label, notify.ShortID(fullID), body)

	default:
		return "未识别的指令。发送 /inbox 查看所有终端状态，引用通知卡片可直接回复内容，或发送 /help 查看用法。"
	}
}

func (c *Controller) handleQuotedReply(ctx context.Context, ref notify.SentMessageRef, text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	label := ref.App
	if ref.SessionLabel != "" {
		if label != "" {
			label = ref.SessionLabel + " · " + label
		} else {
			label = ref.SessionLabel
		}
	}
	if label == "" {
		label = notify.ShortID(ref.BlockID)
	}

	switch lower {
	case "/peek", "peek":
		out, err := c.PeekOutput(ctx, ref.BlockID, 35)
		if err != nil {
			return "❌ 读取输出失败: " + err.Error()
		}
		if out == "" {
			out = "（终端暂无可显示输出）"
		}
		return fmt.Sprintf("📟 %s (%s)\n\n%s", label, notify.ShortID(ref.BlockID), out)
	case "/ctrlc", "ctrl+c", "ctrlc":
		if err := c.rex.BlockWrite(ctx, ref.BlockID, "\x03"); err != nil {
			return "❌ 发送失败: " + err.Error()
		}
		return fmt.Sprintf("✅ 已发送 Ctrl+C → %s (%s)", label, notify.ShortID(ref.BlockID))
	case "/enter", "enter":
		if err := c.rex.BlockWrite(ctx, ref.BlockID, "\r"); err != nil {
			return "❌ 发送失败: " + err.Error()
		}
		return fmt.Sprintf("✅ 已发送 Enter → %s (%s)", label, notify.ShortID(ref.BlockID))
	default:
		if err := c.rex.BlockWrite(ctx, ref.BlockID, text+"\r"); err != nil {
			return "❌ 发送失败: " + err.Error()
		}
		return fmt.Sprintf("✅ 已回复到 %s (%s): %s", label, notify.ShortID(ref.BlockID), text)
	}
}

func (c *Controller) handleQuickCommand(ctx context.Context, args []string, payload, label string) string {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	fullID, name, err := c.ResolveBlockID(ctx, target)
	if err != nil {
		return "❌ " + err.Error()
	}
	if err := c.rex.BlockWrite(ctx, fullID, payload); err != nil {
		return "❌ 发送失败: " + err.Error()
	}
	return fmt.Sprintf("✅ 已发送 %s → %s (%s)", label, name, notify.ShortID(fullID))
}

// PeekOutput fetches and cleans recent terminal output for blockID.
func (c *Controller) PeekOutput(ctx context.Context, blockID string, maxLines int) (string, error) {
	raw, err := c.rex.BlockFormatText(ctx, blockID)
	if err != nil {
		return "", err
	}
	if maxLines <= 0 {
		maxLines = 35
	}
	return notify.ExtractTerminalConclusion(raw, maxLines, 2400), nil
}

// ListInbox collects active status-reporting Rex panes sorted by urgency.
func (c *Controller) ListInbox(ctx context.Context) ([]InboxEntry, error) {
	list, err := c.rex.SessionList(ctx)
	if err != nil {
		return nil, err
	}
	rank := map[string]int{"blocked": 1, "error": 2, "done": 3, "working": 4, "idle": 5}
	var entries []InboxEntry
	for _, s := range list.Sessions {
		view, err := c.rex.SessionView(ctx, s.SessionID)
		if err != nil || view == nil {
			continue
		}
		for _, w := range view.Windows {
			for _, l := range w.Layers {
				for _, b := range l.Blocks {
					if b.CreatorName != "com.superlogical.terminal" {
						continue
					}
					st, err := c.rex.BlockProgramStatus(ctx, b.BlockID)
					if err != nil || st == nil {
						continue
					}
					for _, rec := range st.Records {
						app := rec.EffectiveApp
						if app == "" {
							app = rec.App
						}
						r := rank[rec.State]
						if r == 0 {
							r = 9
						}
						entries = append(entries, InboxEntry{
							Rank:         r,
							SessionID:    s.SessionID,
							SessionLabel: s.Label,
							WindowLabel:  w.Label,
							BlockID:      b.BlockID,
							BlockLabel:   b.Label,
							State:        rec.State,
							Kind:         rec.Kind,
							App:          app,
							Title:        rec.Title,
							Msg:          rec.Msg,
							UpdatedAt:    rec.UpdatedAt,
						})
					}
				}
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Rank != entries[j].Rank {
			return entries[i].Rank < entries[j].Rank
		}
		return entries[i].UpdatedAt > entries[j].UpdatedAt
	})
	return entries, nil
}

// FormatInbox returns a human-readable summary of active Rex panes.
func (c *Controller) FormatInbox(ctx context.Context) string {
	entries, err := c.ListInbox(ctx)
	if err != nil {
		return "❌ 查询 Rex 状态失败: " + err.Error()
	}
	if len(entries) == 0 {
		return "当前没有活跃的终端状态记录。"
	}
	var sb strings.Builder
	sb.WriteString("📋 Rex 终端状态收件箱：\n")
	for _, e := range entries {
		loc := e.SessionLabel
		if loc == "" {
			loc = notify.ShortID(e.SessionID)
		}
		if e.WindowLabel != "" {
			loc += "/" + e.WindowLabel
		}
		line := fmt.Sprintf("- [%s] %s (%s) · %s", strings.ToUpper(e.State), e.App, notify.ShortID(e.BlockID), loc)
		if e.Msg != "" {
			line += " — " + e.Msg
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n可用指令: /peek <ID>  |  /y <ID>  |  /reply <ID> <文本>")
	return strings.TrimSpace(sb.String())
}

// ResolveBlockID resolves a full ID, short suffix, or auto-selects when unambiguous.
func (c *Controller) ResolveBlockID(ctx context.Context, idOrShort string) (string, string, error) {
	q := strings.TrimSpace(idOrShort)
	if q == "" {
		entries, err := c.ListInbox(ctx)
		if err != nil {
			return "", "", fmt.Errorf("查询终端列表失败: %w", err)
		}
		var blocked []InboxEntry
		for _, e := range entries {
			if e.State == "blocked" {
				blocked = append(blocked, e)
			}
		}
		if len(blocked) == 1 {
			return blocked[0].BlockID, formatEntryLabel(blocked[0]), nil
		}
		if len(blocked) > 1 {
			return "", "", fmt.Errorf("当前有 %d 个阻塞分屏，请指定分屏 ID（发送 /inbox 查看）", len(blocked))
		}
		if len(entries) == 1 {
			return entries[0].BlockID, formatEntryLabel(entries[0]), nil
		}
		return "", "", fmt.Errorf("请指定分屏 ID（发送 /inbox 查看）")
	}

	if strings.HasPrefix(q, "block:") && len(q) > 20 {
		return q, notify.ShortID(q), nil
	}
	if c.refs != nil {
		if full, ok := c.refs.LookupShortBlock(q); ok {
			return full, notify.ShortID(full), nil
		}
	}

	list, err := c.rex.SessionList(ctx)
	if err != nil {
		return "", "", fmt.Errorf("查询终端列表失败: %w", err)
	}
	type match struct {
		blockID string
		label   string
	}
	var matches []match
	for _, s := range list.Sessions {
		view, err := c.rex.SessionView(ctx, s.SessionID)
		if err != nil || view == nil {
			continue
		}
		for _, w := range view.Windows {
			for _, l := range w.Layers {
				for _, b := range l.Blocks {
					if b.CreatorName != "com.superlogical.terminal" {
						continue
					}
					short := notify.ShortID(b.BlockID)
					if b.BlockID == q || short == q || strings.HasSuffix(b.BlockID, q) || strings.HasPrefix(short, q) {
						lbl := b.Label
						if s.Label != "" {
							lbl = s.Label + " · " + lbl
						}
						matches = append(matches, match{blockID: b.BlockID, label: lbl})
					}
				}
			}
		}
	}
	if len(matches) == 1 {
		return matches[0].blockID, matches[0].label, nil
	}
	if len(matches) > 1 {
		return "", "", fmt.Errorf("分屏 ID %q 匹配到多个结果，请多输入几位", q)
	}
	return "", "", fmt.Errorf("未找到分屏 %q（发送 /inbox 查看可用 ID）", q)
}

func formatEntryLabel(e InboxEntry) string {
	name := e.App
	if name == "" {
		name = e.BlockLabel
	}
	if e.SessionLabel != "" && name != "" {
		return e.SessionLabel + " · " + name
	}
	if name != "" {
		return name
	}
	return notify.ShortID(e.BlockID)
}

func formatSentPreview(text string) string {
	switch text {
	case "\r", "\n":
		return "Enter"
	case "\x03":
		return "Ctrl+C"
	default:
		return strings.TrimSpace(text)
	}
}

func cleanIncomingText(s string) string {
	// Strip Feishu group mention placeholders like "@_user_1" and Telegram @bot suffix in "/cmd@MyBot".
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return ""
	}
	var kept []string
	for i, f := range fields {
		if strings.HasPrefix(f, "@_user_") {
			continue
		}
		if i == 0 && strings.HasPrefix(f, "/") {
			if idx := strings.IndexByte(f, '@'); idx > 0 {
				f = f[:idx]
			}
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

func extractArgTail(text string, skipWords int) string {
	i := 0
	n := len(text)
	for w := 0; w < skipWords && i < n; w++ {
		for i < n && unicode.IsSpace(rune(text[i])) {
			i++
		}
		for i < n && !unicode.IsSpace(rune(text[i])) {
			i++
		}
	}
	for i < n && unicode.IsSpace(rune(text[i])) {
		i++
	}
	return strings.TrimSpace(text[i:])
}

func commandHelpText() string {
	return strings.TrimSpace(`可用指令：
- 引用任意通知卡片直接回复：把回复内容发送到对应终端并回车（回复 peek / ctrlc / enter 可看输出或中断）
- /inbox：查看所有活跃终端状态与短 ID
- /peek [ID]：查看指定终端最新输出
- /y [ID] | /1 [ID] | /n [ID] | /ctrlc [ID]：快捷确认/中断
- /reply <ID> <内容>：向指定终端发送任意输入`)
}
