package cli

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
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
		vise.KillActiveProbeGroup()
		code := 128
		if number, ok := sig.(syscall.Signal); ok {
			code += int(number)
		}
		os.Exit(code)
	}()
}

func Run(args []string, cwd string, stdout, stderr io.Writer) int {
	args, jsonMode := removeGlobalJSON(args)
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return vise.ExitOK
	}
	if args[0] == "version" || args[0] == "--version" {
		if jsonMode {
			return writeJSON(stdout, map[string]any{"v": 1, "cmd": "version", "exit": 0, "version": Version, "next": vise.Next{Action: "proceed", Detail: "version reported"}})
		}
		fmt.Fprintln(stdout, "vise "+Version)
		return vise.ExitOK
	}
	command := args[0]
	if hasHelp(args[1:]) {
		printCommandHelp(stdout, command)
		return vise.ExitOK
	}

	root, err := vise.GitRoot(cwd)
	if err != nil {
		if command == "status" {
			report := vise.StatusReport{V: 1, Cmd: "status", Exit: 0, State: "no-git", Next: vise.Next{Action: "fix_probe", Detail: err.Error()}}
			if jsonMode {
				return writeJSON(stdout, report)
			}
			renderStatus(stdout, report)
			return vise.ExitOK
		}
		return renderSimpleError(command, err.Error(), jsonMode, stdout, stderr)
	}
	stateLock, err := vise.AcquireStateLock(root)
	if err != nil {
		if command == "status" {
			report := vise.StatusReport{V: 1, Cmd: "status", Exit: 0, State: "harness-error", Next: vise.Next{Action: "fix_probe", Detail: err.Error()}}
			if jsonMode {
				return writeJSON(stdout, report)
			}
			renderStatus(stdout, report)
			return vise.ExitOK
		}
		return renderSimpleError(command, err.Error(), jsonMode, stdout, stderr)
	}
	defer stateLock.Close()

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
	case "status":
		if len(args) != 1 {
			return renderSimpleError("status", "status accepts no positional arguments", jsonMode, stdout, stderr)
		}
		report := vise.BuildStatus(root)
		if jsonMode {
			return writeJSON(stdout, report)
		}
		renderStatus(stdout, report)
		return vise.ExitOK
	default:
		return renderSimpleError("vise", fmt.Sprintf("unknown command %q; run 'vise --help'", command), jsonMode, stdout, stderr)
	}
}

func runInit(args []string, root string, jsonMode bool, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return renderSimpleError("init", "init accepts no arguments", jsonMode, stdout, stderr)
	}
	if err := vise.InitRepository(root); err != nil {
		return renderSimpleError("init", err.Error(), jsonMode, stdout, stderr)
	}
	response := map[string]any{
		"v":       1,
		"cmd":     "init",
		"exit":    0,
		"created": []string{"vise.toml"},
		"next":    vise.Next{Action: "fix_probe", Detail: "uncomment and configure at least one probe, then run vise record"},
	}
	if jsonMode {
		return writeJSON(stdout, response)
	}
	fmt.Fprintln(stdout, "INITIALIZED — wrote vise.toml and wired local state into .gitignore")
	fmt.Fprintln(stdout, "NEXT — declare at least one probe, commit the harness, then run vise record")
	return vise.ExitOK
}

func runRecord(args []string, root string, jsonMode bool, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allowDirty := fs.Bool("allow-dirty", false, "allow recording a dirty work tree")
	reviewed := fs.Bool("i-reviewed-the-diff", false, "accept overwriting the current lockfile")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if err == nil {
			err = fmt.Errorf("record accepts no positional arguments")
		}
		return renderSimpleError("record", err.Error(), jsonMode, stdout, stderr)
	}
	manifest, manifestBytes, err := vise.LoadManifest(root)
	if err != nil {
		return renderSimpleError("record", err.Error(), jsonMode, stdout, stderr)
	}
	opts := vise.RecordOptions{AllowDirty: *allowDirty, ReviewedDiff: *reviewed}
	if *reviewed && !jsonMode {
		opts.BeforeOverwrite = func(diff string) error {
			fmt.Fprintln(stdout, "BEHAVIOR DIFF UNDER REVIEW")
			fmt.Fprintln(stdout, diff)
			return nil
		}
	}
	result := vise.Record(root, manifest, manifestBytes, opts)
	if jsonMode {
		extra := map[string]any{}
		if result.ReviewDiff != "" {
			extra["review_diff"] = result.ReviewDiff
		}
		return writeOutcomeJSON(stdout, result.Outcome, extra)
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
		return renderSimpleError(name, err.Error(), jsonMode, stdout, stderr)
	}
	result := vise.Verify(root, manifest, manifestBytes, vise.VerifyOptions{ProbeID: *probeID, EnforceRerunLimit: true})
	result.Outcome.Cmd = name
	if (gate || len(result.Flaky) > 0) && !result.RerunRefused {
		if result.Commit != "" {
			if err := vise.JournalVerifyResult(root, name, result); err != nil {
				result.Outcome.AddFailure("journal", vise.Failure{Class: "harness", Detail: err.Error()})
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
		return renderSimpleError("run", err.Error(), jsonMode, stdout, stderr)
	}
	probe, ok := manifest.Probe(args[0])
	if !ok {
		return renderSimpleError("run", fmt.Sprintf("unknown probe %q", args[0]), jsonMode, stdout, stderr)
	}
	result := (vise.Runner{Root: root, Manifest: manifest}).RunProbe(probe, false)
	if result.HarnessError != "" {
		return renderSimpleError("run", result.HarnessError, jsonMode, stdout, stderr)
	}
	if jsonMode {
		response := map[string]any{
			"v": 1, "cmd": "run", "exit": result.Exit, "probe": probe.ID,
			"files": hashFiles(result.Files), "next": vise.Next{Action: "proceed", Detail: "raw probe execution finished"},
		}
		addBytes(response, "stdout", result.Stdout)
		addBytes(response, "stderr", result.Stderr)
		return writeJSONWithExit(stdout, response, result.Exit)
	}
	_, _ = stdout.Write(result.Stdout)
	_, _ = stderr.Write(result.Stderr)
	return result.Exit
}

func renderSimpleError(command, detail string, jsonMode bool, stdout, stderr io.Writer) int {
	outcome := vise.NewOutcome(command)
	outcome.Counts.Declared = 1
	outcome.AddFailure(command, vise.Failure{Class: "harness", Detail: detail})
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
	data, _ := json.Marshal(outcome)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	for key, value := range extra {
		object[key] = value
	}
	_ = writeJSON(w, object)
	return outcome.Exit
}

func addBytes(response map[string]any, key string, data []byte) {
	if utf8.Valid(data) {
		response[key] = string(data)
		return
	}
	response[key+"_base64"] = base64.StdEncoding.EncodeToString(data)
}

func hashFiles(files map[string][]byte) map[string]string {
	result := make(map[string]string, len(files))
	for path, data := range files {
		result[path] = vise.HashBytes(data)
	}
	return result
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `vise — deterministic behavior locks for agent-led refactoring

Usage:
  vise <command> [options]

Commands:
  init                 Write a stub vise.toml and local-state ignores
  record               Freeze deterministic behavior into vise.lock
  verify [--probe ID]  Replay and diagnose behavior
  gate [--probe ID]    Emit the bounded refactor-loop verdict
  run <probe-id>       Execute one probe without judgment
  status               Render the complete bounded situation
  version              Print the vise version

Global options:
  --json               Replace human output with one JSON object
  --help               Show help without requiring a Git repository

Run 'vise <command> --help' for command-specific help.
`)
}

func printCommandHelp(w io.Writer, command string) {
	text := map[string]string{
		"init":   "Usage: vise init [--json]\nWrites a commented starter manifest and local-state .gitignore entries.\n",
		"record": "Usage: vise record [--allow-dirty] [--i-reviewed-the-diff] [--json]\nRuns two full suite passes and atomically writes the behavior baseline.\n",
		"verify": "Usage: vise verify [--probe ID] [--json]\nReplays all probes or one judged probe against vise.lock.\n",
		"gate":   "Usage: vise gate [--probe ID] [--quiet] [--json]\nRuns verification and emits the refactor-loop verdict.\n",
		"run":    "Usage: vise run <probe-id> [--json]\nExecutes a probe raw without reading or changing vise.lock.\n",
		"status": "Usage: vise status [--json]\nReports manifest, lock, fingerprint, proposals, and the last five journal events.\n",
	}
	if help, ok := text[command]; ok {
		fmt.Fprint(w, help)
		return
	}
	printHelp(w)
}
