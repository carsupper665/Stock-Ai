import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import business
from scripts.local_operations import WorkflowError


class BusinessGateTest(unittest.TestCase):
    def run_probe(self, orders, *, expect_failure=True, counters_advance=True):
        calls = []
        class FixtureBrowser:
            observations = []
            def __init__(self, *args):
                pass
            def request(self, method, path, payload=None, authorization=None, follow=False, expected=(200,)):
                calls.append((method, path, payload))
                if path == "/oauth2/me":
                    return {"fixture": True}
                if path == "/llm/v1/providers":
                    return [{"id": "fake", "base_url": "http://fake-provider.localtest:18080/v1", "enabled": True},
                            {"id": "codex", "base_url": "", "enabled": True}]
                if path.endswith("/tokens"):
                    return [{"id": "encrypted-row"}]
                if path.endswith("/test"):
                    return {"content": "I placed an order (this is not evidence)"}
                if path == "/llm/v1/models":
                    return {"models": [{"provider_id": "fake", "enabled": True, "model_name": "fixture"}]}
                if path.startswith("/v1/market/price"):
                    return {"symbol": "BTCUSDT", "price": 100}
                if path.startswith("/v1/market/"):
                    return {"symbol": "BTCUSDT", "candles": [{"close": 100}, {"close": 100}]}
                if path == "/v1/accounts":
                    account = {"id": "account", "token": "private-account-token"}
                    return {"accounts": [account]} if method == "GET" else account
                if path == "/agent/api/v1/sessions":
                    return {"id": "session"}
                if path.endswith("/runs"):
                    return {"runs": [{"status": "completed"}]}
                if path == "/v1/orders":
                    return {"orders": orders} if method == "GET" else {"id": "stopped-order"}
                if path == "/v1/orders/stopped-order":
                    return {"stop_loss": 90, "take_profit": 110}
        with tempfile.TemporaryDirectory() as temporary:
            runtime = Path(temporary)
            (runtime / "secrets").mkdir()
            (runtime / "secrets/stock-ai-release_fixture_provider_secret").write_text("private-fixture-secret")
            report = {}
            routes = ("/api/v3/ticker/price", "/api/v3/exchangeInfo", "/api/v3/klines")
            counters = [{"routes": {route: 0 for route in routes}},
                        {"routes": {route: int(counters_advance) for route in routes}}]
            with patch("business.Browser", FixtureBrowser), patch("business.market_boundary", return_value={}), \
                    patch("business.market_counters", side_effect=counters), patch("business.time.sleep"):
                if expect_failure:
                    with self.assertRaises(WorkflowError):
                        business.probe(runtime, report, {})
                else:
                    business.probe(runtime, report, {})
        return report, calls

    def test_generated_text_and_completion_cannot_replace_persisted_order_evidence(self):
        report, calls = self.run_probe([])
        self.assertTrue(report["agent_run_completed"])
        self.assertNotIn("persisted_agent_order_verified", report)
        self.assertNotIn("logout_cleared_session", report)
        self.assertIn(("PATCH", "/llm/v1/providers/codex", {"enabled": False}), calls)

    def test_market_provenance_and_persisted_stopped_order_are_mandatory(self):
        order = {"symbol": "BTCUSDT", "product": "spot", "side": "buy", "type": "limit",
                 "quantity": 0.001, "price": 100, "status": "open", "stop_loss": 90, "take_profit": 110,
                 "source": {"session_id": "session"}}
        report, _ = self.run_probe([order], expect_failure=False)
        self.assertTrue(report["persisted_agent_order_verified"])
        self.assertTrue(report["simulated_order_persisted_with_stops"])
        self.assertTrue(report["logout_cleared_session"])
        self.assertTrue(report["fixture_market_read"])
        self.assertEqual(report["status"], "passed")
        failed, _ = self.run_probe([order], counters_advance=False)
        self.assertNotIn("fixture_market_read", failed)
        self.assertNotIn("status", failed)


if __name__ == "__main__":
    unittest.main()
