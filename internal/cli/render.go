package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

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
		fmt.Fprintf(w, "%s [%s] — %s\n", id, failure.Class, failure.Detail)
		if failure.Diff != "" {
			fmt.Fprintln(w, failure.Diff)
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
	fmt.Fprintf(w, "next: %s — %s\n", outcome.Next.Action, outcome.Next.Detail)
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
		fmt.Fprintf(w, "next: %s — %s\n", outcome.Next.Action, outcome.Next.Detail)
	}
}

func renderStatus(w io.Writer, report vise.StatusReport) {
	fmt.Fprintln(w, "VISE STATUS — "+strings.ToUpper(report.State))
	if report.Manifest.Present {
		fmt.Fprintf(w, "manifest: valid=%t · probes=%d · metrics=%d\n", report.Manifest.Valid, report.Manifest.Probes, report.Manifest.Metrics)
	} else {
		fmt.Fprintln(w, "manifest: missing")
	}
	if report.Manifest.Error != "" {
		fmt.Fprintln(w, "manifest error: "+report.Manifest.Error)
	}
	if report.Lock.Present {
		fmt.Fprintf(w, "lockfile: valid=%t · probes=%d · metrics=%d\n", report.Lock.Valid, report.Lock.Probes, report.Lock.Metrics)
	} else {
		fmt.Fprintln(w, "lockfile: missing")
	}
	if report.Lock.FingerprintMatch != nil {
		fmt.Fprintf(w, "fingerprint: match=%t\n", *report.Lock.FingerprintMatch)
	}
	if len(report.Lock.RecordedCommits) > 0 {
		fmt.Fprintln(w, "recorded commits: "+strings.Join(report.Lock.RecordedCommits, ", "))
	}
	if report.Lock.Hash != "" {
		fmt.Fprintln(w, "lock: "+report.Lock.Hash)
	}
	if report.Lock.Error != "" {
		fmt.Fprintln(w, "lock error: "+report.Lock.Error)
	}
	fmt.Fprintf(w, "pending proposals: %d\n", report.PendingProposals)
	if report.ProposalError != "" {
		fmt.Fprintln(w, "proposal error: "+report.ProposalError)
	}
	if len(report.Journal) == 0 {
		fmt.Fprintln(w, "journal: empty")
	} else {
		for _, event := range report.Journal {
			fmt.Fprintf(w, "journal: %s · %s · %s\n", event.Event, event.Commit, event.Verdict)
		}
	}
	fmt.Fprintf(w, "next: %s — %s\n", report.Next.Action, report.Next.Detail)
}
