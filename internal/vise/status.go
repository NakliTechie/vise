package vise

import (
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

type StatusReport struct {
	V                int            `json:"v"`
	Cmd              string         `json:"cmd"`
	Exit             int            `json:"exit"`
	State            string         `json:"state"`
	Manifest         StatusManifest `json:"manifest"`
	Lock             StatusLock     `json:"lock"`
	PendingProposals int            `json:"pending_proposals"`
	ProposalError    string         `json:"proposal_error,omitempty"`
	Journal          []JournalEvent `json:"journal,omitempty"`
	Next             Next           `json:"next"`
}

func BuildStatus(root string) StatusReport {
	report := StatusReport{V: LockVersion, Cmd: "status", Exit: ExitOK, State: "not-initialized", Next: Next{Action: "record_first", Detail: "run vise init, declare probes, and record a baseline"}}
	manifest, manifestBytes, manifestErr := LoadManifest(root)
	if manifestErr != nil {
		if os.IsNotExist(manifestErr) {
			report.Manifest = StatusManifest{Present: false, Valid: false}
		} else {
			report.Manifest = StatusManifest{Present: true, Valid: false, Error: manifestErr.Error()}
			report.State = "harness-error"
			report.Next = Next{Action: "fix_probe", Detail: "repair vise.toml, then rerun status"}
		}
	} else {
		report.Manifest = StatusManifest{Present: true, Valid: true, Probes: len(manifest.Probes), Metrics: len(manifest.Metrics)}
		report.State = "unrecorded"
		if len(manifest.Probes) == 0 {
			report.Next = Next{Action: "fix_probe", Detail: "declare at least one probe in vise.toml, commit the harness, then run vise record"}
		} else {
			report.Next = Next{Action: "record_first", Detail: "commit the harness, then run vise record"}
		}
	}

	lock, lockBytes, lockErr := LoadLockfile(root)
	if lockErr != nil {
		if os.IsNotExist(lockErr) {
			report.Lock = StatusLock{Present: false, Valid: false}
		} else {
			report.Lock = StatusLock{Present: true, Valid: false, Error: lockErr.Error()}
			report.State = "harness-error"
			report.Next = Next{Action: "fix_probe", Detail: "restore a valid vise.lock, then rerun status"}
		}
	} else {
		report.Lock = StatusLock{Present: true, Valid: true, Probes: len(lock.Probes), Metrics: len(lock.Metrics)}
		commitSet := make(map[string]bool)
		for _, probe := range lock.Probes {
			commitSet[probe.RecordedCommit] = true
		}
		for commit := range commitSet {
			report.Lock.RecordedCommits = append(report.Lock.RecordedCommits, commit)
		}
		sort.Strings(report.Lock.RecordedCommits)
		if manifestErr == nil {
			fingerprint, err := CaptureFingerprint(root, manifest)
			if err != nil {
				report.State = "harness-error"
				report.Lock.Error = err.Error()
				report.Next = Next{Action: "fix_probe", Detail: "repair the environment fingerprint command"}
			} else {
				matches := FingerprintEqual(fingerprint, lock.Fingerprint)
				report.Lock.FingerprintMatch = &matches
				if matches {
					report.State = "ready"
					report.Next = Next{Action: "proceed", Detail: "run vise gate before the next refactor step"}
				} else {
					report.State = "environment-drift"
					report.Next = Next{Action: "human", Detail: "restore the recorded toolchain or ask an operator to re-record"}
				}
			}
			hash, err := TamperHash(root, manifestBytes, lockBytes)
			if err == nil {
				report.Lock.Hash = hash
			} else {
				report.State = "harness-error"
				report.Lock.Error = err.Error()
				report.Next = Next{Action: "fix_probe", Detail: "restore a valid vise.lock and referenced blobs"}
			}
			report.Lock.Drift = baselineDrift(root, manifest, lock)
			if len(report.Lock.Drift) > 0 && report.State == "ready" {
				report.State = "baseline-drift"
				report.Next = Next{Action: "human", Detail: "vise.toml and vise.lock disagree (" + report.Lock.Drift[0] + "); restore the manifest or ask an operator to re-record"}
			}
			if report.State == "ready" {
				if refused, detail := nextGateRefused(root, manifest, report.Lock.Hash); refused {
					report.State = "rerun-refused"
					report.Next = Next{Action: "human", Detail: "the next gate is refused (" + detail + "); commit, re-record, or change the manifest"}
				}
			}
			if len(manifest.Probes) == 0 && report.State != "harness-error" {
				// A lock beside a manifest with no probes judges nothing; gate
				// refuses it, so status must not promise proceed.
				report.State = "harness-error"
				report.Next = Next{Action: "fix_probe", Detail: "manifest declares no [[probe]]; declare at least one probe in vise.toml and record a baseline"}
			}
		}
	}

	// proposals.toml is agent-writable and judges nothing, so a malformed file
	// is reported but never changes the state or the next action.
	proposals, err := LoadProposals(root)
	if err != nil {
		report.ProposalError = err.Error()
	} else {
		report.PendingProposals = len(proposals.Probes)
	}
	journal, err := ReadJournal(root, 5)
	if err != nil {
		report.State = "harness-error"
		report.Next = Next{Action: "fix_probe", Detail: "repair the local journal"}
	} else {
		report.Journal = journal
	}
	return report
}

// baselineDrift runs verify's static manifest-versus-lock checks (no probe
// executes) and returns one "id: detail" line per disagreement, sorted by id.
func baselineDrift(root string, manifest Manifest, lock Lockfile) []string {
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
		if tracked, err := GitTrackedPaths(root, probe.Files); err == nil && len(tracked) > 0 {
			outcome.AddFailure(probe.ID, Failure{Class: "harness", Detail: fmt.Sprintf("declared artifact %q is tracked by git; the next run will refuse it", tracked[0])})
		}
	}
	if len(outcome.Failures) == 0 {
		return nil
	}
	drift := make([]string, 0, len(outcome.Failures))
	for _, id := range sortedKeys(outcome.Failures) {
		drift = append(drift, id+": "+outcome.Failures[id].Detail)
	}
	return drift
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
