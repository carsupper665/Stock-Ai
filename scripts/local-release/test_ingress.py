import json
import subprocess
import unittest
from unittest.mock import patch

from verify_ingress import IngressError, curl_with_retry


class RetryTest(unittest.TestCase):
    def test_rate_limit_retries_keep_real_status_without_relaxing_rate(self):
        observations = []
        responses = [subprocess.CompletedProcess([], 0, "429"), subprocess.CompletedProcess([], 0, "503"),
                     subprocess.CompletedProcess([], 0, "200")]
        with patch("verify_ingress.subprocess.run", side_effect=responses), patch("verify_ingress.time.sleep") as sleep:
            result = curl_with_retry(["curl"], "public login", observations)
        self.assertEqual(result.stdout, "200")
        self.assertEqual([entry["http_status"] for entry in observations], [429, 503, 200])
        self.assertEqual(sleep.call_args_list, [unittest.mock.call(12), unittest.mock.call(12)])

    def test_persistent_failure_is_bounded(self):
        observations = []
        with patch("verify_ingress.subprocess.run", return_value=subprocess.CompletedProcess([], 0, "503")) as run, patch("verify_ingress.time.sleep") as sleep:
            with self.assertRaises(IngressError) as failure:
                curl_with_retry(["curl"], "public login", observations)
        self.assertEqual(run.call_count, 6)
        self.assertEqual(sleep.call_count, 5)
        self.assertEqual(failure.exception.observations, observations)
        self.assertTrue(all(entry["http_status"] == 503 for entry in observations))

    def test_curl_location_json_retries_and_401_is_not_retried(self):
        observations = []
        responses = [subprocess.CompletedProcess([], 0, json.dumps({"http_code": 429})),
                     subprocess.CompletedProcess([], 0, json.dumps({"http_code": 401}))]
        with patch("verify_ingress.subprocess.run", side_effect=responses), patch("verify_ingress.time.sleep") as sleep:
            result = curl_with_retry(["curl"], "curl --location", observations, json_output=True)
        self.assertEqual(json.loads(result.stdout)["http_code"], 401)
        sleep.assert_called_once_with(12)


if __name__ == "__main__":
    unittest.main()
