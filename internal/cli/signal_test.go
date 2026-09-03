package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The signal handler is the reason an interrupted vise does not leave its
// probe running. Nothing exercised it: Main, main, and stopProbeOnSignal were
// the only functions in the module with no coverage at all, and disabling the
// interrupt entirely left the suite green.
//
// It cannot be tested in-process — the handler calls os.Exit — so this builds
// the binary, starts a probe, and sends it a real signal.
//
// Note for anyone reading a coverage report: Main, main and
// stopProbeOnSignal will still show 0%. `go test -cover` instruments the
// test binary, and these tests exercise a separately built one, so the
// execution is real and invisible to the profile. The check that they are
// covered is the mutation, not the percentage: disabling the interrupt call
// makes both tests below fail.
func TestInterruptingViseStopsTheProbeItStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary and waits on a signal")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}

	work := t.TempDir()
	binary := filepath.Join(work, "vise")
	build := exec.Command(goTool, "build", "-o", binary, "../../cmd/vise")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build vise: %v\n%s", err, output)
	}

	// A probe that outlives the signal if nothing kills it, and leaves proof.
	root := cliRepo(t, basicManifest(""), "sleep 5; printf survived > survived.txt")

	command := exec.Command(binary, "run", "behavior")
	command.Dir = root
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	// Long enough for the probe to be running, short enough to be inside it.
	time.Sleep(1500 * time.Millisecond)
	if err := command.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	err = command.Wait()
	exit := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	// 128 + SIGINT, the convention a shell reports for a signalled exit.
	if exit != 130 {
		t.Fatalf("vise exited %d after SIGINT, want 130", exit)
	}

	// Well past the probe's own sleep. The file appearing means the probe
	// outlived the vise that started it and kept writing into a checkout
	// nobody is watching.
	time.Sleep(5 * time.Second)
	if _, err := os.Stat(filepath.Join(root, "survived.txt")); err == nil {
		t.Fatal("the probe kept running after vise was interrupted, and wrote its file")
	}
}

// SIGTERM takes the same path and reports its own code.
func TestTerminatingViseReportsTheSignalItReceived(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary and waits on a signal")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}

	work := t.TempDir()
	binary := filepath.Join(work, "vise")
	if output, err := exec.Command(goTool, "build", "-o", binary, "../../cmd/vise").CombinedOutput(); err != nil {
		t.Fatalf("build vise: %v\n%s", err, output)
	}

	root := cliRepo(t, basicManifest(""), "sleep 5; printf survived > survived.txt")
	command := exec.Command(binary, "run", "behavior")
	command.Dir = root
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	exit := 0
	if err := command.Wait(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		exit = exitErr.ExitCode()
	}
	if exit != 143 {
		t.Fatalf("vise exited %d after SIGTERM, want 143", exit)
	}
	if strings.TrimSpace(root) == "" {
		t.Fatal("unreachable")
	}
}
