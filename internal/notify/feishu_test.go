package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
	if got := ExtractTerminalConclusion("──────────\n   \nline 1\nline 2\n", 5, 1000); got != "line 1\nline 2" {
		t.Fatalf("unexpected ExtractTerminalConclusion: %q", got)
	}

	piRaw := ` - POST /api/model 逐会话改写：ok=160 fail=0
 - 说明：已全部完成。
 DeepSeek-V4.1-Flash · DeepSeek | think:high | dir pi-durable-agent | ⎇ main
 ──────────────────────────────────────────
 >
 ──────────────────────────────────────────
 ↳ 全切吧
 🔌 MCP: 3 servers enabled`
	gotPi := ExtractTerminalConclusion(piRaw, 45, 3200)
	wantPi := " - POST /api/model 逐会话改写：ok=160 fail=0\n - 说明：已全部完成。"
	if gotPi != wantPi {
		t.Fatalf("unexpected pi conclusion:\ngot:  %q\nwant: %q", gotPi, wantPi)
	}

	agyRaw := `  • 第一条结论
  • 第二条结论
──────────────────────────────────────────
>
──────────────────────────────────────────
 ✦ Gemini 4 Hydrogen (High) │ ▰▰ 8%
 ○ idle │ ⎇ main* │ ~/learn/pi-durable-agent │ » auto`
	gotAgy := ExtractTerminalConclusion(agyRaw, 45, 3200)
	wantAgy := "  • 第一条结论\n  • 第二条结论"
	if gotAgy != wantAgy {
		t.Fatalf("unexpected agy conclusion:\ngot:  %q\nwant: %q", gotAgy, wantAgy)
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

func TestFeishuCardTableRendering(t *testing.T) {
	t.Run("borderless clipped screenshot repro", func(t *testing.T) {
		tail := "> *…（前文已省略）*\n\n" +
			"                          │ \"notation\")              │ 改\n" +
			"                          │                          │ 底\n" +
			"                          │                          │ 数\n" +
			"5.\n" +
			"底部折叠面板终端最近   │ expanded:           │ (src/feishu.ts:274-\n" +
			"输出（点击展开）       │ false，灰色圆角边框 │ 323) 已经是用同款"

		card := BuildFeishuCard(Notification{
			State: "done",
			App:   "agy",
			Msg:   "可以，而且几乎不需要大改架构。",
			Tail:  tail,
		})
		elems := card["body"].(map[string]any)["elements"].([]any)

		var tables []map[string]any
		for _, el := range elems {
			m := el.(map[string]any)
			if m["tag"] == "table" {
				tables = append(tables, m)
			}
		}
		if len(tables) != 2 {
			t.Fatalf("expected 2 native table elements, got %d (%#v)", len(tables), elems)
		}

		// Table 1: 13-line/3-line empty-col0 continuation merged into 1 row.
		r1 := tables[0]["rows"].([]any)
		if len(r1) != 1 {
			t.Fatalf("expected table 1 to merge into 1 row, got %d: %#v", len(r1), r1)
		}
		row1 := r1[0].(map[string]any)
		if row1["c1"] != "\"notation\")" || row1["c2"] != "改底数" {
			t.Fatalf("unexpected table 1 row: %#v", row1)
		}

		// Table 2: wrapped CJK+ASCII row merged into 1 clean row.
		r2 := tables[1]["rows"].([]any)
		if len(r2) != 1 {
			t.Fatalf("expected table 2 to merge into 1 row, got %d: %#v", len(r2), r2)
		}
		row2 := r2[0].(map[string]any)
		if row2["c0"] != "底部折叠面板终端最近输出（点击展开）" {
			t.Fatalf("unexpected col 0: %q", row2["c0"])
		}
		if row2["c1"] != "expanded: false，灰色圆角边框" {
			t.Fatalf("unexpected col 1: %q", row2["c1"])
		}
		if row2["c2"] != "(src/feishu.ts:274-323) 已经是用同款" {
			t.Fatalf("unexpected col 2: %q", row2["c2"])
		}
	})

	t.Run("borderless table with header and unmerged short rows", func(t *testing.T) {
		tail := "功能     │ 状态    │ 说明\n" +
			"─────────┼─────────┼──────────────────\n" +
			"卡片宽度 │ default │ 保持默认\n" +
			"状态标签 │ 正常    │ 最多 2 个"

		segs := parseConclusionSegments(tail)
		if len(segs) != 1 || segs[0].table == nil {
			t.Fatalf("expected 1 table segment, got %#v", segs)
		}
		tbl := segs[0].table
		if len(tbl.headers) != 3 || tbl.headers[0] != "功能" || tbl.headers[1] != "状态" || tbl.headers[2] != "说明" {
			t.Fatalf("unexpected headers: %#v", tbl.headers)
		}
		if len(tbl.rows) != 2 {
			t.Fatalf("expected 2 separate rows (not merged), got %d: %#v", len(tbl.rows), tbl.rows)
		}
		if tbl.rows[0][0] != "卡片宽度" || tbl.rows[1][0] != "状态标签" {
			t.Fatalf("unexpected rows: %#v", tbl.rows)
		}
	})

	t.Run("box-drawing table with outer borders", func(t *testing.T) {
		tail := "结论如下：\n" +
			"┌──────────┬────────────┐\n" +
			"│ 模块     │ 结果       │\n" +
			"├──────────┼────────────┤\n" +
			"│ notify   │ passed     │\n" +
			"│          │ 100%       │\n" +
			"├──────────┼────────────┤\n" +
			"│ daemon   │ passed     │\n" +
			"└──────────┴────────────┘\n" +
			"• 全部通过"

		segs := parseConclusionSegments(tail)
		if len(segs) != 3 {
			t.Fatalf("expected 3 segments (md + table + md), got %d: %#v", len(segs), segs)
		}
		if segs[0].markdown != "结论如下：" {
			t.Fatalf("unexpected seg 0: %q", segs[0].markdown)
		}
		if segs[1].table == nil || len(segs[1].table.rows) != 2 {
			t.Fatalf("unexpected seg 1 table: %#v", segs[1].table)
		}
		if segs[1].table.rows[0][1] != "passed 100%" {
			t.Fatalf("expected wrapped cell merged, got %q", segs[1].table.rows[0][1])
		}
		if segs[2].markdown != "- 全部通过" {
			t.Fatalf("unexpected seg 2: %q", segs[2].markdown)
		}
	})

	t.Run("gfm markdown pipe table", func(t *testing.T) {
		tail := "| File | Line | Status |\n| --- | :---: | ---: |\n| main.go | 42 | ok |\n| card.go | 100 | fixed |"
		segs := parseConclusionSegments(tail)
		if len(segs) != 1 || segs[0].table == nil {
			t.Fatalf("expected 1 markdown table segment, got %#v", segs)
		}
		tbl := segs[0].table
		if len(tbl.headers) != 3 || len(tbl.rows) != 2 || tbl.rows[1][2] != "fixed" {
			t.Fatalf("unexpected markdown table: %#v", tbl)
		}
	})

	t.Run("guards: single-col box, fenced code, shell pipes, and >5 tables", func(t *testing.T) {
		// Single-column box stays markdown code fence, not native table.
		singleBox := "┌──────────────┐\n│ hello world  │\n└──────────────┘"
		segs := parseConclusionSegments(singleBox)
		if len(segs) != 1 || segs[0].table != nil || !stringsContainsAll(segs[0].markdown, "```", "hello world") {
			t.Fatalf("expected single-column box to stay fenced markdown, got %#v", segs)
		}

		// Fenced code containing pipe table syntax is left untouched.
		fenced := "```markdown\n| a | b |\n|---|---|\n| 1 | 2 |\n```"
		segs = parseConclusionSegments(fenced)
		if len(segs) != 1 || segs[0].table != nil {
			t.Fatalf("expected fenced code not to parse as table, got %#v", segs)
		}

		// Two short coincidental shell/prose pipes must not become a table.
		shell := "foo | bar\nbaz | qux"
		segs = parseConclusionSegments(shell)
		if len(segs) != 1 || segs[0].table != nil {
			t.Fatalf("expected casual pipe lines not to parse as table, got %#v", segs)
		}

		// More than 5 tables caps native table components at 5.
		var many string
		for i := 0; i < 6; i++ {
			many += "| A | B |\n|---|---|\n| 1 | 2 |\n\n"
		}
		card := BuildFeishuCard(Notification{State: "done", Tail: many})
		elems := card["body"].(map[string]any)["elements"].([]any)
		tableCount := 0
		for _, el := range elems {
			if el.(map[string]any)["tag"] == "table" {
				tableCount++
			}
		}
		if tableCount != 5 {
			t.Fatalf("expected native table count capped at 5, got %d", tableCount)
		}
	})

	t.Run("double ExtractTerminalConclusion does not drop lines", func(t *testing.T) {
		var lines []string
		for i := 1; i <= 10; i++ {
			lines = append(lines, "line")
		}
		lines[0] = "first"
		lines[9] = "last"
		first := ExtractTerminalConclusion(strings.Join(lines, "\n"), 5, 3200)
		second := ExtractTerminalConclusion(first, 5, 3200)
		if first != second {
			t.Fatalf("double ExtractTerminalConclusion drifted:\nfirst:  %q\nsecond: %q", first, second)
		}
	})
}

func stringsContainsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !bytes.Contains([]byte(s), []byte(sub)) {
			return false
		}
	}
	return true
}
