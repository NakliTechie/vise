package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func c23PinManifest() string {
	return `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "p"
run = "./probe.sh"
timeout = 5
expect.stdout = "spec/out"
`
}

func c23Repo(t *testing.T, script string) string {
	t.Helper()
	root := cliRepo(t, c23PinManifest(), script)
	cliWrite(t, root, "spec/out", "ok")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "pin spec")
	return root
}

func c23FailureDetail(t *testing.T, value map[string]any, id string) string {
	t.Helper()
	failures, ok := value["failures"].(map[string]any)
	if !ok {
		t.Fatalf("missing failures: %#v", value)
	}
	failure, ok := failures[id].(map[string]any)
	if !ok {
		t.Fatalf("missing %s failure: %#v", id, failures)
	}
	detail, _ := failure["detail"].(string)
	return detail
}

func c23AssertCapturedNotFound(t *testing.T, detail, mentioned string) {
	t.Helper()
	for _, want := range []string{"exited 127", "captured stderr", mentioned} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail omits %q: %q", want, detail)
		}
	}
	for _, unsupported := range []string{"install what", "not on its PATH", "shell named"} {
		if strings.Contains(detail, unsupported) {
			t.Errorf("detail authenticates application prose as launch evidence (%q): %q", unsupported, detail)
		}
	}
}

func TestC23ExecutedExit127StderrIsOnlyACapturedExcerptForUnacceptedPins(t *testing.T) {
	for _, diagnostic := range []string{"record not found", "sh: 1: fake: not found"} {
		t.Run(diagnostic, func(t *testing.T) {
			root := c23Repo(t, "#!/bin/sh\nprintf witness\nprintf '"+diagnostic+"' >&2\nexit 127\n")
			exit, out, errOut := cliRun(t, root, "record", "--json")
			if exit != 0 || errOut != "" {
				t.Fatalf("record: exit=%d out=%s stderr=%q", exit, out, errOut)
			}
			before, err := os.ReadFile(filepath.Join(root, "vise.lock"))
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"gate", "verify"} {
				exit, out, errOut = cliRun(t, root, command, "--json")
				value := parseCLIJSON(t, out)
				if exit != 6 || errOut != "" || value["verdict"] != "red" || value["next"].(map[string]any)["action"] != "build" {
					t.Fatalf("%s lifecycle: exit=%d value=%#v stderr=%q", command, exit, value, errOut)
				}
				c23AssertCapturedNotFound(t, c23FailureDetail(t, value, "p"), diagnostic)
				pins := value["pins"].(map[string]any)
				if pins["unmet_count"] != float64(1) || pins["passing_unaccepted_count"] != float64(0) {
					t.Fatalf("%s acceptance changed: %#v", command, pins)
				}
			}
			after, err := os.ReadFile(filepath.Join(root, "vise.lock"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("judgment changed lock: err=%v\nbefore=%s\nafter=%s", err, before, after)
			}
		})
	}
}

func TestC23AcceptedPinExit127StaysHardAndRetainsAcceptance(t *testing.T) {
	for _, diagnostic := range []string{"record not found", "sh: 1: fake: not found"} {
		t.Run(diagnostic, func(t *testing.T) {
			root := c23Repo(t, "#!/bin/sh\nprintf ok\n")
			if exit, out, errOut := cliRun(t, root, "record", "--json"); exit != 0 || errOut != "" {
				t.Fatalf("initial record: %d %s %q", exit, out, errOut)
			}
			cliGit(t, root, "add", ".")
			cliGit(t, root, "commit", "-qm", "record met pin")
			if exit, out, errOut := cliRun(t, root, "record", "--i-reviewed-the-diff", "--json"); exit != 0 || errOut != "" {
				t.Fatalf("accept: %d %s %q", exit, out, errOut)
			}
			if exit, out, errOut := cliRun(t, root, "gate", "--json"); exit != 0 || errOut != "" || parseCLIJSON(t, out)["verdict"] != "green" {
				t.Fatalf("accepted green control: exit=%d out=%s stderr=%q", exit, out, errOut)
			}
			statusExit, statusOut, statusErr := cliRun(t, root, "status", "--json")
			status := parseCLIJSON(t, statusOut)
			if statusExit != 0 || statusErr != "" || status["lock"].(map[string]any)["pins"].(map[string]any)["accepted"] != float64(1) {
				t.Fatalf("accepted status control: exit=%d value=%#v stderr=%q", statusExit, status, statusErr)
			}
			accepted, err := os.ReadFile(filepath.Join(root, "vise.lock"))
			if err != nil {
				t.Fatal(err)
			}
			cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf witness\nprintf '"+diagnostic+"' >&2\nexit 127\n")
			for _, command := range []string{"gate", "verify"} {
				exit, out, _ := cliRun(t, root, command, "--json")
				value := parseCLIJSON(t, out)
				if exit != 2 || value["next"].(map[string]any)["action"] != "fix_probe" {
					t.Fatalf("%s accepted failure: exit=%d value=%#v", command, exit, value)
				}
				c23AssertCapturedNotFound(t, c23FailureDetail(t, value, "p"), diagnostic)
			}
			after, err := os.ReadFile(filepath.Join(root, "vise.lock"))
			if err != nil || !bytes.Equal(accepted, after) {
				t.Fatalf("accepted lock changed: err=%v", err)
			}
		})
	}
}

func TestC23RawRunPreservesExitAndBytesWithoutReattribution(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf witness\nprintf 'record not found' >&2\nexit 127\n")
	exit, out, errOut := cliRun(t, root, "run", "behavior", "--json")
	value := parseCLIJSON(t, out)
	if exit != 127 || errOut != "" || value["exit"] != float64(127) || value["stdout"] != "witness" || value["stderr"] != "record not found" {
		t.Fatalf("raw result: exit=%d value=%#v stderr=%q", exit, value, errOut)
	}
	if strings.Contains(out, "captured stderr") || strings.Contains(out, "install") {
		t.Fatalf("raw capture was rewritten as advice: %s", out)
	}
}

func TestC23GenuinelyMissingExecutableRetainsItsCapturedName(t *testing.T) {
	manifest := strings.Replace(basicManifest(""), "./probe.sh", "./vise-c23-genuinely-missing", 1)
	root := cliRepo(t, manifest, "")
	exit, out, _ := cliRun(t, root, "record", "--json")
	value := parseCLIJSON(t, out)
	if exit != 2 {
		t.Fatalf("missing executable record: exit=%d value=%#v", exit, value)
	}
	detail := c23FailureDetail(t, value, "behavior")
	c23AssertCapturedNotFound(t, detail, "vise-c23-genuinely-missing")
}
