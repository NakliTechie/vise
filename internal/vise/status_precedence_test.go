package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BuildStatus assembles four sections, each able to change the reported state.
// When two apply at once only one wins, and which one is a contract an agent
// depends on: it decides whether the next move is "re-record", "restore the
// toolchain", or "repair the journal". The behavioural probe sees one state at
// a time, so the precedence is pinned here.
func TestStatusStatePrecedence(t *testing.T) {
	// A manifest whose fingerprint command can be made to drift or fail.
	manifest := func(probe, fingerprint string) string {
		return "[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n[env]\nfingerprint = [\"" + fingerprint + "\"]\n" + probe
	}
	stable := "[[probe]]\nid = \"stable\"\nrun = \"printf stable\"\n"

	recorded := func(t *testing.T) string {
		t.Helper()
		root := testGitRepo(t)
		writeTestFile(t, root, "tool-version", "v1")
		writeTestFile(t, root, "vise.toml", manifest(stable, "cat tool-version"))
		testGit(t, root, "add", ".")
		testGit(t, root, "commit", "-qm", "manifest")
		parsed, bytes, err := LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if result := Record(root, parsed, bytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
			t.Fatalf("record: %#v", result.Outcome)
		}
		return root
	}

	driftFingerprint := func(t *testing.T, root string) {
		t.Helper()
		writeTestFile(t, root, "tool-version", "v2")
	}
	driftBaseline := func(t *testing.T, root string) {
		t.Helper()
		writeTestFile(t, root, "vise.toml", manifest(stable+"[[probe]]\nid = \"extra\"\nrun = \"printf extra\"\n", "cat tool-version"))
	}

	tests := []struct {
		name       string
		mutate     func(t *testing.T, root string)
		wantState  string
		wantIn     string
		wantAction string
	}{
		{
			name:       "environment drift beats baseline drift",
			mutate:     func(t *testing.T, root string) { driftFingerprint(t, root); driftBaseline(t, root) },
			wantState:  "environment-drift",
			wantAction: "human",
			wantIn:     "restore the recorded toolchain",
		},
		{
			name: "an empty manifest beats environment drift",
			mutate: func(t *testing.T, root string) {
				driftFingerprint(t, root)
				writeTestFile(t, root, "vise.toml", manifest("", "cat tool-version"))
			},
			wantState:  "harness-error",
			wantAction: "human",
			wantIn:     "declares no [[probe]]",
		},
		{
			name: "a broken journal beats environment drift",
			mutate: func(t *testing.T, root string) {
				driftFingerprint(t, root)
				path := filepath.Join(root, ".vise", "journal.jsonl")
				if err := os.WriteFile(path, []byte("{\"e\":\"torn\n{\"e\":\"gate\"}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantState:  "harness-error",
			wantAction: "human",
			wantIn:     "repair the local journal",
		},
		{
			name: "a failing fingerprint command beats baseline drift",
			mutate: func(t *testing.T, root string) {
				driftBaseline(t, root)
				if err := os.Remove(filepath.Join(root, "tool-version")); err != nil {
					t.Fatal(err)
				}
			},
			wantState:  "harness-error",
			wantAction: "human",
			wantIn:     "repair the environment fingerprint command",
		},
		{
			name: "malformed proposals never change the verdict",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, root, ".vise/proposals.toml", "not = [valid\n")
			},
			wantState:  "ready",
			wantAction: "proceed",
			wantIn:     "run vise gate",
		},
		{
			name:       "baseline drift alone reports itself",
			mutate:     driftBaseline,
			wantState:  "baseline-drift",
			wantAction: "human",
			wantIn:     "vise.toml and vise.lock disagree",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := recorded(t)
			test.mutate(t, root)
			report := BuildStatus(root)
			if report.State != test.wantState {
				t.Fatalf("state = %q, want %q (next: %s — %s)", report.State, test.wantState, report.Next.Action, report.Next.Detail)
			}
			if !strings.Contains(report.Next.Detail, test.wantIn) {
				t.Fatalf("next detail %q does not name %q", report.Next.Detail, test.wantIn)
			}
			// The action, not only the prose. An agent branches on the action,
			// and changing environment drift's action to "proceed" — carry on
			// through a moved toolchain — left this test green.
			if report.Next.Action != test.wantAction {
				t.Fatalf("next action = %q, want %q", report.Next.Action, test.wantAction)
			}
			if report.Exit != ExitOK {
				t.Fatalf("status must always exit 0, got %d", report.Exit)
			}
		})
	}
}

// A journal that cannot be read and a journal that holds nothing both produced
// an empty list, so the screen printed "journal: empty" two lines above
// "repair the local journal" — one line contradicting another on the one
// screen an agent reads before it acts. Found by a coding agent asked to
// report what looked wrong.
func TestStatusDistinguishesAnUnreadableJournalFromAnEmptyOne(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	// Before recording there is no journal at all: empty, and readable.
	if report := BuildStatus(root); report.JournalUnreadable {
		t.Fatal("a repository with no journal was called unreadable")
	}

	parsed, bytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, parsed, bytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	if err := os.WriteFile(filepath.Join(root, ".vise", "journal.jsonl"), []byte("{\"e\":\"torn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := BuildStatus(root)
	if !report.JournalUnreadable {
		t.Fatal("a torn journal was reported as an ordinary one")
	}
	if report.State != "harness-error" || !strings.Contains(report.Next.Detail, "repair the local journal") {
		t.Fatalf("state = %q, next = %#v", report.State, report.Next)
	}
}
