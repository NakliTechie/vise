package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every rule in ValidateRelativePath and ValidateArtifactPath, one case each.
// Seven mutations of this code survived the suite: accepting an empty path, an
// absolute one, the repository root itself, one that traverses out, and
// following symlinks where a regular file was required. These paths come from
// a manifest and a lockfile, and vise deletes declared artifacts before every
// run — so a rule that stops being enforced is a rule that deletes somebody's
// files outside the checkout.
func TestPathValidationEnforcesEveryRule(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "real.txt", "real")
	writeTestFile(t, root, filepath.Join("nested", "deep.txt"), "deep")
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}

	refused := []struct {
		name   string
		path   string
		wantIn string
	}{
		{"an empty path", "", "must not be empty"},
		{"an absolute path", "/etc/passwd", "absolute"},
		{"the repository root itself", ".", "inside the repository"},
		{"the parent directory", "..", "inside the repository"},
		{"a traversal", "../outside.txt", "inside the repository"},
		{"a traversal that looks nested", "nested/../../outside.txt", "inside the repository"},
		{"a path through a symlinked directory", "escape/sentinel.txt", "symlink"},
	}
	for _, test := range refused {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRelativePath(root, test.path, false)
			if err == nil {
				t.Fatalf("%q was accepted", test.path)
			}
			if !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error %q does not say %q", err, test.wantIn)
			}
		})
	}

	// Ordinary paths inside the repository are accepted, or the rules above
	// would be satisfied by refusing everything.
	for _, path := range []string{"real.txt", "./real.txt", "nested/deep.txt", "does/not/exist.txt"} {
		if err := ValidateRelativePath(root, path, false); err != nil {
			t.Fatalf("%q was refused: %v", path, err)
		}
	}

	// mustExist demands a regular file that is really there, and a symlink to
	// one does not count: following it reads bytes from outside the checkout.
	if err := ValidateRelativePath(root, "does/not/exist.txt", true); err == nil {
		t.Fatal("a missing file satisfied mustExist")
	}
	if err := ValidateRelativePath(root, "linked.txt", true); err == nil {
		t.Fatal("a symlink satisfied mustExist; its target lies outside the repository")
	}
	if err := ValidateRelativePath(root, "real.txt", true); err != nil {
		t.Fatalf("a real regular file was refused: %v", err)
	}

	// The sentinel outside the repository is untouched by all of the above.
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "do not touch" {
		t.Fatalf("the file outside the repository changed: %q, %v", data, err)
	}
}

// Artifacts get two more rules, because vise deletes them before every run:
// never Git's metadata, never the evaluator's own state. Both compare
// case-insensitively, since on APFS and NTFS ".GIT/index" is .git/index.
func TestArtifactPathsRefuseGitAndEvaluatorState(t *testing.T) {
	root := testGitRepo(t)

	for _, path := range []string{
		".git", ".git/index", ".GIT/index", ".Git/HEAD",
		".vise", ".vise/blobs/x", ".VISE/journal.jsonl",
		"vise.toml", "VISE.TOML", "vise.lock", "Vise.Lock",
	} {
		if err := ValidateArtifactPath(root, path); err == nil {
			t.Errorf("%q was accepted as a declared artifact", path)
		}
	}

	// And an ordinary build output is still allowed, including one whose name
	// merely starts with the same letters.
	for _, path := range []string{"out/result.txt", "vise.tomlx", ".gitignore", "dist/app"} {
		if err := ValidateArtifactPath(root, path); err != nil {
			t.Errorf("%q was refused as a declared artifact: %v", path, err)
		}
	}
}
