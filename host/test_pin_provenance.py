"""Finite pin acceptance-history cases; not acceptance or receipt certification."""
import copy
import hashlib
import json
import unittest

from host.pin_provenance import PinProvenance, PinProvenanceError, validate_pin_provenance


OLD = "1" * 40
EVALUATED = "2" * 40
NEXT = "3" * 40


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def probe(accepted=None):
    return {
        "run_hash": digest(b"definition"), "recorded_commit": OLD, "exit": 0,
        "stdout": digest(b"hello\n"), "stderr": digest(b""),
        "deps": {"input": digest(b"input")},
        "pin": {"spec": {"spec/output": digest(b"hello\n")}, "accepted_commit": accepted},
    }


def lock(probes):
    return json.dumps({"v": 1, "fingerprint": {"os": "linux", "arch": "arm64", "stubs": {}},
                       "probes": probes}, indent=2).encode() + b"\n"


class PinProvenanceTests(unittest.TestCase):
    def check(self, prior, observed, **kwargs):
        return validate_pin_provenance(lock(prior), lock(observed), evaluated_commit=EVALUATED, **kwargs)

    def test_mixed_pin_generation_preserves_old_and_names_evaluated_not_next(self):
        prior = {"retained": probe(OLD), "newly": probe(), "unmet": probe()}
        observed = copy.deepcopy(prior)
        observed["newly"]["pin"]["accepted_commit"] = EVALUATED
        self.assertEqual(PinProvenance(("retained",), ("newly",), ("unmet",)), self.check(prior, observed))
        for name in ("retained", "newly"):
            bad = copy.deepcopy(observed)
            bad[name]["pin"]["accepted_commit"] = NEXT
            with self.subTest(name=name), self.assertRaises(PinProvenanceError):
                self.check(prior, bad)

    def test_retained_pin_cannot_be_revoked_or_restamped(self):
        for changed in (None, EVALUATED, NEXT):
            with self.subTest(changed=changed), self.assertRaises(PinProvenanceError):
                self.check({"p": probe(OLD)}, {"p": probe(changed)})

    def test_earlier_sha256_repository_provenance_is_preserved_exactly(self):
        historical = "a" * 64
        self.assertEqual(("p",), self.check({"p": probe(historical)}, {"p": probe(historical)}).retained)

    def test_each_pin_identity_component_requires_new_acceptance_provenance(self):
        mutations = (
            lambda p: p.update(run_hash=digest(b"new definition")),
            lambda p: p["deps"].update(input=digest(b"changed input")),
            lambda p: p["deps"].update(extra=digest(b"new input")),
            lambda p: p["pin"]["spec"].update({"spec/output": digest(b"changed spec")}),
            lambda p: p["pin"]["spec"].update({"spec/new": digest(b"new spec")}),
        )
        for mutate in mutations:
            for accepted in (None, EVALUATED, OLD):
                fresh = probe(accepted)
                mutate(fresh)
                with self.subTest(mutation=mutations.index(mutate), accepted=accepted):
                    if accepted == OLD:
                        with self.assertRaises(PinProvenanceError):
                            self.check({"p": probe(OLD)}, {"p": fresh})
                    else:
                        result = self.check({"p": probe(OLD)}, {"p": fresh})
                        self.assertEqual(("p",), result.unaccepted if accepted is None else result.newly_accepted)

    def test_new_or_renamed_pin_does_not_inherit_another_probes_acceptance(self):
        with self.assertRaises(PinProvenanceError):
            self.check({"old-name": probe(OLD)}, {"new-name": probe(OLD)})
        self.assertEqual(("new-name",), self.check({"old-name": probe(OLD)},
                                                  {"new-name": probe(EVALUATED)}).newly_accepted)
        self.assertEqual(("new-name",), self.check({}, {"new-name": probe(EVALUATED)}).newly_accepted)

    def test_probe_observations_and_recorded_commit_do_not_redefine_pin_identity(self):
        fresh = probe(OLD)
        fresh.update(stdout=digest(b"other"), stderr=digest(b"changed"), recorded_commit=NEXT, exit=1)
        self.assertEqual(("p",), self.check({"p": probe(OLD)}, {"p": fresh}).retained)

    def test_null_absent_empty_dependencies_and_map_order_match_go_identity(self):
        for deps in (None, {}):
            for spec in (None, {}):
                prior, fresh = probe(OLD), probe(OLD)
                prior.pop("deps")
                prior["pin"]["spec"] = None
                fresh["deps"], fresh["pin"]["spec"] = deps, spec
                self.assertEqual(("p",), self.check({"p": prior}, {"p": fresh}).retained)
        prior, fresh = probe(OLD), probe(OLD)
        prior["deps"].update(z=digest(b"z"), a=digest(b"a"))
        fresh["deps"] = dict(reversed(tuple(prior["deps"].items())))
        self.assertEqual(("p",), self.check({"p": prior}, {"p": fresh}).retained)

    def test_unaccepted_pin_may_remain_unaccepted_or_accept_at_evaluated(self):
        self.assertEqual(("p",), self.check({"p": probe()}, {"p": probe()}).unaccepted)
        self.assertEqual(("p",), self.check({"p": probe()}, {"p": probe(EVALUATED)}).newly_accepted)
        with self.assertRaises(PinProvenanceError):
            self.check({"p": probe()}, {"p": probe(OLD)})

    def test_non_pin_and_removed_identities_are_outside_retained_pin_projection(self):
        ordinary = probe()
        ordinary.pop("pin")
        self.assertEqual(PinProvenance((), (), ()), self.check({"p": probe(OLD)}, {"p": ordinary}))
        self.assertEqual(PinProvenance((), (), ()), self.check({"p": probe(OLD)}, {}))
        self.assertEqual(("p",), self.check({"p": ordinary}, {"p": probe(EVALUATED)}).newly_accepted)

    def test_result_order_and_input_bytes_are_unchanged(self):
        probes = {"z": probe(), "a": probe(EVALUATED), "b": probe()}
        encoded = lock(probes)
        before = bytes(encoded)
        result = validate_pin_provenance(lock({}), encoded, evaluated_commit=EVALUATED)
        self.assertEqual(PinProvenance((), ("a",), ("b", "z")), result)
        self.assertEqual(before, encoded)

    def test_evaluated_commit_and_limits_are_exact_types(self):
        for bad in (None, True, 1, "", "a" * 64, "G" * 40, EVALUATED + "\n"):
            with self.subTest(bad=bad), self.assertRaises(PinProvenanceError):
                validate_pin_provenance(lock({}), lock({}), evaluated_commit=bad)
        for name in ("max_lock_bytes", "max_probes"):
            for bad in (True, 1.0, 0, -1, None):
                with self.subTest(name=name, bad=bad), self.assertRaises(PinProvenanceError):
                    self.check({}, {}, **{name: bad})
        raw = lock({})
        self.assertEqual(PinProvenance((), (), ()), self.check({}, {}, max_lock_bytes=len(raw)))
        with self.assertRaises(PinProvenanceError):
            self.check({}, {}, max_lock_bytes=len(raw)-1)
        with self.assertRaises(PinProvenanceError):
            self.check({}, {"a": probe(), "b": probe()}, max_probes=1)

    def test_projection_rejects_duplicate_trailing_nonfinite_and_malformed_json(self):
        for bad in (b'{"v":1,"v":1,"probes":{}}', b'{"v":1,"probes":{"p":{},"p":{}}}',
                    b'{"v":1,"probes":{},"metrics":{"a":NaN}}', b'{}{}', b'{', b'\xff',
                    b'[]', b'{"v":true,"probes":{}}', b'{"v":1.0,"probes":{}}',
                    b'{"v":2,"probes":{}}', b'{"v":1,"probes":null}', b'[' * 2000,
                    bytearray(lock({})), memoryview(lock({})), None):
            with self.subTest(bad=repr(bad)[:100]), self.assertRaises(PinProvenanceError):
                validate_pin_provenance(lock({}), bad, evaluated_commit=EVALUATED)

    def test_invalid_projection_fields_refuse(self):
        mutations = (
            lambda p: p.update(run_hash=True),
            lambda p: p.update(run_hash="sha256:" + "a" * 63),
            lambda p: p.update(deps=[]),
            lambda p: p.update(deps={"x": "invalid"}),
            lambda p: p["pin"].update(spec=[]),
            lambda p: p["pin"].update(spec={"": digest(b"")}),
            lambda p: p["pin"].update(spec={"\ud800": digest(b"")}),
            lambda p: p["pin"].update(accepted_commit=True),
            lambda p: p["pin"].update(accepted_commit="ABC"),
            lambda p: p["pin"].pop("accepted_commit"),
            lambda p: p["pin"].update(future=1),
            lambda p: p.update(pin=False),
        )
        for mutate in mutations:
            invalid = probe()
            mutate(invalid)
            with self.subTest(mutation=mutations.index(mutate)), self.assertRaises(PinProvenanceError):
                self.check({}, {"p": invalid})
            with self.subTest(prior=mutations.index(mutate)), self.assertRaises(PinProvenanceError):
                self.check({"p": invalid}, {})


if __name__ == "__main__":
    unittest.main()
