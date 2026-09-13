package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExit127WithoutShellEvidenceDoesNotInventAMissingProgram(t *testing.T) {
	for _, run := range []string{"./program", "sh ./program", "printf outer; ./program"} {
		for _, diagnostic := range []string{"", "custom failure"} {
			t.Run(run+"/"+diagnostic, func(t *testing.T) {
				root := testGitRepo(t)
				writeTestFile(t, root, "program", "#!/bin/sh\nprintf ran\nprintf '"+diagnostic+"' >&2\nexit 127\n")
				if err := os.Chmod(filepath.Join(root, "program"), 0o755); err != nil {
					t.Fatal(err)
				}
				probe := Probe{ID: "explicit127", Run: run, Timeout: 5}
				got := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
				if !strings.HasSuffix(string(got.Stdout.Prefix), "ran") || string(got.Stderr.Prefix) != diagnostic {
					t.Fatalf("program execution witness missing: %#v", got)
				}
				if got.Exit != 127 || !got.LaunchFailed || !got.Tolerated {
					t.Fatalf("the exit-127 contract changed: %#v", got)
				}
				if !strings.Contains(got.HarnessError, "exited 127; inspect the command's exit handling and dependencies") {
					t.Errorf("missing evidence-qualified explanation: %q", got.HarnessError)
				}
				for _, unsupported := range []string{"install", "not on its PATH", "could not be launched"} {
					if strings.Contains(got.HarnessError, unsupported) {
						t.Errorf("invented launch evidence %q: %q", unsupported, got.HarnessError)
					}
				}
			})
		}
	}
}

func TestMissingProgramRetainsItsShellDiagnosticAndRemedy(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "missing", Run: "./vise-definitely-missing", Timeout: 5}
	got := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if got.Exit != 127 || !got.LaunchFailed || !got.Tolerated || len(got.Stdout.Prefix) != 0 {
		t.Fatalf("missing-program control changed: %#v", got)
	}
	line := firstNotFoundDiagnostic(got.Stderr)
	if line == "" || !strings.Contains(got.HarnessError, line) || !strings.Contains(got.HarnessError, "captured stderr:") || !strings.Contains(got.HarnessError, "inspect the command's exit handling and dependencies") {
		t.Fatalf("lost observed shell diagnostic or remedy: %#v", got)
	}
}
