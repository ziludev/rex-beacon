package daemon

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
	"github.com/ziludev/rex-beacon/internal/notify"
	"github.com/ziludev/rex-beacon/internal/rexcli"
)

type fakeRex struct {
	view    *rexcli.SessionViewResp
	proc    *rexcli.ProcessResp
	tail    string
	clients *rexcli.ClientListResp
}

func (f *fakeRex) Whoami(context.Context) (*rexcli.WhoamiResp, error) {
	return &rexcli.WhoamiResp{}, nil
}
func (f *fakeRex) SessionList(context.Context) (*rexcli.SessionListResp, error) {
	return &rexcli.SessionListResp{}, nil
}
func (f *fakeRex) SessionView(context.Context, string) (*rexcli.SessionViewResp, error) {
	return f.view, nil
}
func (f *fakeRex) ClientList(context.Context) (*rexcli.ClientListResp, error) {
	return f.clients, nil
}
func (f *fakeRex) BlockProcess(context.Context, string) (*rexcli.ProcessResp, error) {
	return f.proc, nil
}
func (f *fakeRex) BlockProgramStatus(context.Context, string) (*rexcli.ProgramStatusResp, error) {
	return &rexcli.ProgramStatusResp{}, nil
}
func (f *fakeRex) BlockPorts(context.Context, string) (*rexcli.PortsResp, error) {
	return &rexcli.PortsResp{}, nil
}
func (f *fakeRex) BlockFormatText(context.Context, string) (string, error) {
	return f.tail, nil
}
func (f *fakeRex) BlockWrite(context.Context, string, string) error { return nil }
func (f *fakeRex) StreamEvents(context.Context, string, bool, func([]byte) error) error {
	return nil
}

type captureNotifier struct {
	mu   sync.Mutex
	sent []notify.Notification
}

func (c *captureNotifier) Name() string { return "capture" }
func (c *captureNotifier) Send(_ context.Context, n notify.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, n)
	return nil
}

func TestWatcherTracksWorkingDurationAndDeduplicates(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Rules.CooldownSec = 10
	cfg.Rules.TailLines = 2

	api := &fakeRex{
		view: &rexcli.SessionViewResp{
			SessionID: "session:s1",
			Label:     "my-project",
			Windows: []rexcli.SessionWindow{
				{
					WindowID: "window:w1",
					Label:    "Agent",
					Layers: []rexcli.SessionLayer{
						{Blocks: []rexcli.SessionBlock{{BlockID: "block:b1", Label: "claude"}}},
					},
				},
			},
		},
		proc: &rexcli.ProcessResp{
			Foreground: &rexcli.ProcessInfo{Name: "claude", Cwd: "/tmp/proj"},
		},
		tail: "line A\nline B\nline C\n",
	}

	cap := &captureNotifier{}
	w := NewWatcher(api, cfg, cap, nil)
	now := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }

	mkStatus := func(state, kind, msg string) []byte {
		return []byte(fmt.Sprintf(`{"type":"session_event","payload":{"session_id":"session:s1","event":{"type":"block_event","payload":{"block_id":"block:b1","creator_name":"com.superlogical.terminal","name":"program_status_changed","payload":{"id":"","reason":"report","record":{"state":%q,"kind":%q,"app":"claude","msg":%q,"updated_at":"2026-11-26T01:00:00Z"}}}}}}`,
			state, kind, msg))
	}

	// 1. working -> should NOT notify, starts timer
	w.HandleEventLine(context.Background(), mkStatus("working", "", "Running tests"))
	if len(cap.sent) != 0 {
		t.Fatalf("expected 0 alerts on working, got %d", len(cap.sent))
	}

	// 2. blocked -> should notify immediately
	now = now.Add(5 * time.Second)
	w.HandleEventLine(context.Background(), mkStatus("blocked", "permission", "Approve Bash?"))
	// Duplicate within cooldown -> should NOT notify again
	now = now.Add(1 * time.Second)
	w.HandleEventLine(context.Background(), mkStatus("blocked", "permission", "Approve Bash?"))
	if len(cap.sent) != 1 {
		t.Fatalf("expected 1 deduplicated alert on blocked, got %d", len(cap.sent))
	}
	if cap.sent[0].SessionLabel != "my-project" || cap.sent[0].Cwd != "/tmp/proj" || cap.sent[0].Tail != "line B\nline C" {
		t.Fatalf("unexpected enriched notification: %+v", cap.sent[0])
	}

	// 3. done -> should notify with elapsed duration (6s)
	now = now.Add(20 * time.Second)
	w.HandleEventLine(context.Background(), mkStatus("done", "", "All tests passed"))
	if len(cap.sent) != 2 {
		t.Fatalf("expected 2 alerts after done, got %d", len(cap.sent))
	}
	if cap.sent[1].Duration != 26*time.Second {
		t.Fatalf("expected 26s duration, got %s", cap.sent[1].Duration)
	}
}

func TestWatcherNewPortNotification(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Rules.NotifyOnPorts = true
	cap := &captureNotifier{}
	w := NewWatcher(&fakeRex{}, cfg, cap, nil)

	// First scan establishes baseline (no spam on startup)
	ev1 := []byte(`{"type":"session_event","payload":{"session_id":"session:s1","event":{"type":"block_event","payload":{"block_id":"block:b1","creator_name":"com.superlogical.terminal","name":"ports_changed","payload":{"status":"ok","ports":[{"port":22,"address":"127.0.0.1","protocol":"tcp","first_seen_at":"2026-11-26T00:00:00Z","pid":1}],"processes":[{"pid":1,"user_id":501,"name":"sshd"}]}}}}}`)
	w.HandleEventLine(context.Background(), ev1)
	if len(cap.sent) != 0 {
		t.Fatalf("expected 0 alerts on initial port baseline, got %d", len(cap.sent))
	}

	// Second scan detects new port 3000
	ev2 := []byte(`{"type":"session_event","payload":{"session_id":"session:s1","event":{"type":"block_event","payload":{"block_id":"block:b1","creator_name":"com.superlogical.terminal","name":"ports_changed","payload":{"status":"ok","ports":[{"port":22,"address":"127.0.0.1","protocol":"tcp","first_seen_at":"2026-11-26T00:00:00Z","pid":1},{"port":3000,"address":"127.0.0.1","protocol":"tcp","first_seen_at":"2026-11-26T00:01:00Z","pid":2}],"processes":[{"pid":1,"user_id":501,"name":"sshd"},{"pid":2,"user_id":501,"name":"node"}]}}}}}`)
	w.HandleEventLine(context.Background(), ev2)
	if len(cap.sent) != 1 || cap.sent[0].EventKind != "ports" {
		t.Fatalf("expected 1 port alert, got %+v", cap.sent)
	}
}
