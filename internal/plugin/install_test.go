package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAndUninstallPreservesExistingConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	rexDir := filepath.Join(tmp, "rex")
	if err := os.MkdirAll(rexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "-- >>> leaf >>>\ndofile(\"/tmp/leaf.lua\").setup({})\n-- <<< leaf <<<\n"
	if err := os.WriteFile(filepath.Join(rexDir, "init.lua"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginPath, initPath, err := Install("/usr/local/bin/rex-beacon")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(pluginPath); err != nil {
		t.Fatalf("plugin file missing: %v", err)
	}
	afterInstall, _ := os.ReadFile(initPath)
	s := string(afterInstall)
	if !strings.Contains(s, "leaf.lua") || !strings.Contains(s, markerStart) || !strings.Contains(s, "/usr/local/bin/rex-beacon") {
		t.Fatalf("unexpected init.lua after install:\n%s", s)
	}

	// Idempotent reinstall
	if _, _, err := Install("/usr/local/bin/rex-beacon"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	afterReinstall, _ := os.ReadFile(initPath)
	if strings.Count(string(afterReinstall), markerStart) != 1 {
		t.Fatalf("expected exactly 1 managed block:\n%s", string(afterReinstall))
	}

	// Uninstall preserves leaf block
	if _, err := Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	afterUninstall, _ := os.ReadFile(initPath)
	if !strings.Contains(string(afterUninstall), "leaf.lua") || strings.Contains(string(afterUninstall), markerStart) {
		t.Fatalf("unexpected init.lua after uninstall:\n%s", string(afterUninstall))
	}
}
