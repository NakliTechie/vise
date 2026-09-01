package vise

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

# Advanced: fingerprint tool versions that affect probe output.
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
	if err := atomicWrite(root, manifestPath, []byte(StubManifest), 0o644); err != nil {
		return err
	}
	if err := updateGitignore(root); err != nil {
		return fmt.Errorf("vise.toml was written but .gitignore update failed: %w", err)
	}
	return nil
}

func updateGitignore(root string) error {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
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
