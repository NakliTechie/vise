"""Persistent host sessions.

The acceptance reconciliation seam is intentionally a separate layer.
"""

from __future__ import annotations

import hashlib
import json
import uuid
from contextlib import contextmanager
from dataclasses import asdict, dataclass
from pathlib import Path

from host.bundle import BundleError, BundleLimits, SourceBundle, SourceEntry, build_bundle, decode_bundle
from host.git_identity import (
    AssemblyIdentity,
    FixedGitIdentity,
    GitIdentityError,
    GitRunner,
    activate_assembly,
    construct_assembly,
    initialize_repository,
    verify_active_assembly,
)
from host.operator import OperatorGeneration, decode_operator, validate_candidate
from host.git_inventory import GitInventoryError, verify_git_layout
from host.storage import FileBytes, OwnedDirectory, StorageError


class SessionError(RuntimeError):
    """Persistent session state is invalid or an operation cannot proceed."""


class RecoveryRequired(SessionError):
    """A registered operation must be reconciled before the session is usable."""


@dataclass(frozen=True)
class FileRecord:
    path: str
    sha256: str
    size: int
    executable: bool


@dataclass(frozen=True)
class MaterializationResult:
    identity: AssemblyIdentity
    observed_candidate: tuple[FileRecord, ...]
    observed_operator: tuple[FileRecord, ...]


@dataclass
class Session:
    root: Path
    git: GitRunner
    identity: FixedGitIdentity
    bootstrap: str
    candidate: SourceBundle
    operator: OperatorGeneration
    assembly: AssemblyIdentity
    session_id: str
    git_policy: str


_SESSION_VERSION = 1
_ENVELOPE_LIMIT = 384 * 1024 * 1024
# Internal backup names add this fixed prefix to an already-validated public path.
# Public C/O validation retains BundleLimits' original path-byte bound.
_RECOVERY_PATH_PREFIX_BYTES = len((".vise-host/recovery/" + "0" * 32 + "/requested/").encode("ascii"))
_STORAGE_LIMITS = BundleLimits(
    max_file_bytes=_ENVELOPE_LIMIT,
    max_path_bytes=BundleLimits().max_path_bytes + _RECOVERY_PATH_PREFIX_BYTES,
)
_REQUIRED_IGNORE = {
    b".vise-host/", b".vise/journal.jsonl", b".vise/run.lock", b".vise/tmp/",
}


def initialize_session(
    root: Path,
    operator: OperatorGeneration,
    *,
    git: GitRunner,
    identity: FixedGitIdentity,
) -> MaterializationResult:
    """Create and publish an empty-C/initial-O session."""
    empty = build_bundle(())
    try:
        validate_candidate(empty, operator)
        _validate_host_operator(operator)
        root.mkdir(mode=0o700, parents=False, exist_ok=False)
        (root / ".vise-host").mkdir(mode=0o700)
        with _session_storage(root) as owned:
            owned.write_new(".vise-host/session.lock", b"", mode=0o600)
            with owned.exclusive_lock(".vise-host/session.lock"):
                q = uuid.uuid4().hex
                initial_intent = _canonical_json({
                    "version": 1, "operation": "initialize", "phase": "PREPARING", "q": q,
                })
                owned.write_new(".vise-host/intent.json", initial_intent, mode=0o600)
                bootstrap = initialize_repository(
                    root, git=git, identity=identity,
                    prestate={
                        ".vise-host/session.lock": FileBytes(b"", 0o600),
                        ".vise-host/intent.json": FileBytes(initial_intent, 0o600),
                    },
                    caller_lock_held=True,
                )
                owned.mkdirs(".vise-host/generations")
                _store_envelope(owned, empty.identity, empty.encoded)
                _store_envelope(owned, operator.identity, operator.encoded)
                assembly = construct_assembly(
                    root, git=git, identity=identity, bootstrap=bootstrap,
                    candidate=empty, operator=operator,
                )
                _install_entries(owned, (), operator.files)
                initial = _git_state(root, git)
                activate_assembly(
                    root, git=git, requested=assembly, identity=identity,
                    expected_head=initial[0], expected_index_tree=initial[1], caller_lock_held=True,
                )
                session = Session(
                    root, git, identity, bootstrap, empty, operator, assembly,
                    uuid.uuid4().hex, _git_policy_identity(owned),
                )
                _publish_session(owned, session, destination=None)
                _append_operation(owned, {
                    "operation": "initialize", "phase": "complete", "q": q,
                    "requested": _identity_dict(assembly), "outcome": "complete",
                })
                _remove_intent(owned)
    except (BundleError, GitIdentityError, GitInventoryError, StorageError, OSError) as error:
        raise SessionError("session initialization failed") from error
    return observe_generation(session)


def open_session(root: Path, *, git: GitRunner) -> Session:
    """Strictly reopen persistent identity without caller-supplied identity values."""
    try:
        with _locked_storage(root) as owned:
            from host.session_transaction import recover
            recover(root, git, owned)
            session = _load_session(root, git, owned)
            _observe_locked(session, owned)
            return session
    except (BundleError, GitIdentityError, GitInventoryError, StorageError, OSError, KeyError, TypeError) as error:
        if isinstance(error, SessionError):
            raise
        raise SessionError("session reopen failed") from error


def _load_session(root: Path, git: GitRunner, owned: OwnedDirectory, *, pending: bool = False) -> Session:
    if not pending:
        verify_git_layout(owned)
    raw = owned.read_file(".vise-host/session.json", max_bytes=1024 * 1024).data
    value = _decode_canonical_json(raw, "session")
    required = {
        "version", "bundle_contract", "operator_contract", "host_contract",
        "session_id", "git_policy", "git_identity", "bootstrap", "current",
        "candidate_inventory", "operator_inventory",
    }
    if type(value) is not dict or set(value) != required:
        raise SessionError("session schema is invalid")
    versions = (value["version"], value["bundle_contract"], value["operator_contract"], value["host_contract"])
    if any(type(item) is not int for item in versions) or versions != (1, 1, 1, 1):
        raise SessionError("session contract version is unsupported")
    identity = FixedGitIdentity.from_dict(value["git_identity"])
    _validate_retained_envelopes(owned)
    current = value["current"]
    if type(current) is not dict or set(current) != {
        "candidate_envelope", "operator_envelope", "operator_generation", "tree", "commit"
    }:
        raise SessionError("session current pointer is invalid")
    candidate = decode_bundle(_load_envelope(owned, current["candidate_envelope"]))
    operator = decode_operator(_load_envelope(owned, current["operator_envelope"]))
    validate_candidate(candidate, operator)
    _validate_host_operator(operator)
    if type(current["operator_generation"]) is not int or operator.generation != current["operator_generation"]:
        raise SessionError("operator generation differs from current pointer")
    assembly = AssemblyIdentity(
        candidate.identity, operator.identity, current["tree"], current["commit"], value["bootstrap"]
    )
    if any(type(item) is not str for item in (
        current["candidate_envelope"], current["operator_envelope"], current["tree"],
        current["commit"], value["bootstrap"], value["git_policy"],
    )):
        raise SessionError("session identity fields have invalid types")
    if assembly != _compute_historical_identity(candidate, operator, value["bootstrap"], identity):
        raise SessionError("current assembly identity differs from retained envelopes")
    if (
        type(value["session_id"]) is not str
        or len(value["session_id"]) != 32
        or any(char not in "0123456789abcdef" for char in value["session_id"])
        or value["git_policy"] != _git_policy_identity(owned)
    ):
        raise SessionError("session or Git policy identity is invalid")
    session = Session(
        root, git, identity, value["bootstrap"], candidate, operator, assembly,
        value["session_id"], value["git_policy"],
    )
    _require_inventory(value["candidate_inventory"], candidate.entries, "candidate")
    _require_inventory(value["operator_inventory"], operator.files, "operator")
    if not pending:
        _validate_chronology(owned)
        _refuse_intent(owned)
    return session


def materialize_candidate(session: Session, candidate: SourceBundle) -> MaterializationResult:
    """Replace only retained C paths while preserving O under the session lock."""
    try:
        validate_candidate(candidate, session.operator)
        with _locked_storage(session.root) as owned:
            _refresh(session, owned)
            from host.session_transaction import replace
            replace(session, owned, candidate, session.operator, "materialize")
        return observe_generation(session)
    except (BundleError, GitIdentityError, GitInventoryError, StorageError, OSError) as error:
        if isinstance(error, SessionError):
            raise
        raise SessionError("candidate materialization failed; reconciliation required") from error


def advance_operator(
    session: Session,
    *,
    expected_identity: str,
    expected_generation: int,
    replacement: OperatorGeneration,
) -> MaterializationResult:
    """Advance O by exactly one while preserving C."""
    try:
        validate_candidate(session.candidate, replacement)
        _validate_host_operator(replacement)
        with _locked_storage(session.root) as owned:
            _refresh(session, owned)
            if (type(expected_identity) is not str or type(expected_generation) is not int
                    or expected_identity != session.operator.identity
                    or expected_generation != session.operator.generation):
                raise SessionError("stale operator expectation")
            if replacement.generation != session.operator.generation + 1:
                raise SessionError("operator generation must advance by exactly one")
            from host.session_transaction import replace
            replace(session, owned, session.candidate, replacement, "advance-operator")
        return observe_generation(session)
    except (BundleError, GitIdentityError, GitInventoryError, StorageError, OSError) as error:
        if isinstance(error, SessionError):
            raise
        raise SessionError("operator advance failed; reconciliation required") from error


def observe_generation(session: Session) -> MaterializationResult:
    """Require work tree, index, HEAD, and retained identities to agree."""
    try:
        with _locked_storage(session.root) as owned:
            _refresh(session, owned)
            return _observe_locked(session, owned)
    except (GitIdentityError, GitInventoryError, StorageError, OSError) as error:
        raise SessionError("active generation is inconsistent") from error


def reconcile_materialization(session: Session) -> MaterializationResult:
    """Reconcile a registered replacement and refresh this session handle."""
    reopened = open_session(session.root, git=session.git)
    if reopened.session_id != session.session_id:
        raise SessionError("recovery session identity differs")
    session.__dict__.update(reopened.__dict__)
    return observe_generation(session)


def _observe_locked(session: Session, owned: OwnedDirectory) -> MaterializationResult:
    verify_git_layout(owned)
    verify_active_assembly(session.root, git=session.git, identity=session.identity, expected=session.assembly)
    if session.git.run(
        ("status", "--porcelain=v1", "-z", "--untracked-files=all"), root=session.root
    ):
        raise SessionError("Git work tree is not clean")
    candidate = _observe_entries(owned, session.candidate.entries)
    operator = _observe_entries(owned, session.operator.files)
    _validate_observed_paths(owned, session.candidate.entries, session.operator.files)
    return MaterializationResult(session.assembly, candidate, operator)


def _validate_observed_paths(
    owned: OwnedDirectory,
    candidate: tuple[SourceEntry, ...],
    operator: tuple[SourceEntry, ...],
    *,
    registered_files: set[str] | None = None,
    registered_directories: set[str] | None = None,
) -> None:
    exact_files = {entry.path for entry in (*candidate, *operator)}
    exact_files.update({
        ".vise-host/session.lock", ".vise-host/session.json",
        ".vise-host/operations.jsonl",
    })
    exact_directories = _implied_directories((*candidate, *operator)) | {
        ".git", ".vise", ".vise-host", ".vise-host/generations",
        ".vise-host/intents", ".vise-host/recovery", ".vise-host/outcomes",
    }
    exact_files.update(registered_files or ())
    exact_directories.update(registered_directories or ())
    for entry in (*candidate, *operator):
        if entry.path.startswith(".vise/"):
            exact_directories.update(_implied_directories((entry,)))
    for observed in owned.walk(max_entries=1_000_000):
        path = observed.path
        if path.startswith(".git/") or path == ".git":
            continue
        if path.startswith(".vise-host/generations/") and not observed.directory:
            name = path.removeprefix(".vise-host/generations/")
            if len(name) == 64 and all(char in "0123456789abcdef" for char in name):
                continue
        if path.startswith(".vise-host/outcomes/") and not observed.directory:
            name = path.removeprefix(".vise-host/outcomes/")
            if len(name) == 32 and all(char in "0123456789abcdef" for char in name):
                continue
        if path == ".vise/tmp" or path.startswith(".vise/tmp/"):
            continue
        if path in (".vise/journal.jsonl", ".vise/run.lock") and not observed.directory:
            continue
        allowed = exact_directories if observed.directory else exact_files
        if path not in allowed:
            raise SessionError(f"unknown session path: {path}")


def inspect_historical(
    session: Session, candidate_identity: str, operator_identity: str
) -> AssemblyIdentity:
    """Recompute retained historical T/K without activating it."""
    try:
        with _locked_storage(session.root) as owned:
            _refresh(session, owned)
            candidate = decode_bundle(_load_envelope(owned, candidate_identity))
            operator = decode_operator(_load_envelope(owned, operator_identity))
            validate_candidate(candidate, operator)
            return _compute_historical_identity(candidate, operator, session.bootstrap, session.identity)
    except (BundleError, GitIdentityError, GitInventoryError, StorageError, OSError) as error:
        raise SessionError("historical generation is invalid") from error


def _refresh(session: Session, owned: OwnedDirectory) -> None:
    reopened = _load_session(session.root, session.git, owned)
    _observe_locked(reopened, owned)
    if (reopened.assembly != session.assembly or reopened.operator != session.operator
            or reopened.candidate != session.candidate or reopened.identity != session.identity
            or reopened.bootstrap != session.bootstrap or reopened.session_id != session.session_id
            or reopened.git_policy != session.git_policy):
        raise SessionError("session handle is stale")


def _replace_entries(
    owned: OwnedDirectory,
    prior: tuple[SourceEntry, ...],
    requested: tuple[SourceEntry, ...],
    *,
    retained: tuple[SourceEntry, ...],
) -> None:
    for entry in sorted(prior, key=lambda item: item.path, reverse=True):
        owned.remove_expected(entry.path, FileBytes(entry.data, 0o755 if entry.executable else 0o644))
    prior_directories = _implied_directories((*prior, *retained))
    requested_directories = _implied_directories((*requested, *retained))
    for directory in sorted(prior_directories - requested_directories, reverse=True):
        owned.remove_empty_directory(directory)
    _install_entries(owned, (), requested)


def _install_entries(owned: OwnedDirectory, prior: tuple[SourceEntry, ...], entries: tuple[SourceEntry, ...]) -> None:
    del prior
    for directory in sorted(_implied_directories(entries), key=lambda value: (value.count("/"), value)):
        owned.mkdirs(directory)
    for entry in entries:
        owned.write_new(entry.path, entry.data, mode=0o755 if entry.executable else 0o644)


def _implied_directories(entries: tuple[SourceEntry, ...]) -> set[str]:
    result = set()
    for entry in entries:
        parts = entry.path.split("/")
        result.update("/".join(parts[:depth]) for depth in range(1, len(parts)))
    return result


def _store_envelope(owned: OwnedDirectory, identity: str, encoded: bytes) -> None:
    path = ".vise-host/generations/" + _identity_digest(identity)
    try:
        existing = owned.read_file(path, max_bytes=len(encoded))
    except StorageError:
        owned.write_new(path, encoded, mode=0o600)
    else:
        if existing != FileBytes(encoded, 0o600):
            raise SessionError("retained envelope differs from its identity")


def _load_envelope(owned: OwnedDirectory, identity: object) -> bytes:
    if type(identity) is not str:
        raise SessionError("envelope identity is invalid")
    record = owned.read_file(".vise-host/generations/" + _identity_digest(identity), max_bytes=_ENVELOPE_LIMIT)
    if record.mode != 0o600 or "sha256:" + hashlib.sha256(record.data).hexdigest() != identity:
        raise SessionError("envelope bytes or mode differ from content address")
    return record.data


def _validate_retained_envelopes(owned: OwnedDirectory) -> None:
    """Retained history is typed reconstruction material, not an ignored cache."""
    prefix = ".vise-host/generations/"
    for entry in owned.walk(max_entries=1_000_000):
        if not entry.path.startswith(prefix):
            continue
        if entry.directory:
            raise SessionError("retained envelope namespace contains a directory")
        encoded = _load_envelope(owned, "sha256:" + entry.path.removeprefix(prefix))
        value = _decode_canonical_json(encoded, "retained envelope")
        if type(value) is dict and value.get("kind") == "operator":
            _validate_host_operator(decode_operator(encoded))
        else:
            candidate = decode_bundle(encoded)
            if any(part.casefold() in {".gitignore", ".gitattributes"}
                   for item in candidate.entries for part in item.path.split("/")):
                raise SessionError("retained candidate contains operator-owned Git policy")


def _identity_digest(identity: str) -> str:
    if not identity.startswith("sha256:") or len(identity) != 71:
        raise SessionError("envelope identity is invalid")
    digest = identity[7:]
    if any(char not in "0123456789abcdef" for char in digest):
        raise SessionError("envelope identity is invalid")
    return digest


def _observe_entries(owned: OwnedDirectory, entries: tuple[SourceEntry, ...]) -> tuple[FileRecord, ...]:
    result = []
    for entry in entries:
        mode = 0o755 if entry.executable else 0o644
        observed = owned.read_file(entry.path, max_bytes=len(entry.data))
        if observed != FileBytes(entry.data, mode):
            raise SessionError(f"observed file differs: {entry.path}")
        result.append(FileRecord(entry.path, "sha256:" + hashlib.sha256(entry.data).hexdigest(),
                                 len(entry.data), entry.executable))
    return tuple(result)


def _publish_session(owned: OwnedDirectory, session: Session, destination: bytes | None) -> None:
    encoded = _session_bytes(session, candidate=session.candidate, operator=session.operator, assembly=session.assembly)
    stage = ".vise-host/session.json.new"
    owned.write_new(stage, encoded, mode=0o600)
    owned.move_expected(stage, ".vise-host/session.json", expected_source=FileBytes(encoded, 0o600),
                        expected_destination=None if destination is None else FileBytes(destination, 0o600))


def _session_bytes(
    session: Session, *, candidate: SourceBundle, operator: OperatorGeneration, assembly: AssemblyIdentity
) -> bytes:
    value = {
        "version": 1, "bundle_contract": 1, "operator_contract": 1, "host_contract": 1,
        "session_id": session.session_id, "git_policy": session.git_policy,
        "git_identity": session.identity.as_dict(), "bootstrap": session.bootstrap,
        "current": {
            "candidate_envelope": candidate.identity, "operator_envelope": operator.identity,
            "operator_generation": operator.generation, "tree": assembly.tree, "commit": assembly.commit,
        },
        "candidate_inventory": [asdict(item) for item in _records(candidate.entries)],
        "operator_inventory": [asdict(item) for item in _records(operator.files)],
    }
    return _canonical_json(value)


def _records(entries: tuple[SourceEntry, ...]) -> tuple[FileRecord, ...]:
    return tuple(FileRecord(e.path, "sha256:" + hashlib.sha256(e.data).hexdigest(),
                            len(e.data), e.executable) for e in entries)


def _compute_historical_identity(
    candidate: SourceBundle,
    operator: OperatorGeneration,
    bootstrap: str,
    identity: FixedGitIdentity,
) -> AssemblyIdentity:
    root: dict[bytes, object] = {}
    for entry in (*candidate.entries, *operator.files):
        node = root
        parts = entry.path.encode("utf-8").split(b"/")
        for part in parts[:-1]:
            node = node.setdefault(part, {})
        blob = _git_digest(b"blob", entry.data)
        node[parts[-1]] = (b"100755" if entry.executable else b"100644", blob)
    pending = [(root, False)]
    digests: dict[int, str] = {}
    while pending:
        node, expanded = pending.pop()
        if not expanded:
            pending.append((node, True))
            pending.extend((value, False) for value in node.values() if type(value) is dict)
            continue
        records = []
        for name, value in node.items():
            if type(value) is dict:
                oid = digests[id(value)]
                records.append((name + b"/", b"40000 " + name + b"\0" + bytes.fromhex(oid)))
            else:
                mode, oid = value
                records.append((name, mode + b" " + name + b"\0" + bytes.fromhex(oid)))
        payload = b"".join(record for _, record in sorted(records))
        digests[id(node)] = _git_digest(b"tree", payload)
    tree = digests[id(root)]
    message = (
        f"vise-host assembly schema={identity.message_schema}\n"
        f"candidate={candidate.identity}\noperator={operator.identity}\n"
    ).encode("ascii")
    commit = (
        f"tree {tree}\nparent {bootstrap}\n"
        f"author {identity.author_name} <{identity.author_email}> {identity.timestamp} {identity.timezone}\n"
        f"committer {identity.committer_name} <{identity.committer_email}> {identity.timestamp} {identity.timezone}\n"
        "\n"
    ).encode("utf-8") + message
    return AssemblyIdentity(candidate.identity, operator.identity, tree, _git_digest(b"commit", commit), bootstrap)


def _git_digest(kind: bytes, payload: bytes) -> str:
    return hashlib.sha1(kind + b" " + str(len(payload)).encode("ascii") + b"\0" + payload).hexdigest()


def _validate_host_operator(operator: OperatorGeneration) -> None:
    ignore = next((entry for entry in operator.files if entry.path == ".gitignore"), None)
    if ignore is None or ignore.executable:
        raise SessionError("operator generation lacks the canonical root .gitignore")
    lines = set(ignore.data.splitlines())
    if not _REQUIRED_IGNORE.issubset(lines):
        raise SessionError("operator .gitignore lacks required controller/runtime rules")


def _git_policy_identity(owned: OwnedDirectory) -> str:
    pieces = []
    for path in (".git/config", ".git/info/exclude", ".git/info/attributes"):
        value = owned.read_file(path, max_bytes=1024 * 1024)
        pieces.append(
            path.encode("ascii") + b"\0" + value.data + b"\0"
            + str(value.mode).encode("ascii") + b"\n"
        )
    return "sha256:" + hashlib.sha256(b"".join(pieces)).hexdigest()


def _require_inventory(value: object, entries: tuple[SourceEntry, ...], label: str) -> None:
    if type(value) is not list:
        raise SessionError(f"{label} inventory has invalid type")
    for item in value:
        if (
            type(item) is not dict
            or set(item) != {"path", "sha256", "size", "executable"}
            or type(item["path"]) is not str
            or type(item["sha256"]) is not str
            or type(item["size"]) is not int
            or type(item["executable"]) is not bool
        ):
            raise SessionError(f"{label} inventory fields have invalid types")
    expected = [asdict(item) for item in _records(entries)]
    if value != expected:
        raise SessionError(f"{label} inventory differs from envelope")


def _write_intent(
    owned: OwnedDirectory, q: str, phase: str, prior: AssemblyIdentity, requested: AssemblyIdentity
) -> None:
    value = {"version": 1, "q": q, "phase": phase,
             "prior": _identity_dict(prior), "requested": _identity_dict(requested)}
    owned.write_new(".vise-host/intent.json", _canonical_json(value), mode=0o600)


def _remove_intent(owned: OwnedDirectory) -> None:
    raw = owned.read_file(".vise-host/intent.json", max_bytes=1024 * 1024)
    owned.remove_expected(".vise-host/intent.json", raw)


def _refuse_intent(owned: OwnedDirectory) -> None:
    paths = {entry.path for entry in owned.walk(max_entries=1_000_000)}
    if any(path.startswith(".vise-host/intents/") for path in paths):
        raise RecoveryRequired("nonterminal materialization intent requires recovery")
    try:
        owned.read_file(".vise-host/intent.json", max_bytes=1024 * 1024)
    except StorageError:
        paths = {entry.path for entry in owned.walk(max_entries=100_000)}
        if ".vise-host/intent.json" in paths:
            raise SessionError("materialization intent is unreadable")
        return
    raise SessionError("nonterminal materialization intent requires recovery")


def _append_operation(owned: OwnedDirectory, value: dict[str, object]) -> None:
    from host.session_transaction import append_terminal
    append_terminal(owned, value, durable=False)


def _validate_chronology(owned: OwnedDirectory) -> None:
    data = owned.read_file(".vise-host/operations.jsonl", max_bytes=16 * 1024 * 1024)
    if data.mode != 0o600:
        raise SessionError("chronology has an invalid mode")
    _validate_json_lines(data.data)
    records = {_decode_canonical_json(line, "operation")["q"]: line
               for line in data.data.splitlines(keepends=True)}
    for entry in owned.walk(max_entries=1_000_000):
        if entry.path.startswith(".vise-host/outcomes/") and not entry.directory:
            q = entry.path.removeprefix(".vise-host/outcomes/")
            terminal = owned.read_file(entry.path, max_bytes=1024 * 1024)
            if terminal.mode != 0o600 or records.get(q) != terminal.data:
                raise SessionError("retained terminal outcome differs from chronology")


def _validate_json_lines(data: bytes) -> None:
    seen = {}
    for line in data.splitlines(keepends=True):
        value = _decode_canonical_json(line, "operation")
        if type(value) is not dict:
            raise SessionError("operation record is invalid")
        initial = value.get("operation") == "initialize"
        fields = {"operation", "phase", "q", "requested", "outcome"}
        if not initial:
            fields.update({"prior", "refusal"})
        if (set(value) != fields or value["operation"] not in ("initialize", "materialize", "advance-operator")
                or value["phase"] != "complete" or value["outcome"] != "complete"
                or type(value["q"]) is not str or len(value["q"]) != 32
                or any(char not in "0123456789abcdef" for char in value["q"])):
            raise SessionError("operation record has invalid fields or terminal values")
        _require_record_identity(value["requested"])
        if not initial:
            _require_record_identity(value["prior"])
            prior_identity, requested_identity = value["prior"], value["requested"]
            if value["refusal"] is not None or prior_identity["bootstrap"] != requested_identity["bootstrap"]:
                raise SessionError("operation terminal identity or refusal differs")
            if value["operation"] == "materialize" and prior_identity["operator"] != requested_identity["operator"]:
                raise SessionError("candidate chronology changes operator authority")
            if value["operation"] == "advance-operator" and (
                prior_identity["candidate"] != requested_identity["candidate"]
                or prior_identity["operator"] == requested_identity["operator"]
            ):
                raise SessionError("operator chronology violates separate authority")
        prior = seen.get(value["q"])
        if prior is not None and prior != line:
            raise SessionError("operation id conflicts")
        seen[value["q"]] = line


def _require_record_identity(value: object) -> None:
    if (type(value) is not dict or set(value) != {"candidate", "operator", "tree", "commit", "bootstrap"}
            or any(type(item) is not str for item in value.values())):
        raise SessionError("operation assembly identity schema is invalid")
    _identity_digest(value["candidate"])
    _identity_digest(value["operator"])
    for key in ("tree", "commit", "bootstrap"):
        if len(value[key]) != 40 or any(char not in "0123456789abcdef" for char in value[key]):
            raise SessionError("operation Git identity is invalid")


def _identity_dict(value: AssemblyIdentity) -> dict[str, str]:
    return asdict(value)


def _canonical_json(value: object) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode("ascii")


def _decode_canonical_json(raw: bytes, label: str) -> object:
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError, RecursionError, ValueError) as error:
        raise SessionError(f"{label} JSON is invalid") from error
    if _canonical_json(value) != raw:
        raise SessionError(f"{label} JSON is not canonical")
    return value


def _git_state(root: Path, git: GitRunner) -> tuple[str, str]:
    from host.git_identity import inspect_repository
    observed = inspect_repository(root, git=git)
    if observed.head is None:
        raise SessionError("initialized repository has no bootstrap HEAD")
    return observed.head, observed.index_tree


def _session_storage(root: Path) -> OwnedDirectory:
    return OwnedDirectory(root, limits=_STORAGE_LIMITS)


@contextmanager
def _locked_storage(root: Path):
    """Existing sessions never recreate or adopt a missing/replaced lock file."""
    with _session_storage(root) as owned:
        if owned.read_file(".vise-host/session.lock", max_bytes=0) != FileBytes(b"", 0o600):
            raise SessionError("session lock differs from its registered bytes and mode")
        with owned.exclusive_lock(".vise-host/session.lock"):
            yield owned
