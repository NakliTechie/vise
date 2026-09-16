import shutil
import tempfile
import unittest
from pathlib import Path

from host.git_identity import FixedGitIdentity, GitIdentityError, GitRunner, initialize_repository
from host.storage import FileBytes, OwnedDirectory


GIT = Path(shutil.which("git") or "")
IDENTITY = FixedGitIdentity("Vise Host", "host@example.invalid", "Vise Host", "host@example.invalid")


class GitInitializationPrestateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="vise-git-prestate-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.git = GitRunner(GIT, self.base / "home")

    def make_root(self):
        root = self.base / "session"
        root.mkdir(mode=0o700)
        (root / ".vise-host").mkdir(mode=0o700)
        with OwnedDirectory(root) as owned:
            owned.write_new(".vise-host/session.lock", b"", mode=0o600)
            owned.write_new(".vise-host/intent.json", b"intent\n", mode=0o600)
        return root

    def test_exact_prestate_initializes_without_replacing_controller_files(self):
        root = self.make_root()
        expected = {
            ".vise-host/session.lock": FileBytes(b"", 0o600),
            ".vise-host/intent.json": FileBytes(b"intent\n", 0o600),
        }
        with OwnedDirectory(root) as owned, owned.exclusive_lock(".vise-host/session.lock"):
            bootstrap = initialize_repository(
                root, git=self.git, identity=IDENTITY,
                prestate=expected, caller_lock_held=True,
            )
        self.assertEqual(40, len(bootstrap))
        self.assertEqual(b"intent\n", (root / ".vise-host/intent.json").read_bytes())

    def test_extra_missing_changed_partial_git_and_unlocked_prestate_refuse(self):
        mutations = ("extra", "missing", "changed", "partial_git", "unlocked")
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                root = self.make_root()
                expected = {
                    ".vise-host/session.lock": FileBytes(b"", 0o600),
                    ".vise-host/intent.json": FileBytes(b"intent\n", 0o600),
                }
                if mutation == "extra":
                    (root / ".vise-host/extra").write_bytes(b"x")
                elif mutation == "missing":
                    (root / ".vise-host/intent.json").unlink()
                elif mutation == "changed":
                    (root / ".vise-host/intent.json").write_bytes(b"other\n")
                elif mutation == "partial_git":
                    (root / ".git").mkdir()
                locked = mutation != "unlocked"
                with self.assertRaises(GitIdentityError):
                    initialize_repository(
                        root, git=self.git, identity=IDENTITY,
                        prestate=expected, caller_lock_held=locked,
                    )
                self.assertFalse((root / ".git/HEAD").exists())
                if root.exists():
                    shutil.rmtree(root)

    def test_equal_but_inexact_file_fields_refuse_before_git_creation(self):
        bad = (
            FileBytes(b"intent\n", 384.0), FileBytes(b"intent\n", True),
            FileBytes(bytearray(b"intent\n"), 0o600),
            FileBytes(memoryview(b"intent\n"), 0o600),
            FileBytes(False, 0o600), FileBytes(0.0, 0o600),
        )
        for value in bad:
            with self.subTest(value=value):
                root = self.make_root()
                expected = {
                    ".vise-host/session.lock": FileBytes(b"", 0o600),
                    ".vise-host/intent.json": value,
                }
                with self.assertRaises(GitIdentityError):
                    initialize_repository(root, git=self.git, identity=IDENTITY,
                                          prestate=expected, caller_lock_held=True)
                self.assertFalse((root / ".git").exists())
                self.assertEqual(b"intent\n", (root / ".vise-host/intent.json").read_bytes())
                shutil.rmtree(root)

    def test_exact_deep_prestate_has_no_undocumented_directory_cap(self):
        root = self.make_root()
        path = ".vise-host/" + "deep/" * 40 + "state"
        expected = {
            ".vise-host/session.lock": FileBytes(b"", 0o600),
            ".vise-host/intent.json": FileBytes(b"intent\n", 0o600),
            path: FileBytes(b"state\n", 0o600),
        }
        with OwnedDirectory(root) as owned, owned.exclusive_lock(".vise-host/session.lock"):
            owned.mkdirs(path.rsplit("/", 1)[0])
            owned.write_new(path, b"state\n", mode=0o600)
            bootstrap = initialize_repository(root, git=self.git, identity=IDENTITY,
                                              prestate=expected, caller_lock_held=True)
        self.assertEqual(40, len(bootstrap))
        self.assertEqual(b"state\n", (root / path).read_bytes())


if __name__ == "__main__":
    unittest.main()
