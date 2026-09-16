"""Real-Git and filesystem witnesses for isolated initialization capsules."""

from __future__ import annotations

import base64
import hashlib
import json
import os
import select
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path

from host.bundle import SourceEntry, build_bundle
from host.git_identity import FixedGitIdentity, GitRunner, inspect_repository
from host.initial_seed import (
    InitialSeedError, SeedLimits, build_initial_seed, decode_initial_seed, validate_initial_seed,
)
from host.operator import build_operator
from host.storage import OwnedDirectory


GIT = Path(shutil.which("git") or "")
IDENTITY = FixedGitIdentity("Seed Host", "seed@example.invalid", "Seed Host", "seed@example.invalid")
IGNORE = b".vise-host/\n.vise/journal.jsonl\n.vise/run.lock\n.vise/tmp/\n"


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode("ascii")


def snapshot(root):
    result = []
    for path in [root, *sorted(root.rglob("*"))]:
        info = path.lstat()
        data = (path.read_bytes() if stat.S_ISREG(info.st_mode) else
                os.readlink(path) if stat.S_ISLNK(info.st_mode) else None)
        result.append((str(path.relative_to(root)), info.st_mode, info.st_uid, info.st_nlink,
                       info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns, data))
    return result


class InitialSeedTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="vise-seed-test-")
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.git = GitRunner(GIT, self.base / "home")
        self.root = self.base / "builder"
        self.operator = build_operator((
            SourceEntry(".gitignore", IGNORE),
            SourceEntry("vise.toml", b"[vise]\nversion = 1\n"),
            SourceEntry("vise.lock", b"retained operator lock bytes\n"),
            SourceEntry(".vise/blobs/" + "a" * 64, b"\x00\xff\r\n"),
            SourceEntry("specs/nested/expected", b"expected\n"),
            SourceEntry("dependencies/tool", b"not executed\n", True),
            SourceEntry("empty", b""),
        ), generation=0)
        self.seed = self.build(self.root)

    def build(self, root, **overrides):
        args = dict(git=self.git, identity=IDENTITY, session_id="1" * 32, q="2" * 32)
        args.update(overrides)
        return build_initial_seed(root, self.operator, **args)

    def refuse_value(self, value):
        with self.assertRaises(InitialSeedError):
            decode_initial_seed(canonical(value))

    def test_real_git_bootstrap_objects_index_and_capsule_roundtrip(self):
        before = snapshot(self.root)
        decoded = decode_initial_seed(self.seed.encoded)
        self.assertEqual(self.seed, decoded)
        self.assertEqual(self.seed, validate_initial_seed(self.seed.encoded, builder_root=self.root, git=self.git))
        self.assertEqual(before, snapshot(self.root))
        self.assertEqual(build_bundle(()), decoded.candidate)
        self.assertEqual(self.operator, decoded.operator)
        self.assertEqual("sha256:" + hashlib.sha256(decoded.encoded).hexdigest(), decoded.identity)
        state = inspect_repository(self.root, git=self.git)
        self.assertEqual(decoded.assembly.bootstrap, state.head)
        self.assertEqual("4b825dc642cb6eb9a060e54bf8d69288fbee4904", state.index_tree)
        self.assertNotEqual(decoded.assembly.commit, state.head)
        self.assertIn("objects/info", dict(decoded.directories))
        self.assertIn("objects/pack", dict(decoded.directories))
        self.assertIn("refs/tags", dict(decoded.directories))
        self.assertFalse((self.root / ".vise-host/session.json").exists())

    def test_restored_exact_capture_validates_with_real_git(self):
        restored = self.base / "restored"
        restored.mkdir(mode=0o700)
        with OwnedDirectory(restored) as owned:
            for relative, mode in sorted(self.seed.directories, key=lambda row: (row[0].count("/"), row[0])):
                path = ".git/" + relative if relative else ".git"
                owned.mkdirs(path)
                (restored / path).chmod(mode)
            for path, value in self.seed.files:
                owned.write_new(".git/" + path, value.data, mode=0o600)
                (restored / ".git" / path).chmod(value.mode)
            owned.write_new("requested.index", self.seed.requested_index.data, mode=self.seed.requested_index.mode)
        self.assertEqual(self.seed, validate_initial_seed(self.seed.encoded, builder_root=restored, git=self.git))

    def test_same_inputs_seal_same_bytes_and_changed_generation_changes_only_provenance(self):
        repeated = self.build(self.base / "again")
        self.assertEqual(self.seed.encoded, repeated.encoded)
        next_operator = build_operator(self.operator.files, generation=1)
        changed = build_initial_seed(self.base / "next", next_operator, git=self.git, identity=IDENTITY,
                                     session_id="1" * 32, q="2" * 32)
        self.assertEqual(self.seed.assembly.tree, changed.assembly.tree)
        self.assertNotEqual(self.seed.assembly.commit, changed.assembly.commit)
        self.assertNotEqual(self.seed.identity, changed.identity)

    def test_legal_quoted_space_and_unicode_operator_paths_are_exact_git_bytes(self):
        extra = (
            SourceEntry('"quoted"', b"literal quotes"),
            SourceEntry('"leading', b"leading quote"),
            SourceEntry('trailing"', b"trailing quote"),
            SourceEntry('nested/"two words"', b"space and quotes", True),
            SourceEntry("é/日本語", b"canonical unicode"),
        )
        operator = build_operator((*self.operator.files, *extra), generation=0)
        root = self.base / "quoted"
        seed = build_initial_seed(root, operator, git=self.git, identity=IDENTITY,
                                  session_id="1" * 32, q="2" * 32)
        self.assertEqual(operator, seed.operator)
        expected = b"".join(entry.path.encode("utf-8") + b"\0" for entry in operator.files)
        actual = self.git.run(("ls-files", "-z"), root=root, index_file=root / "requested.index")
        self.assertEqual(expected, actual)
        actual_tree = self.git.run(("ls-tree", "-r", "--name-only", "-z", seed.assembly.tree), root=root)
        self.assertEqual(expected, actual_tree)
        self.assertEqual(seed, validate_initial_seed(seed.encoded, builder_root=root, git=self.git))

    def test_private_umask_and_nonzero_fixed_timestamp(self):
        previous = os.umask(0o077)
        try:
            seeded = self.build(self.base / "private", identity=replace(IDENTITY, timestamp=123456))
        finally:
            os.umask(previous)
        self.assertEqual(123456, seeded.git_identity.timestamp)
        self.assertEqual(self.operator, seeded.operator)
        self.assertIn(b"123456 +0000\n", dict(seeded.files)["logs/HEAD"].data)

    def test_noncanonical_duplicate_and_malformed_json_refuse(self):
        variants = (bytearray(self.seed.encoded), self.seed.encoded[:-1], b" " + self.seed.encoded,
                    b'{"kind":"initial-seed","kind":"initial-seed"}\n', b"[]\n", b"null\n",
                    b"[" * 1100 + b"]" * 1100, b'{"version":' + b"9" * 5000 + b"}\n")
        for encoded in variants:
            with self.subTest(encoded=repr(encoded[:50])), self.assertRaises(InitialSeedError):
                decode_initial_seed(encoded)

    def test_contract_identifiers_counters_and_fixed_identity_types_refuse(self):
        for field, bad in (("version", True), ("version", 1.0), ("host_contract", 2),
                           ("operator_generation", True), ("operator_generation", 1.0),
                           ("operator_generation", 1), ("q", "A" * 32), ("q", "1" * 31),
                           ("session_id", None), ("kind", "operator"), ("git_policy", "sha256:" + "0" * 64)):
            with self.subTest(field=field, bad=bad):
                value = json.loads(self.seed.encoded)
                value[field] = bad
                self.refuse_value(value)
        for field, bad in (("timestamp", True), ("timestamp", 0.0), ("message_schema", True),
                           ("timezone", "-0500"), ("object_format", "sha256"), ("committer_name", "other")):
            with self.subTest(identity=field):
                value = json.loads(self.seed.encoded)
                value["git_identity"][field] = bad
                self.refuse_value(value)
        value = json.loads(self.seed.encoded)
        value["extra"] = "unknown"
        self.refuse_value(value)

    def test_retained_c_o_assembly_and_bootstrap_tamper_refuse(self):
        value = json.loads(self.seed.encoded)
        value["candidate"] = base64.b64encode(build_bundle((SourceEntry("candidate", b"x"),)).encoded).decode()
        self.refuse_value(value)
        for field in ("candidate", "operator", "tree", "commit", "bootstrap"):
            with self.subTest(field=field):
                value = json.loads(self.seed.encoded)
                value["assembly"][field] = "0" * 40
                self.refuse_value(value)
        for field in ("candidate", "operator"):
            value = json.loads(self.seed.encoded)
            value[field] = "%%%"
            self.refuse_value(value)

    def test_seed_path_duplicates_aliases_unknown_and_missing_entries_refuse(self):
        for path in ("../outside", "config/", "Config", "objects/../config", ".git/config", "", "hooks/evil"):
            with self.subTest(path=path):
                value = json.loads(self.seed.encoded)
                value["files"][0]["path"] = path
                value["files"].sort(key=lambda record: record["path"])
                self.refuse_value(value)
        for field in ("files", "directories"):
            value = json.loads(self.seed.encoded)
            value[field].append(dict(value[field][0]))
            value[field].sort(key=lambda record: record["path"])
            self.refuse_value(value)
            value = json.loads(self.seed.encoded)
            value[field].pop()
            self.refuse_value(value)
            value = json.loads(self.seed.encoded)
            value[field].reverse()
            self.refuse_value(value)
        value = json.loads(self.seed.encoded)
        value["directories"].append({"path": "objects/ab/extra", "mode": 0o700})
        value["directories"].sort(key=lambda record: record["path"])
        self.refuse_value(value)

    def test_seed_bytes_modes_objects_refs_policy_and_indices_refuse_tamper(self):
        for path in ("config", "HEAD", "refs/heads/vise-host", "logs/HEAD", "index", "info/exclude"):
            value = json.loads(self.seed.encoded)
            record = next(row for row in value["files"] if row["path"] == path)
            record["data"] = base64.b64encode(b"tampered").decode()
            with self.subTest(path=path):
                self.refuse_value(value)
        value = json.loads(self.seed.encoded)
        record = next(row for row in value["files"] if row["path"].startswith("objects/"))
        record["data"] = base64.b64encode(base64.b64decode(record["data"]) + b"trailing").decode()
        self.refuse_value(value)
        for field in ("files", "directories"):
            for mode in (True, 384.0, "600", 0o777, 0o1700):
                value = json.loads(self.seed.encoded)
                value[field][0]["mode"] = mode
                self.refuse_value(value)
        for mode in (True, 420.0, 0o755):
            value = json.loads(self.seed.encoded)
            value["requested_index"]["mode"] = mode
            self.refuse_value(value)
        value = json.loads(self.seed.encoded)
        data = bytearray(base64.b64decode(value["requested_index"]["data"]))
        data[20] ^= 1
        data[-20:] = hashlib.sha1(data[:-20]).digest()
        value["requested_index"]["data"] = base64.b64encode(data).decode()
        self.refuse_value(value)

    def test_capsule_limits_are_exact_and_enforced(self):
        for value in (True, 1.0, 0, -1, "100"):
            with self.subTest(value=value), self.assertRaises(InitialSeedError):
                SeedLimits(max_entries=value)
        for limits in (SeedLimits(max_encoded_bytes=len(self.seed.encoded) - 1),
                       SeedLimits(max_entries=1), SeedLimits(max_file_bytes=1), SeedLimits(max_seed_bytes=1)):
            with self.subTest(limits=limits), self.assertRaises(InitialSeedError):
                decode_initial_seed(self.seed.encoded, limits=limits)
        self.assertEqual(self.seed, decode_initial_seed(self.seed.encoded,
                         limits=SeedLimits(max_encoded_bytes=len(self.seed.encoded))))

    def test_native_validator_refuses_unknown_content_before_calling_git(self):
        class NoCalls(GitRunner):
            def run(self, *args, **kwargs):
                raise AssertionError("native Git must not see unknown builder content")

        guarded = NoCalls(GIT, self.base / "guarded-home")
        for relative, directory in (("extra", False), (".git/unknown", False),
                                    (".git/hooks", True), (".git/objects/ab", True)):
            path = self.root / relative
            if path.exists():
                continue
            if directory:
                path.mkdir()
            else:
                path.write_bytes(b"unknown")
            before = snapshot(self.root)
            with self.subTest(relative=relative), self.assertRaises(InitialSeedError):
                validate_initial_seed(self.seed.encoded, builder_root=self.root, git=guarded)
            self.assertEqual(before, snapshot(self.root))
            path.rmdir() if directory else path.unlink()

    def test_native_validator_refuses_changed_missing_linked_and_special_entries(self):
        path = self.root / ".git/info/exclude"
        original = path.read_bytes()
        for kind in ("changed", "missing", "symlink", "hardlink", "fifo", "mode"):
            path.unlink()
            if kind == "changed":
                path.write_bytes(b"other")
            elif kind == "symlink":
                path.symlink_to(self.base / "absent")
            elif kind == "hardlink":
                os.link(self.root / ".git/info/attributes", path)
            elif kind == "fifo":
                os.mkfifo(path)
            elif kind == "mode":
                path.write_bytes(original)
                path.chmod(0o755)
            before = snapshot(self.root)
            with self.subTest(kind=kind), self.assertRaises(InitialSeedError):
                validate_initial_seed(self.seed.encoded, builder_root=self.root, git=self.git)
            self.assertEqual(before, snapshot(self.root))
            if path.exists() or path.is_symlink():
                path.unlink()
            path.write_bytes(original)
            path.chmod(0o644)

    def test_existing_builder_never_adopted_or_reset(self):
        before = snapshot(self.root)
        with self.assertRaises(InitialSeedError):
            self.build(self.root)
        self.assertEqual(before, snapshot(self.root))
        partial = self.base / "partial"
        partial.mkdir(mode=0o700)
        (partial / ".git").mkdir()
        before = snapshot(partial)
        with self.assertRaises(InitialSeedError):
            self.build(partial)
        self.assertEqual(before, snapshot(partial))

    def test_malformed_inputs_refuse_before_builder_creation(self):
        for overrides in ({"q": True}, {"session_id": "invalid"}, {"identity": object()}, {"limits": object()}):
            path = self.base / "never-created"
            with self.subTest(overrides=overrides), self.assertRaises(InitialSeedError):
                self.build(path, **overrides)
            self.assertFalse(path.exists())
        operator = build_operator((SourceEntry("plain", b"x"),), generation=0)
        with self.assertRaises(InitialSeedError):
            build_initial_seed(self.base / "bad-policy", operator, git=self.git, identity=IDENTITY,
                               q="1" * 32, session_id="2" * 32)
        self.assertFalse((self.base / "bad-policy").exists())

    def test_failed_bounded_build_retains_actual_git_evidence(self):
        root = self.base / "too-small"
        with self.assertRaises(InitialSeedError):
            self.build(root, limits=SeedLimits(max_entries=1))
        self.assertTrue((root / ".git/HEAD").is_file())
        self.assertTrue((root / ".git/config").is_file())
        self.assertFalse((root / ".vise-host/session.json").exists())

    def test_actual_builder_child_interruption_retains_partial_git(self):
        # This retained fixture deliberately lives outside TemporaryDirectory's
        # recursive cleanup. The caller/reviewer owns later reconciliation.
        base = Path(tempfile.mkdtemp(prefix="vise-seed-interrupted-"))
        root = base / "builder"
        script = '''
import os, signal, sys
from pathlib import Path
from host.bundle import SourceEntry
from host.operator import build_operator
from host.git_identity import GitRunner, FixedGitIdentity
from host.initial_seed import build_initial_seed
class StopAfterRealInit(GitRunner):
    def run(self, args, **kwargs):
        if args == ("mktree",):
            print("REAL_INIT_FINISHED_BOOTSTRAP_NOT_STARTED", flush=True)
            os.kill(os.getpid(), signal.SIGSTOP)
        return super().run(args, **kwargs)
base=Path(sys.argv[1])
git=StopAfterRealInit(Path(sys.argv[2]),base/"home")
op=build_operator((SourceEntry(".gitignore", b".vise-host/\\n.vise/journal.jsonl\\n.vise/run.lock\\n.vise/tmp/\\n"),),generation=0)
identity=FixedGitIdentity("Seed Host","seed@example.invalid","Seed Host","seed@example.invalid")
build_initial_seed(base/"builder",op,git=git,identity=identity,session_id="1"*32,q="2"*32)
'''
        child = subprocess.Popen([sys.executable, "-B", "-c", script, str(base), str(GIT)],
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            self.assertTrue(select.select([child.stdout], [], [], 15)[0], "builder readiness timed out")
            line = child.stdout.readline().strip()
            self.assertEqual("REAL_INIT_FINISHED_BOOTSTRAP_NOT_STARTED", line)
            self.assertTrue((root / ".git/HEAD").is_file())
            self.assertTrue((root / ".git/config").is_file())
            self.assertFalse((root / ".git/index").exists())
            os.kill(child.pid, signal.SIGKILL)
            self.assertEqual(-signal.SIGKILL, child.wait(timeout=5))
            before = snapshot(root)
            with self.assertRaises(InitialSeedError):
                self.build(root)
            self.assertEqual(before, snapshot(root))
            self.assertFalse((root / ".vise-host/session.json").exists())
            print(f"interruption_evidence pid={child.pid} returncode={child.returncode} retained={base}", flush=True)
        finally:
            if child.poll() is None:
                child.kill()
                child.wait(timeout=5)
            child.stdout.close()
            child.stderr.close()


if __name__ == "__main__":
    unittest.main()
