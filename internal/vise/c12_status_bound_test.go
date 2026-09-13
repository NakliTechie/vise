package vise

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestC12StatusPinSummaryIsBoundedSortedHistoricalAndDoesNotExecute(t *testing.T) {
	root := testGitRepo(t)
	ids := []string{"zeta", "beta", "delta", "alpha", "omega"}
	var manifest strings.Builder
	manifest.WriteString("[vise]\nversion = 1\n[stubs]\nnetwork = \"declared-off\"\n")
	for _, id := range ids {
		want := "wanted-" + id + "\n"
		got := "wrong-" + id + "\n"
		if id == "omega" {
			got = want
		}
		writeTestFile(t, root, "spec/"+id+".stdout", want)
		writeTestFile(t, root, "bin/"+id, "#!/bin/sh\nprintf '"+id+"' >> .calls\nprintf '"+strings.TrimSuffix(got, "\n")+"\\n'\n")
		if err := os.Chmod(filepath.Join(root, "bin", id), 0o755); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&manifest, "[[probe]]\nid = %q\nrun = %q\nexpect.stdout = %q\n", id, "./bin/"+id, "spec/"+id+".stdout")
	}
	writeTestFile(t, root, "vise.toml", manifest.String())
	writeTestFile(t, root, ".gitignore", ".calls\n.vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "five pins")
	loaded, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	result := Record(root, loaded, manifestBytes, RecordOptions{})
	if result.Outcome.Exit != ExitOK || result.Pins == nil || !reflect.DeepEqual(result.Pins.Accepted, []string{"omega"}) || len(result.Pins.Unmet) != 4 {
		t.Fatalf("record = %#v %#v", result.Outcome, result.Pins)
	}
	marker := filepath.Join(root, ".calls")
	if calls, err := os.ReadFile(marker); err != nil || len(calls) == 0 {
		t.Fatalf("record did not execute the real probes: calls=%q err=%v", calls, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	report := BuildStatus(root)
	pins := report.Lock.Pins
	if report.State != "ready" || pins == nil || pins.Declared != 5 || pins.Accepted != 1 || pins.UnacceptedCount != 4 || !reflect.DeepEqual(pins.Unaccepted, []string{"alpha", "beta", "delta"}) {
		t.Fatalf("status = state %q, pins %#v", report.State, pins)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("status executed a real probe: %v", err)
	}
}
