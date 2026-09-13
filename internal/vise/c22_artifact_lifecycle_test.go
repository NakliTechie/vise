package vise

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func c22State(t *testing.T, root string) map[string][]byte {
	t.Helper()
	got := map[string][]byte{}
	for _, rel := range []string{"vise.lock", ".vise/journal.jsonl"} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		got[rel] = b
	}
	err := filepath.WalkDir(filepath.Join(root, ".vise", "blobs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		got[rel] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestC22AcceptedAndUnacceptedMultiArtifactLifecycle(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nout/\n")
	writeTestFile(t, root, "spec/a", "a\n")
	writeTestFile(t, root, "spec/b", "b\n")
	writeTestFile(t, root, "bin/report", "#!/bin/sh\nmkdir -p out; printf 'a\\n' > out/a\n")
	if err := os.Chmod(filepath.Join(root, "bin/report"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.toml", "[vise]\nversion=1\n[stubs]\nnetwork='declared-off'\n[[probe]]\nid='report'\nrun='./bin/report'\nfiles=['out/a','out/b']\nexpect.files={ 'out/a'='spec/a', 'out/b'='spec/b' }\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "two artifact pin")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	initial := Record(root, manifest, manifestBytes, RecordOptions{})
	if initial.Outcome.Exit != ExitOK || initial.Pins == nil || !reflectString(initial.Pins.Unmet, "report") {
		t.Fatalf("initial=%#v %#v", initial.Outcome, initial.Pins)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "initial unmet baseline")
	unaccepted := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if unaccepted.Outcome.Exit != ExitUnmet || !strings.Contains(unaccepted.Outcome.Failures["report"].Detail, `"out/b" was not produced`) {
		t.Fatalf("unaccepted=%#v", unaccepted.Outcome)
	}

	writeTestFile(t, root, "bin/report", "#!/bin/sh\nmkdir -p out; printf 'a\\n' > out/a; printf 'b\\n' > out/b\n")
	testGit(t, root, "add", "bin/report")
	testGit(t, root, "commit", "-qm", "complete pin")
	accepted := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
	if accepted.Outcome.Exit != ExitOK || accepted.Pins == nil || !reflectString(accepted.Pins.Accepted, "report") {
		t.Fatalf("accepted=%#v %#v", accepted.Outcome, accepted.Pins)
	}
	before := c22State(t, root)

	writeTestFile(t, root, "bin/report", "#!/bin/sh\nmkdir -p out; printf 'a\\n' > out/a; printf 'changed\\n' > out/b\n")
	changed := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if changed.Outcome.Exit != ExitBehavior || !strings.Contains(changed.Outcome.Failures["report"].Diff, "file/out/b") {
		t.Fatalf("changed=%#v", changed.Outcome)
	}
	if !equalByteMap(before, c22State(t, root)) {
		t.Fatal("behavior judgment changed evaluator generation")
	}

	writeTestFile(t, root, "bin/report", "#!/bin/sh\nmkdir -p out; printf 'a\\n' > out/a\n")
	missing := Verify(root, manifest, manifestBytes, VerifyOptions{})
	if missing.Outcome.Exit != ExitHarness || !strings.Contains(missing.Outcome.Failures["report"].Detail, `"out/b" was not produced`) {
		t.Fatalf("accepted missing=%#v", missing.Outcome)
	}
	if !equalByteMap(before, c22State(t, root)) {
		t.Fatal("accepted missing artifact changed evaluator generation")
	}
}

func reflectString(got []string, want string) bool { return len(got) == 1 && got[0] == want }
func equalByteMap(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		got, ok := b[k]
		if !ok || !bytes.Equal(v, got) {
			return false
		}
	}
	return true
}

func TestC22GenerationSnapshotEqualityRequiresTheSameKeys(t *testing.T) {
	if !equalByteMap(map[string][]byte{"empty": {}}, map[string][]byte{"empty": {}}) {
		t.Fatal("equal empty entries did not compare equal")
	}
	if equalByteMap(map[string][]byte{"first": {}}, map[string][]byte{"second": {}}) {
		t.Fatal("same-size snapshots with different empty keys compared equal")
	}
}

func TestC22RefusedArtifactsRemainOwnedByTheUser(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, "out/keep", "directory-data")
		result := (Runner{Root: root, Manifest: testManifest(Probe{ID: "p", Run: "true", Timeout: 5, Files: []string{"out"}})}).RunProbe(Probe{ID: "p", Run: "true", Timeout: 5, Files: []string{"out"}}, false)
		data, err := os.ReadFile(filepath.Join(root, "out/keep"))
		if !strings.Contains(result.HarnessError, "recursive deletion is refused") || err != nil || string(data) != "directory-data" {
			t.Fatalf("result=%#v data=%q err=%v", result, data, err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := testGitRepo(t)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("target-data"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "out-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		probe := Probe{ID: "p", Run: "true", Timeout: 5, Files: []string{"out-link"}}
		result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
		info, linkErr := os.Lstat(link)
		data, targetErr := os.ReadFile(target)
		if !strings.Contains(result.HarnessError, "symlink") || linkErr != nil || info.Mode()&os.ModeSymlink == 0 || targetErr != nil || string(data) != "target-data" {
			t.Fatalf("result=%#v link=%v target=%q/%v", result, linkErr, data, targetErr)
		}
	})
	t.Run("tracked", func(t *testing.T) {
		root := testGitRepo(t)
		before, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
		if err != nil {
			t.Fatal(err)
		}
		probe := Probe{ID: "p", Run: "true", Timeout: 5, Files: []string{"tracked.txt"}}
		result := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
		after, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
		if !strings.Contains(result.HarnessError, "is tracked by git") || err != nil || !bytes.Equal(before, after) {
			t.Fatalf("result=%#v before=%q after=%q err=%v", result, before, after, err)
		}
	})
}
