package vise

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// Finalize decides next.action from the failures an outcome carries, and it
// gets the operator question right: harness plus any operator-owned failure is
// human, harness alone is fix_probe. Every bug in this area came from code
// assigning a whole Next over the top of that decision afterwards.
//
// Eight of them shipped. A rerun refusal, an empty manifest, and seven record
// failures all set next.action human by hand and left the operator field
// absent — the field AGENTS.md tells an agent to read instead of matching on
// the message. An agent that did as it was told saw no operator flag on a
// refused rerun and read the fix_probe row: repair the probe your change broke.
// It had not broken anything, and the repair was an operator's.
//
// So overriding the action is the thing to police, not the flag. A detail
// override is fine: it changes the wording of the remedy, never the branch.
// Anything that replaces the whole Next has to appear here with a reason.
func TestNothingOverridesTheActionFinalizeChose(t *testing.T) {
	allowed := map[string]string{
		"record.go:264": "record --preview: no failure exists, so there is nothing for the operator flag to mark; human means an operator reviews a diff",
		"record.go:378": "record's flake path: no harness failure exists, and a nondeterministic probe is usually a nondeterministic program, which an agent may fix",
	}
	// Not `outcome.Next`: the first version of this guard matched that literal
	// name, and the very bug it was written for assigns to `blocked.Next`. It
	// passed against the real defect, restored on purpose to check it. Match
	// the field on any receiver instead, and skip the files that carry a
	// different type entirely.
	override := regexp.MustCompile(`\.Next\s*=\s*Next\{`)
	skip := map[string]string{
		"types.go":  "Finalize is the decision this guard protects",
		"status.go": "StatusReport, not an Outcome: it carries no failures and no operator field",
		"doctor.go": "DoctorReport, same",
	}
	for _, dir := range []string{filepath.Join("..", "vise"), filepath.Join("..", "cli")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if _, ok := skip[name]; ok {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for number, line := range strings.Split(string(data), "\n") {
				if !override.MatchString(line) {
					continue
				}
				site := fmt.Sprintf("%s:%d", name, number+1)
				if _, ok := allowed[site]; !ok {
					t.Errorf("%s replaces the whole Next that Finalize chose:\n\t%s\nSet Operator on the failure instead, so next.action and the operator field agree. If this really is neither, add the site here with a reason.", site, strings.TrimSpace(line))
				}
			}
		}
	}
}

// The third way this bug got in, after the two the guards above close: a
// failure built with the right id and no Operator flag at all. The CLI's
// journal-append failure shipped that way, and neither the literal-detail scan
// nor the override guard could see it — nothing was overridden and the detail
// was err.Error(), built at run time.
//
// These ids name the protected surface itself. A failure carrying one of them
// is an operator's by definition, whatever its message says, so the id is the
// thing to check. Only literal ids are scanned, which is exactly right: a
// failure keyed by a probe id is a variable, so a probe some operator happened
// to name "journal" is not swept up by this.
func TestAFailureNamedForAProtectedFileIsMarkedAsTheOperatorsOwn(t *testing.T) {
	protected := []string{
		"journal",         // .vise/journal.jsonl, from which the rerun budget is derived
		"manifest",        // vise.toml
		"vise.lock",       // the baseline
		"tamper-hash",     // the baseline again
		"fingerprint",     // the [env] block of vise.toml
		"rerun-limit",     // only an operator lifts it
		"operator-review", // by name
		"persistence",     // a write to .vise/ failed
		"working-tree",    // commit or stash; not an agent's call
	}
	// A construction is either AddFailure("id", ...) or one of the harness
	// helpers, whose second argument is the id.
	built := regexp.MustCompile(`(?:AddFailure|harnessOnly|harnessForOperator|harnessForOperatorSaying)\((?:"[a-z]+", )?"([a-z.-]+)"`)
	for _, dir := range []string{filepath.Join("..", "vise"), filepath.Join("..", "cli")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for number, line := range strings.Split(string(data), "\n") {
				match := built.FindStringSubmatch(line)
				if match == nil || !slices.Contains(protected, match[1]) {
					continue
				}
				// harnessOnly is the one that cannot be an operator's, by
				// construction; the others either set the flag or take it.
				marked := strings.Contains(line, "harnessForOperator") || strings.Contains(line, "Operator: true")
				if !marked {
					t.Errorf("%s:%d builds a failure named %q — a file an agent may not write — and does not mark it as the operator's:\n\t%s\nThe agent is told to read the operator field; without it this answers fix_probe.", name, number+1, match[1], strings.TrimSpace(line))
				}
			}
		}
	}
}
