package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// evaluatorStateDigest is what makes "a probe may not touch the judge" real.
// Each of its inputs needs a case, or dropping one — the blob names were the
// one found — leaves the guard passing while that whole class of tampering
// stops being noticed.
func TestTheEvaluatorDigestCoversEveryPieceOfState(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n")
	writeTestFile(t, root, "vise.lock", "{\"v\":1}")
	if err := os.MkdirAll(filepath.Join(root, ".vise", "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, filepath.Join(".vise", "journal.jsonl"), "{\"e\":\"gate\"}\n")
	writeTestFile(t, root, filepath.Join(".vise", "blobs", strings.Repeat("a", 64)), "one")

	base, err := evaluatorStateDigest(root)
	if err != nil {
		t.Fatal(err)
	}

	// Each change takes the directory to act on, so no subtest depends on a
	// variable another one reassigned.
	tests := []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"the manifest", func(t *testing.T, dir string) { writeTestFile(t, dir, "vise.toml", "[vise]\nversion = 1\n# edited\n") }},
		{"the lockfile", func(t *testing.T, dir string) { writeTestFile(t, dir, "vise.lock", "{\"v\":1,\"probes\":{}}") }},
		{"the journal", func(t *testing.T, dir string) {
			writeTestFile(t, dir, filepath.Join(".vise", "journal.jsonl"), "{\"e\":\"gate\"}\n{\"e\":\"gate\"}\n")
		}},
		{"a blob added", func(t *testing.T, dir string) {
			writeTestFile(t, dir, filepath.Join(".vise", "blobs", strings.Repeat("b", 64)), "two")
		}},
		{"a blob removed", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, ".vise", "blobs", strings.Repeat("a", 64))); err != nil {
				t.Fatal(err)
			}
		}},
		{"a blob's content overwritten in place", func(t *testing.T, dir string) {
			// Same filename, different bytes. The store is content-addressed,
			// so the name no longer matches the content — and hashing the name
			// alone left this invisible, which is what the guard's message about
			// "blobs" promised it would catch.
			writeTestFile(t, dir, filepath.Join(".vise", "blobs", strings.Repeat("a", 64)), "tampered")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Each subtest works on its own copy of the state.
			snapshot := t.TempDir()
			if err := copyTree(filepath.Join(root), snapshot); err != nil {
				t.Fatal(err)
			}
			before, err := evaluatorStateDigest(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if before != base {
				t.Fatalf("the copied state digests differently: %s vs %s", before, base)
			}
			test.change(t, snapshot)

			after, err := evaluatorStateDigest(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if after == before {
				t.Fatalf("changing %s did not change the evaluator digest", test.name)
			}
		})
	}
}

func copyTree(from, to string) error {
	return filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// The declared timeout must be the timeout. Forcing every probe to two seconds
// left the suite green, because no test compared a configured value against
// the time a probe was actually allowed.
func TestAProbeIsAllowedTheTimeoutItDeclares(t *testing.T) {
	root := testGitRepo(t)
	runner := Runner{Root: root}

	// A probe that outlives a one-second budget is killed at about one second,
	// not at some constant the runner chose for itself.
	start := time.Now()
	result := runner.RunProbe(Probe{ID: "slow", Run: "sleep 30", Timeout: 1}, false)
	elapsed := time.Since(start)
	if !result.TimedOut {
		t.Fatalf("a probe that sleeps 30s under a 1s timeout was not timed out: %#v", result)
	}
	// An upper bound close to the declared value, not a generous one: a runner
	// that ignored the declaration and used a constant of its own would sit
	// comfortably inside a five-second allowance and prove nothing.
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("a 1s timeout took %v to fire", elapsed)
	}
	if !strings.Contains(result.HarnessError, "timed out after 1s") {
		t.Fatalf("the message does not name the declared timeout: %q", result.HarnessError)
	}

	// And a probe that runs longer than any plausible constant, but well
	// inside its own declared budget, must not be killed. This is the half
	// that catches a runner using a fixed timeout: a 4s probe survives a
	// declared 20s and dies under anything shorter.
	start = time.Now()
	result = runner.RunProbe(Probe{ID: "patient", Run: "sleep 4; printf done", Timeout: 20}, false)
	if result.TimedOut || result.HarnessError != "" {
		t.Fatalf("a 4s probe under a 20s timeout was cut short after %v: %#v", time.Since(start), result)
	}
	if got := string(result.Stdout.Prefix); got != "done" {
		t.Fatalf("the probe did not run to completion: %q", got)
	}
}

// firstShellDiagnostic picks the shell's own not-found line out of stderr, so
// exit 127 can name the missing tool instead of merely reporting that
// something failed. Bypassing it, or taking the first line of stderr whatever
// it says, left the suite green — because the only test went through a probe
// whose stderr had one line in it.
func TestTheLaunchFailureNamesTheToolAndNotTheNoise(t *testing.T) {
	tests := []struct {
		name    string
		stderr  string
		command string
		wantIn  string
		wantOut string
	}{
		{
			name:    "a warning before the diagnostic",
			stderr:  "warning: something unrelated\nsh: mytool: command not found\n",
			command: "mytool --version",
			wantIn:  "mytool: command not found",
			wantOut: "warning: something unrelated",
		},
		{
			name:    "the other phrasing",
			stderr:  "sh: 1: othertool: No such file or directory\n",
			command: "othertool run",
			wantIn:  "othertool",
		},
		{
			name:    "no diagnostic at all falls back to the command",
			stderr:  "some unrelated noise\n",
			command: "thirdtool --flag",
			wantIn:  `"thirdtool" is not on its PATH`,
			wantOut: "some unrelated noise",
		},
		{
			name:    "a compound command names its first word",
			stderr:  "",
			command: "fourthtool | grep x",
			wantIn:  `"fourthtool"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := launchFailureDetail("probe", test.command, CaptureBytes([]byte(test.stderr)))
			if !strings.Contains(got, "127") {
				t.Fatalf("the message does not say it was a launch failure: %q", got)
			}
			if !strings.Contains(got, test.wantIn) {
				t.Fatalf("message %q does not contain %q", got, test.wantIn)
			}
			if test.wantOut != "" && strings.Contains(got, test.wantOut) {
				t.Fatalf("message %q carried noise %q", got, test.wantOut)
			}
		})
	}

	// And it is bounded, because a probe's stderr is not.
	long := "sh: " + strings.Repeat("x", 4000) + ": command not found\n"
	if got := launchFailureDetail("probe", "x", CaptureBytes([]byte(long))); len(got) > 400 {
		t.Fatalf("a 4000-character diagnostic rendered %d characters", len(got))
	}
}

// A state file replaced by a symlink to identical bytes is a mutation the guard
// used to miss: os.ReadFile followed the link and hashed the target, so the
// digest did not move — while the checkout now had a vise.lock pointing outside
// the repository. readRegularFile refuses the symlink, so the digest call fails
// instead, which classes the run as harness. hash.go's TamperHash already used
// readRegularFile; the guard did not, and the two disagreed on the same check.
func TestTheGuardRefusesAStateFileSwappedForASymlink(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n")
	writeTestFile(t, root, "vise.lock", "{\"v\":1}")
	if _, err := evaluatorStateDigest(root); err != nil {
		t.Fatalf("a clean state failed to digest: %v", err)
	}

	// An identical-content file elsewhere, and vise.lock pointed at it.
	decoy := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(decoy, []byte("{\"v\":1}"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "vise.lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, lock); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if _, err := evaluatorStateDigest(root); err == nil {
		t.Error("a symlinked vise.lock digested as though it were the file")
	}
}

// Absent and empty must not hash alike. A probe that creates an empty vise.lock
// where none existed has changed the evaluator's state, and writeHashPart frames
// nil and a zero-byte file identically, so without a presence marker the digest
// did not move.
func TestTheGuardTellsAbsentFromEmpty(t *testing.T) {
	withLock := testGitRepo(t)
	writeTestFile(t, withLock, "vise.toml", "[vise]\nversion = 1\n")
	writeTestFile(t, withLock, "vise.lock", "")

	withoutLock := testGitRepo(t)
	writeTestFile(t, withoutLock, "vise.toml", "[vise]\nversion = 1\n")

	empty, err := evaluatorStateDigest(withLock)
	if err != nil {
		t.Fatal(err)
	}
	absent, err := evaluatorStateDigest(withoutLock)
	if err != nil {
		t.Fatal(err)
	}
	if empty == absent {
		t.Error("an empty vise.lock and an absent one produced the same digest")
	}
}
