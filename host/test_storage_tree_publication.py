"""Real-filesystem checks for registered whole-directory publication."""

from __future__ import annotations

import os
import stat
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from host.storage import FileBytes, OwnedDirectory, StorageError, StorageMutationError


def snapshot(root):
    records = []
    for path in [root, *sorted(root.rglob("*"))]:
        info = path.lstat()
        data = (path.read_bytes() if stat.S_ISREG(info.st_mode) else
                os.readlink(path) if stat.S_ISLNK(info.st_mode) else None)
        records.append((str(path.relative_to(root)), info.st_mode, info.st_uid,
                        info.st_ino, info.st_nlink, info.st_size,
                        info.st_mtime_ns, info.st_ctime_ns, data))
    return records


class TreePublicationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="vise-tree-publication-")
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.root = self.base / "root"
        self.root.mkdir(mode=0o700)
        self.owned = OwnedDirectory(self.root)
        self.addCleanup(self.owned.close)
        self.files = {
            "nested/binary": FileBytes(b"\x00\xff\r\n", 0o600),
            "nested/empty-file": FileBytes(b"", 0o644),
            "tool": FileBytes(b"never executed\n", 0o755),
            "readonly": FileBytes(b"object", 0o444),
        }
        self.directories = {"": 0o700, "nested": 0o755, "empty-dir": 0o700}
        self.owned.mkdirs("recovery/q/source/nested")
        self.owned.mkdirs("recovery/q/source/empty-dir")
        self.owned.mkdirs("published")
        (self.root / "recovery/q/source/nested").chmod(0o755)
        for path, value in self.files.items():
            self.owned.write_new("recovery/q/source/" + path, value.data)
            (self.root / "recovery/q/source" / path).chmod(value.mode)
        self.source = "recovery/q/source"
        self.destination = "published/live"

    def publish(self, **overrides):
        args = dict(source=self.source, destination=self.destination,
                    expected_files=self.files, expected_directories=self.directories)
        args.update(overrides)
        self.owned.move_tree_expected(**args)

    def refuse(self, **overrides):
        before = snapshot(self.root)
        with self.assertRaises(StorageError):
            self.publish(**overrides)
        self.assertEqual(before, snapshot(self.root))

    def assert_published(self):
        self.assertFalse((self.root / self.source).exists())
        for path, expected in self.files.items():
            self.assertEqual(expected, self.owned.read_file(self.destination + "/" + path,
                                                          max_bytes=len(expected.data)))
        for path, mode in self.directories.items():
            self.assertEqual(mode, stat.S_IMODE((self.root / self.destination / path).stat().st_mode))

    def test_nested_binary_empty_executable_tree_moves_once_and_syncs_both_parents(self):
        original_inode = (self.root / self.source).stat().st_ino
        rename = os.rename
        sync = os.fsync
        events = []

        def record_rename(*args, **kwargs):
            events.append(("rename", os.fstat(kwargs["src_dir_fd"]).st_ino,
                           os.fstat(kwargs["dst_dir_fd"]).st_ino))
            return rename(*args, **kwargs)

        def record_sync(fd):
            events.append(("sync", os.fstat(fd).st_ino))
            sync(fd)

        with patch("host.storage.os.rename", side_effect=record_rename), patch(
            "host.storage.os.fsync", side_effect=record_sync,
        ):
            self.publish()
        self.assert_published()
        self.assertEqual(original_inode, (self.root / self.destination).stat().st_ino)
        source_parent = (self.root / "recovery/q").stat().st_ino
        destination_parent = (self.root / "published").stat().st_ino
        self.assertEqual([("sync", source_parent), ("sync", destination_parent),
                          ("rename", source_parent, destination_parent),
                          ("sync", source_parent), ("sync", destination_parent)], events)

    def test_same_parent_publication(self):
        self.destination = "recovery/q/published"
        self.publish()
        self.assert_published()

    def test_empty_tree_root_is_explicit(self):
        self.owned.mkdirs("empty")
        self.publish(source="empty", destination="published/empty",
                     expected_files={}, expected_directories={"": 0o700})
        self.assertEqual([], list((self.root / "published/empty").iterdir()))
        self.assertFalse((self.root / "empty").exists())

    def test_root_descriptor_stays_anchored_after_root_path_replacement(self):
        moved = self.base / "moved"
        self.root.rename(moved)
        self.root.mkdir(mode=0o700)
        self.publish()
        self.assertEqual([], list(self.root.iterdir()))
        self.root = moved
        self.assert_published()

    def test_invalid_source_destination_paths_aliases_and_ancestor_overlap(self):
        for path in (None, True, 1, "", ".", "/absolute", "../outside", "a/../b", "a//b",
                     "a/", "a\\b", "a/\x00", "a/e\u0301", "x" * 256):
            with self.subTest(source=path):
                self.refuse(source=path)
            with self.subTest(destination=path):
                self.refuse(destination=path)
        for destination in (self.source, self.source.upper(), self.source + "/inside", "recovery/q"):
            with self.subTest(destination=destination):
                self.refuse(destination=destination)
        self.refuse(source="recovery/Q/source")
        self.refuse(source="recovery/q/Source")
        self.refuse(destination="Published/live")

    def test_invalid_inventory_types_paths_modes_and_collisions(self):
        class DictSubclass(dict):
            pass

        class BytesSubclass(bytes):
            pass

        class IntSubclass(int):
            pass

        cases = [
            {"expected_files": []}, {"expected_files": DictSubclass(self.files)},
            {"expected_directories": []}, {"expected_directories": DictSubclass(self.directories)},
            {"expected_directories": {}}, {"expected_files": {"": FileBytes(b"", 0o600)}},
        ]
        for bad in (True, 0o700 * 1.0, "700", -1, 0o777, 0o1700, IntSubclass(0o700)):
            cases.append({"expected_directories": {**self.directories, "": bad}})
        for bad in (FileBytes(b"object", True), FileBytes(b"object", 292.0),
                    FileBytes(bytearray(b"object"), 0o444), FileBytes(memoryview(b"object"), 0o444),
                    FileBytes(BytesSubclass(b"object"), 0o444), FileBytes(True, 0o444),
                    FileBytes(b"object", IntSubclass(0o444)), FileBytes(b"object", 0o4666), object()):
            cases.append({"expected_files": {**self.files, "readonly": bad}})
        for path in (None, True, "nested/./binary", "nested//binary", "nested/../binary",
                     "nested/binary/", "nested\\binary", "e\u0301", "x" * 256):
            cases.append({"expected_files": {**self.files, path: FileBytes(b"", 0o644)}})
        cases.extend([
            {"expected_files": {**self.files, "TOOL": self.files["tool"]}},
            {"expected_directories": {**self.directories, "tool": 0o700}},
            {"expected_directories": {**self.directories, "NESTED": 0o755}},
            {"expected_directories": {"": 0o700, "empty-dir": 0o700}},
            {"expected_directories": {"": 0o700, "NESTED": 0o755, "empty-dir": 0o700}},
        ])
        for overrides in cases:
            with self.subTest(overrides=overrides):
                self.refuse(**overrides)

    def test_expected_missing_files_empty_directories_and_wrong_exact_modes_refuse(self):
        self.refuse(expected_files={**self.files, "missing": FileBytes(b"", 0o644)})
        self.refuse(expected_directories={**self.directories, "missing-empty": 0o700})
        self.refuse(expected_directories={**self.directories, "nested": 0o700})
        self.refuse(expected_files={**self.files, "tool": FileBytes(b"never executed\n", 0o644)})
        self.refuse(expected_files={**self.files, "tool": FileBytes(b"different", 0o755)})

    def test_missing_source_and_parent_refuse_without_creation(self):
        self.refuse(source="missing/source")
        self.refuse(source="recovery/q/missing")
        self.refuse(destination="missing/live")

    def test_existing_destination_file_directory_and_symlink_never_overwritten(self):
        for kind in ("file", "directory", "symlink"):
            destination = self.root / "published" / kind
            if kind == "file":
                destination.write_bytes(b"untouched")
            elif kind == "directory":
                destination.mkdir()
            else:
                destination.symlink_to(self.base / "absent")
            with self.subTest(kind=kind):
                self.refuse(destination="published/" + kind)

    def test_case_alias_destination_refuses_on_any_filesystem(self):
        (self.root / "published/LIVE").write_bytes(b"untouched")
        self.refuse()

    def test_unregistered_file_and_empty_directory_refuse(self):
        (self.root / self.source / "extra").write_bytes(b"unregistered")
        self.refuse()
        (self.root / self.source / "extra").unlink()
        (self.root / self.source / "extra-dir").mkdir()
        self.refuse()

    def test_special_types_links_and_unsafe_modes_refuse(self):
        file = self.root / self.source / "tool"
        for kind in ("symlink", "hardlink", "fifo", "directory", "writable", "special"):
            with self.subTest(kind=kind):
                file.unlink()
                if kind == "symlink":
                    file.symlink_to(self.base / "absent")
                elif kind == "hardlink":
                    original = self.base / "original"
                    original.write_bytes(self.files["tool"].data)
                    original.chmod(0o755)
                    os.link(original, file)
                elif kind == "fifo":
                    os.mkfifo(file, 0o600)
                elif kind == "directory":
                    file.mkdir()
                else:
                    file.write_bytes(self.files["tool"].data)
                    file.chmod(0o777 if kind == "writable" else 0o4755)
                self.refuse()
                if kind == "directory":
                    file.rmdir()
                else:
                    file.unlink()
                file.write_bytes(self.files["tool"].data)
                file.chmod(0o755)

    def test_symlinked_source_and_destination_ancestors_refuse(self):
        (self.root / "alias").symlink_to(self.root / "recovery", target_is_directory=True)
        self.refuse(source="alias/q/source")
        (self.root / "alias-destination").symlink_to(self.root / "published", target_is_directory=True)
        self.refuse(destination="alias-destination/live")
        (self.root / "source-link").symlink_to(self.root / self.source, target_is_directory=True)
        self.refuse(source="source-link")

    def test_changed_during_file_read_refuses(self):
        original_read = os.read
        changed = False

        def mutate(fd, size):
            nonlocal changed
            result = original_read(fd, size)
            if not changed:
                changed = True
                (self.root / self.source / "nested/binary").write_bytes(b"tamper")
            return result

        with patch("host.storage.os.read", side_effect=mutate):
            with self.assertRaises(StorageError):
                self.publish()
        self.assertTrue((self.root / self.source).exists())
        self.assertFalse((self.root / self.destination).exists())
        self.assertEqual(b"tamper", (self.root / self.source / "nested/binary").read_bytes())

    def test_pre_rename_sync_failures_do_not_publish(self):
        for fail_at in (1, 2):
            original_sync = os.fsync
            count = 0

            def fail(fd):
                nonlocal count
                count += 1
                if count == fail_at:
                    raise OSError("injected pre-rename fsync failure")
                original_sync(fd)

            with self.subTest(fail_at=fail_at), patch("host.storage.os.fsync", side_effect=fail):
                self.refuse()

    def test_rename_failure_leaves_source_and_destination_unchanged(self):
        with patch("host.storage.os.rename", side_effect=OSError("injected rename failure")):
            self.refuse()

    def test_each_post_rename_sync_failure_reports_phase_and_attempts_both_parents(self):
        for fail_at in (3, 4):
            original_sync = os.fsync
            calls = []

            def fail(fd):
                calls.append(os.fstat(fd).st_ino)
                if len(calls) == fail_at:
                    raise OSError("injected post-rename fsync failure")
                original_sync(fd)

            with self.subTest(fail_at=fail_at), patch("host.storage.os.fsync", side_effect=fail):
                with self.assertRaises(StorageMutationError) as raised:
                    self.publish()
            self.assertEqual(("move-tree", "renamed"), (raised.exception.operation, raised.exception.phase))
            self.assertEqual(4, len(calls))
            self.assertEqual(calls[:2], calls[2:])
            self.assert_published()
            self.owned.move_tree_expected(self.destination, self.source,
                                          expected_files=self.files, expected_directories=self.directories)


if __name__ == "__main__":
    unittest.main()
