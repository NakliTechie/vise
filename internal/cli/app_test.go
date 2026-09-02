package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	// A single-probe verify is a different probe set: it gets its own two
	// reruns, and its flakes neither reset nor extend the full-set chain.
	for attempt := 1; attempt <= 2; attempt++ {
		exit, stdout, _ := cliRun(t, root, "verify", "--probe", "behavior", "--json")
		value := parseCLIJSON(t, stdout)
		if exit != 3 || value["verdict"] != "indeterminate" {
			t.Fatalf("single-probe attempt %d: exit=%d value=%#v", attempt, exit, value)
		}
	}
	exit, stdout, _ := cliRun(t, root, "verify", "--probe", "behavior", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "human" {
		t.Fatalf("single-probe third: exit=%d value=%#v", exit, value)
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
	if exit, stdout, stderr := cliRun(t, root, "status"); exit != 0 || !strings.Contains(stdout, "UNRECORDED") || !strings.Contains(stdout, "declare at least one probe") || strings.Contains(stdout, "run vise init") || stderr != "" {
		t.Fatalf("status: %d %q %q", exit, stdout, stderr)
	}
	if exit, _, stderr := cliRun(t, root, "record"); exit != 2 || !strings.Contains(stderr, "at least one") || !strings.Contains(stderr, "0/0") {
		t.Fatalf("record empty: %d %q", exit, stderr)
	}
	exit, stdout, _ := cliRun(t, root, "record", "--json")
	value := parseCLIJSON(t, stdout)
	if exit != 2 || value["next"].(map[string]any)["action"] != "fix_probe" || !strings.Contains(value["next"].(map[string]any)["detail"].(string), "declare at least one probe") {
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
		if exit != 2 || value["next"].(map[string]any)["action"] != "fix_probe" || !strings.Contains(value["next"].(map[string]any)["detail"].(string), "fingerprint") {
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
	if exit != 2 || value["verdict"] == "green" || value["next"].(map[string]any)["action"] != "fix_probe" {
		t.Fatalf("empty manifest gate: exit=%d value=%#v", exit, value)
	}
	if exit, stdout, _ := cliRun(t, root, "status", "--json"); exit != 0 || strings.Contains(stdout, `"state":"ready"`) || !strings.Contains(stdout, `"action":"fix_probe"`) {
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
