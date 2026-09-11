package vise

import (
	"os"
	"path/filepath"
	"regexp"
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

// pinRepo is a committed repository with one pin whose program does not exist
// yet: the spec is on disk, the manifest names it, and ./bin/greet is not
// there. It returns the root and a loader for the manifest.
func pinRepo(t *testing.T) (string, func() (Manifest, []byte)) {
	t.Helper()
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/greet.stdout", "hello Ada\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"greet\"\nrun = \"./bin/greet\"\nexpect.stdout = \"spec/greet.stdout\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "pin greet before building it")
	load := func() (Manifest, []byte) {
		t.Helper()
		manifest, bytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		return manifest, bytes
	}
	return root, load
}

func buildGreet(t *testing.T, root, output string) {
	t.Helper()
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf '"+output+"'\n")
	if err := os.Chmod(filepath.Join(root, "bin", "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "build greet: "+output)
}

func headCommit(t *testing.T, root string) string {
	t.Helper()
	commit, err := GitHead(root)
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

// Recording before the program exists freezes the spec as the expectation,
// tolerates the launch failure, accepts nothing, and says so — in the
// result, the counts, and the journal — instead of claiming every check
// matched.
func TestRecordFreezesAnUnmetPinWithoutAcceptingIt(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	result := Record(root, manifest, manifestBytes, RecordOptions{})
	if result.Outcome.Exit != ExitOK {
		t.Fatalf("record refused an unbuilt pin: %#v", result.Outcome)
	}
	lock, _, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	entry := lock.Probes["greet"]
	specHash := HashBytes([]byte("hello Ada\n"))
	if entry.Stdout != specHash || entry.Stderr != emptyHash || entry.Exit != 0 {
		t.Fatalf("the expectation is not the spec: %#v", entry)
	}
	if entry.Pin == nil || entry.Pin.AcceptedCommit != nil || entry.Pin.Spec["spec/greet.stdout"] != specHash {
		t.Fatalf("pin provenance = %#v", entry.Pin)
	}
	if data, ok, err := BlobData(root, specHash, false); !ok || err != nil || string(data) != "hello Ada\n" {
		t.Fatalf("the spec bytes were not stored as a blob: %v %v", ok, err)
	}
	if result.Pins == nil || len(result.Pins.Unmet) != 1 || result.Pins.Unmet[0] != "greet" || len(result.Pins.Accepted) != 0 {
		t.Fatalf("pins = %#v", result.Pins)
	}
	counts := result.Outcome.Counts
	if counts.Declared != 1 || counts.Pass != 0 || counts.Unmet != 1 {
		t.Fatalf("counts = %#v; an unmet pin is not a pass", counts)
	}
	if !strings.Contains(result.Outcome.Next.Detail, "1 pin(s) unmet: greet") {
		t.Fatalf("next = %#v", result.Outcome.Next)
	}
	events, err := ReadJournal(root, 5)
	if err != nil || len(events) != 1 || events[0].Pins == nil || len(events[0].Pins.Unmet) != 1 || events[0].Counts.Unmet != 1 || events[0].Counts.Pass != 0 {
		t.Fatalf("journal = %#v, %v", events, err)
	}
}

// Two records on the same tree produce byte-identical lockfiles, an unmet pin
// included, and the review diff between them is empty.
func TestRecordOfAnUnchangedUnmetPinIsByteIdentical(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("first record: %#v", result.Outcome)
	}
	first, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "baseline")
	result := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
	if result.Outcome.Exit != ExitOK {
		t.Fatalf("second record: %#v", result.Outcome)
	}
	second, _ := os.ReadFile(filepath.Join(root, "vise.lock"))
	if string(first) != string(second) {
		t.Fatalf("re-record changed the lockfile:\n%s\n---\n%s", first, second)
	}
	if result.ReviewDiff != "No recorded behavior changed." {
		t.Fatalf("diff = %q", result.ReviewDiff)
	}
}

// Once the program produces the spec, a clean-tree record accepts the pin at
// HEAD; the preview shows the transition as a line of its own; a later record
// on an unchanged tree carries the acceptance instead of restamping it.
func TestRecordAcceptsAMetPinOnACleanTreeAndCarriesItForward(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("first record: %#v", result.Outcome)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "baseline")
	buildGreet(t, root, "hello Ada\\n")
	built := headCommit(t, root)

	preview := Record(root, manifest, manifestBytes, RecordOptions{Preview: true})
	if preview.Outcome.Exit != ExitOK || !strings.Contains(preview.ReviewDiff, "greet pin: unaccepted -> accepted at "+built) {
		t.Fatalf("preview = %#v\n%s", preview.Outcome, preview.ReviewDiff)
	}
	if preview.Pins == nil || len(preview.Pins.Accepted) != 1 {
		t.Fatalf("preview pins = %#v", preview.Pins)
	}
	accepted := Record(root, manifest, manifestBytes, RecordOptions{Accept: preview.Candidate})
	if accepted.Outcome.Exit != ExitOK {
		t.Fatalf("accept: %#v", accepted.Outcome)
	}
	lock, _, _ := LoadLockfile(root)
	if got := lock.Probes["greet"].Pin.AcceptedCommit; got == nil || *got != built {
		t.Fatalf("accepted_commit = %v, want %s", got, built)
	}
	if accepted.Outcome.Counts.Unmet != 0 || accepted.Outcome.Counts.Pass != 1 || strings.Contains(accepted.Outcome.Next.Detail, "unmet") {
		t.Fatalf("an accepted pin reported as unmet: %#v", accepted.Outcome)
	}
	bytesAfterAccept, _ := os.ReadFile(filepath.Join(root, "vise.lock"))

	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "accept greet")
	again := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
	if again.Outcome.Exit != ExitOK {
		t.Fatalf("re-record: %#v", again.Outcome)
	}
	bytesAgain, _ := os.ReadFile(filepath.Join(root, "vise.lock"))
	if string(bytesAfterAccept) != string(bytesAgain) {
		t.Fatalf("acceptance was restamped:\n%s\n---\n%s", bytesAfterAccept, bytesAgain)
	}
}

// A dirty tree is not HEAD, so a record with --allow-dirty freezes an unmet
// pin but never accepts a met one.
func TestRecordOnADirtyTreeNeverAcceptsAPin(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'hello Ada\\n'\n")
	if err := os.Chmod(filepath.Join(root, "bin", "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true})
	if result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	lock, _, _ := LoadLockfile(root)
	if lock.Probes["greet"].Pin.AcceptedCommit != nil {
		t.Fatal("a dirty record accepted a pin; the tree it saw is not HEAD")
	}
	if result.Pins == nil || len(result.Pins.Unmet) != 0 || len(result.Pins.PassingUnaccepted) != 1 {
		t.Fatalf("a met pin on a dirty tree is passing-unaccepted, not unmet: %#v", result.Pins)
	}
}

// An accepted pin whose identity is unchanged must still meet its spec, or
// the record is refused as a behavior failure — acceptance is never revoked
// by a later record, and a regression is never laundered into "not built
// yet".
func TestRecordRefusesToWriteWhenAnAcceptedPinNoLongerMeetsItsSpec(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	buildGreet(t, root, "hello Ada\\n")
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK || len(result.Pins.Accepted) != 1 {
		t.Fatalf("first record: %#v %#v", result.Outcome, result.Pins)
	}
	before, _ := os.ReadFile(filepath.Join(root, "vise.lock"))
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "accepted")
	buildGreet(t, root, "goodbye Ada\\n")

	result := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
	failure := result.Outcome.Failures["greet"]
	if result.Outcome.Exit != ExitBehavior || failure.Class != "behavior" || result.Outcome.Next.Action != NextRevert {
		t.Fatalf("outcome = %#v", result.Outcome)
	}
	if !strings.Contains(failure.Detail, "accepted pin no longer meets its spec") || !strings.Contains(result.Outcome.Next.Detail, "change the spec to re-accept") {
		t.Fatalf("failure = %#v next = %#v", failure, result.Outcome.Next)
	}
	if !strings.Contains(failure.Diff, "-hello Ada") || !strings.Contains(failure.Diff, "+goodbye Ada") {
		t.Fatalf("diff %q does not show spec versus observed", failure.Diff)
	}
	after, _ := os.ReadFile(filepath.Join(root, "vise.lock"))
	if string(before) != string(after) {
		t.Fatal("a refused record wrote the lockfile")
	}

	// The program regressing to a launch failure is the same refusal, not a
	// tolerated condition: tolerance is for pins nobody has accepted.
	os.Remove(filepath.Join(root, "bin", "greet"))
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "remove greet")
	gone := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
	if gone.Outcome.Exit != ExitHarness || gone.Outcome.Failures["greet"].Class != "harness" {
		t.Fatalf("an accepted pin that cannot launch must be harness, not unmet: %#v", gone.Outcome)
	}
}

// Changing the spec changes the pin's identity: the old acceptance is not
// carried, the diff says so, and the pin is unaccepted until the tree meets
// the new spec.
func TestRecordTreatsAChangedSpecAsANewPin(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	buildGreet(t, root, "hello Ada\\n")
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("first record: %#v", result.Outcome)
	}
	accepted := headCommit(t, root)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "accepted")
	writeTestFile(t, root, "spec/greet.stdout", "hello, Ada!\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "raise the bar")

	preview := Record(root, manifest, manifestBytes, RecordOptions{Preview: true})
	want := "greet pin: accepted at " + accepted + " -> unaccepted (spec, definition, or inputs changed; a new acceptance is due)"
	if !strings.Contains(preview.ReviewDiff, want) {
		t.Fatalf("diff lacks %q:\n%s", want, preview.ReviewDiff)
	}
	if !strings.Contains(preview.ReviewDiff, "-hello Ada") || !strings.Contains(preview.ReviewDiff, "+hello, Ada!") {
		t.Fatalf("diff does not show old spec versus new spec:\n%s", preview.ReviewDiff)
	}
	result := Record(root, manifest, manifestBytes, RecordOptions{Accept: preview.Candidate})
	if result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	lock, _, _ := LoadLockfile(root)
	if lock.Probes["greet"].Pin.AcceptedCommit != nil || len(result.Pins.Unmet) != 1 {
		t.Fatalf("a changed spec kept its acceptance: %#v", lock.Probes["greet"].Pin)
	}
}

// A spec that is not on disk stops the record before any probe runs, as an
// operator repair naming the file.
func TestRecordRefusesAMissingSpecByNameBeforeRunningAnything(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	os.Remove(filepath.Join(root, "spec", "greet.stdout"))
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "lose the spec")
	result := Record(root, manifest, manifestBytes, RecordOptions{})
	failure := result.Outcome.Failures["greet"]
	if result.Outcome.Exit != ExitHarness || !failure.Operator || result.Outcome.Next.Action != NextHuman {
		t.Fatalf("outcome = %#v", result.Outcome)
	}
	if !strings.Contains(failure.Detail, `spec "spec/greet.stdout" does not exist`) {
		t.Fatalf("detail = %q", failure.Detail)
	}
	if _, err := os.Stat(filepath.Join(root, ".vise", "tmp")); err == nil {
		t.Fatal("a probe ran: the scratch directory exists")
	}
}

// A skeleton that exits cleanly without writing its artifact yet is an
// unmet pin, not a broken harness — the freeze proceeds with the missing
// artifact as the pin's stable condition.
func TestRecordToleratesAMissingArtifactOnAnUnacceptedPin(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nout/\n")
	writeTestFile(t, root, "spec/report.json", "{}\n")
	writeTestFile(t, root, "bin/report", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(root, "bin", "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"report\"\nrun = \"./bin/report\"\nfiles = [\"out/report.json\"]\nexpect.files = { \"out/report.json\" = \"spec/report.json\" }\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "skeleton")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	result := Record(root, manifest, manifestBytes, RecordOptions{})
	if result.Outcome.Exit != ExitOK || result.Pins == nil || len(result.Pins.Unmet) != 1 {
		t.Fatalf("outcome = %#v pins = %#v", result.Outcome, result.Pins)
	}
	lock, _, _ := LoadLockfile(root)
	if lock.Probes["report"].Files["out/report.json"] != HashBytes([]byte("{}\n")) {
		t.Fatalf("artifact expectation = %v", lock.Probes["report"].Files)
	}
}

func recordPinRepo(t *testing.T, root string, manifest Manifest, manifestBytes []byte) {
	t.Helper()
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "baseline")
}

// The unmet table, row by row, on a pin nobody has accepted. Exit 6, class
// unmet, next build; the diff is spec versus observed; the detail says what
// the run did so a 127 is visible as a 127.
func TestVerifyClassifiesAnUnacceptedPinPerTheUnmetTable(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	recordPinRepo(t, root, manifest, manifestBytes)

	check := func(name, wantDetail string) VerifyResult {
		t.Helper()
		result := Verify(root, manifest, manifestBytes, VerifyOptions{})
		outcome := result.Outcome
		failure := outcome.Failures["greet"]
		if outcome.Exit != ExitUnmet || outcome.Verdict != "red" || failure.Class != "unmet" || outcome.Next.Action != NextBuild {
			t.Fatalf("%s: outcome = %#v", name, outcome)
		}
		if !strings.Contains(failure.Detail, wantDetail) {
			t.Fatalf("%s: detail %q lacks %q", name, failure.Detail, wantDetail)
		}
		if outcome.Counts.Unmet != 1 || outcome.Counts.Pass != 0 || outcome.Classes[0] != "unmet" {
			t.Fatalf("%s: counts = %#v classes = %v", name, outcome.Counts, outcome.Classes)
		}
		if outcome.Pins == nil || outcome.Pins.Evaluated != 1 || outcome.Pins.UnmetCount != 1 || outcome.Pins.Unmet[0] != "greet" {
			t.Fatalf("%s: pins = %#v", name, outcome.Pins)
		}
		if !strings.Contains(outcome.Next.Detail, "1 pin(s) unmet: greet") || !strings.Contains(outcome.Next.Detail, "do not revert") || !strings.Contains(outcome.Next.Detail, "vise verify --probe <id> shows the diff") {
			t.Fatalf("%s: next = %#v", name, outcome.Next)
		}
		return result
	}

	// Row: launch failure, stable — the program does not exist.
	launch := check("launch failure", "not built yet: probe could not be launched (exit 127)")
	if !strings.Contains(launch.Outcome.Failures["greet"].Detail, "greet") {
		t.Fatalf("the missing word is not named: %q", launch.Outcome.Failures["greet"].Detail)
	}

	// Row: stable mismatch — the program exists and prints the wrong thing.
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'hi Ada\\n'\n")
	if err := os.Chmod(filepath.Join(root, "bin", "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	mismatch := check("mismatch", "does not match the spec")
	if diff := mismatch.Outcome.Failures["greet"].Diff; !strings.Contains(diff, "-hello Ada") || !strings.Contains(diff, "+hi Ada") {
		t.Fatalf("diff %q is not spec versus observed", diff)
	}

	// Row: unstable — flake, never unmet.
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nif test -f .toggle; then rm .toggle; printf 'a\\n'; else touch .toggle; printf 'b\\n'; fi\n")
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.toggle\n")
	flake := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if flake.Outcome.Exit != ExitIndeterminate || flake.Outcome.Failures["greet"].Class != "flake" || flake.Outcome.Counts.Unmet != 0 {
		t.Fatalf("an unstable unmet pin must be a flake: %#v", flake.Outcome)
	}
	os.Remove(filepath.Join(root, ".toggle"))

	// Row: a hard condition beside a tolerated one is harness, whatever the
	// phase — the run left a stray file in the checkout.
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf x > stray.txt; ./bin/not-built\n")
	hard := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if hard.Outcome.Exit != ExitHarness || hard.Outcome.Failures["greet"].Class != "harness" {
		t.Fatalf("a hard condition on an unmet pin must be harness: %#v", hard.Outcome)
	}
	os.Remove(filepath.Join(root, "stray.txt"))

	// Row: the spec met — pass, and named as passing-unaccepted on a green
	// gate, never silently promoted.
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'hello Ada\\n'\n")
	met := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if met.Outcome.Exit != ExitOK || met.Outcome.Verdict != "green" || met.Outcome.Counts.Pass != 1 {
		t.Fatalf("met: %#v", met.Outcome)
	}
	if met.Outcome.Pins == nil || met.Outcome.Pins.PassingUnacceptedCount != 1 || met.Outcome.Pins.PassingUnaccepted[0] != "greet" {
		t.Fatalf("passing-unaccepted not reported: %#v", met.Outcome.Pins)
	}
	if !strings.Contains(met.Outcome.Next.Detail, "1 pin(s) passing, not yet accepted: greet") {
		t.Fatalf("next = %#v", met.Outcome.Next)
	}
	lock, _, _ := LoadLockfile(root)
	if lock.Probes["greet"].Pin.AcceptedCommit != nil {
		t.Fatal("a gate accepted a pin; only record may")
	}
}

// Row: timeout, stable — the bytes printed before the kill differ between
// the two runs (each prints its pid) and are not compared; two timeouts agree
// by having both timed out. The manifest declares the one-second timeout, so
// the definition is what was recorded.
func TestVerifyTreatsAStableTimeoutOnAnUnacceptedPinAsUnmet(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/slow.stdout", "done\n")
	writeTestFile(t, root, "bin/slow", "#!/bin/sh\nprintf \"$$\"; sleep 5\n")
	if err := os.Chmod(filepath.Join(root, "bin", "slow"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"slow\"\nrun = \"./bin/slow\"\ntimeout = 1\nexpect.stdout = \"spec/slow.stdout\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "slow pin")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, root, manifest, manifestBytes)
	result := Verify(root, manifest, manifestBytes, VerifyOptions{})
	failure := result.Outcome.Failures["slow"]
	if result.Outcome.Exit != ExitUnmet || failure.Class != "unmet" || result.Outcome.Counts.Flaky != 0 {
		t.Fatalf("two timeouts printing different bytes must agree as unmet: %#v", result.Outcome)
	}
	if !strings.Contains(failure.Detail, "not built yet: probe timed out after 1s") || failure.Diff != "probe timed out after 1s" {
		t.Fatalf("failure = %#v", failure)
	}
}

// A skeleton that exits cleanly without its artifact is unmet with the
// artifact named; the artifacts it did produce are compared.
func TestVerifyTreatsAMissingArtifactOnAnUnacceptedPinAsUnmet(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nout/\n")
	writeTestFile(t, root, "spec/a.json", "a\n")
	writeTestFile(t, root, "spec/b.json", "b\n")
	writeTestFile(t, root, "bin/report", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(root, "bin", "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"report\"\nrun = \"./bin/report\"\nfiles = [\"out/a.json\", \"out/b.json\"]\nexpect.files = { \"out/a.json\" = \"spec/a.json\", \"out/b.json\" = \"spec/b.json\" }\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "skeleton")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, root, manifest, manifestBytes)

	result := Verify(root, manifest, manifestBytes, VerifyOptions{})
	failure := result.Outcome.Failures["report"]
	if result.Outcome.Exit != ExitUnmet || failure.Class != "unmet" || !strings.Contains(failure.Detail, `declared artifact "out/a.json" was not produced`) {
		t.Fatalf("outcome = %#v", result.Outcome)
	}

	// Half built: a is right, b still missing — still unmet, still naming b.
	writeTestFile(t, root, "bin/report", "#!/bin/sh\nmkdir -p out; printf 'a\\n' > out/a.json\n")
	half := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if half.Outcome.Exit != ExitUnmet || !strings.Contains(half.Outcome.Failures["report"].Detail, `"out/b.json" was not produced`) {
		t.Fatalf("half built: %#v", half.Outcome)
	}
	if diff := half.Outcome.Failures["report"].Diff; !strings.Contains(diff, "file/out/b.json: the baseline records it and the run produced nothing") {
		t.Fatalf("diff = %q", diff)
	}
}

// An accepted pin is judged exactly as a preserve probe: a divergence is
// behavior → revert (exit 1, which outranks any unmet pin), and a condition
// is harness — tolerance is for pins nobody has accepted.
func TestVerifyJudgesAnAcceptedPinAsAPreserveProbe(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	buildGreet(t, root, "hello Ada\\n")
	recordPinRepo(t, root, manifest, manifestBytes)

	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'goodbye Ada\\n'\n")
	regressed := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if regressed.Outcome.Exit != ExitBehavior || regressed.Outcome.Failures["greet"].Class != "behavior" || regressed.Outcome.Next.Action != NextRevert {
		t.Fatalf("regressed: %#v", regressed.Outcome)
	}
	if regressed.Outcome.Pins == nil || regressed.Outcome.Pins.Evaluated != 1 || regressed.Outcome.Pins.UnmetCount != 0 {
		t.Fatalf("pins = %#v", regressed.Outcome.Pins)
	}

	os.Remove(filepath.Join(root, "bin", "greet"))
	gone := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if gone.Outcome.Exit != ExitHarness || gone.Outcome.Failures["greet"].Class != "harness" {
		t.Fatalf("an accepted pin that cannot launch is harness: %#v", gone.Outcome)
	}
}

// Behavior outranks unmet: while building, breaking something that already
// held is the first thing to undo. And metrics are not run while a pin is
// unmet, counted as skipped rather than as passes.
func TestVerifyPrecedenceBehaviorOverUnmetAndMetricsSkipped(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/greet.stdout", "hello Ada\n")
	writeTestFile(t, root, "bin/old", "#!/bin/sh\nprintf 'old\\n'\n")
	if err := os.Chmod(filepath.Join(root, "bin", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "metric.sh", "#!/bin/sh\nprintf 1\n")
	writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "old"
run = "./bin/old"
[[probe]]
id = "greet"
run = "./bin/greet"
expect.stdout = "spec/greet.stdout"
[[metric]]
id = "count"
run = "sh metric.sh"
[[metric]]
id = "count2"
run = "sh metric.sh"
`)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "one preserve, one pin, two metrics")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, root, manifest, manifestBytes)

	unmet := Verify(root, manifest, manifestBytes, VerifyOptions{})
	counts := unmet.Outcome.Counts
	if unmet.Outcome.Exit != ExitUnmet || counts.Declared != 4 || counts.Pass != 1 || counts.Unmet != 1 || counts.Skipped != 2 {
		t.Fatalf("unmet with metrics: exit %d counts %#v", unmet.Outcome.Exit, counts)
	}
	if len(unmet.Outcome.Metrics) != 0 {
		t.Fatalf("metrics were evaluated while a pin was unmet: %#v", unmet.Outcome.Metrics)
	}

	writeTestFile(t, root, "bin/old", "#!/bin/sh\nprintf 'changed\\n'\n")
	both := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if both.Outcome.Exit != ExitBehavior || both.Outcome.Next.Action != NextRevert || both.Outcome.Counts.Behavior != 1 || both.Outcome.Counts.Unmet != 1 {
		t.Fatalf("behavior must outrank unmet: %#v", both.Outcome)
	}
	if got := both.Outcome.Classes; len(got) != 2 || got[0] != "behavior" || got[1] != "unmet" {
		t.Fatalf("classes = %v", got)
	}
}

// The planted failure: an agent that cannot write vise.lock edits the spec
// to match its output instead. The spec's hash is in the lockfile, so the
// gate answers harness, operator, human — never green, never build.
func TestVerifyRoutesAnEditedSpecToAHumanNotToGreen(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	recordPinRepo(t, root, manifest, manifestBytes)
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'whatever I printed\\n'\n")
	if err := os.Chmod(filepath.Join(root, "bin", "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "spec/greet.stdout", "whatever I printed\n")

	result := Verify(root, manifest, manifestBytes, VerifyOptions{})
	failure := result.Outcome.Failures["greet"]
	if result.Outcome.Exit != ExitHarness || failure.Class != "harness" || !failure.Operator || result.Outcome.Next.Action != NextHuman {
		t.Fatalf("an edited spec gated %#v", result.Outcome)
	}
	if !strings.Contains(failure.Detail, "spec changed after recording, not behavior") {
		t.Fatalf("detail = %q", failure.Detail)
	}

	// Deleting the spec, or replacing it with a symlink to matching bytes,
	// is the same answer by name.
	os.Remove(filepath.Join(root, "spec", "greet.stdout"))
	missing := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if missing.Outcome.Exit != ExitHarness || !strings.Contains(missing.Outcome.Failures["greet"].Detail, `spec "spec/greet.stdout" does not exist`) {
		t.Fatalf("missing spec: %#v", missing.Outcome)
	}
	elsewhere := filepath.Join(t.TempDir(), "greet.stdout")
	if err := os.WriteFile(elsewhere, []byte("hello Ada\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "spec", "greet.stdout")); err != nil {
		t.Fatal(err)
	}
	linked := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if linked.Outcome.Exit != ExitHarness || !strings.Contains(linked.Outcome.Failures["greet"].Detail, "symlink") {
		t.Fatalf("symlinked spec with matching bytes must not be followed: %#v", linked.Outcome)
	}
}

// An unmet-only verdict does not end a flake chain: unmet is the build loop's
// resting state, and letting it renew the budget would let an agent buy
// reruns by alternating a flaky implementation with a deliberate 127.
func TestAnUnmetOnlyVerdictDoesNotResetTheFlakeBudget(t *testing.T) {
	commit, lock := strings.Repeat("a", 40), "sha256:lock"
	flake := JournalEvent{Event: "flake", Commit: commit, Lock: lock, Verdict: "indeterminate", Flaky: []string{"greet"}, Probes: []string{"greet"}}
	unmet := JournalEvent{Event: "gate", Commit: commit, Lock: lock, Verdict: "red", Counts: &Counts{Declared: 1, Unmet: 1}, Probes: []string{"greet"}}
	behavior := JournalEvent{Event: "gate", Commit: commit, Lock: lock, Verdict: "red", Counts: &Counts{Declared: 1, Behavior: 1}, Probes: []string{"greet"}}

	if count, _ := ConsecutiveFlakes([]JournalEvent{flake, unmet, flake}, commit, lock, []string{"greet"}); count != 2 {
		t.Fatalf("an unmet-only red reset the chain: count = %d, want 2", count)
	}
	if count, _ := ConsecutiveFlakes([]JournalEvent{flake, behavior, flake}, commit, lock, []string{"greet"}); count != 1 {
		t.Fatalf("a behavior red must still end the chain: count = %d, want 1", count)
	}
}

// Pin state travels on the gate as a bounded summary of what was evaluated:
// --probe reports one pin, and ids are capped at three with a count.
func TestGatePinSummaryIsBoundedAndScopedToWhatRan(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/out", "hello\n")
	body := "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n"
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5"} {
		body += "[[probe]]\nid = \"" + id + "\"\nrun = \"./bin/" + id + "\"\nexpect.stdout = \"spec/out\"\n"
	}
	writeTestFile(t, root, "vise.toml", body)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "five pins")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, root, manifest, manifestBytes)

	all := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if all.Outcome.Pins.Evaluated != 5 || all.Outcome.Pins.UnmetCount != 5 || len(all.Outcome.Pins.Unmet) != 3 {
		t.Fatalf("pins = %#v", all.Outcome.Pins)
	}
	if !strings.Contains(all.Outcome.Next.Detail, "5 pin(s) unmet: p1, p2, p3, … and 2 more") {
		t.Fatalf("next = %#v", all.Outcome.Next)
	}
	one := Verify(root, manifest, manifestBytes, VerifyOptions{ProbeID: "p4"})
	if one.Outcome.Pins.Evaluated != 1 || one.Outcome.Pins.UnmetCount != 1 || one.Outcome.Pins.Unmet[0] != "p4" {
		t.Fatalf("--probe pins = %#v", one.Outcome.Pins)
	}
}

// The record self-test on an unaccepted pin: a stable timeout freezes; two
// passes that disagree on the observation refuse with exit 3; a hard
// condition refuses with exit 2 whatever the pin's phase.
func TestRecordSelfTestOnAnUnacceptedPin(t *testing.T) {
	setup := func(t *testing.T, program, extraIgnore string) (string, Manifest, []byte) {
		t.Helper()
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n"+extraIgnore)
		writeTestFile(t, root, "spec/p.stdout", "done\n")
		writeTestFile(t, root, "bin/p", program)
		if err := os.Chmod(filepath.Join(root, "bin", "p"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"p\"\nrun = \"./bin/p\"\ntimeout = 1\nexpect.stdout = \"spec/p.stdout\"\n")
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "pin")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		return root, manifest, manifestBytes
	}

	root, manifest, manifestBytes := setup(t, "#!/bin/sh\nprintf \"$$\"; sleep 5\n", "")
	timeout := Record(root, manifest, manifestBytes, RecordOptions{})
	if timeout.Outcome.Exit != ExitOK || timeout.Pins == nil || len(timeout.Pins.Unmet) != 1 {
		t.Fatalf("a stable timeout must freeze as unmet: %#v", timeout.Outcome)
	}

	root, manifest, manifestBytes = setup(t, "#!/bin/sh\nif test -f .toggle; then rm .toggle; printf a; else touch .toggle; printf b; fi\n", ".toggle\n")
	unstable := Record(root, manifest, manifestBytes, RecordOptions{})
	if unstable.Outcome.Exit != ExitIndeterminate || unstable.Outcome.Failures["p"].Class != "flake" {
		t.Fatalf("passes that disagree must refuse with exit 3: %#v", unstable.Outcome)
	}
	if _, err := os.Stat(filepath.Join(root, "vise.lock")); err == nil {
		t.Fatal("a refused self-test wrote a lockfile")
	}

	root, manifest, manifestBytes = setup(t, "#!/bin/sh\nprintf x > stray.txt; ./bin/not-built\n", "")
	hard := Record(root, manifest, manifestBytes, RecordOptions{})
	if hard.Outcome.Exit != ExitHarness || hard.Outcome.Failures["p"].Class != "harness" || !strings.Contains(hard.Outcome.Failures["p"].Detail, "neither tracks nor ignores") {
		t.Fatalf("a hard condition on an unaccepted pin must refuse with exit 2: %#v", hard.Outcome)
	}
}

// A metric held back behind a behavior failure is skipped, not passed. v0.3
// counted it as a pass: one behavior failure beside one metric read 1/2 with
// the metric never run. No pins involved; this is the counts rule on its own.
func TestAMetricHeldBackBehindABehaviorFailureIsSkippedNotPassed(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "bin/p", "#!/bin/sh\nprintf ok\n")
	if err := os.Chmod(filepath.Join(root, "bin", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"p\"\nrun = \"./bin/p\"\n[[metric]]\nid = \"m\"\nrun = \"printf 1\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "probe and metric")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, root, manifest, manifestBytes)
	writeTestFile(t, root, "bin/p", "#!/bin/sh\nprintf changed\n")
	result := Verify(root, manifest, manifestBytes, VerifyOptions{})
	counts := result.Outcome.Counts
	if result.Outcome.Exit != ExitBehavior || counts.Declared != 2 || counts.Pass != 0 || counts.Behavior != 1 || counts.Skipped != 1 {
		t.Fatalf("counts = %#v", counts)
	}
}

// status reports pins from the lockfile only — recorded acceptance, never a
// live observation — bounded at three ids, and says nothing at all when the
// baseline declares no pin.
func TestStatusReportsRecordedPinAcceptanceOnly(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	recordPinRepo(t, root, manifest, manifestBytes)

	report := BuildStatus(root)
	pins := report.Lock.Pins
	if pins == nil || pins.Declared != 1 || pins.Accepted != 0 || pins.UnacceptedCount != 1 || len(pins.Unaccepted) != 1 || pins.Unaccepted[0] != "greet" {
		t.Fatalf("pins = %#v", pins)
	}

	// The tree now meets the spec, but nobody has recorded: status must not
	// say so, because it did not run the probe and acceptance is record's.
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'hello Ada\\n'\n")
	if err := os.Chmod(filepath.Join(root, "bin", "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	if again := BuildStatus(root); again.Lock.Pins.Accepted != 0 || again.Lock.Pins.UnacceptedCount != 1 {
		t.Fatalf("status reported a live observation as acceptance: %#v", again.Lock.Pins)
	}

	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "build greet")
	if result := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true}); result.Outcome.Exit != ExitOK || len(result.Pins.Accepted) != 1 {
		t.Fatalf("record: %#v %#v", result.Outcome, result.Pins)
	}
	if accepted := BuildStatus(root); accepted.Lock.Pins.Accepted != 1 || accepted.Lock.Pins.UnacceptedCount != 0 || len(accepted.Lock.Pins.Unaccepted) != 0 {
		t.Fatalf("after acceptance: %#v", accepted.Lock.Pins)
	}

	plain := testGitRepo(t)
	writeTestFile(t, plain, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"p\"\nrun = \"printf ok\"\n")
	testGit(t, plain, "add", ".")
	testGit(t, plain, "commit", "-qm", "no pins")
	m, b, err := LoadManifest(plain)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, plain, m, b)
	if BuildStatus(plain).Lock.Pins != nil {
		t.Fatal("a baseline with no pins must report no pins object")
	}
}

// doctor names a spec that is missing, uncommitted, modified since its
// commit, or under an ignore rule — each an operator repair a fresh clone
// would otherwise discover — and says nothing about a committed one.
func TestDoctorReportsSpecGaps(t *testing.T) {
	root, load := pinRepo(t)
	manifest, _ := load()
	findingsFor := func(check string) []DoctorFinding {
		var out []DoctorFinding
		for _, f := range Doctor(root).Findings {
			if f.Check == check {
				out = append(out, f)
			}
		}
		return out
	}
	if f := findingsFor("spec-committed"); len(f) != 0 {
		t.Fatalf("a committed spec was reported: %#v", f)
	}
	if f := findingsFor("spec-ignored"); len(f) != 0 {
		t.Fatalf("an unignored spec was reported: %#v", f)
	}

	writeTestFile(t, root, "spec/greet.stdout", "edited\n")
	if f := findingsFor("spec-committed"); len(f) != 1 || !strings.Contains(f[0].Detail, "differs from the committed one") {
		t.Fatalf("modified spec: %#v", f)
	}
	testGit(t, root, "checkout", "--", "spec/greet.stdout")

	writeTestFile(t, root, "spec/new.stdout", "new\n")
	manifest.Probes = append(manifest.Probes, Probe{ID: "second", Run: "./bin/second", Expect: &Expect{Stdout: "spec/new.stdout"}})
	if f := checkSpecsCommitted(root, manifest); len(f) != 1 || !strings.Contains(f[0].Detail, "is not committed") {
		t.Fatalf("uncommitted spec: %#v", f)
	}
	testGit(t, root, "add", "spec/new.stdout")
	if f := checkSpecsCommitted(root, manifest); len(f) != 1 || !strings.Contains(f[0].Detail, "is not committed") {
		t.Fatalf("a staged spec is tracked and still not what a clone gets: %#v", f)
	}

	os.Remove(filepath.Join(root, "spec", "greet.stdout"))
	if f := findingsFor("spec-committed"); len(f) != 1 || !strings.Contains(f[0].Detail, "does not exist") {
		t.Fatalf("missing spec: %#v", f)
	}
	testGit(t, root, "checkout", "--", "spec/greet.stdout")

	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nspec/\n")
	if f := findingsFor("spec-ignored"); len(f) != 1 || !strings.Contains(f[0].Detail, "spec/greet.stdout") {
		t.Fatalf("ignored spec: %#v", f)
	}
}

// Findings from the between-batch cold review (GPT via codex, 2026-09-12),
// each pinned so the fix cannot drift out.

// Two timed-out runs still compare their missing-artifact sets: a run that
// sometimes writes its artifact before hanging is unstable, not slow.
func TestTimedOutPinRunsStillCompareTheirArtifactSets(t *testing.T) {
	a := RunResult{TimedOut: true, Tolerated: true, HarnessError: "t", MissingFiles: []string{"out/a"}}
	b := RunResult{TimedOut: true, Tolerated: true, HarnessError: "t"}
	if pinObservationsEqual(a, b) {
		t.Fatal("different artifact sets across two timeouts must not agree")
	}
	if !pinObservationsEqual(a, a) {
		t.Fatal("the same timeout must agree with itself")
	}
}

// A probe killed by a signal is a condition, never an exit a pin could
// expect: tolerated on an unaccepted pin, harness on an accepted one.
func TestASignalDeathIsATypedConditionNotAnExit(t *testing.T) {
	root := testGitRepo(t)
	runner := Runner{Root: root, Manifest: testManifest()}
	run := runner.RunProbe(Probe{ID: "crash", Run: "kill -9 $$", Timeout: 5}, true)
	if !run.Terminated || !run.Tolerated || run.Exit >= 0 || pinCondition(run) != "terminated" || !strings.Contains(run.HarnessError, "terminated by a signal") {
		t.Fatalf("signal death: %#v", run)
	}

	writeTestFile(t, root, "spec/crash.stdout", "ok\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"crash\"\nrun = \"kill -9 $$\"\nexpect.stdout = \"spec/crash.stdout\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "crashing pin")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, root, manifest, manifestBytes)
	result := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if result.Outcome.Exit != ExitUnmet || !strings.Contains(result.Outcome.Failures["crash"].Detail, "terminated by a signal") {
		t.Fatalf("a crashing unaccepted pin is unmet, naming the signal: %#v", result.Outcome)
	}
}

// --allow-dirty freezes a met pin without accepting it, and the record says
// which pins were met rather than calling them unmet.
func TestADirtyRecordReportsAMetPinAsPassingNotUnmet(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'hello Ada\\n'\n")
	if err := os.Chmod(filepath.Join(root, "bin", "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true})
	if result.Outcome.Exit != ExitOK || result.Pins == nil {
		t.Fatalf("record: %#v", result.Outcome)
	}
	if len(result.Pins.Unmet) != 0 || len(result.Pins.PassingUnaccepted) != 1 || result.Pins.PassingUnaccepted[0] != "greet" || len(result.Pins.Accepted) != 0 {
		t.Fatalf("pins = %#v", result.Pins)
	}
	if result.Outcome.Counts.Unmet != 0 || !strings.Contains(result.Outcome.Next.Detail, "met but not accepted because the tree is dirty") {
		t.Fatalf("outcome = %#v", result.Outcome)
	}
}

// The unmet detail names the part of the observation that missed the spec.
func TestUnmetDetailNamesTheStreamThatMissed(t *testing.T) {
	expected := ProbeLock{Exit: 0, Stdout: "sha256:a", Stderr: "sha256:b", Files: map[string]string{"out/x": "sha256:c"}}
	cases := []struct {
		run  RunResult
		want string
	}{
		{RunResult{Exit: 3, Stdout: Capture{Hash: "sha256:a"}}, "exit 3 where the spec expects 0"},
		{RunResult{Stdout: Capture{Hash: "sha256:z"}}, "stdout does not match"},
		{RunResult{Stdout: Capture{Hash: "sha256:a"}, Stderr: Capture{Hash: "sha256:z"}}, "stderr does not match"},
		{RunResult{Stdout: Capture{Hash: "sha256:a"}, Stderr: Capture{Hash: "sha256:b"}, Files: map[string]Capture{"out/x": {Hash: "sha256:z"}}}, "artifact out/x does not match"},
		{RunResult{LaunchFailed: true, Tolerated: true, HarnessError: "probe could not be launched (exit 127)"}, "not built yet: probe could not be launched"},
	}
	for _, c := range cases {
		if got := unmetDetail(c.run, expected); !strings.Contains(got, c.want) {
			t.Fatalf("detail %q lacks %q", got, c.want)
		}
	}
}

// A lockfile whose pin object is malformed is refused at load, as every
// other malformed hash or commit is.
func TestLockfileRefusesAMalformedPinObject(t *testing.T) {
	root, load := pinRepo(t)
	manifest, manifestBytes := load()
	recordPinRepo(t, root, manifest, manifestBytes)
	good, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(good), `"accepted_commit": null`) {
		t.Fatalf("an unaccepted pin must carry accepted_commit as null, never omit it:\n%s", good)
	}
	specHash := HashBytes([]byte("hello Ada\n"))
	for name, mutate := range map[string]func(string) string{
		"a spec hash that is not a hash": func(s string) string {
			return strings.Replace(s, `"spec/greet.stdout": "`+specHash+`"`, `"spec/greet.stdout": "nonsense"`, 1)
		},
		"an accepted_commit that is not a commit": func(s string) string {
			return strings.Replace(s, `"accepted_commit": null`, `"accepted_commit": "abc"`, 1)
		},
		"a pin with no spec hashes": func(s string) string {
			return regexp.MustCompile(`(?s)"spec": \{[^}]*\}`).ReplaceAllString(s, `"spec": {}`)
		},
	} {
		mutated := mutate(string(good))
		if mutated == string(good) {
			t.Fatalf("%s: the mutation did not apply:\n%s", name, good)
		}
		if err := os.WriteFile(filepath.Join(root, "vise.lock"), []byte(mutated), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadLockfile(root); err == nil {
			t.Fatalf("%s loaded", name)
		}
	}
	os.WriteFile(filepath.Join(root, "vise.lock"), good, 0o644)

	// And a lockfile whose expectation is not what the spec says — the spec
	// hashes match, the stdout hash names other bytes — is refused by the
	// gate's pre-flight as an operator repair, never judged.
	other := HashBytes([]byte("other\n"))
	if err := WriteBlobs(root, map[string][]byte{other: []byte("other\n")}); err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(good), `"stdout": "`+specHash+`"`, `"stdout": "`+other+`"`, 1)
	if err := os.WriteFile(filepath.Join(root, "vise.lock"), []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if result.Outcome.Exit != ExitHarness || !strings.Contains(result.Outcome.Failures["greet"].Detail, "not what its spec files say") {
		t.Fatalf("a lockfile expectation that is not the spec must be refused: %#v", result.Outcome)
	}
}

// Identity is run_hash plus deps plus spec hashes: changing the command or an
// input resets acceptance the same way changing the spec does.
func TestPinIdentityCoversTheCommandAndTheInputs(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "spec/greet.stdout", "hello Ada\n")
	writeTestFile(t, root, "fixtures/name", "Ada\n")
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'hello %s\\n' \"$(cat fixtures/name)\"\n")
	if err := os.Chmod(filepath.Join(root, "bin", "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestText := "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"greet\"\nrun = \"./bin/greet\"\ndeps = [\"fixtures/name\"]\nexpect.stdout = \"spec/greet.stdout\"\n"
	writeTestFile(t, root, "vise.toml", manifestText)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "pin with a dep")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	recordPinRepo(t, root, manifest, manifestBytes)
	if lock, _, _ := LoadLockfile(root); lock.Probes["greet"].Pin.AcceptedCommit == nil {
		t.Fatal("the pin was not accepted on a clean, met tree")
	}

	// A changed input: still met (the output is the same because the spec
	// and the fixture were changed together), but a new identity.
	writeTestFile(t, root, "fixtures/name", "Ada\n\n")
	writeTestFile(t, root, "bin/greet", "#!/bin/sh\nprintf 'hello %s\\n' \"$(head -1 fixtures/name)\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "change the input")
	preview := Record(root, manifest, manifestBytes, RecordOptions{Preview: true})
	if !strings.Contains(preview.ReviewDiff, "accepted afresh") {
		t.Fatalf("a changed dep must reset the identity:\n%s", preview.ReviewDiff)
	}

	// A changed command: same.
	writeTestFile(t, root, "vise.toml", strings.Replace(manifestText, "./bin/greet", "sh bin/greet", 1))
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "change the command")
	manifest2, manifestBytes2, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	preview = Record(root, manifest2, manifestBytes2, RecordOptions{Preview: true})
	if !strings.Contains(preview.ReviewDiff, "accepted afresh") {
		t.Fatalf("a changed command must reset the identity:\n%s", preview.ReviewDiff)
	}
}
