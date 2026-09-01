package vise

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func HashFile(path string) (string, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	return HashBytes(data), data, nil
}

func HashDependencies(root string, deps []string) (map[string]string, error) {
	result := make(map[string]string, len(deps))
	for _, rel := range deps {
		if err := ValidateRelativePath(root, rel, true); err != nil {
			return nil, fmt.Errorf("dependency %q: %w", rel, err)
		}
		hash, _, err := HashFile(filepath.Join(root, rel))
		if err != nil {
			return nil, fmt.Errorf("hash dependency %q: %w", rel, err)
		}
		result[filepath.ToSlash(filepath.Clean(rel))] = hash
	}
	return result, nil
}

func TamperHash(root string, manifest, lock []byte) (string, error) {
	h := sha256.New()
	writeHashPart(h, "manifest", manifest)
	writeHashPart(h, "lockfile", lock)
	var parsed Lockfile
	if err := json.Unmarshal(lock, &parsed); err != nil {
		return "", fmt.Errorf("parse lockfile for tamper hash: %w", err)
	}
	refs := referencedHashes(parsed)
	hashes := make([]string, 0, len(refs))
	for hash := range refs {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	for _, hash := range hashes {
		data, err := os.ReadFile(filepath.Join(root, ".vise", "blobs", HashName(hash)))
		if err != nil {
			return "", err
		}
		if HashBytes(data) != hash {
			return "", fmt.Errorf("blob %s failed its content hash", hash)
		}
		writeHashPart(h, "blob-index", []byte(hash))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func writeHashPart(w io.Writer, label string, data []byte) {
	fmt.Fprintf(w, "%d:%s:%d:", len(label), label, len(data))
	_, _ = w.Write(data)
	_, _ = io.WriteString(w, "\n")
}

func HashName(hash string) string {
	return strings.TrimPrefix(hash, "sha256:")
}
