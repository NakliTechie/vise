package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRefusesSymlinkedGitignoreBeforeWritingAnything(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "shared-ignore")
	if err := os.WriteFile(target, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	err := InitRepository(root)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "vise.toml")); !os.IsNotExist(statErr) {
		t.Fatal("init wrote vise.toml despite refusing the ignore file")
	}
	info, err := os.Lstat(filepath.Join(root, ".gitignore"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".gitignore symlink was replaced: %v %v", info, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "node_modules/\n" {
		t.Fatalf("symlink target changed: %q %v", data, err)
	}
}
