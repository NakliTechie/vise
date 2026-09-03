package vise

import (
	"os"
	"path/filepath"
	"regexp"
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

	// And a harness failure the agent really can fix still says so. This is the
	// negative control, and the first version of it did not work: the probe I
	// wrote exited 126, which is behavior drift and not a harness failure at
	// all, so the branch under test was never reached and making
	// hasOperatorFailure return true unconditionally passed.
	t.Run("a harness failure the agent can fix itself", func(t *testing.T) {
		// The manifest is left alone: editing it is operator drift, which
		// would make this pass for the wrong reason. The stray comes from the
		// script the probe runs, which is ordinary source the agent may edit.
		root := testGitRepo(t)
		writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
		writeTestFile(t, root, "tool.sh", "#!/bin/sh\nprintf p\n")
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
		writeTestFile(t, root, "tool.sh", "#!/bin/sh\nprintf p\nprintf stray > leftover.txt\n")

		outcome := Verify(root, manifest, manifestBytes, VerifyOptions{}).Outcome
		if outcome.Exit != ExitHarness {
			t.Fatalf("a stray write was not a harness failure: %#v", outcome)
		}
		if outcome.Next.Action != NextFixProbe {
			t.Fatalf("next.action = %q, want fix_probe: removing a stray write is the agent's own job", outcome.Next.Action)
		}
	})
}

// hasOperatorFailure decides between "repair the harness" and "stop and fetch a
// human", and both directions matter: an agent told to repair a file it may not
// write has no legal move, and an agent that stops on something it could have
// fixed wastes the operator's attention. Tested directly, because reaching
// every one of the twelve sites through a real repository is not possible and
// the sites that are reachable would leave the rest unguarded.
func TestTheOperatorRoutingDependsOnTheFailuresPresent(t *testing.T) {
	tests := []struct {
		name       string
		failures   map[string]Failure
		wantAction string
	}{
		{
			name:       "one failure the agent can fix",
			failures:   map[string]Failure{"p": {Class: "harness", Detail: "probe wrote a stray"}},
			wantAction: NextFixProbe,
		},
		{
			name:       "one failure only an operator can fix",
			failures:   map[string]Failure{"p": {Class: "harness", Detail: "probe definition changed", Operator: true}},
			wantAction: NextHuman,
		},
		{
			// human wins: the agent cannot finish while the operator's one
			// stands, so telling it to repair the other sends it round a loop
			// it cannot leave.
			name: "one of each",
			failures: map[string]Failure{
				"p": {Class: "harness", Detail: "probe wrote a stray"},
				"q": {Class: "harness", Detail: "probe definition changed", Operator: true},
			},
			wantAction: NextHuman,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := NewOutcome("verify")
			outcome.Counts.Declared = len(test.failures)
			for id, failure := range test.failures {
				outcome.AddFailure(id, failure)
			}
			outcome.Finalize()

			if outcome.Exit != ExitHarness {
				t.Fatalf("exit = %d, want harness", outcome.Exit)
			}
			if outcome.Next.Action != test.wantAction {
				t.Fatalf("next.action = %q, want %q", outcome.Next.Action, test.wantAction)
			}
		})
	}
}

// Seven times tonight a repair living in a protected file was offered to the
// agent as fix_probe. Each was fixed where it was found, and the next audit
// found another. This is the exhaustive version: every next.action detail the
// binary can emit is scanned, and one that names a file the agent contract
// forbids the agent from writing must not be fix_probe.
func TestNoFixProbeActionPointsAtAFileTheAgentMayNotWrite(t *testing.T) {
	// The files rule 1 protects, as they appear in prose.
	protected := []string{"vise.toml", "vise.lock", ".vise/blobs", ".vise/journal.jsonl"}

	roots := []string{filepath.Join("..", "vise"), filepath.Join("..", "cli")}
	action := regexp.MustCompile(`Next\{Action: (?:vise\.)?(\w+), Detail: "((?:[^"\\]|\\.)*)"`)

	var checked int
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range action.FindAllStringSubmatch(string(data), -1) {
				kind, detail := match[1], match[2]
				checked++
				if kind != "NextFixProbe" {
					continue
				}
				for _, file := range protected {
					if strings.Contains(detail, file) {
						t.Errorf("%s/%s offers fix_probe for a repair in %s: %q", root, name, file, detail)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("scanned no next actions; the pattern is not matching what the code writes")
	}
	t.Logf("scanned %d next actions", checked)
}
