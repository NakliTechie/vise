package vise

import (
	"bytes"
	"fmt"
	"testing"
)

func TestC19RunnerCapturesEveryChannelAtTheExactBoundary(t *testing.T) {
	sizes := []struct {
		name      string
		size      int
		truncated bool
	}{
		{"under", CaptureLimit - 1, false},
		{"exact", CaptureLimit, false},
		{"over", CaptureLimit + 1, true},
	}
	channels := []struct {
		name          string
		command       func(int) string
		selectCapture func(RunResult) Capture
	}{
		{
			name: "stdout",
			command: func(size int) string {
				return fmt.Sprintf("dd if=/dev/zero bs=%d count=1 2>/dev/null", size)
			},
			selectCapture: func(result RunResult) Capture { return result.Stdout },
		},
		{
			name: "stderr",
			command: func(size int) string {
				return fmt.Sprintf("dd if=/dev/zero bs=%d count=1 1>&2 2>/dev/null", size)
			},
			selectCapture: func(result RunResult) Capture { return result.Stderr },
		},
		{
			name: "artifact",
			command: func(size int) string {
				return fmt.Sprintf("mkdir -p out; dd if=/dev/zero bs=%d count=1 of=out/blob.bin 2>/dev/null", size)
			},
			selectCapture: func(result RunResult) Capture { return result.Files["out/blob.bin"] },
		},
	}

	for _, channel := range channels {
		for _, boundary := range sizes {
			t.Run(channel.name+"/"+boundary.name, func(t *testing.T) {
				root := testGitRepo(t)
				probe := Probe{ID: channel.name, Run: channel.command(boundary.size), Timeout: 30}
				if channel.name == "artifact" {
					probe.Files = []string{"out/blob.bin"}
				}
				result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
				if result.HarnessError != "" || result.Exit != 0 {
					t.Fatalf("result = %#v", result)
				}
				capture := channel.selectCapture(result)
				wantPrefix := boundary.size
				if wantPrefix > CaptureLimit {
					wantPrefix = CaptureLimit
				}
				if capture.Size != int64(boundary.size) || len(capture.Prefix) != wantPrefix || capture.Truncated() != boundary.truncated {
					t.Fatalf("capture = size %d prefix %d truncated %t; want %d %d %t", capture.Size, len(capture.Prefix), capture.Truncated(), boundary.size, wantPrefix, boundary.truncated)
				}
				if capture.Hash != zeroHash(int64(boundary.size)) {
					t.Fatalf("hash %q is not the full %d-byte hash", capture.Hash, boundary.size)
				}
			})
		}
	}
}

func TestC19BinarySplitPrefixRetainsRawIdentity(t *testing.T) {
	payload := append(bytes.Repeat([]byte{'a'}, CaptureLimit-1), 0xc3, 0xa9, 0xff)
	wantPrefix := append(bytes.Repeat([]byte{'a'}, CaptureLimit-1), 0xc3)
	t.Run("split prefix and full identity", func(t *testing.T) {
		capture := CaptureBytes(payload)
		if !bytes.Equal(capture.Prefix, wantPrefix) {
			t.Fatal("capture did not retain the exact split-UTF-8 prefix bytes")
		}
		if capture.Size != int64(len(payload)) || !capture.Truncated() || capture.Hash != HashBytes(payload) {
			t.Fatalf("capture = size %d truncated %t hash %q", capture.Size, capture.Truncated(), capture.Hash)
		}
	})
	t.Run("post-prefix bytes remain distinct", func(t *testing.T) {
		capture := CaptureBytes(payload)
		changedAfterPrefix := append(append([]byte(nil), wantPrefix...), 0xaa, 0xff)
		changed := CaptureBytes(changedAfterPrefix)
		if !bytes.Equal(changed.Prefix, capture.Prefix) {
			t.Fatal("control payloads do not share the same retained prefix")
		}
		if changed.Hash == capture.Hash {
			t.Fatal("a post-prefix byte change was not detected by the full hash")
		}
	})
}
