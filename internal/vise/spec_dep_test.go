package vise

import (
	"os"
	"path/filepath"
	"testing"
)

func TestASpecThatIsAlsoItsOwnDependencyRoutesToHuman(t *testing.T) {
	for _, change := range []string{"edited", "missing", "symlink", "dependency-only"} {
		t.Run(change, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.calls\n")
			writeTestFile(t, root, "spec/shared", "hello\n")
			writeTestFile(t, root, "spec/other", "hello\n")
			writeTestFile(t, root, "fixtures/input", "input\n")
			writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"pin\"\nrun = \"printf ran >> .calls; cat fixtures/input > /dev/null; cat spec/shared\"\ndeps = [\"spec/shared\", \"fixtures/input\"]\nexpect.stdout = \"spec/shared\"\n")
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "shared spec and dependency")
			manifest, data, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			recordPinRepo(t, root, manifest, data)
			if got := Verify(root, manifest, data, VerifyOptions{}).Outcome; got.Exit != ExitOK {
				t.Fatalf("known-good shared-role control: %#v", got)
			}
			if err := os.Remove(filepath.Join(root, ".calls")); err != nil {
				t.Fatal(err)
			}
			spec := filepath.Join(root, "spec", "shared")
			switch change {
			case "edited":
				writeTestFile(t, root, "spec/shared", "changed\n")
			case "missing", "symlink":
				if err := os.Remove(spec); err != nil {
					t.Fatal(err)
				}
				if change == "symlink" {
					if err := os.Symlink("other", spec); err != nil {
						t.Fatal(err)
					}
				}
			case "dependency-only":
				writeTestFile(t, root, "fixtures/input", "changed\n")
			}
			want := NextHuman
			if change == "dependency-only" {
				want = NextFixProbe
			}
			got := Verify(root, manifest, data, VerifyOptions{}).Outcome
			if got.Exit != ExitHarness || got.Next.Action != want || got.Failures["pin"].Operator != (want == NextHuman) {
				t.Errorf("repair owner: %#v", got)
			}
			if status := BuildStatus(root); status.Next.Action != want {
				t.Errorf("status repair owner: %#v", status.Next)
			}
			if _, err := os.Stat(filepath.Join(root, ".calls")); !os.IsNotExist(err) {
				t.Fatalf("preflight drift executed the probe: %v", err)
			}
		})
	}
}
