package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const gitAuthorityManifest = `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "p1"
run = "printf p1 >> witness; printf one"
[[probe]]
id = "p2"
run = "printf p2 >> witness; printf two"
[[metric]]
id = "m"
run = "printf metric >> witness; printf 1"
direction = "down"
enforce = "none"
`

const gitAuthorityFingerprintManifest = `[vise]
version = 1
[stubs]
network = "declared-off"
[env]
fingerprint = ["sh fingerprint.sh"]
[[probe]]
id = "p1"
run = "printf p1 >> witness; printf one"
[[probe]]
id = "p2"
run = "printf p2 >> witness; printf two"
[[metric]]
id = "m"
run = "printf metric >> witness; printf 1"
direction = "down"
enforce = "none"
`

type gitAuthorityGeneration struct {
	lock, journal []byte
	blobsPresent  bool
	blobs         map[string][]byte
}

func gitAuthorityRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gitAuthoritySnapshot(t *testing.T, root string) gitAuthorityGeneration {
	t.Helper()
	generation := gitAuthorityGeneration{
		lock:    gitAuthorityRead(t, filepath.Join(root, "vise.lock")),
		journal: gitAuthorityRead(t, filepath.Join(root, ".vise", "journal.jsonl")),
		blobs:   make(map[string][]byte),
	}
	blobRoot := filepath.Join(root, ".vise", "blobs")
	if _, err := os.Lstat(blobRoot); err == nil {
		generation.blobsPresent = true
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if generation.blobsPresent {
		if err := filepath.Walk(blobRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(blobRoot, path)
			if err != nil {
				return err
			}
			generation.blobs[filepath.ToSlash(rel)] = gitAuthorityRead(t, path)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return generation
}

func gitAuthorityAssertGeneration(t *testing.T, before, after gitAuthorityGeneration) {
	t.Helper()
	if !bytes.Equal(before.lock, after.lock) {
		t.Error("vise.lock changed")
	}
	if !bytes.Equal(before.journal, after.journal) {
		t.Error("journal changed")
	}
	if before.blobsPresent != after.blobsPresent {
		t.Errorf("blob directory presence changed: before=%t after=%t", before.blobsPresent, after.blobsPresent)
	}
	if len(before.blobs) != len(after.blobs) {
		t.Errorf("blob file count changed: before=%d after=%d", len(before.blobs), len(after.blobs))
	}
	for path, want := range before.blobs {
		if got, ok := after.blobs[path]; !ok || !bytes.Equal(want, got) {
			t.Fatalf("blob %q changed or disappeared", path)
		}
	}
}

func gitAuthorityAssertLockAndBlobs(t *testing.T, before, after gitAuthorityGeneration) {
	t.Helper()
	withoutJournalBefore := before
	withoutJournalAfter := after
	withoutJournalBefore.journal = nil
	withoutJournalAfter.journal = nil
	gitAuthorityAssertGeneration(t, withoutJournalBefore, withoutJournalAfter)
}

func gitAuthorityAssertOneMatchingGateEvent(t *testing.T, before, after []byte, outcome map[string]any, commit string) {
	t.Helper()
	if !bytes.HasPrefix(after, before) {
		t.Fatal("gate did not retain the existing journal byte prefix")
	}
	appended := after[len(before):]
	if len(appended) == 0 || appended[len(appended)-1] != '\n' {
		t.Fatalf("gate did not append one newline-terminated event: %q", appended)
	}
	lines := bytes.Split(bytes.TrimSuffix(appended, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("gate appended %d journal records, want 1", len(lines))
	}
	var event map[string]any
	if err := json.Unmarshal(lines[0], &event); err != nil {
		t.Fatalf("appended gate event is not JSON: %v", err)
	}
	if event["e"] != "gate" || event["verdict"] != outcome["verdict"] || event["lock"] != outcome["lock"] || !reflect.DeepEqual(event["counts"], outcome["counts"]) {
		t.Fatalf("journal event does not match returned outcome: event=%#v outcome=%#v", event, outcome)
	}
	if event["commit"] != commit || !reflect.DeepEqual(event["probe_set"], []any{"m", "p1", "p2"}) {
		t.Fatalf("journal commit/scope mismatch: %#v; expected commit %s", event, commit)
	}
}

func gitAuthorityRepo(t *testing.T) string {
	t.Helper()
	root := cliRepo(t, gitAuthorityManifest, "")
	cliWrite(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nwitness\n")
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "authority fixture")
	if exit, out, errOut := cliRun(t, root, "record", "--json"); exit != 0 || errOut != "" {
		t.Fatalf("record: %d %s %q", exit, out, errOut)
	}
	cliGit(t, root, "add", "vise.lock", ".vise/blobs")
	cliGit(t, root, "commit", "-qm", "baseline")
	if err := os.Remove(filepath.Join(root, "witness")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return root
}

func gitAuthorityFingerprintRepo(t *testing.T) string {
	t.Helper()
	root := cliRepo(t, gitAuthorityFingerprintManifest, "")
	cliWrite(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nwitness\n.mode\n")
	cliWrite(t, root, "fingerprint.sh", `#!/bin/sh
case "$(cat .mode)" in
stable) printf tool-v1 ;;
fail) printf 'selected fingerprint failure' >&2; exit 23 ;;
git) git config c09.fingerprint changed; printf tool-v1 ;;
*) exit 24 ;;
esac
`)
	cliWrite(t, root, ".mode", "stable\n")
	cliGit(t, root, "add", ".gitignore", "vise.toml", "fingerprint.sh")
	cliGit(t, root, "commit", "-qm", "fingerprint fixture")
	if exit, out, errOut := cliRun(t, root, "record", "--json"); exit != 0 || errOut != "" {
		t.Fatalf("record: %d %s %q", exit, out, errOut)
	}
	cliGit(t, root, "add", "vise.lock", ".vise/blobs")
	cliGit(t, root, "commit", "-qm", "fingerprint baseline")
	if err := os.Remove(filepath.Join(root, "witness")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return root
}

func assertGitAuthorityCounts(t *testing.T, v map[string]any, declared, skipped int) {
	t.Helper()
	c, ok := v["counts"].(map[string]any)
	if !ok {
		t.Fatalf("missing counts: %#v", v)
	}
	want := map[string]int{
		"declared": declared, "pass": 0, "behavior": 0, "flaky": 0,
		"harness": 1, "metric": 0, "unmet": 0, "skipped": skipped,
	}
	for field, value := range want {
		if c[field] != float64(value) {
			t.Errorf("counts.%s=%v, want %d; all counts=%#v", field, c[field], value, c)
		}
	}
}

func assertGitAuthorityFailure(t *testing.T, exit int, out string, key string, declared, skipped int) {
	t.Helper()
	v := parseCLIJSON(t, out)
	if v["exit"] != float64(exit) {
		t.Errorf("returned exit %d disagrees with JSON exit %#v", exit, v["exit"])
	}
	next, ok := v["next"].(map[string]any)
	if exit != 2 || !ok || next["action"] != "human" {
		t.Errorf("route: exit=%d value=%#v", exit, v)
	}
	failures, ok := v["failures"].(map[string]any)
	if !ok {
		t.Fatalf("missing failures: %#v", v)
	}
	if len(failures) != 1 {
		t.Errorf("failure count=%d, want 1: %#v", len(failures), failures)
	}
	f, ok := failures[key].(map[string]any)
	if !ok {
		t.Fatalf("missing %q failure: %#v", key, failures)
	}
	if f["class"] != "harness" || f["operator"] != true {
		t.Errorf("%s failure: %#v", key, f)
	}
	assertGitAuthorityCounts(t, v, declared, skipped)
}

func assertGitAuthorityOperatorRefusal(t *testing.T, exit int, out string, declared, skipped int) {
	t.Helper()
	v := parseCLIJSON(t, out)
	if v["exit"] != float64(exit) {
		t.Errorf("returned exit %d disagrees with JSON exit %#v", exit, v["exit"])
	}
	next, ok := v["next"].(map[string]any)
	if exit != 2 || !ok || next["action"] != "human" {
		t.Errorf("route: exit=%d value=%#v", exit, v)
	}
	failures, ok := v["failures"].(map[string]any)
	if !ok || len(failures) != 1 {
		t.Fatalf("failures: %#v", v["failures"])
	}
	for _, raw := range failures {
		failure, ok := raw.(map[string]any)
		if !ok || failure["class"] != "harness" || failure["operator"] != true {
			t.Fatalf("operator refusal: %#v", raw)
		}
	}
	assertGitAuthorityCounts(t, v, declared, skipped)
}

func TestGitAuthorityPreflightCorruptionIsHumanAndDoesNotExecute(t *testing.T) {
	for _, damage := range []string{"index", "head"} {
		for _, command := range []string{"gate", "verify"} {
			for _, subset := range []bool{false, true} {
				t.Run(damage+"/"+command+map[bool]string{false: "/full", true: "/subset"}[subset], func(t *testing.T) {
					root := gitAuthorityRepo(t)
					before := gitAuthoritySnapshot(t, root)
					if damage == "index" {
						cliWrite(t, root, ".git/index", "corrupt index")
					} else {
						cliWrite(t, root, ".git/HEAD", "ref: refs/heads/does-not-exist\n")
					}
					args := []string{command, "--json"}
					declared := 3
					if subset {
						args = []string{command, "--probe", "p1", "--json"}
						declared = 1
					}
					exit, out, _ := cliRun(t, root, args...)
					assertGitAuthorityFailure(t, exit, out, "git", declared, declared)
					if b := gitAuthorityRead(t, filepath.Join(root, "witness")); len(b) != 0 {
						t.Fatalf("candidate executed: %q", b)
					}
					after := gitAuthoritySnapshot(t, root)
					gitAuthorityAssertGeneration(t, before, after)
				})
			}
		}
	}
}

func TestGitAuthorityFingerprintFailureIsOperatorOwnedAndSkipsCandidates(t *testing.T) {
	for _, mode := range []string{"fail", "git"} {
		t.Run(mode, func(t *testing.T) {
			root := gitAuthorityFingerprintRepo(t)
			before := gitAuthoritySnapshot(t, root)
			configBefore := gitAuthorityRead(t, filepath.Join(root, ".git/config"))
			headCommand := exec.Command("git", "rev-parse", "HEAD")
			headCommand.Dir = root
			head, err := headCommand.Output()
			if err != nil {
				t.Fatal(err)
			}
			cliWrite(t, root, ".mode", mode+"\n")
			exit, out, _ := cliRun(t, root, "gate", "--json")
			assertGitAuthorityFailure(t, exit, out, "fingerprint", 3, 3)
			outcome := parseCLIJSON(t, out)
			if b := gitAuthorityRead(t, filepath.Join(root, "witness")); len(b) != 0 {
				t.Fatalf("candidate ran after fingerprint refusal: %q", b)
			}
			after := gitAuthoritySnapshot(t, root)
			gitAuthorityAssertLockAndBlobs(t, before, after)
			gitAuthorityAssertOneMatchingGateEvent(t, before.journal, after.journal, outcome, strings.TrimSpace(string(head)))
			configAfter := gitAuthorityRead(t, filepath.Join(root, ".git/config"))
			if changed := !bytes.Equal(configBefore, configAfter); changed != (mode == "git") {
				t.Fatalf("fingerprint Git mutation witness: changed=%v mode=%s", changed, mode)
			}
		})
	}
}

func TestGitAuthorityStatusAndRecordRoutes(t *testing.T) {
	t.Run("normal dirty status remains ready", func(t *testing.T) {
		root := gitAuthorityRepo(t)
		cliWrite(t, root, "dirty", "ordinary")
		exit, out, _ := cliRun(t, root, "status", "--json")
		v := parseCLIJSON(t, out)
		if exit != 0 || v["state"] != "ready" {
			t.Fatalf("status: %d %#v", exit, v)
		}
	})
	for _, damage := range []string{"index", "head"} {
		t.Run(damage, func(t *testing.T) {
			root := gitAuthorityRepo(t)
			before := gitAuthoritySnapshot(t, root)
			if damage == "index" {
				cliWrite(t, root, ".git/index", "corrupt")
			} else {
				cliWrite(t, root, ".git/HEAD", "ref: refs/heads/missing\n")
			}
			exit, out, _ := cliRun(t, root, "status", "--json")
			v := parseCLIJSON(t, out)
			if exit != 0 || v["state"] != "harness-error" || v["next"].(map[string]any)["action"] != "human" {
				t.Errorf("status: %d %#v", exit, v)
			}
			for _, args := range [][]string{{"record", "--json"}, {"record", "--preview", "--json"}} {
				exit, out, _ = cliRun(t, root, args...)
				assertGitAuthorityOperatorRefusal(t, exit, out, 0, 0)
				if b := gitAuthorityRead(t, filepath.Join(root, "witness")); len(b) != 0 {
					t.Fatalf("record refusal executed candidate: %q", b)
				}
			}
			after := gitAuthoritySnapshot(t, root)
			gitAuthorityAssertGeneration(t, before, after)
		})
	}
}

func TestGitAuthorityRestoredRepositoryIsUsable(t *testing.T) {
	for _, damage := range []string{"index", "head"} {
		t.Run(damage, func(t *testing.T) {
			root := gitAuthorityRepo(t)
			path := filepath.Join(root, ".git", map[string]string{"index": "index", "head": "HEAD"}[damage])
			original := gitAuthorityRead(t, path)
			cliWrite(t, root, strings.TrimPrefix(path, root+string(filepath.Separator)), "corrupt")
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatal(err)
			}
			exit, out, _ := cliRun(t, root, "gate", "--json")
			v := parseCLIJSON(t, out)
			if exit != 0 || v["verdict"] != "green" || v["next"].(map[string]any)["action"] != "proceed" {
				t.Fatalf("restored repository: exit=%d value=%#v", exit, v)
			}
		})
	}
}

func TestGitAuthorityRootFailuresKeepCommandEnvelope(t *testing.T) {
	roots := map[string]func(t *testing.T) string{
		"outside": func(t *testing.T) string { return t.TempDir() },
		"missing HEAD": func(t *testing.T) string {
			root := cliRepo(t, gitAuthorityManifest, "")
			if err := os.Remove(filepath.Join(root, ".git", "HEAD")); err != nil {
				t.Fatal(err)
			}
			return root
		},
	}
	for rootName, makeRoot := range roots {
		for _, command := range []string{"gate", "verify", "record", "run"} {
			t.Run(rootName+"/"+command, func(t *testing.T) {
				root := makeRoot(t)
				args := []string{command, "--json"}
				if command == "run" {
					args = []string{"run", "p1", "--json"}
				}
				exit, out, _ := cliRun(t, root, args...)
				assertGitAuthorityFailure(t, exit, out, command, 1, 0)
			})
		}
	}
}

func TestGitAuthorityRawRunRouting(t *testing.T) {
	t.Run("pre-execution corrupt index is operator owned", func(t *testing.T) {
		root := gitAuthorityRepo(t)
		before := gitAuthoritySnapshot(t, root)
		cliWrite(t, root, ".git/index", "corrupt")
		exit, out, _ := cliRun(t, root, "run", "p1", "--json")
		assertGitAuthorityFailure(t, exit, out, "run", 1, 0)
		if b := gitAuthorityRead(t, filepath.Join(root, "witness")); len(b) != 0 {
			t.Fatalf("raw probe executed before Git snapshot: %q", b)
		}
		after := gitAuthoritySnapshot(t, root)
		gitAuthorityAssertGeneration(t, before, after)
	})

	t.Run("executed Git tamper remains probe owned", func(t *testing.T) {
		root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
		cliWrite(t, root, "probe.sh", "#!/bin/sh\ngit config c09.changed yes\nprintf stable")
		exit, out, _ := cliRun(t, root, "run", "behavior", "--json")
		v := parseCLIJSON(t, out)
		if exit != 2 || v["cmd"] != "run" || v["next"].(map[string]any)["action"] != "fix_probe" {
			t.Fatalf("executed tamper route: %d %#v", exit, v)
		}
		f := v["failures"].(map[string]any)["run"].(map[string]any)
		if _, operator := f["operator"]; operator || !strings.Contains(f["detail"].(string), "probe modified git's own state") {
			t.Fatalf("executed tamper envelope: %#v", f)
		}
	})

	t.Run("executed evaluator tamper remains probe owned", func(t *testing.T) {
		root := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
		cliWrite(t, root, "probe.sh", "#!/bin/sh\nprintf changed > vise.lock\nprintf stable")
		exit, out, _ := cliRun(t, root, "run", "behavior", "--json")
		v := parseCLIJSON(t, out)
		if exit != 2 || v["exit"] != float64(exit) || v["cmd"] != "run" || v["next"].(map[string]any)["action"] != "fix_probe" {
			t.Fatalf("executed evaluator tamper route: %d %#v", exit, v)
		}
		failures := v["failures"].(map[string]any)
		f := failures["run"].(map[string]any)
		if len(failures) != 1 || f["class"] != "harness" || f["operator"] != nil || !strings.Contains(f["detail"].(string), "probe modified vise state") {
			t.Fatalf("executed evaluator tamper envelope: %#v", v)
		}
		assertGitAuthorityCounts(t, v, 1, 0)
	})

	t.Run("tracked declared artifact carries operator authority", func(t *testing.T) {
		manifest := basicManifest("files = [\"tracked.txt\"]\n")
		root := cliRepo(t, manifest, "#!/bin/sh\nprintf should-not-run >> witness\nprintf stable")
		cliWrite(t, root, "tracked.txt", "operator-owned\n")
		cliGit(t, root, "add", "tracked.txt")
		cliGit(t, root, "commit", "-qm", "track unsafe artifact")
		before := gitAuthoritySnapshot(t, root)
		exit, out, _ := cliRun(t, root, "run", "behavior", "--json")
		assertGitAuthorityFailure(t, exit, out, "run", 1, 0)
		if got := gitAuthorityRead(t, filepath.Join(root, "tracked.txt")); string(got) != "operator-owned\n" {
			t.Fatalf("tracked artifact was changed: %q", got)
		}
		if b := gitAuthorityRead(t, filepath.Join(root, "witness")); len(b) != 0 {
			t.Fatalf("probe executed despite artifact reset refusal: %q", b)
		}
		gitAuthorityAssertGeneration(t, before, gitAuthoritySnapshot(t, root))
	})
}

func TestGitAuthorityPrecedenceControls(t *testing.T) {
	root := cliRepo(t, gitAuthorityManifest, "")
	if exit, out, _ := cliRun(t, root, "gate", "--json"); exit != 4 || parseCLIJSON(t, out)["next"].(map[string]any)["action"] != "record_first" {
		t.Fatalf("no baseline precedence: %d %s", exit, out)
	}
	if exit, out, _ := cliRun(t, root, "gate", "--unknown", "--json"); exit != 2 || parseCLIJSON(t, out)["next"].(map[string]any)["action"] != "fix_invocation" {
		t.Fatalf("invocation precedence: %d %s", exit, out)
	}
	if exit, out, _ := cliRun(t, root, "status", "extra", "--json"); exit != 2 || parseCLIJSON(t, out)["next"].(map[string]any)["action"] != "fix_invocation" {
		t.Fatalf("arity precedence: %d %s", exit, out)
	}
	cliWrite(t, root, ".git/index", "corrupt")
	if exit, out, _ := cliRun(t, root, "gate", "--json"); exit != 4 || parseCLIJSON(t, out)["next"].(map[string]any)["action"] != "record_first" {
		t.Fatalf("missing baseline did not retain precedence over Git inspection: %d %s", exit, out)
	}

	root = cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf stable")
	if exit, _, errOut := cliRun(t, root, "record"); exit != 0 {
		t.Fatalf("record: %d %s", exit, errOut)
	}
	cliWrite(t, root, "probe.sh", "#!/bin/sh\ngit config c09.changed yes\nprintf stable")
	exit, out, _ := cliRun(t, root, "gate", "--json")
	v := parseCLIJSON(t, out)
	if exit != 2 || v["next"].(map[string]any)["action"] != "fix_probe" {
		t.Fatalf("executed tamper route: %d %#v", exit, v)
	}
	f := v["failures"].(map[string]any)["behavior"].(map[string]any)
	if _, operator := f["operator"]; operator {
		t.Fatalf("executed tamper became operator-owned: %#v", f)
	}
}
