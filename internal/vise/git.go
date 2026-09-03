package vise

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func GitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git work tree required: %s", strings.TrimSpace(stderr.String()))
	}
	root := strings.TrimSpace(stdout.String())
	if root == "" {
		return "", fmt.Errorf("git returned an empty work-tree root")
	}
	return filepath.Clean(root), nil
}

func GitHead(root string) (string, error) {
	return gitOutput(root, "rev-parse", "HEAD")
}

func GitDirty(root string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git status: %s", strings.TrimSpace(stderr.String()))
	}
	entries := bytes.Split(stdout.Bytes(), []byte{0})
	for _, entry := range entries {
		if len(entry) < 4 {
			continue
		}
		path := filepath.ToSlash(string(entry[3:]))
		if path == ".vise/run.lock" || path == ".vise/journal.jsonl" || strings.HasPrefix(path, ".vise/tmp/") {
			continue
		}
		return true, nil
	}
	return false, nil
}

// WorkspaceSnapshot is everything in the checkout a probe could change: the
// diff of tracked files against HEAD, and the content of every file Git
// neither tracks nor ignores.
type WorkspaceSnapshot struct {
	Tracked   string
	Untracked map[string]string
	// Git holds the parts of the repository's own state that the rest of this
	// snapshot trusts: HEAD, and the ignore rules that decide which files the
	// untracked scan even looks at.
	//
	// Without it the check could be walked past with three ordinary commands.
	// A probe stages its edit, writes a tree, commits it, and moves HEAD onto
	// that commit; the working tree now matches the new HEAD exactly, so the
	// diff against HEAD sees nothing and vise reports a clean run. The same
	// shape applies to .git/info/exclude: a probe that adds a pattern there
	// makes its own strays invisible to `git ls-files --others
	// --exclude-standard`. Both were found by a cold read, and the first was
	// reproduced before this field existed.
	Git string
}

// ChangedUntracked names the paths that appeared, vanished, or changed
// content between two snapshots, sorted, so a failure can say which file
// rather than only that one exists.
func (s WorkspaceSnapshot) ChangedUntracked(other WorkspaceSnapshot) []string {
	var changed []string
	for path, hash := range s.Untracked {
		if other.Untracked[path] != hash {
			changed = append(changed, path)
		}
	}
	for path := range other.Untracked {
		if _, ok := s.Untracked[path]; !ok {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

// GitWorkspaceSnapshot captures what a probe must leave alone.
//
// Tracked files are covered by their diff against HEAD. Untracked files are
// covered individually, because a probe that drops a stray file into the
// checkout changes what every later probe and every later build sees, and the
// tracked diff cannot see it at all. Ignored paths are deliberately outside
// the snapshot: a build cache is the one thing a probe is expected to write,
// and .gitignore is where the operator already declared which those are.
//
// exclude names paths the probe is entitled to write — its declared
// artifacts, which vise deletes and recreates on every run by design, and
// which are hashed and compared separately.
func GitWorkspaceSnapshot(root string, exclude []string) (WorkspaceSnapshot, error) {
	cmd := exec.Command("git", "diff", "--binary", "--no-ext-diff", "HEAD", "--", ".")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("snapshot tracked files: %w", err)
	}
	gitState, err := gitOwnState(root)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	snapshot := WorkspaceSnapshot{Tracked: HashBytes(data), Git: gitState}

	skip := make(map[string]bool, len(exclude))
	for _, rel := range exclude {
		skip[filepath.ToSlash(filepath.Clean(rel))] = true
	}
	paths, err := gitUntrackedPaths(root)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	// An empty directory a probe leaves behind is not detected. `git ls-files
	// --others` lists files, so finding one means walking the whole checkout
	// on every probe run — and that walk cannot honour the boundary this
	// snapshot is built on, because pruning ignored subtrees needs a
	// check-ignore call per directory. The cost is certain and the case is
	// marginal, so the limit is stated rather than paid for. It was written
	// and removed the same night: measure before believing that a
	// cheap-sounding check is cheap.
	snapshot.Untracked = make(map[string]string, len(paths))
	for _, rel := range paths {
		if skip[rel] || isViseLocalState(rel) {
			continue
		}
		hash, err := hashWorkspaceEntry(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return WorkspaceSnapshot{}, err
		}
		snapshot.Untracked[rel] = hash
	}
	return snapshot, nil
}

// isViseLocalState reports whether a path is vise's own per-checkout state,
// which changes during a run by design. The lockfile, the manifest, and the
// blobs are absent on purpose: those are the evaluator's own inputs, and a
// probe touching them is caught by evaluatorStateDigest with a message that
// says so.
func isViseLocalState(rel string) bool {
	return rel == ".vise/run.lock" || rel == ".vise/journal.jsonl" ||
		rel == ".vise/tmp" || strings.HasPrefix(rel, ".vise/tmp/")
}

func gitUntrackedPaths(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--others", "--exclude-standard", "--", ".")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("list untracked files: %s", strings.TrimSpace(stderr.String()))
	}
	var paths []string
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(entry) > 0 {
			paths = append(paths, filepath.ToSlash(string(entry)))
		}
	}
	return paths, nil
}

// hashWorkspaceEntry digests one untracked entry by content, size, and
// modification time.
//
// The modification time is in the digest because content alone lets a probe
// launder its own failure. A probe that writes a stray fails the first run;
// on the next run the stray already exists, the probe rewrites it with the
// same bytes, and a content-only comparison sees nothing — so rerunning turns
// a harness error green, which is the one move the tool must never reward.
// The same reasoning applies to a second writer touching the checkout mid-run,
// and the tracked half of this check has always failed on that, so treating
// untracked files more leniently was the inconsistency.
//
// A file that is not a regular file is recorded by its type instead of its
// content: reading a named pipe would block until something wrote to it,
// which would hang the judge on a file a probe left behind.
func hashWorkspaceEntry(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Raced with something removing it between listing and hashing;
			// treat it as absent, which is what the next snapshot will see.
			return "", nil
		}
		return "", fmt.Errorf("inspect untracked %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// The target, not just the fact that it is a symlink: a probe that
		// retargets an existing link changes what every later probe reads
		// through it, and the link itself looks untouched.
		target, err := os.Readlink(path)
		if err != nil {
			return "", fmt.Errorf("read link %s: %w", path, err)
		}
		// Modification time as well as the target, for the same reason a
		// regular file carries it: a link removed and recreated pointing at
		// the same place is still a probe changing the checkout, and comparing
		// the target alone cannot see it.
		return fmt.Sprintf("symlink:%s:%d", target, info.ModTime().UnixNano()), nil
	}
	if !info.Mode().IsRegular() {
		return "mode:" + info.Mode().Type().String(), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read untracked %s: %w", path, err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("read untracked %s: %w", path, err)
	}
	// Permission bits are in the digest because the executable bit is
	// behaviour: chmod +x on a script changes what running it does, and the
	// content hash does not move.
	return fmt.Sprintf("sha256:%s:%d:%d:%o", hex.EncodeToString(digest.Sum(nil)), info.Size(), info.ModTime().UnixNano(), info.Mode().Perm()), nil
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// GitTrackedPaths returns the subset of rels that Git tracks at root, in the
// order Git reports them. A declared artifact must not be tracked: vise
// deletes artifacts before every run, and a probe that then fails would
// leave a tracked file, and any uncommitted edits to it, deleted.
func GitTrackedPaths(root string, rels []string) ([]string, error) {
	if len(rels) == 0 {
		return nil, nil
	}
	// Match literally but case-insensitively: on a case-insensitive filesystem
	// (APFS, NTFS) a declared "tracked.txt" would delete a tracked
	// "Tracked.txt". Over-refusing a case variant on a case-sensitive
	// filesystem is the safe side.
	args := make([]string, 0, len(rels)+3)
	args = append(args, "ls-files", "-z", "--")
	for _, rel := range rels {
		args = append(args, ":(literal,icase)"+rel)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files: %s", strings.TrimSpace(stderr.String()))
	}
	var tracked []string
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(entry) > 0 {
			tracked = append(tracked, filepath.ToSlash(string(entry)))
		}
	}
	return tracked, nil
}

// gitOwnState digests the repository state this snapshot depends on: the
// commit the tracked diff is taken against, and the ignore rules that decide
// which files the untracked scan reports. A probe that changes either one
// changes what "the checkout is unchanged" means, without changing a file the
// snapshot would otherwise look at.
//
// The config file is in there because core.excludesFile points the ignore
// rules somewhere else, and a probe that repoints it has the same effect as
// editing the rules directly.
func gitOwnState(root string) (string, error) {
	digest := sha256.New()

	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		// A repository with no commits yet has no HEAD, and that is a state
		// vise supports: record refuses it for other reasons, and a probe run
		// through `vise run` should not fail here.
		head = ""
	}
	writeHashPart(digest, "head", []byte(head))

	gitDir, err := gitOutput(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("locate the git directory: %w", err)
	}
	for _, rel := range []string{filepath.Join("info", "exclude"), "config"} {
		data, err := os.ReadFile(filepath.Join(gitDir, rel))
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("read git %s: %w", rel, err)
		}
		writeHashPart(digest, rel, data)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
