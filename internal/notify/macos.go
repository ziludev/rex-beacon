package notify

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/ziludev/rex-beacon/internal/config"
)

// MacOSNotifier triggers native macOS notifications via osascript.
type MacOSNotifier struct {
	cfg config.MacOSConfig
}

// NewMacOSNotifier returns a macOS desktop notifier.
func NewMacOSNotifier(cfg config.MacOSConfig) *MacOSNotifier {
	return &MacOSNotifier{cfg: cfg}
}

func (m *MacOSNotifier) Name() string {
	return "macos"
}

// Send displays a native macOS notification when running on darwin.
func (m *MacOSNotifier) Send(ctx context.Context, n Notification) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	title, body := BuildPlainSummary(n)
	script := fmt.Sprintf("display notification %s with title %s",
		appleScriptQuote(body),
		appleScriptQuote(title),
	)
	if m.cfg.Sound != "" {
		script += fmt.Sprintf(" sound name %s", appleScriptQuote(m.cfg.Sound))
	}
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return `"` + s + `"`
}
