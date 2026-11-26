package control

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/ziludev/rex-beacon/internal/config"
	"github.com/ziludev/rex-beacon/internal/notify"
	"github.com/ziludev/rex-beacon/internal/rexcli"
)

type writeEntry struct {
	blockID string
	data    string
}

type fakeControlRex struct {
	mu       sync.Mutex
	sessions []rexcli.SessionSummary
	views    map[string]*rexcli.SessionViewResp
	statuses map[string]*rexcli.ProgramStatusResp
	outputs  map[string]string
	writes   []writeEntry
}

func (f *fakeControlRex) Whoami(context.Context) (*rexcli.WhoamiResp, error) {
	return &rexcli.WhoamiResp{}, nil
}
func (f *fakeControlRex) SessionList(context.Context) (*rexcli.SessionListResp, error) {
	return &rexcli.SessionListResp{Sessions: f.sessions}, nil
}
func (f *fakeControlRex) SessionView(_ context.Context, id string) (*rexcli.SessionViewResp, error) {
	return f.views[id], nil
}
func (f *fakeControlRex) ClientList(context.Context) (*rexcli.ClientListResp, error) {
	return &rexcli.ClientListResp{}, nil
}
func (f *fakeControlRex) BlockProcess(context.Context, string) (*rexcli.ProcessResp, error) {
	return &rexcli.ProcessResp{}, nil
}
func (f *fakeControlRex) BlockProgramStatus(_ context.Context, id string) (*rexcli.ProgramStatusResp, error) {
	return f.statuses[id], nil
}
func (f *fakeControlRex) BlockPorts(context.Context, string) (*rexcli.PortsResp, error) {
	return &rexcli.PortsResp{}, nil
}
func (f *fakeControlRex) BlockFormatText(_ context.Context, id string) (string, error) {
	return f.outputs[id], nil
}
func (f *fakeControlRex) BlockWrite(_ context.Context, blockID, data string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, writeEntry{blockID: blockID, data: data})
	return nil
}
func (f *fakeControlRex) StreamEvents(context.Context, string, bool, func([]byte) error) error {
	return nil
}

func newTestRex() *fakeControlRex {
	return &fakeControlRex{
		sessions: []rexcli.SessionSummary{{SessionID: "session:s1", Label: "rex-beacon"}},
		views: map[string]*rexcli.SessionViewResp{
			"session:s1": {
				SessionID: "session:s1",
				Label:     "rex-beacon",
				Windows: []rexcli.SessionWindow{
					{
						WindowID: "window:w1",
						Label:    "Agent",
						Layers: []rexcli.SessionLayer{
							{
								Blocks: []rexcli.SessionBlock{
									{BlockID: "block:01a18b97-1111-2222-3333-444455556666", Label: "claude", CreatorName: "com.superlogical.terminal"},
									{BlockID: "block:01a18b97-aaaa-bbbb-cccc-ddddeeeeffff", Label: "tests", CreatorName: "com.superlogical.terminal"},
								},
							},
						},
					},
				},
			},
		},
		statuses: map[string]*rexcli.ProgramStatusResp{
			"block:01a18b97-1111-2222-3333-444455556666": {
				Records: []rexcli.ProgramStatusRecord{
					{State: "blocked", Kind: "permission", App: "claude", Msg: "Approve Bash?", UpdatedAt: "2026-11-26T08:00:00Z"},
				},
			},
			"block:01a18b97-aaaa-bbbb-cccc-ddddeeeeffff": {
				Records: []rexcli.ProgramStatusRecord{
					{State: "done", App: "go", Msg: "ok", UpdatedAt: "2026-11-26T07:59:00Z"},
				},
			},
		},
		outputs: map[string]string{
			"block:01a18b97-1111-2222-3333-444455556666": "Running command\nApprove Bash rm -rf /tmp/x? [y/n]\n",
		},
	}
}

func TestControllerCommandsAndActions(t *testing.T) {
	rex := newTestRex()
	refs := notify.NewRefStore(100)
	refs.Record("om_msg_1", notify.Notification{
		BlockID:      "block:01a18b97-1111-2222-3333-444455556666",
		SessionLabel: "rex-beacon",
		App:          "claude",
	})
	ctrl := NewController(rex, refs)

	// 1. /inbox lists blocked first
	inbox := ctrl.HandleTextCommand(context.Background(), "/inbox", "")
	if !strings.Contains(inbox, "[BLOCKED] claude (444455556666)") || !strings.Contains(inbox, "[DONE] go (ddddeeeeffff)") {
		t.Fatalf("unexpected inbox output:\n%s", inbox)
	}

	// 2. /y with no arg auto-selects the single blocked pane
	res := ctrl.HandleTextCommand(context.Background(), "/y", "")
	if !strings.Contains(res, "已发送 y") {
		t.Fatalf("expected /y to auto-select single blocked pane, got %q", res)
	}
	if len(rex.writes) != 1 || rex.writes[0].data != "y\r" || rex.writes[0].blockID != "block:01a18b97-1111-2222-3333-444455556666" {
		t.Fatalf("unexpected writes after /y: %+v", rex.writes)
	}

	// 3. Quoted reply to notification card routes custom text + \r
	quoted := ctrl.HandleTextCommand(context.Background(), "用方案二继续", "om_msg_1")
	if !strings.Contains(quoted, "已回复到") {
		t.Fatalf("unexpected quoted reply output: %q", quoted)
	}
	if rex.writes[1].data != "用方案二继续\r" {
		t.Fatalf("unexpected quoted write data: %q", rex.writes[1].data)
	}

	// 4. Quoted reply "peek" returns pane output without writing to terminal
	peekQuoted := ctrl.HandleTextCommand(context.Background(), "peek", "om_msg_1")
	if !strings.Contains(peekQuoted, "Approve Bash") || len(rex.writes) != 2 {
		t.Fatalf("unexpected quoted peek: %q writes=%d", peekQuoted, len(rex.writes))
	}

	// 5. /reply with short ID
	rep := ctrl.HandleTextCommand(context.Background(), "/reply ddddeeeeffff ls -la", "")
	if !strings.Contains(rep, "已发送到") || rex.writes[2].blockID != "block:01a18b97-aaaa-bbbb-cccc-ddddeeeeffff" || rex.writes[2].data != "ls -la\r" {
		t.Fatalf("unexpected /reply result: %q writes=%+v", rep, rex.writes)
	}

	// 6. Plain chatter without reply/command does NOT write to terminal
	chatter := ctrl.HandleTextCommand(context.Background(), "你好在吗", "")
	if !strings.Contains(chatter, "未识别的指令") || len(rex.writes) != 3 {
		t.Fatalf("plain chatter should not write to terminal: %q writes=%d", chatter, len(rex.writes))
	}

	// 7. Button action peek & reply
	actRes := ctrl.HandleButtonAction(context.Background(), "peek", "444455556666", "", "")
	if actRes.IsError || !strings.Contains(actRes.DetailText, "Approve Bash") {
		t.Fatalf("unexpected button peek: %+v", actRes)
	}
	actReply := ctrl.HandleButtonAction(context.Background(), "reply", "block:01a18b97-1111-2222-3333-444455556666", "\x03", "Ctrl+C")
	if actReply.IsError || rex.writes[3].data != "\x03" {
		t.Fatalf("unexpected button reply: %+v writes=%+v", actReply, rex.writes)
	}
}

func TestFeishuBotAuthorizationAndCallbacks(t *testing.T) {
	rex := newTestRex()
	refs := notify.NewRefStore(10)
	ctrl := NewController(rex, refs)

	bot := NewFeishuBot(config.FeishuAppConfig{
		Enabled:       true,
		AppID:         "cli_x",
		AppSecret:     "sec_x",
		ReceiveID:     "ou_owner",
		ReceiveIDType: "open_id",
	}, ctrl, nil, log.New(io.Discard, "", 0))

	// Unauthorized user rejected
	unauthResp, err := bot.handleCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_stranger"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "reply", "block_id": "444455556666", "text": "y\r"}},
		},
	})
	if err != nil || unauthResp.Toast.Type != "error" || len(rex.writes) != 0 {
		t.Fatalf("expected unauthorized rejection, got resp=%+v err=%v writes=%d", unauthResp, err, len(rex.writes))
	}

	// Authorized owner succeeds
	authResp, err := bot.handleCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_owner"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "reply", "block_id": "444455556666", "text": "y\r", "label": "y"}},
		},
	})
	if err != nil || authResp.Toast.Type != "success" || len(rex.writes) != 1 || rex.writes[0].data != "y\r" {
		t.Fatalf("expected authorized success, got resp=%+v err=%v writes=%+v", authResp, err, rex.writes)
	}

	// Bot self message ignored cleanly
	botSender := "bot"
	if err := bot.handleMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender:  &larkim.EventSender{SenderType: &botSender},
			Message: &larkim.EventMessage{},
		},
	}); err != nil {
		t.Fatalf("handleMessage bot ignore failed: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramBotCallbacksAndCommands(t *testing.T) {
	rex := newTestRex()
	refs := notify.NewRefStore(10)
	ctrl := NewController(rex, refs)

	var answered []string
	var sentHTML []string

	bot := NewTelegramBot(config.TelegramConfig{
		Enabled:  true,
		BotToken: "123:tok",
		ChatID:   "777",
	}, ctrl, log.New(io.Discard, "", 0))
	bot.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch {
			case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
				answered = append(answered, body["text"].(string))
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				sentHTML = append(sentHTML, body["text"].(string))
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}

	// Inline button y
	bot.handleUpdate(context.Background(), tgUpdate{
		UpdateID: 1,
		CallbackQuery: &tgCallbackQuery{
			ID:      "cq_1",
			From:    tgUser{ID: 777},
			Message: &tgMessage{MessageID: 10, Chat: tgChat{ID: 777}},
			Data:    "b:444455556666:y",
		},
	})
	if len(rex.writes) != 1 || rex.writes[0].data != "y\r" || len(answered) != 1 {
		t.Fatalf("unexpected telegram callback result: writes=%+v answered=%v", rex.writes, answered)
	}

	// Command /inbox@MyBot
	bot.handleUpdate(context.Background(), tgUpdate{
		UpdateID: 2,
		Message: &tgMessage{
			MessageID: 11,
			From:      &tgUser{ID: 777},
			Chat:      tgChat{ID: 777},
			Text:      "/inbox@MyBot",
		},
	})
	if len(sentHTML) != 1 || !strings.Contains(sentHTML[0], "[BLOCKED]") {
		t.Fatalf("unexpected telegram /inbox response: %v", sentHTML)
	}
}
