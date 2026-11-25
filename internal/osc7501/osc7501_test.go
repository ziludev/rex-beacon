package osc7501

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncodeAndSanitize(t *testing.T) {
	p := 42
	seq, err := Encode(Report{
		State:    StateBlocked,
		ID:       "deploy/us-east",
		Kind:     KindPermission,
		Progress: &p,
		App:      "claude-code",
		Title:    "US East\nCluster",
		Msg:      "Approve\tdeploy\x07?",
	})
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	if !strings.HasPrefix(seq, "\x1b]7501;") || !strings.HasSuffix(seq, "\x1b\\") {
		t.Fatalf("unexpected OSC wrapper: %q", seq)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]7501;"), "\x1b\\")
	kv := map[string]string{}
	for _, pair := range strings.Split(body, ":") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			t.Fatalf("malformed pair %q", pair)
		}
		kv[k] = v
	}
	if kv["state"] != "blocked" || kv["id"] != "deploy/us-east" || kv["kind"] != "permission" || kv["progress"] != "42" || kv["app"] != "claude-code" {
		t.Fatalf("unexpected encoded pairs: %#v", kv)
	}
	decTitle, err := base64.StdEncoding.DecodeString(kv["title"])
	if err != nil || string(decTitle) != "US East Cluster" {
		t.Fatalf("unexpected decoded title: %q (%v)", decTitle, err)
	}
	decMsg, err := base64.StdEncoding.DecodeString(kv["msg"])
	if err != nil || string(decMsg) != "Approve deploy ?" {
		t.Fatalf("unexpected decoded msg: %q (%v)", decMsg, err)
	}
}

func TestEncodeValidation(t *testing.T) {
	if _, err := Encode(Report{State: "bogus"}); err == nil {
		t.Fatal("expected error for invalid state")
	}
	if _, err := Encode(Report{State: StateWorking, ID: "bad id"}); err == nil {
		t.Fatal("expected error for invalid id")
	}
	if _, err := Encode(Report{State: StateWorking, App: "has space"}); err == nil {
		t.Fatal("expected error for invalid app")
	}
	badProg := 101
	if _, err := Encode(Report{State: StateWorking, Progress: &badProg}); err == nil {
		t.Fatal("expected error for out-of-range progress")
	}
}

func TestClearOmitsOtherMetadata(t *testing.T) {
	var buf bytes.Buffer
	if err := EmitTo(&buf, Report{State: StateClear, ID: "build", App: "ignored", Msg: "ignored"}); err != nil {
		t.Fatalf("EmitTo failed: %v", err)
	}
	if buf.String() != "\x1b]7501;state=clear:id=build\x1b\\" {
		t.Fatalf("unexpected clear sequence: %q", buf.String())
	}
}
