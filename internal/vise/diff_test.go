package vise

import (
	"strings"
	"testing"
)

func TestFirstDiffMarksContextLinesAsContext(t *testing.T) {
	expected := []byte("one\ntwo\nthree\nfour\n")
	got := []byte("one\ntwo\nTHREE\nfour\n")
	want := "--- expected/stdout\n+++ got/stdout\n@@ first divergence line 3 @@\n one\n two\n-three\n-four\n+THREE\n+four"
	if diff := FirstDiff("stdout", expected, got); diff != want {
		t.Fatalf("diff = %q\nwant %q", diff, want)
	}
	if diff := FirstDiff("stdout", expected, expected); diff != "" {
		t.Fatalf("equal inputs produced %q", diff)
	}
}

func TestFirstDiffShowsTrailingNewlineChange(t *testing.T) {
	diff := FirstDiff("stdout", []byte("a\n"), []byte("a"))
	if diff == "" || !strings.Contains(diff, "+\\ No newline at end of file") || !strings.Contains(diff, " a\n") {
		t.Fatalf("diff = %q", diff)
	}
	if diff := FirstDiff("stdout", []byte("a"), []byte("a\n")); !strings.Contains(diff, "-\\ No newline at end of file") {
		t.Fatalf("diff = %q", diff)
	}
}
