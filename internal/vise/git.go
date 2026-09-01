package vise

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func GitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git work tree required: %s", strings.TrimSpace(stderr.String()))
	}
	root := strings.TrimSpace(stdout.String())
	if root == "" {
		return "", fmt.Errorf("git returned an empty work-tree root")
	}
	return filepath.Clean(root), nil
}

func GitHead(root string) (string, error) {
	return gitOutput(root, "rev-parse", "HEAD")
}

func GitDirty(root string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git status: %s", strings.TrimSpace(stderr.String()))
	}
	entries := bytes.Split(stdout.Bytes(), []byte{0})
	for _, entry := range entries {
		if len(entry) < 4 {
			continue
		}
		path := filepath.ToSlash(string(entry[3:]))
		if path == ".vise/run.lock" || path == ".vise/journal.jsonl" || strings.HasPrefix(path, ".vise/tmp/") {
			continue
		}
		return true, nil
	}
	return false, nil
}

func GitTrackedSnapshot(root string) (string, error) {
	cmd := exec.Command("git", "diff", "--binary", "--no-ext-diff", "HEAD", "--", ".")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("snapshot tracked files: %w", err)
	}
	return HashBytes(data), nil
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
