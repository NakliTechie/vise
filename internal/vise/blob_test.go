package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evaluatorStateDigest lists blob names, not their contents, so a probe that
// rewrites a blob in place leaves the digest unmoved. The claim is that this
// is still safe, because a blob is content-addressed and re-verified whenever
// it is read. Worth demonstrating rather than assuming.
func TestAProbeRewritingABlobIsCaughtWhenItIsRead(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "tool.sh", "#!/bin/sh\nprintf original\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"sh tool.sh\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	// Rewrite a blob in place, keeping its name. The name is what the digest
	// holds, so the digest does not move.
	entries, err := os.ReadDir(filepath.Join(root, ".vise", "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	var rewrote bool
	for _, entry := range entries {
		path := filepath.Join(root, ".vise", "blobs", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		if err := os.WriteFile(path, []byte("substituted"), 0o644); err != nil {
			t.Fatal(err)
		}
		rewrote = true
		break
	}
	if !rewrote {
		t.Skip("no non-empty blob to rewrite")
	}

	outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
	if outcome.Exit == ExitOK {
		t.Fatalf("a rewritten blob gated green: %#v", outcome)
	}
	var detail string
	for _, failure := range outcome.Failures {
		detail += failure.Detail + " "
	}
	if !strings.Contains(detail, "content hash") && !strings.Contains(detail, "blob") {
		t.Fatalf("the failure does not name the blob: %q", detail)
	}
	t.Logf("reported as: %s", strings.TrimSpace(detail))
}
