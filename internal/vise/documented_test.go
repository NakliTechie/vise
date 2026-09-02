package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Twice in one night a surface grew and its documentation did not: a command
// added without a line in the README's table, a check added while the prose
// still said six. Counting in prose is not worth policing, but "this exists
// and no document mentions it" is, and it is the half that misleads a reader.
func TestEveryDoctorCheckIsDocumented(t *testing.T) {
	spec := sectionOf(t, readRepositoryFile(t, "SPEC.md"), "**`vise doctor`**")
	// Every name Doctor can emit. Kept here rather than derived, so adding a
	// check means deciding where it is documented rather than discovering
	// later that nobody said.
	checks := []string{
		"env-fingerprint",
		"portable-paths",
		"declared-inputs",
		"baseline-committed",
		"local-state-ignored",
		"agent-contract",
		"snapshot-cost",
		"manifest",
		"git-work-tree",
	}
	for _, check := range checks {
		if !strings.Contains(spec, check) {
			t.Errorf("doctor can report %q and SPEC.md never mentions it", check)
		}
	}
}

// The README's command table is where someone decides whether vise does the
// thing they came for. A command missing from it does not exist as far as they
// are concerned.
//
// The search is scoped to the table itself. Searching the whole file passed
// with the row deleted, because the command was named in prose further down —
// a test that cannot fail is worse than no test, since it is also believed.
func TestEveryCommandIsInTheReadmeTable(t *testing.T) {
	table := sectionOf(t, readRepositoryFile(t, "README.md"), "## Commands")
	for _, command := range []string{"init", "record", "verify", "gate", "run", "status", "doctor", "version"} {
		if !strings.Contains(table, "`vise "+command+"`") && !strings.Contains(table, "`vise "+command+" ") {
			t.Errorf("vise %s is a command and the README table does not list it", command)
		}
	}
}

// sectionOf returns the text under a heading, up to the next heading of the
// same level.
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
