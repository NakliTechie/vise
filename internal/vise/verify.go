package vise

import (
	"fmt"
	"os"
	"sort"
)

type VerifyOptions struct {
	ProbeID           string
	EnforceRerunLimit bool
}

type VerifyResult struct {
	Outcome  Outcome
	Flaky    []string
	CheckSet []string
	Commit   string
	Dirty    bool
	// RerunRefused marks a verify that never ran because the rerun limit
	// blocked it; such a result is not a judgment and must not be journaled.
	RerunRefused bool
}

func Verify(root string, manifest Manifest, manifestBytes []byte, opts VerifyOptions) VerifyResult {
	outcome := NewOutcome("verify")
	result := VerifyResult{Outcome: outcome}
	lock, lockBytes, err := LoadLockfile(root)
	if os.IsNotExist(err) {
		outcome.Exit = ExitNotInitialized
		outcome.Counts.Declared = len(manifest.Probes) + len(manifest.Metrics)
		outcome.Finalize()
		result.Outcome = outcome
		return result
	}
	if err != nil {
		result.Outcome = harnessOnly("verify", "vise.lock", err.Error())
		return result
	}
	lockHash, err := TamperHash(root, manifestBytes, lockBytes)
	if err != nil {
		result.Outcome = harnessOnly("verify", "tamper-hash", err.Error())
		return result
	}
	outcome.Lock = lockHash
	commit, err := GitHead(root)
	if err != nil {
		result.Outcome = harnessOnly("verify", "git", err.Error())
		return result
	}
	dirty, err := GitDirty(root)
	if err != nil {
		result.Outcome = harnessOnly("verify", "git", err.Error())
		return result
	}
	result.Commit = commit
	result.Dirty = dirty

	if len(manifest.Probes) == 0 {
		// Green requires every declared probe to pass; with none declared there
		// is nothing to judge, and a 0/0 green would be a verdict without a judge.
		result.Outcome = harnessWithNext("verify", "manifest", "manifest declares no [[probe]]; nothing can be judged", "fix_probe", "declare at least one probe in vise.toml and record a baseline")
		return result
	}
	selected, selectionFailure := selectedProbes(manifest, opts.ProbeID)
	if selectionFailure != nil {
		result.Outcome = harnessOnly("verify", "probe", selectionFailure.Error())
		return result
	}
	outcome.Counts.Declared = len(selected)
	probeIDs := make([]string, 0, len(selected))
	for _, probe := range selected {
		probeIDs = append(probeIDs, probe.ID)
	}
	if opts.ProbeID == "" {
		// Metrics are judged checks too: they count in the denominator so a
		// failing metric lowers pass instead of hiding behind the probe count.
		outcome.Counts.Declared += len(manifest.Metrics)
		for _, metric := range manifest.Metrics {
			probeIDs = append(probeIDs, metric.ID)
		}
	}
	sort.Strings(probeIDs)
	result.CheckSet = append([]string(nil), probeIDs...)

	if opts.EnforceRerunLimit {
		refused, detail, err := RerunLimitReached(root, commit, lockHash, probeIDs)
		if err != nil {
			result.Outcome = harnessOnly("verify", "journal", err.Error())
			return result
		}
		if refused {
			blocked := harnessOnly("verify", "rerun-limit", detail)
			blocked.Next = Next{Action: "human", Detail: "operator intervention is required before another rerun"}
			result.Outcome = blocked
			result.RerunRefused = true
			return result
		}
	}

	fingerprint, err := CaptureFingerprint(root, manifest)
	if err != nil {
		outcome.AddFailure("fingerprint", Failure{Class: "harness", Detail: err.Error()})
	} else if mismatch := FingerprintMismatch(fingerprint, lock.Fingerprint); mismatch != "" {
		outcome.AddFailure("fingerprint", Failure{Class: "harness", Detail: "environment differs from recording: " + mismatch})
	}

	if opts.ProbeID == "" {
		validateProbeSet(&outcome, manifest, lock)
		validateMetricSet(&outcome, manifest, lock)
	}
	for _, probe := range selected {
		validateProbeEntry(root, &outcome, probe, lock)
	}
	if outcome.Counts.Harness > 0 {
		outcome.Finalize()
		if _, ok := outcome.Failures["fingerprint"]; ok {
			outcome.Next = Next{Action: "human", Detail: "restore the recorded toolchain or ask an operator to re-record on this machine"}
		}
		// No probe ran, so nothing passed; the count must not imply otherwise.
		outcome.Counts.Pass = 0
		result.Outcome = outcome
		return result
	}

	runner := Runner{Root: root, Manifest: manifest}
	for _, probe := range selected {
		expected := lock.Probes[probe.ID]
		first := runner.RunProbe(probe, true)
		if first.HarnessError != "" {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: first.HarnessError})
			continue
		}
		if RunMatchesLock(first, expected) {
			continue
		}
		second := runner.RunProbe(probe, true)
		if second.HarnessError != "" {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: second.HarnessError})
			continue
		}
		if RunResultsEqual(first, second) {
			outcome.AddFailure(probe.ID, Failure{
				Class:  "behavior",
				Detail: "observed behavior differs consistently from the lockfile",
				Expect: ExpectedFromLock(expected),
				Got:    ActualFromRun(first),
				Diff:   DiffRuns(root, expected, first),
			})
		} else {
			outcome.AddFailure(probe.ID, Failure{
				Class:  "flake",
				Detail: "mismatching observations differed across the single retry",
				Expect: ExpectedFromLock(expected),
				Got:    ActualFromRun(first),
				Diff:   DiffRuns(root, expected, first),
			})
			result.Flaky = append(result.Flaky, probe.ID)
		}
	}

	if opts.ProbeID == "" && outcome.Counts.Harness == 0 {
		for _, metric := range manifest.Metrics {
			expected := lock.Metrics[metric.ID]
			first := runner.RunMetric(metric)
			if first.HarnessError != "" {
				outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: first.HarnessError})
				continue
			}
			if first.ToolVersion != expected.ToolVersion {
				outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: "metric tool version differs from recording"})
				continue
			}
			if first.Value != expected.Value {
				second := runner.RunMetric(metric)
				if second.HarnessError != "" {
					outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: second.HarnessError})
					continue
				}
				if first.Value != second.Value || first.ToolVersion != second.ToolVersion {
					outcome.AddFailure(metric.ID, Failure{Class: "flake", Detail: "metric changed across the single retry"})
					result.Flaky = append(result.Flaky, metric.ID)
					continue
				}
			}
			delta := MetricDelta{Base: expected.Value, Now: first.Value, Delta: first.Value - expected.Value, Direction: metric.Direction, Enforce: metric.Enforce}
			outcome.Metrics[metric.ID] = delta
			regressed := metric.Enforce == "no-regress" && ((metric.Direction == "down" && first.Value > expected.Value) || (metric.Direction == "up" && first.Value < expected.Value))
			if regressed {
				outcome.AddFailure(metric.ID, Failure{Class: "metric", Detail: fmt.Sprintf("metric regressed from %g to %g", expected.Value, first.Value)})
			}
		}
	}
	outcome.Finalize()
	result.Outcome = outcome
	sort.Strings(result.Flaky)
	return result
}

func selectedProbes(manifest Manifest, id string) ([]Probe, error) {
	if id == "" {
		return append([]Probe(nil), manifest.Probes...), nil
	}
	probe, ok := manifest.Probe(id)
	if !ok {
		return nil, fmt.Errorf("unknown probe %q", id)
	}
	return []Probe{probe}, nil
}

func validateProbeSet(outcome *Outcome, manifest Manifest, lock Lockfile) {
	declared := make(map[string]bool, len(manifest.Probes))
	for _, probe := range manifest.Probes {
		declared[probe.ID] = true
	}
	for id := range declared {
		if _, ok := lock.Probes[id]; !ok {
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "probe is declared but absent from vise.lock; record a new baseline"})
		}
	}
	for id := range lock.Probes {
		if !declared[id] {
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "probe exists in vise.lock but not vise.toml; restore the manifest or record a new baseline"})
		}
	}
}

func validateMetricSet(outcome *Outcome, manifest Manifest, lock Lockfile) {
	declared := make(map[string]bool, len(manifest.Metrics))
	for _, metric := range manifest.Metrics {
		declared[metric.ID] = true
	}
	for id := range declared {
		if _, ok := lock.Metrics[id]; !ok {
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "metric is declared but absent from vise.lock"})
		}
	}
	for _, metric := range manifest.Metrics {
		expected, ok := lock.Metrics[metric.ID]
		if !ok {
			continue
		}
		runHash, err := MetricRunHash(metric)
		if err != nil {
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: err.Error()})
			continue
		}
		switch {
		case expected.RunHash == "":
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: "metric definition was not frozen by this baseline; re-record"})
		case expected.RunHash != runHash:
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: "metric definition changed after recording"})
		}
	}
	for id := range lock.Metrics {
		if !declared[id] {
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "metric exists in vise.lock but not vise.toml"})
		}
	}
}

func validateProbeEntry(root string, outcome *Outcome, probe Probe, lock Lockfile) {
	expected, ok := lock.Probes[probe.ID]
	if !ok {
		if _, exists := outcome.Failures[probe.ID]; !exists {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: "probe is absent from vise.lock"})
		}
		return
	}
	runHash, err := ProbeRunHash(probe)
	if err != nil {
		outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: err.Error()})
		return
	}
	if expected.RunHash != runHash {
		outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: "probe definition changed after recording"})
		return
	}
	deps, err := HashDependencies(root, probe.Deps)
	if err != nil {
		outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: err.Error()})
		return
	}
	if !stringMapEqual(deps, expected.Deps) {
		outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: "declared probe input changed after recording"})
		return
	}
	checks := []struct {
		hash  string
		large bool
		label string
	}{
		{expected.Stdout, expected.StdoutLarge, "stdout"},
		{expected.Stderr, expected.StderrLarge, "stderr"},
	}
	for path, hash := range expected.Files {
		checks = append(checks, struct {
			hash  string
			large bool
			label string
		}{hash, expected.FilesLarge[path], "file " + path})
	}
	for _, check := range checks {
		if check.large {
			continue
		}
		if _, _, err := BlobData(root, check.hash, false); err != nil {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: fmt.Sprintf("expected %s blob is unavailable: %v", check.label, err)})
			return
		}
	}
}

func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func RunMatchesLock(run RunResult, expected ProbeLock) bool {
	if run.Exit != expected.Exit || HashBytes(run.Stdout) != expected.Stdout || HashBytes(run.Stderr) != expected.Stderr || len(run.Files) != len(expected.Files) {
		return false
	}
	for path, hash := range expected.Files {
		if HashBytes(run.Files[path]) != hash {
			return false
		}
	}
	return true
}

func ExpectedFromLock(lock ProbeLock) *ExpectedActual {
	return &ExpectedActual{Exit: IntPtr(lock.Exit), Stdout: lock.Stdout, Stderr: lock.Stderr, Files: lock.Files}
}

func ActualFromRun(run RunResult) *ExpectedActual {
	files := make(map[string]string, len(run.Files))
	for path, data := range run.Files {
		files[path] = HashBytes(data)
	}
	if len(files) == 0 {
		files = nil
	}
	return &ExpectedActual{Exit: IntPtr(run.Exit), Stdout: HashBytes(run.Stdout), Stderr: HashBytes(run.Stderr), Files: files}
}

func JournalVerifyResult(root, command string, result VerifyResult) error {
	event := JournalEvent{
		Event:   command,
		Commit:  result.Commit,
		Dirty:   result.Dirty,
		Verdict: result.Outcome.Verdict,
		Counts:  &result.Outcome.Counts,
		Lock:    result.Outcome.Lock,
	}
	if len(result.Outcome.Metrics) > 0 {
		event.Metrics = make(map[string]float64, len(result.Outcome.Metrics))
		for id, metric := range result.Outcome.Metrics {
			event.Metrics[id] = metric.Now
		}
	}
	if len(result.CheckSet) > 0 {
		event.Probes = append([]string(nil), result.CheckSet...)
	}
	if len(result.Flaky) > 0 {
		event.Event = "flake"
		event.Flaky = append([]string(nil), result.Flaky...)
	}
	return AppendJournal(root, event)
}

// RerunLimitReached reports whether the next judged run for this commit,
// lock, and probe set would be refused, and why. Both verify and status use
// it so the perception act never promises a gate that would be refused.
func RerunLimitReached(root, commit, lockHash string, probeIDs []string) (bool, string, error) {
	events, truncated, err := readJournalTail(root)
	if err != nil {
		return false, "", err
	}
	flakes, bounded := ConsecutiveFlakes(events, commit, lockHash, probeIDs)
	switch {
	case flakes >= 2:
		return true, "second consecutive rerun already consumed for this commit, lock, and probe set", nil
	case truncated && !bounded:
		return true, "journal tail holds only unjudged events for this commit and lock; the rerun chain cannot be bounded", nil
	}
	return false, "", nil
}
