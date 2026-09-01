package vise

import (
	"crypto/sha256"
	"encoding/hex"
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
	blobDir := filepath.Join(root, ".vise", "blobs")
	entries, err := os.ReadDir(blobDir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(blobDir, name))
		if err != nil {
			return "", err
		}
		writeHashPart(h, "blob:"+name, data)
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
