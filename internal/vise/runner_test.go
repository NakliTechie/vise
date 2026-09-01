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
