package vise

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitTrackedPathsReportsOnlyTrackedFiles(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "out/result.txt", "untracked\n")
	tracked, err := GitTrackedPaths(root, []string{"out/result.txt", "tracked.txt", "missing.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tracked, []string{"tracked.txt"}) {
		t.Fatalf("tracked = %#v", tracked)
	}
	if tracked, err := GitTrackedPaths(root, nil); err != nil || tracked != nil {
		t.Fatalf("empty = %#v, %v", tracked, err)
	}
}

func TestGitTrackedPathsMatchesCaseInsensitively(t *testing.T) {
	root := testGitRepo(t)
	tracked, err := GitTrackedPaths(root, []string{"TRACKED.TXT", "Out/Result.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tracked, []string{"tracked.txt"}) {
		t.Fatalf("tracked = %#v", tracked)
	}
}

// A probe that detaches HEAD at the commit it is already on leaves the
// resolved commit identical and the repository on no branch. The snapshot
// digested the resolved value, so the gate said green, and the operator's next
// commit went somewhere they did not expect.
//
// SPEC says a probe must not change the checkout. Which branch you are on is
// part of the checkout even when the tree is byte-identical.
func TestAProbeThatDetachesHeadAtTheSameCommitIsDetected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := testGitRepo(t)
	result := Runner{Root: root}.RunProbe(Probe{
		ID:      "detacher",
		Run:     "git checkout -q --detach HEAD && printf ok",
		Timeout: 30,
	}, true)
	if result.HarnessError == "" {
		t.Fatal("a probe left the repository on no branch and the run came back clean")
	}
	if !strings.Contains(result.HarnessError, "git") {
		t.Fatalf("reported, but not as a change to git's own state: %q", result.HarnessError)
	}
}

// The untracked half of the snapshot hashes content, size, modification time
// and permission bits. Three of those four were protected; content was not.
//
// Dropping the content digest survives the suite because a rewrite normally
// moves the modification time too. It does not have to: a probe that rewrites
// a file and restores its timestamp — the shape of a formatter or a
// normalizer, and one `touch -r` away — changes what every later probe reads
// with nothing else moving. The size is the same because the bytes are the
// same length, which is exactly the case a byte-blind hash cannot see.
func TestTheSnapshotSeesAContentChangeThatHidesItsTimestamp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/\n")
	writeTestFile(t, root, "stray.txt", "aaaa\n")

	before, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Same length, same timestamp, different bytes.
	path := filepath.Join(root, "stray.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bbbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, after) {
		t.Error("a file was rewritten with its timestamp restored and the snapshot did not move")
	}
}
