package vise

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The declared-artifact lifecycle decides which failure wins when two are true
// at once. A behavioural probe cannot see that ordering — it only ever sees the
// first message — so it is pinned here.
func TestDeclaredArtifactFailurePrecedence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) []string
		want  string
	}{
		{
			name: "tracked file is refused before its path is validated",
			setup: func(t *testing.T, root string) []string {
				// tracked.txt is committed by the helper; a tracked artifact and
				// a symlinked one are both wrong, and tracked must win.
				return []string{"tracked.txt"}
			},
			want: "is tracked by git",
		},
		{
			name: "a directory is refused rather than deleted",
			setup: func(t *testing.T, root string) []string {
				if err := os.MkdirAll(filepath.Join(root, "out"), 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{"out"}
			},
			want: "is a directory; recursive deletion is refused",
		},
		{
			name: "a symlink component is refused",
			setup: func(t *testing.T, root string) []string {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				return []string{"link/out.txt"}
			},
			want: "symlink components are not allowed",
		},
		{
			name: "an undeletable artifact names the delete failure",
			setup: func(t *testing.T, root string) []string {
				dir := filepath.Join(root, "ro")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "out.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
				return []string{"ro/out.txt"}
			},
			want: "delete artifact",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := testGitRepo(t)
			files := test.setup(t, root)
			artifacts := newDeclaredArtifacts(root, files)
			err := artifacts.reset()
			if err == nil {
				t.Fatalf("reset accepted %v", files)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not name %q", err.Error(), test.want)
			}
		})
	}
}

func TestDeclaredArtifactCaptureFailures(t *testing.T) {
	root := testGitRepo(t)

	// A probe that promised an artifact and did not produce one.
	artifacts := newDeclaredArtifacts(root, []string{"out/missing.txt"})
	if _, err := artifacts.capture(); err == nil || !strings.Contains(err.Error(), "was not produced") {
		t.Fatalf("missing artifact: %v", err)
	}

	// A symlink where a regular file was promised: refused at path validation,
	// before the file is ever opened, so the artifact cannot point outside.
	if err := os.MkdirAll(filepath.Join(root, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(target, []byte("elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "out", "linked.txt")); err != nil {
		t.Fatal(err)
	}
	artifacts = newDeclaredArtifacts(root, []string{"out/linked.txt"})
	if _, err := artifacts.capture(); err == nil || !strings.Contains(err.Error(), "symlink components are not allowed") {
		t.Fatalf("symlinked artifact: %v", err)
	}

	// A named pipe is neither a symlink nor a regular file: reading it would
	// block forever, so it is refused by the regular-file check.
	if err := syscall.Mkfifo(filepath.Join(root, "out", "pipe"), 0o644); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	artifacts = newDeclaredArtifacts(root, []string{"out/pipe"})
	if _, err := artifacts.capture(); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("fifo artifact: %v", err)
	}

	// The ordinary case still yields a non-nil map keyed by the cleaned path.
	if err := os.WriteFile(filepath.Join(root, "out", "real.txt"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts = newDeclaredArtifacts(root, []string{"./out/real.txt"})
	captured, err := artifacts.capture()
	if err != nil {
		t.Fatal(err)
	}
	if capture, ok := captured["out/real.txt"]; !ok || string(capture.Prefix) != "real" {
		t.Fatalf("captured = %#v", captured)
	}

	// No declared files still means an empty map, never nil: callers compare
	// lengths and render it as {}.
	empty, err := newDeclaredArtifacts(root, nil).capture()
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %#v, %v", empty, err)
	}
}

// A probe that produces a directory where it declared a file. The reset path
// refuses a directory before the run; nothing covered the capture path after
// it, so accepting a produced directory as an empty artifact left the suite
// green — and an empty artifact hashes to something stable, which means the
// baseline would have frozen "the probe produced nothing" as correct.
func TestAProducedDirectoryIsNotCapturedAsAnArtifact(t *testing.T) {
	root := testGitRepo(t)
	runner := Runner{Root: root}

	probe := Probe{ID: "dir", Run: "mkdir -p out/result.txt", Timeout: 30, Files: []string{"out/result.txt"}}
	result := runner.RunProbe(probe, false)
	if result.HarnessError == "" {
		t.Fatalf("a directory was accepted where a file was declared: %#v", result.Files)
	}
	if !strings.Contains(result.HarnessError, "out/result.txt") {
		t.Fatalf("the error does not name the artifact: %q", result.HarnessError)
	}
	if len(result.Files) != 0 {
		t.Fatalf("a directory was captured as an artifact: %#v", result.Files)
	}
}
