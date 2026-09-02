package vise

import (
	"strings"
	"testing"
	"unicode/utf8"
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

func TestFirstDiffDistinguishesEmptyFromOneNewline(t *testing.T) {
	if diff := FirstDiff("stdout", []byte(""), []byte("\n")); diff == "" || !strings.Contains(diff, "@@ first divergence line 1 @@") || !strings.Contains(diff, "\n+") {
		t.Fatalf("diff = %q", diff)
	}
	if diff := FirstDiff("stdout", []byte("\n"), []byte("")); diff == "" || !strings.Contains(diff, "\n-") {
		t.Fatalf("diff = %q", diff)
	}
}

// The line count was bounded; the line length was not. A probe whose output is
// one long line — a minified bundle, a JSON document, a base64 blob — could
// put the entire capture bound into a single rendered line, which spends an
// agent's context on something it cannot read.
func TestFirstDiffBoundsHowMuchOfALineItPrints(t *testing.T) {
	expected := []byte(strings.Repeat("a", 5000) + "X" + strings.Repeat("a", 5000))
	got := []byte(strings.Repeat("a", 5000) + "Y" + strings.Repeat("a", 5000))

	diff := FirstDiff("stdout", expected, got)
	if len(diff) > 2000 {
		t.Fatalf("diff of two 10001-byte lines rendered %d bytes", len(diff))
	}
	// The window has to contain the difference. Clipping from the line start
	// would hide the one thing the diff exists to show.
	if !strings.Contains(diff, "X") || !strings.Contains(diff, "Y") {
		t.Fatalf("the window omitted the divergence:\n%s", diff)
	}
	if !strings.Contains(diff, "line is 10001 runes") {
		t.Fatalf("the diff does not say how long the line was:\n%s", diff)
	}
	if !strings.Contains(diff, "rune(s)…") {
		t.Fatalf("the diff does not mark what it clipped:\n%s", diff)
	}
}

// Clipping counts runes, not bytes, so a window never splits a character.
func TestFirstDiffClipsOnRuneBoundaries(t *testing.T) {
	expected := []byte(strings.Repeat("é", 400) + "X")
	got := []byte(strings.Repeat("é", 400) + "Y")

	diff := FirstDiff("stdout", expected, got)
	if !utf8.ValidString(diff) {
		t.Fatal("clipping split a character")
	}
	if !strings.Contains(diff, "X") || !strings.Contains(diff, "Y") {
		t.Fatalf("the window omitted the divergence:\n%s", diff)
	}
}

// A short line must come out exactly as it is: the bound is a safety net for
// pathological output, not a reformatting of ordinary output.
func TestFirstDiffLeavesOrdinaryLinesAlone(t *testing.T) {
	diff := FirstDiff("stdout", []byte("hello\n"), []byte("goodbye\n"))
	if !strings.Contains(diff, "-hello\n") || !strings.Contains(diff, "+goodbye") {
		t.Fatalf("an ordinary line was rewritten:\n%s", diff)
	}
	if strings.Contains(diff, "rune") {
		t.Fatalf("an ordinary line was annotated:\n%s", diff)
	}
}
