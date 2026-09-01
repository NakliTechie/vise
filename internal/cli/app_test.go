package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func cliRepo(t *testing.T, manifest, script string) string {
	t.Helper()
	root := t.TempDir()
	cliGit(t, root, "init", "-q")
	cliGit(t, root, "config", "user.email", "cli-tests@example.invalid")
	cliGit(t, root, "config", "user.name", "cli tests")
	cliWrite(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.toggle\n")
	if manifest != "" {
		cliWrite(t, root, "vise.toml", manifest)
	}
	if script != "" {
		cliWrite(t, root, "probe.sh", script)
		if err := os.Chmod(filepath.Join(root, "probe.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "fixture")
	return root
}

func basicManifest(extra string) string {
	return `[vise]
version = 1

[stubs]
tz = "UTC"
lang = "C"
seed = "1729"
network = "declared-off"

[[probe]]
id = "behavior"
run = "./probe.sh"
timeout = 5
` + extra
}

func cliWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cliGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func cliRun(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := Run(args, root, &stdout, &stderr)
	return exit, stdout.String(), stderr.String()
}

func parseCLIJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("JSON %q: %v", text, err)
	}
	return value
}

func TestRecordVerifyGateAndBehaviorDiff(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	exit, stdout, stderr := cliRun(t, root, "record")
	if exit != 0 || !strings.Contains(stdout, "RECORDED") || stderr != "" {
		t.Fatalf("record: %d %q %q", exit, stdout, stderr)
	}

	exit, stdout, stderr = cliRun(t, root, "verify")
	if exit != 0 || !strings.Contains(stdout, "VERIFY GREEN") || stderr != "" {
		t.Fatalf("verify: %d %q %q", exit, stdout, stderr)
	}

	exit, stdout, stderr = cliRun(t, root, "gate", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 0 || value["verdict"] != "green" || stderr != "" {
		t.Fatalf("gate: %d %#v %q", exit, value, stderr)
	}

	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf changed")
	exit, _, stderr = cliRun(t, root, "verify")
	if exit != 1 || !strings.Contains(stderr, "[behavior]") || !strings.Contains(stderr, "expected/stdio") && !strings.Contains(stderr, "expected/stdout") {
		t.Fatalf("behavior verify: %d %q", exit, stderr)
	}
}

func TestExpectedNonzeroExitIsBehavior(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf expected >&2\nexit 7")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	if exit, stdout, stderr := cliRun(t, root, "verify"); exit != 0 || !strings.Contains(stdout, "VERIFY GREEN") || stderr != "" {
		t.Fatalf("verify: %d %q %q", exit, stdout, stderr)
	}
	if exit, _, stderr := cliRun(t, root, "run", "behavior"); exit != 7 || !strings.Contains(stderr, "expected") {
		t.Fatalf("run: %d %q", exit, stderr)
	}
}

func TestFlakeIsIndeterminateAndThirdRerunRefused(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	cliWrite(t, root, "probe.sh", "#!/bin/sh\nif [ -f .toggle ]; then rm .toggle; printf b; else touch .toggle; printf a; fi")
	for attempt := 1; attempt <= 2; attempt++ {
		exit, stdout, _ := cliRun(t, root, "gate", "--json")
		value := parseCLIJSON(t, stdout)
		if exit != 3 || value["verdict"] != "indeterminate" {
			t.Fatalf("attempt %d: exit=%d value=%#v", attempt, exit, value)
		}
	}
	exit, stdout, _ := cliRun(t, root, "gate", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("third: exit=%d value=%#v", exit, value)
	}
}

func TestDependencyAndFingerprintDriftAreHarnessFailures(t *testing.T) {
	manifest := `[vise]
version = 1
[stubs]
network = "declared-off"
[env]
fingerprint = ["cat tool-version"]
[[probe]]
id = "behavior"
run = "cat fixture.txt"
deps = ["fixture.txt"]
`
	root := cliRepo(t, manifest, "")
	cliWrite(t, root, "fixture.txt", "one")
	cliWrite(t, root, "tool-version", "v1")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "inputs")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}

	cliWrite(t, root, "fixture.txt", "two")
	if exit, _, stderr := cliRun(t, root, "verify"); exit != 2 || !strings.Contains(stderr, "declared probe input changed") {
		t.Fatalf("dependency: %d %q", exit, stderr)
	}
	cliWrite(t, root, "fixture.txt", "one")
	cliWrite(t, root, "tool-version", "v2")
	exit, stdout, _ := cliRun(t, root, "verify", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("fingerprint: %d %#v", exit, value)
	}
}

func TestMetricNoRegress(t *testing.T) {
	manifest := basicManifest(`
[[metric]]
id = "complexity"
run = "cat metric.txt"
direction = "down"
enforce = "no-regress"
version_cmd = "printf analyzer-1"
`)
	root := cliRepo(t, manifest, "#!/bin/sh\nprintf stable")
	cliWrite(t, root, "metric.txt", "10")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "metric")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	cliWrite(t, root, "metric.txt", "12")
	exit, stdout, _ := cliRun(t, root, "gate", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 5 || value["verdict"] != "red" {
		t.Fatalf("metric: %d %#v", exit, value)
	}
}

func TestDirtyRecordGuardAndOverride(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	cliWrite(t, root, "untracked.txt", "dirty")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "clean working tree") {
		t.Fatalf("guard: %d %q", exit, stderr)
	}
	if exit, stdout, stderr := cliRun(t, root, "record", "--allow-dirty"); exit != 0 || !strings.Contains(stdout, "RECORDED") || stderr != "" {
		t.Fatalf("override: %d %q %q", exit, stdout, stderr)
	}
}

func TestOverwriteRequiresReviewGestureAndPrintsDiffFirst(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf old")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	cliGit(t, root, "add", "vise.lock", ".vise/blobs")
	cliGit(t, root, "commit", "-qm", "baseline")
	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf new")
	cliGit(t, root, "add", "probe.sh")
	cliGit(t, root, "commit", "-qm", "new behavior")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "--i-reviewed-the-diff") {
		t.Fatalf("review guard: %d %q", exit, stderr)
	}
	exit, stdout, stderr := cliRun(t, root, "record", "--i-reviewed-the-diff")
	if exit != 0 || !strings.HasPrefix(stdout, "BEHAVIOR DIFF UNDER REVIEW") || !strings.Contains(stdout, "RECORDED") || stderr != "" {
		t.Fatalf("reviewed: %d %q %q", exit, stdout, stderr)
	}
}

func TestInitStatusAndEmptyManifestRecordRemedy(t *testing.T) {
	root := cliRepo(t, "", "")
	if exit, stdout, stderr := cliRun(t, root, "init"); exit != 0 || !strings.Contains(stdout, "INITIALIZED") || stderr != "" {
		t.Fatalf("init: %d %q %q", exit, stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil || !strings.Contains(string(data), ".vise/journal.jsonl") {
		t.Fatalf("gitignore: %v %q", err, data)
	}
	if exit, stdout, stderr := cliRun(t, root, "status"); exit != 0 || !strings.Contains(stdout, "UNRECORDED") || stderr != "" {
		t.Fatalf("status: %d %q %q", exit, stdout, stderr)
	}
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "at least one") {
		t.Fatalf("record empty: %d %q", exit, stderr)
	}
}

func TestHelpAndStatusOutsideGit(t *testing.T) {
	root := t.TempDir()
	if exit, stdout, stderr := cliRun(t, root, "--help"); exit != 0 || !strings.Contains(stdout, "Usage:") || stderr != "" {
		t.Fatalf("help: %d %q %q", exit, stdout, stderr)
	}
	exit, stdout, stderr := cliRun(t, root, "status", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 0 || value["state"] != "no-git" || stderr != "" {
		t.Fatalf("status: %d %#v %q", exit, value, stderr)
	}
}
