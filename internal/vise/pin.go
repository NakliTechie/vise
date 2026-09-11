package vise

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Expect turns a probe into a pin: its expected observation comes from files a
// human wrote, not from a record pass. Absent is strict, not free — a stream
// with no spec is expected empty, and a pin with no artifacts declares no
// `files` — because an unconstrained stream is the hole an agent prints
// through. The one thing absence never means is a declared file that is not
// there: a named spec that is missing, unreadable, or a symlink is a harness
// error, never an empty expectation.
type Expect struct {
	Stdout string            `toml:"stdout" json:"stdout,omitempty"`
	Stderr string            `toml:"stderr" json:"stderr,omitempty"`
	Exit   *int              `toml:"exit" json:"exit,omitempty"`
	Files  map[string]string `toml:"files" json:"files,omitempty"`
}

// PinLock is the acceptance provenance a pin carries in the lockfile beside
// its spec-derived expectation. Spec hashes every file the expectation was
// read from, the way Deps hashes a probe's inputs, so an edited spec is
// harness drift the gate routes to a human — the expectation is hashed into
// the judge. AcceptedCommit is null until a clean-tree record has seen the
// working tree produce the spec; after that it is historical, carried forward
// for as long as the pin's identity is unchanged, and never revoked by a later
// record.
type PinLock struct {
	Spec           map[string]string `json:"spec"`
	AcceptedCommit *string           `json:"accepted_commit"`
}

// IsPin reports whether a probe carries an expectation.
func (p Probe) IsPin() bool { return p.Expect != nil }

// isEmpty reports an expectation that expects nothing, which is refused.
func (e Expect) isEmpty() bool {
	return e.Stdout == "" && e.Stderr == "" && e.Exit == nil && len(e.Files) == 0
}

// SpecPaths lists every file a pin's expectation is read from, in a stable
// order, with duplicates removed: two artifacts may share one spec.
func (e Expect) SpecPaths() []string {
	seen := make(map[string]bool)
	var paths []string
	add := func(path string) {
		if path == "" {
			return
		}
		clean := filepath.ToSlash(filepath.Clean(path))
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	add(e.Stdout)
	add(e.Stderr)
	for _, path := range e.Files {
		add(path)
	}
	sort.Strings(paths)
	return paths
}

// validateExpect checks a pin's expectation for shape: something is expected,
// the exit is a shell status that is not the launch-failure code, the
// artifact set and the expectation's artifact set are the same set, and every
// spec path is a well-formed repository path that is not evaluator state or
// Git metadata. Existence is not checked here, for the same reason a
// dependency's is not: doctor and the record/verify pre-flight report a
// missing spec by name, and a manifest that refuses to load says only
// "manifest".
func validateExpect(root string, probe Probe, where string) error {
	expect := probe.Expect
	if expect == nil {
		return nil
	}
	if expect.isEmpty() {
		return fmt.Errorf("%s.expect is empty; a pin must expect something (stdout, stderr, exit, or files)", where)
	}
	if expect.Exit != nil {
		if *expect.Exit < 0 || *expect.Exit > 255 {
			return fmt.Errorf("%s.expect.exit must be a shell exit status between 0 and 255", where)
		}
		if *expect.Exit == 127 {
			return fmt.Errorf("%s.expect.exit is 127, which is the shell's launch-failure status; a pin cannot expect not to be launched", where)
		}
	}
	declared := make(map[string]bool, len(probe.Files))
	for _, path := range probe.Files {
		declared[filepath.ToSlash(filepath.Clean(path))] = true
	}
	expected := make(map[string]bool, len(expect.Files))
	for artifact, spec := range expect.Files {
		clean := filepath.ToSlash(filepath.Clean(artifact))
		if !declared[clean] {
			return fmt.Errorf("%s.expect.files names %q, which is not in %s.files; every expected artifact must be a declared one", where, artifact, where)
		}
		expected[clean] = true
		if spec == "" {
			return fmt.Errorf("%s.expect.files[%q] names no spec file", where, artifact)
		}
	}
	for _, path := range probe.Files {
		clean := filepath.ToSlash(filepath.Clean(path))
		if !expected[clean] {
			return fmt.Errorf("%s.files declares %q and %s.expect.files gives it no expectation; a pin's artifacts and its expected artifacts are the same set", where, path, where)
		}
	}
	for _, spec := range expect.SpecPaths() {
		if err := ValidateSpecPath(root, spec); err != nil {
			return fmt.Errorf("%s spec %q: %w", where, spec, err)
		}
		if declared[spec] {
			return fmt.Errorf("%s spec %q is also one of its declared artifacts; vise deletes artifacts before every run", where, spec)
		}
	}
	return nil
}

// ValidateSpecPath accepts a path a pin's expectation may be read from: inside
// the repository, no symlink components, not evaluator state, not Git
// metadata. A spec may coincide with a dependency — both roles only read the
// bytes — but never with an artifact, which vise deletes.
func ValidateSpecPath(root, path string) error {
	if err := ValidateRelativePath(root, path, false); err != nil {
		return err
	}
	clean := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return fmt.Errorf("a spec cannot live in Git metadata")
	}
	if clean == ".vise" || strings.HasPrefix(clean, ".vise/") || clean == "vise.toml" || clean == "vise.lock" {
		return fmt.Errorf("a spec cannot be evaluator state")
	}
	return nil
}

// validateSpecArtifactOverlap refuses a manifest in which any pin's spec is
// some probe's declared artifact. Within one probe validateExpect already
// refuses it; across probes the artifact would be deleted before the other
// probe's run and the spec read back empty or missing.
func validateSpecArtifactOverlap(probes []Probe) error {
	artifacts := make(map[string]string)
	for _, probe := range probes {
		for _, path := range probe.Files {
			artifacts[filepath.ToSlash(filepath.Clean(path))] = probe.ID
		}
	}
	for _, probe := range probes {
		if !probe.IsPin() {
			continue
		}
		for _, spec := range probe.Expect.SpecPaths() {
			if owner, ok := artifacts[spec]; ok && owner != probe.ID {
				return fmt.Errorf("probe %q spec %q is a declared artifact of probe %q; vise deletes artifacts before every run", probe.ID, spec, owner)
			}
		}
	}
	return nil
}

// specContents reads every spec file a pin names and returns the bytes keyed
// by slash path, plus the hashes. Any failure is an operator's to repair: the
// spec is theirs, and an agent may not write it. A spec over the capture bound
// is refused for the reason an over-bound fingerprint is — a reviewer shown
// two hashes cannot review.
func specContents(root string, expect Expect) (map[string][]byte, map[string]string, error) {
	contents := make(map[string][]byte)
	hashes := make(map[string]string)
	for _, spec := range expect.SpecPaths() {
		if err := ValidateRelativePath(root, spec, false); err != nil {
			return nil, nil, fmt.Errorf("spec %q: %w", spec, err)
		}
		data, err := readRegularFile(filepath.Join(root, spec))
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("spec %q does not exist; the expectation names a file that is not there", spec)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("spec %q: %v", spec, err)
		}
		if len(data) > CaptureLimit {
			return nil, nil, fmt.Errorf("spec %q is %d bytes, larger than the %d-byte capture bound; a spec that cannot be shown cannot be reviewed", spec, len(data), CaptureLimit)
		}
		contents[spec] = data
		hashes[spec] = HashBytes(data)
	}
	return contents, hashes, nil
}

// pinExpectation builds a pin's lockfile entry from its spec files, storing
// their bytes as blobs so the diff renderer, the blob integrity check, and the
// tamper hash treat a human-written expectation exactly like an observed one.
func pinExpectation(root string, probe Probe, blobs map[string][]byte) (ProbeLock, error) {
	contents, hashes, err := specContents(root, *probe.Expect)
	if err != nil {
		return ProbeLock{}, err
	}
	stream := func(spec string) string {
		if spec == "" {
			blobs[emptyHash] = []byte{}
			return emptyHash
		}
		clean := filepath.ToSlash(filepath.Clean(spec))
		blobs[hashes[clean]] = append([]byte(nil), contents[clean]...)
		return hashes[clean]
	}
	entry := ProbeLock{
		Stdout: stream(probe.Expect.Stdout),
		Stderr: stream(probe.Expect.Stderr),
		Pin:    &PinLock{Spec: hashes},
	}
	if probe.Expect.Exit != nil {
		entry.Exit = *probe.Expect.Exit
	}
	if len(probe.Expect.Files) > 0 {
		entry.Files = make(map[string]string, len(probe.Expect.Files))
		for artifact, spec := range probe.Expect.Files {
			entry.Files[filepath.ToSlash(filepath.Clean(artifact))] = stream(spec)
		}
	}
	return entry, nil
}

// emptyHash is the hash of no bytes, which is what an undeclared stream is
// expected to produce.
var emptyHash = HashBytes(nil)

// pinIdentityEqual reports whether two lockfile entries describe the same pin
// as far as acceptance is concerned: the same definition (run_hash covers
// expect), the same inputs, and the same spec bytes. A change to any of them
// makes a new pin, and a new pin is unaccepted until an operator's record sees
// it met.
func pinIdentityEqual(old, fresh ProbeLock) bool {
	if old.Pin == nil || fresh.Pin == nil {
		return false
	}
	return old.RunHash == fresh.RunHash && stringMapEqual(old.Deps, fresh.Deps) && stringMapEqual(old.Pin.Spec, fresh.Pin.Spec)
}

// pinObservationsEqual is RunResultsEqual for an unaccepted pin, where a
// tolerated execution condition is part of the observation and a timeout is
// only its condition: the bytes a run printed before it was killed depend on
// timing, so two timed-out runs agree by having both timed out.
func pinObservationsEqual(a, b RunResult) bool {
	if a.LaunchFailed != b.LaunchFailed || a.Terminated != b.Terminated || !stringSliceEqual(a.MissingFiles, b.MissingFiles) {
		return false
	}
	if a.TimedOut || b.TimedOut {
		// Bytes printed before the kill depend on timing and are not compared;
		// the artifact set is, because a run that sometimes writes its
		// artifact before hanging is unstable, not merely slow.
		return a.TimedOut == b.TimedOut
	}
	return RunResultsEqual(a, b)
}

// pinCondition names the tolerated condition a run ended in, or "" when it
// ran to completion with every artifact in place.
func pinCondition(run RunResult) string {
	switch {
	case run.TimedOut:
		return "timed_out"
	case run.LaunchFailed:
		return "launch_failed"
	case run.Terminated:
		return "terminated"
	case len(run.MissingFiles) > 0:
		return "artifact_missing"
	}
	return ""
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
