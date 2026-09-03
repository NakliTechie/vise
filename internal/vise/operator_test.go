package vise

import (
	"strings"
	"testing"
)

// The agent contract forbids an agent from writing vise.toml, vise.lock, the
// blobs, or the journal. Every harness failure used to answer with
// next.action fix_probe — "repair the harness" — so an agent that edited the
// manifest was handed two correct instructions pointing opposite ways and no
// legal move between them. Found by an agent reading the documents as the
// party that has to obey them.
func TestHarnessFailuresInProtectedFilesTellTheAgentToStop(t *testing.T) {
	setup := func(t *testing.T) (string, Manifest, []byte) {
		t.Helper()
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "manifest")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
			t.Fatalf("record: %#v", result.Outcome)
		}
		return root, manifest, manifestBytes
	}

	t.Run("a probe added to the manifest after recording", func(t *testing.T) {
		root, _, _ := setup(t)
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n[[probe]]\nid = \"q\"\nrun = \"printf q\"\n")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
		if outcome.Exit != ExitHarness {
			t.Fatalf("exit = %d, want harness", outcome.Exit)
		}
		if outcome.Next.Action != NextHuman {
			t.Fatalf("next.action = %q, want human: the repair is in vise.toml, which an agent may not write", outcome.Next.Action)
		}
		if !strings.Contains(outcome.Next.Detail, "may not write") {
			t.Fatalf("the detail does not say why: %q", outcome.Next.Detail)
		}
	})

	t.Run("a probe command changed in the manifest", func(t *testing.T) {
		root, _, _ := setup(t)
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf changed\"\n")
		manifest, manifestBytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
		if outcome.Next.Action != NextHuman {
			t.Fatalf("next.action = %q, want human", outcome.Next.Action)
		}
	})

	// And a harness failure the agent really can fix still says so: a probe
	// whose command its own change broke is its own to repair.
	t.Run("a probe command the agent can fix", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
		writeTestFile(t, root, "tool.sh", "#!/bin/sh\nprintf ok\n")
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
		// The script the probe runs is now unrunnable, which is the agent's
		// own mess to clean up.
		writeTestFile(t, root, "tool.sh", "#!/bin/sh\nexec /nonexistent/binary\n")

		outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
		if outcome.Exit == ExitOK {
			t.Fatalf("a broken probe command gated green: %#v", outcome)
		}
		if outcome.Next.Action == NextHuman {
			t.Fatalf("next.action = human for a failure the agent can fix itself: %#v", outcome)
		}
	})
}
