package vise

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Manifest struct {
	Vise        ViseSettings        `toml:"vise" json:"vise"`
	Stubs       StubSettings        `toml:"stubs" json:"stubs"`
	Environment EnvironmentSettings `toml:"env" json:"env"`
	Probes      []Probe             `toml:"probe" json:"probe"`
	Metrics     []Metric            `toml:"metric" json:"metric"`
}

type ViseSettings struct {
	Version int `toml:"version" json:"version"`
}

type StubSettings struct {
	TZ      string `toml:"tz" json:"tz"`
	Lang    string `toml:"lang" json:"lang"`
	Seed    string `toml:"seed" json:"seed"`
	Network string `toml:"network" json:"network"`
}

type EnvironmentSettings struct {
	Fingerprint []string `toml:"fingerprint" json:"fingerprint"`
}

type Probe struct {
	ID      string            `toml:"id" json:"id"`
	Run     string            `toml:"run" json:"run"`
	Timeout int               `toml:"timeout" json:"timeout"`
	Deps    []string          `toml:"deps" json:"deps,omitempty"`
	Files   []string          `toml:"files" json:"files,omitempty"`
	Env     map[string]string `toml:"env" json:"env,omitempty"`
}

type Metric struct {
	ID         string            `toml:"id" json:"id"`
	Run        string            `toml:"run" json:"run"`
	Direction  string            `toml:"direction" json:"direction"`
	Enforce    string            `toml:"enforce" json:"enforce"`
	VersionCmd string            `toml:"version_cmd" json:"version_cmd,omitempty"`
	Timeout    int               `toml:"timeout" json:"timeout"`
	Env        map[string]string `toml:"env" json:"env,omitempty"`
}

type Proposals struct {
	Probes []Probe `toml:"probe" json:"probe"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func LoadManifest(root string) (Manifest, []byte, error) {
	path := filepath.Join(root, "vise.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, nil, err
	}
	var manifest Manifest
	metadata, err := toml.Decode(string(data), &manifest)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("parse vise.toml: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		parts := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			parts = append(parts, key.String())
		}
		sort.Strings(parts)
		return Manifest{}, nil, fmt.Errorf("unknown vise.toml keys: %s", strings.Join(parts, ", "))
	}
	manifest.applyDefaults()
	if err := manifest.Validate(root); err != nil {
		return Manifest{}, nil, err
	}
	return manifest, data, nil
}

func (m *Manifest) applyDefaults() {
	if m.Stubs.TZ == "" {
		m.Stubs.TZ = "UTC"
	}
	if m.Stubs.Lang == "" {
		m.Stubs.Lang = "C"
	}
	if m.Stubs.Seed == "" {
		m.Stubs.Seed = "1729"
	}
	if m.Stubs.Network == "" {
		m.Stubs.Network = "declared-off"
	}
	for i := range m.Probes {
		if m.Probes[i].Timeout == 0 {
			m.Probes[i].Timeout = 30
		}
		sort.Strings(m.Probes[i].Deps)
		sort.Strings(m.Probes[i].Files)
	}
	for i := range m.Metrics {
		if m.Metrics[i].Timeout == 0 {
			m.Metrics[i].Timeout = 30
		}
		if m.Metrics[i].Direction == "" {
			m.Metrics[i].Direction = "down"
		}
		if m.Metrics[i].Enforce == "" {
			m.Metrics[i].Enforce = "none"
		}
	}
}

func (m Manifest) Validate(root string) error {
	if m.Vise.Version != LockVersion {
		return fmt.Errorf("vise.version must be %d", LockVersion)
	}
	if m.Stubs.Network != "declared-off" {
		return fmt.Errorf("stubs.network must be declared-off in v0")
	}
	seen := make(map[string]string)
	for i, probe := range m.Probes {
		where := fmt.Sprintf("probe[%d]", i)
		if err := validateID(probe.ID, where); err != nil {
			return err
		}
		if prior, ok := seen[probe.ID]; ok {
			return fmt.Errorf("duplicate id %q in %s and %s", probe.ID, prior, where)
		}
		seen[probe.ID] = where
		if strings.TrimSpace(probe.Run) == "" {
			return fmt.Errorf("%s.run must not be empty", where)
		}
		if probe.Timeout < 1 || probe.Timeout > 86400 {
			return fmt.Errorf("%s.timeout must be between 1 and 86400 seconds", where)
		}
		for _, path := range append(append([]string{}, probe.Deps...), probe.Files...) {
			if err := ValidateRelativePath(root, path, false); err != nil {
				return fmt.Errorf("%s path %q: %w", where, path, err)
			}
		}
		if err := validateEnv(probe.Env, where); err != nil {
			return err
		}
	}
	for i, metric := range m.Metrics {
		where := fmt.Sprintf("metric[%d]", i)
		if err := validateID(metric.ID, where); err != nil {
			return err
		}
		if prior, ok := seen[metric.ID]; ok {
			return fmt.Errorf("duplicate id %q in %s and %s", metric.ID, prior, where)
		}
		seen[metric.ID] = where
		if strings.TrimSpace(metric.Run) == "" {
			return fmt.Errorf("%s.run must not be empty", where)
		}
		if metric.Direction != "down" && metric.Direction != "up" {
			return fmt.Errorf("%s.direction must be down or up", where)
		}
		if metric.Enforce != "none" && metric.Enforce != "no-regress" {
			return fmt.Errorf("%s.enforce must be none or no-regress", where)
		}
		if metric.Timeout < 1 || metric.Timeout > 86400 {
			return fmt.Errorf("%s.timeout must be between 1 and 86400 seconds", where)
		}
		if err := validateEnv(metric.Env, where); err != nil {
			return err
		}
	}
	return nil
}

func validateID(id, where string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%s.id %q must match %s", where, id, idPattern.String())
	}
	return nil
}

func validateEnv(values map[string]string, where string) error {
	for key := range values {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("%s.env contains invalid key %q", where, key)
		}
	}
	return nil
}

func (m Manifest) Probe(id string) (Probe, bool) {
	for _, probe := range m.Probes {
		if probe.ID == id {
			return probe, true
		}
	}
	return Probe{}, false
}

func (m Manifest) Metric(id string) (Metric, bool) {
	for _, metric := range m.Metrics {
		if metric.ID == id {
			return metric, true
		}
	}
	return Metric{}, false
}

func ProbeRunHash(probe Probe) (string, error) {
	data, err := json.Marshal(probe)
	if err != nil {
		return "", err
	}
	return HashBytes(data), nil
}

func LoadProposals(root string) (Proposals, error) {
	path := filepath.Join(root, ".vise", "proposals.toml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Proposals{}, nil
	}
	if err != nil {
		return Proposals{}, err
	}
	var proposals Proposals
	metadata, err := toml.Decode(string(data), &proposals)
	if err != nil {
		return Proposals{}, fmt.Errorf("parse .vise/proposals.toml: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		parts := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			parts = append(parts, key.String())
		}
		sort.Strings(parts)
		return Proposals{}, fmt.Errorf("unknown proposal keys: %s", strings.Join(parts, ", "))
	}
	seen := make(map[string]bool)
	for i := range proposals.Probes {
		probe := &proposals.Probes[i]
		if probe.Timeout == 0 {
			probe.Timeout = 30
		}
		where := fmt.Sprintf("proposal[%d]", i)
		if err := validateID(probe.ID, where); err != nil {
			return Proposals{}, err
		}
		if seen[probe.ID] {
			return Proposals{}, fmt.Errorf("duplicate proposal id %q", probe.ID)
		}
		seen[probe.ID] = true
		if strings.TrimSpace(probe.Run) == "" {
			return Proposals{}, fmt.Errorf("%s.run must not be empty", where)
		}
		if probe.Timeout < 1 || probe.Timeout > 86400 {
			return Proposals{}, fmt.Errorf("%s.timeout must be between 1 and 86400 seconds", where)
		}
	}
	return proposals, nil
}
