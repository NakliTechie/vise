package vise

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const (
	c16HelperMode = "VISE_C16_PROCESS_HELPER"
	c16HelperRoot = "VISE_C16_ROOT"
	c16HelperMark = "VISE_C16_MARKER"
)

// c16BlockingFS exposes two exact process-death cut points while retaining the
// real filesystem implementation for every persistence operation. This is a
// package-level persistence test, not an invocation of the public CLI.
type c16BlockingFS struct {
	inner  fileSystem
	root   string
	marker string
	phase  string
}

func (f c16BlockingFS) MkdirAll(path string, mode os.FileMode) error {
	return f.inner.MkdirAll(path, mode)
}
func (f c16BlockingFS) CreateStaged(dir, pattern string) (stagedFile, error) {
	return f.inner.CreateStaged(dir, pattern)
}
func (f c16BlockingFS) Rename(from, to string) error {
	if f.phase == "before-lock-rename" && filepath.Base(to) == "vise.lock" {
		c16MarkAndBlock(f.marker)
	}
	return f.inner.Rename(from, to)
}
func (f c16BlockingFS) SyncDir(path string) error {
	if f.phase == "after-lock-rename" && filepath.Clean(path) == filepath.Clean(f.root) {
		c16MarkAndBlock(f.marker)
	}
	return f.inner.SyncDir(path)
}
func (f c16BlockingFS) Remove(path string) error { return f.inner.Remove(path) }

func c16MarkAndBlock(marker string) {
	if err := os.WriteFile(marker, []byte("persistence cut reached\n"), 0o600); err != nil {
		panic(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func c16AssertBlobClosure(t *testing.T, root string, lock Lockfile) {
	t.Helper()
	for hash := range referencedHashes(lock) {
		path, err := BlobPath(root, hash)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || HashBytes(data) != hash {
			t.Fatalf("generation references unavailable blob %s: %v", hash, err)
		}
	}
}

func TestC16ProcessDeathLeavesCompleteGenerationAndRecovers(t *testing.T) {
	if phase := os.Getenv(c16HelperMode); phase != "" {
		root := os.Getenv(c16HelperRoot)
		persistence = c16BlockingFS{inner: osFileSystem{}, root: root,
			marker: os.Getenv(c16HelperMark), phase: phase}
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		result := Record(root, manifest, manifestBytes,
			RecordOptions{AllowDirty: true, ReviewedDiff: true})
		t.Fatalf("helper passed persistence cut: %#v", result.Outcome)
	}

	for _, phase := range []string{"before-lock-rename", "after-lock-rename"} {
		t.Run(phase, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
			writeTestFile(t, root, "input.txt", "old generation\n")
			writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "p"
run = "cat input.txt"
deps = ["input.txt"]
timeout = 2
`)
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "old source")
			manifest, manifestBytes, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
				t.Fatalf("initial record: %#v", result.Outcome)
			}
			testGit(t, root, "add", "vise.lock", ".vise/blobs")
			testGit(t, root, "commit", "-qm", "old baseline")

			oldLockBytes := mustRead(t, filepath.Join(root, "vise.lock"))
			oldJournal := mustRead(t, filepath.Join(root, ".vise", "journal.jsonl"))
			writeTestFile(t, root, "input.txt", "new generation\n")
			changedManifest, changedManifestBytes, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			wantRunHash, err := ProbeRunHash(changedManifest.Probes[0])
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "cut-reached")
			command := exec.Command(os.Args[0], "-test.run=^TestC16ProcessDeathLeavesCompleteGenerationAndRecovers$")
			command.Env = append(os.Environ(), c16HelperMode+"="+phase,
				c16HelperRoot+"="+root, c16HelperMark+"="+marker)
			var helperStdout, helperStderr bytes.Buffer
			command.Stdout, command.Stderr = &helperStdout, &helperStderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- command.Wait() }()
			reaped := false
			defer func() {
				if reaped {
					return
				}
				_ = command.Process.Kill()
				select {
				case <-waited:
				case <-time.After(2 * time.Second):
					t.Error("helper was not reaped within two seconds")
				}
			}()
			deadline := time.Now().Add(10 * time.Second)
			var processErr error
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				select {
				case processErr = <-waited:
					reaped = true
					t.Fatalf("helper exited before persistence cut: %v; stdout=%q stderr=%q",
						processErr, helperStdout.String(), helperStderr.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("helper did not reach persistence cut before deadline")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case processErr = <-waited:
				reaped = true
			case <-time.After(2 * time.Second):
				t.Fatal("helper did not exit within two seconds after SIGKILL")
			}
			exitErr, ok := processErr.(*exec.ExitError)
			if !ok {
				t.Fatalf("helper process exit = %v, want SIGKILL; stdout=%q stderr=%q",
					processErr, helperStdout.String(), helperStderr.String())
			}
			waitStatus, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGKILL {
				t.Fatalf("helper process exit = %v, want SIGKILL; stdout=%q stderr=%q",
					processErr, helperStdout.String(), helperStderr.String())
			}

			currentLockBytes := mustRead(t, filepath.Join(root, "vise.lock"))
			if phase == "before-lock-rename" && !bytes.Equal(currentLockBytes, oldLockBytes) {
				t.Fatal("death before lock rename exposed neither the complete old generation")
			}
			if phase == "after-lock-rename" && bytes.Equal(currentLockBytes, oldLockBytes) {
				t.Fatal("death after lock rename did not expose the complete new generation")
			}
			lock, _, err := LoadLockfile(root)
			if err != nil {
				t.Fatalf("surviving lock does not load: %v", err)
			}
			if phase == "after-lock-rename" {
				entry := lock.Probes["p"]
				if entry.RunHash != wantRunHash || entry.Exit != 0 || entry.Stdout != HashBytes([]byte("new generation\n")) ||
					entry.Stderr != emptyHash || entry.Deps["input.txt"] != HashBytes([]byte("new generation\n")) || len(entry.Files) != 0 {
					t.Fatalf("post-rename lock is not the expected new generation: %#v", entry)
				}
			}
			c16AssertBlobClosure(t, root, lock)
			if journal := mustRead(t, filepath.Join(root, ".vise", "journal.jsonl")); !bytes.Equal(journal, oldJournal) {
				t.Fatal("killed record left a journal event for an incomplete receipt")
			}

			recovered := Record(root, changedManifest, changedManifestBytes,
				RecordOptions{AllowDirty: true, ReviewedDiff: true})
			if recovered.Outcome.Exit != ExitOK {
				t.Fatalf("record did not recover after process death: %#v", recovered.Outcome)
			}
			recoveredLock, _, err := LoadLockfile(root)
			if err != nil {
				t.Fatalf("recovered generation is unusable: %v", err)
			}
			c16AssertBlobClosure(t, root, recoveredLock)
			verified := Verify(root, changedManifest, changedManifestBytes, VerifyOptions{})
			if verified.Outcome.Exit != ExitOK {
				t.Fatalf("recovered generation does not verify: %#v", verified.Outcome)
			}
			events, err := ReadJournal(root, 0)
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents := bytes.Count(oldJournal, []byte("\n"))
			if len(events) != beforeEvents+1 || events[len(events)-1].Event != "record" {
				t.Fatalf("recovery journal = %#v, want exactly one new record event", events)
			}
			if recovered.Outcome.Lock == "" || events[len(events)-1].Lock != recovered.Outcome.Lock {
				t.Fatalf("recovery journal lock %q does not match receipt %q",
					events[len(events)-1].Lock, recovered.Outcome.Lock)
			}
			recoveredJournal := mustRead(t, filepath.Join(root, ".vise", "journal.jsonl"))
			if !bytes.HasPrefix(recoveredJournal, oldJournal) {
				t.Fatal("recovery journal did not preserve the original byte prefix")
			}
		})
	}
}
