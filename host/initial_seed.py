"""Sealed initial Git inputs, not persistent session initialization or recovery.

The caller supplies a fresh isolated builder path and owns its entire lifecycle.
Success and failure both retain the builder. This module never recursively
deletes, adopts, resets, or publishes a builder, and never creates a session.
Capsules must be retained durably before a later ownership intent references
them. Their SHA-256 identity is a binding, not authentication of their origin.
"""

from __future__ import annotations

import base64
import binascii
import hashlib
import json
import struct
import zlib
from dataclasses import dataclass
from pathlib import Path

from host.bundle import BundleError, SourceBundle, build_bundle, decode_bundle
from host.git_identity import (
    AssemblyIdentity, FixedGitIdentity, GitIdentityError, GitRunner,
    _CANONICAL_CONFIG, _EMPTY_TREE, _assembly_message, _checked_object, _commit_tree,
    _validate_bootstrap, _validate_commit, _verify_tree_objects,
    initialize_repository, inspect_repository, verify_repository_policy,
)
from host.git_inventory import GitInventoryError, _DIRECTORIES, _FILES, verify_git_layout
from host.operator import OperatorGeneration, decode_operator, validate_candidate
from host.storage import FileBytes, OwnedDirectory, StorageError


class InitialSeedError(RuntimeError):
    """The isolated builder or capsule violates the initialization seed profile."""


@dataclass(frozen=True)
class SeedLimits:
    max_encoded_bytes: int = 1024 * 1024 * 1024
    max_seed_bytes: int = 384 * 1024 * 1024
    max_file_bytes: int = 17 * 1024 * 1024
    max_entries: int = 100_000

    def __post_init__(self):
        if any(type(value) is not int or value <= 0 for value in vars(self).values()):
            raise InitialSeedError("seed limits must be exact positive integers")


@dataclass(frozen=True)
class InitialSeed:
    encoded: bytes
    identity: str
    candidate: SourceBundle
    operator: OperatorGeneration
    git_identity: FixedGitIdentity
    session_id: str
    q: str
    assembly: AssemblyIdentity
    git_policy: str
    files: tuple[tuple[str, FileBytes], ...]
    directories: tuple[tuple[str, int], ...]
    requested_index: FileBytes


_INDEX = "requested.index"
_IGNORE = {b".vise-host/", b".vise/journal.jsonl", b".vise/run.lock", b".vise/tmp/"}


def _json(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode("ascii")


def _b64(data):
    return base64.b64encode(data).decode("ascii")


def _unbase64(value, limit):
    if type(value) is not str or len(value) > 4 * ((limit + 2) // 3):
        raise InitialSeedError("seed bytes exceed bounds or have invalid type")
    try:
        raw = base64.b64decode(value, validate=True)
    except (binascii.Error, ValueError) as error:
        raise InitialSeedError("seed bytes are not canonical base64") from error
    if len(raw) > limit or _b64(raw) != value:
        raise InitialSeedError("seed bytes are not canonical or exceed bounds")
    return raw


def _identifier(value):
    if type(value) is not str or len(value) != 32 or any(c not in "0123456789abcdef" for c in value):
        raise InitialSeedError("initialization identifier must be 32 lowercase hex characters")


def _operator(operator):
    empty = build_bundle(())
    validate_candidate(empty, operator)
    ignore = next((entry for entry in operator.files if entry.path == ".gitignore"), None)
    if ignore is None or ignore.executable or not _IGNORE.issubset(ignore.data.splitlines()):
        raise InitialSeedError("initial operator lacks required root ignore policy")
    return empty


def _policy(files):
    content = b"".join(
        (".git/" + path).encode("ascii") + b"\0" + files[path].data + b"\0"
        + str(files[path].mode).encode("ascii") + b"\n"
        for path in ("config", "info/exclude", "info/attributes")
    )
    return "sha256:" + hashlib.sha256(content).hexdigest()


def _objects(operator, identity):
    """Reconstruct the complete expected object graph without interpreting Git."""
    objects = {}

    def add(kind, payload):
        raw = kind + b" " + str(len(payload)).encode("ascii") + b"\0" + payload
        oid = hashlib.sha1(raw).hexdigest()
        objects[oid] = raw
        return oid

    empty = add(b"tree", b"")
    who = (f"author {identity.author_name} <{identity.author_email}> {identity.timestamp} {identity.timezone}\n"
           f"committer {identity.committer_name} <{identity.committer_email}> {identity.timestamp} {identity.timezone}\n\n")
    bootstrap = add(b"commit", (f"tree {empty}\n" + who + "vise-host bootstrap schema=1\n").encode("utf-8"))
    root = {}
    for entry in operator.files:
        node = root
        parts = entry.path.encode("utf-8").split(b"/")
        for part in parts[:-1]:
            node = node.setdefault(part, {})
        node[parts[-1]] = (b"100755" if entry.executable else b"100644", add(b"blob", entry.data))
    pending, trees = [(root, False)], {}
    while pending:
        node, expanded = pending.pop()
        if not expanded:
            pending.append((node, True))
            pending.extend((value, False) for value in node.values() if type(value) is dict)
            continue
        records = []
        for name, value in node.items():
            if type(value) is dict:
                records.append((name + b"/", b"40000 " + name + b"\0" + bytes.fromhex(trees[id(value)])))
            else:
                mode, oid = value
                records.append((name, mode + b" " + name + b"\0" + bytes.fromhex(oid)))
        trees[id(node)] = add(b"tree", b"".join(raw for _, raw in sorted(records)))
    tree = trees[id(root)]
    candidate = build_bundle(())
    message = _assembly_message(candidate.identity, operator.identity, identity.message_schema)
    commit = add(b"commit", (f"tree {tree}\nparent {bootstrap}\n" + who).encode("utf-8") + message)
    return AssemblyIdentity(candidate.identity, operator.identity, tree, commit, bootstrap), objects


def _blob_id(data):
    return hashlib.sha1(b"blob " + str(len(data)).encode("ascii") + b"\0" + data).hexdigest()


def _index_bytes(entries):
    """Canonical index v2: zero stat cache, stage zero, no optional extensions."""
    records = [b"DIRC" + struct.pack(">II", 2, len(entries))]
    for entry in entries:
        path = entry.path.encode("utf-8")
        raw = (struct.pack(">10I", 0, 0, 0, 0, 0, 0, 0o100755 if entry.executable else 0o100644, 0, 0, 0)
               + bytes.fromhex(_blob_id(entry.data)) + struct.pack(">H", min(len(path), 0xFFF)) + path + b"\0")
        records.append(raw + b"\0" * (-len(raw) % 8))
    body = b"".join(records)
    return body + hashlib.sha1(body).digest()


def decode_initial_seed(encoded: bytes, *, limits: SeedLimits = SeedLimits()) -> InitialSeed:
    """Strict canonical decoding, including exact objects and canonical indices.

    Call validate_initial_seed before using a restored seed with Git. A capsule
    is not a usable session and its requested index is never the bootstrap index.
    """
    if type(limits) is not SeedLimits or type(encoded) is not bytes or len(encoded) > limits.max_encoded_bytes:
        raise InitialSeedError("capsule type or encoded-byte limit is invalid")

    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise InitialSeedError("duplicate capsule key")
            result[key] = value
        return result

    try:
        value = json.loads(encoded, object_pairs_hook=pairs)
        if type(value) is not dict or set(value) != {
            "kind", "version", "bundle_contract", "operator_contract", "host_contract", "q", "session_id",
            "candidate", "operator", "operator_generation", "git_identity", "assembly", "git_policy",
            "files", "directories", "requested_index",
        }:
            raise InitialSeedError("capsule fields differ")
        for key in ("version", "bundle_contract", "operator_contract", "host_contract"):
            if type(value[key]) is not int or value[key] != 1:
                raise InitialSeedError("capsule contract version differs")
        if value["kind"] != "initial-seed" or _json(value) != encoded:
            raise InitialSeedError("capsule is not canonical initial-seed JSON")
        _identifier(value["q"])
        _identifier(value["session_id"])
        candidate = decode_bundle(_unbase64(value["candidate"], limits.max_encoded_bytes))
        operator = decode_operator(_unbase64(value["operator"], limits.max_encoded_bytes))
        if candidate != _operator(operator):
            raise InitialSeedError("initial candidate is not canonical empty C")
        if type(value["operator_generation"]) is not int or value["operator_generation"] != operator.generation:
            raise InitialSeedError("initial operator counter differs")
        identity = FixedGitIdentity.from_dict(value["git_identity"])
        assembly, objects = _objects(operator, identity)
        if value["assembly"] != vars(assembly):
            raise InitialSeedError("capsule assembly differs from retained inputs")
        raw_files, raw_directories = value["files"], value["directories"]
        if (type(raw_files) is not list or type(raw_directories) is not list
                or len(raw_files) + len(raw_directories) > limits.max_entries):
            raise InitialSeedError("seed inventory count/type differs")
        files, directories, total = {}, {}, 0
        for records, target, directory in ((raw_files, files, False), (raw_directories, directories, True)):
            for record in records:
                wanted = {"path", "mode"} if directory else {"path", "mode", "data"}
                if type(record) is not dict or set(record) != wanted or type(record["path"]) is not str:
                    raise InitialSeedError("seed inventory record differs")
                path, mode = record["path"], record["mode"]
                if path in target or type(mode) is not int or mode < 0 or mode > 0o777 or mode & 0o022:
                    raise InitialSeedError("seed inventory path/mode differs")
                if directory:
                    target[path] = mode
                else:
                    data = _unbase64(record["data"], min(limits.max_file_bytes, limits.max_seed_bytes - total))
                    total += len(data)
                    target[path] = FileBytes(data, mode)
            if [record["path"] for record in records] != sorted(target):
                raise InitialSeedError("seed inventory is not sorted")
        expected_files = {path.removeprefix(".git/") for path in _FILES}
        expected_files.update("objects/" + oid[:2] + "/" + oid[2:] for oid in objects)
        expected_directories = {"" if path == ".git" else path.removeprefix(".git/") for path in _DIRECTORIES}
        expected_directories.update("objects/" + oid[:2] for oid in objects)
        if set(files) != expected_files or set(directories) != expected_directories:
            raise InitialSeedError("seed contains missing, aliased or unknown paths")
        raw_index = value["requested_index"]
        if type(raw_index) is not dict or set(raw_index) != {"data", "mode"}:
            raise InitialSeedError("requested index schema differs")
        requested_index = FileBytes(_unbase64(raw_index["data"], limits.max_file_bytes), raw_index["mode"])
        if type(requested_index.mode) is not int or requested_index.mode not in (0o600, 0o644):
            raise InitialSeedError("requested index mode differs")
        if total + len(requested_index.data) > limits.max_seed_bytes:
            raise InitialSeedError("seed exceeds total byte limit")
        for path, record in files.items():
            allowed = (0o400, 0o444, 0o600, 0o644) if path.startswith("objects/") else (0o600, 0o644)
            if record.mode not in allowed:
                raise InitialSeedError("seed metadata mode differs")
        for path, content in (("config", _CANONICAL_CONFIG), ("info/exclude", b""), ("info/attributes", b"")):
            if files[path] != FileBytes(content, 0o644):
                raise InitialSeedError("seed Git policy differs")
        if files["HEAD"].data != b"ref: refs/heads/vise-host\n" or files["refs/heads/vise-host"].data != (assembly.bootstrap + "\n").encode():
            raise InitialSeedError("seed bootstrap ref differs")
        reflog = (f"{'0' * 40} {assembly.bootstrap} {identity.committer_name} <{identity.committer_email}> "
                  f"{identity.timestamp} {identity.timezone}\n").encode("utf-8")
        if any(files[path].data != reflog for path in ("logs/HEAD", "logs/refs/heads/vise-host")):
            raise InitialSeedError("seed reflog differs from fixed initialization identity")
        for oid, expected in objects.items():
            compressed = files["objects/" + oid[:2] + "/" + oid[2:]].data
            inflater = zlib.decompressobj()
            actual = inflater.decompress(compressed, len(expected) + 1)
            if actual != expected or not inflater.eof or inflater.unused_data or inflater.unconsumed_tail:
                raise InitialSeedError("seed object differs from retained inputs")
        if files["index"].data != _index_bytes(()) or requested_index.data != _index_bytes(operator.files):
            raise InitialSeedError("seed index bytes differ from retained inventories")
        policy = _policy(files)
        if value["git_policy"] != policy:
            raise InitialSeedError("capsule Git policy identity differs")
        return InitialSeed(encoded, "sha256:" + hashlib.sha256(encoded).hexdigest(), candidate, operator,
                           identity, value["session_id"], value["q"], assembly, policy,
                           tuple(sorted(files.items())), tuple(sorted(directories.items())), requested_index)
    except (BundleError, GitIdentityError, UnicodeError, ValueError, TypeError, KeyError, RecursionError, zlib.error) as error:
        raise InitialSeedError("invalid initialization capsule") from error


def _capture(root, limits):
    files, directories, index, total = {}, {}, None, 0
    with OwnedDirectory(root) as owned:
        verify_git_layout(owned)
        for entry in owned.walk(max_entries=limits.max_entries + 1):
            if entry.path == _INDEX and not entry.directory:
                index = owned.read_file(entry.path, max_bytes=limits.max_file_bytes)
                total += len(index.data)
            elif entry.path == ".git" or entry.path.startswith(".git/"):
                relative = "" if entry.path == ".git" else entry.path[5:]
                if entry.directory:
                    directories[relative] = entry.mode
                else:
                    files[relative] = owned.read_file(entry.path, max_bytes=limits.max_file_bytes)
                    total += len(files[relative].data)
            else:
                raise InitialSeedError("isolated builder contains an unknown entry")
            if total > limits.max_seed_bytes:
                raise InitialSeedError("builder exceeds total seed byte limit")
    if index is None:
        raise InitialSeedError("isolated builder lacks the requested index")
    return files, directories, index


def validate_initial_seed(encoded: bytes, *, builder_root: Path, git: GitRunner,
                          limits: SeedLimits = SeedLimits()) -> InitialSeed:
    """Compare a complete isolated builder and validate actual Git without writes.

    Only .git/ and requested.index may exist in this builder. The exact capture
    comparison precedes native Git calls. No failed validation cleans up files.
    """
    seed = decode_initial_seed(encoded, limits=limits)
    try:
        files, directories, index = _capture(builder_root, limits)
        if (files != dict(seed.files) or directories != dict(seed.directories) or index != seed.requested_index):
            raise InitialSeedError("builder differs from sealed capsule")
        verify_repository_policy(builder_root)
        observed = inspect_repository(builder_root, git=git)
        if (observed.head != seed.assembly.bootstrap or observed.index_tree != _EMPTY_TREE
                or observed.commit_tree != _EMPTY_TREE or observed.commit_parent is not None):
            raise InitialSeedError("native bootstrap identity differs")
        _validate_bootstrap(_checked_object(builder_root, git, seed.assembly.bootstrap, "commit"), seed.git_identity)
        _validate_commit(_checked_object(builder_root, git, seed.assembly.commit, "commit"), seed.assembly, seed.git_identity)
        _verify_tree_objects(builder_root, git, seed.assembly.tree)
        actual = git.run(("ls-files", "--stage", "--full-name", "-z"), root=builder_root,
                         index_file=builder_root / _INDEX)
        expected = b"".join(
            f"{'100755' if e.executable else '100644'} {_blob_id(e.data)} 0\t{e.path}\0".encode()
            for e in seed.operator.files
        )
        if actual != expected:
            raise InitialSeedError("native requested index differs from retained O")
        return seed
    except (GitIdentityError, GitInventoryError, StorageError, OSError) as error:
        raise InitialSeedError("isolated seed validation failed") from error


class _FixedIdentityRunner:
    def __init__(self, git, identity):
        self.git, self.identity = git, identity
        self.executable, self.private_home = git.executable, git.private_home

    def run(self, args, **kwargs):
        kwargs.setdefault("commit_identity", self.identity)
        return self.git.run(args, **kwargs)


def build_initial_seed(builder_root: Path, operator: OperatorGeneration, *, git: GitRunner,
                       identity: FixedGitIdentity, session_id: str, q: str,
                       limits: SeedLimits = SeedLimits()) -> InitialSeed:
    """Build in a fresh caller-owned isolated root; retain every partial failure.

    The supplied root must not exist. It is a disposable builder, never a live
    session path. The caller must serialize access and retain failed builders
    until their observed contents are explicitly reconciled or reviewed.
    """
    try:
        if type(limits) is not SeedLimits or type(identity) is not FixedGitIdentity:
            raise InitialSeedError("seed limits or Git identity have invalid types")
        empty = _operator(operator)
        fields = identity.as_dict()
        if type(fields["timezone"]) is not str or type(fields["object_format"]) is not str:
            raise InitialSeedError("Git identity text must have exact string types")
        identity = FixedGitIdentity.from_dict(fields)
        _identifier(session_id)
        _identifier(q)
        fixed = _FixedIdentityRunner(git, identity)
        bootstrap = initialize_repository(builder_root, git=fixed, identity=identity)
        target = builder_root / _INDEX
        fixed.run(("read-tree", "--empty"), root=builder_root, index_file=target, write=True)
        records = []
        for entry in operator.files:
            oid = fixed.run(("hash-object", "-w", "--stdin"), root=builder_root, stdin=entry.data, write=True).strip().decode("ascii")
            records.append(f"{'100755' if entry.executable else '100644'} {oid}\t{entry.path}\0".encode())
        fixed.run(("update-index", "-z", "--index-info"), root=builder_root, index_file=target, stdin=b"".join(records), write=True)
        tree = fixed.run(("write-tree",), root=builder_root, index_file=target, write=True).strip().decode("ascii")
        commit = _commit_tree(fixed, builder_root, tree, bootstrap,
                              _assembly_message(empty.identity, operator.identity, identity.message_schema), identity)
        assembly = AssemblyIdentity(empty.identity, operator.identity, tree, commit, bootstrap)
        # Discard incidental stat/cache-tree metadata only in this fresh isolated
        # builder. Native validation below checks the canonical index encoding.
        with OwnedDirectory(builder_root) as owned:
            for path, entries in ((".git/index", ()), (_INDEX, operator.files)):
                old = owned.read_file(path, max_bytes=limits.max_file_bytes)
                replacement = FileBytes(_index_bytes(entries), 0o644)
                owned.write_new(path + ".new", replacement.data, mode=replacement.mode)
                owned.move_expected(path + ".new", path, expected_source=replacement, expected_destination=old)
        files, directories, index = _capture(builder_root, limits)
        value = {
            "kind": "initial-seed", "version": 1, "bundle_contract": 1, "operator_contract": 1, "host_contract": 1,
            "q": q, "session_id": session_id, "candidate": _b64(empty.encoded), "operator": _b64(operator.encoded),
            "operator_generation": operator.generation, "git_identity": identity.as_dict(), "assembly": vars(assembly),
            "git_policy": _policy(files),
            "files": [{"path": path, "data": _b64(value.data), "mode": value.mode} for path, value in sorted(files.items())],
            "directories": [{"path": path, "mode": mode} for path, mode in sorted(directories.items())],
            "requested_index": {"data": _b64(index.data), "mode": index.mode},
        }
        encoded = _json(value)
        return validate_initial_seed(encoded, builder_root=builder_root, git=git, limits=limits)
    except (BundleError, GitIdentityError, GitInventoryError, StorageError, OSError, UnicodeError) as error:
        raise InitialSeedError("isolated seed construction failed; builder retained") from error
