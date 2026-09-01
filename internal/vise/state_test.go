package vise

import (
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
	if len(events) != 5 || ConsecutiveFlakes(events, "c", "l", []string{"p"}) != 2 {
		t.Fatalf("events = %#v", events)
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
	err := atomicWrite(path, []byte("new"), 0o644)
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
