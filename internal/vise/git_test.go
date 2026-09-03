package vise

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitTrackedPathsReportsOnlyTrackedFiles(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "out/result.txt", "untracked\n")
	tracked, err := GitTrackedPaths(root, []string{"out/result.txt", "tracked.txt", "missing.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tracked, []string{"tracked.txt"}) {
		t.Fatalf("tracked = %#v", tracked)
	}
	if tracked, err := GitTrackedPaths(root, nil); err != nil || tracked != nil {
		t.Fatalf("empty = %#v, %v", tracked, err)
	}
}

func TestGitTrackedPathsMatchesCaseInsensitively(t *testing.T) {
	root := testGitRepo(t)
	tracked, err := GitTrackedPaths(root, []string{"TRACKED.TXT", "Out/Result.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tracked, []string{"tracked.txt"}) {
		t.Fatalf("tracked = %#v", tracked)
	}
}

// A probe that detaches HEAD at the commit it is already on leaves the
// resolved commit identical and the repository on no branch. The snapshot
// digested the resolved value, so the gate said green, and the operator's next
// commit went somewhere they did not expect.
//
// SPEC says a probe must not change the checkout. Which branch you are on is
// part of the checkout even when the tree is byte-identical.
func TestAProbeThatDetachesHeadAtTheSameCommitIsDetected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := testGitRepo(t)
	result := Runner{Root: root}.RunProbe(Probe{
		ID:      "detacher",
		Run:     "git checkout -q --detach HEAD && printf ok",
		Timeout: 30,
	}, true)
	if result.HarnessError == "" {
		t.Fatal("a probe left the repository on no branch and the run came back clean")
	}
	if !strings.Contains(result.HarnessError, "git") {
		t.Fatalf("reported, but not as a change to git's own state: %q", result.HarnessError)
	}
}

// The untracked half of the snapshot hashes content, size, modification time
// and permission bits. Three of those four were protected; content was not.
//
// Dropping the content digest survives the suite because a rewrite normally
// moves the modification time too. It does not have to: a probe that rewrites
// a file and restores its timestamp — the shape of a formatter or a
// normalizer, and one `touch -r` away — changes what every later probe reads
// with nothing else moving. The size is the same because the bytes are the
// same length, which is exactly the case a byte-blind hash cannot see.
func TestTheSnapshotSeesAContentChangeThatHidesItsTimestamp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/\n")
	writeTestFile(t, root, "stray.txt", "aaaa\n")

	before, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Same length, same timestamp, different bytes.
	path := filepath.Join(root, "stray.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bbbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, after) {
		t.Error("a file was rewritten with its timestamp restored and the snapshot did not move")
	}
}

// A file that races away between the listing and the hash is absent, and
// hashWorkspaceEntry says so by returning an empty hash. The caller stored it
// anyway, as a key with an empty value, which is the opposite of absent.
//
// ChangedUntracked has two loops: the first compares values, the second tests
// key presence. With an empty-valued key they disagree, so the same race
// produced a finding or no finding depending on which of the two snapshots it
// landed in — and when it produced one, it named a file that does not exist.
//
// Reported by a coding agent working under the gate, as the first of three
// things it noticed while reading code it had been asked not to change.
func TestASnapshotDoesNotHoldAKeyForAFileThatIsNotThere(t *testing.T) {
	absent := WorkspaceSnapshot{Untracked: map[string]string{}}
	raced := WorkspaceSnapshot{Untracked: map[string]string{"gone.txt": ""}}

	if changed := absent.ChangedUntracked(raced); len(changed) != 0 {
		t.Errorf("a snapshot holding an empty-valued key reads as a change: %v", changed)
	}
	if changed := raced.ChangedUntracked(absent); len(changed) != 0 {
		t.Errorf("the same pair compared the other way disagrees: %v", changed)
	}
}

// And the same concept written down twice, which had already drifted: the
// dirty-tree check's inline list of vise's own local state was missing the bare
// `.vise/tmp` that the snapshot's predicate covers.
func TestOneDefinitionOfViseLocalState(t *testing.T) {
	for _, path := range []string{".vise/run.lock", ".vise/journal.jsonl", ".vise/tmp", ".vise/tmp/scratch"} {
		if !isViseLocalState(path) {
			t.Errorf("%q is vise's own state and the predicate says otherwise", path)
		}
	}
	for _, path := range []string{"vise.toml", "vise.lock", ".vise/blobs/ab/cd", ".viserc", "src/.vise/tmp"} {
		if isViseLocalState(path) {
			t.Errorf("%q is not vise's per-run state and the predicate says it is", path)
		}
	}
	// The dirty check must use the predicate, not a copy of it.
	source, err := os.ReadFile("git.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), `path == ".vise/run.lock"`) {
		t.Error("git.go carries a second, inline copy of the local-state list")
	}
}

// A staged rename is the case the -z parse got wrong: git emits two NUL
// records, and the second holds the source path with no status prefix. Slicing
// three characters off every record took them off a real path.
//
// It could not change the answer, because the new-path record returns true one
// iteration earlier. This pins the parse anyway — a loop that mangles a path is
// a trap for whoever extends it to report which path is dirty — and asserts the
// answer both before and after the rename, which is the part that must not move.
func TestTheDirtyCheckReadsARenameAsTwoPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := testGitRepo(t)
	writeTestFile(t, root, "before.txt", "content\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "a file to rename")

	if dirty, err := GitDirty(root); err != nil || dirty {
		t.Fatalf("clean tree reported dirty=%v err=%v", dirty, err)
	}
	testGit(t, root, "mv", "before.txt", "after.txt")
	dirty, err := GitDirty(root)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Error("a staged rename left the tree reported clean")
	}
}
