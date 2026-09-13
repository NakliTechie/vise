package vise

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// C14 is exercised through Record's acceptance-carry path, not only through
// ProbeRunHash unit comparisons. Every row starts from its own accepted lock,
// proves unchanged identity carries the original provenance, then changes one
// identity dimension and proves preview treats the still-met pin as new and
// unaccepted without changing the persisted generation.
func TestC14FiniteAcceptanceIdentityDimensionsRequireFreshAcceptance(t *testing.T) {
	type identityChange struct {
		name   string
		mutate func(t *testing.T, root string, manifest *Manifest)
	}
	changes := []identityChange{
		{"command", func(_ *testing.T, _ string, m *Manifest) { m.Probes[0].Run = "sh ./program.sh" }},
		{"timeout", func(_ *testing.T, _ string, m *Manifest) { m.Probes[0].Timeout++ }},
		{"environment", func(_ *testing.T, _ string, m *Manifest) { m.Probes[0].Env = map[string]string{"C14": "one"} }},
		{"artifact declaration and required expectation mapping", func(_ *testing.T, _ string, m *Manifest) {
			m.Probes[0].Files = append(m.Probes[0].Files, "out/b")
			m.Probes[0].Expect.Files["out/b"] = "spec/b"
		}},
		{"expected exit", func(t *testing.T, root string, m *Manifest) {
			m.Probes[0].Expect.Exit = IntPtr(7)
			writeTestFile(t, root, "program.sh", "mkdir -p out; printf stable; printf error >&2; printf artifact > out/a; printf artifact > out/b; exit 7\n")
		}},
		{"stdout stream mapping", func(_ *testing.T, _ string, m *Manifest) { m.Probes[0].Expect.Stdout = "spec/stdout-copy" }},
		{"stderr stream mapping", func(_ *testing.T, _ string, m *Manifest) { m.Probes[0].Expect.Stderr = "spec/stderr-copy" }},
		{"artifact file mapping", func(_ *testing.T, _ string, m *Manifest) { m.Probes[0].Expect.Files["out/a"] = "spec/a-copy" }},
		{"dependency bytes", func(t *testing.T, root string, _ *Manifest) { writeTestFile(t, root, "dep", "changed") }},
		{"stdout spec bytes", func(t *testing.T, root string, _ *Manifest) {
			writeTestFile(t, root, "spec/stdout", "changed")
			writeTestFile(t, root, "program.sh", "mkdir -p out; printf changed; printf error >&2; printf artifact > out/a; printf artifact > out/b\n")
		}},
		{"stderr spec bytes", func(t *testing.T, root string, _ *Manifest) {
			writeTestFile(t, root, "spec/stderr", "changed-error")
			writeTestFile(t, root, "program.sh", "mkdir -p out; printf stable; printf changed-error >&2; printf artifact > out/a; printf artifact > out/b\n")
		}},
		{"artifact spec bytes", func(t *testing.T, root string, _ *Manifest) {
			writeTestFile(t, root, "spec/a", "changed-artifact")
			writeTestFile(t, root, "program.sh", "mkdir -p out; printf stable; printf error >&2; printf changed-artifact > out/a; printf artifact > out/b\n")
		}},
	}

	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nout/\n")
			writeTestFile(t, root, "dep", "original")
			for _, path := range []string{"stdout", "stdout-copy"} {
				writeTestFile(t, root, "spec/"+path, "stable")
			}
			for _, path := range []string{"stderr", "stderr-copy"} {
				writeTestFile(t, root, "spec/"+path, "error")
			}
			for _, path := range []string{"a", "a-copy", "b"} {
				writeTestFile(t, root, "spec/"+path, "artifact")
			}
			writeTestFile(t, root, "program.sh", "mkdir -p out; printf stable; printf error >&2; printf artifact > out/a; printf artifact > out/b\n")
			writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[stubs]
network = "declared-off"
[[probe]]
id = "identity"
run = "sh program.sh"
timeout = 5
deps = ["dep"]
files = ["out/a"]
expect.stdout = "spec/stdout"
expect.stderr = "spec/stderr"
expect.exit = 0
expect.files = { "out/a" = "spec/a" }
`)
			// Normalize the manifest representation before the accepted baseline,
			// so later rows change identity rather than merely changing TOML style.
			canonical, _, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			var initialTOML bytes.Buffer
			if err := toml.NewEncoder(&initialTOML).Encode(canonical); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, root, "vise.toml", initialTOML.String())
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "accepted identity")
			manifest, manifestBytes, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			initial := Record(root, manifest, manifestBytes, RecordOptions{})
			if initial.Outcome.Exit != ExitOK || initial.Pins == nil || len(initial.Pins.Accepted) != 1 {
				t.Fatalf("initial acceptance: %#v %#v", initial.Outcome, initial.Pins)
			}
			lock, _, err := LoadLockfile(root)
			if err != nil || lock.Probes["identity"].Pin.AcceptedCommit == nil {
				t.Fatalf("initial provenance: %#v, %v", lock.Probes["identity"].Pin, err)
			}
			acceptedAt := *lock.Probes["identity"].Pin.AcceptedCommit
			testGit(t, root, "add", "vise.lock", ".vise/blobs")
			testGit(t, root, "commit", "-qm", "commit accepted baseline")

			beforeUnchanged := snapshotAcceptanceGeneration(t, root)
			unchanged := Record(root, manifest, manifestBytes, RecordOptions{Preview: true})
			if unchanged.Outcome.Exit != ExitOK || unchanged.Pins == nil || len(unchanged.Pins.Accepted) != 1 {
				t.Fatalf("unchanged identity did not carry acceptance: %#v %#v", unchanged.Outcome, unchanged.Pins)
			}
			if afterUnchanged := snapshotAcceptanceGeneration(t, root); !reflect.DeepEqual(beforeUnchanged, afterUnchanged) {
				t.Fatalf("unchanged preview mutated persistent generation\nbefore=%#v\nafter=%#v", beforeUnchanged, afterUnchanged)
			}
			persisted := Record(root, manifest, manifestBytes, RecordOptions{Accept: unchanged.Candidate})
			if persisted.Outcome.Exit != ExitOK || persisted.Pins == nil || len(persisted.Pins.Accepted) != 1 {
				t.Fatalf("unchanged exact-digest persistence: %#v %#v", persisted.Outcome, persisted.Pins)
			}
			unchangedLock, _, err := LoadLockfile(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := unchangedLock.Probes["identity"].Pin.AcceptedCommit; got == nil || *got != acceptedAt {
				t.Fatalf("unchanged preview changed provenance: %v, want %s", got, acceptedAt)
			}

			before := snapshotAcceptanceGeneration(t, root)
			change.mutate(t, root, &manifest)
			var encoded bytes.Buffer
			if err := toml.NewEncoder(&encoded).Encode(manifest); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, root, "vise.toml", encoded.String())
			manifest, manifestBytes, err = LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			preview := Record(root, manifest, manifestBytes, RecordOptions{AllowDirty: true, Preview: true})
			if preview.Outcome.Exit != ExitOK || preview.Pins == nil || len(preview.Pins.Accepted) != 0 || len(preview.Pins.PassingUnaccepted) != 1 || preview.Pins.PassingUnaccepted[0] != "identity" {
				t.Fatalf("changed %s identity inherited acceptance: %#v %#v", change.name, preview.Outcome, preview.Pins)
			}
			if preview.Candidate == unchanged.Candidate {
				t.Fatalf("changed %s identity retained the unchanged candidate %s", change.name, preview.Candidate)
			}
			if !strings.Contains(preview.ReviewDiff, "accepted at "+acceptedAt+" -> unaccepted") {
				t.Fatalf("changed %s preview did not disclose provenance transition:\n%s", change.name, preview.ReviewDiff)
			}
			if after := snapshotAcceptanceGeneration(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("changed %s preview mutated persistent generation\nbefore=%#v\nafter=%#v", change.name, before, after)
			}
		})
	}
}
