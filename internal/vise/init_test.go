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
	_, err := InitRepository(root)
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

func TestInitInstallsTheAgentContractWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	created, err := InitRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil || string(contract) != AgentContract {
		t.Fatalf("AGENTS.md = %d bytes, %v", len(contract), err)
	}
	if len(created) != 2 || created[1] != "AGENTS.md" {
		t.Fatalf("created = %v", created)
	}

	// A project that already has one has already thought about this.
	other := t.TempDir()
	mine := "# our own rules\n"
	if err := os.WriteFile(filepath.Join(other, "AGENTS.md"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = InitRepository(other)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range created {
		if name == "AGENTS.md" {
			t.Error("init reported writing an AGENTS.md it left alone")
		}
	}
	if data, _ := os.ReadFile(filepath.Join(other, "AGENTS.md")); string(data) != mine {
		t.Fatalf("init overwrote an existing AGENTS.md: %q", data)
	}
}

func TestTheRepositoryContractMatchesTheEmbeddedOne(t *testing.T) {
	// vise gates itself, so its own AGENTS.md is the template in use. If they
	// drift, one of them is lying to somebody.
	root, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Skipf("repository AGENTS.md unavailable: %v", err)
	}
	if string(root) != AgentContract {
		t.Fatal("AGENTS.md and internal/vise/agents.md have drifted; copy one over the other")
	}
}
