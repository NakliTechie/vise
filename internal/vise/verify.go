package vise

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
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

type verifyState struct {
	lock     Lockfile
	lockHash string
	commit   string
	dirty    bool
}

func Verify(root string, manifest Manifest, manifestBytes []byte, opts VerifyOptions) VerifyResult {
	outcome := NewOutcome("verify")
	result := VerifyResult{Outcome: outcome}
	state, loadFailure := loadVerifyState(root, manifest, manifestBytes, &outcome)
	if loadFailure != nil {
		result.Outcome = *loadFailure
		return result
	}
	result.Commit = state.commit
	result.Dirty = state.dirty

	selected, checkSet, prepareFailure := prepareVerifyChecks(manifest, opts.ProbeID, &outcome)
	if prepareFailure != nil {
		result.Outcome = *prepareFailure
		return result
	}
	result.CheckSet = checkSet

	if opts.EnforceRerunLimit {
		if rerunFailure, refused := checkVerifyRerunLimit(root, state, checkSet); rerunFailure != nil {
			result.Outcome = *rerunFailure
			result.RerunRefused = refused
			return result
		}
	}

	if !validateVerifyInputs(root, &outcome, manifest, state.lock, selected, opts.ProbeID == "") {
		result.Outcome = outcome
		return result
	}

	runner := Runner{Root: root, Manifest: manifest}
	result.Flaky = replayProbes(root, &outcome, runner, selected, state.lock.Probes)

	// Quality is only asked about once behavior has held. The verdict already
	// ordered it that way — behavior outranks metric in Finalize — but the
	// metrics were still being run, which means an analyzer executes against a
	// tree whose behavior has already changed, its number is compared to a
	// baseline recorded against different behavior, and the agent is handed a
	// quality figure that describes something it is about to revert.
	if opts.ProbeID == "" && outcome.Counts.Harness == 0 && outcome.Counts.Behavior == 0 && outcome.Counts.Flaky == 0 {
		result.Flaky = append(result.Flaky, evaluateMetrics(&outcome, runner, manifest.Metrics, state.lock.Metrics)...)
	}
	return finalizeVerifyResult(result, outcome)
}

func finalizeVerifyResult(result VerifyResult, outcome Outcome) VerifyResult {
	outcome.Finalize()
	result.Outcome = outcome
	sort.Strings(result.Flaky)
	return result
}

func loadVerifyState(root string, manifest Manifest, manifestBytes []byte, outcome *Outcome) (verifyState, *Outcome) {
	lock, lockBytes, err := LoadLockfile(root)
	if os.IsNotExist(err) {
		outcome.Exit = ExitNotInitialized
		outcome.Counts.Declared = len(manifest.Probes) + len(manifest.Metrics)
		outcome.Finalize()
		return verifyState{}, outcome
	}
	if err != nil {
		failure := harnessForOperator("verify", "vise.lock", err.Error())
		return verifyState{}, &failure
	}
	lockHash, err := TamperHash(root, manifestBytes, lockBytes)
	if err != nil {
		failure := harnessForOperator("verify", "tamper-hash", err.Error())
		return verifyState{}, &failure
	}
	outcome.Lock = lockHash
	commit, err := GitHead(root)
	if err != nil {
		failure := harnessOnly("verify", "git", err.Error())
		return verifyState{}, &failure
	}
	dirty, err := GitDirty(root)
	if err != nil {
		failure := harnessOnly("verify", "git", err.Error())
		return verifyState{}, &failure
	}
	return verifyState{lock: lock, lockHash: lockHash, commit: commit, dirty: dirty}, nil
}

func prepareVerifyChecks(manifest Manifest, probeID string, outcome *Outcome) ([]Probe, []string, *Outcome) {
	if len(manifest.Probes) == 0 {
		// Green requires every declared probe to pass; with none declared there
		// is nothing to judge, and a 0/0 green would be a verdict without a judge.
		failure := harnessForOperator("verify", "manifest", "manifest declares no [[probe]]; nothing can be judged")
		failure.Next.Detail = "an operator declares a probe in vise.toml and records a baseline"
		return nil, nil, &failure
	}
	selected, checkSet, err := selectVerifyChecks(manifest, probeID)
	if err != nil {
		failure := harnessOnly("verify", "probe", err.Error())
		return nil, nil, &failure
	}
	outcome.Counts.Declared = len(checkSet)
	return selected, checkSet, nil
}

func checkVerifyRerunLimit(root string, state verifyState, checkSet []string) (*Outcome, bool) {
	refused, detail, err := RerunLimitReached(root, state.commit, state.lockHash, checkSet)
	if err != nil {
		failure := harnessForOperator("verify", "journal", err.Error())
		return &failure, false
	}
	if !refused {
		return nil, false
	}
	blocked := harnessForOperator("verify", "rerun-limit", detail)
	blocked.Next.Detail = "operator intervention is required before another rerun"
	return &blocked, true
}

func validateVerifyInputs(root string, outcome *Outcome, manifest Manifest, lock Lockfile, probes []Probe, validateSets bool) bool {
	fingerprint, err := CaptureFingerprint(root, manifest)
	if err != nil {
		outcome.AddFailure("fingerprint", Failure{Class: "harness", Detail: err.Error(), Operator: true})
	} else if mismatch := FingerprintMismatch(fingerprint, lock.Fingerprint); mismatch != "" {
		outcome.AddFailure("fingerprint", Failure{Class: "harness", Detail: "environment differs from recording: " + mismatch, Operator: true})
	}

	if validateSets {
		validateProbeSet(outcome, manifest, lock)
		validateMetricSet(outcome, manifest, lock)
	}
	for _, probe := range probes {
		validateProbeEntry(root, outcome, probe, lock)
	}
	if outcome.Counts.Harness == 0 {
		return true
	}

	outcome.Finalize()
	if _, ok := outcome.Failures["fingerprint"]; ok {
		outcome.Next.Detail = "restore the recorded toolchain or ask an operator to re-record on this machine"
	}
	// No probe ran, so nothing passed; the count must not imply otherwise.
	outcome.Counts.Pass = 0
	return false
}

func selectVerifyChecks(manifest Manifest, probeID string) ([]Probe, []string, error) {
	selected, err := selectedProbes(manifest, probeID)
	if err != nil {
		return nil, nil, err
	}
	checkSet := make([]string, 0, len(selected)+len(manifest.Metrics))
	for _, probe := range selected {
		checkSet = append(checkSet, probe.ID)
	}
	if probeID == "" {
		// Metrics are judged checks too: they count in the denominator so a
		// failing metric lowers pass instead of hiding behind the probe count.
		for _, metric := range manifest.Metrics {
			checkSet = append(checkSet, metric.ID)
		}
	}
	sort.Strings(checkSet)
	return selected, checkSet, nil
}

func replayProbes(root string, outcome *Outcome, runner Runner, probes []Probe, expectedProbes map[string]ProbeLock) []string {
	var flaky []string
	for _, probe := range probes {
		expected := expectedProbes[probe.ID]
		first := runner.RunProbe(probe, true)
		if first.HarnessError != "" {
			outcome.AddFailure(probe.ID, first.harnessFailure())
			continue
		}
		if RunMatchesLock(first, expected) {
			continue
		}
		second := runner.RunProbe(probe, true)
		if second.HarnessError != "" {
			outcome.AddFailure(probe.ID, second.harnessFailure())
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
			flaky = append(flaky, probe.ID)
		}
	}
	return flaky
}

func evaluateMetrics(outcome *Outcome, runner Runner, metrics []Metric, expectedMetrics map[string]MetricLock) []string {
	var flaky []string
	for _, metric := range metrics {
		expected := expectedMetrics[metric.ID]
		first := runner.RunMetric(metric)
		if first.HarnessError != "" {
			outcome.AddFailure(metric.ID, first.harnessFailure())
			continue
		}
		if first.ToolVersion != expected.ToolVersion {
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: "metric tool version differs from recording", Operator: true})
			continue
		}
		if first.Value != expected.Value {
			second := runner.RunMetric(metric)
			if second.HarnessError != "" {
				outcome.AddFailure(metric.ID, second.harnessFailure())
				continue
			}
			if first.Value != second.Value || first.ToolVersion != second.ToolVersion {
				outcome.AddFailure(metric.ID, Failure{Class: "flake", Detail: "metric changed across the single retry"})
				flaky = append(flaky, metric.ID)
				continue
			}
		}
		// Both values are finite by the time they get here, and their
		// difference still need not be: two measurements near the top of the
		// float range subtract to infinity, which is not a number JSON can
		// carry, so the verdict would fail to encode and the agent would get a
		// harness error about a metric that was merely enormous.
		difference := first.Value - expected.Value
		if math.IsInf(difference, 0) || math.IsNaN(difference) {
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: "the metric's change is not a finite number; the values are too far apart to subtract, which means the metric is not measuring a scale anyone can act on"})
			continue
		}
		delta := MetricDelta{Base: expected.Value, Now: first.Value, Delta: difference, Direction: metric.Direction, Enforce: metric.Enforce}
		outcome.Metrics[metric.ID] = delta
		regressed := metricRegressed(metric.Direction, metric.Enforce, expected.Value, first.Value)
		if regressed {
			outcome.AddFailure(metric.ID, Failure{Class: "metric", Detail: fmt.Sprintf("metric regressed from %g to %g", expected.Value, first.Value)})
		}
	}
	return flaky
}

func selectedProbes(manifest Manifest, id string) ([]Probe, error) {
	if id == "" {
		return append([]Probe(nil), manifest.Probes...), nil
	}
	probe, ok := manifest.Probe(id)
	if !ok {
		return nil, fmt.Errorf("unknown probe %q; %s", id, DeclaredProbeList(manifest))
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
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "probe is declared but absent from vise.lock; record a new baseline", Operator: true})
		}
	}
	for id := range lock.Probes {
		if !declared[id] {
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "probe exists in vise.lock but not vise.toml; restore the manifest or record a new baseline", Operator: true})
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
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "metric is declared but absent from vise.lock", Operator: true})
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
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: "metric definition was not frozen by this baseline; re-record", Operator: true})
		case expected.RunHash != runHash:
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: "metric definition changed after recording", Operator: true})
		}
	}
	for id := range lock.Metrics {
		if !declared[id] {
			outcome.AddFailure(id, Failure{Class: "harness", Detail: "metric exists in vise.lock but not vise.toml", Operator: true})
		}
	}
}

func validateProbeEntry(root string, outcome *Outcome, probe Probe, lock Lockfile) {
	expected, ok := lock.Probes[probe.ID]
	if !ok {
		if _, exists := outcome.Failures[probe.ID]; !exists {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: "probe is absent from vise.lock", Operator: true})
		}
		return
	}
	runHash, err := ProbeRunHash(probe)
	if err != nil {
		outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: err.Error()})
		return
	}
	if expected.RunHash != runHash {
		outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: "probe definition changed after recording", Operator: true})
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
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: fmt.Sprintf("expected %s blob is unavailable: %v", check.label, err), Operator: true})
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
	if run.Exit != expected.Exit || run.Stdout.Hash != expected.Stdout || run.Stderr.Hash != expected.Stderr || len(run.Files) != len(expected.Files) {
		return false
	}
	for path, hash := range expected.Files {
		if run.Files[path].Hash != hash {
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
	for path, capture := range run.Files {
		files[path] = capture.Hash
	}
	if len(files) == 0 {
		files = nil
	}
	return &ExpectedActual{Exit: IntPtr(run.Exit), Stdout: run.Stdout.Hash, Stderr: run.Stderr.Hash, Files: files}
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
		return true, "two runs at this commit and lock already ended indeterminate for the checks this one covers; the budget follows the unstable probes, so narrowing to one that flaked once still runs and running the same set again does not renew it", nil
	case truncated && !bounded:
		return true, "journal tail holds only unjudged events for this commit and lock; the rerun chain cannot be bounded", nil
	}
	return false, "", nil
}

// metricRegressed decides whether a metric moved the wrong way, which is the
// whole difference between a quality gate and a random one. It is a named
// function so it can be tested directly across both directions and both
// enforcement settings: inverting the "up" case used to leave the suite green,
// because every metric fixture in it counted downwards.
func metricRegressed(direction, enforce string, base, now float64) bool {
	if enforce != "no-regress" {
		return false
	}
	switch direction {
	case "down":
		return now > base
	case "up":
		return now < base
	default:
		return false
	}
}

// declaredProbeList names what the caller could have asked for. A typo used to
// be answered with "repair the harness or restore its declared inputs", which
// is true of nothing the caller did and helps with nothing they might do next.
// Bounded, because a manifest can declare many.
func DeclaredProbeList(manifest Manifest) string {
	if len(manifest.Probes) == 0 {
		return "this manifest declares no probes"
	}
	ids := make([]string, 0, len(manifest.Probes))
	for _, probe := range manifest.Probes {
		ids = append(ids, probe.ID)
	}
	sort.Strings(ids)
	if len(ids) > 10 {
		return fmt.Sprintf("declared probes include %s (and %d more)", strings.Join(ids[:10], ", "), len(ids)-10)
	}
	return "declared probes are " + strings.Join(ids, ", ")
}
