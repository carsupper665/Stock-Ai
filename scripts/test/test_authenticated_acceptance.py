import unittest
from scripts.release.verify_authenticated import AcceptanceError, session_request, verify, verify_tool_run


class AuthenticatedAcceptanceTest(unittest.TestCase):
    def test_completion_text_and_zero_tool_calls_are_not_acceptance(self):
        with self.assertRaisesRegex(AcceptanceError, "zero executed tools"):
            verify_tool_run({"status": "completed", "tool_call_count": 0, "output": "Unable to execute tools."})
        with self.assertRaisesRegex(AcceptanceError, "successful get_account"):
            verify_tool_run({"status": "completed", "tool_call_count": 1, "progress": [
                {"type": "tool_call", "tool_name": "get_account", "result": {"ok": False}}]})

    def test_backend_401_stops_before_creating_probe_resources(self):
        calls = []
        def request(method, path, payload=None):
            calls.append((method, path))
            if path == "/v1/me":
                raise AcceptanceError("HTTP 401")
            return {}
        with self.assertRaisesRegex(AcceptanceError, "401"):
            verify(request, "codex")
        self.assertEqual(calls, [("GET", "/oauth2/me"), ("GET", "/v1/me")])

    def test_agent_auth_failure_still_removes_probe_account(self):
        calls = []
        def request(method, path, payload=None):
            calls.append((method, path))
            if method == "POST" and path == "/v1/accounts":
                return {"id": "probe-account"}
            if method == "POST" and path == "/agent/api/v1/sessions":
                raise AcceptanceError("Backend rejected its configured account lookup credential")
            return {}
        with self.assertRaisesRegex(AcceptanceError, "credential"):
            verify(request, "codex")
        self.assertEqual(calls[-1], ("DELETE", "/v1/accounts/probe-account"))

    def test_success_requires_persisted_tool_result_and_preserves_login(self):
        calls = []
        def request(method, path, payload=None):
            calls.append((method, path))
            if method == "POST" and path == "/v1/accounts": return {"id": "account"}
            if method == "POST" and path == "/agent/api/v1/sessions": return {"id": "session"}
            if path.endswith("/runs"):
                return {"runs": [{"status": "completed", "tool_call_count": 1, "progress": [
                    {"type": "tool_call", "tool_name": "get_account", "result": {"ok": True}}]}]}
            return {}
        report = verify(request, "deployed-codex")
        self.assertEqual(report["status"], "passed")
        self.assertTrue(report["tool_execution_verified"])
        self.assertEqual(calls.count(("GET", "/v1/me")), 2)
        self.assertEqual(calls[-1], ("DELETE", "/v1/accounts/account"))

    def test_http_and_credential_bearing_origins_are_rejected(self):
        for url in ("http://app.example", "https://user:secret@app.example", "https://app.example/path"):
            with self.assertRaisesRegex(AcceptanceError, "HTTPS origin"):
                session_request(url, "unused")


if __name__ == "__main__":
    unittest.main()
