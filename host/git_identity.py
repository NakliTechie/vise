"""Deterministic Git object identities for the trusted local host."""

from __future__ import annotations

import hashlib
import os
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path
from host.bundle import BundleError, SourceBundle
from host.operator import OperatorGeneration, validate_candidate
from host.storage import FileBytes, OwnedDirectory, StorageError

__all__ = [
    "AssemblyIdentity",
    "FixedGitIdentity",
    "GitIdentityError",
    "GitInspection",
    "GitRunner",
    "activate_assembly",
    "construct_assembly",
    "initialize_repository",
    "inspect_repository",
    "verify_repository_policy",
    "verify_active_assembly",
]

_CANONICAL_CONFIG = b"""[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
	logallrefupdates = true
	hooksPath = /dev/null
	attributesFile = /dev/null
	excludesFile = /dev/null
"""
_EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"


class GitIdentityError(RuntimeError):
    """Git identity input or repository state violates the fixed policy."""


@dataclass(frozen=True)
class FixedGitIdentity:
    author_name: str
    author_email: str
    committer_name: str
    committer_email: str
    timestamp: int = 0
    timezone: str = "+0000"
    message_schema: int = 1
    object_format: str = "sha1"

    def __post_init__(self) -> None:
        text = (self.author_name, self.author_email, self.committer_name, self.committer_email)
        if any(type(value) is not str or not value or any(char in value for char in "\x00\n\r<>") for value in text):
            raise GitIdentityError("Git names and emails must be nonempty single-line text")
        if type(self.timestamp) is not int or self.timestamp < 0:
            raise GitIdentityError("Git timestamp must be a non-negative integer")
        if (
            self.timezone != "+0000"
            or type(self.message_schema) is not int
            or self.message_schema != 1
            or self.object_format != "sha1"
        ):
            raise GitIdentityError("unsupported fixed Git identity schema")

    def as_dict(self) -> dict[str, object]:
        return {
            "author_email": self.author_email,
            "author_name": self.author_name,
            "committer_email": self.committer_email,
            "committer_name": self.committer_name,
            "message_schema": self.message_schema,
            "object_format": self.object_format,
            "timestamp": self.timestamp,
            "timezone": self.timezone,
        }

    @classmethod
    def from_dict(cls, value: object) -> "FixedGitIdentity":
        keys = {
            "author_email", "author_name", "committer_email", "committer_name",
            "message_schema", "object_format", "timestamp", "timezone",
        }
        if type(value) is not dict or set(value) != keys:
            raise GitIdentityError("invalid fixed Git identity")
        return cls(**value)


@dataclass(frozen=True)
class AssemblyIdentity:
    candidate: str
    operator: str
    tree: str
    commit: str
    bootstrap: str


@dataclass(frozen=True)
class GitInspection:
    head: str | None
    index_tree: str
    commit_tree: str | None
    commit_parent: str | None
    commit_message: bytes | None


class GitRunner:
    """Run one explicitly selected Git binary with a closed environment."""

    def __init__(self, executable: Path, private_home: Path):
        executable = executable.resolve()
        if not executable.is_file() or not os.access(executable, os.X_OK):
            raise GitIdentityError("trusted Git executable is unavailable")
        private_home.mkdir(mode=0o700, parents=True, exist_ok=True)
        if not private_home.is_dir():
            raise GitIdentityError("private Git home is not a directory")
        self.executable = executable
        self.private_home = private_home.resolve()

    def run(
        self,
        args: tuple[str, ...],
        *,
        root: Path,
        stdin: bytes = b"",
        index_file: Path | None = None,
        write: bool = False,
        commit_identity: FixedGitIdentity | None = None,
    ) -> bytes:
        if type(args) is not tuple or any(type(arg) is not str for arg in args):
            raise GitIdentityError("Git arguments must be a fixed string tuple")
        root = root.resolve()
        env = {
            "PATH": str(self.executable.parent),
            "HOME": str(self.private_home),
            "LC_ALL": "C",
            "TZ": "UTC",
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_SYSTEM": os.devnull,
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_COUNT": "0",
            "GIT_TERMINAL_PROMPT": "0",
            "GIT_ATTR_NOSYSTEM": "1",
            "GIT_DIR": str(root / ".git"),
            "GIT_WORK_TREE": str(root),
        }
        if index_file is not None:
            env["GIT_INDEX_FILE"] = str(index_file.resolve())
        if not write:
            env["GIT_OPTIONAL_LOCKS"] = "0"
        if commit_identity is not None:
            env.update({
                "GIT_AUTHOR_NAME": commit_identity.author_name,
                "GIT_AUTHOR_EMAIL": commit_identity.author_email,
                "GIT_AUTHOR_DATE": f"@{commit_identity.timestamp} {commit_identity.timezone}",
                "GIT_COMMITTER_NAME": commit_identity.committer_name,
                "GIT_COMMITTER_EMAIL": commit_identity.committer_email,
                "GIT_COMMITTER_DATE": f"@{commit_identity.timestamp} {commit_identity.timezone}",
            })
        command = (
            str(self.executable),
            "-c", "core.hooksPath=/dev/null",
            "-c", "diff.external=",
            "-c", "filter.lfs.clean=",
            "-c", "filter.lfs.smudge=",
            *args,
        )
        result = subprocess.run(
            command, cwd=root, env=env, input=stdin, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, check=False,
        )
        if result.returncode != 0:
            detail = result.stderr.decode("utf-8", "replace").strip()
            raise GitIdentityError(f"Git command failed ({args[0]}): {detail}")
        return result.stdout


def initialize_repository(root: Path, *, git: GitRunner, identity: FixedGitIdentity) -> str:
    """Initialize a new private SHA-1 repository and return its bootstrap commit."""
    root.mkdir(mode=0o700, parents=False, exist_ok=False)
    env = _init_env(git)
    result = subprocess.run(
        (
            str(git.executable), "init", "--quiet", "--object-format=sha1",
            "--initial-branch=vise-host", "--template=", str(root),
        ),
        env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
    )
    if result.returncode != 0:
        raise GitIdentityError(result.stderr.decode("utf-8", "replace").strip())
    (root / ".git" / "info").mkdir(mode=0o700, exist_ok=False)
    for path, content in (
        (root / ".git" / "config", _CANONICAL_CONFIG),
        (root / ".git" / "info" / "exclude", b""),
        (root / ".git" / "info" / "attributes", b""),
    ):
        path.write_bytes(content)
        path.chmod(0o644)
    verify_repository_policy(root)
    empty_tree = git.run(("mktree",), root=root, stdin=b"", write=True).strip().decode("ascii")
    bootstrap = _commit_tree(git, root, empty_tree, None, b"vise-host bootstrap schema=1\n", identity)
    _validate_bootstrap(_checked_object(root, git, bootstrap, "commit"), identity)
    git.run(("update-ref", "HEAD", bootstrap, ""), root=root, write=True)
    git.run(("read-tree", empty_tree), root=root, write=True)
    return bootstrap


def construct_assembly(
    root: Path,
    *,
    git: GitRunner,
    identity: FixedGitIdentity,
    bootstrap: str,
    candidate: SourceBundle,
    operator: OperatorGeneration,
) -> AssemblyIdentity:
    """Write deterministic blobs/tree/commit without reading work-tree content."""
    verify_repository_policy(root)
    _require_object_id(bootstrap)
    _validate_bootstrap(_checked_object(root, git, bootstrap, "commit"), identity)
    try:
        checked = validate_candidate(candidate, operator)
    except BundleError as error:
        raise GitIdentityError("invalid or colliding C/O inventory") from error
    operator_items = operator.files
    with tempfile.TemporaryDirectory(prefix="vise-git-index-") as temporary:
        index = Path(temporary) / "index"
        git.run(("read-tree", "--empty"), root=root, index_file=index, write=True)
        records = []
        for entry in (*candidate.entries, *operator_items):
            oid = git.run(("hash-object", "-w", "--stdin"), root=root, stdin=entry.data, write=True).strip().decode("ascii")
            _require_object_id(oid)
            mode = "100755" if entry.executable else "100644"
            records.append(f"{mode} {oid}\t{entry.path}\n".encode("utf-8"))
        if records:
            git.run(("update-index", "--index-info"), root=root, stdin=b"".join(records), index_file=index, write=True)
        tree = git.run(("write-tree",), root=root, index_file=index, write=True).strip().decode("ascii")
    message = _assembly_message(candidate.identity, operator.identity, identity.message_schema)
    commit = _commit_tree(git, root, tree, bootstrap, message, identity)
    _verify_tree_objects(root, git, tree)
    return AssemblyIdentity(checked.identity, operator.identity, tree, commit, bootstrap)


def inspect_repository(root: Path, *, git: GitRunner) -> GitInspection:
    """Read actual HEAD, index, and commit linkage without changing repository state."""
    try:
        head = git.run(("rev-parse", "--verify", "HEAD"), root=root).strip().decode("ascii")
    except GitIdentityError:
        head = None
    index_tree = _read_index_tree(root, git)
    if head is None:
        return GitInspection(None, index_tree, None, None, None)
    raw = _checked_object(root, git, head, "commit")
    try:
        header, message = raw.split(b"\n\n", 1)
        lines = header.splitlines()
        trees = [line[5:].decode("ascii") for line in lines if line.startswith(b"tree ")]
        parents = [line[7:].decode("ascii") for line in lines if line.startswith(b"parent ")]
    except (ValueError, UnicodeDecodeError) as error:
        raise GitIdentityError("HEAD commit cannot be inspected") from error
    if len(trees) != 1 or len(parents) > 1:
        raise GitIdentityError("HEAD commit has invalid tree or parent cardinality")
    return GitInspection(head, index_tree, trees[0], parents[0] if parents else None, message)


def verify_repository_policy(root: Path) -> None:
    """Require the exact controller-owned Git configuration files."""
    expected = {
        ".git/config": FileBytes(_CANONICAL_CONFIG, 0o644),
        ".git/info/exclude": FileBytes(b"", 0o644),
        ".git/info/attributes": FileBytes(b"", 0o644),
    }
    try:
        with OwnedDirectory(root) as owned:
            for path, content in expected.items():
                if owned.read_file(path, max_bytes=len(content.data)) != content:
                    raise GitIdentityError(f"Git policy differs at {path}")
    except StorageError as error:
        raise GitIdentityError("Git policy is not descriptor-safe") from error


def verify_active_assembly(
    root: Path, *, git: GitRunner, identity: FixedGitIdentity, expected: AssemblyIdentity
) -> GitInspection:
    """Require actual HEAD/index/commit linkage to equal one assembly identity."""
    verify_repository_policy(root)
    observed = inspect_repository(root, git=git)
    message = _assembly_message(expected.candidate, expected.operator, identity.message_schema)
    if (
        observed.head != expected.commit
        or observed.index_tree != expected.tree
        or observed.commit_tree != expected.tree
        or observed.commit_parent != expected.bootstrap
        or observed.commit_message != message
    ):
        raise GitIdentityError("active Git identity is inconsistent")
    _validate_commit(_checked_object(root, git, expected.commit, "commit"), expected, identity)
    _validate_bootstrap(_checked_object(root, git, expected.bootstrap, "commit"), identity)
    _verify_tree_objects(root, git, expected.tree)
    return observed


def activate_assembly(
    root: Path,
    *,
    git: GitRunner,
    requested: AssemblyIdentity,
    identity: FixedGitIdentity,
    expected_head: str,
    expected_index_tree: str,
    caller_lock_held: bool,
) -> None:
    """Publish index then CAS HEAD; caller must own the enclosing session lock."""
    if caller_lock_held is not True:
        raise GitIdentityError("activation requires the caller-owned session lock")
    _require_object_id(expected_head)
    _require_object_id(expected_index_tree)
    verify_repository_policy(root)
    observed = inspect_repository(root, git=git)
    if observed.head != expected_head or observed.index_tree != expected_index_tree:
        raise GitIdentityError("Git state changed before activation")
    _validate_commit(_checked_object(root, git, requested.commit, "commit"), requested, identity)
    _validate_bootstrap(_checked_object(root, git, requested.bootstrap, "commit"), identity)
    _verify_tree_objects(root, git, requested.tree)
    git.run(("read-tree", requested.tree), root=root, write=True)
    try:
        git.run(("update-ref", "HEAD", requested.commit, expected_head), root=root, write=True)
    except GitIdentityError as error:
        raise GitIdentityError("HEAD compare-and-swap failed after index publication; recovery required") from error


def _assembly_message(candidate: str, operator: str, schema: int) -> bytes:
    return f"vise-host assembly schema={schema}\ncandidate={candidate}\noperator={operator}\n".encode("ascii")


def _validate_commit(raw: bytes, expected: AssemblyIdentity, identity: FixedGitIdentity) -> None:
    wanted = (
        f"tree {expected.tree}\n"
        f"parent {expected.bootstrap}\n"
        f"author {identity.author_name} <{identity.author_email}> {identity.timestamp} {identity.timezone}\n"
        f"committer {identity.committer_name} <{identity.committer_email}> {identity.timestamp} {identity.timezone}\n"
        "\n"
    ).encode("utf-8") + _assembly_message(expected.candidate, expected.operator, identity.message_schema)
    if raw != wanted:
        raise GitIdentityError("requested assembly commit does not match the fixed identity schema")


def _validate_bootstrap(raw: bytes, identity: FixedGitIdentity) -> None:
    wanted = (
        f"tree {_EMPTY_TREE}\n"
        f"author {identity.author_name} <{identity.author_email}> {identity.timestamp} {identity.timezone}\n"
        f"committer {identity.committer_name} <{identity.committer_email}> {identity.timestamp} {identity.timezone}\n"
        "\n"
        "vise-host bootstrap schema=1\n"
    ).encode("utf-8")
    if raw != wanted:
        raise GitIdentityError("bootstrap commit does not match the fixed identity schema")


def _read_index_tree(root: Path, git: GitRunner) -> str:
    """Compute the index tree object ID from ls-files bytes without writing an object."""
    raw = git.run(("ls-files", "--stage", "--full-name", "-z"), root=root)
    tree: dict[bytes, object] = {}
    for record in raw.split(b"\0"):
        if not record:
            continue
        try:
            metadata, path = record.split(b"\t", 1)
            mode, oid, stage = metadata.split(b" ")
        except ValueError as error:
            raise GitIdentityError("index entry has invalid shape") from error
        if stage != b"0" or mode not in (b"100644", b"100755") or len(oid) != 40:
            raise GitIdentityError("index entry has unsupported mode or stage")
        parts = path.split(b"/")
        if any(not part for part in parts):
            raise GitIdentityError("index entry has invalid path")
        node = tree
        for part in parts[:-1]:
            child = node.setdefault(part, {})
            if type(child) is not dict:
                raise GitIdentityError("index contains a prefix collision")
            node = child
        if parts[-1] in node:
            raise GitIdentityError("index contains a duplicate path")
        node[parts[-1]] = (mode, oid)
    return _tree_digest(tree)


def _tree_digest(node: dict[bytes, object]) -> str:
    # Valid bundle paths can have more components than Python's recursion limit.
    # Hash post-order with an explicit stack, preserving Git's directory sorting.
    pending = [(node, False)]
    digests: dict[int, str] = {}
    while pending:
        current, ready = pending.pop()
        if not ready:
            pending.append((current, True))
            pending.extend((value, False) for value in current.values() if type(value) is dict)
            continue
        records: list[tuple[bytes, bytes]] = []
        for name, value in current.items():
            if type(value) is dict:
                oid = digests[id(value)]
                records.append((name + b"/", b"40000 " + name + b"\0" + bytes.fromhex(oid)))
            else:
                mode, oid = value
                records.append((name, mode + b" " + name + b"\0" + bytes.fromhex(oid.decode("ascii"))))
        payload = b"".join(record for _, record in sorted(records))
        digests[id(current)] = hashlib.sha1(b"tree " + str(len(payload)).encode("ascii") + b"\0" + payload).hexdigest()
    return digests[id(node)]


def _checked_object(root: Path, git: GitRunner, oid: str, kind: str) -> bytes:
    """Authenticate object contents, not merely Git's declared object type."""
    _require_object_id(oid)
    raw = git.run(("cat-file", kind, oid), root=root)
    actual = hashlib.sha1(f"{kind} {len(raw)}\0".encode("ascii") + raw).hexdigest()
    if actual != oid:
        raise GitIdentityError("Git object contents do not match their identity")
    return raw


def _verify_tree_objects(root: Path, git: GitRunner, tree: str) -> None:
    """Check every reachable tree/blob without recursion or writing objects."""
    pending = [(tree, "tree")]
    seen: set[tuple[str, str]] = set()
    while pending:
        oid, kind = pending.pop()
        if (oid, kind) in seen:
            continue
        raw = _checked_object(root, git, oid, kind)
        seen.add((oid, kind))
        if kind == "blob":
            continue
        offset = 0
        names: set[bytes] = set()
        while offset < len(raw):
            space = raw.find(b" ", offset)
            end = raw.find(b"\0", space + 1)
            if space < offset or end < 0 or end + 21 > len(raw):
                raise GitIdentityError("Git tree record is malformed")
            mode, name = raw[offset:space], raw[space + 1:end]
            if not name or name in (b".", b"..") or b"/" in name or name in names:
                raise GitIdentityError("Git tree entry name is invalid")
            names.add(name)
            child = raw[end + 1:end + 21].hex()
            if mode == b"40000":
                pending.append((child, "tree"))
            elif mode in (b"100644", b"100755"):
                pending.append((child, "blob"))
            else:
                raise GitIdentityError("Git tree entry mode is unsupported")
            offset = end + 21


def _commit_tree(
    git: GitRunner, root: Path, tree: str, parent: str | None, message: bytes, identity: FixedGitIdentity
) -> str:
    args = ("commit-tree", tree) if parent is None else ("commit-tree", tree, "-p", parent)
    oid = git.run(args, root=root, stdin=message, write=True, commit_identity=identity).strip().decode("ascii")
    _require_object_id(oid)
    return oid


def _init_env(git: GitRunner) -> dict[str, str]:
    return {
        "PATH": str(git.executable.parent), "HOME": str(git.private_home), "LC_ALL": "C", "TZ": "UTC",
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_SYSTEM": os.devnull,
        "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_COUNT": "0",
        "GIT_TERMINAL_PROMPT": "0", "GIT_ATTR_NOSYSTEM": "1",
    }


def _require_object_id(value: str) -> None:
    if type(value) is not str or len(value) != 40 or any(char not in "0123456789abcdef" for char in value):
        raise GitIdentityError("invalid SHA-1 object id")
