package vise

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
)

type RecordOptions struct {
	AllowDirty   bool
	ReviewedDiff bool
	// Preview runs both passes, builds the candidate lockfile, and returns
	// its review diff and digest without writing anything.
	Preview bool
	// Accept writes the candidate only if its digest equals this value, so
	// what gets frozen is exactly what a preview showed.
	Accept          string
	BeforeOverwrite func(string) error
}

type RecordResult struct {
	Outcome    Outcome
	ReviewDiff string
	// Candidate is the digest of the lockfile these passes would write.
	Candidate string
}

type recordSelfTestResult struct {
	probes  map[string]RunResult
	metrics map[string]MetricResult
}

func Record(root string, manifest Manifest, manifestBytes []byte, opts RecordOptions) RecordResult {
	outcome := NewOutcome("record")
	outcome.Counts.Declared = len(manifest.Probes) + len(manifest.Metrics)
	result := RecordResult{Outcome: outcome}
	if len(manifest.Probes) == 0 {
		result.Outcome = harnessWithNext("record", "manifest", "manifest must declare at least one [[probe]] before recording", Next{Action: NextFixProbe, Detail: "declare at least one probe in vise.toml, commit the harness, then rerun vise record"})
		return result
	}

	dirty, err := GitDirty(root)
	if err != nil {
		result.Outcome = harnessOnly("record", "git", err.Error())
		return result
	}
	if dirty && !opts.AllowDirty {
		result.Outcome = harnessWithNext("record", "working-tree", "record requires a clean working tree; commit or stash changes, or pass --allow-dirty", Next{Action: NextHuman, Detail: "commit or stash the current tree, or rerun record with --allow-dirty"})
		return result
	}

	oldLock, _, oldErr := LoadLockfile(root)
	hasOld := oldErr == nil
	if oldErr != nil && !os.IsNotExist(oldErr) {
		result.Outcome = harnessOnly("record", "vise.lock", oldErr.Error())
		return result
	}
	if hasOld && !opts.ReviewedDiff && !opts.Preview && opts.Accept == "" {
		result.Outcome = harnessWithNext("record", "operator-review", "vise.lock already exists; preview the behavior diff with --preview and accept its digest with --accept, or rerun with --i-reviewed-the-diff", Next{Action: NextHuman, Detail: "run record --preview, review the diff, then record --accept <digest>; or rerun with --i-reviewed-the-diff to review and write in one step"})
		return result
	}

	fingerprint, err := captureStableFingerprint(root, manifest)
	if err != nil {
		result.Outcome = harnessWithNext("record", "fingerprint", err.Error(), Next{Action: NextFixProbe, Detail: "repair the environment fingerprint command, then rerun record"})
		return result
	}
	commit, err := GitHead(root)
	if err != nil {
		result.Outcome = harnessOnly("record", "git", err.Error())
		return result
	}

	selfTest, ok := runRecordSelfTest(root, manifest, &result.Outcome)
	if !ok {
		return result
	}

	lock := Lockfile{
		V:           LockVersion,
		Fingerprint: fingerprint,
		Probes:      make(map[string]ProbeLock, len(manifest.Probes)),
		Metrics:     make(map[string]MetricLock, len(manifest.Metrics)),
	}
	blobs := make(map[string][]byte)
	for _, probe := range manifest.Probes {
		runHash, err := ProbeRunHash(probe)
		if err != nil {
			result.Outcome = harnessOnly("record", probe.ID, err.Error())
			return result
		}
		deps, err := HashDependencies(root, probe.Deps)
		if err != nil {
			result.Outcome = harnessOnly("record", probe.ID, err.Error())
			return result
		}
		entry := AddObservationBlobs(blobs, selfTest.probes[probe.ID])
		entry.RunHash = runHash
		entry.Deps = deps
		entry.RecordedCommit = firstFrozenAt(oldLock.Probes[probe.ID], entry, commit)
		lock.Probes[probe.ID] = entry
	}
	for _, metric := range manifest.Metrics {
		runHash, err := MetricRunHash(metric)
		if err != nil {
			result.Outcome = harnessOnly("record", metric.ID, err.Error())
			return result
		}
		run := selfTest.metrics[metric.ID]
		lock.Metrics[metric.ID] = MetricLock{RunHash: runHash, Value: run.Value, ToolVersion: run.ToolVersion}
	}
	if len(lock.Metrics) == 0 {
		lock.Metrics = nil
	}

	candidateBytes, err := CanonicalJSON(lock)
	if err != nil {
		result.Outcome = harnessOnly("record", "persistence", err.Error())
		return result
	}
	result.Candidate = HashBytes(candidateBytes)
	if hasOld {
		result.ReviewDiff = LockfileDiff(root, oldLock, lock, blobs)
	}
	if opts.Preview {
		// Nothing is written: no blobs, no lock, no journal event.
		result.Outcome.Counts.Pass = result.Outcome.Counts.Declared
		result.Outcome.Finalize()
		result.Outcome.Next = Next{Action: NextHuman, Detail: "review the diff, then freeze it with record --accept " + result.Candidate}
		return result
	}
	if opts.Accept != "" && opts.Accept != result.Candidate {
		result.Outcome = harnessWithNext("record", "operator-review", "candidate "+result.Candidate+" differs from the accepted "+opts.Accept+"; the tree or environment changed since the preview", Next{Action: NextHuman, Detail: "rerun record --preview and review the new diff"})
		return result
	}
	if hasOld && opts.BeforeOverwrite != nil {
		if err := opts.BeforeOverwrite(result.ReviewDiff); err != nil {
			result.Outcome = harnessOnly("record", "operator-review", err.Error())
			return result
		}
	}
	lockBytes, err := WriteGeneration(root, lock, blobs)
	if err != nil {
		result.Outcome = harnessOnly("record", "persistence", err.Error())
		return result
	}
	lockHash, err := TamperHash(root, manifestBytes, lockBytes)
	if err != nil {
		result.Outcome = harnessOnly("record", "tamper-hash", err.Error())
		return result
	}
	declared := len(manifest.Probes) + len(manifest.Metrics)
	counts := Counts{Declared: declared, Pass: declared}
	if err := AppendJournal(root, JournalEvent{Event: "record", Commit: commit, Dirty: dirty, Counts: &counts, Lock: lockHash}); err != nil {
		result.Outcome = harnessOnly("record", "journal", "baseline was written but journal append failed: "+err.Error())
		return result
	}

	result.Outcome.Lock = lockHash
	result.Outcome.Finalize()
	return result
}

func runRecordSelfTest(root string, manifest Manifest, outcome *Outcome) (recordSelfTestResult, bool) {
	runner := Runner{Root: root, Manifest: manifest}
	first := recordSelfTestResult{
		probes:  make(map[string]RunResult, len(manifest.Probes)),
		metrics: make(map[string]MetricResult, len(manifest.Metrics)),
	}
	for _, probe := range manifest.Probes {
		run := runner.RunProbe(probe, true)
		if run.HarnessError != "" {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: run.HarnessError})
		} else {
			first.probes[probe.ID] = run
		}
	}
	for _, metric := range manifest.Metrics {
		run := runner.RunMetric(metric)
		if run.HarnessError != "" {
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: run.HarnessError})
		} else {
			first.metrics[metric.ID] = run
		}
	}
	if outcome.Counts.Harness > 0 {
		outcome.Finalize()
		outcome.Counts.Pass = 0 // a baseline needs both passes; none was frozen
		return recordSelfTestResult{}, false
	}

	for _, probe := range manifest.Probes {
		run := runner.RunProbe(probe, true)
		if run.HarnessError != "" {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: run.HarnessError})
			continue
		}
		if !RunResultsEqual(first.probes[probe.ID], run) {
			outcome.AddFailure(probe.ID, Failure{
				Class:  "flake",
				Detail: "record self-test diverged across two full-suite passes",
				Diff:   DiffRunResults(first.probes[probe.ID], run),
			})
		}
	}
	for _, metric := range manifest.Metrics {
		run := runner.RunMetric(metric)
		if run.HarnessError != "" {
			outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: run.HarnessError})
			continue
		}
		firstMetric := first.metrics[metric.ID]
		if firstMetric.Value != run.Value || firstMetric.ToolVersion != run.ToolVersion {
			outcome.AddFailure(metric.ID, Failure{Class: "flake", Detail: "metric diverged across two full-suite passes"})
		}
	}
	if outcome.Counts.Harness > 0 || outcome.Counts.Flaky > 0 {
		outcome.Finalize()
		outcome.Counts.Pass = 0 // a baseline needs both passes; none was frozen
		if outcome.Counts.Harness == 0 {
			outcome.Next = Next{Action: NextFixProbe, Detail: "make the named probes deterministic (normalize timestamps, ordering, temp paths, seeds), then rerun vise record"}
		}
		return recordSelfTestResult{}, false
	}

	return first, true
}

func RunResultsEqual(a, b RunResult) bool {
	if a.Exit != b.Exit || a.HarnessError != b.HarnessError || !a.Stdout.Equal(b.Stdout) || !a.Stderr.Equal(b.Stderr) || len(a.Files) != len(b.Files) {
		return false
	}
	for path, capture := range a.Files {
		other, ok := b.Files[path]
		if !ok || !capture.Equal(other) {
			return false
		}
	}
	return true
}

func harnessOnly(cmd, id, detail string) Outcome {
	outcome := NewOutcome(cmd)
	outcome.AddFailure(id, Failure{Class: "harness", Detail: detail})
	outcome.Finalize()
	return outcome
}

func harnessWithNext(cmd, id, detail string, next Next) Outcome {
	outcome := harnessOnly(cmd, id, detail)
	outcome.Next = next
	return outcome
}

// LockfileDiff explains old versus new. newBlobs carries the new side's bytes
// when they are not on disk yet (a preview); the old side reads the store.
func LockfileDiff(root string, oldLock, newLock Lockfile, newBlobs map[string][]byte) string {
	var b strings.Builder
	ids := make(map[string]bool)
	for id := range oldLock.Probes {
		ids[id] = true
	}
	for id := range newLock.Probes {
		ids[id] = true
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		oldProbe, oldOK := oldLock.Probes[id]
		newProbe, newOK := newLock.Probes[id]
		switch {
		case !oldOK:
			fmt.Fprintf(&b, "+ probe %s (exit %d, stdout %s, stderr %s, %d file(s), recorded at %s)\n", id, newProbe.Exit, newProbe.Stdout, newProbe.Stderr, len(newProbe.Files), newProbe.RecordedCommit)
		case !newOK:
			fmt.Fprintf(&b, "- probe %s (exit %d, stdout %s, stderr %s)\n", id, oldProbe.Exit, oldProbe.Stdout, oldProbe.Stderr)
		default:
			if oldProbe.RunHash != newProbe.RunHash {
				fmt.Fprintf(&b, "%s definition changed since the recorded baseline (run_hash %s -> %s); see git diff vise.toml\n", id, oldProbe.RunHash, newProbe.RunHash)
			}
			if oldProbe.Exit != newProbe.Exit {
				fmt.Fprintf(&b, "%s exit: %d -> %d\n", id, oldProbe.Exit, newProbe.Exit)
			}
			depPaths := make(map[string]bool)
			for path := range oldProbe.Deps {
				depPaths[path] = true
			}
			for path := range newProbe.Deps {
				depPaths[path] = true
			}
			for _, path := range sortedKeys(depPaths) {
				if oldProbe.Deps[path] != newProbe.Deps[path] {
					fmt.Fprintf(&b, "%s dep %s: %s -> %s\n", id, path, hashOrNone(oldProbe.Deps[path]), hashOrNone(newProbe.Deps[path]))
				}
			}
			appendBlobDiff(&b, root, newBlobs, id+"/stdout", oldProbe.Stdout, oldProbe.StdoutLarge, newProbe.Stdout, newProbe.StdoutLarge)
			appendBlobDiff(&b, root, newBlobs, id+"/stderr", oldProbe.Stderr, oldProbe.StderrLarge, newProbe.Stderr, newProbe.StderrLarge)
			paths := make(map[string]bool)
			for path := range oldProbe.Files {
				paths[path] = true
			}
			for path := range newProbe.Files {
				paths[path] = true
			}
			for _, path := range sortedKeys(paths) {
				appendBlobDiff(&b, root, newBlobs, id+"/"+path, oldProbe.Files[path], oldProbe.FilesLarge[path], newProbe.Files[path], newProbe.FilesLarge[path])
			}
		}
	}
	if mismatch := FingerprintMismatch(newLock.Fingerprint, oldLock.Fingerprint); mismatch != "" {
		fmt.Fprintf(&b, "fingerprint: %s\n", mismatch)
	}
	metricIDs := make(map[string]bool)
	for id := range oldLock.Metrics {
		metricIDs[id] = true
	}
	for id := range newLock.Metrics {
		metricIDs[id] = true
	}
	for _, id := range sortedKeys(metricIDs) {
		oldMetric, oldOK := oldLock.Metrics[id]
		newMetric, newOK := newLock.Metrics[id]
		switch {
		case !oldOK:
			fmt.Fprintf(&b, "+ metric %s (value %g, tool_version %q)\n", id, newMetric.Value, newMetric.ToolVersion)
		case !newOK:
			fmt.Fprintf(&b, "- metric %s (value %g, tool_version %q)\n", id, oldMetric.Value, oldMetric.ToolVersion)
		default:
			if oldMetric.RunHash != newMetric.RunHash {
				fmt.Fprintf(&b, "%s definition changed since the recorded baseline (run, direction, enforce, env, timeout, or version_cmd; run_hash %s -> %s); see git diff vise.toml\n", id, oldMetric.RunHash, newMetric.RunHash)
			}
			if oldMetric.Value != newMetric.Value {
				fmt.Fprintf(&b, "%s value: %g -> %g\n", id, oldMetric.Value, newMetric.Value)
			}
			if oldMetric.ToolVersion != newMetric.ToolVersion {
				fmt.Fprintf(&b, "%s tool_version: %q -> %q\n", id, oldMetric.ToolVersion, newMetric.ToolVersion)
			}
		}
	}
	if b.Len() == 0 {
		return "No recorded behavior changed."
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func appendBlobDiff(b *strings.Builder, root string, newBlobs map[string][]byte, label, oldHash string, oldLarge bool, newHash string, newLarge bool) {
	if oldHash == newHash && oldLarge == newLarge {
		return
	}
	oldData, oldAvailable, _ := BlobData(root, oldHash, oldLarge)
	newData, newAvailable := newBlobs[newHash]
	if !newAvailable {
		newData, newAvailable, _ = BlobData(root, newHash, newLarge)
	}
	if oldAvailable && newAvailable {
		fmt.Fprintln(b, FirstDiff(label, oldData, newData))
		return
	}
	fmt.Fprintf(b, "%s hash: %s -> %s\n", label, oldHash, newHash)
}

func DiffRuns(root string, expected ProbeLock, got RunResult) string {
	if expected.Exit != got.Exit {
		return fmt.Sprintf("exit: expected %d, got %d", expected.Exit, got.Exit)
	}
	if expected.Stdout != got.Stdout.Hash {
		if diff := captureDiff(root, "stdout", expected.Stdout, expected.StdoutLarge, got.Stdout); diff != "" {
			return diff
		}
	}
	if expected.Stderr != got.Stderr.Hash {
		if diff := captureDiff(root, "stderr", expected.Stderr, expected.StderrLarge, got.Stderr); diff != "" {
			return diff
		}
	}
	for _, path := range sortedKeys(expected.Files) {
		actual := got.Files[path]
		if expected.Files[path] != actual.Hash {
			if diff := captureDiff(root, "file/"+path, expected.Files[path], expected.FilesLarge[path], actual); diff != "" {
				return diff
			}
		}
	}
	return "observation differs"
}

// captureDiff renders a divergence as bytes whenever the retained prefix
// actually holds it — an observation past the capture bound still gets a real
// diff when it changed early. It degrades to hashes only when the expected
// bytes are unavailable or the two agree everywhere vise still has.
func captureDiff(root, label, expected string, expectedLarge bool, got Capture) string {
	data, available, _ := BlobData(root, expected, expectedLarge)
	switch {
	case !available:
		return truncatedDiff(label, expected, got)
	case !got.Truncated():
		return FirstDiff(label, data, got.Prefix)
	case divergesWithin(data, got.Prefix):
		return FirstDiff(label, data, got.Prefix) + fmt.Sprintf("\n… the observation continues to %d bytes, past the %d-byte capture bound", got.Size, CaptureLimit)
	default:
		return truncatedDiff(label, expected, got)
	}
}

// divergesWithin reports whether two streams already differ inside the bytes
// both sides still have; if they agree there, the divergence is past the bound
// and no byte-level diff would be honest.
func divergesWithin(expected, prefix []byte) bool {
	n := len(expected)
	if len(prefix) < n {
		n = len(prefix)
	}
	return !bytes.Equal(expected[:n], prefix[:n])
}

// truncatedDiff explains a difference vise cannot render as bytes: either the
// expected blob is hash-only or the streams agree everywhere vise retained.
func truncatedDiff(label, expected string, got Capture) string {
	if got.Truncated() {
		return fmt.Sprintf("%s hash: expected %s, got %s (%d bytes, larger than the %d-byte capture bound)", label, expected, got.Hash, got.Size, CaptureLimit)
	}
	return fmt.Sprintf("%s hash: expected %s, got %s", label, expected, got.Hash)
}

// DiffRunResults explains the first divergence between two observations of
// the same probe that are both still in memory, such as the two record
// passes. Nothing here touches the blob store.
func DiffRunResults(first, second RunResult) string {
	if first.Exit != second.Exit {
		return fmt.Sprintf("exit: first pass %d, second pass %d", first.Exit, second.Exit)
	}
	if !first.Stdout.Equal(second.Stdout) {
		if diff := FirstDiff("stdout", first.Stdout.Prefix, second.Stdout.Prefix); diff != "" {
			return diff
		}
		return fmt.Sprintf("stdout differs beyond the %d-byte capture bound: %s (%d bytes) then %s (%d bytes)", CaptureLimit, first.Stdout.Hash, first.Stdout.Size, second.Stdout.Hash, second.Stdout.Size)
	}
	if !first.Stderr.Equal(second.Stderr) {
		if diff := FirstDiff("stderr", first.Stderr.Prefix, second.Stderr.Prefix); diff != "" {
			return diff
		}
		return fmt.Sprintf("stderr differs beyond the %d-byte capture bound: %s (%d bytes) then %s (%d bytes)", CaptureLimit, first.Stderr.Hash, first.Stderr.Size, second.Stderr.Hash, second.Stderr.Size)
	}
	paths := make(map[string]bool, len(first.Files))
	for path := range first.Files {
		paths[path] = true
	}
	for path := range second.Files {
		paths[path] = true
	}
	for _, path := range sortedKeys(paths) {
		if first.Files[path].Equal(second.Files[path]) {
			continue
		}
		if diff := FirstDiff("file/"+path, first.Files[path].Prefix, second.Files[path].Prefix); diff != "" {
			return diff
		}
		return fmt.Sprintf("file/%s differs beyond the %d-byte capture bound: %s (%d bytes) then %s (%d bytes)", path, CaptureLimit, first.Files[path].Hash, first.Files[path].Size, second.Files[path].Hash, second.Files[path].Size)
	}
	return "observation differs"
}

func hashOrNone(hash string) string {
	if hash == "" {
		return "(none)"
	}
	return hash
}

// firstFrozenAt returns the commit to stamp on a freshly recorded probe.
// A re-record that observes exactly what the old baseline holds keeps the
// commit the observation was first frozen at, so an unchanged behavior
// produces a byte-identical lockfile: a reviewer reading `git diff vise.lock`
// sees nothing, which is the truth. Stamping HEAD every time churned the file
// and the tamper hash with it, and taught reviewers to skim a diff that was
// usually noise — the one habit that lets a real change through.
func firstFrozenAt(old, fresh ProbeLock, head string) string {
	if old.RecordedCommit == "" {
		return head
	}
	fresh.RecordedCommit = old.RecordedCommit
	if observationsEqual(old, fresh) {
		return old.RecordedCommit
	}
	return head
}

func observationsEqual(a, b ProbeLock) bool {
	return a.RunHash == b.RunHash &&
		a.RecordedCommit == b.RecordedCommit &&
		a.Exit == b.Exit &&
		a.Stdout == b.Stdout &&
		a.Stderr == b.Stderr &&
		a.StdoutLarge == b.StdoutLarge &&
		a.StderrLarge == b.StderrLarge &&
		maps.Equal(a.Deps, b.Deps) &&
		maps.Equal(a.Files, b.Files) &&
		maps.Equal(a.FilesLarge, b.FilesLarge)
}

// captureStableFingerprint runs the environment fingerprint twice and refuses
// a baseline whose fingerprint does not agree with itself.
//
// Probes get a two-pass self test at record; the fingerprint did not, and it
// is the one value compared on every later run. A command that prints a
// timestamp, a PID, or a path with a random component would be frozen once and
// then never match again, so every gate on every machine would report
// environment drift — an exit-2 the agent is told to stop and report, pointing
// at a toolchain that never moved. Catching it here costs one extra run of a
// command that is meant to print a version string.
func captureStableFingerprint(root string, manifest Manifest) (Fingerprint, error) {
	first, err := CaptureFingerprint(root, manifest)
	if err != nil {
		return Fingerprint{}, err
	}
	if len(manifest.Environment.Fingerprint) == 0 {
		return first, nil
	}
	second, err := CaptureFingerprint(root, manifest)
	if err != nil {
		return Fingerprint{}, err
	}
	if mismatch := FingerprintMismatch(first, second); mismatch != "" {
		return Fingerprint{}, fmt.Errorf("environment fingerprint is not deterministic: %s; a fingerprint command must print the same bytes every time, or every later run reports environment drift against a toolchain that never moved", mismatch)
	}
	return first, nil
}
