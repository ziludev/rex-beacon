package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
	"github.com/ziludev/rex-beacon/internal/daemon"
	"github.com/ziludev/rex-beacon/internal/hooks"
	"github.com/ziludev/rex-beacon/internal/notify"
	"github.com/ziludev/rex-beacon/internal/osc7501"
	"github.com/ziludev/rex-beacon/internal/plugin"
	"github.com/ziludev/rex-beacon/internal/rexcli"
)

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	sub := os.Args[1]
	args := os.Args[2:]

	var err error
	switch sub {
	case "status":
		err = cmdStatus(args)
	case "wrap":
		err = cmdWrap(args)
	case "watch":
		err = cmdWatch(args)
	case "daemon":
		err = cmdDaemon(args)
	case "notify":
		err = cmdNotify(args)
	case "inbox":
		err = cmdInbox(args)
	case "peek":
		err = cmdPeek(args)
	case "hook":
		err = cmdHook(args)
	case "claude":
		err = cmdClaude(args)
	case "shell-init":
		err = cmdShellInit(args)
	case "rex":
		err = cmdRexPlugin(args)
	case "init":
		err = cmdInit(args)
	case "version", "--version", "-v":
		fmt.Printf("rex-beacon %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", sub)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		var exitErr *exitCodeError
		if errors.As(err, &exitErr) {
			if exitErr.msg != "" {
				fmt.Fprintln(os.Stderr, exitErr.msg)
			}
			os.Exit(exitErr.code)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }

func printUsage() {
	fmt.Print(`rex-beacon — OSC 7501 status emitter, Rex event daemon, Feishu/Lark notifier & palette inbox

Usage:
  rex-beacon <command> [flags]

Emit OSC 7501 Status:
  status      Emit an OSC 7501 status sequence to the terminal
  wrap        Run a command and emit working -> done/error OSC 7501 status
  hook        Handle agent hooks (e.g. "rex-beacon hook claude")
  shell-init  Print zsh/bash/fish helper functions

Watch & Notify:
  watch       Run the event watcher in the foreground
  daemon      Manage the detached background watcher (start|stop|status|logs)
  notify      Send an immediate test/manual alert via configured notifiers
  inbox       List all active OSC 7501 status records across Rex sessions
  peek        Print recent output lines of a terminal block

Setup & Integrations:
  init        Create or update ~/.config/rex-beacon/config.json
  rex         Install or uninstall the Rex Lua plugin (Mac & iOS palette actions)
  claude      Install or uninstall Claude Code hooks in ~/.claude/settings.json
  version     Print version
`)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	state := fs.String("state", "", "State: idle|working|done|blocked|error|clear")
	kind := fs.String("kind", "", "Blocked kind: permission|question|auth")
	app := fs.String("app", "", "Program identifier (<=32 chars)")
	id := fs.String("id", "", "Hierarchical record ID (e.g. build/test)")
	title := fs.String("title", "", "Short human-readable record title")
	msg := fs.String("msg", "", "Single-line status message")
	progress := fs.Int("progress", -1, "Progress percentage (0-100)")
	stdout := fs.Bool("stdout", false, "Write escape sequence to stdout instead of /dev/tty")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *state == "" {
		return errors.New("--state is required")
	}
	rep := osc7501.Report{
		State: osc7501.State(*state),
		Kind:  osc7501.Kind(*kind),
		App:   *app,
		ID:    *id,
		Title: *title,
		Msg:   *msg,
	}
	if *progress >= 0 {
		p := *progress
		rep.Progress = &p
	}
	if *stdout {
		return osc7501.EmitTo(os.Stdout, rep)
	}
	return osc7501.Emit(rep)
}

func cmdWrap(args []string) error {
	fs := flag.NewFlagSet("wrap", flag.ContinueOnError)
	app := fs.String("app", "", "Program identifier (defaults to command basename)")
	title := fs.String("title", "", "Title shown in Rex and alerts")
	id := fs.String("id", "", "Optional hierarchical record ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		return errors.New("usage: rex-beacon wrap [flags] -- <command...>")
	}

	appName := *app
	if appName == "" {
		base := cmdArgs[0]
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		appName = sanitizeAppToken(base)
	}
	cmdTitle := *title
	if cmdTitle == "" {
		cmdTitle = strings.Join(cmdArgs, " ")
	}

	_ = osc7501.Emit(osc7501.Report{
		State: osc7501.StateWorking,
		ID:    *id,
		App:   appName,
		Title: cmdTitle,
		Msg:   "Running: " + strings.Join(cmdArgs, " "),
	})

	started := time.Now()
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()
	elapsed := notify.FormatDuration(time.Since(started))
	if elapsed == "" {
		elapsed = "<1s"
	}

	if runErr != nil {
		code := 1
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		}
		_ = osc7501.Emit(osc7501.Report{
			State: osc7501.StateError,
			ID:    *id,
			App:   appName,
			Title: cmdTitle,
			Msg:   fmt.Sprintf("Failed (exit %d) in %s: %s", code, elapsed, cmdTitle),
		})
		return &exitCodeError{code: code}
	}

	_ = osc7501.Emit(osc7501.Report{
		State: osc7501.StateDone,
		ID:    *id,
		App:   appName,
		Title: cmdTitle,
		Msg:   fmt.Sprintf("Finished in %s: %s", elapsed, cmdTitle),
	})
	return nil
}

func sanitizeAppToken(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < 32; i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '+' || c == '-' {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "cmd"
	}
	return b.String()
}

func cmdWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "Path to config.json")
	rexBin := fs.String("rex-bin", "", "Path to rex binary")
	server := fs.String("server", "", "Rex server label or address")
	session := fs.String("session", "", "Watch only one session (overrides --all)")
	fs.StringVar(session, "s", "", "Alias for --session")
	all := fs.Bool("all", true, "Watch all sessions on the server")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, usedPath, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *rexBin != "" {
		cfg.RexBin = *rexBin
	}
	if *server != "" {
		cfg.Server = *server
	}
	if *session != "" {
		cfg.Session = *session
		cfg.All = false
	} else {
		cfg.All = *all
	}

	client, err := rexcli.NewClient(cfg.RexBin, cfg.Server)
	if err != nil {
		return err
	}
	notifier, err := buildNotifier(cfg)
	if err != nil {
		return err
	}
	logger := log.New(os.Stdout, "[rex-beacon] ", log.LstdFlags)
	logger.Printf("config=%s rex=%s channels=%v", usedPath, client.Binary, notifier.Names())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w := daemon.NewWatcher(client, cfg, notifier, logger)
	err = w.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func cmdDaemon(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rex-beacon daemon <start|stop|status|logs>")
	}
	switch args[0] {
	case "start":
		pid, logPath, err := daemon.StartBackground(args[1:])
		if err != nil {
			return err
		}
		fmt.Printf("rex-beacon daemon started (pid %d)\nlog: %s\n", pid, logPath)
		return nil
	case "stop":
		pid, err := daemon.StopBackground()
		if err != nil {
			return err
		}
		fmt.Printf("rex-beacon daemon stopped (pid %d)\n", pid)
		return nil
	case "status":
		running, pid, err := daemon.Status()
		if err != nil {
			return err
		}
		if running {
			fmt.Printf("running (pid %d, log: %s)\n", pid, daemon.LogFilePath())
		} else {
			fmt.Println("stopped")
		}
		return nil
	case "logs":
		raw, err := os.ReadFile(daemon.LogFilePath())
		if err != nil {
			return err
		}
		fmt.Print(rexcli.TailLines(string(raw), 50))
		if len(raw) > 0 {
			fmt.Println()
		}
		return nil
	default:
		return fmt.Errorf("unknown daemon subcommand %q (expected start|stop|status|logs)", args[0])
	}
}

func cmdNotify(args []string) error {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "Path to config.json")
	rexBin := fs.String("rex-bin", "", "Path to rex binary")
	state := fs.String("state", "blocked", "Notification state: blocked|error|done|working|info")
	kind := fs.String("kind", "", "Blocked kind: permission|question|auth")
	app := fs.String("app", "rex-beacon", "App name")
	title := fs.String("title", "", "Card title")
	msg := fs.String("msg", "Test notification from rex-beacon", "Notification message")
	sessionID := fs.String("session", "", "Rex session ID to enrich from")
	fs.StringVar(sessionID, "s", "", "Alias for --session")
	blockID := fs.String("block", os.Getenv("REX_BLOCK"), "Rex block ID to enrich from")
	fs.StringVar(blockID, "b", os.Getenv("REX_BLOCK"), "Alias for --block")
	tailLines := fs.Int("tail", -1, "Override tail lines (0 disables)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, _, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *rexBin != "" {
		cfg.RexBin = *rexBin
	}
	notifier, err := buildNotifier(cfg)
	if err != nil {
		return err
	}
	if notifier.Count() == 0 {
		return errors.New("no notification channels are enabled (configure feishu_app, webhook, lark_cli, or macos)")
	}

	host, _ := os.Hostname()
	n := notify.Notification{
		EventKind: "test",
		State:     *state,
		Kind:      *kind,
		App:       *app,
		Title:     *title,
		Msg:       *msg,
		SessionID: *sessionID,
		BlockID:   *blockID,
		Host:      host,
		Timestamp: time.Now(),
	}

	if client, cerr := rexcli.NewClient(cfg.RexBin, cfg.Server); cerr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if n.SessionID == "" && n.BlockID != "" {
			if who, werr := client.Whoami(ctx); werr == nil {
				n.SessionID = who.SessionID
			}
		}
		if n.SessionID != "" {
			if view, verr := client.SessionView(ctx, n.SessionID); verr == nil && view != nil {
				n.SessionLabel = view.Label
				for _, win := range view.Windows {
					for _, layer := range win.Layers {
						for _, blk := range layer.Blocks {
							if blk.BlockID == n.BlockID {
								n.WindowID = win.WindowID
								n.WindowLabel = win.Label
								n.BlockLabel = blk.Label
							}
						}
					}
				}
			}
		}
		if n.BlockID != "" {
			if proc, perr := client.BlockProcess(ctx, n.BlockID); perr == nil && proc != nil && proc.Foreground != nil {
				n.ProcessName = proc.Foreground.Name
				n.Cwd = proc.Foreground.Cwd
			}
			tl := cfg.Rules.TailLines
			if *tailLines >= 0 {
				tl = *tailLines
			}
			if tl > 0 {
				if raw, ferr := client.BlockFormatText(ctx, n.BlockID); ferr == nil {
					n.Tail = notify.ExtractTerminalConclusion(raw, tl, 3200)
				}
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := notifier.Send(ctx, n); err != nil {
		return err
	}
	fmt.Printf("sent [%s] via %s\n", n.State, strings.Join(notifier.Names(), ", "))
	return nil
}

func cmdInbox(args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	rexBin := fs.String("rex-bin", "", "Path to rex binary")
	server := fs.String("server", "", "Rex server label or address")
	asJSON := fs.Bool("json", false, "Output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := rexcli.NewClient(*rexBin, *server)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := client.SessionList(ctx)
	if err != nil {
		return err
	}
	type item struct {
		SessionID    string `json:"session_id"`
		SessionLabel string `json:"session_label"`
		WindowLabel  string `json:"window_label"`
		BlockID      string `json:"block_id"`
		BlockLabel   string `json:"block_label"`
		State        string `json:"state"`
		Kind         string `json:"kind,omitempty"`
		App          string `json:"app"`
		Title        string `json:"title,omitempty"`
		Msg          string `json:"msg,omitempty"`
		UpdatedAt    string `json:"updated_at"`
	}
	var items []item
	for _, s := range list.Sessions {
		view, err := client.SessionView(ctx, s.SessionID)
		if err != nil || view == nil {
			continue
		}
		for _, w := range view.Windows {
			for _, l := range w.Layers {
				for _, b := range l.Blocks {
					if b.CreatorName != "com.superlogical.terminal" {
						continue
					}
					st, err := client.BlockProgramStatus(ctx, b.BlockID)
					if err != nil || st == nil {
						continue
					}
					for _, rec := range st.Records {
						app := rec.EffectiveApp
						if app == "" {
							app = rec.App
						}
						items = append(items, item{
							SessionID:    s.SessionID,
							SessionLabel: s.Label,
							WindowLabel:  w.Label,
							BlockID:      b.BlockID,
							BlockLabel:   b.Label,
							State:        rec.State,
							Kind:         rec.Kind,
							App:          app,
							Title:        rec.Title,
							Msg:          rec.Msg,
							UpdatedAt:    rec.UpdatedAt,
						})
					}
				}
			}
		}
	}
	rank := map[string]int{"blocked": 1, "error": 2, "done": 3, "working": 4, "idle": 5}
	sort.Slice(items, func(i, j int) bool {
		ri, rj := rank[items[i].State], rank[items[j].State]
		if ri == 0 {
			ri = 9
		}
		if rj == 0 {
			rj = 9
		}
		if ri != rj {
			return ri < rj
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"items": items})
	}
	if len(items) == 0 {
		fmt.Println("No active OSC 7501 status records.")
		return nil
	}
	for _, it := range items {
		line := fmt.Sprintf("%-8s %-14s %s / %s (%s)", strings.ToUpper(it.State), it.App, it.SessionLabel, it.WindowLabel, notify.ShortID(it.BlockID))
		if it.Msg != "" {
			line += " — " + it.Msg
		}
		fmt.Println(line)
	}
	return nil
}

func cmdPeek(args []string) error {
	fs := flag.NewFlagSet("peek", flag.ContinueOnError)
	lines := fs.Int("lines", 40, "Number of tail lines to show")
	wait := fs.Bool("wait", false, "Wait for Enter before exiting (for floating layers)")
	rexBin := fs.String("rex-bin", "", "Path to rex binary")
	server := fs.String("server", "", "Rex server")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: rex-beacon peek [--lines 40] [--wait] <block_id>")
	}
	blockID := fs.Arg(0)
	client, err := rexcli.NewClient(*rexBin, *server)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := client.BlockFormatText(ctx, blockID)
	if err != nil {
		return err
	}
	fmt.Println(rexcli.TailLines(raw, *lines))
	if *wait {
		fmt.Print("\n--- Press Enter to close ---")
		_, _ = io.ReadAll(limitOneLineReader{})
	}
	return nil
}

type limitOneLineReader struct{}

func (limitOneLineReader) Read(p []byte) (int, error) {
	buf := make([]byte, 1)
	n, err := os.Stdin.Read(buf)
	if n > 0 {
		p[0] = buf[0]
		if buf[0] == '\n' || buf[0] == '\r' || buf[0] == 'q' {
			return 1, io.EOF
		}
		return 1, nil
	}
	return 0, err
}

func cmdHook(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rex-beacon hook claude [--force]")
	}
	switch args[0] {
	case "claude":
		fs := flag.NewFlagSet("hook claude", flag.ContinueOnError)
		force := fs.Bool("force", false, "Emit even outside Rex terminal")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return hooks.RunClaudeHook(os.Stdin, nil, *force)
	default:
		return fmt.Errorf("unknown hook target %q", args[0])
	}
}

func cmdClaude(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rex-beacon claude <install|uninstall>")
	}
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			exe = "rex-beacon"
		}
		path, err := hooks.InstallClaudeHooks(exe)
		if err != nil {
			return err
		}
		fmt.Printf("Installed Claude Code OSC 7501 hooks in %s\n", path)
		return nil
	case "uninstall":
		path, err := hooks.UninstallClaudeHooks()
		if err != nil {
			return err
		}
		fmt.Printf("Removed rex-beacon hooks from %s\n", path)
		return nil
	default:
		return fmt.Errorf("unknown claude subcommand %q", args[0])
	}
}

func cmdShellInit(args []string) error {
	shell := "zsh"
	if len(args) > 0 {
		shell = args[0]
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "rex-beacon"
	}
	out, err := hooks.ShellInit(shell, exe)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

func cmdRexPlugin(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rex-beacon rex <install|uninstall>")
	}
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			exe = "rex-beacon"
		}
		pluginPath, initPath, err := plugin.Install(exe)
		if err != nil {
			return err
		}
		fmt.Printf("Installed plugin: %s\nUpdated config:   %s\n", pluginPath, initPath)
		if rexBin, rerr := rexcli.FindBinary(""); rerr == nil {
			_ = exec.Command(rexBin, "config", "reload").Run()
			fmt.Println("Reloaded Rex config.")
		}
		return nil
	case "uninstall":
		initPath, err := plugin.Uninstall()
		if err != nil {
			return err
		}
		fmt.Printf("Removed rex-beacon plugin from %s\n", initPath)
		if rexBin, rerr := rexcli.FindBinary(""); rerr == nil {
			_ = exec.Command(rexBin, "config", "reload").Run()
		}
		return nil
	default:
		return fmt.Errorf("unknown rex subcommand %q (expected install|uninstall)", args[0])
	}
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "Config file path")
	brand := fs.String("brand", "feishu", "feishu or lark")
	appID := fs.String("app-id", "", "Feishu/Lark App ID")
	appSecret := fs.String("app-secret", "", "Feishu/Lark App Secret")
	receiveID := fs.String("receive-id", "", "Recipient open_id or chat_id")
	receiveType := fs.String("receive-id-type", "open_id", "open_id|chat_id|user_id|email")
	webhookURL := fs.String("webhook-url", "", "Feishu/Lark custom bot webhook URL")
	webhookSecret := fs.String("webhook-secret", "", "Webhook HMAC secret")
	larkChatID := fs.String("lark-cli-chat", "", "Use local lark-cli to send to chat_id")
	larkUserID := fs.String("lark-cli-user", "", "Use local lark-cli to send to user_id")
	macos := fs.Bool("macos", true, "Enable macOS desktop notifications")
	sound := fs.String("sound", "Glass", "macOS notification sound")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, path, err := config.Load(*cfgPath)
	if err != nil {
		cfg = config.DefaultConfig()
	}
	if *appID != "" {
		cfg.FeishuApp.AppID = *appID
	}
	if *appSecret != "" {
		cfg.FeishuApp.AppSecret = *appSecret
	}
	if *receiveID != "" {
		cfg.FeishuApp.ReceiveID = *receiveID
		cfg.FeishuApp.ReceiveIDType = *receiveType
		cfg.FeishuApp.Brand = *brand
		if cfg.FeishuApp.AppID != "" && cfg.FeishuApp.AppSecret != "" {
			cfg.FeishuApp.Enabled = true
		}
	}
	if *webhookURL != "" {
		cfg.Webhook.URL = *webhookURL
		cfg.Webhook.Secret = *webhookSecret
		cfg.Webhook.Enabled = true
	}
	if *larkChatID != "" || *larkUserID != "" {
		cfg.LarkCLI.ChatID = *larkChatID
		cfg.LarkCLI.UserID = *larkUserID
		cfg.LarkCLI.Enabled = true
	}
	cfg.MacOS.Enabled = *macos
	cfg.MacOS.Sound = *sound

	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Printf("Saved configuration to %s\n", path)
	return nil
}

func buildNotifier(cfg config.Config) (*notify.MultiNotifier, error) {
	var channels []notify.Notifier
	if cfg.FeishuApp.Enabled {
		n, err := notify.NewFeishuAppNotifier(cfg.FeishuApp)
		if err != nil {
			return nil, err
		}
		channels = append(channels, n)
	}
	if cfg.Webhook.Enabled {
		n, err := notify.NewFeishuWebhookNotifier(cfg.Webhook)
		if err != nil {
			return nil, err
		}
		channels = append(channels, n)
	}
	if cfg.LarkCLI.Enabled {
		n, err := notify.NewLarkCLINotifier(cfg.LarkCLI)
		if err != nil {
			return nil, err
		}
		channels = append(channels, n)
	}
	if cfg.MacOS.Enabled {
		channels = append(channels, notify.NewMacOSNotifier(cfg.MacOS))
	}
	return notify.NewMultiNotifier(channels...), nil
}
