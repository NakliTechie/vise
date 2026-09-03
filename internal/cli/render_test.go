package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NakliTechie/vise/internal/vise"
)

func TestTerminalSafeEscapesControlsAndPreservesLines(t *testing.T) {
	got := terminalSafe("line1\n\x1b[31mline2", true)
	if got != "line1\n\\u001b[31mline2" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderStatusIncludesFlakesAndMetrics(t *testing.T) {
	report := vise.StatusReport{
		State:    "ready",
		Manifest: vise.StatusManifest{Present: true, Valid: true, Probes: 1},
		Lock:     vise.StatusLock{Present: true, Valid: true, Probes: 1},
		Journal:  []vise.JournalEvent{{Event: "flake", Commit: "abc", Verdict: "indeterminate", Flaky: []string{"p"}, Metrics: map[string]float64{"complexity": 12}}},
		Next:     vise.Next{Action: "human", Detail: "review"},
	}
	var output bytes.Buffer
	renderStatus(&output, report)
	text := output.String()
	if !strings.Contains(text, "flaky=p") || !strings.Contains(text, "metrics=complexity=12") {
		t.Fatalf("status = %q", text)
	}
}

func TestRenderStatusBoundsDriftLines(t *testing.T) {
	drift := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		drift = append(drift, "p"+string(rune('0'+i))+": probe is declared but absent from vise.lock")
	}
	report := vise.StatusReport{State: "baseline-drift", Lock: vise.StatusLock{Present: true, Valid: true, Drift: drift}}
	var output bytes.Buffer
	renderStatus(&output, report)
	text := output.String()
	if strings.Count(text, "drift: ") != 6 || !strings.Contains(text, "drift: … 3 more (see --json)") || strings.Contains(text, "p5:") {
		t.Fatalf("status = %q", text)
	}
}

func TestRenderStatusEscapesJournalAndLockFields(t *testing.T) {
	report := vise.StatusReport{
		State:   "ready",
		Lock:    vise.StatusLock{Present: true, Valid: true, Hash: "sha256:\x1b[2Jabc", RecordedCommits: []string{"\x1b[31mred"}},
		Journal: []vise.JournalEvent{{Event: "gate", Commit: "\x1b[2J", Verdict: "green"}},
	}
	var output bytes.Buffer
	renderStatus(&output, report)
	if strings.Contains(output.String(), "\x1b") {
		t.Fatalf("control byte reached the terminal: %q", output.String())
	}
}

// terminalSafe is what stops a probe's output from driving the terminal it is
// printed to. The test covered ESC and nothing else, so allowing every other
// control through — carriage returns that erase the line above, backspaces
// that rewrite what a reviewer just read, bells — left the suite green.
func TestTerminalSafeEscapesEveryControlCharacter(t *testing.T) {
	// Tab and newline are the two deliberate exceptions, and newline only
	// where the field is allowed to span lines.
	for r := rune(0); r < 0x20; r++ {
		if r == '\t' || r == '\n' {
			continue
		}
		got := terminalSafe(string(r), true)
		if got == string(r) {
			t.Errorf("control %U passed through unescaped", r)
		}
	}
	for _, r := range []rune{0x7f, 0x80, 0x9b, 0x2028, 0x2029} {
		if got := terminalSafe(string(r), true); got == string(r) {
			t.Errorf("control %U passed through unescaped", r)
		}
	}

	// Tab survives, because output that uses it to align is readable and a tab
	// cannot move the cursor anywhere a reviewer is not looking.
	if got := terminalSafe("a\tb", false); got != "a\tb" {
		t.Errorf("tab was escaped: %q", got)
	}

	// Newline survives only where the caller says the field may span lines. A
	// one-line field that lets a newline through can print a whole fake report
	// under a real one.
	if got := terminalSafe("a\nb", true); got != "a\nb" {
		t.Errorf("newline was escaped in a multi-line field: %q", got)
	}
	if got := terminalSafe("a\nb", false); got == "a\nb" {
		t.Error("newline passed through a single-line field")
	}
}

// And the escaping has to be applied where output actually reaches a terminal,
// not only where a unit test calls the helper directly.
func TestHumanRenderingsEscapeProbeOutput(t *testing.T) {
	hostile := "before\rerased\x1b[2Kafter"

	var status bytes.Buffer
	renderStatus(&status, vise.StatusReport{
		V: 1, Cmd: "status", State: "harness-error",
		Manifest: vise.StatusManifest{Present: true, Valid: false, Error: hostile},
		Next:     vise.Next{Action: "human", Detail: hostile},
	})
	if strings.Contains(status.String(), "\x1b") || strings.Contains(status.String(), "\r") {
		t.Errorf("status passed a control character through:\n%q", status.String())
	}

	var doctor bytes.Buffer
	renderDoctor(&doctor, vise.DoctorReport{
		V: 1, Cmd: "doctor",
		Findings: []vise.DoctorFinding{{Check: "manifest", Detail: hostile, Remedy: hostile}},
		Next:     vise.Next{Action: "human", Detail: hostile},
	})
	if strings.Contains(doctor.String(), "\x1b") || strings.Contains(doctor.String(), "\r") {
		t.Errorf("doctor passed a control character through:\n%q", doctor.String())
	}
}
