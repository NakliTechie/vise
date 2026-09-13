package vise

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDependencyDiagnosticRetainsOriginalFilesystemErrors(t *testing.T) {
	for _, operation := range []string{"lstat", "open", "read"} {
		cause := &os.PathError{Op: operation, Path: filepath.Join(t.TempDir(), "input"), Err: os.ErrPermission}
		err := &dependencyError{operation: "hash dependency", path: "fixtures/input", cause: cause}
		want := fmt.Sprintf("hash dependency %q: %s: %v", "fixtures/input", operation, os.ErrPermission)
		if err.Error() != want || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("operation or cause lost: %v", err)
		}
		var got *os.PathError
		if !errors.As(err, &got) || got != cause || errors.Unwrap(err) != cause {
			t.Fatalf("original filesystem error was replaced: %#v", got)
		}
	}
	cause := errors.New("symlink components are not allowed")
	err := &dependencyError{operation: "dependency", path: "fixtures/input", cause: cause}
	if err.Error() != `dependency "fixtures/input": symlink components are not allowed` || !errors.Is(err, cause) {
		t.Fatalf("non-filesystem error changed: %v", err)
	}
}

func TestDependencyDiagnosticsDoNotDependOnCheckoutLocation(t *testing.T) {
	for _, kind := range []string{"missing-leaf", "missing-parent", "not-directory", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			var first string
			for copy := 0; copy < 2; copy++ {
				root := t.TempDir()
				rel := "fixtures/input"
				writeTestFile(t, root, "fixtures/good", "good input")
				switch kind {
				case "missing-parent":
					rel = "missing-parent/input"
				case "not-directory":
					rel = "fixtures/good/input"
				case "symlink":
					if err := os.Symlink("good", filepath.Join(root, rel)); err != nil {
						t.Fatal(err)
					}
				case "directory":
					rel = "fixtures"
				}
				_, err := HashDependencies(root, []string{rel})
				if err == nil {
					t.Fatal("invalid dependency accepted")
				}
				if !strings.Contains(err.Error(), rel) || strings.Contains(err.Error(), root) {
					t.Errorf("diagnostic must identify the declared path without the checkout root: %v", err)
				}
				if copy == 0 {
					first = err.Error()
				} else if err.Error() != first {
					t.Errorf("same failure in different root: %q != %q", err.Error(), first)
				}
				var want error
				switch kind {
				case "missing-leaf", "missing-parent":
					want = os.ErrNotExist
				case "not-directory":
					want = syscall.ENOTDIR
				}
				if want != nil {
					var pathErr *os.PathError
					if !errors.Is(err, want) || !errors.As(err, &pathErr) {
						t.Fatalf("lost filesystem cause: %v", err)
					}
					if pathErr.Path != filepath.Join(root, rel) || pathErr.Op != "lstat" {
						t.Errorf("underlying error changed: %#v", pathErr)
					}
					if !strings.Contains(err.Error(), pathErr.Op) || !strings.Contains(err.Error(), pathErr.Err.Error()) {
						t.Errorf("diagnostic hid operation or useful cause: %v", err)
					}
				}
				good, err := HashDependencies(root, []string{"fixtures/good"})
				if err != nil || good["fixtures/good"] != HashBytes([]byte("good input")) {
					t.Fatalf("good dependency control: %v %v", good, err)
				}
			}
		})
	}
}
