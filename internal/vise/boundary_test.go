package vise

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Five mutations of init's .gitignore handling survived: omitting each
// required entry independently, discarding whatever was already in the file,
// and appending a duplicate of an entry that was already there. The first
// makes every gate dirty the tree; the second destroys the user's file.
func TestInitWritesExactlyTheIgnoreEntriesItOwes(t *testing.T) {
	required := []string{".vise/journal.jsonl", ".vise/run.lock", ".vise/tmp/"}

	t.Run("a repository with no ignore file", func(t *testing.T) {
		root := t.TempDir()
		testGit(t, root, "init", "-q")
		if err := InitRepository(root); err != nil {
			t.Fatal(err)
		}
		content := readIgnore(t, root)
		for _, entry := range required {
			if occurrences(content, entry) != 1 {
				t.Errorf("%q appears %d times, want exactly once", entry, occurrences(content, entry))
			}
		}
	})

	t.Run("an ignore file that already has entries", func(t *testing.T) {
		root := t.TempDir()
		testGit(t, root, "init", "-q")
		existing := "node_modules/\ndist/\n"
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(existing), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := InitRepository(root); err != nil {
			t.Fatal(err)
		}
		content := readIgnore(t, root)
		// What was there stays there. Rewriting somebody's .gitignore because
		// three lines were missing from it is not a fix.
		for _, kept := range []string{"node_modules/", "dist/"} {
			if !strings.Contains(content, kept) {
				t.Errorf("init discarded %q from an existing .gitignore", kept)
			}
		}
		for _, entry := range required {
			if occurrences(content, entry) != 1 {
				t.Errorf("%q appears %d times, want exactly once", entry, occurrences(content, entry))
			}
		}
	})

	t.Run("run twice", func(t *testing.T) {
		root := t.TempDir()
		testGit(t, root, "init", "-q")
		if err := InitRepository(root); err != nil {
			t.Fatal(err)
		}
		first := readIgnore(t, root)
		if err := os.Remove(filepath.Join(root, "vise.toml")); err != nil {
			t.Fatal(err)
		}
		if err := InitRepository(root); err != nil {
			t.Fatal(err)
		}
		// Byte-identical: a second init must not append what the first wrote.
		if second := readIgnore(t, root); second != first {
			t.Fatalf("a second init changed .gitignore:\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})
}

func readIgnore(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func occurrences(content, entry string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == entry {
			count++
		}
	}
	return count
}

// The capture bound is a number in the spec and a promise to the reader: an
// observation is compared exactly and rendered up to 256 KiB. Reducing the
// limit by one byte, and calling an exactly-at-limit capture truncated, both
// survived — so the boundary itself was never tested, only the middle.
func TestTheCaptureBoundHoldsExactlyWhereItSays(t *testing.T) {
	if CaptureLimit != 256*1024 {
		t.Fatalf("CaptureLimit is %d; the spec and this test both say 256 KiB", CaptureLimit)
	}

	tests := []struct {
		name          string
		size          int
		wantTruncated bool
		wantPrefix    int
	}{
		{"one byte under the bound", 256*1024 - 1, false, 256*1024 - 1},
		{"exactly at the bound", 256 * 1024, false, 256 * 1024},
		{"one byte over the bound", 256*1024 + 1, true, 256 * 1024},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := bytes.Repeat([]byte("x"), test.size)
			capture := CaptureBytes(data)

			if capture.Truncated() != test.wantTruncated {
				t.Errorf("truncated = %t, want %t at %d bytes", capture.Truncated(), test.wantTruncated, test.size)
			}
			if len(capture.Prefix) != test.wantPrefix {
				t.Errorf("prefix is %d bytes, want %d", len(capture.Prefix), test.wantPrefix)
			}
			// The size is the whole stream and the hash is of the whole
			// stream, whatever was retained: judgment is the hash, and a hash
			// of the prefix would call two different observations identical.
			if capture.Size != int64(test.size) {
				t.Errorf("size = %d, want %d", capture.Size, test.size)
			}
			if capture.Hash != HashBytes(data) {
				t.Errorf("the hash is not the hash of the whole stream")
			}
		})
	}

	// Two observations that differ only past the bound must not be equal.
	long := bytes.Repeat([]byte("x"), 256*1024+10)
	other := append(bytes.Repeat([]byte("x"), 256*1024+9), 'y')
	if CaptureBytes(long).Hash == CaptureBytes(other).Hash {
		t.Fatal("two observations differing past the capture bound hash the same")
	}
}
