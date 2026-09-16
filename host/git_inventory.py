"""Quiescent Git layout checks for the reference host's private repository.

This is the layout created by initialize_repository and the host's object/index/
ref operations, not a validator for arbitrary user Git repositories. No Git
process runs here. Call while holding the session lock, before allowing Git to
interpret metadata. Reachable object contents and exact index/commit identity
are separately authenticated by git_identity.
"""

from __future__ import annotations

from host.storage import OwnedDirectory, StorageError


class GitInventoryError(RuntimeError):
    """The private repository contains missing or unregistered Git metadata."""


_DIRECTORIES = frozenset({
    ".git", ".git/info", ".git/objects", ".git/objects/info",
    ".git/objects/pack", ".git/refs", ".git/refs/heads", ".git/refs/tags",
    ".git/logs", ".git/logs/refs", ".git/logs/refs/heads",
})
_FILES = frozenset({
    ".git/HEAD", ".git/config", ".git/index", ".git/info/exclude",
    ".git/info/attributes", ".git/refs/heads/vise-host", ".git/logs/HEAD",
    ".git/logs/refs/heads/vise-host",
})
_HEAD = b"ref: refs/heads/vise-host\n"
_HEX = frozenset("0123456789abcdef")


def _hex(value: str, size: int) -> bool:
    return len(value) == size and all(char in _HEX for char in value)


def verify_git_layout(owned: OwnedDirectory) -> None:
    """Reject unregistered metadata without following links or invoking Git.

    Only the fixed symbolic HEAD and branch exist. Loose SHA-1 objects are the
    registered object storage class; packs, alternates, replacement refs, locks,
    hooks, worktrees and all other extensions are not part of this profile.
    This checks object path/type/mode, not the contents of unreachable objects.
    Interrupted Git lockfiles need explicit transaction recovery; this function
    never removes them or mistakes them for a usable quiescent generation.
    """
    seen_files: set[str] = set()
    seen_directories: set[str] = set()
    try:
        for entry in owned.walk(max_entries=1_000_000):
            path = entry.path
            if path != ".git" and not path.startswith(".git/"):
                continue
            if entry.directory:
                parts = path.split("/")
                fanout = len(parts) == 3 and parts[:2] == [".git", "objects"] and _hex(parts[2], 2)
                if path not in _DIRECTORIES and not fanout:
                    raise GitInventoryError(f"unregistered Git directory: {path}")
                seen_directories.add(path)
                continue
            if path in _FILES:
                if entry.mode not in (0o600, 0o644):
                    raise GitInventoryError(f"invalid Git metadata mode: {path}")
                seen_files.add(path)
                continue
            parts = path.split("/")
            loose_object = (
                len(parts) == 4 and parts[:2] == [".git", "objects"]
                and _hex(parts[2], 2) and _hex(parts[3], 38)
            )
            if not loose_object or entry.mode not in (0o400, 0o444, 0o600, 0o644):
                raise GitInventoryError(f"unregistered Git file: {path}")
        if seen_files != _FILES or not _DIRECTORIES.issubset(seen_directories):
            raise GitInventoryError("private Git layout is missing required metadata")
        head = owned.read_file(".git/HEAD", max_bytes=len(_HEAD))
        if head.data != _HEAD:
            raise GitInventoryError("private Git HEAD is not the fixed symbolic branch")
        branch = owned.read_file(".git/refs/heads/vise-host", max_bytes=41).data
        if len(branch) != 41 or branch[-1:] != b"\n":
            raise GitInventoryError("private Git branch is not one loose SHA-1 ref")
        try:
            oid = branch[:-1].decode("ascii")
        except UnicodeDecodeError as error:
            raise GitInventoryError("private Git branch is not ASCII") from error
        if not _hex(oid, 40):
            raise GitInventoryError("private Git branch is not one loose SHA-1 ref")
    except StorageError as error:
        raise GitInventoryError("private Git inventory is not descriptor-safe") from error
