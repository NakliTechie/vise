package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NakliTechie/vise/internal/vise"
)

func TestDependencyDiagnosticRoutingAcrossCheckoutLocations(t *testing.T) {
	for _, sharedSpec := range []bool{false, true} {
		name := "dependency"
		if sharedSpec {
			name = "protected-spec"
		}
		t.Run(name, func(t *testing.T) {
			first := map[string]string{}
			for copy := 0; copy < 2; copy++ {
				manifest := basicManifest("deps = [\"fixtures/input\"]\n")
				if sharedSpec {
					manifest += "expect.stdout = \"fixtures/input\"\n"
				}
				root := cliRepo(t, manifest, "#!/bin/sh\nprintf ran >> .calls\ncat fixtures/input\n")
				cliWrite(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.calls\n")
				cliWrite(t, root, "fixtures/input", "input\n")
				cliGit(t, root, "add", ".")
				cliGit(t, root, "commit", "-qm", "declared input")
				if exit, out, errOut := cliRun(t, root, "record", "--json"); exit != 0 {
					t.Fatalf("control record: %d %s %s", exit, out, errOut)
				}
				if exit, out, errOut := cliRun(t, root, "gate", "--json"); exit != 0 {
					t.Fatalf("control gate: %d %s %s", exit, out, errOut)
				}
				for _, path := range []string{"fixtures/input", ".calls"} {
					if err := os.Remove(filepath.Join(root, path)); err != nil {
						t.Fatal(err)
					}
				}
				for _, command := range []string{"status", "verify", "gate"} {
					exit, out, errOut := cliRun(t, root, command, "--json")
					wantExit, wantAction := vise.ExitHarness, vise.NextFixProbe
					if sharedSpec {
						wantAction = vise.NextHuman
					}
					var detail, action string
					if command == "status" {
						wantExit = 0
						var report vise.StatusReport
						if err := json.Unmarshal([]byte(out), &report); err != nil {
							t.Fatal(err)
						}
						if len(report.Lock.Drift) != 1 || report.Exit != 0 || report.State != "baseline-drift" {
							t.Fatalf("status did not report the single refusal: %s", out)
						}
						detail, action = report.Lock.Drift[0], report.Next.Action
					} else {
						var outcome vise.Outcome
						if err := json.Unmarshal([]byte(out), &outcome); err != nil {
							t.Fatal(err)
						}
						failure := outcome.Failures["behavior"]
						if outcome.Exit != wantExit || failure.Class != "harness" || failure.Operator != sharedSpec {
							t.Fatalf("failure ownership changed: %s", out)
						}
						detail, action = failure.Detail, outcome.Next.Action
					}
					if exit != wantExit || errOut != "" || action != wantAction {
						t.Fatalf("%s: exit=%d action=%s stdout=%s stderr=%s", command, exit, action, out, errOut)
					}
					if !strings.Contains(detail, "fixtures/input") || strings.Contains(detail, root) {
						t.Errorf("nonportable or unhelpful detail: %s", detail)
					}
					if copy == 0 {
						first[command] = detail
					} else if first[command] != detail {
						t.Errorf("%s detail changed with checkout: %q != %q", command, first[command], detail)
					}
				}
				if _, err := os.Stat(filepath.Join(root, ".calls")); !os.IsNotExist(err) {
					t.Fatalf("preflight refusal executed the candidate: %v", err)
				}
			}
		})
	}
}
