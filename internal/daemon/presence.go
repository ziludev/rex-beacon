package daemon

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// isUserAtComputer reports whether the user is actively interacting with this Mac
// (screen unlocked and HID idle time below threshold). Fails open (returns false)
// on non-macOS hosts or when system state cannot be read, so alerts are never lost.
func isUserAtComputer(ctx context.Context, threshold time.Duration) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if threshold <= 0 {
		threshold = 60 * time.Second
	}

	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	if rootOut, err := exec.CommandContext(probeCtx, "/usr/sbin/ioreg", "-n", "Root", "-d1").Output(); err == nil {
		if isScreenLockedFromIOReg(string(rootOut)) {
			return false
		}
	}

	hidOut, err := exec.CommandContext(probeCtx, "/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return false
	}
	idle, ok := parseHIDIdleTimeFromIOReg(string(hidOut))
	if !ok {
		return false
	}
	return idle < threshold
}

func isScreenLockedFromIOReg(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "CGSSessionScreenIsLocked") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "= yes") || strings.Contains(lower, "= true") {
				return true
			}
		}
		if strings.Contains(line, "kCGSSessionOnConsoleKey") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "= no") || strings.Contains(lower, "= false") {
				return true
			}
		}
	}
	return false
}

func parseHIDIdleTimeFromIOReg(out string) (time.Duration, bool) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "HIDIdleTime") {
			continue
		}
		idx := strings.LastIndex(line, "=")
		if idx < 0 {
			continue
		}
		valStr := strings.TrimSpace(line[idx+1:])
		if valStr == "" {
			continue
		}
		ns, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil || ns < 0 {
			continue
		}
		return time.Duration(ns) * time.Nanosecond, true
	}
	return 0, false
}
