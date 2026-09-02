package vise

import (
	"bytes"
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
		report.finish()
		return report
	}

	report.Findings = append(report.Findings, checkFingerprint(manifest)...)
	report.Findings = append(report.Findings, checkPortablePaths(manifest)...)
	report.Findings = append(report.Findings, checkUndeclaredScripts(root, manifest)...)
	report.Findings = append(report.Findings, checkBaselineCommitted(root)...)
	report.Findings = append(report.Findings, checkLocalStateIgnored(root)...)
	report.Findings = append(report.Findings, checkAgentContract(root)...)
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
	if len(manifest.Environment.Fingerprint) > 0 {
		return nil
	}
	return []DoctorFinding{{
		Check:  "env-fingerprint",
		Detail: "the manifest declares no [env] fingerprint, so a toolchain change is invisible to the gate",
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
func checkPortablePaths(manifest Manifest) []DoctorFinding {
	unportable := func(value string) bool {
		if strings.HasPrefix(value, "/bin/") || strings.HasPrefix(value, "/usr/bin/") {
			return false
		}
		return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") || strings.Contains(value, "$HOME")
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

// checkBaselineCommitted: a baseline that lives only on the operator's disk
// cannot judge anything in a fresh clone, which is where an agent and CI both
// work.
func checkBaselineCommitted(root string) []DoctorFinding {
	if _, err := os.Stat(filepath.Join(root, "vise.lock")); err != nil {
		return []DoctorFinding{{
			Check:  "baseline-committed",
			Detail: "no vise.lock exists, so there is nothing to gate against",
			Remedy: "run vise record, then commit vise.lock and .vise/blobs/",
		}}
	}
	tracked, err := GitTrackedPaths(root, []string{"vise.lock"})
	if err != nil {
		return []DoctorFinding{{Check: "baseline-committed", Detail: err.Error(), Remedy: "run doctor inside a Git work tree"}}
	}
	var findings []DoctorFinding
	if len(tracked) == 0 {
		findings = append(findings, DoctorFinding{
			Check:  "baseline-committed",
			Detail: "vise.lock is not committed, so a fresh clone has no baseline",
			Remedy: "git add vise.lock && git commit",
		})
	}
	entries, err := os.ReadDir(filepath.Join(root, ".vise", "blobs"))
	if err != nil || len(entries) == 0 {
		return findings
	}
	blobs, err := GitTrackedPaths(root, []string{".vise/blobs"})
	if err == nil && len(blobs) == 0 {
		findings = append(findings, DoctorFinding{
			Check:  "baseline-committed",
			Detail: "the recorded blobs are not committed, so a fresh clone cannot render a divergence",
			Remedy: "git add .vise/blobs && git commit",
		})
	}
	return findings
}

// checkLocalStateIgnored: the journal, the run lock, and the probe scratch are
// per-checkout state. Committing them makes every gate dirty the tree, which
// an agent then reports as a change it did not make.
func checkLocalStateIgnored(root string) []DoctorFinding {
	var missing []string
	for _, path := range []string{".vise/journal.jsonl", ".vise/run.lock", ".vise/tmp/"} {
		cmd := exec.Command("git", "check-ignore", "-q", "--no-index", path)
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			missing = append(missing, path)
		}
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
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err == nil {
		return nil
	}
	return []DoctorFinding{{
		Check:  "agent-contract",
		Detail: "no AGENTS.md at the repository root, so an agent working here has no written rules",
		Remedy: "run vise init, which writes the agent contract without overwriting an existing one",
	}}
}
