package vise

import (
	"bytes"
	"encoding/json"
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
		"p": {RunHash: runHash, RecordedCommit: "abc", Stdout: keepHash, Stderr: HashBytes(nil)},
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
		"p": {RunHash: runHash, RecordedCommit: "abc", Stdout: hash, Stderr: hash},
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
	entry := AddObservationBlobs(blobs, RunResult{Stdout: data, Stderr: nil})
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
	count, bounded := ConsecutiveFlakes(events, "c", "l", []string{"p", "q"})
	if !truncated || bounded || count != 0 {
		t.Fatalf("truncated=%t bounded=%t count=%d", truncated, bounded, count)
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
		if _, err := AcquireStateLock(root); err == nil {
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
	first, err := AcquireStateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() {
		second, err := AcquireStateLock(root)
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
