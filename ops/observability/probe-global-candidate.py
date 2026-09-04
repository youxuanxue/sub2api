#!/usr/bin/env python3
"""Verify the public contract of the TokenKey global candidate homepage."""
from __future__ import annotations

import argparse
import http.client
import json
import ssl
import sys
from dataclasses import asdict, dataclass
from urllib.parse import urlsplit


@dataclass
class Result:
    check: str
    status: str
    detail: str


class Probe:
    def __init__(self, base_url: str, product_url: str, phase: str, timeout: float) -> None:
        self.base = self._origin(base_url)
        self.product = self._origin(product_url)
        self.phase = phase
        self.timeout = timeout
        self.results: list[Result] = []

    @staticmethod
    def _origin(raw: str):
        parsed = urlsplit(raw)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname:
            raise ValueError(f"invalid origin: {raw!r}")
        if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
            raise ValueError(f"origin must not include path, query, or fragment: {raw!r}")
        return parsed

    def _request(self, path: str, *, crawler: bool = False) -> tuple[int, dict[str, str], bytes]:
        port = self.base.port or (443 if self.base.scheme == "https" else 80)
        if self.base.scheme == "https":
            connection = http.client.HTTPSConnection(
                self.base.hostname,
                port,
                timeout=self.timeout,
                context=ssl.create_default_context(),
            )
        else:
            connection = http.client.HTTPConnection(self.base.hostname, port, timeout=self.timeout)
        headers = {"User-Agent": "Googlebot" if crawler else "TokenKey-global-candidate-probe/1"}
        try:
            connection.request("GET", path, headers=headers)
            response = connection.getresponse()
            body = response.read()
            return response.status, {key.lower(): value for key, value in response.getheaders()}, body
        finally:
            connection.close()

    def _record(self, check: str, ok: bool, detail: str) -> None:
        self.results.append(Result(check=check, status="ok" if ok else "fail", detail=detail))

    @staticmethod
    def _homepage_contract(status: int, headers: dict[str, str], body: bytes) -> bool:
        text = body.decode("utf-8", errors="replace")
        return (
            status == 200
            and "<h1>China's leading AI models. One API.</h1>" in text
            and '<link rel="canonical" href="https://global.tokenkey.dev/">' in text
            and "noindex" not in headers.get("x-robots-tag", "").lower()
            and "noindex" not in text.lower()
        )

    def run(self) -> list[Result]:
        redirect_code = 302 if self.phase == "candidate" else 301
        try:
            for check, path in (("homepage", "/"), ("home_alias", "/home")):
                status, headers, body = self._request(path, crawler=True)
                homepage_ok = self._homepage_contract(status, headers, body)
                self._record(check, homepage_ok, f"http={status} crawler_contract={homepage_ok}")

            status, _, body = self._request("/setup/status")
            try:
                setup = json.loads(body)
            except json.JSONDecodeError:
                setup = {}
            if not isinstance(setup, dict):
                setup = {}
            setup_data = setup.get("data")
            if not isinstance(setup_data, dict):
                setup_data = {}
            setup_ok = status == 200 and setup.get("code") == 0 and setup_data.get("needs_setup") is False
            self._record("setup_status", setup_ok, f"http={status} ready={setup_ok}")

            status, _, body = self._request("/api/v1/settings/public")
            try:
                envelope = json.loads(body)
            except json.JSONDecodeError:
                envelope = {}
            if not isinstance(envelope, dict):
                envelope = {}
            settings = envelope.get("data")
            if not isinstance(settings, dict):
                settings = {}
            expected = {
                "registration_enabled": True,
                "pricing_catalog_public": True,
                "signup_bonus_enabled": True,
            }
            mismatches = {key: settings.get(key) for key, value in expected.items() if settings.get(key) != value}
            signup_bonus = settings.get("signup_bonus_balance_usd")
            if (
                isinstance(signup_bonus, bool)
                or not isinstance(signup_bonus, (int, float))
                or signup_bonus <= 0
            ):
                mismatches["signup_bonus_balance_usd"] = signup_bonus
            settings_ok = status == 200 and envelope.get("code") == 0 and not mismatches
            self._record(
                "public_settings",
                settings_ok,
                f"http={status} api_code={envelope.get('code')!r} "
                f"mismatches={json.dumps(mismatches, sort_keys=True)}",
            )

            for check, path in (
                ("login_redirect", "/login?next=%2Fconsole"),
                ("register_redirect", "/register"),
            ):
                status, headers, _ = self._request(path)
                expected_location = f"{self.product.scheme}://{self.product.netloc}{path}"
                location = headers.get("location", "")
                self._record(
                    check,
                    status == redirect_code and location == expected_location,
                    f"http={status} location={location!r}",
                )
        except (OSError, ssl.SSLError, http.client.HTTPException, ValueError) as exc:
            self._record("transport", False, f"{type(exc).__name__}: {exc}")
        return self.results


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="https://global.tokenkey.dev")
    parser.add_argument("--product-url", default="https://tokenkey.dev")
    parser.add_argument("--phase", choices=("candidate", "live"), default="candidate")
    parser.add_argument("--timeout", type=float, default=15)
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    try:
        results = Probe(args.base_url, args.product_url, args.phase, args.timeout).run()
    except ValueError as exc:
        print(json.dumps({"summary": "global_candidate", "status": "fail", "error": str(exc)}, sort_keys=True))
        return 2
    for result in results:
        print(json.dumps(asdict(result), sort_keys=True))
    failures = [result.check for result in results if result.status != "ok"]
    print(json.dumps({
        "summary": "global_candidate",
        "status": "ok" if not failures else "fail",
        "ok": len(results) - len(failures),
        "total": len(results),
        "failures": failures,
    }, sort_keys=True))
    return 0 if not failures else 4


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
