package vise

import (
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
			}
		}
	}

	proposals, err := LoadProposals(root)
	if err != nil {
		report.ProposalError = err.Error()
		if report.State != "harness-error" {
			report.State = "harness-error"
			report.Next = Next{Action: "fix_probe", Detail: "repair .vise/proposals.toml"}
		}
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
