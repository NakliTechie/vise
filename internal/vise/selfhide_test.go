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

// .gitattributes is the same shape as .gitignore: git diff reads it from the
// work tree whatever its ignore status, and it decides whether a path is
// diffed at all. An ignored one could therefore change what the comparison
// sees while staying outside the snapshot itself.
func TestProbeHidingBehindAnIgnoredGitattributes(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.gitattributes\n")
	writeTestFile(t, root, "source.txt", "original\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "source")

	// The attributes file is ignored, so it is invisible to the untracked
	// scan; it still changes how the tracked file is diffed.
	run := "printf 'source.txt -diff\\n' > .gitattributes && printf tampered > source.txt && printf done"
	result := Runner{Root: root}.RunProbe(Probe{ID: "attrhide", Run: run, Timeout: 30}, true)

	t.Logf("harness error: %q", result.HarnessError)
	if result.HarnessError == "" {
		t.Fatal("a probe changed a tracked file behind an ignored .gitattributes and nothing noticed")
	}
}
