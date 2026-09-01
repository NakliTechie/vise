package vise

import (
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
	if report.State != "baseline-drift" || !strings.Contains(strings.Join(report.Lock.Drift, "\n"), "stable: probe exists in vise.lock but not vise.toml") {
		t.Fatalf("empty-manifest status = %#v", report)
	}
}
