package vise

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerSanitizesEnvironment(t *testing.T) {
	root := testGitRepo(t)
	t.Setenv("VISE_TEST_SECRET", "must-not-leak")
	probe := Probe{ID: "env", Run: `printf '%s|%s|%s|%s' "$TZ" "$LANG" "${VISE_TEST_SECRET-unset}" "$CUSTOM"`, Timeout: 5, Env: map[string]string{"CUSTOM": "declared"}}
	manifest := testManifest(probe)
	result := (Runner{Root: root, Manifest: manifest}).RunProbe(probe, false)
	if result.HarnessError != "" {
		t.Fatal(result.HarnessError)
	}
	if got := string(result.Stdout.Prefix); got != "UTC|C|unset|declared" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestRunnerDeletesAndCapturesArtifacts(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "out/result.txt", "stale")
	probe := Probe{ID: "artifact", Run: `test ! -e out/result.txt && mkdir -p out && printf fresh > out/result.txt`, Timeout: 5, Files: []string{"out/result.txt"}}
	manifest := testManifest(probe)
	result := (Runner{Root: root, Manifest: manifest}).RunProbe(probe, false)
	if result.HarnessError != "" {
		t.Fatal(result.HarnessError)
	}
	if got := string(result.Files["out/result.txt"].Prefix); got != "fresh" {
		t.Fatalf("artifact = %q", got)
	}
}

func TestRunnerRejectsMissingArtifact(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "missing", Run: "true", Timeout: 5, Files: []string{"out/missing"}}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if !strings.Contains(result.HarnessError, "was not produced") {
		t.Fatalf("harness error = %q", result.HarnessError)
	}
}

func TestRunnerRefusesDirectoryArtifactWithoutDeletingIt(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "out/keep.txt", "keep")
	probe := Probe{ID: "directory", Run: "true", Timeout: 5, Files: []string{"out"}}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if !strings.Contains(result.HarnessError, "recursive deletion is refused") {
		t.Fatalf("harness error = %q", result.HarnessError)
	}
	if _, err := os.Stat(filepath.Join(root, "out/keep.txt")); err != nil {
		t.Fatalf("directory content was removed: %v", err)
	}
}

func TestRunnerDetectsTrackedMutation(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "mutation", Run: "printf changed > tracked.txt", Timeout: 5}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, true)
	if result.HarnessError != "probe modified tracked files" {
		t.Fatalf("harness error = %q", result.HarnessError)
	}
}

func TestRunnerKillsProcessGroupOnTimeout(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "timeout", Run: "sleep 20 & echo $! > child.pid; wait", Timeout: 1}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if !result.TimedOut || !strings.Contains(result.HarnessError, "timed out") {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d still exists: %v", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMetricParsingAndVersion(t *testing.T) {
	root := testGitRepo(t)
	metric := Metric{ID: "complexity", Run: "printf 12.5", Direction: "down", Enforce: "none", VersionCmd: "printf tool-1", Timeout: 5}
	result := (Runner{Root: root, Manifest: testManifest(Probe{ID: "p", Run: "true", Timeout: 5})}).RunMetric(metric)
	if result.HarnessError != "" || result.Value != 12.5 || result.ToolVersion != "tool-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMetricDetectsTrackedMutation(t *testing.T) {
	root := testGitRepo(t)
	metric := Metric{ID: "mutation", Run: "printf changed > tracked.txt; printf 1", Direction: "down", Enforce: "none", Timeout: 5}
	result := (Runner{Root: root, Manifest: testManifest(Probe{ID: "p", Run: "true", Timeout: 5})}).RunMetric(metric)
	if result.HarnessError != "metric modified tracked files" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerReturnsWhenDetachedChildHoldsStdout(t *testing.T) {
	root := testGitRepo(t)
	// perl leaves the process group via setsid, so the timeout kill cannot
	// reach it, and it inherits the probe's stdout pipe.
	detached := "perl -MPOSIX -e 'POSIX::setsid(); sleep 6' & printf started"
	probe := Probe{ID: "detached", Run: detached, Timeout: 30}
	start := time.Now()
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("probe took %v; vise waited on the detached child", elapsed)
	}
	if result.TimedOut || !strings.Contains(result.HarnessError, "background process") || string(result.Stdout.Prefix) != "started" {
		t.Fatalf("result = %#v", result)
	}

	probe = Probe{ID: "detached-timeout", Run: "perl -MPOSIX -e 'POSIX::setsid(); sleep 6' & sleep 30", Timeout: 1}
	start = time.Now()
	result = (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timed-out probe took %v; vise waited on the detached child", elapsed)
	}
	if !result.TimedOut || !strings.Contains(result.HarnessError, "timed out") {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerRefusesTrackedArtifactWithoutDeletingIt(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "artifact", Run: "printf regenerated > tracked.txt", Timeout: 5, Files: []string{"tracked.txt"}}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if !strings.Contains(result.HarnessError, `"tracked.txt" is tracked by git`) {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || string(data) != "original\n" {
		t.Fatalf("tracked file was touched: %q, %v", data, err)
	}
}

func TestKillActiveProbeGroupStopsTheRunningProbe(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "interrupted", Run: "sleep 20 & echo $! > child.pid; wait", Timeout: 30}
	results := make(chan RunResult, 1)
	go func() { results <- (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false) }()
	pidPath := filepath.Join(root, "child.pid")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(pidPath); err == nil && activeProbeGroup.Load() != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	KillActiveProbeGroup()
	select {
	case result := <-results:
		if result.TimedOut || result.Exit == 0 {
			t.Fatalf("result = %#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunProbe did not return after the group was killed")
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the group kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if activeProbeGroup.Load() != 0 {
		t.Fatal("active group was not cleared")
	}
}

func TestProbeScratchLivesUnderViseTmpAndIsWiped(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "scratch", Run: "printf '%s' \"$VISE_TMP\"; printf data > \"$VISE_TMP/file\"", Timeout: 5}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, true)
	if result.HarnessError != "" || result.Exit != 0 {
		t.Fatalf("result = %#v", result)
	}
	scratch := string(result.Stdout.Prefix)
	if !strings.HasPrefix(scratch, filepath.Join(root, ".vise", "tmp")+string(filepath.Separator)) {
		t.Fatalf("VISE_TMP = %q, want it under .vise/tmp", scratch)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch dir survived the run: %v", err)
	}
	dirty, err := GitDirty(root)
	if err != nil || dirty {
		t.Fatalf("scratch made the tree dirty: dirty=%t err=%v", dirty, err)
	}
}

func TestRunnerKillsLeftoverGroupMembersAfterTheShellExits(t *testing.T) {
	root := testGitRepo(t)
	// A redirected background child keeps no pipe open, so Wait returns as
	// soon as the shell exits; the child must still not survive the run.
	probe := Probe{ID: "leftover", Run: "(sleep 1; printf late > late.txt) >/dev/null 2>&1 & printf done", Timeout: 10}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, true)
	if result.HarnessError != "" || string(result.Stdout.Prefix) != "done" {
		t.Fatalf("result = %#v", result)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "late.txt")); !os.IsNotExist(err) {
		t.Fatalf("background child outlived the run and wrote late.txt: %v", err)
	}
}

func TestProbeScratchRefusesSymlinkedTmp(t *testing.T) {
	root := testGitRepo(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vise"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".vise", "tmp")); err != nil {
		t.Fatal(err)
	}
	probe := Probe{ID: "escape", Run: "printf '%s' \"$VISE_TMP\"", Timeout: 5}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if !strings.Contains(result.HarnessError, "real directory") {
		t.Fatalf("result = %#v", result)
	}
	if err := atomicWrite(root, filepath.Join(root, "vise.lock"), []byte("x"), 0o644); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("atomicWrite through a symlinked staging dir: %v", err)
	}
}

func TestProbeThatTouchesEvaluatorStateIsHarness(t *testing.T) {
	root := testGitRepo(t)
	if err := AppendJournal(root, JournalEvent{Event: "flake", Commit: "c", Lock: "l", Probes: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]string{
		"journal":  "rm .vise/journal.jsonl; printf done",
		"lockfile": "printf x > vise.lock; printf done",
		"manifest": "printf '[vise]\\nversion = 1\\n' > vise.toml; printf done",
	} {
		probe := Probe{ID: name, Run: run, Timeout: 5}
		result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
		if !strings.Contains(result.HarnessError, "probe modified vise state") {
			t.Fatalf("%s: result = %#v", name, result)
		}
		os.Remove(filepath.Join(root, "vise.lock"))
		os.Remove(filepath.Join(root, "vise.toml"))
	}
	if err := AppendJournal(root, JournalEvent{Event: "flake", Commit: "c", Lock: "l", Probes: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	metric := Metric{ID: "m", Run: "rm -f .vise/journal.jsonl; printf 1", Timeout: 5, Direction: "down", Enforce: "none"}
	if result := (Runner{Root: root, Manifest: testManifest()}).RunMetric(metric); !strings.Contains(result.HarnessError, "probe modified vise state") {
		t.Fatalf("metric: %#v", result)
	}
}

func TestProbeStartedAfterInterruptIsKilledOnRegistration(t *testing.T) {
	root := testGitRepo(t)
	interrupted.Store(true)
	defer interrupted.Store(false)
	probe := Probe{ID: "late", Run: "sleep 20 & echo $! > child.pid; wait", Timeout: 30}
	start := time.Now()
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("probe ran for %v after an interrupt", elapsed)
	}
	if !strings.Contains(result.HarnessError, "interrupted before the probe started") {
		t.Fatalf("probe was started after an interrupt: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "child.pid"))
	if err != nil {
		return // the probe never started; nothing to reap
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the interrupt", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProbeTempDirectoryIsPinnedToTheProbeScratch(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "tmp", Run: "printf '%s|%s' \"$TMPDIR\" \"$VISE_TMP\"", Timeout: 5}
	result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if result.HarnessError != "" {
		t.Fatalf("result = %#v", result)
	}
	parts := strings.SplitN(string(result.Stdout.Prefix), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[0] != parts[1] {
		t.Fatalf("TMPDIR = %q, VISE_TMP = %q — they must be the same scratch", parts[0], parts[len(parts)-1])
	}
	if !strings.HasPrefix(parts[0], filepath.Join(root, ".vise", "tmp")+string(filepath.Separator)) {
		t.Fatalf("TMPDIR %q is not under the repository scratch", parts[0])
	}
}

// The tracked-file diff cannot see a file that was never tracked, so a probe
// that drops a stray into the checkout used to pass. That stray is visible to
// every later probe and every later build, which makes the baseline depend on
// the order runs happened in.
func TestAProbeThatWritesAnUntrackedFileIsAHarnessError(t *testing.T) {
	root := testGitRepo(t)
	runner := Runner{Root: root, Manifest: Manifest{}}

	result := runner.RunProbe(Probe{ID: "stray", Run: "printf stray > leftover.txt", Timeout: 30}, true)
	if !strings.Contains(result.HarnessError, "leftover.txt") {
		t.Fatalf("harness error %q does not name the stray file", result.HarnessError)
	}
	if !strings.Contains(result.HarnessError, "neither tracks nor ignores") {
		t.Fatalf("harness error %q does not say what the rule is", result.HarnessError)
	}
}

// An ignored path is the one place a probe is expected to write: a build
// cache, a compiler's scratch. .gitignore is where the operator already said
// which those are, so the snapshot must not second-guess it.
func TestAProbeMayWriteWhereGitIsToldToIgnore(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", "cache/\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "ignore cache")
	runner := Runner{Root: root, Manifest: Manifest{}}

	result := runner.RunProbe(Probe{ID: "cache", Run: "mkdir -p cache && printf warm > cache/warm", Timeout: 30}, true)
	if result.HarnessError != "" {
		t.Fatalf("writing an ignored path was refused: %s", result.HarnessError)
	}
}

// A declared artifact is untracked by rule, and vise deletes and recreates it
// on every run by design. Counting that as a stray would make every probe
// with a files = [...] entry fail.
func TestADeclaredArtifactIsNotAStray(t *testing.T) {
	root := testGitRepo(t)
	runner := Runner{Root: root, Manifest: Manifest{}}

	probe := Probe{ID: "artifact", Run: "mkdir -p out && printf produced > out/result.txt", Timeout: 30, Files: []string{"out/result.txt"}}
	result := runner.RunProbe(probe, true)
	if result.HarnessError != "" {
		t.Fatalf("a declared artifact was reported as a stray: %s", result.HarnessError)
	}
	if got := string(result.Files["out/result.txt"].Prefix); got != "produced" {
		t.Fatalf("artifact = %q", got)
	}
}

// A probe that deletes a stray it did not create is changing the checkout
// just as much as one that adds a file.
func TestAProbeThatRemovesAnUntrackedFileIsAHarnessError(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "scratch.txt", "was here")
	runner := Runner{Root: root, Manifest: Manifest{}}

	result := runner.RunProbe(Probe{ID: "remover", Run: "rm scratch.txt", Timeout: 30}, true)
	if !strings.Contains(result.HarnessError, "scratch.txt") {
		t.Fatalf("harness error %q does not name the removed file", result.HarnessError)
	}
}

// A probe that writes a stray fails the first run. On the second run the
// stray already exists and the probe rewrites it byte-for-byte, so comparing
// content alone would report green — turning "rerun it" into a way to launder
// a harness error, which is the one move the tool must never reward.
func TestRerunningDoesNotLaunderAStrayWrite(t *testing.T) {
	root := testGitRepo(t)
	runner := Runner{Root: root, Manifest: Manifest{}}
	probe := Probe{ID: "stray", Run: "printf leftover > leftover.txt", Timeout: 30}

	if first := runner.RunProbe(probe, true); !strings.Contains(first.HarnessError, "leftover.txt") {
		t.Fatalf("first run: %q", first.HarnessError)
	}
	second := runner.RunProbe(probe, true)
	if !strings.Contains(second.HarnessError, "leftover.txt") {
		t.Fatalf("rerunning laundered the stray write: %q", second.HarnessError)
	}
}

// The evaluator-state guard used to live in the callers, and two of them did
// not have it: the environment fingerprint commands had none at all, and a
// metric's version_cmd ran after RunMetric had already compared the state.
// Either could delete the journal, which is where the flake history and the
// rerun budget live — so "an agent cannot eject the judge by making a probe
// flaky" had a hole behind it. Found by a cold audit, with a green gate.
func TestEveryManifestCommandIsGuardedAgainstTouchingViseState(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		root := testGitRepo(t)
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
		if err := os.MkdirAll(filepath.Join(root, ".vise"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".vise", "journal.jsonl"), []byte("{\"e\":\"gate\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	// A command that erases the journal, which is exactly the flake history an
	// agent would want gone.
	erase := "rm -f .vise/journal.jsonl"

	t.Run("a probe", func(t *testing.T) {
		root := setup(t)
		result := Runner{Root: root}.RunProbe(Probe{ID: "p", Run: erase, Timeout: 30}, false)
		if !strings.Contains(result.HarnessError, "probe modified vise state") {
			t.Fatalf("harness error = %q", result.HarnessError)
		}
	})

	t.Run("a metric version command", func(t *testing.T) {
		root := setup(t)
		metric := Metric{ID: "m", Run: "printf 1", Timeout: 30, VersionCmd: erase}
		result := Runner{Root: root}.RunMetric(metric)
		if !strings.Contains(result.HarnessError, "probe modified vise state") {
			t.Fatalf("harness error = %q", result.HarnessError)
		}
	})

	t.Run("an environment fingerprint command", func(t *testing.T) {
		root := setup(t)
		manifest := Manifest{Environment: EnvironmentSettings{Fingerprint: []string{erase}}}
		_, err := CaptureFingerprint(root, manifest)
		if err == nil || !strings.Contains(err.Error(), "probe modified vise state") {
			t.Fatalf("fingerprint error = %v", err)
		}
	})
}

// The work-tree snapshot compared content and missed two ways a probe can
// change the checkout without changing any file's bytes. Found by a cold audit
// with the gate green. (A third, an empty directory left behind, is a stated
// limit: finding it needs a full walk of the checkout on every probe run.)
func TestTheSnapshotSeesChangesThatLeaveContentAlone(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		run   string
		want  string
	}{
		{
			name: "a permission change on a script",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, root, "helper.sh", "#!/bin/sh\nprintf helper\n")
				if err := os.Chmod(filepath.Join(root, "helper.sh"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			run:  "chmod +x helper.sh",
			want: "helper.sh",
		},
		{
			name: "a symlink pointed somewhere else",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, root, "one.txt", "one")
				writeTestFile(t, root, "two.txt", "two")
				if err := os.Symlink("one.txt", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			},
			run:  "rm link && ln -s two.txt link",
			want: "link",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := testGitRepo(t)
			test.setup(t, root)
			result := Runner{Root: root}.RunProbe(Probe{ID: "p", Run: test.run, Timeout: 30}, true)
			if !strings.Contains(result.HarnessError, test.want) {
				t.Fatalf("harness error %q does not name %q", result.HarnessError, test.want)
			}
		})
	}
}

// The lifecycle lock exists for one window: a signal arriving after a probe
// has been cleared to start and before its process group is registered.
// Without the lock the interrupt finds no group, kills nothing, and the probe
// outlives vise — still writing declared artifacts into a checkout nobody is
// judging any more. Removing the lock leaves every other test in this package
// green, so this one opens the window on purpose.
func TestAnInterruptInTheStartWindowStillKillsTheProbe(t *testing.T) {
	root := testGitRepo(t)
	defer interrupted.Store(false)

	entered := make(chan struct{})
	probeAboutToStart = func() {
		go func() {
			close(entered)
			InterruptProbes()
		}()
		// Give the goroutine time to reach the lock. With the lock held it
		// waits there until the group is registered; without it, it runs to
		// completion here and finds nothing to kill.
		<-entered
		time.Sleep(200 * time.Millisecond)
	}
	defer func() { probeAboutToStart = nil }()

	probe := Probe{ID: "window", Run: "sleep 3; printf survived > survived.txt", Timeout: 30}
	(Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)

	// Well past the probe's own sleep. If it was killed, the file never
	// appears; if it outlived the interrupt, it writes into a checkout vise
	// has stopped watching.
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(filepath.Join(root, "survived.txt")); err == nil {
		t.Fatal("a probe interrupted in the start window kept running and wrote its artifact")
	}
}

// Three conformance gaps between what SPEC promises and what the runner did.
func TestTheRunnerKeepsThePromisesTheSpecMakes(t *testing.T) {
	t.Run("a probe that fails is still checked for mutations", func(t *testing.T) {
		// A probe that writes a stray and then fails. The failure used to be
		// reported and the stray never looked for, so every probe after this
		// one ran against a checkout this one had changed.
		root := testGitRepo(t)
		probe := Probe{ID: "messy", Run: "printf stray > leftover.txt; exit 127", Timeout: 30}
		result := Runner{Root: root}.RunProbe(probe, true)

		if !strings.Contains(result.HarnessError, "leftover.txt") {
			t.Fatalf("a failing probe's stray write went unreported: %q", result.HarnessError)
		}
		// The original failure is kept: it is the cause, and the mutation is a
		// consequence worth naming beside it, not instead of it.
		if !strings.Contains(result.HarnessError, "127") {
			t.Fatalf("the original failure was lost: %q", result.HarnessError)
		}
	})

	t.Run("a symlink recreated at the same target is a change", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, "one.txt", "one")
		if err := os.Symlink("one.txt", filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		// Long enough that the recreated link gets a different timestamp on a
		// filesystem with coarse resolution.
		time.Sleep(1100 * time.Millisecond)

		probe := Probe{ID: "relink", Run: "rm link && ln -s one.txt link", Timeout: 30}
		result := Runner{Root: root}.RunProbe(probe, true)
		if !strings.Contains(result.HarnessError, "link") {
			t.Fatalf("a symlink removed and recreated went unreported: %q", result.HarnessError)
		}
	})

	t.Run("a missing tool is named, not just reported", func(t *testing.T) {
		root := testGitRepo(t)
		probe := Probe{ID: "absent", Run: "definitely-not-a-real-tool --version", Timeout: 30}
		result := Runner{Root: root}.RunProbe(probe, false)

		if !strings.Contains(result.HarnessError, "127") {
			t.Fatalf("a missing tool was not reported as a launch failure: %q", result.HarnessError)
		}
		if !strings.Contains(result.HarnessError, "definitely-not-a-real-tool") {
			t.Fatalf("the message does not name the tool that is missing: %q", result.HarnessError)
		}
	})
}
