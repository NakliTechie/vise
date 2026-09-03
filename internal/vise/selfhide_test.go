package vise

import (
	"strings"
	"testing"
)

// `git ls-files --others --exclude-standard` obeys .gitignore files in the work
// tree, and an untracked one can list itself. A probe that writes
// ".gitignore" containing both its own name and its stray makes both invisible
// to the untracked scan, without touching anything in the git directory.
func TestProbeHidingBehindASelfIgnoringGitignore(t *testing.T) {
	root := testGitRepo(t)

	// Nothing but the two hidden files: a .gitignore naming itself, and the
	// stray it also names.
	run := "mkdir -p sub && printf '.gitignore\\nleftover.txt\\n' > sub/.gitignore && " +
		"printf stray > sub/leftover.txt && printf done"
	result := Runner{Root: root}.RunProbe(Probe{ID: "selfhide", Run: run, Timeout: 30}, true)

	t.Logf("harness error: %q", result.HarnessError)
	if result.HarnessError == "" {
		t.Fatal("a probe hid itself behind a self-ignoring .gitignore and nothing noticed")
	}
	if !strings.Contains(result.HarnessError, "sub") && !strings.Contains(result.HarnessError, "gitignore") {
		t.Fatalf("reported, but not naming what it saw: %q", result.HarnessError)
	}
}
