package vise

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Fingerprint struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// Stubs are the manifest's [stubs] values in force when the baseline was
	// recorded. They shape every probe's environment, so a change is
	// environment drift (harness class), never a behavior change.
	Stubs StubSettings      `json:"stubs"`
	Env   map[string]string `json:"env,omitempty"`
}

type ProbeLock struct {
	RunHash        string            `json:"run_hash"`
	Deps           map[string]string `json:"deps,omitempty"`
	RecordedCommit string            `json:"recorded_commit"`
	Exit           int               `json:"exit"`
	Stdout         string            `json:"stdout"`
	Stderr         string            `json:"stderr"`
	StdoutLarge    bool              `json:"stdout_large,omitempty"`
	StderrLarge    bool              `json:"stderr_large,omitempty"`
	Files          map[string]string `json:"files,omitempty"`
	FilesLarge     map[string]bool   `json:"files_large,omitempty"`
}

type MetricLock struct {
	// RunHash freezes the metric definition (run, direction, enforce, env,
	// timeout, version_cmd) the value was recorded under; a changed definition
	// is harness drift, never a quality improvement.
	RunHash     string  `json:"run_hash,omitempty"`
	Value       float64 `json:"value"`
	ToolVersion string  `json:"tool_version,omitempty"`
}

type Lockfile struct {
	V           int                   `json:"v"`
	Fingerprint Fingerprint           `json:"fingerprint"`
	Probes      map[string]ProbeLock  `json:"probes"`
	Metrics     map[string]MetricLock `json:"metrics,omitempty"`
}

type JournalEvent struct {
	Event   string             `json:"e"`
	At      string             `json:"at,omitempty"`
	Commit  string             `json:"commit,omitempty"`
	Dirty   bool               `json:"dirty,omitempty"`
	Verdict string             `json:"verdict,omitempty"`
	Counts  *Counts            `json:"counts,omitempty"`
	Metrics map[string]float64 `json:"metrics,omitempty"`
	Flaky   []string           `json:"flaky,omitempty"`
	Probes  []string           `json:"probe_set,omitempty"`
	Lock    string             `json:"lock,omitempty"`
}

type StateLock struct {
	file *os.File
}

func AcquireStateLock(root string, notice io.Writer) (*StateLock, error) {
	dir := filepath.Join(root, ".vise")
	if err := ensureDirectory(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create .vise state directory: %w", err)
	}
	lockPath := filepath.Join(dir, "run.lock")
	if err := rejectExistingSymlinkOrSpecial(lockPath); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open run lock: %w", err)
	}
	// Try without blocking first, purely so the wait can be announced. A
	// second gate used to print nothing at all while the first ran: no output,
	// no explanation, for as long as the suite takes. An operator reads that
	// as a hang, and an agent with a turn budget gets killed mid-wait with no
	// idea what it was waiting for. The notice goes to stderr, so a --json
	// consumer reading stdout is unaffected.
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if notice != nil {
			fmt.Fprintf(notice, "vise: waiting for the run already in progress to finish (%s)\n", lockPath)
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("lock run state: %w", err)
		}
	}
	return &StateLock{file: file}, nil
}

func (l *StateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	// Both errors are knowable, and returning only the first hid the second:
	// a failed unlock masked a failed close, and the descriptor leaked with
	// nothing said about it.
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	switch {
	case unlockErr != nil && closeErr != nil:
		return fmt.Errorf("release the run lock: %w; and close it: %w", unlockErr, closeErr)
	case unlockErr != nil:
		return unlockErr
	}
	return closeErr
}

func CaptureFingerprint(root string, manifest Manifest) (Fingerprint, error) {
	fingerprint := Fingerprint{OS: runtime.GOOS, Arch: runtime.GOARCH, Stubs: manifest.Stubs}
	if len(manifest.Environment.Fingerprint) == 0 {
		return fingerprint, nil
	}
	before, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		return Fingerprint{}, err
	}
	fingerprint.Env = make(map[string]string, len(manifest.Environment.Fingerprint))
	runner := Runner{Root: root, Manifest: manifest}
	for _, command := range manifest.Environment.Fingerprint {
		result := runner.runShell("environment fingerprint command", "fingerprint", command, 30, nil)
		if result.HarnessError != "" {
			return Fingerprint{}, fmt.Errorf("fingerprint %q: %s", command, result.HarnessError)
		}
		if result.Exit != 0 {
			return Fingerprint{}, fmt.Errorf("fingerprint %q exited %d: %s", command, result.Exit, strings.TrimSpace(string(result.Stderr.Prefix)))
		}
		if result.Stdout.Truncated() {
			return Fingerprint{}, fmt.Errorf("fingerprint %q printed more than %d bytes", command, CaptureLimit)
		}
		fingerprint.Env[command] = strings.TrimSpace(string(result.Stdout.Prefix))
	}
	after, err := GitWorkspaceSnapshot(root, nil)
	if err != nil {
		return Fingerprint{}, err
	}
	if before.Git != after.Git {
		return Fingerprint{}, fmt.Errorf("%s", gitStateMutated("environment fingerprint command"))
	}
	if before.Tracked != after.Tracked {
		return Fingerprint{}, fmt.Errorf("environment fingerprint command modified tracked files")
	}
	if stray := before.ChangedUntracked(after); len(stray) > 0 {
		return Fingerprint{}, errors.New(strayFilesError("environment fingerprint command", stray))
	}
	return fingerprint, nil
}

func FingerprintEqual(a, b Fingerprint) bool {
	return FingerprintMismatch(a, b) == ""
}

// FingerprintMismatch names the first way current differs from recorded, or
// returns "" when the two fingerprints match.
// FingerprintMismatches returns every way current differs from recorded.
//
// FingerprintMismatch names only the first, which is right where the output is
// one line and the reader needs one cause to act on. It is wrong in the review
// diff an operator reads before accepting a new baseline: if the platform and
// two tool versions all moved, being shown one of them and told nothing about
// the others is how a baseline gets accepted for a reason that was only a third
// of the truth.
func FingerprintMismatches(current, recorded Fingerprint) []string {
	var all []string
	if current.OS != recorded.OS || current.Arch != recorded.Arch {
		all = append(all, fmt.Sprintf("platform %s/%s differs from the recorded %s/%s", current.OS, current.Arch, recorded.OS, recorded.Arch))
	}
	if current.Stubs != recorded.Stubs {
		all = append(all, "manifest [stubs] differ from the recorded baseline")
	}
	return append(all, environmentDifferences(current, recorded)...)
}

// environmentDifferences walks the union of both sides, which is the whole
// point and was not what it did.
//
// It iterated the current side only, so a fingerprint command present in the
// baseline and gone from the manifest was never named — caught, if at all, by a
// length check above it. Remove one command and add another and the lengths
// match: the operator was told about the command that appeared and never about
// the one that vanished, while the function's own comment promises that
// reporting one difference and not the others is exactly the failure it exists
// to prevent.
//
// Found by a coding agent working under the gate, which was asked to say what
// looked wrong without fixing it, and did.
func environmentDifferences(current, recorded Fingerprint) []string {
	keys := make(map[string]bool, len(current.Env)+len(recorded.Env))
	for key := range current.Env {
		keys[key] = true
	}
	for key := range recorded.Env {
		keys[key] = true
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)

	var differences []string
	for _, key := range names {
		now, inNow := current.Env[key]
		then, inThen := recorded.Env[key]
		switch {
		case !inThen:
			differences = append(differences, fmt.Sprintf("fingerprint %q is not in the recorded baseline", key))
		case !inNow:
			differences = append(differences, fmt.Sprintf("fingerprint %q was recorded and is no longer declared", key))
		case now != then:
			differences = append(differences, fmt.Sprintf("fingerprint %q output differs from the recorded baseline", key))
		}
	}
	return differences
}

func FingerprintMismatch(current, recorded Fingerprint) string {
	if current.OS != recorded.OS || current.Arch != recorded.Arch {
		return fmt.Sprintf("platform %s/%s differs from the recorded %s/%s", current.OS, current.Arch, recorded.OS, recorded.Arch)
	}
	if current.Stubs != recorded.Stubs {
		return "manifest [stubs] differ from the recorded baseline"
	}
	if differences := environmentDifferences(current, recorded); len(differences) > 0 {
		return differences[0]
	}
	return ""
}

func LoadLockfile(root string) (Lockfile, []byte, error) {
	data, err := readRegularFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		return Lockfile{}, nil, err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Lockfile{}, nil, fmt.Errorf("parse vise.lock: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lock Lockfile
	if err := decoder.Decode(&lock); err != nil {
		return Lockfile{}, nil, describeLockfileParseError(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Lockfile{}, nil, fmt.Errorf("parse vise.lock: trailing JSON data")
	}
	if lock.V != LockVersion {
		// Two paths report the same situation and only one of them used to
		// help. An unknown *key* in the lockfile gets a one-line fix (see
		// schema.go); an unsupported *version* got four words. The version is
		// the coarser and likelier signal of the two, so it was the more
		// common way to be told nothing.
		if lock.V > LockVersion {
			return Lockfile{}, nil, fmt.Errorf("vise.lock is version %d and this vise understands %d: the baseline was written by a newer vise, so upgrade this one (go install ./cmd/vise) and check `vise version --json` for the build you are running", lock.V, LockVersion)
		}
		return Lockfile{}, nil, fmt.Errorf("vise.lock is version %d and this vise understands %d: the baseline predates this vise, so an operator re-records it", lock.V, LockVersion)
	}
	if lock.Probes == nil {
		return Lockfile{}, nil, fmt.Errorf("vise.lock has no probe map")
	}
	if err := validateLockfileHashes(lock); err != nil {
		return Lockfile{}, nil, err
	}
	if err := validateLockfileSchema(lock); err != nil {
		return Lockfile{}, nil, fmt.Errorf("vise.lock: %w", err)
	}
	return lock, data, nil
}

func validateLockfileHashes(lock Lockfile) error {
	for id, probe := range lock.Probes {
		if _, err := HashName(probe.RunHash); err != nil {
			return fmt.Errorf("probe %s run_hash: %w", id, err)
		}
		if _, err := HashName(probe.Stdout); err != nil {
			return fmt.Errorf("probe %s stdout: %w", id, err)
		}
		if _, err := HashName(probe.Stderr); err != nil {
			return fmt.Errorf("probe %s stderr: %w", id, err)
		}
		for path, hash := range probe.Deps {
			if _, err := HashName(hash); err != nil {
				return fmt.Errorf("probe %s dependency %s: %w", id, path, err)
			}
		}
		for path, hash := range probe.Files {
			if _, err := HashName(hash); err != nil {
				return fmt.Errorf("probe %s file %s: %w", id, path, err)
			}
		}
	}
	for id, metric := range lock.Metrics {
		if metric.RunHash == "" {
			continue // recorded before definitions were frozen; verify reports the drift
		}
		if _, err := HashName(metric.RunHash); err != nil {
			return fmt.Errorf("metric %s run_hash: %w", id, err)
		}
	}
	return nil
}

func WriteGeneration(root string, lock Lockfile, blobs map[string][]byte) ([]byte, error) {
	if err := WriteBlobs(root, blobs); err != nil {
		return nil, err
	}
	blobDir := filepath.Join(root, ".vise", "blobs")
	data, err := CanonicalJSON(lock)
	if err != nil {
		return nil, fmt.Errorf("encode vise.lock: %w", err)
	}
	if err := atomicWrite(root, filepath.Join(root, "vise.lock"), data, 0o644); err != nil {
		return nil, fmt.Errorf("write vise.lock: %w", err)
	}
	// The lockfile is committed at this point. Orphan blobs are harmless
	// (SPEC §3.1) and the next record prunes them again, so a prune failure
	// must not turn a written baseline into an exit-2 that looks unwritten.
	_ = pruneBlobs(blobDir, referencedHashes(lock))
	return data, nil
}

func WriteBlobs(root string, blobs map[string][]byte) error {
	stateDir := filepath.Join(root, ".vise")
	if err := ensureDirectory(stateDir, 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	blobDir := filepath.Join(stateDir, "blobs")
	if err := ensureDirectory(blobDir, 0o755); err != nil {
		return fmt.Errorf("create blob directory: %w", err)
	}
	for hash, data := range blobs {
		name, err := HashName(hash)
		if err != nil {
			return err
		}
		path := filepath.Join(blobDir, name)
		if existing, err := readRegularFile(path); err == nil {
			if HashBytes(existing) != hash {
				return fmt.Errorf("blob collision at %s", path)
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := atomicWrite(root, path, data, 0o644); err != nil {
			return fmt.Errorf("write blob %s: %w", hash, err)
		}
	}
	return nil
}

// atomicWrite stages the new content under root/.vise/tmp (ignored by init,
// skipped by the dirty-tree check) and renames it over path, so a crash
// leaves the old file intact and any residue where state is expected, never
// an untracked stray beside vise.lock.
func atomicWrite(root, path string, data []byte, mode os.FileMode) error {
	// The directory this lands in gets the same policy every other directory in
	// this package gets. It was the one state writer that did not: MkdirAll
	// follows symlinks, so a symlinked target directory would have let the
	// staged rename land outside the checkout. The file itself was always safe,
	// because rename replaces a symlink rather than following it.
	//
	// This checks the directory the write lands in, which is the component a
	// probe can create. An intermediate component that is a symlink is not
	// checked, and MkdirAll would follow it — the same limit ensureDirectory
	// has, and worth knowing rather than assuming away.
	dir := filepath.Dir(path)
	if err := persistence.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a real directory, not a symlink or special file", dir)
	}
	staging, err := stateScratchDir(root)
	if err != nil {
		return err
	}
	tmp, err := persistence.CreateStaged(staging, "write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer persistence.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Until this rename the target still holds the previous generation; after
	// it, the new one. There is no moment where it holds neither, so the
	// rename is the commit point.
	if err := persistence.Rename(tmpName, path); err != nil {
		return err
	}
	// Flushing the directory entry is a durability upgrade on an already
	// committed write. Reporting its failure would tell the caller the
	// baseline was not written when it was — the same lie a failed prune used
	// to tell (SPEC §3.1) — so it is best effort.
	_ = persistence.SyncDir(filepath.Dir(path))
	return nil
}

func referencedHashes(lock Lockfile) map[string]bool {
	refs := make(map[string]bool)
	for _, probe := range lock.Probes {
		if !probe.StdoutLarge {
			refs[probe.Stdout] = true
		}
		if !probe.StderrLarge {
			refs[probe.Stderr] = true
		}
		for path, hash := range probe.Files {
			if !probe.FilesLarge[path] {
				refs[hash] = true
			}
		}
	}
	return refs
}

func pruneBlobs(dir string, refs map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		hash := "sha256:" + entry.Name()
		if !refs[hash] {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return fmt.Errorf("prune blob %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

func BlobData(root, hash string, large bool) ([]byte, bool, error) {
	if large {
		return nil, false, nil
	}
	path, err := BlobPath(root, hash)
	if err != nil {
		return nil, false, err
	}
	data, err := readRegularFile(path)
	if err != nil {
		return nil, false, err
	}
	if HashBytes(data) != hash {
		return nil, false, fmt.Errorf("blob %s failed its content hash", hash)
	}
	return data, true, nil
}

func AddObservationBlobs(blobs map[string][]byte, result RunResult) ProbeLock {
	probe := ProbeLock{
		Exit:       result.Exit,
		Stdout:     result.Stdout.Hash,
		Stderr:     result.Stderr.Hash,
		Files:      make(map[string]string, len(result.Files)),
		FilesLarge: make(map[string]bool),
	}
	// An observation larger than the capture bound was never held whole, so
	// it is hash-only in the lockfile and its diff degrades to hashes.
	if data, complete := result.Stdout.Complete(); complete {
		blobs[probe.Stdout] = append([]byte(nil), data...)
	} else {
		probe.StdoutLarge = true
	}
	if data, complete := result.Stderr.Complete(); complete {
		blobs[probe.Stderr] = append([]byte(nil), data...)
	} else {
		probe.StderrLarge = true
	}
	for path, capture := range result.Files {
		probe.Files[path] = capture.Hash
		if data, complete := capture.Complete(); complete {
			blobs[capture.Hash] = append([]byte(nil), data...)
		} else {
			probe.FilesLarge[path] = true
		}
	}
	if len(probe.Files) == 0 {
		probe.Files = nil
		probe.FilesLarge = nil
	} else if len(probe.FilesLarge) == 0 {
		probe.FilesLarge = nil
	}
	return probe
}

// appendJournal is the seam record writes through, so a test can make the last
// of the three writes fail. The first two go through the persistence seam
// already; without this one, the branch that reports "the baseline was written
// but the journal append failed" could stop working unnoticed — and that is
// the message telling an operator the state on disk is half of what they
// asked for.
var appendJournal = AppendJournal

func AppendJournal(root string, event JournalEvent) error {
	dir := filepath.Join(root, ".vise")
	if err := ensureDirectory(dir, 0o755); err != nil {
		return err
	}
	journalPath := filepath.Join(dir, "journal.jsonl")
	if err := rejectExistingSymlinkOrSpecial(journalPath); err != nil {
		return err
	}
	event.At = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(journalPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	// An interrupted append can leave a torn final line without a newline.
	// Drop that fragment before writing so the journal never carries a
	// malformed interior line.
	if err := truncateTornTail(file); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// ReadJournal returns the last limit events from the bounded journal tail.
// A limit of zero or less returns every event the tail holds.
func ReadJournal(root string, limit int) ([]JournalEvent, error) {
	events, _, err := readJournalTail(root)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

// readJournalTail scans at most the final 256 KiB of the journal. truncated
// reports whether older events exist beyond the scanned window.
func readJournalTail(root string) (events []JournalEvent, truncated bool, err error) {
	path := filepath.Join(root, ".vise", "journal.jsonl")
	if err := rejectExistingSymlinkOrSpecial(path); err != nil {
		return nil, false, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	const scanBytes int64 = 256 * 1024
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	offset := info.Size() - scanBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	if offset > 0 {
		truncated = true
		_ = scanner.Scan()
	}
	var lines [][]byte
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	tornTail := false
	if info.Size() > 0 {
		terminated, err := endsWithNewline(file, info.Size())
		if err != nil {
			return nil, false, err
		}
		tornTail = !terminated
	}
	for i, line := range lines {
		var event JournalEvent
		if err := json.Unmarshal(line, &event); err != nil {
			if i == len(lines)-1 && tornTail {
				// A torn final line is what an interrupted append leaves behind;
				// the next append truncates it, so the tail stays readable. A
				// newline-terminated malformed line is corruption and fails.
				break
			}
			return nil, false, fmt.Errorf("parse journal: %w", err)
		}
		events = append(events, event)
	}
	return events, truncated, nil
}

// ConsecutiveFlakes counts flake events for this probe set since the last
// chain boundary: a record, a judged verdict (green or red) whose probe set
// covers this one, or any event at another commit or lock. Events for other probe sets, and indeterminate
// events that judged nothing, are transparent: they neither count nor reset
// the chain, so a rerun refusal or a single-probe verify cannot buy more
// reruns. bounded reports whether a boundary was reached inside events; when
// it is false the count is only a lower bound on the true chain.
func ConsecutiveFlakes(events []JournalEvent, commit, lock string, probes []string) (count int, bounded bool) {
	want := append([]string(nil), probes...)
	sort.Strings(want)
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Lock == "" {
			// Written before any lock existed or by a run that judged nothing;
			// it neither counts nor bounds a chain.
			continue
		}
		if event.Commit != commit || event.Lock != lock || event.Event == "record" {
			return count, true
		}
		if event.Event == "flake" {
			// The budget follows the unstable probe, not the exact set it was
			// running in. Keying on the set gave every subset its own two
			// budgets: a flake seen in the full suite did not count against
			// `verify --probe p`, and an agent diagnosing with --probe walked
			// into a fresh budget without meaning to. Two flakes then a human
			// has to mean two for the probe.
			if flakeTouches(event, want) {
				count++
			}
			continue
		}
		if (event.Verdict == "green" || event.Verdict == "red") && setCovers(event.Probes, want) {
			return count, true
		}
	}
	return count, false
}

// setCovers reports whether a judged event's probe set contains every wanted
// id. An event recorded without a probe set (older journals) counts as
// covering everything.
func setCovers(got, want []string) bool {
	if len(got) == 0 {
		return true
	}
	seen := make(map[string]bool, len(got))
	for _, id := range got {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			return false
		}
	}
	return true
}

// truncateTornTail removes a trailing partial line (no final newline) from
// the journal, which is what an interrupted append leaves behind.
func truncateTornTail(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	const window int64 = 1024 * 1024
	start := size - window
	if start < 0 {
		start = 0
	}
	tail := make([]byte, size-start)
	if _, err := file.ReadAt(tail, start); err != nil && err != io.EOF {
		return err
	}
	if tail[len(tail)-1] == '\n' {
		return nil
	}
	cut := bytes.LastIndexByte(tail, '\n')
	fragment := tail[cut+1:]
	var event JournalEvent
	if json.Unmarshal(fragment, &event) == nil {
		// A complete record that only lost its newline: keep it, terminate it.
		_, err := file.Write([]byte{'\n'})
		return err
	}
	if cut < 0 && start > 0 {
		// No newline anywhere in the window, and the window did not reach the
		// start of the file — so the torn record is longer than a megabyte and
		// `start` is an arbitrary offset inside some earlier record. Truncating
		// there would leave a stump: exactly the malformed interior line this
		// function exists to prevent, and one that makes every later read of
		// the journal fail. Refuse instead, and say what it would take to fix.
		return fmt.Errorf("the journal's final record is longer than the %d-byte repair window, so its torn tail cannot be trimmed without cutting an earlier record; an operator should inspect or delete .vise/journal.jsonl", window)
	}
	keep := start
	if cut >= 0 {
		keep = start + int64(cut) + 1
	}
	return file.Truncate(keep)
}

// endsWithNewline reports whether the journal's last byte terminates a record.
//
// It used to swallow a read error and answer false, which the caller reads as
// "the last line was expected to be incomplete" — so an I/O failure silently
// became a torn tail, the final event was dropped rather than reported, and the
// rerun budget counted one flake fewer than had happened. A budget that fails
// open buys a rerun that should have been refused, which is the one direction
// this file's own comment says it must never fail in.
func endsWithNewline(file *os.File, size int64) (bool, error) {
	last := make([]byte, 1)
	if _, err := file.ReadAt(last, size-1); err != nil {
		return false, fmt.Errorf("read the end of the journal: %w", err)
	}
	return last[0] == '\n', nil
}

// flakeTouches reports whether a journalled flake involved any probe that is
// about to run. It reads the flaky ids when the event has them; an event
// written before those were recorded falls back to its probe set, which is the
// most that can be said about it.
func flakeTouches(event JournalEvent, want []string) bool {
	flaky := event.Flaky
	if len(flaky) == 0 {
		flaky = event.Probes
	}
	if len(flaky) == 0 {
		// Nothing recorded about which probes were involved: count it, because
		// the budget must fail closed.
		return true
	}
	wanted := make(map[string]bool, len(want))
	for _, id := range want {
		wanted[id] = true
	}
	for _, id := range flaky {
		if wanted[id] {
			return true
		}
	}
	return false
}
