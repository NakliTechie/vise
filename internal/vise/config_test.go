package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestAppliesDefaults(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "vise.toml", `[vise]
version = 1

[[probe]]
id = "hello"
run = "printf hello"
`)
	manifest, _, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Stubs.TZ != "UTC" || manifest.Stubs.Lang != "C" || manifest.Stubs.Seed != "1729" {
		t.Fatalf("unexpected defaults: %#v", manifest.Stubs)
	}
	if manifest.Probes[0].Timeout != 30 {
		t.Fatalf("timeout = %d", manifest.Probes[0].Timeout)
	}
}

func TestLoadManifestRejectsUnknownAndDuplicateIDs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"unknown", "[vise]\nversion=1\nunknown=true\n", "unknown vise.toml keys"},
		{"duplicate", "[vise]\nversion=1\n[[probe]]\nid='x'\nrun='true'\n[[metric]]\nid='x'\nrun='printf 1'\n", "duplicate id"},
		{"reserved probe id", "[vise]\nversion=1\n[[probe]]\nid='fingerprint'\nrun='true'\n", "reserved for harness failures"},
		{"reserved metric id", "[vise]\nversion=1\n[[metric]]\nid='journal'\nrun='printf 1'\n", "reserved for harness failures"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "vise.toml", test.body)
			_, _, err := LoadManifest(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManifestRejectsReservedEnvironmentAndEvaluatorArtifacts(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"reserved-env", "[vise]\nversion=1\n[[probe]]\nid='x'\nrun='true'\nenv={TZ='local'}\n", "reserved variable TZ"},
		{"git-artifact", "[vise]\nversion=1\n[[probe]]\nid='x'\nrun='true'\nfiles=['.git/index']\n", "Git metadata"},
		{"lock-artifact", "[vise]\nversion=1\n[[probe]]\nid='x'\nrun='true'\nfiles=['vise.lock']\n", "evaluator state"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "vise.toml", test.body)
			_, _, err := LoadManifest(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRelativePathRejectsEscapesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := ValidateRelativePath(root, "../escape", false); err == nil {
		t.Fatal("expected traversal rejection")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRelativePath(root, "link", true); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestLoadManifestRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "manifest")
	if err := os.WriteFile(target, []byte("[vise]\nversion=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "vise.toml")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadManifest(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadProposals(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".vise/proposals.toml", "[[probe]]\nid='regression'\nrun='printf fixed'\n")
	proposals, err := LoadProposals(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals.Probes) != 1 || proposals.Probes[0].Timeout != 30 {
		t.Fatalf("proposals = %#v", proposals)
	}
}

func TestArtifactPathExclusionsAreCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{".GIT/index", ".Git/HEAD", ".VISE/run.lock", ".Vise/blobs/x", "VISE.LOCK", "Vise.toml"} {
		if err := ValidateArtifactPath(root, path); err == nil {
			t.Fatalf("%q was accepted as an artifact", path)
		}
	}
	if err := ValidateArtifactPath(root, "out/result.txt"); err != nil {
		t.Fatalf("plain artifact rejected: %v", err)
	}
}
