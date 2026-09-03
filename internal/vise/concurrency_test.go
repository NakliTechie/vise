package vise

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The concurrency story is written down — the state lock serialises the
// commands that write, the lockfile is replaced by atomic rename so a reader
// sees one generation or the other, status and doctor take no lock — and
// nothing exercised it. Two agents in two worktrees on one repository is the
// case vise is built for, so it is worth knowing what actually happens.
func TestConcurrentGatesAgreeAndLeaveTheStateIntact(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n[[probe]]\nid = \"q\"\nrun = \"printf q\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	before, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}

	// Eight gates at once. Every one must agree, because they are judging the
	// same unchanged tree — a disagreement here is the tool inventing a
	// verdict out of its own concurrency.
	const runners = 8
	var wait sync.WaitGroup
	outcomes := make([]Outcome, runners)
	for i := 0; i < runners; i++ {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			outcomes[slot] = Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
		}(i)
	}
	wait.Wait()

	for i, outcome := range outcomes {
		if outcome.Exit != ExitOK {
			t.Fatalf("gate %d of %d returned %d: %#v", i+1, runners, outcome.Exit, outcome)
		}
		if outcome.Counts.Pass != 2 || outcome.Counts.Declared != 2 {
			t.Fatalf("gate %d counted %#v", i+1, outcome.Counts)
		}
	}

	// The baseline is untouched: verify judges, it does not write.
	after, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("concurrent gates changed the lockfile")
	}

	// And the journal is still readable: every append went through whole, or
	// the torn-tail handling recovered it.
	if _, err := ReadJournal(root, 100); err != nil {
		t.Fatalf("concurrent gates left an unreadable journal: %v", err)
	}
}

// status takes no lock, so it runs while a record holds one. This asserts that
// it never exits nonzero and never invents a broken baseline while a record
// churns underneath it.
//
// What it does NOT prove is that the torn-read retry works: removing the retry
// leaves this green, because the window is narrow and the test does not
// reliably land in it. The retry has its own test below, which does.
func TestStatusStaysCoherentWhileARecordRuns(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "counter.sh", "#!/bin/sh\nprintf steady\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"sh counter.sh\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		// Re-record repeatedly while status reads. Each one replaces the
		// lockfile and prunes blobs the previous generation referenced.
		for i := 0; i < 5; i++ {
			Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
		}
	}()

	var sawHarness string
	for i := 0; i < 40; i++ {
		report := BuildStatus(root)
		if report.Exit != ExitOK {
			t.Fatalf("status exited %d while a record was running", report.Exit)
		}
		if report.State == "harness-error" && strings.Contains(report.Lock.Error, "blob") {
			sawHarness = report.Lock.Error
		}
	}
	wait.Wait()

	if sawHarness != "" {
		t.Fatalf("status reported a broken baseline during a concurrent record: %s", sawHarness)
	}
}

// The torn read itself, without racing for it. status reads one generation of
// the lockfile and then reaches for the blobs it references; a record between
// those two steps prunes them. That is not a broken baseline and reporting one
// sends an agent to repair something that was never wrong.
func TestStatusTreatsATornReadAsARetryAndNotADisaster(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	// Bytes from a generation that is no longer on disk: exactly what a status
	// holds when a record has replaced the lockfile since it read it.
	stale := []byte(`{"v":1,"probes":{"p":{"run_hash":"sha256:` + strings.Repeat("a", 64) +
		`","recorded_commit":"` + strings.Repeat("b", 40) +
		`","exit":0,"stdout":"sha256:` + strings.Repeat("c", 64) +
		`","stderr":"sha256:` + strings.Repeat("d", 64) + `"}}}`)

	report := StatusReport{V: 1, Cmd: "status", State: "ready"}
	torn := buildTamperHash(root, manifestBytes, stale, &report)
	if !torn {
		t.Fatal("a lockfile that no longer matches the one on disk was not treated as a torn read")
	}
	if report.State == "harness-error" {
		t.Fatalf("a torn read was reported as a broken baseline: %s", report.Lock.Error)
	}

	// And a genuinely broken baseline is still reported: the bytes on disk are
	// the bytes being hashed, so nothing was torn, and the blobs really are
	// missing.
	if err := os.RemoveAll(filepath.Join(root, ".vise", "blobs")); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	report = StatusReport{V: 1, Cmd: "status", State: "ready"}
	if torn := buildTamperHash(root, manifestBytes, current, &report); torn {
		t.Fatal("a baseline with its blobs deleted was excused as a torn read")
	}
	if report.State != "harness-error" {
		t.Fatalf("missing blobs were not reported: %#v", report)
	}
}
