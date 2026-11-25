package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds rex-beacon daemon, notifier, and rule configuration.
type Config struct {
	RexBin    string          `json:"rex_bin,omitempty"`
	Server    string          `json:"server,omitempty"`
	Session   string          `json:"session,omitempty"`
	All       bool            `json:"all"`
	FeishuApp FeishuAppConfig `json:"feishu_app"`
	Webhook   WebhookConfig   `json:"webhook"`
	LarkCLI   LarkCLIConfig   `json:"lark_cli"`
	MacOS     MacOSConfig     `json:"macos"`
	Rules     WatchRules      `json:"rules"`
}

// FeishuAppConfig configures sending interactive cards via a Feishu/Lark custom app.
type FeishuAppConfig struct {
	Enabled       bool   `json:"enabled"`
	Brand         string `json:"brand,omitempty"` // "feishu" (default) or "lark"
	AppID         string `json:"app_id,omitempty"`
	AppSecret     string `json:"app_secret,omitempty"`
	ReceiveID     string `json:"receive_id,omitempty"`
	ReceiveIDType string `json:"receive_id_type,omitempty"` // "open_id", "chat_id", "user_id", "email"
}

// WebhookConfig configures sending cards via a Feishu/Lark custom bot webhook.
type WebhookConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"`
	Secret  string `json:"secret,omitempty"`
}

// LarkCLIConfig configures sending messages using the local lark-cli binary.
type LarkCLIConfig struct {
	Enabled bool   `json:"enabled"`
	Binary  string `json:"binary,omitempty"`
	As      string `json:"as,omitempty"` // "bot" (default) or "user"
	ChatID  string `json:"chat_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
}

// MacOSConfig configures native macOS notifications.
type MacOSConfig struct {
	Enabled bool   `json:"enabled"`
	Sound   string `json:"sound,omitempty"`
}

// WatchRules controls which Rex events trigger notifications and how they are enriched.
type WatchRules struct {
	NotifyStates           []string `json:"notify_states"`
	MinWorkingDurationSec  int      `json:"min_working_duration_sec"`
	CooldownSec            int      `json:"cooldown_sec"`
	TailLines              int      `json:"tail_lines"`
	NotifyOnPorts          bool     `json:"notify_on_ports"`
	NotifyOnBell           bool     `json:"notify_on_bell"`
	NotifyOnNonZeroExit    bool     `json:"notify_on_non_zero_exit"`
	IncludeApps            []string `json:"include_apps,omitempty"`
	IgnoreApps             []string `json:"ignore_apps,omitempty"`
	SkipIfClientFocusedNow bool     `json:"skip_if_client_focused_now"`
}

// DefaultConfig returns sensible defaults for rex-beacon.
func DefaultConfig() Config {
	return Config{
		All: true,
		FeishuApp: FeishuAppConfig{
			Brand:         "feishu",
			ReceiveIDType: "open_id",
		},
		LarkCLI: LarkCLIConfig{
			Binary: "lark-cli",
			As:     "bot",
		},
		MacOS: MacOSConfig{
			Enabled: true,
			Sound:   "Glass",
		},
		Rules: WatchRules{
			NotifyStates:          []string{"blocked", "error", "done"},
			MinWorkingDurationSec: 0,
			CooldownSec:           5,
			TailLines:             15,
			NotifyOnNonZeroExit:   true,
		},
	}
}

// DefaultPath returns the default configuration file path.
func DefaultPath() string {
	if p := strings.TrimSpace(os.Getenv("REX_BEACON_CONFIG")); p != "" {
		return p
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "rex-beacon", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "rex-beacon.json"
	}
	return filepath.Join(home, ".config", "rex-beacon", "config.json")
}

// StateDir returns the directory for daemon PID and log files.
func StateDir() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return filepath.Join(xdg, "rex-beacon")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp/rex-beacon"
	}
	return filepath.Join(home, ".local", "state", "rex-beacon")
}

// Load reads config from path (missing file falls back to defaults) and applies env overrides.
func Load(path string) (Config, string, error) {
	if path == "" {
		path = DefaultPath()
	}
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return cfg, path, err
		}
	} else if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, path, err
		}
	}

	applyDefaults(&cfg)
	applyEnv(&cfg)
	return cfg, path, nil
}

func applyDefaults(cfg *Config) {
	if cfg.FeishuApp.Brand == "" {
		cfg.FeishuApp.Brand = "feishu"
	}
	if cfg.FeishuApp.ReceiveIDType == "" {
		cfg.FeishuApp.ReceiveIDType = "open_id"
	}
	if cfg.LarkCLI.Binary == "" {
		cfg.LarkCLI.Binary = "lark-cli"
	}
	if cfg.LarkCLI.As == "" {
		cfg.LarkCLI.As = "bot"
	}
	if len(cfg.Rules.NotifyStates) == 0 {
		cfg.Rules.NotifyStates = []string{"blocked", "error", "done"}
	}
	if cfg.Rules.CooldownSec <= 0 {
		cfg.Rules.CooldownSec = 5
	}
	if cfg.Rules.TailLines <= 0 {
		cfg.Rules.TailLines = 15
	}
}

func applyEnv(cfg *Config) {
	if v := strings.TrimSpace(os.Getenv("REX_BEACON_REX_BIN")); v != "" {
		cfg.RexBin = v
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_BRAND")); v != "" {
		cfg.FeishuApp.Brand = strings.ToLower(v)
	} else if v := strings.TrimSpace(os.Getenv("LARK_BRAND")); v != "" {
		cfg.FeishuApp.Brand = strings.ToLower(v)
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_APP_ID")); v != "" {
		cfg.FeishuApp.AppID = v
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_APP_SECRET")); v != "" {
		cfg.FeishuApp.AppSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_RECEIVE_ID")); v != "" {
		cfg.FeishuApp.ReceiveID = v
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_RECEIVE_ID_TYPE")); v != "" {
		cfg.FeishuApp.ReceiveIDType = v
	}
	if cfg.FeishuApp.AppID != "" && cfg.FeishuApp.AppSecret != "" && cfg.FeishuApp.ReceiveID != "" {
		if envBool("FEISHU_APP_ENABLED", true) {
			cfg.FeishuApp.Enabled = true
		}
	}

	if v := strings.TrimSpace(os.Getenv("FEISHU_WEBHOOK_URL")); v != "" {
		cfg.Webhook.URL = v
		cfg.Webhook.Enabled = true
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_WEBHOOK_SECRET")); v != "" {
		cfg.Webhook.Secret = v
	}
	if v := os.Getenv("REX_BEACON_MACOS_ENABLED"); v != "" {
		cfg.MacOS.Enabled = envBool("REX_BEACON_MACOS_ENABLED", cfg.MacOS.Enabled)
	}
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

// Save writes cfg as formatted JSON with 0600 file permissions.
func Save(path string, cfg Config) error {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
