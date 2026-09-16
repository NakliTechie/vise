import os
import shutil
import tempfile
import unittest
from pathlib import Path

from host.bundle import SourceEntry, build_bundle
from host.git_identity import (
    FixedGitIdentity, GitRunner, activate_assembly, construct_assembly,
    initialize_repository, inspect_repository,
)
from host.git_inventory import GitInventoryError, verify_git_layout
from host.operator import build_operator
from host.storage import OwnedDirectory


class GitInventoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="vise-git-inventory-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / "repo"
        self.git = GitRunner(Path(shutil.which("git")), self.base / "home")
        self.identity = FixedGitIdentity("Vise", "v@example.invalid", "Vise", "v@example.invalid")
        self.bootstrap = initialize_repository(self.root, git=self.git, identity=self.identity)

    def verify(self):
        with OwnedDirectory(self.root) as owned:
            verify_git_layout(owned)

    def test_bootstrap_and_actual_assembly_are_allowed_without_mutation(self):
        self.verify()
        wanted = construct_assembly(
            self.root, git=self.git, identity=self.identity, bootstrap=self.bootstrap,
            candidate=build_bundle((SourceEntry("dir/raw", b"\0\xff"), SourceEntry("run", b"x", True))),
            operator=build_operator((), generation=0),
        )
        activate_assembly(
            self.root, git=self.git, identity=self.identity, requested=wanted,
            expected_head=self.bootstrap, expected_index_tree=inspect_repository(self.root, git=self.git).index_tree,
            caller_lock_held=True,
        )
        before = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        self.verify()
        after = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        self.assertEqual(before, after)

    def test_private_umask_initialization_is_allowed(self):
        prior = os.umask(0o077)
        try:
            other = self.base / "private"
            initialize_repository(other, git=self.git, identity=self.identity)
        finally:
            os.umask(prior)
        with OwnedDirectory(other) as owned:
            verify_git_layout(owned)

    def test_unknown_files_and_git_interpretation_extensions_refuse(self):
        paths = (
            ".git/unknown", ".git/objects/info/alternates", ".git/info/grafts",
            ".git/packed-refs", ".git/index.lock", ".git/refs/heads/other",
            ".git/refs/tags/tag", ".git/objects/pack/pack-" + "0" * 40 + ".pack",
        )
        for path in paths:
            with self.subTest(path=path):
                target = self.root / path
                target.write_bytes(b"unregistered\n")
                with self.assertRaises(GitInventoryError):
                    self.verify()
                self.assertEqual(b"unregistered\n", target.read_bytes())
                target.unlink()
        self.verify()

    def test_unknown_empty_directories_refuse(self):
        for path in (".git/hooks", ".git/worktrees", ".git/refs/replace", ".git/objects/xx", ".git/info/extra"):
            with self.subTest(path=path):
                target = self.root / path
                target.mkdir()
                with self.assertRaises(GitInventoryError):
                    self.verify()
                self.assertTrue(target.is_dir())
                target.rmdir()

    def test_detached_or_wrong_symbolic_head_refuses(self):
        head = self.root / ".git/HEAD"
        prior = head.read_bytes()
        for value in (self.bootstrap.encode() + b"\n", b"ref: refs/heads/other\n", b"ref: refs/heads/vise-host\nextra"):
            with self.subTest(value=value):
                head.write_bytes(value)
                with self.assertRaises(GitInventoryError):
                    self.verify()
                self.assertEqual(value, head.read_bytes())
        head.write_bytes(prior)
        self.verify()

    def test_missing_metadata_bad_ref_or_executable_object_refuses(self):
        index = self.root / ".git/index"
        index.rename(self.base / "index.saved")
        with self.assertRaises(GitInventoryError):
            self.verify()
        (self.base / "index.saved").rename(index)
        ref = self.root / ".git/refs/heads/vise-host"
        prior = ref.read_bytes()
        for value in (b"0" * 40, b"g" * 40 + b"\n", b"ref: refs/heads/other\n", b"\xff" * 40 + b"\n"):
            ref.write_bytes(value)
            with self.assertRaises(GitInventoryError):
                self.verify()
        ref.write_bytes(prior)
        obj = next(p for p in (self.root / ".git/objects").glob("??/*") if p.is_file())
        mode = obj.stat().st_mode & 0o777
        obj.chmod(0o755)
        with self.assertRaises(GitInventoryError):
            self.verify()
        obj.chmod(mode)
        self.verify()

    def test_symlink_hardlink_fifo_and_directory_as_file_refuse(self):
        target = self.root / ".git/info/exclude"
        prior = target.read_bytes()
        target.unlink()
        outside = self.base / "outside"
        outside.write_bytes(prior)
        for kind in ("symlink", "hardlink", "fifo", "directory"):
            with self.subTest(kind=kind):
                if kind == "symlink":
                    target.symlink_to(outside)
                elif kind == "hardlink":
                    os.link(outside, target)
                elif kind == "fifo":
                    os.mkfifo(target)
                else:
                    target.mkdir()
                with self.assertRaises(GitInventoryError):
                    self.verify()
                target.rmdir() if kind == "directory" else target.unlink()
        target.write_bytes(prior)
        self.verify()


if __name__ == "__main__":
    unittest.main()
