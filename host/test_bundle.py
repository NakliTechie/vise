import json
import unittest

from host.bundle import BundleError, BundleLimits, OperatorInventory, SourceEntry, build_bundle, decode_bundle


class BundleTests(unittest.TestCase):
    def test_canonical_round_trip_preserves_bytes_modes_and_order(self):
        entries = [SourceEntry("z/empty", b""), SourceEntry("a/bin", b"\x00\xff", True)]
        first = build_bundle(entries)
        second = build_bundle(reversed(entries))
        self.assertEqual(first.encoded, second.encoded)
        self.assertEqual(first.identity, second.identity)
        self.assertEqual(first, decode_bundle(first.encoded))
        self.assertEqual([entry.path for entry in first.entries], ["a/bin", "z/empty"])

    def test_path_mode_and_byte_changes_change_identity(self):
        base = build_bundle([SourceEntry("a", b"x")]).identity
        for changed in ([SourceEntry("b", b"x")], [SourceEntry("a", b"y")], [SourceEntry("a", b"x", True)]):
            self.assertNotEqual(base, build_bundle(changed).identity)

    def test_rejects_each_invalid_path_category(self):
        for path in ("", ".", "/absolute", "..", "../x", "a/../../x", "a//b", "a/./b", "a\\b", "a\nb"):
            with self.subTest(path=path), self.assertRaises(BundleError):
                build_bundle([SourceEntry(path, b"")])

    def test_rejects_duplicate_prefix_and_casefold_collisions(self):
        for paths in (("a", "a"), ("a", "a/b"), ("a", "a-else", "a/b"), ("A", "a"), ("Dir", "dir/B")):
            with self.subTest(paths=paths), self.assertRaises(BundleError):
                build_bundle(SourceEntry(path, b"") for path in paths)

    def test_paths_are_nfc_and_lone_surrogates_are_contract_errors(self):
        self.assertEqual(build_bundle([SourceEntry("é/file", b"")]).entries[0].path, "é/file")
        for path in ("e\N{COMBINING ACUTE ACCENT}/file", "bad\ud800path"):
            with self.subTest(path=path), self.assertRaises(BundleError):
                build_bundle([SourceEntry(path, b"")])

    def test_rejects_protected_and_operator_collisions(self):
        operator = OperatorInventory.build(("vise.toml", "spec/expected"))
        for path in (".git/config", ".VISE/blobs/x", ".vise-host/receipt", "VISE.TOML", "spec", "spec/expected/x"):
            with self.subTest(path=path), self.assertRaises(BundleError):
                build_bundle([SourceEntry(path, b"")], operator=operator)
        self.assertEqual(build_bundle([SourceEntry("src/main", b"")], operator=operator).entries[0].path, "src/main")
        protected = OperatorInventory.build((".git/config", ".vise/blobs/x", ".vise-host/receipt"))
        self.assertEqual(protected.paths, (".git/config", ".vise/blobs/x", ".vise-host/receipt"))
        with self.assertRaises(BundleError):
            build_bundle([SourceEntry(".vise/blobs/y", b"")], operator=protected)
        with self.assertRaises(BundleError):
            build_bundle([SourceEntry("safe", b"")], operator=OperatorInventory(("../bad",)))

    def test_rejects_reserved_namespace_components_at_any_depth(self):
        for path in ("src/.git/config", "src/.VISE/x", "src/.vise-host/receipt"):
            with self.subTest(path=path), self.assertRaises(BundleError):
                build_bundle([SourceEntry(path, b"")])
        paths = ("host/src/main.py", "src/git/config", "src/vise-host/receipt")
        self.assertEqual(tuple(entry.path for entry in build_bundle(SourceEntry(path, b"") for path in paths).entries), paths)

    def test_rejects_nonregular_declarations(self):
        for kind in ("symlink", "special"):
            with self.assertRaises(BundleError):
                build_bundle([SourceEntry("out", b"", kind=kind)])

    def test_bounds_are_explicit_and_exact(self):
        limits = BundleLimits(max_files=2, max_file_bytes=2, max_total_bytes=3, max_path_bytes=4, max_encoded_bytes=1_000)
        self.assertEqual(len(build_bundle([SourceEntry("a", b"xx")], limits=limits).entries), 1)
        for entries in (
            [SourceEntry("a", b""), SourceEntry("b", b""), SourceEntry("c", b"")],
            [SourceEntry("a", b"xxx")],
            [SourceEntry("a", b"xx"), SourceEntry("b", b"xx")],
            [SourceEntry("abcde", b"")],
        ):
            with self.assertRaises(BundleError):
                build_bundle(entries, limits=limits)
        with self.assertRaises(BundleError):
            build_bundle([SourceEntry("a", b"")], limits=BundleLimits(max_encoded_bytes=1))
        with self.assertRaises(BundleError):
            OperatorInventory.build(("a", "b", "c"), limits=limits)

    def test_limits_require_exact_positive_integers(self):
        for value in (True, 0, -1, 1.0, float("nan")):
            with self.subTest(value=value), self.assertRaises(BundleError):
                BundleLimits(max_files=value)

    def test_count_limits_consume_at_most_one_excess_value(self):
        def too_many_then_bomb():
            yield SourceEntry("a", b"")
            yield SourceEntry("b", b"")
            raise RuntimeError("consumed beyond max_files + 1")

        def too_many_paths_then_bomb():
            yield "a"
            yield "b"
            raise RuntimeError("consumed beyond max_files + 1")

        limits = BundleLimits(max_files=1)
        with self.assertRaises(BundleError):
            build_bundle(too_many_then_bomb(), limits=limits)
        with self.assertRaises(BundleError):
            OperatorInventory.build(too_many_paths_then_bomb(), limits=limits)

    def test_decode_is_strict(self):
        canonical = build_bundle([SourceEntry("a", b"x")]).encoded
        bad_values = [
            b"{}", b'{"version":1,"files":[],"extra":0}\n',
            b'{"files":[{"path":"a","data":"@@","executable":false}],"version":1}\n',
            b'{"files":[{"path":"a","path":"b","data":"eA==","executable":false}],"version":1}\n',
            canonical.replace(b'"executable":false', b'"executable":0'),
            json.dumps(json.loads(canonical), indent=2).encode(),
        ]
        for value in bad_values:
            with self.subTest(value=value), self.assertRaises(BundleError):
                decode_bundle(value)

    def test_decode_translates_excessive_json_nesting(self):
        with self.assertRaises(BundleError):
            decode_bundle(b"[" * 10_000 + b"]" * 10_000)

    def test_decode_translates_excessive_json_integer(self):
        encoded = b'{"version":' + b"1" * 5_000 + b',"files":[]}'
        with self.assertRaises(BundleError):
            decode_bundle(encoded)


if __name__ == "__main__":
    unittest.main()
