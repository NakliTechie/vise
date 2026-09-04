package vise

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The whole agent contract rests on next.action being a closed set: an agent
// switches on it, and a seventh value makes every caller fall through to its
// default, leaving the agent with no defined next move. The constants make a
// typo a compile error; this catches the literal that goes in beside them.
func TestEveryNextActionIsInTheClosedVocabulary(t *testing.T) {
	// Every emitted action now goes through a Next value or a named constant,
	// so one pattern covers the whole surface. The helper that used to take an
	// action as a parameter takes none: it always means human, so there is no
	// action literal at those seven call sites for a scan to miss.
	actionLiteral := regexp.MustCompile(`Action:\s*"([a-z_]+)"`)

	roots := []string{filepath.Join("..", "vise"), filepath.Join("..", "cli")}
	checked := 0
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
			checked++
			for _, match := range actionLiteral.FindAllStringSubmatch(string(data), -1) {
				if !slices.Contains(KnownNextActions, match[1]) {
					t.Errorf("%s/%s uses next action %q, which is not in the closed vocabulary %v",
						root, name, match[1], KnownNextActions)
					continue
				}
				t.Errorf("%s/%s writes %q as a literal; use the constant so a typo cannot compile",
					root, name, match[1])
			}
		}
	}
	if checked == 0 {
		t.Fatal("scanned no source files; the test is not looking where it thinks it is")
	}
}

// Every action in the vocabulary must be reachable, or the table in the README
// promises the agent a branch that never fires.
func TestEveryDeclaredNextActionIsUsed(t *testing.T) {
	constants := map[string]string{
		NextProceed:       "NextProceed",
		NextRevert:        "NextRevert",
		NextFixProbe:      "NextFixProbe",
		NextHuman:         "NextHuman",
		NextRecordFirst:   "NextRecordFirst",
		NextQuarantineAck: "NextQuarantineAck",
		NextFixInvocation: "NextFixInvocation",
	}
	if len(constants) != len(KnownNextActions) {
		t.Fatalf("KnownNextActions has %d entries, the constants have %d", len(KnownNextActions), len(constants))
	}
	// Read every non-test source file, including types.go: its usages count,
	// and the declaration lines are removed rather than the whole file, or the
	// only use of an action defined and emitted there would be invisible.
	var source strings.Builder
	for _, root := range []string{filepath.Join("..", "vise"), filepath.Join("..", "cli")} {
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
			for _, line := range strings.Split(string(data), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "Next") && strings.Contains(trimmed, "= \"") {
					continue // a const declaration
				}
				if strings.HasPrefix(trimmed, "var KnownNextActions") {
					continue
				}
				source.WriteString(line)
				source.WriteString("\n")
			}
		}
	}
	for _, identifier := range constants {
		if !strings.Contains(source.String(), identifier) {
			t.Errorf("%s is declared but never emitted; the exit table promises a branch that never fires", identifier)
		}
	}
}
