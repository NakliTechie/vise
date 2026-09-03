package vise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fifteen mutations of manifest and proposal validation survived the suite:
// accepting the wrong version, the wrong stub value, malformed ids, blank
// commands, out-of-range timeouts, reserved environment names, duplicate
// artifact aliases, and every one of the five metric-field checks. Each rule
// here is a rule an operator or an agent can violate, so each gets a case.
func TestManifestValidationEnforcesEveryRule(t *testing.T) {
	root := testGitRepo(t)

	// The smallest manifest that passes, so each case below differs from a
	// valid one by exactly the rule it is testing.
	valid := func() Manifest {
		return Manifest{
			Vise:   ViseSettings{Version: LockVersion},
			Stubs:  StubSettings{Network: "declared-off"},
			Probes: []Probe{{ID: "ok", Run: "printf ok", Timeout: 30}},
		}
	}
	if err := valid().Validate(root); err != nil {
		t.Fatalf("the baseline manifest is not valid: %v", err)
	}

	tests := []struct {
		name   string
		change func(m *Manifest)
		wantIn string
	}{
		{"an unsupported version", func(m *Manifest) { m.Vise.Version = 2 }, "vise.version"},
		{"a missing version", func(m *Manifest) { m.Vise.Version = 0 }, "vise.version"},
		{"network that is not declared-off", func(m *Manifest) { m.Stubs.Network = "on" }, "declared-off"},
		{"an empty probe id", func(m *Manifest) { m.Probes[0].ID = "" }, "must match"},
		{"a probe id with a space", func(m *Manifest) { m.Probes[0].ID = "two words" }, "must match"},
		{"a probe id with a slash", func(m *Manifest) { m.Probes[0].ID = "a/b" }, "must match"},
		{"a reserved probe id", func(m *Manifest) { m.Probes[0].ID = "vise" }, "reserved"},
		{"a blank probe command", func(m *Manifest) { m.Probes[0].Run = "   " }, "run must not be empty"},
		{"a zero timeout", func(m *Manifest) { m.Probes[0].Timeout = 0 }, "timeout must be between"},
		{"a negative timeout", func(m *Manifest) { m.Probes[0].Timeout = -1 }, "timeout must be between"},
		{"a timeout beyond a day", func(m *Manifest) { m.Probes[0].Timeout = 86401 }, "timeout must be between"},
		{"two probes with one id", func(m *Manifest) {
			m.Probes = append(m.Probes, Probe{ID: "ok", Run: "printf again", Timeout: 30})
		}, "duplicate id"},
		{"a probe and a metric sharing an id", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "ok", Run: "printf 1", Direction: "down", Enforce: "none", Timeout: 30}}
		}, "duplicate id"},
		{"an artifact declared twice", func(m *Manifest) {
			m.Probes[0].Files = []string{"out/a.txt", "./out/a.txt"}
		}, "duplicates"},
		{"a dependency declared twice", func(m *Manifest) {
			m.Probes[0].Deps = []string{"fixtures/in.csv", "fixtures/./in.csv"}
		}, "duplicates"},
		{"an artifact escaping the repository", func(m *Manifest) {
			m.Probes[0].Files = []string{"../outside.txt"}
		}, "inside the repository"},
		{"an artifact targeting git", func(m *Manifest) {
			m.Probes[0].Files = []string{".git/index"}
		}, "Git metadata"},
		{"an empty environment key", func(m *Manifest) {
			m.Probes[0].Env = map[string]string{"": "x"}
		}, "invalid key"},
		{"an environment key with an equals sign", func(m *Manifest) {
			m.Probes[0].Env = map[string]string{"A=B": "x"}
		}, "invalid key"},
		{"an environment key vise reserves", func(m *Manifest) {
			m.Probes[0].Env = map[string]string{"TMPDIR": "/tmp"}
		}, "reserved variable"},
		{"a blank metric command", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "m", Run: " ", Direction: "down", Enforce: "none", Timeout: 30}}
		}, "run must not be empty"},
		{"a metric with no direction", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "m", Run: "printf 1", Enforce: "none", Timeout: 30}}
		}, "direction must be"},
		{"a metric direction that is neither", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "m", Run: "printf 1", Direction: "sideways", Enforce: "none", Timeout: 30}}
		}, "direction must be"},
		{"a metric enforcement that is neither", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "m", Run: "printf 1", Direction: "down", Enforce: "always", Timeout: 30}}
		}, "enforce must be"},
		{"a metric timeout out of range", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "m", Run: "printf 1", Direction: "down", Enforce: "none", Timeout: 0}}
		}, "timeout must be between"},
		{"a metric id that is reserved", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "vise", Run: "printf 1", Direction: "down", Enforce: "none", Timeout: 30}}
		}, "reserved"},
		{"a metric environment key vise reserves", func(m *Manifest) {
			m.Metrics = []Metric{{ID: "m", Run: "printf 1", Direction: "down", Enforce: "none", Timeout: 30, Env: map[string]string{"TMPDIR": "/tmp"}}}
		}, "reserved variable"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := valid()
			test.change(&manifest)
			err := manifest.Validate(root)
			if err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
			if !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error %q does not say %q", err, test.wantIn)
			}
		})
	}
}

// Nine mutations of lockfile validation survived: accepting an unsupported
// version, accepting a missing probes map, rejecting a valid 64-character Git
// object name, and skipping hash validation for stdout, stderr, dependencies,
// artifacts and metrics. This is the code that decides whether a baseline can
// be trusted at all, so every field it inspects gets one malformed value.
func TestLockfileValidationEnforcesEveryRule(t *testing.T) {
	root := testGitRepo(t)
	good := "sha256:" + strings.Repeat("a", 64)
	sha1Commit := strings.Repeat("b", 40)
	sha256Commit := strings.Repeat("c", 64)

	valid := func() string {
		return `{"v":1,"probes":{"p":{"run_hash":"` + good + `","recorded_commit":"` + sha1Commit +
			`","exit":0,"stdout":"` + good + `","stderr":"` + good + `"}}}`
	}
	write := func(t *testing.T, body string) error {
		t.Helper()
		writeTestFile(t, root, "vise.lock", body)
		_, _, err := LoadLockfile(root)
		return err
	}

	if err := write(t, valid()); err != nil {
		t.Fatalf("the baseline lockfile is not valid: %v", err)
	}
	// Both Git object-name lengths are legitimate; rejecting the sha256 one
	// would refuse every baseline recorded in a sha256 repository.
	if err := write(t, strings.Replace(valid(), sha1Commit, sha256Commit, 1)); err != nil {
		t.Fatalf("a sha256 commit name was refused: %v", err)
	}

	tests := []struct {
		name   string
		body   string
		wantIn string
	}{
		{"an unsupported version", `{"v":2,"probes":{}}`, "v"},
		{"a missing version", `{"probes":{}}`, "v"},
		{"an unknown field", `{"v":1,"probes":{},"surprise":true}`, "surprise"},
		{"a duplicated key", `{"v":1,"v":1,"probes":{}}`, "v"},
		{"a commit name that is not one", strings.Replace(valid(), sha1Commit, "not-a-commit", 1), "recorded_commit"},
		{"a short commit name", strings.Replace(valid(), sha1Commit, strings.Repeat("b", 39), 1), "recorded_commit"},
		{"a stdout hash that is not a hash", strings.Replace(valid(), `"stdout":"`+good+`"`, `"stdout":"nonsense"`, 1), "stdout"},
		{"a stderr hash that is not a hash", strings.Replace(valid(), `"stderr":"`+good+`"`, `"stderr":"nonsense"`, 1), "stderr"},
		{"a run hash that is not a hash", strings.Replace(valid(), `"run_hash":"`+good+`"`, `"run_hash":"nonsense"`, 1), "run_hash"},
		{
			"a dependency hash that is not a hash",
			strings.Replace(valid(), `"exit":0`, `"deps":{"in.csv":"nonsense"},"exit":0`, 1),
			"nonsense",
		},
		{
			"an artifact hash that is not a hash",
			strings.Replace(valid(), `"stderr":"`+good+`"`, `"stderr":"`+good+`","files":{"out/a.txt":"nonsense"}`, 1),
			"nonsense",
		},
		{
			"a metric hash that is not a hash",
			`{"v":1,"probes":{},"metrics":{"m":{"run_hash":"nonsense","value":1}}}`,
			"run_hash",
		},
		{"a path where a hash belongs", strings.Replace(valid(), `"stdout":"`+good+`"`, `"stdout":"../../etc/passwd"`, 1), "stdout"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := write(t, test.body)
			if err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
			if !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error %q does not say %q", err, test.wantIn)
			}
		})
	}
}

// A metric with no frozen definition is tolerated by the lockfile loader and
// refused by verify: the baseline predates definition freezing, so the honest
// answer is "re-record", not "trust it". Pinning it here because the loader's
// silence looks like acceptance, and the only thing that makes it safe is the
// refusal further on.
func TestAMetricWithNoFrozenDefinitionIsAHarnessFailureNotAPass(t *testing.T) {
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion = 1\n[[probe]]\nid = \"p\"\nrun = \"printf p\"\n[[metric]]\nid = \"m\"\nrun = \"printf 1\"\ndirection = \"down\"\nenforce = \"none\"\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "manifest")

	manifest, manifestBytes, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if result := Record(root, manifest, manifestBytes, RecordOptions{}); result.Outcome.Exit != ExitOK {
		t.Fatalf("record: %#v", result.Outcome)
	}

	// Strip the metric's frozen definition, the way a hand-edited lockfile
	// would to escape definition-drift detection.
	lockBytes, err := os.ReadFile(filepath.Join(root, "vise.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var lock Lockfile
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		t.Fatal(err)
	}
	metric := lock.Metrics["m"]
	metric.RunHash = ""
	lock.Metrics["m"] = metric
	stripped, err := CanonicalJSON(lock)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "vise.lock", string(stripped))

	result := Verify(root, manifest, manifestBytes, VerifyOptions{})
	failure := result.Outcome.Failures["m"]
	if failure.Class != "harness" {
		t.Fatalf("an unfrozen metric definition was not a harness failure: %#v", result.Outcome)
	}
	if !strings.Contains(failure.Detail, "re-record") {
		t.Fatalf("the failure does not name the remedy: %q", failure.Detail)
	}
}

// A proposal whose id is already a probe or a metric in the manifest can never
// be promoted, and listing it as pending tells an operator there is something
// to consider when there is only something to rename.
func TestAProposalCannotCollideWithTheManifest(t *testing.T) {
	root := testGitRepo(t)
	manifest := Manifest{
		Vise:    ViseSettings{Version: LockVersion},
		Stubs:   StubSettings{Network: "declared-off"},
		Probes:  []Probe{{ID: "taken", Run: "printf p", Timeout: 30}},
		Metrics: []Metric{{ID: "counted", Run: "printf 1", Direction: "down", Enforce: "none", Timeout: 30}},
	}

	for _, id := range []string{"taken", "counted"} {
		writeTestFile(t, root, filepath.Join(".vise", "proposals.toml"),
			"[[probe]]\nid = \""+id+"\"\nrun = \"printf x\"\n")
		_, err := LoadProposals(root, manifest)
		if err == nil {
			t.Fatalf("a proposal named %q was accepted although the manifest already uses it", id)
		}
		if !strings.Contains(err.Error(), "never be promoted") {
			t.Fatalf("the error does not say why: %v", err)
		}
	}

	// A free id is still accepted, or the rule would be satisfied by refusing
	// every proposal.
	writeTestFile(t, root, filepath.Join(".vise", "proposals.toml"),
		"[[probe]]\nid = \"fresh\"\nrun = \"printf x\"\n")
	proposals, err := LoadProposals(root, manifest)
	if err != nil {
		t.Fatalf("a proposal with a free id was refused: %v", err)
	}
	if len(proposals.Probes) != 1 {
		t.Fatalf("proposals = %#v", proposals.Probes)
	}
}
