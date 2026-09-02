package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordedStatusRepo(t *testing.T) string {
	t.Helper()
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"stable\"\nrun = \"printf stable\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	result := Record(root, manifest, manifestBytes, RecordOptions{})
	if result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	return root
}

func TestStatusReportsBaselineDriftWithoutRunningProbes(t *testing.T) {
	root := recordedStatusRepo(t)
	report := BuildStatus(root)
	if report.State != "ready" || len(report.Lock.Drift) != 0 {
		t.Fatalf("clean status = %#v", report)
	}

	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"stable\"\nrun = \"printf changed\"\n[[probe]]\nid = \"extra\"\nrun = \"printf extra\"\n")
	report = BuildStatus(root)
	if report.State != "baseline-drift" || report.Next.Action != "human" {
		t.Fatalf("drift status = %#v", report)
	}
	joined := strings.Join(report.Lock.Drift, "\n")
	for _, want := range []string{"extra: probe is declared but absent from vise.lock", "stable: probe definition changed after recording"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("drift %q lacks %q", joined, want)
		}
	}
	if !strings.Contains(report.Next.Detail, "extra: probe is declared but absent") {
		t.Fatalf("next detail = %q", report.Next.Detail)
	}

	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n")
	report = BuildStatus(root)
	// With no probes declared, gate refuses before judging, so status reports
	// harness-error while still listing the drift that explains it.
	if report.State != "harness-error" || report.Next.Action != "fix_probe" || !strings.Contains(strings.Join(report.Lock.Drift, "\n"), "stable: probe exists in vise.lock but not vise.toml") {
		t.Fatalf("empty-manifest status = %#v", report)
	}
}

func TestMalformedProposalsDoNotChangeStatusState(t *testing.T) {
	root := recordedStatusRepo(t)
	writeTestFile(t, root, ".vise/proposals.toml", "[[probe]]\nid = \"\"\nrun = \"\"\nnot = valid\n")
	report := BuildStatus(root)
	if report.State != "ready" || report.Next.Action != "proceed" || report.ProposalError == "" || report.PendingProposals != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestStatusReportsTrackedArtifactAsDrift(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"build\"\nrun = \"mkdir -p out; printf x > out/result.txt; printf stable\"\nfiles = [\"out/result.txt\"]\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	if report := BuildStatus(root); report.State != "ready" {
		t.Fatalf("clean status = %#v", report)
	}
	// The artifact becomes tracked after recording: the next run will refuse
	// to delete it, so status must say so instead of promising proceed.
	testGit(t, root, "add", "out/result.txt")
	testGit(t, root, "commit", "-qm", "track the artifact")
	report := BuildStatus(root)
	if report.State != "baseline-drift" || !strings.Contains(strings.Join(report.Lock.Drift, "\n"), `build: declared artifact "out/result.txt" is tracked by git`) {
		t.Fatalf("tracked artifact status = %#v", report)
	}
}

func TestStatusReportsGitInspectionFailureAsDrift(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"build\"\nrun = \"mkdir -p out; printf x > out/result.txt; printf stable\"\nfiles = [\"out/result.txt\"]\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	index := filepath.Join(root, ".git", "index")
	if err := os.WriteFile(index, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := BuildStatus(root)
	if report.State == "ready" || !strings.Contains(strings.Join(report.Lock.Drift, "\n"), "cannot inspect declared artifacts") {
		t.Fatalf("report = %#v", report)
	}
}
