"""Read-only pin-provenance projection for a separately authorized acceptance.

This is not a lockfile validator or acceptance authority. The caller must bind
the exact reviewed lock-byte digest, validate the complete lock/blob generation
and all unrelated C/O paths, and independently validate any execution receipt.
These checks neither invoke record nor establish that record ran successfully.
Only the fields used by Vise's pinIdentityEqual and acceptance are interpreted.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass


class PinProvenanceError(ValueError):
    """A lock projection or its historical pin transition is invalid."""


@dataclass(frozen=True)
class PinProvenance:
    retained: tuple[str, ...]
    newly_accepted: tuple[str, ...]
    unaccepted: tuple[str, ...]


@dataclass(frozen=True)
class _Pin:
    run_hash: str
    dependencies: tuple[tuple[str, str], ...]
    specs: tuple[tuple[str, str], ...]
    accepted_commit: str | None

    def same_identity(self, other: _Pin) -> bool:
        return (self.run_hash, self.dependencies, self.specs) == (
            other.run_hash, other.dependencies, other.specs,
        )


_HASH = re.compile(r"sha256:[0-9a-f]{64}\Z")
_COMMIT = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")


def _hash(value: object) -> bool:
    return type(value) is str and _HASH.fullmatch(value) is not None


def _commit(value: object) -> bool:
    return type(value) is str and _COMMIT.fullmatch(value) is not None


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise PinProvenanceError("lock JSON has a duplicate key")
        result[key] = value
    return result


def _reject_constant(value):
    raise PinProvenanceError("lock JSON contains a non-finite constant")


def _hash_map(value: object, label: str) -> tuple[tuple[str, str], ...]:
    # Go stringMapEqual treats omitted, null and empty maps as equivalent.
    if value is None:
        return ()
    if type(value) is not dict:
        raise PinProvenanceError(f"{label} is not a hash map")
    for path, digest in value.items():
        if type(path) is not str or not path or not _hash(digest):
            raise PinProvenanceError(f"{label} has an invalid path or digest")
        try:
            path.encode("utf-8")
        except UnicodeError as error:
            raise PinProvenanceError(f"{label} path is not valid Unicode") from error
    return tuple(sorted(value.items()))


def _project(raw: bytes, *, max_lock_bytes: int, max_probes: int) -> dict[str, _Pin]:
    if type(raw) is not bytes or len(raw) > max_lock_bytes:
        raise PinProvenanceError("lock bytes have an invalid type or exceed the limit")
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object,
                           parse_constant=_reject_constant)
    except (UnicodeError, ValueError, RecursionError) as error:
        raise PinProvenanceError("lock JSON cannot be projected") from error
    if (type(value) is not dict or type(value.get("v")) is not int or value["v"] != 1
            or type(value.get("probes")) is not dict or len(value["probes"]) > max_probes):
        raise PinProvenanceError("lock version or probe collection is unsupported")
    result = {}
    for probe_id, entry in value["probes"].items():
        if (type(probe_id) is not str or not probe_id or type(entry) is not dict
                or not _hash(entry.get("run_hash"))):
            raise PinProvenanceError("probe identity projection is invalid")
        try:
            probe_id.encode("utf-8")
        except UnicodeError as error:
            raise PinProvenanceError("probe id is not valid Unicode") from error
        deps = _hash_map(entry.get("deps"), "dependencies")
        pin = entry.get("pin")
        if pin is None:
            continue
        if type(pin) is not dict or set(pin) != {"spec", "accepted_commit"}:
            raise PinProvenanceError("pin provenance fields are unsupported")
        accepted = pin["accepted_commit"]
        if accepted is not None and not _commit(accepted):
            raise PinProvenanceError("pin accepted_commit is not a full Git object name")
        result[probe_id] = _Pin(entry["run_hash"], deps, _hash_map(pin["spec"], "specs"), accepted)
    return result


def validate_pin_provenance(
    prior_lock: bytes,
    observed_lock: bytes,
    *,
    evaluated_commit: str,
    max_lock_bytes: int = 16 * 1024 * 1024,
    max_probes: int = 10_000,
) -> PinProvenance:
    """Check retained and newly accepted pins without rewriting lock bytes.

    Identity is exactly (probe id, run_hash, deps, pin.spec), not observed output
    or recorded_commit. A still-present unchanged accepted pin cannot lose or
    alter its old acceptance. New/changed identities may remain unaccepted; any
    acceptance on them must name evaluated_commit, never the next assembly.
    Removed probes or conversion to non-pin are not retained identities; their
    authorization is the caller's manifest/preview policy, not this projection.
    The returned names describe observed state, not operation outcome.
    """
    if (type(max_lock_bytes) is not int or max_lock_bytes <= 0
            or type(max_probes) is not int or max_probes <= 0):
        raise PinProvenanceError("projection limits must be positive integers")
    if (type(evaluated_commit) is not str or len(evaluated_commit) != 40
            or not _commit(evaluated_commit)):
        raise PinProvenanceError("evaluated commit must name this host's SHA-1 assembly")
    prior = _project(prior_lock, max_lock_bytes=max_lock_bytes, max_probes=max_probes)
    observed = _project(observed_lock, max_lock_bytes=max_lock_bytes, max_probes=max_probes)
    retained, newly_accepted, unaccepted = [], [], []
    for probe_id, fresh in sorted(observed.items()):
        old = prior.get(probe_id)
        if old is not None and old.accepted_commit is not None and old.same_identity(fresh):
            if fresh.accepted_commit != old.accepted_commit:
                raise PinProvenanceError(f"unchanged accepted pin lost historical provenance: {probe_id}")
            retained.append(probe_id)
        elif fresh.accepted_commit is None:
            unaccepted.append(probe_id)
        elif fresh.accepted_commit != evaluated_commit:
            raise PinProvenanceError(f"newly accepted pin does not name the evaluated commit: {probe_id}")
        else:
            newly_accepted.append(probe_id)
    return PinProvenance(tuple(retained), tuple(newly_accepted), tuple(unaccepted))
