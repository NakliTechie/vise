package vise

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func gitAuthorityFixture(t *testing.T) (string, Manifest, []byte) {
	t.Helper()
	root := testGitRepo(t)
	writeTestFile(t, root, ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nran\n")
	writeTestFile(t, root, "vise.toml", "[vise]\nversion=1\n[[probe]]\nid='p'\nrun='printf p >> ran; printf stable'\n[[probe]]\nid='q'\nrun='printf q >> ran; printf stable'\n[[metric]]\nid='m'\nrun='printf m >> ran; printf 1'\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "Git authority fixture")
	manifest, data, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := Record(root, manifest, data, RecordOptions{}); got.Outcome.Exit != ExitOK {
		t.Fatalf("initial record: %#v", got)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-qm", "Git authority baseline")
	if err := os.Remove(filepath.Join(root, "ran")); err != nil {
		t.Fatal(err)
	}
	return root, manifest, data
}

func gitAuthorityNoRun(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "ran")); !os.IsNotExist(err) {
		t.Fatalf("candidate witness exists or unreadable: %v", err)
	}
}

func TestGitAuthorityPreflightPreservesScopeAndGeneration(t *testing.T) {
	for _, fault := range []string{"index", "head"} {
		for _, scope := range []struct {
			id    string
			count int
		}{{"", 3}, {"p", 1}} {
			t.Run(fault+"/"+scope.id, func(t *testing.T) {
				root, manifest, data := gitAuthorityFixture(t)
				rel, broken := ".git/index", "not an index"
				if fault == "head" {
					rel, broken = ".git/HEAD", "ref: refs/heads/absent-fixture-branch\n"
				}
				original, err := os.ReadFile(filepath.Join(root, rel))
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, root, rel, broken)
				before := snapshotGenerationForReview(t, root)
				result := Verify(root, manifest, data, VerifyOptions{ProbeID: scope.id, EnforceRerunLimit: true})
				c, f := result.Outcome.Counts, result.Outcome.Failures["git"]
				if result.Outcome.Exit != ExitHarness || result.Outcome.Next.Action != NextHuman || !f.Operator || f.Class != "harness" || len(result.Outcome.Failures) != 1 {
					t.Errorf("Git preflight ownership: %#v", result.Outcome)
				}
				if c.Declared != scope.count || c.Skipped != scope.count || c.Pass != 0 || c.Harness != 1 {
					t.Errorf("Git preflight counts: %#v", c)
				}
				if !reflect.DeepEqual(before, snapshotGenerationForReview(t, root)) {
					t.Fatal("preflight changed generation")
				}
				gitAuthorityNoRun(t, root)
				if err := os.WriteFile(filepath.Join(root, rel), original, 0o644); err != nil {
					t.Fatal(err)
				}
				healthy := Verify(root, manifest, data, VerifyOptions{ProbeID: scope.id})
				if healthy.Outcome.Exit != ExitOK || healthy.Outcome.Counts.Pass != scope.count {
					t.Fatalf("restored positive control: %#v", healthy.Outcome)
				}
			})
		}
	}
}

func TestGitAuthorityRecordIndexRefusalIsOperatorOwned(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "record", true: "preview"}[preview], func(t *testing.T) {
			root, manifest, data := gitAuthorityFixture(t)
			writeTestFile(t, root, ".git/index", "not an index")
			before := snapshotGenerationForReview(t, root)
			got := Record(root, manifest, data, RecordOptions{AllowDirty: true, Preview: preview, ReviewedDiff: !preview})
			if got.Outcome.Exit != ExitHarness || got.Outcome.Next.Action != NextHuman || !got.Outcome.Failures["git"].Operator {
				t.Errorf("record Git ownership: %#v", got.Outcome)
			}
			if !reflect.DeepEqual(before, snapshotGenerationForReview(t, root)) {
				t.Fatal("refused record changed generation")
			}
			gitAuthorityNoRun(t, root)
		})
	}
}

func TestGitAuthorityStatusRejectsBadIndexButAllowsDirtyTree(t *testing.T) {
	root, _, _ := gitAuthorityFixture(t)
	before := snapshotGenerationForReview(t, root)
	writeTestFile(t, root, "notes", "ordinary dirty source")
	normal := BuildStatus(root)
	if normal.Exit != ExitOK || normal.State != "ready" || normal.Next.Action != NextProceed {
		t.Fatalf("dirty positive control: %#v", normal)
	}
	writeTestFile(t, root, ".git/index", "not an index")
	got := BuildStatus(root)
	if got.Exit != ExitOK || got.State != "harness-error" || got.Next.Action != NextHuman || !strings.Contains(got.Next.Detail, "git") {
		t.Errorf("status hides index failure: %#v", got)
	}
	if !reflect.DeepEqual(before, snapshotGenerationForReview(t, root)) {
		t.Fatal("status changed generation")
	}
	gitAuthorityNoRun(t, root)
}

func TestGitAuthorityRunnerDistinguishesSnapshotOrigin(t *testing.T) {
	for _, kind := range []string{"probe", "metric"} {
		for _, origin := range []string{"index", "git-rule-file", "ignored-gitignore", "ignored-gitattributes", "untracked-gitignore", "untracked-gitattributes", "workspace-entry"} {
			t.Run(kind+"/"+origin, func(t *testing.T) {
				root, _, _ := gitAuthorityFixture(t)
				var restore func()
				switch origin {
				case "index":
					p := filepath.Join(root, ".git/index")
					old, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					writeTestFile(t, root, ".git/index", "not an index")
					restore = func() {
						if err := os.WriteFile(p, old, 0o644); err != nil {
							t.Fatal(err)
						}
					}
				case "git-rule-file":
					p := filepath.Join(root, ".git/info/exclude")
					backup := p + ".fixture-backup"
					if err := os.Rename(p, backup); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(p, 0o755); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						if err := os.Remove(p); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(backup, p); err != nil {
							t.Fatal(err)
						}
					}
				case "ignored-gitignore", "ignored-gitattributes", "untracked-gitignore", "untracked-gitattributes", "workspace-entry":
					rel := "unreadable"
					if origin != "workspace-entry" {
						rel = "rules/." + strings.TrimPrefix(strings.TrimPrefix(origin, "ignored-"), "untracked-")
						if strings.HasPrefix(origin, "ignored-") {
							writeTestFile(t, root, ".git/info/exclude", "rules/\n")
						}
					}
					p := filepath.Join(root, rel)
					writeTestFile(t, root, rel, "# source\n")
					if err := os.Chmod(p, 0); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						if err := os.Chmod(p, 0o644); err != nil {
							t.Fatal(err)
						}
					}
					t.Cleanup(restore)
				}
				_, snapshotErr := GitWorkspaceSnapshot(root, nil)
				if snapshotErr == nil {
					t.Fatal("fault did not make the snapshot fail")
				}
				if origin == "workspace-entry" || strings.HasPrefix(origin, "ignored-") || strings.HasPrefix(origin, "untracked-") {
					var cause *os.PathError
					if !errors.As(snapshotErr, &cause) {
						t.Fatalf("lost filesystem error chain: %T %v", snapshotErr, snapshotErr)
					}
				} else {
					var cause *exec.ExitError
					if !errors.As(snapshotErr, &cause) {
						t.Fatalf("lost Git process error chain: %T %v", snapshotErr, snapshotErr)
					}
				}
				run := func() (string, bool) {
					r := Runner{Root: root}
					if kind == "probe" {
						got := r.RunProbe(Probe{ID: "p", Run: "printf executed > ran; printf 1", Timeout: 5}, true)
						return got.HarnessError, got.HarnessOperator
					}
					got := r.RunMetric(Metric{ID: "m", Run: "printf executed > ran; printf 1", Timeout: 5})
					return got.HarnessError, got.HarnessOperator
				}
				detail, operator := run()
				if detail != snapshotErr.Error() || operator != (origin != "workspace-entry") {
					t.Errorf("pre-execution origin=%s detail=%q operator=%v; want operator=%v", origin, detail, operator, origin != "workspace-entry")
				}
				gitAuthorityNoRun(t, root)
				restore()
				detail, operator = run()
				if detail != "" || operator {
					t.Fatalf("restored positive control: detail=%q operator=%v", detail, operator)
				}
				if b, err := os.ReadFile(filepath.Join(root, "ran")); err != nil || string(b) != "executed" {
					t.Fatalf("restored witness=%q %v", b, err)
				}
			})
		}
	}
}

func TestGitAuthorityStatusDoesNotRefreshTheIndex(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	root, _, _ := gitAuthorityFixture(t)
	tracked := filepath.Join(root, "vise.toml")
	info, err := os.Stat(tracked)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(5 * time.Second)
	if err := os.Chtimes(tracked, later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, ".git/index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	got := BuildStatus(root)
	if got.State != "ready" || got.Next.Action != NextProceed {
		t.Fatalf("positive status: %#v", got)
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("status refreshed the index: %v", err)
	}
	gitAuthorityNoRun(t, root)
	// A plain Git status refreshes this stat cache, demonstrating that the
	// fixture distinguishes optional index writes from a read-only query.
	testGit(t, root, "status", "--porcelain=v1")
	refreshed, err := os.ReadFile(index)
	if err != nil || bytes.Equal(before, refreshed) {
		t.Fatalf("index-refresh control did not move: %v", err)
	}
}

func TestGitAuthorityTypedOriginPreservesErrorChain(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "fixture", Err: os.ErrPermission}
	marked := &gitSnapshotError{cause}
	nested := fmt.Errorf("outer: %w", marked)
	if !gitSnapshotNeedsOperator(nested) || marked.Error() != cause.Error() || !errors.Is(nested, os.ErrPermission) {
		t.Fatalf("typed origin lost through wrapping: %v", nested)
	}
	var recovered *os.PathError
	if !errors.As(nested, &recovered) || recovered != cause {
		t.Fatal("original filesystem cause was replaced")
	}
	// Identical prose does not authenticate origin.
	if gitSnapshotNeedsOperator(errors.New(nested.Error())) {
		t.Fatal("ownership was inferred from prose")
	}
	if gitSnapshotNeedsOperator(nil) {
		t.Fatal("nil was classified as a Git failure")
	}
}

func TestGitAuthorityPostExecutionDamageStaysProbeOwned(t *testing.T) {
	for _, kind := range []string{"probe", "metric", "metric-version"} {
		for _, fault := range []string{"index", "config"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				root, _, _ := gitAuthorityFixture(t)
				damage := "printf broken > .git/index"
				if fault == "config" {
					damage = "git config --local fixture.changed yes"
				}
				r := Runner{Root: root}
				var detail string
				var operator bool
				if kind == "probe" {
					got := r.RunProbe(Probe{ID: "p", Run: "printf executed > ran; " + damage + "; exit 127", Timeout: 5}, true)
					detail, operator = got.HarnessError, got.HarnessOperator
					if got.Tolerated {
						t.Error("Git damage was tolerated beside exit127")
					}
				} else {
					m := Metric{ID: "m", Run: "printf executed > ran; " + damage + "; printf 1", Timeout: 5}
					if kind == "metric-version" {
						m.Run = "printf executed > ran; printf 1"
						m.VersionCmd = damage + "; printf v1"
					}
					got := r.RunMetric(m)
					detail, operator = got.HarnessError, got.HarnessOperator
				}
				if detail == "" || operator {
					t.Errorf("executed damage must remain probe-owned: detail=%q operator=%v", detail, operator)
				}
				if b, err := os.ReadFile(filepath.Join(root, "ran")); err != nil || string(b) != "executed" {
					t.Fatalf("no executed witness: %q %v", b, err)
				}
			})
		}
	}
}
