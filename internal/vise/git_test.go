package vise

import (
	"reflect"
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
