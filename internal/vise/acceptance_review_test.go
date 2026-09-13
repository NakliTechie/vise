package vise

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Snapshot persistent generation state, including blob names and bytes. Runtime
// scratch and coordination locks are intentionally not acceptance artifacts.
func snapshotGenerationForReview(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	for _, relative := range []string{"vise.lock", ".vise/journal.jsonl", ".vise/blobs"} {
		err := filepath.WalkDir(filepath.Join(root, relative), func(path string, entry fs.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				snapshot[relative] = "absent"
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			key, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				snapshot[key] = "directory"
				return nil
			}
			if !entry.Type().IsRegular() {
				t.Fatalf("unexpected generation entry type: %s %v", key, entry.Type())
			}
			data, err := os.ReadFile(path)
			if err == nil {
				snapshot[key] = "file:" + string(data)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

// C13: stale means a previously valid preview, not an invented invalid hash.
// Keep HEAD fixed between previews, so changed commit metadata cannot mask an
// omitted observation/dependency/spec/environment component in the digest.
func TestStaleReviewedCandidateCannotWriteGeneration(t *testing.T) {
	for _, changed := range []string{"observation", "dependency", "spec", "environment"} {
		t.Run(changed, func(t *testing.T) {
			root := testGitRepo(t)
			writeTestFile(t, root, "observation", "initial\n")
			writeTestFile(t, root, "dependency", "input-v1\n")
			writeTestFile(t, root, "environment", "tool-v1\n")
			writeTestFile(t, root, "spec", "pinned\n")
			writeTestFile(t, root, "vise.toml", `[vise]
version = 1
[env]
fingerprint = ["cat environment"]
[[probe]]
id = "preserve"
run = "cat observation"
deps = ["dependency"]
[[probe]]
id = "pin"
run = "printf 'pinned\\n'"
expect.stdout = "spec"
`)
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "review fixture")
			manifest, manifestBytes, err := LoadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			initial := Record(root, manifest, manifestBytes, RecordOptions{})
			if initial.Outcome.Exit != ExitOK || len(initial.Pins.Accepted) != 1 {
				t.Fatalf("control did not accept the met pin: %#v", initial)
			}
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-qm", "accepted baseline")
			head := headCommit(t, root)
			before := snapshotGenerationForReview(t, root)
			writeTestFile(t, root, "observation", "review-v1\n")
			preview := Record(root, manifest, manifestBytes, RecordOptions{Preview: true, AllowDirty: true})
			if preview.Outcome.Exit != ExitOK || preview.Candidate == "" {
				t.Fatalf("preview: %#v", preview)
			}
			if !maps.Equal(before, snapshotGenerationForReview(t, root)) {
				t.Fatal("preview changed persistent generation state")
			}
			writeTestFile(t, root, changed, "changed-after-preview\n")
			refused := Record(root, manifest, manifestBytes, RecordOptions{Accept: preview.Candidate, AllowDirty: true})
			failure := refused.Outcome.Failures["operator-review"]
			if refused.Outcome.Exit != ExitHarness || refused.Outcome.Next.Action != NextHuman || !failure.Operator ||
				!strings.Contains(failure.Detail, "differs from the accepted") || refused.Candidate == preview.Candidate {
				t.Fatalf("stale reviewed digest was not refused as operator review: %#v", refused)
			}
			if !maps.Equal(before, snapshotGenerationForReview(t, root)) {
				t.Fatal("refused stale digest changed lock, blobs or journal")
			}
			current := Record(root, manifest, manifestBytes, RecordOptions{Preview: true, AllowDirty: true})
			if current.Outcome.Exit != ExitOK || current.Candidate != refused.Candidate ||
				!maps.Equal(before, snapshotGenerationForReview(t, root)) {
				t.Fatalf("fresh preview was unstable or wrote generation: %#v", current)
			}
			accepted := Record(root, manifest, manifestBytes, RecordOptions{Accept: current.Candidate, AllowDirty: true})
			if accepted.Outcome.Exit != ExitOK {
				t.Fatalf("fresh digest control refused: %#v", accepted)
			}
			lockBytes, err := os.ReadFile(filepath.Join(root, "vise.lock"))
			if err != nil || HashBytes(lockBytes) != current.Candidate {
				t.Fatalf("accepted bytes differ from reviewed candidate: %v", err)
			}
			if headCommit(t, root) != head {
				t.Fatal("fixture moved HEAD and confounded the digest control")
			}
		})
	}
}
