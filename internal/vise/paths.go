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

func ensureDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return os.Mkdir(path, mode)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a real directory, not a symlink or special file", path)
	}
	return nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink or special file", path)
	}
	return os.ReadFile(path)
}

func rejectExistingSymlinkOrSpecial(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a symlink or special file", path)
	}
	return nil
}

// stateScratchDir returns root/.vise/tmp, creating .vise and tmp as real
// directories. A symlink at either level is refused: scratch and staging
// files must stay inside the repository.
func stateScratchDir(root string) (string, error) {
	stateDir := filepath.Join(root, ".vise")
	if err := ensureDirectory(stateDir, 0o755); err != nil {
		return "", err
	}
	scratch := filepath.Join(stateDir, "tmp")
	if err := ensureDirectory(scratch, 0o755); err != nil {
		return "", err
	}
	return scratch, nil
}
