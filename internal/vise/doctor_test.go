package vise

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func doctorChecks(report DoctorReport) []string {
	seen := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		seen = append(seen, finding.Check)
	}
	return seen
}

// Every check below corresponds to a setup failure that actually cost a
// session when vise was handed to a coding agent. The point of the command is
// that each is visible in one second from outside the sandbox and takes an
// hour to diagnose from inside it.
func TestDoctorNamesTheSetupFailuresThatCostASession(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, root string)
		wantOne  string
		wantText string
	}{
		{
			name: "an unfingerprinted toolchain",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
			},
			wantOne:  "env-fingerprint",
			wantText: "toolchain change is invisible",
		},
		{
			name: "a path outside the checkout",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"true\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\nenv = { CACHE = \"/opt/somewhere/cache\" }\n")
			},
			wantOne:  "portable-paths",
			wantText: "/opt/somewhere/cache",
		},
		{
			name: "a harness wrapper a probe runs without declaring it",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, root, "tool.sh", "#!/bin/sh\nBIN=\"$VISE_TMP/app\"\nprintf tool\n")
				writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"true\"]\n[[probe]]\nid = \"p\"\nrun = \"sh tool.sh\"\n")
			},
			wantOne:  "declared-inputs",
			wantText: "not in its deps",
		},
		{
			name: "an uncommitted baseline",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"true\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
				writeTestFile(t, root, "vise.lock", "{}")
			},
			wantOne:  "baseline-committed",
			wantText: "fresh clone has no baseline",
		},
		{
			name: "no agent contract",
			setup: func(t *testing.T, root string) {
				writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"true\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
			},
			wantOne:  "agent-contract",
			wantText: "no written rules",
		},
		{
			name: "unignored local state",
			setup: func(t *testing.T, root string) {
				// The shared helper writes the ignore entries; this is the
				// repository that never ran vise init.
				writeTestFile(t, root, ".gitignore", "node_modules/\n")
				writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"true\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
			},
			wantOne:  "local-state-ignored",
			wantText: "per-checkout state",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := testGitRepo(t)
			test.setup(t, root)
			report := Doctor(root)
			if report.Exit != ExitOK {
				t.Fatalf("doctor must always exit 0, got %d", report.Exit)
			}
			if report.Ready {
				t.Fatal("doctor reported ready")
			}
			var found *DoctorFinding
			for i := range report.Findings {
				if report.Findings[i].Check == test.wantOne {
					found = &report.Findings[i]
				}
			}
			if found == nil {
				t.Fatalf("no %q finding; got %v", test.wantOne, doctorChecks(report))
			}
			if !strings.Contains(found.Detail, test.wantText) {
				t.Fatalf("detail %q does not say %q", found.Detail, test.wantText)
			}
			if found.Remedy == "" {
				t.Fatalf("%s carries no remedy", test.wantOne)
			}
		})
	}
}

// A repository set up the way the guide says must come back clean, or the
// command is a source of noise rather than a check an operator can act on.
func TestDoctorReportsReadyForACorrectlySetUpRepository(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "tool.sh", "#!/bin/sh\nprintf tool\n")
	writeTestFile(t, root, "AGENTS.md", AgentContract)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"sh --version\"]\n[[probe]]\nid = \"p\"\nrun = \"sh tool.sh\"\ndeps = [\"tool.sh\"]\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "harness")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	testGit(t, root, "add", "vise.lock", ".vise/blobs")
	testGit(t, root, "commit", "-qm", "baseline")

	report := Doctor(root)
	if !report.Ready {
		t.Fatalf("doctor found %v: %#v", doctorChecks(report), report.Findings)
	}
	if report.Next.Action != "proceed" {
		t.Fatalf("next = %#v", report.Next)
	}
}

// Doctor must not write anything: an operator runs it to ask a question, and
// a question that creates a lockfile is not read-only.
func TestDoctorWritesNothing(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
	before, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	Doctor(root)
	after, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.Tracked != after.Tracked || len(before.ChangedUntracked(after)) > 0 {
		t.Fatalf("doctor changed the checkout: %v", before.ChangedUntracked(after))
	}
	if _, err := os.Stat(filepath.Join(root, ".vise", "run.lock")); err == nil {
		t.Fatal("doctor created .vise/run.lock")
	}
}

// Declaring the code under test as a probe dep would make every refactor of it
// a harness error, which disables the gate the operator came here to set up.
// A check that cannot tell the harness from the subject must not guess, so it
// fires only on a file that refers to $VISE_TMP — the one thing nothing but a
// probe wrapper knows about.
func TestDoctorDoesNotAskForTheCodeUnderTestToBeDeclared(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "AGENTS.md", AgentContract)
	writeTestFile(t, root, "mytool", "#!/bin/sh\nprintf 'mytool 1.0\\n'\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"git --version\"]\n[[probe]]\nid = \"cli\"\nrun = \"./mytool\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "subject")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	testGit(t, root, "add", "vise.lock", ".vise/blobs")
	testGit(t, root, "commit", "-qm", "baseline")

	report := Doctor(root)
	for _, finding := range report.Findings {
		if finding.Check == "declared-inputs" {
			t.Fatalf("doctor asked for the code under test to be declared: %s", finding.Detail)
		}
	}
	if !report.Ready {
		t.Fatalf("doctor found %v", doctorChecks(report))
	}
}

// A finding's check is a stable identifier a script can act on without reading
// prose, so the set is part of the contract and SPEC names it.
func TestDoctorFindingsUseStableCheckNames(t *testing.T) {
	// The registry, not a copy of it. A second hand-copied list is a second
	// thing that goes stale, which is the defect this test is guarding.
	known := DoctorChecks

	// A repository with a broken manifest, and one with every other gap open.
	broken := testGitRepo(t)
	writeTestFile(t, broken, "vise.toml", "not = [valid\n")
	bare := testGitRepo(t)
	writeTestFile(t, bare, ".gitignore", "node_modules/\n")
	writeTestFile(t, bare, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\nenv = { CACHE = \"/opt/elsewhere\" }\n")

	seen := map[string]bool{}
	for _, root := range []string{broken, bare} {
		report := Doctor(root)
		if len(report.Findings) == 0 {
			t.Fatalf("no findings for %s", root)
		}
		for _, finding := range report.Findings {
			if !slices.Contains(known, finding.Check) {
				t.Errorf("check %q is not one of the names SPEC documents: %v", finding.Check, known)
			}
			seen[finding.Check] = true
		}
	}
	if !seen["manifest"] {
		t.Error("a manifest that will not parse produced no manifest finding")
	}
}

// The work-tree snapshot hashes every untracked, unignored file twice per
// probe run. A checkout with a dependency directory nobody ignored turns that
// into a gate that is slow for no visible reason, and slow is the failure mode
// nobody reports as a bug.
func TestDoctorNamesAnExpensiveWorkTreeSnapshot(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"true\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
	for i := 0; i < snapshotFileBudget+1; i++ {
		writeTestFile(t, root, filepath.Join("deps", "pkg"+strconv.Itoa(i)+".txt"), "x")
	}

	report := Doctor(root)
	var found *DoctorFinding
	for i := range report.Findings {
		if report.Findings[i].Check == "snapshot-cost" {
			found = &report.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("no snapshot-cost finding; got %v", doctorChecks(report))
	}
	if !strings.Contains(found.Remedy, ".gitignore") {
		t.Fatalf("remedy %q does not name the fix", found.Remedy)
	}

	// Ignored is the answer, so an ignored tree must not be counted.
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\ndeps/\n")
	for _, finding := range Doctor(root).Findings {
		if finding.Check == "snapshot-cost" {
			t.Fatalf("an ignored tree was still counted: %s", finding.Detail)
		}
	}
}

// Three ways doctor said "ready" about a repository that was not. All found by
// a cold audit while the gate was green.
func TestDoctorIsNotSatisfiedByAppearances(t *testing.T) {
	t.Run("a staged lockfile is not a committed one", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, "AGENTS.md", AgentContract)
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"git --version\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
		writeTestFile(t, root, "vise.lock", "{}")
		testGit(t, root, "add", "vise.lock")

		var found bool
		for _, finding := range Doctor(root).Findings {
			if finding.Check == "baseline-committed" {
				found = true
			}
		}
		if !found {
			t.Fatal("a lockfile that is staged but never committed passed as committed; a fresh clone has no baseline")
		}
	})

	t.Run("a blank fingerprint command records nothing", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")

		var detail string
		for _, finding := range Doctor(root).Findings {
			if finding.Check == "env-fingerprint" {
				detail = finding.Detail
			}
		}
		if !strings.Contains(detail, "blank") {
			t.Fatalf("a blank fingerprint command passed as a declared one: %q", detail)
		}
	})

	t.Run("an empty agent contract is not a contract", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, "AGENTS.md", "")
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"git --version\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")

		var detail string
		for _, finding := range Doctor(root).Findings {
			if finding.Check == "agent-contract" {
				detail = finding.Detail
			}
		}
		if !strings.Contains(detail, "empty") {
			t.Fatalf("an empty AGENTS.md passed as a written contract: %q", detail)
		}
	})
}

// A lockfile that is committed but is not the one on disk means a fresh clone
// gates against a different baseline. Merely existing in HEAD used to satisfy
// the check.
func TestDoctorNoticesTheCommittedBaselineIsNotTheOneHere(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "AGENTS.md", AgentContract)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"git --version\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
	writeTestFile(t, root, "vise.lock", "{\"v\":1}")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "baseline")

	if findings := Doctor(root).Findings; len(findings) != 0 {
		t.Fatalf("a committed baseline produced findings: %#v", findings)
	}

	// The working tree moves on without a commit.
	writeTestFile(t, root, "vise.lock", "{\"v\":1,\"probes\":{}}")
	var detail string
	for _, finding := range Doctor(root).Findings {
		if finding.Check == "baseline-committed" {
			detail = finding.Detail
		}
	}
	if !strings.Contains(detail, "not the one in the working tree") {
		t.Fatalf("an uncommitted change to the baseline went unreported: %q", detail)
	}
}

// The blob check asked the directory, so an uncommitted orphan produced a
// finding while a referenced blob that was never committed produced none —
// the wrong way round, since the missing one is what a reviewer in a fresh
// clone finds they cannot render.
func TestDoctorAsksAboutTheBlobsTheLockfileReferences(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "AGENTS.md", AgentContract)
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"git --version\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "harness")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}
	testGit(t, root, "add", "vise.lock", ".vise/blobs")
	testGit(t, root, "commit", "-qm", "baseline")
	if findings := Doctor(root).Findings; len(findings) != 0 {
		t.Fatalf("a fully committed baseline produced findings: %#v", findings)
	}

	// An orphan nobody references is harmless and must not be a finding.
	writeTestFile(t, root, filepath.Join(".vise", "blobs", strings.Repeat("a", 64)), "orphan")
	for _, finding := range Doctor(root).Findings {
		if finding.Check == "baseline-committed" {
			t.Fatalf("an uncommitted orphan blob was reported: %s", finding.Detail)
		}
	}
}
