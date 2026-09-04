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
	if report.State != "harness-error" || report.Next.Action != NextHuman || !strings.Contains(strings.Join(report.Lock.Drift, "\n"), "stable: probe exists in vise.lock but not vise.toml") {
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

// The contract tells an agent to read status first and then gate. For the most
// ordinary drift there is — a declared input the agent itself edited — status
// said human and the gate said fix_probe seconds later. Two correct-sounding
// instructions pointing opposite ways is the worst thing this tool can do, and
// the operator flag exists precisely so the two commands answer alike.
//
// Reported by a coding agent that had been blocked by a red repository and
// read the two answers side by side.
func TestStatusAndGateAgreeAboutWhoRepairsTheDrift(t *testing.T) {
	newRepo := func(t *testing.T) string {
		t.Helper()
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n\n[[probe]]\nid = \"p\"\nrun = \"cat dep.txt\"\ntimeout = 30\ndeps = [\"dep.txt\"]\n")
		writeTestFile(t, root, "dep.txt", "original\n")
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "harness")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
			t.Fatalf("record: %#v", result.Outcome)
		}
		return root
	}

	both := func(t *testing.T, root string) (string, string) {
		t.Helper()
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		gate := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome.Next.Action
		return BuildStatus(root).Next.Action, gate
	}

	t.Run("the agent edited a declared input, so the agent restores it", func(t *testing.T) {
		root := newRepo(t)
		writeTestFile(t, root, "dep.txt", "changed\n")
		status, gate := both(t, root)
		if status != gate {
			t.Errorf("status says %q and gate says %q for the same drift", status, gate)
		}
		if gate != NextFixProbe {
			t.Errorf("action %q, want %q — the agent changed a file it may change", gate, NextFixProbe)
		}
	})

	t.Run("the probe definition moved, so an operator owns it", func(t *testing.T) {
		root := newRepo(t)
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n\n[[probe]]\nid = \"p\"\nrun = \"cat dep.txt\"\ntimeout = 45\ndeps = [\"dep.txt\"]\n")
		status, gate := both(t, root)
		if status != gate {
			t.Errorf("status says %q and gate says %q for the same drift", status, gate)
		}
		if gate != NextHuman {
			t.Errorf("action %q, want %q — the repair is in vise.toml", gate, NextHuman)
		}
	})

	// A declared artifact somebody committed. This is the branch a heterogeneous
	// reviewer caught: gate reaches it through artifacts.reset(), which sets the
	// operator flag, and the dep-input agreement fix beside it did not extend to
	// the artifact case two lines down — so status said fix_probe where gate
	// said human, and the pinning test above covered every drift kind but this.
	t.Run("a declared artifact is tracked, so an operator owns it", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n\n[[probe]]\nid = \"p\"\nrun = \"printf ok > out.txt; printf ok\"\ntimeout = 30\nfiles = [\"out.txt\"]\n")
		testGit(t, root, "add", "vise.toml", ".gitignore")
		testGit(t, root, "commit", "-qm", "harness")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
			t.Fatalf("record: %#v", result.Outcome)
		}
		// The artifact gets committed after recording — the exact "somebody ran
		// git add -A" mistake.
		testGit(t, root, "add", "out.txt")
		testGit(t, root, "commit", "-qm", "committed the artifact by mistake")

		status, gate := both(t, root)
		if status != gate {
			t.Errorf("status says %q and gate says %q for a tracked declared artifact", status, gate)
		}
		if gate != NextHuman {
			t.Errorf("action %q, want %q — the agent cannot git rm --cached and edit .gitignore for the manifest it may not touch", gate, NextHuman)
		}
	})
}

// A valid baseline with the manifest deleted is a broken harness, and status
// used to call it not-initialized — "run vise init, declare probes, and record
// a baseline" — while a baseline was sitting right there and gate mapped the
// same missing manifest to an operator harness error. record_first tells an
// operator to create state that exists.
func TestStatusDoesNotSayRecordFirstWhenABaselineExists(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n\n[[probe]]\nid = \"p\"\nrun = \"printf ok\"\ntimeout = 30\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "harness")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	if err := os.Remove(filepath.Join(root, "vise.toml")); err != nil {
		t.Fatal(err)
	}
	report := BuildStatus(root)
	if report.Next.Action == NextRecordFirst {
		t.Errorf("status says record_first with a baseline present: %q", report.Next.Detail)
	}
	if report.Next.Action != NextHuman {
		t.Errorf("next %q, want human — the manifest that defines the baseline is gone", report.Next.Action)
	}

	// The genuinely uninitialized repo must still say record_first.
	fresh := testGitRepo(t)
	if action := BuildStatus(fresh).Next.Action; action != NextRecordFirst {
		t.Errorf("a fresh repo says %q, want record_first", action)
	}
}

// A HEAD that will not read, with a valid baseline present, is a harness
// problem the next gate will hit. nextGateRefused swallowed the GitHead error
// into "not refused", so status reported ready/proceed while gate would fail on
// the same HEAD. The journal read error beside it stays swallowed on purpose —
// buildJournalStatus rescues that and gate re-reports it — but a HEAD error is
// rescued by nothing.
func TestStatusSurfacesAnUnreadableHead(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n\n[[probe]]\nid = \"p\"\nrun = \"printf ok\"\ntimeout = 30\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "harness")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	// Corrupt HEAD after the baseline is in place: a valid lockfile, no
	// readable HEAD. rev-parse will refuse this.
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("this is not a ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := BuildStatus(root)
	if report.State == "ready" {
		t.Errorf("status reports ready with an unreadable HEAD: next=%q", report.Next.Action)
	}
	if report.Next.Action != NextHuman {
		t.Errorf("next %q, want human", report.Next.Action)
	}
}
