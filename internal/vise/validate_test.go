package vise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
		}, "not a usable variable name"},
		{"an environment key with an equals sign", func(m *Manifest) {
			m.Probes[0].Env = map[string]string{"A=B": "x"}
		}, "not a usable variable name"},
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
	// The colliding ids are second in each list: a check that inspects only the
	// first entry passes on a one-entry fixture while seeing nothing.
	manifest := Manifest{
		Vise:  ViseSettings{Version: LockVersion},
		Stubs: StubSettings{Network: "declared-off"},
		Probes: []Probe{
			{ID: "first", Run: "printf p", Timeout: 30},
			{ID: "taken", Run: "printf p", Timeout: 30},
		},
		Metrics: []Metric{
			{ID: "leading", Run: "printf 1", Direction: "down", Enforce: "none", Timeout: 30},
			{ID: "counted", Run: "printf 1", Direction: "down", Enforce: "none", Timeout: 30},
		},
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

// A metric that enforces has to name its analyzer. Without a version_cmd the
// recorded tool version is the empty string, which compares equal to the empty
// string forever — so replacing the analyzer, or editing a script it calls, is
// invisible, and "swapping the analyzer is harness drift, never a free
// improvement" is not true. Found by an adversarial audit of the metric path.
func TestAnEnforcedMetricMustNameItsAnalyzer(t *testing.T) {
	root := testGitRepo(t)
	manifest := func(enforce, versionCmd string) Manifest {
		return Manifest{
			Vise:    ViseSettings{Version: LockVersion},
			Stubs:   StubSettings{Network: "declared-off"},
			Probes:  []Probe{{ID: "p", Run: "printf p", Timeout: 30}},
			Metrics: []Metric{{ID: "m", Run: "printf 1", Direction: "down", Enforce: enforce, Timeout: 30, VersionCmd: versionCmd}},
		}
	}

	err := manifest("no-regress", "").Validate(root)
	if err == nil {
		t.Fatal("an enforced metric with no version_cmd was accepted")
	}
	if !strings.Contains(err.Error(), "free improvement") {
		t.Fatalf("the error does not say why: %v", err)
	}
	if err := manifest("no-regress", "   ").Validate(root); err == nil {
		t.Fatal("a blank version_cmd satisfied the rule")
	}

	// Enforced with an analyzer named: fine.
	if err := manifest("no-regress", "analyzer --version").Validate(root); err != nil {
		t.Fatalf("an enforced metric naming its analyzer was refused: %v", err)
	}
	// Tracked but not enforced: no version command needed, because nothing is
	// gated on it.
	if err := manifest("none", "").Validate(root); err != nil {
		t.Fatalf("a tracked-only metric was refused: %v", err)
	}
}

// A mistyped probe name was answered with "repair the harness or restore its
// declared inputs" — true of nothing the caller did, and useless for what they
// might do next. Naming what they could have asked for is the whole fix.
func TestAnUnknownProbeNamesTheOnesThatExist(t *testing.T) {
	manifest := Manifest{
		Vise:  ViseSettings{Version: LockVersion},
		Stubs: StubSettings{Network: "declared-off"},
		Probes: []Probe{
			{ID: "beta", Run: "printf b", Timeout: 30},
			{ID: "alpha", Run: "printf a", Timeout: 30},
		},
	}

	got := DeclaredProbeList(manifest)
	// Sorted, so the message does not change between runs of the same manifest.
	if got != "declared probes are alpha, beta" {
		t.Fatalf("list = %q", got)
	}

	// Bounded: a manifest with many probes must not print all of them into a
	// message that is supposed to fit on a line.
	many := manifest
	many.Probes = nil
	for i := 0; i < 40; i++ {
		many.Probes = append(many.Probes, Probe{ID: "probe" + strconv.Itoa(i), Run: "printf x", Timeout: 30})
	}
	long := DeclaredProbeList(many)
	if !strings.Contains(long, "and 30 more") {
		t.Fatalf("a 40-probe manifest rendered %q", long)
	}
	if len(long) > 200 {
		t.Fatalf("a 40-probe manifest rendered %d characters", len(long))
	}

	// And an empty manifest says so rather than trailing off.
	if got := DeclaredProbeList(Manifest{}); !strings.Contains(got, "no probes") {
		t.Fatalf("empty manifest rendered %q", got)
	}
}

// Four gaps in the manifest validator, found by a coding agent reading it
// under the gate. Each is a thing vise accepted and should not have.
func TestTheManifestRefusesWhatItCannotHonour(t *testing.T) {
	root := testGitRepo(t)
	base := func() Manifest {
		return Manifest{
			Vise:  ViseSettings{Version: 1},
			Stubs: StubSettings{TZ: "UTC", Lang: "C", Seed: "1729", Network: "declared-off"},
			Probes: []Probe{{ID: "p", Run: "printf ok", Timeout: 30}},
		}
	}
	for _, c := range []struct {
		name   string
		change func(*Manifest)
		wantIn string
	}{
		{
			// It runs through the same shell a probe does, and a probe's run
			// has always been refused when blank.
			"a blank fingerprint command",
			func(m *Manifest) { m.Environment.Fingerprint = []string{"go version", "  "} },
			"fingerprint[1] must not be empty",
		},
		{
			// The recorded fingerprint is keyed by the command text, so a
			// repeat records one entry: the manifest claims two things are
			// pinned and the lockfile holds one, silently.
			"a repeated fingerprint command",
			func(m *Manifest) { m.Environment.Fingerprint = []string{"go version", "go version"} },
			"repeats env.fingerprint[0]",
		},
		{
			// The shell has no way to set a variable whose name holds a space,
			// and no way to say so.
			"an env key with a space",
			func(m *Manifest) { m.Probes[0].Env = map[string]string{"A B": "1"} },
			"not a usable variable name",
		},
		{
			"an env key with a newline",
			func(m *Manifest) { m.Probes[0].Env = map[string]string{"A\nB": "1"} },
			"not a usable variable name",
		},
		{
			// gettext reads LANGUAGE ahead of LC_ALL, so a probe declaring it
			// defeats the lang stub the moment an operator sets a real locale.
			"an env key that overrides LANGUAGE",
			func(m *Manifest) { m.Probes[0].Env = map[string]string{"LANGUAGE": "fr"} },
			"reserved variable LANGUAGE",
		},
		{
			"an env key that redirects the zone database",
			func(m *Manifest) { m.Probes[0].Env = map[string]string{"TZDIR": "/tmp/zones"} },
			"reserved variable TZDIR",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			manifest := base()
			c.change(&manifest)
			err := manifest.Validate(root)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.wantIn) {
				t.Errorf("error %q does not say %q", err, c.wantIn)
			}
		})
	}
}

// And the message a first-time user is likeliest to see. A manifest with no
// [vise] table decodes to version 0 and used to produce "vise.version must be
// 1" — telling someone to change a field they never wrote.
func TestAMissingViseTableSaysTheTableIsMissing(t *testing.T) {
	root := testGitRepo(t)
	manifest := Manifest{Stubs: StubSettings{Network: "declared-off"}, Probes: []Probe{{ID: "p", Run: "printf ok", Timeout: 30}}}
	err := manifest.Validate(root)
	if err == nil {
		t.Fatal("a manifest with no version was accepted")
	}
	if !strings.Contains(err.Error(), "[vise] table") {
		t.Errorf("error %q does not mention the missing table", err)
	}
}
