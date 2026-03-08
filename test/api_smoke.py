import argparse
import json
import sys
import urllib.error
import urllib.request


def get_json(url: str) -> tuple[int, dict]:
    req = urllib.request.Request(url, method="GET")
    with urllib.request.urlopen(req, timeout=5) as resp:
        body = resp.read().decode("utf-8")
        return resp.status, json.loads(body)


def main() -> int:
    parser = argparse.ArgumentParser(description="Simple API smoke checks.")
    parser.add_argument(
        "--base-url",
        default="http://127.0.0.1:7794",
        help="Base URL for the running API server.",
    )
    args = parser.parse_args()

    checks = (
        ("/api/v1/healthz", lambda body: body.get("message") == "ok"),
        ("/api/v1/time", lambda body: bool(body.get("time"))),
    )

    for path, validator in checks:
        url = args.base_url.rstrip("/") + path
        try:
            status, body = get_json(url)
        except urllib.error.URLError as err:
            print(f"FAIL {path}: request error: {err}", file=sys.stderr)
            return 1
        except json.JSONDecodeError as err:
            print(f"FAIL {path}: invalid json: {err}", file=sys.stderr)
            return 1

        if status != 200:
            print(f"FAIL {path}: expected 200, got {status}", file=sys.stderr)
            return 1

        if not validator(body):
            print(f"FAIL {path}: unexpected body: {body}", file=sys.stderr)
            return 1

        print(f"PASS {path}: {body}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
