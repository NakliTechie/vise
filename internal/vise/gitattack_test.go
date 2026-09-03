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
