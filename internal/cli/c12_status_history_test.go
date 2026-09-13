package cli

import (
	"bytes"
	"github.com/NakliTechie/vise/internal/vise"
	"os"
	"path/filepath"
	"testing"
)

func c12Lock(t *testing.T, root string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func c12Accepted(t *testing.T, root string) bool {
	t.Helper()
	l, _, err := vise.LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	return l.Probes["p"].Pin.AcceptedCommit != nil
}

func TestC12StatusKeepsAcceptedHistoryWhileGateDetectsRegression(t *testing.T) {
	manifest := "[vise]\nversion=1\n[stubs]\nnetwork='declared-off'\n[[probe]]\nid='p'\nrun='./probe.sh'\nexpect.stdout='spec/out'\n"
	root := cliRepo(t, manifest, "#!/bin/sh\nprintf ran >> .calls\nprintf 'wanted\\n'\n")
	cliWrite(t, root, "spec/out", "wanted\n")
	cliWrite(t, root, ".gitignore", ".calls\n.vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "healthy pin")
	if exit, _, _ := cliRun(t, root, "record", "--json"); exit != 0 {
		t.Fatalf("record exit=%d", exit)
	}
	before := c12Lock(t, root)
	if err := os.Remove(filepath.Join(root, ".calls")); err != nil {
		t.Fatal(err)
	}
	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf ran >> .calls\nprintf 'regressed\\n'\n")
	exit, out, stderr := cliRun(t, root, "status", "--json")
	report := parseCLIJSON(t, out)
	pins := report["lock"].(map[string]any)["pins"].(map[string]any)
	if exit != 0 || stderr != "" || pins["accepted"] != float64(1) || pins["unaccepted_count"] != float64(0) {
		t.Fatalf("status: exit=%d reply=%#v stderr=%q", exit, report, stderr)
	}
	if !bytes.Equal(before, c12Lock(t, root)) {
		t.Fatal("status rewrote acceptance history")
	}
	if _, err := os.Stat(filepath.Join(root, ".calls")); !os.IsNotExist(err) {
		t.Fatalf("status executed the regressed probe: %v", err)
	}
	exit, out, stderr = cliRun(t, root, "gate", "--json")
	gate := parseCLIJSON(t, out)
	if exit != 1 || stderr != "" || gate["verdict"] != "red" || gate["next"].(map[string]any)["action"] != "revert" {
		t.Fatalf("gate: exit=%d reply=%#v stderr=%q", exit, gate, stderr)
	}
	if !bytes.Equal(before, c12Lock(t, root)) || !c12Accepted(t, root) {
		t.Fatal("gate rewrote or lost accepted history")
	}
	if _, err := os.Stat(filepath.Join(root, ".calls")); err != nil {
		t.Fatalf("gate did not execute the regressed probe: %v", err)
	}
}
