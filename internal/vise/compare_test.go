package vise

import (
	"testing"
)

// RunMatchesLock is the whole judgment: it decides green from red. It had no
// test of its own, so removing any single comparator from it — the exit code,
// stderr, the artifact hashes — left the suite green while the gate stopped
// noticing that class of change entirely.
//
// One case per comparator, so dropping any one of them fails here.
func TestRunMatchesLockComparesEveryObservation(t *testing.T) {
	hash := func(s string) string { return HashBytes([]byte(s)) }

	baseline := ProbeLock{
		Exit:   0,
		Stdout: hash("out"),
		Stderr: hash("err"),
		Files:  map[string]string{"out/a.txt": hash("a"), "out/b.txt": hash("b")},
	}
	matching := func() RunResult {
		return RunResult{
			Exit:   0,
			Stdout: CaptureBytes([]byte("out")),
			Stderr: CaptureBytes([]byte("err")),
			Files: map[string]Capture{
				"out/a.txt": CaptureBytes([]byte("a")),
				"out/b.txt": CaptureBytes([]byte("b")),
			},
		}
	}

	if !RunMatchesLock(matching(), baseline) {
		t.Fatal("an identical run did not match its baseline")
	}

	tests := []struct {
		name   string
		break_ func(run *RunResult)
	}{
		{"a different exit code", func(r *RunResult) { r.Exit = 1 }},
		{"different stdout", func(r *RunResult) { r.Stdout = CaptureBytes([]byte("other")) }},
		{"different stderr", func(r *RunResult) { r.Stderr = CaptureBytes([]byte("other")) }},
		{"a changed artifact", func(r *RunResult) { r.Files["out/a.txt"] = CaptureBytes([]byte("changed")) }},
		{"a missing artifact", func(r *RunResult) { delete(r.Files, "out/b.txt") }},
		{"an extra artifact", func(r *RunResult) { r.Files["out/c.txt"] = CaptureBytes([]byte("c")) }},
		{"an artifact under a different name", func(r *RunResult) {
			delete(r.Files, "out/a.txt")
			r.Files["out/renamed.txt"] = CaptureBytes([]byte("a"))
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := matching()
			test.break_(&run)
			if RunMatchesLock(run, baseline) {
				t.Fatalf("%s was judged identical to the baseline", test.name)
			}
		})
	}
}

// An empty baseline and an empty run match, and neither is confused with the
// other having something.
func TestRunMatchesLockOnEmptyObservations(t *testing.T) {
	empty := ProbeLock{Exit: 0, Stdout: HashBytes(nil), Stderr: HashBytes(nil)}
	run := RunResult{Exit: 0, Stdout: CaptureBytes(nil), Stderr: CaptureBytes(nil)}
	if !RunMatchesLock(run, empty) {
		t.Fatal("two empty observations did not match")
	}

	withFile := empty
	withFile.Files = map[string]string{"out/a.txt": HashBytes([]byte("a"))}
	if RunMatchesLock(run, withFile) {
		t.Fatal("a run producing no artifact matched a baseline that expects one")
	}
}

// The counts are how an agent reads a verdict at a glance and how the journal
// records what happened. Setting the declared count to zero left the suite
// green, because every test asserted the verdict and none asserted the
// arithmetic behind it.
func TestVerifyCountsAddUpForEveryVerdict(t *testing.T) {
	newRepo := func(t *testing.T, probes string) string {
		t.Helper()
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.toggle\n")
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n"+probes)
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "manifest")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
			t.Fatalf("record: %#v", result.Outcome)
		}
		return root
	}

	t.Run("green counts every declared probe as a pass", func(t *testing.T) {
		root := newRepo(t, "[[probe]]\nid = \"a\"\nrun = \"printf a\"\n[[probe]]\nid = \"b\"\nrun = \"printf b\"\n")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		counts := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome.Counts
		if counts.Declared != 2 || counts.Pass != 2 {
			t.Fatalf("counts = %#v, want 2 declared and 2 passing", counts)
		}
		if counts.Behavior+counts.Flaky+counts.Harness+counts.Metric != 0 {
			t.Fatalf("a green verdict carried failures: %#v", counts)
		}
	})

	t.Run("red counts the failure and keeps the denominator", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
		writeTestFile(t, root, "a.sh", "#!/bin/sh\nprintf a\n")
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"a\"\nrun = \"sh a.sh\"\n[[probe]]\nid = \"b\"\nrun = \"printf b\"\n")
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "manifest")
		{
			manifest, manifestBytes, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
				t.Fatalf("record: %#v", result.Outcome)
			}
		}
		// One probe's behaviour moves; the other is untouched.
		writeTestFile(t, root, "a.sh", "#!/bin/sh\nprintf changed\n")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		counts := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome.Counts
		// The denominator is what makes a verdict readable: one of two failing
		// is not the same news as one of two hundred.
		if counts.Declared != 2 {
			t.Fatalf("declared = %d, want 2", counts.Declared)
		}
		if counts.Behavior != 1 || counts.Pass != 1 {
			t.Fatalf("counts = %#v, want one behavior failure beside one pass", counts)
		}
	})
}
