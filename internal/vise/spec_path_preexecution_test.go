package vise

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestC02InvalidSpecPathsRefuseBeforeProbeExecution(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) Manifest
		want  string
	}{
		{"missing", func(t *testing.T, root string) Manifest { return c02Manifest("spec/value") }, "does not exist"},
		{"leaf-symlink", func(t *testing.T, root string) Manifest {
			writeTestFile(t, root, "outside", "value")
			if err := os.MkdirAll(filepath.Join(root, "spec"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "spec/value")); err != nil {
				t.Fatal(err)
			}
			return c02Manifest("spec/value")
		}, "symlink"},
		{"parent-symlink", func(t *testing.T, root string) Manifest {
			writeTestFile(t, root, "real/value", "value")
			if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "spec")); err != nil {
				t.Fatal(err)
			}
			return c02Manifest("spec/value")
		}, "symlink"},
		{"directory", func(t *testing.T, root string) Manifest {
			if err := os.MkdirAll(filepath.Join(root, "spec/value"), 0o755); err != nil {
				t.Fatal(err)
			}
			return c02Manifest("spec/value")
		}, "regular file"},
		{"fifo", func(t *testing.T, root string) Manifest {
			if err := os.MkdirAll(filepath.Join(root, "spec"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(filepath.Join(root, "spec/value"), 0o600); err != nil {
				t.Fatal(err)
			}
			return c02Manifest("spec/value")
		}, "regular file"},
		{"oversized", func(t *testing.T, root string) Manifest {
			writeTestFile(t, root, "spec/value", strings.Repeat("x", CaptureLimit+1))
			return c02Manifest("spec/value")
		}, "capture bound"},
		{"own-artifact-overlap", func(t *testing.T, root string) Manifest {
			writeTestFile(t, root, "out/value", "value")
			m := c02Manifest("out/value")
			m.Probes[0].Files = []string{"out/value"}
			m.Probes[0].Expect = &Expect{Files: map[string]string{"out/value": "out/value"}}
			return m
		}, "also one of its declared artifacts"},
		{"cross-artifact-overlap", func(t *testing.T, root string) Manifest {
			writeTestFile(t, root, "spec/value", "value")
			m := c02Manifest("spec/value")
			m.Probes = append(m.Probes, Probe{ID: "producer", Run: "true", Timeout: 30, Files: []string{"spec/value"}})
			return m
		}, "declared artifact of probe"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, ".gitignore", ".ran\n")
			m := tc.setup(t, root)
			err := m.Validate(root)
			recorded := false
			if err == nil {
				result := Record(root, m, []byte("c02"), RecordOptions{AllowDirty: true})
				if result.Outcome.Exit == ExitOK {
					recorded = true
				}
				if f, ok := result.Outcome.Failures["pin"]; ok {
					err = stringsError(f.Detail)
				}
			}
			if _, statErr := os.Stat(filepath.Join(root, ".ran")); !os.IsNotExist(statErr) {
				t.Fatalf("probe executed before refusal: %v", statErr)
			}
			if recorded {
				t.Fatal("invalid spec recorded")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestC02ExactlyBoundedSpecRecordsAndReloads(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".ran\n")
	data := strings.Repeat("x", CaptureLimit)
	writeTestFile(t, root, "spec/value", data)
	m := c02Manifest("spec/value")
	result := Record(root, m, []byte("c02"), RecordOptions{AllowDirty: true})
	if result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	lock, _, err := LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	entry := lock.Probes["pin"]
	got, available, err := BlobData(root, entry.Stdout, false)
	if err != nil || !available || string(got) != data {
		t.Fatalf("boundary blob unavailable: available=%v err=%v size=%d", available, err, len(got))
	}
	if _, err := os.Stat(filepath.Join(root, ".ran")); err != nil {
		t.Fatalf("valid boundary probe did not execute: %v", err)
	}
}

func c02Manifest(spec string) Manifest {
	return testManifest(Probe{ID: "pin", Run: "printf ran > .ran; cat " + spec, Timeout: 5, Expect: &Expect{Stdout: spec}})
}

type stringsError string

func (e stringsError) Error() string { return string(e) }
