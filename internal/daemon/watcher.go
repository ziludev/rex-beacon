package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ziludev/rex-beacon/internal/config"
	"github.com/ziludev/rex-beacon/internal/notify"
	"github.com/ziludev/rex-beacon/internal/rexcli"
)

type alertStamp struct {
	signature string
	at        time.Time
}

// Watcher listens to Rex events, enriches them with session/pane context,
// and dispatches alerts through the configured Notifier.
type Watcher struct {
	rex      rexcli.API
	cfg      config.Config
	notifier notify.Notifier
	log      *log.Logger
	hostname string

	mu            sync.Mutex
	workingStart  map[string]time.Time
	lastNotified  map[string]alertStamp
	turnNotified  map[string]string
	lastBlockDone map[string]time.Time
	lastErrExit   map[string]time.Time
	knownPorts    map[string]map[string]struct{}
	sessionCache  map[string]*rexcli.SessionViewResp
	now           func() time.Time
	atComputer    func(context.Context) bool
}

// NewWatcher creates a Rex event watcher.
func NewWatcher(rex rexcli.API, cfg config.Config, notifier notify.Notifier, logger *log.Logger) *Watcher {
	if logger == nil {
		logger = log.New(os.Stderr, "[rex-beacon] ", log.LstdFlags)
	}
	host, _ := os.Hostname()
	idleThreshold := time.Duration(cfg.Rules.UserIdleThresholdSec) * time.Second
	return &Watcher{
		rex:           rex,
		cfg:           cfg,
		notifier:      notifier,
		log:           logger,
		hostname:      host,
		workingStart:  make(map[string]time.Time),
		lastNotified:  make(map[string]alertStamp),
		turnNotified:  make(map[string]string),
		lastBlockDone: make(map[string]time.Time),
		lastErrExit:   make(map[string]time.Time),
		knownPorts:    make(map[string]map[string]struct{}),
		sessionCache:  make(map[string]*rexcli.SessionViewResp),
		now:           time.Now,
		atComputer: func(ctx context.Context) bool {
			return isUserAtComputer(ctx, idleThreshold)
		},
	}
}

// Run streams events continuously with exponential backoff on transient errors.
func (w *Watcher) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		started := w.now()
		w.log.Printf("connecting to rex event stream (all=%v session=%q)", w.cfg.All, w.cfg.Session)
		err := w.rex.StreamEvents(ctx, w.cfg.Session, w.cfg.All, func(line []byte) error {
			w.HandleEventLine(ctx, line)
			return nil
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if w.now().Sub(started) > 30*time.Second {
			backoff = time.Second
		}
		w.log.Printf("event stream ended (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

// HandleEventLine parses and processes one NDJSON line from `rex events --json`.
func (w *Watcher) HandleEventLine(ctx context.Context, line []byte) {
	var env rexcli.EventEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		w.log.Printf("ignore malformed event json: %v", err)
		return
	}

	switch env.Type {
	case "session_destroyed":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(env.Payload, &p) == nil && p.SessionID != "" {
			w.mu.Lock()
			delete(w.sessionCache, p.SessionID)
			w.mu.Unlock()
		}
	case "session_event":
		var sp rexcli.SessionEventPayload
		if err := json.Unmarshal(env.Payload, &sp); err != nil {
			return
		}
		var inner rexcli.EventEnvelope
		if err := json.Unmarshal(sp.Event, &inner); err != nil {
			return
		}
		w.handleSessionEvent(ctx, sp.SessionID, inner)
	}
}

func (w *Watcher) handleSessionEvent(ctx context.Context, sessionID string, inner rexcli.EventEnvelope) {
	switch inner.Type {
	case "session_view_changed", "window_created", "window_closed", "window_label_changed", "block_created", "session_label_changed":
		w.mu.Lock()
		delete(w.sessionCache, sessionID)
		w.mu.Unlock()
	case "block_closed":
		var p struct {
			BlockID string `json:"block_id"`
		}
		if json.Unmarshal(inner.Payload, &p) == nil && p.BlockID != "" {
			w.cleanupBlock(sessionID, p.BlockID)
		}
	case "block_event":
		var bp rexcli.BlockEventPayload
		if err := json.Unmarshal(inner.Payload, &bp); err != nil {
			return
		}
		if bp.CreatorName != "com.superlogical.terminal" {
			return
		}
		w.handleTerminalEvent(ctx, sessionID, bp)
	}
}

func (w *Watcher) cleanupBlock(sessionID, blockID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.sessionCache, sessionID)
	prefix := blockID + "|"
	for k := range w.workingStart {
		if strings.HasPrefix(k, prefix) {
			delete(w.workingStart, k)
		}
	}
	for k := range w.turnNotified {
		if strings.HasPrefix(k, prefix) {
			delete(w.turnNotified, k)
		}
	}
	for k := range w.lastNotified {
		if strings.HasPrefix(k, prefix) {
			delete(w.lastNotified, k)
		}
	}
	delete(w.lastBlockDone, blockID)
	delete(w.lastErrExit, blockID)
	delete(w.knownPorts, blockID)
}

func (w *Watcher) handleTerminalEvent(ctx context.Context, sessionID string, bp rexcli.BlockEventPayload) {
	switch bp.Name {
	case "program_status_changed":
		var p rexcli.ProgramStatusChangedPayload
		if err := json.Unmarshal(bp.Payload, &p); err != nil {
			return
		}
		w.onProgramStatusChanged(ctx, sessionID, bp.BlockID, p)

	case "program_status_removed":
		var p rexcli.ProgramStatusRemovedPayload
		if err := json.Unmarshal(bp.Payload, &p); err != nil {
			return
		}
		w.mu.Lock()
		for _, id := range p.IDs {
			k := bp.BlockID + "|" + id
			delete(w.workingStart, k)
			delete(w.turnNotified, k)
		}
		w.mu.Unlock()

	case "ports_changed":
		if !w.cfg.Rules.NotifyOnPorts {
			return
		}
		var p rexcli.PortsChangedPayload
		if err := json.Unmarshal(bp.Payload, &p); err != nil {
			return
		}
		w.onPortsChanged(ctx, sessionID, bp.BlockID, p)

	case "bell":
		if !w.cfg.Rules.NotifyOnBell {
			return
		}
		n := w.enrichNotification(ctx, sessionID, bp.BlockID, notify.Notification{
			EventKind: "bell",
			State:     "info",
			Title:     "Terminal Bell",
			Msg:       "A terminal rang the bell",
			Timestamp: w.now(),
		})
		w.dispatch(ctx, bp.BlockID+"|bell", "bell", n)

	case "child_exited":
		var p rexcli.ChildExitedPayload
		if err := json.Unmarshal(bp.Payload, &p); err != nil {
			return
		}
		cooldown := time.Duration(w.cfg.Rules.CooldownSec) * time.Second
		if cooldown < 15*time.Second {
			cooldown = 15 * time.Second
		}
		w.mu.Lock()
		recentErr := !w.lastErrExit[bp.BlockID].IsZero() && w.now().Sub(w.lastErrExit[bp.BlockID]) < cooldown
		w.mu.Unlock()
		w.cleanupBlock(sessionID, bp.BlockID)
		if !w.cfg.Rules.NotifyOnNonZeroExit || p.ExitCode == 0 || isInterruptExitCode(p.ExitCode) || recentErr {
			return
		}
		msg := fmt.Sprintf("Process exited with code %d", p.ExitCode)
		if p.Error != "" {
			msg = p.Error
		}
		n := w.enrichNotification(ctx, sessionID, bp.BlockID, notify.Notification{
			EventKind: "child_exited",
			State:     "error",
			Title:     "Process Exited",
			Msg:       msg,
			Timestamp: w.now(),
		})
		w.dispatch(ctx, bp.BlockID+"|exit", msg, n)
	}
}

func isInterruptExitCode(code int) bool {
	// Ignore SIGHUP (129), SIGINT/Ctrl+C (130), SIGTERM (143).
	return code == 129 || code == 130 || code == 143
}

func (w *Watcher) onProgramStatusChanged(ctx context.Context, sessionID, blockID string, p rexcli.ProgramStatusChangedPayload) {
	rec := p.Record
	key := blockID + "|" + p.ID
	now := w.now()

	app := rec.EffectiveApp
	if app == "" {
		app = rec.App
	}
	if !w.appAllowed(app) {
		return
	}

	var dur time.Duration
	w.mu.Lock()
	switch rec.State {
	case "working":
		if _, ok := w.workingStart[key]; !ok {
			w.workingStart[key] = now
		}
		delete(w.turnNotified, key)
		w.clearBlockKindStampsLocked(blockID)
	case "done", "error", "idle":
		if started, ok := w.workingStart[key]; ok {
			dur = now.Sub(started)
			delete(w.workingStart, key)
		}
		if rec.State == "idle" {
			delete(w.turnNotified, key)
		}
		if rec.State == "error" {
			w.lastErrExit[blockID] = now
		}
	case "blocked":
		if _, ok := w.workingStart[key]; !ok {
			w.workingStart[key] = now
		}
	}
	w.mu.Unlock()

	if !w.stateEnabled(rec.State) {
		return
	}

	// Minimum working duration filter for "done" (blocked & error always alert).
	if rec.State == "done" && w.cfg.Rules.MinWorkingDurationSec > 0 && dur > 0 {
		if dur < time.Duration(w.cfg.Rules.MinWorkingDurationSec)*time.Second {
			return
		}
	}

	if w.cfg.Rules.SkipIfClientFocusedNow && w.isFocusedByConnectedClient(ctx, sessionID, blockID) {
		return
	}

	skipAtComputer, localOnly := w.applyAtComputerPolicy(ctx, sessionID, blockID)
	if skipAtComputer {
		return
	}

	sig := fmt.Sprintf("%s|%s|%s|%s", rec.State, rec.Kind, rec.Title, rec.Msg)
	if !w.reserveStatusDispatch(blockID, key, rec.State, rec.Kind, sig, dur, now) {
		return
	}

	n := notify.Notification{
		EventKind: "program_status",
		State:     rec.State,
		Kind:      rec.Kind,
		App:       app,
		Title:     rec.Title,
		Msg:       rec.Msg,
		RecordID:  p.ID,
		Progress:  rec.Progress,
		Duration:  dur,
		Timestamp: now,
		LocalOnly: localOnly,
	}
	if rec.Owner != nil {
		n.ProcessName = rec.Owner.Name
	}
	n = w.enrichNotification(ctx, sessionID, blockID, n)
	w.sendNotification(ctx, n)
}

func (w *Watcher) clearBlockKindStampsLocked(blockID string) {
	prefix := blockID + "|kind|"
	for k := range w.lastNotified {
		if strings.HasPrefix(k, prefix) {
			delete(w.lastNotified, k)
		}
	}
}

func (w *Watcher) reserveStatusDispatch(blockID, key, state, kind, sig string, dur time.Duration, now time.Time) bool {
	cooldown := time.Duration(w.cfg.Rules.CooldownSec) * time.Second
	repeatWindow := cooldown
	if repeatWindow < 5*time.Minute {
		repeatWindow = 5 * time.Minute
	}
	turnTag := state + "|" + kind
	blockKindKey := blockID + "|kind|" + turnTag

	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. Suppress duplicate "done" in the same turn (without an intervening "working").
	if state == "done" && w.turnNotified[key] == turnTag {
		return false
	}

	// 2. Throttle rapid consecutive "done" alerts on the same pane within cooldown.
	if state == "done" {
		if lastDone, ok := w.lastBlockDone[blockID]; ok && now.Sub(lastDone) < cooldown {
			if dur == 0 || dur < cooldown {
				return false
			}
		}
	}

	// 3. Deduplicate same (blockID, state, kind) bursts within cooldown even if msg differs
	// (e.g., Claude Code PermissionRequest + Notification hooks firing together).
	if prev, ok := w.lastNotified[blockKindKey]; ok && now.Sub(prev.at) < cooldown {
		return false
	}

	// 4. Deduplicate identical signatures within cooldown, or within repeatWindow while
	// still in the same turn without an intervening "working" state.
	if prev, ok := w.lastNotified[key]; ok && prev.signature == sig {
		if now.Sub(prev.at) < cooldown {
			return false
		}
		if w.turnNotified[key] == turnTag && now.Sub(prev.at) < repeatWindow {
			return false
		}
	}

	stamp := alertStamp{signature: sig, at: now}
	w.lastNotified[key] = stamp
	w.lastNotified[blockKindKey] = stamp
	w.turnNotified[key] = turnTag
	if state == "done" {
		w.lastBlockDone[blockID] = now
	}
	return true
}

func (w *Watcher) onPortsChanged(ctx context.Context, sessionID, blockID string, p rexcli.PortsChangedPayload) {
	procByPID := make(map[int]rexcli.ProcessInfo, len(p.Processes))
	for _, pr := range p.Processes {
		procByPID[pr.PID] = pr
	}

	current := make(map[string]struct{}, len(p.Ports))
	var opened []string
	w.mu.Lock()
	prev := w.knownPorts[blockID]
	for _, port := range p.Ports {
		k := fmt.Sprintf("%s:%d", port.Address, port.Port)
		current[k] = struct{}{}
		if prev == nil {
			continue
		}
		if _, seen := prev[k]; !seen {
			label := k
			if port.PID != nil {
				if pr, ok := procByPID[*port.PID]; ok && pr.Name != "" {
					label = fmt.Sprintf("%s (%s)", k, pr.Name)
				}
			}
			opened = append(opened, label)
		}
	}
	w.knownPorts[blockID] = current
	w.mu.Unlock()

	if len(opened) == 0 {
		return
	}

	msg := "Listening on " + strings.Join(opened, ", ")
	n := w.enrichNotification(ctx, sessionID, blockID, notify.Notification{
		EventKind: "ports",
		State:     "info",
		Title:     "Port Opened",
		Msg:       msg,
		Timestamp: w.now(),
	})
	w.dispatch(ctx, blockID+"|ports", msg, n)
}

func (w *Watcher) dispatch(ctx context.Context, key, sig string, n notify.Notification) {
	skipAtComputer, localOnly := w.applyAtComputerPolicy(ctx, n.SessionID, n.BlockID)
	if skipAtComputer {
		return
	}
	if localOnly {
		n.LocalOnly = true
	}

	cooldown := time.Duration(w.cfg.Rules.CooldownSec) * time.Second
	now := w.now()

	w.mu.Lock()
	prev, ok := w.lastNotified[key]
	if ok && prev.signature == sig && now.Sub(prev.at) < cooldown {
		w.mu.Unlock()
		return
	}
	w.lastNotified[key] = alertStamp{signature: sig, at: now}
	w.mu.Unlock()

	w.sendNotification(ctx, n)
}

func (w *Watcher) applyAtComputerPolicy(ctx context.Context, sessionID, blockID string) (skip bool, localOnly bool) {
	mode := strings.ToLower(strings.TrimSpace(w.cfg.Rules.WhenAtComputer))
	if mode == "" || mode == "notify" || mode == "off" {
		return false, false
	}
	if w.atComputer == nil || !w.atComputer(ctx) {
		return false, false
	}
	switch mode {
	case "skip_all":
		return true, false
	case "skip_remote":
		if w.isFocusedByConnectedClient(ctx, sessionID, blockID) {
			return true, false
		}
		return false, true
	default:
		return false, false
	}
}

func (w *Watcher) sendNotification(ctx context.Context, n notify.Notification) {
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := w.notifier.Send(sendCtx, n); err != nil {
		w.log.Printf("notify error [%s %s]: %v", n.State, n.App, err)
	} else {
		w.log.Printf("notified [%s] app=%q session=%q block=%s", n.State, n.App, n.SessionLabel, notify.ShortID(n.BlockID))
	}
}

func (w *Watcher) enrichNotification(ctx context.Context, sessionID, blockID string, n notify.Notification) notify.Notification {
	n.SessionID = sessionID
	n.BlockID = blockID
	n.Host = w.hostname

	enrichCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	if view := w.getSessionView(enrichCtx, sessionID); view != nil {
		n.SessionLabel = view.Label
		for _, win := range view.Windows {
			for _, layer := range win.Layers {
				for _, blk := range layer.Blocks {
					if blk.BlockID == blockID {
						n.WindowID = win.WindowID
						n.WindowLabel = win.Label
						n.BlockLabel = blk.Label
					}
				}
			}
		}
	}

	if proc, err := w.rex.BlockProcess(enrichCtx, blockID); err == nil && proc != nil {
		if proc.Foreground != nil {
			if n.ProcessName == "" {
				n.ProcessName = proc.Foreground.Name
			}
			n.Cwd = proc.Foreground.Cwd
		} else if proc.Child != nil {
			if n.ProcessName == "" {
				n.ProcessName = proc.Child.Name
			}
			n.Cwd = proc.Child.Cwd
		}
	}

	if w.cfg.Rules.TailLines > 0 {
		if raw, err := w.rex.BlockFormatText(enrichCtx, blockID); err == nil {
			n.Tail = notify.ExtractTerminalConclusion(raw, w.cfg.Rules.TailLines, 3200)
		}
	}
	return n
}

func (w *Watcher) getSessionView(ctx context.Context, sessionID string) *rexcli.SessionViewResp {
	if sessionID == "" {
		return nil
	}
	w.mu.Lock()
	cached := w.sessionCache[sessionID]
	w.mu.Unlock()
	if cached != nil {
		return cached
	}
	view, err := w.rex.SessionView(ctx, sessionID)
	if err != nil || view == nil {
		return nil
	}
	w.mu.Lock()
	w.sessionCache[sessionID] = view
	w.mu.Unlock()
	return view
}

func (w *Watcher) isFocusedByConnectedClient(ctx context.Context, sessionID, blockID string) bool {
	view := w.getSessionView(ctx, sessionID)
	if view == nil {
		return false
	}
	for _, win := range view.Windows {
		if win.WindowID == view.ActiveWindowID && win.FocusedBlockID == blockID {
			clients, err := w.rex.ClientList(ctx)
			if err != nil || clients == nil {
				return false
			}
			for _, c := range clients.Clients {
				if c.Info.Kind == "app" && c.ConnectionState == "connected" {
					for _, sid := range c.SessionIDs {
						if sid == sessionID {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func (w *Watcher) stateEnabled(state string) bool {
	for _, s := range w.cfg.Rules.NotifyStates {
		if strings.EqualFold(strings.TrimSpace(s), state) {
			return true
		}
	}
	return false
}

func (w *Watcher) appAllowed(app string) bool {
	if len(w.cfg.Rules.IncludeApps) > 0 {
		matched := false
		for _, inc := range w.cfg.Rules.IncludeApps {
			if strings.EqualFold(strings.TrimSpace(inc), app) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, ign := range w.cfg.Rules.IgnoreApps {
		if strings.EqualFold(strings.TrimSpace(ign), app) {
			return false
		}
	}
	return true
}
