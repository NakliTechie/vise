package vise

import (
	"os"
	"path/filepath"
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

// Every finding a registered check produces must carry the name it was
// registered under. Renaming the emitted string used to leave the
// documentation guard checking a name nothing emitted any more, and passing.
func TestEachDoctorCheckEmitsTheNameItIsRegisteredUnder(t *testing.T) {
	// A repository with as many gaps open at once as static checks can see.
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", "node_modules/\n")
	writeTestFile(t, root, "wrapper.sh", "#!/bin/sh\nprintf %s \"$VISE_TMP\"\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"sh wrapper.sh\"\nfiles = [\"out/result.txt\"]\nenv = { CACHE = \"/opt/elsewhere\" }\n")
	// A declared artifact somebody committed, the way `git add -A` does.
	writeTestFile(t, root, filepath.Join("out", "result.txt"), "produced")
	testGit(t, root, "add", "out/result.txt")
	testGit(t, root, "commit", "-qm", "commit the artifact")
	for i := 0; i < 2001; i++ {
		writeTestFile(t, root, filepath.Join("deps", "pkg"+strconv.Itoa(i)+".txt"), "x")
	}
	manifest, _, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, check := range doctorRegistry {
		findings := check.Run(root, manifest)
		if len(findings) == 0 {
			t.Errorf("%s produced no finding in a repository with every gap open, so nothing here proves it runs", check.Name)
			continue
		}
		for _, finding := range findings {
			if finding.Check != check.Name {
				t.Errorf("the %s check emitted a finding named %q", check.Name, finding.Check)
			}
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

// The counts stated in prose were wrong three times tonight: the harness
// features, doctor's checks twice. A number in a document is a claim like any
// other, and this one is cheap to check.
func TestTheStatedCheckCountMatchesTheRegistry(t *testing.T) {
	// The registry plus the two names it cannot hold — "manifest" and
	// "git-work-tree" — is what DoctorChecks reports.
	registered := len(doctorRegistry)

	words := map[int]string{
		6: "six", 7: "seven", 8: "eight", 9: "nine", 10: "ten", 11: "eleven", 12: "twelve",
	}
	if _, ok := words[registered]; !ok {
		t.Skipf("no spelled-out form for %d checks; extend the table", registered)
	}

	for _, name := range []string{"README.md", "GUIDE.md", "SPEC.md"} {
		document := strings.ToLower(readRepositoryFile(t, name))
		var mentions int
		for count, word := range words {
			for _, phrase := range []string{word + " checks", word + " static checks"} {
				if strings.Contains(document, phrase) {
					mentions++
					if count != registered {
						t.Errorf("%s says %q; the registry has %d", name, phrase, registered)
					}
				}
			}
		}
		if mentions == 0 {
			t.Logf("note: %s states no check count", name)
		}
	}
}
