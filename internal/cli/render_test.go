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
