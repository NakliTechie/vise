package vise

import (
	"encoding/json"
	"fmt"
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

type Counts struct {
	Declared int `json:"declared"`
	Pass     int `json:"pass"`
	Behavior int `json:"behavior"`
	Flaky    int `json:"flaky"`
	Harness  int `json:"harness"`
	Metric   int `json:"metric,omitempty"`
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
		Next:     Next{Action: "proceed", Detail: "all declared checks matched"},
	}
}

func (o *Outcome) AddFailure(id string, f Failure) {
	o.Failures[id] = f
	switch f.Class {
	case "behavior":
		o.Counts.Behavior++
	case "flake":
		o.Counts.Flaky++
	case "harness":
		o.Counts.Harness++
	case "metric":
		o.Counts.Metric++
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
	o.Counts.Pass = o.Counts.Declared - o.Counts.Behavior - o.Counts.Flaky - o.Counts.Harness
	if o.Counts.Pass < 0 {
		o.Counts.Pass = 0
	}

	switch {
	case o.Exit == ExitNotInitialized:
		o.Verdict = "indeterminate"
		o.Next = Next{Action: "record_first", Detail: "initialize the repository and record a behavior baseline"}
	case o.Counts.Harness > 0:
		o.Exit = ExitHarness
		o.Verdict = "indeterminate"
		o.Next = Next{Action: "fix_probe", Detail: "repair the harness or restore its declared inputs, then rerun"}
	case o.Counts.Flaky > 0:
		o.Exit = ExitIndeterminate
		o.Verdict = "indeterminate"
		o.Next = Next{Action: "quarantine_ack", Detail: "the operator must resolve or explicitly tolerate the indeterminate verdict"}
	case o.Counts.Behavior > 0:
		o.Exit = ExitBehavior
		o.Verdict = "red"
		o.Next = Next{Action: "revert", Detail: "revert the unintended behavior change or ask an operator to accept a new baseline"}
	case o.Counts.Metric > 0:
		o.Exit = ExitMetric
		o.Verdict = "red"
		o.Next = Next{Action: "revert", Detail: "revert the quality regression or change the operator-owned metric policy"}
	default:
		o.Exit = ExitOK
		o.Verdict = "green"
		o.Next = Next{Action: "proceed", Detail: "all declared checks matched"}
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

func formatNumber(v float64) string {
	return fmt.Sprintf("%g", v)
}
