package vise

import "testing"

// C07 freezes which parts of two unaccepted-pin executions define stability.
// A timeout is compared as a condition plus its complete artifact-presence
// set: bytes collected before the kill are timing residue. Other executions
// retain ordinary byte-for-byte comparison in addition to their condition.
func TestC07PinObservationComparisonBoundary(t *testing.T) {
	capture := func(s string) Capture { return CaptureBytes([]byte(s)) }
	timeout := func(stream, artifact string, missing ...string) RunResult {
		return RunResult{
			TimedOut:     true,
			Tolerated:    true,
			HarnessError: "probe timed out after 1s",
			Stdout:       capture(stream),
			Stderr:       capture("stderr-" + stream),
			Files:        map[string]Capture{"out/present": capture(artifact)},
			MissingFiles: missing,
		}
	}

	t.Run("stable timeout ignores partial stream and artifact bytes", func(t *testing.T) {
		if !pinObservationsEqual(timeout("first", "one", "out/missing"), timeout("second", "two", "out/missing")) {
			t.Fatal("timing-dependent bytes made equal timeout conditions flaky")
		}
	})

	t.Run("timeout artifact presence set must be stable", func(t *testing.T) {
		if pinObservationsEqual(timeout("same", "same", "out/a"), timeout("same", "same", "out/b")) {
			t.Fatal("different missing-artifact sets compared equal")
		}
	})

	t.Run("timeout condition must be stable", func(t *testing.T) {
		completed := timeout("same", "same", "out/missing")
		completed.TimedOut = false
		if pinObservationsEqual(timeout("same", "same", "out/missing"), completed) {
			t.Fatal("a timeout compared equal to a completed execution")
		}
	})

	t.Run("launch condition must be stable", func(t *testing.T) {
		launch := RunResult{Exit: 127, LaunchFailed: true, Tolerated: true, HarnessError: "probe exited 127"}
		ordinary127 := launch
		ordinary127.LaunchFailed = false
		if pinObservationsEqual(launch, ordinary127) {
			t.Fatal("launch failure compared equal to an ordinary exit 127")
		}
	})

	t.Run("signal condition must be stable", func(t *testing.T) {
		terminated := RunResult{Exit: -1, Terminated: true, Tolerated: true, HarnessError: "terminated by signal"}
		ordinarySignalExit := terminated
		ordinarySignalExit.Terminated = false
		if pinObservationsEqual(terminated, ordinarySignalExit) {
			t.Fatal("signal termination compared equal to an ordinary result")
		}
	})

	t.Run("non-timeout artifacts retain complete ordinary comparison", func(t *testing.T) {
		a := RunResult{Files: map[string]Capture{"out/a": capture("a"), "out/b": capture("b")}}
		b := RunResult{Files: map[string]Capture{"out/a": capture("a"), "out/b": capture("changed")}}
		if pinObservationsEqual(a, b) {
			t.Fatal("changed artifact bytes compared equal")
		}
		delete(b.Files, "out/b")
		if pinObservationsEqual(a, b) {
			t.Fatal("different complete artifact sets compared equal")
		}
	})
}
