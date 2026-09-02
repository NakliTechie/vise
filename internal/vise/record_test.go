package vise

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordFlakeShowsInMemoryDivergenceAndNamesTheRemedy(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "probe.sh", "#!/bin/sh\nif test -f .toggle; then rm .toggle; printf 'line1\\nline2-b\\n'; else touch .toggle; printf 'line1\\nline2-a\\n'; fi\n")
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.toggle\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"flaky\"\nrun = \"sh probe.sh\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "flaky")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	result := Record(root, manifest, manifestBytes, RecordOptions{})
	failure := result.Outcome.Failures["flaky"]
	if result.Outcome.Exit != ExitIndeterminate || failure.Class != "flake" {
		t.Fatalf("outcome = %#v", result.Outcome)
	}
	for _, want := range []string{" line1", "-line2-a", "+line2-b"} {
		if !strings.Contains(failure.Diff, want) {
			t.Fatalf("diff %q lacks %q", failure.Diff, want)
		}
	}
	if result.Outcome.Next.Action != "fix_probe" || !strings.Contains(result.Outcome.Next.Detail, "deterministic") {
		t.Fatalf("next = %#v", result.Outcome.Next)
	}
}

func TestLockfileDiffCoversFingerprintMetricsAndDeps(t *testing.T) {
	root := t.TempDir()
	stdout := HashBytes([]byte("same"))
	if err := WriteBlobs(root, map[string][]byte{stdout: []byte("same")}); err != nil {
		t.Fatal(err)
	}
	old := Lockfile{
		Fingerprint: Fingerprint{OS: "darwin", Arch: "arm64", Stubs: StubSettings{Seed: "1729"}},
		Probes:      map[string]ProbeLock{"p": {Exit: 0, Stdout: stdout, Stderr: stdout, Deps: map[string]string{"fixture.txt": "sha256:aaaa"}}},
		Metrics:     map[string]MetricLock{"complexity": {Value: 10, ToolVersion: "v1"}, "gone": {Value: 1}},
	}
	updated := Lockfile{
		Fingerprint: Fingerprint{OS: "darwin", Arch: "arm64", Stubs: StubSettings{Seed: "42"}},
		Probes:      map[string]ProbeLock{"p": {Exit: 0, Stdout: stdout, Stderr: stdout, Deps: map[string]string{"fixture.txt": "sha256:bbbb"}}},
		Metrics:     map[string]MetricLock{"complexity": {Value: 12, ToolVersion: "v2"}, "added": {Value: 3}},
	}
	diff := LockfileDiff(root, old, updated, nil)
	for _, want := range []string{
		"p dep fixture.txt: sha256:aaaa -> sha256:bbbb",
		"fingerprint: manifest [stubs] differ from the recorded baseline",
		"+ metric added",
		"- metric gone",
		"complexity value: 10 -> 12",
		`complexity tool_version: "v1" -> "v2"`,
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff %q lacks %q", diff, want)
		}
	}
	if diff := LockfileDiff(root, old, old, nil); diff != "No recorded behavior changed." {
		t.Fatalf("unchanged diff = %q", diff)
	}
}

func TestLockfileDiffShowsDefinitionChangesAndDescribesAdditions(t *testing.T) {
	root := t.TempDir()
	stdout := HashBytes([]byte("same"))
	old := Lockfile{
		Probes:  map[string]ProbeLock{"p": {RunHash: "sha256:aaaa", Exit: 0, Stdout: stdout, Stderr: stdout}},
		Metrics: map[string]MetricLock{"m": {RunHash: "sha256:cccc", Value: 10}},
	}
	updated := Lockfile{
		Probes:  map[string]ProbeLock{"p": {RunHash: "sha256:bbbb", Exit: 0, Stdout: stdout, Stderr: stdout}, "q": {Exit: 3, Stdout: stdout, Stderr: stdout, RecordedCommit: "abc"}},
		Metrics: map[string]MetricLock{"m": {RunHash: "sha256:dddd", Value: 10}, "n": {Value: 4, ToolVersion: "v2"}},
	}
	diff := LockfileDiff(root, old, updated, nil)
	for _, want := range []string{
		"p definition changed since the recorded baseline (run_hash sha256:aaaa -> sha256:bbbb)",
		"+ probe q (exit 3, stdout sha256:",
		"recorded at abc)",
		"m definition changed since the recorded baseline (run, direction, enforce, env, timeout, or version_cmd; run_hash sha256:cccc -> sha256:dddd)",
		`+ metric n (value 4, tool_version "v2")`,
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff %q lacks %q", diff, want)
		}
	}
}

// A re-record that observes exactly what the baseline already holds must
// produce a byte-identical lockfile. Restamping recorded_commit made every
// re-record churn the file and its tamper hash, which trains a reviewer to
// skim the one diff that is supposed to be readable.
func TestReRecordingUnchangedBehaviorLeavesTheLockfileByteIdentical(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"stable\"\nrun = \"printf stable\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	record := func(t *testing.T) []byte {
		t.Helper()
		parsed, bytes_, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		result := Record(root, parsed, bytes_, RecordOptions{ReviewedDiff: true})
		if result.Outcome.Exit != ExitOK {
			t.Fatalf("record: %#v", result.Outcome)
		}
		data, err := os.ReadFile(filepath.Join(root, "vise.lock"))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	first := record(t)
	// A new commit moves HEAD without changing anything a probe observes.
	writeTestFile(t, root, "unrelated.txt", "unrelated")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "unrelated")
	second := record(t)

	if !bytes.Equal(first, second) {
		t.Fatalf("re-record changed the lockfile:\nfirst:  %s\nsecond: %s", first, second)
	}

	// A real behavior change still restamps: provenance names the commit the
	// new observation was frozen at, not the one the old one was.
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"stable\"\nrun = \"printf changed\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "changed")
	third := record(t)
	if bytes.Equal(second, third) {
		t.Fatal("a changed observation left the lockfile untouched")
	}
	head, err := GitHead(root)
	if err != nil {
		t.Fatal(err)
	}
	var lock Lockfile
	if err := json.Unmarshal(third, &lock); err != nil {
		t.Fatal(err)
	}
	if got := lock.Probes["stable"].RecordedCommit; got != head {
		t.Fatalf("recorded_commit = %s, want HEAD %s", got, head)
	}
}

// The fingerprint is the one value compared on every later run, and it was the
// only recorded value that never got the two-pass check probes get. A command
// that prints something different each time would be frozen once and then
// never match, so every gate on every machine would report drift against a
// toolchain that never moved.
func TestRecordRefusesANonDeterministicFingerprint(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "counter.sh", "#!/bin/sh\ncount=0\nif test -f .count; then count=$(cat .count); fi\ncount=$((count+1))\nprintf '%s' \"$count\" > .count\nprintf 'tool v%s\\n' \"$count\"\n")
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.count\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"sh counter.sh\"]\n[[probe]]\nid = \"stable\"\nrun = \"printf stable\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	result := Record(root, manifest, manifestBytes, RecordOptions{})
	if result.Outcome.Exit != ExitHarness {
		t.Fatalf("exit = %d, want harness; outcome %#v", result.Outcome.Exit, result.Outcome)
	}
	failure := result.Outcome.Failures["fingerprint"]
	if !strings.Contains(failure.Detail, "not deterministic") {
		t.Fatalf("detail %q does not name the cause", failure.Detail)
	}
	if _, err := os.Stat(filepath.Join(root, "vise.lock")); err == nil {
		t.Fatal("a baseline was written despite an unstable fingerprint")
	}
}

// A removed probe is the entry in a review diff an operator most needs to
// scrutinise: an observation is going away. It was rendered with fewer fields
// than an added probe, which is backwards. Found by a coding agent asked to
// report anything that looked wrong while refactoring this function.
func TestTheReviewDiffDescribesARemovedProbeAsFullyAsAnAddedOne(t *testing.T) {
	probe := ProbeLock{
		RunHash:        "sha256:" + strings.Repeat("a", 64),
		RecordedCommit: strings.Repeat("b", 40),
		Exit:           0,
		Stdout:         "sha256:" + strings.Repeat("c", 64),
		Stderr:         "sha256:" + strings.Repeat("d", 64),
		Files:          map[string]string{"out/one.txt": "sha256:" + strings.Repeat("e", 64)},
	}
	old := Lockfile{V: LockVersion, Probes: map[string]ProbeLock{"gone": probe}}
	fresh := Lockfile{V: LockVersion, Probes: map[string]ProbeLock{"added": probe}}

	diff := LockfileDiff(t.TempDir(), old, fresh, nil)
	var removed, added string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "- probe gone") {
			removed = line
		}
		if strings.HasPrefix(line, "+ probe added") {
			added = line
		}
	}
	if removed == "" || added == "" {
		t.Fatalf("diff did not describe both probes:\n%s", diff)
	}
	for _, field := range []string{"1 file(s)", "recorded at " + probe.RecordedCommit} {
		if !strings.Contains(added, field) {
			t.Fatalf("the added line lost %q: %s", field, added)
		}
		if !strings.Contains(removed, field) {
			t.Fatalf("the removed line omits %q, which the added line shows: %s", field, removed)
		}
	}
}

// The review diff is what an operator reads before accepting a new baseline.
// It reported only the first environment difference, so a baseline could be
// accepted because one of three changes looked reasonable while the other two
// were never shown. Found by a coding agent asked to report what looked wrong.
func TestTheReviewDiffReportsEveryEnvironmentChange(t *testing.T) {
	oldLock := Lockfile{V: LockVersion, Fingerprint: Fingerprint{
		OS: "linux", Arch: "amd64",
		Env: map[string]string{"go version": "go1.24.0", "jq --version": "jq-1.6"},
	}}
	newLock := Lockfile{V: LockVersion, Fingerprint: Fingerprint{
		OS: "darwin", Arch: "arm64",
		Env: map[string]string{"go version": "go1.25.8", "jq --version": "jq-1.7"},
	}}

	diff := LockfileDiff(t.TempDir(), oldLock, newLock, nil)
	for _, expected := range []string{"platform", "go version", "jq --version"} {
		if !strings.Contains(diff, expected) {
			t.Errorf("the review diff never mentions %q:\n%s", expected, diff)
		}
	}
}

// A blob deliberately withheld (over the capture bound) and one that is
// missing or fails its content hash rendered identically, so an operator could
// not tell "too large to show you" from "the evidence is gone". The second is
// a reason to stop reviewing and repair the baseline.
func TestTheReviewDiffSaysWhenABlobCannotBeRead(t *testing.T) {
	root := testGitRepo(t)
	present := []byte("present")
	hash := HashBytes(present)
	missing := HashBytes([]byte("this blob was never written"))

	oldLock := Lockfile{V: LockVersion, Probes: map[string]ProbeLock{"p": {Stdout: missing, Stderr: hash}}}
	newLock := Lockfile{V: LockVersion, Probes: map[string]ProbeLock{"p": {Stdout: hash, Stderr: hash}}}

	diff := LockfileDiff(root, oldLock, newLock, map[string][]byte{hash: present})
	if !strings.Contains(diff, "blob unreadable") {
		t.Fatalf("a missing blob rendered as an ordinary withheld one:\n%s", diff)
	}

	// A large blob is withheld on purpose and must not be reported as broken.
	largeOld := Lockfile{V: LockVersion, Probes: map[string]ProbeLock{"p": {Stdout: hash, StdoutLarge: true}}}
	largeNew := Lockfile{V: LockVersion, Probes: map[string]ProbeLock{"p": {Stdout: hash, StdoutLarge: false}}}
	if diff := LockfileDiff(root, largeOld, largeNew, map[string][]byte{hash: present}); strings.Contains(diff, "unreadable") {
		t.Fatalf("a deliberately withheld blob was reported as broken:\n%s", diff)
	}
}
