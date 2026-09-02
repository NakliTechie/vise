package vise

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// zeroHash streams n zero bytes through sha256 without allocating them, so
// the test itself holds no more memory than the code under test.
func zeroHash(n int64) string {
	digest := sha256.New()
	chunk := make([]byte, 64*1024)
	for written := int64(0); written < n; {
		size := int64(len(chunk))
		if remaining := n - written; remaining < size {
			size = remaining
		}
		digest.Write(chunk[:size])
		written += size
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func TestProbeOutputLargerThanTheBoundIsHashedNotHeld(t *testing.T) {
	root := testGitRepo(t)
	const size = 10 << 20
	probe := Probe{ID: "noisy", Run: "dd if=/dev/zero bs=65536 count=160 2>/dev/null", Timeout: 60}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if result.HarnessError != "" || result.Exit != 0 {
		t.Fatalf("result = %#v", result)
	}
	if result.Stdout.Size != size {
		t.Fatalf("size = %d, want %d", result.Stdout.Size, size)
	}
	if len(result.Stdout.Prefix) != CaptureLimit {
		t.Fatalf("retained %d bytes, want the %d-byte bound", len(result.Stdout.Prefix), CaptureLimit)
	}
	if !result.Stdout.Truncated() {
		t.Fatal("a 10 MiB stream must report as truncated")
	}
	if want := zeroHash(size); result.Stdout.Hash != want {
		t.Fatalf("hash = %s, want %s (the hash must cover the whole stream)", result.Stdout.Hash, want)
	}

	// The same probe run twice must produce the same capture: the hash is the
	// judgment, and nothing about it depends on what was retained.
	second := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if !RunResultsEqual(result, second) {
		t.Fatalf("two runs of a bounded probe differ: %s vs %s", result.Stdout.Hash, second.Stdout.Hash)
	}

	blobs := make(map[string][]byte)
	entry := AddObservationBlobs(blobs, result)
	if !entry.StdoutLarge || entry.Stdout != result.Stdout.Hash {
		t.Fatalf("entry = %#v", entry)
	}
	if _, stored := blobs[entry.Stdout]; stored {
		t.Fatal("an over-bound observation must not be stored as a blob")
	}
}

func TestArtifactLargerThanTheBoundIsHashedNotHeld(t *testing.T) {
	root := testGitRepo(t)
	const size = 2 << 20
	probe := Probe{
		ID:      "big-artifact",
		Run:     "mkdir -p out; dd if=/dev/zero bs=65536 count=32 of=out/blob.bin 2>/dev/null; printf done",
		Timeout: 60,
		Files:   []string{"out/blob.bin"},
	}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if result.HarnessError != "" {
		t.Fatalf("result = %#v", result)
	}
	capture := result.Files["out/blob.bin"]
	if capture.Size != size || len(capture.Prefix) != CaptureLimit || !capture.Truncated() {
		t.Fatalf("capture = size %d, prefix %d, truncated %t", capture.Size, len(capture.Prefix), capture.Truncated())
	}
	if want := zeroHash(size); capture.Hash != want {
		t.Fatalf("hash = %s, want %s", capture.Hash, want)
	}
}

func TestDiffRunsRendersInsideTheBoundAndDegradesBeyondIt(t *testing.T) {
	root := t.TempDir()
	// A divergence inside the retained prefix still renders as bytes.
	expected := []byte("alpha\nbeta\n")
	got := CaptureBytes([]byte("alpha\nBETA\n"))
	if err := WriteBlobs(root, map[string][]byte{HashBytes(expected): expected}); err != nil {
		t.Fatal(err)
	}
	lock := ProbeLock{Exit: 0, Stdout: HashBytes(expected), Stderr: HashBytes(nil)}
	diff := DiffRuns(root, lock, RunResult{Stdout: got, Stderr: CaptureBytes(nil)})
	if !strings.Contains(diff, "-beta") || !strings.Contains(diff, "+BETA") {
		t.Fatalf("diff inside the bound = %q", diff)
	}

	// An observation that outgrew the bound and agrees with the baseline
	// everywhere vise retained degrades to hashes and says why: the
	// divergence is past the bound, so no byte-level diff would be honest.
	agreeing := append(append([]byte(nil), expected...), make([]byte, CaptureLimit-len(expected))...)
	over := Capture{Prefix: agreeing, Hash: "sha256:" + strings.Repeat("a", 64), Size: CaptureLimit + 1}
	diff = DiffRuns(root, lock, RunResult{Stdout: over, Stderr: CaptureBytes(nil)})
	if !strings.Contains(diff, "capture bound") || !strings.Contains(diff, lock.Stdout) {
		t.Fatalf("diff beyond the bound = %q", diff)
	}
}

func TestCaptureWriterMirrorsEveryByte(t *testing.T) {
	var mirror strings.Builder
	writer := newCaptureWriter(&mirror)
	payload := strings.Repeat("x", CaptureLimit+1024)
	if _, err := writer.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	capture := writer.Capture()
	if mirror.Len() != len(payload) {
		t.Fatalf("mirror got %d bytes, want %d", mirror.Len(), len(payload))
	}
	if len(capture.Prefix) != CaptureLimit || capture.Size != int64(len(payload)) {
		t.Fatalf("capture = prefix %d, size %d", len(capture.Prefix), capture.Size)
	}
	if capture.Hash != HashBytes([]byte(payload)) {
		t.Fatal("streamed hash differs from the whole-buffer hash")
	}
}

func TestCaptureDiffRendersADivergenceInsideThePrefix(t *testing.T) {
	root := t.TempDir()
	expected := []byte("alpha\nbeta\n")
	if err := WriteBlobs(root, map[string][]byte{HashBytes(expected): expected}); err != nil {
		t.Fatal(err)
	}
	// The observation outgrew the bound, but it changed early: the retained
	// prefix holds the divergence, so a byte-level diff is still honest.
	prefix := append([]byte("alpha\nBETA\n"), make([]byte, CaptureLimit-11)...)
	early := Capture{Prefix: prefix, Hash: "sha256:" + strings.Repeat("b", 64), Size: CaptureLimit * 4}
	diff := captureDiff(root, "stdout", HashBytes(expected), false, early)
	if !strings.Contains(diff, "-beta") || !strings.Contains(diff, "+BETA") || !strings.Contains(diff, "past the") {
		t.Fatalf("early divergence = %q", diff)
	}

	// Agreeing everywhere vise retained means the divergence is past the
	// bound; a byte diff would be a lie, so it degrades to hashes.
	same := append(append([]byte(nil), expected...), make([]byte, CaptureLimit-len(expected))...)
	late := Capture{Prefix: same, Hash: "sha256:" + strings.Repeat("c", 64), Size: CaptureLimit * 4}
	diff = captureDiff(root, "stdout", HashBytes(expected), false, late)
	if !strings.Contains(diff, "capture bound") || strings.Contains(diff, "-beta") {
		t.Fatalf("late divergence = %q", diff)
	}
}
