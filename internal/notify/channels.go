package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
)

type errCodeResp struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func postJSON(ctx context.Context, client *http.Client, targetURL string, headers map[string]string, payload any) (int, []byte, error) {
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	return postRawJSON(ctx, client, targetURL, headers, rawBody)
}

func postRawJSON(ctx context.Context, client *http.Client, targetURL string, headers map[string]string, rawBody []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(rawBody))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, rawResp, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(rawResp)))
	}
	return resp.StatusCode, rawResp, nil
}

func checkErrCodeJSON(channel string, rawResp []byte) error {
	if len(bytes.TrimSpace(rawResp)) == 0 {
		return nil
	}
	var res errCodeResp
	if err := json.Unmarshal(rawResp, &res); err != nil {
		return nil
	}
	if res.ErrCode != 0 {
		return fmt.Errorf("%s error (%d): %s", channel, res.ErrCode, res.ErrMsg)
	}
	return nil
}

func hmacSHA256Bytes(secret, msg string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	const ellipsis = "…"
	limit := maxBytes - len(ellipsis)
	if limit <= 0 {
		return ""
	}
	cut := 0
	for i := range s {
		if i > limit {
			break
		}
		cut = i
	}
	if cut == 0 {
		return ""
	}
	return s[:cut] + ellipsis
}

// formatConclusionForPlatform extracts the terminal conclusion and formats any parsed
// tables as compact readable rows (using bold markers or plain lines) suitable for narrow chat clients.
func formatConclusionForPlatform(tail string, boldOpen, boldClose string, maxRunes int) string {
	c := ExtractTerminalConclusion(tail, 35, maxRunes)
	if c == "" {
		return ""
	}
	segs := parseConclusionSegments(c)
	if len(segs) == 0 {
		return truncateRunes(c, maxRunes)
	}
	var out []string
	for _, seg := range segs {
		if seg.table != nil {
			out = append(out, formatTableAsBulletLines(seg.table, boldOpen, boldClose))
		} else if strings.TrimSpace(seg.markdown) != "" {
			out = append(out, seg.markdown)
		}
	}
	return truncateRunes(strings.Join(out, "\n\n"), maxRunes)
}

func formatTableAsBulletLines(tbl *parsedTable, boldOpen, boldClose string) string {
	hasNamedHeaders := false
	for i, h := range tbl.headers {
		t := strings.TrimSpace(h)
		if t != "" && t != fmt.Sprintf("列 %d", i+1) {
			hasNamedHeaders = true
			break
		}
	}
	var lines []string
	for _, row := range tbl.rows {
		var parts []string
		for i := range tbl.headers {
			val := ""
			if i < len(row) {
				val = strings.TrimSpace(row[i])
			}
			if val == "" {
				continue
			}
			if hasNamedHeaders && i < len(tbl.headers) && strings.TrimSpace(tbl.headers[i]) != "" {
				parts = append(parts, fmt.Sprintf("%s%s%s: %s", boldOpen, strings.TrimSpace(tbl.headers[i]), boldClose, val))
			} else {
				parts = append(parts, val)
			}
		}
		if len(parts) > 0 {
			lines = append(lines, "- "+strings.Join(parts, " | "))
		}
	}
	return strings.Join(lines, "\n")
}

func buildMetaPairs(n Notification) [][2]string {
	var pairs [][2]string
	if dir := shortenPath(n.Cwd, 36); dir != "" {
		pairs = append(pairs, [2]string{"目录", dir})
	}
	if prog := formatProgram(n); prog != "" {
		pairs = append(pairs, [2]string{"程序", prog})
	}
	if loc := formatHeaderLocation(n); loc != "" {
		pairs = append(pairs, [2]string{"会话", loc})
	}
	if dur := FormatDuration(n.Duration); dur != "" {
		pairs = append(pairs, [2]string{"耗时", dur})
	}
	if right := formatHostAndTime(n); right != "" {
		pairs = append(pairs, [2]string{"节点", right})
	}
	return pairs
}

// --- 1. DingTalk Custom Robot Webhook ---

// DingTalkNotifier sends markdown alerts via a DingTalk custom robot webhook.
type DingTalkNotifier struct {
	cfg        config.DingTalkConfig
	httpClient *http.Client
	now        func() time.Time
}

// NewDingTalkNotifier validates cfg and creates a DingTalkNotifier.
func NewDingTalkNotifier(cfg config.DingTalkConfig) (*DingTalkNotifier, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("dingtalk requires url")
	}
	return &DingTalkNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		now:        time.Now,
	}, nil
}

func (d *DingTalkNotifier) Name() string { return "dingtalk" }

// SignDingTalkURL appends timestamp and URL-encoded HMAC-SHA256 signature to webhookURL.
func SignDingTalkURL(webhookURL, secret string, tsMillis int64) (string, error) {
	if secret == "" {
		return webhookURL, nil
	}
	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", err
	}
	tsStr := strconv.FormatInt(tsMillis, 10)
	sig := base64.StdEncoding.EncodeToString(hmacSHA256Bytes(secret, tsStr+"\n"+secret))
	q := u.Query()
	q.Set("timestamp", tsStr)
	q.Set("sign", sig)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (d *DingTalkNotifier) Send(ctx context.Context, n Notification) error {
	targetURL, err := SignDingTalkURL(d.cfg.URL, d.cfg.Secret, d.now().UnixMilli())
	if err != nil {
		return err
	}
	th := resolveTheme(n)
	var sb strings.Builder
	fmt.Fprintf(&sb, "### %s\n\n", th.title)
	if msg := escapeMarkdownInline(n.Msg); msg != "" {
		fmt.Fprintf(&sb, "**%s**\n\n", msg)
	}
	if body := formatConclusionForPlatform(n.Tail, "**", "**", 2400); body != "" {
		sb.WriteString(body + "\n\n")
	}
	for _, kv := range buildMetaPairs(n) {
		fmt.Fprintf(&sb, "- **%s**: `%s`\n", kv[0], kv[1])
	}
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": th.title,
			"text":  truncateRunes(strings.TrimSpace(sb.String()), 3800),
		},
	}
	_, rawResp, err := postJSON(ctx, d.httpClient, targetURL, nil, payload)
	if err != nil {
		return fmt.Errorf("dingtalk: %w", err)
	}
	return checkErrCodeJSON("dingtalk", rawResp)
}

// --- 2. WeCom (企业微信) Group Bot Webhook ---

// WeComNotifier sends markdown alerts via a WeCom group bot webhook.
type WeComNotifier struct {
	cfg        config.WeComConfig
	httpClient *http.Client
}

// NewWeComNotifier validates cfg and creates a WeComNotifier.
func NewWeComNotifier(cfg config.WeComConfig) (*WeComNotifier, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("wecom requires url")
	}
	return &WeComNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (w *WeComNotifier) Name() string { return "wecom" }

func (w *WeComNotifier) Send(ctx context.Context, n Notification) error {
	th := resolveTheme(n)
	color := "comment"
	switch n.State {
	case "done":
		color = "info"
	case "blocked", "error":
		color = "warning"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "### <font color=\"%s\">%s</font>\n", color, th.title)
	if msg := escapeMarkdownInline(n.Msg); msg != "" {
		fmt.Fprintf(&sb, "**%s**\n", msg)
	}
	if body := formatConclusionForPlatform(n.Tail, "**", "**", 2000); body != "" {
		sb.WriteString("\n" + body + "\n")
	}
	if pairs := buildMetaPairs(n); len(pairs) > 0 {
		sb.WriteString("\n")
		for _, kv := range pairs {
			fmt.Fprintf(&sb, "> %s：<font color=\"comment\">%s</font>\n", kv[0], kv[1])
		}
	}

	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": truncateUTF8Bytes(strings.TrimSpace(sb.String()), 4000),
		},
	}
	_, rawResp, err := postJSON(ctx, w.httpClient, w.cfg.URL, nil, payload)
	if err != nil {
		return fmt.Errorf("wecom: %w", err)
	}
	return checkErrCodeJSON("wecom", rawResp)
}

// --- 3. Discord Webhook (Rich Embed) ---

// DiscordNotifier sends Rich Embed notifications via a Discord channel webhook.
type DiscordNotifier struct {
	cfg        config.DiscordConfig
	httpClient *http.Client
}

// NewDiscordNotifier validates cfg and creates a DiscordNotifier.
func NewDiscordNotifier(cfg config.DiscordConfig) (*DiscordNotifier, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("discord requires url")
	}
	return &DiscordNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (d *DiscordNotifier) Name() string { return "discord" }

func (d *DiscordNotifier) Send(ctx context.Context, n Notification) error {
	th := resolveTheme(n)
	color := 0x3B82F6
	switch n.State {
	case "blocked":
		color = 0xF59E0B
	case "error":
		color = 0xEF4444
	case "done":
		color = 0x22C55E
	}

	var descParts []string
	if msg := escapeMarkdownInline(n.Msg); msg != "" {
		descParts = append(descParts, "**"+msg+"**")
	}
	if body := formatConclusionForPlatform(n.Tail, "**", "**", 3000); body != "" {
		descParts = append(descParts, body)
	}

	var fields []map[string]any
	for _, kv := range buildMetaPairs(n) {
		val := kv[1]
		if kv[0] == "目录" {
			val = "`" + val + "`"
		}
		fields = append(fields, map[string]any{
			"name":   kv[0],
			"value":  truncateRunes(val, 1000),
			"inline": true,
		})
	}

	embed := map[string]any{
		"title":       truncateRunes(th.title, 256),
		"description": truncateRunes(strings.Join(descParts, "\n\n"), 3800),
		"color":       color,
		"fields":      fields,
	}
	if !n.Timestamp.IsZero() {
		embed["timestamp"] = n.Timestamp.UTC().Format(time.RFC3339)
	}
	if n.State != "" {
		embed["footer"] = map[string]string{"text": "rex-beacon · " + n.State}
	}

	payload := map[string]any{
		"embeds": []any{embed},
	}
	if strings.TrimSpace(d.cfg.Username) != "" {
		payload["username"] = d.cfg.Username
	}
	_, _, err := postJSON(ctx, d.httpClient, d.cfg.URL, nil, payload)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	return nil
}

// --- 4. Slack Incoming Webhook (Attachment + Block Kit) ---

// SlackNotifier sends colored Block Kit alerts via a Slack incoming webhook.
type SlackNotifier struct {
	cfg        config.SlackConfig
	httpClient *http.Client
}

// NewSlackNotifier validates cfg and creates a SlackNotifier.
func NewSlackNotifier(cfg config.SlackConfig) (*SlackNotifier, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("slack requires url")
	}
	return &SlackNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (s *SlackNotifier) Name() string { return "slack" }

func (s *SlackNotifier) Send(ctx context.Context, n Notification) error {
	th := resolveTheme(n)
	color := "#3b82f6"
	switch n.State {
	case "blocked":
		color = "#f59e0b"
	case "error":
		color = "#ef4444"
	case "done":
		color = "#22c55e"
	}

	var secLines []string
	secLines = append(secLines, "*"+th.title+"*")
	if msg := escapeMarkdownInline(n.Msg); msg != "" {
		secLines = append(secLines, "*"+msg+"*")
	}
	if body := formatConclusionForPlatform(n.Tail, "*", "*", 2200); body != "" {
		secLines = append(secLines, body)
	}

	blocks := []any{
		map[string]any{
			"type": "section",
			"text": map[string]any{
				"type": "mrkdwn",
				"text": truncateRunes(strings.Join(secLines, "\n\n"), 2900),
			},
		},
	}
	if pairs := buildMetaPairs(n); len(pairs) > 0 {
		var metaParts []string
		for _, kv := range pairs {
			metaParts = append(metaParts, fmt.Sprintf("*%s:* `%s`", kv[0], kv[1]))
		}
		blocks = append(blocks, map[string]any{
			"type": "context",
			"elements": []any{
				map[string]any{
					"type": "mrkdwn",
					"text": truncateRunes(strings.Join(metaParts, "  ·  "), 2000),
				},
			},
		})
	}

	title, plainBody := BuildPlainSummary(n)
	payload := map[string]any{
		"text": title + " — " + plainBody,
		"attachments": []any{
			map[string]any{
				"color":  color,
				"blocks": blocks,
			},
		},
	}
	_, _, err := postJSON(ctx, s.httpClient, s.cfg.URL, nil, payload)
	if err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	return nil
}

// --- 5. Telegram Bot API ---

// TelegramNotifier sends HTML-formatted notifications via the Telegram Bot API.
type TelegramNotifier struct {
	cfg        config.TelegramConfig
	baseURL    string
	httpClient *http.Client
}

// NewTelegramNotifier validates cfg and creates a TelegramNotifier.
func NewTelegramNotifier(cfg config.TelegramConfig) (*TelegramNotifier, error) {
	if strings.TrimSpace(cfg.BotToken) == "" || strings.TrimSpace(cfg.ChatID) == "" {
		return nil, fmt.Errorf("telegram requires bot_token and chat_id")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	return &TelegramNotifier{
		cfg:        cfg,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (t *TelegramNotifier) Name() string { return "telegram" }

func (t *TelegramNotifier) Send(ctx context.Context, n Notification) error {
	th := resolveTheme(n)
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b>\n", html.EscapeString(th.title))
	if msg := escapeMarkdownInline(n.Msg); msg != "" {
		fmt.Fprintf(&sb, "<b>%s</b>\n", html.EscapeString(msg))
	}
	if body := formatConclusionForPlatform(n.Tail, "", "", 2600); body != "" {
		fmt.Fprintf(&sb, "\n<pre>%s</pre>\n", html.EscapeString(body))
	}
	if pairs := buildMetaPairs(n); len(pairs) > 0 {
		sb.WriteString("\n")
		for _, kv := range pairs {
			fmt.Fprintf(&sb, "<b>%s:</b> <code>%s</code>\n", html.EscapeString(kv[0]), html.EscapeString(kv[1]))
		}
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.cfg.BotToken)
	payload := map[string]any{
		"chat_id":                  t.cfg.ChatID,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"text":                     truncateRunes(strings.TrimSpace(sb.String()), 4000),
	}
	_, rawResp, err := postJSON(ctx, t.httpClient, endpoint, nil, payload)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	var res struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(rawResp, &res); err == nil && !res.OK {
		return fmt.Errorf("telegram error: %s", res.Description)
	}
	return nil
}

// --- 6. Generic / Custom JSON Webhook ---

// CustomWebhookNotifier posts structured JSON notification payloads to any HTTP endpoint.
type CustomWebhookNotifier struct {
	cfg        config.CustomWebhookConfig
	httpClient *http.Client
	now        func() time.Time
}

// NewCustomWebhookNotifier validates cfg and creates a CustomWebhookNotifier.
func NewCustomWebhookNotifier(cfg config.CustomWebhookConfig) (*CustomWebhookNotifier, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("custom_webhook requires url")
	}
	return &CustomWebhookNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		now:        time.Now,
	}, nil
}

func (c *CustomWebhookNotifier) Name() string { return "custom_webhook" }

func (c *CustomWebhookNotifier) Send(ctx context.Context, n Notification) error {
	title, summary := BuildPlainSummary(n)
	payload := map[string]any{
		"source":       "rex-beacon",
		"title":        title,
		"summary":      summary,
		"conclusion":   formatConclusionForPlatform(n.Tail, "**", "**", 3200),
		"notification": n,
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	headers := make(map[string]string, len(c.cfg.Headers)+2)
	for k, v := range c.cfg.Headers {
		headers[k] = v
	}
	if c.cfg.Secret != "" {
		ts := strconv.FormatInt(c.now().Unix(), 10)
		sig := hex.EncodeToString(hmacSHA256Bytes(c.cfg.Secret, ts+"."+string(rawBody)))
		headers["X-Rex-Beacon-Timestamp"] = ts
		headers["X-Rex-Beacon-Signature"] = "sha256=" + sig
	}

	if _, _, err := postRawJSON(ctx, c.httpClient, c.cfg.URL, headers, rawBody); err != nil {
		return fmt.Errorf("custom_webhook: %w", err)
	}
	return nil
}
