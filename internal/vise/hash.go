package vise

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return formatSHA256(sum[:])
}

// formatSHA256 renders a digest as vise writes it everywhere: the "sha256:"
// prefix and lowercase hex. Three functions here built that string by hand.
func formatSHA256(sum []byte) string {
	return "sha256:" + hex.EncodeToString(sum)
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
	return formatSHA256(digest.Sum(nil)), nil
}

func HashDependencies(root string, deps []string) (map[string]string, error) {
	result := make(map[string]string, len(deps))
	for _, rel := range deps {
		if err := ValidateRelativePath(root, rel, true); err != nil {
			return nil, &dependencyError{operation: "dependency", path: rel, cause: err}
		}
		hash, err := HashFile(filepath.Join(root, rel))
		if err != nil {
			return nil, &dependencyError{operation: "hash dependency", path: rel, cause: err}
		}
		result[filepath.ToSlash(filepath.Clean(rel))] = hash
	}
	return result, nil
}

// dependencyError keeps checkout-specific OS paths available through Unwrap,
// but renders the declared path instead. Moving the same broken fixture into
// a different checkout must not change the diagnostic captured by a probe.
// Preserve the failed operation and OS cause; this is not error suppression.
type dependencyError struct {
	operation string
	path      string
	cause     error
}

func (e *dependencyError) Error() string {
	detail := e.cause.Error()
	var pathErr *os.PathError
	if errors.As(e.cause, &pathErr) {
		detail = fmt.Sprintf("%s: %v", pathErr.Op, pathErr.Err)
	}
	return fmt.Sprintf("%s %q: %s", e.operation, e.path, detail)
}

func (e *dependencyError) Unwrap() error { return e.cause }

func TamperHash(root string, manifest, lock []byte) (string, error) {
	h := sha256.New()
	writeHashPart(h, "manifest", manifest)
	writeHashPart(h, "lockfile", lock)
	var parsed Lockfile
	if err := json.Unmarshal(lock, &parsed); err != nil {
		return "", fmt.Errorf("parse lockfile for tamper hash: %w", err)
	}
	for _, hash := range sortedKeys(referencedHashes(parsed)) {
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
