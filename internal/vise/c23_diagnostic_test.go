package vise

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestC23ExecutedNotFoundTextIsOnlyCapturedEvidence(t *testing.T) {
	for _, command := range []string{"./program", "sh ./program", "printf outer; ./program"} {
		for _, diagnostic := range []string{"record not found", "No such file in application database", "sh: 1: invented-program: not found", "record not found\t\x1b[31m"} {
			t.Run(command+"/"+diagnostic, func(t *testing.T) {
				root := testGitRepo(t)
				writeTestFile(t, root, "message", diagnostic)
				writeTestFile(t, root, "program", "#!/bin/sh\nprintf ran\ncat message >&2\nexit 127\n")
				if err := os.Chmod(filepath.Join(root, "program"), 0o755); err != nil {
					t.Fatal(err)
				}
				probe := Probe{ID: "app", Run: command, Timeout: 5}
				got := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
				if got.Exit != 127 || !got.LaunchFailed || !got.Tolerated || got.HarnessOperator {
					t.Fatalf("classification changed: %#v", got)
				}
				if !strings.HasSuffix(string(got.Stdout.Prefix), "ran") || string(got.Stderr.Prefix) != diagnostic {
					t.Fatalf("lost executed-program/raw stderr witness: %#v", got)
				}
				want := "probe exited 127; captured stderr: " + strconv.Quote(diagnostic) + "; inspect the command's exit handling and dependencies"
				if got.HarnessError != want {
					t.Fatalf("diagnostic attributed more than captured evidence: got %q, want %q", got.HarnessError, want)
				}
			})
		}
	}
}

func TestC23MissingExecutableRetainsCapturedNameAndClassification(t *testing.T) {
	root := testGitRepo(t)
	probe := Probe{ID: "missing", Run: "./c23-actually-absent", Timeout: 5}
	got := (Runner{Root: root, Manifest: testManifest(probe)}).RunProbe(probe, false)
	if got.Exit != 127 || !got.LaunchFailed || !got.Tolerated || len(got.Stdout.Prefix) != 0 {
		t.Fatalf("missing-executable control changed: %#v", got)
	}
	line := strings.TrimSpace(string(got.Stderr.Prefix))
	if !strings.Contains(line, "c23-actually-absent") || !strings.Contains(got.HarnessError, "captured stderr: "+strconv.Quote(line)) {
		t.Fatalf("missing useful observed name: %#v", got)
	}
	if strings.Contains(got.HarnessError, "install what") || strings.Contains(got.HarnessError, "could not be launched") {
		t.Fatalf("unsupported provenance: %q", got.HarnessError)
	}
}

func TestC23MetricVersionAndFingerprintUseEvidenceQualifiedDiagnostics(t *testing.T) {
	root := testGitRepo(t)
	command := "printf 'record not found' >&2; exit 127"
	for _, tc := range []struct {
		name string
		run  func() string
	}{
		{"metric", func() string {
			return (Runner{Root: root}).RunMetric(Metric{ID: "m", Run: command, Timeout: 5}).HarnessError
		}},
		{"metric version command", func() string {
			return (Runner{Root: root}).RunMetric(Metric{ID: "m", Run: "printf 1", VersionCmd: command, Timeout: 5}).HarnessError
		}},
		{"environment fingerprint command", func() string {
			manifest := testManifest(Probe{ID: "p", Run: "true"})
			manifest.Environment.Fingerprint = []string{command}
			_, err := CaptureFingerprint(root, manifest)
			if err == nil {
				t.Fatal("fingerprint unexpectedly succeeded")
			}
			return err.Error()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.run()
			want := tc.name + " exited 127; captured stderr: \"record not found\"; inspect the command's exit handling and dependencies"
			if !strings.Contains(got, want) || strings.Contains(got, "install what") || strings.Contains(got, "could not be launched") {
				t.Fatalf("lost kind/evidence boundary: %q", got)
			}
		})
	}
}

func TestC23DiagnosticExcerptBoundAndSelectionDoNotAlterCapture(t *testing.T) {
	for _, length := range []int{199, 200, 201} {
		line := "not found " + strings.Repeat("界", length-10)
		capture := CaptureBytes([]byte("warning: unrelated\n" + line + "\nlater noise\n"))
		before := append([]byte(nil), capture.Prefix...)
		got := launchFailureDetail("probe", capture)
		shown := line
		if length > 200 {
			shown = string([]rune(line)[:200]) + "…"
		}
		want := "probe exited 127; captured stderr: " + strconv.Quote(shown) + "; inspect the command's exit handling and dependencies"
		if got != want || !utf8.ValidString(got) || !bytes.Equal(capture.Prefix, before) {
			t.Fatalf("%d-code-point boundary: got %q want %q; capture changed=%v", length, got, want, !bytes.Equal(capture.Prefix, before))
		}
	}
	for _, text := range []string{"", "unrelated application failure", " \n\t"} {
		got := launchFailureDetail("probe", CaptureBytes([]byte(text)))
		want := "probe exited 127; inspect the command's exit handling and dependencies"
		if got != want {
			t.Fatalf("no selected excerpt: got %q, want %q", got, want)
		}
	}
}

func TestC23SharedShellAndMetricResultsPreserveRawDiagnosticBytes(t *testing.T) {
	root := testGitRepo(t)
	wantOut := []byte("raw-stdout")
	wantErr := []byte("warning first\nrecord not found\t\x1b[31m\nraw tail")
	command := "printf 'raw-stdout'; printf 'warning first\\nrecord not found\\t\\033[31m\\nraw tail' >&2; exit 127"
	for _, kind := range []string{"probe", "metric", "metric version command", "environment fingerprint command"} {
		t.Run(kind, func(t *testing.T) {
			got := (Runner{Root: root}).runShell(kind, "raw", command, 5, nil)
			if !bytes.Equal(got.Stdout.Prefix, wantOut) || !bytes.Equal(got.Stderr.Prefix, wantErr) {
				t.Fatalf("%s raw captures changed: stdout=%q stderr=%q", kind, got.Stdout.Prefix, got.Stderr.Prefix)
			}
			if got.Stdout.Hash != HashBytes(wantOut) || got.Stderr.Hash != HashBytes(wantErr) {
				t.Fatalf("%s raw hashes changed: stdout=%s stderr=%s", kind, got.Stdout.Hash, got.Stderr.Hash)
			}
		})
	}
	metric := (Runner{Root: root}).RunMetric(Metric{ID: "raw", Run: command, Timeout: 5})
	if !bytes.Equal(metric.Stdout.Prefix, wantOut) || !bytes.Equal(metric.Stderr.Prefix, wantErr) || metric.Stdout.Hash != HashBytes(wantOut) || metric.Stderr.Hash != HashBytes(wantErr) {
		t.Fatalf("public metric result changed raw captures: stdout=%#v stderr=%#v", metric.Stdout, metric.Stderr)
	}
}
