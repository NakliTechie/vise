package cli

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/NakliTechie/vise/internal/vise"
)

const Version = "0.3.0-dev"

func Main(args []string, stdout, stderr io.Writer) int {
	stopProbeOnSignal()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "vise: determine current directory: %v\n", err)
		return vise.ExitHarness
	}
	return Run(args, cwd, stdout, stderr)
}

// stopProbeOnSignal makes SIGINT and SIGTERM kill the running probe's process
// group before vise exits. Probes run in their own group, so the terminal's
// Ctrl-C reaches vise alone; without this the probe would keep running and
// writing declared artifacts after vise was gone.
func stopProbeOnSignal() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-signals
		vise.InterruptProbes()
		code := 128
		if number, ok := sig.(syscall.Signal); ok {
			code += int(number)
		}
		os.Exit(code)
	}()
}

func Run(args []string, cwd string, stdout, stderr io.Writer) int {
	args, jsonMode := removeGlobalJSON(args)
	if exit, answered := answerHelpOrVersion(args, jsonMode, stdout, stderr); answered {
		return exit
	}
	command := args[0]

	// A complaint about the command line does not depend on where you are
	// standing, so the whole invocation is judged before the repository is
	// resolved. It used to be the other way round, and two calls answered the
	// wrong question because of it: `vise status bogus` outside a repository
	// returned a status report and exit 0, because resolveGitRoot
	// short-circuits status and doctor to keep their always-exit-0 promise and
	// the argument check sat behind it in the dispatch; and `vise no-such-cmd`
	// outside a repository complained about the repository rather than the
	// typo. That promise is about reports on a repository. A usage error is
	// exit 2 everywhere, which SPEC states for exactly this reason.
	//
	// An unknown command must also be refused before anything blocks on it.
	// The state lock is held for the length of a record or a gate, so reaching
	// for it first turned a typo into a wait as long as the probe suite —
	// found by a probe that ran `vise no-such-command` inside a `vise record`.
	if !isKnownCommand(command) {
		return refuseUnknownCommand(command, jsonMode, stdout, stderr)
	}
	if takesNoPositionalArguments[command] && len(args) != 1 {
		return renderSimpleError(command, command+" accepts no positional arguments", jsonMode, stdout, stderr)
	}
	root, exit, resolved := resolveGitRoot(command, cwd, jsonMode, stdout, stderr)
	if !resolved {
		return exit
	}
	// status and doctor read the situation and write nothing, so they take no
	// lock and create no state directory: a repository that has never run vise
	// is unchanged by asking what its situation is, and asking is possible
	// while a run is in progress — the moment you most want to ask what is
	// happening. Concurrency is safe without a lock because the lockfile is
	// replaced by atomic rename, so a reader sees the old generation or the
	// new one, and a torn journal tail is already tolerated.
	stateLock, err := acquireStateLockForCommand(command, root, stderr)
	if err != nil {
		return renderSimpleError(command, err.Error(), jsonMode, stdout, stderr)
	}
	if stateLock != nil {
		defer stateLock.Close()
	}

	return dispatchCommand(args, root, jsonMode, stdout, stderr)
}

func refuseUnknownCommand(command string, jsonMode bool, stdout, stderr io.Writer) int {
	return renderSimpleError("vise", fmt.Sprintf("unknown command %q; run 'vise --help'", command), jsonMode, stdout, stderr)
}

// The commands resolveGitRoot answers without a repository. Their arity is
// checked before it runs, because after it runs they have already replied.
var takesNoPositionalArguments = map[string]bool{"status": true, "doctor": true}

func acquireStateLockForCommand(command, root string, notice io.Writer) (*vise.StateLock, error) {
	if readOnlyCommands[command] {
		return nil, nil
	}
	return vise.AcquireStateLock(root, notice)
}

func dispatchCommand(args []string, root string, jsonMode bool, stdout, stderr io.Writer) int {
	command := args[0]
	switch command {
	case "init":
		return runInit(args[1:], root, jsonMode, stdout, stderr)
	case "record":
		return runRecord(args[1:], root, jsonMode, stdout, stderr)
	case "verify":
		return runVerify(args[1:], root, jsonMode, false, stdout, stderr)
	case "gate":
		return runVerify(args[1:], root, jsonMode, true, stdout, stderr)
	case "run":
		return runProbe(args[1:], root, jsonMode, stdout, stderr)
	case "doctor":
		if len(args) != 1 {
			return renderSimpleError("doctor", "doctor accepts no positional arguments", jsonMode, stdout, stderr)
		}
		report := vise.Doctor(root)
		if jsonMode {
			return writeJSON(stdout, report)
		}
		renderDoctor(stdout, report)
		return vise.ExitOK
	case "status":
		if len(args) != 1 {
			return renderSimpleError("status", "status accepts no positional arguments", jsonMode, stdout, stderr)
		}
		report := vise.BuildStatus(root)
		if jsonMode {
			report.Tool = toolIdentity()
			return writeJSON(stdout, report)
		}
		renderStatus(stdout, report)
		return vise.ExitOK
	default:
		// Unreachable: isKnownCommand above rejects anything not in the table.
		return refuseUnknownCommand(command, jsonMode, stdout, stderr)
	}
}

func resolveGitRoot(command, cwd string, jsonMode bool, stdout, stderr io.Writer) (string, int, bool) {
	root, err := vise.GitRoot(cwd)
	if err == nil {
		return root, vise.ExitOK, true
	}
	if command == "status" {
		// Not fix_probe. There is no probe, no manifest, and nothing an agent
		// could repair here: it is standing in the wrong directory, which is
		// the one thing in the vocabulary only a human can put right. doctor
		// four lines down already answered this case with human.
		report := vise.StatusReport{V: 1, Cmd: "status", Exit: 0, State: "no-git", Next: vise.Next{Action: vise.NextHuman, Detail: err.Error() + "; run vise inside the repository you intend to gate"}}
		if jsonMode {
			report.Tool = toolIdentity()
			return "", writeJSON(stdout, report), false
		}
		renderStatus(stdout, report)
		return "", vise.ExitOK, false
	}
	// doctor says of itself that it always exits 0, so a directory that is not
	// a Git work tree has to be a finding rather than a failure. It is also the
	// most likely first thing anyone types after reading about the command, and
	// answering a question about readiness with a harness error is a poor way
	// to say "you are not in a repository".
	if command == "doctor" {
		report := vise.DoctorReport{V: 1, Cmd: "doctor", Findings: []vise.DoctorFinding{{
			Check:  "git-work-tree",
			Detail: err.Error(),
			Remedy: "run doctor inside the repository you intend to gate",
		}}, Next: vise.Next{Action: vise.NextHuman, Detail: "1 finding(s) an operator should resolve before an agent works here"}}
		if jsonMode {
			return "", writeJSON(stdout, report), false
		}
		renderDoctor(stdout, report)
		return "", vise.ExitOK, false
	}
	return "", renderSimpleError(command, err.Error(), jsonMode, stdout, stderr), false
}

func answerHelpOrVersion(args []string, jsonMode bool, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if jsonMode {
			return writeJSON(stdout, helpDocument("")), true
		}
		printHelp(stdout)
		return vise.ExitOK, true
	}
	if args[0] == "version" || args[0] == "--version" {
		if len(args) != 1 {
			return renderSimpleError("version", "version accepts no arguments", jsonMode, stdout, stderr), true
		}
		if jsonMode {
			// One source for the identity, so version and status cannot
			// disagree about the same binary. They did: status always carried
			// a tool object and version dropped revision and modified
			// entirely on a build with no VCS stamps, while the contract asks
			// an agent to report which tool answered it.
			response := map[string]any{"v": 1, "cmd": "version", "exit": 0, "version": Version, "next": vise.Next{Action: vise.NextProceed, Detail: "version reported"}}
			tool := toolIdentity()
			if tool.Revision != "" {
				response["revision"] = tool.Revision
			}
			if tool.Modified != nil {
				response["modified"] = *tool.Modified
			}
			if built, ok := buildIdentity()["built"].(string); ok {
				response["built"] = built
			}
			return writeJSON(stdout, response), true
		}
		fmt.Fprintln(stdout, "vise "+Version)
		return vise.ExitOK, true
	}
	command := args[0]
	if hasHelp(args[1:]) {
		// `vise recrod --help` used to print the top-level help and exit 0,
		// which answers a typo with a page that never mentions the typo. The
		// refusal has to come first, and it still needs no repository: help is
		// the one thing that must work anywhere, including about a word that
		// is not a command.
		if !isKnownCommand(command) {
			return refuseUnknownCommand(command, jsonMode, stdout, stderr), true
		}
		if jsonMode {
			return writeJSON(stdout, helpDocument(command)), true
		}
		printCommandHelp(stdout, command)
		return vise.ExitOK, true
	}
	return vise.ExitOK, false
}

// readOnlyCommands take no state lock. Each one reports a situation without
// changing it, which is only true if it also creates nothing — see the tests
// that assert a repository which has never run vise is unchanged by asking.
var readOnlyCommands = map[string]bool{"status": true, "doctor": true}

func isKnownCommand(name string) bool {
	_, ok := commandUsageFor(name)
	return ok
}

func runInit(args []string, root string, jsonMode bool, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return renderSimpleError("init", "init accepts no arguments", jsonMode, stdout, stderr)
	}
	created, err := vise.InitRepository(root)
	if err != nil {
		return renderSimpleError("init", err.Error(), jsonMode, stdout, stderr)
	}
	response := map[string]any{
		"v":       1,
		"cmd":     "init",
		"exit":    0,
		"created": created,
		"next":    vise.Next{Action: vise.NextHuman, Detail: "uncomment and configure at least one probe, then run vise record"},
	}
	if jsonMode {
		return writeJSON(stdout, response)
	}
	if len(created) == 0 {
		fmt.Fprintln(stdout, "INITIALIZED — nothing to write; vise.toml, AGENTS.md and the .gitignore entries are all present")
	} else {
		fmt.Fprintln(stdout, "INITIALIZED — wrote "+strings.Join(created, ", ")+" and wired local state into .gitignore")
	}
	fmt.Fprintln(stdout, "NEXT — declare at least one probe, commit the harness, then run vise record")
	return vise.ExitOK
}

type recordFlags struct {
	allowDirty bool
	reviewed   bool
	preview    bool
	accept     string
	acceptSet  bool
}

func parseRecordFlags(args []string) (recordFlags, error) {
	var flags recordFlags
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&flags.allowDirty, "allow-dirty", false, "allow recording a dirty work tree")
	fs.BoolVar(&flags.reviewed, "i-reviewed-the-diff", false, "accept overwriting the current lockfile")
	fs.BoolVar(&flags.preview, "preview", false, "run the passes and show the candidate diff and digest without writing")
	fs.StringVar(&flags.accept, "accept", "", "write the candidate only if its digest equals this value")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if err == nil {
			err = fmt.Errorf("record accepts no positional arguments")
		}
		return recordFlags{}, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "accept" {
			flags.acceptSet = true
		}
	})
	return flags, nil
}

func validateRecordFlags(flags recordFlags) error {
	if flags.preview && flags.accept != "" {
		return fmt.Errorf("--preview and --accept are mutually exclusive")
	}
	if flags.acceptSet && flags.accept == "" {
		return fmt.Errorf("--accept needs the candidate digest printed by --preview")
	}
	return nil
}

func chooseRecordRoute(flags recordFlags) vise.RecordOptions {
	opts := vise.RecordOptions{AllowDirty: flags.allowDirty, ReviewedDiff: flags.reviewed}
	switch {
	case flags.preview:
		opts.Preview = true
	case flags.accept != "":
		opts.Accept = flags.accept
	}
	return opts
}

func wireRecordConfirmation(opts *vise.RecordOptions, jsonMode bool, stdout io.Writer) {
	if !opts.ReviewedDiff || jsonMode {
		return
	}
	opts.BeforeOverwrite = func(diff string) error {
		fmt.Fprintln(stdout, "BEHAVIOR DIFF UNDER REVIEW")
		fmt.Fprintln(stdout, terminalSafe(diff, true))
		return nil
	}
}

func runRecord(args []string, root string, jsonMode bool, stdout, stderr io.Writer) int {
	flags, err := parseRecordFlags(args)
	if err != nil {
		return renderSimpleError("record", err.Error(), jsonMode, stdout, stderr)
	}
	manifest, manifestBytes, err := vise.LoadManifest(root)
	if err != nil {
		return renderOperatorError("record", err.Error(), jsonMode, stdout, stderr)
	}
	if err := validateRecordFlags(flags); err != nil {
		return renderSimpleError("record", err.Error(), jsonMode, stdout, stderr)
	}
	opts := chooseRecordRoute(flags)
	wireRecordConfirmation(&opts, jsonMode, stdout)
	result := vise.Record(root, manifest, manifestBytes, opts)
	return renderRecordResult(result, manifest, flags.preview, jsonMode, stdout, stderr)
}

func renderRecordResult(result vise.RecordResult, manifest vise.Manifest, preview, jsonMode bool, stdout, stderr io.Writer) int {
	if jsonMode {
		extra := map[string]any{}
		if result.ReviewDiff != "" {
			extra["review_diff"] = result.ReviewDiff
		}
		if result.Candidate != "" {
			extra["candidate"] = result.Candidate
		}
		return writeOutcomeJSON(stdout, result.Outcome, extra)
	}
	if preview && result.Outcome.Exit == vise.ExitOK {
		fmt.Fprintln(stdout, "CANDIDATE BASELINE — no baseline state written (probes ran; declared artifacts were regenerated)")
		if result.ReviewDiff != "" {
			fmt.Fprintln(stdout, terminalSafe(result.ReviewDiff, true))
		} else {
			fmt.Fprintln(stdout, "No lockfile yet; the candidate is a fresh baseline.")
		}
		fmt.Fprintln(stdout, "candidate: "+result.Candidate)
		fmt.Fprintf(stdout, "next: %s — %s\n", result.Outcome.Next.Action, terminalSafe(result.Outcome.Next.Detail, false))
		return vise.ExitOK
	}
	if result.Outcome.Exit == vise.ExitOK {
		fmt.Fprintf(stdout, "RECORDED — %d probe(s) · %d metric(s)\n", len(manifest.Probes), len(manifest.Metrics))
		fmt.Fprintln(stdout, "lock: "+result.Outcome.Lock)
		return vise.ExitOK
	}
	renderOutcome(stderr, result.Outcome, "RECORD")
	return result.Outcome.Exit
}

func runVerify(args []string, root string, jsonMode, gate bool, stdout, stderr io.Writer) int {
	name := "verify"
	if gate {
		name = "gate"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	probeID := fs.String("probe", "", "verify one probe")
	quiet := fs.Bool("quiet", false, "print only the gate verdict")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if err == nil {
			err = fmt.Errorf("%s accepts no positional arguments", name)
		}
		return renderSimpleError(name, err.Error(), jsonMode, stdout, stderr)
	}
	if !gate && *quiet {
		return renderSimpleError(name, "--quiet is available only for gate", jsonMode, stdout, stderr)
	}
	manifest, manifestBytes, err := vise.LoadManifest(root)
	if err != nil {
		return renderOperatorError(name, err.Error(), jsonMode, stdout, stderr)
	}
	result := vise.Verify(root, manifest, manifestBytes, vise.VerifyOptions{ProbeID: *probeID, EnforceRerunLimit: true})
	result.Outcome.Cmd = name
	// Every judged run is journaled, verify and gate alike, so a green verify
	// ends a flake chain the same way a green gate does. A refusal is not a
	// judgment and is never journaled, and neither is an outcome that stopped
	// before the tamper hash (it carries no lock and judged nothing).
	if !result.RerunRefused && result.Outcome.Lock != "" {
		if result.Commit != "" {
			if err := vise.JournalVerifyResult(root, name, result); err != nil {
				// The journal is on the protected surface: the rerun budget is
				// derived from it, so an agent may not write it. Without the
				// flag this said fix_probe, which is an instruction the agent
				// contract forbids it from following.
				result.Outcome.AddFailure("journal", vise.Failure{Class: "harness", Detail: err.Error(), Operator: true})
				result.Outcome.Finalize()
				result.Outcome.Cmd = name
			}
		}
	}
	if jsonMode {
		return writeOutcomeJSON(stdout, result.Outcome, nil)
	}
	if gate {
		renderGate(stdout, result.Outcome, *quiet)
		return result.Outcome.Exit
	}
	writer := stdout
	if result.Outcome.Exit != vise.ExitOK {
		writer = stderr
	}
	renderOutcome(writer, result.Outcome, "VERIFY")
	return result.Outcome.Exit
}

func runProbe(args []string, root string, jsonMode bool, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return renderSimpleError("run", "usage: vise run <probe-id>", jsonMode, stdout, stderr)
	}
	manifest, _, err := vise.LoadManifest(root)
	if err != nil {
		return renderOperatorError("run", err.Error(), jsonMode, stdout, stderr)
	}
	probe, ok := manifest.Probe(args[0])
	if !ok {
		return renderSimpleError("run", fmt.Sprintf("unknown probe %q; %s", args[0], vise.DeclaredProbeList(manifest)), jsonMode, stdout, stderr)
	}
	runner := vise.Runner{Root: root, Manifest: manifest}
	if !jsonMode {
		// Raw execution: the probe's complete output streams to the terminal
		// as it is produced, while vise keeps only a bounded capture.
		runner.MirrorStdout = stdout
		runner.MirrorStderr = stderr
	}
	// The work-tree check applies here too, which SPEC, the README and the
	// agent contract all say and the code did not do. `run` passed false and
	// skipped the before/after snapshot entirely, so a probe that dropped a
	// stray file exited 0 with the stray left in the checkout, while the same
	// probe under record or verify was refused. An agent debugging a probe
	// with `run` got a clean answer and then a contradicting gate.
	//
	// It also made the one command that does not judge the probe the only one
	// that would let it dirty the tree — the opposite of the right way round,
	// since `run` is what somebody reaches for when a probe is misbehaving.
	result := runner.RunProbe(probe, true)
	// run mirrors the probe's own exit. A launch failure is the probe's exit 127 and
	// passes through; a timeout, a refused artifact, or a lingering pipe holder
	// has no probe exit to mirror and stays a harness error.
	if result.HarnessError != "" && !(result.Exit == 127 && !result.TimedOut) {
		return renderSimpleError("run", result.HarnessError, jsonMode, stdout, stderr)
	}
	if jsonMode {
		response := map[string]any{
			"v": 1, "cmd": "run", "exit": result.Exit, "probe": probe.ID,
			"files": hashFiles(result.Files), "next": vise.Next{Action: vise.NextProceed, Detail: "raw probe execution finished"},
		}
		addCapture(response, "stdout", result.Stdout)
		addCapture(response, "stderr", result.Stderr)
		return writeJSONWithExit(stdout, response, result.Exit)
	}
	return result.Exit
}

func renderSimpleError(command, detail string, jsonMode bool, stdout, stderr io.Writer) int {
	return renderFailure(command, vise.Failure{Class: "harness", Detail: detail}, jsonMode, stdout, stderr)
}

// renderOperatorError is renderSimpleError for a failure whose repair lives in
// a file the agent contract forbids an agent from writing. A malformed
// vise.toml or vise.lock is the common case, and it reached the agent as
// fix_probe — "repair the harness" — against a rule that forbids touching it,
// leaving no legal move. The Operator flag existed and this path did not set
// it, which is the same conflict fixed in two places out of three.
func renderOperatorError(command, detail string, jsonMode bool, stdout, stderr io.Writer) int {
	return renderFailure(command, vise.Failure{Class: "harness", Detail: detail, Operator: true}, jsonMode, stdout, stderr)
}

func renderFailure(command string, failure vise.Failure, jsonMode bool, stdout, stderr io.Writer) int {
	outcome := vise.NewOutcome(command)
	outcome.Counts.Declared = 1
	outcome.AddFailure(command, failure)
	outcome.Finalize()
	if jsonMode {
		return writeOutcomeJSON(stdout, outcome, nil)
	}
	renderOutcome(stderr, outcome, strings.ToUpper(command))
	return outcome.Exit
}

func removeGlobalJSON(args []string) ([]string, bool) {
	clean := make([]string, 0, len(args))
	jsonMode := false
	for _, arg := range args {
		if arg == "--json" {
			jsonMode = true
			continue
		}
		clean = append(clean, arg)
	}
	return clean, jsonMode
}

func hasHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func writeJSON(w io.Writer, value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(w, `{"v":1,"cmd":"internal","exit":2,"next":{"action":"fix_probe","detail":%q}}`, err.Error())
		fmt.Fprintln(w)
		return vise.ExitHarness
	}
	_, _ = w.Write(append(data, '\n'))
	return vise.ExitOK
}

func writeJSONWithExit(w io.Writer, value any, exit int) int {
	_ = writeJSON(w, value)
	return exit
}

func writeOutcomeJSON(w io.Writer, outcome vise.Outcome, extra map[string]any) int {
	// A verdict that cannot be serialized must not be reported as the verdict.
	// A non-finite metric delta is the reachable case: json.Marshal refuses it,
	// and silently dropping the error would print an incomplete object under a
	// green exit code — the one failure an agent has no way to notice.
	data, err := json.Marshal(outcome)
	if err != nil {
		_ = writeJSON(w, encodingFailure(outcome.Cmd, err))
		return vise.ExitHarness
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		_ = writeJSON(w, encodingFailure(outcome.Cmd, err))
		return vise.ExitHarness
	}
	for key, value := range extra {
		object[key] = value
	}
	if exit := writeJSON(w, object); exit != vise.ExitOK {
		return exit
	}
	return outcome.Exit
}

// encodingFailure is the outcome vise reports when it cannot report the real
// one: harness class, so the caller repairs rather than trusting a verdict.
func encodingFailure(command string, err error) map[string]any {
	return map[string]any{
		"v": 1, "cmd": command, "exit": vise.ExitHarness, "verdict": "indeterminate",
		"classes": []string{"harness"},
		"failures": map[string]any{
			"encoding": map[string]any{"class": "harness", "detail": "the verdict could not be encoded as JSON: " + err.Error()},
		},
		"next": vise.Next{Action: vise.NextFixProbe, Detail: "a value in the outcome cannot be represented in JSON; a metric that printed a non-finite number is the usual cause"},
	}
}

// addCapture reports an observation in JSON. Output larger than the capture
// bound is reported by its prefix, hash, and size rather than in full.
// addCapture renders one observation. The hash, the size, and the truncation
// flag are always present, never only when truncated: a field that appears
// some of the time is a field every consumer has to handle two ways, and the
// whole point of this object is that a machine can read it without branching
// on what happens to be there. The hash is also what makes `run` useful next
// to a lockfile — it can be compared without recomputing anything.
func addCapture(response map[string]any, key string, capture vise.Capture) {
	addBytes(response, key, capture.Prefix)
	response[key+"_truncated"] = capture.Truncated()
	response[key+"_size"] = capture.Size
	response[key+"_hash"] = capture.Hash
}

func addBytes(response map[string]any, key string, data []byte) {
	if utf8.Valid(data) {
		response[key] = string(data)
		return
	}
	response[key+"_base64"] = base64.StdEncoding.EncodeToString(data)
}

func hashFiles(files map[string]vise.Capture) map[string]string {
	result := make(map[string]string, len(files))
	for path, capture := range files {
		result[path] = capture.Hash
	}
	return result
}

// buildIdentity reports which build of vise this is. Two binaries built from
// different commits carry the same Version string, which is exactly the
// confusion that sends an agent hunting a phantom bug when the vise on its PATH
// is older than the lockfile in front of it.
func buildIdentity() map[string]any {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	identity := map[string]any{}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			identity["revision"] = setting.Value
		case "vcs.time":
			identity["built"] = setting.Value
		case "vcs.modified":
			identity["modified"] = setting.Value == "true"
		}
	}
	if len(identity) == 0 {
		return nil
	}
	return identity
}

// toolIdentity is the build identity in the shape status reports it.
func toolIdentity() *vise.StatusTool {
	tool := &vise.StatusTool{Version: Version}
	identity := buildIdentity()
	if revision, ok := identity["revision"].(string); ok {
		tool.Revision = revision
	}
	if modified, ok := identity["modified"].(bool); ok {
		tool.Modified = &modified
	}
	return tool
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, "vise — deterministic behavior locks for agent-led refactoring\n\nUsage:\n  vise <command> [options]\n\nCommands:\n")
	for _, entry := range commands {
		fmt.Fprintf(w, "  %-20s %s\n", entry.Invocation, entry.Summary)
	}
	fmt.Fprint(w, `
Global options:
  --json               Replace human output with one JSON object
  --help               Show help without requiring a Git repository

Run 'vise <command> --help' for command-specific help.
`)
}

// commands is the one source for every rendering of help: the top-level list,
// the per-command usage, and the JSON document. The top-level list used to be
// a separate hardcoded string, which is how a command surface and its
// documentation drift apart one addition at a time.
var commands = []struct {
	Name       string
	Invocation string
	Summary    string
	Usage      string
}{
	{"init", "init", "Write a stub vise.toml and local-state ignores",
		"Usage: vise init [--json]\nWrites a commented starter manifest, the agent contract, and local-state .gitignore entries.\n"},
	{"record", "record", "Freeze deterministic behavior into vise.lock",
		"Usage: vise record [--allow-dirty] [--i-reviewed-the-diff] [--preview | --accept DIGEST] [--json]\nRuns two full suite passes and atomically writes the behavior baseline.\n--preview shows the candidate diff and digest without writing; --accept writes only that candidate.\n"},
	{"verify", "verify [--probe ID]", "Replay and diagnose behavior",
		"Usage: vise verify [--probe ID] [--json]\nReplays all probes or one judged probe against vise.lock.\n"},
	{"gate", "gate [--probe ID]", "Emit the bounded refactor-loop verdict",
		"Usage: vise gate [--probe ID] [--quiet] [--json]\nRuns verification and emits the refactor-loop verdict.\n"},
	{"run", "run <probe-id>", "Run one probe and report it, without judging it",
		"Usage: vise run <probe-id> [--json]\nRuns one probe and reports what it observed, without comparing it to vise.lock.\nThe probe still runs through the full lifecycle: declared artifacts are deleted\nfirst, the evaluator-state and work-tree checks still apply, and output streams\nto your terminal in full.\n"},
	{"status", "status", "Render the complete bounded situation",
		"Usage: vise status [--json]\nReports manifest, lock, fingerprint, proposals, and the last five journal events.\n"},
	{"doctor", "doctor", "Check the repository is fit to hand to an agent",
		"Usage: vise doctor [--json]\nReports what an operator should fix before an agent works here: an unfingerprinted toolchain, a probe that names a path outside the checkout, a script a probe runs without declaring, an uncommitted baseline, unignored local state, a missing agent contract.\nRuns no probe, writes nothing, and always exits 0.\n"},
	{"version", "version", "Print the vise version",
		"Usage: vise version [--json]\nPrints the version, and with --json the build revision and whether the tree was modified.\n"},
}

func commandUsageFor(name string) (string, bool) {
	for _, entry := range commands {
		if entry.Name == name {
			return entry.Usage, true
		}
	}
	return "", false
}

func printCommandHelp(w io.Writer, command string) {
	if help, ok := commandUsageFor(command); ok {
		fmt.Fprint(w, help)
		return
	}
	printHelp(w)
}

// helpDocument answers `--json` for help. Every command answers --json, and
// help is not the exception the README would otherwise have to apologise for.
func helpDocument(command string) map[string]any {
	document := map[string]any{
		"v":    1,
		"cmd":  "help",
		"exit": 0,
		"next": vise.Next{Action: vise.NextProceed, Detail: "help reported"},
	}
	if usage, ok := commandUsageFor(command); ok {
		document["command"] = command
		document["usage"] = strings.TrimSpace(usage)
		return document
	}
	listed := make(map[string]string, len(commands))
	for _, entry := range commands {
		listed[entry.Name] = strings.TrimSpace(entry.Usage)
	}
	document["commands"] = listed
	document["global_options"] = map[string]string{
		"--json": "Replace human output with one JSON object",
		"--help": "Show help without requiring a Git repository",
	}
	return document
}
