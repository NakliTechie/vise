package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/NakliTechie/vise/internal/vise"
)

func renderOutcome(w io.Writer, outcome vise.Outcome, label string) {
	fmt.Fprintf(w, "%s %s%s — %d/%d\n", label, strings.ToUpper(outcome.Verdict), outcome.ClassLabel(), outcome.Counts.Pass, outcome.Counts.Declared)
	ids := make([]string, 0, len(outcome.Failures))
	for id := range outcome.Failures {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		failure := outcome.Failures[id]
		fmt.Fprintf(w, "%s [%s] — %s\n", id, failure.Class, terminalSafe(failure.Detail, false))
		if failure.Diff != "" {
			fmt.Fprintln(w, terminalSafe(failure.Diff, true))
		}
	}
	metricIDs := make([]string, 0, len(outcome.Metrics))
	for id := range outcome.Metrics {
		metricIDs = append(metricIDs, id)
	}
	sort.Strings(metricIDs)
	for _, id := range metricIDs {
		metric := outcome.Metrics[id]
		fmt.Fprintf(w, "metric %s: %g -> %g (%+g)\n", id, metric.Base, metric.Now, metric.Delta)
	}
	if outcome.Lock != "" {
		fmt.Fprintln(w, "lock: "+outcome.Lock)
	}
	fmt.Fprintf(w, "next: %s — %s\n", outcome.Next.Action, terminalSafe(outcome.Next.Detail, false))
}

func renderGate(w io.Writer, outcome vise.Outcome, quiet bool) {
	ids := make([]string, 0, len(outcome.Failures))
	for id := range outcome.Failures {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	detail := ""
	if len(ids) > 0 {
		detail = ": " + strings.Join(ids, ", ")
	}
	fmt.Fprintf(w, "GATE %s%s — %d/%d%s\n", strings.ToUpper(outcome.Verdict), outcome.ClassLabel(), outcome.Counts.Pass, outcome.Counts.Declared, detail)
	if quiet {
		return
	}
	if outcome.Lock != "" {
		fmt.Fprintln(w, "lock: "+outcome.Lock)
	}
	if outcome.Exit != vise.ExitOK {
		fmt.Fprintf(w, "next: %s — %s\n", outcome.Next.Action, terminalSafe(outcome.Next.Detail, false))
	}
}

// maxDriftLines keeps the human status bounded when many probes drift; the
// JSON report carries the full list.
const maxDriftLines = 5

func renderStatus(w io.Writer, report vise.StatusReport) {
	renderStatusState(w, report.State)
	renderStatusManifest(w, report.Manifest)
	renderStatusLockfile(w, report.Lock)
	renderStatusFingerprint(w, report.Lock.FingerprintMatch)
	renderStatusRecordedCommits(w, report.Lock.RecordedCommits)
	renderStatusLockHash(w, report.Lock.Hash)
	renderStatusLockError(w, report.Lock.Error)
	renderStatusDrift(w, report.Lock.Drift)
	renderStatusProposals(w, report.PendingProposals, report.ProposalError)
	renderStatusJournalTail(w, report.Journal, report.JournalUnreadable)
	renderStatusNextAction(w, report.Next)
}

func renderStatusState(w io.Writer, state string) {
	fmt.Fprintln(w, "VISE STATUS — "+strings.ToUpper(state))
}

func renderStatusManifest(w io.Writer, manifest vise.StatusManifest) {
	if manifest.Present {
		fmt.Fprintf(w, "manifest: valid=%t · probes=%d · metrics=%d\n", manifest.Valid, manifest.Probes, manifest.Metrics)
	} else {
		fmt.Fprintln(w, "manifest: missing")
	}
	if manifest.Error != "" {
		fmt.Fprintln(w, "manifest error: "+terminalSafe(manifest.Error, false))
	}
}

func renderStatusLockfile(w io.Writer, lock vise.StatusLock) {
	if lock.Present {
		fmt.Fprintf(w, "lockfile: valid=%t · probes=%d · metrics=%d\n", lock.Valid, lock.Probes, lock.Metrics)
	} else {
		fmt.Fprintln(w, "lockfile: missing")
	}
}

func renderStatusFingerprint(w io.Writer, match *bool) {
	if match != nil {
		fmt.Fprintf(w, "fingerprint: match=%t\n", *match)
	}
}

func renderStatusRecordedCommits(w io.Writer, commits []string) {
	if len(commits) > 0 {
		fmt.Fprintln(w, "recorded commits: "+terminalSafe(strings.Join(commits, ", "), false))
	}
}

func renderStatusLockHash(w io.Writer, hash string) {
	if hash != "" {
		fmt.Fprintln(w, "lock: "+terminalSafe(hash, false))
	}
}

func renderStatusLockError(w io.Writer, lockError string) {
	if lockError != "" {
		fmt.Fprintln(w, "lock error: "+terminalSafe(lockError, false))
	}
}

func renderStatusDrift(w io.Writer, drift []string) {
	for i, line := range drift {
		if i == maxDriftLines {
			fmt.Fprintf(w, "drift: … %d more (see --json)\n", len(drift)-maxDriftLines)
			break
		}
		fmt.Fprintln(w, "drift: "+terminalSafe(line, false))
	}
}

func renderStatusProposals(w io.Writer, pending int, proposalError string) {
	fmt.Fprintf(w, "pending proposals: %d\n", pending)
	if proposalError != "" {
		fmt.Fprintln(w, "proposal error: "+terminalSafe(proposalError, false))
	}
}

func renderStatusJournalTail(w io.Writer, journal []vise.JournalEvent, unreadable bool) {
	if unreadable {
		fmt.Fprintln(w, "journal: unreadable")
		return
	}
	if len(journal) == 0 {
		fmt.Fprintln(w, "journal: empty")
		return
	}
	for _, event := range journal {
		details := []string{event.Event, event.Commit, event.Verdict}
		if len(event.Flaky) > 0 {
			details = append(details, "flaky="+strings.Join(event.Flaky, ","))
		}
		if metrics := renderStatusMetrics(event.Metrics); metrics != "" {
			details = append(details, metrics)
		}
		fmt.Fprintln(w, "journal: "+terminalSafe(strings.Join(details, " · "), false))
	}
}

func renderStatusMetrics(metrics map[string]float64) string {
	if len(metrics) == 0 {
		return ""
	}
	metricIDs := make([]string, 0, len(metrics))
	for id := range metrics {
		metricIDs = append(metricIDs, id)
	}
	sort.Strings(metricIDs)
	pairs := make([]string, 0, len(metricIDs))
	for _, id := range metricIDs {
		pairs = append(pairs, fmt.Sprintf("%s=%g", id, metrics[id]))
	}
	return "metrics=" + strings.Join(pairs, ",")
}

func renderStatusNextAction(w io.Writer, next vise.Next) {
	fmt.Fprintf(w, "next: %s — %s\n", next.Action, terminalSafe(next.Detail, false))
}

func terminalSafe(value string, allowNewline bool) string {
	var b strings.Builder
	for _, r := range value {
		if allowNewline && r == '\n' {
			b.WriteRune(r)
			continue
		}
		if r == '\t' {
			b.WriteRune(r)
			continue
		}
		// U+2028 and U+2029 are line and paragraph separators, which
		// unicode.IsControl does not report: they are category Zl and Zp. They
		// still end a line for anything that reads output line by line, so a
		// single-line field containing one can carry a second line nobody
		// expected.
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// renderDoctor prints the operator's readiness check. Bounded like every
// other rendering: one line per finding, its remedy beneath it, and nothing
// that grows with the size of the repository.
func renderDoctor(w io.Writer, report vise.DoctorReport) {
	if report.Ready {
		fmt.Fprintln(w, "DOCTOR READY — nothing to fix before an agent works here")
	} else {
		fmt.Fprintf(w, "DOCTOR — %d finding(s)\n", len(report.Findings))
	}
	// Escaped like every other human rendering. A finding's detail can be a
	// manifest parse error, which carries bytes from vise.toml — so without
	// this a hostile manifest drives the operator's terminal through the one
	// command they were told to run first.
	for _, finding := range report.Findings {
		fmt.Fprintf(w, "%s — %s\n", terminalSafe(finding.Check, false), terminalSafe(finding.Detail, false))
		fmt.Fprintf(w, "  fix: %s\n", terminalSafe(finding.Remedy, false))
	}
	fmt.Fprintf(w, "next: %s — %s\n", terminalSafe(report.Next.Action, false), terminalSafe(report.Next.Detail, false))
}
