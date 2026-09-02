package vise

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// lineWindow bounds how much of any single line the diff renders.
//
// The line *count* has always been bounded; the line *length* was not, and a
// probe whose output is one long line — a minified bundle, a JSON document, a
// base64 blob — could put the whole 256 KiB capture bound into a single
// rendered line. "Bounded output, always" has to mean bounded in both
// directions, or an agent's context is spent on one line it cannot read.
const lineWindow = 160

func FirstDiff(label string, expected, got []byte) string {
	if bytes.Equal(expected, got) {
		return ""
	}
	if !utf8.Valid(expected) || !utf8.Valid(got) {
		return fmt.Sprintf("%s differs: expected %s, got %s", label, bytePreview(expected), bytePreview(got))
	}
	expectedLines := splitLines(expected)
	gotLines := splitLines(got)
	index := 0
	for index < len(expectedLines) && index < len(gotLines) && expectedLines[index] == gotLines[index] {
		index++
	}
	start := index - 2
	if start < 0 {
		start = 0
	}
	endExpected := index + 8
	if endExpected > len(expectedLines) {
		endExpected = len(expectedLines)
	}
	endGot := index + 8
	if endGot > len(gotLines) {
		endGot = len(gotLines)
	}
	// The two lines that actually diverge are windowed around the differing
	// column rather than the line start. Truncating from the left would cut
	// away the very difference the diff exists to show, which is worse than
	// printing nothing.
	column := 0
	if index < len(expectedLines) && index < len(gotLines) {
		column = firstDifferingColumn(expectedLines[index], gotLines[index])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- expected/%s\n+++ got/%s\n@@ first divergence line %d @@\n", label, label, index+1)
	for i := start; i < index; i++ {
		fmt.Fprintf(&b, " %s\n", clipLine(expectedLines[i], 0))
	}
	for i := index; i < endExpected; i++ {
		fmt.Fprintf(&b, "-%s\n", clipLine(expectedLines[i], columnFor(i, index, column)))
	}
	for i := index; i < endGot; i++ {
		fmt.Fprintf(&b, "+%s\n", clipLine(gotLines[i], columnFor(i, index, column)))
	}
	remaining := max(len(expectedLines)-endExpected, len(gotLines)-endGot)
	if remaining > 0 {
		fmt.Fprintf(&b, "… %d later line(s) omitted\n", remaining)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// columnFor centres the window on the differing column for the diverging line
// itself, and on the start of the line for everything after it.
func columnFor(i, index, column int) int {
	if i == index {
		return column
	}
	return 0
}

// firstDifferingColumn returns the rune index at which two lines first differ.
func firstDifferingColumn(a, b string) int {
	runesA := []rune(a)
	runesB := []rune(b)
	i := 0
	for i < len(runesA) && i < len(runesB) && runesA[i] == runesB[i] {
		i++
	}
	return i
}

// clipLine renders at most lineWindow runes of a line, centred on around, and
// says how many runes it left out on each side. It counts runes rather than
// bytes so a window never splits a character, and it reports the full length
// so the reader knows what was withheld rather than guessing.
func clipLine(line string, around int) string {
	runes := []rune(line)
	if len(runes) <= lineWindow {
		return line
	}
	start := around - lineWindow/2
	if start < 0 {
		start = 0
	}
	if start > len(runes)-lineWindow {
		start = len(runes) - lineWindow
	}
	end := start + lineWindow
	var b strings.Builder
	if start > 0 {
		fmt.Fprintf(&b, "…%d rune(s)…", start)
	}
	b.WriteString(string(runes[start:end]))
	if end < len(runes) {
		fmt.Fprintf(&b, "…%d rune(s)… (line is %d runes)", len(runes)-end, len(runes))
	} else {
		fmt.Fprintf(&b, " (line is %d runes)", len(runes))
	}
	return b.String()
}

func bytePreview(data []byte) string {
	const limit = 64
	if len(data) <= limit {
		return strconv.QuoteToASCII(string(data))
	}
	return fmt.Sprintf("%s… (%d bytes total)", strconv.QuoteToASCII(string(data[:limit])), len(data))
}

// noNewlineMarker is the line shown for a side whose data does not end in a
// newline, so a change in the trailing newline alone still renders a diff.
const noNewlineMarker = "\\ No newline at end of file"

// splitLines splits on newlines without producing a phantom empty last line
// for input that ends in a newline; input that does not end in one gets the
// marker line appended instead.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	if len(data) > 0 {
		lines = append(lines, noNewlineMarker)
	}
	return lines
}
