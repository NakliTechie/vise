package vise

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteGenerationRoundTripAndPrune(t *testing.T) {
	root := t.TempDir()
	keepData := []byte("keep")
	oldData := []byte("old")
	keepHash := HashBytes(keepData)
	oldHash := HashBytes(oldData)
	runHash := HashBytes([]byte("run"))
	if err := WriteBlobs(root, map[string][]byte{keepHash: keepData, oldHash: oldData}); err != nil {
		t.Fatal(err)
	}
	lock := Lockfile{V: 1, Fingerprint: Fingerprint{OS: "test", Arch: "test"}, Probes: map[string]ProbeLock{
		"p": {RunHash: runHash, RecordedCommit: "3319316e4a7a5f1fb2e80de6f001a1355269464a", Stdout: keepHash, Stderr: HashBytes(nil)},
	}}
	data, err := WriteGeneration(root, lock, map[string][]byte{keepHash: keepData, HashBytes(nil): nil})
	if err != nil {
		t.Fatal(err)
	}
	got, gotData, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Probes["p"].Stdout != keepHash || string(data) != string(gotData) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	oldPath, err := BlobPath(root, oldHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old blob not pruned: %v", err)
	}
}

func TestTamperHashIgnoresOrphanBlobsAndChecksReferencedContent(t *testing.T) {
	root := t.TempDir()
	data := []byte("expected")
	hash := HashBytes(data)
	runHash := HashBytes([]byte("run"))
	lock := Lockfile{V: 1, Fingerprint: Fingerprint{OS: "test", Arch: "test"}, Probes: map[string]ProbeLock{
		"p": {RunHash: runHash, RecordedCommit: "3319316e4a7a5f1fb2e80de6f001a1355269464a", Stdout: hash, Stderr: hash},
	}}
	lockBytes, err := CanonicalJSON(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBlobs(root, map[string][]byte{hash: data, HashBytes([]byte("orphan")): []byte("orphan")}); err != nil {
		t.Fatal(err)
	}
	first, err := TamperHash(root, []byte("manifest"), lockBytes)
	if err != nil {
		t.Fatal(err)
	}
	orphanPath, err := BlobPath(root, HashBytes([]byte("another orphan")))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanPath, []byte("another orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := TamperHash(root, []byte("manifest"), lockBytes)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("orphan changed hash: %s != %s", first, second)
	}
	hashPath, err := BlobPath(root, hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hashPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TamperHash(root, []byte("manifest"), lockBytes); err == nil {
		t.Fatal("expected corrupt blob rejection")
	}
}

func TestLockfileRejectsPathShapedHashes(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "vise.lock", `{
  "v": 1,
  "fingerprint": {"os": "test", "arch": "test"},
  "probes": {
    "p": {
      "run_hash": "sha256:../../vise.toml",
      "recorded_commit": "abc",
      "exit": 0,
      "stdout": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "stderr": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    }
  }
}`)
	if _, _, err := LoadLockfile(root); err == nil || !strings.Contains(err.Error(), "invalid sha256 hash") {
		t.Fatalf("error = %v", err)
	}
}

func TestFingerprintRejectsTrackedMutation(t *testing.T) {
	root := testGitRepo(t)
	manifest := testManifest(Probe{ID: "p", Run: "true", Timeout: 5})
	manifest.Environment.Fingerprint = []string{"printf changed > tracked.txt; printf tool"}
	if _, err := CaptureFingerprint(root, manifest); err == nil || !strings.Contains(err.Error(), "modified tracked files") {
		t.Fatalf("error = %v", err)
	}
}

func TestLargeObservationIsHashOnly(t *testing.T) {
	blobs := make(map[string][]byte)
	data := make([]byte, MaxBlobSize+1)
	entry := AddObservationBlobs(blobs, RunResult{Stdout: CaptureBytes(data), Stderr: CaptureBytes(nil)})
	if !entry.StdoutLarge {
		t.Fatal("large marker missing")
	}
	if _, ok := blobs[entry.Stdout]; ok {
		t.Fatal("large stdout stored as a blob")
	}
}

func TestJournalTailAndConsecutiveFlakes(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 7; i++ {
		event := JournalEvent{Event: "gate", Commit: "c", Lock: "l"}
		if i >= 5 {
			event.Event = "flake"
			event.Probes = []string{"p"}
		}
		if err := AppendJournal(root, event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := ReadJournal(root, 5)
	if err != nil {
		t.Fatal(err)
	}
	count, bounded := ConsecutiveFlakes(events, "c", "l", []string{"p"})
	if len(events) != 5 || count != 2 || bounded {
		t.Fatalf("events = %#v count=%d bounded=%t", events, count, bounded)
	}
}

func TestConsecutiveFlakesIgnoresTransparentEvents(t *testing.T) {
	flake := func(probes ...string) JournalEvent {
		return JournalEvent{Event: "flake", Commit: "c", Lock: "l", Verdict: "indeterminate", Probes: probes}
	}
	tests := []struct {
		name    string
		events  []JournalEvent
		want    int
		bounded bool
	}{
		{"refusal between flakes is transparent", []JournalEvent{flake("p"), flake("p"), {Event: "gate", Commit: "c", Lock: "l", Verdict: "indeterminate"}}, 2, false},
		{"other probe set is transparent", []JournalEvent{flake("p"), flake("p"), flake("q")}, 2, false},
		{"green verdict resets", []JournalEvent{flake("p"), {Event: "gate", Commit: "c", Lock: "l", Verdict: "green"}, flake("p")}, 1, true},
		{"red verdict resets", []JournalEvent{flake("p"), {Event: "verify", Commit: "c", Lock: "l", Verdict: "red"}}, 0, true},
		{"record at the same lock is a boundary", []JournalEvent{flake("p"), {Event: "record", Commit: "c", Lock: "l"}, flake("p")}, 1, true},
		{"other commit ends the scan", []JournalEvent{flake("p"), {Event: "flake", Commit: "d", Lock: "l", Probes: []string{"p"}}, flake("p")}, 1, true},
		{"other lock ends the scan", []JournalEvent{flake("p"), {Event: "record", Commit: "c", Lock: "m"}, flake("p")}, 1, true},
		{"empty journal is unbounded with zero flakes", nil, 0, false},
		{"lock-less event is transparent", []JournalEvent{flake("p"), flake("p"), {Event: "verify", Commit: "c", Verdict: "indeterminate"}}, 2, false},
		{"green verdict for another probe set is transparent", []JournalEvent{flake("p"), {Event: "gate", Commit: "c", Lock: "l", Verdict: "green", Probes: []string{"q"}}, flake("p")}, 2, false},
		{"green verdict for a superset is a boundary", []JournalEvent{flake("p"), {Event: "verify", Commit: "c", Lock: "l", Verdict: "green", Probes: []string{"p", "q"}}, flake("p")}, 1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, bounded := ConsecutiveFlakes(test.events, "c", "l", []string{"p"})
			if got != test.want || bounded != test.bounded {
				t.Fatalf("got %d bounded=%t, want %d bounded=%t", got, bounded, test.want, test.bounded)
			}
		})
	}
}

func TestVerifyRefusesWhenTruncatedJournalHasNoBoundary(t *testing.T) {
	root := t.TempDir()
	// Fill more than the 256 KiB scan window with unjudged single-probe flakes
	// at one commit and lock, so no chain boundary survives inside the tail.
	line, err := json.Marshal(JournalEvent{Event: "flake", At: "2026-01-01T00:00:00Z", Commit: "c", Lock: "l", Verdict: "indeterminate", Flaky: []string{"q"}, Probes: []string{"q"}})
	if err != nil {
		t.Fatal(err)
	}
	journal := bytes.Repeat(append(line, '\n'), 2200)
	if err := os.MkdirAll(filepath.Join(root, ".vise"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".vise", "journal.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	events, truncated, err := readJournalTail(root)
	if err != nil {
		t.Fatal(err)
	}
	// The budget follows the probe: these are flakes of "q", and the run about
	// to happen includes "q", so they count. What makes the refusal here is
	// that nothing in the surviving tail bounds the chain — the count is
	// incidental, and used to be zero only because the old keying compared
	// probe sets exactly.
	count, bounded := ConsecutiveFlakes(events, "c", "l", []string{"p", "q"})
	if !truncated || bounded || count == 0 {
		t.Fatalf("truncated=%t bounded=%t count=%d", truncated, bounded, count)
	}
	// A run that does not include the flaky probe is not charged for it.
	if other, _ := ConsecutiveFlakes(events, "c", "l", []string{"p"}); other != 0 {
		t.Fatalf("flakes of q were charged to a run of p alone: %d", other)
	}
}

func TestReadJournalUsesBoundedTail(t *testing.T) {
	root := t.TempDir()
	padding := strings.Repeat("x", 300*1024)
	path := filepath.Join(root, ".vise", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"e\":\"old\",\"at\":\""+padding+"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendJournal(root, JournalEvent{Event: "gate", Commit: "new"}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadJournal(root, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Commit != "new" {
		t.Fatalf("events = %#v", events)
	}
}

func TestStatePathsRejectSymlinks(t *testing.T) {
	t.Run("state-directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, ".vise")); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireStateLock(root, nil); err == nil {
			t.Fatal("expected state-directory symlink rejection")
		}
		if _, err := os.Stat(filepath.Join(outside, "run.lock")); !os.IsNotExist(err) {
			t.Fatalf("outside lock created: %v", err)
		}
	})
	t.Run("journal", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".vise"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, ".vise", "journal.jsonl")); err != nil {
			t.Fatal(err)
		}
		if err := AppendJournal(root, JournalEvent{Event: "gate"}); err == nil {
			t.Fatal("expected journal symlink rejection")
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "unchanged" {
			t.Fatalf("target changed: %v %q", err, data)
		}
	})
}

func TestStateLockSerializesInvocations(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireStateLock(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() {
		second, err := AcquireStateLock(root, nil)
		if err == nil {
			err = second.Close()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		t.Fatalf("second invocation bypassed lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second invocation did not acquire released lock")
	}
}

func TestAtomicWriteFailurePreservesOldFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	err := atomicWrite(root, path, []byte("new"), 0o644)
	if restoreErr := os.Chmod(root, 0o700); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err == nil {
		t.Skip("filesystem allowed writes through a read-only directory")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old" {
		t.Fatalf("old state lost: %v %q", readErr, data)
	}
}

func TestOutcomePrecedence(t *testing.T) {
	outcome := NewOutcome("verify")
	outcome.Counts.Declared = 3
	outcome.AddFailure("behavior", Failure{Class: "behavior"})
	outcome.AddFailure("flake", Failure{Class: "flake"})
	outcome.AddFailure("harness", Failure{Class: "harness"})
	outcome.Finalize()
	if outcome.Exit != ExitHarness || outcome.Verdict != "indeterminate" || outcome.Next.Action != "fix_probe" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestFinalizeCountsEveryFailingCheckAgainstPass(t *testing.T) {
	// Two probes and two metrics declared: one metric regressed, one flaked.
	outcome := NewOutcome("verify")
	outcome.Counts.Declared = 4
	outcome.AddFailure("complexity", Failure{Class: "metric"})
	outcome.AddFailure("size", Failure{Class: "flake"})
	outcome.Finalize()
	if outcome.Counts.Pass != 2 || outcome.Counts.Metric != 1 || outcome.Counts.Flaky != 1 {
		t.Fatalf("counts = %#v", outcome.Counts)
	}
	if outcome.Exit != ExitIndeterminate {
		t.Fatalf("exit = %d, want flake precedence over metric", outcome.Exit)
	}
}

func TestOutcomeReplacesFailureWithoutDoubleCounting(t *testing.T) {
	outcome := NewOutcome("verify")
	outcome.Counts.Declared = 1
	outcome.AddFailure("probe", Failure{Class: "harness"})
	outcome.AddFailure("probe", Failure{Class: "behavior"})
	outcome.Finalize()
	if outcome.Counts.Harness != 0 || outcome.Counts.Behavior != 1 || outcome.Exit != ExitBehavior {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestReadJournalToleratesTornFinalLineOnly(t *testing.T) {
	root := t.TempDir()
	if err := AppendJournal(root, JournalEvent{Event: "gate", Commit: "c", Verdict: "green"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".vise", "journal.jsonl")
	if err := os.WriteFile(path, append(mustRead(t, path), []byte(`{"e":"ga`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := ReadJournal(root, 5)
	if err != nil || len(events) != 1 || events[0].Verdict != "green" {
		t.Fatalf("torn tail: events=%#v err=%v", events, err)
	}
	for i := 0; i < 2; i++ {
		if err := AppendJournal(root, JournalEvent{Event: "gate", Commit: "c", Verdict: "red"}); err != nil {
			t.Fatal(err)
		}
	}
	events, err = ReadJournal(root, 5)
	if err != nil || len(events) != 3 || events[2].Verdict != "red" {
		t.Fatalf("appends after a torn tail: events=%#v err=%v", events, err)
	}
	if err := os.WriteFile(path, []byte("{\"e\":\"ga\n{\"e\":\"gate\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournal(root, 5); err == nil {
		t.Fatal("a torn interior line must still fail")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAtomicWriteStagesUnderViseTmp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "vise.lock")
	if err := atomicWrite(root, path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "content" {
		t.Fatalf("written = %q, %v", data, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".vise-write-") {
			t.Fatalf("stray staging file beside the target: %s", entry.Name())
		}
	}
	if info, err := os.Stat(filepath.Join(root, ".vise", "tmp")); err != nil || !info.IsDir() {
		t.Fatalf("staging directory missing: %v", err)
	}
	leftovers, err := os.ReadDir(filepath.Join(root, ".vise", "tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("staging residue after a clean write: %v %v", leftovers, err)
	}
}

func TestAppendJournalKeepsCompleteUnterminatedFinalRecord(t *testing.T) {
	root := t.TempDir()
	if err := AppendJournal(root, JournalEvent{Event: "gate", Commit: "c", Verdict: "green"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".vise", "journal.jsonl")
	data := mustRead(t, path)
	if err := os.WriteFile(path, append(data, []byte(`{"e":"gate","commit":"c","verdict":"red"}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendJournal(root, JournalEvent{Event: "gate", Commit: "c", Verdict: "green"}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadJournal(root, 5)
	if err != nil || len(events) != 3 || events[1].Verdict != "red" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestReadJournalFailsOnNewlineTerminatedMalformedFinalLine(t *testing.T) {
	root := t.TempDir()
	if err := AppendJournal(root, JournalEvent{Event: "gate", Commit: "c", Verdict: "green"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".vise", "journal.jsonl")
	if err := os.WriteFile(path, append(mustRead(t, path), []byte("{\"e\":\"ga\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournal(root, 5); err == nil {
		t.Fatal("a newline-terminated malformed line is corruption and must fail")
	}
}

func TestWriteGenerationSurvivesPruneFailure(t *testing.T) {
	root := t.TempDir()
	data := []byte("keep")
	hash := HashBytes(data)
	lock := Lockfile{V: 1, Probes: map[string]ProbeLock{"p": {RunHash: HashBytes([]byte("r")), RecordedCommit: "3319316e4a7a5f1fb2e80de6f001a1355269464a", Stdout: hash, Stderr: hash}}}
	if err := WriteBlobs(root, map[string][]byte{HashBytes([]byte("orphan")): []byte("orphan")}); err != nil {
		t.Fatal(err)
	}
	blobDir := filepath.Join(root, ".vise", "blobs")
	if _, err := WriteGeneration(root, lock, map[string][]byte{hash: data}); err != nil {
		t.Fatal(err)
	}
	if err := WriteBlobs(root, map[string][]byte{HashBytes([]byte("orphan2")): []byte("orphan2")}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blobDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blobDir, 0o755)
	// The lock is written and the generation succeeds even though the orphan
	// cannot be pruned from a read-only directory.
	lock.Probes["p"] = ProbeLock{RunHash: HashBytes([]byte("r2")), RecordedCommit: "3319316e4a7a5f1fb2e80de6f001a1355269464a", Stdout: hash, Stderr: hash}
	if _, err := WriteGeneration(root, lock, map[string][]byte{hash: data}); err != nil {
		t.Fatalf("generation failed on prune: %v", err)
	}
	got, _, err := LoadLockfile(root)
	if err != nil || got.Probes["p"].RunHash != HashBytes([]byte("r2")) {
		t.Fatalf("lock not written: %#v %v", got, err)
	}
}

// The rerun budget follows the probe that flaked, not the set it flaked in.
// Keying on the exact set gave every subset its own two reruns: a flake seen
// in the full suite did not count against `verify --probe p`, and with N
// probes there were as many independent budgets as there are subsets. An agent
// diagnosing with --probe walked into a fresh one without meaning to.
func TestTheRerunBudgetFollowsTheProbeNotTheSet(t *testing.T) {
	flake := func(flaky ...string) JournalEvent {
		return JournalEvent{Event: "flake", At: "2026-01-01T00:00:00Z", Commit: "c", Lock: "l",
			Verdict: "indeterminate", Flaky: flaky, Probes: []string{"a", "b", "c"}}
	}
	boundary := JournalEvent{Event: "gate", At: "2026-01-01T00:00:00Z", Commit: "c", Lock: "l",
		Verdict: "green", Probes: []string{"a", "b", "c"}}
	events := []JournalEvent{boundary, flake("b"), flake("b")}

	tests := []struct {
		name string
		run  []string
		want int
	}{
		{"the full suite that flaked", []string{"a", "b", "c"}, 2},
		{"the flaky probe alone", []string{"b"}, 2},
		{"a pair containing it", []string{"a", "b"}, 2},
		{"a probe that did not flake", []string{"a"}, 0},
		{"a pair without it", []string{"a", "c"}, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _ := ConsecutiveFlakes(events, "c", "l", test.run)
			if got != test.want {
				t.Fatalf("count = %d, want %d for %v", got, test.want, test.run)
			}
		})
	}
}

// A lockfile this vise cannot read is a tooling problem with a one-line fix,
// and which fix depends on the direction. An unknown key already said so; an
// unsupported version said "vise.lock version 99 is unsupported" and stopped —
// four words, no remedy, for the coarser and likelier of the two signals.
func TestAnUnreadableLockfileVersionSaysWhichWayToGo(t *testing.T) {
	for _, c := range []struct {
		name    string
		version int
		wants   string
	}{
		{"written by a newer vise", LockVersion + 1, "upgrade this one"},
		{"written by an older vise", LockVersion - 1, "re-records it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := testGitRepo(t)
			body := fmt.Sprintf(`{"v":%d,"probes":{}}`, c.version)
			writeTestFile(t, root, "vise.lock", body)
			_, _, err := LoadLockfile(root)
			if err == nil {
				t.Fatal("a lockfile this vise cannot read was accepted")
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("the message does not say what to do (%q):\n\t%s", c.wants, err)
			}
		})
	}
}

// truncateTornTail repairs a journal whose last append was interrupted, and it
// reads a one-megabyte window to find where the last complete record ended. If
// that window holds no newline the record is longer than the window, and the
// offset it was about to cut at is an arbitrary byte inside some earlier
// record. Truncating there leaves a stump — precisely the malformed interior
// line the function's own comment promises never to create, and one that makes
// every later read of the journal fail.
//
// It must refuse instead. An unreadable journal is an operator's problem; an
// unreadable journal vise created while claiming to repair it is worse.
func TestARecordLongerThanTheRepairWindowIsRefusedNotCut(t *testing.T) {
	root := testGitRepo(t)
	path := filepath.Join(root, ".vise", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// One good record, then a torn one larger than the window.
	good := `{"e":"gate","at":"2026-09-03T00:00:00Z","commit":"abc","verdict":"green"}` + "\n"
	torn := "{" + strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(path, []byte(good+torn), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The repair runs on append, which is when vise would otherwise write past
	// the stump it had just created.
	err = AppendJournal(root, JournalEvent{Event: "gate", Commit: "def", Verdict: "green"})
	if err == nil {
		t.Fatal("a journal whose torn record exceeds the repair window was appended to anyway")
	}
	if !strings.Contains(err.Error(), "repair window") {
		t.Errorf("the error does not say what happened: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the journal was cut anyway: %d bytes became %d", len(before), len(after))
	}
}

// And the ordinary torn tail is still repaired, so the refusal above is not
// simply a refusal to do the job.
func TestAnOrdinaryTornTailIsStillTrimmed(t *testing.T) {
	root := testGitRepo(t)
	path := filepath.Join(root, ".vise", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	good := `{"e":"gate","at":"2026-09-03T00:00:00Z","commit":"abc","verdict":"green"}` + "\n"
	if err := os.WriteFile(path, []byte(good+`{"e":"ga`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendJournal(root, JournalEvent{Event: "gate", Commit: "def", Verdict: "green"}); err != nil {
		t.Fatalf("an ordinary torn tail was not repaired: %v", err)
	}
	events, _, err := readJournalTail(root)
	if err != nil {
		t.Fatalf("the repaired journal does not read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want the complete record plus the new one", len(events))
	}
}

// endsWithNewline answers whether the journal's last byte terminates a record,
// and it used to swallow a read error and answer false. The caller reads false
// as "the last line was expected to be incomplete", so an I/O failure became a
// torn tail: the final event was dropped rather than reported, and the rerun
// budget counted one flake fewer than had happened.
//
// A budget that fails open buys a rerun that should have been refused, which is
// the one direction this file's own comment says it must never fail in.
func TestAJournalReadErrorIsNotMistakenForATornTail(t *testing.T) {
	root := testGitRepo(t)
	path := filepath.Join(root, ".vise", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A closed handle is the cheapest reliable read failure there is.
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	terminated, err := endsWithNewline(file, 3)
	if err == nil {
		t.Fatal("a failed read was answered rather than reported")
	}
	if terminated {
		t.Error("a failed read claimed the journal was terminated")
	}
}

// atomicWrite was the one state writer that did not apply this package's
// symlink policy to the directory it writes into. MkdirAll follows symlinks, so
// a symlinked target directory would have let the staged rename land outside
// the checkout. Every other directory creation here goes through
// ensureDirectory, whose whole purpose is to refuse exactly that.
func TestAtomicWriteRefusesASymlinkedTargetDirectory(t *testing.T) {
	root := testGitRepo(t)
	elsewhere := t.TempDir()
	target := filepath.Join(root, "redirected")
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	err := atomicWrite(root, filepath.Join(target, "vise.lock"), []byte("{}\n"), 0o644)
	if err == nil {
		t.Fatal("a write into a symlinked directory was accepted")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("the error does not say why: %v", err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "vise.lock")); err == nil {
		t.Error("the write landed outside the checkout")
	}
}

// The budget is two flakes, not two flakes of one probe. Two different probes
// flaking once each exhausts a full-suite run, because the run is what is not
// converging — and each of them individually still has budget, so narrowing to
// one that flaked once still runs.
//
// Everything written about this said "a probe that has already flaked twice",
// which is false for the commonest shape of a flaky suite. The refusal message
// said it too, so an operator was told something about one probe that no probe
// had done.
func TestTheBudgetCountsFlakesNotFlakesOfOneProbe(t *testing.T) {
	events := []JournalEvent{
		{Event: "record", Commit: "c1", Lock: "L"},
		{Event: "flake", Commit: "c1", Lock: "L", Flaky: []string{"a"}, Probes: []string{"a", "b"}},
		{Event: "flake", Commit: "c1", Lock: "L", Flaky: []string{"b"}, Probes: []string{"a", "b"}},
	}
	if count, bounded := ConsecutiveFlakes(events, "c1", "L", []string{"a", "b"}); count != 2 || !bounded {
		t.Errorf("the whole suite counts %d flakes (bounded=%v), want 2 — a third full run must be refused", count, bounded)
	}
	for _, id := range []string{"a", "b"} {
		if count, _ := ConsecutiveFlakes(events, "c1", "L", []string{id}); count != 1 {
			t.Errorf("probe %s alone counts %d, want 1 — narrowing to diagnose must still run", id, count)
		}
	}
}
