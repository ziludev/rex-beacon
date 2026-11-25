package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func TestBuildFeishuCardAndSummary(t *testing.T) {
	n := Notification{
		EventKind:    "program_status",
		State:        "blocked",
		Kind:         "permission",
		App:          "claude",
		Msg:          "Approve Bash rm -rf /tmp/foo?",
		SessionID:    "session:01a18a82-66dd-753f-b9d7-8b88312ee1fa",
		SessionLabel: "rex-beacon",
		WindowLabel:  "Agent",
		BlockID:      "block:01a18b97-296a-73c4-bc16-cff98000212c",
		BlockLabel:   "claude",
		Cwd:          "/Users/alexwang/work/rex-beacon",
		Duration:     95 * time.Second,
		Tail:         "line 1\n```code```\nline 3",
		Timestamp:    time.Date(2026, 11, 26, 0, 10, 0, 0, time.UTC),
	}
	card := BuildFeishuCard(n)
	if card["schema"] != "2.0" {
		t.Fatalf("expected schema 2.0, got %v", card["schema"])
	}
	header := card["header"].(map[string]any)
	if header["template"] != "orange" {
		t.Fatalf("expected orange template, got %v", header["template"])
	}
	bodyMap := card["body"].(map[string]any)
	elems := bodyMap["elements"].([]any)
	if len(elems) < 3 {
		t.Fatalf("expected callout + metadata + collapsible_panel, got %d elements", len(elems))
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatal("invalid card json")
	}
	title, body := BuildPlainSummary(n)
	if title == "" || body == "" {
		t.Fatalf("expected non-empty plain summary: %q %q", title, body)
	}
	if got := CleanTerminalTail("──────────\n   \nline 1\nline 2\n", 5); got != "line 1\nline 2" {
		t.Fatalf("unexpected CleanTerminalTail: %q", got)
	}
}

func TestFeishuAppNotifierMock(t *testing.T) {
	var tokenCalls atomic.Int32
	var sendCalls atomic.Int32

	n, err := NewFeishuAppNotifier(config.FeishuAppConfig{
		Enabled:   true,
		AppID:     "cli_test",
		AppSecret: "sec_test",
		ReceiveID: "ou_test",
	})
	if err != nil {
		t.Fatalf("NewFeishuAppNotifier: %v", err)
	}
	n.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/open-apis/auth/v3/tenant_access_token/internal":
				tokenCalls.Add(1)
				return jsonResponse(200, map[string]any{
					"code":                0,
					"msg":                 "ok",
					"tenant_access_token": "t-test-token",
					"expire":              7200,
				}), nil
			case "/open-apis/im/v1/messages":
				if got := r.Header.Get("Authorization"); got != "Bearer t-test-token" {
					t.Fatalf("unexpected auth header: %q", got)
				}
				if r.URL.Query().Get("receive_id_type") != "open_id" {
					t.Fatalf("unexpected receive_id_type: %q", r.URL.Query().Get("receive_id_type"))
				}
				sendCalls.Add(1)
				return jsonResponse(200, map[string]any{"code": 0, "msg": "success"}), nil
			default:
				return jsonResponse(404, map[string]any{"code": 404}), nil
			}
		}),
	}

	for i := 0; i < 2; i++ {
		if err := n.Send(context.Background(), Notification{State: "done", App: "go", Msg: "ok"}); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("expected token caching (1 call), got %d", tokenCalls.Load())
	}
	if sendCalls.Load() != 2 {
		t.Fatalf("expected 2 send calls, got %d", sendCalls.Load())
	}
}

func TestFeishuWebhookNotifierMock(t *testing.T) {
	var gotBody map[string]any
	wh, err := NewFeishuWebhookNotifier(config.WebhookConfig{
		Enabled: true,
		URL:     "https://open.feishu.cn/open-apis/bot/v2/hook/test",
		Secret:  "my-secret",
	})
	if err != nil {
		t.Fatalf("NewFeishuWebhookNotifier: %v", err)
	}
	wh.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			return jsonResponse(200, map[string]any{"StatusCode": 0, "StatusMessage": "success"}), nil
		}),
	}
	if err := wh.Send(context.Background(), Notification{State: "error", App: "make", Msg: "build failed"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBody["msg_type"] != "interactive" || gotBody["sign"] == "" || gotBody["timestamp"] == "" {
		t.Fatalf("unexpected webhook payload: %#v", gotBody)
	}
}
