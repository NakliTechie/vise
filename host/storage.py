"""Descriptor-anchored I/O inside a controller-owned private directory.

These primitives do not implement a session transaction or sandbox. The caller
must hold its session lock, register recovery paths before writes, and never
expose this object to candidate code. Concurrent hostile host writers are outside
the ownership contract. No method recursively removes a directory.
Any failed mutation requires observed-state reconciliation; an exception does
not promise that the filesystem is unchanged or that a retry is safe.
"""

from __future__ import annotations

import os
import stat
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

from host.bundle import BundleError, BundleLimits, OperatorInventory


class StorageError(RuntimeError):
    """An owned-path operation failed or observed unexpected filesystem state."""


class StorageMutationError(StorageError):
    """A syscall completed but its subsequent durability sync failed."""

    def __init__(self, operation: str, phase: str):
        self.operation = operation
        self.phase = phase
        super().__init__(f"owned {operation} reached {phase}; durability is unconfirmed")


@dataclass(frozen=True)
class FileBytes:
    data: bytes
    mode: int


@dataclass(frozen=True)
class DirectoryEntry:
    path: str
    directory: bool
    mode: int
    size: int


class OwnedDirectory:
    """An already-created private root; its ancestor path is controller input."""

    def __init__(self, root: Path, *, limits: BundleLimits = BundleLimits()):
        self._fd = -1
        self._limits = limits
        required = (os.open, os.stat, os.mkdir, os.unlink, os.rmdir, os.rename)
        if (
            not all(call in os.supports_dir_fd for call in required)
            or os.scandir not in os.supports_fd
            or not hasattr(os, "O_NOFOLLOW")
            or not hasattr(os, "O_DIRECTORY")
        ):
            raise StorageError("descriptor-based owned storage is unsupported on this platform")
        try:
            fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try:
                info = os.fstat(fd)
                self._check_owned(info, directory=True)
                if stat.S_IMODE(info.st_mode) & 0o077:
                    raise StorageError("controller root must be private to its owner")
                self._name_max = os.fpathconf(fd, "PC_NAME_MAX")
                self._fd = fd
            except BaseException:
                os.close(fd)
                raise
        except OSError as error:
            raise StorageError("cannot open controller-owned root") from error

    def __enter__(self) -> OwnedDirectory:
        self._require_open()
        return self

    def __exit__(self, *_: object) -> None:
        self.close()

    def close(self) -> None:
        fd, self._fd = self._fd, -1
        if fd >= 0:
            os.close(fd)

    def _require_open(self) -> None:
        if self._fd < 0:
            raise StorageError("controller directory is closed")

    def _parts(self, path: str) -> list[str]:
        self._require_open()
        try:
            OperatorInventory.build((path,), limits=self._limits)
        except BundleError as error:
            raise StorageError("invalid owned relative path") from error
        parts = path.split("/")
        if self._name_max > 0 and any(len(part.encode("utf-8")) > self._name_max for part in parts):
            raise StorageError("owned path component exceeds filesystem limit")
        return parts

    @staticmethod
    def _check_owned(info: os.stat_result, *, directory: bool) -> None:
        wanted_type = stat.S_ISDIR if directory else stat.S_ISREG
        if not wanted_type(info.st_mode):
            raise StorageError("owned entry has an unexpected type")
        if info.st_uid != os.geteuid() or info.st_mode & 0o022:
            raise StorageError("owned entry has unexpected ownership or write permissions")
        if info.st_mode & 0o7000:
            raise StorageError("owned entry has special permission bits")
        if not directory and info.st_nlink != 1:
            raise StorageError("owned file must have exactly one link")

    @contextmanager
    def _directory(self, parts: list[str], *, exact: bool = False) -> Iterator[int]:
        self._require_open()
        fd = os.dup(self._fd)
        try:
            for part in parts:
                if exact:
                    with os.scandir(fd) as entries:
                        if not any(entry.name == part for entry in entries):
                            raise StorageError("owned directory spelling differs")
                next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                os.close(fd)
                fd = next_fd
                self._check_owned(os.fstat(fd), directory=True)
            yield fd
        except OSError as error:
            raise StorageError("owned directory operation failed") from error
        finally:
            os.close(fd)

    @contextmanager
    def _parent(self, path: str) -> Iterator[tuple[int, str]]:
        parts = self._parts(path)
        with self._directory(parts[:-1]) as fd:
            yield fd, parts[-1]

    def mkdirs(self, path: str) -> None:
        """Create ancestors only inside a caller-registered recovery namespace."""
        parts = self._parts(path)
        for depth, name in enumerate(parts):
            with self._directory(parts[:depth]) as parent:
                try:
                    os.mkdir(name, 0o700, dir_fd=parent)
                except FileExistsError:
                    pass
                fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
                try:
                    self._check_owned(os.fstat(fd), directory=True)
                    os.fsync(fd)
                finally:
                    os.close(fd)
                os.fsync(parent)

    @staticmethod
    def _regular_at(parent: int, name: str) -> os.stat_result:
        info = os.stat(name, dir_fd=parent, follow_symlinks=False)
        OwnedDirectory._check_owned(info, directory=False)
        return info

    @staticmethod
    def _stamp(info: os.stat_result) -> tuple[int, ...]:
        return (info.st_dev, info.st_ino, info.st_size, info.st_mode,
                info.st_uid, info.st_nlink, info.st_mtime_ns, info.st_ctime_ns)

    def read_file(self, path: str, *, max_bytes: int) -> FileBytes:
        if type(max_bytes) is not int or max_bytes < 0:
            raise StorageError("read limit must be a non-negative integer")
        with self._parent(path) as (parent, name):
            before = self._regular_at(parent, name)
            if before.st_size > max_bytes:
                raise StorageError("owned file exceeds read limit")
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
            try:
                opened = os.fstat(fd)
                self._check_owned(opened, directory=False)
                if self._stamp(before) != self._stamp(opened):
                    raise StorageError("owned file changed before reading")
                chunks: list[bytes] = []
                size = 0
                while True:
                    chunk = os.read(fd, min(64 * 1024, max_bytes + 1 - size))
                    if not chunk:
                        break
                    chunks.append(chunk)
                    size += len(chunk)
                    if size > max_bytes:
                        raise StorageError("owned file exceeded read limit while reading")
                after = os.fstat(fd)
                named = self._regular_at(parent, name)
                if self._stamp(opened) != self._stamp(after) or self._stamp(after) != self._stamp(named):
                    raise StorageError("owned file changed while reading")
                if size != after.st_size:
                    raise StorageError("owned file size differs from captured bytes")
                return FileBytes(b"".join(chunks), stat.S_IMODE(after.st_mode))
            finally:
                os.close(fd)

    def write_new(self, path: str, data: bytes, *, mode: int = 0o644) -> None:
        """Exclusive durable creation; a failed write retains its partial stage."""
        if type(data) is not bytes or len(data) > self._limits.max_file_bytes:
            raise StorageError("new owned file exceeds byte contract")
        if type(mode) is not int or mode not in (0o600, 0o644, 0o700, 0o755):
            raise StorageError("unsupported owned file mode")
        with self._parent(path) as (parent, name):
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode, dir_fd=parent)
            try:
                os.fchmod(fd, mode)
                offset = 0
                while offset < len(data):
                    count = os.write(fd, data[offset:])
                    if count <= 0:
                        raise StorageError("owned file write made no progress")
                    offset += count
                os.fsync(fd)
            finally:
                os.close(fd)
            os.fsync(parent)

    @staticmethod
    def _expected(value: FileBytes) -> None:
        if type(value) is not FileBytes or type(value.data) is not bytes or type(value.mode) is not int:
            raise StorageError("expected file must include exact bytes and mode")

    def remove_expected(self, path: str, expected: FileBytes) -> None:
        """Remove one inventory-named file only after its exact preflight check."""
        self._expected(expected)
        if self.read_file(path, max_bytes=len(expected.data)) != expected:
            raise StorageError("owned file differs from removal inventory")
        with self._parent(path) as (parent, name):
            self._regular_at(parent, name)
            os.unlink(name, dir_fd=parent)
            try:
                os.fsync(parent)
            except OSError as error:
                raise StorageMutationError("remove", "unlinked") from error

    def move_expected(self, source: str, destination: str, *,
                      expected_source: FileBytes, expected_destination: FileBytes | None) -> None:
        """Rename a registered stage after checking both entries under caller lock."""
        self._expected(expected_source)
        if expected_destination is not None:
            self._expected(expected_destination)
        self._parts(source)
        self._parts(destination)
        if source.casefold() == destination.casefold():
            raise StorageError("source and destination must differ")
        if self.read_file(source, max_bytes=len(expected_source.data)) != expected_source:
            raise StorageError("owned stage differs from publication inventory")
        with self._parent(source) as (source_fd, source_name), self._parent(destination) as (dest_fd, dest_name):
            if expected_destination is None:
                try:
                    os.stat(dest_name, dir_fd=dest_fd, follow_symlinks=False)
                except FileNotFoundError:
                    pass
                else:
                    raise StorageError("new destination already exists")
            elif self.read_file(destination, max_bytes=len(expected_destination.data)) != expected_destination:
                raise StorageError("destination differs from publication inventory")
            self._regular_at(source_fd, source_name)
            os.rename(source_name, dest_name, src_dir_fd=source_fd, dst_dir_fd=dest_fd)
            try:
                os.fsync(source_fd)
                os.fsync(dest_fd)
            except OSError as error:
                raise StorageMutationError("move", "renamed") from error

    def move_tree_expected(
        self, source: str, destination: str, *,
        expected_files: dict[str, FileBytes], expected_directories: dict[str, int],
    ) -> None:
        """Publish one exact registered tree to an absent destination.

        Inventory keys are relative to the tree; directories must include ""
        for its root, plus every ancestor and empty directory, with exact modes.
        The caller holds its lock and has durably registered and staged this
        tree. No contents are deleted or rewritten. Parents are synced before
        rename and both are attempted afterward; a post-rename sync failure
        reports phase "renamed". This is not CAS against hostile host writers.
        """
        source_parts, destination_parts = self._parts(source), self._parts(destination)
        first, second = source.casefold(), destination.casefold()
        if first == second or first.startswith(second + "/") or second.startswith(first + "/"):
            raise StorageError("tree source and destination overlap")
        if type(expected_files) is not dict or type(expected_directories) is not dict:
            raise StorageError("tree inventories must be exact dictionaries")
        files, directories = dict(expected_files), dict(expected_directories)
        if "" not in directories or len(files) > self._limits.max_files:
            raise StorageError("tree inventory lacks its root or exceeds the file limit")
        folded: set[str] = set()
        total = 0
        for path, value in (*files.items(), *directories.items()):
            is_directory = path in directories
            if path != "" or not is_directory:
                self._parts(path)
            if type(path) is not str or path.casefold() in folded:
                raise StorageError("tree inventory paths collide")
            folded.add(path.casefold())
            for prefix in (source, destination):
                self._parts(prefix + "/" + path if path else prefix)
            if is_directory:
                mode = value
            else:
                self._expected(value)
                mode = value.mode
                if len(value.data) > self._limits.max_file_bytes:
                    raise StorageError("tree file exceeds byte limit")
                total += len(value.data)
            if type(mode) is not int or mode < 0 or mode > 0o777 or mode & 0o022:
                raise StorageError("tree inventory mode is unsafe")
            if path:
                parent = path.rpartition("/")[0]
                if parent not in directories:
                    raise StorageError("tree inventory lacks an exact directory ancestor")
        if total > self._limits.max_total_bytes:
            raise StorageError("tree inventory exceeds total byte limit")

        with self._directory(source_parts[:-1], exact=True) as source_fd, self._directory(
            destination_parts[:-1], exact=True,
        ) as destination_fd:
            before = os.stat(source_parts[-1], dir_fd=source_fd, follow_symlinks=False)
            self._check_owned(before, directory=True)
            seen_files: set[str] = set()
            seen_directories: set[str] = set()
            pending = [""]
            while pending:
                relative = pending.pop()
                parts = source_parts + (relative.split("/") if relative else [])
                with self._directory(parts, exact=True) as directory_fd:
                    info = os.fstat(directory_fd)
                    if stat.S_IMODE(info.st_mode) != directories[relative]:
                        raise StorageError("tree directory differs from publication inventory")
                    seen_directories.add(relative)
                    with os.scandir(directory_fd) as entries:
                        for entry in entries:
                            path = relative + "/" + entry.name if relative else entry.name
                            if path in directories:
                                self._check_owned(entry.stat(follow_symlinks=False), directory=True)
                                pending.append(path)
                            elif path in files:
                                expected = files[path]
                                if self.read_file(source + "/" + path, max_bytes=len(expected.data)) != expected:
                                    raise StorageError("tree file differs from publication inventory")
                                seen_files.add(path)
                            else:
                                raise StorageError("tree contains an unregistered entry")
            if seen_files != set(files) or seen_directories != set(directories):
                raise StorageError("tree publication inventory is incomplete")

            def require_absent() -> None:
                try:
                    os.stat(destination_parts[-1], dir_fd=destination_fd, follow_symlinks=False)
                except FileNotFoundError:
                    pass
                else:
                    raise StorageError("tree destination already exists")
                with os.scandir(destination_fd) as entries:
                    if any(entry.name.casefold() == destination_parts[-1].casefold() for entry in entries):
                        raise StorageError("tree destination aliases an existing entry")

            require_absent()
            os.fsync(source_fd)
            os.fsync(destination_fd)
            named = os.stat(source_parts[-1], dir_fd=source_fd, follow_symlinks=False)
            if self._stamp(before) != self._stamp(named):
                raise StorageError("tree source changed before publication")
            require_absent()
            os.rename(source_parts[-1], destination_parts[-1],
                      src_dir_fd=source_fd, dst_dir_fd=destination_fd)
            failure = None
            for fd in (source_fd, destination_fd):
                try:
                    os.fsync(fd)
                except OSError as error:
                    failure = failure or error
            if failure is not None:
                raise StorageMutationError("move-tree", "renamed") from failure

    def remove_empty_directory(self, path: str) -> None:
        with self._parent(path) as (parent, name):
            self._check_owned(os.stat(name, dir_fd=parent, follow_symlinks=False), directory=True)
            os.rmdir(name, dir_fd=parent)
            try:
                os.fsync(parent)
            except OSError as error:
                raise StorageMutationError("remove-directory", "removed") from error

    def walk(self, *, max_entries: int) -> tuple[DirectoryEntry, ...]:
        """Bounded metadata inventory; caller classifies registered runtime paths."""
        if type(max_entries) is not int or max_entries < 0:
            raise StorageError("entry limit must be a non-negative integer")
        result: list[DirectoryEntry] = []
        pending: list[list[str]] = [[]]
        while pending:
            parts = pending.pop()
            with self._directory(parts) as fd:
                with os.scandir(fd) as entries:
                    for entry in entries:
                        if len(result) >= max_entries:
                            raise StorageError("owned inventory exceeds entry limit")
                        path = "/".join([*parts, entry.name])
                        child = self._parts(path)
                        info = entry.stat(follow_symlinks=False)
                        directory = stat.S_ISDIR(info.st_mode)
                        self._check_owned(info, directory=directory)
                        result.append(DirectoryEntry(path, directory, stat.S_IMODE(info.st_mode), info.st_size))
                        if directory:
                            pending.append(child)
        return tuple(sorted(result, key=lambda entry: entry.path))

    @contextmanager
    def exclusive_lock(self, path: str) -> Iterator[None]:
        """Nonblocking advisory controller lock; never truncates existing bytes."""
        import fcntl

        with self._parent(path) as (parent, name):
            try:
                self._regular_at(parent, name)
            except FileNotFoundError:
                pass
            fd = os.open(name, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=parent)
            try:
                self._check_owned(os.fstat(fd), directory=False)
                os.fsync(parent)
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError as error:
                    raise StorageError("controller lock is already held") from error
                try:
                    yield
                finally:
                    fcntl.flock(fd, fcntl.LOCK_UN)
            finally:
                os.close(fd)
