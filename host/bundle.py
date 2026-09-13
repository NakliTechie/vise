"""Canonical, filesystem-independent source bundle values."""

from __future__ import annotations

import base64
import binascii
import hashlib
import json
import unicodedata
from dataclasses import dataclass
from itertools import islice
from typing import Iterable, Literal


class BundleError(ValueError):
    """The supplied inventory or encoded bundle violates the bundle contract."""


@dataclass(frozen=True)
class BundleLimits:
    max_files: int = 10_000
    max_file_bytes: int = 16 * 1024 * 1024
    max_total_bytes: int = 256 * 1024 * 1024
    max_path_bytes: int = 4_096
    max_encoded_bytes: int = 384 * 1024 * 1024

    def __post_init__(self) -> None:
        values = (
            self.max_files,
            self.max_file_bytes,
            self.max_total_bytes,
            self.max_path_bytes,
            self.max_encoded_bytes,
        )
        if any(type(value) is not int or value <= 0 for value in values):
            raise BundleError("bundle limits must be positive integers")


@dataclass(frozen=True)
class SourceEntry:
    path: str
    data: bytes
    executable: bool = False
    kind: Literal["file", "symlink", "special"] = "file"


@dataclass(frozen=True)
class OperatorInventory:
    paths: tuple[str, ...]

    @classmethod
    def build(cls, paths: Iterable[str], *, limits: BundleLimits = BundleLimits()) -> "OperatorInventory":
        supplied = list(islice(paths, limits.max_files + 1))
        if len(supplied) > limits.max_files:
            raise BundleError("operator inventory exceeds path-count limit")
        normalized = _validated_paths(supplied, limits=limits, label="operator")
        return cls(tuple(normalized))


@dataclass(frozen=True)
class SourceBundle:
    entries: tuple[SourceEntry, ...]
    encoded: bytes
    identity: str


CONTROLLER_NAMESPACES = (".git", ".vise", ".vise-host")


def build_bundle(
    entries: Iterable[SourceEntry],
    *,
    operator: OperatorInventory = OperatorInventory(()),
    limits: BundleLimits = BundleLimits(),
) -> SourceBundle:
    items = list(islice(entries, limits.max_files + 1))
    if len(items) > limits.max_files:
        raise BundleError("source bundle exceeds file-count limit")
    for item in items:
        if type(item) is not SourceEntry:
            raise BundleError("source entries must be SourceEntry values")
        if item.kind != "file":
            raise BundleError("source entries must declare regular files")
        if type(item.data) is not bytes or type(item.executable) is not bool:
            raise BundleError("source entry bytes and executable bit have invalid types")
        if len(item.data) > limits.max_file_bytes:
            raise BundleError(f"source file {item.path!r} exceeds per-file byte limit")
    if sum(len(item.data) for item in items) > limits.max_total_bytes:
        raise BundleError("source bundle exceeds total-byte limit")

    paths = _validated_paths((item.path for item in items), limits=limits, label="source")
    if type(operator) is not OperatorInventory or type(operator.paths) is not tuple:
        raise BundleError("operator inventory has invalid type")
    if len(operator.paths) > limits.max_files:
        raise BundleError("operator inventory exceeds path-count limit")
    operator_paths = _validated_paths(operator.paths, limits=limits, label="operator")
    _reject_cross_inventory_collisions(paths, operator_paths)
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
        "version": 1,
    }
    encoded = (json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n").encode("ascii")
    if len(encoded) > limits.max_encoded_bytes:
        raise BundleError("encoded source bundle exceeds byte limit")
    return SourceBundle(ordered, encoded, "sha256:" + hashlib.sha256(encoded).hexdigest())


def decode_bundle(
    encoded: bytes,
    *,
    operator: OperatorInventory = OperatorInventory(()),
    limits: BundleLimits = BundleLimits(),
) -> SourceBundle:
    if type(encoded) is not bytes:
        raise BundleError("encoded bundle must be bytes")
    if len(encoded) > limits.max_encoded_bytes:
        raise BundleError("encoded source bundle exceeds byte limit")

    def no_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
        result: dict[str, object] = {}
        for key, value in pairs:
            if key in result:
                raise BundleError(f"duplicate JSON key {key!r}")
            result[key] = value
        return result

    try:
        value = json.loads(encoded, object_pairs_hook=no_duplicate_keys)
    except BundleError:
        raise
    except (UnicodeDecodeError, json.JSONDecodeError, RecursionError, ValueError) as error:
        raise BundleError("bundle is not valid JSON") from error
    if type(value) is not dict or set(value) != {"version", "files"} or value["version"] != 1:
        raise BundleError("bundle envelope is invalid")
    raw_files = value["files"]
    if type(raw_files) is not list:
        raise BundleError("bundle files must be a list")
    entries: list[SourceEntry] = []
    for raw in raw_files:
        if type(raw) is not dict or set(raw) != {"path", "data", "executable"}:
            raise BundleError("bundle file entry is invalid")
        if type(raw["path"]) is not str or type(raw["data"]) is not str or type(raw["executable"]) is not bool:
            raise BundleError("bundle file entry types are invalid")
        try:
            data = base64.b64decode(raw["data"], validate=True)
        except (binascii.Error, ValueError) as error:
            raise BundleError("bundle file data is not canonical base64") from error
        if base64.b64encode(data).decode("ascii") != raw["data"]:
            raise BundleError("bundle file data is not canonical base64")
        entries.append(SourceEntry(raw["path"], data, raw["executable"]))
    bundle = build_bundle(entries, operator=operator, limits=limits)
    if bundle.encoded != encoded:
        raise BundleError("bundle encoding is not canonical")
    return bundle


def _validated_paths(paths: Iterable[str], *, limits: BundleLimits, label: str) -> list[str]:
    result: list[str] = []
    folded: dict[str, str] = {}
    for path in paths:
        if type(path) is not str:
            raise BundleError(f"{label} path must be text")
        if not path or path == "." or path.startswith("/") or "\\" in path:
            raise BundleError(f"invalid {label} path {path!r}")
        try:
            encoded_path = path.encode("utf-8")
        except UnicodeEncodeError as error:
            raise BundleError(f"invalid {label} path {path!r}") from error
        if len(encoded_path) > limits.max_path_bytes:
            raise BundleError(f"{label} path exceeds byte limit")
        if unicodedata.normalize("NFC", path) != path:
            raise BundleError(f"non-canonical Unicode {label} path {path!r}")
        parts = path.split("/")
        if any(part in ("", ".", "..") for part in parts) or any(ord(char) < 32 or ord(char) == 127 for char in path):
            raise BundleError(f"invalid {label} path {path!r}")
        key = path.casefold()
        if key in folded:
            raise BundleError(f"case-fold or duplicate {label} path collision: {path!r}")
        folded[key] = path
        result.append(path)
    for path in result:
        parts = path.casefold().split("/")
        for depth in range(1, len(parts)):
            ancestor = "/".join(parts[:depth])
            if ancestor in folded:
                raise BundleError(f"prefix {label} path collision: {folded[ancestor]!r} and {path!r}")
    return result


def _reject_cross_inventory_collisions(source: Iterable[str], operator: Iterable[str]) -> None:
    protected = tuple(CONTROLLER_NAMESPACES) + tuple(operator)
    reserved_components = {path.casefold() for path in CONTROLLER_NAMESPACES}
    for candidate in source:
        folded = candidate.casefold()
        if any(part in reserved_components for part in folded.split("/")):
            raise BundleError(f"source path {candidate!r} contains a protected namespace")
        for reserved in protected:
            reserved_folded = reserved.casefold()
            if folded == reserved_folded or folded.startswith(reserved_folded + "/") or reserved_folded.startswith(folded + "/"):
                raise BundleError(f"source path {candidate!r} collides with protected path {reserved!r}")
