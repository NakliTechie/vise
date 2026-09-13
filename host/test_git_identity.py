import os
import shutil
import tempfile
import unittest
import zlib
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch

from host.bundle import BundleError, SourceEntry, build_bundle
from host.git_identity import (
    FixedGitIdentity,
    GitIdentityError,
    GitRunner,
    activate_assembly,
    construct_assembly,
    initialize_repository,
    inspect_repository,
    verify_active_assembly,
    verify_repository_policy,
)
from host.operator import build_operator


GIT = Path(shutil.which("git") or "")
IDENTITY = FixedGitIdentity("Vise Host", "host@example.invalid", "Vise Host", "host@example.invalid")


class GitIdentityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.base = Path(self.temporary.name)
        self.root = self.base / "session"
        self.git = GitRunner(GIT, self.base / "private-home")
        self.bootstrap = initialize_repository(self.root, git=self.git, identity=IDENTITY)

    def tearDown(self):
        self.temporary.cleanup()

    def assembly(self, candidate_entries, operator_entries=(), generation=0):
        candidate = build_bundle(candidate_entries)
        operator = build_operator(operator_entries, generation=generation)
        return construct_assembly(
            self.root,
            git=self.git,
            identity=IDENTITY,
            bootstrap=self.bootstrap,
            candidate=candidate,
            operator=operator,
        )

    def test_multifile_binary_empty_and_executable_have_actual_git_identity(self):
        candidate = (
            SourceEntry("bin/tool", b"#!/bin/sh\n", True),
            SourceEntry("data/empty", b""),
            SourceEntry("data/raw", b"\x00\xff"),
        )
        operator = (SourceEntry("vise.toml", b"[probe.x]\n"),)
        result = self.assembly(candidate, operator)
        activate_assembly(
            self.root, git=self.git, requested=result, identity=IDENTITY, expected_head=self.bootstrap,
            expected_index_tree=inspect_repository(self.root, git=self.git).index_tree,
            caller_lock_held=True,
        )
        observed = inspect_repository(self.root, git=self.git)
        self.assertEqual((observed.head, observed.index_tree, observed.commit_tree, observed.commit_parent),
                         (result.commit, result.tree, result.tree, self.bootstrap))
        self.assertEqual(
            observed.commit_message,
            f"vise-host assembly schema=1\ncandidate={result.candidate}\noperator={result.operator}\n".encode(),
        )
        listing = self.git.run(("ls-tree", "-r", result.tree), root=self.root).decode()
        self.assertIn("100755 blob", listing)
        self.assertIn("\tbin/tool\n", listing)
        self.assertIn("\tdata/empty\n", listing)
        self.assertIn("\tdata/raw\n", listing)
        self.assertIn("\tvise.toml\n", listing)
        self.assertEqual(
            verify_active_assembly(self.root, git=self.git, identity=IDENTITY, expected=result), observed
        )

    def test_same_inputs_and_reverted_candidate_are_deterministic(self):
        first = self.assembly((SourceEntry("a", b"one"),))
        repeated = self.assembly((SourceEntry("a", b"one"),))
        changed = self.assembly((SourceEntry("a", b"two"),))
        reverted = self.assembly((SourceEntry("a", b"one"),))
        self.assertEqual((first.tree, first.commit), (repeated.tree, repeated.commit))
        self.assertNotEqual((first.tree, first.commit), (changed.tree, changed.commit))
        self.assertEqual((first.tree, first.commit), (reverted.tree, reverted.commit))

    def test_candidate_path_byte_and_mode_each_change_tree_and_commit(self):
        base = self.assembly((SourceEntry("a", b"x"),))
        for entries in (
            (SourceEntry("b", b"x"),),
            (SourceEntry("a", b"y"),),
            (SourceEntry("a", b"x", True),),
        ):
            changed = self.assembly(entries)
            self.assertNotEqual(base.tree, changed.tree)
            self.assertNotEqual(base.commit, changed.commit)

    def test_operator_generation_only_changes_commit_not_tree(self):
        operator = (SourceEntry("vise.toml", b"x"),)
        first = self.assembly((SourceEntry("a", b"x"),), operator, 0)
        second = self.assembly((SourceEntry("a", b"x"),), operator, 1)
        self.assertEqual(first.tree, second.tree)
        self.assertNotEqual(first.operator, second.operator)
        self.assertNotEqual(first.commit, second.commit)

    def test_blob_identity_uses_exact_bytes_without_attributes_or_filters(self):
        hostile = self.base / "hostile"
        hostile.mkdir()
        (hostile / ".gitattributes").write_text("* filter=evil text eol=crlf\n")
        data = b"a\r\nb\x00\xff"
        result = self.assembly((SourceEntry("payload", data),))
        line = self.git.run(("ls-tree", result.tree, "payload"), root=self.root).decode()
        oid = line.split()[2]
        self.assertEqual(self.git.run(("cat-file", "blob", oid), root=self.root), data)

    def test_hostile_inherited_git_environment_and_global_config_are_ignored(self):
        hostile_home = self.base / "hostile-home"
        hostile_home.mkdir()
        marker = self.base / "hook-ran"
        hooks = self.base / "hooks"
        hooks.mkdir()
        hook = hooks / "post-commit"
        hook.write_text(f"#!/bin/sh\ntouch {marker}\n")
        hook.chmod(0o755)
        (hostile_home / ".gitconfig").write_text(
            f"[user]\nname = Hostile\nemail = hostile@example.invalid\n[core]\nhooksPath = {hooks}\n"
        )
        names = ("HOME", "GIT_CONFIG_GLOBAL", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
                 "GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME", "GIT_PAGER", "GIT_EDITOR", "SHELL")
        saved = {name: os.environ.get(name) for name in names}
        try:
            os.environ.update({
                "HOME": str(hostile_home), "GIT_CONFIG_GLOBAL": str(hostile_home / ".gitconfig"),
                "GIT_OBJECT_DIRECTORY": str(self.base / "missing-objects"),
                "GIT_ALTERNATE_OBJECT_DIRECTORIES": str(self.base / "missing-alternates"),
                "GIT_AUTHOR_NAME": "Hostile", "GIT_COMMITTER_NAME": "Hostile",
                "GIT_PAGER": "false", "GIT_EDITOR": "false", "SHELL": "/nonexistent",
            })
            result = self.assembly((SourceEntry("hook-looking", hook.read_bytes(), True),))
        finally:
            for name, value in saved.items():
                if value is None:
                    os.environ.pop(name, None)
                else:
                    os.environ[name] = value
        self.assertFalse(marker.exists())
        commit = self.git.run(("cat-file", "commit", result.commit), root=self.root)
        self.assertIn(b"author Vise Host <host@example.invalid> 0 +0000", commit)
        verify_repository_policy(self.root)
        with (self.root / ".git" / "config").open("ab") as stream:
            stream.write(b"[alias]\nowned = status\n")
        with self.assertRaises(GitIdentityError):
            verify_repository_policy(self.root)
        with self.assertRaises(GitIdentityError):
            self.assembly((SourceEntry("after-tamper", b"x"),))

    def test_fixed_identity_schema_round_trips_and_rejects_malformed_values(self):
        self.assertEqual(FixedGitIdentity.from_dict(IDENTITY.as_dict()), IDENTITY)
        for value in (
            {**IDENTITY.as_dict(), "extra": 1},
            {**IDENTITY.as_dict(), "object_format": "sha256"},
            {**IDENTITY.as_dict(), "timestamp": True},
            {**IDENTITY.as_dict(), "author_name": "bad\nname"},
        ):
            with self.subTest(value=value), self.assertRaises(GitIdentityError):
                FixedGitIdentity.from_dict(value)
        for field, value in (
            ("message_schema", True), ("message_schema", 1.0),
            ("author_name", "bad\x00name"), ("author_name", "bad <name>"),
            ("author_email", "bad<email>"),
        ):
            with self.subTest(field=field, value=repr(value)), self.assertRaises(GitIdentityError):
                FixedGitIdentity(**{
                    "author_name": "Vise Host", "author_email": "host@example.invalid",
                    "committer_name": "Vise Host", "committer_email": "host@example.invalid",
                    field: value,
                })

    def test_malformed_identities_and_inventory_collisions_refuse(self):
        initial = inspect_repository(self.root, git=self.git)
        candidate = build_bundle((SourceEntry("a", b"x"),))
        operator = build_operator((), generation=0)
        for forged in (
            replace(operator, identity="sha256:" + "0" * 64),
            replace(operator, generation=True),
        ):
            with self.subTest(operator=forged), self.assertRaises(GitIdentityError):
                construct_assembly(
                    self.root, git=self.git, identity=IDENTITY, bootstrap=self.bootstrap,
                    candidate=candidate, operator=forged,
                )
        with self.assertRaises(GitIdentityError):
            self.assembly((SourceEntry("vise.toml", b"candidate"),), (SourceEntry("vise.toml", b"operator"),))
        with self.assertRaises(BundleError):
            self.assembly((SourceEntry("safe", b"x"),), (SourceEntry("../operator", b"x"),))
        for path in (".git/config", "nested/.vise-host/state", ".vise/journal.jsonl", ".vise/run.lock", ".vise/tmp/x"):
            with self.subTest(path=path), self.assertRaises(BundleError):
                self.assembly((SourceEntry("safe", b"x"),), (SourceEntry(path, b"operator"),))
        with self.assertRaises(GitIdentityError):
            construct_assembly(
                self.root, git=self.git, identity=IDENTITY, bootstrap="not-an-object",
                candidate=candidate, operator=operator,
            )
        nonbootstrap = self.git.run(
            ("commit-tree", "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "-p", self.bootstrap),
            root=self.root, stdin=b"not bootstrap\n", write=True, commit_identity=IDENTITY,
        ).strip().decode()
        with self.assertRaises(GitIdentityError):
            construct_assembly(
                self.root, git=self.git, identity=IDENTITY, bootstrap=nonbootstrap,
                candidate=candidate, operator=operator,
            )
        for path in (".gitignore", "src/.GITATTRIBUTES"):
            with self.subTest(path=path), self.assertRaises(GitIdentityError):
                self.assembly((SourceEntry(path, b"x"),))
        self.assertEqual(inspect_repository(self.root, git=self.git), initial)

    def test_activation_requires_lock_and_exact_old_head_and_index(self):
        requested = self.assembly((SourceEntry("a", b"x"),))
        initial = inspect_repository(self.root, git=self.git)
        with self.assertRaises(GitIdentityError):
            activate_assembly(
                self.root, git=self.git, requested=requested, identity=IDENTITY, expected_head=self.bootstrap,
                expected_index_tree=initial.index_tree, caller_lock_held=False,
            )
        with self.assertRaises(GitIdentityError):
            activate_assembly(
                self.root, git=self.git, requested=requested, identity=IDENTITY, expected_head="0" * 40,
                expected_index_tree=initial.index_tree, caller_lock_held=True,
            )
        self.assertEqual(inspect_repository(self.root, git=self.git), initial)

    def test_activation_refuses_noncanonical_commit_before_mutation(self):
        requested = self.assembly((SourceEntry("a", b"x"),))
        initial = inspect_repository(self.root, git=self.git)
        other = self.git.run(
            ("commit-tree", requested.tree, "-p", self.bootstrap),
            root=self.root, stdin=b"other\n", write=True, commit_identity=IDENTITY,
        ).strip().decode()
        message = (
            f"vise-host assembly schema=1\ncandidate={requested.candidate}\n"
            f"operator={requested.operator}\n"
        ).encode()
        multiple_parent = self.git.run(
            ("commit-tree", requested.tree, "-p", self.bootstrap, "-p", other),
            root=self.root, stdin=message, write=True, commit_identity=IDENTITY,
        ).strip().decode()
        canonical = self.git.run(("cat-file", "commit", requested.commit), root=self.root)
        wrong_author = self.git.run(
            ("hash-object", "-t", "commit", "-w", "--stdin"), root=self.root,
            stdin=canonical.replace(b"author Vise Host", b"author Other Host", 1), write=True,
        ).strip().decode()
        extra_header = self.git.run(
            ("hash-object", "-t", "commit", "-w", "--stdin"), root=self.root,
            stdin=canonical.replace(b"\n\n", b"\nencoding UTF-8\n\n", 1), write=True,
        ).strip().decode()
        for malicious in (multiple_parent, wrong_author, extra_header):
            with self.subTest(commit=malicious), self.assertRaises(GitIdentityError):
                activate_assembly(
                    self.root, git=self.git, requested=replace(requested, commit=malicious), identity=IDENTITY,
                    expected_head=self.bootstrap, expected_index_tree=initial.index_tree, caller_lock_held=True,
                )
            self.assertEqual(inspect_repository(self.root, git=self.git), initial)

    def test_policy_refuses_symlinked_info_component(self):
        external = self.base / "external-info"
        external.mkdir()
        (external / "exclude").write_bytes(b"")
        (external / "attributes").write_bytes(b"")
        info = self.root / ".git" / "info"
        info.rename(self.root / ".git" / "real-info")
        info.symlink_to(external, target_is_directory=True)
        with self.assertRaises(GitIdentityError):
            verify_repository_policy(self.root)

    def test_inspection_does_not_write_missing_index_tree_object(self):
        blob = self.git.run(
            ("hash-object", "-w", "--stdin"), root=self.root, stdin=b"new", write=True
        ).strip().decode()
        self.git.run(
            ("update-index", "--add", "--cacheinfo", f"100644,{blob},new-file"),
            root=self.root, write=True,
        )
        objects = self.root / ".git" / "objects"
        before = {path.relative_to(objects) for path in objects.glob("*/*") if path.is_file()}
        observed = inspect_repository(self.root, git=self.git)
        after = {path.relative_to(objects) for path in objects.glob("*/*") if path.is_file()}
        self.assertEqual(after, before)
        self.assertNotIn(Path(observed.index_tree[:2]) / observed.index_tree[2:], after - before)

    def test_cas_failure_does_not_automatically_restore_old_index(self):
        requested = self.assembly((SourceEntry("a", b"x"),))
        initial = inspect_repository(self.root, git=self.git)
        original = self.git.run

        def fail_update_ref(args, **kwargs):
            if args[0] == "update-ref":
                raise GitIdentityError("injected CAS failure")
            return original(args, **kwargs)

        with patch.object(self.git, "run", side_effect=fail_update_ref):
            with self.assertRaisesRegex(GitIdentityError, "recovery required"):
                activate_assembly(
                    self.root, git=self.git, requested=requested, identity=IDENTITY,
                    expected_head=self.bootstrap, expected_index_tree=initial.index_tree,
                    caller_lock_held=True,
                )
        observed = inspect_repository(self.root, git=self.git)
        self.assertEqual(self.bootstrap, observed.head)
        self.assertEqual(requested.tree, observed.index_tree)

    def test_active_verification_authenticates_bootstrap_contents(self):
        requested = self.assembly((SourceEntry("a", b"original"),))
        false_bootstrap = self.git.run(
            ("commit-tree", requested.tree), root=self.root, stdin=b"not the empty bootstrap\n",
            write=True, commit_identity=IDENTITY,
        ).strip().decode()
        message = f"vise-host assembly schema=1\ncandidate={requested.candidate}\noperator={requested.operator}\n".encode()
        false_commit = self.git.run(
            ("commit-tree", requested.tree, "-p", false_bootstrap), root=self.root,
            stdin=message, write=True, commit_identity=IDENTITY,
        ).strip().decode()
        self.git.run(("read-tree", requested.tree), root=self.root, write=True)
        self.git.run(("update-ref", "HEAD", false_commit, self.bootstrap), root=self.root, write=True)
        with self.assertRaises(GitIdentityError):
            verify_active_assembly(self.root, git=self.git, identity=IDENTITY,
                                   expected=replace(requested, bootstrap=false_bootstrap, commit=false_commit))

    def test_referenced_blob_and_nested_tree_corruption_refuses(self):
        requested = self.assembly((SourceEntry("nested/a", b"original"),))
        activate_assembly(self.root, git=self.git, requested=requested, identity=IDENTITY,
                          expected_head=self.bootstrap, expected_index_tree=inspect_repository(self.root, git=self.git).index_tree,
                          caller_lock_held=True)
        blob = self.git.run(("rev-parse", f"{requested.tree}:nested/a"), root=self.root).strip().decode()
        subtree = self.git.run(("rev-parse", f"{requested.tree}:nested"), root=self.root).strip().decode()
        for oid, corrupt in (
            (blob, b"blob 4\0evil"),
            (subtree, b"tree 0\0"),
            (requested.tree, b"tree 0\0"),
        ):
            path = self.root / ".git" / "objects" / oid[:2] / oid[2:]
            original = path.read_bytes()
            mode = path.stat().st_mode & 0o777
            path.chmod(0o600)
            try:
                path.write_bytes(zlib.compress(corrupt))
                with self.subTest(oid=oid), self.assertRaises(GitIdentityError):
                    verify_active_assembly(self.root, git=self.git, identity=IDENTITY, expected=requested)
            finally:
                path.write_bytes(original)
                path.chmod(mode)
        verify_active_assembly(self.root, git=self.git, identity=IDENTITY, expected=requested)

    def test_corrupt_referenced_object_refuses_activation_before_git_publication(self):
        requested = self.assembly((SourceEntry("a", b"original"),))
        initial = inspect_repository(self.root, git=self.git)
        blob = self.git.run(("rev-parse", f"{requested.tree}:a"), root=self.root).strip().decode()
        path = self.root / ".git" / "objects" / blob[:2] / blob[2:]
        path.chmod(0o600)
        path.write_bytes(zlib.compress(b"blob 4\0evil"))
        with self.assertRaises(GitIdentityError):
            activate_assembly(self.root, git=self.git, requested=requested, identity=IDENTITY,
                              expected_head=initial.head, expected_index_tree=initial.index_tree,
                              caller_lock_held=True)
        self.assertEqual(initial, inspect_repository(self.root, git=self.git))

    def test_deep_valid_path_matches_git_without_python_recursion(self):
        requested = self.assembly((SourceEntry("/".join(["d"] * 1200), b"deep"),))
        self.git.run(("read-tree", requested.tree), root=self.root, write=True)
        objects = self.root / ".git" / "objects"
        before = {path.relative_to(objects) for path in objects.glob("*/*") if path.is_file()}
        observed = inspect_repository(self.root, git=self.git)
        self.assertEqual(requested.tree, observed.index_tree)
        self.assertEqual(before, {path.relative_to(objects) for path in objects.glob("*/*") if path.is_file()})


if __name__ == "__main__":
    unittest.main()
