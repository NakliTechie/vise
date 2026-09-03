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
	// HarnessOperator marks a harness error whose repair is in a file the
	// agent contract forbids an agent from writing. It travels with the result
	// rather than being recovered from the message, because matching on a
	// message is what the contract tells agents not to do, and a guard that
	// scanned literal strings could not see an error built at run time.
	HarnessOperator bool
}

type MetricResult struct {
	Value           float64
	ToolVersion     string
	Stdout          Capture
	Stderr          Capture
	HarnessError    string
	HarnessOperator bool
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
	artifacts := newDeclaredArtifacts(r.Root, probe.Files)
	if err := artifacts.reset(); err != nil {
		return RunResult{HarnessError: err.Error(), HarnessOperator: true}
	}

	result := r.runShell(probe.ID, probe.Run, probe.Timeout, probe.Env)

	// The work-tree check runs even when the probe already failed. A probe
	// that times out, or cannot be launched, or writes to evaluator state can
	// still have changed the checkout on its way down — and the probes after
	// it would then run against a tree this one left behind, which is the
	// order-dependence the snapshot exists to prevent. The earlier failure is
	// kept when there is one: it is the cause, and the mutation is a
	// consequence worth naming beside it.
	if result.HarnessError == "" {
		files, err := artifacts.capture()
		if err != nil {
			result.HarnessError = err.Error()
		} else {
			result.Files = files
		}
	}
	if checkTracked {
		mutation := ""
		after, err := GitWorkspaceSnapshot(r.Root, probe.Files)
		switch {
		case err != nil:
			mutation = err.Error()
		case before.Git != after.Git:
			mutation = "probe modified git's own state (HEAD, the ignore rules, or the config); the checkout is judged against those, so changing them changes what unchanged means"
		case before.Tracked != after.Tracked:
			mutation = "probe modified tracked files"
		default:
			if stray := before.ChangedUntracked(after); len(stray) > 0 {
				mutation = strayFilesError("probe", stray)
			}
		}
		if mutation != "" {
			if result.HarnessError == "" {
				result.HarnessError = mutation
			} else {
				result.HarnessError = result.HarnessError + "; and " + mutation
			}
		}
	}
	return result
}

func (r Runner) RunMetric(metric Metric) MetricResult {
	before, err := GitWorkspaceSnapshot(r.Root, nil)
	if err != nil {
		return MetricResult{HarnessError: err.Error()}
	}
	result := r.runShell(metric.ID, metric.Run, metric.Timeout, metric.Env)
	if result.HarnessError != "" {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: result.HarnessError, HarnessOperator: result.HarnessOperator}
	}
	if result.Exit != 0 {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: metricExitDetail(result)}
	}
	text := strings.TrimSpace(string(result.Stdout.Prefix))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || result.Stdout.Truncated() || strings.ContainsAny(text, "\r\n \t") {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: metricNumberDetail(text, result.Stdout.Truncated())}
	}
	if value != value || value > 1.7976931348623157e+308 || value < -1.7976931348623157e+308 {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: metricNumberDetail(text, false)}
	}
	version := ""
	if metric.VersionCmd != "" {
		vr := r.runShell(metric.ID+"-version", metric.VersionCmd, metric.Timeout, metric.Env)
		if vr.HarnessError != "" {
			return MetricResult{Value: value, HarnessError: "metric version command: " + vr.HarnessError}
		}
		if vr.Exit != 0 {
			detail := fmt.Sprintf("metric version command exited %d", vr.Exit)
			if line := firstShellDiagnostic(vr.Stderr); line != "" {
				detail += ": " + line
			}
			return MetricResult{Value: value, HarnessError: detail}
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
	if before.Git != after.Git {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: "metric modified git's own state (HEAD, the ignore rules, or the config)"}
	}
	if before.Tracked != after.Tracked {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: "metric modified tracked files"}
	}
	if stray := before.ChangedUntracked(after); len(stray) > 0 {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: strayFilesError("metric", stray)}
	}
	return MetricResult{Value: value, ToolVersion: version, Stdout: result.Stdout, Stderr: result.Stderr}
}

// runShell executes one manifest-declared command and refuses any run that
// touched the evaluator's own state.
//
// The check used to live in the callers, and two of them did not have it: the
// environment fingerprint commands had none at all, and a metric's version_cmd
// ran after RunMetric had already compared the state. Either could have
// deleted .vise/journal.jsonl, which is where the flake history and the rerun
// budget live — so the property that an agent cannot eject the judge by making
// a probe flaky had a hole behind it. A guard that each caller must remember
// is a guard that some caller will forget, so it lives here now, around every
// command the manifest can name.
func (r Runner) runShell(id, command string, timeoutSeconds int, extra map[string]string) RunResult {
	stateBefore, err := evaluatorStateDigest(r.Root)
	if err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	result := r.runShellUnguarded(id, command, timeoutSeconds, extra)
	stateAfter, stateErr := evaluatorStateDigest(r.Root)
	if stateErr != nil {
		result.HarnessError = stateErr.Error()
		return result
	}
	if stateAfter != stateBefore {
		result.HarnessError = evaluatorStateMutated
	}
	return result
}

func (r Runner) runShellUnguarded(id, command string, timeoutSeconds int, extra map[string]string) RunResult {
	tmp, err := prepareProbeScratch(r.Root, id)
	if err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.Root
	cmd.Env = r.assembleProbeEnv(tmp, extra)
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
	if err := startProbe(cmd); err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	defer setActiveProbeGroup(0)

	waitErr, timedOut := awaitProbe(cmd, time.Duration(timeoutSeconds)*time.Second)

	return classifyProbe(command, timeoutSeconds, cmd, stdout, stderr, waitErr, timedOut)
}

func startProbe(cmd *exec.Cmd) error {
	probeLifecycle.Lock()
	if interrupted.Load() {
		probeLifecycle.Unlock()
		return errors.New("vise was interrupted before the probe started")
	}
	if probeAboutToStart != nil {
		probeAboutToStart()
	}
	if err := cmd.Start(); err != nil {
		probeLifecycle.Unlock()
		return fmt.Errorf("launch probe: %v", err)
	}
	setActiveProbeGroup(cmd.Process.Pid)
	probeLifecycle.Unlock()
	return nil
}

func classifyProbe(command string, timeoutSeconds int, cmd *exec.Cmd, stdout, stderr *captureWriter, waitErr error, timedOut bool) RunResult {
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
			// Name the word the shell could not resolve. "could not be
			// launched" tells the reader something failed; the missing tool
			// tells them what to install, and the whole point of these
			// messages is that the remedy arrives with the failure.
			result.HarnessError = launchFailureDetail(command, result.Stderr)
		}
		return result
	}
	result.HarnessError = fmt.Sprintf("wait for probe: %v", waitErr)
	return result
}

func prepareProbeScratch(root, id string) (string, error) {
	// VISE_TMP lives under .vise/tmp inside the repository: init ignores it,
	// the dirty-tree check skips it, and it is wiped after every run, so a
	// crash leaves residue where the operator expects state, not in /tmp.
	scratchRoot, err := stateScratchDir(root)
	if err != nil {
		return "", fmt.Errorf("create VISE_TMP: %v", err)
	}
	tmp, err := os.MkdirTemp(scratchRoot, sanitizeTempName(id)+"-")
	if err != nil {
		return "", fmt.Errorf("create VISE_TMP: %v", err)
	}
	return tmp, nil
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

func (r Runner) assembleProbeEnv(tmp string, extra map[string]string) []string {
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

// launchFailureDetail explains an exit 127. The shell already says which word
// it could not find, so that line is quoted when it is there; the first word of
// the command is the fallback, since it is what the reader will go and look
// for either way.
func launchFailureDetail(command string, stderr Capture) string {
	word := command
	if fields := strings.Fields(command); len(fields) > 0 {
		word = fields[0]
	}
	// The remedy goes on both branches. It used to appear only when the shell
	// said nothing useful, so the case where the shell *did* speak — the common
	// one — lost the sentence telling the reader what to do about it. The
	// README's claim that a missing tool is "exit 2 with the remedy in the
	// message" was true of the rarer half.
	remedy := fmt.Sprintf("install %s, or name it by an absolute path", word)
	if line := firstShellDiagnostic(stderr); line != "" {
		return fmt.Sprintf("probe command could not be launched (exit 127): %s; %s", line, remedy)
	}
	return fmt.Sprintf("probe command could not be launched (exit 127): %q is not on the probe's PATH; %s", word, remedy)
}

// firstShellDiagnostic returns the shell's own not-found line, bounded, or "".
func firstShellDiagnostic(stderr Capture) string {
	for _, line := range strings.Split(string(stderr.Prefix), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, "not found") && !strings.Contains(line, "No such file") {
			continue
		}
		if len(line) > 200 {
			line = line[:200] + "…"
		}
		return line
	}
	return ""
}

// harnessFailure is the one conversion from a run's harness error to the
// failure an outcome carries. Eight call sites built this struct by hand and
// every one of them dropped the ownership flag, so the gate answered fix_probe
// — "repair the probe your change broke" — for a tracked declared artifact the
// agent had not touched and could not fix.
func (r RunResult) harnessFailure() Failure {
	return Failure{Class: "harness", Detail: r.HarnessError, Operator: r.HarnessOperator}
}

func (m MetricResult) harnessFailure() Failure {
	return Failure{Class: "harness", Detail: m.HarnessError, Operator: m.HarnessOperator}
}

// metricExitDetail names what the analyzer said, not only that it failed.
//
// "metric exited 1" was the whole message. A probe that cannot launch names the
// missing tool; a metric that cannot run named nothing, and the design rule the
// project states is that every failure names its remedy. The most common cause
// is an input the refactor moved or deleted, and the shell says so on stderr in
// the line this picks out.
func metricExitDetail(result RunResult) string {
	detail := fmt.Sprintf("metric exited %d", result.Exit)
	if line := firstShellDiagnostic(result.Stderr); line != "" {
		return detail + ": " + line
	}
	return detail
}

// metricNumberDetail shows what arrived instead of a number. An analyzer that
// prints a warning line first, or a table, or nothing at all, produced the same
// sentence as one that printed "NaN", and the author had to rerun the command
// by hand to see which.
func metricNumberDetail(text string, truncated bool) string {
	const rule = "metric must print exactly one finite number"
	switch {
	case truncated:
		return rule + "; it printed more than the capture bound"
	case text == "":
		return rule + "; it printed nothing"
	}
	shown := text
	if len(shown) > 80 {
		shown = shown[:80] + "…"
	}
	return fmt.Sprintf("%s; it printed %q", rule, shown)
}
