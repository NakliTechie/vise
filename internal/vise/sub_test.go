package vise

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A vendored dependency held as a submodule is an ordinary thing to have, and
// a probe writing into one is changing the checkout. Git reports a modified
// submodule as a one-line diff of the gitlink, so the question is whether the
// snapshot sees it at all.
func TestAProbeWritingIntoASubmoduleIsDetected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	inner := t.TempDir()
	testGit(t, inner, "init", "-q")
	testGit(t, inner, "config", "user.email", "vise-tests@example.invalid")
	testGit(t, inner, "config", "user.name", "vise tests")
	if err := os.WriteFile(filepath.Join(inner, "dep.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, inner, "add", ".")
	testGit(t, inner, "commit", "-qm", "dependency")

	root := testGitRepo(t)
	cmd := exec.Command("git", "-c", "protocol.file.allow=always", "submodule", "add", "-q", inner, "vendor/dep")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot add a submodule here: %v\n%s", err, output)
	}
	testGit(t, root, "commit", "-qm", "vendor the dependency")

	result := Runner{Root: root}.RunProbe(Probe{
		ID:      "meddler",
		Run:     "printf tampered > vendor/dep/dep.txt && printf done",
		Timeout: 30,
	}, true)

	t.Logf("harness error: %q", result.HarnessError)
	if result.HarnessError == "" {
		t.Fatal("a probe wrote into a submodule and the run came back clean")
	}
	if !strings.Contains(result.HarnessError, "tracked") && !strings.Contains(result.HarnessError, "vendor") {
		t.Fatalf("reported, but not as a checkout change: %q", result.HarnessError)
	}
}
