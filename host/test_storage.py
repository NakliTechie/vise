"""Real-filesystem witnesses for controller-owned storage, not a sandbox."""

from __future__ import annotations

import os
import stat
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from host.bundle import BundleLimits
from host.storage import FileBytes, OwnedDirectory, StorageError, StorageMutationError


class OwnedStorageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="vise-owned-storage-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / "root"
        self.root.mkdir(mode=0o700)
        self.storage = OwnedDirectory(self.root)
        self.addCleanup(self.storage.close)

    def test_exact_files_modes_empty_binary_and_reopen(self):
        self.storage.mkdirs("recovery/q/requested")
        for name, data, mode in (
            ("empty", b"", 0o644), ("binary", b"\x00\xff\r\n", 0o600),
            ("executable", b"not executed", 0o755), ("private", b"x", 0o700),
        ):
            path = "recovery/q/requested/" + name
            self.storage.write_new(path, data, mode=mode)
            self.assertEqual(FileBytes(data, mode), self.storage.read_file(path, max_bytes=len(data)))
            self.assertEqual(mode, stat.S_IMODE((self.root / path).stat().st_mode))
        self.storage.close()
        with OwnedDirectory(self.root) as reopened:
            self.assertEqual(FileBytes(b"\x00\xff\r\n", 0o600), reopened.read_file(
                "recovery/q/requested/binary", max_bytes=4))
        self.storage.close()
        with self.assertRaises(StorageError):
            self.storage.mkdirs("closed")

    def test_root_must_be_private_real_directory(self):
        alias = self.base / "alias"
        alias.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(StorageError):
            OwnedDirectory(alias)
        self.root.chmod(0o755)
        with self.assertRaises(StorageError):
            OwnedDirectory(self.root)
        self.root.chmod(0o700)

    def test_descriptor_root_survives_path_replacement(self):
        moved = self.base / "moved"
        self.root.rename(moved)
        self.root.mkdir(mode=0o700)
        self.storage.write_new("anchored", b"old root")
        self.assertEqual(b"old root", (moved / "anchored").read_bytes())
        self.assertEqual([], list(self.root.iterdir()))

    def test_invalid_paths_refuse_before_creating_any_ancestor(self):
        paths = ["", ".", "../escape", "a/../b", "a//b", "a/", "/absolute",
                 "a\\b", "a/\x00", "a/e\u0301", "a/" + "x" * 256, None, 4]
        for path in paths:
            with self.subTest(path=path), self.assertRaises(StorageError):
                self.storage.mkdirs(path)
        self.assertEqual([], list(self.root.iterdir()))
        for path in paths:
            with self.subTest(source=path), self.assertRaises(StorageError):
                self.storage.move_expected(path, "dest", expected_source=FileBytes(b"", 0o644),
                                           expected_destination=None)

    def test_read_and_write_bounds_and_strict_types(self):
        self.storage.write_new("file", b"123")
        self.assertEqual(b"123", self.storage.read_file("file", max_bytes=3).data)
        for limit in (0, 2, -1, True, 3.0, None):
            with self.subTest(limit=limit), self.assertRaises(StorageError):
                self.storage.read_file("file", max_bytes=limit)
        with OwnedDirectory(self.root, limits=BundleLimits(max_file_bytes=2)) as bounded:
            for data in (b"123", "12", bytearray(b"12")):
                with self.subTest(data=data), self.assertRaises(StorageError):
                    bounded.write_new("rejected", data)
            bounded.write_new("exact", b"12")
        for mode in (True, 0o777, 0o4755, "644"):
            with self.subTest(mode=mode), self.assertRaises(StorageError):
                self.storage.write_new("bad-mode", b"", mode=mode)
        self.assertFalse((self.root / "rejected").exists())
        self.assertFalse((self.root / "bad-mode").exists())

    def test_symlink_parent_and_leaf_refuse_without_touching_target(self):
        outside = self.base / "outside"
        outside.mkdir(mode=0o700)
        sentinel = outside / "sentinel"
        sentinel.write_bytes(b"untouched")
        (self.root / "parent").symlink_to(outside, target_is_directory=True)
        (self.root / "leaf").symlink_to(sentinel)
        for path in ("parent/sentinel", "leaf"):
            with self.subTest(path=path):
                with self.assertRaises(StorageError):
                    self.storage.read_file(path, max_bytes=100)
                with self.assertRaises(StorageError):
                    self.storage.remove_expected(path, FileBytes(b"untouched", 0o644))
                with self.assertRaises(StorageError):
                    self.storage.write_new(path, b"replacement")
        with self.assertRaises(StorageError):
            self.storage.mkdirs("parent/new")
        self.assertEqual(b"untouched", sentinel.read_bytes())
        self.assertFalse((outside / "new").exists())

    def test_hardlink_fifo_and_unsafe_modes_refuse(self):
        outside = self.base / "original"
        outside.write_bytes(b"sentinel")
        os.link(outside, self.root / "hard")
        os.mkfifo(self.root / "fifo", 0o600)
        self.storage.write_new("writable", b"sentinel")
        (self.root / "writable").chmod(0o666)
        self.storage.write_new("special", b"sentinel")
        (self.root / "special").chmod(0o4644)
        for path in ("hard", "fifo", "writable", "special"):
            with self.subTest(path=path):
                with self.assertRaises(StorageError):
                    self.storage.read_file(path, max_bytes=100)
                with self.assertRaises(StorageError):
                    self.storage.remove_expected(path, FileBytes(b"sentinel", 0o644))
                with self.assertRaises(StorageError):
                    with self.storage.exclusive_lock(path):
                        self.fail("unsafe entry locked")
        self.assertEqual(b"sentinel", outside.read_bytes())
        self.assertTrue(stat.S_ISFIFO((self.root / "fifo").lstat().st_mode))

    def test_exclusive_creation_and_exact_move_remove(self):
        self.storage.mkdirs("stages")
        original = FileBytes(b"original", 0o644)
        replacement = FileBytes(b"replacement", 0o755)
        self.storage.write_new("live", original.data, mode=original.mode)
        self.storage.write_new("stages/new", replacement.data, mode=replacement.mode)
        with self.assertRaises(StorageError):
            self.storage.write_new("live", b"overwrite")
        for expected in (None, FileBytes(b"changed!", 0o644), FileBytes(b"original", 0o755)):
            with self.subTest(expected=expected), self.assertRaises(StorageError):
                self.storage.move_expected("stages/new", "live", expected_source=replacement,
                                           expected_destination=expected)
            self.assertEqual(original, self.storage.read_file("live", max_bytes=100))
            self.assertEqual(replacement, self.storage.read_file("stages/new", max_bytes=100))
        self.storage.move_expected("stages/new", "live", expected_source=replacement,
                                   expected_destination=original)
        self.assertFalse((self.root / "stages/new").exists())
        self.assertEqual(replacement, self.storage.read_file("live", max_bytes=100))
        self.storage.move_expected("live", "stages/final", expected_source=replacement,
                                   expected_destination=None)
        with self.assertRaises(StorageError):
            self.storage.remove_expected("stages/final", original)
        with self.assertRaises(StorageError):
            self.storage.remove_empty_directory("stages")
        self.storage.remove_expected("stages/final", replacement)
        self.storage.remove_empty_directory("stages")
        self.assertEqual([], list(self.root.iterdir()))

    def test_walk_is_bounded_and_rejects_unknown_types(self):
        self.assertEqual((), self.storage.walk(max_entries=0))
        self.storage.mkdirs("a/b")
        self.storage.write_new("a/b/file", b"bytes", mode=0o755)
        self.storage.write_new("z", b"")
        entries = self.storage.walk(max_entries=4)
        self.assertEqual(["a", "a/b", "a/b/file", "z"], [e.path for e in entries])
        self.assertEqual([True, True, False, False], [e.directory for e in entries])
        self.assertEqual((0o755, 5), (entries[2].mode, entries[2].size))
        for limit in (0, 3, -1, True):
            with self.subTest(limit=limit), self.assertRaises(StorageError):
                self.storage.walk(max_entries=limit)
        (self.root / "alias").symlink_to(self.root / "z")
        with self.assertRaises(StorageError):
            self.storage.walk(max_entries=100)

    def test_lock_contends_and_preserves_existing_bytes(self):
        self.storage.write_new("session.lock", b"lock sentinel", mode=0o600)
        with OwnedDirectory(self.root) as second:
            with self.storage.exclusive_lock("session.lock"):
                with self.assertRaisesRegex(StorageError, "already held"):
                    with second.exclusive_lock("session.lock"):
                        self.fail("second lock acquired")
            with second.exclusive_lock("session.lock"):
                self.assertEqual(b"lock sentinel", second.read_file("session.lock", max_bytes=100).data)
            with second.exclusive_lock("new.lock"):
                self.assertEqual(FileBytes(b"", 0o600), second.read_file("new.lock", max_bytes=0))

    def test_failed_write_retains_stage_for_registered_recovery(self):
        with patch("host.storage.os.write", return_value=0):
            with self.assertRaisesRegex(StorageError, "no progress"):
                self.storage.write_new("partial", b"not written")
        self.assertEqual(FileBytes(b"", 0o644), self.storage.read_file("partial", max_bytes=0))
        with patch("host.storage.os.fsync", side_effect=OSError("injected sync failure")):
            with self.assertRaises(StorageError):
                self.storage.write_new("unsynced", b"retained")
        self.assertEqual(FileBytes(b"retained", 0o644), self.storage.read_file("unsynced", max_bytes=8))

    def test_changed_during_read_refuses_and_does_not_delete(self):
        self.storage.write_new("file", b"initial")
        read = os.read
        changed = False

        def change_after_read(fd, size):
            nonlocal changed
            chunk = read(fd, size)
            if not changed:
                changed = True
                (self.root / "file").write_bytes(b"changed")
            return chunk

        with patch("host.storage.os.read", side_effect=change_after_read):
            with self.assertRaisesRegex(StorageError, "changed while reading"):
                self.storage.read_file("file", max_bytes=100)
        self.assertEqual(b"changed", (self.root / "file").read_bytes())

    def test_post_mutation_sync_failure_reports_observed_phase(self):
        self.storage.mkdirs("stages")
        for failed_sync in (1, 2):
            self.storage.write_new("stages/new", b"new")
            sync = os.fsync
            count = 0

            def fail_selected(fd):
                nonlocal count
                count += 1
                if count == failed_sync:
                    raise OSError("injected sync failure")
                sync(fd)

            with patch("host.storage.os.fsync", side_effect=fail_selected):
                with self.assertRaises(StorageMutationError) as raised:
                    self.storage.move_expected("stages/new", "live", expected_source=FileBytes(b"new", 0o644),
                                               expected_destination=None)
            self.assertEqual(("move", "renamed"), (raised.exception.operation, raised.exception.phase))
            self.assertFalse((self.root / "stages/new").exists())
            self.assertEqual(b"new", (self.root / "live").read_bytes())
            with patch("host.storage.os.fsync", side_effect=OSError("injected sync failure")):
                with self.assertRaises(StorageMutationError) as removed:
                    self.storage.remove_expected("live", FileBytes(b"new", 0o644))
            self.assertEqual(("remove", "unlinked"), (removed.exception.operation, removed.exception.phase))
            self.assertFalse((self.root / "live").exists())
        with patch("host.storage.os.fsync", side_effect=OSError("injected sync failure")):
            with self.assertRaises(StorageMutationError) as directory:
                self.storage.remove_empty_directory("stages")
        self.assertEqual(("remove-directory", "removed"), (directory.exception.operation, directory.exception.phase))
        self.assertFalse((self.root / "stages").exists())


if __name__ == "__main__":
    unittest.main()
