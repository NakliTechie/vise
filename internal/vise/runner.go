package vise

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type RunResult struct {
	Exit         int
	Stdout       Capture
	Stderr       Capture
	Files        map[string]Capture
	TimedOut     bool
	HarnessError string
}

type MetricResult struct {
	Value        float64
	ToolVersion  string
	Stdout       Capture
	Stderr       Capture
	HarnessError string
}

type Runner struct {
	Root     string
	Manifest Manifest
	// MirrorStdout and MirrorStderr receive every byte a probe writes, in
	// order, as it is produced. `vise run` sets them so raw execution prints
	// the probe's complete output while vise itself keeps only a bounded
	// capture. Judgment paths leave them nil.
	MirrorStdout io.Writer
	MirrorStderr io.Writer
}

// pipeCloseDelay bounds how long a finished or killed probe may keep its
// output pipes open through a process it left behind.
const pipeCloseDelay = time.Second

func (r Runner) RunProbe(probe Probe, checkTracked bool) RunResult {
	var before WorkspaceSnapshot
	if checkTracked {
		var err error
		before, err = GitWorkspaceSnapshot(r.Root, probe.Files)
		if err != nil {
			return RunResult{HarnessError: err.Error()}
		}
	}
	stateBefore, err := evaluatorStateDigest(r.Root)
	if err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	artifacts := newDeclaredArtifacts(r.Root, probe.Files)
	if err := artifacts.reset(); err != nil {
		return RunResult{HarnessError: err.Error()}
	}

	result := r.runShell(probe.ID, probe.Run, probe.Timeout, probe.Env)
	if result.HarnessError != "" {
		return result
	}
	if stateAfter, err := evaluatorStateDigest(r.Root); err != nil {
		result.HarnessError = err.Error()
		return result
	} else if stateAfter != stateBefore {
		result.HarnessError = evaluatorStateMutated
		return result
	}
	files, err := artifacts.capture()
	if err != nil {
		result.HarnessError = err.Error()
		return result
	}
	result.Files = files
	if checkTracked {
		after, err := GitWorkspaceSnapshot(r.Root, probe.Files)
		if err != nil {
			result.HarnessError = err.Error()
			return result
		}
		if before.Tracked != after.Tracked {
			result.HarnessError = "probe modified tracked files"
		} else if stray := before.ChangedUntracked(after); len(stray) > 0 {
			result.HarnessError = strayFilesError("probe", stray)
		}
	}
	return result
}

func (r Runner) RunMetric(metric Metric) MetricResult {
	before, err := GitWorkspaceSnapshot(r.Root, nil)
	if err != nil {
		return MetricResult{HarnessError: err.Error()}
	}
	stateBefore, err := evaluatorStateDigest(r.Root)
	if err != nil {
		return MetricResult{HarnessError: err.Error()}
	}
	result := r.runShell(metric.ID, metric.Run, metric.Timeout, metric.Env)
	if result.HarnessError != "" {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: result.HarnessError}
	}
	if stateAfter, err := evaluatorStateDigest(r.Root); err != nil {
		return MetricResult{HarnessError: err.Error()}
	} else if stateAfter != stateBefore {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: evaluatorStateMutated}
	}
	if result.Exit != 0 {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: fmt.Sprintf("metric exited %d", result.Exit)}
	}
	text := strings.TrimSpace(string(result.Stdout.Prefix))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || result.Stdout.Truncated() || strings.ContainsAny(text, "\r\n \t") {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: "metric must print exactly one finite number"}
	}
	if value != value || value > 1.7976931348623157e+308 || value < -1.7976931348623157e+308 {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: "metric must print exactly one finite number"}
	}
	version := ""
	if metric.VersionCmd != "" {
		vr := r.runShell(metric.ID+"-version", metric.VersionCmd, metric.Timeout, metric.Env)
		if vr.HarnessError != "" {
			return MetricResult{Value: value, HarnessError: "metric version command: " + vr.HarnessError}
		}
		if vr.Exit != 0 {
			return MetricResult{Value: value, HarnessError: fmt.Sprintf("metric version command exited %d", vr.Exit)}
		}
		version = strings.TrimSpace(string(vr.Stdout.Prefix))
		if version == "" || vr.Stdout.Truncated() {
			return MetricResult{Value: value, HarnessError: "metric version command returned empty output"}
		}
	}
	after, err := GitWorkspaceSnapshot(r.Root, nil)
	if err != nil {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: err.Error()}
	}
	if before.Tracked != after.Tracked {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: "metric modified tracked files"}
	}
	if stray := before.ChangedUntracked(after); len(stray) > 0 {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: strayFilesError("metric", stray)}
	}
	return MetricResult{Value: value, ToolVersion: version, Stdout: result.Stdout, Stderr: result.Stderr}
}

func (r Runner) runShell(id, command string, timeoutSeconds int, extra map[string]string) RunResult {
	// VISE_TMP lives under .vise/tmp inside the repository: init ignores it,
	// the dirty-tree check skips it, and it is wiped after every run, so a
	// crash leaves residue where the operator expects state, not in /tmp.
	scratchRoot, err := stateScratchDir(r.Root)
	if err != nil {
		return RunResult{HarnessError: fmt.Sprintf("create VISE_TMP: %v", err)}
	}
	tmp, err := os.MkdirTemp(scratchRoot, sanitizeTempName(id)+"-")
	if err != nil {
		return RunResult{HarnessError: fmt.Sprintf("create VISE_TMP: %v", err)}
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.Root
	cmd.Env = r.sanitizedEnv(tmp, extra)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Wait blocks until every holder of the stdout/stderr pipes exits. A probe
	// that leaves a detached process (setsid, a daemon, a preloader) holding
	// them would otherwise hang vise past its timeout. WaitDelay closes the
	// pipes shortly after the shell exits or the timeout kill lands. It is set
	// here, with the rest of the process configuration and before Start,
	// because Start also consults it to arm the context watchdog: that path is
	// dormant only for as long as this command carries no context, and a field
	// whose correctness depends on an unstated invariant is a trap for whoever
	// switches to exec.CommandContext.
	cmd.WaitDelay = pipeCloseDelay
	stdout := newCaptureWriter(r.MirrorStdout)
	stderr := newCaptureWriter(r.MirrorStderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	probeLifecycle.Lock()
	if interrupted.Load() {
		probeLifecycle.Unlock()
		return RunResult{HarnessError: "vise was interrupted before the probe started"}
	}
	if err := cmd.Start(); err != nil {
		probeLifecycle.Unlock()
		return RunResult{HarnessError: fmt.Sprintf("launch probe: %v", err)}
	}
	setActiveProbeGroup(cmd.Process.Pid)
	probeLifecycle.Unlock()
	defer setActiveProbeGroup(0)

	waitErr, timedOut := awaitProbe(cmd, time.Duration(timeoutSeconds)*time.Second)

	result := RunResult{Stdout: stdout.Capture(), Stderr: stderr.Capture(), TimedOut: timedOut}
	if timedOut {
		result.HarnessError = fmt.Sprintf("probe timed out after %ds", timeoutSeconds)
		return result
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		result.Exit = cmd.ProcessState.ExitCode()
		result.HarnessError = "probe exited but left a background process holding its stdout or stderr; redirect that process to /dev/null or wait for it inside the probe"
		return result
	}
	if waitErr == nil {
		result.Exit = 0
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.Exit = exitErr.ExitCode()
		if result.Exit == 127 {
			result.HarnessError = "probe command could not be launched (exit 127)"
		}
		return result
	}
	result.HarnessError = fmt.Sprintf("wait for probe: %v", waitErr)
	return result
}

// awaitProbe waits for a started probe, kills its process group when the
// timeout lands, and sweeps the group again once the shell is gone.
func awaitProbe(cmd *exec.Cmd, timeout time.Duration) (waitErr error, timedOut bool) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case waitErr = <-done:
	case <-timer.C:
		timedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-done
	}
	// The shell has exited (or been killed). Anything it left behind in the
	// process group — a redirected background child, a pipe holder — must not
	// outlive the run: it could keep writing artifacts or tracked files after
	// the tracked-tree check, or hold the next run's state.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	return waitErr, timedOut
}

func (r Runner) sanitizedEnv(tmp string, extra map[string]string) []string {
	values := map[string]string{
		"PATH":              os.Getenv("PATH"),
		"HOME":              os.Getenv("HOME"),
		"TZ":                r.Manifest.Stubs.TZ,
		"LANG":              r.Manifest.Stubs.Lang,
		"LC_ALL":            r.Manifest.Stubs.Lang,
		"VISE_SEED":         r.Manifest.Stubs.Seed,
		"SOURCE_DATE_EPOCH": "0",
		"VISE":              "1",
		"PYTHONHASHSEED":    "0",
		"NO_COLOR":          "1",
		"TERM":              "dumb",
		"COLUMNS":           "80",
		"CI":                "1",
		"VISE_TMP":          tmp,
		// Every tool that reaches for a temp directory lands in the probe's own
		// scratch, which is wiped after the run. Left unpinned, TMPDIR resolves
		// against the host: inside an agent sandbox macOS cannot resolve it and
		// git prints a warning into the observation, so the same probe is green
		// in a terminal and red in a sandbox.
		"TMPDIR": tmp,
	}
	for key, value := range extra {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func sanitizeTempName(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
	if value == "" {
		return "probe"
	}
	return value
}

// strayFilesError names the files a run left in the checkout. It names at
// most three: the agent reading this needs to know which write to remove, and
// an unbounded list of a thousand stray files is a wall of text carrying the
// same one instruction.
func strayFilesError(kind string, paths []string) string {
	named := paths
	suffix := ""
	if len(named) > 3 {
		named = named[:3]
		suffix = fmt.Sprintf(" (and %d more)", len(paths)-3)
	}
	return fmt.Sprintf("%s wrote files git neither tracks nor ignores: %s%s; a %s may write only its declared files and $VISE_TMP, or the operator must ignore these paths",
		kind, strings.Join(named, ", "), suffix, kind)
}
