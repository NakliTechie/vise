package vise

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type RunResult struct {
	Exit         int
	Stdout       []byte
	Stderr       []byte
	Files        map[string][]byte
	TimedOut     bool
	HarnessError string
}

type MetricResult struct {
	Value        float64
	ToolVersion  string
	Stdout       []byte
	Stderr       []byte
	HarnessError string
}

type Runner struct {
	Root     string
	Manifest Manifest
}

// pipeCloseDelay bounds how long a finished or killed probe may keep its
// output pipes open through a process it left behind.
const pipeCloseDelay = time.Second

func (r Runner) RunProbe(probe Probe, checkTracked bool) RunResult {
	var before string
	if checkTracked {
		var err error
		before, err = GitTrackedSnapshot(r.Root)
		if err != nil {
			return RunResult{HarnessError: err.Error()}
		}
	}
	tracked, err := GitTrackedPaths(r.Root, probe.Files)
	if err != nil {
		return RunResult{HarnessError: err.Error()}
	}
	if len(tracked) > 0 {
		return RunResult{HarnessError: fmt.Sprintf("declared artifact %q is tracked by git; artifacts must be gitignored build outputs because vise deletes them before every run", tracked[0])}
	}
	for _, rel := range probe.Files {
		if err := ValidateArtifactPath(r.Root, rel); err != nil {
			return RunResult{HarnessError: fmt.Sprintf("artifact %q: %v", rel, err)}
		}
		path := filepath.Join(r.Root, rel)
		if info, err := os.Lstat(path); err == nil && info.IsDir() {
			return RunResult{HarnessError: fmt.Sprintf("declared artifact %q is a directory; recursive deletion is refused", rel)}
		} else if err != nil && !os.IsNotExist(err) {
			return RunResult{HarnessError: fmt.Sprintf("inspect artifact %q: %v", rel, err)}
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return RunResult{HarnessError: fmt.Sprintf("delete artifact %q: %v", rel, err)}
		}
	}

	result := r.runShell(probe.ID, probe.Run, probe.Timeout, probe.Env)
	if result.HarnessError != "" {
		return result
	}
	result.Files = make(map[string][]byte, len(probe.Files))
	for _, rel := range probe.Files {
		if err := ValidateArtifactPath(r.Root, rel); err != nil {
			result.HarnessError = fmt.Sprintf("artifact %q after probe: %v", rel, err)
			return result
		}
		path := filepath.Join(r.Root, rel)
		info, err := os.Lstat(path)
		if err != nil {
			result.HarnessError = fmt.Sprintf("declared artifact %q was not produced", rel)
			return result
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			result.HarnessError = fmt.Sprintf("declared artifact %q is not a regular file", rel)
			return result
		}
		data, err := os.ReadFile(path)
		if err != nil {
			result.HarnessError = fmt.Sprintf("read artifact %q: %v", rel, err)
			return result
		}
		result.Files[filepath.ToSlash(filepath.Clean(rel))] = data
	}
	if checkTracked {
		after, err := GitTrackedSnapshot(r.Root)
		if err != nil {
			result.HarnessError = err.Error()
			return result
		}
		if before != after {
			result.HarnessError = "probe modified tracked files"
		}
	}
	return result
}

func (r Runner) RunMetric(metric Metric) MetricResult {
	before, err := GitTrackedSnapshot(r.Root)
	if err != nil {
		return MetricResult{HarnessError: err.Error()}
	}
	result := r.runShell(metric.ID, metric.Run, metric.Timeout, metric.Env)
	if result.HarnessError != "" {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: result.HarnessError}
	}
	if result.Exit != 0 {
		return MetricResult{Stdout: result.Stdout, Stderr: result.Stderr, HarnessError: fmt.Sprintf("metric exited %d", result.Exit)}
	}
	text := strings.TrimSpace(string(result.Stdout))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || strings.ContainsAny(text, "\r\n \t") {
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
		version = strings.TrimSpace(string(vr.Stdout))
		if version == "" {
			return MetricResult{Value: value, HarnessError: "metric version command returned empty output"}
		}
	}
	after, err := GitTrackedSnapshot(r.Root)
	if err != nil {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: err.Error()}
	}
	if before != after {
		return MetricResult{Value: value, ToolVersion: version, HarnessError: "metric modified tracked files"}
	}
	return MetricResult{Value: value, ToolVersion: version, Stdout: result.Stdout, Stderr: result.Stderr}
}

func (r Runner) runShell(id, command string, timeoutSeconds int, extra map[string]string) RunResult {
	// VISE_TMP lives under .vise/tmp inside the repository: init ignores it,
	// the dirty-tree check skips it, and it is wiped after every run, so a
	// crash leaves residue where the operator expects state, not in /tmp.
	scratchRoot := filepath.Join(r.Root, ".vise", "tmp")
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
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
	// pipes shortly after the shell exits or the timeout kill lands.
	cmd.WaitDelay = pipeCloseDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return RunResult{HarnessError: fmt.Sprintf("launch probe: %v", err)}
	}
	setActiveProbeGroup(cmd.Process.Pid)
	defer setActiveProbeGroup(0)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timer.Stop()
	var waitErr error
	timedOut := false
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

	result := RunResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), TimedOut: timedOut}
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
