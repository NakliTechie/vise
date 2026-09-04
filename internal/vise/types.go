package vise

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	ExitOK             = 0
	ExitBehavior       = 1
	ExitHarness        = 2
	ExitIndeterminate  = 3
	ExitNotInitialized = 4
	ExitMetric         = 5

	LockVersion = 1
	MaxBlobSize = 256 * 1024
)

type Next struct {
	Action string `json:"action"`
	Detail string `json:"detail"`
}

// The closed next.action vocabulary. An agent branches on these, so the set is
// part of the contract: adding a seventh means every caller that switches on
// the field silently falls through to its default, and the agent's next move
// becomes "I do not know". They are constants so a typo is a compile error,
// and TestEveryNextActionIsInTheClosedVocabulary scans the source for any
// literal that slipped past them.
//
// SPEC reserves a seventh, `rerun`, which v0.3 never emits. It gets no
// constant here on purpose: a value an agent can never receive should not be
// declarable, and a constant nothing emits is a promise of a branch that
// never fires.
const (
	NextProceed       = "proceed"        // green; take the next step
	NextRevert        = "revert"         // behavior or a metric moved; undo the change
	NextFixProbe      = "fix_probe"      // the harness is broken; repair it, never the code under test
	NextHuman         = "human"          // nothing the agent may decide; stop and report
	NextRecordFirst   = "record_first"   // no baseline exists; an operator records one
	NextQuarantineAck = "quarantine_ack" // an observation was unstable; stop unless policy tolerates indeterminate
	NextFixInvocation = "fix_invocation" // the command line was malformed; correct it and rerun, nothing in the repo changed
)

// KnownNextActions is the vocabulary as data, for the tests that police it.
var KnownNextActions = []string{NextProceed, NextRevert, NextFixProbe, NextHuman, NextRecordFirst, NextQuarantineAck, NextFixInvocation}

type Counts struct {
	Declared int `json:"declared"`
	Pass     int `json:"pass"`
	Behavior int `json:"behavior"`
	Flaky    int `json:"flaky"`
	Harness  int `json:"harness"`
	// No omitempty, unlike the fields around it once had: a consumer summing
	// the classes should not have to know which names might be missing, and a
	// zero that disappears is the one a reader assumes is there.
	Metric int `json:"metric"`
}

type ExpectedActual struct {
	Exit   *int              `json:"exit,omitempty"`
	Stdout string            `json:"stdout,omitempty"`
	Stderr string            `json:"stderr,omitempty"`
	Files  map[string]string `json:"files,omitempty"`
}

type Failure struct {
	Class  string          `json:"class"`
	Detail string          `json:"detail,omitempty"`
	Expect *ExpectedActual `json:"expect,omitempty"`
	Got    *ExpectedActual `json:"got,omitempty"`
	Diff   string          `json:"diff,omitempty"`
	// Operator marks a failure whose repair is in a file the agent contract
	// forbids an agent from writing: the manifest, the lockfile, the blobs,
	// the journal, or the recorded environment. Without it every harness
	// failure produced next.action fix_probe — "repair the harness" — and an
	// agent handed that about a drifted vise.toml has no legal move, because
	// rule 1 forbids it from touching the file. Two correct instructions
	// pointing opposite ways is the worst thing a contract can do.
	Operator bool `json:"operator,omitempty"`
	// Usage marks a failure that is a complaint about the command line — an
	// unknown command, a mistyped probe id, a flag that does not apply —
	// rather than anything about the repository. Without it a typo answered
	// next.action fix_probe, sending an agent to repair a harness it had not
	// broken; with it the answer is fix_invocation: correct the command and
	// rerun, the repository is untouched.
	Usage bool `json:"usage,omitempty"`
}

type MetricDelta struct {
	Base      float64 `json:"base"`
	Now       float64 `json:"now"`
	Delta     float64 `json:"delta"`
	Direction string  `json:"direction"`
	Enforce   string  `json:"enforce"`
}

type Outcome struct {
	V        int                    `json:"v"`
	Cmd      string                 `json:"cmd"`
	Exit     int                    `json:"exit"`
	Verdict  string                 `json:"verdict,omitempty"`
	Classes  []string               `json:"classes,omitempty"`
	Counts   Counts                 `json:"counts"`
	Failures map[string]Failure     `json:"failures,omitempty"`
	Metrics  map[string]MetricDelta `json:"metrics,omitempty"`
	Lock     string                 `json:"lock,omitempty"`
	Next     Next                   `json:"next"`
}

func NewOutcome(cmd string) Outcome {
	return Outcome{
		V:        LockVersion,
		Cmd:      cmd,
		Exit:     ExitOK,
		Failures: make(map[string]Failure),
		Metrics:  make(map[string]MetricDelta),
		Next:     Next{Action: NextProceed, Detail: "all declared checks matched"},
	}
}

func (o *Outcome) AddFailure(id string, f Failure) {
	if prior, ok := o.Failures[id]; ok {
		*o.counterFor(prior.Class)--
	}
	o.Failures[id] = f
	*o.counterFor(f.Class)++
}

// counterFor selects the count a failure class belongs to.
//
// It used to be two switches, one incrementing and one decrementing, with no
// default arm on either — so a failure whose class was misspelled or empty was
// written into Failures and counted nowhere. Finalize then derived pass as
// declared minus the four known classes, counted it as a pass, fell through
// every case, and returned exit 0, verdict green, next.action proceed, with a
// non-empty failures map in the same JSON object.
//
// A green verdict carrying a failure is the one thing this tool exists not to
// do. An unknown class is now harness, which is the fail-closed answer: the
// verdict becomes indeterminate and an operator is told the harness is wrong,
// which it is, because a class vise does not recognise means vise itself is
// broken.
//
// Found by a coding agent working under the gate, which was blocked by a red
// repository, could not do its task, and read the code it had been asked to
// change instead.
func (o *Outcome) counterFor(class string) *int {
	switch class {
	case "behavior":
		return &o.Counts.Behavior
	case "flake":
		return &o.Counts.Flaky
	case "metric":
		return &o.Counts.Metric
	default:
		// "harness", and anything vise does not recognise.
		return &o.Counts.Harness
	}
}

func (o *Outcome) Finalize() {
	classes := make([]string, 0, 4)
	if o.Counts.Harness > 0 {
		classes = append(classes, "harness")
	}
	if o.Counts.Flaky > 0 {
		classes = append(classes, "flake")
	}
	if o.Counts.Behavior > 0 {
		classes = append(classes, "behavior")
	}
	if o.Counts.Metric > 0 {
		classes = append(classes, "metric")
	}
	o.Classes = classes
	o.Counts.Pass = o.Counts.Declared - o.Counts.Behavior - o.Counts.Flaky - o.Counts.Harness - o.Counts.Metric
	if o.Counts.Pass < 0 {
		o.Counts.Pass = 0
	}

	switch {
	case o.Exit == ExitNotInitialized:
		o.Verdict = "indeterminate"
		o.Next = Next{Action: NextRecordFirst, Detail: "initialize the repository and record a behavior baseline"}
	case o.Counts.Harness > 0:
		o.Exit = ExitHarness
		o.Verdict = "indeterminate"
		switch {
		case o.hasUsageFailure():
			// A complaint about the command line, not the repository. The agent
			// corrects what it typed; nothing in the checkout needs to change.
			o.Next = Next{Action: NextFixInvocation, Detail: "the command was rejected; correct the command line and rerun — the repository is untouched"}
		case o.hasOperatorFailure():
			// human wins whenever any harness failure needs an operator,
			// including when others do not: the agent cannot finish while one
			// of them stands, so telling it to repair the rest would send it
			// round a loop it cannot leave.
			o.Next = Next{Action: NextHuman, Detail: "an operator must restore the harness; the repair is in a file an agent may not write"}
		default:
			o.Next = Next{Action: NextFixProbe, Detail: "repair the harness or restore its declared inputs, then rerun"}
		}
	case o.Counts.Flaky > 0:
		o.Exit = ExitIndeterminate
		o.Verdict = "indeterminate"
		o.Next = Next{Action: NextQuarantineAck, Detail: "the operator must resolve or explicitly tolerate the indeterminate verdict"}
	case o.Counts.Behavior > 0:
		o.Exit = ExitBehavior
		o.Verdict = "red"
		o.Next = Next{Action: NextRevert, Detail: "revert the unintended behavior change or ask an operator to accept a new baseline"}
	case o.Counts.Metric > 0:
		o.Exit = ExitMetric
		o.Verdict = "red"
		o.Next = Next{Action: NextRevert, Detail: "revert the quality regression or change the operator-owned metric policy"}
	default:
		o.Exit = ExitOK
		o.Verdict = "green"
		o.Next = Next{Action: NextProceed, Detail: "all declared checks matched"}
	}
	if len(o.Failures) == 0 {
		o.Failures = nil
	}
	if len(o.Metrics) == 0 {
		o.Metrics = nil
	}
}

func (o Outcome) ClassLabel() string {
	if len(o.Classes) == 0 {
		return ""
	}
	return " [" + strings.Join(o.Classes, ",") + "]"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func CanonicalJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func IntPtr(v int) *int { return &v }

func (o Outcome) hasOperatorFailure() bool {
	for _, failure := range o.Failures {
		if failure.Operator {
			return true
		}
	}
	return false
}

func (o Outcome) hasUsageFailure() bool {
	for _, failure := range o.Failures {
		if failure.Usage {
			return true
		}
	}
	return false
}
