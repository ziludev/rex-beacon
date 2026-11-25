package rexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Client wraps calls to the rex CLI binary.
type Client struct {
	Binary string
	Server string
}

// FindBinary locates the rex CLI executable.
func FindBinary(explicit string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if envBin := strings.TrimSpace(os.Getenv("REX_BEACON_REX_BIN")); envBin != "" {
		candidates = append(candidates, envBin)
	}
	if binDir := strings.TrimSpace(os.Getenv("REX_BIN_DIR")); binDir != "" {
		candidates = append(candidates, filepath.Join(binDir, "rex"))
	}
	if p, err := exec.LookPath("rex"); err == nil {
		candidates = append(candidates, p)
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "Library", "Application Support", "com.superlogical.rex", "RexServer", "Versions", "Current", "Rex Server.app", "Contents", "MacOS", "rex"),
		)
	}
	candidates = append(candidates,
		"/Applications/Rex.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
	)

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("could not find rex CLI binary; pass --rex-bin or set REX_BEACON_REX_BIN")
}

// NewClient constructs a Rex CLI client, discovering the rex binary if empty.
func NewClient(binary, server string) (*Client, error) {
	bin, err := FindBinary(binary)
	if err != nil {
		return nil, err
	}
	return &Client{Binary: bin, Server: server}, nil
}

// API provides the subset of Rex control operations needed by the watcher and CLI.
type API interface {
	Whoami(ctx context.Context) (*WhoamiResp, error)
	SessionList(ctx context.Context) (*SessionListResp, error)
	SessionView(ctx context.Context, sessionID string) (*SessionViewResp, error)
	ClientList(ctx context.Context) (*ClientListResp, error)
	BlockProcess(ctx context.Context, blockID string) (*ProcessResp, error)
	BlockProgramStatus(ctx context.Context, blockID string) (*ProgramStatusResp, error)
	BlockPorts(ctx context.Context, blockID string) (*PortsResp, error)
	BlockFormatText(ctx context.Context, blockID string) (string, error)
	BlockWrite(ctx context.Context, blockID, data string) error
	StreamEvents(ctx context.Context, session string, all bool, handler func([]byte) error) error
}

func (c *Client) baseArgs() []string {
	if c.Server != "" {
		return []string{"-S", c.Server}
	}
	return nil
}

func (c *Client) runJSON(ctx context.Context, args []string, out any) error {
	full := append(c.baseArgs(), args...)
	cmd := exec.CommandContext(ctx, c.Binary, full...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(raw))
		if msg != "" {
			return fmt.Errorf("rex %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("rex %s: %w", strings.Join(args, " "), err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode rex %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Whoami calls `rex whoami --json`.
func (c *Client) Whoami(ctx context.Context) (*WhoamiResp, error) {
	var resp WhoamiResp
	if err := c.runJSON(ctx, []string{"whoami", "--json"}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SessionList calls `rex ls --json`.
func (c *Client) SessionList(ctx context.Context) (*SessionListResp, error) {
	var resp SessionListResp
	if err := c.runJSON(ctx, []string{"ls", "--json"}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SessionView calls `rex api call -s <sessionID> session.view`.
func (c *Client) SessionView(ctx context.Context, sessionID string) (*SessionViewResp, error) {
	var resp SessionViewResp
	if err := c.runJSON(ctx, []string{"api", "call", "-s", sessionID, "session.view"}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ClientList calls `rex client ls --json`.
func (c *Client) ClientList(ctx context.Context) (*ClientListResp, error) {
	var resp ClientListResp
	if err := c.runJSON(ctx, []string{"client", "ls", "--json"}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BlockProcess calls `rex block call -b <blockID> process`.
func (c *Client) BlockProcess(ctx context.Context, blockID string) (*ProcessResp, error) {
	var resp ProcessResp
	if err := c.runJSON(ctx, []string{"block", "call", "-b", blockID, "process"}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BlockProgramStatus calls `rex block call -b <blockID> program_status`.
func (c *Client) BlockProgramStatus(ctx context.Context, blockID string) (*ProgramStatusResp, error) {
	var resp ProgramStatusResp
	if err := c.runJSON(ctx, []string{"block", "call", "-b", blockID, "program_status"}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BlockPorts calls `rex block call -b <blockID> ports`.
func (c *Client) BlockPorts(ctx context.Context, blockID string) (*PortsResp, error) {
	var resp PortsResp
	if err := c.runJSON(ctx, []string{"block", "call", "-b", blockID, "ports"}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BlockFormatText captures trimmed, unwrapped terminal screen+scrollback text.
func (c *Client) BlockFormatText(ctx context.Context, blockID string) (string, error) {
	var resp FormatResp
	reqArgs, _ := json.Marshal(map[string]any{
		"format":    "text",
		"trim":      true,
		"unwrap":    true,
		"max_bytes": 65536,
	})
	if err := c.runJSON(ctx, []string{"block", "call", "-b", blockID, "format", string(reqArgs)}, &resp); err != nil {
		return "", err
	}
	if resp.ID == nil || resp.EOF {
		return resp.Content, nil
	}
	// If truncated, read the tail end (up to ~64 KiB from end) using blob_read?
	// Since format returns from the start and total is known, jump close to the tail when huge.
	var sb strings.Builder
	offset := len(resp.Content)
	if resp.Total > 65536 && resp.Total-65536 > offset {
		offset = resp.Total - 65536
	} else {
		sb.WriteString(resp.Content)
	}
	for {
		bArgs, _ := json.Marshal(map[string]any{
			"id":        *resp.ID,
			"offset":    offset,
			"max_bytes": 65536,
		})
		var chunk BlobReadResp
		if err := c.runJSON(ctx, []string{"block", "call", "-b", blockID, "blob_read", string(bArgs)}, &chunk); err != nil {
			break
		}
		sb.WriteString(chunk.Data)
		if chunk.EOF || len(chunk.Data) == 0 {
			break
		}
		offset += len(chunk.Data)
	}
	return sb.String(), nil
}

// BlockWrite writes text directly to a terminal block.
func (c *Client) BlockWrite(ctx context.Context, blockID, data string) error {
	payload, _ := json.Marshal(map[string]string{"data": data})
	return c.runJSON(ctx, []string{"block", "call", "-b", blockID, "write", string(payload)}, nil)
}

// StreamEvents runs `rex events --json` and invokes handler for each JSON line.
func (c *Client) StreamEvents(ctx context.Context, session string, all bool, handler func([]byte) error) error {
	args := c.baseArgs()
	args = append(args, "events", "--json")
	if all {
		args = append(args, "--all")
	} else if session != "" {
		args = append(args, "-s", session)
	}

	cmd := exec.CommandContext(ctx, c.Binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	errCh := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(io.LimitReader(stderr, 8192))
		errCh <- strings.TrimSpace(string(raw))
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := handler([]byte(line)); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	errText := <-errCh
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		if errText != "" {
			return fmt.Errorf("%w: %s", waitErr, errText)
		}
		return waitErr
	}
	return nil
}

// TailLines returns the last n non-empty lines from terminal text output.
func TailLines(content string, n int) string {
	if n <= 0 {
		return ""
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	// Drop trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// --- JSON payloads matching Rex Server API revision 19 ---

type EventEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type SessionEventPayload struct {
	SessionID string          `json:"session_id"`
	Event     json.RawMessage `json:"event"`
}

type BlockEventPayload struct {
	BlockID     string          `json:"block_id"`
	CreatorName string          `json:"creator_name"`
	Name        string          `json:"name"`
	Payload     json.RawMessage `json:"payload"`
}

type ProcessOwner struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

type ProgramStatusRecord struct {
	ID           string        `json:"id,omitempty"`
	State        string        `json:"state"`
	Kind         string        `json:"kind,omitempty"`
	Progress     *int          `json:"progress,omitempty"`
	App          string        `json:"app,omitempty"`
	EffectiveApp string        `json:"effective_app,omitempty"`
	Title        string        `json:"title,omitempty"`
	Msg          string        `json:"msg,omitempty"`
	UpdatedAt    string        `json:"updated_at"`
	Owner        *ProcessOwner `json:"owner,omitempty"`
}

type ProgramStatusChangedPayload struct {
	ID     string              `json:"id"`
	Record ProgramStatusRecord `json:"record"`
	Reason string              `json:"reason"`
}

type ProgramStatusRemovedPayload struct {
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
}

type ChildExitedPayload struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

type DesktopNotificationPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type PortEntry struct {
	Port        int    `json:"port"`
	Address     string `json:"address"`
	Protocol    string `json:"protocol"`
	FirstSeenAt string `json:"first_seen_at"`
	PID         *int   `json:"pid,omitempty"`
}

type ProcessInfo struct {
	PID         int    `json:"pid"`
	UserID      int    `json:"user_id"`
	User        string `json:"user,omitempty"`
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	InvokedPath string `json:"invoked_path,omitempty"`
	Argv0       string `json:"argv0,omitempty"`
	Script      string `json:"script,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
}

type PortsChangedPayload struct {
	Status    string        `json:"status"`
	Ports     []PortEntry   `json:"ports"`
	Processes []ProcessInfo `json:"processes"`
}

type PortsResp = PortsChangedPayload

type ProcessResp struct {
	LastExit   *ChildExitedPayload `json:"last_exit"`
	Child      *ProcessInfo        `json:"child,omitempty"`
	Foreground *ProcessInfo        `json:"foreground,omitempty"`
	Ancestors  []ProcessInfo       `json:"ancestors,omitempty"`
	Home       string              `json:"home,omitempty"`
}

type ProgramStatusResp struct {
	Records []ProgramStatusRecord `json:"records"`
}

type FormatResp struct {
	Content string `json:"content"`
	ID      *int   `json:"id,omitempty"`
	Total   int    `json:"total,omitempty"`
	EOF     bool   `json:"eof,omitempty"`
}

type BlobReadResp struct {
	ID    int    `json:"id"`
	Data  string `json:"data"`
	Total int    `json:"total"`
	EOF   bool   `json:"eof"`
}

type WhoamiResp struct {
	Account    string `json:"account"`
	Endpoint   string `json:"endpoint"`
	RexVersion string `json:"rex_version"`
	SessionID  string `json:"session_id"`
	WindowID   string `json:"window_id"`
	BlockID    string `json:"block_id"`
}

type SessionSummary struct {
	SessionID string `json:"session_id"`
	Label     string `json:"label"`
}

type SessionListResp struct {
	Sessions []SessionSummary `json:"sessions"`
}

type SessionBlock struct {
	BlockID     string `json:"block_id"`
	Label       string `json:"label"`
	CreatorName string `json:"creator_name"`
	Flavor      string `json:"flavor"`
	HostID      string `json:"host_id"`
	Owner       string `json:"owner,omitempty"`
}

type SessionLayer struct {
	LayerID string         `json:"layer_id"`
	Kind    string         `json:"kind"`
	Blocks  []SessionBlock `json:"blocks"`
}

type SessionWindow struct {
	WindowID       string         `json:"window_id"`
	Label          string         `json:"label"`
	FocusedBlockID string         `json:"focused_block_id"`
	Layers         []SessionLayer `json:"layers"`
}

type SessionViewResp struct {
	Revision       int64           `json:"revision"`
	SessionID      string          `json:"session_id"`
	Label          string          `json:"label"`
	ActiveWindowID string          `json:"active_window_id"`
	Windows        []SessionWindow `json:"windows"`
}

type ClientInfo struct {
	Kind     string `json:"kind"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Label    string `json:"label"`
}

type ConnectedClient struct {
	ClientID        string     `json:"client_id"`
	Info            ClientInfo `json:"info"`
	ConnectionState string     `json:"connection_state"`
	SessionIDs      []string   `json:"session_ids,omitempty"`
}

type ClientListResp struct {
	Clients []ConnectedClient `json:"clients"`
}
