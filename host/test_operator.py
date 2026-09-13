import hashlib
import json
import unittest
from dataclasses import replace

from host.bundle import BundleError, BundleLimits, SourceBundle, SourceEntry, build_bundle
from host.operator import OperatorGeneration, build_operator, decode_operator, validate_candidate


class OperatorGenerationTests(unittest.TestCase):
    def test_exact_golden_encoding_identity_and_round_trip(self):
        result = build_operator(
            [SourceEntry("z/empty", b""), SourceEntry(".vise/blobs/abc", b"\x00\xff", True)],
            generation=2,
        )
        expected = (
            b'{"files":[{"data":"AP8=","executable":true,"path":".vise/blobs/abc"},'
            b'{"data":"","executable":false,"path":"z/empty"}],'
            b'"generation":2,"kind":"operator","version":1}\n'
        )
        self.assertEqual(result.encoded, expected)
        self.assertEqual(result.identity, "sha256:" + hashlib.sha256(expected).hexdigest())
        self.assertEqual(decode_operator(expected), result)

    def test_order_is_canonical_and_each_identity_input_matters(self):
        entries = [SourceEntry("b", b"y"), SourceEntry("a", b"x")]
        base = build_operator(entries, generation=0)
        self.assertEqual(base, build_operator(reversed(entries), generation=0))
        changes = (
            build_operator([SourceEntry("c", b"x"), SourceEntry("b", b"y")], generation=0),
            build_operator([SourceEntry("a", b"z"), SourceEntry("b", b"y")], generation=0),
            build_operator([SourceEntry("a", b"x", True), SourceEntry("b", b"y")], generation=0),
            build_operator(entries, generation=1),
        )
        for changed in changes:
            self.assertNotEqual(base.identity, changed.identity)

    def test_generation_is_an_exact_nonnegative_integer(self):
        for generation in (-1, True, 1.0, float("nan"), "1"):
            with self.subTest(generation=generation), self.assertRaises(BundleError):
                build_operator([], generation=generation)

    def test_operator_paths_allow_policy_and_blobs_but_refuse_control_and_runtime(self):
        allowed = ("vise.toml", "spec/expected", "deps/input", ".gitignore", "src/.gitattributes", ".vise/blobs/abc")
        self.assertEqual(len(build_operator((SourceEntry(path, b"") for path in allowed), generation=0).files), 6)
        forbidden = (
            ".git/config", "src/.GIT/config", ".vise-host/session.json", "src/.VISE-HOST/x",
            ".vise/journal.jsonl", ".VISE/run.lock", ".vise/tmp", ".vise/tmp/x",
            ".vise", ".VISE",
        )
        for path in forbidden:
            with self.subTest(path=path), self.assertRaises(BundleError):
                build_operator([SourceEntry(path, b"")], generation=0)

    def test_operator_reuses_path_alias_and_regular_file_rules(self):
        for paths in (("../x",), ("a\\b",), ("e\N{COMBINING ACUTE ACCENT}",), ("A", "a"), ("a", "a-else", "a/b")):
            with self.subTest(paths=paths), self.assertRaises(BundleError):
                build_operator((SourceEntry(path, b"") for path in paths), generation=0)
        for kind in ("symlink", "special"):
            with self.assertRaises(BundleError):
                build_operator([SourceEntry("a", b"", kind=kind)], generation=0)

    def test_bounds_and_generator_consumption_are_finite(self):
        limits = BundleLimits(max_files=1, max_file_bytes=1, max_total_bytes=1, max_path_bytes=1, max_encoded_bytes=200)

        def too_many_then_bomb():
            yield SourceEntry("a", b"")
            yield SourceEntry("b", b"")
            raise RuntimeError("consumed too far")

        with self.assertRaises(BundleError):
            build_operator(too_many_then_bomb(), generation=0, limits=limits)
        for entries in ([SourceEntry("a", b"xx")], [SourceEntry("ab", b"")]):
            with self.assertRaises(BundleError):
                build_operator(entries, generation=0, limits=limits)
        with self.assertRaises(BundleError):
            build_operator([SourceEntry("a", b"")], generation=0, limits=BundleLimits(max_encoded_bytes=1))

    def test_decode_is_strict_and_bounded(self):
        canonical = build_operator([SourceEntry("a", b"x")], generation=0).encoded
        bad = (
            b"{}", canonical.replace(b'"kind":"operator"', b'"kind":"candidate"'),
            canonical.replace(b'"generation":0', b'"generation":true'),
            canonical.replace(b'"data":"eA=="', b'"data":"@@"'),
            canonical.replace(b'"path":"a"', b'"path":"a","path":"b"'),
            json.dumps(json.loads(canonical), indent=2).encode(),
            b"[" * 10_000 + b"]" * 10_000,
            b'{"files":[],"generation":' + b"1" * 5_000 + b',"kind":"operator","version":1}',
        )
        for encoded in bad:
            with self.subTest(encoded=encoded[:80]), self.assertRaises(BundleError):
                decode_operator(encoded)

    def test_validate_candidate_rebuilds_values_and_checks_collisions(self):
        operator = build_operator([SourceEntry("spec/expected", b"ok"), SourceEntry(".gitignore", b".vise/tmp/\n")], generation=3)
        candidate = build_bundle([SourceEntry("src/main", b"x")])
        self.assertEqual(validate_candidate(candidate, operator), candidate)
        forged_candidate = SourceBundle(candidate.entries, candidate.encoded, "sha256:forged")
        forged_operator = OperatorGeneration(operator.generation, operator.files, operator.encoded, "sha256:forged")
        for supplied_candidate, supplied_operator in ((forged_candidate, operator), (candidate, forged_operator)):
            with self.assertRaises(BundleError):
                validate_candidate(supplied_candidate, supplied_operator)
        for path in ("spec", "spec/expected/x", ".GITIGNORE", "src/.gitattributes", "src/.vise/x"):
            with self.subTest(path=path), self.assertRaises(BundleError):
                validate_candidate(build_bundle([SourceEntry(path, b"")]), operator)

    def test_forged_equal_values_do_not_bypass_exact_types(self):
        candidate = build_bundle([SourceEntry("src/main", b"x")])
        operator = build_operator([SourceEntry("spec", b"ok")], generation=1)
        for forged in (
            replace(operator, generation=True), replace(operator, generation=1.0),
            replace(operator, files=(SourceEntry("spec", bytearray(b"ok")),)),
            replace(operator, files=(SourceEntry("spec", b"ok", 0),)),
            replace(operator, files=list(operator.files)),
        ):
            with self.subTest(operator=forged), self.assertRaises(BundleError):
                validate_candidate(candidate, forged)
        for forged in (
            replace(candidate, entries=(SourceEntry("src/main", b"x", 0),)),
            replace(candidate, entries=(SourceEntry("src/main", bytearray(b"x")),)),
            replace(candidate, entries=list(candidate.entries)),
        ):
            with self.subTest(candidate=forged), self.assertRaises(BundleError):
                validate_candidate(forged, operator)

    def test_forged_noncanonical_entry_order_refuses(self):
        candidate = build_bundle([SourceEntry("a", b"a"), SourceEntry("b", b"b")])
        operator = build_operator([SourceEntry("x", b"x"), SourceEntry("y", b"y")], generation=0)
        with self.assertRaises(BundleError):
            validate_candidate(replace(candidate, entries=tuple(reversed(candidate.entries))), operator)
        with self.assertRaises(BundleError):
            validate_candidate(candidate, replace(operator, files=tuple(reversed(operator.files))))


if __name__ == "__main__":
    unittest.main()
