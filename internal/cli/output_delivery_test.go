package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NakliTechie/vise/internal/vise"
)

type deliveryFault struct {
	mode  string
	calls int
	allow int
}

func (w *deliveryFault) Write(p []byte) (int, error) {
	w.calls++
	if w.calls <= w.allow {
		return len(p), nil
	}
	switch w.mode {
	case "short":
		return len(p) / 2, nil
	case "zero":
		return 0, nil
	case "partial-error":
		return len(p) / 2, errors.New("destination failed")
	case "full-error":
		return len(p), errors.New("destination failed")
	default:
		return 0, errors.New("destination failed")
	}
}

func TestResultDeliveryErrorsOverrideCommandExit(t *testing.T) {
	root := t.TempDir() // version/help/status/doctor do not need a repository.
	for _, args := range [][]string{{"version"}, {"version", "--json"}, {"--help"}, {"--help", "--json"}, {"status"}, {"status", "--json"}, {"doctor"}, {"doctor", "--json"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var good bytes.Buffer
			if exit := Run(args, root, &good, io.Discard); exit != 0 || good.Len() == 0 {
				t.Fatalf("positive delivery: exit=%d output=%q", exit, good.String())
			}
			for _, mode := range []string{"error", "short", "zero", "partial-error", "full-error"} {
				t.Run(mode, func(t *testing.T) {
					bad := &deliveryFault{mode: mode}
					if exit := Run(args, root, bad, io.Discard); exit != vise.ExitHarness {
						t.Fatalf("failed delivery returned %d, want 2", exit)
					}
				})
			}
		})
	}
}

func TestJSONDeliveryPreservesOnlyDeliveredExits(t *testing.T) {
	for _, intended := range []int{0, 1, 2, 3, 4, 5, 6, 7, 127} {
		t.Run(fmt.Sprint(intended), func(t *testing.T) {
			value := map[string]any{"v": 1, "cmd": "run", "exit": intended}
			var good bytes.Buffer
			if exit := writeJSONWithExit(&good, value, intended); exit != intended {
				t.Fatalf("delivered exit = %d, want %d", exit, intended)
			}
			if !json.Valid(good.Bytes()) || !bytes.HasSuffix(good.Bytes(), []byte("\n")) {
				t.Fatalf("invalid delivered framing: %q", good.Bytes())
			}
			for _, mode := range []string{"error", "short", "zero", "partial-error", "full-error"} {
				bad := &deliveryFault{mode: mode}
				if exit := writeJSONWithExit(bad, value, intended); exit != vise.ExitHarness {
					t.Errorf("%s: undelivered raw exit = %d", mode, exit)
				}
				if bad.calls != 1 {
					t.Errorf("%s: retried partial result (%d writes)", mode, bad.calls)
				}
			}
		})
	}
	plain := vise.NewOutcome("gate")
	plain.Finalize()
	if exit := writeOutcomeJSON(&deliveryFault{mode: "short"}, plain, nil); exit != 2 {
		t.Fatalf("undelivered green outcome returned %d", exit)
	}
}

type unencodableDelivery struct{}

func (unencodableDelivery) MarshalJSON() ([]byte, error) {
	return nil, errors.New("invalid \x01 byte and \"quoted\"\ntext")
}

func TestEncodingFailureIsACompleteDiagnostic(t *testing.T) {
	for _, intended := range []int{0, 7} {
		var out bytes.Buffer
		if exit := writeJSONWithExit(&out, unencodableDelivery{}, intended); exit != 2 {
			t.Fatalf("unencodable value returned %d", exit)
		}
		assertEncodingDelivery(t, out.Bytes(), "internal")
		for _, mode := range []string{"error", "short", "full-error"} {
			bad := &deliveryFault{mode: mode}
			if exit := writeJSONWithExit(bad, unencodableDelivery{}, intended); exit != 2 || bad.calls != 1 {
				t.Fatalf("broken fallback: exit=%d writes=%d", exit, bad.calls)
			}
		}
	}
	// An unencodable extra field must retain the command but none of the
	// original success/candidate fields, just like an unencodable outcome.
	var out bytes.Buffer
	outcome := vise.NewOutcome("record")
	outcome.Finalize()
	if exit := writeOutcomeJSON(&out, outcome, map[string]any{"candidate": "discard-me", "bad": unencodableDelivery{}}); exit != 2 {
		t.Fatalf("unencodable outcome returned %d", exit)
	}
	assertEncodingDelivery(t, out.Bytes(), "record")
}

func assertEncodingDelivery(t *testing.T, data []byte, command string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("invalid fallback: %v %q", err, data)
	}
	var counts map[string]int
	if err := json.Unmarshal(object["counts"], &counts); err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{"declared": 1, "harness": 1, "pass": 0, "skipped": 0, "behavior": 0, "flaky": 0, "metric": 0, "unmet": 0}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("diagnostic counts: %v", counts)
	}
	var got struct {
		Cmd      string                                         `json:"cmd"`
		Exit     int                                            `json:"exit"`
		Verdict  string                                         `json:"verdict"`
		Counts   struct{ Declared, Harness, Pass, Skipped int } `json:"counts"`
		Failures map[string]vise.Failure                        `json:"failures"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("fallback is not one JSON value: %v: %q", err, data)
	}
	if !bytes.HasSuffix(data, []byte("\n")) || got.Cmd != command || got.Exit != 2 || got.Verdict != "indeterminate" || got.Counts.Declared != 1 || got.Counts.Harness != 1 || got.Counts.Pass != 0 || got.Counts.Skipped != 0 || len(got.Failures) != 1 || got.Failures["encoding"].Class != "harness" {
		t.Fatalf("fallback is not an honest diagnostic: %s", data)
	}
	if bytes.Contains(data, []byte("discard-me")) || bytes.Contains(data, []byte(`"candidate"`)) || bytes.Contains(data, []byte(`"lock"`)) {
		t.Fatalf("fallback retained original judgment identity: %s", data)
	}
}

func TestRawDeliveryBothStreams(t *testing.T) {
	t.Run("zero", func(t *testing.T) { testRawDeliveryBothStreams(t, 0) })
	t.Run("seven", func(t *testing.T) { testRawDeliveryBothStreams(t, 7) })
}

func testRawDeliveryBothStreams(t *testing.T, intended int) {
	t.Helper()
	root := cliRepo(t, basicManifest(""), fmt.Sprintf("printf stdout; printf stderr >&2; exit %d", intended))
	for _, jsonMode := range []bool{false, true} {
		args := []string{"run", "behavior"}
		if jsonMode {
			args = append(args, "--json")
		}
		var goodOut, goodErr bytes.Buffer
		if exit := Run(args, root, &goodOut, &goodErr); exit != intended {
			t.Fatalf("delivered raw exit: %d stdout=%s stderr=%s", exit, &goodOut, &goodErr)
		}
		for _, stream := range []string{"stdout", "stderr"} {
			if jsonMode && stream == "stderr" {
				continue
			} // JSON has no raw stderr mirror.
			for _, mode := range []string{"error", "short"} {
				var stdout, stderr io.Writer = io.Discard, io.Discard
				bad := &deliveryFault{mode: mode}
				if stream == "stdout" {
					stdout = bad
				} else {
					stderr = bad
				}
				if exit := Run(args, root, stdout, stderr); exit != 2 {
					t.Errorf("json=%v %s/%s: exit=%d", jsonMode, stream, mode, exit)
				}
			}
		}
	}
}

func TestRepositoryResultDeliveryRoutes(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "printf before")
	if exit, out, err := cliRun(t, root, "record", "--json"); exit != 0 {
		t.Fatalf("record: %d %s %s", exit, out, err)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "baseline")
	for _, args := range [][]string{{"gate"}, {"verify"}, {"record", "--preview"}, {"status"}, {"doctor"}} {
		for _, jsonMode := range []bool{false, true} {
			command := append([]string{}, args...)
			if jsonMode {
				command = append(command, "--json")
			}
			t.Run(strings.Join(command, "_"), func(t *testing.T) {
				assertResultDeliveryRoute(t, root, command, 0)
			})
		}
	}
	cliWrite(t, root, "probe.sh", "printf after")
	cliGit(t, root, "commit", "-qam", "regression")
	for _, args := range [][]string{{"gate"}, {"verify"}, {"record"}, {"no-such-command"}} {
		for _, jsonMode := range []bool{false, true} {
			command := append([]string{}, args...)
			if jsonMode {
				command = append(command, "--json")
			}
			want := 1
			if args[0] == "record" || args[0] == "no-such-command" {
				want = 2
			}
			t.Run("refusal_"+strings.Join(command, "_"), func(t *testing.T) {
				assertResultDeliveryRoute(t, root, command, want)
			})
		}
	}
	// init is idempotent but writes setup on its first invocation. Confine it
	// to another disposable checkout, not the judging fixture above.
	initRoot := cliRepo(t, "", "")
	assertResultDeliveryRoute(t, initRoot, []string{"init"}, 0)
	assertResultDeliveryRoute(t, initRoot, []string{"init", "--json"}, 0)
}

func assertResultDeliveryRoute(t *testing.T, root string, args []string, want int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if exit := Run(args, root, &stdout, &stderr); exit != want {
		t.Fatalf("%v positive exit %d want %d: %s %s", args, exit, want, &stdout, &stderr)
	}
	if stdout.Len()+stderr.Len() == 0 {
		t.Fatalf("%v produced no result", args)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if stream == "stdout" && stdout.Len() == 0 || stream == "stderr" && stderr.Len() == 0 {
			continue
		}
		for _, mode := range []string{"error", "short"} {
			var out, errOut io.Writer = io.Discard, io.Discard
			bad := &deliveryFault{mode: mode}
			if stream == "stdout" {
				out = bad
			} else {
				errOut = bad
			}
			if exit := Run(args, root, out, errOut); exit != 2 {
				t.Errorf("%v %s/%s returned %d", args, stream, mode, exit)
			}
		}
	}
}

func TestUndeliveredReviewCannotOverwriteBaseline(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "printf before")
	if exit, out, err := cliRun(t, root, "record", "--json"); exit != 0 {
		t.Fatalf("record: %d %s %s", exit, out, err)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-qm", "baseline")
	cliWrite(t, root, "probe.sh", "printf after")
	cliGit(t, root, "commit", "-qam", "intentional observation change")
	before := deliveryGeneration(t, root)
	for _, allow := range []int{0, 1} { // fail on the heading or on the diff itself.
		for _, mode := range []string{"error", "short"} {
			bad := &deliveryFault{mode: mode, allow: allow}
			if exit := Run([]string{"record", "--i-reviewed-the-diff"}, root, bad, io.Discard); exit != 2 {
				t.Errorf("allow=%d %s: undelivered review returned %d", allow, mode, exit)
			}
			if after := deliveryGeneration(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("undelivered review changed lock/blobs/journal")
			}
		}
	}
	if exit, out, err := cliRun(t, root, "record", "--i-reviewed-the-diff"); exit != 0 || !strings.Contains(out, "BEHAVIOR DIFF UNDER REVIEW") {
		t.Fatalf("delivered reviewed record: %d %s %s", exit, out, err)
	}
	if reflect.DeepEqual(before, deliveryGeneration(t, root)) {
		t.Fatal("successful review did not persist new generation")
	}
}

func deliveryGeneration(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range []string{"vise.lock", ".vise/blobs", ".vise/journal.jsonl"} {
		err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			result[rel] = string(data)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestUndeliveredRecordReceiptDoesNotUndoPersistence(t *testing.T) {
	root := cliRepo(t, basicManifest(""), "printf recorded")
	if exit := Run([]string{"record", "--json"}, root, &deliveryFault{mode: "short"}, io.Discard); exit != 2 {
		t.Fatalf("undelivered record returned %d", exit)
	}
	if _, err := os.Stat(filepath.Join(root, "vise.lock")); err != nil {
		t.Fatal(err)
	}
	if exit, out, err := cliRun(t, root, "gate", "--json"); exit != 0 {
		t.Fatalf("persisted generation unusable: %d %s %s", exit, out, err)
	}
}

func TestExecutableClosedResultPipeCannotSucceed(t *testing.T) {
	work := t.TempDir()
	binary := filepath.Join(work, "vise")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vise").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, args := range [][]string{{"version"}, {"version", "--json"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		command := exec.CommandContext(ctx, binary, args...)
		if out, err := command.Output(); err != nil || len(out) == 0 {
			cancel()
			t.Fatalf("positive executable: %v %s", err, out)
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		command = exec.CommandContext(ctx, binary, args...)
		command.Stdout = writer
		err = command.Run()
		writer.Close()
		cancel()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("closed pipe was not a process failure: %v", err)
		}
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if exitErr.ExitCode() != 2 && !(ok && status.Signaled() && status.Signal() == syscall.SIGPIPE) {
			t.Fatalf("unexpected failure (not delivery 2 or SIGPIPE): %v", err)
		}
	}
}
