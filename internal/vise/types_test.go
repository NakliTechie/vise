package vise

import "testing"

// A green verdict carrying a failure is the one thing this tool exists not to
// do, and it could produce one.
//
// AddFailure counted only four literal class strings and had no default arm.
// A failure whose class was misspelled or empty went into Failures and
// incremented nothing; Finalize then derived pass as declared minus the four
// known classes, counted it as a pass, fell through every case, and returned
// exit 0, verdict green, next.action proceed — with the failure still in the
// JSON object beside it. Reproduced before the fix: exit=0 verdict="green"
// pass=1 failures=1 next=proceed.
//
// An unknown class is harness now, which is the fail-closed answer. A class
// vise does not recognise means vise is broken, and that is exactly what a
// harness verdict says.
func TestAFailureViseDoesNotRecogniseIsNeverGreen(t *testing.T) {
	for _, class := range []string{"", "behaviour", "Behavior", "regression", "unknown"} {
		t.Run("class "+class, func(t *testing.T) {
			outcome := NewOutcome("gate")
			outcome.Counts.Declared = 1
			outcome.AddFailure("p", Failure{Class: class, Detail: "something failed"})
			outcome.Finalize()

			if outcome.Exit == ExitOK || outcome.Verdict == "green" {
				t.Errorf("a failure of class %q produced exit %d verdict %q", class, outcome.Exit, outcome.Verdict)
			}
			if len(outcome.Failures) == 0 {
				t.Fatal("the failure disappeared")
			}
			if outcome.Counts.Pass != 0 {
				t.Errorf("the failure was counted as a pass: %#v", outcome.Counts)
			}
		})
	}
}

// And the counts still add up for the classes it does recognise, including
// when one failure replaces another under the same id.
func TestReplacingAFailureMovesItBetweenCounts(t *testing.T) {
	outcome := NewOutcome("gate")
	outcome.Counts.Declared = 1
	outcome.AddFailure("p", Failure{Class: "behavior"})
	outcome.AddFailure("p", Failure{Class: "flake"})
	if outcome.Counts.Behavior != 0 || outcome.Counts.Flaky != 1 {
		t.Fatalf("counts = %#v, want behavior 0 and flaky 1", outcome.Counts)
	}
	outcome.Finalize()
	if outcome.Exit != ExitIndeterminate {
		t.Errorf("exit %d, want %d", outcome.Exit, ExitIndeterminate)
	}
}
