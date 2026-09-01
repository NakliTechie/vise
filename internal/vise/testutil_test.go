package vise

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func testGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init", "-q")
	testGit(t, root, "config", "user.email", "vise-tests@example.invalid")
	testGit(t, root, "config", "user.name", "vise tests")
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "tracked.txt", "original\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "initial")
	return root
}

func testGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testManifest(probes ...Probe) Manifest {
	manifest := Manifest{Vise: ViseSettings{Version: 1}, Stubs: StubSettings{TZ: "UTC", Lang: "C", Seed: "1729", Network: "declared-off"}, Probes: probes}
	manifest.applyDefaults()
	return manifest
}
