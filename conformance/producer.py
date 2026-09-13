#!/usr/bin/env python3
"""Bounded independent conformance fixtures for a Vise JSON producer."""

from __future__ import annotations

import argparse
import base64
import copy
import datetime as dt
import hashlib
import json
import math
import os
import platform
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any, Callable

KIT_VERSION = "1"
SCHEMA_VERSION = 1
TIMEOUT = 10
MAX_STREAM = 1024 * 1024
ACTIONS = {"proceed", "revert", "fix_probe", "human", "record_first",
           "quarantine_ack", "fix_invocation", "build"}
FIXED_ENV = {
    "GIT_AUTHOR_NAME": "Vise Conformance",
    "GIT_AUTHOR_EMAIL": "conformance@example.invalid",
    "GIT_COMMITTER_NAME": "Vise Conformance",
    "GIT_COMMITTER_EMAIL": "conformance@example.invalid",
    "GIT_AUTHOR_DATE": "2001-01-01T00:00:00Z",
    "GIT_COMMITTER_DATE": "2001-01-01T00:00:00Z",
    "TZ": "UTC", "LANG": "C", "LC_ALL": "C",
}
ACTIVE_EVENTS: list[dict[str, Any]] | None = None


def execution_env(home: Path) -> dict[str, str]:
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"),
           "HOME": str(home), "GIT_CONFIG_NOSYSTEM": "1",
           "GIT_CONFIG_GLOBAL": "/dev/null"}
    env.update(FIXED_ENV)
    return env


class CaseError(Exception):
    def __init__(self, message: str, checks: list[dict[str, Any]] | None = None):
        super().__init__(message)
        self.checks = checks or []


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(65536), b""):
            digest.update(block)
    return "sha256:" + digest.hexdigest()


def transport(raw: bytes, process_exit: int) -> tuple[Any, list[dict[str, Any]]]:
    checks: list[dict[str, Any]] = []

    def ck(name: str, condition: bool, detail: str = "") -> None:
        checks.append({"name": name, "pass": bool(condition), "detail": detail})
        if not condition:
            raise CaseError(f"transport: {name}: {detail}", checks)

    def strings(node: Any) -> bool:
        return isinstance(node, list) and all(isinstance(v, str) for v in node)

    def natural(node: Any) -> bool:
        return type(node) is int and node >= 0

    def digest(node: Any) -> bool:
        return isinstance(node, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", node) is not None

    def hashes(node: Any) -> bool:
        return isinstance(node, dict) and all(digest(v) for v in node.values())

    def identity(node: Any) -> bool:
        return isinstance(node, dict) and isinstance(node.get("version"), str) and all(
            k not in node or isinstance(node[k], t)
            for k, t in (("revision", str), ("modified", bool), ("built", str)))

    ck("stdout-ends-lf", raw.endswith(b"\n"), "normal JSON replies end in LF")
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        ck("stdout-utf8", False, str(exc))
        raise AssertionError("unreachable")
    ck("stdout-utf8", True)
    def unique_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in pairs:
            if key in result:
                raise ValueError(f"duplicate key: {key}")
            result[key] = value
        return result

    def reject_constant(value: str) -> None:
        raise ValueError(f"non-finite JSON number: {value}")

    decoder = json.JSONDecoder(object_pairs_hook=unique_object, parse_constant=reject_constant)
    try:
        value, end = decoder.raw_decode(text)
    except (json.JSONDecodeError, ValueError) as exc:
        ck("one-json-object", False, str(exc))
        raise AssertionError("unreachable")
    ck("one-json-object", isinstance(value, dict), "top-level JSON must be an object")
    ck("no-extra-frame", text[end:] == "\n", repr(text[end:]))
    def finite_tree(node: Any) -> bool:
        if isinstance(node, float):
            return math.isfinite(node)
        if isinstance(node, dict):
            return all(finite_tree(v) for v in node.values())
        if isinstance(node, list):
            return all(finite_tree(v) for v in node)
        return True
    ck("finite-numbers", finite_tree(value))
    ck("v", type(value.get("v")) is int and value["v"] == 1)
    ck("cmd", isinstance(value.get("cmd"), str))
    ck("exit", type(value.get("exit")) is int)
    next_value = value.get("next")
    ck("next-shape", isinstance(next_value, dict) and
       isinstance(next_value.get("action"), str) and
       isinstance(next_value.get("detail"), str))
    ck("known-action", next_value["action"] in ACTIONS, str(next_value["action"]))
    ck("process-exit-equality", value["exit"] == process_exit,
       f"json={value['exit']} process={process_exit}")
    cmd = value["cmd"]
    # Error routes use Outcome even for commands whose successful reply is a report.
    is_outcome = "verdict" in value or cmd in {"gate", "verify", "record"}
    if is_outcome:
        action_by_exit = {0: {"proceed"}, 1: {"revert"},
                          2: {"fix_probe", "human", "fix_invocation"},
                          3: {"quarantine_ack"}, 4: {"record_first"},
                          5: {"revert"}, 6: {"build"}}
        if cmd in {"gate", "verify"}:
            ck("judgment-exit-range", value["exit"] in action_by_exit)
            ck("judgment-exit-action", next_value["action"] in action_by_exit[value["exit"]])
            ck("judgment-exit-verdict", value.get("verdict") == {
                0: "green", 1: "red", 2: "indeterminate", 3: "indeterminate",
                4: "indeterminate", 5: "red", 6: "red"}[value["exit"]])
        ck("verdict-enum", isinstance(value.get("verdict"), str) and value["verdict"] in {"green", "red", "indeterminate"})
        counts = value.get("counts")
        count_keys = {"declared", "pass", "behavior", "flaky", "harness", "metric", "unmet", "skipped"}
        ck("counts-shape", isinstance(counts, dict) and count_keys <= counts.keys() and
           all(natural(counts[k]) for k in count_keys))
    if "classes" in value:
        ck("classes-shape", isinstance(value["classes"], list) and
           all(isinstance(v, str) and v in {"harness", "flake", "behavior", "unmet", "metric"} for v in value["classes"]))
    if "failures" in value:
        failures = value["failures"]
        ck("failures-shape", isinstance(failures, dict) and bool(failures))
        for failure in failures.values():
            ck("failure-entry", isinstance(failure, dict) and
               isinstance(failure.get("class"), str) and
               failure["class"] in {"harness", "flake", "behavior", "unmet", "metric"} and
               all(key not in failure or isinstance(failure[key], bool) for key in ("operator", "usage")))
            ck("failure-display", all(k not in failure or isinstance(failure[k], str) for k in ("detail", "diff")))
            for side in ("expect", "got"):
                if side in failure:
                    observation = failure[side]
                    ck("failure-" + side, isinstance(observation, dict) and
                       ("exit" not in observation or type(observation["exit"]) is int) and
                       all(k not in observation or digest(observation[k]) for k in ("stdout", "stderr")) and
                       ("files" not in observation or hashes(observation["files"])))
    if cmd in {"gate", "verify"} and value.get("verdict") == "green":
        ck("no-false-green", "failures" not in value and "classes" not in value)
        ck("green-counts-consistent", counts["pass"] == counts["declared"] and
           all(counts[k] == 0 for k in count_keys - {"pass", "declared"}))
    if "metrics" in value:
        metrics = value["metrics"]
        ck("metrics-shape", isinstance(metrics, dict))
        for metric in metrics.values():
            ck("metric-entry", isinstance(metric, dict) and
               all(type(metric.get(k)) in {int, float} and not isinstance(metric.get(k), bool) and math.isfinite(metric[k]) for k in ("base", "now", "delta")) and
               isinstance(metric.get("direction"), str) and metric["direction"] in {"up", "down"} and
               isinstance(metric.get("enforce"), str) and metric["enforce"] in {"none", "no-regress"})
    if "pins" in value:
        pins = value["pins"]
        if cmd == "record":
            ck("record-pins-shape", isinstance(pins, dict) and {"accepted", "unmet"} <= pins.keys() and
               all(strings(pins[k]) for k in ("accepted", "unmet")) and
               ("passing_unaccepted" not in pins or strings(pins["passing_unaccepted"])))
        else:
            keys = {"evaluated", "unmet", "unmet_count", "passing_unaccepted", "passing_unaccepted_count"}
            ck("judgment-pins-shape", isinstance(pins, dict) and keys <= pins.keys() and
               type(pins["evaluated"]) is int and pins["evaluated"] >= 0 and
               all(type(pins[k]) is int and pins[k] >= 0 for k in ("unmet_count", "passing_unaccepted_count")) and
               all(isinstance(pins[k], list) and len(pins[k]) <= 3 and all(isinstance(v, str) for v in pins[k]) for k in ("unmet", "passing_unaccepted")))
    if is_outcome:
        for key in ("lock", "candidate"):
            ck(key + "-hash", key not in value or digest(value[key]))
        ck("review-diff", "review_diff" not in value or isinstance(value["review_diff"], str))
        return value, checks
    if cmd != "run":
        ck("report-exit", value["exit"] == 0)
    else:
        ck("raw-run-action", next_value["action"] == "proceed")
    if cmd == "version":
        ck("version-shape", identity(value))
    elif cmd == "help":
        if "command" in value:
            ck("command-help-shape", isinstance(value["command"], str) and isinstance(value.get("usage"), str))
        else:
            ck("help-shape", isinstance(value.get("commands"), dict) and isinstance(value.get("global_options"), dict) and
               all(isinstance(v, dict) and all(isinstance(v.get(k), str) for k in ("summary", "usage")) for v in value["commands"].values()) and
               all(isinstance(v, str) for v in value["global_options"].values()))
    elif cmd == "init":
        ck("init-shape", "created" in value and (value["created"] is None or strings(value["created"])))
    elif cmd == "status":
        ck("status-shape", isinstance(value.get("state"), str) and value["state"] in {
           "no-git", "not-initialized", "unrecorded", "ready", "harness-error",
           "environment-drift", "baseline-drift", "rerun-refused"} and natural(value.get("pending_proposals")) and
           all(isinstance(value.get(k), dict) for k in ("manifest", "lock", "tool")) and
           all(natural(value["manifest"].get(k)) for k in ("probes", "metrics")) and
           all(isinstance(value["manifest"].get(k), bool) for k in ("present", "valid")) and
           all(natural(value["lock"].get(k)) for k in ("probes", "metrics")) and
           all(isinstance(value["lock"].get(k), bool) for k in ("present", "valid")) and
           identity(value["tool"]))
        for section in ("manifest", "lock"):
            ck(section + "-error", "error" not in value[section] or isinstance(value[section]["error"], str))
        lock = value["lock"]
        ck("status-lock-optional", ("hash" not in lock or digest(lock["hash"])) and
           ("fingerprint_match" not in lock or isinstance(lock["fingerprint_match"], bool)) and
           all(k not in lock or strings(lock[k]) for k in ("recorded_commits", "drift")))
        if "pins" in lock:
            pins = lock["pins"]
            ck("status-pins", isinstance(pins, dict) and
               all(natural(pins.get(k)) for k in ("declared", "accepted", "unaccepted_count")) and
               strings(pins.get("unaccepted")) and len(pins["unaccepted"]) <= 3)
        ck("status-optional", ("proposal_error" not in value or isinstance(value["proposal_error"], str)) and
           ("journal_unreadable" not in value or isinstance(value["journal_unreadable"], bool)) and
           ("journal" not in value or isinstance(value["journal"], list) and len(value["journal"]) <= 5 and
            all(isinstance(v, dict) and isinstance(v.get("e"), str) for v in value["journal"])))
    elif cmd == "doctor":
        ck("doctor-shape", isinstance(value.get("ready"), bool) and isinstance(value.get("findings"), list) and
           all(isinstance(f, dict) and all(isinstance(f.get(k), str) for k in ("check", "detail", "remedy")) for f in value["findings"]))
    elif cmd == "run":
        ck("run-shape", isinstance(value.get("probe"), str) and hashes(value.get("files")))
        for stream in ("stdout", "stderr"):
            text_key, b64_key = stream, stream + "_base64"
            ck(stream + "-one-encoding", (text_key in value) != (b64_key in value))
            ck(stream + "-capture-shape", type(value.get(stream + "_size")) is int and value[stream + "_size"] >= 0 and
               isinstance(value.get(stream + "_truncated"), bool) and
               isinstance(value.get(stream + "_hash"), str) and re.fullmatch(r"sha256:[0-9a-f]{64}", value[stream + "_hash"]) is not None)
            encoded = value.get(text_key, value.get(b64_key))
            ck(stream + "-encoded-type", isinstance(encoded, str))
            try:
                captured = encoded.encode("utf-8") if text_key in value else base64.b64decode(encoded, validate=True)
            except (ValueError, UnicodeError) as exc:
                ck(stream + "-encoding", False, str(exc))
            if b64_key in value:
                ck(stream + "-canonical-base64", base64.b64encode(captured).decode("ascii") == encoded)
                try:
                    captured.decode("utf-8")
                except UnicodeDecodeError:
                    valid_utf8 = False
                else:
                    valid_utf8 = True
                ck(stream + "-binary-encoding-needed", not valid_utf8)
            size = value[stream + "_size"]
            ck(stream + "-prefix-size", len(captured) == min(size, 262144))
            ck(stream + "-truncation", value[stream + "_truncated"] == (size > 262144))
            if not value[stream + "_truncated"]:
                ck(stream + "-full-hash", value[stream + "_hash"] == "sha256:" + hashlib.sha256(captured).hexdigest())
    return value, checks


class Run:
    def __init__(self, binary: Path, evidence: dict[str, Any]):
        self.binary = binary
        self.evidence = evidence

    def invoke(self, fixture: str, case: str, cwd: Path, *args: str) -> dict[str, Any]:
        argv = [str(self.binary), *args, "--json"]
        started = time.monotonic()
        env = execution_env(cwd)
        with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
            proc = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                    stdout=out, stderr=err)
            timed_out = False
            try:
                process_exit = proc.wait(timeout=TIMEOUT)
            except subprocess.TimeoutExpired:
                timed_out = True
                proc.terminate()
                try:
                    process_exit = proc.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    process_exit = proc.wait(timeout=2)
            out_size, err_size = out.tell(), err.tell()
            out.seek(0); err.seek(0)
            stdout = out.read(MAX_STREAM + 1)
            stderr = err.read(MAX_STREAM + 1)
        item: dict[str, Any] = {
            "kind": "actual-producer-output", "fixture": fixture, "case": case,
            "argv": argv, "cwd": str(cwd), "timeout_seconds": TIMEOUT,
            "environment": env,
            "duration_ms": round((time.monotonic() - started) * 1000, 3),
            "timed_out": timed_out, "process_exit": process_exit,
            "stdout_bytes": out_size, "stderr_bytes": err_size,
            "stdout": stdout.decode("utf-8", "backslashreplace"),
            "stderr": stderr.decode("utf-8", "backslashreplace"),
            "stdout_base64": base64.b64encode(stdout).decode("ascii"),
            "stderr_base64": base64.b64encode(stderr).decode("ascii"),
            "assertions": [],
        }
        self.evidence["actual_runs"].append(item)
        if timed_out:
            raise CaseError(f"{fixture}/{case}: timed out")
        if out_size > MAX_STREAM or err_size > MAX_STREAM:
            raise CaseError(f"{fixture}/{case}: stream exceeded {MAX_STREAM} bytes")
        try:
            value, checks = transport(stdout, process_exit)
            item["reply"] = value
            item["assertions"].extend(checks)
        except CaseError as exc:
            item["assertions"].extend(exc.checks)
            item["transport_error"] = str(exc)
            raise
        expected_cmd = "help" if any(arg in {"--help", "-h"} for arg in args) else args[0]
        check(item, "expected-command", value.get("cmd") == expected_cmd,
              f"got {value.get('cmd')}, want {expected_cmd}")
        return item


def check(item: dict[str, Any], name: str, condition: bool, detail: str = "") -> None:
    item["assertions"].append({"name": name, "pass": bool(condition), "detail": detail})
    if not condition:
        raise CaseError(f"{item['fixture']}/{item['case']}: {name}: {detail}")


def outcome(item: dict[str, Any], exit_code: int, action: str,
            verdict: str | None = None) -> dict[str, Any]:
    reply = item["reply"]
    check(item, "expected-exit", reply.get("exit") == exit_code,
          f"got {reply.get('exit')}, want {exit_code}")
    check(item, "expected-action", reply.get("next", {}).get("action") == action,
          f"got {reply.get('next', {}).get('action')}, want {action}")
    if verdict is not None:
        check(item, "expected-verdict", reply.get("verdict") == verdict,
              f"got {reply.get('verdict')}, want {verdict}")
    if exit_code == 0 and verdict == "green":
        counts = reply.get("counts")
        check(item, "green-counts", isinstance(counts, dict) and
              {"declared", "pass", "behavior", "flaky", "harness", "metric", "unmet", "skipped"} <= counts.keys() and
              counts["pass"] == counts["declared"] and all(counts[k] == 0 for k in ("behavior", "flaky", "harness", "metric", "unmet", "skipped")))
        check(item, "green-no-failures", "failures" not in reply and "classes" not in reply)
    return reply


def write(path: Path, data: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(data, encoding="utf-8")
    if ACTIVE_EVENTS is not None:
        ACTIVE_EVENTS.append({"operation": "write", "path": str(path),
                              "bytes": len(data.encode("utf-8")),
                              "sha256": "sha256:" + hashlib.sha256(data.encode("utf-8")).hexdigest()})


def git(root: Path, *args: str) -> str:
    env = execution_env(root)
    done = subprocess.run(["git", *args], cwd=root, env=env, stdin=subprocess.DEVNULL,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=TIMEOUT)
    if done.returncode:
        raise CaseError(f"git {' '.join(args)}: {done.stderr.decode('utf-8', 'replace')}")
    return done.stdout.decode("utf-8").strip()


def init_repo(root: Path, manifest: str, extra: dict[str, str] | None = None) -> None:
    root.mkdir()
    git(root, "init", "-q", "--initial-branch=main")
    write(root / ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.state\n.mode\n.toggle\n.executions\n.hard\n")
    write(root / "vise.toml", manifest)
    for name, content in (extra or {}).items():
        write(root / name, content)
    git(root, "add", ".")
    git(root, "commit", "-q", "-m", "fixture source")


def commit_baseline(root: Path, message: str = "fixture baseline") -> str:
    git(root, "add", "vise.lock", ".vise/blobs")
    git(root, "commit", "-q", "-m", message)
    return git(root, "rev-parse", "HEAD")


def record_new(run: Run, fixture: str, root: Path) -> dict[str, Any]:
    item = run.invoke(fixture, "record-new", root, "record")
    reply = item["reply"]
    check(item, "record-exit-zero", reply.get("exit") == 0, str(reply))
    commit_baseline(root)
    return reply


BASE = """[vise]\nversion = 1\n[stubs]\nseed = \"1729\"\n"""


def missing_baseline(run: Run, root: Path) -> None:
    init_repo(root, BASE + '\n[[probe]]\nid="p"\nrun="printf ok"\ntimeout=2\n')
    item = run.invoke("missing-baseline", "gate", root, "gate")
    reply = outcome(item, 4, "record_first", "indeterminate")
    counts = reply.get("counts", {})
    item["known_anomaly"] = {"description": "counts are not execution evidence on exit 4",
                             "observed_pass": counts.get("pass"),
                             "observed_declared": counts.get("declared")}


def unknown_selector(run: Run, root: Path) -> None:
    init_repo(root, BASE + '\n[[probe]]\nid="known"\nrun="printf ok"\ntimeout=2\n')
    record_new(run, "unknown-selector", root)
    bad = run.invoke("unknown-selector", "unknown", root, "gate", "--probe=absent")
    reply = outcome(bad, 2, "fix_invocation")
    failure = reply.get("failures", {}).get("gate", {})
    check(bad, "usage-marker", failure.get("usage") is True, str(failure))
    good = run.invoke("unknown-selector", "known", root, "gate", "--probe=known")
    outcome(good, 0, "proceed", "green")
    verified = run.invoke("unknown-selector", "known-verify", root, "verify", "--probe=known")
    outcome(verified, 0, "proceed", "green")


def green_behavior(run: Run, root: Path) -> None:
    manifest = BASE + '\n[[probe]]\nid="p"\nrun="if test -f .mode; then printf changed; else printf stable; fi"\ntimeout=2\n'
    init_repo(root, manifest)
    record_new(run, "green-behavior", root)
    outcome(run.invoke("green-behavior", "green", root, "gate"), 0, "proceed", "green")
    write(root / ".mode", "changed\n")
    item = run.invoke("green-behavior", "stable-divergence", root, "gate")
    reply = outcome(item, 1, "revert", "red")
    check(item, "behavior-class", "behavior" in reply.get("classes", []))


def hard_harness(run: Run, root: Path) -> None:
    manifest = BASE + '\n[[probe]]\nid="p"\nrun="if test -f .mode; then touch stray; fi; printf ok"\ntimeout=2\n'
    init_repo(root, manifest)
    record_new(run, "hard-harness", root)
    write(root / ".mode", "hard\n")
    item = run.invoke("hard-harness", "untracked-write", root, "gate")
    reply = outcome(item, 2, "fix_probe", "indeterminate")
    check(item, "ordinary-harness", reply.get("failures", {}).get("p", {}).get("class") == "harness")


def operator_spec_drift(run: Run, root: Path) -> None:
    manifest = BASE + '\n[[probe]]\nid="pin"\nrun="printf run >> .executions; printf wanted"\ntimeout=2\nexpect.stdout="spec.txt"\n'
    init_repo(root, manifest, {"spec.txt": "wanted"})
    record_new(run, "operator-spec-drift", root)
    before = (root / ".executions").read_text().count("run")
    journal = root / ".vise/journal.jsonl"
    journal_before = len(journal.read_text(encoding="utf-8").splitlines())
    write(root / "spec.txt", "changed")
    item = run.invoke("operator-spec-drift", "edited-spec", root, "gate")
    reply = outcome(item, 2, "human", "indeterminate")
    after = (root / ".executions").read_text().count("run")
    journal_after = len(journal.read_text(encoding="utf-8").splitlines())
    check(item, "operator-marker", reply.get("failures", {}).get("pin", {}).get("operator") is True)
    check(item, "candidate-not-executed", before == after, f"before={before} after={after}")
    check(item, "preflight-judgment-journaled-once", journal_after == journal_before + 1,
          f"before={journal_before} after={journal_after}")


def flake_budget(run: Run, root: Path) -> None:
    command = "if test -f .mode; then if test -f .toggle; then rm .toggle; printf B; else touch .toggle; printf A; fi; else printf stable; fi"
    init_repo(root, BASE + f'\n[[probe]]\nid="p"\nrun="{command}"\ntimeout=2\n')
    record_new(run, "flake-budget", root)
    write(root / ".mode", "flake\n")
    for number in (1, 2):
        item = run.invoke("flake-budget", f"flake-{number}", root, "gate")
        outcome(item, 3, "quarantine_ack", "indeterminate")
    item = run.invoke("flake-budget", "third-rerun-refused", root, "gate")
    reply = outcome(item, 2, "human", "indeterminate")
    check(item, "rerun-limit", "rerun-limit" in reply.get("failures", {}))


def metric_regression(run: Run, root: Path) -> None:
    manifest = BASE + '''
[[probe]]
id="p"
run="printf ok"
timeout=2
[[metric]]
id="size"
run="if test -f .mode; then printf 2; else printf 1; fi"
direction="down"
enforce="no-regress"
version_cmd="printf metric-v1"
timeout=2
'''
    init_repo(root, manifest)
    record_new(run, "metric-regression", root)
    write(root / ".mode", "regress\n")
    item = run.invoke("metric-regression", "regressed", root, "gate")
    reply = outcome(item, 5, "revert", "red")
    check(item, "metric-class", reply.get("failures", {}).get("size", {}).get("class") == "metric")
    check(item, "metric-delta", reply.get("metrics", {}).get("size", {}).get("now") == 2)


def pin_lifecycle(run: Run, root: Path) -> None:
    manifest = BASE + '\n[[probe]]\nid="pin"\nrun="./candidate.sh"\ntimeout=2\nexpect.stdout="spec.txt"\n'
    init_repo(root, manifest, {"spec.txt": "wanted", "candidate.sh": "#!/bin/sh\nprintf wrong"})
    os.chmod(root / "candidate.sh", 0o755)
    git(root, "add", "candidate.sh"); git(root, "commit", "-q", "--amend", "--no-edit")
    record = run.invoke("pin-lifecycle", "record-unmet", root, "record")
    check(record, "known-record-anomaly", record["reply"].get("exit") == 0,
          "record currently exits 0 although the pin observation is unmet")
    check(record, "record-unmet-array", record["reply"].get("pins") == {"accepted": [], "unmet": ["pin"]})
    record["known_anomaly"] = "record exit 0 is not a gate verdict that the pin is met"
    commit_baseline(root)
    outcome(run.invoke("pin-lifecycle", "unaccepted-unmet", root, "gate"), 6, "build", "red")
    write(root / "candidate.sh", "#!/bin/sh\nprintf wanted")
    os.chmod(root / "candidate.sh", 0o755)
    dirty = run.invoke("pin-lifecycle", "dirty-met-preview", root, "record", "--allow-dirty", "--preview")
    check(dirty, "dirty-preview-no-acceptance", dirty["reply"].get("pins", {}).get("accepted") == [] and
          dirty["reply"].get("pins", {}).get("passing_unaccepted") == ["pin"])
    git(root, "add", "candidate.sh"); git(root, "commit", "-q", "-m", "implement pin")
    met = run.invoke("pin-lifecycle", "met-unaccepted", root, "gate")
    reply = outcome(met, 0, "proceed", "green")
    check(met, "passing-unaccepted", reply.get("pins", {}).get("passing_unaccepted") == ["pin"])
    preview = run.invoke("pin-lifecycle", "accept-preview", root, "record", "--preview")
    check(preview, "preview-exit", preview["reply"].get("exit") == 0)
    digest = preview["reply"].get("candidate")
    check(preview, "candidate-digest", isinstance(digest, str) and digest.startswith("sha256:"), str(digest))
    check(preview, "preview-acceptance-array", preview["reply"].get("pins") == {"accepted": ["pin"], "unmet": []})
    accepted = run.invoke("pin-lifecycle", "accept-digest", root, "record", "--accept", digest)
    check(accepted, "accept-exit", accepted["reply"].get("exit") == 0)
    check(accepted, "accepted-array", accepted["reply"].get("pins") == {"accepted": ["pin"], "unmet": []})
    commit_baseline(root, "accept pin")
    outcome(run.invoke("pin-lifecycle", "accepted-green", root, "gate"), 0, "proceed", "green")
    write(root / "candidate.sh", "#!/bin/sh\nprintf regressed")
    os.chmod(root / "candidate.sh", 0o755)
    git(root, "add", "candidate.sh"); git(root, "commit", "-q", "-m", "regress pin")
    outcome(run.invoke("pin-lifecycle", "accepted-regression", root, "gate"), 1, "revert", "red")


def four_pins(run: Run, root: Path) -> None:
    entries = []
    extras: dict[str, str] = {}
    for index in range(1, 5):
        pin = f"pin{index}"
        extras[f"{pin}.txt"] = pin
        entries.append(f'[[probe]]\nid="{pin}"\nrun="if test -f .mode; then printf {pin}; else printf wrong; fi"\ntimeout=2\nexpect.stdout="{pin}.txt"')
    metric = '[[metric]]\nid="score"\nrun="printf 1"\ndirection="down"\nenforce="no-regress"\nversion_cmd="printf metric-v1"\ntimeout=2'
    init_repo(root, BASE + "\n" + "\n".join(entries) + "\n" + metric + "\n", extras)
    record_new(run, "four-pins", root)
    item = run.invoke("four-pins", "four-unmet", root, "gate")
    reply = outcome(item, 6, "build", "red")
    pins = reply.get("pins", {})
    failures = reply.get("failures", {})
    check(item, "unmet-summary-bounded", pins.get("unmet_count") == 4 and len(pins.get("unmet", [])) == 3)
    check(item, "complete-unmet-failures", sorted(k for k, v in failures.items() if v.get("class") == "unmet") == ["pin1", "pin2", "pin3", "pin4"])
    counts = reply.get("counts", {})
    check(item, "metric-skipped", counts.get("skipped") == 1 and counts.get("declared") == 5)
    write(root / ".mode", "met\n")
    item = run.invoke("four-pins", "four-passing-unaccepted", root, "gate")
    reply = outcome(item, 0, "proceed", "green")
    pins = reply.get("pins", {})
    check(item, "passing-summary-bounded", pins.get("passing_unaccepted_count") == 4 and len(pins.get("passing_unaccepted", [])) == 3)


def strict_streams(run: Run, root: Path) -> None:
    manifest = BASE + '\n[[probe]]\nid="exit-only"\nrun="if test -f .mode; then printf noise; printf error >&2; fi; exit 7"\ntimeout=2\nexpect.exit=7\n'
    init_repo(root, manifest)
    record_new(run, "strict-streams", root)
    item = run.invoke("strict-streams", "exit-only-empty-streams", root, "gate")
    outcome(item, 0, "proceed", "green")
    write(root / ".mode", "noise\n")
    item = run.invoke("strict-streams", "omitted-stdout-is-empty", root, "gate")
    reply = outcome(item, 1, "revert", "red")
    check(item, "strict-empty-stream", reply.get("failures", {}).get("exit-only", {}).get("class") == "behavior")
    failure = reply.get("failures", {}).get("exit-only", {})
    check(item, "strict-empty-stdout-and-stderr", failure.get("expect", {}).get("stdout") != failure.get("got", {}).get("stdout") and
          failure.get("expect", {}).get("stderr") != failure.get("got", {}).get("stderr"))


def public_routes(run: Run, root: Path) -> None:
    root.mkdir()
    git(root, "init", "-q", "--initial-branch=main")
    help_item = run.invoke("public-routes", "help", root, "--help")
    reply = outcome(help_item, 0, "proceed")
    check(help_item, "help-command", reply.get("cmd") == "help")
    check(help_item, "multi-command-map", isinstance(reply.get("commands"), dict) and len(reply["commands"]) >= 8)
    specific = run.invoke("public-routes", "command-help", root, "gate", "--help")
    outcome(specific, 0, "proceed")
    check(specific, "help-target", specific["reply"].get("command") == "gate")
    version = run.invoke("public-routes", "version", root, "version")
    reply = outcome(version, 0, "proceed")
    check(version, "version-command", reply.get("cmd") == "version")
    init = run.invoke("public-routes", "init", root, "init")
    reply = outcome(init, 0, "human")
    check(init, "init-command", reply.get("cmd") == "init")
    second = run.invoke("public-routes", "init-existing", root, "init")
    outcome(second, 0, "human")
    check(second, "nothing-created", second["reply"].get("created") in (None, []))
    write(root / "vise.toml", BASE + '\n[[probe]]\nid="raw"\nrun="printf raw; printf diagnostic >&2; exit 7"\ntimeout=2\n')
    git(root, "add", ".")
    git(root, "commit", "-q", "-m", "public fixture")
    status = run.invoke("public-routes", "status", root, "status")
    reply = outcome(status, 0, "record_first")
    check(status, "status-command", reply.get("cmd") == "status")
    outcome(run.invoke("public-routes", "status-usage-error", root, "status", "bogus"), 2, "fix_invocation", "indeterminate")
    doctor = run.invoke("public-routes", "doctor", root, "doctor")
    reply = outcome(doctor, 0, "human")
    check(doctor, "doctor-command", reply.get("cmd") == "doctor")
    check(doctor, "findings-collection", isinstance(reply.get("findings"), list))
    raw = run.invoke("public-routes", "raw-run", root, "run", "raw")
    reply = outcome(raw, 7, "proceed")
    check(raw, "run-command", reply.get("cmd") == "run")
    check(raw, "raw-streams", reply.get("stdout") == "raw" and reply.get("stderr") == "diagnostic")
    check(raw, "empty-files-map", reply.get("files") == {})
    outcome(run.invoke("public-routes", "run-usage-error", root, "run", "missing"), 2, "fix_invocation", "indeterminate")


def raw_captures(run: Run, root: Path) -> None:
    # The first stream splits a UTF-8 sequence exactly at the prefix limit;
    # the second is binary but short enough to verify its full hash locally.
    command = "awk 'BEGIN {for(i=0;i<262143;i++) printf \"a\"}'; printf '\\303\\251'; printf '\\377' >&2"
    init_repo(root, BASE + '\n[[probe]]\nid="capture"\nrun=' + json.dumps(command) + '\ntimeout=2\n')
    item = run.invoke("raw-captures", "binary-and-split-prefix", root, "run", "capture")
    reply = outcome(item, 0, "proceed")
    stdout = b"a" * 262143 + "é".encode("utf-8")
    check(item, "split-prefix-base64", reply.get("stdout_base64") == base64.b64encode(stdout[:262144]).decode("ascii"))
    check(item, "full-stream-not-prefix-hash", reply.get("stdout_hash") == "sha256:" + hashlib.sha256(stdout).hexdigest())
    check(item, "binary-stderr", reply.get("stderr_base64") == "/w==")


def hard_over_tolerated(run: Run, root: Path) -> None:
    manifest = BASE + '\n[[probe]]\nid="pin"\nrun="if test -f .hard; then touch stray; fi; command-that-does-not-exist"\ntimeout=2\nexpect.exit=0\n'
    init_repo(root, manifest)
    record_new(run, "hard-over-tolerated", root)
    tolerated = run.invoke("hard-over-tolerated", "exit-127-unmet", root, "gate")
    reply = outcome(tolerated, 6, "build", "red")
    failure = reply.get("failures", {}).get("pin", {})
    check(tolerated, "exit-127-preserved-as-unmet", failure.get("class") == "unmet" and
          failure.get("got", {}).get("exit") == 127 and "127" in failure.get("detail", ""))
    write(root / ".hard", "hard\n")
    item = run.invoke("hard-over-tolerated", "hard-wins", root, "gate")
    reply = outcome(item, 2, "fix_probe", "indeterminate")
    check(item, "hard-harness-wins", reply.get("failures", {}).get("pin", {}).get("class") == "harness")


CASES: list[tuple[str, Callable[[Run, Path], None]]] = [
    ("public-routes", public_routes),
    ("raw-captures", raw_captures),
    ("missing-baseline", missing_baseline), ("unknown-selector", unknown_selector),
    ("green-behavior", green_behavior), ("hard-harness", hard_harness),
    ("operator-spec-drift", operator_spec_drift), ("flake-budget", flake_budget),
    ("metric-regression", metric_regression), ("pin-lifecycle", pin_lifecycle),
    ("four-pins", four_pins), ("strict-streams", strict_streams),
    ("hard-over-tolerated", hard_over_tolerated),
]


def negative_controls(good_item: dict[str, Any]) -> list[dict[str, Any]]:
    raw = good_item["stdout"].encode("utf-8")
    process_exit = good_item["process_exit"]
    value = json.loads(raw)
    controls: list[tuple[str, bytes, int]] = []
    changed = copy.deepcopy(value); changed["exit"] = (changed["exit"] + 1) % 7
    controls.append(("json-exit-changed", (json.dumps(changed) + "\n").encode(), process_exit))
    controls.append(("process-exit-changed", raw, process_exit + 1))
    controls.append(("extra-json-frame", raw + b"{}\n", process_exit))
    controls.append(("truncated-json", raw[:-2], process_exit))
    controls.append(("duplicate-key", b'{"v":1,"v":1,"cmd":"gate","exit":0,"next":{"action":"proceed","detail":"x"}}\n', 0))
    controls.append(("non-finite-number", b'{"v":1,"cmd":"gate","exit":0,"next":{"action":"proceed","detail":"x"},"bad":NaN}\n', 0))
    changed = copy.deepcopy(value); changed["next"]["action"] = "future-action"
    controls.append(("unknown-action", (json.dumps(changed) + "\n").encode(), process_exit))
    changed = copy.deepcopy(value); del changed["next"]
    controls.append(("omitted-required-field", (json.dumps(changed) + "\n").encode(), process_exit))
    changed = copy.deepcopy(value); changed["exit"] = "0"
    controls.append(("malformed-field-type", (json.dumps(changed) + "\n").encode(), process_exit))
    changed = copy.deepcopy(value); changed["failures"] = {"x": {"class": "behavior"}}
    controls.append(("false-green-with-failure", (json.dumps(changed) + "\n").encode(), process_exit))
    changed = copy.deepcopy(value); changed["exit"] = 6; changed["verdict"] = "red"; changed["next"]["action"] = "revert"
    controls.append(("semantic-wrong-build-action", (json.dumps(changed) + "\n").encode(), 6))
    changed = copy.deepcopy(value); changed["exit"] = 1; changed["verdict"] = "red"; changed["next"]["action"] = "build"
    controls.append(("semantic-wrong-revert-action", (json.dumps(changed) + "\n").encode(), 1))
    controls.append(("exponent-overflow", b'{"v":1,"cmd":"gate","exit":0,"next":{"action":"proceed","detail":"x"},"bad":1e999}\n', 0))
    results = []
    for name, sample, code in controls:
        rejected = False; error = ""
        try:
            transport(sample, code)
        except CaseError as exc:
            rejected = True; error = str(exc)
        results.append({"kind": "synthetic-negative-control", "name": name,
                        "rejected": rejected, "expected": "rejected", "error": error})
    return results


def schema_controls(items: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Exercise the kit validator, never mislabel modified replies as producer output."""
    positive, negative = [], []

    def exercise(case: str, name: str, changes: list[tuple[tuple[str, ...], Any]], accept: bool) -> None:
        source = next((item for item in items if item["case"] == case and "reply" in item), None)
        entry: dict[str, Any] = {"kind": "synthetic-positive-control" if accept else "synthetic-negative-control",
                                 "name": name, "source_case": case, "expected": "accepted" if accept else "rejected"}
        (positive if accept else negative).append(entry)
        if source is None:
            entry.update({"pass": False, "error": "required source observation missing"})
            return
        sample = copy.deepcopy(source["reply"])
        for path, replacement in changes:
            target = sample
            for key in path[:-1]:
                target = target[key]
            target[path[-1]] = replacement
        raw = (json.dumps(sample) + "\n").encode("utf-8")
        entry.update({"changes": [{"path": list(path), "value": replacement} for path, replacement in changes],
                      "process_exit": source["process_exit"]})
        try:
            _, checks = transport(raw, source["process_exit"])
        except CaseError as exc:
            entry.update({"accepted": False, "pass": not accept, "error": str(exc), "assertions": exc.checks})
        else:
            entry.update({"accepted": True, "pass": accept, "assertions": checks})

    for case in ("green", "four-unmet", "accept-preview", "dirty-met-preview", "command-help",
                 "init-existing", "status", "status-usage-error", "run-usage-error", "binary-and-split-prefix"):
        exercise(case, "valid-additive-" + case, [(("future_description",), {"note": "ignored"})], True)
    exercise("green", "nested-additive-counts-next", [(("counts", "future_note"), "ignored"),
             (("next", "future_note"), None)], True)
    exercise("four-unmet", "nested-additive-pin-summary", [(("pins", "future_note"), "ignored")], True)
    exercise("accept-preview", "nested-additive-record-pins", [(("pins", "future_note"), "ignored")], True)
    exercise("status", "nested-additive-status", [(("lock", "future_note"), "ignored"),
             (("manifest", "future_note"), "ignored"), (("tool", "future_note"), "ignored")], True)
    cases = [
        ("green", "counts-boolean", ("counts", "pass"), True),
        ("green", "false-green-counts", ("counts", "behavior"), 1),
        ("four-unmet", "exit6-wrong-verdict", ("verdict",), "indeterminate"),
        ("four-unmet", "invalid-failure-observation", ("failures", "pin1", "got", "exit"), "0"),
        ("four-unmet", "invalid-failure-detail", ("failures", "pin1", "detail"), []),
        ("four-unmet", "invalid-failure-class", ("failures", "pin1", "class"), {}),
        ("dirty-met-preview", "invalid-optional-pin-list", ("pins", "passing_unaccepted"), "pin"),
        ("command-help", "invalid-command-help", ("usage",), 12),
        ("init-existing", "invalid-init-created", ("created",), False),
        ("status", "unknown-status-state", ("state",), "future-state"),
        ("status", "invalid-status-lock", ("lock", "present"), "false"),
        ("status", "invalid-tool-identity", ("tool", "modified"), "false"),
        ("status-usage-error", "invalid-error-outcome", ("counts", "harness"), "1"),
        ("binary-and-split-prefix", "invalid-files-hash", ("files",), {"artifact": "not-a-hash"}),
        ("binary-and-split-prefix", "invalid-capture-type", ("stderr_base64",), 12),
        ("binary-and-split-prefix", "invalid-base64", ("stderr_base64",), "!"),
        ("binary-and-split-prefix", "invalid-prefix-size", ("stdout_size",), 2),
        ("binary-and-split-prefix", "invalid-truncated-flag", ("stdout_truncated",), False),
        ("binary-and-split-prefix", "invalid-full-hash", ("stderr_hash",), "sha256:" + "0" * 64),
        ("regressed", "invalid-metric-direction", ("metrics", "size", "direction"), "future-direction"),
    ]
    for case, name, path, replacement in cases:
        exercise(case, name, [(path, replacement)], False)
    return positive, negative


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="explicit Vise executable under test")
    parser.add_argument("--evidence-dir", required=True, help="new directory for evidence.json")
    return parser.parse_args()


def main() -> int:
    global ACTIVE_EVENTS
    args = parse_args()
    requested = Path(args.binary)
    binary = requested.resolve()
    evidence_dir = Path(args.evidence_dir).resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK) or stat.S_ISDIR(binary.stat().st_mode):
        print(f"error: --binary must name an executable regular file: {binary}", file=sys.stderr)
        return 2
    try:
        evidence_dir.mkdir(parents=True, exist_ok=False)
    except FileExistsError:
        print(f"error: --evidence-dir already exists: {evidence_dir}", file=sys.stderr)
        return 2
    evidence: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION, "kit_version": KIT_VERSION,
        "kit_sha256": sha256(Path(__file__).resolve()),
        "started_at": utc_now(),
        "subject": {"requested": str(requested), "resolved": str(binary), "sha256": sha256(binary)},
        "platform": {"system": platform.system(), "release": platform.release(),
                     "machine": platform.machine(), "python": platform.python_version()},
        "bounds": {"child_timeout_seconds": TIMEOUT, "max_stream_bytes": MAX_STREAM,
                   "finite_fixture_count": len(CASES)},
        "actual_runs": [], "fixtures": [], "synthetic_negative_controls": [], "synthetic_positive_controls": [],
        "limitations": ["Evidence supports only the recorded execution platform.",
                        "The kit is not a hostile-code containment boundary."],
    }
    runner = Run(binary, evidence)
    failures: list[str] = []
    try:
        version = runner.invoke("subject", "version", Path.cwd(), "version")
        evidence["subject"]["version_reply"] = version["reply"]
        check(version, "version-command", version["reply"].get("cmd") == "version")
    except Exception as exc:  # evidence must survive producer/transport failures
        failures.append(f"subject/version: {exc}")
    with tempfile.TemporaryDirectory(prefix="vise-producer-conformance-") as temporary:
        temp = Path(temporary)
        evidence["fixture_root_disposed"] = str(temp)
        for index, (name, case) in enumerate(CASES):
            fixture = root = temp / f"{index:02d}-{name}"
            result = {"name": name, "status": "pass", "commits": [], "setup_writes": []}
            evidence["fixtures"].append(result)
            ACTIVE_EVENTS = result["setup_writes"]
            try:
                case(runner, root)
                if (root / ".git").exists():
                    result["commits"] = git(root, "log", "--format=%H %aI %s").splitlines()
            except Exception as exc:
                result["status"] = "fail"; result["error"] = str(exc)
                failures.append(f"{name}: {exc}")
            finally:
                ACTIVE_EVENTS = None
    good = next((item for item in evidence["actual_runs"]
                 if item.get("case") == "green" and "reply" in item), None)
    if good is None:
        failures.append("synthetic controls: no known-good green reply available")
    else:
        evidence["synthetic_negative_controls"] = negative_controls(good)
        for control in evidence["synthetic_negative_controls"]:
            if not control["rejected"]:
                failures.append(f"negative control accepted: {control['name']}")
    positive, negative = schema_controls(evidence["actual_runs"])
    evidence["synthetic_positive_controls"] = positive
    evidence["synthetic_negative_controls"].extend(negative)
    for control in positive + negative:
        if not control["pass"]:
            failures.append(f"schema control failed: {control['name']}")
    evidence["subject"]["sha256_after"] = sha256(binary)
    if evidence["subject"]["sha256_after"] != evidence["subject"]["sha256"]:
        failures.append("subject executable changed during conformance run")
    evidence["finished_at"] = utc_now()
    evidence["result"] = "pass" if not failures else "fail"
    evidence["failures"] = failures
    destination = evidence_dir / "evidence.json"
    destination.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"result": evidence["result"], "evidence": str(destination),
                      "actual_runs": len(evidence["actual_runs"]), "failures": failures},
                     sort_keys=True))
    return 0 if not failures else 1


if __name__ == "__main__":
    raise SystemExit(main())
