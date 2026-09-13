package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/NakliTechie/vise/internal/vise"
)

func c03PinAcceptance(t *testing.T, root string) *string {
	t.Helper()
	lock, _, err := vise.LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	return lock.Probes["p"].Pin.AcceptedCommit
}

func c03LockBytes(t *testing.T, root string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func c03AssertUnacceptedJudgment(t *testing.T, root, command string, before []byte) {
	t.Helper()
	exit, stdout, stderr := cliRun(t, root, command, "--json")
	reply := parseCLIJSON(t, stdout)
	if exit != 6 || stderr != "" || reply["verdict"] != "red" || reply["next"].(map[string]any)["action"] != "build" {
		t.Fatalf("%s: exit=%d reply=%#v stderr=%q", command, exit, reply, stderr)
	}
	pins := reply["pins"].(map[string]any)
	if pins["unmet_count"] != float64(1) || pins["passing_unaccepted_count"] != float64(0) {
		t.Fatalf("%s pins = %#v", command, pins)
	}
	if after := c03LockBytes(t, root); !bytes.Equal(before, after) {
		t.Fatalf("%s changed the lock:\nbefore=%s\nafter=%s", command, before, after)
	}
	if accepted := c03PinAcceptance(t, root); accepted != nil {
		t.Fatalf("%s accepted the pin at %q", command, *accepted)
	}
}

func TestC03VerifyAndGatePreserveEveryStableUnacceptedCondition(t *testing.T) {
	base := `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "p"
run = "./probe.sh"
timeout = %d
expect.stdout = "spec/out"
%s`
	cases := []struct {
		name, script, extra string
		timeout             int
	}{
		{"mismatch", "#!/bin/sh\nprintf 'wrong\\n'\n", "", 5},
		{"exit-127", "#!/bin/sh\nexit 127\n", "", 5},
		{"live-timeout", "#!/bin/sh\nprintf partial\nsleep 5\n", "", 1},
		{"signal", "#!/bin/sh\nkill -TERM $$\n", "", 5},
		{"missing-artifact", "#!/bin/sh\nexit 0\n", "files = [\"out/report\"]\nexpect.files = { \"out/report\" = \"spec/report\" }\n", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := fmt.Sprintf(base, tc.timeout, tc.extra)
			root := cliRepo(t, manifest, tc.script)
			cliWrite(t, root, "spec/out", "wanted\n")
			if tc.name == "missing-artifact" {
				cliWrite(t, root, "spec/report", "report\n")
				cliWrite(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nout/\n")
			}
			cliGit(t, root, "add", ".")
			cliGit(t, root, "commit", "-qm", "pin fixture")
			if exit, stdout, stderr := cliRun(t, root, "record", "--json"); exit != 0 || stderr != "" || parseCLIJSON(t, stdout)["exit"] != float64(0) {
				t.Fatalf("record: exit=%d stdout=%s stderr=%q", exit, stdout, stderr)
			}
			before := c03LockBytes(t, root)
			if c03PinAcceptance(t, root) != nil {
				t.Fatal("record accepted an unmet pin")
			}
			for _, command := range []string{"verify", "gate"} {
				c03AssertUnacceptedJudgment(t, root, command, before)
			}
		})
	}
}

func TestC03MetGateNeverAcceptsAndLaterRegressionRemainsUnmet(t *testing.T) {
	manifest := `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "p"
run = "./probe.sh"
timeout = 5
expect.stdout = "spec/out"
`
	root := cliRepo(t, manifest, "#!/bin/sh\nexit 127\n")
	cliWrite(t, root, "spec/out", "wanted\n")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "pin fixture")
	if exit, _, _ := cliRun(t, root, "record", "--json"); exit != 0 {
		t.Fatalf("record exit = %d", exit)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "unaccepted baseline")
	before := c03LockBytes(t, root)

	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf 'wanted\\n'\n")
	cliGit(t, root, "add", "probe.sh")
	cliGit(t, root, "commit", "-qm", "meet pin")
	for _, command := range []string{"verify", "gate"} {
		exit, stdout, stderr := cliRun(t, root, command, "--json")
		reply := parseCLIJSON(t, stdout)
		pins := reply["pins"].(map[string]any)
		if exit != 0 || stderr != "" || reply["verdict"] != "green" || pins["passing_unaccepted_count"] != float64(1) {
			t.Fatalf("%s met: exit=%d reply=%#v stderr=%q", command, exit, reply, stderr)
		}
		if !bytes.Equal(before, c03LockBytes(t, root)) || c03PinAcceptance(t, root) != nil {
			t.Fatalf("%s accepted or rewrote the met pin", command)
		}
	}

	cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf 'regressed\\n'\n")
	cliGit(t, root, "add", "probe.sh")
	cliGit(t, root, "commit", "-qm", "regress before acceptance")
	for _, command := range []string{"verify", "gate"} {
		c03AssertUnacceptedJudgment(t, root, command, before)
	}
}
