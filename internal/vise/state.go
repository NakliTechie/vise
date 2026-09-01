package vise

import (
	"bufio"
	"bytes"
	"encoding/json"
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

func AcquireStateLock(root string) (*StateLock, error) {
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock run state: %w", err)
	}
	return &StateLock{file: file}, nil
}

func (l *StateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func CaptureFingerprint(root string, manifest Manifest) (Fingerprint, error) {
	fingerprint := Fingerprint{OS: runtime.GOOS, Arch: runtime.GOARCH, Stubs: manifest.Stubs}
	if len(manifest.Environment.Fingerprint) == 0 {
		return fingerprint, nil
	}
	before, err := GitTrackedSnapshot(root)
	if err != nil {
		return Fingerprint{}, err
	}
	fingerprint.Env = make(map[string]string, len(manifest.Environment.Fingerprint))
	runner := Runner{Root: root, Manifest: manifest}
	for _, command := range manifest.Environment.Fingerprint {
		result := runner.runShell("fingerprint", command, 30, nil)
		if result.HarnessError != "" {
			return Fingerprint{}, fmt.Errorf("fingerprint %q: %s", command, result.HarnessError)
		}
		if result.Exit != 0 {
			return Fingerprint{}, fmt.Errorf("fingerprint %q exited %d: %s", command, result.Exit, strings.TrimSpace(string(result.Stderr)))
		}
		fingerprint.Env[command] = strings.TrimSpace(string(result.Stdout))
	}
	after, err := GitTrackedSnapshot(root)
	if err != nil {
		return Fingerprint{}, err
	}
	if before != after {
		return Fingerprint{}, fmt.Errorf("environment fingerprint command modified tracked files")
	}
	return fingerprint, nil
}

func FingerprintEqual(a, b Fingerprint) bool {
	return FingerprintMismatch(a, b) == ""
}

// FingerprintMismatch names the first way current differs from recorded, or
// returns "" when the two fingerprints match.
func FingerprintMismatch(current, recorded Fingerprint) string {
	if current.OS != recorded.OS || current.Arch != recorded.Arch {
		return fmt.Sprintf("platform %s/%s differs from the recorded %s/%s", current.OS, current.Arch, recorded.OS, recorded.Arch)
	}
	if current.Stubs != recorded.Stubs {
		return "manifest [stubs] differ from the recorded baseline"
	}
	if len(current.Env) != len(recorded.Env) {
		return "the set of fingerprint commands differs from the recorded baseline"
	}
	for _, key := range sortedKeys(current.Env) {
		if recorded.Env[key] != current.Env[key] {
			return fmt.Sprintf("fingerprint %q output differs from the recorded baseline", key)
		}
	}
	return ""
}

func LoadLockfile(root string) (Lockfile, []byte, error) {
	data, err := readRegularFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		return Lockfile{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lock Lockfile
	if err := decoder.Decode(&lock); err != nil {
		return Lockfile{}, nil, fmt.Errorf("parse vise.lock: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Lockfile{}, nil, fmt.Errorf("parse vise.lock: trailing JSON data")
	}
	if lock.V != LockVersion {
		return Lockfile{}, nil, fmt.Errorf("vise.lock version %d is unsupported", lock.V)
	}
	if lock.Probes == nil {
		return Lockfile{}, nil, fmt.Errorf("vise.lock has no probe map")
	}
	if err := validateLockfileHashes(lock); err != nil {
		return Lockfile{}, nil, err
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
	if err := pruneBlobs(blobDir, referencedHashes(lock)); err != nil {
		return nil, err
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	staging := filepath.Join(root, ".vise", "tmp")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(staging, "write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dirHandle, err := os.Open(filepath.Dir(path))
	if err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
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
		Stdout:     HashBytes(result.Stdout),
		Stderr:     HashBytes(result.Stderr),
		Files:      make(map[string]string, len(result.Files)),
		FilesLarge: make(map[string]bool),
	}
	if len(result.Stdout) > MaxBlobSize {
		probe.StdoutLarge = true
	} else {
		blobs[probe.Stdout] = append([]byte(nil), result.Stdout...)
	}
	if len(result.Stderr) > MaxBlobSize {
		probe.StderrLarge = true
	} else {
		blobs[probe.Stderr] = append([]byte(nil), result.Stderr...)
	}
	for path, data := range result.Files {
		hash := HashBytes(data)
		probe.Files[path] = hash
		if len(data) > MaxBlobSize {
			probe.FilesLarge[path] = true
		} else {
			blobs[hash] = append([]byte(nil), data...)
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
	for i, line := range lines {
		var event JournalEvent
		if err := json.Unmarshal(line, &event); err != nil {
			if i == len(lines)-1 {
				// A torn final line is what an interrupted append leaves behind;
				// the next append starts a fresh line, so the tail stays readable.
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
	wantKey := strings.Join(want, "\x00")
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Commit != commit || event.Lock != lock || event.Event == "record" {
			return count, true
		}
		if event.Event == "flake" {
			got := append([]string(nil), event.Probes...)
			sort.Strings(got)
			if strings.Join(got, "\x00") == wantKey {
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
	keep := start
	if cut >= 0 {
		keep = start + int64(cut) + 1
	}
	return file.Truncate(keep)
}
