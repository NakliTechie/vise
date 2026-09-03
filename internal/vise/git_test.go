package vise

import (
	"os/exec"
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
