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
	// Under -z a record is `XY PATH`, except that a rename or a copy emits a
	// second record holding the original path with no status prefix. Slicing
	// three characters off every record therefore took them off a real path.
	// It could not change the answer, because the new-path record returns true
	// one iteration earlier — but a loop that mangles a path is a trap for
	// whoever extends it to report which path is dirty.
	entries := bytes.Split(stdout.Bytes(), []byte{0})
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		status := entry[:2]
		// The same predicate the snapshot uses, not a second copy of it. The
		// copy had drifted narrower — it was missing the bare `.vise/tmp` —
		// which is the failure mode of writing a concept down twice.
		if !isViseLocalState(filepath.ToSlash(string(entry[3:]))) {
			return true, nil
		}
		if bytes.ContainsAny(status, "RC") && i+1 < len(entries) {
			// The record that follows is this one's source path, whole.
			i++
			if len(entries[i]) > 0 && !isViseLocalState(filepath.ToSlash(string(entries[i]))) {
				return true, nil
			}
		}
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
// ChangedUntracked names every untracked path whose entry differs between two
// snapshots, in either direction.
//
// One pass over the union, comparing values. It used to be two passes that
// disagreed with each other: the first compared values and the second tested
// key presence, so a key present with an empty value — which is what a file
// that raced away between the listing and the hash used to produce — was a
// change in one direction and not in the other. Absent and empty are the same
// thing here, and now they compare the same way whichever side holds which.
func (s WorkspaceSnapshot) ChangedUntracked(other WorkspaceSnapshot) []string {
	var changed []string
	for _, path := range sortedUnionKeys(s.Untracked, other.Untracked) {
		if s.Untracked[path] != other.Untracked[path] {
			changed = append(changed, path)
		}
	}
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
	// Streamed into the hash rather than buffered. The diff of a large dirty
	// tree is proportional to the change, not to the observation bound, and
	// only its digest is wanted — so buffering it was memory spent on bytes
	// nobody reads.
	cmd := exec.Command("git", "diff", "--binary", "--no-ext-diff", "HEAD", "--", ".")
	cmd.Dir = root
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("snapshot tracked files: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("snapshot tracked files: %w", err)
	}
	digest := sha256.New()
	if _, copyErr := io.Copy(digest, pipe); copyErr != nil {
		_ = cmd.Wait()
		return WorkspaceSnapshot{}, fmt.Errorf("snapshot tracked files: %w", copyErr)
	}
	if err := cmd.Wait(); err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("snapshot tracked files: %w", err)
	}
	trackedHash := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	gitState, err := gitOwnState(root)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	snapshot := WorkspaceSnapshot{Tracked: trackedHash, Git: gitState}

	skip := make(map[string]bool, len(exclude))
	for _, rel := range exclude {
		skip[filepath.ToSlash(filepath.Clean(rel))] = true
	}
	paths, err := gitUntrackedPaths(root)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	hidden, err := gitIgnoredRuleFiles(root)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	paths = append(paths, hidden...)
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
		if hash == "" {
			// It raced away between the listing and the hash.
			// hashWorkspaceEntry says to treat that as absent, and a snapshot
			// that holds a key for a file which is not there is not describing
			// the checkout.
			//
			// Removing this line survives the suite, and should: the
			// comparison walks the union and compares values, so an
			// empty-valued key and an absent one are already the same answer.
			// That was the actual bug — two loops, one comparing values and
			// one testing key presence, disagreeing about exactly this case —
			// and it is fixed there rather than here. This stays so the map
			// means what it says, and the note is here so the next mutation
			// audit knows why nothing catches it.
			continue
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

// gitIgnoredRuleFiles lists .gitignore and .gitattributes files that Git is
// ignoring.
//
// Ignored paths are outside the snapshot on purpose — a build cache is what a
// probe is expected to write. These two are not that: they decide what the
// comparison reads. A .gitignore decides what "ignored" means, so a probe can
// write one naming both its stray and itself and leave nothing for the
// untracked scan to see; that was demonstrated with an ordinary shell probe
// before this existed. A .gitattributes decides whether a path is diffed at
// all and through what, and git reads it from the work tree whatever its
// ignore status.
//
// The pathspec keeps this from walking an ignored dependency tree: Git filters
// to these two names rather than listing everything it ignores.
func gitIgnoredRuleFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--others", "--ignored", "--exclude-standard",
		"--", ".gitignore", "*/.gitignore", ".gitattributes", "*/.gitattributes")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("list ignored rule files: %s", strings.TrimSpace(stderr.String()))
	}
	var paths []string
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(entry) > 0 {
			paths = append(paths, filepath.ToSlash(string(entry)))
		}
	}
	return paths, nil
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
		// Defensive, and deliberately not more than that. `git ls-files
		// --others` lists regular files and symlinks and nothing else, and
		// symlinks are handled above, so nothing normally reaches this line —
		// only a path that turned into a fifo, socket or device between the
		// listing and the hash. A mutation audit will report it as untested
		// because it is unreachable, not because it is unguarded.
		//
		// The consequence is a stated limit, beside the empty directory in
		// SPEC 2.2: a fifo or socket a probe leaves behind is invisible to the
		// snapshot, because it is invisible to Git.
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

	// One rev-parse for both answers: this runs twice per probe, and a process
	// spawn costs more than everything it computes.
	gitDir, head, err := gitDirAndHead(root)
	if err != nil {
		return "", err
	}
	writeHashPart(digest, "head", []byte(head))
	// The commit HEAD resolves to is not the whole of HEAD. A probe that runs
	// `git checkout --detach` at the same commit leaves the resolved value
	// identical and the repository on no branch, so the gate said green and
	// the operator's next commit went somewhere they did not expect. The file
	// carries exactly that distinction — `ref: refs/heads/main` against a bare
	// sha — and reading it costs no process, unlike a second rev-parse.
	//
	// This is the sixth thing the judged party could change that the snapshot
	// trusted, after HEAD's value, info/exclude, info/attributes, the resolved
	// config, and the resolved excludes file.
	headFile, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read git HEAD: %w", err)
	}
	writeHashPart(digest, "head-ref", headFile)
	// The repository's own rule files, which live in the git directory rather
	// than the work tree and so appear in neither half of the snapshot.
	// attributes is here beside exclude because `git diff` consults it: an
	// attribute decides whether a path is diffed at all and through what.
	for _, rel := range []string{filepath.Join("info", "exclude"), filepath.Join("info", "attributes")} {
		data, err := os.ReadFile(filepath.Join(gitDir, rel))
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("read git %s: %w", rel, err)
		}
		writeHashPart(digest, rel, data)
	}

	// The configuration Git resolves, not the repository's file. Hashing
	// .git/config alone left every setting inherited from the global or system
	// level outside the snapshot, and those decide things the comparison
	// depends on — where the ignore rules live, which attributes apply, what a
	// textconv filter renders a file as. `git config --list` is what Git
	// itself would answer.
	config, err := gitOutput(root, "config", "--list")
	if err != nil {
		// A repository with no readable configuration is a situation the rest
		// of the run will report better than this line can.
		config = ""
	}
	writeHashPart(digest, "config", []byte(config))

	// The excludes file Git actually resolves, wherever it lives. Digesting
	// .git/config catches a probe that *repoints* core.excludesFile; it does
	// nothing about a probe that appends to the file already pointed at, which
	// is usually outside the checkout entirely and is obeyed by
	// `git ls-files --others --exclude-standard` exactly like info/exclude.
	// Demonstrated with an ordinary shell probe before this line existed.
	if path := globalExcludesPath(root); path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("read the excludes file at %s: %w", path, err)
		}
		writeHashPart(digest, "excludesfile", data)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// globalExcludesPath returns the ignore file Git consults besides the
// repository's own, or "" when there is none. Git takes core.excludesFile when
// it is set, and otherwise $XDG_CONFIG_HOME/git/ignore, defaulting to
// ~/.config/git/ignore.
func globalExcludesPath(root string) string {
	if configured, err := gitOutput(root, "config", "--get", "core.excludesFile"); err == nil && configured != "" {
		return expandHome(configured)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git", "ignore")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "git", "ignore")
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
}

// gitDirAndHead resolves the git directory and the current commit in one call.
// A repository with no commits yet has no HEAD, and that is a state vise
// supports: record refuses it for its own reasons, and a probe run through
// `vise run` should not fail here.
func gitDirAndHead(root string) (gitDir, head string, err error) {
	out, err := gitOutput(root, "rev-parse", "--absolute-git-dir", "HEAD")
	if err != nil {
		// HEAD is the part that can be absent, so ask for the directory alone
		// before giving up.
		gitDir, dirErr := gitOutput(root, "rev-parse", "--absolute-git-dir")
		if dirErr != nil {
			return "", "", fmt.Errorf("locate the git directory: %w", dirErr)
		}
		return gitDir, "", nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return strings.TrimSpace(out), "", nil
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), nil
}

// GitHasCommits reports whether HEAD names a commit. An unborn branch — a
// repository somebody has just run `git init` in — makes rev-parse fail with a
// message about an ambiguous argument, which is true and unhelpful.
func GitHasCommits(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD")
	cmd.Dir = root
	return cmd.Run() == nil
}
