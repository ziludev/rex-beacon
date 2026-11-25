package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ziludev/rex-beacon/internal/config"
)

// LarkCLINotifier sends interactive cards via the local `lark-cli` binary.
type LarkCLINotifier struct {
	cfg config.LarkCLIConfig
}

// NewLarkCLINotifier creates a notifier backed by `lark-cli im +messages-send`.
func NewLarkCLINotifier(cfg config.LarkCLIConfig) (*LarkCLINotifier, error) {
	if cfg.ChatID == "" && cfg.UserID == "" {
		return nil, fmt.Errorf("lark_cli requires chat_id or user_id")
	}
	if cfg.Binary == "" {
		cfg.Binary = "lark-cli"
	}
	if cfg.As == "" {
		cfg.As = "bot"
	}
	return &LarkCLINotifier{cfg: cfg}, nil
}

func (l *LarkCLINotifier) Name() string {
	return "lark_cli"
}

// Send delivers an interactive card via `lark-cli im +messages-send`.
func (l *LarkCLINotifier) Send(ctx context.Context, n Notification) error {
	cardBytes, err := json.Marshal(BuildFeishuCard(n))
	if err != nil {
		return err
	}
	args := []string{"im", "+messages-send", "--as", l.cfg.As}
	if l.cfg.ChatID != "" {
		args = append(args, "--chat-id", l.cfg.ChatID)
	} else {
		args = append(args, "--user-id", l.cfg.UserID)
	}
	args = append(args, "--msg-type", "interactive", "--content", string(cardBytes))

	cmd := exec.CommandContext(ctx, l.cfg.Binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lark-cli failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
