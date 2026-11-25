package osc7501

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// State represents a valid OSC 7501 program status state.
type State string

const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateDone    State = "done"
	StateBlocked State = "blocked"
	StateError   State = "error"
	StateClear   State = "clear"
)

// Kind represents why a blocked program is waiting on the user.
type Kind string

const (
	KindPermission Kind = "permission"
	KindQuestion   Kind = "question"
	KindAuth       Kind = "auth"
)

const (
	maxSequenceBytes    = 4096
	maxIDBytes          = 128
	maxIDSegments       = 8
	maxSegmentBytes     = 32
	maxAppBytes         = 32
	maxTitleDecodedByte = 192
	maxMsgDecodedByte   = 2048
)

// Report describes an OSC 7501 status sequence payload.
type Report struct {
	State    State
	ID       string
	Kind     Kind
	Progress *int
	App      string
	Title    string
	Msg      string
}

// IsRexTerminal returns true if the current process appears to be running inside Rex.
func IsRexTerminal() bool {
	return os.Getenv("REX_BLOCK") != "" || strings.EqualFold(os.Getenv("TERM_PROGRAM"), "rex")
}

// ParseState validates and parses a state string.
func ParseState(s string) (State, error) {
	switch State(strings.ToLower(strings.TrimSpace(s))) {
	case StateIdle:
		return StateIdle, nil
	case StateWorking:
		return StateWorking, nil
	case StateDone:
		return StateDone, nil
	case StateBlocked:
		return StateBlocked, nil
	case StateError:
		return StateError, nil
	case StateClear:
		return StateClear, nil
	default:
		return "", fmt.Errorf("invalid OSC 7501 state %q (expected idle, working, done, blocked, error, or clear)", s)
	}
}

// ParseKind validates and parses a blocked kind string.
func ParseKind(s string) (Kind, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(s))) {
	case "":
		return "", nil
	case KindPermission:
		return KindPermission, nil
	case KindQuestion:
		return KindQuestion, nil
	case KindAuth:
		return KindAuth, nil
	default:
		return "", fmt.Errorf("invalid OSC 7501 kind %q (expected permission, question, or auth)", s)
	}
}

func isTokenChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '.' || b == '+' || b == '-'
}

func validateToken(val string, maxLen int) bool {
	if len(val) == 0 || len(val) > maxLen {
		return false
	}
	for i := 0; i < len(val); i++ {
		if !isTokenChar(val[i]) {
			return false
		}
	}
	return true
}

func validateID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > maxIDBytes {
		return fmt.Errorf("id exceeds %d bytes", maxIDBytes)
	}
	parts := strings.Split(id, "/")
	if len(parts) > maxIDSegments {
		return fmt.Errorf("id exceeds %d segments", maxIDSegments)
	}
	for _, seg := range parts {
		if !validateToken(seg, maxSegmentBytes) {
			return fmt.Errorf("invalid id segment %q", seg)
		}
	}
	return nil
}

// SanitizeText removes control characters forbidden by OSC 7501 (C0/DEL/C1) and truncates
// cleanly on a UTF-8 rune boundary to at most maxBytes.
func SanitizeText(s string, maxBytes int) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || unicode.IsControl(r) {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		if r == ' ' {
			if lastSpace {
				continue
			}
			lastSpace = true
		} else {
			lastSpace = false
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) <= maxBytes {
		return out
	}
	// Truncate on UTF-8 rune boundary.
	for len(out) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(out)
		if size <= 0 {
			break
		}
		out = out[:len(out)-size]
	}
	return strings.TrimSpace(out)
}

// Encode builds a valid OSC 7501 escape sequence string.
func Encode(r Report) (string, error) {
	state, err := ParseState(string(r.State))
	if err != nil {
		return "", err
	}
	if err := validateID(r.ID); err != nil {
		return "", err
	}

	pairs := []string{"state=" + string(state)}
	if r.ID != "" {
		pairs = append(pairs, "id="+r.ID)
	}

	if state != StateClear {
		if r.App != "" {
			if !validateToken(r.App, maxAppBytes) {
				return "", fmt.Errorf("invalid app %q: must match [A-Za-z0-9_.+-]{1,%d}", r.App, maxAppBytes)
			}
			pairs = append(pairs, "app="+r.App)
		}

		if state == StateBlocked && r.Kind != "" {
			kind, err := ParseKind(string(r.Kind))
			if err != nil {
				return "", err
			}
			if kind != "" {
				pairs = append(pairs, "kind="+string(kind))
			}
		}

		if (state == StateWorking || state == StateBlocked) && r.Progress != nil {
			p := *r.Progress
			if p < 0 || p > 100 {
				return "", fmt.Errorf("progress %d out of range [0, 100]", p)
			}
			pairs = append(pairs, fmt.Sprintf("progress=%d", p))
		}

		if cleanTitle := SanitizeText(r.Title, maxTitleDecodedByte); cleanTitle != "" {
			enc := base64.StdEncoding.EncodeToString([]byte(cleanTitle))
			pairs = append(pairs, "title="+enc)
		}

		if cleanMsg := SanitizeText(r.Msg, maxMsgDecodedByte); cleanMsg != "" {
			enc := base64.StdEncoding.EncodeToString([]byte(cleanMsg))
			pairs = append(pairs, "msg="+enc)
		}
	}

	seq := "\x1b]7501;" + strings.Join(pairs, ":") + "\x1b\\"
	if len(seq) > maxSequenceBytes {
		return "", errors.New("encoded OSC 7501 sequence exceeds 4096 bytes")
	}
	return seq, nil
}

// EmitTo encodes and writes an OSC 7501 sequence to the provided writer.
func EmitTo(w io.Writer, r Report) error {
	seq, err := Encode(r)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, seq)
	return err
}

// Emit writes the OSC 7501 sequence to /dev/tty when available, falling back to stdout.
func Emit(r Report) error {
	seq, err := Encode(r)
	if err != nil {
		return err
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		if _, werr := io.WriteString(tty, seq); werr == nil {
			return nil
		}
	}
	_, err = io.WriteString(os.Stdout, seq)
	return err
}
