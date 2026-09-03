package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NakliTechie/vise/internal/vise"

	core "github.com/NakliTechie/vise/internal/vise"
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
	root := cliRepo(t, basicManifest(`
[[probe]]
id = "steady"
run = "printf steady"
`), "#!/bin/sh\nprintf stable")
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
	for attempt := 3; attempt <= 5; attempt++ {
		exit, stdout, _ := cliRun(t, root, "gate", "--json")
		value := parseCLIJSON(t, stdout)
		if exit != 2 || value["next"].(map[string]any)["action"] != "human" {
			t.Fatalf("attempt %d: refusal must persist, got exit=%d value=%#v", attempt, exit, value)
		}
	}
	// Running the unstable probe on its own does not renew the budget. It used
	// to: the chain was keyed to the exact probe set, so every subset had its
	// own two reruns and an agent diagnosing with --probe walked into a fresh
	// one without meaning to. The budget follows the probe that flaked.
	exit, stdout, _ := cliRun(t, root, "verify", "--probe", "behavior", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("running the flaky probe alone renewed the budget: exit=%d value=%#v", exit, value)
	}
	exit, stdout, _ = cliRun(t, root, "gate", "--json")
	value = parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("gate after single-probe flakes must stay refused: exit=%d value=%#v", exit, value)
	}
	// A green verdict for a different probe set is not a boundary for the
	// full-set chain either.
	if exit, _, _ := cliRun(t, root, "gate", "--probe", "steady", "--json"); exit != 0 {
		t.Fatalf("single stable probe gate: exit=%d", exit)
	}
	exit, stdout, _ = cliRun(t, root, "gate", "--json")
	value = parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("gate after a green single-probe verify must stay refused: exit=%d value=%#v", exit, value)
	}
	if exit, stdout, _ := cliRun(t, root, "status", "--json"); exit != 0 || !strings.Contains(stdout, `"state":"rerun-refused"`) {
		t.Fatalf("status must report the refusal: %d %s", exit, stdout)
	}
	events, err := core.ReadJournal(root, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Event == "gate" && event.Verdict == "indeterminate" && len(event.Flaky) == 0 {
			t.Fatalf("rerun refusal was journaled: %#v", event)
		}
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

func TestStubChangeIsEnvironmentDriftNotBehavior(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf '%s' \"$VISE_SEED\"")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	cliWrite(t, root, "vise.toml", strings.Replace(basicManifest(""), `seed = "1729"`, `seed = "42"`, 1))
	exit, stdout, _ := cliRun(t, root, "gate", "--json")
	value := parseCLIJSON(t, stdout)
	failures, _ := value["failures"].(map[string]any)
	fingerprint, _ := failures["fingerprint"].(map[string]any)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" || fingerprint == nil || !strings.Contains(fingerprint["detail"].(string), "[stubs]") {
		t.Fatalf("stub change: exit=%d value=%#v", exit, value)
	}
	if _, behavior := failures["behavior"]; behavior {
		t.Fatalf("stub change was classed as a probe failure: %#v", failures)
	}
	if exit, stdout, _ := cliRun(t, root, "status", "--json"); exit != 0 || !strings.Contains(stdout, `"state":"environment-drift"`) {
		t.Fatalf("status: %d %s", exit, stdout)
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
	counts := value["counts"].(map[string]any)
	if counts["declared"] != 2.0 || counts["pass"] != 1.0 || counts["metric"] != 1.0 {
		t.Fatalf("metric counts must include the metric in the denominator: %#v", counts)
	}
	if _, text, _ := cliRun(t, root, "gate"); !strings.Contains(text, "GATE RED [metric] — 1/2: complexity") {
		t.Fatalf("gate line = %q", text)
	}
}

func TestDirtyRecordGuardAndOverride(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	cliWrite(t, root, "untracked.txt", "dirty")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "clean working tree") {
		t.Fatalf("guard: %d %q", exit, stderr)
	}
	exit, stdout, _ := cliRun(t, root, "record", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" || !strings.Contains(value["next"].(map[string]any)["detail"].(string), "--allow-dirty") {
		t.Fatalf("guard json: %d %#v", exit, value)
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
	exit, stdout, _ := cliRun(t, root, "record", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" || !strings.Contains(value["next"].(map[string]any)["detail"].(string), "--i-reviewed-the-diff") {
		t.Fatalf("review json: %d %#v", exit, value)
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
	if exit, stdout, stderr := cliRun(t, root, "status"); exit != 0 || !strings.Contains(stdout, "UNRECORDED") || !strings.Contains(stdout, "an operator declares a probe") || strings.Contains(stdout, "run vise init") || stderr != "" {
		t.Fatalf("status: %d %q %q", exit, stdout, stderr)
	}
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "at least one") || !strings.Contains(stderr, "0/0") {
		t.Fatalf("record empty: %d %q", exit, stderr)
	}
	exit, stdout, _ := cliRun(t, root, "record", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" || !strings.Contains(value["next"].(map[string]any)["detail"].(string), "an operator declares a probe") {
		t.Fatalf("record empty json: %d %#v", exit, value)
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

func TestVerifySingleProbeAndMissingBlob(t *testing.T) {
	manifest := `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "one"
run = "./one.sh"
[[probe]]
id = "two"
run = "./two.sh"
`
	root := cliRepo(t, manifest, "")
	cliWrite(t, root, "one.sh", "#!/bin/sh\nprintf one")
	cliWrite(t, root, "two.sh", "#!/bin/sh\nprintf two")
	if err := os.Chmod(filepath.Join(root, "one.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "two.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "probes")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}

	cliWrite(t, root, "two.sh", "#!/bin/sh\nprintf changed")
	if exit, stdout, stderr := cliRun(t, root, "verify", "--probe", "one"); exit != 0 || !strings.Contains(stdout, "1/1") || stderr != "" {
		t.Fatalf("single: %d %q %q", exit, stdout, stderr)
	}
	if exit, _, stderr := cliRun(t, root, "verify"); exit != 1 || !strings.Contains(stderr, "two [behavior]") {
		t.Fatalf("full: %d %q", exit, stderr)
	}

	cliWrite(t, root, "two.sh", "#!/bin/sh\nprintf two")
	lock, _, err := core.LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := core.BlobPath(root, lock.Probes["one"].Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	if exit, _, stderr := cliRun(t, root, "verify", "--probe", "one"); exit != 2 || !strings.Contains(stderr, "tamper-hash") {
		t.Fatalf("missing blob: %d %q", exit, stderr)
	}
	exit, stdout, stderr := cliRun(t, root, "status", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 0 || value["state"] != "harness-error" || stderr != "" {
		t.Fatalf("status after missing blob: %d %#v %q", exit, value, stderr)
	}
}

func TestRecordRejectsFlakeAndLaunchFailure(t *testing.T) {
	t.Run("flake", func(t *testing.T) {
		root := cliRepo(t, basicManifest(""), "#!/bin/sh\nif test -f .toggle; then rm .toggle; printf b; else touch .toggle; printf a; fi")
		exit, stdout, _ := cliRun(t, root, "record", "--json")
		value := parseCLIJSON(t, stdout)
		if exit != 3 || value["verdict"] != "indeterminate" {
			t.Fatalf("record: %d %#v", exit, value)
		}
		if _, err := os.Stat(filepath.Join(root, "vise.lock")); !os.IsNotExist(err) {
			t.Fatalf("lock exists after flake: %v", err)
		}
	})
	t.Run("exit-127", func(t *testing.T) {
		root := cliRepo(t, basicManifest(""), "#!/bin/sh\ncommand-that-does-not-exist")
		exit, stdout, _ := cliRun(t, root, "record", "--json")
		value := parseCLIJSON(t, stdout)
		if exit != 2 || value["verdict"] != "indeterminate" {
			t.Fatalf("record: %d %#v", exit, value)
		}
	})
	t.Run("fingerprint", func(t *testing.T) {
		manifest := `[vise]
version = 1
[stubs]
network = "declared-off"
[env]
fingerprint = ["missing-command-for-record"]
[[probe]]
id = "behavior"
run = "printf stable"
`
		root := cliRepo(t, manifest, "")
		exit, stdout, _ := cliRun(t, root, "record", "--json")
		value := parseCLIJSON(t, stdout)
		if exit != 2 || value["next"].(map[string]any)["action"] != "human" || !strings.Contains(value["next"].(map[string]any)["detail"].(string), "fingerprint") {
			t.Fatalf("fingerprint: %d %#v", exit, value)
		}
	})
}

func TestRunBinaryJSONAndProposalStatus(t *testing.T) {
	manifest := `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "binary"
run = "printf '\\377'"
`
	root := cliRepo(t, manifest, "")
	cliWrite(t, root, ".vise/proposals.toml", "[[probe]]\nid='escaped-defect'\nrun='printf fixed'\n")
	exit, stdout, stderr := cliRun(t, root, "run", "binary", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 0 || value["stdout_base64"] != "/w==" || stderr != "" {
		t.Fatalf("run: %d %#v %q", exit, value, stderr)
	}
	exit, stdout, stderr = cliRun(t, root, "status", "--json")
	value = parseCLIJSON(t, stdout)
	if exit != 0 || value["pending_proposals"] != float64(1) || stderr != "" {
		t.Fatalf("status: %d %#v %q", exit, value, stderr)
	}
}

func TestInitNeverOverwrites(t *testing.T) {
	root := cliRepo(t, "", "")
	if exit, _, stderr := cliRun(t, root, "init"); exit != 0 {
		t.Fatalf("first init: %d %q", exit, stderr)
	}
	before, err := os.ReadFile(filepath.Join(root, "vise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(before), "[[probe]]") > strings.Index(string(before), "[stubs]") {
		t.Fatal("starter manifest does not lead with the first probe")
	}
	if exit, _, stderr := cliRun(t, root, "init"); exit != 2 || !strings.Contains(stderr, "never overwrites") {
		t.Fatalf("second init: %d %q", exit, stderr)
	}
	after, err := os.ReadFile(filepath.Join(root, "vise.toml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("manifest changed: %v", err)
	}
}

func TestRecordIsByteReproducibleOnSameHead(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("first: %d %q", exit, stderr)
	}
	first, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if exit, _, stderr := cliRun(t, root, "record", "--allow-dirty", "--i-reviewed-the-diff"); exit != 0 {
		t.Fatalf("second: %d %q", exit, stderr)
	}
	second, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("lockfile bytes changed:\nfirst=%s\nsecond=%s", first, second)
	}
}

func TestRunMirrorsLaunchFailureExit(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nexec definitely-not-a-command-xyz")
	exit, _, stderr := cliRun(t, root, "run", "behavior")
	if exit != 127 || !strings.Contains(stderr, "not found") {
		t.Fatalf("run: exit=%d stderr=%q", exit, stderr)
	}
	exit, stdout, _ := cliRun(t, root, "run", "behavior", "--json")
	if value := parseCLIJSON(t, stdout); exit != 127 || value["exit"] != 127.0 {
		t.Fatalf("run --json: exit=%d value=%#v", exit, value)
	}
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "exit 127") {
		t.Fatalf("record must still refuse a launch failure: exit=%d stderr=%q", exit, stderr)
	}
}

func TestEmptyManifestNeverGatesGreen(t *testing.T) {
	root := cliRepo(t, "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n", "")
	cliWrite(t, root, "vise.lock", "{\n  \"v\": 1,\n  \"fingerprint\": {\"os\": \""+runtime.GOOS+"\", \"arch\": \""+runtime.GOARCH+"\", \"stubs\": {\"tz\": \"UTC\", \"lang\": \"C\", \"seed\": \"1729\", \"network\": \"declared-off\"}},\n  \"probes\": {}\n}\n")
	exit, stdout, _ := cliRun(t, root, "gate", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["verdict"] == "green" || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("empty manifest gate: exit=%d value=%#v", exit, value)
	}
	// status routes to human, not fix_probe: the repair is in vise.toml,
	// which the agent contract forbids the agent from writing.
	if exit, stdout, _ := cliRun(t, root, "status", "--json"); exit != 0 || strings.Contains(stdout, `"state":"ready"`) || !strings.Contains(stdout, `"action":"human"`) {
		t.Fatalf("empty manifest status must not say ready: %d %s", exit, stdout)
	}
}

func TestGreenVerifyEndsAFlakeChainAndHarnessStopsReportPassZero(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	flaky := "#!/bin/sh\nif [ -f .toggle ]; then rm .toggle; printf b; else touch .toggle; printf a; fi"
	cliWrite(t, root, "probe.sh", flaky)
	if exit, _, _ := cliRun(t, root, "verify", "--json"); exit != 3 {
		t.Fatalf("first flaky verify: %d", exit)
	}
	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf stable")
	os.Remove(filepath.Join(root, ".toggle"))
	if exit, _, _ := cliRun(t, root, "verify", "--json"); exit != 0 {
		t.Fatalf("green verify: %d", exit)
	}
	cliWrite(t, root, "probe.sh", flaky)
	if exit, _, _ := cliRun(t, root, "verify", "--json"); exit != 3 {
		t.Fatalf("flaky verify after a green one must be the first of a new chain: %d", exit)
	}
	if exit, _, _ := cliRun(t, root, "verify", "--json"); exit != 3 {
		t.Fatalf("second flaky verify: %d", exit)
	}
	// A verify that stops before judging (unknown probe) carries no lock and
	// must neither end the chain nor be journaled as a boundary.
	if exit, _, _ := cliRun(t, root, "verify", "--probe", "bogus", "--json"); exit != 2 {
		t.Fatalf("bogus probe verify: %d", exit)
	}
	if exit, _, _ := cliRun(t, root, "verify", "--json"); exit != 2 {
		t.Fatalf("third flaky verify must be refused: %d", exit)
	}

	// A fingerprint failure stops judgment before any probe runs: pass is 0.
	cliWrite(t, root, "vise.toml", strings.Replace(basicManifest(""), `seed = "1729"`, `seed = "7"`, 1))
	exit, stdout, _ := cliRun(t, root, "verify", "--json")
	value := parseCLIJSON(t, stdout)
	counts := value["counts"].(map[string]any)
	if exit != 2 || counts["pass"] != 0.0 || counts["declared"] != 1.0 {
		t.Fatalf("harness stop counts: exit=%d counts=%#v", exit, counts)
	}
}

func TestMetricDefinitionChangeIsHarnessNotImprovement(t *testing.T) {
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
	// Replacing the analyzer with one that prints 0 used to gate green as an
	// improvement from 10 to 0. It is a changed judge: harness, fix_probe.
	cliWrite(t, root, "vise.toml", strings.Replace(manifest, `run = "cat metric.txt"`, `run = "printf 0"`, 1))
	exit, stdout, _ := cliRun(t, root, "gate", "--json")
	value := parseCLIJSON(t, stdout)
	failure, _ := value["failures"].(map[string]any)["complexity"].(map[string]any)
	if exit != 2 || failure == nil || failure["class"] != "harness" || !strings.Contains(failure["detail"].(string), "metric definition changed") {
		t.Fatalf("metric definition change: exit=%d value=%#v", exit, value)
	}
	if exit, stdout, _ := cliRun(t, root, "status", "--json"); exit != 0 || !strings.Contains(stdout, `"state":"baseline-drift"`) || !strings.Contains(stdout, "complexity: metric definition changed") {
		t.Fatalf("status: %d %s", exit, stdout)
	}
	// A lock recorded before definitions were frozen reports drift, not green.
	cliWrite(t, root, "vise.toml", manifest)
	var lock map[string]any
	if err := json.Unmarshal(cliReadFile(t, root, "vise.lock"), &lock); err != nil {
		t.Fatal(err)
	}
	delete(lock["metrics"].(map[string]any)["complexity"].(map[string]any), "run_hash")
	legacy, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	cliWrite(t, root, "vise.lock", string(legacy))
	exit, stdout, _ = cliRun(t, root, "gate", "--json")
	value = parseCLIJSON(t, stdout)
	failure, _ = value["failures"].(map[string]any)["complexity"].(map[string]any)
	if exit != 2 || failure == nil || !strings.Contains(failure["detail"].(string), "not frozen") {
		t.Fatalf("legacy lock: exit=%d value=%#v", exit, value)
	}
}

func cliReadFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRecordPreviewThenAcceptWritesOnlyTheReviewedCandidate(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "baseline")
	before := cliReadFile(t, root, "vise.lock")
	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf changed")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "intended change")

	exit, stdout, _ := cliRun(t, root, "record", "--preview", "--json")
	value := parseCLIJSON(t, stdout)
	candidate, _ := value["candidate"].(string)
	diff, _ := value["review_diff"].(string)
	if exit != 0 || candidate == "" || !strings.Contains(diff, "-stable") || !strings.Contains(diff, "+changed") || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("preview: exit=%d value=%#v", exit, value)
	}
	if string(cliReadFile(t, root, "vise.lock")) != string(before) {
		t.Fatal("preview wrote vise.lock")
	}
	if exit, stdout, _ := cliRun(t, root, "record", "--accept", "sha256:0000", "--json"); exit != 2 || !strings.Contains(stdout, "differs from the accepted") {
		t.Fatalf("wrong digest: %d %s", exit, stdout)
	}
	if string(cliReadFile(t, root, "vise.lock")) != string(before) {
		t.Fatal("a refused accept wrote vise.lock")
	}
	if exit, _, stderr := cliRun(t, root, "record", "--accept", candidate); exit != 0 {
		t.Fatalf("accept: %d %s", exit, stderr)
	}
	if string(cliReadFile(t, root, "vise.lock")) == string(before) {
		t.Fatal("accept did not write the candidate")
	}
	if exit, _, _ := cliRun(t, root, "gate", "--quiet"); exit != 0 {
		t.Fatalf("gate after accept: %d", exit)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "accepted baseline")
	// A plain record still needs a gesture, and the human preview writes nothing.
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "--preview") {
		t.Fatalf("gesture: %d %s", exit, stderr)
	}
	if exit, stdout, _ := cliRun(t, root, "record", "--preview"); exit != 0 || !strings.Contains(stdout, "CANDIDATE BASELINE") || !strings.Contains(stdout, "candidate: sha256:") {
		t.Fatalf("human preview: %d %s", exit, stdout)
	}
	if exit, _, stderr := cliRun(t, root, "record", "--preview", "--accept", candidate); exit != 2 || !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("exclusive flags: %d %s", exit, stderr)
	}
	if exit, _, stderr := cliRun(t, root, "record", "--accept", ""); exit != 2 || !strings.Contains(stderr, "needs the candidate digest") {
		t.Fatalf("empty accept: %d %s", exit, stderr)
	}
}

func TestReviewDiffIsTerminalSafeAndVersionRejectsArguments(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf old")
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "baseline")
	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf '\\033[2Jnew'")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "escape")
	_, stdout, _ := cliRun(t, root, "record", "--i-reviewed-the-diff")
	if strings.ContainsRune(stdout, 0x1b) || !strings.Contains(stdout, `[2Jnew`) {
		t.Fatalf("review diff reached the terminal raw: %q", stdout)
	}
	if exit, _, stderr := cliRun(t, root, "version", "extra"); exit != 2 || !strings.Contains(stderr, "no arguments") {
		t.Fatalf("version extra: %d %s", exit, stderr)
	}
	if exit, stdout, _ := cliRun(t, root, "record", "--preview", "--allow-dirty"); exit != 0 || !strings.Contains(stdout, "no baseline state written") {
		t.Fatalf("preview wording: %d %s", exit, stdout)
	}
}

func TestRunPrintsCompleteOutputWhileRecordBoundsIt(t *testing.T) {
	const size = 1 << 20
	manifest := `[vise]
version = 1

[stubs]
network = "declared-off"

[[probe]]
id = "noisy"
run = "dd if=/dev/zero bs=65536 count=16 2>/dev/null"
timeout = 60
`
	root := cliRepo(t, manifest, "")
	// Raw run streams every byte, however large.
	exit, stdout, _ := cliRun(t, root, "run", "noisy")
	if exit != 0 || len(stdout) != size {
		t.Fatalf("run printed %d bytes, want %d (exit %d)", len(stdout), size, exit)
	}
	// JSON mode reports the bounded prefix and says the stream was larger.
	exit, stdout, _ = cliRun(t, root, "run", "noisy", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 0 || value["stdout_truncated"] != true || value["stdout_size"] != float64(size) {
		t.Fatalf("run --json: exit=%d truncated=%v size=%v", exit, value["stdout_truncated"], value["stdout_size"])
	}
	if hash, _ := value["stdout_hash"].(string); !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("run --json carried no stream hash: %#v", value)
	}

	// The baseline holds the hash, not the bytes: no blob is written for it.
	if exit, _, stderr := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, stderr)
	}
	lock := string(cliReadFile(t, root, "vise.lock"))
	if !strings.Contains(lock, `"stdout_large": true`) {
		t.Fatalf("lockfile did not mark the observation hash-only: %s", lock)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".vise", "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > int64(core.CaptureLimit) {
			t.Fatalf("blob %s is %d bytes, larger than the capture bound", entry.Name(), info.Size())
		}
	}
	if exit, _, _ := cliRun(t, root, "gate", "--quiet"); exit != 0 {
		t.Fatalf("gate on a bounded observation: %d", exit)
	}
}

func TestVersionJSONIdentifiesTheBuild(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	exit, stdout, _ := cliRun(t, root, "version", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 0 || value["version"] != Version {
		t.Fatalf("version --json: exit=%d value=%#v", exit, value)
	}
	// Built from a VCS checkout, the response must say which commit it is:
	// two binaries with the same Version string are otherwise indistinguishable.
	if _, ok := buildIdentity()["revision"]; ok {
		if revision, _ := value["revision"].(string); len(revision) != 40 {
			t.Fatalf("revision = %q, want a full object name", revision)
		}
	}
	// Plain `vise version` stays stable so it remains usable as a probe.
	if exit, stdout, _ := cliRun(t, root, "version"); exit != 0 || strings.TrimSpace(stdout) != "vise "+Version {
		t.Fatalf("plain version: %d %q", exit, stdout)
	}
}

func TestStatusJSONNamesTheToolButHumanStatusDoesNot(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	exit, stdout, _ := cliRun(t, root, "status", "--json")
	value := parseCLIJSON(t, stdout)
	tool, _ := value["tool"].(map[string]any)
	if exit != 0 || tool == nil || tool["version"] != Version {
		t.Fatalf("status --json tool = %#v", value["tool"])
	}
	// The human rendering stays free of build identity so it remains stable
	// enough to be a probe surface.
	exit, stdout, _ = cliRun(t, root, "status")
	if exit != 0 || strings.Contains(stdout, Version) {
		t.Fatalf("human status leaked build identity: %q", stdout)
	}
}

func TestStatusWritesNothingAtAll(t *testing.T) {
	root := t.TempDir()
	cliGit(t, root, "init", "-q")
	cliGit(t, root, "config", "user.email", "cli-tests@example.invalid")
	cliGit(t, root, "config", "user.name", "cli tests")
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if exit, _, _ := cliRun(t, root, "status", "--json"); exit != 0 {
		t.Fatalf("status: %d", exit)
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		names := make([]string, 0, len(after))
		for _, entry := range after {
			names = append(names, entry.Name())
		}
		t.Fatalf("status created state in a repository that had none: %v", names)
	}
	if _, err := os.Lstat(filepath.Join(root, ".vise")); !os.IsNotExist(err) {
		t.Fatalf(".vise exists after status: %v", err)
	}
}

func TestHelpAnswersJSONToo(t *testing.T) {
	root := t.TempDir()
	exit, stdout, _ := cliRun(t, root, "--help", "--json")
	value := parseCLIJSON(t, stdout)
	commands, _ := value["commands"].(map[string]any)
	if exit != 0 || value["cmd"] != "help" || commands["gate"] == nil {
		t.Fatalf("help --json: exit=%d value=%#v", exit, value)
	}
	if _, ok := value["global_options"]; !ok {
		t.Fatalf("help --json omitted the global options: %#v", value)
	}

	exit, stdout, _ = cliRun(t, root, "record", "--help", "--json")
	value = parseCLIJSON(t, stdout)
	if exit != 0 || value["command"] != "record" || !strings.Contains(value["usage"].(string), "--preview") {
		t.Fatalf("record --help --json: exit=%d value=%#v", exit, value)
	}

	// The human rendering is unchanged, so it stays usable as a probe surface.
	exit, stdout, _ = cliRun(t, root, "--help")
	if exit != 0 || !strings.Contains(stdout, "deterministic behavior locks") {
		t.Fatalf("human help: %d %q", exit, stdout)
	}
}

func TestAnUnencodableVerdictIsReportedAsHarness(t *testing.T) {
	// A metric delta of +Inf cannot be JSON: reporting it as a green verdict
	// with a missing field is the one failure a caller cannot detect.
	outcome := core.NewOutcome("gate")
	outcome.Counts.Declared = 1
	outcome.Metrics = map[string]core.MetricDelta{
		"complexity": {Base: 1, Now: math.Inf(1), Delta: math.Inf(1), Direction: "down", Enforce: "none"},
	}
	outcome.Finalize()

	var out bytes.Buffer
	exit := writeOutcomeJSON(&out, outcome, nil)
	if exit != core.ExitHarness {
		t.Fatalf("exit = %d, want harness", exit)
	}
	value := parseCLIJSON(t, out.String())
	failures, _ := value["failures"].(map[string]any)
	encoding, _ := failures["encoding"].(map[string]any)
	if value["verdict"] != "indeterminate" || encoding == nil || !strings.Contains(encoding["detail"].(string), "could not be encoded") {
		t.Fatalf("value = %#v", value)
	}

	// An ordinary outcome still round-trips and keeps its own exit code.
	plain := core.NewOutcome("gate")
	plain.Counts.Declared = 1
	plain.AddFailure("p", core.Failure{Class: "behavior", Detail: "differs"})
	plain.Finalize()
	out.Reset()
	if exit := writeOutcomeJSON(&out, plain, map[string]any{"extra": true}); exit != core.ExitBehavior {
		t.Fatalf("plain exit = %d", exit)
	}
	if value := parseCLIJSON(t, out.String()); value["verdict"] != "red" || value["extra"] != true {
		t.Fatalf("plain value = %#v", value)
	}
}

// doctor is an operator's read-only question about the repository. Like
// status it always exits 0: its findings are advice to a human, not a verdict
// an agent branches on, and giving it an exit code would put a seventh
// meaning into a vocabulary whose whole value is that each code names one
// next action.
func TestDoctorAlwaysExitsZeroAndAnswersJSON(t *testing.T) {
	root := t.TempDir()
	cliGit(t, root, "init", "-q")
	cliGit(t, root, "config", "user.email", "vise-tests@example.invalid")
	cliGit(t, root, "config", "user.name", "vise tests")
	cliWrite(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")

	var out, errOut bytes.Buffer
	if exit := Run([]string{"doctor", "--json"}, root, &out, &errOut); exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %s)", exit, errOut.String())
	}
	var document struct {
		Cmd      string `json:"cmd"`
		Ready    bool   `json:"ready"`
		Findings []struct {
			Check  string `json:"check"`
			Remedy string `json:"remedy"`
		} `json:"findings"`
		Next struct {
			Action string `json:"action"`
		} `json:"next"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("doctor --json is not one object: %v\n%s", err, out.String())
	}
	if document.Cmd != "doctor" || document.Ready {
		t.Fatalf("document = %#v", document)
	}
	if len(document.Findings) == 0 {
		t.Fatal("doctor found nothing in a repository with no fingerprint, no contract, and no baseline")
	}
	for _, finding := range document.Findings {
		if finding.Remedy == "" {
			t.Fatalf("finding %q carries no remedy", finding.Check)
		}
	}
	if document.Next.Action != "human" {
		t.Fatalf("next.action = %q", document.Next.Action)
	}

	// A positional argument is a mistake worth naming rather than ignoring.
	out.Reset()
	errOut.Reset()
	if exit := Run([]string{"doctor", "extra"}, root, &out, &errOut); exit == 0 {
		t.Fatal("doctor accepted a positional argument")
	}
}

// Help has one source. The top-level list and the per-command usage used to be
// separate strings, which is how a command exists and is undocumented.
func TestEveryCommandAppearsInBothRenderingsOfHelp(t *testing.T) {
	var human bytes.Buffer
	if exit := Run([]string{"--help"}, t.TempDir(), &human, io.Discard); exit != 0 {
		t.Fatalf("help exit = %d", exit)
	}
	var machine bytes.Buffer
	if exit := Run([]string{"--help", "--json"}, t.TempDir(), &machine, io.Discard); exit != 0 {
		t.Fatalf("help --json exit = %d", exit)
	}
	var document struct {
		Commands map[string]string `json:"commands"`
	}
	if err := json.Unmarshal(machine.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	for _, entry := range commands {
		if !strings.Contains(human.String(), entry.Name) {
			t.Errorf("%q is missing from the human help", entry.Name)
		}
		if _, ok := document.Commands[entry.Name]; !ok {
			t.Errorf("%q is missing from the JSON help", entry.Name)
		}
	}
	if len(document.Commands) != len(commands) {
		t.Errorf("JSON help lists %d commands, the table has %d", len(document.Commands), len(commands))
	}
}

// The moment you most want to ask what is happening is while something is
// happening. status and doctor report a situation without changing it, so
// neither may queue behind a record or a gate that holds the state lock.
func TestReadOnlyCommandsDoNotWaitForARunInProgress(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "printf hello")
	held, err := core.AcquireStateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	for _, command := range []string{"status", "doctor"} {
		done := make(chan int, 1)
		go func() {
			var out, errOut bytes.Buffer
			done <- Run([]string{command, "--json"}, root, &out, &errOut)
		}()
		select {
		case exit := <-done:
			if exit != 0 {
				t.Fatalf("%s exited %d while the lock was held", command, exit)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("%s blocked on the state lock", command)
		}
	}
}

// A typo used to reach for the state lock before anyone checked the word was a
// command, so `vise recrod` waited as long as the running suite. Found by a
// probe that ran an unknown command inside a record.
func TestAnUnknownCommandIsRefusedBeforeTheStateLock(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "printf hello")
	held, err := core.AcquireStateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	done := make(chan int, 1)
	go func() {
		var out, errOut bytes.Buffer
		done <- Run([]string{"no-such-command"}, root, &out, &errOut)
	}()
	select {
	case exit := <-done:
		if exit == 0 {
			t.Fatal("an unknown command exited 0")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("an unknown command blocked on the state lock")
	}
}

// doctor says of itself that it always exits 0. A directory that is not a Git
// work tree is the most likely first thing anyone types after reading about
// the command, and it was answering that with a harness error — so the promise
// had an exception nobody had written down.
func TestDoctorOutsideAGitWorkTreeStillExitsZero(t *testing.T) {
	outside := t.TempDir()
	for _, args := range [][]string{{"doctor"}, {"doctor", "--json"}} {
		var out, errOut bytes.Buffer
		if exit := Run(args, outside, &out, &errOut); exit != 0 {
			t.Fatalf("%v exited %d outside a work tree: %s%s", args, exit, out.String(), errOut.String())
		}
		if !strings.Contains(out.String(), "git-work-tree") {
			t.Fatalf("%v did not name the cause: %s", args, out.String())
		}
	}
}

// `vise recrod --help` printed the top-level help and exited 0, answering a
// typo with a page that never mentions the typo.
func TestHelpForAnUnknownCommandRefusesIt(t *testing.T) {
	var out, errOut bytes.Buffer
	exit := Run([]string{"no-such-command", "--help"}, t.TempDir(), &out, &errOut)
	if exit == 0 {
		t.Fatalf("an unknown command's help exited 0: %s", out.String())
	}
	if !strings.Contains(out.String()+errOut.String(), "unknown command") {
		t.Fatalf("the refusal does not name the problem: %s%s", out.String(), errOut.String())
	}

	// A real command's help must still work, anywhere, with no repository.
	out.Reset()
	errOut.Reset()
	if exit := Run([]string{"gate", "--help"}, t.TempDir(), &out, &errOut); exit != 0 {
		t.Fatalf("gate --help exited %d", exit)
	}
	if !strings.Contains(out.String(), "vise gate") {
		t.Fatalf("gate --help printed %q", out.String())
	}
}

// The README's command table is where someone decides whether vise does the
// thing they came for. A command missing from it does not exist as far as they
// are concerned.
//
// The list is the same table the CLI dispatches on, not a copy: a hand-copied
// list in a test stops matching the code the moment somebody adds a command,
// and then the guard passes while the new surface is undocumented — the exact
// failure the guard is for, reproduced one level up. The search is scoped to
// the table, because searching the whole file passed with the row deleted.
func TestEveryCommandIsInTheReadmeTable(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Skipf("README.md unavailable: %v", err)
	}
	table := readmeSection(t, string(readme), "## Commands")
	for _, entry := range commands {
		if !strings.Contains(table, "`vise "+entry.Name+"`") && !strings.Contains(table, "`vise "+entry.Name+" ") {
			t.Errorf("vise %s is a command and the README table does not list it", entry.Name)
		}
	}
}

func readmeSection(t *testing.T, document, heading string) string {
	t.Helper()
	start := strings.Index(document, heading)
	if start < 0 {
		t.Fatalf("%q is not in the document", heading)
	}
	rest := document[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// Eight mutations of the CLI's argument validation survived the suite: init,
// status and run accepting extra arguments; record, verify and gate accepting
// positional ones; verify accepting --quiet; and three commands ignoring flag
// parse errors outright. A command that silently accepts what it does not
// understand is worse than one that refuses: the agent believes it asked for
// something it did not get.
func TestEveryCommandRefusesWhatItDoesNotUnderstand(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "printf hello")

	tests := []struct {
		name string
		args []string
	}{
		{"init with an argument", []string{"init", "extra"}},
		{"status with an argument", []string{"status", "extra"}},
		{"doctor with an argument", []string{"doctor", "extra"}},
		{"version with an argument", []string{"version", "extra"}},
		{"run with no probe", []string{"run"}},
		{"run with two probes", []string{"run", "a", "b"}},
		{"record with a positional argument", []string{"record", "extra"}},
		{"verify with a positional argument", []string{"verify", "extra"}},
		{"gate with a positional argument", []string{"gate", "extra"}},
		{"verify with --quiet, which is only for gate", []string{"verify", "--quiet"}},
		{"record with an unknown flag", []string{"record", "--no-such-flag"}},
		{"verify with an unknown flag", []string{"verify", "--no-such-flag"}},
		{"gate with an unknown flag", []string{"gate", "--no-such-flag"}},
		{"record --accept with no digest", []string{"record", "--accept"}},
		{"verify --probe with no id", []string{"verify", "--probe"}},
		{"an unknown command", []string{"no-such-command"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			exit := Run(test.args, root, &out, &errOut)
			if exit == 0 {
				t.Fatalf("%v was accepted (exit 0)\nstdout: %s\nstderr: %s", test.args, out.String(), errOut.String())
			}
			// And the refusal has to say something, in either rendering.
			if out.Len() == 0 && errOut.Len() == 0 {
				t.Fatalf("%v was refused silently", test.args)
			}
		})
	}
}

// The same refusals in JSON mode, where an agent reads them. A refusal that is
// not valid JSON under --json is a refusal the caller cannot parse.
func TestRefusalsAreStillOneJSONObject(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "printf hello")

	for _, args := range [][]string{
		{"status", "extra", "--json"},
		{"gate", "extra", "--json"},
		{"run", "--json"},
		{"no-such-command", "--json"},
	} {
		var out, errOut bytes.Buffer
		if exit := Run(args, root, &out, &errOut); exit == 0 {
			t.Fatalf("%v was accepted", args)
		}
		text := out.String() + errOut.String()
		var document map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &document); err != nil {
			t.Fatalf("%v did not refuse in JSON: %v\n%s", args, err, text)
		}
		if _, ok := document["next"]; !ok {
			t.Fatalf("%v refused without a next action: %s", args, text)
		}
	}
}

// `vise run --json` is how an agent inspects one probe without judgment, and
// its object is the whole answer. Emptying the captured output, emptying the
// artifact hashes, and emptying the probe id all left the suite green: the
// existing cases asserted the metadata around the values and never the values.
func TestRunJSONCarriesEveryFieldOfTheObservation(t *testing.T) {
	manifest := basicManifest("files = [\"out/result.txt\"]\n")
	root := cliRepo(t, manifest, "mkdir -p out && printf produced > out/result.txt && printf hello && printf oops >&2 && exit 3")

	exit, stdout, _ := cliRun(t, root, "run", "behavior", "--json")
	if exit != 3 {
		t.Fatalf("run exit = %d, want the probe's own 3", exit)
	}
	document := parseCLIJSON(t, stdout)

	if document["probe"] != "behavior" {
		t.Fatalf("probe = %v", document["probe"])
	}
	if document["exit"] != float64(3) {
		t.Fatalf("exit = %v, want 3", document["exit"])
	}
	if got, _ := document["stdout"].(string); got != "hello" {
		t.Fatalf("stdout = %q, want the bytes the probe printed", got)
	}
	if got, _ := document["stderr"].(string); got != "oops" {
		t.Fatalf("stderr = %q, want the bytes the probe printed", got)
	}
	// The hashes are what a baseline is made of, so an empty one is not a
	// missing detail but a different observation entirely.
	for _, field := range []string{"stdout_hash", "stderr_hash"} {
		got, _ := document[field].(string)
		if !strings.HasPrefix(got, "sha256:") {
			t.Fatalf("%s = %q", field, got)
		}
	}
	files, ok := document["files"].(map[string]any)
	if !ok || len(files) != 1 {
		t.Fatalf("files = %v, want the one declared artifact", document["files"])
	}
	hash, _ := files["out/result.txt"].(string)
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("the artifact hash is %q", hash)
	}
}

// A malformed vise.toml or vise.lock is the operator's to repair, and the
// agent contract forbids an agent from writing either. Both reached the agent
// as fix_probe — "repair the harness" — leaving no legal move. The Operator
// flag existed and these paths did not set it, which is the same conflict
// fixed in two places out of three.
func TestMalformedOperatorStateTellsTheAgentToStop(t *testing.T) {
	t.Run("a manifest that will not parse", func(t *testing.T) {
		root := t.TempDir()
		cliGit(t, root, "init", "-q")
		cliGit(t, root, "config", "user.email", "vise-tests@example.invalid")
		cliGit(t, root, "config", "user.name", "vise tests")
		cliWrite(t, root, "vise.toml", "not = [valid\n")

		for _, command := range []string{"gate", "verify", "record"} {
			exit, stdout, stderr := cliRun(t, root, command, "--json")
			if exit == 0 {
				t.Fatalf("%s accepted a malformed manifest", command)
			}
			document := parseCLIJSON(t, stdout+stderr)
			next, _ := document["next"].(map[string]any)
			if next["action"] != "human" {
				t.Fatalf("%s said %v for a malformed manifest; the repair is in vise.toml, which an agent may not write", command, next["action"])
			}
		}
	})

	t.Run("a lockfile that will not parse", func(t *testing.T) {
		root := cliRepo(t, basicManifest(""), "printf hello")
		if exit, _, _ := cliRun(t, root, "record", "--json"); exit != 0 {
			t.Fatal("record failed")
		}
		cliWrite(t, root, "vise.lock", "not json at all\n")

		exit, stdout, stderr := cliRun(t, root, "gate", "--json")
		if exit == 0 {
			t.Fatal("gate accepted a malformed lockfile")
		}
		document := parseCLIJSON(t, stdout+stderr)
		next, _ := document["next"].(map[string]any)
		if next["action"] != "human" {
			t.Fatalf("gate said %v for a malformed lockfile", next["action"])
		}
	})
}

// The other direction, which is the one that costs an agent a turn. The guard
// above stops a command from going undocumented; nothing stopped the table
// from naming a command that does not exist. An agent reads the table, runs
// what it says, and gets a usage error back from the tool that is supposed to
// be telling it what to do — and the tool is right, so it has no way to learn
// the document was wrong.
//
// vise map is the live risk here rather than a hypothetical: SPEC.md names it
// as a v0 non-goal, so the word is already in the repository, one careless
// paste from the table.
func TestTheReadmeTableNamesNoCommandThatDoesNotExist(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Skipf("README.md unavailable: %v", err)
	}
	table := readmeSection(t, string(readme), "## Commands")
	quoted := regexp.MustCompile("`vise ([a-z][a-z-]*)")
	for _, match := range quoted.FindAllStringSubmatch(table, -1) {
		if !isKnownCommand(match[1]) {
			t.Errorf("the README table offers `vise %s` and the CLI has no such command", match[1])
		}
	}
}

// Two calls used to answer the wrong question outside a repository, because
// the repository was resolved before the command line was judged.
//
// `vise status bogus` returned a status report and exit 0. status promises
// exit 0 for any call it understands, and that promise had quietly grown to
// cover a call it does not understand. `vise no-such-command` complained about
// the repository rather than the typo, which sends someone looking in the
// wrong place. A usage error is a complaint about the command line and does
// not depend on where the caller is standing.
func TestAUsageErrorIsAUsageErrorOutsideARepositoryToo(t *testing.T) {
	outside := t.TempDir() // deliberately not a git work tree
	for _, call := range []struct {
		name string
		args []string
		says string
	}{
		{"status with a positional argument", []string{"status", "bogus"}, "positional"},
		{"doctor with a positional argument", []string{"doctor", "bogus"}, "positional"},
		// The exit code was already 2 here, so asserting it alone proved
		// nothing: what was wrong was the message. Outside a repository a typo
		// rendered as NO-SUCH-COMMAND INDETERMINATE [harness] 0/1, framing the
		// typo as a probe that had failed, and complained about the missing
		// repository rather than the word the caller had just mistyped.
		{"an unknown command", []string{"no-such-command"}, "unknown command"},
	} {
		t.Run(call.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := Run(call.args, outside, &stdout, &stderr)
			if exit != vise.ExitHarness {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", exit, vise.ExitHarness, stdout.String(), stderr.String())
			}
			said := stdout.String() + stderr.String()
			if !strings.Contains(said, call.says) {
				t.Errorf("the complaint never mentions %q, so it is about the wrong thing:\n%s", call.says, said)
			}
		})
	}
}

// And the report it does understand there names the one action a human can
// act on. It used to say fix_probe: repair the probe your change broke. There
// is no probe, no manifest, and nothing an agent can repair by standing in a
// directory that is not a repository.
func TestStatusOutsideARepositoryDoesNotAskAnAgentToFixAProbe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"--json", "status"}, t.TempDir(), &stdout, &stderr); exit != vise.ExitOK {
		t.Fatalf("exit %d, want 0: %s", exit, stderr.String())
	}
	var report struct {
		State string `json:"state"`
		Next  struct {
			Action string `json:"action"`
		} `json:"next"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.State != "no-git" {
		t.Fatalf("state %q, want no-git", report.State)
	}
	if report.Next.Action != vise.NextHuman {
		t.Errorf("next.action %q outside a repository, want %q", report.Next.Action, vise.NextHuman)
	}
}

// The contract asks an agent to report which tool answered it, and the two
// commands that state the tool's identity disagreed about the same binary.
// status always carried a tool object; version dropped revision and modified
// entirely on a build with no VCS stamps, which is what an install from a
// tarball or a `-buildvcs=false` build produces.
//
// Worse than the disagreement: status reported `"modified": false` on such a
// build — a claim that the tree was clean, from a binary with no way to know.
// The field is a pointer now, so unknown has a representation, and this asserts
// the two commands say the same thing whichever kind of build runs the test.
func TestVersionAndStatusAgreeAboutTheBinaryAnswering(t *testing.T) {
	root := cliRepo(t, "", "")

	var versionOut bytes.Buffer
	if exit := Run([]string{"--json", "version"}, root, &versionOut, io.Discard); exit != vise.ExitOK {
		t.Fatalf("version exit %d", exit)
	}
	var version map[string]any
	if err := json.Unmarshal(versionOut.Bytes(), &version); err != nil {
		t.Fatal(err)
	}

	var statusOut bytes.Buffer
	if exit := Run([]string{"--json", "status"}, root, &statusOut, io.Discard); exit != vise.ExitOK {
		t.Fatalf("status exit %d", exit)
	}
	var status struct {
		Tool map[string]any `json:"tool"`
	}
	if err := json.Unmarshal(statusOut.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Tool == nil {
		t.Fatal("status reported no tool identity")
	}

	for _, field := range []string{"version", "revision", "modified"} {
		fromVersion, inVersion := version[field]
		fromStatus, inStatus := status.Tool[field]
		if inVersion != inStatus {
			t.Errorf("%q is present in one command and absent in the other: version=%v status=%v", field, inVersion, inStatus)
			continue
		}
		if inVersion && fromVersion != fromStatus {
			t.Errorf("%q disagrees: version says %v, status says %v", field, fromVersion, fromStatus)
		}
	}
}
