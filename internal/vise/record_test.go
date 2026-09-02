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
