package vise

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// snapshotAcceptanceGeneration records every persistent generation surface a
// record or preview could change. Scratch and the run lock are deliberately
// excluded: they are runtime state, not accepted provenance.
func snapshotAcceptanceGeneration(t *testing.T, root string) map[string]string {
	t.Helper()
	got := make(map[string]string)
	for _, name := range []string{"vise.lock", ".vise/journal.jsonl"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			if os.IsNotExist(err) {
				got[name] = "<absent>"
				continue
			}
			t.Fatal(err)
		}
		got[name] = string(data)
	}
	blobRoot := filepath.Join(root, ".vise", "blobs")
	err := filepath.Walk(blobRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		got[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return got
}

// A dirty declared dependency is a new pin identity even when the observation
// still meets the same spec. Preview is read-only; an explicitly reviewed
// dirty record may freeze that identity but may neither inherit the old
// acceptance nor create a new one. Only a later clean exact-digest record may
// accept it, after which unchanged records carry the original provenance.
func TestDirtyDeclaredDependencyRequiresFreshCleanAcceptance(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "spec/result.stdout", "stable\n")
	writeTestFile(t, root, "fixtures/input", "first\n")
	writeTestFile(t, root, "program.sh", "#!/bin/sh\nprintf 'stable\\n'\n")
	if err := os.Chmod(filepath.Join(root, "program.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "result"
run = "./program.sh"
deps = ["fixtures/input"]
expect.stdout = "spec/result.stdout"
`)
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "accepted dependency pin")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}

	initial := Record(root, manifest, manifestBytes, RecordOptions{})
	if initial.Outcome.Exit != ExitOK || initial.Pins == nil || len(initial.Pins.Accepted) != 1 {
		t.Fatalf("initial clean acceptance: %#v %#v", initial.Outcome, initial.Pins)
	}
	oldLock, _, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	oldAccepted := oldLock.Probes["result"].Pin.AcceptedCommit
	if oldAccepted == nil {
		t.Fatal("initial met pin was not accepted")
	}
	testGit(t, root, "add", "vise.lock", ".vise/blobs")
	testGit(t, root, "commit", "-qm", "record accepted baseline")

	writeTestFile(t, root, "fixtures/input", "second\n")
	beforePreview := snapshotAcceptanceGeneration(t, root)
	preview := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true, Preview: true})
	if preview.Outcome.Exit != ExitOK || preview.Pins == nil || len(preview.Pins.Accepted) != 0 ||
		len(preview.Pins.PassingUnaccepted) != 1 || preview.Pins.PassingUnaccepted[0] != "result" {
		t.Fatalf("dirty dependency preview: %#v %#v", preview.Outcome, preview.Pins)
	}
	if afterPreview := snapshotAcceptanceGeneration(t, root); !reflect.DeepEqual(beforePreview, afterPreview) {
		t.Fatalf("preview changed persistent generation\nbefore: %#v\nafter:  %#v", beforePreview, afterPreview)
	}

	dirty := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true, Accept: preview.Candidate})
	if dirty.Outcome.Exit != ExitOK || dirty.Pins == nil || len(dirty.Pins.Accepted) != 0 ||
		len(dirty.Pins.PassingUnaccepted) != 1 || dirty.Pins.PassingUnaccepted[0] != "result" {
		t.Fatalf("reviewed dirty dependency record: %#v %#v", dirty.Outcome, dirty.Pins)
	}
	dirtyLock, dirtyBytes, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	entry := dirtyLock.Probes["result"]
	if entry.Pin.AcceptedCommit != nil {
		t.Fatalf("dirty dependency identity carried or acquired acceptance: %#v", entry.Pin)
	}
	if entry.Deps["fixtures/input"] == oldLock.Probes["result"].Deps["fixtures/input"] {
		t.Fatal("dirty record did not freeze the changed dependency bytes")
	}

	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "freeze changed dependency without acceptance")
	cleanPreview := Record(root, manifest, manifestBytes, RecordOptions{Preview: true})
	if cleanPreview.Outcome.Exit != ExitOK || cleanPreview.Candidate == "" || cleanPreview.Pins == nil || len(cleanPreview.Pins.Accepted) != 1 {
		t.Fatalf("clean preview for exact acceptance: %#v %#v", cleanPreview.Outcome, cleanPreview.Pins)
	}
	accepted := Record(root, manifest, manifestBytes, RecordOptions{Accept: cleanPreview.Candidate})
	if accepted.Outcome.Exit != ExitOK || accepted.Pins == nil || len(accepted.Pins.Accepted) != 1 {
		t.Fatalf("clean exact acceptance: %#v %#v", accepted.Outcome, accepted.Pins)
	}
	acceptedLock, acceptedBytes, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	newAccepted := acceptedLock.Probes["result"].Pin.AcceptedCommit
	if newAccepted == nil || *newAccepted != headCommit(t, root) || *newAccepted == *oldAccepted {
		t.Fatalf("fresh acceptance provenance = %v, old %v, HEAD %s", newAccepted, oldAccepted, headCommit(t, root))
	}
	if bytes.Equal(dirtyBytes, acceptedBytes) {
		t.Fatal("clean acceptance did not change the unaccepted lock generation")
	}

	testGit(t, root, "add", "vise.lock", ".vise/blobs")
	testGit(t, root, "commit", "-qm", "accept changed dependency identity")
	again := Record(root, manifest, manifestBytes, RecordOptions{ReviewedDiff: true})
	if again.Outcome.Exit != ExitOK {
		t.Fatalf("unchanged record: %#v", again.Outcome)
	}
	againLock, _, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	carried := againLock.Probes["result"].Pin.AcceptedCommit
	if carried == nil || *carried != *newAccepted {
		t.Fatalf("unchanged identity restamped acceptance: got %v, want %s", carried, *newAccepted)
	}
}

// Dirty bytes unrelated to an already accepted pin do not create a new pin
// identity. An allow-dirty record therefore carries the historical acceptance;
// this is distinct from acquiring acceptance for a dirty identity.
func TestUnrelatedDirtySourceCarriesExistingPinAcceptance(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "spec/result.stdout", "stable\n")
	writeTestFile(t, root, "fixtures/input", "declared\n")
	writeTestFile(t, root, "program.sh", "#!/bin/sh\nprintf 'stable\\n'\n")
	if err := os.Chmod(filepath.Join(root, "program.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.toml", "[vise]\nversion=1\n[[probe]]\nid='result'\nrun='./program.sh'\ndeps=['fixtures/input']\nexpect.stdout='spec/result.stdout'\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "accepted pin")
	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	first := Record(root, manifest, manifestBytes, RecordOptions{})
	if first.Outcome.Exit != ExitOK {
		t.Fatalf("initial record: %#v", first.Outcome)
	}
	lock, _, _ := LoadLockfile(root)
	want := lock.Probes["result"].Pin.AcceptedCommit
	if want == nil {
		t.Fatal("initial pin was not accepted")
	}
	testGit(t, root, "add", "vise.lock", ".vise/blobs")
	testGit(t, root, "commit", "-qm", "baseline")

	writeTestFile(t, root, "notes.txt", "unrelated dirty source\n")
	result := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true, ReviewedDiff: true})
	if result.Outcome.Exit != ExitOK || result.Pins == nil || len(result.Pins.Accepted) != 1 || len(result.Pins.PassingUnaccepted) != 0 {
		t.Fatalf("unrelated dirty record: %#v %#v", result.Outcome, result.Pins)
	}
	after, _, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	got := after.Probes["result"].Pin.AcceptedCommit
	if got == nil || *got != *want {
		t.Fatalf("historical acceptance was not carried: got %v, want %s", got, *want)
	}
}
