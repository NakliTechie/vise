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
	// The three conditions below are the typed form of what HarnessError says
	// when a run did not complete: the shell could not launch the command,
	// the run was killed at its timeout, or a declared artifact was genuinely
	// absent afterwards. For a preserve probe every one of them is a harness
	// failure, as before. For a pin that no operator has accepted yet they are
	// what "not built yet" looks like, and the judge needs them told apart
	// from each other and from the hard conditions — a mutated checkout, a
	// write to evaluator state, a pipe holder left behind — which no phase of
	// any probe may produce. Tolerated is true only when HarnessError describes
	// nothing but one of the three; any hard condition clears it.
	LaunchFailed bool
	Terminated   bool
	MissingFiles []string
	Tolerated    bool
}

// hardHarnessError records a condition no probe may produce whatever its
// phase, appending it to any earlier failure so the cause is named first.
func (r *RunResult) hardHarnessError(detail string) {
	if r.HarnessError == "" {
		r.HarnessError = detail
	} else {
		r.HarnessError = r.HarnessError + "; and " + detail
	}
	r.Tolerated = false
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

	result := r.runShell("probe", probe.ID, probe.Run, probe.Timeout, probe.Env)

	// The work-tree check runs even when the probe already failed. A probe
	// that times out, or cannot be launched, or writes to evaluator state can
	// still have changed the checkout on its way down — and the probes after
	// it would then run against a tree this one left behind, which is the
	// order-dependence the snapshot exists to prevent. The earlier failure is
	// kept when there is one: it is the cause, and the mutation is a
	// consequence worth naming beside it.
	// Artifacts are inspected after a tolerated failure too. A run that timed
	// out or could not be launched can still have left a symlink or a fifo at
	// a declared artifact path, and the work-tree snapshot excludes those
	// paths by design; skipping the inspection would leave Tolerated true
	// beside a condition no probe may produce. The earlier failure stays the
	// cause; a hard condition found here is appended, and a missing artifact
	// is recorded without displacing it.
	if result.HarnessError == "" || result.Tolerated {
		files, missing, err := artifacts.capture()
		result.Files = files
		switch {
		case err != nil:
			result.hardHarnessError(err.Error())
		case len(missing) > 0:
			// Genuinely absent, and every artifact that was produced is kept
			// beside the list — a run that wrote two of three has two
			// observations worth comparing, and folding them into one string
			// hid a nondeterministic artifact behind a missing one.
			result.MissingFiles = missing
			if result.HarnessError == "" {
				result.HarnessError = fmt.Sprintf("declared artifact %q was not produced", missing[0])
				result.Tolerated = true
			}
		}
	}
	if checkTracked {
		mutation := ""
		after, err := GitWorkspaceSnapshot(r.Root, probe.Files)
		switch {
		case err != nil:
			mutation = err.Error()
		default:
			mutation = workspaceMutation("probe", before, after)
		}
		if mutation != "" {
			result.hardHarnessError(mutation)
		}
	}
	return result
}

func (r Runner) RunMetric(metric Metric) MetricResult {
	before, err := GitWorkspaceSnapshot(r.Root, nil)
	if err != nil {
		return MetricResult{HarnessError: err.Error()}
	}
	result := r.runShell("metric", metric.ID, metric.Run, metric.Timeout, metric.Env)
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
		vr := r.runShell("metric version command", metric.ID+"-version", metric.VersionCmd, metric.Timeout, metric.Env)
		if vr.HarnessError != "" {
			// The message already opens with "metric version command", because
			// the kind is threaded through runShell now — prefixing it again
			// said it twice. And the ownership travels with it: folding an
			// error into a string and dropping HarnessOperator is exactly the
			// bug that put fix_probe on eight operator-owned failures earlier
			// tonight. Latent here, since nothing reachable from runShell sets
			// the flag today, which is a property of the call sites and not of
			// this line.
			return MetricResult{Value: value, HarnessError: vr.HarnessError, HarnessOperator: vr.HarnessOperator}
		}
		if vr.Exit != 0 {
			detail := fmt.Sprintf("metric version command exited %d", vr.Exit)
			if line := firstNotFoundDiagnostic(vr.Stderr); line != "" {
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
	if mutation := workspaceMutation("metric", before, after); mutation != "" {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: mutation}
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
func (r Runner) runShell(kind, id, command string, timeoutSeconds int, extra map[string]string) RunResult {
	stateBefore, err := evaluatorStateDigest(r.Root)
	if err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	result := r.runShellUnguarded(kind, id, command, timeoutSeconds, extra)
	stateAfter, stateErr := evaluatorStateDigest(r.Root)
	if stateErr != nil {
		result.hardHarnessError(stateErr.Error())
		return result
	}
	if stateAfter != stateBefore {
		result.hardHarnessError(evaluatorStateMutated)
	}
	return result
}

func (r Runner) runShellUnguarded(kind, id, command string, timeoutSeconds int, extra map[string]string) RunResult {
	tmp, err := prepareProbeScratch(r.Root, id)
	if err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.Root
	cmd.Env = r.assembleProbeEnv(tmp, extra)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Own the output pipes so cmd.Wait reports process exit, not pipe drain.
	// exec.Cmd's internal copy wait otherwise consumes the execution timeout
	// after the parent has exited, and hides ErrWaitDelay behind nonzero exits.
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return RunResult{HarnessError: fmt.Sprintf("create stdout pipe: %v", err)}
	}
	defer outRead.Close()
	defer outWrite.Close()
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		return RunResult{HarnessError: fmt.Sprintf("create stderr pipe: %v", err)}
	}
	defer errRead.Close()
	defer errWrite.Close()
	stdout := newCaptureWriter(r.MirrorStdout)
	stderr := newCaptureWriter(r.MirrorStderr)
	cmd.Stdout = outWrite
	cmd.Stderr = errWrite
	if err := startProbe(cmd); err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	defer setActiveProbeGroup(0)
	// Sweep even redirected children, but only after the independent drain
	// check: killing pipe holders first would conceal the harness failure.
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = outWrite.Close()
	_ = errWrite.Close()
	outDone := copyProbeOutput(stdout, outRead)
	errDone := copyProbeOutput(stderr, errRead)

	waitErr, timedOut := awaitProbe(cmd, time.Duration(timeoutSeconds)*time.Second)
	pipeHeld, copyErr := awaitProbeOutput(outRead, errRead, outDone, errDone)

	result := classifyProbe(kind, timeoutSeconds, stdout, stderr, waitErr, timedOut)
	if pipeHeld {
		result.hardHarnessError("probe exited but left a background process holding its stdout or stderr; redirect that process to /dev/null or wait for it inside the probe")
	} else if copyErr != nil {
		result.hardHarnessError(fmt.Sprintf("capture probe output: %v", copyErr))
	}
	return result
}

func copyProbeOutput(dst io.Writer, src *os.File) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(dst, src)
		done <- err
	}()
	return done
}

// Drain both streams under one deadline, then join the readers before their
// captures are inspected. Closing our read ends bounds detached pipe holders.
func awaitProbeOutput(stdout, stderr *os.File, outDone, errDone <-chan error) (held bool, err error) {
	timer := time.NewTimer(pipeCloseDelay)
	defer timer.Stop()
	deadline := timer.C
	var outErr, errErr error
	for outDone != nil || errDone != nil {
		select {
		case outErr = <-outDone:
			outDone = nil
		case errErr = <-errDone:
			errDone = nil
		case <-deadline:
			held = true
			_ = stdout.Close()
			_ = stderr.Close()
			deadline = nil
		}
	}
	return held, errors.Join(outErr, errErr)
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

func classifyProbe(kind string, timeoutSeconds int, stdout, stderr *captureWriter, waitErr error, timedOut bool) RunResult {
	result := RunResult{Stdout: stdout.Capture(), Stderr: stderr.Capture(), TimedOut: timedOut}
	if timedOut {
		result.HarnessError = fmt.Sprintf("%s timed out after %ds", kind, timeoutSeconds)
		result.Tolerated = true
		return result
	}
	if waitErr == nil {
		result.Exit = 0
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.Exit = exitErr.ExitCode()
		if result.Exit < 0 {
			// Killed by a signal: Go reports -1, which is not an exit status a
			// probe can expect and was being frozen as one. It is a condition —
			// tolerated on a pin nobody has accepted, where a skeleton that
			// crashes is not built yet, and harness everywhere else. The -1
			// stays on the result: it is nonzero, it can never equal a pin's
			// expected exit, and the interrupt path asserts on it.
			result.Terminated = true
			result.Tolerated = true
			result.HarnessError = fmt.Sprintf("%s was terminated by a signal before it exited", kind)
			return result
		}
		if result.Exit == 127 {
			// Exit 127 retains the contract's launch-failure classification,
			// but the number alone cannot establish which program was absent.
			result.HarnessError = launchFailureDetail(kind, result.Stderr)
			result.LaunchFailed = true
			result.Tolerated = true
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

// awaitProbe waits only for the process; the caller owns output copying and
// the final group sweep. The execution deadline cannot be spent draining pipes.
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

// launchFailureDetail reports the observed status, not an inferred missing
// installation. Even shell-shaped stderr can be printed by an executable that
// ran and deliberately returned 127. Keep the useful excerpt as quoted data.
func launchFailureDetail(kind string, stderr Capture) string {
	if line := firstNotFoundDiagnostic(stderr); line != "" {
		return fmt.Sprintf("%s exited 127; captured stderr: %q; inspect the command's exit handling and dependencies", kind, line)
	}
	return fmt.Sprintf("%s exited 127; inspect the command's exit handling and dependencies", kind)
}

// firstNotFoundDiagnostic selects a useful captured line, not its author.
// The wording match is an excerpt heuristic, never evidence of shell origin.
func firstNotFoundDiagnostic(stderr Capture) string {
	for _, line := range strings.Split(string(stderr.Prefix), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, "not found") && !strings.Contains(line, "No such file") {
			continue
		}
		if shown := []rune(line); len(shown) > 200 {
			line = string(shown[:200]) + "…"
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
	if line := firstNotFoundDiagnostic(result.Stderr); line != "" {
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

// gitStateMutated is the one wording for one condition. There were three: the
// probe's carried the explanation, the metric's dropped it, and the
// fingerprint's said only that it happened. The reader needs the explanation
// most when the thing that did it is the least expected, and a fingerprint
// command modifying HEAD is about as unexpected as it gets.
func gitStateMutated(kind string) string {
	return kind + " modified git's own state (HEAD, the ignore rules, or the config); the checkout is judged against those, so changing them changes what unchanged means"
}

// workspaceMutation reports how a command changed the checkout, or "" if it did
// not. Git's own state first, then tracked files, then strays — the order is
// most-general to most-specific, so the message names the largest thing that
// moved.
//
// One copy. It was written out twice, in RunProbe and RunMetric, with the same
// order and the same predicates, differing only in the noun. This is the
// comparison the whole tool rests on: whatever the snapshot comes to mean, it
// has to mean the same thing for both, and two copies is how that stops being
// true one edit at a time.
func workspaceMutation(kind string, before, after WorkspaceSnapshot) string {
	switch {
	case before.Git != after.Git:
		return gitStateMutated(kind)
	case before.Tracked != after.Tracked:
		return kind + " modified tracked files"
	}
	if stray := before.ChangedUntracked(after); len(stray) > 0 {
		return strayFilesError(kind, stray)
	}
	return ""
}
