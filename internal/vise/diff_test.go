package vise

import (
	"bytes"
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

// A divergence in output that is not valid UTF-8 cannot be rendered as lines,
// so it falls to a byte preview. Nothing tested that branch, and returning ""
// from it would have reported "no difference" for two observations that
// differ — a red gate rendering as though nothing were wrong.
func TestFirstDiffDescribesABinaryDivergence(t *testing.T) {
	expected := []byte{0x00, 0xff, 0xfe, 0x01}
	got := []byte{0x00, 0xff, 0xfe, 0x02}

	diff := FirstDiff("stdout", expected, got)
	if diff == "" {
		t.Fatal("two different binary observations rendered as no difference")
	}
	if !strings.Contains(diff, "stdout differs") {
		t.Fatalf("the diff does not say what differs: %q", diff)
	}

	// Identical binary output is still no difference.
	if diff := FirstDiff("stdout", expected, expected); diff != "" {
		t.Fatalf("identical binary output rendered a difference: %q", diff)
	}

	// And a long binary observation is bounded like everything else. The
	// preview holds 64 bytes a side and escapes each unprintable one to four
	// characters, so the ceiling is a few hundred bytes and not a fraction of
	// the observation.
	long := append(bytes.Repeat([]byte{0xff}, 4096), 0x01)
	other := append(bytes.Repeat([]byte{0xff}, 4096), 0x02)
	rendered := FirstDiff("stdout", long, other)
	if len(rendered) > 1000 {
		t.Fatalf("a binary diff of two 4097-byte observations rendered %d bytes", len(rendered))
	}
	if !strings.Contains(rendered, "4097 bytes total") {
		t.Fatalf("the diff does not say how long the observation was: %q", rendered)
	}
}

// The diff's bounds are a promise about how much of an agent's context a red
// gate can consume. Three mutations survived: widening the 160-rune window to
// 500, rendering every later line instead of eight, and dropping the marker
// that says how many lines were omitted. Each turns a bounded rendering into
// an unbounded one while every existing assertion still held.
func TestTheDiffBoundsAreExactlyWhatTheSpecSays(t *testing.T) {
	if lineWindow != 160 {
		t.Fatalf("lineWindow is %d; the spec and this test both say 160", lineWindow)
	}

	// Line count: two lines of context, eight of each side, a header, and the
	// omission marker. Twenty lines of divergence must not render twenty.
	expected := make([]string, 0, 40)
	got := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		expected = append(expected, "line")
		got = append(got, "line")
	}
	for i := 10; i < 40; i++ {
		got[i] = "changed"
	}
	diff := FirstDiff("stdout", []byte(strings.Join(expected, "\n")), []byte(strings.Join(got, "\n")))
	lines := strings.Split(diff, "\n")
	// header (3) + context (2) + 8 expected + 8 got + omission marker.
	if len(lines) > 24 {
		t.Fatalf("a 30-line divergence rendered %d lines:\n%s", len(lines), diff)
	}
	if !strings.Contains(diff, "later line(s) omitted") {
		t.Fatalf("the diff does not say how much it left out:\n%s", diff)
	}
	// And it says how many, because "some" is not a number a reader can act on.
	// 23, not 22: neither observation ends in a newline, and the marker that
	// records that is itself a line.
	if !strings.Contains(diff, "23 later line(s) omitted") {
		t.Fatalf("the omitted count is wrong or missing:\n%s", diff)
	}

	// Line length: a rendered line carries at most the window plus its
	// annotations, never the whole line.
	long := strings.Repeat("a", 5000) + "X"
	other := strings.Repeat("a", 5000) + "Y"
	clipped := FirstDiff("stdout", []byte(long), []byte(other))
	for _, line := range strings.Split(clipped, "\n") {
		if len([]rune(line)) > lineWindow+80 {
			t.Fatalf("a rendered line is %d runes, past the %d-rune window plus its annotations:\n%s",
				len([]rune(line)), lineWindow, line)
		}
	}
}
