package daemon

import (
	"context"
	"fmt"
	"strings"
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
	w.atComputer = func(context.Context) bool { return false }
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
	if cap.sent[0].SessionLabel != "my-project" || cap.sent[0].Cwd != "/tmp/proj" || !strings.Contains(cap.sent[0].Tail, "line B\nline C") || strings.Contains(cap.sent[0].Tail, "line A") {
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
	w.atComputer = func(context.Context) bool { return false }

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

func TestWatcherNoiseReduction(t *testing.T) {
	mkStatus := func(id, state, kind, msg string) []byte {
		return []byte(fmt.Sprintf(`{"type":"session_event","payload":{"session_id":"session:s1","event":{"type":"block_event","payload":{"block_id":"block:b1","creator_name":"com.superlogical.terminal","name":"program_status_changed","payload":{"id":%q,"reason":"report","record":{"state":%q,"kind":%q,"app":"claude","msg":%q}}}}}}`,
			id, state, kind, msg))
	}
	mkExit := func(code int) []byte {
		return []byte(fmt.Sprintf(`{"type":"session_event","payload":{"session_id":"session:s1","event":{"type":"block_event","payload":{"block_id":"block:b1","creator_name":"com.superlogical.terminal","name":"child_exited","payload":{"exit_code":%d}}}}}`, code))
	}

	t.Run("suppresses short done turns and repeat done without working", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cap := &captureNotifier{}
		w := NewWatcher(&fakeRex{}, cfg, cap, nil)
		w.atComputer = func(context.Context) bool { return false }
		now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
		w.now = func() time.Time { return now }

		// 5s quick turn -> suppressed by default MinWorkingDurationSec (15s)
		w.HandleEventLine(context.Background(), mkStatus("", "working", "", "Thinking"))
		now = now.Add(5 * time.Second)
		w.HandleEventLine(context.Background(), mkStatus("", "done", "", "Quick reply"))
		if len(cap.sent) != 0 {
			t.Fatalf("expected short turn done to be suppressed, got %d", len(cap.sent))
		}

		// 20s turn -> notifies once
		w.HandleEventLine(context.Background(), mkStatus("", "working", "", "Building"))
		now = now.Add(20 * time.Second)
		w.HandleEventLine(context.Background(), mkStatus("", "done", "", "Done 1"))
		if len(cap.sent) != 1 {
			t.Fatalf("expected 1 done alert, got %d", len(cap.sent))
		}

		// Duplicate done without intervening working (even after cooldown & different msg) -> suppressed
		now = now.Add(60 * time.Second)
		w.HandleEventLine(context.Background(), mkStatus("", "done", "", "Done 1 refresh"))
		if len(cap.sent) != 1 {
			t.Fatalf("expected duplicate done without working to be suppressed, got %d", len(cap.sent))
		}
	})

	t.Run("deduplicates dual blocked hooks but allows new blocked after working", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Rules.CooldownSec = 30
		cap := &captureNotifier{}
		w := NewWatcher(&fakeRex{}, cfg, cap, nil)
		w.atComputer = func(context.Context) bool { return false }
		now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
		w.now = func() time.Time { return now }

		// PermissionRequest + Notification within 1s with different messages
		w.HandleEventLine(context.Background(), mkStatus("", "blocked", "permission", "Approve Bash?"))
		now = now.Add(1 * time.Second)
		w.HandleEventLine(context.Background(), mkStatus("hook2", "blocked", "permission", "Claude needs your permission"))
		if len(cap.sent) != 1 {
			t.Fatalf("expected dual blocked hooks to deduplicate to 1, got %d", len(cap.sent))
		}

		// User approves -> working runs -> new permission prompt -> alerts immediately
		now = now.Add(2 * time.Second)
		w.HandleEventLine(context.Background(), mkStatus("", "working", "", "Running Bash"))
		now = now.Add(3 * time.Second)
		w.HandleEventLine(context.Background(), mkStatus("", "blocked", "permission", "Approve Edit?"))
		if len(cap.sent) != 2 {
			t.Fatalf("expected new blocked prompt after working to notify, got %d", len(cap.sent))
		}
	})

	t.Run("ignores interrupt exit codes and recent error double-alerts", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Rules.CooldownSec = 30
		cap := &captureNotifier{}
		w := NewWatcher(&fakeRex{}, cfg, cap, nil)
		w.atComputer = func(context.Context) bool { return false }
		now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
		w.now = func() time.Time { return now }

		// Ctrl+C (130), SIGTERM (143), SIGHUP (129) should not notify
		w.HandleEventLine(context.Background(), mkExit(130))
		w.HandleEventLine(context.Background(), mkExit(143))
		w.HandleEventLine(context.Background(), mkExit(129))
		if len(cap.sent) != 0 {
			t.Fatalf("expected interrupt exit codes to be ignored, got %d", len(cap.sent))
		}

		// program_status error followed 5s later by exit 1 -> only 1 alert
		w.HandleEventLine(context.Background(), mkStatus("", "error", "", "build failed"))
		now = now.Add(5 * time.Second)
		w.HandleEventLine(context.Background(), mkExit(1))
		if len(cap.sent) != 1 {
			t.Fatalf("expected error + exit within cooldown to emit 1 alert, got %d", len(cap.sent))
		}
	})
}

type namedCaptureNotifier struct {
	name string
	sent []notify.Notification
}

func (n *namedCaptureNotifier) Name() string { return n.name }
func (n *namedCaptureNotifier) Send(_ context.Context, msg notify.Notification) error {
	n.sent = append(n.sent, msg)
	return nil
}

func TestWatcherAtComputer(t *testing.T) {
	mkBlocked := []byte(`{"type":"session_event","payload":{"session_id":"session:s1","event":{"type":"block_event","payload":{"block_id":"block:b1","creator_name":"com.superlogical.terminal","name":"program_status_changed","payload":{"id":"","reason":"report","record":{"state":"blocked","kind":"permission","app":"claude","msg":"Approve?"}}}}}}`)

	t.Run("skip_all suppresses alerts when user is at computer", func(t *testing.T) {
		cfg := config.DefaultConfig() // WhenAtComputer defaults to "skip_all"
		cap := &captureNotifier{}
		w := NewWatcher(&fakeRex{}, cfg, cap, nil)
		at := true
		w.atComputer = func(context.Context) bool { return at }

		w.HandleEventLine(context.Background(), mkBlocked)
		if len(cap.sent) != 0 {
			t.Fatalf("expected 0 alerts while at computer, got %d", len(cap.sent))
		}

		// User walks away / locks screen -> alerts resume
		at = false
		w.HandleEventLine(context.Background(), mkBlocked)
		if len(cap.sent) != 1 {
			t.Fatalf("expected 1 alert when away from computer, got %d", len(cap.sent))
		}
	})

	t.Run("skip_remote delivers local macos only when unfocused at computer", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Rules.WhenAtComputer = "skip_remote"
		feishu := &namedCaptureNotifier{name: "feishu_app"}
		macos := &namedCaptureNotifier{name: "macos"}
		multi := notify.NewMultiNotifier(feishu, macos)

		api := &fakeRex{
			view: &rexcli.SessionViewResp{
				SessionID:      "session:s1",
				ActiveWindowID: "window:w1",
				Windows: []rexcli.SessionWindow{
					{WindowID: "window:w1", FocusedBlockID: "block:other"},
				},
			},
			clients: &rexcli.ClientListResp{
				Clients: []rexcli.ConnectedClient{
					{Info: rexcli.ClientInfo{Kind: "app"}, ConnectionState: "connected", SessionIDs: []string{"session:s1"}},
				},
			},
		}
		w := NewWatcher(api, cfg, multi, nil)
		w.atComputer = func(context.Context) bool { return true }

		w.HandleEventLine(context.Background(), mkBlocked)
		if len(feishu.sent) != 0 || len(macos.sent) != 1 {
			t.Fatalf("expected feishu=0 macos=1, got feishu=%d macos=%d", len(feishu.sent), len(macos.sent))
		}

		// If the target pane itself is focused in Rex, skip both remote and local.
		api.view.Windows[0].FocusedBlockID = "block:b1"
		w.HandleEventLine(context.Background(), []byte(`{"type":"session_event","payload":{"session_id":"session:s1","event":{"type":"block_event","payload":{"block_id":"block:b1","creator_name":"com.superlogical.terminal","name":"program_status_changed","payload":{"id":"","reason":"report","record":{"state":"error","app":"claude","msg":"Failed"}}}}}}`))
		if len(feishu.sent) != 0 || len(macos.sent) != 1 {
			t.Fatalf("expected focused pane to skip all, got feishu=%d macos=%d", len(feishu.sent), len(macos.sent))
		}
	})

	t.Run("notify mode always sends even at computer", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Rules.WhenAtComputer = "notify"
		cap := &captureNotifier{}
		w := NewWatcher(&fakeRex{}, cfg, cap, nil)
		w.atComputer = func(context.Context) bool { return true }

		w.HandleEventLine(context.Background(), mkBlocked)
		if len(cap.sent) != 1 {
			t.Fatalf("expected 1 alert in notify mode, got %d", len(cap.sent))
		}
	})
}

func TestIORegPresenceParsing(t *testing.T) {
	if !isScreenLockedFromIOReg(`"IOConsoleUsers" = ({"CGSSessionScreenIsLocked" = Yes,"kCGSSessionOnConsoleKey" = Yes})`) {
		t.Fatal("expected CGSSessionScreenIsLocked = Yes to report locked")
	}
	if isScreenLockedFromIOReg("\"IOConsoleUsers\" = ({\n  \"CGSSessionScreenIsLocked\" = No\n  \"kCGSSessionOnConsoleKey\" = Yes\n})") {
		t.Fatal("expected unlocked screen")
	}
	if !isScreenLockedFromIOReg(`"kCGSSessionOnConsoleKey" = No`) {
		t.Fatal("expected off-console session to be treated as away/locked")
	}

	hidSample := `  |   "HIDIdleTime" = 12500000000`
	dur, ok := parseHIDIdleTimeFromIOReg(hidSample)
	if !ok || dur != 12500*time.Millisecond {
		t.Fatalf("unexpected HIDIdleTime parse: dur=%v ok=%v", dur, ok)
	}
}
