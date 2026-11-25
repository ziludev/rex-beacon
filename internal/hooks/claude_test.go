package hooks

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ziludev/rex-beacon/internal/osc7501"
)

func TestMapClaudeHook(t *testing.T) {
	cases := []struct {
		name      string
		in        ClaudeHookInput
		wantOK    bool
		wantState osc7501.State
		wantKind  osc7501.Kind
	}{
		{
			name:   "ignore subagent",
			in:     ClaudeHookInput{HookEventName: "Stop", AgentID: "sub-1"},
			wantOK: false,
		},
		{
			name:      "permission request",
			in:        ClaudeHookInput{HookEventName: "PermissionRequest", ToolName: "Bash"},
			wantOK:    true,
			wantState: osc7501.StateBlocked,
			wantKind:  osc7501.KindPermission,
		},
		{
			name:      "stop done",
			in:        ClaudeHookInput{HookEventName: "Stop"},
			wantOK:    true,
			wantState: osc7501.StateDone,
		},
		{
			name:      "stop failure",
			in:        ClaudeHookInput{HookEventName: "StopFailure", Error: "API timeout"},
			wantOK:    true,
			wantState: osc7501.StateError,
		},
		{
			name:      "session end clears",
			in:        ClaudeHookInput{HookEventName: "SessionEnd"},
			wantOK:    true,
			wantState: osc7501.StateClear,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, ok := MapClaudeHook(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if ok && (rep.State != tc.wantState || rep.Kind != tc.wantKind) {
				t.Fatalf("got %+v want state=%s kind=%s", rep, tc.wantState, tc.wantKind)
			}
		})
	}
}

func TestRunClaudeHook(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Write"}`)
	if err := RunClaudeHook(in, &out, true); err != nil {
		t.Fatalf("RunClaudeHook: %v", err)
	}
	if !strings.Contains(out.String(), "\x1b]7501;state=blocked:") ||
		!strings.Contains(out.String(), "kind=permission") ||
		!strings.Contains(out.String(), "app=claude") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}
