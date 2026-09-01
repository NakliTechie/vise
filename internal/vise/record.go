package vise

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RecordOptions struct {
	AllowDirty      bool
	ReviewedDiff    bool
	BeforeOverwrite func(string) error
}

type RecordResult struct {
	Outcome    Outcome
	Lockfile   Lockfile
	LockBytes  []byte
	ReviewDiff string
}

func Record(root string, manifest Manifest, manifestBytes []byte, opts RecordOptions) RecordResult {
	outcome := NewOutcome("record")
	outcome.Counts.Declared = len(manifest.Probes)
	result := RecordResult{Outcome: outcome}
	if len(manifest.Probes) == 0 {
		result.Outcome = harnessOnly("record", "manifest", "manifest must declare at least one [[probe]] before recording")
		return result
	}

	dirty, err := GitDirty(root)
	if err != nil {
		result.Outcome = harnessOnly("record", "git", err.Error())
		return result
	}
	if dirty && !opts.AllowDirty {
		result.Outcome = harnessOnly("record", "working-tree", "record requires a clean working tree; commit or stash changes, or pass --allow-dirty")
		return result
	}

	oldLock, oldBytes, oldErr := LoadLockfile(root)
	hasOld := oldErr == nil
	if oldErr != nil && !os.IsNotExist(oldErr) {
		result.Outcome = harnessOnly("record", "vise.lock", oldErr.Error())
		return result
	}
	if hasOld && !opts.ReviewedDiff {
		result.Outcome = harnessOnly("record", "operator-review", "vise.lock already exists; review the behavior diff and rerun with --i-reviewed-the-diff")
		return result
	}

	fingerprint, err := CaptureFingerprint(root, manifest)
	if err != nil {
		result.Outcome = harnessOnly("record", "fingerprint", err.Error())
		return result
	}
	commit, err := GitHead(root)
	if err != nil {
		result.Outcome = harnessOnly("record", "git", err.Error())
		return result
	}

	runner := Runner{Root: root, Manifest: manifest}
	firstProbes := make(map[string]RunResult, len(manifest.Probes))
	firstMetrics := make(map[string]MetricResult, len(manifest.Metrics))
	for _, probe := range manifest.Probes {
		run := runner.RunProbe(probe, true)
		if run.HarnessError != "" {
			result.Outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: run.HarnessError})
		} else {
			firstProbes[probe.ID] = run
		}
	}
	for _, metric := range manifest.Metrics {
		run := runner.RunMetric(metric)
		if run.HarnessError != "" {
			result.Outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: run.HarnessError})
		} else {
			firstMetrics[metric.ID] = run
		}
	}
	if result.Outcome.Counts.Harness > 0 {
		result.Outcome.Finalize()
		return result
	}

	for _, probe := range manifest.Probes {
		run := runner.RunProbe(probe, true)
		if run.HarnessError != "" {
			result.Outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: run.HarnessError})
			continue
		}
		if !RunResultsEqual(firstProbes[probe.ID], run) {
			result.Outcome.AddFailure(probe.ID, Failure{
				Class:  "flake",
				Detail: "record self-test diverged across two full-suite passes",
				Diff:   DiffRuns(root, ProbeLockFromRun(firstProbes[probe.ID]), run),
			})
		}
	}
	for _, metric := range manifest.Metrics {
		run := runner.RunMetric(metric)
		if run.HarnessError != "" {
			result.Outcome.AddFailure(metric.ID, Failure{Class: "harness", Detail: run.HarnessError})
			continue
		}
		first := firstMetrics[metric.ID]
		if first.Value != run.Value || first.ToolVersion != run.ToolVersion {
			result.Outcome.AddFailure(metric.ID, Failure{Class: "flake", Detail: "metric diverged across two full-suite passes"})
		}
	}
	if result.Outcome.Counts.Harness > 0 || result.Outcome.Counts.Flaky > 0 {
		result.Outcome.Finalize()
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
		entry := AddObservationBlobs(blobs, firstProbes[probe.ID])
		entry.RunHash = runHash
		entry.Deps = deps
		entry.RecordedCommit = commit
		lock.Probes[probe.ID] = entry
	}
	for _, metric := range manifest.Metrics {
		run := firstMetrics[metric.ID]
		lock.Metrics[metric.ID] = MetricLock{Value: run.Value, ToolVersion: run.ToolVersion}
	}
	if len(lock.Metrics) == 0 {
		lock.Metrics = nil
	}

	if hasOld {
		if err := WriteBlobs(root, blobs); err != nil {
			result.Outcome = harnessOnly("record", "persistence", err.Error())
			return result
		}
		result.ReviewDiff = LockfileDiff(root, oldLock, lock)
		if opts.BeforeOverwrite != nil {
			if err := opts.BeforeOverwrite(result.ReviewDiff); err != nil {
				result.Outcome = harnessOnly("record", "operator-review", err.Error())
				return result
			}
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
	counts := Counts{Declared: len(manifest.Probes), Pass: len(manifest.Probes)}
	if err := AppendJournal(root, JournalEvent{Event: "record", Commit: commit, Dirty: dirty, Counts: &counts, Lock: lockHash}); err != nil {
		result.Outcome = harnessOnly("record", "journal", "baseline was written but journal append failed: "+err.Error())
		result.Lockfile = lock
		result.LockBytes = lockBytes
		return result
	}

	result.Outcome.Counts.Pass = len(manifest.Probes)
	result.Outcome.Lock = lockHash
	result.Outcome.Finalize()
	result.Lockfile = lock
	result.LockBytes = lockBytes
	_ = oldBytes
	return result
}

func RunResultsEqual(a, b RunResult) bool {
	if a.Exit != b.Exit || a.HarnessError != b.HarnessError || !bytes.Equal(a.Stdout, b.Stdout) || !bytes.Equal(a.Stderr, b.Stderr) || len(a.Files) != len(b.Files) {
		return false
	}
	for path, data := range a.Files {
		if !bytes.Equal(data, b.Files[path]) {
			return false
		}
	}
	return true
}

func ProbeLockFromRun(run RunResult) ProbeLock {
	entry := ProbeLock{Exit: run.Exit, Stdout: HashBytes(run.Stdout), Stderr: HashBytes(run.Stderr), Files: make(map[string]string)}
	for path, data := range run.Files {
		entry.Files[path] = HashBytes(data)
	}
	return entry
}

func harnessOnly(cmd, id, detail string) Outcome {
	outcome := NewOutcome(cmd)
	outcome.Counts.Declared = 1
	outcome.AddFailure(id, Failure{Class: "harness", Detail: detail})
	outcome.Finalize()
	return outcome
}

func LockfileDiff(root string, oldLock, newLock Lockfile) string {
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
			fmt.Fprintf(&b, "+ probe %s\n", id)
		case !newOK:
			fmt.Fprintf(&b, "- probe %s\n", id)
		default:
			if oldProbe.Exit != newProbe.Exit {
				fmt.Fprintf(&b, "%s exit: %d -> %d\n", id, oldProbe.Exit, newProbe.Exit)
			}
			appendBlobDiff(&b, root, id+"/stdout", oldProbe.Stdout, oldProbe.StdoutLarge, newProbe.Stdout, newProbe.StdoutLarge)
			appendBlobDiff(&b, root, id+"/stderr", oldProbe.Stderr, oldProbe.StderrLarge, newProbe.Stderr, newProbe.StderrLarge)
			paths := make(map[string]bool)
			for path := range oldProbe.Files {
				paths[path] = true
			}
			for path := range newProbe.Files {
				paths[path] = true
			}
			for _, path := range sortedKeys(paths) {
				appendBlobDiff(&b, root, id+"/"+path, oldProbe.Files[path], oldProbe.FilesLarge[path], newProbe.Files[path], newProbe.FilesLarge[path])
			}
		}
	}
	if b.Len() == 0 {
		return "No recorded behavior changed."
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func appendBlobDiff(b *strings.Builder, root, label, oldHash string, oldLarge bool, newHash string, newLarge bool) {
	if oldHash == newHash && oldLarge == newLarge {
		return
	}
	oldData, oldAvailable, _ := BlobData(root, oldHash, oldLarge)
	newData, newAvailable, _ := BlobData(root, newHash, newLarge)
	if oldAvailable && newAvailable {
		fmt.Fprintln(b, FullByteDiff(label, oldData, newData))
		return
	}
	fmt.Fprintf(b, "%s hash: %s -> %s\n", label, oldHash, newHash)
}

func DiffRuns(root string, expected ProbeLock, got RunResult) string {
	if expected.Exit != got.Exit {
		return fmt.Sprintf("exit: expected %d, got %d", expected.Exit, got.Exit)
	}
	if expected.Stdout != HashBytes(got.Stdout) {
		data, available, _ := BlobData(root, expected.Stdout, expected.StdoutLarge)
		if available {
			return FirstDiff("stdout", data, got.Stdout)
		}
		return fmt.Sprintf("stdout hash: expected %s, got %s", expected.Stdout, HashBytes(got.Stdout))
	}
	if expected.Stderr != HashBytes(got.Stderr) {
		data, available, _ := BlobData(root, expected.Stderr, expected.StderrLarge)
		if available {
			return FirstDiff("stderr", data, got.Stderr)
		}
		return fmt.Sprintf("stderr hash: expected %s, got %s", expected.Stderr, HashBytes(got.Stderr))
	}
	for _, path := range sortedKeys(expected.Files) {
		actual := got.Files[path]
		if expected.Files[path] != HashBytes(actual) {
			data, available, _ := BlobData(root, expected.Files[path], expected.FilesLarge[path])
			if available {
				return FirstDiff("file/"+path, data, actual)
			}
			return fmt.Sprintf("file %s hash: expected %s, got %s", path, expected.Files[path], HashBytes(actual))
		}
	}
	return "observation differs"
}

func RecordArtifactPaths(manifest Manifest) []string {
	paths := make([]string, 0)
	for _, probe := range manifest.Probes {
		paths = append(paths, probe.Files...)
	}
	sort.Strings(paths)
	return paths
}

func CleanArtifacts(root string, manifest Manifest) error {
	for _, rel := range RecordArtifactPaths(manifest) {
		if err := os.RemoveAll(filepath.Join(root, rel)); err != nil {
			return err
		}
	}
	return nil
}
