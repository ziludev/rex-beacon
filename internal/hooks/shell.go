package hooks

import (
	"fmt"
	"strings"
)

// ShellInit returns a shell helper snippet for zsh, bash, or fish.
func ShellInit(shell, binPath string) (string, error) {
	if binPath == "" {
		binPath = "rex-beacon"
	}
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "", "zsh", "bash":
		return fmt.Sprintf(`# rex-beacon shell helpers (eval "$(%s shell-init %s)")
beacon_status() {
  local state="${1:?usage: beacon_status <state> [msg]}"
  shift || true
  %q status --state "$state" --msg "$*"
}

# Run a command with OSC 7501 progress + Feishu/macOS alert when done:
#   bw make test
bw() {
  %q wrap -- "$@"
}
`, binPath, shell, binPath, binPath), nil
	case "fish":
		return fmt.Sprintf(`# rex-beacon shell helpers (%s shell-init fish | source)
function beacon_status
  set -l state $argv[1]
  set -e argv[1]
  %q status --state "$state" --msg (string join " " $argv)
end

function bw
  %q wrap -- $argv
end
`, binPath, binPath, binPath), nil
	default:
		return "", fmt.Errorf("unsupported shell %q (expected zsh, bash, or fish)", shell)
	}
}
