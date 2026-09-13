package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const preflightCountsManifest = `[vise]
version = 1
[[probe]]
id = "p1"
run = "printf p1 >> .toggle; printf one"
[[probe]]
id = "p2"
run = "printf p2 >> .toggle; printf two"
[[metric]]
id = "size"
run = "printf metric >> .toggle; printf 10"
version_cmd = "printf version >> .toggle; printf v1"
direction = "down"
enforce = "no-regress"
`

func TestPreflightJSONCountsDescribeRequestedScopeWithoutExecution(t *testing.T) {
	root := cliRepo(t, preflightCountsManifest, "")
	for _, command := range []string{"gate", "verify"} {
		for _, selected := range []bool{false, true} {
			args := []string{command}
			declared := 3
			if selected {
				args = append(args, "--probe", "p1")
				declared = 1
			}
			reply := protocolReply(t, root, 4, command, "record_first", args...)
			want := map[string]any{"declared": float64(declared), "pass": float64(0), "skipped": float64(declared), "behavior": float64(0), "flaky": float64(0), "harness": float64(0), "metric": float64(0), "unmet": float64(0)}
			if !reflect.DeepEqual(reply["counts"], want) {
				t.Errorf("%v counts = %#v, want %#v", args, reply["counts"], want)
			}
			protocolAbsent(t, reply, "lock", "failures", "classes", "pins", "metrics")
			for _, path := range []string{".toggle", "vise.lock", ".vise/journal.jsonl"} {
				if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
					t.Errorf("preflight created %s: %v", path, err)
				}
			}
		}
		// Invocation rejection remains prior to missing-baseline handling.
		protocolReply(t, root, 2, command, "fix_invocation", command, "--probe", "absent")
	}
}

func TestPreflightCountCorrectionRetainsFullAndSubsetReplay(t *testing.T) {
	root := cliRepo(t, preflightCountsManifest, "")
	protocolReply(t, root, 0, "record", "proceed", "record")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "baseline")
	for _, command := range []string{"gate", "verify"} {
		for _, selected := range []bool{false, true} {
			if err := os.Remove(filepath.Join(root, ".toggle")); err != nil {
				t.Fatal(err)
			}
			args := []string{command}
			declared, witness := 3, "p1p2metricversion"
			if selected {
				args = append(args, "--probe", "p1")
				declared, witness = 1, "p1"
			}
			reply := protocolReply(t, root, 0, command, "proceed", args...)
			counts := reply["counts"].(map[string]any)
			if counts["declared"] != float64(declared) || counts["pass"] != float64(declared) || counts["skipped"] != float64(0) {
				t.Errorf("%v: %#v", args, counts)
			}
			got, err := os.ReadFile(filepath.Join(root, ".toggle"))
			if err != nil || string(got) != witness {
				t.Errorf("%v execution witness: %q, %v; want %q", args, got, err, witness)
			}
			if selected {
				protocolAbsent(t, reply, "metrics")
			} else if len(reply["metrics"].(map[string]any)) != 1 {
				t.Errorf("missing metric result: %#v", reply)
			}
		}
	}
}
