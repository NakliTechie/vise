package vise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are deliberately end-to-end Verify fixtures.  C11 is about the
// report at the preflight boundary, so asserting helpers below reject both a
// plausible verdict with invented passes and a truthful count with judgment
// fields accidentally attached.
func c11Manifest(t *testing.T, root string, withMetric bool) (Manifest, []byte) {
	t.Helper()
	metric := ""
	if withMetric {
		metric = `
[[metric]]
id = "m"
run = "printf metric >> ran; printf 1"
version_cmd = "printf version >> ran; printf tool-1"
direction = "down"
enforce = "no-regress"
`
	}
	writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[stubs]
network = "declared-off"
[env]
fingerprint = ["printf fingerprint >> ran; printf host"]
[[probe]]
id = "p1"
run = "printf p1 >> ran; printf one"
[[probe]]
id = "p2"
run = "printf p2 >> ran; printf two"
`+metric)
	manifest, bytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, bytes
}

func assertC11Preflight(t *testing.T, got VerifyResult, declared, skipped, exit int, failureIDs ...string) {
	t.Helper()
	c := got.Outcome.Counts
	if got.Outcome.Exit != exit || c.Declared != declared || c.Pass != 0 || c.Skipped != skipped {
		t.Fatalf("exit/counts = %d %#v, want exit %d declared %d pass 0 skipped %d", got.Outcome.Exit, c, exit, declared, skipped)
	}
	want := map[string]bool{}
	for _, id := range failureIDs {
		want[id] = true
	}
	if c.Harness != len(want) || c.Behavior != 0 || c.Flaky != 0 || c.Metric != 0 || c.Unmet != 0 {
		t.Fatalf("preflight class counts = %#v, want only %d harness failures", c, len(want))
	}
	if len(got.Outcome.Failures) != len(want) {
		t.Fatalf("failures = %#v, want ids %v", got.Outcome.Failures, failureIDs)
	}
	for id := range want {
		if failure, ok := got.Outcome.Failures[id]; !ok {
			t.Errorf("missing failure %q in %#v", id, got.Outcome.Failures)
		} else if failure.Class != "harness" {
			t.Errorf("preflight failure %q class = %q, want harness", id, failure.Class)
		}
	}
}

func assertC11NoJudgmentFields(t *testing.T, got VerifyResult) {
	t.Helper()
	b, err := json.Marshal(got.Outcome)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, field := range []string{`"classes"`, `"failures"`, `"lock"`, `"metrics"`, `"pins"`} {
		if strings.Contains(text, field) {
			t.Errorf("preflight result contains judgment field %s: %s", field, text)
		}
	}
}

func TestC11NoBaselineRetainsRequestedScopeWithoutExecutingAnything(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metric   bool
		probe    string
		declared int
	}{
		{"full probes and metric", true, "", 3},
		{"subset excludes metrics", true, "p1", 1},
		{"full metricless", false, "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nran\n")
			manifest, bytes := c11Manifest(t, root, tc.metric)
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "C11 fixture")
			got := Verify(root, manifest, bytes, VerifyOptions{ProbeID: tc.probe, EnforceRerunLimit: true})
			assertC11Preflight(t, got, tc.declared, tc.declared, ExitNotInitialized)
			assertC11NoJudgmentFields(t, got)
			if _, err := os.Stat(filepath.Join(root, "ran")); !os.IsNotExist(err) {
				t.Fatalf("a probe, metric, version, or fingerprint command ran: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".vise", "journal.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("preflight created a judgment journal: %v", err)
			}
		})
	}
}

func c11RecordedRepo(t *testing.T, withMetric bool) (string, Manifest, []byte) {
	t.Helper()
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nran\n")
	manifest, bytes := c11Manifest(t, root, withMetric)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "C11 recorded fixture")
	if got := Record(root, manifest, bytes, RecordOptions{}); got.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", got.Outcome)
	}
	if err := os.Remove(filepath.Join(root, "ran")); err != nil {
		t.Fatal(err)
	}
	return root, manifest, bytes
}

func TestC11NormalReplayPositiveControlsRetainScope(t *testing.T) {
	root, manifest, bytes := c11RecordedRepo(t, true)
	full := Verify(root, manifest, bytes, VerifyOptions{})
	if c := full.Outcome.Counts; full.Outcome.Exit != ExitOK || c.Declared != 3 || c.Pass != 3 || c.Skipped != 0 {
		t.Fatalf("full replay = %#v", full.Outcome)
	}
	if strings.Join(full.CheckSet, ",") != "m,p1,p2" {
		t.Fatalf("full check set = %v", full.CheckSet)
	}
	_ = os.Remove(filepath.Join(root, "ran"))
	subset := Verify(root, manifest, bytes, VerifyOptions{ProbeID: "p1"})
	if c := subset.Outcome.Counts; subset.Outcome.Exit != ExitOK || c.Declared != 1 || c.Pass != 1 || c.Skipped != 0 || len(subset.Outcome.Metrics) != 0 {
		t.Fatalf("subset replay = %#v", subset.Outcome)
	}
	if strings.Join(subset.CheckSet, ",") != "p1" {
		t.Fatalf("subset check set = %v", subset.CheckSet)
	}
	run, err := os.ReadFile(filepath.Join(root, "ran"))
	if err != nil || string(run) != "fingerprintp1" {
		t.Fatalf("subset execution witness = %q, %v", run, err)
	}
}

func TestC11EarlyHarnessRefusalsRetainDeclaredScopeAndTruthfulSkips(t *testing.T) {
	t.Run("malformed lock is infrastructure outside the denominator", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nran\n")
		manifest, bytes := c11Manifest(t, root, true)
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "fixture")
		writeTestFile(t, root, "vise.lock", "{not-json\n")
		got := Verify(root, manifest, bytes, VerifyOptions{})
		assertC11Preflight(t, got, 3, 3, ExitHarness, "vise.lock")
		if got.Outcome.Next.Action != NextHuman {
			t.Errorf("repair action = %q", got.Outcome.Next.Action)
		}
	})

	t.Run("declared probe definition drift fails that probe and skips the rest", func(t *testing.T) {
		root, manifest, bytes := c11RecordedRepo(t, true)
		manifest.Probes[0].Run = "printf changed"
		got := Verify(root, manifest, bytes, VerifyOptions{})
		assertC11Preflight(t, got, 3, 2, ExitHarness, "p1")
		if got.Outcome.Next.Action != NextHuman {
			t.Errorf("repair action = %q", got.Outcome.Next.Action)
		}
		run, err := os.ReadFile(filepath.Join(root, "ran"))
		if err != nil || string(run) != "fingerprint" {
			t.Fatalf("preflight execution witness = %q, %v; a probe, metric, or metric version ran", run, err)
		}
	})

	t.Run("fingerprint drift is infrastructure and skips every requested check", func(t *testing.T) {
		root, manifest, bytes := c11RecordedRepo(t, true)
		manifest.Environment.Fingerprint = []string{"printf other"}
		got := Verify(root, manifest, bytes, VerifyOptions{ProbeID: "p1"})
		assertC11Preflight(t, got, 1, 1, ExitHarness, "fingerprint")
		if got.Outcome.Next.Action != NextHuman {
			t.Errorf("repair action = %q", got.Outcome.Next.Action)
		}
	})
}

func TestC11MetricsAreEligibleOnlyAfterBehaviorHolds(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nmetric.sh\n")
	writeTestFile(t, root, "metric.sh", "printf 1")
	writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "p1"
run = "printf one"
[[probe]]
id = "p2"
run = "printf two"
[[metric]]
id = "m"
run = "sh metric.sh"
version_cmd = "printf tool-1"
direction = "down"
enforce = "no-regress"
`)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "metric fixture")
	manifest, bytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if recorded := Record(root, manifest, bytes, RecordOptions{}); recorded.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", recorded.Outcome)
	}
	writeTestFile(t, root, "metric.sh", "printf 2")
	got := Verify(root, manifest, bytes, VerifyOptions{})
	if c := got.Outcome.Counts; got.Outcome.Exit != ExitMetric || c.Declared != 3 || c.Pass != 2 || c.Metric != 1 || c.Skipped != 0 {
		t.Fatalf("metric regression = %#v", got.Outcome)
	}
}

func c11AssertWitness(t *testing.T, root, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "ran"))
	if want == "" && os.IsNotExist(err) {
		return
	}
	if err != nil || string(b) != want {
		t.Fatalf("execution witness = %q, %v; want %q", b, err, want)
	}
}

func TestC11StatePreflightFailuresRetainFullAndSubsetScope(t *testing.T) {
	tests := []struct {
		name    string
		failure string
		breakIt func(t *testing.T, root string, manifest Manifest, manifestBytes []byte)
	}{
		{
			name:    "corrupted referenced blob",
			failure: "tamper-hash",
			breakIt: func(t *testing.T, root string, manifest Manifest, _ []byte) {
				lock, _, err := LoadLockfile(root)
				if err != nil {
					t.Fatal(err)
				}
				path, err := BlobPath(root, lock.Probes[manifest.Probes[0].ID].Stdout)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("corrupt"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "unreadable HEAD",
			failure: "git",
			breakIt: func(t *testing.T, root string, _ Manifest, _ []byte) {
				if err := os.Rename(filepath.Join(root, ".git", "HEAD"), filepath.Join(root, ".git", "HEAD.hidden")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "corrupt git index",
			failure: "git",
			breakIt: func(t *testing.T, root string, _ Manifest, _ []byte) {
				if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("not an index"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range tests {
		for _, scope := range []struct {
			name, probe string
			declared    int
		}{{"full", "", 3}, {"subset", "p1", 1}} {
			t.Run(tc.name+"/"+scope.name, func(t *testing.T) {
				root, manifest, bytes := c11RecordedRepo(t, true)
				tc.breakIt(t, root, manifest, bytes)
				got := Verify(root, manifest, bytes, VerifyOptions{ProbeID: scope.probe})
				assertC11Preflight(t, got, scope.declared, scope.declared, ExitHarness, tc.failure)
				if got.Outcome.Next.Action != NextHuman && tc.failure != "git" {
					t.Errorf("repair action = %q, want human", got.Outcome.Next.Action)
				}
				if tc.failure == "git" && got.Outcome.Next.Action != NextFixProbe {
					t.Errorf("repair action = %q, want fix_probe", got.Outcome.Next.Action)
				}
				c11AssertWitness(t, root, "")
			})
		}
	}
}

func TestC11JournalPreflightFailuresRetainFullAndSubsetScope(t *testing.T) {
	for _, scope := range []struct {
		name, probe string
		declared    int
	}{{"full", "", 3}, {"subset", "p1", 1}} {
		t.Run("malformed/"+scope.name, func(t *testing.T) {
			root, manifest, bytes := c11RecordedRepo(t, true)
			writeTestFile(t, root, ".vise/journal.jsonl", "{malformed\n")
			got := Verify(root, manifest, bytes, VerifyOptions{ProbeID: scope.probe, EnforceRerunLimit: true})
			assertC11Preflight(t, got, scope.declared, scope.declared, ExitHarness, "journal")
			if got.Outcome.Next.Action != NextHuman || got.RerunRefused {
				t.Errorf("malformed journal action/refused = %q/%v", got.Outcome.Next.Action, got.RerunRefused)
			}
			c11AssertWitness(t, root, "")
		})

		t.Run("rerun refused/"+scope.name, func(t *testing.T) {
			root, manifest, bytes := c11RecordedRepo(t, true)
			lockBytes, err := os.ReadFile(filepath.Join(root, "vise.lock"))
			if err != nil {
				t.Fatal(err)
			}
			lockHash, err := TamperHash(root, bytes, lockBytes)
			if err != nil {
				t.Fatal(err)
			}
			commit, err := GitHead(root)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{"p1"}
			if scope.probe == "" {
				ids = []string{"m", "p1", "p2"}
			}
			for i := 0; i < 2; i++ {
				if err := AppendJournal(root, JournalEvent{Event: "flake", Commit: commit, Lock: lockHash, Verdict: "indeterminate", Flaky: []string{"p1"}, Probes: ids}); err != nil {
					t.Fatal(err)
				}
			}
			got := Verify(root, manifest, bytes, VerifyOptions{ProbeID: scope.probe, EnforceRerunLimit: true})
			assertC11Preflight(t, got, scope.declared, scope.declared, ExitHarness, "rerun-limit")
			if got.Outcome.Next.Action != NextHuman || !got.RerunRefused {
				t.Errorf("rerun refusal action/refused = %q/%v", got.Outcome.Next.Action, got.RerunRefused)
			}
			c11AssertWitness(t, root, "")
		})
	}
}

func TestC11MetricDefinitionFailureCountsTheMetricAndSubsetIgnoresIt(t *testing.T) {
	root, manifest, bytes := c11RecordedRepo(t, true)
	manifest.Metrics[0].Run = "printf changed"
	full := Verify(root, manifest, bytes, VerifyOptions{})
	assertC11Preflight(t, full, 3, 2, ExitHarness, "m")
	if full.Outcome.Next.Action != NextHuman {
		t.Errorf("metric definition action = %q", full.Outcome.Next.Action)
	}
	c11AssertWitness(t, root, "fingerprint")
	if err := os.Remove(filepath.Join(root, "ran")); err != nil {
		t.Fatal(err)
	}
	subset := Verify(root, manifest, bytes, VerifyOptions{ProbeID: "p1"})
	if c := subset.Outcome.Counts; subset.Outcome.Exit != ExitOK || c.Declared != 1 || c.Pass != 1 || c.Skipped != 0 || len(subset.Outcome.Failures) != 0 {
		t.Fatalf("subset beside metric drift = %#v", subset.Outcome)
	}
	c11AssertWitness(t, root, "fingerprintp1")
}

func TestC11DeclaredInputFailuresDoNotAlsoCountAsSkipped(t *testing.T) {
	for _, change := range []string{"probe-definition", "spec", "dependency"} {
		for _, scope := range []struct {
			name, probe string
			declared    int
		}{{"full", "", 3}, {"subset", "p1", 1}} {
			t.Run(change+"/"+scope.name, func(t *testing.T) {
				root := testGitRepo(t)
				writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nran\n")
				_, data := c11Manifest(t, root, true)
				writeTestFile(t, root, "spec", "one")
				writeTestFile(t, root, "input", "input")
				text := strings.Replace(string(data), "id = \"p1\"\n", "id = \"p1\"\ndeps = [\"input\", \"spec\"]\nexpect.stdout = \"spec\"\n", 1)
				writeTestFile(t, root, "vise.toml", text)
				manifest, bytes, err := LoadManifest(root)
				if err != nil {
					t.Fatal(err)
				}
				testGit(t, root, "add", ".")
				testGit(t, root, "commit", "-qm", "C11 declared-input fixture")
				if got := Record(root, manifest, bytes, RecordOptions{}); got.Outcome.Exit != ExitOK {
					t.Fatalf("record: %#v", got.Outcome)
				}
				positive := Verify(root, manifest, bytes, VerifyOptions{ProbeID: scope.probe})
				if c := positive.Outcome.Counts; positive.Outcome.Exit != ExitOK || c.Declared != scope.declared || c.Pass != scope.declared || c.Skipped != 0 {
					t.Fatalf("valid declared-input control: %#v", positive.Outcome)
				}
				if err := os.Remove(filepath.Join(root, "ran")); err != nil {
					t.Fatal(err)
				}
				want := NextHuman
				switch change {
				case "probe-definition":
					manifest.Probes[0].Run = "printf changed"
				case "spec":
					writeTestFile(t, root, "spec", "changed")
				case "dependency":
					writeTestFile(t, root, "input", "changed")
					want = NextFixProbe
				}
				got := Verify(root, manifest, bytes, VerifyOptions{ProbeID: scope.probe})
				assertC11Preflight(t, got, scope.declared, scope.declared-1, ExitHarness, "p1")
				if got.Outcome.Next.Action != want || got.Outcome.Failures["p1"].Operator != (want == NextHuman) {
					t.Fatalf("declared-input repair owner: %#v", got.Outcome)
				}
				c11AssertWitness(t, root, "fingerprint")
			})
		}
	}
}
