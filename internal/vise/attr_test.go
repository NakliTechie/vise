package vise

import (
	"strings"
	"testing"
)

// git diff consults gitattributes, and an attribute can suppress a file's diff
// entirely. .git/info/attributes is the exact analogue of .git/info/exclude:
// not tracked, not in the working tree, and not digested.
func TestProbeSuppressingItsDiffWithGitAttributes(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "source.txt", "original\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "source")

	// Mark the file binary-and-undiffable, then change it. `git diff` for a
	// path with -diff prints only "Binary files differ" — but it does print
	// something, so the question is whether the digest still moves.
	run := "mkdir -p .git/info && printf 'source.txt -diff\\n' >> .git/info/attributes && " +
		"printf tampered > source.txt && printf done"
	result := Runner{Root: root}.RunProbe(Probe{ID: "attr", Run: run, Timeout: 30}, true)

	t.Logf("harness error: %q", result.HarnessError)
	if result.HarnessError == "" {
		t.Fatal("a probe changed a tracked file behind a gitattributes rule and nothing noticed")
	}
	if !strings.Contains(result.HarnessError, "git's own state") && !strings.Contains(result.HarnessError, "tracked") {
		t.Fatalf("reported, but not as what it is: %q", result.HarnessError)
	}
}

// The configuration Git resolves, not the repository's file. Hashing
// .git/config alone left every setting inherited from the global or system
// level outside the snapshot, and those decide things the comparison depends
// on. A probe that sets one at the global level moves nothing in the
// repository's own config file.
func TestProbeChangingGlobalGitConfigIsDetected(t *testing.T) {
	// A private HOME for the whole test, so vise's own git calls and the
	// probe's resolve the same global config and the real one is untouched.
	// Setting it for the probe alone would prove nothing: vise would never
	// read the file the probe wrote.
	t.Setenv("HOME", t.TempDir())
	root := testGitRepo(t)

	result := Runner{Root: root}.RunProbe(Probe{
		ID:      "global",
		Run:     "git config --global core.excludesFile /dev/null && printf done",
		Timeout: 30,
	}, true)

	if !strings.Contains(result.HarnessError, "git's own state") {
		t.Fatalf("a probe that set a global config value was not reported: %q", result.HarnessError)
	}
}
