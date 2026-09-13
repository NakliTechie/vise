package vise

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This is a simulated Git-inspection failure, not a damaged repository. The
// shim fails only GitHead's exact command and forwards GitHasCommits and every
// other invocation to the real Git binary. A real unborn repository exercises
// resolveHead's separate no-commits branch and cannot reach this fallback.
func TestGitAuthorityResolveHeadFailureWithExistingCommitIsOperatorOwned(t *testing.T) {
	root := testGitRepo(t)
	if !GitHasCommits(root) {
		t.Fatal("fixture must have a commit")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "calls")
	shim := `#!/bin/sh
printf '%s\n' "$*" >> ` + strconv.Quote(logPath) + `
if [ "$1" = rev-parse ] && [ "$2" = HEAD ] && [ "$#" -eq 2 ]; then
  printf 'simulated GitHead inspection failure' >&2
  exit 71
fi
exec ` + strconv.Quote(realGit) + ` "$@"
`
	writeTestFile(t, shimDir, "git", shim)
	if err := os.Chmod(filepath.Join(shimDir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	record := newRecordRun(root, Manifest{}, nil, RecordOptions{})
	if record.resolveHead() {
		t.Fatal("resolveHead accepted a failed GitHead inspection")
	}
	got := record.result.Outcome
	failure, ok := got.Failures["git"]
	if got.Exit != ExitHarness || got.Next.Action != NextHuman || len(got.Failures) != 1 || !ok || failure.Class != "harness" || !failure.Operator {
		t.Fatalf("resolveHead authority: %#v", got)
	}
	if !strings.Contains(failure.Detail, "simulated GitHead inspection failure") {
		t.Fatalf("underlying GitHead cause was lost: %q", failure.Detail)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	callText := string(calls)
	if !strings.Contains(callText, "rev-parse HEAD") || !strings.Contains(callText, "rev-parse --verify --quiet HEAD") {
		t.Fatalf("shim did not isolate fallback branches: %q", callText)
	}
}
