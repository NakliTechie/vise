import json
import shutil
import tempfile
import unittest
from pathlib import Path

from host.bundle import SourceEntry, build_bundle
from host.git_identity import FixedGitIdentity, GitRunner, inspect_repository
from host.operator import build_operator
from host.session import (
    SessionError,
    advance_operator,
    initialize_session,
    inspect_historical,
    materialize_candidate,
    observe_generation,
    open_session,
)
from host.storage import OwnedDirectory


GIT = Path(shutil.which("git") or "")
IDENTITY = FixedGitIdentity("Vise Host", "host@example.invalid", "Vise Host", "host@example.invalid")
IGNORE = b".vise-host/\n.vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n"


def operator(generation=0, extra=()):
    return build_operator((SourceEntry(".gitignore", IGNORE), *extra), generation=generation)


class SessionLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="vise-session-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / "session"
        self.git = GitRunner(GIT, self.base / "git-home")
        self.initial_operator = operator(0, (SourceEntry("vise.toml", b"[probe.x]\n"),))
        self.initial = initialize_session(self.root, self.initial_operator, git=self.git, identity=IDENTITY)

    def reopen(self):
        return open_session(self.root, git=self.git)

    def snapshot(self):
        paths = (
            ".vise-host/session.json", ".vise-host/operations.jsonl",
            ".git/HEAD", ".git/index",
        )
        return {path: (self.root / path).read_bytes() for path in paths}

    def test_initialize_and_reopen_bind_actual_empty_c_and_operator(self):
        session = self.reopen()
        self.assertEqual(self.initial, observe_generation(session))
        self.assertEqual(0, session.operator.generation)
        self.assertEqual([], [record.path for record in self.initial.observed_candidate])
        self.assertEqual([".gitignore", "vise.toml"], [record.path for record in self.initial.observed_operator])
        self.assertEqual(session.assembly.commit, inspect_repository(self.root, git=self.git).head)
        lines = (self.root / ".vise-host/operations.jsonl").read_bytes().splitlines()
        self.assertEqual(1, len(lines))
        self.assertEqual("initialize", json.loads(lines[0])["operation"])

    def test_repeated_candidate_is_stable_and_restart_preserves_identity(self):
        session = self.reopen()
        candidate = build_bundle((
            SourceEntry("bin/tool", b"#!/bin/sh\n", True),
            SourceEntry("data/empty", b""),
            SourceEntry("data/raw", b"\x00\xff"),
        ))
        first = materialize_candidate(session, candidate)
        reopened = self.reopen()
        second = materialize_candidate(reopened, candidate)
        self.assertEqual((first.identity.tree, first.identity.commit),
                         (second.identity.tree, second.identity.commit))
        self.assertEqual(first, observe_generation(self.reopen()))
        self.assertEqual(3, len((self.root / ".vise-host/operations.jsonl").read_bytes().splitlines()))

    def test_candidate_change_and_revert_restore_tree_and_commit_under_same_o(self):
        session = self.reopen()
        first_candidate = build_bundle((SourceEntry("src/a", b"one"),))
        changed_candidate = build_bundle((SourceEntry("src/a", b"two", True),))
        first = materialize_candidate(session, first_candidate)
        changed = materialize_candidate(session, changed_candidate)
        reverted = materialize_candidate(session, first_candidate)
        self.assertNotEqual((first.identity.tree, first.identity.commit),
                            (changed.identity.tree, changed.identity.commit))
        self.assertEqual((first.identity.tree, first.identity.commit),
                         (reverted.identity.tree, reverted.identity.commit))
        nested = materialize_candidate(session, build_bundle((SourceEntry("shape/leaf", b"x"),)))
        flat = materialize_candidate(session, build_bundle((SourceEntry("shape", b"x"),)))
        self.assertNotEqual(nested.identity.tree, flat.identity.tree)
        self.assertEqual(b"x", (self.root / "shape").read_bytes())

    def test_generation_only_operator_advance_changes_k_not_t_and_stale_refuses(self):
        session = self.reopen()
        replacement = operator(1, (SourceEntry("vise.toml", b"[probe.x]\n"),))
        before = session.assembly
        result = advance_operator(
            session, expected_identity=session.operator.identity,
            expected_generation=0, replacement=replacement,
        )
        self.assertEqual(before.tree, result.identity.tree)
        self.assertNotEqual(before.commit, result.identity.commit)
        snapshot = self.snapshot()
        with self.assertRaises(SessionError):
            advance_operator(
                session, expected_identity=self.initial_operator.identity,
                expected_generation=0, replacement=operator(2),
            )
        self.assertEqual(snapshot, self.snapshot())

    def test_two_open_handles_cannot_advance_from_stale_operator_state(self):
        first = self.reopen()
        stale = self.reopen()
        replacement = operator(1, (SourceEntry("vise.toml", b"[probe.x]\n"),))
        advance_operator(
            first, expected_identity=first.operator.identity,
            expected_generation=0, replacement=replacement,
        )
        snapshot = self.snapshot()
        with self.assertRaises(SessionError):
            advance_operator(
                stale, expected_identity=stale.operator.identity,
                expected_generation=0, replacement=replacement,
            )
        self.assertEqual(snapshot, self.snapshot())

    def test_shared_directory_survives_candidate_replacement(self):
        session = self.reopen()
        replacement = operator(1, (
            SourceEntry("vise.toml", b"[probe.x]\n"),
            SourceEntry("shared/operator", b"owned by O"),
        ))
        advance_operator(
            session, expected_identity=session.operator.identity,
            expected_generation=0, replacement=replacement,
        )
        materialize_candidate(session, build_bundle((SourceEntry("shared/candidate", b"old"),)))
        materialize_candidate(session, build_bundle((SourceEntry("other", b"new"),)))
        self.assertEqual(b"owned by O", (self.root / "shared/operator").read_bytes())

    def test_persisted_equal_value_wrong_types_and_recomputed_identity_refuse(self):
        cases = (
            ("version", True),
            ("bundle_contract", 1.0),
            ("operator_generation", True),
            ("inventory_size", False),
            ("inventory_executable", 0),
            ("tree", "0" * 40),
        )
        original = (self.root / ".vise-host/session.json").read_bytes()
        for field, replacement in cases:
            with self.subTest(field=field):
                value = json.loads(original)
                if field in ("version", "bundle_contract"):
                    value[field] = replacement
                elif field == "operator_generation":
                    value["current"][field] = replacement
                elif field == "inventory_size":
                    value["operator_inventory"][0]["size"] = replacement
                elif field == "inventory_executable":
                    value["operator_inventory"][0]["executable"] = replacement
                else:
                    value["current"][field] = replacement
                encoded = (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode("ascii")
                (self.root / ".vise-host/session.json").write_bytes(encoded)
                with self.assertRaises(SessionError):
                    self.reopen()
                (self.root / ".vise-host/session.json").write_bytes(original)

    def test_existing_handle_refuses_changed_current_or_nonterminal_intent(self):
        session = self.reopen()
        current = self.root / ".vise-host/session.json"
        original = current.read_bytes()
        value = json.loads(original)
        value["current"]["commit"] = "0" * 40
        current.write_bytes((json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode("ascii"))
        with self.assertRaises(SessionError):
            observe_generation(session)
        current.write_bytes(original)
        intent = self.root / ".vise-host/intent.json"
        intent.write_bytes(b'{"phase":"PREPARING"}\n')
        intent.chmod(0o600)
        with self.assertRaises(SessionError):
            observe_generation(session)

    def test_ignored_unknown_paths_refuse_but_registered_runtime_paths_survive(self):
        session = self.reopen()
        reviewed_ignore = build_operator((
            SourceEntry("vise.toml", b"[probe.x]\n"),
            SourceEntry(".gitignore", IGNORE + b"unknown\n"),
        ), generation=1)
        advance_operator(
            session, expected_identity=session.operator.identity,
            expected_generation=0, replacement=reviewed_ignore,
        )
        (self.root / "unknown").write_bytes(b"ignored by reviewed rule")
        with self.assertRaises(SessionError):
            observe_generation(session)
        (self.root / "unknown").unlink()
        (self.root / ".vise").mkdir(mode=0o700)
        (self.root / ".vise/tmp").mkdir(mode=0o700)
        (self.root / ".vise/tmp/scratch").write_bytes(b"runtime")
        (self.root / ".vise/journal.jsonl").write_bytes(b"journal\n")
        (self.root / ".vise/run.lock").write_bytes(b"lock")
        before = {
            path: (self.root / path).read_bytes()
            for path in (".vise/tmp/scratch", ".vise/journal.jsonl", ".vise/run.lock")
        }
        materialize_candidate(session, build_bundle((SourceEntry("src/main", b"new"),)))
        self.assertEqual(before, {path: (self.root / path).read_bytes() for path in before})

    def test_unknown_vise_child_and_unexpected_empty_directory_refuse(self):
        session = self.reopen()
        (self.root / ".vise").mkdir(mode=0o700)
        (self.root / ".vise/unknown").write_bytes(b"x")
        with self.assertRaises(SessionError):
            observe_generation(session)
        (self.root / ".vise/unknown").unlink()
        (self.root / ".vise").rmdir()
        (self.root / "empty").mkdir()
        with self.assertRaises(SessionError):
            observe_generation(session)

    def test_historical_recompute_does_not_change_live_pointer_head_or_index(self):
        session = self.reopen()
        old_c, old_o = session.candidate.identity, session.operator.identity
        materialize_candidate(session, build_bundle((SourceEntry("new", b"value"),)))
        before = self.snapshot()
        objects = self.root / ".git/objects"
        object_files_before = {path.relative_to(objects) for path in objects.glob("*/*") if path.is_file()}
        historical = inspect_historical(session, old_c, old_o)
        self.assertEqual(before, self.snapshot())
        self.assertEqual(
            object_files_before,
            {path.relative_to(objects) for path in objects.glob("*/*") if path.is_file()},
        )
        self.assertEqual(self.initial.identity, historical)

    def test_worktree_current_and_envelope_tamper_refuse_reopen_or_observe(self):
        session = self.reopen()
        candidate = build_bundle((SourceEntry("src/main", b"good"),))
        materialize_candidate(session, candidate)
        (self.root / "src/main").write_bytes(b"evil")
        with self.assertRaises(SessionError):
            observe_generation(session)
        (self.root / "src/main").write_bytes(b"good")
        current = self.root / ".vise-host/session.json"
        raw = current.read_bytes()
        current.write_bytes(raw.replace(b'"host_contract":1', b'"host_contract":2'))
        with self.assertRaises(SessionError):
            self.reopen()

    def test_nonterminal_or_unsafe_intent_refuses_reopen(self):
        intent = self.root / ".vise-host/intent.json"
        intent.write_bytes(b'{"phase":"PREPARING"}\n')
        intent.chmod(0o600)
        with self.assertRaises(SessionError):
            self.reopen()
        intent.unlink()
        outside = self.base / "outside"
        outside.write_bytes(b'{"phase":"PREPARING"}\n')
        intent.symlink_to(outside)
        with self.assertRaises(SessionError):
            self.reopen()

    def test_public_reopen_respects_nonblocking_session_lock(self):
        with OwnedDirectory(self.root) as owned, owned.exclusive_lock(".vise-host/session.lock"):
            with self.assertRaises(SessionError):
                self.reopen()


if __name__ == "__main__":
    unittest.main()
