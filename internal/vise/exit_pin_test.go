package vise

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// An explicit exit is a complete expectation: no spec files are required,
// but both undeclared streams must still be empty. Exercise the disk round
// trip, not just manifest validation, which already accepted these pins.
func TestExitOnlyPinSurvivesRecordAndAcceptance(t *testing.T) {
	for _, exit := range []int{0, 42, 255} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, "vise.toml", fmt.Sprintf("[vise]\nversion = 1\n[[probe]]\nid = \"exit-only\"\nrun = \"sh program.sh\"\nexpect.exit = %d\n", exit))
			writeTestFile(t, root, "program.sh", fmt.Sprintf("printf unexpected\nexit %d\n", exit))
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "exit-only pin")
			manifest, data, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			recordPinRepo(t, root, manifest, data)
			lock, _, err := LoadLockfile(root)
			if err != nil {
				t.Fatalf("record wrote an unreadable exit-only pin: %v", err)
			}
			if pin := lock.Probes["exit-only"].Pin; pin == nil || len(pin.Spec) != 0 || pin.AcceptedCommit != nil {
				t.Fatalf("unmet exit-only pin = %#v", pin)
			}
			if got := Verify(root, manifest, data, VerifyOptions{}).Outcome; got.Exit != ExitUnmet {
				t.Fatalf("undeclared stdout must be unmet: %#v", got)
			}
			writeTestFile(t, root, "program.sh", fmt.Sprintf("exit %d\n", exit))
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "meet the exit-only pin")
			if got := Verify(root, manifest, data, VerifyOptions{}).Outcome; got.Exit != ExitOK {
				t.Fatalf("met, unaccepted pin: %#v", got)
			}
			if got := Record(root, manifest, data, RecordOptions{ReviewedDiff: true}).Outcome; got.Exit != ExitOK {
				t.Fatalf("accept exit-only pin: %#v", got)
			}
			lock, before, err := LoadLockfile(root)
			if err != nil {
				t.Fatal(err)
			}
			if pin := lock.Probes["exit-only"].Pin; pin.AcceptedCommit == nil || *pin.AcceptedCommit != headCommit(t, root) {
				t.Fatalf("met exit-only pin was not accepted: %#v", pin)
			}
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "accept baseline")
			if got := Record(root, manifest, data, RecordOptions{ReviewedDiff: true}).Outcome; got.Exit != ExitOK {
				t.Fatalf("repeat record: %#v", got)
			}
			after, err := os.ReadFile(filepath.Join(root, "vise.lock"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("repeated record changed the baseline: %v", err)
			}
			writeTestFile(t, root, "program.sh", fmt.Sprintf("printf unexpected >&2\nexit %d\n", exit))
			if got := Verify(root, manifest, data, VerifyOptions{}).Outcome; got.Exit != ExitBehavior {
				t.Fatalf("accepted pin must reject undeclared stderr: %#v", got)
			}
		})
	}
}
