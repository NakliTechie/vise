"""Focused initialization lifecycle/crash rows; not the exhaustive seam matrix."""

import hashlib
import json
import os
import select
import signal
import stat
import subprocess
import sys
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch

from host import session_initialization as init
from host.bundle import BundleLimits, SourceEntry, build_bundle
from host.git_identity import GitRunner, inspect_repository
from host.operator import build_operator
from host.session import SessionError, advance_operator, initialize_session, materialize_candidate, open_session
from host.storage import OwnedDirectory, StorageError
from host.test_session import GIT, IDENTITY, IGNORE


class Crash(BaseException):
    pass


def snapshot(root):
    result = []
    for path in [root, *sorted(root.rglob("*"))]:
        info = path.lstat()
        data = (path.read_bytes() if stat.S_ISREG(info.st_mode) else
                os.readlink(path) if stat.S_ISLNK(info.st_mode) else None)
        result.append((str(path.relative_to(root)), info.st_mode, info.st_uid, info.st_nlink,
                       info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns, data))
    return result


CRASH_CHILD = r'''
import json, os, signal, sys
from pathlib import Path
from host import session_initialization as init
from host.git_identity import FixedGitIdentity, GitRunner
from host.operator import decode_operator
from host.session import initialize_session
root, executable, home, scenario = sys.argv[1:]
def stop(label):
    print(json.dumps({"stopped":label,"pid":os.getpid(),"root":root}), flush=True)
    os.kill(os.getpid(), signal.SIGSTOP)
def checkpoint(label):
    if scenario == 'directory-rename' and label == 'after-replacing':
        os.rename = observed_rename
    if (label == scenario or scenario == 'first-o' and label.startswith('after-install:')
            or scenario == 'cleanup-delete' and label.startswith('after-cleanup-delete:')):
        stop(label)
init._checkpoint = checkpoint
rename = os.rename
def observed_rename(source, destination, **kwargs):
    result = rename(source, destination, **kwargs)
    if scenario == 'directory-rename' and source == 'git-seed' and destination == '.git':
        stop('actual-directory-rename-before-parent-fsync')
    return result
identity = FixedGitIdentity('Vise Host','host@example.invalid','Vise Host','host@example.invalid')
initialize_session(Path(root), decode_operator(sys.stdin.buffer.read()),
                   git=GitRunner(Path(executable),Path(home)), identity=identity)
raise AssertionError('crash seam was not reached')
'''


REOPEN_CHILD = r'''
import hashlib, json, stat, sys
from dataclasses import asdict
from pathlib import Path
from host.git_identity import GitRunner, inspect_repository
from host.session import open_session, observe_generation
root, executable, home = map(Path,sys.argv[1:])
git = GitRunner(executable,home)
session = open_session(root,git=git)
result = observe_generation(session)
observed = inspect_repository(root,git=git)
entries = sorted((*session.candidate.entries,*session.operator.files),key=lambda e:e.path)
index = b''
tree = b''
for entry in entries:
    actual = (root/entry.path).read_bytes()
    assert actual == entry.data
    mode = '100755' if entry.executable else '100644'
    assert stat.S_IMODE((root/entry.path).stat().st_mode) == (0o755 if entry.executable else 0o644)
    oid = hashlib.sha1(b'blob '+str(len(actual)).encode()+b'\0'+actual).hexdigest()
    index += f'{mode} {oid} 0\t{entry.path}\0'.encode()
    tree += f'{mode} blob {oid}\t{entry.path}\0'.encode()
assert git.run(('ls-files','--stage','-z'),root=root) == index
assert git.run(('ls-tree','-r','-z',result.identity.tree),root=root) == tree
assert git.run(('rev-parse','HEAD'),root=root).strip().decode() == result.identity.commit
assert not git.run(('status','--porcelain=v1','-z','--untracked-files=all'),root=root)
assert observed.index_tree == result.identity.tree
assert not tuple((root/'.vise-host/intents').iterdir())
assert not tuple((root/'.vise-host/recovery').iterdir())
lines = (root/'.vise-host/operations.jsonl').read_bytes().splitlines()
assert len(lines) == 1 and json.loads(lines[0])['operation'] == 'initialize'
print(json.dumps({'identity':asdict(result.identity),'session_id':session.session_id,
                  'fixed':session.identity.as_dict(),'operator':session.operator.identity,
                  'candidate':session.candidate.identity,'native_index':observed.index_tree},sort_keys=True))
'''


class InitializationTests(unittest.TestCase):
    def setUp(self):
        # Deliberately retained: failed process/recovery fixtures are evidence.
        self.base = Path(tempfile.mkdtemp(prefix="vise-initialization-focused-"))
        print("initialization_fixture=" + str(self.base), flush=True)
        self.root = self.base / "session"
        self.git = GitRunner(GIT, self.base / "home")
        self.operator = build_operator((
            SourceEntry(".gitignore", IGNORE), SourceEntry("vise.toml", b"opaque manifest\n"),
            SourceEntry("vise.lock", b"opaque lock\n"), SourceEntry(".vise/blobs/" + "a" * 64, b"\x00\xff"),
            SourceEntry("spec/expected", b"spec\n"), SourceEntry("tool", b"opaque executable", True),
            SourceEntry("empty", b""),
        ), generation=0)

    def start(self):
        return initialize_session(self.root, self.operator, git=self.git, identity=IDENTITY)

    def interrupt(self, label):
        def checkpoint(actual):
            if actual == label:
                raise Crash()
        with patch.object(init, "_checkpoint", checkpoint), self.assertRaises(Crash):
            self.start()

    def test_initial_c_o_git_current_and_reserved_looking_data_names(self):
        self.operator = build_operator((*self.operator.files, SourceEntry("manifest.json", b"data"),
                                        SourceEntry("requested/prior/value", b"data"),
                                        SourceEntry('"quoted"', b"literal")), generation=0)
        result = self.start()
        session = open_session(self.root, git=self.git)
        self.assertEqual(build_bundle(()).identity, result.identity.candidate)
        self.assertEqual(self.operator, session.operator)
        self.assertEqual(result.identity.commit, inspect_repository(self.root, git=self.git).head)
        self.assertEqual(1, len((self.root / ".vise-host/operations.jsonl").read_bytes().splitlines()))
        self.assertFalse(tuple((self.root / ".vise-host/recovery").iterdir()))
        self.assertEqual(session.assembly, open_session(self.root, git=self.git).assembly)

    def test_registered_preparing_prefix_resumes(self):
        self.interrupt("after-ownership-intent")
        with OwnedDirectory(self.root) as owned:
            seed = init._load_seed(owned)
            requested = init._session(self.root, self.git, seed)
            files, _ = init._registered(seed, requested)
            path = next(path for path in files if path.endswith("/requested/vise.toml"))
            stage = init._stage(seed, path)
            owned.mkdirs(stage.rsplit("/", 1)[0])
            owned.write_new(stage, files[path].data[:4], mode=0o600)
        session = open_session(self.root, git=self.git)
        self.assertEqual(self.operator, session.operator)
        self.assertEqual(session.assembly, open_session(self.root, git=self.git).assembly)

    def test_actual_process_crashes_reopen_in_two_fresh_processes(self):
        for scenario in ("directory-rename", "first-o", "after-current-publication", "cleanup-delete"):
            with self.subTest(scenario=scenario):
                root = self.base / scenario
                process = subprocess.Popen([sys.executable, "-B", "-c", CRASH_CHILD, str(root), str(GIT),
                                            str(self.base / (scenario + "-home")), scenario],
                                           stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                process.stdin.write(self.operator.encoded)
                process.stdin.close()
                ready, _, _ = select.select([process.stdout], [], [], 30)
                if not ready:
                    process.kill()
                    process.wait()
                    process.stdout.close()
                    process.stderr.close()
                    self.fail("child did not reach registered seam; retained " + str(root))
                stopped = process.stdout.readline().decode().strip()
                if not stopped:
                    error = process.stderr.read().decode()
                    process.wait(timeout=10)
                    process.stdout.close()
                    process.stderr.close()
                    self.fail(error)
                os.kill(process.pid, signal.SIGKILL)
                process.wait(timeout=10)
                self.assertEqual(-signal.SIGKILL, process.returncode)
                print("initialization_kill=" + stopped + " returncode=" + str(process.returncode), flush=True)
                process.stdout.close()
                process.stderr.close()
                answers = []
                for number in range(2):
                    environment = dict(os.environ, GIT_AUTHOR_NAME="ambient attacker", GIT_INDEX_FILE="/absent/index")
                    result = subprocess.run([sys.executable, "-B", "-c", REOPEN_CHILD, str(root), str(GIT),
                                             str(self.base / f"{scenario}-reopen-{number}")],
                                            env=environment, capture_output=True, timeout=30)
                    self.assertEqual(0, result.returncode, result.stderr.decode())
                    answers.append(json.loads(result.stdout))
                    print("initialization_reopen=" + result.stdout.decode().strip(), flush=True)
                self.assertEqual(answers[0], answers[1])
                self.assertEqual(self.operator.identity, answers[0]["operator"])
                self.assertEqual(IDENTITY.as_dict(), answers[0]["fixed"])

    def test_preownership_refuses_without_mutation_even_with_partial_intent_stage(self):
        self.interrupt("before-ownership-intent")
        for partial in (False, True):
            if partial:
                path = self.root / (init.INTENT + ".new")
                path.write_bytes(b'{"operation":"initial')
                path.chmod(0o600)
            before = snapshot(self.root)
            with self.assertRaises(SessionError):
                open_session(self.root, git=self.git)
            self.assertEqual(before, snapshot(self.root))
        self.assertFalse((self.root / ".git").exists())

    def test_registered_replacing_tamper_refuses_before_git_and_without_mutation(self):
        class NoCalls(GitRunner):
            def run(self, *args, **kwargs):
                raise AssertionError("tampered initialization reached native Git")
        for variant in ("seed-byte", "missing-seed-file", "missing-index", "unknown-directory", "both-present"):
            with self.subTest(variant=variant):
                self.root = self.base / variant
                self.interrupt("after-replacing")
                recovery = next((self.root / ".vise-host/recovery").iterdir())
                if variant == "seed-byte":
                    (recovery / "git-seed/HEAD").write_bytes(b"tampered\n")
                elif variant == "missing-seed-file":
                    (recovery / "git-seed/info/exclude").unlink()
                elif variant == "missing-index":
                    (recovery / "index").unlink()
                elif variant == "unknown-directory":
                    (recovery / "unregistered").mkdir(mode=0o700)
                else:
                    (self.root / ".git").mkdir(mode=0o700)
                before = snapshot(self.root)
                with self.assertRaises(SessionError):
                    open_session(self.root, git=NoCalls(GIT, self.base / (variant + "-guard")))
                self.assertEqual(before, snapshot(self.root))

    def test_native_ref_transaction_debris_is_preserved_not_reset(self):
        self.interrupt("after-git-directory-publication")
        path = self.root / ".git/refs/heads/vise-host.lock"
        path.write_bytes(b"partial native ref")
        path.chmod(0o600)
        before = snapshot(self.root)
        with self.assertRaises(SessionError):
            open_session(self.root, git=self.git)
        self.assertEqual(before, snapshot(self.root))

    def test_initial_capsule_retains_original_o_after_later_c_and_o(self):
        self.start()
        session = open_session(self.root, git=self.git)
        original_capsule = next((self.root / init.CAPSULES).iterdir()).read_bytes()
        materialize_candidate(session, build_bundle((SourceEntry("candidate", b"next"),)))
        replacement = build_operator((*self.operator.files, SourceEntry("next-spec", b"operator")), generation=1)
        advance_operator(session, expected_identity=self.operator.identity, expected_generation=0, replacement=replacement)
        reopened = open_session(self.root, git=self.git)
        self.assertEqual(replacement, reopened.operator)
        self.assertEqual(original_capsule, next((self.root / init.CAPSULES).iterdir()).read_bytes())

    def test_capsule_ceiling_is_separate_from_ordinary_file_bound(self):
        root = self.base / "bounded"
        root.mkdir(mode=0o700)
        with OwnedDirectory(root, limits=BundleLimits(max_file_bytes=2)) as owned:
            owned.mkdirs(init.CAPSULES)
            raw = b"abc"
            path = init.CAPSULES + "/" + hashlib.sha256(raw).hexdigest()
            with patch.object(init, "LIMITS", replace(init.LIMITS, max_encoded_bytes=2)):
                with self.assertRaises(SessionError):
                    init._write_capsule(owned, path, raw)
            self.assertFalse((root / path).exists())
            with patch.object(init, "LIMITS", replace(init.LIMITS, max_encoded_bytes=3)):
                init._write_capsule(owned, path, raw)
            self.assertEqual(raw, owned.read_file(path, max_bytes=3).data)
            with self.assertRaises(StorageError):
                owned.write_new("ordinary", raw)


if __name__ == "__main__":
    unittest.main()
