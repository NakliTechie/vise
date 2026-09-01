package vise

import "testing"

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
