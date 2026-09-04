package vise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// DoctorFinding is one thing an operator should fix before handing the
// repository to an agent. Every check here corresponds to a failure that
// actually happened while running coding agents against a vise-gated
// checkout: each one cost an hour to diagnose from inside the agent's
// sandbox, and each one is visible in seconds from outside it.
type DoctorFinding struct {
	Check  string `json:"check"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy"`
}

// DoctorReport is the whole answer to "is this repository ready for an agent
// to work in". It is read-only and always exits 0, for the same reason status
// does: it reports a situation to an operator, it does not judge a change.
type DoctorReport struct {
	V        int             `json:"v"`
	Cmd      string          `json:"cmd"`
	Exit     int             `json:"exit"`
	Ready    bool            `json:"ready"`
	Findings []DoctorFinding `json:"findings"`
	Next     Next            `json:"next"`
}

// doctorCheck pairs a check's name with the function that performs it.
//
// A hand-maintained list beside a hand-written call chain is two things that
// drift: deleting a call left the "is it documented" guard passing, and
// renaming the emitted name left it checking the old one. The registry is the
// chain — Doctor iterates it — and a test asserts every finding carries the
// name it was registered under.
// Doctor is an advisory, best-effort predictor, held to a lower bar than the
// gate on purpose. It reads the manifest and the checkout without running a
// probe, so it cannot know everything the gate learns by running one, and it is
// not trying to. Two rules bound what a maintainer may rely on:
//
//   - It fails safe. Where a check cannot answer — a git command that errors, a
//     file it cannot read — it reports the uncertainty as a finding, never a
//     clean pass. A doctor that says ready means "nothing I check is wrong",
//     not "nothing is wrong".
//   - A miss costs a surprise, not a wrong verdict. The worst a false clean
//     does is let an operator meet a gate failure unwarned; the gate still
//     refuses the bad state.
//
// The known limits are the ones a static token scan inherits, and closing them
// completely is a non-goal because the gate is the real check: portability is
// judged by the shape of a token, so a path in an unusual place can be missed
// and shell syntax that looks like a path can be over-flagged; an undeclared
// wrapper is found by the heuristic that a file names $VISE_TMP, which a
// coincidence can trip and an unusual wrapper can evade. Four spar rounds by
// three model families drove the reachable cases to ground; the tail that
// remains is prediction error, caught by the gate when the probe runs.

type doctorCheck struct {
	Name string
	// NeedsManifest is false for a check that inspects only the checkout on
	// disk. Those four still run when vise.toml will not parse, so a manifest
	// error does not hide an uncommitted baseline, unignored state, a missing
	// contract, or an oversized tree — findings the operator would otherwise
	// discover only after fixing the manifest and running doctor a second time.
	NeedsManifest bool
	Run           func(root string, manifest Manifest) []DoctorFinding
}

var doctorRegistry = []doctorCheck{
	{"env-fingerprint", true, func(_ string, m Manifest) []DoctorFinding { return checkFingerprint(m) }},
	{"portable-paths", true, func(_ string, m Manifest) []DoctorFinding { return checkPortablePaths(m) }},
	{"declared-inputs", true, checkUndeclaredScripts},
	{"baseline-committed", false, func(root string, _ Manifest) []DoctorFinding { return checkBaselineCommitted(root) }},
	{"local-state-ignored", false, func(root string, _ Manifest) []DoctorFinding { return checkLocalStateIgnored(root) }},
	{"agent-contract", false, func(root string, _ Manifest) []DoctorFinding { return checkAgentContract(root) }},
	{"tracked-artifacts", true, checkTrackedArtifacts},
	{"snapshot-cost", false, func(root string, _ Manifest) []DoctorFinding { return checkSnapshotCost(root) }},
}

// DoctorChecks is every check name Doctor can emit: the registry, plus the two
// the registry cannot hold — "manifest", which replaces all of them when
// vise.toml will not parse, and "git-work-tree", which the CLI emits before a
// repository is even resolved.
var DoctorChecks = func() []string {
	names := []string{"manifest", "git-work-tree"}
	for _, check := range doctorRegistry {
		names = append(names, check.Name)
	}
	sort.Strings(names)
	return names
}()

// Doctor inspects the checkout without running a probe or writing anything.
func Doctor(root string) DoctorReport {
	report := DoctorReport{V: 1, Cmd: "doctor", Findings: []DoctorFinding{}}

	manifest, _, err := LoadManifest(root)
	if err != nil {
		report.Findings = append(report.Findings, DoctorFinding{
			Check:  "manifest",
			Detail: err.Error(),
			Remedy: "fix vise.toml, or run vise init to write a starter manifest",
		})
		// The manifest is unusable, so the checks that read it cannot run — but
		// the four that inspect only the checkout still can, and hiding them
		// behind the parse error meant a second wave of findings appeared only
		// after the operator fixed the manifest. Report everything visible now.
		for _, check := range doctorRegistry {
			if !check.NeedsManifest {
				report.Findings = append(report.Findings, check.Run(root, Manifest{})...)
			}
		}
		report.finish()
		return report
	}

	for _, check := range doctorRegistry {
		report.Findings = append(report.Findings, check.Run(root, manifest)...)
	}
	report.finish()
	return report
}

func (r *DoctorReport) finish() {
	sort.SliceStable(r.Findings, func(i, j int) bool { return r.Findings[i].Check < r.Findings[j].Check })
	r.Ready = len(r.Findings) == 0
	if r.Ready {
		r.Next = Next{Action: NextProceed, Detail: "the repository is ready to hand to an agent"}
		return
	}
	r.Next = Next{Action: NextHuman, Detail: fmt.Sprintf("%d finding(s) an operator should resolve before an agent works here", len(r.Findings))}
}

// checkFingerprint: an unfingerprinted toolchain moves without saying so. The
// baseline then holds observations from a compiler nobody can name, and the
// first cross-machine red looks like a behavior change.
func checkFingerprint(manifest Manifest) []DoctorFinding {
	// Declared is not the same as useful. fingerprint = [""] parses, passes
	// validation, and captures nothing, which is worse than declaring none:
	// the operator believes the toolchain is watched.
	for _, command := range manifest.Environment.Fingerprint {
		if strings.TrimSpace(command) != "" {
			return nil
		}
	}
	detail := "the manifest declares no [env] fingerprint, so a toolchain change is invisible to the gate"
	if len(manifest.Environment.Fingerprint) > 0 {
		detail = "every [env] fingerprint command is blank, so the toolchain is recorded as nothing and a change to it is invisible"
	}
	return []DoctorFinding{{
		Check:  "env-fingerprint",
		Detail: detail,
		Remedy: "uncomment the [env] fingerprint block vise init writes, and name the version command for every tool a probe runs",
	}}
}

// checkPortablePaths: a probe that names an absolute path, $HOME, or ~ runs on
// the machine that recorded it and nowhere else. An agent sandbox is a
// different machine even when it is the same computer, and this is the single
// failure that cost the most time when vise was first handed to real agents.
//
// Findings are grouped by the offending value, not by the probe that carries
// it: one module cache named in eight probes is one thing to fix, and eight
// identical lines is a wall the operator learns to scroll past.
// namesHomeVariable reports whether a value references the HOME variable, as
// $HOME or ${HOME}. A plain substring test got both ends wrong: it missed
// ${HOME}/x (the "$HOME" bytes are not contiguous) and flagged $HOMEBREW (the
// bytes are contiguous but the variable is HOMEBREW, not HOME).
func namesHomeVariable(value string) bool {
	if strings.Contains(value, "${HOME}") {
		return true
	}
	rest := value
	for {
		i := strings.Index(rest, "$HOME")
		if i < 0 {
			return false
		}
		after := rest[i+len("$HOME"):]
		// $HOME ends the reference unless a letter, digit or underscore
		// continues the variable name (which would make it $HOMEBREW, say).
		if after == "" || !isVarNameByte(after[0]) {
			return true
		}
		rest = after
	}
}

func isVarNameByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func checkPortablePaths(manifest Manifest) []DoctorFinding {
	looksMachineLocal := func(part string) bool {
		if strings.HasPrefix(part, "/bin/") || strings.HasPrefix(part, "/usr/bin/") {
			return false
		}
		return strings.HasPrefix(part, "/") || strings.HasPrefix(part, "~/") || part == "~" ||
			strings.HasPrefix(part, "../") || namesHomeVariable(part)
	}
	unportable := func(value string) bool {
		// A machine-local path hides after KEY= (GOMODCACHE=/home/op/cache) and
		// after a compiler flag (-I/opt/include), not only at the token's
		// start — the fingerprint loop already strips one such prefix, which is
		// the author knowing these exist. Split on = only (splitting on : would
		// turn http://x into //x and flag every URL), and strip a leading short
		// flag like -I or -L.
		for _, part := range strings.Split(value, "=") {
			trimmed := part
			if len(trimmed) >= 2 && trimmed[0] == '-' && (trimmed[1] == 'I' || trimmed[1] == 'L' || trimmed[1] == 'o') {
				trimmed = trimmed[2:]
			}
			if looksMachineLocal(trimmed) {
				return true
			}
		}
		return false
	}
	sites := map[string][]string{}
	note := func(value, where string) {
		if !unportable(value) {
			return
		}
		if !slices.Contains(sites[value], where) {
			sites[value] = append(sites[value], where)
		}
	}
	for _, probe := range manifest.Probes {
		for _, token := range strings.Fields(probe.Run) {
			note(strings.Trim(token, "\"'"), "probe "+probe.ID)
		}
		for key, value := range probe.Env {
			note(value, "probe "+probe.ID+" env "+key)
		}
		for _, dep := range probe.Deps {
			note(dep, "probe "+probe.ID+" deps")
		}
	}
	// Metrics run the same shell a probe does (RunMetric), so a metric that
	// names a machine-local path is the same trap and was invisible here.
	for _, metric := range manifest.Metrics {
		for _, token := range strings.Fields(metric.Run) {
			note(strings.Trim(token, "\"'"), "metric "+metric.ID)
		}
		for _, token := range strings.Fields(metric.VersionCmd) {
			note(strings.Trim(token, "\"'"), "metric "+metric.ID+" version_cmd")
		}
		for key, value := range metric.Env {
			note(value, "metric "+metric.ID+" env "+key)
		}
	}
	// The fingerprint command carries the same trap and is easier to miss:
	// it names the toolchain, so it is exactly where an absolute path to a
	// module cache or an SDK ends up.
	for _, command := range manifest.Environment.Fingerprint {
		for _, token := range strings.Fields(command) {
			note(strings.TrimPrefix(strings.Trim(token, "\"'"), "GOMODCACHE="), "[env] fingerprint")
		}
	}

	values := make([]string, 0, len(sites))
	for value := range sites {
		values = append(values, value)
	}
	sort.Strings(values)

	findings := make([]DoctorFinding, 0, len(values))
	for _, value := range values {
		where := sites[value]
		sort.Strings(where)
		named := where
		suffix := ""
		if len(named) > 3 {
			named = named[:3]
			suffix = fmt.Sprintf(" (and %d more)", len(where)-3)
		}
		findings = append(findings, DoctorFinding{
			Check:  "portable-paths",
			Detail: fmt.Sprintf("%q is outside the checkout, named by %s%s", value, strings.Join(named, ", "), suffix),
			Remedy: "make the path relative to the repository, or accept that this baseline only gates on a machine where that path exists and say so in the agent contract",
		})
	}
	return findings
}

// checkUndeclaredScripts: a probe that runs a harness wrapper without listing
// it in deps can have its own definition changed underneath it. The run string
// is hashed; the script it calls is not, so editing the wrapper changes what
// is observed with no harness drift to show for it.
//
// The check fires only on a file that mentions $VISE_TMP. That is the line
// between the two cases, and getting it wrong is worse than not checking:
// declaring the *code under test* as a dep would make every refactor of it a
// harness error, which disables the gate the operator came here to set up.
// Nothing but a vise-aware wrapper refers to $VISE_TMP, so a file that does is
// part of the harness and belongs in deps; a file that does not is the subject
// and must stay out of them.
func checkUndeclaredScripts(root string, manifest Manifest) []DoctorFinding {
	var findings []DoctorFinding
	for _, probe := range manifest.Probes {
		declared := make(map[string]bool, len(probe.Deps))
		for _, dep := range probe.Deps {
			declared[filepath.ToSlash(filepath.Clean(dep))] = true
		}
		for _, token := range strings.Fields(probe.Run) {
			token = strings.Trim(token, "\"'")
			if strings.HasPrefix(token, "-") || strings.ContainsAny(token, "$*?|<>") {
				continue
			}
			rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(token, "./")))
			if rel == "." || strings.HasPrefix(rel, "..") || strings.HasPrefix(rel, "/") || declared[rel] {
				continue
			}
			if !mentionsProbeScratch(filepath.Join(root, filepath.FromSlash(rel))) {
				continue
			}
			findings = append(findings, DoctorFinding{
				Check:  "declared-inputs",
				Detail: fmt.Sprintf("probe %s runs %s, a harness wrapper that is not in its deps", probe.ID, rel),
				Remedy: fmt.Sprintf("add deps = [%q] to probe %s, so editing the wrapper is harness drift rather than a silent change of what is observed", rel, probe.ID),
			})
		}
	}
	return findings
}

// mentionsProbeScratch reports whether a regular file refers to $VISE_TMP, the
// one thing only a probe wrapper knows about. Bounded: a wrapper is a small
// script, and a doctor run must not read a binary into memory to answer a
// question about a shell script.
func mentionsProbeScratch(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte("VISE_TMP"))
}

// contractHasContent reports whether AGENTS.md holds anything but whitespace. A
// file of only spaces and newlines has a non-zero size and tells an agent
// nothing, which is the one thing this check exists to prevent. Bounded: the
// contract is small and the read is capped.
func contractHasContent(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		// Unreadable is handled by the caller's error branch; treat an
		// unreadable file as content here so this helper does not turn a
		// permission error into "empty".
		return true
	}
	return len(bytes.TrimSpace(data)) > 0
}

// checkBaselineCommitted: a baseline that lives only on the operator's disk
// cannot judge anything in a fresh clone, which is where an agent and CI both
// work.
func checkBaselineCommitted(root string) []DoctorFinding {
	if _, err := os.Stat(filepath.Join(root, "vise.lock")); err != nil {
		// "run vise record" is only runnable once there is a commit to record
		// against — record refuses a repository with no commits (resolveHead).
		// On a fresh git init the bare remedy fails and the finding persists,
		// so name the missing step.
		remedy := "run vise record, then commit vise.lock and .vise/blobs/"
		switch {
		case !GitHasCommits(root):
			remedy = "commit the harness first (a baseline freezes behavior at a commit), then run vise record and commit vise.lock and .vise/blobs/"
		default:
			// record refuses a dirty tree, so "run vise record" fails verbatim
			// when the harness is uncommitted. Name the commit step, which also
			// cleans the tree.
			if dirty, err := GitDirty(root); err == nil && dirty {
				remedy = "commit or stash the working tree first (record refuses a dirty one), then run vise record and commit vise.lock and .vise/blobs/"
			}
		}
		return []DoctorFinding{{
			Check:  "baseline-committed",
			Detail: "no vise.lock exists, so there is nothing to gate against",
			Remedy: remedy,
		}}
	}
	// Against HEAD, and against the bytes that are actually here. `git
	// ls-files` calls a staged file tracked, so a `git add vise.lock` with no
	// commit used to pass; and merely existing in HEAD is not enough either,
	// because an older committed lock at that path would satisfy it while the
	// baseline in the working tree is something else entirely. A fresh clone
	// gets what HEAD holds, and that is the only thing this check is about.
	var findings []DoctorFinding
	// readRegularFile, not os.ReadFile: a vise.lock that is a symlink or a fifo
	// makes os.ReadFile follow it or block on it, so doctor could hang or read
	// off the repository where the gate loads the same file through
	// readRegularFile precisely to refuse both. A non-regular lock is itself a
	// finding, not a clean pass.
	current, lockErr := readRegularFile(filepath.Join(root, "vise.lock"))
	if lockErr != nil && !os.IsNotExist(lockErr) {
		return append(findings, DoctorFinding{
			Check:  "baseline-committed",
			Detail: "vise.lock is not a regular file (" + lockErr.Error() + "), so the gate cannot load it",
			Remedy: "replace vise.lock with the recorded baseline file; a symlink or special file here is not one",
		})
	}
	committed, headErr := gitFileAtHead(root, "vise.lock")
	switch {
	case headErr != nil:
		findings = append(findings, DoctorFinding{
			Check:  "baseline-committed",
			Detail: "vise.lock is not committed, so a fresh clone has no baseline",
			Remedy: "git add vise.lock && git commit",
		})
	case lockErr == nil && !bytes.Equal(current, committed):
		findings = append(findings, DoctorFinding{
			Check:  "baseline-committed",
			Detail: "the committed vise.lock is not the one in the working tree, so a fresh clone gates against a different baseline",
			Remedy: "git add vise.lock && git commit",
		})
	}

	// Every blob the lockfile references, not every file in the blob
	// directory. Asking the directory meant a stray orphan produced a finding
	// while a referenced blob that was never committed produced none — the
	// wrong way round, since the missing one is what a reviewer in a fresh
	// clone will find they cannot render.
	var lock Lockfile
	source := current
	if lockErr != nil {
		source = committed
	}
	if len(source) == 0 || json.Unmarshal(source, &lock) != nil {
		// A working-tree vise.lock that is present but empty or unparseable is
		// a corrupt baseline the gate refuses (LoadLockfile), not a clean pass.
		// Only report it when there is one to be corrupt: a missing lock is the
		// finding above, and an unparseable committed side with no working-tree
		// lock is not this check's concern.
		if lockErr == nil {
			detail := "vise.lock is present but not valid JSON, so the gate cannot load this baseline"
			if len(current) == 0 {
				detail = "vise.lock is present but empty, so the gate cannot load this baseline"
			}
			findings = append(findings, DoctorFinding{
				Check:  "baseline-committed",
				Detail: detail,
				// record refuses an existing lock it cannot load, so re-record
				// in place fails; the corrupt file has to go first.
				Remedy: "remove the corrupt vise.lock, then run vise record and commit the new one",
			})
		}
		return findings
	}
	var missing, malformed int
	referenced := referencedHashes(lock)
	for hash := range referenced {
		name, err := HashName(hash)
		if err != nil {
			// A referenced hash that will not parse is not a clean skip: the
			// gate refuses a lockfile like this (validateLockfileHashes), so
			// doctor silently continuing reported ready for a baseline the gate
			// cannot load.
			malformed++
			continue
		}
		if !gitBlobCommitted(root, ".vise/blobs/"+name) {
			missing++
		}
	}
	if malformed > 0 {
		findings = append(findings, DoctorFinding{
			Check:  "baseline-committed",
			Detail: fmt.Sprintf("%d of %d hashes the lockfile references are malformed, so the gate cannot load this baseline", malformed, len(referenced)),
			Remedy: "remove the corrupt vise.lock, then run vise record and commit the new one",
		})
	}
	if missing > 0 {
		findings = append(findings, DoctorFinding{
			Check:  "baseline-committed",
			Detail: fmt.Sprintf("%d of %d blobs the lockfile references are not committed, so a fresh clone cannot render a divergence", missing, len(referenced)),
			Remedy: "git add .vise/blobs && git commit",
		})
	}
	return findings
}

// gitBlobCommitted reports whether a path exists in the commit HEAD points at,
// without reading its contents. checkBaselineCommitted asked a yes/no question
// with git show, which buffered the whole blob into memory; cat-file -e answers
// it with an exit code.
func gitBlobCommitted(root, rel string) bool {
	cmd := exec.Command("git", "cat-file", "-e", "HEAD:"+rel)
	cmd.Dir = root
	return cmd.Run() == nil
}

// gitFileAtHead returns a file's contents from the commit HEAD points at.
func gitFileAtHead(root, rel string) ([]byte, error) {
	cmd := exec.Command("git", "show", "HEAD:"+rel)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show HEAD:%s: %s", rel, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// checkLocalStateIgnored: the journal, the run lock, and the probe scratch are
// per-checkout state. Committing them makes every gate dirty the tree, which
// an agent then reports as a change it did not make.
func checkLocalStateIgnored(root string) []DoctorFinding {
	var missing []string
	for _, path := range []string{".vise/journal.jsonl", ".vise/run.lock", ".vise/tmp/"} {
		cmd := exec.Command("git", "check-ignore", "-q", "--no-index", path)
		cmd.Dir = root
		err := cmd.Run()
		if err == nil {
			continue // git says this path is ignored
		}
		// check-ignore exits 1 when the path is not ignored and 128 on a real
		// failure (not a repo, broken git). Only exit 1 is the finding; a git
		// failure reported as "not ignored, run vise init" sends the operator
		// to fix the wrong thing, so it is surfaced distinctly.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			missing = append(missing, path)
			continue
		}
		return []DoctorFinding{{
			Check:  "local-state-ignored",
			Detail: "could not check whether git ignores vise's state (" + path + "): " + err.Error(),
			Remedy: "resolve the git error above, then run vise doctor again",
		}}
	}
	if len(missing) == 0 {
		return nil
	}
	return []DoctorFinding{{
		Check:  "local-state-ignored",
		Detail: "git does not ignore vise's per-checkout state: " + strings.Join(missing, ", "),
		Remedy: "run vise init, which appends the missing entries to .gitignore without touching anything else",
	}}
}

// checkAgentContract: an agent with no written rules will discover them by
// breaking them, and the cheapest of those discoveries costs a session.
func checkAgentContract(root string) []DoctorFinding {
	// A regular file with something in it. An empty AGENTS.md, or a directory
	// by that name, satisfied a bare Stat while telling an agent nothing —
	// and the whole value of the check is that somebody wrote the rules down.
	contractPath := filepath.Join(root, "AGENTS.md")
	info, err := os.Lstat(contractPath)
	switch {
	case err == nil && info.Mode().IsRegular() && info.Size() > 0 && contractHasContent(contractPath):
		return nil
	case err == nil:
		// "run vise init" is a dead end here: init will not overwrite a file
		// that is already there, so on an empty AGENTS.md it reports nothing to
		// write and leaves the empty file. The operator has to remove it first.
		return []DoctorFinding{{
			Check:  "agent-contract",
			Detail: "AGENTS.md exists but is empty or is not a regular file, so an agent working here has no written rules",
			Remedy: "remove the empty AGENTS.md and run vise init, or copy the agent contract into it by hand; init will not overwrite a file that is already there",
		}}
	}
	if !os.IsNotExist(err) {
		// A permission error is not a missing file. Reporting "no AGENTS.md"
		// and "run vise init" misstates what is on disk and cannot fix
		// permissions.
		return []DoctorFinding{{
			Check:  "agent-contract",
			Detail: "AGENTS.md could not be inspected: " + err.Error(),
			Remedy: "make AGENTS.md readable, then run vise doctor again",
		}}
	}
	return []DoctorFinding{{
		Check:  "agent-contract",
		Detail: "no AGENTS.md at the repository root, so an agent working here has no written rules",
		Remedy: "run vise init, which writes the agent contract without overwriting an existing one",
	}}
}

// snapshotFileBudget and snapshotByteBudget bound what the work-tree snapshot
// can cost before it is worth an operator's attention.
//
// vise hashes every untracked, unignored file before and after each judged
// run, so the cost is proportional to that set — twice per probe, times the
// probe count, times two passes at record. A checkout with a dependency
// directory nobody ignored turns that into a gate that is mysteriously slow,
// and slow is the failure mode nobody reports as a bug. The numbers are the
// point at which the cost stops being noise on ordinary hardware, not a limit:
// nothing refuses to run, doctor just says what is happening.
const (
	snapshotFileBudget = 2000
	snapshotByteBudget = 16 << 20
)

func checkSnapshotCost(root string) []DoctorFinding {
	paths, err := gitUntrackedPaths(root)
	if err != nil {
		// Reported, not swallowed: a git failure listing the untracked tree is
		// doctor unable to size the snapshot, which is a thing to say rather
		// than a clean pass.
		return []DoctorFinding{{
			Check:  "snapshot-cost",
			Detail: "could not list the untracked tree to size the work-tree snapshot: " + err.Error(),
			Remedy: "resolve the git error above, then run vise doctor again",
		}}
	}
	var bytes int64
	counted := 0
	for _, rel := range paths {
		if isViseLocalState(rel) {
			continue
		}
		counted++
		if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil && info.Mode().IsRegular() {
			bytes += info.Size()
		}
	}
	if counted <= snapshotFileBudget && bytes <= snapshotByteBudget {
		return nil
	}
	return []DoctorFinding{{
		Check:  "snapshot-cost",
		Detail: fmt.Sprintf("%d files totalling %d bytes are untracked and unignored; vise hashes all of them before and after every probe run", counted, bytes),
		Remedy: "add the generated ones to .gitignore, or commit the ones that belong in the repository; besides its declared files and $VISE_TMP, an ignored path is where a probe may write without tripping the work-tree check",
	}}
}

// checkTrackedArtifacts: a declared artifact that Git tracks stops the gate
// dead. vise deletes artifacts before every run and refuses to delete a
// tracked file, so the first gate is exit 2 and an agent's correct response is
// to stop and fetch an operator.
//
// The trap is ordinary: `git add -A` after a run, once a probe has produced
// its artifact. Nothing about it looks wrong, the file is real output, and the
// failure arrives later in a message about the manifest. Found when an agent
// handed a repository set up exactly that way refused to start.
func checkTrackedArtifacts(root string, manifest Manifest) []DoctorFinding {
	var declared []string
	for _, probe := range manifest.Probes {
		declared = append(declared, probe.Files...)
	}
	if len(declared) == 0 {
		return nil
	}
	tracked, err := GitTrackedPaths(root, declared)
	if err != nil {
		// A git failure inspecting declared artifacts is not a clean pass: it
		// is doctor unable to answer the question, and reporting ready would
		// tell an operator the repository is fit when it is only uninspected.
		return []DoctorFinding{{
			Check:  "tracked-artifacts",
			Detail: "could not inspect declared artifacts: " + err.Error(),
			Remedy: "resolve the git error above, then run vise doctor again",
		}}
	}
	if len(tracked) == 0 {
		return nil
	}
	sort.Strings(tracked)
	named := tracked
	suffix := ""
	if len(named) > 3 {
		named = named[:3]
		suffix = fmt.Sprintf(" (and %d more)", len(tracked)-3)
	}
	return []DoctorFinding{{
		Check:  "tracked-artifacts",
		Detail: fmt.Sprintf("declared artifacts are tracked by git: %s%s; vise deletes artifacts before every run and refuses to delete a tracked file, so every gate here is a harness error", strings.Join(named, ", "), suffix),
		Remedy: "git rm --cached the artifacts and add them to .gitignore; they are build output, not source",
	}}
}
