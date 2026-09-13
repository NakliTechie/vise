package vise

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestC05AcceptedPinFailuresPreserveGenerationAndAcceptance(t *testing.T) {
	tests := []struct {
		name       string
		broken     string
		wantExit   int
		wantClass  string
		wantAction string
		wantDetail string
	}{
		{name: "mismatch", broken: "#!/bin/sh\nmkdir -p out; printf 'wrong\\n'; printf 'artifact\\n' > out/report\n", wantExit: ExitBehavior, wantClass: "behavior", wantAction: NextRevert, wantDetail: "accepted pin no longer meets its spec"},
		{name: "exit-127", broken: "#!/bin/sh\nexit 127\n", wantExit: ExitHarness, wantClass: "harness", wantAction: NextFixProbe, wantDetail: "exited 127"},
		{name: "live-timeout", broken: "#!/bin/sh\nsleep 5\n", wantExit: ExitHarness, wantClass: "harness", wantAction: NextFixProbe, wantDetail: "timed out"},
		{name: "missing-artifact", broken: "#!/bin/sh\nrm -f out/report; printf 'expected\\n'\n", wantExit: ExitHarness, wantClass: "harness", wantAction: NextFixProbe, wantDetail: "was not produced"},
		{name: "symlink-artifact", broken: "#!/bin/sh\nmkdir -p out; rm -f out/report; ln -s ../spec/report out/report; printf 'expected\\n'\n", wantExit: ExitHarness, wantClass: "harness", wantAction: NextFixProbe, wantDetail: "symlink"},
	}

	const healthy = "#!/bin/sh\nmkdir -p out; rm -f out/report; printf 'expected\\n'; printf 'artifact\\n' > out/report\n"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nout/\n")
			writeTestFile(t, root, "spec/stdout", "expected\n")
			writeTestFile(t, root, "spec/report", "artifact\n")
			writeTestFile(t, root, "program.sh", healthy)
			writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "accepted"
run = "sh program.sh"
timeout = 1
files = ["out/report"]
expect.stdout = "spec/stdout"
expect.files = { "out/report" = "spec/report" }
`)
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "healthy accepted pin")
			manifest, manifestBytes, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			accepted := Record(root, manifest, manifestBytes, RecordOptions{})
			if accepted.Outcome.Exit != ExitOK || accepted.Pins == nil || len(accepted.Pins.Accepted) != 1 {
				t.Fatalf("initial acceptance: outcome=%#v pins=%#v", accepted.Outcome, accepted.Pins)
			}
			lock, _, err := LoadLockfile(root)
			if err != nil {
				t.Fatal(err)
			}
			acceptedCommit := lock.Probes["accepted"].Pin.AcceptedCommit
			if acceptedCommit == nil {
				t.Fatal("initial record did not retain acceptance")
			}
			testGit(t, root, "add", "vise.lock", ".vise/blobs")
			testGit(t, root, "commit", "-qm", "accepted baseline")

			writeTestFile(t, root, "program.sh", tc.broken)
			testGit(t, root, "add", "program.sh")
			testGit(t, root, "commit", "-qm", "break accepted pin")
			before := snapshotAcceptanceGeneration(t, root)
			result := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
			failure := result.Outcome.Failures["accepted"]
			if result.Outcome.Exit != tc.wantExit || failure.Class != tc.wantClass || result.Outcome.Next.Action != tc.wantAction || !strings.Contains(failure.Detail, tc.wantDetail) {
				t.Fatalf("refusal = %#v, want exit=%d class=%s action=%s detail containing %q", result.Outcome, tc.wantExit, tc.wantClass, tc.wantAction, tc.wantDetail)
			}
			if after := snapshotAcceptanceGeneration(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed lock, blobs, or journal:\nbefore=%#v\nafter=%#v", before, after)
			}
			lock, _, err = LoadLockfile(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := lock.Probes["accepted"].Pin.AcceptedCommit; got == nil || *got != *acceptedCommit {
				t.Fatalf("accepted_commit = %v, want retained %s", got, *acceptedCommit)
			}

			if err := os.Remove(filepath.Join(root, "out", "report")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			writeTestFile(t, root, "program.sh", healthy)
			testGit(t, root, "add", "program.sh")
			testGit(t, root, "commit", "-qm", "recover accepted pin")
			recovered := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
			if recovered.Outcome.Exit != ExitOK {
				t.Fatalf("healthy recovery: %#v", recovered.Outcome)
			}
			lock, _, err = LoadLockfile(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := lock.Probes["accepted"].Pin.AcceptedCommit; got == nil || *got != *acceptedCommit {
				t.Fatalf("recovery changed accepted_commit = %v, want %s", got, *acceptedCommit)
			}
		})
	}
}
