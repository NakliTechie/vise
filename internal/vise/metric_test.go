package vise

import (
	"os"
	"path/filepath"
	"testing"
)

// A metric's direction decides which way is worse, and that is the whole
// difference between a quality gate and a random one. Inverting the "up" case
// left the suite green, because every metric fixture in it counted downwards.
//
// The table covers both directions and both enforcement settings, so no single
// branch of the regression rule can be removed without a failure here.
func TestMetricRegressionDependsOnDirectionAndEnforcement(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		enforce   string
		base      float64
		now       float64
		regressed bool
	}{
		{"lower is better and it rose", "down", "no-regress", 10, 12, true},
		{"lower is better and it fell", "down", "no-regress", 10, 8, false},
		{"lower is better and it held", "down", "no-regress", 10, 10, false},
		{"higher is better and it fell", "up", "no-regress", 10, 8, true},
		{"higher is better and it rose", "up", "no-regress", 10, 12, false},
		{"higher is better and it held", "up", "no-regress", 10, 10, false},
		{"tracked only, moving the wrong way", "down", "none", 10, 12, false},
		{"tracked only, higher is better", "up", "none", 10, 8, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := metricRegressed(test.direction, test.enforce, test.base, test.now)
			if got != test.regressed {
				t.Fatalf("regressed = %t, want %t for %s %s %v -> %v",
					got, test.regressed, test.direction, test.enforce, test.base, test.now)
			}
		})
	}
}

// Quality is only asked about once behaviour has held. The verdict already
// ordered it that way, but the metrics were still being run — so an analyzer
// executed against a tree whose behaviour had changed, its number was compared
// to a baseline recorded against different behaviour, and the agent was handed
// a quality figure describing something it was about to revert.
func TestMetricsAreNotEvaluatedOnceBehaviorHasFailed(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "tool.sh", "#!/bin/sh\nprintf original\n")
	writeTestFile(t, root, "count.txt", "10")
	// The metric writes a marker when it runs, so the test can tell whether it
	// ran rather than inferring it from the verdict.
	writeTestFile(t, root, "metric.sh", "#!/bin/sh\nprintf ran > \"$VISE_TMP/../metric-ran\"\ncat count.txt\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n"+
		"[[probe]]\nid = \"p\"\nrun = \"sh tool.sh\"\n"+
		"[[metric]]\nid = \"m\"\nrun = \"sh metric.sh\"\ndirection = \"down\"\nenforce = \"no-regress\"\nversion_cmd = \"printf analyzer-1\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	marker := filepath.Join(root, ".vise", "metric-ran")
	_ = os.Remove(marker)
	// Behaviour moves. The metric must not be consulted at all.
	writeTestFile(t, root, "tool.sh", "#!/bin/sh\nprintf changed\n")
	outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome

	if outcome.Exit != ExitBehavior {
		t.Fatalf("exit = %d, want behavior", outcome.Exit)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the metric ran although behavior had already failed")
	}
	if len(outcome.Metrics) != 0 {
		t.Fatalf("a quality figure was reported beside a behavior failure: %#v", outcome.Metrics)
	}
}
