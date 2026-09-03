package vise

import (
	"bytes"
	"fmt"
	"os"
	"sort"
)

type StatusManifest struct {
	Present bool   `json:"present"`
	Valid   bool   `json:"valid"`
	Probes  int    `json:"probes"`
	Metrics int    `json:"metrics"`
	Error   string `json:"error,omitempty"`
}

type StatusLock struct {
	Present          bool     `json:"present"`
	Valid            bool     `json:"valid"`
	Probes           int      `json:"probes"`
	Metrics          int      `json:"metrics"`
	FingerprintMatch *bool    `json:"fingerprint_match,omitempty"`
	RecordedCommits  []string `json:"recorded_commits,omitempty"`
	Hash             string   `json:"hash,omitempty"`
	Error            string   `json:"error,omitempty"`
	// Drift lists every way vise.toml and vise.lock disagree without running a
	// probe: missing or extra ids, changed probe definitions, changed
	// dependency hashes, missing blobs. Non-empty drift means gate will refuse.
	Drift []string `json:"drift,omitempty"`
}

// StatusTool identifies the binary answering the question. Two builds print
// the same version string, so an agent that only reads `version` cannot tell a
// stale tool from broken state.
type StatusTool struct {
	Version  string `json:"version"`
	Revision string `json:"revision,omitempty"`
	// Three states, not two: built clean, built dirty, and no version stamps
	// at all. A plain bool had only the first two and reported the third as
	// `"modified": false` — a claim that the tree was clean, made by a binary
	// that has no way to know. That is the exact shape of thing vise exists to
	// refuse, asserted by vise about itself.
	//
	// A pointer gives the third state a representation: present true is dirty,
	// present false is clean, absent is unknown. omitempty on a pointer omits
	// only nil, so a known-false is still written.
	Modified *bool `json:"modified,omitempty"`
}

type StatusReport struct {
	V                int            `json:"v"`
	Cmd              string         `json:"cmd"`
	Exit             int            `json:"exit"`
	State            string         `json:"state"`
	Tool             *StatusTool    `json:"tool,omitempty"`
	Manifest         StatusManifest `json:"manifest"`
	Lock             StatusLock     `json:"lock"`
	PendingProposals int            `json:"pending_proposals"`
	ProposalError    string         `json:"proposal_error,omitempty"`
	Journal          []JournalEvent `json:"journal,omitempty"`
	// JournalUnreadable distinguishes a journal that could not be read from one
	// that holds nothing. Both produced an empty Journal, so the screen said
	// "journal: empty" two lines above "repair the local journal" — one line
	// contradicting another on the one screen an agent reads before it acts.
	JournalUnreadable bool `json:"journal_unreadable,omitempty"`
	Next              Next `json:"next"`
}

func BuildStatus(root string) StatusReport {
	report := StatusReport{V: LockVersion, Cmd: "status", Exit: ExitOK, State: "not-initialized", Next: Next{Action: NextRecordFirst, Detail: "run vise init, declare probes, and record a baseline"}}
	manifest, manifestBytes, manifestErr := buildManifestStatus(root, &report)
	buildLockStatus(root, manifest, manifestBytes, manifestErr, &report)
	buildProposalsStatus(root, manifest, &report)
	buildJournalStatus(root, &report)
	return report
}

func buildProposalsStatus(root string, manifest Manifest, report *StatusReport) {
	// proposals.toml is agent-writable and judges nothing, so a malformed file
	// is reported but never changes the state or the next action.
	proposals, err := LoadProposals(root, manifest)
	if err != nil {
		report.ProposalError = err.Error()
	} else {
		report.PendingProposals = len(proposals.Probes)
	}
}

// Every next action below is human, not fix_probe. The agent contract forbids
// an agent from writing vise.toml, vise.lock, the blobs, or the journal, so
// telling it to repair one of them offered a choice between disobeying the
// rules and disobeying the tool. fix_probe stays for the harness an agent may
// touch: a probe command its own change broke.
func buildJournalStatus(root string, report *StatusReport) {
	journal, err := ReadJournal(root, 5)
	if err != nil {
		report.State = "harness-error"
		report.JournalUnreadable = true
		report.Next = Next{Action: NextHuman, Detail: "repair the local journal (.vise/journal.jsonl); an agent may not write it"}
	} else {
		report.Journal = journal
	}
}

func buildLockStatus(root string, manifest Manifest, manifestBytes []byte, manifestErr error, report *StatusReport) {
	// status takes no lock, so it can read one generation of the lockfile and
	// then reach for blobs a concurrent record has already pruned. The retry
	// is for the whole lock section, not one field: patching the hash from a
	// second generation onto commits and drift derived from the first would
	// print a report describing a baseline that never existed, which is worse
	// than the spurious harness error it was meant to avoid.
	for attempt := 0; attempt < 2; attempt++ {
		before := *report
		lock, lockBytes, valid := buildLockfilePresenceAndValidity(root, report)
		if !valid {
			return
		}
		buildLockCounts(lock, report)
		buildRecordedCommits(lock, report)
		if manifestErr != nil {
			return
		}
		buildFingerprintComparison(root, manifest, lock, report)
		tornRead := buildTamperHash(root, manifestBytes, lockBytes, report)
		if tornRead && attempt == 0 {
			// The lockfile moved while we were reading it. Start the whole
			// section again from the generation that is there now.
			*report = before
			continue
		}
		buildStaticDrift(root, manifest, lock, report)
		buildRerunRefusal(root, manifest, report)
		buildEmptyManifestRefusal(manifest, report)
		return
	}
}

func buildLockfilePresenceAndValidity(root string, report *StatusReport) (Lockfile, []byte, bool) {
	lock, lockBytes, err := LoadLockfile(root)
	if err == nil {
		report.Lock = StatusLock{Present: true, Valid: true}
		return lock, lockBytes, true
	}
	if os.IsNotExist(err) {
		report.Lock = StatusLock{Present: false, Valid: false}
		return Lockfile{}, nil, false
	}
	report.Lock = StatusLock{Present: true, Valid: false, Error: err.Error()}
	report.State = "harness-error"
	report.Next = Next{Action: NextHuman, Detail: "restore a valid vise.lock; an agent may not write it"}
	return Lockfile{}, nil, false
}

func buildLockCounts(lock Lockfile, report *StatusReport) {
	report.Lock.Probes = len(lock.Probes)
	report.Lock.Metrics = len(lock.Metrics)
}

func buildRecordedCommits(lock Lockfile, report *StatusReport) {
	commitSet := make(map[string]bool)
	for _, probe := range lock.Probes {
		commitSet[probe.RecordedCommit] = true
	}
	for commit := range commitSet {
		report.Lock.RecordedCommits = append(report.Lock.RecordedCommits, commit)
	}
	sort.Strings(report.Lock.RecordedCommits)
}

func buildFingerprintComparison(root string, manifest Manifest, lock Lockfile, report *StatusReport) {
	fingerprint, err := CaptureFingerprint(root, manifest)
	if err != nil {
		report.State = "harness-error"
		report.Lock.Error = err.Error()
		report.Next = Next{Action: NextHuman, Detail: "repair the environment fingerprint command in vise.toml; an agent may not write it"}
		return
	}
	matches := FingerprintEqual(fingerprint, lock.Fingerprint)
	report.Lock.FingerprintMatch = &matches
	if matches {
		report.State = "ready"
		report.Next = Next{Action: NextProceed, Detail: "run vise gate before the next refactor step"}
	} else {
		report.State = "environment-drift"
		report.Next = Next{Action: NextHuman, Detail: "restore the recorded toolchain or ask an operator to re-record"}
	}
}

// buildTamperHash records the lock hash, and reports whether the failure it
// saw looks like a torn read rather than a broken baseline.
func buildTamperHash(root string, manifestBytes, lockBytes []byte, report *StatusReport) (tornRead bool) {
	hash, err := TamperHash(root, manifestBytes, lockBytes)
	if err == nil {
		report.Lock.Hash = hash
		return false
	}
	// If the bytes on disk are no longer the bytes we hashed, a record ran
	// between the two reads and the blobs this generation referenced are gone
	// on purpose. That is not a baseline to repair.
	if _, current, reloadErr := LoadLockfile(root); reloadErr == nil && !bytes.Equal(current, lockBytes) {
		return true
	}
	report.State = "harness-error"
	report.Lock.Error = err.Error()
	report.Next = Next{Action: NextHuman, Detail: "restore a valid vise.lock and referenced blobs; an agent may not write them"}
	return false
}

func buildStaticDrift(root string, manifest Manifest, lock Lockfile, report *StatusReport) {
	drift, operatorOwned := baselineDrift(root, manifest, lock)
	report.Lock.Drift = drift
	if len(drift) == 0 || report.State != "ready" {
		return
	}
	report.State = "baseline-drift"
	// The same ownership question the gate answers, answered the same way.
	//
	// status said human for every kind of drift, and the gate said fix_probe
	// for the kinds an agent caused and can undo — a declared input it edited,
	// most often. The contract tells an agent to read status first, so it was
	// told to stop and then, seconds later, told to fix its own change. Two
	// correct-sounding instructions pointing opposite ways is the worst thing
	// this tool can do, and it was doing it about the most ordinary drift
	// there is.
	//
	// baselineDrift already runs the same validators as verify, so the flag is
	// computed and was being discarded on the way to a string.
	if operatorOwned {
		report.Next = Next{Action: NextHuman, Detail: "vise.toml and vise.lock disagree (" + drift[0] + "); restore the manifest or ask an operator to re-record"}
		return
	}
	// Deliberately not naming the manifest and the lockfile here, though they
	// are what disagree. This branch means the agent moved something it may
	// repair, and a fix_probe message that names a file the agent may not
	// write is the exact contradiction the guard in operator_test.go exists to
	// catch — it caught this line when it did. The drift entry already says
	// which probe and what moved.
	report.Next = Next{Action: NextFixProbe, Detail: "the baseline no longer matches what a probe reads (" + drift[0] + "); restore what your change moved, then rerun"}
}

func buildRerunRefusal(root string, manifest Manifest, report *StatusReport) {
	if report.State != "ready" {
		return
	}
	if refused, detail := nextGateRefused(root, manifest, report.Lock.Hash); refused {
		report.State = "rerun-refused"
		report.Next = Next{Action: NextHuman, Detail: "the next gate is refused (" + detail + "); commit, re-record, or change the manifest"}
	}
}

func buildEmptyManifestRefusal(manifest Manifest, report *StatusReport) {
	if len(manifest.Probes) == 0 && report.State != "harness-error" {
		// A lock beside a manifest with no probes judges nothing; gate
		// refuses it, so status must not promise proceed.
		report.State = "harness-error"
		report.Next = Next{Action: NextHuman, Detail: "manifest declares no [[probe]]; an operator declares one in vise.toml and records a baseline"}
	}
}

func buildManifestStatus(root string, report *StatusReport) (Manifest, []byte, error) {
	manifest, manifestBytes, manifestErr := LoadManifest(root)
	if manifestErr != nil {
		if os.IsNotExist(manifestErr) {
			report.Manifest = StatusManifest{Present: false, Valid: false}
		} else {
			report.Manifest = StatusManifest{Present: true, Valid: false, Error: manifestErr.Error()}
			report.State = "harness-error"
			report.Next = Next{Action: NextHuman, Detail: "repair vise.toml; an agent may not write it"}
		}
	} else {
		report.Manifest = StatusManifest{Present: true, Valid: true, Probes: len(manifest.Probes), Metrics: len(manifest.Metrics)}
		report.State = "unrecorded"
		if len(manifest.Probes) == 0 {
			report.Next = Next{Action: NextHuman, Detail: "an operator declares a probe in vise.toml, commits the harness, and runs vise record"}
		} else {
			report.Next = Next{Action: NextRecordFirst, Detail: "commit the harness, then run vise record"}
		}
	}
	return manifest, manifestBytes, manifestErr
}

// baselineDrift runs verify's static manifest-versus-lock checks (no probe
// executes) and returns one "id: detail" line per disagreement, sorted by id.
// baselineDrift reports the manifest-versus-lock differences a gate would find
// without running a probe, and whether any of them is an operator's to repair.
func baselineDrift(root string, manifest Manifest, lock Lockfile) ([]string, bool) {
	outcome := NewOutcome("status")
	validateProbeSet(&outcome, manifest, lock)
	validateMetricSet(&outcome, manifest, lock)
	for _, probe := range manifest.Probes {
		validateProbeEntry(root, &outcome, probe, lock)
	}
	for _, probe := range manifest.Probes {
		if _, exists := outcome.Failures[probe.ID]; exists {
			continue
		}
		tracked, err := GitTrackedPaths(root, probe.Files)
		switch {
		case err != nil:
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: "cannot inspect declared artifacts: " + err.Error()})
		case len(tracked) > 0:
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: fmt.Sprintf("declared artifact %q is tracked by git; the next run will refuse it", tracked[0])})
		}
	}
	if len(outcome.Failures) == 0 {
		return nil, false
	}
	drift := make([]string, 0, len(outcome.Failures))
	for _, id := range sortedKeys(outcome.Failures) {
		drift = append(drift, id+": "+outcome.Failures[id].Detail)
	}
	return drift, outcome.hasOperatorFailure()
}

// nextGateRefused asks the rerun limit whether a full gate at HEAD would be
// refused right now. Errors read as "not refused": status must stay bounded
// and exit 0, and gate itself reports the failure with its remedy.
func nextGateRefused(root string, manifest Manifest, lockHash string) (bool, string) {
	commit, err := GitHead(root)
	if err != nil {
		return false, ""
	}
	ids := make([]string, 0, len(manifest.Probes)+len(manifest.Metrics))
	for _, probe := range manifest.Probes {
		ids = append(ids, probe.ID)
	}
	for _, metric := range manifest.Metrics {
		ids = append(ids, metric.ID)
	}
	sort.Strings(ids)
	refused, detail, err := RerunLimitReached(root, commit, lockHash, ids)
	if err != nil {
		return false, ""
	}
	return refused, detail
}
