package vise

import (
	"fmt"
	"strings"
	"testing"
)

// A probe may declare several artifacts, and the comparison walks them in a
// map. Nothing checked that it walks all of them: a version that compared one
// and stopped passed the entire suite. Every existing test with artifacts had
// exactly one, which is the fixture shape that hides a loop-over-first — the
// fourth time that shape has hidden a mutation in this project.
//
// Six artifacts, one differing at a time. Go randomises map order, so a
// compare-one-and-stop version escapes a given case only when it happens to
// pick the differing key: one time in six, and it has to escape all six.
func TestEveryDeclaredArtifactIsCompared(t *testing.T) {
	const count = 6
	expected := ProbeLock{Exit: 0, Stdout: "sha256:out", Stderr: "sha256:err", Files: map[string]string{}}
	for i := 0; i < count; i++ {
		expected.Files[fmt.Sprintf("out/%d.txt", i)] = fmt.Sprintf("sha256:artifact-%d", i)
	}

	matching := func() RunResult {
		run := RunResult{Exit: 0, Stdout: Capture{Hash: "sha256:out"}, Stderr: Capture{Hash: "sha256:err"}, Files: map[string]Capture{}}
		for path, hash := range expected.Files {
			run.Files[path] = Capture{Hash: hash}
		}
		return run
	}
	if !RunMatchesLock(matching(), expected) {
		t.Fatal("an identical run did not match its own lock")
	}

	for i := 0; i < count; i++ {
		path := fmt.Sprintf("out/%d.txt", i)
		t.Run(path, func(t *testing.T) {
			run := matching()
			run.Files[path] = Capture{Hash: "sha256:changed"}
			if RunMatchesLock(run, expected) {
				t.Errorf("%s changed and the run still matched the lock", path)
			}
		})
	}
}

// SPEC is emphatic about this: metrics are evaluated only when behavior held,
// "not merely ranked below behavior in the verdict — not run at all". Running
// an analyzer against a tree whose behavior has already moved produces a number
// compared against a baseline recorded under different behavior, handed to an
// agent that is about to revert the change the number describes.
//
// Behavior "held" has three ways to fail and only one of them was tested. A
// flake means behavior is *unknown*, which is not the same as unchanged, and a
// single-probe verify establishes nothing about the probes it did not run.
// Removing either guard passed the whole suite.
func TestMetricsAreNotEvaluatedUnlessBehaviorHeld(t *testing.T) {
	// A probe that alternates on a gitignored counter, so the suite can be made
	// to flake without touching anything vise watches.
	const manifestBody = `[vise]
version = 1

[[probe]]
id = "steady"
run = "printf steady"
timeout = 30

[[probe]]
id = "swing"
run = "sh ./swing.sh"
timeout = 30

[[metric]]
id = "size"
run = "printf 10"
version_cmd = "printf analyzer-1"
direction = "down"
enforce = "no-regress"
timeout = 30
`
	newRepo := func(t *testing.T, swing string) (string, Manifest, []byte) {
		t.Helper()
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.toggle\nswing.sh\n")
		writeTestFile(t, root, "vise.toml", manifestBody)
		writeTestFile(t, root, "swing.sh", "printf steady\n")
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "manifest")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
			t.Fatalf("record: %#v", result.Outcome)
		}
		// swing.sh is neither declared nor tracked, so replacing it after the
		// baseline is invisible to vise and changes only what the probe prints.
		writeTestFile(t, root, "swing.sh", swing)
		return root, manifest, manifestBytes
	}

	t.Run("a flake leaves behavior unknown, so no metric runs", func(t *testing.T) {
		root, manifest, manifestBytes := newRepo(t,
			"n=$(cat .toggle 2>/dev/null || echo 0); n=$((n+1)); echo $n > .toggle; "+
				"if [ $((n % 2)) -eq 0 ]; then printf steady; else printf drifted; fi\n")
		outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
		if outcome.Counts.Flaky == 0 {
			t.Fatalf("the probe did not flake, so this proves nothing: %#v", outcome.Counts)
		}
		if len(outcome.Metrics) != 0 {
			t.Errorf("a metric was evaluated while behavior was unknown: %#v", outcome.Metrics)
		}
	})

	t.Run("a single-probe verify establishes nothing, so no metric runs", func(t *testing.T) {
		root, manifest, manifestBytes := newRepo(t, "printf steady\n")
		outcome := Verify(root, manifest, manifestBytes, VerifyOptions{ProbeID: "steady"}).Outcome
		if outcome.Exit != ExitOK {
			t.Fatalf("the narrowed verify did not pass, so this proves nothing: %#v", outcome)
		}
		if len(outcome.Metrics) != 0 {
			t.Errorf("a metric was evaluated on a verify that ran one probe: %#v", outcome.Metrics)
		}
	})
}

// DiffRuns explains a divergence, and it used to walk the baseline's artifact
// list alone. Its two sibling renderers walk the union. An artifact on only one
// side fell through to the bare string "observation differs", which names
// nothing an operator could act on.
//
// The case is unreachable through the CLI today — the manifest's `files` list
// is part of the probe definition, so adding or removing one is caught as
// definition drift three functions earlier. That guard is what makes this safe,
// not this function, and a guard three functions away is not a thing to rely on
// for the quality of an error message.
func TestTheExplanationNamesAnArtifactOnEitherSide(t *testing.T) {
	root := testGitRepo(t)

	t.Run("the run produced one the baseline has no record of", func(t *testing.T) {
		expected := ProbeLock{Exit: 0, Stdout: "sha256:out", Stderr: "sha256:err", Files: map[string]string{}}
		got := RunResult{
			Exit:   0,
			Stdout: Capture{Hash: "sha256:out"},
			Stderr: Capture{Hash: "sha256:err"},
			Files:  map[string]Capture{"out/surprise.txt": {Hash: "sha256:new"}},
		}
		detail := DiffRuns(root, expected, got)
		if !strings.Contains(detail, "out/surprise.txt") {
			t.Errorf("the explanation does not name the artifact: %q", detail)
		}
	})

	t.Run("the baseline records one the run did not produce", func(t *testing.T) {
		expected := ProbeLock{
			Exit: 0, Stdout: "sha256:out", Stderr: "sha256:err",
			Files: map[string]string{"out/expected.txt": "sha256:old"},
		}
		got := RunResult{
			Exit:   0,
			Stdout: Capture{Hash: "sha256:out"},
			Stderr: Capture{Hash: "sha256:err"},
			Files:  map[string]Capture{},
		}
		detail := DiffRuns(root, expected, got)
		if !strings.Contains(detail, "out/expected.txt") {
			t.Errorf("the explanation does not name the artifact: %q", detail)
		}
	})
}

// A metric that will not run has to reach the operator as a harness failure
// with its own id, its own message, and the right owner — and the conversion
// that does that was the last function in either package with no coverage.
//
// Everything about metric failures was tested at the runner, where the message
// is built, and nothing carried one through Verify, where it becomes a verdict.
// That is the seam the eight dropped-ownership bugs lived in earlier tonight.
func TestAMetricThatWillNotRunReachesTheVerdict(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", `[vise]
version = 1

[[probe]]
id = "steady"
run = "printf steady"
timeout = 30

[[metric]]
id = "size"
run = "cat size.txt"
version_cmd = "printf analyzer-1"
direction = "down"
enforce = "no-regress"
timeout = 30
`)
	writeTestFile(t, root, "size.txt", "10\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "harness")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	// The analyzer's input goes away, the way a refactor moves a file.
	testGit(t, root, "rm", "-q", "size.txt")
	testGit(t, root, "commit", "-qm", "the analyzer's input moved")

	outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
	failure, ok := outcome.Failures["size"]
	if !ok {
		t.Fatalf("no failure keyed by the metric: %#v", outcome.Failures)
	}
	if failure.Class != "harness" {
		t.Errorf("class %q, want harness", failure.Class)
	}
	if !strings.Contains(failure.Detail, "size.txt") {
		t.Errorf("the message does not name what went missing:\n\t%s", failure.Detail)
	}
	if outcome.Exit != ExitHarness {
		t.Errorf("exit %d, want %d", outcome.Exit, ExitHarness)
	}
	if outcome.Counts.Harness != 1 {
		t.Errorf("counts = %#v, want one harness failure", outcome.Counts)
	}
}
