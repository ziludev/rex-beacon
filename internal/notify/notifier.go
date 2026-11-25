package notify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Notification describes an enriched Rex terminal event ready for delivery.
type Notification struct {
	EventKind    string // "program_status", "ports", "bell", "child_exited", "desktop_notification", "test"
	State        string // "blocked", "error", "done", "working", "info"
	Kind         string // "permission", "question", "auth", etc.
	App          string
	Title        string
	Msg          string
	RecordID     string
	Progress     *int
	SessionID    string
	SessionLabel string
	WindowID     string
	WindowLabel  string
	BlockID      string
	BlockLabel   string
	Cwd          string
	ProcessName  string
	Host         string
	Duration     time.Duration
	Tail         string
	Timestamp    time.Time
}

// Notifier delivers a Notification to a target channel.
type Notifier interface {
	Name() string
	Send(ctx context.Context, n Notification) error
}

// MultiNotifier fans out notifications to all enabled channels and aggregates errors.
type MultiNotifier struct {
	notifiers []Notifier
}

// NewMultiNotifier creates a composite notifier.
func NewMultiNotifier(notifiers ...Notifier) *MultiNotifier {
	var active []Notifier
	for _, n := range notifiers {
		if n != nil {
			active = append(active, n)
		}
	}
	return &MultiNotifier{notifiers: active}
}

// Count returns the number of configured channels.
func (m *MultiNotifier) Count() int {
	return len(m.notifiers)
}

// Names returns the names of configured channels.
func (m *MultiNotifier) Names() []string {
	out := make([]string, len(m.notifiers))
	for i, n := range m.notifiers {
		out[i] = n.Name()
	}
	return out
}

// Name implements Notifier.
func (m *MultiNotifier) Name() string {
	if len(m.notifiers) == 0 {
		return "none"
	}
	return strings.Join(m.Names(), ",")
}

// Send delivers n to every configured channel, continuing even if one fails.
func (m *MultiNotifier) Send(ctx context.Context, n Notification) error {
	if len(m.notifiers) == 0 {
		return nil
	}
	type result struct {
		name string
		err  error
	}
	ch := make(chan result, len(m.notifiers))
	for _, target := range m.notifiers {
		target := target
		go func() {
			ch <- result{name: target.Name(), err: target.Send(ctx, n)}
		}()
	}
	var errs []string
	for i := 0; i < len(m.notifiers); i++ {
		r := <-ch
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.name, r.err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// FormatDuration renders a human-friendly elapsed duration.
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// ShortID returns the last segment suffix for compact display.
func ShortID(id string) string {
	if id == "" {
		return ""
	}
	if idx := strings.LastIndex(id, "-"); idx >= 0 && len(id)-idx-1 >= 6 {
		return id[idx+1:]
	}
	if len(id) > 12 {
		return id[len(id)-12:]
	}
	return id
}
