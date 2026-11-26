package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
)

func sampleNotification() Notification {
	return Notification{
		EventKind:    "program_status",
		State:        "blocked",
		Kind:         "permission",
		App:          "claude",
		Msg:          "Approve Bash rm -rf /tmp/foo?",
		SessionID:    "session:01a18a82",
		SessionLabel: "rex-beacon",
		WindowLabel:  "Agent",
		BlockID:      "block:01a18b97",
		BlockLabel:   "claude",
		Cwd:          "/Users/alexwang/work/rex-beacon",
		Host:         "Mac.local",
		Duration:     42 * time.Second,
		Tail:         "Col1 │ Col2\n─────┼─────\nA    │ B\n",
		Timestamp:    time.Date(2026, 11, 26, 8, 30, 0, 0, time.UTC),
	}
}

func TestDingTalkNotifierMock(t *testing.T) {
	var gotURL string
	var gotBody map[string]any
	d, err := NewDingTalkNotifier(config.DingTalkConfig{
		Enabled: true,
		URL:     "https://oapi.dingtalk.com/robot/send?access_token=abc",
		Secret:  "SEC_test_secret",
	})
	if err != nil {
		t.Fatalf("NewDingTalkNotifier: %v", err)
	}
	d.now = func() time.Time { return time.UnixMilli(1700000000000) }
	d.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			return jsonResponse(200, map[string]any{"errcode": 0, "errmsg": "ok"}), nil
		}),
	}
	if err := d.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(gotURL, "timestamp=1700000000000") || !strings.Contains(gotURL, "sign=") {
		t.Fatalf("expected signed DingTalk URL, got %q", gotURL)
	}
	md := gotBody["markdown"].(map[string]any)
	if gotBody["msgtype"] != "markdown" || !strings.Contains(md["text"].(string), "**Col1**: A") {
		t.Fatalf("unexpected DingTalk body: %#v", gotBody)
	}
}

func TestWeComNotifierMock(t *testing.T) {
	var gotBody map[string]any
	w, err := NewWeComNotifier(config.WeComConfig{
		Enabled: true,
		URL:     "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test",
	})
	if err != nil {
		t.Fatalf("NewWeComNotifier: %v", err)
	}
	w.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			return jsonResponse(200, map[string]any{"errcode": 0, "errmsg": "ok"}), nil
		}),
	}
	n := sampleNotification()
	n.State = "done"
	if err := w.Send(context.Background(), n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	md := gotBody["markdown"].(map[string]any)
	if !strings.Contains(md["content"].(string), `<font color="info">`) {
		t.Fatalf("expected WeCom info color tag, got %q", md["content"])
	}
}

func TestDiscordNotifierMock(t *testing.T) {
	var gotBody map[string]any
	d, err := NewDiscordNotifier(config.DiscordConfig{
		Enabled:  true,
		URL:      "https://discord.com/api/webhooks/1/tok",
		Username: "rex-beacon",
	})
	if err != nil {
		t.Fatalf("NewDiscordNotifier: %v", err)
	}
	d.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
		}),
	}
	if err := d.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	embeds := gotBody["embeds"].([]any)
	emb := embeds[0].(map[string]any)
	if int(emb["color"].(float64)) != 0xF59E0B || gotBody["username"] != "rex-beacon" {
		t.Fatalf("unexpected Discord payload: %#v", gotBody)
	}
}

func TestSlackNotifierMock(t *testing.T) {
	var gotBody map[string]any
	s, err := NewSlackNotifier(config.SlackConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T/B/X",
	})
	if err != nil {
		t.Fatalf("NewSlackNotifier: %v", err)
	}
	s.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}),
	}
	n := sampleNotification()
	n.State = "error"
	if err := s.Send(context.Background(), n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	atts := gotBody["attachments"].([]any)
	att := atts[0].(map[string]any)
	if att["color"] != "#ef4444" {
		t.Fatalf("expected red Slack attachment color, got %#v", att)
	}
}

func TestTelegramNotifierMock(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	tg, err := NewTelegramNotifier(config.TelegramConfig{
		Enabled:  true,
		BotToken: "123:ABC",
		ChatID:   "9999",
	})
	if err != nil {
		t.Fatalf("NewTelegramNotifier: %v", err)
	}
	tg.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			return jsonResponse(200, map[string]any{"ok": true}), nil
		}),
	}
	n := sampleNotification()
	n.Msg = "Check <tag> & \"quotes\""
	if err := tg.Send(context.Background(), n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/bot123:ABC/sendMessage" || gotBody["parse_mode"] != "HTML" {
		t.Fatalf("unexpected Telegram request: path=%q body=%#v", gotPath, gotBody)
	}
	if !strings.Contains(gotBody["text"].(string), "&lt;tag&gt; &amp;") {
		t.Fatalf("expected escaped HTML entities, got %q", gotBody["text"])
	}
}

func TestCustomWebhookNotifierMock(t *testing.T) {
	var gotSig string
	var gotAuth string
	var gotBody map[string]any
	cw, err := NewCustomWebhookNotifier(config.CustomWebhookConfig{
		Enabled: true,
		URL:     "https://example.com/hook",
		Secret:  "top-secret",
		Headers: map[string]string{"Authorization": "Bearer abc"},
	})
	if err != nil {
		t.Fatalf("NewCustomWebhookNotifier: %v", err)
	}
	cw.now = func() time.Time { return time.Unix(1700000000, 0) }
	cw.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotSig = r.Header.Get("X-Rex-Beacon-Signature")
			gotAuth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			return jsonResponse(200, map[string]any{"ok": true}), nil
		}),
	}
	if err := cw.Send(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.HasPrefix(gotSig, "sha256=") || gotAuth != "Bearer abc" || gotBody["source"] != "rex-beacon" {
		t.Fatalf("unexpected custom webhook request: sig=%q auth=%q body=%#v", gotSig, gotAuth, gotBody)
	}
}
