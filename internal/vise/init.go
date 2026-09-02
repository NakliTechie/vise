package vise

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentContract is the rulebook a coding agent reads before working in a gated
// repository. It is embedded so `init` can deliver it: a gate nobody explained
// is a gate an agent works around. vise's own AGENTS.md is a copy of this file,
// which a test keeps identical.
//
//go:embed agents.md
var AgentContract string

const StubManifest = `[vise]
version = 1

# Start here: uncomment one probe and point it at deterministic behavior.
# [[probe]]
# id = "cli-help"
# run = "./mytool --help"
# timeout = 30
# deps = ["fixtures/input.txt"]
# files = ["out/result.json"]

# Universal determinism defaults applied to every probe.
[stubs]
tz = "UTC"
lang = "C"
seed = "1729"
network = "declared-off"

# Name the version of every tool a probe runs. Without this a compiler or
# formatter can change under the baseline and the first red looks like a
# behavior change, which is the most expensive way to learn that it was not.
# vise doctor reports this as missing until you uncomment it, because on a
# repository an agent works in it is not optional. Left commented only because
# the right commands are yours, not vise's.
# [env]
# fingerprint = ["sh --version | head -1", "git --version"]

# Advanced: track a numeric quality objective beside behavior.
# [[metric]]
# id = "complexity"
# run = "your-analyzer --numeric-output"
# direction = "down"
# enforce = "none"
# version_cmd = "your-analyzer --version"
`

var stateIgnoreLines = []string{
	".vise/journal.jsonl",
	".vise/run.lock",
	".vise/tmp/",
}

func InitRepository(root string) error {
	manifestPath := filepath.Join(root, "vise.toml")
	if _, err := os.Lstat(manifestPath); err == nil {
		return fmt.Errorf("vise.toml already exists; init never overwrites it")
	} else if !os.IsNotExist(err) {
		return err
	}
	// Check the ignore file first so a refusal leaves nothing behind.
	if err := rejectExistingSymlinkOrSpecial(filepath.Join(root, ".gitignore")); err != nil {
		return fmt.Errorf("init refuses to rewrite .gitignore: %w", err)
	}
	if err := atomicWrite(root, manifestPath, []byte(StubManifest), 0o644); err != nil {
		return err
	}
	if err := updateGitignore(root); err != nil {
		return fmt.Errorf("vise.toml was written but .gitignore update failed: %w", err)
	}
	if err := writeAgentContract(root); err != nil {
		return fmt.Errorf("vise.toml was written but AGENTS.md could not be created: %w", err)
	}
	return nil
}

// InitCreated lists the files a fresh init wrote, so the caller can report them.
func InitCreated(root string) []string {
	created := []string{"vise.toml"}
	if _, err := os.Lstat(filepath.Join(root, "AGENTS.md")); err == nil {
		created = append(created, "AGENTS.md")
	}
	return created
}

// writeAgentContract installs the agent rulebook, and never overwrites one the
// project already has — a project with its own AGENTS.md has already thought
// about this, and clobbering it would be the tool overruling the operator.
func writeAgentContract(root string) error {
	path := filepath.Join(root, "AGENTS.md")
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicWrite(root, path, []byte(AgentContract), 0o644)
}

func updateGitignore(root string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := readRegularFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(data)
	existing := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		existing[strings.TrimSpace(line)] = true
	}
	var additions []string
	for _, line := range stateIgnoreLines {
		if !existing[line] {
			additions = append(additions, line)
		}
	}
	if len(additions) == 0 {
		return nil
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += strings.Join(additions, "\n") + "\n"
	return atomicWrite(root, path, []byte(text), 0o644)
}
