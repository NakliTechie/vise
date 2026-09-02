package vise

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Twice in one night a surface grew and its documentation did not: a command
// added without a line in the README's table, a check added while the prose
// still said six. Counting in prose is not worth policing, but "this exists
// and no document mentions it" is, and it is the half that misleads a reader.
func TestEveryDoctorCheckIsDocumented(t *testing.T) {
	spec := sectionOf(t, readRepositoryFile(t, "SPEC.md"), "**`vise doctor`**")
	for _, check := range DoctorChecks {
		if !strings.Contains(spec, check) {
			t.Errorf("doctor can report %q and SPEC.md never mentions it", check)
		}
	}
}

// Every check name Doctor emits must be in the registry the guard reads.
// Without this, a check added to the code and not to the list leaves the guard
// passing while the new surface is undocumented — which is the failure the
// guard exists to prevent, reproduced one level up.
func TestDoctorChecksRegistryMatchesWhatDoctorEmits(t *testing.T) {
	emitted := map[string]bool{}
	collect := func(report DoctorReport) {
		for _, finding := range report.Findings {
			emitted[finding.Check] = true
		}
	}

	// A repository with every static gap open at once.
	bare := testGitRepo(t)
	writeTestFile(t, bare, ".gitignore", "node_modules/\n")
	writeTestFile(t, bare, "wrapper.sh", "#!/bin/sh\nprintf %s \"$VISE_TMP\"\n")
	writeTestFile(t, bare, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"sh wrapper.sh\"\nenv = { CACHE = \"/opt/elsewhere\" }\n")
	for i := 0; i < snapshotFileBudget+1; i++ {
		writeTestFile(t, bare, filepath.Join("deps", "pkg"+strconv.Itoa(i)+".txt"), "x")
	}
	collect(Doctor(bare))

	broken := testGitRepo(t)
	writeTestFile(t, broken, "vise.toml", "not = [valid\n")
	collect(Doctor(broken))

	// git-work-tree is emitted by the CLI, which owns the no-repository path.
	emitted["git-work-tree"] = true

	for check := range emitted {
		if !slices.Contains(DoctorChecks, check) {
			t.Errorf("Doctor emitted %q and DoctorChecks does not list it, so nothing requires it to be documented", check)
		}
	}
	for _, check := range DoctorChecks {
		if !emitted[check] {
			t.Logf("note: %q is registered and was not emitted by these fixtures", check)
		}
	}
}

// Every next.action an agent can receive has to appear in the exit table it
// branches on, or the contract documents a move the agent is never told about.
// Scoped to the table for the same reason as above.
func TestEveryNextActionIsInTheReadmeExitTable(t *testing.T) {
	table := sectionOf(t, readRepositoryFile(t, "README.md"), "## Built for the agent in the driver's seat")
	for _, action := range KnownNextActions {
		if !strings.Contains(table, action) {
			t.Errorf("next.action %q is emitted and the README exit table never names it", action)
		}
	}
}

func readRepositoryFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Skipf("%s unavailable: %v", name, err)
	}
	return string(data)
}

// sectionOf returns the text under a heading, up to the next heading of the
// same level. Scoping matters: the first version of these guards searched the
// whole document and passed with the row it protected deleted, because the
// word appeared in prose further down.
func sectionOf(t *testing.T, document, heading string) string {
	t.Helper()
	start := strings.Index(document, heading)
	if start < 0 {
		t.Fatalf("%q is not in the document", heading)
	}
	rest := document[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}
