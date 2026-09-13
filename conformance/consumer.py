#!/usr/bin/env python3
"""Independent, provider-free reference consumer for the Vise capture envelope."""

import json
import math
import os
import re
import sys


FILE_LIMIT = 4 * 1024 * 1024
STREAM_LIMIT = 1024 * 1024
DIAGNOSTIC_LIMIT = 512
MAX_DEPTH = 128
HASH_RE = re.compile(r"sha256:[0-9a-f]{64}\Z")
REVISION_RE = re.compile(r"[0-9a-f]{40}\Z")
CLASSES = {"harness", "flake", "behavior", "unmet", "metric"}
VERDICTS = {"green", "red", "indeterminate"}
ACTIONS = {
    "proceed", "revert", "fix_probe", "human", "record_first",
    "quarantine_ack", "fix_invocation", "build",
}
EXIT_MAP = {
    0: ("green", {"proceed"}),
    1: ("red", {"revert"}),
    2: ("indeterminate", {"fix_probe", "human", "fix_invocation"}),
    3: ("indeterminate", {"quarantine_ack"}),
    4: ("indeterminate", {"record_first"}),
    5: ("red", {"revert"}),
    6: ("red", {"build"}),
}
BINDING_KEYS = (
    "request_id", "cmd", "argv", "root", "scope", "candidate", "manifest",
    "lock", "evaluator", "environment", "artifact", "producer",
)
COUNT_KEYS = (
    "declared", "pass", "behavior", "flaky", "harness", "metric", "unmet",
    "skipped",
)


class Refusal(Exception):
    pass


def refuse(message):
    raise Refusal(message)


def is_int(value):
    return isinstance(value, int) and not isinstance(value, bool)


def require(condition, message):
    if not condition:
        refuse(message)


def exact_keys(obj, required, optional=()):
    require(isinstance(obj, dict), "expected object")
    missing = set(required) - set(obj)
    if missing:
        refuse("missing field: " + sorted(missing)[0])


def object_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            refuse("duplicate JSON key")
        result[key] = value
    return result


def reject_constant(_value):
    refuse("non-finite JSON number")


def decode_json(data, label):
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        refuse(label + " is not UTF-8")
    try:
        value = json.loads(text, object_pairs_hook=object_pairs,
                           parse_constant=reject_constant)
    except Refusal:
        raise
    except (ValueError, json.JSONDecodeError):
        refuse(label + " is malformed JSON")
    require(isinstance(value, dict), label + " must be a JSON object")
    validate_tree(value, label)
    return value


def validate_tree(value, label):
    pending = [(value, 1)]
    while pending:
        item, depth = pending.pop()
        require(depth <= MAX_DEPTH, label + " exceeds JSON nesting bound")
        if isinstance(item, float):
            require(math.isfinite(item), label + " contains a non-finite number")
        elif isinstance(item, list):
            pending.extend((child, depth + 1) for child in item)
        elif isinstance(item, dict):
            pending.extend((child, depth + 1) for child in item.values())


def load_json(path, label):
    try:
        size = os.path.getsize(path)
        require(size <= FILE_LIMIT, label + " exceeds 4 MiB")
        with open(path, "rb") as handle:
            data = handle.read(FILE_LIMIT + 1)
    except (OSError, ValueError) as exc:
        refuse(label + " cannot be read: " + str(exc))
    require(len(data) <= FILE_LIMIT, label + " exceeds 4 MiB")
    return decode_json(data, label)


def string(value, label, nonempty=False):
    require(isinstance(value, str), label + " must be a string")
    if nonempty:
        require(bool(value), label + " must be nonempty")
    return value


def integer(value, label, minimum=None):
    require(is_int(value), label + " must be an integer")
    if minimum is not None:
        require(value >= minimum, label + " is out of range")
    return value


def hash_value(value, label):
    require(isinstance(value, str) and HASH_RE.fullmatch(value) is not None,
            label + " must be a sha256 identity")
    return value


def sorted_ids(value, label, nonempty=False, maximum=None):
    require(isinstance(value, list), label + " must be an array")
    for item in value:
        string(item, label + " item", nonempty=True)
    require(value == sorted(set(value)), label + " must be sorted and unique")
    if nonempty:
        require(bool(value), label + " must be nonempty")
    if maximum is not None:
        require(len(value) <= maximum, label + " is too long")
    return value


def validate_scope(scope, label):
    exact_keys(scope, ("kind", "probes", "metrics"))
    require(scope["kind"] in {"full", "probe"}, label + ".kind is unsupported")
    probes = sorted_ids(scope["probes"], label + ".probes", nonempty=True)
    metrics = sorted_ids(scope["metrics"], label + ".metrics")
    require(not set(probes) & set(metrics), label + " probe and metric IDs overlap")
    if scope["kind"] == "probe":
        require(len(probes) == 1 and not metrics, label + " probe scope is invalid")


def validate_argv(binding, label):
    argv = binding["argv"]
    require(isinstance(argv, list), label + ".argv must be an array")
    for arg in argv:
        string(arg, label + ".argv item")
    cmd = binding["cmd"]
    if binding["scope"]["kind"] == "full":
        require(argv in ([cmd, "--json"], [cmd,]), label + ".argv is not canonical")
        require("--json" in argv, label + ".argv lacks --json")
    else:
        probe = binding["scope"]["probes"][0]
        require(argv in ([cmd, "--json", "--probe", probe],
                         [cmd, "--probe", probe, "--json"]),
                label + ".argv is not canonical")


def validate_binding(binding, label):
    exact_keys(binding, BINDING_KEYS)
    string(binding["request_id"], label + ".request_id", nonempty=True)
    require(binding["cmd"] in {"gate", "verify"}, label + ".cmd is unsupported")
    string(binding["root"], label + ".root", nonempty=True)
    require(os.path.isabs(binding["root"]), label + ".root must be absolute")
    validate_scope(binding["scope"], label + ".scope")
    validate_argv(binding, label)
    for key in ("candidate", "manifest", "lock", "evaluator", "environment", "artifact"):
        hash_value(binding[key], label + "." + key)
    producer = binding["producer"]
    exact_keys(producer, ("version", "revision", "modified"), ("built",))
    string(producer["version"], label + ".producer.version")
    require(isinstance(producer["revision"], str) and
            REVISION_RE.fullmatch(producer["revision"]) is not None,
            label + ".producer.revision must be a 40-hex revision")
    require(type(producer["modified"]) is bool,
            label + ".producer.modified must be boolean")
    if "built" in producer:
        string(producer["built"], label + ".producer.built")


def validate_expected(expected, progress=False):
    required = ["v", "binding", "trusted_producers", "max_elapsed_ms", "max_stream_bytes"]
    if progress:
        required += ["previous_binding", "lineage"]
    exact_keys(expected, required)
    require(expected["v"] == 1 and is_int(expected["v"]), "unsupported expected version")
    validate_binding(expected["binding"], "expected.binding")
    if progress:
        validate_binding(expected["previous_binding"], "expected.previous_binding")
        exact_keys(expected["lineage"], ("parent_candidate", "child_candidate"))
        hash_value(expected["lineage"]["parent_candidate"], "lineage.parent_candidate")
        hash_value(expected["lineage"]["child_candidate"], "lineage.child_candidate")
    trusted = expected["trusted_producers"]
    require(isinstance(trusted, list) and trusted, "trusted_producers must be nonempty")
    seen = set()
    for index, item in enumerate(trusted):
        exact_keys(item, ("evaluator", "revision", "modified"))
        hash_value(item["evaluator"], "trusted producer evaluator")
        require(isinstance(item["revision"], str) and REVISION_RE.fullmatch(item["revision"]),
                "trusted producer revision is invalid")
        require(type(item["modified"]) is bool, "trusted producer modified is invalid")
        triple = (item["evaluator"], item["revision"], item["modified"])
        require(triple not in seen, "duplicate trusted producer")
        seen.add(triple)
    integer(expected["max_elapsed_ms"], "max_elapsed_ms", 1)
    bound = integer(expected["max_stream_bytes"], "max_stream_bytes", 1)
    require(bound <= STREAM_LIMIT, "max_stream_bytes exceeds 1 MiB")
    return seen


def validate_failure(value, label):
    exact_keys(value, ("class",), ("detail", "diff", "operator", "usage", "expect", "got"))
    require(value["class"] in CLASSES, label + ".class is unsupported")
    if value["class"] != "harness":
        require("usage" not in value and "operator" not in value,
                label + " has a routing marker on a non-harness class")
    for key in ("detail", "diff"):
        if key in value:
            string(value[key], label + "." + key)
    for key in ("operator", "usage"):
        if key in value:
            require(value[key] is True, label + "." + key + " may only be true")
    for key in ("expect", "got"):
        if key in value:
            observation = value[key]
            exact_keys(observation, (), ("exit", "stdout", "stderr", "files"))
            if "exit" in observation:
                integer(observation["exit"], label + "." + key + ".exit")
            for stream in ("stdout", "stderr"):
                if stream in observation:
                    hash_value(observation[stream], label + "." + key + "." + stream)
            if "files" in observation:
                require(isinstance(observation["files"], dict), label + " files must be object")
                for path, digest in observation["files"].items():
                    string(path, label + " file path", nonempty=True)
                    hash_value(digest, label + " file hash")


def validate_metric(value, label):
    exact_keys(value, ("base", "now", "delta", "direction", "enforce"))
    for key in ("base", "now", "delta"):
        number = value[key]
        require((is_int(number) or isinstance(number, float)) and
                not isinstance(number, bool) and math.isfinite(number),
                label + "." + key + " must be finite")
    require(value["direction"] in {"up", "down"}, label + ".direction unsupported")
    require(value["enforce"] in {"none", "no-regress"}, label + ".enforce unsupported")


def validate_pins(value, scope_probes):
    exact_keys(value, ("evaluated", "unmet", "unmet_count", "passing_unaccepted",
                       "passing_unaccepted_count"))
    evaluated = integer(value["evaluated"], "pins.evaluated", 0)
    unmet = sorted_ids(value["unmet"], "pins.unmet", maximum=3)
    passing = sorted_ids(value["passing_unaccepted"], "pins.passing_unaccepted", maximum=3)
    unmet_count = integer(value["unmet_count"], "pins.unmet_count", 0)
    passing_count = integer(value["passing_unaccepted_count"],
                            "pins.passing_unaccepted_count", 0)
    require(unmet_count >= len(unmet), "pins.unmet_count is too small")
    require(passing_count >= len(passing), "pins passing count is too small")
    require(evaluated >= unmet_count + passing_count,
            "pins.evaluated is too small")
    require(evaluated <= len(scope_probes), "pins.evaluated exceeds probe scope")
    require(set(unmet) | set(passing) <= set(scope_probes),
            "pin summary ID is outside probe scope")
    require(not set(unmet) & set(passing), "pin summaries overlap")
    return passing, passing_count


def binding_projection(binding):
    result = {key: binding[key] for key in BINDING_KEYS}
    result["scope"] = {
        "kind": binding["scope"]["kind"],
        "probes": binding["scope"]["probes"],
        "metrics": binding["scope"]["metrics"],
    }
    result["producer"] = {
        "version": binding["producer"]["version"],
        "revision": binding["producer"]["revision"],
        "modified": binding["producer"]["modified"],
    }
    if "built" in binding["producer"]:
        result["producer"]["built"] = binding["producer"]["built"]
    return result


def interpret(capture, expected_binding, trusted, max_elapsed, max_stream):
    exact_keys(capture, ("v", "binding", "binding_after", "termination", "elapsed_ms",
                         "stdout", "stderr"))
    require(capture["v"] == 1 and is_int(capture["v"]), "unsupported capture version")
    validate_binding(capture["binding"], "capture.binding")
    validate_binding(capture["binding_after"], "capture.binding_after")
    require(binding_projection(capture["binding"]) ==
            binding_projection(capture["binding_after"]),
            "before/after capture bindings differ")
    require(binding_projection(capture["binding"]) == binding_projection(expected_binding),
            "capture binding does not match expected binding")
    binding = capture["binding"]
    producer_tuple = (binding["evaluator"], binding["producer"]["revision"],
                      binding["producer"]["modified"])
    require(producer_tuple in trusted, "producer is not trusted")
    termination = capture["termination"]
    exact_keys(termination, ("kind",), ("code",))
    require(termination["kind"] in {"exit", "signal", "timeout", "launch_error",
                                    "collection_error"},
            "unsupported termination kind")
    require(termination["kind"] == "exit", "attempt did not exit normally")
    exact_keys(termination, ("kind", "code"))
    code = integer(termination["code"], "termination.code")
    elapsed = integer(capture["elapsed_ms"], "elapsed_ms", 0)
    require(elapsed <= max_elapsed, "attempt exceeded elapsed bound")
    stdout = string(capture["stdout"], "stdout")
    stderr = string(capture["stderr"], "stderr")
    stdout_bytes = stdout.encode("utf-8")
    stderr_bytes = stderr.encode("utf-8")
    require(len(stdout_bytes) <= max_stream and len(stderr_bytes) <= max_stream,
            "captured stream exceeds policy bound")
    require(stdout.endswith("\n") and not stdout.endswith("\n\n"),
            "stdout framing is invalid")
    reply = decode_json(stdout_bytes[:-1], "embedded stdout")
    exact_keys(reply, ("v", "cmd", "exit", "next", "verdict", "counts"),
               ("classes", "failures", "metrics", "lock", "pins"))
    require(reply["v"] == 1 and is_int(reply["v"]), "unsupported reply version")
    require(reply["cmd"] == binding["cmd"], "reply command mismatch")
    reply_exit = integer(reply["exit"], "reply.exit")
    require(reply_exit == code, "process/reply exit mismatch")
    require(reply_exit in EXIT_MAP, "unsupported gate exit")
    verdict, allowed_actions = EXIT_MAP[reply_exit]
    require(reply["verdict"] in VERDICTS and reply["verdict"] == verdict,
            "exit/verdict mismatch")
    next_value = reply["next"]
    exact_keys(next_value, ("action", "detail"))
    require(next_value["action"] in ACTIONS and next_value["action"] in allowed_actions,
            "exit/action mismatch")
    string(next_value["detail"], "next.detail")
    counts = reply["counts"]
    exact_keys(counts, COUNT_KEYS)
    for key in COUNT_KEYS:
        integer(counts[key], "counts." + key, 0)
    classes = reply.get("classes", [])
    require(isinstance(classes, list) and len(classes) == len(set(classes)),
            "classes must be unique")
    require(set(classes) <= CLASSES, "unsupported failure class")
    classes = sorted(classes)
    failures = reply.get("failures", {})
    require(isinstance(failures, dict), "failures must be an object")
    failure_classes = set()
    failure_counts = {name: 0 for name in CLASSES}
    unmet_ids = []
    for failure_id, failure in failures.items():
        string(failure_id, "failure ID", nonempty=True)
        validate_failure(failure, "failure " + failure_id)
        failure_classes.add(failure["class"])
        failure_counts[failure["class"]] += 1
        if failure["class"] == "unmet":
            unmet_ids.append(failure_id)
    require(failure_classes == set(classes), "classes do not match failures")
    require(counts["behavior"] == failure_counts["behavior"],
            "behavior count does not match failures")
    require(counts["flaky"] == failure_counts["flake"],
            "flaky count does not match failures")
    require(counts["harness"] == failure_counts["harness"],
            "harness count does not match failures")
    require(counts["metric"] == failure_counts["metric"],
            "metric count does not match failures")
    unmet_ids.sort()
    require(len(unmet_ids) == counts["unmet"], "unmet count is incomplete")
    metrics = reply.get("metrics", {})
    require(isinstance(metrics, dict), "metrics must be an object")
    for metric_id, metric in metrics.items():
        string(metric_id, "metric ID", nonempty=True)
        validate_metric(metric, "metric " + metric_id)
    if "lock" in reply:
        hash_value(reply["lock"], "reply.lock")
        require(reply["lock"] == binding["lock"], "reply lock mismatch")
    if reply_exit in {0, 1, 3, 5, 6}:
        require("lock" in reply, "completed judgment lacks lock")
        require(counts["pass"] + counts["behavior"] + counts["flaky"] +
                counts["harness"] + counts["metric"] + counts["unmet"] +
                counts["skipped"] == counts["declared"],
                "completed judgment counts do not account for declared checks")
    if reply_exit in {0, 1, 3, 4, 5, 6}:
        require(counts["declared"] == len(binding["scope"]["probes"]) +
                len(binding["scope"]["metrics"]),
                "declared count does not match bound scope")
    passing, passing_count = [], 0
    if "pins" in reply:
        passing, passing_count = validate_pins(reply["pins"], binding["scope"]["probes"])
        require(reply["pins"]["unmet_count"] == counts["unmet"],
                "pin unmet count mismatch")
        require(reply["pins"]["unmet"] == unmet_ids[:3],
                "pin unmet summary mismatch")
    else:
        require(not unmet_ids, "unmet result lacks pin summary")
    if reply_exit == 0:
        require("failures" not in reply and "classes" not in reply,
                "green reply contains failures/classes fields")
        require(all(counts[key] == 0 for key in COUNT_KEYS[2:]),
                "green reply contains failures or skips")
        require(counts["pass"] == counts["declared"], "green pass count mismatch")
    if reply_exit == 4:
        require(counts["pass"] == 0 and counts["skipped"] == counts["declared"] and
                all(counts[key] == 0 for key in ("behavior", "flaky", "harness",
                                                  "metric", "unmet")),
                "missing-baseline counts imply executed checks")
        require(all(key not in reply for key in
                    ("lock", "failures", "classes", "metrics", "pins")),
                "missing-baseline reply contains judgment fields")
    if reply_exit == 6:
        require(set(classes) == {"unmet"}, "build result is not unmet-only")
    if reply_exit == 2:
        harness_failures = [failure for failure in failures.values()
                            if failure["class"] == "harness"]
        require(harness_failures, "exit 2 lacks a harness failure")
        for failure in failures.values():
            if failure["class"] != "harness":
                require("usage" not in failure and "operator" not in failure,
                        "routing marker appears on non-harness failure")
        if any("usage" in failure for failure in harness_failures):
            routed_action = "fix_invocation"
        elif any("operator" in failure for failure in harness_failures):
            routed_action = "human"
        else:
            routed_action = "fix_probe"
        require(next_value["action"] == routed_action,
                "exit 2 action disagrees with harness routing markers")
    disposition = ({0: "proceed", 1: "revert", 5: "revert", 6: "build"}
                   .get(reply_exit, "escalate"))
    if binding["scope"]["kind"] == "probe":
        metrics_skipped = 0
    elif reply_exit == 4:
        metrics_skipped = len(binding["scope"]["metrics"])
    elif reply_exit == 2:
        metrics_skipped = None
    else:
        metrics_skipped = counts["skipped"]
    return {
        "v": 1,
        "disposition": disposition,
        "vise_exit": reply_exit,
        "verdict": verdict,
        "next_action": next_value["action"],
        "classes": classes,
        "unmet_ids": unmet_ids,
        "checks_skipped": counts["skipped"],
        "metrics_skipped": metrics_skipped,
        "metrics_checked": (binding["scope"]["kind"] == "full" and
                            reply_exit in {0, 5} and counts["skipped"] == 0),
        "passing_unaccepted_ids": passing,
        "passing_unaccepted_count": passing_count,
        "operator_acceptance_required": passing_count != 0,
        "binding": binding_projection(binding),
        "_reply": reply,
    }


def consume(capture_path, expected_path):
    capture = load_json(capture_path, "capture")
    expected = load_json(expected_path, "expected")
    trusted = validate_expected(expected)
    result = interpret(capture, expected["binding"], trusted,
                       expected["max_elapsed_ms"], expected["max_stream_bytes"])
    result.pop("_reply")
    return result


def progress(previous_path, current_path, expected_path):
    previous_capture = load_json(previous_path, "previous capture")
    current_capture = load_json(current_path, "current capture")
    expected = load_json(expected_path, "expected")
    trusted = validate_expected(expected, progress=True)
    previous = interpret(previous_capture, expected["previous_binding"], trusted,
                         expected["max_elapsed_ms"], expected["max_stream_bytes"])
    current = interpret(current_capture, expected["binding"], trusted,
                        expected["max_elapsed_ms"], expected["max_stream_bytes"])
    old_binding = previous["binding"]
    new_binding = current["binding"]
    require(expected["lineage"]["parent_candidate"] == old_binding["candidate"] and
            expected["lineage"]["child_candidate"] == new_binding["candidate"],
            "lineage does not match candidates")
    eligible_shape = all(
        item["binding"]["cmd"] == "gate" and
        item["binding"]["scope"]["kind"] == "full" and
        item["vise_exit"] == 6 and item["classes"] == ["unmet"]
        for item in (previous, current)
    )
    if not eligible_shape:
        return {"v": 1, "progress_allowed": False, "metrics_checked": False}
    for item in (previous, current):
        binding = item["binding"]
        reply = item["_reply"]
        failures = reply["failures"]
        require(set(failures) == set(item["unmet_ids"]),
                "progress failures are not exactly unmet")
        scope = binding["scope"]
        require(set(item["unmet_ids"]) <= set(scope["probes"]),
                "unmet ID is outside scope")
        counts = reply["counts"]
        require(counts["declared"] == len(scope["probes"]) + len(scope["metrics"]),
                "progress declared count mismatch")
        require(counts["skipped"] == len(scope["metrics"]),
                "progress skipped metric count mismatch")
        require(counts["pass"] == len(scope["probes"]) - len(item["unmet_ids"]),
                "progress pass count mismatch")
        require(reply["pins"]["unmet_count"] == len(item["unmet_ids"]),
                "progress pin count mismatch")
    same_identity = all(
        old_binding[key] == new_binding[key]
        for key in ("root", "argv", "scope", "manifest", "lock", "evaluator",
                    "producer", "environment")
    )
    distinct_attempt = (old_binding["request_id"] != new_binding["request_id"] and
                        old_binding["candidate"] != new_binding["candidate"])
    allowed = (same_identity and distinct_attempt and
               set(current["unmet_ids"]) < set(previous["unmet_ids"]))
    return {"v": 1, "progress_allowed": allowed, "metrics_checked": False}


def emit_refusal(message):
    safe = " ".join(str(message).split())
    encoded = ("consumer refused: " + safe).encode("utf-8", "replace")
    if len(encoded) > DIAGNOSTIC_LIMIT - 1:
        encoded = encoded[:DIAGNOSTIC_LIMIT - 4] + b"..."
    sys.stderr.buffer.write(encoded + b"\n")


def main(argv):
    try:
        if len(argv) == 4 and argv[1] == "consume":
            result = consume(argv[2], argv[3])
        elif len(argv) == 5 and argv[1] == "progress":
            result = progress(argv[2], argv[3], argv[4])
        else:
            refuse("usage: consumer.py consume CAPTURE.json EXPECTED.json; or progress PREVIOUS.json CURRENT.json EXPECTED.json")
        sys.stdout.write(json.dumps(result, sort_keys=True, separators=(",", ":")) + "\n")
        return 0
    except Refusal as exc:
        emit_refusal(exc)
        return 2
    except (OSError, UnicodeError, ValueError, TypeError, OverflowError,
            RecursionError) as exc:
        emit_refusal("invalid input: " + str(exc))
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
