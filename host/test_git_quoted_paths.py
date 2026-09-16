"""Valid public paths remain byte-exact through native Git index transport."""

import hashlib
import shutil
import tempfile
import unittest
from pathlib import Path

from host.bundle import SourceEntry, build_bundle
from host.git_identity import (
    FixedGitIdentity, GitRunner, activate_assembly, construct_assembly,
    initialize_repository, inspect_repository,
)
from host.operator import build_operator


class GitQuotedPathTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="vise-git-quoted-")
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.root = self.base / "repository"
        self.git = GitRunner(Path(shutil.which("git")), self.base / "home")
        self.identity = FixedGitIdentity("Host", "host@example.invalid", "Host", "host@example.invalid")
        self.bootstrap = initialize_repository(self.root, git=self.git, identity=self.identity)

    def check_owner(self, owner):
        entries = (
            SourceEntry('"quoted"', b"\x00\xffexact"),
            SourceEntry('"leading', b"leading"),
            SourceEntry('trailing"', b"trailing"),
            SourceEntry('nested/"two words"', b"opaque executable", True),
            SourceEntry("é/日本語", b"utf8"),
        )
        candidate = build_bundle(entries if owner == "candidate" else ())
        operator = build_operator(entries if owner == "operator" else (), generation=0)
        result = construct_assembly(self.root, git=self.git, identity=self.identity,
                                    bootstrap=self.bootstrap, candidate=candidate, operator=operator)
        expected = []
        for entry in sorted(entries, key=lambda entry: entry.path):
            oid = hashlib.sha1(b"blob " + str(len(entry.data)).encode() + b"\0" + entry.data).hexdigest()
            mode = "100755" if entry.executable else "100644"
            expected.append(f"{mode} blob {oid}\t{entry.path}\0".encode("utf-8"))
        self.assertEqual(b"".join(expected), self.git.run(("ls-tree", "-r", "-z", result.tree), root=self.root))
        activate_assembly(self.root, git=self.git, requested=result, identity=self.identity,
                          expected_head=self.bootstrap,
                          expected_index_tree=inspect_repository(self.root, git=self.git).index_tree,
                          caller_lock_held=True)
        names = b"".join(entry.path.encode("utf-8") + b"\0" for entry in sorted(entries, key=lambda entry: entry.path))
        self.assertEqual(names, self.git.run(("ls-files", "-z"), root=self.root))
        self.assertEqual(result.commit, inspect_repository(self.root, git=self.git).head)

    def test_candidate_literal_quotes_space_unicode_and_modes(self):
        self.check_owner("candidate")

    def test_operator_literal_quotes_space_unicode_and_modes(self):
        self.check_owner("operator")


if __name__ == "__main__":
    unittest.main()
