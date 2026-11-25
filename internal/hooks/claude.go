package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ziludev/rex-beacon/internal/osc7501"
)

// ClaudeHookInput models the JSON payload Claude Code passes to hook commands via stdin.
type ClaudeHookInput struct {
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	Error            string `json:"error"`
	AgentID          string `json:"agent_id"`
	Cwd              string `json:"cwd"`
}

// MapClaudeHook converts a Claude Code hook event into an OSC 7501 Report.
// Returns (report, true) when a report should be emitted, or (zero, false) to ignore.
func MapClaudeHook(in ClaudeHookInput) (osc7501.Report, bool) {
	// Ignore subagent events so subagents don't overwrite the main session status.
	if in.AgentID != "" {
		return osc7501.Report{}, false
	}

	switch in.HookEventName {
	case "SessionStart":
		return osc7501.Report{
			State: osc7501.StateIdle,
			App:   "claude",
			Msg:   "Session ready",
		}, true
	case "UserPromptSubmit":
		return osc7501.Report{
			State: osc7501.StateWorking,
			App:   "claude",
			Msg:   "Thinking…",
		}, true
	case "PreToolUse":
		msg := "Running tool"
		if in.ToolName != "" {
			msg = "Running " + in.ToolName
		}
		return osc7501.Report{
			State: osc7501.StateWorking,
			App:   "claude",
			Msg:   msg,
		}, true
	case "PermissionRequest":
		msg := "Permission required"
		if in.ToolName != "" {
			msg = "Approve " + in.ToolName + "?"
		}
		return osc7501.Report{
			State: osc7501.StateBlocked,
			Kind:  osc7501.KindPermission,
			App:   "claude",
			Msg:   msg,
		}, true
	case "Notification":
		kind := osc7501.KindQuestion
		nt := strings.ToLower(in.NotificationType)
		switch {
		case strings.Contains(nt, "permission"):
			kind = osc7501.KindPermission
		case strings.Contains(nt, "auth"):
			kind = osc7501.KindAuth
		}
		msg := strings.TrimSpace(in.Message)
		if msg == "" {
			msg = "Claude needs your attention"
		}
		return osc7501.Report{
			State: osc7501.StateBlocked,
			Kind:  kind,
			App:   "claude",
			Msg:   msg,
		}, true
	case "Stop":
		return osc7501.Report{
			State: osc7501.StateDone,
			App:   "claude",
			Msg:   "Task complete",
		}, true
	case "StopFailure":
		msg := strings.TrimSpace(in.Error)
		if msg == "" {
			msg = "Stopped with error"
		}
		return osc7501.Report{
			State: osc7501.StateError,
			App:   "claude",
			Msg:   msg,
		}, true
	case "SessionEnd":
		return osc7501.Report{
			State: osc7501.StateClear,
		}, true
	default:
		return osc7501.Report{}, false
	}
}

// RunClaudeHook reads Claude hook JSON from stdin and emits OSC 7501 to writer (or /dev/tty).
func RunClaudeHook(stdin io.Reader, out io.Writer, force bool) error {
	if !force && !osc7501.IsRexTerminal() {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var in ClaudeHookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil
	}
	rep, ok := MapClaudeHook(in)
	if !ok {
		return nil
	}
	if out != nil {
		return osc7501.EmitTo(out, rep)
	}
	return osc7501.Emit(rep)
}

var claudeHookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PermissionRequest",
	"Notification",
	"Stop",
	"StopFailure",
	"SessionEnd",
}

const claudeHookMarker = "rex-beacon hook claude"

// InstallClaudeHooks safely adds rex-beacon OSC 7501 hooks to ~/.claude/settings.json.
func InstallClaudeHooks(binPath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return settingsPath, err
	}

	root := map[string]any{}
	if raw, err := os.ReadFile(settingsPath); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return settingsPath, fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	}

	hooksMap, ok := root["hooks"].(map[string]any)
	if !ok || hooksMap == nil {
		hooksMap = map[string]any{}
	}

	cmdStr := fmt.Sprintf("/bin/sh -c '%s 2>/dev/null || true'", strings.ReplaceAll(binPath, "'", "'\\''")+" hook claude")

	for _, ev := range claudeHookEvents {
		entries, _ := hooksMap[ev].([]any)
		if containsBeaconHook(entries) {
			continue
		}
		entries = append(entries, map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": cmdStr,
					"timeout": 5,
				},
			},
		})
		hooksMap[ev] = entries
	}
	root["hooks"] = hooksMap

	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return settingsPath, err
	}
	encoded = append(encoded, '\n')
	return settingsPath, os.WriteFile(settingsPath, encoded, 0o600)
}

// UninstallClaudeHooks removes rex-beacon hook entries from ~/.claude/settings.json.
func UninstallClaudeHooks() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return settingsPath, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return settingsPath, err
	}
	hooksMap, ok := root["hooks"].(map[string]any)
	if !ok || hooksMap == nil {
		return settingsPath, nil
	}
	for _, ev := range claudeHookEvents {
		entries, ok := hooksMap[ev].([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, item := range entries {
			b, _ := json.Marshal(item)
			if !strings.Contains(string(b), claudeHookMarker) {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(hooksMap, ev)
		} else {
			hooksMap[ev] = kept
		}
	}
	root["hooks"] = hooksMap
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return settingsPath, err
	}
	encoded = append(encoded, '\n')
	return settingsPath, os.WriteFile(settingsPath, encoded, 0o600)
}

func containsBeaconHook(entries []any) bool {
	for _, item := range entries {
		b, _ := json.Marshal(item)
		if strings.Contains(string(b), claudeHookMarker) {
			return true
		}
	}
	return false
}
