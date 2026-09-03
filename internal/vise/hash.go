package vise

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HashFile digests a file without holding it in memory. A declared dependency
// can be a fixture of any size, and nobody wants the contents — only the hash —
// so reading the whole thing was a memory cost with nothing bought by it.
func HashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func HashDependencies(root string, deps []string) (map[string]string, error) {
	result := make(map[string]string, len(deps))
	for _, rel := range deps {
		if err := ValidateRelativePath(root, rel, true); err != nil {
			return nil, fmt.Errorf("dependency %q: %w", rel, err)
		}
		hash, err := HashFile(filepath.Join(root, rel))
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
		path, err := BlobPath(root, hash)
		if err != nil {
			return "", err
		}
		data, err := readRegularFile(path)
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

var sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func HashName(hash string) (string, error) {
	if !sha256Pattern.MatchString(hash) {
		return "", fmt.Errorf("invalid sha256 hash %q", hash)
	}
	return strings.TrimPrefix(hash, "sha256:"), nil
}

func BlobPath(root, hash string) (string, error) {
	name, err := HashName(hash)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".vise", "blobs", name), nil
}
