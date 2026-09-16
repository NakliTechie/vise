"""Finite local replacement/restart witnesses for SESSION rows 11–15.

These exercise controller crash seams and actual retained files/Git, not Docker,
acceptance, execution containment, physical power loss, or concurrent writers.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from host.bundle import BundleError, BundleLimits, SourceEntry, build_bundle
from host.git_identity import GitRunner
from host.operator import build_operator
from host.session import (
    SessionError, advance_operator, initialize_session, inspect_historical,
    materialize_candidate, observe_generation, open_session, reconcile_materialization,
)
from host import session as s
from host import session_transaction as tx
from host.storage import StorageError
from host.test_session import GIT, IDENTITY, operator


PARTIAL_CHILD = r'''
import hashlib, json, os, sys
from pathlib import Path
from host.bundle import decode_bundle
from host.git_identity import GitRunner
from host.session import open_session, materialize_candidate
root, executable, home, scenario = map(str, sys.argv[1:])
root = Path(root)
git = GitRunner(Path(executable), Path(home))
session = open_session(root, git=git)
candidate = decode_bundle(sys.stdin.buffer.read())
original = os.write
def interrupted(fd, data):
    if scenario == 'install':
        suffix = hashlib.sha256(b'src/a').hexdigest()
        paths = list(root.glob('.vise-host/recovery/*/install/' + suffix))
    elif scenario == 'terminal':
        paths = list(root.glob('.vise-host/outcomes/*.new'))
    elif scenario == 'copy':
        suffix = hashlib.sha256(b'requested/src/a').hexdigest()
        paths = list(root.glob('.vise-host/recovery/*/copies/' + suffix))
    elif scenario == 'current':
        paths = [root / '.vise-host/session.json.new']
    elif scenario == 'index-lock':
        paths = [root / '.git/index.lock']
    elif scenario == 'chronology':
        paths = [root / '.vise-host/operations.jsonl']
    else:
        intent = root / '.vise-host/intents/materialize.json'
        phase = 'PREPARING' if scenario == 'intent-replacing' else 'REPLACING'
        paths = [Path(str(intent) + '.new')] if intent.exists() and json.loads(intent.read_bytes())['phase'] == phase else []
    opened = os.fstat(fd)
    for path in paths:
        if path.exists():
            named = path.stat()
            if (opened.st_dev, opened.st_ino) == (named.st_dev, named.st_ino):
                count = original(fd, data[:max(1, len(data)//2)])
                original(1, ('PARTIAL ' + scenario + ' ' + str(count) + '\n').encode())
                os._exit(91)
    return original(fd, data)
os.write = interrupted
materialize_candidate(session, candidate)
raise AssertionError('partial write seam was not reached')
'''

REOPEN_CHILD = r'''
import json, sys
from dataclasses import asdict
from pathlib import Path
from host.git_identity import GitRunner
from host.session import open_session, observe_generation
root, executable, home = map(Path, sys.argv[1:])
session = open_session(root, git=GitRunner(executable, home))
result = observe_generation(session)
print(json.dumps(asdict(result), sort_keys=True))
'''


class Crash(BaseException):
    pass


class RecoveryPathBoundsTests(unittest.TestCase):
    def test_registered_prefix_does_not_reduce_public_path_bound(self):
        # Exercise canonical/path guards, not platform deep-path or Git support.
        path = "/".join(["x" * 127] * 32) + "x"
        self.assertEqual(BundleLimits().max_path_bytes, len(path.encode("utf-8")))
        entry = SourceEntry(path, b"bounded")
        self.assertEqual(path, build_bundle((entry,)).entries[0].path)
        self.assertEqual(path, build_operator((entry,), generation=0).files[0].path)
        with tempfile.TemporaryDirectory(prefix="vise-recovery-path-bound-") as temporary:
            root = Path(temporary)
            root.chmod(0o700)
            with s._session_storage(root) as owned:
                for side in ("prior", "requested"):
                    registered = ".vise-host/recovery/" + "1" * 32 + "/" + side + "/" + path
                    self.assertEqual(registered.split("/"), owned._parts(registered))
                self.assertEqual(4159, len(registered.encode("utf-8")))
                with self.assertRaises(StorageError):
                    owned._parts(registered + "x")

    def test_public_path_bound_is_not_expanded(self):
        path = "/".join(["x" * 127] * 32) + "xx"
        self.assertEqual(BundleLimits().max_path_bytes + 1, len(path.encode("utf-8")))
        entry = SourceEntry(path, b"overlong")
        with self.assertRaises(BundleError):
            build_bundle((entry,))
        with self.assertRaises(BundleError):
            build_operator((entry,), generation=0)


class RecoveryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="vise-replacement-fixture-")
        cls.base = Path(cls.temp.name)
        cls.template = cls.base / "template"
        cls.git = GitRunner(GIT, cls.base / "git-home")
        cls.op = operator(0, (SourceEntry("vise.toml", b"operator\n"),))
        initialize_session(cls.template, cls.op, git=cls.git, identity=IDENTITY)
        cls.prior = build_bundle((SourceEntry("src/a", b"prior\x00"), SourceEntry("gone/x", b"old", True)))
        cls.next = build_bundle((SourceEntry("src/a", b"requested\xff", True), SourceEntry("new/b", b"new")))
        materialize_candidate(open_session(cls.template, git=cls.git), cls.prior)
        (cls.template / ".vise/tmp").mkdir(parents=True)
        (cls.template / ".vise/tmp/sentinel").write_bytes(b"scratch")
        (cls.template / ".vise/journal.jsonl").write_bytes(b"journal")
        (cls.template / ".vise/run.lock").write_bytes(b"run lock")

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def setUp(self):
        self.temp_case = tempfile.TemporaryDirectory(prefix="vise-replacement-case-")
        self.addCleanup(self.temp_case.cleanup)
        self.root = Path(self.temp_case.name) / "session"
        shutil.copytree(self.template, self.root)

    def reset(self):
        shutil.rmtree(self.root)
        shutil.copytree(self.template, self.root)

    def session(self):
        return open_session(self.root, git=self.git)

    def crash(self, point, *, operation=None):
        session = self.session()
        seen = []

        def inject(label):
            seen.append(label)
            if label == point:
                raise Crash(label)

        with patch.object(tx, "_checkpoint", inject), self.assertRaises(Crash):
            if operation is None:
                materialize_candidate(session, self.next)
            else:
                operation(session)
        self.assertIn(point, seen)
        return session

    def intent(self):
        return json.loads((self.root / tx.INTENT).read_bytes())

    def snapshot_live(self):
        paths = ("src/a", "gone/x", "new/b", ".git/index", ".git/HEAD",
                 ".git/refs/heads/vise-host", ".vise-host/session.json")
        return {p: ((self.root / p).read_bytes(), (self.root / p).stat().st_mode)
                for p in paths if (self.root / p).is_file()}

    def assert_restarted(self, candidate=None):
        candidate = candidate or self.next
        one = self.session()
        first = observe_generation(one)
        two = self.session()
        self.assertEqual(first, observe_generation(two))
        self.assertEqual(candidate, two.candidate)
        self.assertFalse((self.root / tx.INTENT).exists())
        self.assertFalse(tuple((self.root / ".vise-host/recovery").iterdir()))
        history = (self.root / tx.CHRONOLOGY).read_bytes().splitlines()
        qs = [json.loads(line)["q"] for line in history]
        self.assertEqual(len(qs), len(set(qs)))
        for p, data in (("tmp/sentinel", b"scratch"), ("journal.jsonl", b"journal"), ("run.lock", b"run lock")):
            self.assertEqual(data, (self.root / ".vise" / p).read_bytes())
        return one

    def test_every_recorded_durable_boundary_restarts_twice(self):
        points = []
        with patch.object(tx, "_checkpoint", points.append):
            materialize_candidate(self.session(), self.next)
        self.assertGreater(len(points), 35)
        self.assertEqual(len(points), len(set(points)))
        for point in points:
            with self.subTest(point=point):
                self.reset()
                before = self.snapshot_live()
                self.crash(point)
                if point in points[:points.index("after-replacing")]:
                    self.assertEqual(before, self.snapshot_live())
                if point == "before-ownership-intent":
                    self.assert_restarted(self.prior)
                else:
                    self.assert_restarted()

    def test_operator_replacement_restarts_and_remains_monotonic(self):
        replacement = operator(1, (SourceEntry("vise.toml", b"next O"), SourceEntry("spec/new", b"spec")))
        for point in ("after-ownership-intent", "remove:vise.toml", "after-index-publication",
                      "after-head-cas", "after-current-publication", "after-terminal-outcome", "after-cleanup"):
            with self.subTest(point=point):
                self.reset()
                def advance(session):
                    advance_operator(session, expected_identity=session.operator.identity,
                                     expected_generation=0, replacement=replacement)
                self.crash(point, operation=advance)
                one = self.assert_restarted(self.prior)
                self.assertEqual(replacement, one.operator)
                with self.assertRaises(SessionError):
                    advance_operator(one, expected_identity=self.op.identity, expected_generation=0,
                                     replacement=replacement)

    def test_recovery_tamper_refuses_before_and_after_deletion(self):
        for point in ("before-replacing", "remove:src/a"):
            for side in ("prior", "requested"):
                for corruption in ("bytes", "missing", "mode", "link", "extra"):
                    with self.subTest(point=point, side=side, corruption=corruption):
                        self.reset()
                        self.crash(point)
                        intent = self.intent()
                        directory = self.root / intent["recovery"] / side
                        path = directory / "src/a"
                        if corruption == "bytes":
                            path.write_bytes(b"tampered")
                        elif corruption == "missing":
                            path.unlink()
                        elif corruption == "mode":
                            path.chmod(0o600)
                        elif corruption == "link":
                            path.unlink()
                            path.symlink_to(self.template / "src/a")
                        else:
                            (directory / "extra").write_bytes(b"unregistered")
                        before = self.snapshot_live()
                        # PREPARING missing copies are legitimately resumable; all other
                        # corruption and missing REPLACING backups must refuse unchanged.
                        if corruption == "missing" and point == "before-replacing":
                            self.assert_restarted()
                        else:
                            with self.assertRaises(SessionError):
                                self.session()
                            self.assertEqual(before, self.snapshot_live())
                            self.assertTrue((self.root / tx.INTENT).exists())

    def test_manifest_tamper_and_unregistered_recovery_refuse(self):
        for case in ("manifest", "unregistered", "unknown-cleanup"):
            with self.subTest(case=case):
                self.reset()
                self.crash("after-cleanup" if case == "unknown-cleanup" else "after-replacing")
                intent = self.intent()
                recovery = self.root / intent["recovery"]
                if case == "manifest":
                    (recovery / "manifest.json").write_bytes(b"{}\n")
                elif case == "unregistered":
                    (self.root / ".vise-host/recovery/unregistered").mkdir()
                else:
                    (recovery / "surprise").write_bytes(b"unknown")
                before = self.snapshot_live()
                with self.assertRaises(SessionError):
                    self.session()
                self.assertEqual(before, self.snapshot_live())
                self.assertTrue(recovery.exists())

    def test_terminal_outcome_never_reopens_replacement(self):
        self.crash("after-terminal-outcome")
        intent = self.intent()
        # Durable completion is authoritative even if cleanup already removed backups.
        shutil.rmtree(self.root / intent["recovery"] / "prior")
        with patch.object(tx, "_finish_replacement", side_effect=AssertionError("replacement reopened")):
            self.assert_restarted()

    def test_torn_terminal_line_repairs_only_exact_durable_prefix(self):
        for bad in (False, True):
            with self.subTest(bad=bad):
                self.reset()
                self.crash("after-terminal-outcome")
                value = self.intent()
                encoded = (self.root / ".vise-host/outcomes" / value["q"]).read_bytes()
                path = self.root / tx.CHRONOLOGY
                prefix = encoded[:len(encoded)//2] if not bad else b'{"not_the_outcome":'
                with path.open("ab") as stream:
                    stream.write(prefix)
                before = path.read_bytes()
                if bad:
                    with self.assertRaises(SessionError):
                        self.session()
                    self.assertEqual(before, path.read_bytes())
                else:
                    self.assert_restarted()
                    self.assertTrue(path.read_bytes().startswith(before))
                    self.assertTrue(path.read_bytes().endswith(encoded))

    def test_duplicate_terminal_q_is_noop_conflicting_q_refuses(self):
        for conflict in (False, True):
            with self.subTest(conflict=conflict):
                self.reset()
                self.crash("after-terminal-append")
                value = self.intent()
                encoded = (self.root / ".vise-host/outcomes" / value["q"]).read_bytes()
                if conflict:
                    record = json.loads(encoded)
                    record["outcome"] = "different"
                    with (self.root / tx.CHRONOLOGY).open("ab") as stream:
                        stream.write(s._canonical_json(record))
                    before = self.snapshot_live()
                    with self.assertRaises(SessionError):
                        self.session()
                    self.assertEqual(before, self.snapshot_live())
                else:
                    before = (self.root / tx.CHRONOLOGY).read_bytes()
                    self.assert_restarted()
                    self.assertEqual(before, (self.root / tx.CHRONOLOGY).read_bytes())

    def test_incomplete_preparation_copy_resumes_from_registered_envelope(self):
        self.crash("copy:prior/gone/x")
        value = self.intent()
        partial = self.root / tx._copy_stage(value, value["recovery"] + "/requested/src/a")
        partial.parent.mkdir(exist_ok=True)
        partial.write_bytes(b"requested")
        partial.chmod(0o755)
        before = self.snapshot_live()
        self.assertEqual(before, self.snapshot_live())
        self.assert_restarted()

    def test_unknown_git_metadata_refuses_before_git_runner_is_called(self):
        self.crash("remove:src/a")
        path = self.root / ".git/objects/info/alternates"
        path.write_bytes(b"/untrusted/objects\n")
        before = self.snapshot_live()
        with patch.object(self.git, "run", side_effect=AssertionError("untrusted Git read")):
            with self.assertRaises(SessionError):
                self.session()
        self.assertEqual(before, self.snapshot_live())

    def test_missing_prior_git_object_refuses_before_construct_can_repair(self):
        self.crash("remove:src/a")
        blob = s._git_digest(b"blob", b"prior\x00")
        (self.root / ".git/objects" / blob[:2] / blob[2:]).unlink()
        before = self.snapshot_live()
        with patch.object(tx, "construct_assembly", side_effect=AssertionError("repair before validation")):
            with self.assertRaises(SessionError):
                self.session()
        self.assertEqual(before, self.snapshot_live())

    def test_replacement_respects_changed_ambient_umask(self):
        previous = os.umask(0o077)
        try:
            materialize_candidate(self.session(), self.next)
            self.assert_restarted()
        finally:
            os.umask(previous)

    def test_content_address_alias_and_wrong_envelope_mode_refuse(self):
        session = self.session()
        original = self.root / ".vise-host/generations" / self.prior.identity[7:]
        for bad_mode in (False, True):
            saved = original.read_bytes()
            if bad_mode:
                original.chmod(0o644)
            else:
                original.write_bytes(self.next.encoded)
            with self.assertRaises(SessionError):
                inspect_historical(session, self.prior.identity, self.op.identity)
            original.write_bytes(saved)
            original.chmod(0o600)

    def test_expected_generation_rejects_bool_float_and_stale_handle(self):
        session = self.session()
        replacement = operator(1, (SourceEntry("vise.toml", b"operator\n"),))
        for bad in (False, 0.0):
            before = self.snapshot_live()
            with self.assertRaises(SessionError):
                advance_operator(session, expected_identity=session.operator.identity,
                                 expected_generation=bad, replacement=replacement)
            self.assertEqual(before, self.snapshot_live())
        stale = self.session()
        materialize_candidate(session, self.next)
        with self.assertRaises(SessionError):
            observe_generation(stale)

    def test_explicit_reconcile_refreshes_interrupted_handle(self):
        session = self.crash("after-head-cas")
        result = reconcile_materialization(session)
        self.assertEqual(result, observe_generation(session))
        self.assertEqual(self.next, session.candidate)

    def test_partial_initialization_remains_explicit_refusal(self):
        root = Path(self.temp_case.name) / "initial"
        with patch.object(s, "initialize_repository", side_effect=Crash):
            with self.assertRaises(Crash):
                initialize_session(root, self.op, git=self.git, identity=IDENTITY)
        with self.assertRaises(SessionError):
            open_session(root, git=self.git)
        self.assertTrue((root / ".vise-host/intent.json").is_file())

    def test_retained_terminal_corruption_refuses_reopen(self):
        materialize_candidate(self.session(), self.next)
        outcomes = self.root / ".vise-host/outcomes"
        outcome = next(outcomes.iterdir())
        outcome.write_bytes(b"{}\n")
        with self.assertRaises(SessionError):
            self.session()

    def test_mutated_handle_metadata_refuses_without_registering_intent(self):
        session = self.session()
        session.session_id = "0" * 32
        before = self.snapshot_live()
        with self.assertRaises(SessionError):
            materialize_candidate(session, self.next)
        self.assertEqual(before, self.snapshot_live())
        self.assertFalse((self.root / tx.INTENT).exists())

    def test_data_suffix_paths_are_disjoint_from_copy_stages_and_restart(self):
        candidates = (
            build_bundle((SourceEntry("a", b"one"), SourceEntry("a.new", b"two"))),
            build_bundle((SourceEntry("a", b"one"), SourceEntry("a.new/child", b"two"))),
        )
        for candidate in candidates:
            for point in (None, "copy:requested/a", "before-replacing", "install:a", "after-cleanup"):
                with self.subTest(paths=[e.path for e in candidate.entries], point=point):
                    self.reset()
                    if point is None:
                        materialize_candidate(self.session(), candidate)
                    else:
                        def inject(label):
                            if label == point:
                                raise Crash(label)
                        with patch.object(tx, "_checkpoint", inject), self.assertRaises(Crash):
                            materialize_candidate(self.session(), candidate)
                    self.assert_restarted(candidate)
                    for entry in candidate.entries:
                        self.assertEqual(entry.data, (self.root / entry.path).read_bytes())
                    # The same paths are now prior backup entries on the next operation.
                    self.crash("copy:prior/a")
                    self.assert_restarted()

    def test_reserved_looking_candidate_names_materialize_and_restart(self):
        names = ("manifest.json", "data/manifest.json", "prior/payload", "data/prior/payload")
        prior = build_bundle(tuple(SourceEntry(name, b"prior " + name.encode()) for name in names))
        requested = build_bundle(tuple(SourceEntry(name, b"next " + name.encode(), True) for name in names))
        for point in (None, "copy:prior/data/manifest.json", "copy:requested/data/prior/payload"):
            with self.subTest(point=point):
                self.reset()
                materialize_candidate(self.session(), prior)
                self.assert_restarted(prior)
                if point is None:
                    materialize_candidate(self.session(), requested)
                else:
                    self.crash(point, operation=lambda session: materialize_candidate(session, requested))
                result = self.assert_restarted(requested)
                self.assertEqual(self.op, result.operator)

    def test_reserved_looking_operator_names_advance_and_restart(self):
        names = ("manifest.json", "data/manifest.json", "prior/payload", "data/prior/payload")
        prior = operator(1, tuple(SourceEntry(name, b"prior " + name.encode()) for name in names))
        requested = operator(2, tuple(SourceEntry(name, b"next " + name.encode(), True) for name in names))
        for point in (None, "copy:prior/manifest.json", "copy:requested/prior/payload"):
            with self.subTest(point=point):
                self.reset()
                session = self.session()
                advance_operator(session, expected_identity=self.op.identity,
                                 expected_generation=0, replacement=prior)
                self.assertEqual(prior, self.assert_restarted(self.prior).operator)

                def advance(session):
                    advance_operator(session, expected_identity=prior.identity,
                                     expected_generation=1, replacement=requested)

                if point is None:
                    advance(self.session())
                else:
                    self.crash(point, operation=advance)
                self.assertEqual(requested, self.assert_restarted(self.prior).operator)

    def test_missing_or_changed_session_lock_refuses_without_mutation(self):
        for corruption in ("missing", "bytes", "mode"):
            for operation in ("open", "observe", "materialize", "advance", "historical", "reconcile"):
                with self.subTest(corruption=corruption, operation=operation):
                    self.reset()
                    session = self.session()
                    lock = self.root / ".vise-host/session.lock"
                    if corruption == "missing":
                        lock.unlink()
                    elif corruption == "bytes":
                        lock.write_bytes(b"changed")
                    else:
                        lock.chmod(0o644)
                    before = self.snapshot_live()
                    with self.assertRaises(SessionError):
                        if operation == "open":
                            self.session()
                        elif operation == "observe":
                            observe_generation(session)
                        elif operation == "materialize":
                            materialize_candidate(session, self.next)
                        elif operation == "advance":
                            advance_operator(session, expected_identity=self.op.identity, expected_generation=0,
                                             replacement=operator(1))
                        elif operation == "historical":
                            inspect_historical(session, self.prior.identity, self.op.identity)
                        else:
                            reconcile_materialization(session)
                    self.assertEqual(before, self.snapshot_live())
                    if corruption == "missing":
                        self.assertFalse(lock.exists())
                    elif corruption == "bytes":
                        self.assertEqual(b"changed", lock.read_bytes())
                    else:
                        self.assertEqual(0o644, lock.stat().st_mode & 0o777)

    def test_removing_last_operator_blob_preserves_vise_runtime_directory(self):
        session = self.session()
        with_blob = operator(1, (
            SourceEntry("vise.toml", b"operator\n"),
            SourceEntry(".vise/blobs/" + "a" * 64, b"operator blob"),
        ))
        advance_operator(session, expected_identity=session.operator.identity,
                         expected_generation=0, replacement=with_blob)
        without_blob = operator(2, (SourceEntry("vise.toml", b"operator\n"),))
        advance_operator(session, expected_identity=session.operator.identity,
                         expected_generation=1, replacement=without_blob)
        self.assertEqual(without_blob, self.assert_restarted(self.prior).operator)
        self.assertFalse((self.root / ".vise/blobs").exists())

    def test_partial_terminal_stage_resumes_without_reopening_replacement(self):
        self.crash("before-terminal-publication")
        value = self.intent()
        stage = self.root / ".vise-host/outcomes" / (value["q"] + ".new")
        full = stage.read_bytes()
        stage.write_bytes(full[:len(full)//2])
        with patch.object(tx, "_finish_replacement", side_effect=AssertionError("replacement reopened")):
            self.assert_restarted()
        self.assertEqual(full, stage.with_suffix("").read_bytes())

    def test_terminal_live_prior_only_addition_refuses_and_retains_evidence(self):
        for point in ("before-terminal-publication", "after-terminal-outcome", "after-cleanup"):
            with self.subTest(point=point):
                self.reset()
                self.crash(point)
                value = self.intent()
                (self.root / "gone").mkdir()
                (self.root / "gone/x").write_bytes(b"old")
                before = self.snapshot_live()
                intent = (self.root / tx.INTENT).read_bytes()
                recovery = self.root / value["recovery"]
                retained = {str(path.relative_to(recovery)): path.read_bytes()
                            for path in recovery.rglob("*") if path.is_file()}
                with self.assertRaises(SessionError):
                    self.session()
                self.assertEqual(before, self.snapshot_live())
                self.assertEqual(intent, (self.root / tx.INTENT).read_bytes())
                self.assertEqual(retained, {str(path.relative_to(recovery)): path.read_bytes()
                                           for path in recovery.rglob("*") if path.is_file()})

    def test_actual_partial_write_abrupt_exit_and_fresh_restart_twice(self):
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1", PYTHONPATH=str(Path(__file__).resolve().parents[1]))
        args = (str(self.root), str(self.git.executable), str(self.git.private_home))
        for scenario in ("copy", "install", "terminal", "current", "intent-replacing", "intent-cleanup", "chronology", "index-lock"):
            with self.subTest(scenario=scenario):
                self.reset()
                before = self.snapshot_live()
                expected = s._compute_historical_identity(self.next, self.op, self.session().bootstrap, IDENTITY)
                child = subprocess.run((sys.executable, "-B", "-c", PARTIAL_CHILD, *args, scenario),
                                       input=self.next.encoded, capture_output=True, env=env, timeout=90)
                self.assertEqual(91, child.returncode, child.stderr.decode())
                self.assertTrue(child.stdout.startswith(("PARTIAL " + scenario).encode()))
                live = self.root / "src/a"
                if live.exists():
                    self.assertIn(live.read_bytes(), (b"prior\x00", b"requested\xff"))
                if scenario in ("copy", "intent-replacing"):
                    self.assertEqual(before, self.snapshot_live())
                results = []
                for _ in range(2):
                    reopened = subprocess.run((sys.executable, "-B", "-c", REOPEN_CHILD, *args),
                                              capture_output=True, env=env, timeout=90)
                    self.assertEqual(0, reopened.returncode, reopened.stderr.decode())
                    results.append(json.loads(reopened.stdout))
                self.assertEqual(results[0], results[1])
                self.assertEqual(s._identity_dict(expected), results[0]["identity"])
                self.assert_restarted()

    def test_chronology_closed_schema_refuses_earlier_malformed_rows_without_rewrite(self):
        original = (self.root / tx.CHRONOLOGY).read_bytes()
        valid = json.loads(original.splitlines()[0])
        cases = [{"q": "0" * 32}]
        for field, value in (("extra", None), ("operation", "unknown"), ("phase", "PREPARING"),
                             ("outcome", False), ("q", True), ("q", "bad"), ("requested", [])):
            cases.append(dict(valid, **{field: value}))
        for field, value in (("candidate", "0" * 64), ("operator", True), ("tree", "bad"),
                             ("commit", 1), ("bootstrap", "g" * 40), ("extra", "x")):
            record = dict(valid)
            record["requested"] = dict(valid["requested"], **{field: value})
            cases.append(record)
        for number, record in enumerate(cases):
            with self.subTest(number=number):
                data = s._canonical_json(record) + original
                (self.root / tx.CHRONOLOGY).write_bytes(data)
                before = self.snapshot_live()
                with self.assertRaises(SessionError):
                    self.session()
                self.assertEqual(data, (self.root / tx.CHRONOLOGY).read_bytes())
                self.assertEqual(before, self.snapshot_live())
        (self.root / tx.CHRONOLOGY).write_bytes(original)
        self.assertEqual(self.prior, self.session().candidate)

    def test_chronology_legitimate_forms_and_authority_fields(self):
        session = self.session()
        advance_operator(session, expected_identity=session.operator.identity, expected_generation=0,
                         replacement=operator(1, (SourceEntry("vise.toml", b"operator\n"),)))
        data = (self.root / tx.CHRONOLOGY).read_bytes()
        s._validate_json_lines(data)
        rows = [json.loads(line) for line in data.splitlines()]
        self.assertEqual(["initialize", "materialize", "advance-operator"], [row["operation"] for row in rows])
        for field, value in (("refusal", False), ("prior", None)):
            bad = dict(rows[-1], **{field: value})
            with self.assertRaises(SessionError):
                s._validate_json_lines(s._canonical_json(bad))
        bad = dict(rows[-1])
        bad["requested"] = dict(bad["requested"], candidate="sha256:" + "0" * 64)
        with self.assertRaises(SessionError):
            s._validate_json_lines(s._canonical_json(bad))
        self.assertEqual(session.operator, self.session().operator)

    def test_partial_terminal_stage_still_requires_complete_backups(self):
        self.crash("before-terminal-publication")
        value = self.intent()
        stage = self.root / ".vise-host/outcomes" / (value["q"] + ".new")
        stage.write_bytes(stage.read_bytes()[:10])
        (self.root / value["recovery"] / "prior/src/a").unlink()
        before = self.snapshot_live()
        with self.assertRaises(SessionError):
            self.session()
        self.assertEqual(before, self.snapshot_live())
        self.assertEqual(10, stage.stat().st_size)

    def test_unregistered_or_changed_retained_envelopes_refuse_without_mutation(self):
        import hashlib
        for corruption in ("wrong-address", "canonical-garbage", "historical-bytes", "historical-mode"):
            with self.subTest(corruption=corruption):
                self.reset()
                session = self.session()
                historical = session.candidate.identity
                materialize_candidate(session, self.next)
                directory = self.root / ".vise-host/generations"
                if corruption == "wrong-address":
                    path = directory / ("0" * 64)
                    path.write_bytes(b"not an envelope\n")
                    path.chmod(0o600)
                elif corruption == "canonical-garbage":
                    data = b'{"unrelated":true}\n'
                    path = directory / hashlib.sha256(data).hexdigest()
                    path.write_bytes(data)
                    path.chmod(0o600)
                else:
                    path = directory / historical[7:]
                    if corruption == "historical-bytes":
                        path.write_bytes(b"damaged history\n")
                    else:
                        path.chmod(0o644)
                before = self.snapshot_live()
                bad_bytes = path.read_bytes()
                with self.assertRaises(SessionError):
                    self.session()
                self.assertEqual(before, self.snapshot_live())
                self.assertEqual(bad_bytes, path.read_bytes())

    def test_changed_index_lock_prefix_refuses_before_git_reads(self):
        self.crash("after-index-lock-sync")
        path = self.root / ".git/index.lock"
        path.write_bytes(b"not an index prefix")
        before = self.snapshot_live()
        with patch.object(self.git, "run", side_effect=AssertionError("untrusted Git read")):
            with self.assertRaises(SessionError):
                self.session()
        self.assertEqual(before, self.snapshot_live())
        self.assertEqual(b"not an index prefix", path.read_bytes())


if __name__ == "__main__":
    unittest.main()
