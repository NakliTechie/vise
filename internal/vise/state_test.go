package vise

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteGenerationRoundTripAndPrune(t *testing.T) {
	root := t.TempDir()
	keepData := []byte("keep")
	oldData := []byte("old")
	keepHash := HashBytes(keepData)
	oldHash := HashBytes(oldData)
	if err := WriteBlobs(root, map[string][]byte{keepHash: keepData, oldHash: oldData}); err != nil {
		t.Fatal(err)
	}
	lock := Lockfile{V: 1, Fingerprint: Fingerprint{OS: "test", Arch: "test"}, Probes: map[string]ProbeLock{
		"p": {RunHash: "sha256:run", RecordedCommit: "abc", Stdout: keepHash, Stderr: HashBytes(nil)},
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
	if _, err := os.Stat(filepath.Join(root, ".vise", "blobs", HashName(oldHash))); !os.IsNotExist(err) {
		t.Fatalf("old blob not pruned: %v", err)
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
