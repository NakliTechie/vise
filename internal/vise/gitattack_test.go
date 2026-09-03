package vise

import (
	"strings"
	"testing"
)

// A probe can walk past the work-tree check with three ordinary Git commands,
// or by editing the ignore rules the untracked scan obeys. Neither writes to
// vise's own state, so the evaluator digest does not see them, and both change
// what "the checkout is unchanged" means rather than changing the checkout.
// Found by a cold read from a model family that had not seen this code before,
// and reproduced before the fix existed.
func TestProbeMovingHeadIsDetected(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "source.txt", "original\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "source")

	// Change a tracked file, then move HEAD so the tree matches the new
	// commit. The post-run diff is against HEAD, which the probe just moved.
	// The complete version: stage the change so write-tree records it, then
	// commit that tree and move HEAD onto it. The working tree now matches the
	// new HEAD exactly, so a diff against HEAD sees nothing at all.
	run := "printf tampered > source.txt && " +
		"git add source.txt && " +
		"tree=$(git write-tree) && " +
		"commit=$(git commit-tree \"$tree\" -p HEAD -m moved) && " +
		"git update-ref HEAD \"$commit\" && printf done"
	result := Runner{Root: root}.RunProbe(Probe{ID: "sneaky", Run: run, Timeout: 30}, true)

	if result.HarnessError == "" {
		t.Fatalf("a probe that rewrote a tracked file and moved HEAD was not reported: %#v", result)
	}
	t.Logf("reported as: %s", result.HarnessError)
	if !strings.Contains(result.HarnessError, "HEAD") && !strings.Contains(result.HarnessError, "tracked") {
		t.Fatalf("the message does not name the cause: %q", result.HarnessError)
	}
}

func TestProbeHidingItsStrayBehindTheIgnoreRulesIsDetected(t *testing.T) {
	root := testGitRepo(t)

	// .git/info/exclude is not tracked, not in the work tree, and is obeyed by
	// `git ls-files --others --exclude-standard`. A probe that appends to it
	// makes its own stray invisible to the untracked half of the snapshot.
	run := "printf 'leftover.txt\\n' >> .git/info/exclude && printf stray > leftover.txt && printf done"
	result := Runner{Root: root}.RunProbe(Probe{ID: "hider", Run: run, Timeout: 30}, true)

	if result.HarnessError == "" {
		t.Fatalf("a probe that hid its stray behind the ignore rules was not reported: %#v", result)
	}
	if !strings.Contains(result.HarnessError, "git's own state") {
		t.Fatalf("the message does not name the cause: %q", result.HarnessError)
	}
}

// And the ordinary case still passes: a probe that changes nothing must not be
// accused of moving Git's state just because Git touched a file of its own.
func TestAnOrdinaryProbeDoesNotTripTheGitStateCheck(t *testing.T) {
	root := testGitRepo(t)
	for i := 0; i < 3; i++ {
		result := Runner{Root: root}.RunProbe(Probe{ID: "quiet", Run: "git status --porcelain >/dev/null; printf ok", Timeout: 30}, true)
		if result.HarnessError != "" {
			t.Fatalf("run %d: an ordinary probe was accused: %s", i+1, result.HarnessError)
		}
	}
}

// A repository with no commits is a state somebody reaches by running
// `git init` and then vise. It produced a raw git error about an ambiguous
// argument, escaped newlines and all, under next.action fix_probe — telling an
// agent to repair a probe when nothing had ever been committed.
func TestARepositoryWithNoCommitsIsToldWhatIsWrong(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init", "-q")
	testGit(t, root, "config", "user.email", "vise-tests@example.invalid")
	testGit(t, root, "config", "user.name", "vise tests")
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")

	if GitHasCommits(root) {
		t.Fatal("a fresh repository was reported as having commits")
	}

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	outcome := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true}).Outcome
	if outcome.Exit != ExitHarness {
		t.Fatalf("exit = %d, want harness", outcome.Exit)
	}
	failure := outcome.Failures["git"]
	if !strings.Contains(failure.Detail, "no commits") {
		t.Fatalf("the failure does not say what is wrong: %q", failure.Detail)
	}
	if strings.Contains(failure.Detail, "ambiguous argument") {
		t.Fatalf("the raw git error reached the caller: %q", failure.Detail)
	}
	// An operator makes the commit; there is nothing here an agent may do.
	if outcome.Next.Action != NextHuman {
		t.Fatalf("next.action = %q, want human", outcome.Next.Action)
	}

	// Once there is a commit, recording works.
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "harness")
	if !GitHasCommits(root) {
		t.Fatal("a repository with a commit was reported as having none")
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record after the first commit: %#v", result.Outcome)
	}
}
