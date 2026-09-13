package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NakliTechie/vise/internal/vise"
)

// These are focused executable assertions of PROTOCOL.md, not independent
// producer conformance: they invoke the CLI dispatcher within the Go process.
func protocolReply(t *testing.T, root string, wantExit int, wantCmd, wantAction string, args ...string) map[string]any {
	t.Helper()
	exit, stdout, stderr := cliRun(t, root, append(args, "--json")...)
	if exit != wantExit || stderr != "" || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, exit, stdout, stderr)
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var reply map[string]any
	if err := decoder.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("%v: extra JSON frame: %v (%#v)", args, err, extra)
	}
	if reply["v"] != float64(1) || reply["exit"] != float64(wantExit) || reply["cmd"] != wantCmd {
		t.Fatalf("%v: wrong envelope: %#v", args, reply)
	}
	next, ok := reply["next"].(map[string]any)
	if !ok || next["action"] != wantAction {
		t.Fatalf("%v: next=%#v, want %s", args, reply["next"], wantAction)
	}
	if _, ok := next["detail"].(string); !ok {
		t.Fatalf("next detail is not a string: %#v", next)
	}
	return reply
}

func protocolAbsent(t *testing.T, reply map[string]any, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if value, present := reply[field]; present {
			t.Errorf("%s should be absent, got %#v", field, value)
		}
	}
}

func TestProtocolReportAndInvocationShapes(t *testing.T) {
	outside := t.TempDir()
	version := protocolReply(t, outside, 0, "version", "proceed", "version")
	protocolAbsent(t, version, "counts", "verdict", "failures")
	if _, ok := version["version"].(string); !ok {
		t.Fatalf("version absent: %#v", version)
	}
	help := protocolReply(t, outside, 0, "help", "proceed", "help", "ignored")
	if len(help["commands"].(map[string]any)) != 8 {
		t.Fatalf("unexpected command inventory: %#v", help)
	}
	help = protocolReply(t, outside, 0, "help", "proceed", "gate", "--bad", "--help")
	if help["command"] != "gate" {
		t.Fatalf("command help did not precede parsing: %#v", help)
	}
	protocolAbsent(t, help, "commands", "counts", "verdict")
	status := protocolReply(t, outside, 0, "status", "human", "status")
	if status["state"] != "no-git" || status["tool"] == nil {
		t.Fatalf("no-git status: %#v", status)
	}
	protocolAbsent(t, status, "verdict", "counts", "journal")
	doctor := protocolReply(t, outside, 0, "doctor", "human", "doctor")
	if doctor["ready"] != false || len(doctor["findings"].([]any)) != 1 {
		t.Fatalf("no-git doctor: %#v", doctor)
	}
	for _, tc := range []struct {
		args []string
		cmd  string
	}{
		{[]string{"status", "bogus"}, "status"},
		{[]string{"doctor", "bogus"}, "doctor"},
		{[]string{"no-such-command", "--help"}, "vise"},
		{[]string{"version", "bogus"}, "version"},
	} {
		reply := protocolReply(t, outside, 2, tc.cmd, "fix_invocation", tc.args...)
		if reply["verdict"] != "indeterminate" {
			t.Fatalf("invocation error is not an outcome: %#v", reply)
		}
		protocolAbsent(t, reply, "state", "ready", "lock")
	}
}

func TestProtocolPreflightCountsAreNotExecutionEvidence(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nexit 0\n")
	reply := protocolReply(t, root, 4, "gate", "record_first", "gate")
	counts := reply["counts"].(map[string]any)
	// Deliberately records the current anomaly, not the desired future rule.
	// A consumer must branch on exit/action before interpreting these counts.
	if counts["declared"] != float64(1) || counts["pass"] != float64(1) {
		t.Fatalf("update PROTOCOL.md's observed preflight exception: %#v", counts)
	}
	protocolAbsent(t, reply, "lock", "failures", "classes", "pins")
	if _, err := os.Stat(filepath.Join(root, ".vise", "journal.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("preflight journaled a judgment: %v", err)
	}
	protocolReply(t, root, 2, "gate", "fix_invocation", "gate", "--probe", "unknown")
}

func TestProtocolGreenAndRawRunAreDistinct(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf '\\377'\nprintf message >&2\nexit 6\n")
	protocolReply(t, root, 0, "record", "proceed", "record")
	gate := protocolReply(t, root, 0, "gate", "proceed", "gate")
	if gate["verdict"] != "green" || gate["lock"] == nil {
		t.Fatalf("gate: %#v", gate)
	}
	protocolAbsent(t, gate, "classes", "failures", "metrics", "pins")
	counts := gate["counts"].(map[string]any)
	for _, key := range []string{"declared", "pass", "behavior", "flaky", "harness", "metric", "unmet", "skipped"} {
		if _, ok := counts[key].(float64); !ok {
			t.Errorf("count %q missing/non-numeric: %#v", key, counts)
		}
	}
	raw := protocolReply(t, root, 6, "run", "proceed", "run", "behavior")
	protocolAbsent(t, raw, "counts", "verdict", "lock", "stdout")
	if raw["stdout_base64"] != base64.StdEncoding.EncodeToString([]byte{255}) || raw["stdout_size"] != float64(1) || raw["stdout_hash"] != vise.HashBytes([]byte{255}) || raw["stdout_truncated"] != false {
		t.Fatalf("binary raw capture: %#v", raw)
	}
	if raw["stderr"] != "message" || len(raw["files"].(map[string]any)) != 0 {
		t.Fatalf("raw streams/files: %#v", raw)
	}
	protocolReply(t, root, 0, "gate", "proceed", "gate", "--probe", "unknown", "--probe", "")
	protocolReply(t, root, 0, "verify", "proceed", "verify", "--quiet=false")
	protocolReply(t, root, 2, "verify", "fix_invocation", "verify", "--quiet")
}

func TestProtocolPinSummaryIsNotCompleteIDSet(t *testing.T) {
	manifest := "[vise]\nversion = 1\n"
	for i := 0; i < 4; i++ {
		manifest += fmt.Sprintf("\n[[probe]]\nid = 'p%d'\nrun = './probe.sh'\n[probe.expect]\nexit = 42\n", i)
	}
	manifest += "\n[[metric]]\nid = 'quality'\nrun = 'printf 10'\ndirection = 'down'\n"
	root := cliRepo(t, manifest, "#!/bin/sh\nexit 0\n")
	record := protocolReply(t, root, 0, "record", "proceed", "record")
	if record["verdict"] != "green" || record["counts"].(map[string]any)["unmet"] != float64(4) {
		t.Fatalf("record success was confused with pin success: %#v", record)
	}
	if len(record["pins"].(map[string]any)["unmet"].([]any)) != 4 {
		t.Fatalf("record pin list is not complete: %#v", record)
	}
	gate := protocolReply(t, root, 6, "gate", "build", "gate")
	pins := gate["pins"].(map[string]any)
	if pins["unmet_count"] != float64(4) || len(pins["unmet"].([]any)) != 3 || len(gate["failures"].(map[string]any)) != 4 || len(pins["passing_unaccepted"].([]any)) != 0 {
		t.Fatalf("bounded summary vs full failures: %#v", gate)
	}
	if gate["counts"].(map[string]any)["skipped"] != float64(1) {
		t.Fatalf("metrics not reported skipped: %#v", gate)
	}
	protocolAbsent(t, gate, "metrics")
	cliWrite(t, root, "probe.sh", "#!/bin/sh\nexit 42\n")
	gate = protocolReply(t, root, 0, "gate", "proceed", "gate")
	pins = gate["pins"].(map[string]any)
	if pins["passing_unaccepted_count"] != float64(4) || len(pins["passing_unaccepted"].([]any)) != 3 || len(pins["unmet"].([]any)) != 0 {
		t.Fatalf("passing unaccepted shape: %#v", gate)
	}
	status := protocolReply(t, root, 0, "status", "proceed", "status")
	stored := status["lock"].(map[string]any)["pins"].(map[string]any)
	if stored["accepted"] != float64(0) || stored["unaccepted_count"] != float64(4) {
		t.Fatalf("gate silently accepted pins: %#v", status)
	}
}

func TestProtocolCaptureBoundAndEncodingException(t *testing.T) {
	// A retained prefix can cut through a multibyte rune. Base64 must preserve
	// those bytes, and its digest/size describe the full stream, not the prefix.
	data := append(bytes.Repeat([]byte{'x'}, vise.CaptureLimit-1), []byte("€")...)
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\ncat input.bin\n")
	if err := os.WriteFile(filepath.Join(root, "input.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	raw := protocolReply(t, root, 0, "run", "proceed", "run", "behavior")
	protocolAbsent(t, raw, "stdout")
	prefix, err := base64.StdEncoding.DecodeString(raw["stdout_base64"].(string))
	if err != nil || !bytes.Equal(prefix, data[:vise.CaptureLimit]) || raw["stdout_hash"] != vise.HashBytes(data) || raw["stdout_size"] != float64(len(data)) || raw["stdout_truncated"] != true {
		t.Fatalf("capture bound/encoding mismatch: %#v (%v)", raw, err)
	}
}
