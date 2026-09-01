package vise

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ValidateRelativePath(root, path string, mustExist bool) error {
	if path == "" {
		return fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute paths are not allowed")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must remain inside the repository")
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes the repository")
	}
	if err := rejectSymlinkComponents(root, clean); err != nil {
		return err
	}
	if mustExist {
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path is not a regular file")
		}
	}
	return nil
}

func ValidateArtifactPath(root, path string) error {
	if err := ValidateRelativePath(root, path, false); err != nil {
		return err
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return fmt.Errorf("artifacts cannot target Git metadata")
	}
	if clean == ".vise" || strings.HasPrefix(clean, ".vise/") || clean == "vise.toml" || clean == "vise.lock" {
		return fmt.Errorf("artifacts cannot target evaluator state")
	}
	return nil
}

func rejectSymlinkComponents(root, rel string) error {
	current := root
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink components are not allowed")
		}
	}
	return nil
}
