package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func c18RunAt(t *testing.T, cwd string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := Run(args, cwd, &stdout, &stderr)
	return exit, stdout.String(), stderr.String()
}

func c18EvaluatorSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, rel := range []string{"vise.lock", ".vise/blobs", ".vise/journal.jsonl"} {
		path := filepath.Join(root, rel)
		err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			got[name] = fmt.Sprintf("%x", sha256.Sum256(data))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func TestC18IndependentFixturesRetainTheirOwnEvaluatorState(t *testing.T) {
	first := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf first")
	second := cliRepo(t, basicManifest(""), "#!/bin/sh\nprintf second")
	for _, root := range []string{first, second} {
		if exit, out, errOut := cliRun(t, root, "record", "--json"); exit != 0 {
			t.Fatalf("record %s: %d %s %s", root, exit, out, errOut)
		}
	}
	firstBefore, secondBefore := c18EvaluatorSnapshot(t, first), c18EvaluatorSnapshot(t, second)
	firstNested, secondNested := filepath.Join(first, "nested"), filepath.Join(second, "nested")
	if err := os.MkdirAll(firstNested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondNested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "doctor", "verify", "gate"} {
		if exit, out, errOut := c18RunAt(t, firstNested, command, "--json"); exit != 0 {
			t.Fatalf("first %s: %d %s %s", command, exit, out, errOut)
		}
		if got := c18EvaluatorSnapshot(t, second); !reflect.DeepEqual(got, secondBefore) {
			t.Fatalf("first %s replaced second fixture state: before=%v after=%v", command, secondBefore, got)
		}
	}
	firstBefore = c18EvaluatorSnapshot(t, first)
	for _, command := range []string{"status", "doctor", "verify", "gate"} {
		if exit, out, errOut := c18RunAt(t, secondNested, command, "--json"); exit != 0 {
			t.Fatalf("second %s: %d %s %s", command, exit, out, errOut)
		}
		if got := c18EvaluatorSnapshot(t, first); !reflect.DeepEqual(got, firstBefore) {
			t.Fatalf("second %s replaced first fixture state: before=%v after=%v", command, firstBefore, got)
		}
	}
}
