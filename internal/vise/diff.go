package vise

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

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
	var b strings.Builder
	fmt.Fprintf(&b, "--- expected/%s\n+++ got/%s\n@@ first divergence line %d @@\n", label, label, index+1)
	for i := start; i < index; i++ {
		fmt.Fprintf(&b, " %s\n", expectedLines[i])
	}
	for i := index; i < endExpected; i++ {
		fmt.Fprintf(&b, "-%s\n", expectedLines[i])
	}
	for i := index; i < endGot; i++ {
		fmt.Fprintf(&b, "+%s\n", gotLines[i])
	}
	remaining := max(len(expectedLines)-endExpected, len(gotLines)-endGot)
	if remaining > 0 {
		fmt.Fprintf(&b, "… %d later line(s) omitted\n", remaining)
	}
	return strings.TrimSuffix(b.String(), "\n")
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
