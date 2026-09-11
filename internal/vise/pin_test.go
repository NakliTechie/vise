package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One case per rule in the pin contract's manifest section, each differing
// from a valid pin by exactly the rule it exercises.
func TestPinExpectationEnforcesEveryManifestRule(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/greet.stdout", "hello Ada\n")
	writeTestFile(t, root, "spec/greet.report", "{}\n")

	if err := os.Symlink(filepath.Join(root, "spec"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	valid := func() Manifest {
		return Manifest{
			Vise:  ViseSettings{Version: LockVersion},
			Stubs: StubSettings{Network: "declared-off"},
			Probes: []Probe{{
				ID: "greet", Run: "./bin/greet", Timeout: 30,
				Files:  []string{"out/report.json"},
				Expect: &Expect{Stdout: "spec/greet.stdout", Exit: IntPtr(0), Files: map[string]string{"out/report.json": "spec/greet.report"}},
			}},
		}
	}
	if err := valid().Validate(root); err != nil {
		t.Fatalf("the baseline pin is not valid: %v", err)
	}

	tests := []struct {
		name   string
		change func(m *Manifest)
		wantIn string
	}{
		{"an empty expectation", func(m *Manifest) {
			m.Probes[0].Files = nil
			m.Probes[0].Expect = &Expect{}
		}, "expect is empty"},
		{"an exit above 255", func(m *Manifest) { m.Probes[0].Expect.Exit = IntPtr(256) }, "between 0 and 255"},
		{"a negative exit", func(m *Manifest) { m.Probes[0].Expect.Exit = IntPtr(-1) }, "between 0 and 255"},
		{"exit 127, the launch-failure status", func(m *Manifest) { m.Probes[0].Expect.Exit = IntPtr(127) }, "launch-failure status"},
		{"an expected artifact that is not declared", func(m *Manifest) {
			m.Probes[0].Expect.Files["out/other.json"] = "spec/greet.report"
		}, "not in probe[0].files"},
		{"a declared artifact with no expectation", func(m *Manifest) {
			m.Probes[0].Files = append(m.Probes[0].Files, "out/other.json")
		}, "gives it no expectation"},
		{"an artifact expectation naming no spec", func(m *Manifest) {
			m.Probes[0].Expect.Files["out/report.json"] = ""
		}, "names no spec file"},
		{"artifacts declared but expect.files absent", func(m *Manifest) {
			m.Probes[0].Expect.Files = nil
		}, "gives it no expectation"},
		{"a spec that is an absolute path", func(m *Manifest) { m.Probes[0].Expect.Stdout = "/etc/passwd" }, "absolute paths"},
		{"a spec outside the repository", func(m *Manifest) { m.Probes[0].Expect.Stdout = "../greet.stdout" }, "inside the repository"},
		{"a spec inside evaluator state", func(m *Manifest) { m.Probes[0].Expect.Stdout = ".vise/greet.stdout" }, "evaluator state"},
		{"a spec that is the lockfile", func(m *Manifest) { m.Probes[0].Expect.Stdout = "vise.lock" }, "evaluator state"},
		{"a spec that is the manifest", func(m *Manifest) { m.Probes[0].Expect.Stdout = "vise.toml" }, "evaluator state"},
		{"a spec under a symlinked directory", func(m *Manifest) { m.Probes[0].Expect.Stdout = "linked/greet.stdout" }, "symlink components"},
		{"a spec inside Git metadata", func(m *Manifest) { m.Probes[0].Expect.Stdout = ".git/HEAD" }, "Git metadata"},
		{"a spec that is the probe's own artifact", func(m *Manifest) {
			m.Probes[0].Expect.Stdout = "out/report.json"
		}, "also one of its declared artifacts"},
		{"a spec that is another probe's artifact", func(m *Manifest) {
			m.Probes = append(m.Probes, Probe{ID: "other", Run: "true", Timeout: 30, Files: []string{"spec/greet.stdout"}})
		}, "is a declared artifact of probe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := valid()
			test.change(&manifest)
			err := manifest.Validate(root)
			if err == nil {
				t.Fatalf("validation accepted %s", test.name)
			}
			if !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error %q does not mention %q", err.Error(), test.wantIn)
			}
		})
	}
}

// `expect = {}` has to be refused on the real load path, where defaults are
// applied before validation: the first cut defaulted exit to 0 first, which
// turned an empty expectation into a pin expecting silence and exit 0.
func TestLoadManifestRefusesAnEmptyExpectation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n\n[[probe]]\nid = \"greet\"\nrun = \"./bin/greet\"\nexpect = {}\n")
	if _, _, err := LoadManifest(root); err == nil || !strings.Contains(err.Error(), "expect is empty") {
		t.Fatalf("an empty expectation loaded: %v", err)
	}
}

// A spec may coincide with a dependency: both roles read the bytes, and
// forbidding the overlap would push a real dependency into the undeclared
// residual. A spec shared by two artifacts, or two pins, is likewise fine.
func TestPinSpecMayAlsoBeADependency(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/greet.stdout", "hello\n")
	manifest := testManifest(
		Probe{ID: "greet", Run: "./bin/greet", Expect: &Expect{Stdout: "spec/greet.stdout"}},
		Probe{ID: "reads-it", Run: "cat spec/greet.stdout", Deps: []string{"spec/greet.stdout"}},
		Probe{ID: "twin", Run: "./bin/greet --again", Deps: []string{"spec/greet.stdout"}, Expect: &Expect{Stdout: "spec/greet.stdout", Stderr: "spec/greet.stdout"}},
	)
	if err := manifest.Validate(root); err != nil {
		t.Fatalf("a spec shared with a dependency was refused: %v", err)
	}
}

// Absence of a declaration is the only thing that means empty. The manifest
// loads with a spec that is not on disk — doctor and the record pre-flight
// name it — and the expectation reader refuses it by name.
func TestPinSpecThatIsMissingUnreadableOrASymlinkIsRefusedByName(t *testing.T) {
	root := testGitRepo(t)
	manifest := testManifest(Probe{ID: "greet", Run: "./bin/greet", Expect: &Expect{Stdout: "spec/greet.stdout"}})
	if err := manifest.Validate(root); err != nil {
		t.Fatalf("a manifest naming a spec that is not there must still load: %v", err)
	}
	if _, _, err := specContents(root, *manifest.Probes[0].Expect); err == nil || !strings.Contains(err.Error(), `spec "spec/greet.stdout" does not exist`) {
		t.Fatalf("missing spec: %v", err)
	}

	elsewhere := filepath.Join(t.TempDir(), "greet.stdout")
	if err := os.WriteFile(elsewhere, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "spec", "greet.stdout")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := specContents(root, *manifest.Probes[0].Expect); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked spec must be refused, not followed to matching bytes: %v", err)
	}

	if os.Geteuid() != 0 {
		os.Remove(filepath.Join(root, "spec", "greet.stdout"))
		writeTestFile(t, root, "spec/greet.stdout", "hello\n")
		if err := os.Chmod(filepath.Join(root, "spec", "greet.stdout"), 0); err != nil {
			t.Fatal(err)
		}
		_, _, err := specContents(root, *manifest.Probes[0].Expect)
		os.Chmod(filepath.Join(root, "spec", "greet.stdout"), 0o644)
		if err == nil || !strings.Contains(err.Error(), `spec "spec/greet.stdout"`) || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("unreadable spec must be refused by name, never read as empty: %v", err)
		}
	}
}

// A spec larger than the capture bound is refused before anything runs: a
// reviewer shown two hashes cannot review.
func TestPinSpecOverTheCaptureBoundIsRefused(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/big.stdout", strings.Repeat("x", CaptureLimit+1))
	manifest := testManifest(Probe{ID: "big", Run: "true", Expect: &Expect{Stdout: "spec/big.stdout"}})
	if _, _, err := specContents(root, *manifest.Probes[0].Expect); err == nil || !strings.Contains(err.Error(), "capture bound") {
		t.Fatalf("over-bound spec: %v", err)
	}
}

// The expectation is built from the spec files and stored as blobs, so that
// nothing downstream needs to know the bytes came from a human. An undeclared
// stream is expected empty, and the empty blob is stored too.
func TestPinExpectationIsTheSpecBytesAndEmptyForUndeclaredStreams(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/greet.stdout", "hello Ada\n")
	writeTestFile(t, root, "spec/greet.report", "{}\n")
	manifest := testManifest(Probe{
		ID: "greet", Run: "./bin/greet", Files: []string{"out/report.json"},
		Expect: &Expect{Stdout: "spec/greet.stdout", Exit: IntPtr(3), Files: map[string]string{"out/report.json": "spec/greet.report"}},
	})
	blobs := make(map[string][]byte)
	entry, err := pinExpectation(root, manifest.Probes[0], blobs)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Exit != 3 {
		t.Fatalf("exit = %d, want the declared 3", entry.Exit)
	}
	if entry.Stdout != HashBytes([]byte("hello Ada\n")) || string(blobs[entry.Stdout]) != "hello Ada\n" {
		t.Fatalf("stdout expectation is not the spec bytes: %s", entry.Stdout)
	}
	if entry.Stderr != emptyHash || blobs[emptyHash] == nil {
		t.Fatalf("an undeclared stream must be expected empty, with the empty blob stored: %s", entry.Stderr)
	}
	if entry.Files["out/report.json"] != HashBytes([]byte("{}\n")) {
		t.Fatalf("artifact expectation is not the spec bytes: %v", entry.Files)
	}
	if entry.Pin == nil || entry.Pin.AcceptedCommit != nil || len(entry.Pin.Spec) != 2 {
		t.Fatalf("pin provenance = %#v, want two spec hashes and no acceptance", entry.Pin)
	}
}

// run_hash covers expect, so changing which file is the spec is definition
// drift; and a preserve probe's hash is what it was before pins existed, so
// existing lockfiles do not drift on upgrade.
func TestRunHashCoversExpectAndLeavesPreserveProbesAlone(t *testing.T) {
	preserve := Probe{ID: "p", Run: "true", Timeout: 30}
	before, _ := ProbeRunHash(preserve)
	if !strings.Contains(before, "sha256:") {
		t.Fatal(before)
	}
	// The literal is the hash a v0.3.0 vise computed for this probe as loaded
	// through LoadManifest, before Expect existed. It is the one thing that
	// keeps every existing lockfile valid on upgrade, so it is pinned here and
	// not derived from the code under test.
	root := t.TempDir()
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n\n[[probe]]\nid = \"p\"\nrun = \"printf hi\"\n")
	manifest, _, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := ProbeRunHash(manifest.Probes[0])
	const golden = "sha256:44810386e9c1b30d117d0b985006fc6bd9d88f36b1088594cce4fb9485404de2"
	if loaded != golden {
		t.Fatalf("a preserve probe's run_hash moved from the v0.3.0 value: %s != %s; every existing lockfile would report definition drift", loaded, golden)
	}
	pinA := Probe{ID: "p", Run: "true", Timeout: 30, Expect: &Expect{Stdout: "spec/a", Exit: IntPtr(0)}}
	pinB := Probe{ID: "p", Run: "true", Timeout: 30, Expect: &Expect{Stdout: "spec/b", Exit: IntPtr(0)}}
	hashA, _ := ProbeRunHash(pinA)
	hashB, _ := ProbeRunHash(pinB)
	if hashA == hashB || hashA == before {
		t.Fatalf("expect is not in the hash: %s %s %s", before, hashA, hashB)
	}
}

// The manifest loader defaults a pin's exit to 0 and writes it into the
// entry, so run_hash does not depend on whether the operator typed it.
func TestLoadManifestDefaultsAPinsExitToZero(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "vise.toml", `[vise]
version = 1

[[probe]]
id = "greet"
run = "./bin/greet"
expect.stdout = "spec/greet.stdout"
`)
	manifest, _, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	probe := manifest.Probes[0]
	if !probe.IsPin() || probe.Expect.Exit == nil || *probe.Expect.Exit != 0 {
		t.Fatalf("expect = %#v", probe.Expect)
	}
	typed, _ := ProbeRunHash(probe)
	explicit := probe
	explicit.Expect = &Expect{Stdout: "spec/greet.stdout", Exit: IntPtr(0)}
	explicitHash, _ := ProbeRunHash(explicit)
	if typed != explicitHash {
		t.Fatalf("an omitted exit hashes differently from an explicit 0: %s != %s", typed, explicitHash)
	}
}

// The three tolerated conditions are typed on the result, told apart from
// each other and from the hard conditions; a missing artifact keeps the ones
// that were produced beside it.
func TestRunResultCarriesTypedExecutionConditions(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nout/\n")
	testGit(t, root, "add", ".gitignore")
	testGit(t, root, "commit", "-qm", "ignore out")
	runner := Runner{Root: root, Manifest: testManifest()}

	launch := runner.RunProbe(Probe{ID: "gone", Run: "./bin/not-built-yet", Timeout: 5}, true)
	if !launch.LaunchFailed || !launch.Tolerated || launch.TimedOut || launch.Exit != 127 || pinCondition(launch) != "launch_failed" {
		t.Fatalf("launch failure: %#v", launch)
	}

	timeout := runner.RunProbe(Probe{ID: "slow", Run: "printf partial; sleep 5", Timeout: 1}, true)
	if !timeout.TimedOut || !timeout.Tolerated || timeout.LaunchFailed || pinCondition(timeout) != "timed_out" {
		t.Fatalf("timeout: %#v", timeout)
	}

	partial := runner.RunProbe(Probe{ID: "half", Run: "mkdir -p out; printf a > out/a.txt", Timeout: 5, Files: []string{"out/a.txt", "out/b.txt"}}, true)
	if !partial.Tolerated || len(partial.MissingFiles) != 1 || partial.MissingFiles[0] != "out/b.txt" || pinCondition(partial) != "artifact_missing" {
		t.Fatalf("missing artifact: %#v", partial)
	}
	if got := partial.Files["out/a.txt"]; string(got.Prefix) != "a" {
		t.Fatalf("the artifact that was produced was not kept: %#v", partial.Files)
	}
	if !strings.Contains(partial.HarnessError, `"out/b.txt" was not produced`) {
		t.Fatalf("a preserve probe must still read this as a harness error: %q", partial.HarnessError)
	}

	// A hard condition beside a tolerated one clears the tolerance: the run
	// wrote a stray file into the checkout on its way to timing out.
	hard := runner.RunProbe(Probe{ID: "stray", Run: "printf x > stray.txt; ./bin/not-built-yet", Timeout: 5}, true)
	if hard.Tolerated || !hard.LaunchFailed || !strings.Contains(hard.HarnessError, "neither tracks nor ignores") {
		t.Fatalf("hard condition must clear tolerance: %#v", hard)
	}
	os.Remove(filepath.Join(root, "stray.txt"))

	complete := runner.RunProbe(Probe{ID: "ok", Run: "printf ok", Timeout: 5}, true)
	if complete.Tolerated || complete.HarnessError != "" || pinCondition(complete) != "" {
		t.Fatalf("a completed run has no condition: %#v", complete)
	}
}

// Two timed-out runs agree by having both timed out, whatever bytes each
// printed before the kill; every other comparison is the ordinary one plus the
// condition and the missing set.
func TestPinObservationsCompareConditionsNotBytesOnTimeout(t *testing.T) {
	a := RunResult{TimedOut: true, Stdout: Capture{Hash: "sha256:1", Prefix: []byte("1")}}
	b := RunResult{TimedOut: true, Stdout: Capture{Hash: "sha256:2", Prefix: []byte("2")}}
	if !pinObservationsEqual(a, b) {
		t.Fatal("two timeouts must agree")
	}
	if pinObservationsEqual(a, RunResult{}) {
		t.Fatal("a timeout and a completed run must not agree")
	}
	missingB := RunResult{MissingFiles: []string{"out/b"}, Tolerated: true, HarnessError: "x"}
	missingC := RunResult{MissingFiles: []string{"out/c"}, Tolerated: true, HarnessError: "x"}
	if pinObservationsEqual(missingB, missingC) {
		t.Fatal("different missing sets must not agree")
	}
	if !pinObservationsEqual(missingB, missingB) {
		t.Fatal("the same missing set must agree")
	}
	launched := RunResult{Exit: 127, LaunchFailed: true, Tolerated: true, HarnessError: "not found"}
	if pinObservationsEqual(launched, RunResult{Exit: 127}) {
		t.Fatal("a launch failure and an ordinary exit 127 must not agree")
	}
}
