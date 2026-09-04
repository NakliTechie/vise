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
		data, err := readRegularFile(filepath.Join(root, name))
		switch {
		case os.IsNotExist(err):
			// Absent and empty must not hash alike. writeHashPart frames a nil
			// and a zero-byte file identically, so without a presence marker a
			// probe that creates an empty vise.lock where none existed — or
			// removes one — left the digest unmoved. The label carries the
			// distinction.
			writeHashPart(h, "absent:"+name, nil)
		case err != nil:
			// readRegularFile, not os.ReadFile: a symlink or special file in
			// place of a state file is itself a mutation. os.ReadFile followed
			// it and hashed the target, so a probe could swap vise.lock for a
			// symlink to a byte-identical file and the digest would not move.
			return "", fmt.Errorf("inspect %s: %w", name, err)
		default:
			writeHashPart(h, "present:"+name, data)
		}
	}
	// The blobs by content, not only by name. The store is content-addressed,
	// so a probe that overwrites .vise/blobs/<name> keeps the filename while
	// changing the bytes an operator's diff is rendered from — and hashing the
	// name alone left before and after identical. This guard's own message
	// names "blobs" among what a probe may not touch; until now it could. The
	// cost is bounded: blobs cap at the capture bound and there are few, well
	// under the work-tree snapshot that runs beside this.
	blobDir := filepath.Join(root, ".vise", "blobs")
	entries, err := os.ReadDir(blobDir)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect blobs: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := readRegularFile(filepath.Join(blobDir, name))
		if err != nil {
			return "", fmt.Errorf("inspect blob %s: %w", name, err)
		}
		writeHashPart(h, "blob:"+name, data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

const evaluatorStateMutated = "probe modified vise state (manifest, lockfile, blobs, or journal); probes may write only declared files and $VISE_TMP"
