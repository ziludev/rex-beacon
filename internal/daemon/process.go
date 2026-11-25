package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
)

func pidFilePath() string {
	return filepath.Join(config.StateDir(), "daemon.pid")
}

// LogFilePath returns the path to the background daemon's log file.
func LogFilePath() string {
	return filepath.Join(config.StateDir(), "daemon.log")
}

// StartBackground launches `rex-beacon watch` detached and records its PID.
func StartBackground(extraArgs []string) (int, string, error) {
	if running, pid, _ := Status(); running {
		return pid, LogFilePath(), fmt.Errorf("rex-beacon daemon is already running (pid %d)", pid)
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	stateDir := config.StateDir()
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return 0, "", err
	}
	logPath := LogFilePath()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, "", err
	}
	defer logFile.Close()

	args := append([]string{"watch"}, extraArgs...)
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return pid, logPath, err
	}
	_ = cmd.Process.Release()
	return pid, logPath, nil
}

// StopBackground stops the running background daemon if any.
func StopBackground() (int, error) {
	running, pid, err := Status()
	if err != nil {
		return 0, err
	}
	if !running {
		_ = os.Remove(pidFilePath())
		return 0, errors.New("rex-beacon daemon is not running")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return pid, err
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if alive, _, _ := Status(); !alive {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(pidFilePath())
	return pid, nil
}

// Status reports whether the background daemon is running and its PID.
func Status() (bool, int, error) {
	raw, err := os.ReadFile(pidFilePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false, 0, nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0, nil
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false, 0, nil
	}
	return true, pid, nil
}
