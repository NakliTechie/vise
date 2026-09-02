package vise

import (
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
