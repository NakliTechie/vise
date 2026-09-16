"""Durable empty-C initialization; never initialize Git in the live root.

An isolated builder is retained, including on failure. Before ownership intent
publication an incomplete root is evidence, not an automatically resumable
session. Thereafter only capsule-registered bytes and transition prefixes are
recoverable. Native ref-transaction debris outside the exact completed states
refuses; this module does not infer or reset interrupted Git transactions.
"""

from __future__ import annotations

import hashlib
import os
import tempfile
import uuid
from pathlib import Path

from host import session as s
from host import session_transaction as tx
from host.bundle import build_bundle
from host.git_identity import _EMPTY_TREE, inspect_repository, verify_active_assembly
from host.initial_seed import InitialSeedError, SeedLimits, build_initial_seed, decode_initial_seed
from host.operator import validate_candidate
from host.storage import FileBytes, StorageError


INTENT = ".vise-host/intents/initialize.json"
CAPSULES = ".vise-host/initializations"
CURRENT = ".vise-host/session.json"
LIMITS = SeedLimits()


def _checkpoint(label):
    """No-op seam for bounded process-interruption tests."""


def _paths(owned):
    return {entry.path: entry for entry in owned.walk(max_entries=1_000_000)}


def _capsule_path(seed):
    return CAPSULES + "/" + s._identity_digest(seed.identity)


def _write_capsule(owned, path, encoded):
    """Only sealed controller capsules use the explicit 1-GiB byte ceiling.

    Ordinary storage/public-file/envelope bounds remain unchanged. This fresh
    content-addressed file is retained on failure, before ownership publication.
    """
    if (type(encoded) is not bytes or len(encoded) > LIMITS.max_encoded_bytes
            or path != CAPSULES + "/" + hashlib.sha256(encoded).hexdigest()):
        raise s.SessionError("controller capsule byte bound or content address differs")
    with owned._parent(path) as (parent, name):
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        try:
            os.fchmod(fd, 0o600)
            remaining = memoryview(encoded)
            while remaining:
                written = os.write(fd, remaining[:1024 * 1024])
                if written <= 0:
                    raise s.SessionError("capsule write made no progress")
                remaining = remaining[written:]
            os.fsync(fd)
        finally:
            os.close(fd)
        os.fsync(parent)


def _load_seed(owned):
    paths = _paths(owned)
    names = [path for path in paths if path.startswith(CAPSULES + "/")]
    if len(names) != 1 or paths[names[0]].directory:
        raise s.SessionError("initialization requires one retained sealed capsule")
    raw = owned.read_file(names[0], max_bytes=LIMITS.max_encoded_bytes)
    if raw.mode != 0o600:
        raise s.SessionError("initialization capsule has an invalid mode")
    try:
        seed = decode_initial_seed(raw.data, limits=LIMITS)
    except InitialSeedError as error:
        raise s.SessionError("retained initialization capsule is invalid") from error
    if names[0] != _capsule_path(seed):
        raise s.SessionError("initialization capsule content address differs")
    if (s._load_envelope(owned, seed.candidate.identity) != seed.candidate.encoded
            or s._load_envelope(owned, seed.operator.identity) != seed.operator.encoded):
        raise s.SessionError("initialization retained inputs differ")
    return seed


def _session(root, git, seed):
    return s.Session(root, git, seed.git_identity, seed.assembly.bootstrap, seed.candidate,
                     seed.operator, seed.assembly, seed.session_id, seed.git_policy)


def _terminal(seed):
    return {"operation": "initialize", "phase": "complete", "q": seed.q,
            "requested": s._identity_dict(seed.assembly), "outcome": "complete"}


def validate_retained(owned, session):
    """Retained initial inputs remain bound to immutable session provenance."""
    seed = _load_seed(owned)
    if ((seed.session_id, seed.git_identity, seed.assembly.bootstrap, seed.git_policy)
            != (session.session_id, session.identity, session.bootstrap, session.git_policy)):
        raise s.SessionError("initialization capsule belongs to another session")
    if tx._read(owned, ".vise-host/outcomes/" + seed.q) != s._canonical_json(_terminal(seed)):
        raise s.SessionError("initialization outcome differs from retained capsule")


def _intent(seed, phase):
    return {"version": 1, "operation": "initialize", "phase": phase, "q": seed.q,
            "session_id": seed.session_id, "capsule": seed.identity,
            "recovery": ".vise-host/recovery/" + seed.q}


def _manifest(seed, requested):
    return s._canonical_json({
        "version": 1, "q": seed.q, "session_id": seed.session_id, "capsule": seed.identity,
        "prior": {"candidate": seed.candidate.identity, "operator": None,
                  "tree": _EMPTY_TREE, "commit": seed.assembly.bootstrap},
        "requested": s._decode_canonical_json(tx._state(requested), "initial requested state"),
        "seed_files": [{"path": path, "sha256": hashlib.sha256(value.data).hexdigest(),
                        "size": len(value.data), "mode": value.mode} for path, value in seed.files],
        "seed_directories": dict(seed.directories),
        "requested_index_sha256": hashlib.sha256(seed.requested_index.data).hexdigest(),
    })


def _registered(seed, requested):
    root = _intent(seed, "PREPARING")["recovery"]
    files = {root + "/manifest.json": FileBytes(_manifest(seed, requested), 0o600),
             root + "/index": seed.requested_index}
    directories = {root: 0o700, root + "/requested": 0o700,
                   root + "/copies": 0o700, root + "/install": 0o700}
    files.update({root + "/requested/" + path: content
                  for path, content in tx._files(seed.operator.files).items()})
    directories.update({root + "/requested/" + path: 0o700
                        for path in s._implied_directories(seed.operator.files)})
    files.update({root + "/git-seed/" + path: content for path, content in seed.files})
    directories.update({root + "/git-seed" + ("/" + path if path else ""): mode
                        for path, mode in seed.directories})
    return files, directories


def _stage(seed, path, kind="copies"):
    return ".vise-host/recovery/" + seed.q + "/" + kind + "/" + hashlib.sha256(path.encode()).hexdigest()


def _read_equal(owned, path, expected):
    if owned.read_file(path, max_bytes=len(expected.data)) != expected:
        raise s.SessionError("registered initialization bytes or mode differ: " + path)


def _partial(owned, path, expected):
    actual = owned.read_file(path, max_bytes=len(expected.data))
    # All copies begin in private 0600 stages; chmod follows complete contents.
    if actual.mode not in (0o600, expected.mode) or not expected.data.startswith(actual.data):
        raise s.SessionError("registered initialization stage differs: " + path)
    return actual


def _git_variants(seed):
    base = dict(seed.files)
    following = dict(base)
    following["index"] = seed.requested_index
    active = dict(following)
    ref = base["refs/heads/vise-host"]
    active["refs/heads/vise-host"] = FileBytes((seed.assembly.commit + "\n").encode(), ref.mode)
    identity = seed.git_identity
    line = (f"{seed.assembly.bootstrap} {seed.assembly.commit} {identity.committer_name} "
            f"<{identity.committer_email}> {identity.timestamp} {identity.timezone}\n").encode()
    for path in ("logs/HEAD", "logs/refs/heads/vise-host"):
        active[path] = FileBytes(base[path].data + line, base[path].mode)
    return {"bootstrap": base, "indexed": following, "active": active}


def _preflight(owned, seed, requested, phase, *, complete=False):
    """Classify every name/byte before any recovery mutation or native Git call."""
    paths = _paths(owned)
    root = _intent(seed, phase)["recovery"]
    files, directories = _registered(seed, requested)
    seed_root = root + "/git-seed"
    live_git = ".git" in paths
    if live_git and seed_root in paths:
        raise s.SessionError("initialization has both seed source and Git destination")
    if phase == "PREPARING" and live_git:
        raise s.SessionError("live Git predates replacement authority")
    if phase == "CLEANUP" and not live_git:
        raise s.SessionError("cleanup lacks published Git")
    if phase == "REPLACING" and not live_git and seed_root not in paths:
        raise s.SessionError("initialization lost both seed source and Git destination")
    if live_git:
        files = {path: value for path, value in files.items() if not path.startswith(seed_root + "/")}
        directories = {path: mode for path, mode in directories.items()
                       if path != seed_root and not path.startswith(seed_root + "/")}
    expected = {".vise-host/session.lock": FileBytes(b"", 0o600),
                _capsule_path(seed): FileBytes(seed.encoded, 0o600),
                INTENT: FileBytes(s._canonical_json(_intent(seed, phase)), 0o600)}
    for envelope in (seed.candidate, seed.operator):
        expected[".vise-host/generations/" + s._identity_digest(envelope.identity)] = FileBytes(envelope.encoded, 0o600)
    expected_dirs = {path: 0o700 for path in (
        ".vise-host", ".vise-host/generations", CAPSULES, ".vise-host/intents",
        ".vise-host/recovery", ".vise-host/outcomes")}
    for path, value in expected.items():
        _read_equal(owned, path, value)
    for path in expected_dirs:
        if path not in paths or not paths[path].directory or paths[path].mode != expected_dirs[path]:
            raise s.SessionError("initialization metadata directory differs")
    expected.update(files)
    expected_dirs.update(directories)
    live_files = tx._files(seed.operator.files)
    live_dirs = s._implied_directories(seed.operator.files)
    if phase != "PREPARING":
        expected.update(live_files)
        expected_dirs.update({path: 0o700 for path in live_dirs})
    partials = {}
    if phase == "PREPARING":
        partials.update({_stage(seed, path): content for path, content in files.items()})
    if phase == "REPLACING":
        partials.update({_stage(seed, path, "install"): content for path, content in live_files.items()})
    git_state = None
    if live_git:
        actual = {}
        for path, item in paths.items():
            if path == ".git" or path.startswith(".git/"):
                relative = path[5:] if path != ".git" else ""
                if relative == "index.lock":
                    if phase != "REPLACING":
                        raise s.SessionError("index lock lacks initialization publication authority")
                    partials[path] = seed.requested_index
                elif item.directory:
                    if dict(seed.directories).get(relative) != item.mode:
                        raise s.SessionError("live Git directory differs from capsule")
                else:
                    if relative not in dict(seed.files):
                        raise s.SessionError("unregistered initialization Git entry")
                    actual[relative] = owned.read_file(path, max_bytes=LIMITS.max_file_bytes)
        for name, variant in _git_variants(seed).items():
            if actual == variant:
                git_state = name
        if git_state is None:
            raise s.SessionError("Git bytes are outside the registered initialization sequence")
        for path, mode in seed.directories:
            full = ".git" + ("/" + path if path else "")
            if full not in paths or not paths[full].directory or paths[full].mode != mode:
                raise s.SessionError("required initialized Git directory is missing")
        expected.update({".git/" + path: value for path, value in actual.items()})
        expected_dirs.update({".git" + ("/" + path if path else ""): mode for path, mode in seed.directories})
    state = FileBytes(tx._state(requested), 0o600)
    terminal = FileBytes(s._canonical_json(_terminal(seed)), 0o600)
    outcome = ".vise-host/outcomes/" + seed.q
    if phase != "PREPARING":
        expected[CURRENT] = state
        expected[outcome] = terminal
        if phase == "REPLACING":
            partials[CURRENT + ".new"] = state
            partials[outcome + ".new"] = terminal
    next_phase = {"PREPARING": "REPLACING", "REPLACING": "CLEANUP"}.get(phase)
    if next_phase:
        partials[INTENT + ".new"] = FileBytes(s._canonical_json(_intent(seed, next_phase)), 0o600)
    for path in (CURRENT, outcome):
        if path in paths and path + ".new" in paths:
            raise s.SessionError("initialization has duplicate atomic publication evidence")
    for path, item in paths.items():
        if item.directory:
            mode = expected_dirs.get(path)
            if mode is None or item.mode != mode and not (phase == "PREPARING" and path in directories and item.mode == 0o700):
                raise s.SessionError("unknown initialization directory or mode: " + path)
        elif path == tx.CHRONOLOGY:
            history = tx._read(owned, path)
            if outcome not in paths:
                raise s.SessionError("initial chronology lacks durable terminal evidence")
            last = history.rfind(b"\n") + 1
            if (any(line != terminal.data for line in history[:last].splitlines(keepends=True))
                    or not terminal.data.startswith(history[last:])):
                raise s.SessionError("initial chronology conflicts with terminal outcome")
        elif path in partials:
            _partial(owned, path, partials[path])
        elif path not in expected:
            raise s.SessionError("unknown initialization file: " + path)
        else:
            _read_equal(owned, path, expected[path])
    for path in files:
        if path in paths and _stage(seed, path) in paths:
            raise s.SessionError("initial preparation has both published and staged copy")
    for path in live_files:
        if path in paths and _stage(seed, path, "install") in paths:
            raise s.SessionError("initial installation has both published and staged copy")
    if phase == "PREPARING" and (complete or INTENT + ".new" in paths) or phase == "REPLACING":
        if not set(files).issubset(paths) or not set(directories).issubset(paths):
            raise s.SessionError("required initialization staging is missing")
        for path, mode in directories.items():
            if paths[path].mode != mode:
                raise s.SessionError("complete initialization directory mode differs")
    if phase == "REPLACING" and not live_git and any(path in paths for path in live_files):
        raise s.SessionError("operator installation precedes Git seed publication")
    if git_state in ("indexed", "active") or phase == "CLEANUP":
        s._observe_entries(owned, seed.operator.files)
    if any(path in paths for path in (CURRENT, CURRENT + ".new", outcome, outcome + ".new")):
        if git_state != "active":
            raise s.SessionError("current or outcome precedes active Git identity")
    if outcome in paths or outcome + ".new" in paths or phase == "CLEANUP":
        _read_equal(owned, CURRENT, state)
    if phase == "CLEANUP":
        _read_equal(owned, outcome, terminal)
    if phase == "REPLACING" and INTENT + ".new" in paths:
        _read_equal(owned, outcome, terminal)
        if tx.CHRONOLOGY not in paths or tx._read(owned, tx.CHRONOLOGY) != terminal.data:
            raise s.SessionError("cleanup transition precedes terminal chronology")
    if git_state == "active" and ".git/index.lock" in paths:
        raise s.SessionError("active Git retains an unexpected index lock")
    return git_state


def _chmod_sync(owned, path, mode, *, directory=False):
    with owned._parent(path) as (parent, name):
        flags = os.O_RDONLY | os.O_NOFOLLOW | (os.O_DIRECTORY if directory else 0)
        fd = os.open(name, flags, dir_fd=parent)
        try:
            owned._check_owned(os.fstat(fd), directory=directory)
            os.fchmod(fd, mode)
            os.fsync(fd)
        finally:
            os.close(fd)
        os.fsync(parent)


def _publish_file(owned, path, content, stage, *, prior=None):
    if stage in _paths(owned):
        partial = _partial(owned, stage, content)
        owned.remove_expected(stage, partial)
    owned.write_new(stage, content.data, mode=0o600)
    if content.mode != 0o600:
        _chmod_sync(owned, stage, content.mode)
    owned.move_expected(stage, path, expected_source=content, expected_destination=prior)


def _transition(owned, seed, previous, following):
    tx._atomic(owned, INTENT, s._canonical_json(_intent(seed, following)),
               s._canonical_json(_intent(seed, previous)))
    return following


def _run(owned, seed, requested, phase):
    _preflight(owned, seed, requested, phase)
    root = _intent(seed, phase)["recovery"]
    if phase == "PREPARING":
        files, directories = _registered(seed, requested)
        for path, mode in sorted(directories.items(), key=lambda item: (item[0].count("/"), item[0])):
            owned.mkdirs(path)
            _chmod_sync(owned, path, mode, directory=True)
        for path, content in files.items():
            if path == root + "/manifest.json":
                continue
            if path not in _paths(owned):
                _publish_file(owned, path, content, _stage(seed, path))
        manifest = root + "/manifest.json"
        if manifest not in _paths(owned):
            _publish_file(owned, manifest, files[manifest], _stage(seed, manifest))
        _preflight(owned, seed, requested, phase, complete=True)
        phase = _transition(owned, seed, phase, "REPLACING")
        _checkpoint("after-replacing")
    if phase == "REPLACING":
        state = _preflight(owned, seed, requested, phase)
        if state is None:
            owned.move_tree_expected(root + "/git-seed", ".git", expected_files=dict(seed.files),
                                     expected_directories=dict(seed.directories))
            _checkpoint("after-git-directory-publication")
        for directory in sorted(s._implied_directories(seed.operator.files), key=lambda path: (path.count("/"), path)):
            owned.mkdirs(directory)
        for path, content in tx._files(seed.operator.files).items():
            if path not in _paths(owned):
                _read_equal(owned, root + "/requested/" + path, content)
                _publish_file(owned, path, content, _stage(seed, path, "install"))
                _checkpoint("after-install:" + path)
        state = _preflight(owned, seed, requested, phase)
        if state == "bootstrap":
            _publish_file(owned, ".git/index", seed.requested_index, ".git/index.lock",
                          prior=dict(seed.files)["index"])
        elif ".git/index.lock" in _paths(owned):
            partial = _partial(owned, ".git/index.lock", seed.requested_index)
            owned.remove_expected(".git/index.lock", partial)
        _checkpoint("after-index-publication")
        actual = inspect_repository(requested.root, git=requested.git)
        if (actual.head, actual.index_tree) not in (
                (seed.assembly.bootstrap, seed.assembly.tree), (seed.assembly.commit, seed.assembly.tree)):
            raise s.SessionError("native Git differs from registered initial publication")
        if actual.head != seed.assembly.commit:
            requested.git.run(("update-ref", "HEAD", seed.assembly.commit, seed.assembly.bootstrap),
                              root=requested.root, write=True, commit_identity=seed.git_identity)
            for path in ("refs/heads/vise-host", "logs/HEAD", "logs/refs/heads/vise-host"):
                _chmod_sync(owned, ".git/" + path, dict(seed.files)[path].mode)
        _checkpoint("after-head-cas")
        _preflight(owned, seed, requested, phase)
        verify_active_assembly(requested.root, git=requested.git, identity=requested.identity, expected=requested.assembly)
        if CURRENT not in _paths(owned):
            tx._atomic(owned, CURRENT, tx._state(requested), None, allow_partial=True)
        _checkpoint("after-current-publication")
        _preflight(owned, seed, requested, phase)
        terminal = _terminal(seed)
        outcome = ".vise-host/outcomes/" + seed.q
        if outcome not in _paths(owned):
            tx._atomic(owned, outcome, s._canonical_json(terminal), None, allow_partial=True)
        _checkpoint("after-terminal-outcome")
        tx.append_terminal(owned, terminal)
        _checkpoint("after-terminal-append")
        phase = _transition(owned, seed, phase, "CLEANUP")
        _checkpoint("after-cleanup")
    _preflight(owned, seed, requested, phase)
    verify_active_assembly(requested.root, git=requested.git, identity=requested.identity, expected=requested.assembly)
    tx.append_terminal(owned, _terminal(seed))
    files, directories = _registered(seed, requested)
    for path, content in sorted(files.items(), reverse=True):
        if path in _paths(owned):
            owned.remove_expected(path, content)
            _checkpoint("after-cleanup-delete:" + path.removeprefix(root + "/"))
    for path in sorted(directories, key=lambda value: (value.count("/"), value), reverse=True):
        if path in _paths(owned):
            owned.remove_empty_directory(path)
    _checkpoint("before-intent-removal")
    owned.remove_expected(INTENT, FileBytes(s._canonical_json(_intent(seed, "CLEANUP")), 0o600))
    _checkpoint("after-intent-removal")


def recover(root, git, owned):
    paths = _paths(owned)
    if ".vise-host/intent.json" in paths:
        raise s.SessionError("unsupported legacy initialization intent; evidence retained")
    if INTENT not in paths:
        if INTENT + ".new" in paths or CURRENT not in paths:
            raise s.SessionError("initialization ownership is not durable; evidence retained")
        return
    if tx.INTENT in paths or tx.INTENT + ".new" in paths or ".vise-host/intents/acceptance.json" in paths:
        raise s.SessionError("conflicting initialization operation intents")
    seed = _load_seed(owned)
    value = s._decode_canonical_json(tx._read(owned, INTENT), "initialization intent")
    if type(value) is not dict or value.get("phase") not in ("PREPARING", "REPLACING", "CLEANUP"):
        raise s.SessionError("initialization intent phase is invalid")
    phase = value["phase"]
    if value != _intent(seed, phase) or any(type(value.get(key)) is not type(expected)
                                         for key, expected in _intent(seed, phase).items()):
        raise s.SessionError("initialization ownership binding differs")
    _run(owned, seed, _session(root, git, seed), phase)


def initialize(root, operator, *, git, identity):
    """Retain an isolated builder, then publish a recoverable owned operation."""
    validate_candidate(build_bundle(()), operator)
    s._validate_host_operator(operator)
    if root.exists() or root.is_symlink():
        raise s.SessionError("initialization root already exists")
    container = Path(tempfile.mkdtemp(prefix="vise-initialization-builder-"))
    builder = container / "repository"
    try:
        seed = build_initial_seed(builder, operator, git=git, identity=identity,
                                  session_id=uuid.uuid4().hex, q=uuid.uuid4().hex, limits=LIMITS)
        _checkpoint("after-isolated-builder")
        root.mkdir(mode=0o700)
        parent = os.open(root.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
        with s._session_storage(root) as owned:
            owned.mkdirs(".vise-host")
            owned.write_new(".vise-host/session.lock", b"", mode=0o600)
            with owned.exclusive_lock(".vise-host/session.lock"):
                for path in (".vise-host/generations", CAPSULES, ".vise-host/intents",
                             ".vise-host/recovery", ".vise-host/outcomes"):
                    owned.mkdirs(path)
                s._store_envelope(owned, seed.candidate.identity, seed.candidate.encoded)
                s._store_envelope(owned, seed.operator.identity, seed.operator.encoded)
                _write_capsule(owned, _capsule_path(seed), seed.encoded)
                _checkpoint("before-ownership-intent")
                tx._atomic(owned, INTENT, s._canonical_json(_intent(seed, "PREPARING")), None)
                _checkpoint("after-ownership-intent")
                requested = _session(root, git, seed)
                _run(owned, seed, requested, "PREPARING")
        return s.observe_generation(requested)
    except (InitialSeedError, StorageError, OSError) as error:
        raise s.SessionError(f"initialization failed; isolated builder retained at {builder}") from error
