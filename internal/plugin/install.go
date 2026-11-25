package plugin

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed lua/rex-beacon/init.lua
var embeddedInitLua string

const (
	markerStart = "-- >>> rex-beacon >>>"
	markerEnd   = "-- <<< rex-beacon <<<"
)

// RexConfigDir returns Rex's config directory (~/.config/rex or $XDG_CONFIG_HOME/rex).
func RexConfigDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "rex"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "rex"), nil
}

// Install writes the embedded Lua plugin and registers it cleanly in Rex's init.lua.
func Install(binPath string) (pluginPath string, initPath string, err error) {
	cfgDir, err := RexConfigDir()
	if err != nil {
		return "", "", err
	}
	if binPath == "" {
		binPath = "rex-beacon"
	}

	pluginPath = filepath.Join(cfgDir, "plugins", "rex-beacon", "init.lua")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(pluginPath, []byte(embeddedInitLua), 0o644); err != nil {
		return "", "", err
	}

	initPath = filepath.Join(cfgDir, "init.lua")
	existing := ""
	if raw, readErr := os.ReadFile(initPath); readErr == nil {
		existing = string(raw)
	} else if !os.IsNotExist(readErr) {
		return pluginPath, initPath, readErr
	}

	cleaned := removeManagedBlock(existing)
	block := fmt.Sprintf("%s\n-- Managed by `rex-beacon rex install`; `rex-beacon rex uninstall` removes it.\ndofile(%q).setup({ bin = { %q } })\n%s\n",
		markerStart, pluginPath, binPath, markerEnd)

	var next string
	trimmed := strings.TrimRight(cleaned, "\r\n")
	if trimmed == "" {
		next = block
	} else {
		next = trimmed + "\n\n" + block
	}

	if err := os.WriteFile(initPath, []byte(next), 0o644); err != nil {
		return pluginPath, initPath, err
	}
	return pluginPath, initPath, nil
}

// Uninstall removes the managed block from init.lua and deletes the plugin directory.
func Uninstall() (initPath string, err error) {
	cfgDir, err := RexConfigDir()
	if err != nil {
		return "", err
	}
	initPath = filepath.Join(cfgDir, "init.lua")
	if raw, readErr := os.ReadFile(initPath); readErr == nil {
		next := strings.TrimSpace(removeManagedBlock(string(raw)))
		if next != "" {
			next += "\n"
		}
		if err := os.WriteFile(initPath, []byte(next), 0o644); err != nil {
			return initPath, err
		}
	}
	_ = os.RemoveAll(filepath.Join(cfgDir, "plugins", "rex-beacon"))
	return initPath, nil
}

func removeManagedBlock(src string) string {
	lines := strings.Split(src, "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == markerStart {
			skipping = true
			continue
		}
		if skipping {
			if trimmed == markerEnd {
				skipping = false
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
