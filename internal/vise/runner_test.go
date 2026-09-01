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
	if got := string(result.Stdout); got != "UTC|C|unset|declared" {
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
	if got := string(result.Files["out/result.txt"]); got != "fresh" {
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
	if result.TimedOut || !strings.Contains(result.HarnessError, "background process") || string(result.Stdout) != "started" {
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
