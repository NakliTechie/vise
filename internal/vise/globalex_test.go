package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The snapshot digests .git/config, so a probe that repoints core.excludesFile
// is caught. The file it points AT is a different question: it can live outside
// the checkout, where nothing digests it, and it is obeyed by
// `git ls-files --others --exclude-standard` exactly like .git/info/exclude.
func TestProbeHidingStrayBehindAGlobalExcludesFile(t *testing.T) {
	root := testGitRepo(t)
	outside := t.TempDir()
	excludes := filepath.Join(outside, "global-excludes")
	if err := os.WriteFile(excludes, []byte("# nothing yet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "config", "core.excludesFile", excludes)

	// The probe appends to a file outside the repository, then writes a stray
	// that the new pattern hides.
	run := "printf 'leftover.txt\\n' >> " + excludes + " && printf stray > leftover.txt && printf done"
	result := Runner{Root: root}.RunProbe(Probe{ID: "hider", Run: run, Timeout: 30}, true)

	if !strings.Contains(result.HarnessError, "git's own state") {
		t.Fatalf("a probe hid its stray behind a global excludes file: %q", result.HarnessError)
	}
}

// And the ordinary case: a repository whose excludes file exists and is left
// alone must not be accused of anything, run after run.
func TestAnUntouchedGlobalExcludesFileIsNotAChange(t *testing.T) {
	root := testGitRepo(t)
	outside := t.TempDir()
	excludes := filepath.Join(outside, "global-excludes")
	if err := os.WriteFile(excludes, []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "config", "core.excludesFile", excludes)

	for i := 0; i < 3; i++ {
		result := Runner{Root: root}.RunProbe(Probe{ID: "quiet", Run: "printf ok", Timeout: 30}, true)
		if result.HarnessError != "" {
			t.Fatalf("run %d: %s", i+1, result.HarnessError)
		}
	}

	// A repository with no excludes file configured at all is also fine.
	plain := testGitRepo(t)
	if result := (Runner{Root: plain}).RunProbe(Probe{ID: "quiet", Run: "printf ok", Timeout: 30}, true); result.HarnessError != "" {
		t.Fatalf("a repository with no excludes file was accused: %s", result.HarnessError)
	}
}
