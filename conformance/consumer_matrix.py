#!/usr/bin/env python3
"""Run independent reference callers against real replies and labelled fault captures."""
from __future__ import annotations

import argparse
import copy
import datetime as dt
import hashlib
import json
import os
import signal
import subprocess
import sys
import time
import tomllib
from pathlib import Path

HERE = Path(__file__).resolve().parent


def digest(data: str | bytes) -> str:
    return "sha256:" + hashlib.sha256(data.encode() if isinstance(data, str) else data).hexdigest()


def encoded(value: object) -> bytes:
    return (json.dumps(value, sort_keys=True) + "\n").encode()


def write_json(path: Path, value: object) -> None:
    path.write_bytes(encoded(value))


def bounded_process(argv: list[str], timeout: int, **kwargs) -> tuple[subprocess.CompletedProcess, bool]:
    proc = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            start_new_session=True, **kwargs)
    timed_out = False
    try:
        stdout, stderr = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(proc.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = proc.communicate(timeout=1)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = proc.communicate(timeout=2)
    return subprocess.CompletedProcess(argv, proc.returncode, stdout, stderr), timed_out


def strict_driver_reply(raw: bytes) -> dict:
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result: raise ValueError("duplicate driver reply key")
            result[key] = value
        return result
    def nonfinite(_value): raise ValueError("non-finite driver reply")
    text = raw.decode("utf-8")
    reply, end = json.JSONDecoder(object_pairs_hook=unique, parse_constant=nonfinite).raw_decode(text)
    if not isinstance(reply, dict) or text[end:] != "\n": raise ValueError("driver framing")
    return reply


def binding(name: str, probes: list[str], metrics: list[str], subject: dict) -> dict:
    result = {"request_id": name, "cmd": "gate", "argv": ["gate", "--json"],
            "root": "/synthetic/consumer-fixture", "scope": {"kind": "full", "probes": probes, "metrics": metrics},
            **{key: digest("synthetic-" + key) for key in ("candidate", "manifest", "lock", "environment", "artifact")},
            "evaluator": subject["sha256"],
            "producer": {"version": subject["version_reply"]["version"],
                         "revision": subject["version_reply"]["revision"],
                         "modified": subject["version_reply"]["modified"]}}
    if "built" in subject["version_reply"]:
        result["producer"]["built"] = subject["version_reply"]["built"]
    return result


def policy(bound: dict) -> dict:
    return {"v": 1, "binding": copy.deepcopy(bound), "max_elapsed_ms": 10000,
            "max_stream_bytes": 1048576,
            "trusted_producers": [{"evaluator": bound["evaluator"], "revision": bound["producer"]["revision"], "modified": bound["producer"]["modified"]}]}


def capture(bound: dict, reply: dict, code: int | None = None) -> dict:
    return {"v": 1, "binding": copy.deepcopy(bound), "binding_after": copy.deepcopy(bound), "termination": {"kind": "exit", "code": reply["exit"] if code is None else code},
            "elapsed_ms": 100, "stdout": encoded(reply).decode(), "stderr": ""}


def decision(bound: dict, code: int, action: str, classes: list[str], unmet: list[str] | None = None,
             skipped: int = 0, passing: list[str] | None = None, passing_count: int = 0) -> dict:
    if bound["scope"]["kind"] == "probe":
        metrics_skipped = 0
    elif code == 4:
        metrics_skipped = len(bound["scope"]["metrics"])
    elif code == 2:
        metrics_skipped = None
    else:
        metrics_skipped = skipped
    return {"v": 1, "disposition": {0: "proceed", 1: "revert", 2: "escalate", 3: "escalate", 4: "escalate", 5: "revert", 6: "build"}[code],
            "vise_exit": code, "verdict": {0: "green", 1: "red", 2: "indeterminate", 3: "indeterminate", 4: "indeterminate", 5: "red", 6: "red"}[code],
            "next_action": action, "classes": sorted(classes), "unmet_ids": unmet or [],
            "checks_skipped": skipped, "metrics_skipped": metrics_skipped,
            "metrics_checked": code in (0, 5) and skipped == 0 and bound["scope"]["kind"] == "full",
            "passing_unaccepted_ids": passing or [], "passing_unaccepted_count": passing_count,
            "operator_acceptance_required": passing_count > 0, "binding": copy.deepcopy(bound)}


# Expected interpretations are authored independently of driver code and producer replies.
REAL = [
    ("missing-baseline", "gate", 4, "record_first", []),
    ("missing-baseline-multi", "full", 4, "record_first", []),
    ("missing-baseline-multi", "subset", 4, "record_first", []),
    ("missing-baseline-multi", "subset-verify", 4, "record_first", []),
    ("unknown-selector", "unknown", 2, "fix_invocation", ["harness"]),
    ("unknown-selector", "known", 0, "proceed", []),
    ("unknown-selector", "known-verify", 0, "proceed", []),
    ("green-behavior", "green", 0, "proceed", []),
    ("green-behavior", "stable-divergence", 1, "revert", ["behavior"]),
    ("hard-harness", "untracked-write", 2, "fix_probe", ["harness"]),
    ("operator-spec-drift", "edited-spec", 2, "human", ["harness"]),
    ("flake-budget", "flake-1", 3, "quarantine_ack", ["flake"]),
    ("flake-budget", "third-rerun-refused", 2, "human", ["harness"]),
    ("metric-regression", "regressed", 5, "revert", ["metric"]),
    ("pin-lifecycle", "unaccepted-unmet", 6, "build", ["unmet"]),
    ("pin-lifecycle", "met-unaccepted", 0, "proceed", []),
    ("pin-lifecycle", "accepted-green", 0, "proceed", []),
    ("pin-lifecycle", "accepted-regression", 1, "revert", ["behavior"]),
    ("four-pins", "four-unmet", 6, "build", ["unmet"]),
    ("four-pins", "four-passing-unaccepted", 0, "proceed", []),
    ("strict-streams", "exit-only-empty-streams", 0, "proceed", []),
    ("strict-streams", "omitted-stdout-is-empty", 1, "revert", ["behavior"]),
    ("hard-over-tolerated", "exit-127-unmet", 6, "build", ["unmet"]),
    ("hard-over-tolerated", "hard-wins", 2, "fix_probe", ["harness"]),
]

EXPECTED_SKIPPED = {
    ("missing-baseline", "gate"): 1,
    ("missing-baseline-multi", "full"): 3,
    ("missing-baseline-multi", "subset"): 1,
    ("missing-baseline-multi", "subset-verify"): 1,
    ("flake-budget", "third-rerun-refused"): 1,
    ("four-pins", "four-unmet"): 1,
}


def make_cases(producer: dict) -> list[dict]:
    cases = []

    def add(name: str, cap: dict, expected: dict | None, pol: dict | None = None, kind: str = "synthetic-fault") -> dict:
        item = {"name": name, "kind": kind, "mode": "consume", "capture": copy.deepcopy(cap),
                "policy": copy.deepcopy(pol or policy(cap["binding"])), "expected": copy.deepcopy(expected)}
        cases.append(item)
        return item

    for fixture, name, code, action, classes in REAL:
        actual = next(v for v in producer["actual_runs"] if v["fixture"] == fixture and v["case"] == name)
        probes = ["pin"] if fixture in {"pin-lifecycle", "hard-over-tolerated", "operator-spec-drift"} else ["p"]
        metrics = []
        if fixture == "four-pins":
            probes, metrics = ["pin1", "pin2", "pin3", "pin4"], ["score"]
        if fixture == "metric-regression":
            metrics = ["size"]
        if fixture == "strict-streams":
            probes = ["exit-only"]
        if fixture == "missing-baseline-multi":
            probes, metrics = ["p1", "p2"], ["size"]
        bound = binding("real-" + name, probes, metrics, producer["subject"])
        bound["root"] = actual["cwd"]
        bound["cmd"] = actual["reply"]["cmd"]
        bound["argv"] = [bound["cmd"], "--json"]
        if fixture == "unknown-selector":
            selector = "absent" if name == "unknown" else "known"
            bound["scope"] = {"kind": "probe", "probes": [selector], "metrics": []}
            bound["argv"] += ["--probe", selector]
        if fixture == "missing-baseline-multi" and name in {"subset", "subset-verify"}:
            bound["cmd"] = "verify" if name == "subset-verify" else "gate"
            bound["scope"] = {"kind": "probe", "probes": ["p1"], "metrics": []}
            bound["argv"] = [bound["cmd"], "--json", "--probe", "p1"]
        bound["lock"] = actual["reply"].get("lock", digest("absent-lock"))
        cap = capture(bound, actual["reply"], actual["process_exit"])
        cap.update({"stdout": actual["stdout"], "stderr": actual["stderr"], "elapsed_ms": int(actual["duration_ms"])})
        unmet = probes if code == 6 else []
        passing = probes[:3] if name in {"met-unaccepted", "four-passing-unaccepted"} else []
        expected = decision(bound, code, action, classes, unmet,
                            EXPECTED_SKIPPED.get((fixture, name), 0),
                            passing, len(probes) if passing else 0)
        item = add("actual-" + name, cap, expected, kind="actual-producer-reply-in-synthetic-consumer-envelope")
        item["producer_source"] = {"fixture": fixture, "case": name, "argv": actual["argv"],
                                   "binding_note": "Root, lock and producer identity observed; remaining bindings are synthetic test values. Canonical argv may spell --probe separately."}

    good = next(v for v in cases if v["name"] == "actual-green")
    cap, pol = good["capture"], good["policy"]
    reply = json.loads(cap["stdout"])
    additive = copy.deepcopy(cap)
    extra = copy.deepcopy(reply)
    extra.update({"future": {"description": "ignored"}})
    extra["counts"]["future"] = "ignored"
    extra["next"]["future"] = ["ignored"]
    additive["stdout"] = encoded(extra).decode()
    additive["binding"]["future"] = "ignored"
    add("valid-additive-fields", additive, good["expected"], pol, "synthetic-positive")
    nested = copy.deepcopy(cap)
    for key in ("binding", "binding_after"):
        nested[key]["scope"]["future"] = "ignored"
        nested[key]["producer"]["future"] = "ignored"
    add("valid-additive-binding-objects", nested, good["expected"], pol, "synthetic-positive")
    item = add("outer-whitespace", cap, good["expected"], pol, "synthetic-positive")
    item["raw_capture"] = " \n\t" + encoded(cap).decode() + " \t"
    item["raw_policy"] = " \n\t" + encoded(pol).decode() + " \t"
    pretty = copy.deepcopy(cap); pretty["stdout"] = json.dumps(reply, indent=2) + "\n"
    add("pretty-producer-json", pretty, good["expected"], pol, "synthetic-positive")

    missing_full_bound = binding("synthetic-missing-full", ["p1", "p2"], ["size"], producer["subject"])
    missing_full_reply = {"v": 1, "cmd": "gate", "exit": 4, "verdict": "indeterminate",
                          "next": {"action": "record_first", "detail": "synthetic missing baseline"},
                          "counts": {"declared": 3, "pass": 0, "behavior": 0, "flaky": 0,
                                     "harness": 0, "metric": 0, "unmet": 0, "skipped": 3}}
    missing_full = capture(missing_full_bound, missing_full_reply)
    add("exit4-full-scope", missing_full,
        decision(missing_full_bound, 4, "record_first", [], skipped=3), kind="synthetic-positive")
    missing_subset_bound = copy.deepcopy(missing_full_bound)
    missing_subset_bound.update({"request_id": "synthetic-missing-subset", "scope": {"kind": "probe", "probes": ["p1"], "metrics": []},
                                 "argv": ["gate", "--json", "--probe", "p1"]})
    missing_subset_reply = copy.deepcopy(missing_full_reply)
    missing_subset_reply["counts"].update({"declared": 1, "skipped": 1})
    missing_subset = capture(missing_subset_bound, missing_subset_reply)
    add("exit4-subset-scope", missing_subset,
        decision(missing_subset_bound, 4, "record_first", [], skipped=1), kind="synthetic-positive")
    for name, path, value in [
        ("exit4-legacy-false-pass", ("counts", "pass"), 3),
        ("exit4-wrong-declared-scope", ("counts", "declared"), 2),
        ("exit4-wrong-skips", ("counts", "skipped"), 2),
        ("exit4-lock-field", ("lock",), missing_full_bound["lock"]),
        ("exit4-failures-field", ("failures",), {}),
        ("exit4-classes-field", ("classes",), []),
        ("exit4-metrics-field", ("metrics",), {}),
        ("exit4-pins-field", ("pins",), {"evaluated": 0, "unmet": [], "unmet_count": 0,
                                           "passing_unaccepted": [], "passing_unaccepted_count": 0}),
    ]:
        changed = copy.deepcopy(missing_full_reply); node = changed
        for key in path[:-1]: node = node[key]
        node[path[-1]] = value
        add(name, capture(missing_full_bound, changed), None, policy(missing_full_bound))
    coherent = copy.deepcopy(missing_full_reply)
    coherent["counts"].update({"declared": 2, "skipped": 2})
    add("exit4-coherent-arithmetic-wrong-full-scope", capture(missing_full_bound, coherent),
        None, policy(missing_full_bound))
    leaked = copy.deepcopy(missing_subset_reply)
    leaked["counts"].update({"declared": 3, "skipped": 3})
    add("exit4-subset-leaks-full-scope", capture(missing_subset_bound, leaked),
        None, policy(missing_subset_bound))
    known = next(v for v in cases if v["name"] == "actual-known")
    alternate = copy.deepcopy(known["capture"])
    for key in ("binding", "binding_after"):
        alternate[key]["argv"] = ["gate", "--probe", "known", "--json"]
    expected_alternate = copy.deepcopy(known["expected"]); expected_alternate["binding"] = alternate["binding"]
    add("alternate-probe-argv-order", alternate, expected_alternate, policy(alternate["binding"]), "synthetic-positive")
    mixed = copy.deepcopy(reply)
    mixed.update({"exit": 2, "verdict": "indeterminate", "classes": ["harness", "behavior"],
                  "next": {"action": "fix_probe", "detail": "synthetic mixed failures"},
                  "failures": {"p": {"class": "behavior"}, "synthetic-infrastructure": {"class": "harness"}}})
    mixed["counts"].update({"pass": 0, "behavior": 1, "harness": 1})
    add("mixed-class-order", capture(cap["binding"], mixed), decision(cap["binding"], 2, "fix_probe", ["behavior", "harness"]), pol, "synthetic-positive")
    mixed["classes"] = ["behavior", "harness"]
    add("mixed-class-reordered", capture(cap["binding"], mixed), decision(cap["binding"], 2, "fix_probe", ["behavior", "harness"]), pol, "synthetic-positive")

    def raw_fault(name: str, stdout: str) -> None:
        if stdout == cap["stdout"]: raise AssertionError("mutation did not land: " + name)
        changed = copy.deepcopy(cap); changed["stdout"] = stdout
        add(name, changed, None, pol)

    mutable = encoded(reply).decode()
    for name, stdout in [
        ("malformed", "not-json\n"), ("truncated", cap["stdout"][:-2]),
        ("extra-frame", cap["stdout"] + "{}\n"), ("missing-lf", cap["stdout"][:-1]),
        ("duplicate-top-key", mutable.replace('"v": 1', '"v": 1, "v": 1')),
        ("duplicate-escaped-key", mutable.replace('"v": 1', '"v": 1, "\\u0076": 1')),
        ("duplicate-empty-object", mutable.replace('"v": 1', '"future":{},"future":{},"v": 1')),
        ("nonfinite", mutable.replace('"v": 1', '"future":NaN,"v": 1')),
        ("overflow", mutable.replace('"v": 1', '"future":1e999,"v": 1')),
    ]:
        raw_fault(name, stdout)
    mutations = [
        ("unsupported-version", ("v",), 2), ("boolean-version", ("v",), True),
        ("unknown-action", ("next", "action"), "rerun"),
        ("unknown-verdict", ("verdict",), "yellow"),
        ("wrong-command", ("cmd",), "status"),
        ("boolean-exit", ("exit",), False),
        ("counts-type", ("counts", "pass"), "1"),
        ("green-skipped", ("counts", "skipped"), 1),
        ("full-scope-undercount", ("counts",), {"declared": 0, "pass": 0, "behavior": 0, "flaky": 0, "harness": 0, "metric": 0, "unmet": 0, "skipped": 0}),
        ("full-scope-overcount", ("counts",), {"declared": 2, "pass": 2, "behavior": 0, "flaky": 0, "harness": 0, "metric": 0, "unmet": 0, "skipped": 0}),
        ("false-green", ("failures",), {"p": {"class": "behavior"}}),
        ("green-empty-failures", ("failures",), {}),
        ("green-empty-classes", ("classes",), []),
        ("unknown-class", ("classes",), ["future"]),
        ("wrong-reply-lock", ("lock",), digest("other-lock")),
        ("missing-counts", ("counts",), None),
    ]
    for name, path, value in mutations:
        changed = copy.deepcopy(reply); node = changed
        for key in path[:-1]:
            node = node[key]
        node[path[-1]] = value
        raw_fault(name, encoded(changed).decode())
    for where in ("capture", "policy", "reply"):
        for depth, accept in ((126, True), (129, False), (2000, False)):
            nested_json = "[" * depth + "0" + "]" * depth
            raw = (encoded(cap if where == "capture" else pol if where == "policy" else reply).decode().rstrip()[:-1]
                   + ',"nested_control":' + nested_json + "}\n")
            changed = copy.deepcopy(cap)
            if where == "reply": changed["stdout"] = raw
            item = add(f"nesting-{where}-{depth}", changed, good["expected"] if accept else None, pol,
                       "synthetic-positive" if accept else "synthetic-fault")
            if where == "capture": item["raw_capture"] = raw
            if where == "policy": item["raw_policy"] = raw
    for kind in ("signal", "timeout", "launch_error", "collection_error", "future"):
        changed = copy.deepcopy(cap); changed["termination"] = {"kind": kind, "code": 130}
        add("termination-" + kind, changed, None, pol)
    changed = copy.deepcopy(cap); changed["termination"]["code"] = 1
    add("process-exit-mismatch", changed, None, pol)
    changed = copy.deepcopy(cap); changed["elapsed_ms"] = 10001
    add("elapsed-bound", changed, None, pol)
    for name, value in (("negative-elapsed", -1), ("boolean-elapsed", True)):
        changed = copy.deepcopy(cap); changed["elapsed_ms"] = value
        add(name, changed, None, pol)
    changed = copy.deepcopy(cap); changed["v"] = 2
    add("unsupported-capture-version", changed, None, pol)
    changed = copy.deepcopy(pol); changed["v"] = 2
    add("unsupported-policy-version", cap, None, changed)
    changed = copy.deepcopy(cap); changed["stderr"] = "x" * 1048577
    add("stderr-bound", changed, None, pol)
    smaller = copy.deepcopy(pol); smaller["max_stream_bytes"] = 10
    add("stdout-policy-bound", cap, None, smaller)
    for key in ("request_id", "root", "candidate", "manifest", "lock", "evaluator", "environment", "artifact"):
        changed = copy.deepcopy(cap)
        changed["binding"][key] = digest("changed-" + key) if key not in {"request_id", "root"} else "/different"
        changed["binding_after"] = copy.deepcopy(changed["binding"])
        add("stale-" + key, changed, None, pol)
    changed = copy.deepcopy(cap); changed["binding"]["scope"]["probes"] = ["other"]
    changed["binding_after"] = copy.deepcopy(changed["binding"])
    add("stale-scope", changed, None, pol)
    changed = copy.deepcopy(cap); changed["binding_after"]["candidate"] = digest("during-run-edit")
    add("changed-post-binding", changed, None, pol)
    changed = copy.deepcopy(cap)
    for key in ("binding", "binding_after"):
        changed[key]["cmd"] = "verify"; changed[key]["argv"] = ["verify", "--json"]
    add("stale-exact-argv", changed, None, pol)
    changed = copy.deepcopy(pol); changed["trusted_producers"][0]["revision"] = "0" * 40
    add("untrusted-producer", cap, None, changed)
    changed = copy.deepcopy(cap); del changed["binding"]["producer"]["modified"]
    add("unknown-build-stamp", changed, None, pol)
    item = add("duplicate-envelope", cap, None, pol)
    item["raw_capture"] = encoded(cap).replace(b'"v": 1', b'"v":1,"v":1').decode()
    item = add("extra-envelope-frame", cap, None, pol)
    item["raw_capture"] = encoded(cap).decode() + "{}\n"
    item = add("duplicate-policy", cap, None, pol)
    item["raw_policy"] = encoded(pol).replace(b'"v": 1', b'"v":1,"v":1').decode()

    for source_name, name, path, value in [
        ("actual-four-unmet", "truncated-unmet-count", ("counts", "unmet"), 3),
        ("actual-four-unmet", "wrong-unmet-summary", ("pins", "unmet"), ["pin1", "pin2", "missing"]),
        ("actual-four-unmet", "wrong-exit6-action", ("next", "action"), "revert"),
        ("actual-four-unmet", "malformed-failure", ("failures", "pin1", "got", "exit"), "0"),
        ("actual-regressed", "unknown-metric-direction", ("metrics", "size", "direction"), "future"),
        ("actual-regressed", "unknown-metric-enforcement", ("metrics", "size", "enforce"), "future"),
        ("actual-edited-spec", "operator-wrong-repair-action", ("next", "action"), "fix_probe"),
        ("actual-unknown", "usage-wrong-repair-action", ("next", "action"), "human"),
        ("actual-met-unaccepted", "passing-pin-outside-scope", ("pins", "passing_unaccepted"), ["outside"]),
        ("actual-met-unaccepted", "evaluated-pins-exceed-scope", ("pins", "evaluated"), 2),
        ("actual-stable-divergence", "nonharness-authority-marker", ("failures", "p", "operator"), True),
        ("actual-stable-divergence", "failure-class-count-mismatch", ("counts", "behavior"), 0),
    ]:
        source = next(v for v in cases if v["name"] == source_name)
        changed = copy.deepcopy(source["capture"])
        data = json.loads(changed["stdout"]); node = data
        for key in path[:-1]: node = node[key]
        node[path[-1]] = value
        changed["stdout"] = encoded(data).decode()
        add(name, changed, None, source["policy"])

    for source_name in ("actual-stable-divergence", "actual-flake-1", "actual-regressed", "actual-four-unmet"):
        source = next(v for v in cases if v["name"] == source_name)
        changed = copy.deepcopy(source["capture"])
        data = json.loads(changed["stdout"])
        data["counts"]["declared"] += 1
        changed["stdout"] = encoded(data).decode()
        add("non-green-scope-mismatch-" + source_name.removeprefix("actual-"), changed, None, source["policy"])
    for source_name in ("actual-stable-divergence", "actual-flake-1", "actual-regressed", "actual-four-unmet"):
        source = next(v for v in cases if v["name"] == source_name)
        changed = copy.deepcopy(source["capture"])
        data = json.loads(changed["stdout"])
        data["counts"]["declared"] += 1
        data["counts"]["pass"] += 1
        changed["stdout"] = encoded(data).decode()
        add("non-green-coherent-scope-mismatch-" + source_name.removeprefix("actual-"),
            changed, None, source["policy"])

    probes = ["pin1", "pin2", "pin3", "pin4", "pin5"]
    old_bound = binding("previous", probes, ["score"], producer["subject"])
    new_bound = copy.deepcopy(old_bound)
    new_bound.update({"request_id": "current", "candidate": digest("new-candidate"), "artifact": digest("new-artifact")})

    def unmet_reply(ids: list[str], bound: dict) -> dict:
        return {"v": 1, "cmd": "gate", "exit": 6, "verdict": "red", "classes": ["unmet"],
                "next": {"action": "build", "detail": "synthetic progress fixture"}, "lock": bound["lock"],
                "counts": {"declared": 6, "pass": 5 - len(ids), "behavior": 0, "flaky": 0, "harness": 0, "metric": 0, "unmet": len(ids), "skipped": 1},
                "failures": {key: {"class": "unmet"} for key in ids},
                "pins": {"evaluated": 5, "unmet": ids[:3], "unmet_count": len(ids), "passing_unaccepted": [], "passing_unaccepted_count": 0}}

    previous = capture(old_bound, unmet_reply(probes[:4], old_bound))
    for name, ids, allowed in [("strict-subset", probes[:2], True), ("equal-set", probes[:4], False),
                               ("swapped-regressed-id", ["pin1", "pin5"], False)]:
        current = capture(new_bound, unmet_reply(ids, new_bound))
        pol = policy(new_bound)
        pol.update({"previous_binding": copy.deepcopy(old_bound), "lineage": {"parent_candidate": old_bound["candidate"], "child_candidate": new_bound["candidate"]}})
        cases.append({"name": "progress-" + name, "kind": "synthetic-progress", "mode": "progress", "capture": current,
                      "previous": copy.deepcopy(previous), "policy": pol,
                      "expected": {"v": 1, "progress_allowed": allowed, "metrics_checked": False}})
    source = next(v for v in cases if v["name"] == "progress-strict-subset")
    changed = copy.deepcopy(source); changed["name"] = "progress-subset-invocations"
    for capture_key in ("capture", "previous"):
        bound = changed[capture_key]["binding"]
        bound["argv"] = ["gate", "--json", "--probe", "pin1"]
        bound["scope"] = {"kind": "probe", "probes": ["pin1"], "metrics": []}
        changed[capture_key]["binding_after"] = copy.deepcopy(bound)
        data = unmet_reply(["pin1"], bound)
        data["counts"].update({"declared": 1, "pass": 0, "skipped": 0})
        data["pins"]["evaluated"] = 1
        changed[capture_key]["stdout"] = encoded(data).decode()
    changed["policy"]["binding"] = copy.deepcopy(changed["capture"]["binding"])
    changed["policy"]["previous_binding"] = copy.deepcopy(changed["previous"]["binding"])
    changed["expected"]["progress_allowed"] = False; cases.append(changed)
    changed = copy.deepcopy(source); changed["name"] = "progress-changed-lock"
    changed["capture"]["binding"]["lock"] = digest("new-lock")
    changed["capture"]["binding_after"]["lock"] = digest("new-lock")
    data = json.loads(changed["capture"]["stdout"]); data["lock"] = digest("new-lock")
    changed["capture"]["stdout"] = encoded(data).decode()
    changed["policy"]["binding"] = copy.deepcopy(changed["capture"]["binding"])
    changed["expected"]["progress_allowed"] = False; cases.append(changed)
    changed = copy.deepcopy(source); changed["name"] = "progress-stale-current"
    changed["policy"]["binding"]["candidate"] = digest("different-delivery-candidate")
    changed["expected"] = None; cases.append(changed)
    changed = copy.deepcopy(source); changed["name"] = "progress-bad-lineage"
    changed["policy"]["lineage"]["parent_candidate"] = digest("unrelated")
    changed["expected"] = None; cases.append(changed)
    return cases


def live_identity_cases(output: Path, binary: Path, subject: dict, evidence: dict) -> list[dict]:
    """Collect actual identities before launch and again after the same process exits."""
    root = output / "live-repository"; root.mkdir()
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(root), "LC_ALL": "C", "LANG": "C", "TZ": "UTC",
           "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
           "GIT_AUTHOR_NAME": "Consumer Fixture", "GIT_COMMITTER_NAME": "Consumer Fixture",
           "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_EMAIL": "fixture@example.invalid",
           "GIT_AUTHOR_DATE": "2001-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2001-01-01T00:00:00Z"}
    evidence["live_commands"] = []

    def run(argv: list[str], want: int = 0) -> subprocess.CompletedProcess:
        result = subprocess.run(argv, cwd=root, env=env, capture_output=True, timeout=10)
        evidence["live_commands"].append({"argv": argv, "cwd": str(root), "environment": env,
                                          "exit": result.returncode, "stdout": result.stdout.decode("utf-8", "backslashreplace"),
                                          "stderr": result.stderr.decode("utf-8", "backslashreplace")})
        if result.returncode != want: raise RuntimeError("live capture fixture command failed: " + repr(argv))
        return result

    run(["git", "init", "-q", "--initial-branch=main"])
    (root / ".gitignore").write_text(".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n.interrupt\n.started\n")
    (root / "vise.toml").write_text('[vise]\nversion=1\n[stubs]\nseed="1729"\n[[probe]]\nid="p"\nrun="sh candidate.sh"\ntimeout=2\n')
    source_script = "if test -f .interrupt; then printf started > .started; sleep 30; fi\nprintf stable\n"
    payload = root / "candidate.sh"; payload.write_text(source_script)
    run(["git", "add", "."]); run(["git", "commit", "-q", "-m", "fixture source"])
    run([str(binary), "record", "--json"])
    run(["git", "add", "vise.lock", ".vise/blobs"]); run(["git", "commit", "-q", "-m", "fixture baseline"])

    def snapshot(request_id: str) -> dict:
        manifest = tomllib.loads((root / "vise.toml").read_text())
        probes = sorted(item["id"] for item in manifest["probe"])
        metrics = sorted(item["id"] for item in manifest.get("metric", []))
        current = binding(request_id, probes, metrics, subject)
        status = json.loads(run([str(binary), "status", "--json"]).stdout)
        if status.get("state") != "ready" or not status.get("lock", {}).get("hash"):
            raise RuntimeError("live fixture lacks a ready evaluator identity")
        paths = run(["git", "ls-files", "-z"]).stdout.decode().rstrip("\0").split("\0")
        source = [(name, digest((root / name).read_bytes()), (root / name).stat().st_mode & 0o777) for name in sorted(paths)]
        current.update({"root": str(root), "candidate": digest(encoded(source)), "manifest": digest((root / "vise.toml").read_bytes()),
                        "lock": status["lock"]["hash"], "environment": digest(encoded({"process_environment": env, "interrupt_fixture_enabled": (root / ".interrupt").exists()})),
                        "evaluator": digest(binary.read_bytes()), "artifact": digest(payload.read_bytes())})
        return current

    cases = []
    for name, mutate in (("live-identity-stable", False), ("live-identity-mutated-after-exit", True)):
        before = snapshot(name)
        start = time.monotonic(); result = run([str(binary), "gate", "--json"])
        elapsed = int((time.monotonic() - start) * 1000)
        if mutate: payload.write_text("printf stable\n# changed delivery bytes after the gate\n")
        after = snapshot(name)
        cap = {"v": 1, "binding": before, "binding_after": after, "termination": {"kind": "exit", "code": result.returncode},
               "elapsed_ms": elapsed, "stdout": result.stdout.decode(), "stderr": result.stderr.decode()}
        cases.append({"name": name, "kind": "actual-producer-and-measured-pre-post-bindings", "mode": "consume", "capture": cap,
                      "policy": policy(after), "expected": None if mutate else decision(before, 0, "proceed", [])})
    payload.write_text(source_script)
    (root / ".interrupt").write_text("interrupt enabled\n")
    before = snapshot("live-sigint")
    argv = [str(binary), "gate", "--json"]
    start = time.monotonic()
    proc = subprocess.Popen(argv, cwd=root, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    try:
        deadline = start + 3
        while not (root / ".started").exists() and proc.poll() is None and time.monotonic() < deadline:
            time.sleep(0.01)
        if not (root / ".started").exists(): raise RuntimeError("interruption fixture did not enter its probe")
        proc.send_signal(signal.SIGINT)
        stdout, stderr = proc.communicate(timeout=3)
    finally:
        if proc.poll() is None:
            try: os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError: pass
            proc.communicate(timeout=2)
    after = snapshot("live-sigint")
    evidence["live_commands"].append({"argv": argv, "cwd": str(root), "environment": env, "exit": proc.returncode,
                                      "signal_sent_after_marker": "SIGINT", "stdout": stdout.decode(), "stderr": stderr.decode()})
    if proc.returncode not in (130, -signal.SIGINT): raise RuntimeError("SIGINT did not produce an interruption exit")
    cases.append({"name": "live-sigint", "kind": "actual-interrupted-producer-with-measured-bindings", "mode": "consume",
                  "capture": {"v": 1, "binding": before, "binding_after": after,
                              "termination": {"kind": "signal", "code": proc.returncode, "signal": "SIGINT"},
                              "elapsed_ms": int((time.monotonic() - start) * 1000), "stdout": stdout.decode(), "stderr": stderr.decode()},
                  "policy": policy(after), "expected": None})
    evidence["live_fixture_note"] = "Tracked fixture files define candidate identity; candidate.sh bytes define its delivery artifact. Scope is parsed from the authored manifest. The lock field is Vise's evaluator tamper identity collected through status before and after, not the raw vise.lock file hash. The mutated case changes bytes after gate termination and before the second snapshot. No hostile concurrent writer protection is claimed."
    return cases


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--evidence-dir", required=True)
    args = parser.parse_args()
    output = Path(args.evidence_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    evidence = {"v": 1, "started_at": dt.datetime.now(dt.timezone.utc).isoformat(), "runs": [], "failures": [],
                "limits": {"driver_seconds": 10, "input_bytes": 4194304},
                "limitations": ["Orchestrator bindings are synthetic test values except explicitly recorded observed fields.",
                                "This interpreter matrix does not prove host enforcement or actual delivery freshness."]}
    drivers = {"python": [sys.executable, "-B", str(HERE / "consumer.py")], "shell": ["sh", str(HERE / "consumer-shell.sh")]}
    try:
        produced, timed_out = bounded_process([sys.executable, "-B", str(HERE / "producer.py"), "--binary", args.binary,
                                              "--evidence-dir", str(output / "producer")], 180)
        evidence["producer_execution"] = {"exit": produced.returncode, "timed_out": timed_out, "stdout": produced.stdout.decode(), "stderr": produced.stderr.decode()}
        if produced.returncode or timed_out:
            raise RuntimeError("producer fixture setup failed; evidence retained")
        producer = json.loads((output / "producer/evidence.json").read_text())
        evidence["platform"] = producer["platform"]
        evidence["driver_hashes"] = {name: digest((HERE / name).read_bytes()) for name in ("consumer.py", "consumer-shell.sh", "consumer.jq", "consumer_matrix.py")}
        cases = make_cases(producer)
        cases.extend(live_identity_cases(output, Path(args.binary).resolve(), producer["subject"], evidence))
        fixtures = output / "fixtures"; fixtures.mkdir()
        for number, case in enumerate(cases):
            directory = fixtures / f"{number:03d}-{case['name']}"; directory.mkdir()
            write_json(directory / "case.json", case)
            (directory / "capture.json").write_bytes(case.get("raw_capture", encoded(case["capture"]).decode()).encode())
            (directory / "expected.json").write_bytes(case.get("raw_policy", encoded(case["policy"]).decode()).encode())
            if case["mode"] == "progress": write_json(directory / "previous.json", case["previous"])
            for name, command in drivers.items():
                argv = command + [case["mode"]]
                if case["mode"] == "progress": argv.append(str(directory / "previous.json"))
                argv += [str(directory / "capture.json"), str(directory / "expected.json")]
                completed, timed_out = bounded_process(argv, 10)
                item = {"driver": name, "case": case["name"], "kind": case["kind"], "argv": argv,
                        "process_exit": completed.returncode, "stdout": completed.stdout.decode("utf-8", "backslashreplace"),
                        "stderr": completed.stderr.decode("utf-8", "backslashreplace"), "fixture": str(directory / "case.json"), "timed_out": timed_out}
                evidence["runs"].append(item)
                if case["expected"] is None:
                    passed = completed.returncode == 2 and completed.stdout == b"" and 0 < len(completed.stderr) <= 4096
                else:
                    try:
                        decoded = strict_driver_reply(completed.stdout)
                    except (ValueError, UnicodeError):
                        decoded = None
                    passed = completed.returncode == 0 and decoded == case["expected"]
                passed = passed and not timed_out
                item["pass"] = passed
                if not passed: evidence["failures"].append(f"{name}/{case['name']}")
        evidence["fixture_count"] = len(cases)
        for name, before in evidence["driver_hashes"].items():
            if digest((HERE / name).read_bytes()) != before:
                evidence["failures"].append("driver changed during matrix: " + name)
    except Exception as exc:
        evidence["failures"].append(str(exc))
    evidence["result"] = "pass" if not evidence["failures"] else "fail"
    evidence["finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
    write_json(output / "evidence.json", evidence)
    print(json.dumps({"result": evidence["result"], "runs": len(evidence["runs"]), "failures": evidence["failures"], "evidence": str(output / "evidence.json")}))
    return 0 if not evidence["failures"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
