package vise

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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
	if _, err := os.Stat(filepath.Join(root, ".vise")); err == nil {
		t.Fatal("doctor created vise state in a repository that never ran vise")
	}
	// The work-tree snapshot above deliberately excludes vise's own local
	// state, so it cannot see a write to the journal or the run lock. Without
	// this, making doctor append a journal event left the test green.
	// Also against a repository that has recorded: a write guarded on ".vise
	// already exists" is invisible to the virgin case above, and that is the
	// case a real operator runs doctor in.
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

	digestBefore, err := evaluatorStateDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	Doctor(root)
	digestAfter, err := evaluatorStateDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if digestBefore != digestAfter {
		t.Fatal("doctor wrote to the manifest, lockfile, blobs, or journal")
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
	// A literal count, not one derived from the budget: a fixture sized from
	// the constant under test moves with it, so lowering the budget to 1 left
	// this green while proving nothing.
	for i := 0; i < 2001; i++ {
		writeTestFile(t, root, filepath.Join("deps", "pkg"+strconv.Itoa(i)+".txt"), "x")
	}
	if snapshotFileBudget != 2000 {
		t.Fatalf("snapshotFileBudget is %d; this fixture is sized for 2000 and must be resized deliberately", snapshotFileBudget)
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

	// A blank fingerprint command used to reach doctor as an env-fingerprint
	// finding. The manifest refuses it outright now — it runs through the same
	// shell a probe does, and a probe's `run` has always been refused when
	// blank — so doctor reports it under `manifest`, which replaces every other
	// check when vise.toml will not load. Earlier and harder, with the index
	// named. The point of the test is unchanged: a blank command must not pass
	// as a declared one.
	t.Run("a blank fingerprint command is refused, not recorded", func(t *testing.T) {
		root := testGitRepo(t)
		writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n")

		var detail string
		for _, finding := range Doctor(root).Findings {
			if finding.Check == "manifest" {
				detail = finding.Detail
			}
		}
		if !strings.Contains(detail, "fingerprint[0]") || !strings.Contains(detail, "empty") {
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
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"git --version\"]\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n[[probe]]\nid = \"q\"\nrun = \"printf q\"\n")
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

	// And a blob the lockfile does reference, uncommitted, must be reported —
	// without this the check could examine nothing at all and stay green.
	// The orphan written above is untracked, so remove it before asking git to
	// unstage the tracked ones.
	if err := os.Remove(filepath.Join(root, ".vise", "blobs", strings.Repeat("a", 64))); err != nil {
		t.Fatal(err)
	}

	// Uncommit all but the first blob. A check that inspects only one of them
	// passed when the fixture had a single blob; this repository records two
	// probes, so the survivor cannot be the one that is examined.
	blobNames, err := os.ReadDir(filepath.Join(root, ".vise", "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(blobNames) < 2 {
		t.Fatalf("fixture has %d blobs; this test needs at least two", len(blobNames))
	}
	for _, entry := range blobNames[1:] {
		testGit(t, root, "rm", "-q", "--cached", filepath.Join(".vise", "blobs", entry.Name()))
	}
	testGit(t, root, "commit", "-qm", "drop most of the blobs")
	var detail string
	for _, finding := range Doctor(root).Findings {
		if finding.Check == "baseline-committed" {
			detail = finding.Detail
		}
	}
	if !strings.Contains(detail, "not committed") {
		t.Fatalf("uncommitted referenced blobs went unreported: %q", detail)
	}
}

// A declared artifact that Git tracks stops the gate dead: vise deletes
// artifacts before every run and refuses to delete a tracked file. The trap is
// an ordinary `git add -A` after a probe has produced one — nothing about it
// looks wrong, the file is real output, and the failure arrives later in a
// message about the manifest. Found when an agent handed a repository set up
// exactly that way refused to start, correctly.
func TestDoctorNoticesADeclaredArtifactSomebodyCommitted(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "AGENTS.md", AgentContract)
	// Two probes, two artifacts each, and only the last one committed: a check
	// that inspects the first probe, or the first artifact of each, passes on
	// a single-artifact fixture while seeing nothing.
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[env]\nfingerprint = [\"git --version\"]\n"+
		"[[probe]]\nid = \"p\"\nrun = \"mkdir -p out && printf a > out/a.txt && printf b > out/b.txt\"\nfiles = [\"out/a.txt\", \"out/b.txt\"]\n"+
		"[[probe]]\nid = \"q\"\nrun = \"mkdir -p out && printf c > out/c.txt && printf d > out/d.txt\"\nfiles = [\"out/c.txt\", \"out/d.txt\"]\n")
	writeTestFile(t, root, "vise.lock", "{\"v\":1}")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "harness")

	// Clean so far: the artifact does not exist yet.
	for _, finding := range Doctor(root).Findings {
		if finding.Check == "tracked-artifacts" {
			t.Fatalf("reported before the artifact existed: %s", finding.Detail)
		}
	}

	// Only the very last declared artifact is committed.
	writeTestFile(t, root, filepath.Join("out", "d.txt"), "d")
	testGit(t, root, "add", "out/d.txt")
	testGit(t, root, "commit", "-qm", "commit one artifact")

	var detail, remedy string
	for _, finding := range Doctor(root).Findings {
		if finding.Check == "tracked-artifacts" {
			detail, remedy = finding.Detail, finding.Remedy
		}
	}
	if !strings.Contains(detail, "out/d.txt") {
		t.Fatalf("a committed declared artifact went unreported: %q", detail)
	}
	if !strings.Contains(remedy, "git rm --cached") {
		t.Fatalf("the remedy does not say how to undo it: %q", remedy)
	}
}

// doctor exists to predict what the gate will find. checkPortablePaths flagged
// a machine-local path in a probe and skipped metrics entirely — but a metric
// runs the same shell (RunMetric), so an absolute path in a metric passed
// doctor and failed in the sandbox. The worst kind of doctor bug: a false clean.
func TestDoctorFlagsAMachineLocalPathInAMetricToo(t *testing.T) {
	manifest := Manifest{
		Vise:   ViseSettings{Version: 1},
		Stubs:  StubSettings{TZ: "UTC", Lang: "C", Seed: "1729", Network: "declared-off"},
		Probes: []Probe{{ID: "p", Run: "printf ok", Timeout: 30}},
		Metrics: []Metric{{
			ID: "size", Run: "/opt/analyzer/count .", VersionCmd: "/opt/analyzer/count --version",
			Direction: "down", Enforce: "no-regress", Timeout: 30,
			Env: map[string]string{"CACHE": "/root/cache"},
		}},
	}
	findings := checkPortablePaths(manifest)
	var joined string
	for _, f := range findings {
		joined += f.Detail + "\n"
	}
	for _, want := range []string{"/opt/analyzer/count", "metric size", "/root/cache"} {
		if !strings.Contains(joined, want) {
			t.Errorf("doctor did not flag %q in a metric:\n%s", want, joined)
		}
	}
}

// The HOME check was a raw substring test, wrong at both ends: it missed
// ${HOME}/x and it flagged $HOMEBREW.
func TestDoctorNamesTheHomeVariableAndNotItsLookalikes(t *testing.T) {
	flags := func(value string) bool {
		m := Manifest{
			Vise:   ViseSettings{Version: 1},
			Stubs:  StubSettings{TZ: "UTC", Lang: "C", Seed: "1729", Network: "declared-off"},
			Probes: []Probe{{ID: "p", Run: value, Timeout: 30}},
		}
		return len(checkPortablePaths(m)) > 0
	}
	for _, home := range []string{"$HOME/go", "${HOME}/go", "cat $HOME"} {
		if !flags(home) {
			t.Errorf("%q references HOME and doctor passed it", home)
		}
	}
	for _, notHome := range []string{"$HOMEBREW/bin", "$HOMEPAGE", "tool --home=x"} {
		if flags(notHome) {
			t.Errorf("%q does not reference HOME and doctor flagged it", notHome)
		}
	}
}

// doctor must stay bounded and must not read outside the checkout. checkBaseline
// Committed read vise.lock with os.ReadFile, which follows a symlink and blocks
// on a fifo — so a vise.lock that was a fifo could hang doctor, and one that was
// a symlink would have doctor read its target off the repository. The gate loads
// the same file through readRegularFile precisely to refuse both. A non-regular
// lock is a finding, not a clean pass or a hang.
func TestDoctorRefusesANonRegularLockfileInsteadOfHanging(t *testing.T) {
	if _, err := exec.LookPath("mkfifo"); err != nil {
		t.Skip("mkfifo unavailable")
	}
	root := testGitRepo(t)
	lock := filepath.Join(root, "vise.lock")
	if out, err := exec.Command("mkfifo", lock).CombinedOutput(); err != nil {
		t.Skipf("cannot create a fifo here: %v\n%s", err, out)
	}

	// A bounded call: if checkBaselineCommitted read the fifo it would block
	// here forever, so reaching the assertion at all is half the test.
	done := make(chan []DoctorFinding, 1)
	go func() { done <- checkBaselineCommitted(root) }()
	select {
	case findings := <-done:
		var joined string
		for _, f := range findings {
			joined += f.Detail + "\n"
		}
		if !strings.Contains(joined, "not a regular file") {
			t.Errorf("a fifo vise.lock was not reported as non-regular:\n%s", joined)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("checkBaselineCommitted blocked on a fifo vise.lock")
	}
}

// A git failure inspecting the repository is not a clean pass — it is doctor
// unable to answer, and reporting ready would tell an operator the repository
// is fit when it is only uninspected. checkTrackedArtifacts and checkSnapshotCost
// both swallowed a git error into nil; a directory that is not a git repository
// makes those git calls fail.
func TestDoctorReportsAGitInspectionFailureInsteadOfPassing(t *testing.T) {
	notARepo := t.TempDir() // deliberately no git init

	manifest := Manifest{Probes: []Probe{{ID: "p", Run: "printf ok", Timeout: 30, Files: []string{"out.txt"}}}}
	artifacts := checkTrackedArtifacts(notARepo, manifest)
	if len(artifacts) == 0 || !strings.Contains(artifacts[0].Detail, "could not inspect") {
		t.Errorf("a git failure inspecting artifacts was reported clean: %#v", artifacts)
	}

	cost := checkSnapshotCost(notARepo)
	if len(cost) == 0 || !strings.Contains(cost[0].Detail, "could not list") {
		t.Errorf("a git failure sizing the snapshot was reported clean: %#v", cost)
	}
}

// The no-lockfile remedy said "run vise record", which record refuses on a
// repository with no commits (resolveHead). On a fresh git init the bare remedy
// fails and the finding persists, so it must name the commit step.
func TestTheNoBaselineRemedyIsRunnableOnAFreshRepo(t *testing.T) {
	fresh := t.TempDir()
	testGit(t, fresh, "init", "-q")
	testGit(t, fresh, "config", "user.email", "doctor-tests@example.invalid")
	testGit(t, fresh, "config", "user.name", "doctor tests")

	var found *DoctorFinding
	for i := range checkBaselineCommitted(fresh) {
		f := checkBaselineCommitted(fresh)[i]
		if f.Check == "baseline-committed" {
			found = &f
		}
	}
	if found == nil {
		t.Fatal("no baseline finding on a fresh repo")
	}
	if !strings.Contains(found.Remedy, "commit the harness first") {
		t.Errorf("the remedy does not account for a no-commits repo: %q", found.Remedy)
	}

	// And a repository with a commit still gets the plain remedy.
	withCommit := testGitRepo(t)
	for _, f := range checkBaselineCommitted(withCommit) {
		if f.Check == "baseline-committed" && strings.Contains(f.Remedy, "commit the harness first") {
			t.Errorf("a repo with commits got the no-commits remedy: %q", f.Remedy)
		}
	}
}

// The remedy for an empty AGENTS.md said "run vise init", but init will not
// overwrite a file that is already there, so it reports nothing to write and
// leaves the empty file. Following the remedy fixed nothing. The remedy names
// the removal step now.
func TestTheEmptyContractRemedyIsRunnable(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "AGENTS.md", "")

	var found *DoctorFinding
	for _, f := range checkAgentContract(root) {
		if f.Check == "agent-contract" {
			found = &f
		}
	}
	if found == nil {
		t.Fatal("an empty AGENTS.md was not flagged")
	}
	// The old remedy said init writes one; it does not, so the remedy must not
	// promise that.
	if !strings.Contains(found.Remedy, "remove the empty") {
		t.Errorf("the remedy does not name the removal init requires: %q", found.Remedy)
	}
}

// A manifest that will not parse hid the four checks that inspect only the
// checkout, so an uncommitted baseline, unignored state, a missing contract, or
// an oversized tree surfaced only after the manifest was fixed and doctor run
// again. Those four run now, beside the manifest finding.
func TestAManifestParseErrorDoesNotHideTheCheckoutChecks(t *testing.T) {
	root := testGitRepo(t)
	// A manifest that will not parse, and a repository otherwise unprepared: no
	// AGENTS.md, no .gitignore for vise state, no baseline.
	writeTestFile(t, root, "vise.toml", "this is not valid toml = = =\n")

	report := Doctor(root)
	checks := map[string]bool{}
	for _, f := range report.Findings {
		checks[f.Check] = true
	}
	if !checks["manifest"] {
		t.Fatal("a parse error did not produce a manifest finding")
	}
	for _, want := range []string{"baseline-committed", "agent-contract"} {
		if !checks[want] {
			t.Errorf("the %s finding was hidden behind the manifest parse error", want)
		}
	}
	// The manifest-reading checks must NOT run — they have no manifest to read.
	for _, mustNot := range []string{"portable-paths", "env-fingerprint", "declared-inputs"} {
		if checks[mustNot] {
			t.Errorf("%s ran without a parsed manifest", mustNot)
		}
	}
}

// A referenced hash that will not parse was silently skipped by the blob audit,
// so doctor reported ready for a lockfile the gate refuses (validateLockfileHashes).
// A malformed hash is a corrupt baseline, and doctor should say so rather than
// pass it.
func TestDoctorReportsAMalformedReferencedHash(t *testing.T) {
	root := testGitRepo(t)
	// A lockfile that parses as JSON but references a hash that is not a valid
	// sha256: reference.
	writeTestFile(t, root, "vise.lock", `{"v":1,"probes":{"p":{"exit":0,"stdout":"not-a-valid-hash","recorded_commit":"abc"}}}`)

	var found bool
	for _, f := range checkBaselineCommitted(root) {
		if strings.Contains(f.Detail, "malformed") {
			found = true
		}
	}
	if !found {
		t.Error("a malformed referenced hash was not reported")
	}
}

// A committed vise.lock that is present but not valid JSON is a corrupt
// baseline the gate refuses (LoadLockfile), and doctor's baseline check
// silently passed it — the early return on an unparseable lock reported ready.
func TestDoctorFlagsACorruptLockfile(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, "vise.lock", "this is not json\n")
	var found bool
	for _, f := range checkBaselineCommitted(root) {
		if strings.Contains(f.Detail, "not valid JSON") {
			found = true
		}
	}
	if !found {
		t.Error("a corrupt vise.lock was reported clean")
	}
}

// portable-paths must see a machine-local path embedded after KEY= or a -I flag,
// and a parent-relative path, without false-flagging a URL.
func TestPortablePathsSeesEmbeddedAndRelativePaths(t *testing.T) {
	flags := func(run string) bool {
		m := Manifest{
			Vise: ViseSettings{Version: 1}, Stubs: StubSettings{Network: "declared-off"},
			Probes: []Probe{{ID: "p", Run: run, Timeout: 30}},
		}
		return len(checkPortablePaths(m)) > 0
	}
	for _, local := range []string{
		"env GOMODCACHE=/home/op/cache go test",
		"cc -I/opt/include main.c",
		"sh ../shared/tool.sh",
	} {
		if !flags(local) {
			t.Errorf("%q names a machine-local path and doctor passed it", local)
		}
	}
	for _, portable := range []string{
		"curl https://example.com/data",
		"./bin/tool --format=json",
		"go build ./cmd/app",
	} {
		if flags(portable) {
			t.Errorf("%q is portable and doctor flagged it", portable)
		}
	}
}

// The no-lockfile remedy said "run vise record", which record refuses on a
// dirty tree — so with an uncommitted harness the operator follows it verbatim
// and gets a working-tree refusal. It names the commit step when the tree is
// dirty.
func TestTheNoBaselineRemedyAccountsForADirtyTree(t *testing.T) {
	root := testGitRepo(t) // has one commit, clean
	// Add an uncommitted harness file: commits exist, tree is dirty, no lock.
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n")

	var remedy string
	for _, f := range checkBaselineCommitted(root) {
		if f.Check == "baseline-committed" {
			remedy = f.Remedy
		}
	}
	if remedy == "" {
		t.Fatal("no baseline finding")
	}
	if !strings.Contains(remedy, "commit or stash") {
		t.Errorf("the remedy ignores the dirty tree record refuses: %q", remedy)
	}
}
