#!/usr/bin/env python3
"""Prometheus exporter for ABLESTACK host license API status."""

import json
import logging
import os
import ssl
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, Optional


DEFAULT_SOURCE = "host_api"
DEFAULT_API_URL = "https://127.0.0.1:8080/api/v1/license/isLicenseExpired"
DEFAULT_LISTEN_ADDRESS = "0.0.0.0"
DEFAULT_PORT = 3007
DEFAULT_METRICS_PATH = "/metrics"
DEFAULT_TIMEOUT = 5.0
DEFAULT_LOG_LEVEL = "INFO"


def env(name: str, default: str) -> str:
    value = os.getenv(name)
    if value is None or value == "":
        return default
    return value


def env_float(name: str, default: float) -> float:
    value = os.getenv(name)
    if value is None or value == "":
        return default
    return float(value)


def env_int(name: str, default: int) -> int:
    value = os.getenv(name)
    if value is None or value == "":
        return default
    return int(value)


def normalize_bool(value: Any) -> Optional[bool]:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return bool(value)
    if isinstance(value, str):
        normalized = value.strip().lower()
        if normalized in {"true", "1", "yes", "y", "expired"}:
            return True
        if normalized in {"false", "0", "no", "n", "active", "valid"}:
            return False
    return None


def find_field(payload: Any, field_names: tuple[str, ...]) -> Optional[Any]:
    if isinstance(payload, dict):
        for key in field_names:
            if key in payload and payload[key] not in (None, ""):
                return payload[key]
        for value in payload.values():
            result = find_field(value, field_names)
            if result not in (None, ""):
                return result
    elif isinstance(payload, list):
        for item in payload:
            result = find_field(item, field_names)
            if result not in (None, ""):
                return result
    return None


def parse_datetime_to_timestamp(value: Any) -> Optional[float]:
    if value in (None, ""):
        return None

    if isinstance(value, (int, float)):
        numeric = float(value)
        if numeric > 10_000_000_000:
            return numeric / 1000.0
        return numeric

    if not isinstance(value, str):
        return None

    text = value.strip()
    if not text:
        return None

    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    elif len(text) >= 5 and text[-5] in {"+", "-"} and text[-3] != ":":
        text = f"{text[:-2]}:{text[-2:]}"

    patterns = (
        None,
        "%Y-%m-%d %H:%M:%S",
        "%Y-%m-%d",
        "%Y/%m/%d %H:%M:%S",
        "%Y/%m/%d",
        "%Y.%m.%d %H:%M:%S",
        "%Y.%m.%d",
    )

    for pattern in patterns:
        try:
            if pattern is None:
                parsed = datetime.fromisoformat(text)
            else:
                parsed = datetime.strptime(text, pattern)
            if parsed.tzinfo is None:
                parsed = parsed.replace(tzinfo=timezone.utc)
            return parsed.timestamp()
        except ValueError:
            continue

    return None


def escape_label_value(value: str) -> str:
    return value.replace("\\", "\\\\").replace("\n", "\\n").replace('"', '\\"')


def trim_error_message(value: str, max_length: int = 200) -> str:
    collapsed = " ".join(value.split())
    return collapsed[:max_length]


class LicenseStatusCollector:
    def __init__(self) -> None:
        self.source = env("LICENSE_EXPORTER_SOURCE", DEFAULT_SOURCE).strip().lower()
        self.api_url = env("LICENSE_EXPORTER_API_URL", DEFAULT_API_URL)
        self.timeout = env_float("LICENSE_EXPORTER_TIMEOUT_SECONDS", DEFAULT_TIMEOUT)
        self.instance_name = os.getenv("LICENSE_EXPORTER_INSTANCE", "")
        self.auth_header = os.getenv("LICENSE_EXPORTER_AUTH_HEADER", "")
        self.insecure_skip_verify = env("LICENSE_EXPORTER_INSECURE_SKIP_VERIFY", "true").lower() in {
            "1",
            "true",
            "yes",
            "y",
        }

    def scrape(self) -> Dict[str, Any]:
        if self.source == "host_api":
            return self.scrape_host_api()
        raise RuntimeError(f"unsupported LICENSE_EXPORTER_SOURCE: {self.source}")

    def scrape_host_api(self) -> Dict[str, Any]:
        body, status_code, finished_at, started_at = self.fetch(self.api_url)
        payload = json.loads(body.decode("utf-8"))
        if not isinstance(payload, dict):
            raise RuntimeError("host license API response is not a JSON object")

        expiry_timestamp = parse_datetime_to_timestamp(
            find_field(payload, ("expirydate", "expiryDate", "expired", "expiry_date_text"))
        )
        issued_timestamp = parse_datetime_to_timestamp(
            find_field(payload, ("issueddate", "issuedDate", "issued"))
        )
        expired_flag = normalize_bool(find_field(payload, ("expiry_date", "isLicenseExpired")))
        error_text = str(payload.get("error", "") or "")

        days_until_expiry = None
        if expiry_timestamp is not None:
            days_until_expiry = (expiry_timestamp - finished_at) / 86400.0

        expired = 0
        if expired_flag is not None:
            expired = 1 if expired_flag else 0
        elif days_until_expiry is not None:
            expired = 1 if days_until_expiry < 0 else 0

        has_license = 0 if error_text else 1
        valid = 0 if error_text else 1

        return {
            "source": "host_api",
            "target_url": self.api_url,
            "api_up": 1,
            "status_code": status_code,
            "scrape_duration_seconds": finished_at - started_at,
            "last_scrape_timestamp_seconds": finished_at,
            "expired": expired,
            "has_license": has_license,
            "valid": valid,
            "expiry_timestamp_seconds": expiry_timestamp,
            "issued_timestamp_seconds": issued_timestamp,
            "days_until_expiry": days_until_expiry,
            "error": error_text,
        }

    def fetch(self, request_url: str) -> tuple[bytes, int, float, float]:
        started_at = time.time()
        request = urllib.request.Request(request_url, method="GET")
        request.add_header("Accept", "application/json")
        if self.auth_header:
            if ":" not in self.auth_header:
                raise RuntimeError("LICENSE_EXPORTER_AUTH_HEADER must be 'Header: value'")
            header_name, header_value = self.auth_header.split(":", 1)
            request.add_header(header_name.strip(), header_value.strip())

        context = None
        if request_url.startswith("https://") and self.insecure_skip_verify:
            context = ssl._create_unverified_context()

        try:
            with urllib.request.urlopen(request, timeout=self.timeout, context=context) as response:
                status_code = getattr(response, "status", response.getcode())
                body = response.read()
        except urllib.error.URLError as exc:
            raise RuntimeError(f"failed to reach upstream license API: {exc}") from exc

        finished_at = time.time()
        if status_code < 200 or status_code >= 300:
            raise RuntimeError(f"upstream license API returned HTTP {status_code}")

        return body, status_code, finished_at, started_at


def render_metrics(metrics: Dict[str, Any], instance_name: str) -> bytes:
    labels = [
        f'source="{escape_label_value(metrics["source"])}"',
        f'target_url="{escape_label_value(metrics["target_url"])}"',
    ]
    if instance_name:
        labels.append(f'instance_name="{escape_label_value(instance_name)}"')
    label_block = "{" + ",".join(labels) + "}"

    lines = [
        "# HELP ablestack_license_api_up Whether the upstream license API was reachable.",
        "# TYPE ablestack_license_api_up gauge",
        f"ablestack_license_api_up{label_block} {metrics['api_up']}",
        "# HELP ablestack_license_expired Whether the host license is expired (1=yes, 0=no).",
        "# TYPE ablestack_license_expired gauge",
        f"ablestack_license_expired{label_block} {metrics['expired']}",
        "# HELP ablestack_license_has_license Whether the host has license information (1=yes, 0=no).",
        "# TYPE ablestack_license_has_license gauge",
        f"ablestack_license_has_license{label_block} {metrics['has_license']}",
        "# HELP ablestack_license_valid Whether the upstream license API marks the license as valid.",
        "# TYPE ablestack_license_valid gauge",
        f"ablestack_license_valid{label_block} {metrics['valid']}",
        "# HELP ablestack_license_api_http_status Last HTTP status code returned by the upstream API.",
        "# TYPE ablestack_license_api_http_status gauge",
        f"ablestack_license_api_http_status{label_block} {metrics['status_code']}",
        "# HELP ablestack_license_scrape_duration_seconds Time spent calling the upstream API.",
        "# TYPE ablestack_license_scrape_duration_seconds gauge",
        f"ablestack_license_scrape_duration_seconds{label_block} {metrics['scrape_duration_seconds']:.6f}",
        "# HELP ablestack_license_last_scrape_timestamp_seconds Unix time of the last successful scrape.",
        "# TYPE ablestack_license_last_scrape_timestamp_seconds gauge",
        f"ablestack_license_last_scrape_timestamp_seconds{label_block} {metrics['last_scrape_timestamp_seconds']:.3f}",
    ]

    if metrics["expiry_timestamp_seconds"] is not None:
        lines.extend(
            [
                "# HELP ablestack_license_expiry_timestamp_seconds License expiry date as a Unix timestamp.",
                "# TYPE ablestack_license_expiry_timestamp_seconds gauge",
                f"ablestack_license_expiry_timestamp_seconds{label_block} {metrics['expiry_timestamp_seconds']:.3f}",
            ]
        )

    if metrics["issued_timestamp_seconds"] is not None:
        lines.extend(
            [
                "# HELP ablestack_license_issued_timestamp_seconds License issued date as a Unix timestamp.",
                "# TYPE ablestack_license_issued_timestamp_seconds gauge",
                f"ablestack_license_issued_timestamp_seconds{label_block} {metrics['issued_timestamp_seconds']:.3f}",
            ]
        )

    if metrics["days_until_expiry"] is not None:
        lines.extend(
            [
                "# HELP ablestack_license_days_until_expiry Days remaining until the license expiry date.",
                "# TYPE ablestack_license_days_until_expiry gauge",
                f"ablestack_license_days_until_expiry{label_block} {metrics['days_until_expiry']:.6f}",
            ]
        )

    if metrics["error"]:
        error_block = (
            "{"
            + ",".join(labels + [f'error="{escape_label_value(trim_error_message(metrics["error"]))}"'])
            + "}"
        )
        lines.extend(
            [
                "# HELP ablestack_license_error_info Error marker for upstream license API payload errors.",
                "# TYPE ablestack_license_error_info gauge",
                f"ablestack_license_error_info{error_block} 1",
            ]
        )

    return ("\n".join(lines) + "\n").encode("utf-8")


def render_error_metrics(
    source: str, target_url: str, instance_name: str, error_message: str
) -> bytes:
    labels = [
        f'source="{escape_label_value(source)}"',
        f'target_url="{escape_label_value(target_url)}"',
    ]
    if instance_name:
        labels.append(f'instance_name="{escape_label_value(instance_name)}"')
    label_block = "{" + ",".join(labels) + "}"
    error_block = (
        "{"
        + ",".join(labels + [f'error="{escape_label_value(trim_error_message(error_message))}"'])
        + "}"
    )

    lines = [
        "# HELP ablestack_license_api_up Whether the upstream license API was reachable.",
        "# TYPE ablestack_license_api_up gauge",
        f"ablestack_license_api_up{label_block} 0",
        "# HELP ablestack_license_expired Whether the host license is expired (1=yes, 0=no).",
        "# TYPE ablestack_license_expired gauge",
        f"ablestack_license_expired{label_block} 0",
        "# HELP ablestack_license_error_info Error marker for the last failed scrape.",
        "# TYPE ablestack_license_error_info gauge",
        f"ablestack_license_error_info{error_block} 1",
    ]
    return ("\n".join(lines) + "\n").encode("utf-8")


class MetricsHandler(BaseHTTPRequestHandler):
    collector = LicenseStatusCollector()
    metrics_path = env("LICENSE_EXPORTER_METRICS_PATH", DEFAULT_METRICS_PATH)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/-/healthy":
            self.respond(200, b"ok\n", "text/plain; charset=utf-8")
            return

        if self.path != self.metrics_path:
            self.respond(404, b"not found\n", "text/plain; charset=utf-8")
            return

        try:
            metrics = self.collector.scrape()
            body = render_metrics(metrics, self.collector.instance_name)
            self.respond(200, body, "text/plain; version=0.0.4; charset=utf-8")
        except Exception as exc:  # pylint: disable=broad-except
            logging.exception("license scrape failed")
            body = render_error_metrics(
                self.collector.source, self.collector.api_url, self.collector.instance_name, str(exc)
            )
            self.respond(500, body, "text/plain; version=0.0.4; charset=utf-8")

    def log_message(self, fmt: str, *args: Any) -> None:
        logging.info("%s - %s", self.address_string(), fmt % args)

    def respond(self, status_code: int, body: bytes, content_type: str) -> None:
        self.send_response(status_code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def main() -> int:
    logging.basicConfig(
        level=getattr(
            logging, env("LICENSE_EXPORTER_LOG_LEVEL", DEFAULT_LOG_LEVEL).upper(), logging.INFO
        ),
        format="%(asctime)s %(levelname)s %(message)s",
    )

    listen_address = env("LICENSE_EXPORTER_LISTEN_ADDRESS", DEFAULT_LISTEN_ADDRESS)
    port = env_int("LICENSE_EXPORTER_PORT", DEFAULT_PORT)
    server = ThreadingHTTPServer((listen_address, port), MetricsHandler)

    logging.info(
        "starting license exporter on %s:%s using source=%s",
        listen_address,
        port,
        MetricsHandler.collector.source,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        logging.info("license exporter interrupted, shutting down")
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
