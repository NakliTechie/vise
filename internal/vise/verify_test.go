package vise

import (
	"fmt"
	"testing"
)

// A probe may declare several artifacts, and the comparison walks them in a
// map. Nothing checked that it walks all of them: a version that compared one
// and stopped passed the entire suite. Every existing test with artifacts had
// exactly one, which is the fixture shape that hides a loop-over-first — the
// fourth time that shape has hidden a mutation in this project.
//
// Six artifacts, one differing at a time. Go randomises map order, so a
// compare-one-and-stop version escapes a given case only when it happens to
// pick the differing key: one time in six, and it has to escape all six.
func TestEveryDeclaredArtifactIsCompared(t *testing.T) {
	const count = 6
	expected := ProbeLock{Exit: 0, Stdout: "sha256:out", Stderr: "sha256:err", Files: map[string]string{}}
	for i := 0; i < count; i++ {
		expected.Files[fmt.Sprintf("out/%d.txt", i)] = fmt.Sprintf("sha256:artifact-%d", i)
	}

	matching := func() RunResult {
		run := RunResult{Exit: 0, Stdout: Capture{Hash: "sha256:out"}, Stderr: Capture{Hash: "sha256:err"}, Files: map[string]Capture{}}
		for path, hash := range expected.Files {
			run.Files[path] = Capture{Hash: hash}
		}
		return run
	}
	if !RunMatchesLock(matching(), expected) {
		t.Fatal("an identical run did not match its own lock")
	}

	for i := 0; i < count; i++ {
		path := fmt.Sprintf("out/%d.txt", i)
		t.Run(path, func(t *testing.T) {
			run := matching()
			run.Files[path] = Capture{Hash: "sha256:changed"}
			if RunMatchesLock(run, expected) {
				t.Errorf("%s changed and the run still matched the lock", path)
			}
		})
	}
}
