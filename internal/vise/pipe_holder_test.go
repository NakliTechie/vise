package vise

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExitedProbePipeHolderIsHardRegardlessOfExit(t *testing.T) {
	for _, timeout := range []int{1, 5} {
		for _, exit := range []int{0, 42, 127} {
			t.Run(fmt.Sprintf("timeout%d-exit%d", timeout, exit), func(t *testing.T) {
				root := testGitRepo(t)
				probe := Probe{ID: "holder", Run: fmt.Sprintf("sleep 20 & printf ran; exit %d", exit), Timeout: timeout}
				got := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, true)
				if got.TimedOut || got.Tolerated || got.Exit != exit || !strings.Contains(got.HarnessError, "background process") {
					t.Fatalf("exited parent must retain exit %d and a hard pipe error: %#v", exit, got)
				}
				if string(got.Stdout.Prefix) != "ran" {
					t.Fatalf("lost parent output: %#v", got.Stdout)
				}
			})
		}
	}
}

func TestPipeHolderOnEitherStreamIsKilledBeforeReturning(t *testing.T) {
	for _, redirect := range []string{"2>/dev/null", ">/dev/null"} {
		t.Run(redirect, func(t *testing.T) {
			root := testGitRepo(t)
			probe := Probe{ID: "one-pipe", Run: "sleep 20 " + redirect + " & echo $! > child.pid; exit 42", Timeout: 5}
			got := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
			if got.Tolerated || got.TimedOut || got.Exit != 42 || !strings.Contains(got.HarnessError, "background process") {
				t.Fatalf("single-stream holder escaped detection: %#v", got)
			}
			data, err := os.ReadFile(filepath.Join(root, "child.pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				if time.Now().After(deadline) {
					t.Fatalf("pipe holder %d survived the final group sweep", pid)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

type failingProbeMirror struct{}

func (failingProbeMirror) Write(p []byte) (int, error) {
	return 0, errors.New("mirror refused output")
}

func TestOutputCopyErrorRemainsHardBesideExit127(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "copy-error", Run: "printf ran; exit 127", Timeout: 5}
	got := (Runner{Root: root, Manifest: testManifest(probe), MirrorStdout: failingProbeMirror{}}).RunProbe(probe, false)
	if got.Tolerated || got.Exit != 127 || !strings.Contains(got.HarnessError, "mirror refused output") {
		t.Fatalf("copy failure must not disappear behind exit 127: %#v", got)
	}
}

func TestUnacceptedPinDistinguishesPipeHolderFromLiveTimeout(t *testing.T) {
	for _, run := range []string{"sleep 20 & printf ran; exit 127", "sleep 20 & wait"} {
		t.Run(run, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.mode\n")
			writeTestFile(t, root, "spec/out", "wanted\n")
			writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"pin\"\nrun = \"if test -f .mode; then "+run+"; else exit 127; fi\"\ntimeout = 1\nexpect.stdout = \"spec/out\"\n")
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "pin and execution control")
			manifest, data, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			recordPinRepo(t, root, manifest, data)
			writeTestFile(t, root, ".mode", "run")
			got := Verify(root, manifest, data, VerifyOptions{}).Outcome
			if strings.Contains(run, "exit 127") {
				if got.Exit != ExitHarness || got.Next.Action != NextFixProbe || !strings.Contains(got.Failures["pin"].Detail, "background process") {
					t.Fatalf("pipe holder was tolerated: %#v", got)
				}
			} else if got.Exit != ExitUnmet || got.Next.Action != NextBuild || !strings.Contains(got.Failures["pin"].Detail, "timed out") {
				t.Fatalf("live-parent timeout must remain unmet: %#v", got)
			}
		})
	}
}
