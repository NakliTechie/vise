"""Canonical operator generations and candidate/operator authority checks."""

from __future__ import annotations

import base64
import binascii
import hashlib
import json
from dataclasses import dataclass
from itertools import islice
from typing import Iterable

from host.bundle import (
    BundleError,
    BundleLimits,
    OperatorInventory,
    SourceBundle,
    SourceEntry,
    _validated_paths,
    build_bundle,
    decode_bundle,
)


@dataclass(frozen=True)
class OperatorGeneration:
    generation: int
    files: tuple[SourceEntry, ...]
    encoded: bytes
    identity: str


def build_operator(
    entries: Iterable[SourceEntry],
    *,
    generation: int,
    limits: BundleLimits = BundleLimits(),
) -> OperatorGeneration:
    """Build one canonical, filesystem-independent operator generation."""
    if type(generation) is not int or generation < 0:
        raise BundleError("operator generation must be a non-negative integer")
    items = list(islice(entries, limits.max_files + 1))
    if len(items) > limits.max_files:
        raise BundleError("operator generation exceeds file-count limit")
    for item in items:
        if type(item) is not SourceEntry:
            raise BundleError("operator entries must be SourceEntry values")
        if type(item.kind) is not str or item.kind != "file":
            raise BundleError("operator entries must declare regular files")
        if type(item.data) is not bytes or type(item.executable) is not bool:
            raise BundleError("operator entry bytes and executable bit have invalid types")
        if len(item.data) > limits.max_file_bytes:
            raise BundleError(f"operator file {item.path!r} exceeds per-file byte limit")
    if sum(len(item.data) for item in items) > limits.max_total_bytes:
        raise BundleError("operator generation exceeds total-byte limit")

    paths = _validated_paths((item.path for item in items), limits=limits, label="operator")
    for path in paths:
        _validate_operator_path(path)
    ordered = tuple(sorted(items, key=lambda item: item.path))
    payload = {
        "files": [
            {
                "data": base64.b64encode(item.data).decode("ascii"),
                "executable": item.executable,
                "path": item.path,
            }
            for item in ordered
        ],
        "generation": generation,
        "kind": "operator",
        "version": 1,
    }
    encoded = (json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n").encode("ascii")
    if len(encoded) > limits.max_encoded_bytes:
        raise BundleError("encoded operator generation exceeds byte limit")
    identity = "sha256:" + hashlib.sha256(encoded).hexdigest()
    return OperatorGeneration(generation, ordered, encoded, identity)


def decode_operator(
    encoded: bytes,
    *,
    limits: BundleLimits = BundleLimits(),
) -> OperatorGeneration:
    """Strictly decode and re-canonicalize an operator generation."""
    if type(encoded) is not bytes:
        raise BundleError("encoded operator generation must be bytes")
    if len(encoded) > limits.max_encoded_bytes:
        raise BundleError("encoded operator generation exceeds byte limit")

    def no_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
        value: dict[str, object] = {}
        for key, item in pairs:
            if key in value:
                raise BundleError(f"duplicate JSON key {key!r}")
            value[key] = item
        return value

    try:
        value = json.loads(encoded, object_pairs_hook=no_duplicate_keys)
    except BundleError:
        raise
    except (UnicodeDecodeError, json.JSONDecodeError, RecursionError, ValueError) as error:
        raise BundleError("operator generation is not valid JSON") from error
    required = {"files", "generation", "kind", "version"}
    if type(value) is not dict or set(value) != required:
        raise BundleError("operator generation envelope is invalid")
    if value["kind"] != "operator" or value["version"] != 1:
        raise BundleError("operator generation domain or version is invalid")
    if type(value["generation"]) is not int or value["generation"] < 0:
        raise BundleError("operator generation must be a non-negative integer")
    raw_files = value["files"]
    if type(raw_files) is not list:
        raise BundleError("operator files must be a list")
    entries: list[SourceEntry] = []
    for raw in raw_files:
        if type(raw) is not dict or set(raw) != {"data", "executable", "path"}:
            raise BundleError("operator file entry is invalid")
        if type(raw["data"]) is not str or type(raw["executable"]) is not bool or type(raw["path"]) is not str:
            raise BundleError("operator file entry types are invalid")
        try:
            data = base64.b64decode(raw["data"], validate=True)
        except (binascii.Error, ValueError) as error:
            raise BundleError("operator file data is not canonical base64") from error
        if base64.b64encode(data).decode("ascii") != raw["data"]:
            raise BundleError("operator file data is not canonical base64")
        entries.append(SourceEntry(raw["path"], data, raw["executable"]))
    result = build_operator(entries, generation=value["generation"], limits=limits)
    if result.encoded != encoded:
        raise BundleError("operator generation encoding is not canonical")
    return result


def validate_candidate(
    candidate: SourceBundle,
    operator: OperatorGeneration,
    *,
    limits: BundleLimits = BundleLimits(),
) -> SourceBundle:
    """Revalidate canonical values and enforce the host C/O boundary."""
    if type(operator) is not OperatorGeneration:
        raise BundleError("operator generation has invalid type")
    if type(operator.files) is not tuple or type(operator.identity) is not str:
        raise BundleError("operator generation fields have invalid types")
    rebuilt_operator = build_operator(operator.files, generation=operator.generation, limits=limits)
    canonical_operator = decode_operator(operator.encoded, limits=limits)
    if canonical_operator != rebuilt_operator or canonical_operator != operator:
        raise BundleError("operator generation fields do not match its canonical encoding")
    if type(candidate) is not SourceBundle:
        raise BundleError("candidate bundle has invalid type")
    if type(candidate.entries) is not tuple or type(candidate.identity) is not str:
        raise BundleError("candidate bundle fields have invalid types")
    inventory = OperatorInventory.build((entry.path for entry in operator.files), limits=limits)
    rebuilt_candidate = build_bundle(candidate.entries, operator=inventory, limits=limits)
    if any(type(entry.kind) is not str for entry in candidate.entries):
        raise BundleError("candidate entry kind has invalid type")
    canonical_candidate = decode_bundle(candidate.encoded, operator=inventory, limits=limits)
    if canonical_candidate != rebuilt_candidate or canonical_candidate != candidate:
        raise BundleError("candidate bundle fields do not match its canonical encoding")
    for entry in candidate.entries:
        if any(part.casefold() in {".gitignore", ".gitattributes"} for part in entry.path.split("/")):
            raise BundleError(f"candidate path {entry.path!r} is operator-owned Git policy")
    return canonical_candidate


def _validate_operator_path(path: str) -> None:
    folded_parts = path.casefold().split("/")
    if ".git" in folded_parts or ".vise-host" in folded_parts:
        raise BundleError(f"operator path {path!r} contains controller metadata")
    folded = path.casefold()
    mutable = (".vise/journal.jsonl", ".vise/run.lock", ".vise/tmp")
    if any(folded == item or folded.startswith(item + "/") or item.startswith(folded + "/") for item in mutable):
        raise BundleError(f"operator path {path!r} names mutable evaluator state")
