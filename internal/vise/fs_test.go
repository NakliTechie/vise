package vise

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errInjected = errors.New("injected persistence failure")

// faultyFS fails exactly one persistence step and passes the rest through, so
// each failure point can be tested on its own. target, when set, narrows a
// rename failure to one file name — that is what lets a test fail the
// lockfile write while letting the blob writes before it succeed.
type faultyFS struct {
	inner  fileSystem
	step   string
	target string
}

func (f faultyFS) MkdirAll(path string, perm os.FileMode) error {
	if f.step == "mkdir" {
		return errInjected
	}
	return f.inner.MkdirAll(path, perm)
}

func (f faultyFS) CreateStaged(dir, pattern string) (stagedFile, error) {
	if f.step == "create" {
		return nil, errInjected
	}
	file, err := f.inner.CreateStaged(dir, pattern)
	if err != nil {
		return nil, err
	}
	return faultyFile{inner: file, step: f.step}, nil
}

func (f faultyFS) Rename(from, to string) error {
	if f.step == "rename" && (f.target == "" || filepath.Base(to) == f.target) {
		return errInjected
	}
	return f.inner.Rename(from, to)
}

func (f faultyFS) SyncDir(path string) error {
	if f.step == "syncdir" {
		return errInjected
	}
	return f.inner.SyncDir(path)
}

func (f faultyFS) Remove(path string) error { return f.inner.Remove(path) }

type faultyFile struct {
	inner stagedFile
	step  string
}

func (f faultyFile) Name() string { return f.inner.Name() }

func (f faultyFile) Chmod(mode os.FileMode) error {
	if f.step == "chmod" {
		return errInjected
	}
	return f.inner.Chmod(mode)
}

func (f faultyFile) Write(p []byte) (int, error) {
	if f.step == "write" {
		return 0, errInjected
	}
	return f.inner.Write(p)
}

func (f faultyFile) Sync() error {
	if f.step == "sync" {
		return errInjected
	}
	return f.inner.Sync()
}

func (f faultyFile) Close() error { return f.inner.Close() }

// injectFailure makes the named persistence step fail for the duration of the
// test. The seam is a package-level variable and these tests never call
// t.Parallel(), so installing it is safe; adding parallelism here would need a
// different mechanism.
func injectFailure(t *testing.T, step string) func() {
	t.Helper()
	return injectFailureFor(t, step, "")
}

// injectFailureFor narrows a rename failure to one target file name, so a test
// can fail the lockfile write while the blob writes before it succeed.
func injectFailureFor(t *testing.T, step, target string) func() {
	t.Helper()
	previous := persistence
	persistence = faultyFS{inner: previous, step: step, target: target}
	restore := func() { persistence = previous }
	t.Cleanup(restore)
	return restore
}

func TestEveryPersistenceFailureLeavesTheOldFileIntact(t *testing.T) {
	for _, step := range []string{"mkdir", "create", "chmod", "write", "sync", "rename"} {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "vise.lock")
			if err := os.WriteFile(path, []byte("old generation"), 0o644); err != nil {
				t.Fatal(err)
			}
			injectFailure(t, step)
			err := atomicWrite(root, path, []byte("new generation"), 0o644)
			if !errors.Is(err, errInjected) {
				t.Fatalf("step %s: err = %v, want the injected failure", step, err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != "old generation" {
				t.Fatalf("step %s: target = %q, %v — a failed write must leave the old generation", step, data, readErr)
			}
			// No staging residue is left behind inside the repository.
			staging, statErr := os.ReadDir(filepath.Join(root, ".vise", "tmp"))
			if statErr == nil && len(staging) != 0 {
				t.Fatalf("step %s: staging residue %v", step, staging)
			}
		})
	}
}

func TestPersistenceOrderingHoldsWhenTheLockfileWriteFails(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"stable\"\nrun = \"printf stable\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("first record: %#v", result.Outcome)
	}
	firstLock := mustRead(t, filepath.Join(root, "vise.lock"))
	journalBefore, err := ReadJournal(root, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Change the observation so the second record would write a new lockfile,
	// then make the lockfile rename fail.
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[[probe]]\nid = \"stable\"\nrun = \"printf changed\"\n")
	manifest, manifestBytes, err = LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	blobsBefore := blobNames(t, root)
	// Fail only the lockfile rename, so the blob writes that precede it in
	// WriteGeneration succeed — that ordering is what is under test.
	restore := injectFailureFor(t, "rename", "vise.lock")
	result := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true, ReviewedDiff: true})
	if result.Outcome.Exit != ExitHarness {
		t.Fatalf("record with a failing rename: %#v", result.Outcome)
	}
	if failure, ok := result.Outcome.Failures["persistence"]; !ok || !strings.Contains(failure.Detail, "injected") {
		t.Fatalf("failures = %#v", result.Outcome.Failures)
	}

	// SPEC §3.1: blobs first (orphans are harmless), then the lockfile, then
	// the journal. A crash at the lockfile leaves the old baseline and no
	// journal event — never a hybrid.
	if blobsAfter := blobNames(t, root); len(blobsAfter) <= len(blobsBefore) {
		t.Fatalf("blobs went from %d to %d: the new observation must be written before the lockfile", len(blobsBefore), len(blobsAfter))
	}
	if current := mustRead(t, filepath.Join(root, "vise.lock")); string(current) != string(firstLock) {
		t.Fatal("a failed generation replaced the recorded baseline")
	}
	journalAfter, err := ReadJournal(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(journalAfter) != len(journalBefore) {
		t.Fatalf("journal grew from %d to %d events on a failed write", len(journalBefore), len(journalAfter))
	}
	if _, _, err := LoadLockfile(root); err != nil {
		t.Fatalf("the surviving lockfile no longer loads: %v", err)
	}

	// With the failure removed, the same record succeeds and the journal gains
	// exactly one event; the orphan blobs from the failed attempt are pruned.
	restore()
	if result := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true, ReviewedDiff: true}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record after recovery: %#v", result.Outcome)
	}
	journalRecovered, err := ReadJournal(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(journalRecovered) != len(journalBefore)+1 {
		t.Fatalf("journal = %d events, want %d", len(journalRecovered), len(journalBefore)+1)
	}
}

func TestDirectorySyncFailureDoesNotUndoACommittedWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "vise.lock")
	if err := os.WriteFile(path, []byte("old generation"), 0o644); err != nil {
		t.Fatal(err)
	}
	injectFailure(t, "syncdir")
	// The rename already committed the new generation. Reporting the flush
	// failure would tell the caller nothing was written when everything was.
	if err := atomicWrite(root, path, []byte("new generation"), 0o644); err != nil {
		t.Fatalf("a durability flush failure must not fail a committed write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new generation" {
		t.Fatalf("target = %q, %v", data, err)
	}
}

func blobNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".vise", "blobs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
