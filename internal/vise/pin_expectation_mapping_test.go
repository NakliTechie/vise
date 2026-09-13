package vise

import (
	"reflect"
	"runtime"
	"testing"
)

func TestPinExpectationMapsDeclaredChannelsAndDefaultsOmittedStreamsToEmpty(t *testing.T) {
	tests := []struct {
		name       string
		expect     Expect
		files      []string
		specs      map[string]string
		wantStdout string
		wantStderr string
		wantFiles  map[string]string
	}{
		{name: "stdout-only", expect: Expect{Stdout: "spec/stdout"}, specs: map[string]string{"spec/stdout": "out\n"}, wantStdout: "out\n"},
		{name: "stderr-only", expect: Expect{Stderr: "spec/stderr"}, specs: map[string]string{"spec/stderr": "err\n"}, wantStderr: "err\n"},
		{name: "artifact-only", files: []string{"out/report"}, expect: Expect{Files: map[string]string{"out/report": "spec/report"}}, specs: map[string]string{"spec/report": "artifact\n"}, wantFiles: map[string]string{"out/report": "artifact\n"}},
		{name: "mixed", files: []string{"out/a", "out/b"}, expect: Expect{Stdout: "spec/stdout", Stderr: "spec/stderr", Exit: IntPtr(9), Files: map[string]string{"out/a": "spec/a", "out/b": "spec/b"}}, specs: map[string]string{"spec/stdout": "out\n", "spec/stderr": "err\n", "spec/a": "A\n", "spec/b": "B\n"}, wantStdout: "out\n", wantStderr: "err\n", wantFiles: map[string]string{"out/a": "A\n", "out/b": "B\n"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := testGitRepo(t)
			for path, body := range tc.specs {
				writeTestFile(t, root, path, body)
			}
			probe := Probe{ID: "pin", Run: "true", Files: tc.files, Expect: &tc.expect}
			manifest := testManifest(probe)
			if err := manifest.Validate(root); err != nil {
				t.Fatalf("valid positive manifest: %v", err)
			}
			blobs := map[string][]byte{}
			got, err := pinExpectation(root, probe, blobs)
			if err != nil {
				t.Fatal(err)
			}
			wantExit := 0
			if tc.expect.Exit != nil {
				wantExit = *tc.expect.Exit
			}
			if got.Exit != wantExit {
				t.Fatalf("exit = %d, want %d", got.Exit, wantExit)
			}
			checkStream := func(name, hash, want string) {
				t.Helper()
				data, exists := blobs[hash]
				if !exists || data == nil || hash != HashBytes([]byte(want)) || string(data) != want {
					t.Fatalf("%s = %q at %s (present=%v, nil=%v), want stored %q", name, data, hash, exists, data == nil, want)
				}
			}
			checkStream("stdout", got.Stdout, tc.wantStdout)
			checkStream("stderr", got.Stderr, tc.wantStderr)
			if len(got.Files) != len(tc.wantFiles) {
				t.Fatalf("files = %#v, want %#v", got.Files, tc.wantFiles)
			}
			for path, want := range tc.wantFiles {
				checkStream("file/"+path, got.Files[path], want)
			}
			if got.Pin == nil || got.Pin.AcceptedCommit != nil || len(got.Pin.Spec) != len(tc.specs) {
				t.Fatalf("pin provenance = %#v, want %d unaccepted spec mappings", got.Pin, len(tc.specs))
			}
			for path, body := range tc.specs {
				if got.Pin.Spec[path] != HashBytes([]byte(body)) {
					t.Fatalf("spec mapping %s = %q, want hash of %q", path, got.Pin.Spec[path], body)
				}
			}
			runHash, err := ProbeRunHash(probe)
			if err != nil {
				t.Fatal(err)
			}
			got.RunHash = runHash
			got.RecordedCommit = headCommit(t, root)
			lock := Lockfile{V: LockVersion, Fingerprint: Fingerprint{OS: runtime.GOOS, Arch: runtime.GOARCH, Stubs: manifest.Stubs}, Probes: map[string]ProbeLock{"pin": got}}
			if _, err := WriteGeneration(root, lock, blobs); err != nil {
				t.Fatalf("write generation: %v", err)
			}
			reloaded, _, err := LoadLockfile(root)
			if err != nil {
				t.Fatalf("reload generation: %v", err)
			}
			entry := reloaded.Probes["pin"]
			if !reflect.DeepEqual(entry, got) {
				t.Fatalf("reloaded entry = %#v, want %#v", entry, got)
			}
			for _, hash := range append([]string{entry.Stdout, entry.Stderr}, mapValues(entry.Files)...) {
				data, available, err := BlobData(root, hash, false)
				if err != nil || !available || HashBytes(data) != hash {
					t.Fatalf("reloaded reference %s is not a complete blob: available=%v err=%v", hash, available, err)
				}
			}
		})
	}
}

func mapValues(m map[string]string) []string {
	values := make([]string, 0, len(m))
	for _, value := range m {
		values = append(values, value)
	}
	return values
}
