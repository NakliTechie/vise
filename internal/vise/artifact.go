package vise

import (
	"fmt"
	"os"
	"path/filepath"
)

// declaredArtifacts owns the lifecycle of a probe's declared artifact files:
// the pre-run validation and deletion, and the post-run capture. RunProbe
// delegates to it so that it reads as a sequence of named steps.
type declaredArtifacts struct {
	root  string
	files []string
}

func newDeclaredArtifacts(root string, files []string) declaredArtifacts {
	return declaredArtifacts{root: root, files: files}
}

// reset validates the declared artifact paths and removes any that already
// exist, so every run begins from a clean state. It returns the first harness
// error it encounters, already formatted for the caller.
func (a declaredArtifacts) reset() error {
	tracked, err := GitTrackedPaths(a.root, a.files)
	if err != nil {
		return err
	}
	if len(tracked) > 0 {
		return fmt.Errorf("declared artifact %q is tracked by git; artifacts must be gitignored build outputs because vise deletes them before every run", tracked[0])
	}
	for _, rel := range a.files {
		if err := ValidateArtifactPath(a.root, rel); err != nil {
			return fmt.Errorf("artifact %q: %w", rel, err)
		}
		path := filepath.Join(a.root, rel)
		if info, err := os.Lstat(path); err == nil && info.IsDir() {
			return fmt.Errorf("declared artifact %q is a directory; recursive deletion is refused", rel)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect artifact %q: %w", rel, err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete artifact %q: %w", rel, err)
		}
	}
	return nil
}

// capture reads each declared artifact after the probe has run and returns
// them keyed by their slash-normalized, cleaned path. It returns the first
// harness error it encounters, already formatted for the caller.
func (a declaredArtifacts) capture() (map[string]Capture, error) {
	files := make(map[string]Capture, len(a.files))
	for _, rel := range a.files {
		if err := ValidateArtifactPath(a.root, rel); err != nil {
			return nil, fmt.Errorf("artifact %q after probe: %w", rel, err)
		}
		path := filepath.Join(a.root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("declared artifact %q was not produced", rel)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("declared artifact %q is not a regular file", rel)
		}
		capture, err := captureFile(path)
		if err != nil {
			return nil, fmt.Errorf("read artifact %q: %w", rel, err)
		}
		files[filepath.ToSlash(filepath.Clean(rel))] = capture
	}
	return files, nil
}
