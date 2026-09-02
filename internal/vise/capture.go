package vise

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// CaptureLimit is how many bytes of an observation vise keeps in memory. It
// equals MaxBlobSize, so any observation small enough to be stored as a blob
// is held whole, and anything larger is hash-only in the lockfile anyway.
const CaptureLimit = MaxBlobSize

// Capture is a bounded record of one output stream: the hash and length of
// everything the stream produced, plus its first CaptureLimit bytes. A probe
// that prints gigabytes cannot exhaust memory before its timeout fires.
type Capture struct {
	Prefix []byte
	Hash   string
	Size   int64
}

// Truncated reports whether the stream was longer than the retained prefix.
func (c Capture) Truncated() bool { return c.Size > int64(len(c.Prefix)) }

// Complete returns the whole stream when it fit inside the prefix.
func (c Capture) Complete() ([]byte, bool) {
	if c.Truncated() {
		return nil, false
	}
	return c.Prefix, true
}

// Equal compares two observations of the same stream. Hash and size decide;
// the prefix is only for rendering.
func (c Capture) Equal(other Capture) bool {
	return c.Hash == other.Hash && c.Size == other.Size
}

// CaptureBytes records data that is already in memory.
func CaptureBytes(data []byte) Capture {
	writer := newCaptureWriter(nil)
	_, _ = writer.Write(data)
	return writer.Capture()
}

// captureWriter hashes and counts everything written through it while keeping
// only the first CaptureLimit bytes. A mirror, when set, receives every byte:
// that is how `vise run` prints a probe's complete output without vise ever
// holding it.
type captureWriter struct {
	prefix []byte
	digest hash.Hash
	size   int64
	mirror io.Writer
}

func newCaptureWriter(mirror io.Writer) *captureWriter {
	return &captureWriter{digest: sha256.New(), mirror: mirror}
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.digest.Write(p)
	w.size += int64(len(p))
	if room := CaptureLimit - len(w.prefix); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		w.prefix = append(w.prefix, p[:room]...)
	}
	if w.mirror != nil {
		written, err := w.mirror.Write(p)
		if err != nil {
			return len(p), err
		}
		if written < len(p) {
			return len(p), io.ErrShortWrite
		}
	}
	return len(p), nil
}

func (w *captureWriter) Capture() Capture {
	return Capture{
		Prefix: w.prefix,
		Hash:   "sha256:" + hex.EncodeToString(w.digest.Sum(nil)),
		Size:   w.size,
	}
}

// captureFile reads a declared artifact through the same bound.
func captureFile(path string) (Capture, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return Capture{}, err
	}
	defer file.Close()
	writer := newCaptureWriter(nil)
	if _, err := io.Copy(writer, file); err != nil {
		return Capture{}, err
	}
	return writer.Capture(), nil
}
