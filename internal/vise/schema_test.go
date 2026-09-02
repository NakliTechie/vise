package vise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCommit = "3319316e4a7a5f1fb2e80de6f001a1355269464a"

func writeLockfile(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "vise.lock"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validLockfile is the shape every case below mutates one field of.
func validLockfile(t *testing.T) map[string]any {
	t.Helper()
	empty := HashBytes(nil)
	return map[string]any{
		"v":           1,
		"fingerprint": map[string]any{"os": "darwin", "arch": "arm64", "stubs": map[string]any{"tz": "UTC", "lang": "C", "seed": "1729", "network": "declared-off"}},
		"probes": map[string]any{
			"behavior": map[string]any{
				"run_hash":        empty,
				"recorded_commit": testCommit,
				"exit":            0,
				"stdout":          empty,
				"stderr":          empty,
			},
		},
	}
}

func TestLockfileRejectsMalformedSchema(t *testing.T) {
	encode := func(t *testing.T, value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	empty := HashBytes(nil)

	tests := []struct {
		name string
		body func(t *testing.T) string
		want string
	}{
		{
			name: "duplicate probe key",
			body: func(t *testing.T) string {
				// encoding/json keeps the last of two identical keys, so a
				// reader and a human could see different baselines.
				return `{"v":1,"fingerprint":{"os":"darwin","arch":"arm64","stubs":{"tz":"UTC","lang":"C","seed":"1729","network":"declared-off"}},` +
					`"probes":{"behavior":{"run_hash":"` + empty + `","recorded_commit":"` + testCommit + `","exit":0,"stdout":"` + empty + `","stderr":"` + empty + `"},` +
					`"behavior":{"run_hash":"` + empty + `","recorded_commit":"` + testCommit + `","exit":1,"stdout":"` + empty + `","stderr":"` + empty + `"}}}`
			},
			want: `duplicate key "behavior"`,
		},
		{
			name: "duplicate top-level key",
			body: func(t *testing.T) string {
				return `{"v":1,"v":1,"fingerprint":{"os":"darwin","arch":"arm64","stubs":{"tz":"UTC","lang":"C","seed":"1729","network":"declared-off"}},"probes":{}}`
			},
			want: `duplicate key "v" at the top level`,
		},
		{
			name: "control character in a probe id",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				probes := lock["probes"].(map[string]any)
				probes["beha\x1bvior"] = probes["behavior"]
				delete(probes, "behavior")
				return encode(t, lock)
			},
			want: "probe id",
		},
		{
			name: "empty probe id",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				probes := lock["probes"].(map[string]any)
				probes[""] = probes["behavior"]
				delete(probes, "behavior")
				return encode(t, lock)
			},
			want: "probe id",
		},
		{
			name: "reserved probe id",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				probes := lock["probes"].(map[string]any)
				probes["fingerprint"] = probes["behavior"]
				delete(probes, "behavior")
				return encode(t, lock)
			},
			want: "reserved for harness failures",
		},
		{
			name: "path-shaped recorded commit",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				lock["probes"].(map[string]any)["behavior"].(map[string]any)["recorded_commit"] = "../../etc/passwd"
				return encode(t, lock)
			},
			want: "is not a Git object name",
		},
		{
			name: "short recorded commit",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				lock["probes"].(map[string]any)["behavior"].(map[string]any)["recorded_commit"] = "abc"
				return encode(t, lock)
			},
			want: "is not a Git object name",
		},
		{
			name: "escaping dependency path",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				lock["probes"].(map[string]any)["behavior"].(map[string]any)["deps"] = map[string]any{"../outside.txt": empty}
				return encode(t, lock)
			},
			want: "dependency",
		},
		{
			name: "artifact pointing at evaluator state",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				lock["probes"].(map[string]any)["behavior"].(map[string]any)["files"] = map[string]any{".vise/blobs/x": empty}
				return encode(t, lock)
			},
			want: "artifact",
		},
		{
			name: "reserved metric id",
			body: func(t *testing.T) string {
				lock := validLockfile(t)
				lock["metrics"] = map[string]any{"journal": map[string]any{"value": 1}}
				return encode(t, lock)
			},
			want: "reserved for harness failures",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeLockfile(t, root, test.body(t))
			_, _, err := LoadLockfile(root)
			if err == nil {
				t.Fatal("malformed lockfile was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to name %q", err, test.want)
			}
		})
	}
}

func TestLockfileAcceptsAWellFormedSchema(t *testing.T) {
	root := t.TempDir()
	data, err := json.Marshal(validLockfile(t))
	if err != nil {
		t.Fatal(err)
	}
	writeLockfile(t, root, string(data))
	lock, _, err := LoadLockfile(root)
	if err != nil {
		t.Fatalf("a well-formed lockfile was refused: %v", err)
	}
	if lock.Probes["behavior"].RecordedCommit != testCommit {
		t.Fatalf("lock = %#v", lock)
	}
}

func TestLockfileFromANewerViseSaysSo(t *testing.T) {
	root := t.TempDir()
	body := `{"v":1,"fingerprint":{"os":"darwin","arch":"arm64","stubs":{"tz":"UTC","lang":"C","seed":"1729","network":"declared-off"}},"probes":{},"future_field":42}`
	writeLockfile(t, root, body)
	_, _, err := LoadLockfile(root)
	if err == nil {
		t.Fatal("a lockfile with an unknown field was accepted")
	}
	for _, want := range []string{`"future_field"`, "written by a newer vise", "upgrade", "vise version --json"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err.Error(), want)
		}
	}
}
