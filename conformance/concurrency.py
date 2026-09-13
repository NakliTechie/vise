#!/usr/bin/env python3
"""Black-box Vise CLI concurrency conformance harness (standard library only)."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import traceback

TIMEOUT = 25
WAIT_TIMEOUT = 8


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def command(argv, cwd, timeout=TIMEOUT):
    try:
        p = subprocess.run(argv, cwd=cwd, text=True, capture_output=True, timeout=timeout)
        return {"argv": argv, "exit": p.returncode, "stdout": p.stdout, "stderr": p.stderr}
    except subprocess.TimeoutExpired as exc:
        def text(value):
            if isinstance(value, bytes):
                return value.decode(errors="replace")
            return value or ""
        return {"argv": argv, "exit": None, "stdout": text(exc.stdout), "stderr": text(exc.stderr), "timed_out": True}


def require_command(argv, cwd):
    result = command(argv, cwd)
    assert result["exit"] == 0, f"command failed: {result}"
    return result


def require_green(result, expected_cmd):
    payload = json.loads(result["stdout"])
    assert result["exit"] == payload.get("exit") == 0
    assert payload.get("cmd") == expected_cmd
    assert payload.get("verdict") == "green"
    return payload


def write(path, content, mode=0o644):
    path.write_text(content)
    path.chmod(mode)


def make_fixture(root, name, blocking=True):
    fixture = root / name
    fixture.mkdir()
    write(fixture / "vise.toml", "[vise]\nversion=1\n\n[[probe]]\nid='p'\nrun='sh probe.sh'\ntimeout=20\n")
    if blocking:
        probe = r'''#!/bin/sh
set -eu
mkdir sync/active 2>/dev/null || { printf 'OVERLAP %s\n' "$$" >> sync/trace; exit 91; }
printf 'start %s\n' "$$" >> sync/trace
if mkdir sync/block-once 2>/dev/null; then
  : > sync/first-started
  i=0
  while [ ! -f sync/release ]; do
    i=$((i+1))
    [ "$i" -lt 400 ] || { rmdir sync/active; exit 92; }
    sleep 0.05
  done
fi
printf 'end %s\n' "$$" >> sync/trace
rmdir sync/active
cat value
'''
    else:
        probe = '#!/bin/sh\nset -eu\nprintf "ran %s\\n" "$$" >> trace\ncat value\n'
    write(fixture / "probe.sh", probe, 0o755)
    write(fixture / "value", "one\n")
    write(fixture / ".gitignore", ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nsync/\ntrace\n*.stdout\n*.stderr\n")
    for argv in (
        ["git", "init", "-q"],
        ["git", "config", "user.email", "c15@example.invalid"],
        ["git", "config", "user.name", "C15 Check"],
        ["git", "add", "vise.toml", "probe.sh", "value", ".gitignore"],
        ["git", "commit", "-qm", "fixture"],
    ):
        require_command(argv, fixture)
    return fixture


def reset_sync(fixture):
    shutil.rmtree(fixture / "sync", ignore_errors=True)
    (fixture / "sync").mkdir()


def wait_for_file(path, process, timeout=WAIT_TIMEOUT):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if path.exists():
            return True
        if process.poll() is not None:
            return False
        time.sleep(0.02)
    return False


def wait_for_text(path, text, process, timeout=WAIT_TIMEOUT):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if path.exists() and text in path.read_text(errors="replace"):
            return True
        if process.poll() is not None:
            return False
        time.sleep(0.02)
    return False


class ProcessTracker:
    """Owns only sessions it creates, and always preserves captured output."""
    def __init__(self):
        self.items = []
        self.cleanup_events = []

    def spawn(self, argv, cwd, tag):
        stdout_path, stderr_path = cwd / f"{tag}.stdout", cwd / f"{tag}.stderr"
        out, err = open(stdout_path, "w"), open(stderr_path, "w")
        process = subprocess.Popen(argv, cwd=cwd, text=True, stdout=out, stderr=err, start_new_session=True)
        item = {"process": process, "out": out, "err": err, "tag": tag,
                "stdout_path": stdout_path, "stderr_path": stderr_path, "argv": argv}
        self.items.append(item)
        return item

    def finish(self, item, timeout=TIMEOUT):
        try:
            exit_code = item["process"].wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            raise AssertionError(f"{item['tag']} timed out")
        item["out"].close()
        item["err"].close()
        item["finished"] = True
        return self.snapshot(item, exit_code)

    def snapshot(self, item, exit_code=None):
        for stream in (item["out"], item["err"]):
            if not stream.closed:
                stream.flush()
        return {"argv": item["argv"], "exit": item["process"].poll() if exit_code is None else exit_code,
                "stdout": item["stdout_path"].read_text(errors="replace"),
                "stderr": item["stderr_path"].read_text(errors="replace")}

    def cleanup(self):
        # TERM live Vise leaders first so each can cancel its own probe runner.
        for item in self.items:
            if item["process"].poll() is None:
                item["process"].terminate()
                self.cleanup_events.append({"tag": item["tag"], "action": "term-parent"})
        deadline = time.monotonic() + 1.5
        while time.monotonic() < deadline and any(i["process"].poll() is None for i in self.items):
            time.sleep(0.02)
        # Every process is a session leader created above; never signal another group.
        for item in self.items:
            try:
                os.killpg(item["process"].pid, signal.SIGKILL)
                self.cleanup_events.append({"tag": item["tag"], "action": "kill-session"})
            except ProcessLookupError:
                pass
        for item in self.items:
            try:
                item["process"].wait(timeout=1)
            except subprocess.TimeoutExpired:
                self.cleanup_events.append({"tag": item["tag"], "action": "reap-failed"})
            for stream in (item["out"], item["err"]):
                if not stream.closed:
                    stream.close()
        return {"events": self.cleanup_events, "all_parents_reaped": all(i["process"].poll() is not None for i in self.items)}

    def snapshots(self):
        return {item["tag"]: self.snapshot(item) for item in self.items}


def trace_text(fixture):
    path = fixture / "sync" / "trace"
    return path.read_text(errors="replace") if path.exists() else ""


def process_alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def assert_serial_trace(trace, minimum_starts=3):
    assert "OVERLAP" not in trace
    assert trace.count("start ") >= minimum_starts
    assert trace.count("start ") == trace.count("end ")


def held_writer_readers(binary, fixture, writer, evidence, expected_lock_hash=None):
    """Exercise lock-free readers while a witnessed writer remains blocked."""
    for name in ("status", "doctor"):
        report = command([str(binary), name, "--json"], fixture, timeout=3)
        evidence[name] = report
        evidence[f"{name}_barrier"] = {
            "writer_alive": writer.poll() is None,
            "probe_start_count": trace_text(fixture).count("start "),
            "overlap_absent": "OVERLAP" not in trace_text(fixture),
            "release_absent": not (fixture / "sync" / "release").exists(),
        }
        payload = json.loads(report["stdout"])
        assert report["exit"] == payload.get("exit") == 0
        assert payload.get("cmd") == name
        assert evidence[f"{name}_barrier"]["writer_alive"], f"writer exited while {name} ran"
        assert evidence[f"{name}_barrier"]["probe_start_count"] == 1
        assert evidence[f"{name}_barrier"]["overlap_absent"]
        assert evidence[f"{name}_barrier"]["release_absent"]
    if expected_lock_hash is not None:
        evidence["expected_lock_hash"] = expected_lock_hash
        evidence["observed_status_lock_hash"] = json.loads(evidence["status"]["stdout"])["lock"]["hash"]
        assert evidence["observed_status_lock_hash"] == expected_lock_hash


def run_record_record(binary, root, result, induce_failure=False):
    fixture = make_fixture(root, "record-record", True)
    reset_sync(fixture)
    tracker = ProcessTracker()
    section = result["record_record"] = {"fixture": str(fixture)}
    try:
        first = tracker.spawn([str(binary), "record", "--json"], fixture, "record1")
        assert wait_for_file(fixture / "sync" / "first-started", first["process"]), "first record never entered probe"
        section["trace_before_second"] = trace_text(fixture)
        section["held_writer_readers"] = {}
        held_writer_readers(binary, fixture, first["process"], section["held_writer_readers"])
        if induce_failure:
            section["induced_probe_pid"] = int(section["trace_before_second"].split()[1])
            raise AssertionError("deliberate harness assertion failure")
        second = tracker.spawn([str(binary), "record", "--allow-dirty", "--i-reviewed-the-diff", "--json"], fixture, "record2")
        section["wait_note_observed"] = wait_for_text(fixture / "record2.stderr", "waiting for the run already in progress", second["process"])
        section["trace_before_release"] = trace_text(fixture)
        assert section["wait_note_observed"] and section["trace_before_release"].count("start ") == 1
        assert "OVERLAP" not in section["trace_before_release"]
        (fixture / "sync" / "release").touch()
        section["first"] = tracker.finish(first)
        section["second"] = tracker.finish(second)
        first_json, second_json = require_green(section["first"], "record"), require_green(section["second"], "record")
        assert first_json["candidate"] == second_json["candidate"]
        section["candidate_equal"] = True
        section["final_gate"] = command([str(binary), "gate", "--json"], fixture)
        require_green(section["final_gate"], "gate")
        section["trace_final"] = trace_text(fixture)
        assert_serial_trace(section["trace_final"])
        section["lock_sha256"] = sha256(fixture / "vise.lock")
    finally:
        try:
            section["processes"] = tracker.snapshots()
        finally:
            section["cleanup"] = tracker.cleanup()
            section["trace_at_cleanup"] = trace_text(fixture)
        assert section["cleanup"]["all_parents_reaped"]
        if "induced_probe_pid" in section:
            deadline = time.monotonic() + 2
            while process_alive(section["induced_probe_pid"]) and time.monotonic() < deadline:
                time.sleep(0.02)
            section["induced_probe_pid_alive_after_cleanup"] = process_alive(section["induced_probe_pid"])
            assert not section["induced_probe_pid_alive_after_cleanup"]
            assert section["cleanup"]["all_parents_reaped"]


def run_record_gate(binary, root, result):
    fixture = make_fixture(root, "record-gate", True)
    reset_sync(fixture)
    (fixture / "sync" / "release").touch()
    old = command([str(binary), "record", "--json"], fixture)
    old_payload = require_green(old, "record")
    require_command(["git", "add", "vise.lock", ".vise/blobs"], fixture)
    require_command(["git", "commit", "-qm", "baseline"], fixture)
    reset_sync(fixture)
    write(fixture / "value", "two\n")
    require_command(["git", "add", "value"], fixture)
    require_command(["git", "commit", "-qm", "new generation"], fixture)
    tracker = ProcessTracker()
    section = result["record_gate"] = {"fixture": str(fixture), "old_record": old}
    try:
        writer = tracker.spawn([str(binary), "record", "--i-reviewed-the-diff", "--json"], fixture, "record")
        assert wait_for_file(fixture / "sync" / "first-started", writer["process"]), "record never entered probe"
        section["held_writer_readers"] = {}
        held_writer_readers(binary, fixture, writer["process"], section["held_writer_readers"], old_payload["lock"])
        gate = tracker.spawn([str(binary), "gate", "--json"], fixture, "gate")
        section["wait_note_observed"] = wait_for_text(fixture / "gate.stderr", "waiting for the run already in progress", gate["process"])
        section["trace_before_release"] = trace_text(fixture)
        assert section["wait_note_observed"] and section["trace_before_release"].count("start ") == 1
        assert "OVERLAP" not in section["trace_before_release"]
        (fixture / "sync" / "release").touch()
        section["record"] = tracker.finish(writer)
        section["waiting_gate"] = tracker.finish(gate)
        require_green(section["record"], "record")
        require_green(section["waiting_gate"], "gate")
        section["final_gate"] = command([str(binary), "gate", "--json"], fixture)
        require_green(section["final_gate"], "gate")
        section["trace_final"] = trace_text(fixture)
        assert_serial_trace(section["trace_final"])
        section["lock_sha256"] = sha256(fixture / "vise.lock")
    finally:
        try:
            section["processes"] = tracker.snapshots()
        finally:
            section["cleanup"] = tracker.cleanup()
            section["trace_at_cleanup"] = trace_text(fixture)
        assert section["cleanup"]["all_parents_reaped"]


def run_liveness(binary, root, result):
    """Known-true control proving declared commands execute before contention."""
    fixture = make_fixture(root, "liveness", False)
    record = command([str(binary), "record", "--json"], fixture)
    gate = command([str(binary), "gate", "--json"], fixture)
    require_green(record, "record")
    require_green(gate, "gate")
    trace = (fixture / "trace").read_text(errors="replace")
    assert trace.count("ran ") >= 2
    result["liveness"] = {"fixture": str(fixture), "record": record, "gate": gate, "trace": trace}


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--evidence-dir", type=Path)
    parser.add_argument("--pair", choices=("both", "record-record", "record-gate"), default="both")
    parser.add_argument("--induce-cleanup-failure", action="store_true", help="fail after a blocked probe starts; requires record-record selection")
    return parser.parse_args()


def main():
    if not __debug__:
        raise SystemExit("refusing Python optimization mode: assertions are required")
    args = parse_args()
    args.binary = args.binary.expanduser().resolve()
    if args.evidence_dir:
        args.evidence_dir = args.evidence_dir.expanduser().resolve()
    assert args.binary.is_file(), f"binary not found: {args.binary}"
    if args.induce_cleanup_failure:
        assert args.pair in ("both", "record-record"), "cleanup failure requires record-record"
    if args.evidence_dir:
        root = args.evidence_dir
        if root.exists() and any(root.iterdir()):
            raise SystemExit(f"refusing nonempty evidence directory: {root}")
        root.mkdir(parents=True, exist_ok=True)
    else:
        root = Path(tempfile.mkdtemp(prefix="vise-c15-evidence."))
    initial_hash = sha256(args.binary)
    result = {"schema": 1, "root": str(root), "binary": str(args.binary), "binary_sha256": initial_hash,
              "pair_selection": args.pair, "induced_cleanup_failure": args.induce_cleanup_failure}
    exit_code = 0
    try:
        result["version"] = command([str(args.binary), "version", "--json"], root)
        version = json.loads(result["version"]["stdout"])
        assert result["version"]["exit"] == version.get("exit") == 0 and version.get("cmd") == "version"
        run_liveness(args.binary, root, result)
        if args.pair in ("both", "record-record"):
            run_record_record(args.binary, root, result, args.induce_cleanup_failure)
        if args.pair in ("both", "record-gate") and not args.induce_cleanup_failure:
            run_record_gate(args.binary, root, result)
        if args.induce_cleanup_failure:
            raise AssertionError("induced failure unexpectedly returned")
        result["binary_sha256_at_end"] = sha256(args.binary)
        result["binary_hash_unchanged"] = result["binary_sha256_at_end"] == initial_hash
        assert result["binary_hash_unchanged"], "binary hash changed during run"
        result["ok"] = True
    except BaseException as exc:
        exit_code = 1
        result["ok"] = False
        result["failure"] = {"type": type(exc).__name__, "message": str(exc), "traceback": traceback.format_exc()}
    finally:
        result["binary_sha256_at_end"] = sha256(args.binary)
        result["binary_hash_unchanged"] = result["binary_sha256_at_end"] == initial_hash
        if not result["binary_hash_unchanged"] and result.get("ok") is not False:
            exit_code = 1
            result["ok"] = False
            result["failure"] = {"type": "AssertionError", "message": "binary hash changed during run", "traceback": ""}
        result_path = root / "result.json"
        result_path.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
        print(json.dumps({"ok": result.get("ok", False), "result": str(result_path), "root": str(root)}, sort_keys=True))
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
