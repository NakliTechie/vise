package vise

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// evaluatorStateDigest summarizes everything that judges a probe and must not
// change while one runs: the manifest, the lockfile, the journal, and the set
// of blob names. A probe that touches any of them is refused as a harness
// failure, so a probe cannot erase its own rerun budget or rewrite its judge.
func evaluatorStateDigest(root string) (string, error) {
	h := sha256.New()
	for _, name := range []string{"vise.toml", "vise.lock", filepath.Join(".vise", "journal.jsonl")} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect %s: %w", name, err)
		}
		writeHashPart(h, name, data)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".vise", "blobs"))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect blobs: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		writeHashPart(h, "blob", []byte(name))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

const evaluatorStateMutated = "probe modified vise state (manifest, lockfile, blobs, or journal); probes may write only declared files and $VISE_TMP"
