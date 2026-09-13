#!/usr/bin/env python3
"""Black-box C09/C10 ownership and precedence cases; standard library only."""

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import traceback

ENV = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
ENV.update(
    {
        "GIT_AUTHOR_DATE": "2000-01-01T00:00:00Z",
        "GIT_COMMITTER_DATE": "2000-01-01T00:00:00Z",
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": "/dev/null",
        "GIT_TERMINAL_PROMPT": "0",
        "TZ": "UTC",
        "LANG": "C",
        "LC_ALL": "C",
    }
)


def run(a, d, t=15):
    try:
        p = subprocess.run(a, cwd=d, env=ENV, text=True, capture_output=True, timeout=t)
        return {"argv": a, "exit": p.returncode, "stdout": p.stdout, "stderr": p.stderr}
    except subprocess.TimeoutExpired as e:

        def timeout_text(value):
            return (
                value.decode(errors="replace")
                if isinstance(value, bytes)
                else value or ""
            )

        return {
            "argv": a,
            "exit": None,
            "stdout": timeout_text(e.stdout),
            "stderr": timeout_text(e.stderr),
            "timed_out": True,
        }


def js(r):
    j = json.loads(r["stdout"])
    assert r["exit"] == j["exit"]
    return j


def must(a, d):
    r = run(a, d)
    assert r["exit"] == 0, r
    return r


def put(p, s, m=0o644):
    p.write_text(s)
    p.chmod(m)


def repo(root, name, manifest, files):
    d = root / name
    d.mkdir()
    put(d / "vise.toml", "[vise]\nversion=1\n" + manifest)
    put(
        d / ".gitignore",
        ".vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\nwitness*\nstate*\nout\nstray.safe\n",
    )
    for p, (s, m) in files.items():
        put(d / p, s, m)
    for a in (
        ["git", "-c", "init.defaultBranch=main", "init", "-q"],
        ["git", "config", "core.hooksPath", "/dev/null"],
        ["git", "config", "user.email", "x@y.invalid"],
        ["git", "config", "user.name", "Matrix"],
        ["git", "add", "."],
        ["git", "commit", "-qm", "initial"],
    ):
        must(a, d)
    return d


def commit(d, *paths):
    must(["git", "add", *paths], d)
    must(["git", "commit", "-qm", "activate"], d)


def rec(b, d):
    return run([str(b), "record", "--json"], d)


def gate(b, d):
    return run([str(b), "gate", "--json"], d)


def exact(pid, cmd, timeout=2, files=""):
    return f'[[probe]]\nid="{pid}"\nrun="sh {cmd}"\ntimeout={timeout}\n' + (
        f'files=["{files}"]\n' if files else ""
    )


def pin(pid, cmd, files=""):
    return (
        f'[[probe]]\nid="{pid}"\nrun="sh {cmd}"\ntimeout=2\ndeps=["value"]\nexpect={{stdout="{pid}.expected"'
        + (',files={out="out.expected"}' if files else "")
        + "}\n"
        + (f'files=["{files}"]\n' if files else "")
    )


def metric(i, cmd):
    return f'[[metric]]\nid="{i}"\nrun="sh {cmd}"\ndirection="down"\nenforce="no-regress"\nversion_cmd="printf v1"\n'


def assert_out(r, exit_, classes, counts=None, next_=None):
    j = js(r)
    assert j["exit"] == exit_
    assert set(j.get("classes", [])) == set(classes), (j.get("classes"), classes)
    if counts:
        for k, v in counts.items():
            assert j["counts"][k] == v, (k, j["counts"])
    if next_:
        assert j["next"]["action"] == next_
    return j


def baseline(b, d, o):
    o["record"] = rec(b, d)
    j = js(o["record"])
    assert j["exit"] == 0
    return j


def pin_files(script):
    return {
        "p.expected": ("wanted\n", 0o644),
        "p.sh": (script, 0o755),
        "mode": ("safe\n", 0o644),
        "value": ("target\n", 0o644),
    }


def pin_acceptance(d, pid="p"):
    return json.loads((d / "vise.lock").read_text())["probes"][pid]["pin"][
        "accepted_commit"
    ]


def generation_snapshot(d, with_artifact=False):
    names = ["vise.lock", "p.expected", "value"] + (
        ["out.expected"] if with_artifact else []
    )
    return {n: hashlib.sha256((d / n).read_bytes()).hexdigest() for n in names}


def pure_unmet(b, r, o):
    d = repo(r, "pure-exit127", pin("p", "p.sh"), pin_files("#!/bin/sh\nexit 127\n"))
    baseline(b, d, o)
    assert pin_acceptance(d) is None
    before = generation_snapshot(d)
    o["gate"] = gate(b, d)
    j = assert_out(o["gate"], 6, ["unmet"], {"unmet": 1}, "build")
    assert j["pins"]["passing_unaccepted_count"] == 0
    assert generation_snapshot(d) == before
    assert pin_acceptance(d) is None
    o["generation_unchanged"] = True
    o["accepted_commit_is_null"] = True


def met_pin(b, r, o):
    d = repo(r, "met-pin", pin("p", "p.sh"), pin_files("#!/bin/sh\nexit 127\n"))
    baseline(b, d, o)
    assert pin_acceptance(d) is None
    before = generation_snapshot(d)
    put(d / "p.sh", "#!/bin/sh\nprintf 'wanted\\n'\n", 0o755)
    commit(d, "p.sh")
    o["gate"] = gate(b, d)
    j = assert_out(o["gate"], 0, [], {"pass": 1}, "proceed")
    assert j["pins"]["passing_unaccepted"] == ["p"]
    assert generation_snapshot(d) == before
    assert pin_acceptance(d) is None
    o["generation_unchanged"] = True
    o["accepted_commit_is_null"] = True


def same_pin(b, r, o, kind):
    files = "out" if kind in ("tracked", "invalid") else ""
    manifest = pin("p", "p.sh", files)
    action = {
        "hard": "mkdir -p .vise; printf x > .vise/evaluator-write",
        "git": "printf x >> .git/config",
        "stray": ": > stray.out",
        "invalid": "rm -f out; ln -s value out",
        "tracked": ":",
    }[kind]
    script = f"""#!/bin/sh\n[ "$(cat mode)" = bad ] && {{ {action}; }}\nexit 127\n"""
    fs = pin_files(script)
    if files:
        fs["out.expected"] = ("data", 0o644)
    if kind == "tracked":
        fs["out"] = ("tracked\n", 0o644)
    d = repo(r, "same-pin-" + kind, manifest, fs)
    if kind == "tracked":
        must(["git", "add", "-f", "out"], d)
        must(["git", "commit", "-qm", "track artifact"], d)
        artifact_before = (d / "out").read_bytes()
        o["record"] = rec(b, d)
        j = assert_out(o["record"], 2, ["harness"], {"harness": 1}, "human")
        assert (d / "out").read_bytes() == artifact_before
        assert not (d / "vise.lock").exists()
        o["artifact_bytes_preserved"] = True
        o["baseline_absent"] = True
    else:
        baseline(b, d, o)
        assert pin_acceptance(d) is None
        before = generation_snapshot(d, kind == "invalid")
        put(d / "mode", "bad\n")
        commit(d, "mode")
        o["gate"] = gate(b, d)
        j = assert_out(o["gate"], 2, ["harness"], {"harness": 1}, "fix_probe")
        assert j["failures"]["p"]["class"] == "harness"
        assert j["pins"]["passing_unaccepted_count"] == 0
        assert generation_snapshot(d, kind == "invalid") == before
        assert pin_acceptance(d) is None
        o["generation_unchanged"] = True
        o["accepted_commit_is_null"] = True


def harness_flake(b, r, o):
    m = exact("flaky", "flaky.sh") + exact("hard", "hard.sh", 1)
    fs = {
        "mode": ("safe\n", 0o644),
        "flaky.sh": (
            '#!/bin/sh\nif [ "$(cat mode)" = bad ];then n=$(cat state.f 2>/dev/null||printf 0);n=$((1-n));printf %s $n > state.f;printf "%s\\n" $n;else printf stable;fi\n',
            0o755,
        ),
        "hard.sh": ('#!/bin/sh\n[ "$(cat mode)" = bad ]&&sleep 5\nprintf ok\n', 0o755),
    }
    d = repo(r, "harness-flake", m, fs)
    baseline(b, d, o)
    put(d / "mode", "bad\n")
    commit(d, "mode")
    o["gate"] = gate(b, d)
    assert_out(
        o["gate"], 2, ["harness", "flake"], {"harness": 1, "flaky": 1}, "fix_probe"
    )


def flake_behavior(b, r, o):
    m = exact("flaky", "flaky.sh") + exact("changed", "changed.sh")
    fs = {
        "mode": ("safe\n", 0o644),
        "value": ("old\n", 0o644),
        "flaky.sh": (
            '#!/bin/sh\nif [ "$(cat mode)" = bad ];then n=$(cat state.f 2>/dev/null||printf 0);n=$((1-n));printf %s $n > state.f;printf %s $n;else printf stable;fi\n',
            0o755,
        ),
        "changed.sh": ("#!/bin/sh\ncat value\n", 0o755),
    }
    d = repo(r, "flake-behavior", m, fs)
    baseline(b, d, o)
    put(d / "mode", "bad\n")
    put(d / "value", "new\n")
    commit(d, "mode", "value")
    o["gate"] = gate(b, d)
    assert_out(
        o["gate"],
        3,
        ["flake", "behavior"],
        {"flaky": 1, "behavior": 1},
        "quarantine_ack",
    )


def flake_metric(b, r, o):
    m = (
        exact("p", "p.sh")
        + metric("flaky_metric", "mf.sh")
        + metric("bad_metric", "mb.sh")
    )
    fs = {
        "p.sh": ("#!/bin/sh\nprintf ok\n", 0o755),
        "mode": ("safe\n", 0o644),
        "metric": ("1\n", 0o644),
        "mf.sh": (
            '#!/bin/sh\n:>witness.flaky\nif [ "$(cat mode)" = bad ];then n=$(cat state.m 2>/dev/null||printf 1);n=$((3-n));printf %s $n > state.m;printf "%s\\n" $n;else printf "1\\n";fi\n',
            0o755,
        ),
        "mb.sh": ("#!/bin/sh\n:>witness.bad\ncat metric\n", 0o755),
    }
    d = repo(r, "flake-metric", m, fs)
    baseline(b, d, o)
    (d / "witness.flaky").unlink(missing_ok=True)
    (d / "witness.bad").unlink(missing_ok=True)
    put(d / "mode", "bad\n")
    put(d / "metric", "2\n")
    commit(d, "mode", "metric")
    o["gate"] = gate(b, d)
    assert_out(
        o["gate"],
        3,
        ["flake", "metric"],
        {"flaky": 1, "metric": 1, "pass": 1},
        "quarantine_ack",
    )
    assert (d / "witness.flaky").exists() and (d / "witness.bad").exists()
    o["both_metrics_executed"] = True


def held_metric(b, r, o, behavior):
    m = (
        pin("p", "p.sh")
        + (exact("e", "e.sh") if behavior else "")
        + metric("m", "m.sh")
    )
    fs = pin_files("#!/bin/sh\nexit 127\n")
    fs["m.sh"] = ('#!/bin/sh\n:>witness.metric\nprintf "1\\n"\n', 0o755)
    if behavior:
        fs.update(
            {"e.sh": ("#!/bin/sh\ncat observed\n", 0o755), "observed": ("old\n", 0o644)}
        )
    d = repo(r, "held-" + str(behavior), m, fs)
    baseline(b, d, o)
    (d / "witness.metric").unlink(missing_ok=True)
    if behavior:
        put(d / "observed", "new\n")
        commit(d, "observed")
    o["gate"] = gate(b, d)
    assert_out(
        o["gate"],
        1 if behavior else 6,
        ["behavior", "unmet"] if behavior else ["unmet"],
        {"metric": 0, "skipped": 1},
        "revert" if behavior else "build",
    )
    assert not (d / "witness.metric").exists()
    o["metric_executed"] = False


def metric_only(b, r, o):
    d = repo(
        r,
        "metric-only",
        exact("p", "p.sh") + metric("m", "m.sh"),
        {
            "p.sh": ("#!/bin/sh\nprintf ok\n", 0o755),
            "m.sh": ("#!/bin/sh\n:>witness.metric\ncat value\n", 0o755),
            "value": ("1\n", 0o644),
        },
    )
    baseline(b, d, o)
    (d / "witness.metric").unlink(missing_ok=True)
    put(d / "value", "2\n")
    commit(d, "value")
    o["gate"] = gate(b, d)
    assert_out(o["gate"], 5, ["metric"], {"metric": 1, "pass": 1}, "revert")
    assert (d / "witness.metric").exists()


CASES = {
    "pure-exit127": pure_unmet,
    "met-pin": met_pin,
    **{
        f"same-pin-{k}": (lambda b, r, o, k=k: same_pin(b, r, o, k))
        for k in ("hard", "git", "tracked", "stray", "invalid")
    },
    "harness-flake": harness_flake,
    "flake-behavior": flake_behavior,
    "flake-metric": flake_metric,
    "behavior-unmet-metric-held": lambda b, r, o: held_metric(b, r, o, True),
    "unmet-metric-held": lambda b, r, o: held_metric(b, r, o, False),
    "metric-only": metric_only,
}


def main():
    if not __debug__:
        raise SystemExit("assertions required")
    p = argparse.ArgumentParser()
    p.add_argument("--binary", required=True, type=pathlib.Path)
    p.add_argument("--evidence-dir", type=pathlib.Path)
    p.add_argument("--case", choices=["all", *CASES], default="all")
    a = p.parse_args()
    b = a.binary.resolve()
    root = (
        a.evidence_dir.resolve()
        if a.evidence_dir
        else pathlib.Path(tempfile.mkdtemp(prefix="c09c10."))
    )
    if root.exists() and any(root.iterdir()):
        raise SystemExit("refusing nonempty evidence dir")
    root.mkdir(parents=True, exist_ok=True)
    h = hashlib.sha256(b.read_bytes()).hexdigest()
    res = {"binary": str(b), "binary_sha256": h, "case_selection": a.case, "cases": {}}
    code = 0
    try:
        res["version"] = run([str(b), "version", "--json"], root)
        assert js(res["version"])["cmd"] == "version"
        for n, f in CASES.items():
            if a.case in ("all", n):
                res["cases"][n] = {}
                f(b, root, res["cases"][n])
    except BaseException as e:
        code = 1
        res["failure"] = {
            "type": type(e).__name__,
            "message": str(e),
            "traceback": traceback.format_exc(),
        }
    finally:
        res["binary_sha256_at_end"] = hashlib.sha256(b.read_bytes()).hexdigest()
        res["ok"] = code == 0 and res["binary_sha256_at_end"] == h
        if not res["ok"] and code == 0:
            code = 1
            res["failure"] = {
                "type": "AssertionError",
                "message": "binary hash changed",
            }
        out = root / "result.json"
        out.write_text(json.dumps(res, indent=2, sort_keys=True) + "\n")
        print(json.dumps({"ok": res["ok"], "result": str(out)}))
    return code


if __name__ == "__main__":
    sys.exit(main())
