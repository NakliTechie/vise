package vise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TamperHash is what CI compares against the value recomputed from a trusted
// branch: it is the whole answer to "is this the baseline we approved". It had
// no test that each of its inputs reaches the digest, so omitting the manifest
// bytes, or the lockfile bytes, left the suite green while the hash stopped
// depending on the thing it exists to cover.
func TestTamperHashCoversEveryInput(t *testing.T) {
	root := testGitRepo(t)
	blob := []byte("observation")
	hash := HashBytes(blob)
	if err := os.MkdirAll(filepath.Join(root, ".vise", "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := HashName(hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".vise", "blobs", name), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := []byte("[vise]\nversion = 1\n")
	lock := []byte(`{"v":1,"probes":{"p":{"run_hash":"` + hash + `","recorded_commit":"` + strings.Repeat("a", 40) + `","exit":0,"stdout":"` + hash + `","stderr":"` + hash + `"}}}`)

	base, err := TamperHash(root, manifest, lock)
	if err != nil {
		t.Fatal(err)
	}

	// A different manifest must produce a different hash. Without this,
	// dropping the manifest from the digest is invisible, and an edited
	// vise.toml carries the approved hash.
	other, err := TamperHash(root, []byte("[vise]\nversion = 1\n# changed\n"), lock)
	if err != nil {
		t.Fatal(err)
	}
	if other == base {
		t.Fatal("changing the manifest did not change the tamper hash")
	}

	// And a different lockfile, including a change that no blob reflects: the
	// recorded commit is lockfile bytes and nothing else.
	movedCommit := strings.Replace(string(lock), strings.Repeat("a", 40), strings.Repeat("b", 40), 1)
	other, err = TamperHash(root, manifest, []byte(movedCommit))
	if err != nil {
		t.Fatal(err)
	}
	if other == base {
		t.Fatal("changing the lockfile did not change the tamper hash")
	}

	// A blob whose content no longer matches its name is refused rather than
	// hashed, because the hash is only worth something if the bytes behind it
	// were checked.
	if err := os.WriteFile(filepath.Join(root, ".vise", "blobs", name), []byte("swapped"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TamperHash(root, manifest, lock); err == nil {
		t.Fatal("a blob that fails its content hash was accepted")
	}
}

// FingerprintMismatch decides whether the environment moved, which is a
// harness verdict and not a behaviour one — getting it wrong sends an agent to
// revert a change that was fine. Each branch needs its own case, or removing
// one leaves the suite green and the drift silent.
func TestFingerprintMismatchNoticesEachKindOfDrift(t *testing.T) {
	base := Fingerprint{
		OS:    "darwin",
		Arch:  "arm64",
		Stubs: StubSettings{TZ: "UTC", Lang: "C", Seed: "1729", Network: "declared-off"},
		Env:   map[string]string{"go version": "go1.25.8", "jq --version": "jq-1.7"},
	}
	if mismatch := FingerprintMismatch(base, base); mismatch != "" {
		t.Fatalf("a fingerprint differed from itself: %s", mismatch)
	}

	tests := []struct {
		name   string
		change func(f *Fingerprint)
		wantIn string
	}{
		{"a different operating system", func(f *Fingerprint) { f.OS = "linux" }, "platform"},
		{"a different architecture", func(f *Fingerprint) { f.Arch = "amd64" }, "platform"},
		{"a changed stub", func(f *Fingerprint) { f.Stubs.TZ = "Asia/Kolkata" }, "[stubs]"},
		{"a changed seed", func(f *Fingerprint) { f.Stubs.Seed = "2" }, "[stubs]"},
		{"a tool version that moved", func(f *Fingerprint) {
			f.Env = map[string]string{"go version": "go1.25.9", "jq --version": "jq-1.7"}
		}, "go version"},
		// These used to report only "the set of fingerprint commands differs",
		// from a length check. Naming the command is what an operator needs,
		// and the length check could not name one because the loop under it
		// walked the current side alone.
		{"a fingerprint command added", func(f *Fingerprint) {
			f.Env = map[string]string{"go version": "go1.25.8", "jq --version": "jq-1.7", "cc --version": "clang"}
		}, `"cc --version" is not in the recorded baseline`},
		{"a fingerprint command removed", func(f *Fingerprint) {
			f.Env = map[string]string{"go version": "go1.25.8"}
		}, `"jq --version" was recorded and is no longer declared`},
		// The case both the length check and the one-sided loop missed. Swap a
		// command for another and the lengths match, so the check said nothing;
		// the loop then named the arrival and never the departure, and the
		// operator accepted a baseline knowing half of why.
		{"one command swapped for another", func(f *Fingerprint) {
			f.Env = map[string]string{"go version": "go1.25.8", "cc --version": "clang"}
		}, `"cc --version" is not in the recorded baseline`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := base
			current.Env = map[string]string{}
			for key, value := range base.Env {
				current.Env[key] = value
			}
			test.change(&current)

			mismatch := FingerprintMismatch(current, base)
			if mismatch == "" {
				t.Fatalf("%s was not reported as drift", test.name)
			}
			if !strings.Contains(mismatch, test.wantIn) {
				t.Fatalf("mismatch %q does not name %q", mismatch, test.wantIn)
			}
			if FingerprintEqual(current, base) {
				t.Fatalf("%s was reported equal", test.name)
			}

			// FingerprintMismatch returns the first difference in sorted
			// order; FingerprintMismatches is the one that promises all of
			// them, and its own comment says reporting one and not the others
			// is how a baseline gets accepted for a third of a reason. For the
			// swap that is two differences, and the old one-sided loop could
			// only ever produce the arrival.
			if test.name == "one command swapped for another" {
				all := strings.Join(FingerprintMismatches(current, base), "; ")
				for _, want := range []string{
					`"cc --version" is not in the recorded baseline`,
					`"jq --version" was recorded and is no longer declared`,
				} {
					if !strings.Contains(all, want) {
						t.Errorf("the full report does not name %q:\n\t%s", want, all)
					}
				}
			}
			// And the full list must carry it too, since that is what the
			// review diff renders.
			var found bool
			for _, entry := range FingerprintMismatches(current, base) {
				if strings.Contains(entry, test.wantIn) {
					found = true
				}
			}
			if !found {
				t.Fatalf("FingerprintMismatches omitted %q", test.wantIn)
			}
		})
	}
}
