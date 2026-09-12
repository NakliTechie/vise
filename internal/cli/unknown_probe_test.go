package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/NakliTechie/vise/internal/vise"
)

func TestUnknownSelectedProbeIsAnInvocationError(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprintf("recorded=%t", recorded), func(t *testing.T) {
			root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
			if recorded {
				if exit, out, errOut := cliRun(t, root, "record", "--json"); exit != 0 {
					t.Fatalf("control record: %d %s %s", exit, out, errOut)
				}
			}
			journal := filepath.Join(root, ".vise", "journal.jsonl")
			for _, command := range []string{"gate", "verify"} {
				want := vise.ExitNotInitialized
				if recorded {
					want = vise.ExitOK
				}
				if exit, out, _ := cliRun(t, root, command, "--probe", "behavior", "--json"); exit != want {
					t.Fatalf("known-ID control: %d %s", exit, out)
				}
				before, err := os.ReadFile(journal)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				exit, out, _ := cliRun(t, root, command, "--probe", "no-such-id", "--json")
				value := parseCLIJSON(t, out)
				action := value["next"].(map[string]any)["action"]
				if exit != vise.ExitHarness || value["exit"] != float64(vise.ExitHarness) || action != vise.NextFixInvocation {
					t.Errorf("unknown ID: process exit=%d, response=%s", exit, out)
				}
				after, err := os.ReadFile(journal)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("unknown-ID invocation changed judgment history")
				}
			}
		})
	}
}
