package vise

import (
	"io"
	"os"
)

// The persistence seam. Every write that has to survive a crash goes through
// it, so the ordering contract (SPEC §3.1 — blobs, then the lockfile, then the
// journal) can be tested by making any single step fail.
type stagedFile interface {
	io.Writer
	Name() string
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

type fileSystem interface {
	MkdirAll(path string, perm os.FileMode) error
	CreateStaged(dir, pattern string) (stagedFile, error)
	Rename(from, to string) error
	SyncDir(path string) error
	Remove(path string) error
}

type osFileSystem struct{}

func (osFileSystem) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func (osFileSystem) CreateStaged(dir, pattern string) (stagedFile, error) {
	return os.CreateTemp(dir, pattern)
}

func (osFileSystem) Rename(from, to string) error { return os.Rename(from, to) }

func (osFileSystem) Remove(path string) error { return os.Remove(path) }

// SyncDir flushes a directory entry so a rename survives a power loss. A
// filesystem that cannot sync a directory is not an error: the rename itself
// is already atomic, and the durability upgrade is best effort.
func (osFileSystem) SyncDir(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return nil
	}
	_ = handle.Sync()
	return handle.Close()
}

var persistence fileSystem = osFileSystem{}
