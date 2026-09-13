package vise

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLockfileValidationSelectsErrorsInKeyOrder(t *testing.T) {
	// Each case leaves two invalid entries in exactly one map layer. The
	// lexical first must always be reported, even if Go changes iteration order.
	// Removing it must expose the second, not skip the remaining invalid entry.
	for _, phase := range []string{"schema", "hash"} {
		for _, layer := range []string{"probes", "deps", "files", "spec", "metrics"} {
			t.Run(phase+"/"+layer, func(t *testing.T) {
				root := t.TempDir()
				lock := validLockfile(t)
				probes := lock["probes"].(map[string]any)
				probe := probes["behavior"].(map[string]any)
				empty := HashBytes(nil)
				bad := map[string]any{}
				keys := []string{"alpha", "zulu"}
				switch layer {
				case "probes":
					for _, key := range keys {
						entry := map[string]any{"run_hash": empty, "recorded_commit": testCommit, "stdout": empty, "stderr": empty}
						if phase == "schema" {
							entry["recorded_commit"] = "bad-commit"
						} else {
							entry["run_hash"] = "bad-hash"
						}
						bad[key] = entry
					}
					lock["probes"] = bad
				case "metrics":
					if phase == "schema" {
						keys = []string{"!alpha", "!zulu"}
					}
					for _, key := range keys {
						hash := empty
						if phase == "hash" {
							hash = "bad-hash"
						}
						bad[key] = map[string]any{"run_hash": hash, "value": 0}
					}
					lock["metrics"] = bad
				default:
					if phase == "schema" {
						keys = []string{"../alpha", "../zulu"}
					}
					for _, key := range keys {
						hash := empty
						if phase == "hash" {
							hash = "bad-hash"
						}
						bad[key] = hash
					}
					if layer == "spec" {
						probe["pin"] = map[string]any{"spec": bad, "accepted_commit": nil}
					} else {
						probe[layer] = bad
					}
				}

				for _, key := range keys {
					body, err := json.Marshal(lock)
					if err != nil {
						t.Fatal(err)
					}
					writeLockfile(t, root, string(body))
					for attempt := 0; attempt < 100; attempt++ {
						_, _, err := LoadLockfile(root)
						if err == nil || !strings.Contains(err.Error(), key) {
							t.Fatalf("attempt %d: error = %v, want first invalid key %q", attempt, err, key)
						}
					}
					delete(bad, key)
				}
				body, err := json.Marshal(lock)
				if err != nil {
					t.Fatal(err)
				}
				writeLockfile(t, root, string(body))
				if _, _, err := LoadLockfile(root); err != nil {
					t.Fatalf("all invalid entries repaired, still refused: %v", err)
				}
			})
		}
	}
}

func TestLockfileValidationKeepsHashBeforeSchemaPrecedence(t *testing.T) {
	root := t.TempDir()
	lock := validLockfile(t)
	probe := lock["probes"].(map[string]any)["behavior"].(map[string]any)
	probe["recorded_commit"] = "bad-commit"
	probe["stderr"] = "bad-stderr-hash"
	probe["run_hash"] = "bad-run-hash"
	for _, field := range []string{"run_hash", "stderr", "recorded_commit"} {
		body, err := json.Marshal(lock)
		if err != nil {
			t.Fatal(err)
		}
		writeLockfile(t, root, string(body))
		if _, _, err := LoadLockfile(root); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("want %s first, got %v", field, err)
		}
		if field == "recorded_commit" {
			probe[field] = testCommit
		} else {
			probe[field] = HashBytes(nil)
		}
	}
	body, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	writeLockfile(t, root, string(body))
	if _, _, err := LoadLockfile(root); err != nil {
		t.Fatalf("repaired lock refused: %v", err)
	}
}
