"""Snapshot-only acceptance checks; no execution, receipt, or filesystem claim."""
import copy
import hashlib
import json
import unittest
from dataclasses import replace

from host.acceptance_generation import AcceptanceGenerationError, classify_generation
from host.bundle import BundleLimits, SourceEntry
from host.operator import build_operator


OLD, K, NEXT = "1" * 40, "2" * 40, "3" * 40


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def lock_value():
    def probe(accepted):
        return {"run_hash": digest(b"run"), "recorded_commit": OLD, "exit": 0,
                "stdout": digest(b"old\n"), "stderr": digest(b""),
                "pin": {"spec": {}, "accepted_commit": accepted}}
    return {"v": 1, "fingerprint": {"os": "linux", "arch": "arm64", "stubs": {}},
            "probes": {"old": probe(OLD), "new": probe(None)}}


def encode(value):
    return json.dumps(value, indent=2).encode() + b"\n"


def operator(raw=None, *, blobs=(b"old\n", b""), extra=(), generation=4):
    return build_operator((SourceEntry("vise.lock", encode(lock_value()) if raw is None else raw),
                           SourceEntry("vise.toml", b"manifest"), SourceEntry("spec", b"spec"),
                           *(SourceEntry(".vise/blobs/" + digest(data)[7:], data) for data in blobs),
                           *extra), generation=generation)


class AcceptanceGenerationTests(unittest.TestCase):
    def check(self, prior, observed, *, preview=None, **kwargs):
        raw = next(e.data for e in observed.files if e.path == "vise.lock")
        return classify_generation(prior, observed, reviewed_lock_sha256=preview or digest(raw),
                                   evaluated_commit=K, **kwargs)

    def next_raw(self):
        value = lock_value()
        value["probes"]["new"]["pin"]["accepted_commit"] = K
        return encode(value)

    def test_prior_wins_even_when_reviewed_preview_is_different(self):
        prior = operator()
        result = self.check(prior, prior, preview=digest(self.next_raw()))
        self.assertEqual("prior", result.generation)
        self.assertEqual(("old",), result.pins.retained)
        self.assertFalse(hasattr(result, "outcome"))
        self.assertFalse(hasattr(result, "receipt"))

    def test_reviewed_next_keeps_both_historical_commit_values(self):
        prior, fresh = operator(), operator(self.next_raw())
        result = self.check(prior, fresh)
        self.assertEqual("reviewed-next", result.generation)
        self.assertEqual(("old",), result.pins.retained)
        self.assertEqual(("new",), result.pins.newly_accepted)
        self.assertEqual(4, fresh.generation)
        self.assertEqual(operator(), prior)
        self.assertEqual(operator(self.next_raw()), fresh)

    def test_changed_lock_without_receipt_never_claims_record_success(self):
        result = self.check(operator(), operator(self.next_raw()))
        self.assertEqual({"generation", "lock_sha256", "pins"}, set(result.__dict__))

    def test_exact_raw_digest_not_whitespace_normalization_or_host_combination(self):
        raw = self.next_raw()
        prior = operator()
        altered = json.dumps(json.loads(raw), separators=(",", ":")).encode()
        for observed, preview in ((operator(altered), digest(raw)),
                                  (operator(raw), digest(raw + b"old\n")),
                                  (operator(raw), digest(b"anything else"))):
            with self.subTest(preview=preview), self.assertRaises(AcceptanceGenerationError):
                self.check(prior, observed, preview=preview)

    def test_unrelated_bytes_path_and_mode_changes_refuse(self):
        original = operator(self.next_raw())
        for change in (lambda e: replace(e, data=b"changed"), lambda e: replace(e, path="other"),
                       lambda e: replace(e, executable=True)):
            entries = tuple(change(e) if e.path == "spec" else e for e in original.files)
            with self.subTest(change=change), self.assertRaises(AcceptanceGenerationError):
                self.check(operator(), build_operator(entries, generation=4))
        with self.assertRaises(AcceptanceGenerationError):
            self.check(operator(), operator(self.next_raw(), extra=(SourceEntry("new-path", b"x"),)))

    def test_referenced_blob_addition_and_obsolete_blob_removal(self):
        value = lock_value()
        for probe in value["probes"].values():
            probe["stdout"] = digest(b"new\n")
        fresh = operator(encode(value), blobs=(b"new\n", b""))
        self.assertEqual("reviewed-next", self.check(operator(), fresh).generation)

    def test_missing_changed_nested_executable_and_misnamed_blobs_refuse(self):
        good = operator(self.next_raw())
        path = ".vise/blobs/" + digest(b"old\n")[7:]
        changes = (lambda e: replace(e, data=b"wrong"), lambda e: replace(e, executable=True),
                   lambda e: replace(e, path=e.path + "/nested"),
                   lambda e: replace(e, path=".vise/blobs/" + "f" * 64))
        for change in changes:
            entries = tuple(change(e) if e.path == path else e for e in good.files)
            with self.subTest(change=change), self.assertRaises(AcceptanceGenerationError):
                self.check(operator(), build_operator(entries, generation=4))
        with self.assertRaises(AcceptanceGenerationError):
            self.check(operator(), operator(self.next_raw(), blobs=(b"",)))

    def test_existing_orphans_can_remain_or_be_pruned_not_added(self):
        prior = operator(blobs=(b"old\n", b"", b"orphan"))
        for blobs in ((b"old\n", b""), (b"old\n", b"", b"orphan")):
            self.assertEqual("reviewed-next", self.check(prior, operator(self.next_raw(), blobs=blobs)).generation)
        with self.assertRaises(AcceptanceGenerationError):
            self.check(operator(), operator(self.next_raw(), blobs=(b"old\n", b"", b"unexplained")))

    def test_identical_lock_with_pruned_orphan_is_not_complete_prior_O(self):
        prior = operator(blobs=(b"old\n", b"", b"orphan"))
        fresh = operator()
        self.assertEqual("reviewed-next", self.check(prior, fresh).generation)
        with self.assertRaises(AcceptanceGenerationError):
            self.check(prior, fresh, preview=digest(self.next_raw()))

    def test_large_hash_only_observations_do_not_require_files(self):
        value = lock_value()
        for probe in value["probes"].values():
            probe.update(stdout_large=True, files={"out": digest(b"large")}, files_large={"out": True})
        prior = operator(encode(value), blobs=(b"",))
        self.assertEqual("prior", self.check(prior, prior).generation)
        bad = copy.deepcopy(value)
        bad["probes"]["new"]["files_large"]["out"] = False
        with self.assertRaises(AcceptanceGenerationError):
            self.check(prior, operator(encode(bad), blobs=(b"",)))

    def test_pin_history_and_new_acceptance_have_separate_wrong_commit_cases(self):
        for name in ("old", "new"):
            value = json.loads(self.next_raw())
            value["probes"][name]["pin"]["accepted_commit"] = NEXT
            with self.subTest(name=name), self.assertRaises(AcceptanceGenerationError):
                self.check(operator(), operator(encode(value)))

    def test_counter_changes_forged_values_and_invalid_types_refuse(self):
        prior = operator()
        for bad in (operator(self.next_raw(), generation=5), replace(prior, identity="sha256:" + "a" * 64),
                    replace(prior, generation=True), replace(prior, files=list(prior.files)), None):
            with self.subTest(bad=type(bad)), self.assertRaises(AcceptanceGenerationError):
                classify_generation(prior, bad, reviewed_lock_sha256=digest(self.next_raw()), evaluated_commit=K)
        for bad in (None, True, "", "a" * 64, digest(b"") + "\n"):
            with self.subTest(digest=bad), self.assertRaises(AcceptanceGenerationError):
                classify_generation(prior, prior, reviewed_lock_sha256=bad, evaluated_commit=K)
        with self.assertRaises(AcceptanceGenerationError):
            self.check(prior, prior, limits=None)

    def test_malformed_reference_projection_and_duplicate_json_refuse(self):
        mutations = (
            lambda p: p.update(stdout_large=1), lambda p: p.update(stderr_large=None),
            lambda p: p.update(stdout="bad"), lambda p: p.update(files=[]),
            lambda p: p.update(files_large={"absent": True}),
            lambda p: p.update(files={"a": digest(b"")}, files_large={"a": 1}),
            lambda p: p.update(files={"a": "bad"}),
        )
        for mutate in mutations:
            value = lock_value()
            mutate(value["probes"]["new"])
            with self.subTest(mutate=mutate), self.assertRaises(AcceptanceGenerationError):
                self.check(operator(), operator(encode(value)))
        for raw in (b'{"v":1,"v":1,"probes":{}}', b'{"v":true,"probes":{}}',
                    b'{"v":1,"probes":{},"x":NaN}', b"[]", b"{}{}", b"\xff", b"[" * 2000):
            with self.subTest(raw=raw[:40]), self.assertRaises(AcceptanceGenerationError):
                self.check(operator(), operator(raw))

    def test_read_only_projection_does_not_pretend_to_validate_whole_lock(self):
        value = lock_value()
        value["fingerprint"] = "not a full native schema"
        projected = operator(encode(value))
        self.assertEqual("prior", self.check(projected, projected).generation)

    def test_tight_byte_count_limits_apply_before_classification(self):
        prior = operator()
        for limits in (BundleLimits(max_file_bytes=10), BundleLimits(max_files=1)):
            with self.subTest(limits=limits), self.assertRaises(AcceptanceGenerationError):
                self.check(prior, prior, limits=limits)


if __name__ == "__main__":
    unittest.main()
