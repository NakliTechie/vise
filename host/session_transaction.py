"""Controller-only durable replacement transactions; no candidate execution.

Recovery always completes a registered replacement. PREPARING may resume only
while the entire prior generation remains exact. CLEANUP uses an immutable
terminal outcome, and does not require backups which it already removed.
"""
from __future__ import annotations

import hashlib
import os
import uuid
from dataclasses import asdict, replace as dataclass_replace

from host import session as s
from host.bundle import decode_bundle
from host.git_identity import (
    construct_assembly, inspect_repository, verify_active_assembly, verify_repository_policy,
    _checked_object, _validate_bootstrap, _validate_commit, _verify_tree_objects,
)
from host.git_inventory import verify_git_layout
from host.operator import decode_operator, validate_candidate
from host.storage import FileBytes

INTENT = ".vise-host/intents/materialize.json"
CHRONOLOGY = ".vise-host/operations.jsonl"


def _checkpoint(label: str) -> None:
    """Finite crash seam: production performs no work here."""


def _paths(owned):
    return {entry.path: entry for entry in owned.walk(max_entries=1_000_000)}


def _read(owned, path):
    value = owned.read_file(path, max_bytes=s._ENVELOPE_LIMIT)
    if value.mode != 0o600:
        raise s.SessionError(f"invalid controller file mode: {path}")
    return value.data


def _atomic(owned, path, data, prior, *, allow_partial=False, checkpoint=None):
    if prior is not None:
        if _read(owned, path) != prior:
            raise s.SessionError("atomic publication authority changed")
        allow_partial = True
    stage = path + ".new"
    paths = _paths(owned)
    if stage in paths:
        observed = _read(owned, stage)
        if observed != data:
            if not allow_partial or not data.startswith(observed):
                raise s.SessionError("atomic publication stage differs")
            owned.remove_expected(stage, FileBytes(observed, 0o600))
            owned.write_new(stage, data, mode=0o600)
    else:
        owned.write_new(stage, data, mode=0o600)
    if checkpoint:
        _checkpoint("after-" + checkpoint + "-stage")
        _checkpoint("before-" + checkpoint + "-publication")
    owned.move_expected(stage, path, expected_source=FileBytes(data, 0o600),
                        expected_destination=None if prior is None else FileBytes(prior, 0o600))
    if checkpoint:
        _checkpoint("after-" + checkpoint + "-publication")


def _state(session):
    return s._session_bytes(session, candidate=session.candidate, operator=session.operator,
                            assembly=session.assembly)


def _side(live, owned, value):
    if type(value) is not dict or type(value.get("current")) is not dict:
        raise s.SessionError("replacement state has invalid schema")
    current = value["current"]
    try:
        candidate = decode_bundle(s._load_envelope(owned, current["candidate_envelope"]))
        operator = decode_operator(s._load_envelope(owned, current["operator_envelope"]))
    except (KeyError, TypeError) as error:
        raise s.SessionError("replacement envelope pointer is invalid") from error
    validate_candidate(candidate, operator)
    s._validate_host_operator(operator)
    assembly = s._compute_historical_identity(candidate, operator, live.bootstrap, live.identity)
    side = dataclass_replace(live, candidate=candidate, operator=operator, assembly=assembly)
    if _state(side) != s._canonical_json(value):
        raise s.SessionError("replacement state differs from canonical retained identity")
    return side


def _intent(prior, requested, q, operation, phase="PREPARING"):
    return {
        "version": 1, "q": q, "phase": phase, "operation": operation,
        "session_id": prior.session_id, "recovery": ".vise-host/recovery/" + q,
        "prior": s._decode_canonical_json(_state(prior), "prior"),
        "requested": s._decode_canonical_json(_state(requested), "requested"),
    }


def _validate_intent(live, owned, value):
    if type(value) is not dict or set(value) != {
        "version", "q", "phase", "operation", "session_id", "recovery", "prior", "requested",
    }:
        raise s.SessionError("replacement intent schema is invalid")
    q = value["q"]
    if (type(q) is not str or len(q) != 32 or any(c not in "0123456789abcdef" for c in q)
            or type(value["version"]) is not int or value["version"] != 1
            or value["session_id"] != live.session_id
            or value["recovery"] != ".vise-host/recovery/" + q
            or value["phase"] not in ("PREPARING", "REPLACING", "CLEANUP")
            or value["operation"] not in ("materialize", "advance-operator")):
        raise s.SessionError("replacement ownership or phase is invalid")
    prior = _side(live, owned, value["prior"])
    requested = _side(live, owned, value["requested"])
    if value["operation"] == "materialize":
        if prior.operator != requested.operator:
            raise s.SessionError("candidate replacement changes operator authority")
    elif (prior.candidate != requested.candidate
          or requested.operator.generation != prior.operator.generation + 1):
        raise s.SessionError("operator replacement violates monotonic authority")
    if _state(live) not in (_state(prior), _state(requested)):
        raise s.SessionError("current pointer is outside registered replacement")
    return prior, requested


def _entries(side):
    return tuple(sorted((*side.candidate.entries, *side.operator.files), key=lambda e: e.path))


def _files(entries):
    return {e.path: FileBytes(e.data, 0o755 if e.executable else 0o644) for e in entries}


def _manifest(value, prior, requested):
    prior_records = [asdict(item) for item in s._records(_entries(prior))]
    requested_records = [asdict(item) for item in s._records(_entries(requested))]
    return s._canonical_json({
        "version": 1, "q": value["q"], "session_id": prior.session_id,
        "prior": value["prior"], "requested": value["requested"],
        "prior_head": prior.assembly.commit, "requested_head": requested.assembly.commit,
        "prior_index_tree": prior.assembly.tree, "requested_index_tree": requested.assembly.tree,
        "prior_inventory": prior_records, "requested_inventory": requested_records,
        "prior_inventory_sha256": hashlib.sha256(s._canonical_json(prior_records)).hexdigest(),
        "requested_inventory_sha256": hashlib.sha256(s._canonical_json(requested_records)).hexdigest(),
    })


def _registered(value, prior, requested):
    root = value["recovery"]
    files = {root + "/manifest.json": FileBytes(_manifest(value, prior, requested), 0o600)}
    directories = {root, root + "/prior", root + "/requested", root + "/copies", root + "/install"}
    for name, side in (("prior", prior), ("requested", requested)):
        prefix = root + "/" + name + "/"
        files.update({prefix + path: content for path, content in _files(_entries(side)).items()})
        directories.update(prefix + path for path in s._implied_directories(_entries(side)))
    return files, directories


def _copy_stage(value, path):
    """Copy staging is disjoint from arbitrary C/O file and directory names."""
    relative = path.removeprefix(value["recovery"] + "/")
    return value["recovery"] + "/copies/" + hashlib.sha256(relative.encode("utf-8")).hexdigest()


def _install_stage(value, path):
    return value["recovery"] + "/install/" + hashlib.sha256(path.encode("utf-8")).hexdigest()


def _install_stages(value, requested):
    entries = requested.candidate.entries if value["operation"] == "materialize" else requested.operator.files
    return {_install_stage(value, entry.path): _files((entry,))[entry.path] for entry in entries}


def _check_recovery(owned, value, prior, requested, *, complete):
    files, directories = _registered(value, prior, requested)
    stages = {_copy_stage(value, path): path for path in files}
    installs = _install_stages(value, requested)
    root = value["recovery"]
    seen = _paths(owned)
    for path, entry in seen.items():
        if path == root or path.startswith(root + "/"):
            if path == root + "/index":
                if entry.directory:
                    raise s.SessionError("temporary index has invalid type")
                if value["phase"] == "PREPARING":
                    raise s.SessionError("temporary index predates replacement authority")
                _validate_index(owned, path, prior, requested)
                continue
            if entry.directory:
                if path not in directories:
                    raise s.SessionError("unknown recovery directory")
            elif path in installs and value["phase"] == "REPLACING":
                expected = installs[path]
                partial = owned.read_file(path, max_bytes=len(expected.data))
                if partial.mode != expected.mode or not expected.data.startswith(partial.data):
                    raise s.SessionError("incomplete installation stage differs")
            elif path in stages and value["phase"] == "PREPARING":
                target = stages[path]
                if target in seen:
                    raise s.SessionError("preparation has both complete and incomplete copy")
                expected = files[target]
                partial = owned.read_file(path, max_bytes=len(expected.data))
                if partial.mode != expected.mode or not expected.data.startswith(partial.data):
                    raise s.SessionError("incomplete preparation stage differs")
            elif path not in files or owned.read_file(path, max_bytes=len(files[path].data)) != files[path]:
                raise s.SessionError("recovery inventory differs")
    if complete and (not set(files).issubset(seen) or not directories.issubset(seen)):
        raise s.SessionError("required replacement recovery material is missing")
    return files, directories


def _validate_paths(owned, value, prior, requested, *, requested_live=False):
    files, directories = _registered(value, prior, requested)
    stages = {_copy_stage(value, path) for path in files}
    files.update({INTENT: None, INTENT + ".new": None,
                  ".vise-host/session.json.new": None, value["recovery"] + "/index": None})
    if value["phase"] == "PREPARING":
        files.update({path: None for path in stages})
    if value["phase"] == "REPLACING":
        files[".vise-host/outcomes/" + value["q"] + ".new"] = None
        files.update({path: None for path in _install_stages(value, requested)})
    # A union is used only for path classification; exact live content is checked separately.
    live_prior = () if requested_live or value["phase"] == "CLEANUP" else _entries(prior)
    s._validate_observed_paths(owned, live_prior, _entries(requested),
                               registered_files=set(files), registered_directories=directories)


def _terminal(value, prior, requested):
    return {"operation": value["operation"], "phase": "complete", "q": value["q"],
            "prior": s._identity_dict(prior.assembly), "requested": s._identity_dict(requested.assembly),
            "outcome": "complete", "refusal": None}


def _durable_terminal(owned, value, prior, requested, *, require=False):
    terminal = _terminal(value, prior, requested)
    path = ".vise-host/outcomes/" + value["q"]
    encoded = s._canonical_json(terminal)
    if path in _paths(owned):
        if _read(owned, path) != encoded:
            raise s.SessionError("terminal outcome conflicts")
        if path + ".new" in _paths(owned):
            raise s.SessionError("terminal outcome has an unexpected duplicate stage")
        with owned._parent(path) as (parent, _):
            os.fsync(parent)
    elif require:
        raise s.SessionError("cleanup lacks a durable terminal outcome")
    else:
        _require_requested(owned, requested)
        _validate_paths(owned, value, prior, requested, requested_live=True)
        if any(path in _paths(owned) for path in _install_stages(value, requested)):
            raise s.SessionError("terminal publication has unfinished installation stages")
        _checkpoint("before-terminal-stage")
        _atomic(owned, path, encoded, None, allow_partial=True, checkpoint="terminal")
    return terminal


def append_terminal(owned, value, *, durable=True):
    """Append bytes, never replace/truncate chronology; repair only durable suffixes."""
    encoded = s._canonical_json(value)
    paths = _paths(owned)
    if durable and _read(owned, ".vise-host/outcomes/" + value["q"]) != encoded:
        raise s.SessionError("terminal chronology lacks matching durable evidence")
    if CHRONOLOGY not in paths:
        owned.write_new(CHRONOLOGY, encoded, mode=0o600)
        return
    data = _read(owned, CHRONOLOGY)
    last = data.rfind(b"\n") + 1
    complete, torn = data[:last], data[last:]
    s._validate_json_lines(complete)
    seen = [line for line in complete.splitlines(keepends=True)
            if s._decode_canonical_json(line, "operation")["q"] == value["q"]]
    if seen:
        if any(line != encoded for line in seen) or torn:
            raise s.SessionError("terminal operation id conflicts")
        return
    if torn and (not durable or not encoded.startswith(torn)):
        raise s.SessionError("torn chronology lacks matching durable terminal")
    suffix = encoded[len(torn):]
    with owned._parent(CHRONOLOGY) as (parent, name):
        before = owned._regular_at(parent, name)
        fd = os.open(name, os.O_WRONLY | os.O_APPEND | os.O_NOFOLLOW, dir_fd=parent)
        try:
            if owned._stamp(os.fstat(fd)) != owned._stamp(before):
                raise s.SessionError("chronology changed before append")
            while suffix:
                n = os.write(fd, suffix)
                if n <= 0:
                    raise s.SessionError("chronology append made no progress")
                suffix = suffix[n:]
            os.fsync(fd)
        finally:
            os.close(fd)
        os.fsync(parent)


def replace(session, owned, candidate, operator, operation):
    validate_candidate(candidate, operator)
    s._store_envelope(owned, candidate.identity, candidate.encoded)
    s._store_envelope(owned, operator.identity, operator.encoded)
    requested = dataclass_replace(session, candidate=candidate, operator=operator,
        assembly=s._compute_historical_identity(candidate, operator, session.bootstrap, session.identity))
    value = _intent(session, requested, uuid.uuid4().hex, operation)
    for path in (".vise-host/intents", ".vise-host/recovery", ".vise-host/outcomes"):
        owned.mkdirs(path)
    _checkpoint("before-ownership-intent")
    _atomic(owned, INTENT, s._canonical_json(value), None)
    _checkpoint("after-ownership-intent")
    _run(owned, value, session, requested)
    session.__dict__.update(requested.__dict__)


def recover(root, git, owned):
    paths = _paths(owned)
    if INTENT not in paths and INTENT + ".new" not in paths:
        return
    live = s._load_session(root, git, owned, pending=True)
    source = INTENT if INTENT in paths else INTENT + ".new"
    raw = _read(owned, source)
    value = s._decode_canonical_json(raw, "replacement intent")
    prior, requested = _validate_intent(live, owned, value)
    _git_preflight(owned, value, prior, requested)
    _validate_paths(owned, value, prior, requested)
    if source != INTENT:
        if value["phase"] != "PREPARING":
            raise s.SessionError("unpublished ownership intent has invalid phase")
        _require_prior(owned, value, prior, requested)
        _atomic(owned, INTENT, raw, None)
    elif INTENT + ".new" in paths:
        next_value = dict(value)
        next_value["phase"] = {"PREPARING": "REPLACING", "REPLACING": "CLEANUP"}.get(value["phase"])
        staged = _read(owned, INTENT + ".new")
        if next_value["phase"] is None or not s._canonical_json(next_value).startswith(staged):
            raise s.SessionError("staged intent transition differs")
    if ".vise-host/session.json.new" in paths:
        staged = _read(owned, ".vise-host/session.json.new")
        if not _state(requested).startswith(staged):
            raise s.SessionError("staged current pointer differs")
    _run(owned, value, prior, requested)


def _require_prior(owned, value, prior, requested):
    _git_preflight(owned, value, prior, requested)
    if _read(owned, ".vise-host/session.json") != _state(prior):
        raise s.SessionError("PREPARING current pointer changed")
    verify_active_assembly(prior.root, git=prior.git, identity=prior.identity, expected=prior.assembly)
    s._observe_entries(owned, _entries(prior))
    _validate_paths(owned, value, prior, requested)


def _transition(owned, value, phase):
    previous = s._canonical_json(value)
    value = dict(value, phase=phase)
    _atomic(owned, INTENT, s._canonical_json(value), previous)
    return value


def _run(owned, value, prior, requested):
    _git_preflight(owned, value, prior, requested)
    _validate_paths(owned, value, prior, requested)
    terminal = _terminal(value, prior, requested)
    # Refuse conflicting chronology before touching the live generation.
    history = _read(owned, CHRONOLOGY)
    last = history.rfind(b"\n") + 1
    s._validate_json_lines(history[:last])
    expected = s._canonical_json(terminal)
    for line in history[:last].splitlines(keepends=True):
        if s._decode_canonical_json(line, "operation")["q"] == value["q"] and line != expected:
            raise s.SessionError("operation chronology conflicts with intent")
    if history[last:]:
        _durable_terminal(owned, value, prior, requested, require=True)
        if not expected.startswith(history[last:]):
            raise s.SessionError("torn chronology conflicts with terminal")
    if value["phase"] == "PREPARING":
        _require_prior(owned, value, prior, requested)
        files, directories = _check_recovery(owned, value, prior, requested, complete=False)
        for directory in sorted(directories, key=lambda p: (p.count("/"), p)):
            if directory not in _paths(owned):
                _checkpoint("before-recovery-mkdir:" + directory.removeprefix(value["recovery"]))
                owned.mkdirs(directory)
                _checkpoint("recovery-mkdir:" + directory.removeprefix(value["recovery"]))
        manifest_path = value["recovery"] + "/manifest.json"
        prior_prefix = value["recovery"] + "/prior/"
        for path, content in files.items():
            if path == manifest_path:
                continue
            if path not in _paths(owned):
                _checkpoint("before-copy:" + path.removeprefix(value["recovery"] + "/"))
                if path.startswith(prior_prefix):
                    original = path[len(prior_prefix):]
                    if owned.read_file(original, max_bytes=len(content.data)) != content:
                        raise s.SessionError("prior changed during recovery copy")
                _prepare_file(owned, path, content, stage=_copy_stage(value, path))
                _checkpoint("copy:" + path.removeprefix(value["recovery"] + "/"))
        _checkpoint("before-manifest")
        if manifest_path not in _paths(owned):
            _prepare_file(owned, manifest_path, files[manifest_path], stage=_copy_stage(value, manifest_path))
        _checkpoint("after-manifest")
        _check_recovery(owned, value, prior, requested, complete=True)
        _require_prior(owned, value, prior, requested)
        _checkpoint("before-replacing")
        value = _transition(owned, value, "REPLACING")
        _checkpoint("after-replacing")
    if value["phase"] == "REPLACING":
        outcome = ".vise-host/outcomes/" + value["q"]
        if outcome in _paths(owned) or outcome + ".new" in _paths(owned):
            _require_requested(owned, requested)
            _validate_paths(owned, value, prior, requested, requested_live=True)
            if outcome not in _paths(owned):
                _check_recovery(owned, value, prior, requested, complete=True)
            terminal = _durable_terminal(owned, value, prior, requested)
        else:
            _check_recovery(owned, value, prior, requested, complete=True)
            _finish_replacement(owned, value, prior, requested)
            terminal = _durable_terminal(owned, value, prior, requested)
            _checkpoint("after-terminal-outcome")
        _checkpoint("before-terminal-append")
        append_terminal(owned, terminal)
        _checkpoint("after-terminal-append")
        _checkpoint("before-cleanup")
        value = _transition(owned, value, "CLEANUP")
        _checkpoint("after-cleanup")
    _cleanup(owned, value, prior, requested)


def _prepare_file(owned, path, content, *, stage, checkpoint=None):
    """Incomplete copies are disjoint registered stages; only complete files rename."""
    if stage in _paths(owned):
        partial = owned.read_file(stage, max_bytes=len(content.data))
        if partial.mode != content.mode or not content.data.startswith(partial.data):
            raise s.SessionError("incomplete preparation copy differs")
        owned.remove_expected(stage, partial)
    if checkpoint:
        _checkpoint("before-install-stage:" + checkpoint)
    owned.write_new(stage, content.data, mode=content.mode)
    if checkpoint:
        _checkpoint("after-install-stage:" + checkpoint)
        _checkpoint("before-install-publication:" + checkpoint)
    owned.move_expected(stage, path, expected_source=content, expected_destination=None)
    if checkpoint:
        _checkpoint("after-install-publication:" + checkpoint)


def _git_preflight(owned, value, prior, requested):
    """Authenticate the closed metadata surface before the first Git read."""
    registered_lock = ".git/index.lock"
    paths = _paths(owned)
    if registered_lock in paths:
        if value["phase"] != "REPLACING":
            raise s.SessionError("Git publication lock is outside replacement")
        stage = value["recovery"] + "/index"
        if stage not in paths:
            raise s.SessionError("Git publication lock lacks registered index stage")
        content = owned.read_file(stage, max_bytes=s._ENVELOPE_LIMIT)
        partial = owned.read_file(registered_lock, max_bytes=len(content.data))
        if partial.mode != content.mode or not content.data.startswith(partial.data):
            raise s.SessionError("Git publication lock differs from registered index")

    class RegisteredLayout:
        def walk(self, **kwargs):
            return tuple(item for item in owned.walk(**kwargs) if item.path != registered_lock)

        def read_file(self, *args, **kwargs):
            return owned.read_file(*args, **kwargs)

    verify_git_layout(RegisteredLayout())
    verify_repository_policy(prior.root)


def _require_requested(owned, requested):
    if _read(owned, ".vise-host/session.json") != _state(requested):
        raise s.SessionError("terminal current pointer differs")
    verify_active_assembly(requested.root, git=requested.git, identity=requested.identity,
                           expected=requested.assembly)
    s._observe_entries(owned, _entries(requested))


def _git_pair(prior, requested):
    _validate_commit(_checked_object(prior.root, prior.git, prior.assembly.commit, "commit"),
                     prior.assembly, prior.identity)
    _validate_bootstrap(_checked_object(prior.root, prior.git, prior.bootstrap, "commit"), prior.identity)
    _verify_tree_objects(prior.root, prior.git, prior.assembly.tree)
    observed = inspect_repository(prior.root, git=prior.git)
    pairs = {(prior.assembly.commit, prior.assembly.tree),
             (prior.assembly.commit, requested.assembly.tree),
             (requested.assembly.commit, requested.assembly.tree)}
    if (observed.head, observed.index_tree) not in pairs:
        raise s.SessionError("Git state is outside registered publication sequence")
    if observed.head == requested.assembly.commit:
        # When index/HEAD are requested, verify actual objects and fixed provenance.
        verify_active_assembly(prior.root, git=prior.git, identity=prior.identity, expected=requested.assembly)
    return observed


def _finish_replacement(owned, value, prior, requested):
    _validate_paths(owned, value, prior, requested)
    actual = _git_pair(prior, requested)
    current = _read(owned, ".vise-host/session.json")
    if current not in (_state(prior), _state(requested)):
        raise s.SessionError("unregistered current pointer")
    if current == _state(requested) and (actual.head, actual.index_tree) != (
            requested.assembly.commit, requested.assembly.tree):
        raise s.SessionError("current pointer precedes Git publication")
    old_files, next_files = _files(_entries(prior)), _files(_entries(requested))
    replace_old = _files(prior.candidate.entries if value["operation"] == "materialize" else prior.operator.files)
    replace_next = _files(requested.candidate.entries if value["operation"] == "materialize" else requested.operator.files)
    retained = prior.operator.files if value["operation"] == "materialize" else prior.candidate.entries
    s._observe_entries(owned, retained)
    paths = _paths(owned)
    observed_live = {}
    for path in set(old_files) | set(next_files):
        if path in paths and not paths[path].directory:
            content = owned.read_file(path, max_bytes=max(len(v.data) for v in
                        (old_files.get(path), next_files.get(path)) if v is not None))
            if content not in (old_files.get(path), next_files.get(path)):
                raise s.SessionError("live replacement file is outside registered generations")
            observed_live[path] = content
    if actual.head == requested.assembly.commit and prior.assembly != requested.assembly:
        s._observe_entries(owned, _entries(requested))
    # Validate all recovery material and all live paths before the first deletion.
    constructed = construct_assembly(prior.root, git=prior.git, identity=prior.identity,
        bootstrap=prior.bootstrap, candidate=requested.candidate, operator=requested.operator)
    if constructed != requested.assembly:
        raise s.SessionError("constructed objects differ from manifest")
    _checkpoint("after-object-write")
    for path in sorted(replace_old, reverse=True):
        if path in observed_live and observed_live[path] != replace_next.get(path):
            owned.remove_expected(path, observed_live.pop(path))
            _checkpoint("remove:" + path)
    old_dirs, next_dirs = s._implied_directories(_entries(prior)), s._implied_directories(_entries(requested))
    for path in sorted(old_dirs - next_dirs - {".vise"}, key=lambda p: (p.count("/"), p), reverse=True):
        if path in _paths(owned):
            owned.remove_empty_directory(path)
            _checkpoint("remove-directory:" + path)
    for path in sorted(next_dirs, key=lambda p: (p.count("/"), p)):
        owned.mkdirs(path)
    for path, content in replace_next.items():
        if path not in observed_live:
            staged = value["recovery"] + "/requested/" + path
            if owned.read_file(staged, max_bytes=len(content.data)) != content:
                raise s.SessionError("requested stage changed before installation")
            _prepare_file(owned, path, content, stage=_install_stage(value, path), checkpoint=path)
            _checkpoint("install:" + path)
        elif _install_stage(value, path) in _paths(owned):
            staged = owned.read_file(_install_stage(value, path), max_bytes=len(content.data))
            if staged.mode != content.mode or not content.data.startswith(staged.data):
                raise s.SessionError("redundant installation stage differs")
            owned.remove_expected(_install_stage(value, path), staged)
    s._observe_entries(owned, _entries(requested))
    _publish_git(owned, value, prior, requested)
    if current != _state(requested):
        _atomic(owned, ".vise-host/session.json", _state(requested), current)
    elif ".vise-host/session.json.new" in _paths(owned):
        if _read(owned, ".vise-host/session.json.new") != _state(requested):
            raise s.SessionError("staged current pointer differs")
        owned.remove_expected(".vise-host/session.json.new", FileBytes(_state(requested), 0o600))
    _checkpoint("after-current-publication")
    verify_active_assembly(prior.root, git=prior.git, identity=prior.identity, expected=requested.assembly)


def _publish_git(owned, value, prior, requested):
    actual = _git_pair(prior, requested)
    stage = value["recovery"] + "/index"
    index_path = prior.root / stage
    if stage not in _paths(owned):
        prior.git.run(("read-tree", requested.assembly.tree), root=prior.root, index_file=index_path, write=True)
    _validate_index(owned, stage, prior, requested)
    with owned._parent(stage) as (parent, name):
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=parent)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
        os.fsync(parent)
    _checkpoint("after-temporary-index-sync")
    staged = owned.read_file(stage, max_bytes=s._ENVELOPE_LIMIT)
    lock = ".git/index.lock"
    if actual.index_tree != requested.assembly.tree:
        original = owned.read_file(".git/index", max_bytes=s._ENVELOPE_LIMIT)
        if lock in _paths(owned):
            partial = owned.read_file(lock, max_bytes=len(staged.data))
            if partial.mode != staged.mode or not staged.data.startswith(partial.data):
                raise s.SessionError("index publication lock differs")
            if partial != staged:
                owned.remove_expected(lock, partial)
                owned.write_new(lock, staged.data, mode=staged.mode)
        else:
            owned.write_new(lock, staged.data, mode=staged.mode)
        _checkpoint("after-index-lock-sync")
        owned.move_expected(lock, ".git/index", expected_source=staged, expected_destination=original)
    elif lock in _paths(owned):
        partial = owned.read_file(lock, max_bytes=len(staged.data))
        if partial.mode != staged.mode or not staged.data.startswith(partial.data):
            raise s.SessionError("redundant index publication lock differs")
        owned.remove_expected(lock, partial)
    _checkpoint("after-index-publication")
    if actual.head != requested.assembly.commit:
        prior.git.run(("update-ref", "HEAD", requested.assembly.commit, prior.assembly.commit),
                       root=prior.root, write=True)
    _checkpoint("after-head-cas")


def _validate_index(owned, stage, prior, requested):
    staged = owned.read_file(stage, max_bytes=s._ENVELOPE_LIMIT)
    if staged.mode not in (0o600, 0o644):
        raise s.SessionError("temporary index has unexpected mode")
    expected_records = b"".join(
        (f"{'100755' if entry.executable else '100644'} {s._git_digest(b'blob', entry.data)} 0\t".encode()
         + entry.path.encode() + b"\0") for entry in _entries(requested))
    if prior.git.run(("ls-files", "--stage", "--full-name", "-z"), root=prior.root,
                     index_file=prior.root / stage) != expected_records:
        raise s.SessionError("temporary index differs from requested inventory")


def _cleanup(owned, value, prior, requested):
    if value["phase"] != "CLEANUP":
        raise s.SessionError("cleanup requires a durable phase transition")
    terminal = _durable_terminal(owned, value, prior, requested, require=True)
    if _read(owned, ".vise-host/session.json") != _state(requested):
        raise s.SessionError("cleanup current pointer differs from terminal")
    verify_active_assembly(prior.root, git=prior.git, identity=prior.identity, expected=requested.assembly)
    s._observe_entries(owned, _entries(requested))
    _validate_paths(owned, value, prior, requested, requested_live=True)
    files, directories = _check_recovery(owned, value, prior, requested, complete=False)
    append_terminal(owned, terminal)
    index = value["recovery"] + "/index"
    if index in _paths(owned):
        # Revalidate the retained temporary index before deleting it.
        expected = prior.git.run(("ls-files", "--stage", "--full-name", "-z"), root=prior.root)
        actual = prior.git.run(("ls-files", "--stage", "--full-name", "-z"), root=prior.root,
                                index_file=prior.root / index)
        if actual != expected:
            raise s.SessionError("cleanup index differs")
        files[index] = owned.read_file(index, max_bytes=s._ENVELOPE_LIMIT)
    for path, content in sorted(files.items(), reverse=True):
        if path in _paths(owned):
            _checkpoint("before-cleanup-delete:" + path.removeprefix(value["recovery"] + "/"))
            owned.remove_expected(path, content)
            _checkpoint("after-cleanup-delete:" + path.removeprefix(value["recovery"] + "/"))
    for directory in sorted(directories, key=lambda p: (p.count("/"), p), reverse=True):
        if directory in _paths(owned):
            owned.remove_empty_directory(directory)
            _checkpoint("cleanup-directory:" + directory.removeprefix(value["recovery"]))
    _checkpoint("before-intent-removal")
    owned.remove_expected(INTENT, FileBytes(s._canonical_json(value), 0o600))
    _checkpoint("after-intent-removal-parent-sync")
