"""services_in_use and the names-only derived object the worker writes at publish. Synthetic data only."""
import json, unittest
from unittest import mock

from worker_fakes import *  # noqa: F401,F403 (fakes, constants, Base; also puts worker/ on sys.path)
from worker_fakes import W  # noqa: E402,F401

KEY = "accounts/acct-a/derived/services-in-use.json"


class TestServicesInUse(unittest.TestCase):
    def test_sums_entities_and_orders_by_total(self):
        rows = [{"entity": "a", "service": "Amazon S3", "m6": 10}, {"entity": "a", "service": "Amazon EC2", "m6": 5},
                {"entity": "b", "service": "Amazon EC2", "m6": 20.5}, {"entity": "b", "service": "Amazon S3", "m6": 1}]
        self.assertEqual(W.services_in_use(rows), ["Amazon EC2", "Amazon S3"])

    def test_names_are_stripped_and_merged(self):
        rows = [{"service": " Amazon EC2 ", "m6": 3}, {"service": "Amazon EC2", "m6": 4}, {"service": "Amazon S3", "m6": 6}]
        self.assertEqual(W.services_in_use(rows), ["Amazon EC2", "Amazon S3"])

    def test_ties_are_ordered_by_name(self):
        rows = [{"service": n, "m6": 1} for n in ("Zeta", "Alpha", "Mid")]
        self.assertEqual(W.services_in_use(rows), ["Alpha", "Mid", "Zeta"])

    def test_non_positive_totals_and_bad_names_dropped(self):
        rows = [{"service": "Zero", "m6": 0}, {"service": "Neg", "m6": -4}, {"service": "", "m6": 5},
                {"service": "   ", "m6": 5}, {"service": "x" * 121, "m6": 5}, {"service": "x" * 120, "m6": 1},
                {"service": "Net zero", "m6": 3}, {"service": "Net zero", "m6": -3}]
        self.assertEqual(W.services_in_use(rows), ["x" * 120])

    def test_malformed_rows_are_skipped_not_raised(self):
        rows = [None, "str", 7, [], {}, {"service": None, "m6": 1}, {"service": 5, "m6": 1}, {"service": "NoM6"},
                {"service": "Bad", "m6": "12"}, {"service": "Nan", "m6": float("nan")}, {"service": "Inf", "m6": float("inf")},
                {"service": "Bool", "m6": True}, {"service": "Good", "m6": 2}]
        self.assertEqual(W.services_in_use(rows), ["Good"])
        self.assertEqual(W.services_in_use(None), [])
        self.assertEqual(W.services_in_use([]), [])

    def test_truncates_to_limit(self):
        rows = [{"service": "S%03d" % i, "m6": 1000 - i} for i in range(45)]
        got = W.services_in_use(rows)
        self.assertEqual(len(got), 30)
        self.assertEqual(got[0], "S000")
        self.assertEqual(got[-1], "S029")
        self.assertEqual(len(W.services_in_use(rows, limit=5)), 5)

    def test_output_is_names_only(self):
        got = W.services_in_use([{"service": "Amazon EC2", "m6": 9}])
        self.assertTrue(all(isinstance(x, str) for x in got))


class TestDerivedObject(Base):
    ROWS = [{"entity": A, "service": "Amazon EC2", "m6": 500.25}, {"entity": A, "service": "Amazon Bedrock", "m6": 900},
            {"entity": A, "service": "Amazon S3", "m6": 0}, {"entity": A, "service": "Amazon EBS", "m6": 3}]

    def publish(self, runner, src="cot"):
        self.job(src=src)
        ddb = self.ddb(src=src)
        self.assertEqual(self.run_main(ddb, runner), 0)
        return ddb

    def test_cot_publish_writes_names_only_object(self):
        ddb = self.publish(Runner(services_rows=self.ROWS))
        obj = json.loads(self.store[KEY])
        self.assertEqual(sorted(obj), ["generatedAt", "services", "source", "version", "versionId"])
        self.assertEqual((obj["version"], obj["source"], obj["versionId"]), (1, "cot", VID))
        self.assertEqual(obj["services"], ["Amazon Bedrock", "Amazon EC2", "Amazon EBS"])
        self.assertRegex(obj["generatedAt"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")
        raw = self.store[KEY].decode("utf-8")
        for amount in ("500", "900", "m6", "entity"):
            self.assertNotIn(amount, raw)
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertEqual(self.data_s3.calls.count(("put", KEY)), 1)

    def test_oversized_services_file_skips_the_object_and_logs_class_only(self):
        lines = []
        real_log = W.log
        with mock.patch.object(W, "SERVICES_MAX_BYTES", 20), \
                mock.patch.object(W, "log", side_effect=lambda ev, **kw: (lines.append((ev, kw)), real_log(ev, **kw))[1]):
            ddb = self.publish(Runner(services_rows=self.ROWS))
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertNotIn(KEY, self.store)
        self.assertEqual([kw for ev, kw in lines if ev == "derived_failed"],
                         [{"accountId": A, "cls": "ServicesTooLarge"}])

    def test_publish_without_rows_leaves_the_previous_object(self):
        self.store[KEY] = b'{"old":1}'
        self.publish(Runner())
        self.assertEqual(self.store[KEY], b'{"old":1}')

    def test_cur_source_is_recorded(self):
        self.publish(Runner(services_rows=self.ROWS), src="cur")
        self.assertEqual(json.loads(self.store[KEY])["source"], "cur")

    def test_no_services_dataset_writes_nothing(self):
        self.publish(Runner())
        self.assertNotIn(KEY, self.store)

    def test_no_usable_rows_writes_nothing(self):
        self.publish(Runner(services_rows=[{"service": "Z", "m6": 0}, None]))
        self.assertNotIn(KEY, self.store)

    def test_write_failure_is_logged_without_names_or_amounts_and_publish_succeeds(self):
        orig = self.data_s3.put_object

        def put(Bucket, Key, Body, **kw):
            if Key == KEY:
                raise FakeClientError("AccessDenied")
            return orig(Bucket=Bucket, Key=Key, Body=Body, **kw)
        self.data_s3.put_object = put
        lines = []
        real_log = W.log
        with mock.patch.object(W, "log", side_effect=lambda ev, **kw: (lines.append((ev, kw)), real_log(ev, **kw))[1]):
            ddb = self.publish(Runner(services_rows=self.ROWS))
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertNotIn(KEY, self.store)
        failed = [kw for ev, kw in lines if ev == "derived_failed"]
        self.assertEqual(len(failed), 1)
        self.assertEqual(failed[0], {"accountId": A, "cls": "FakeClientError"})
