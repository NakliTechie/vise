"""Pure generation classification, not execution or acceptance authority.

Inputs are independently observed regular-file snapshots from the trusted
controller. This module cannot establish filesystem type/link safety, unchanged
C, reviewed scope, whole-lock validity, invocation identity or receipt outcome.
Those remain required at the persistent acceptance boundary. No record command,
filesystem write, O advance or receipt fabrication occurs here.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from typing import Literal

from host.bundle import BundleError, BundleLimits, build_bundle
from host.operator import OperatorGeneration, validate_candidate
from host.pin_provenance import PinProvenance, PinProvenanceError, validate_pin_provenance


class AcceptanceGenerationError(ValueError):
    """The snapshot is inconsistent with the registered acceptance projection."""


@dataclass(frozen=True)
class GenerationObservation:
    generation: Literal["prior", "reviewed-next"]
    lock_sha256: str
    pins: PinProvenance


_HASH = re.compile(r"sha256:[0-9a-f]{64}\Z")
_BLOBS = ".vise/blobs/"


def _digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def _hash(value: object) -> bool:
    return type(value) is str and _HASH.fullmatch(value) is not None


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise AcceptanceGenerationError("duplicate lock JSON key")
        result[key] = value
    return result


def _constant(value):
    raise AcceptanceGenerationError("non-finite lock JSON value")


def _blob_references(raw: bytes, *, limits: BundleLimits) -> frozenset[str]:
    """Project Vise referencedHashes, including its explicit hash-only flags.

    This is deliberately not a whole lock schema/manifest validator. Values of
    unrelated lock fields are not interpreted here. Exact reviewed-byte binding
    and the separate native lock validator must precede authority to adopt O.
    """
    if len(raw) > limits.max_file_bytes:
        raise AcceptanceGenerationError("lock exceeds byte bound")
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_object, parse_constant=_constant)
    except (ValueError, UnicodeError, RecursionError) as error:
        raise AcceptanceGenerationError("lock JSON is invalid") from error
    if (type(value) is not dict or type(value.get("v")) is not int or value["v"] != 1
            or type(value.get("probes")) is not dict or len(value["probes"]) > limits.max_files):
        raise AcceptanceGenerationError("unsupported lock reference projection")
    refs = set()
    for probe in value["probes"].values():
        if type(probe) is not dict:
            raise AcceptanceGenerationError("probe reference projection is invalid")
        for stream in ("stdout", "stderr"):
            large = probe.get(stream + "_large", False)
            if not _hash(probe.get(stream)) or type(large) is not bool:
                raise AcceptanceGenerationError("stream hash or hash-only flag is invalid")
            if not large:
                refs.add(probe[stream])
        files, large = probe.get("files"), probe.get("files_large")
        files = {} if files is None else files
        large = {} if large is None else large
        if (type(files) is not dict or type(large) is not dict
                or len(files) > limits.max_files or not set(large).issubset(files)
                or any(type(flag) is not bool for flag in large.values())):
            raise AcceptanceGenerationError("artifact reference projection is invalid")
        for path, digest in files.items():
            if type(path) is not str or not path or not _hash(digest):
                raise AcceptanceGenerationError("artifact reference is invalid")
            if not large.get(path, False):
                refs.add(digest)
    return frozenset(refs)


def _inventory(value: OperatorGeneration, limits: BundleLimits):
    try:
        validate_candidate(build_bundle((), limits=limits), value, limits=limits)
    except (BundleError, TypeError, AttributeError) as error:
        raise AcceptanceGenerationError("operator snapshot is not canonical") from error
    files = {entry.path: entry for entry in value.files}
    lock = files.get("vise.lock")
    if lock is None or lock.executable:
        raise AcceptanceGenerationError("snapshot requires a non-executable vise.lock")
    refs = _blob_references(lock.data, limits=limits)
    blobs = {}
    for path, entry in files.items():
        if path == ".vise/blobs" or path.startswith(_BLOBS):
            digest = "sha256:" + path.removeprefix(_BLOBS)
            if not _hash(digest) or entry.executable or _digest(entry.data) != digest:
                raise AcceptanceGenerationError("blob path, mode or content hash is invalid")
            blobs[digest] = entry
    if not refs.issubset(blobs):
        raise AcceptanceGenerationError("snapshot is missing a referenced regular blob")
    return files, lock.data, refs, blobs


def classify_generation(
    prior: OperatorGeneration,
    observed: OperatorGeneration,
    *,
    reviewed_lock_sha256: str,
    evaluated_commit: str,
    limits: BundleLimits = BundleLimits(),
) -> GenerationObservation:
    """Classify exact snapshots without inferring whether record ran.

    Both snapshots use the *prior* O counter. Only the later durable adoption
    layer may allocate current+1. Exact prior content wins before comparison to
    the reviewed digest, even when that preview proposed something different.
    For non-prior content, unrelated O must be unchanged, the raw observed lock
    digest must match the review, referenced blobs must exist and historical pin
    provenance must hold. Inconsistency raises and grants no adoption authority.

    Orphan policy: existing valid content-addressed orphans may remain or be
    pruned; no novel unreferenced blob is admitted. Hash-only large observations
    do not require blob files, matching Vise's referencedHashes function.
    """
    if type(limits) is not BundleLimits or not _hash(reviewed_lock_sha256):
        raise AcceptanceGenerationError("invalid limits or reviewed lock digest")
    old_files, old_lock, _, old_blobs = _inventory(prior, limits)
    new_files, new_lock, new_refs, new_blobs = _inventory(observed, limits)
    if observed.generation != prior.generation:
        raise AcceptanceGenerationError("observation must retain the prior O counter")
    try:
        pins = validate_pin_provenance(
            old_lock, new_lock, evaluated_commit=evaluated_commit,
            max_lock_bytes=limits.max_file_bytes, max_probes=limits.max_files,
        )
    except PinProvenanceError as error:
        raise AcceptanceGenerationError("pin provenance is inconsistent") from error
    digest = _digest(new_lock)
    if observed == prior:
        return GenerationObservation("prior", digest, pins)
    if digest != reviewed_lock_sha256:
        raise AcceptanceGenerationError("observed lock bytes differ from reviewed digest")
    immutable_old = {p: e for p, e in old_files.items() if p != "vise.lock" and not p.startswith(_BLOBS)}
    immutable_new = {p: e for p, e in new_files.items() if p != "vise.lock" and not p.startswith(_BLOBS)}
    if immutable_new != immutable_old:
        raise AcceptanceGenerationError("unrelated operator content changed")
    if (new_blobs.keys() - old_blobs.keys()) - new_refs:
        raise AcceptanceGenerationError("new unreferenced blob is unexplained")
    return GenerationObservation("reviewed-next", digest, pins)
