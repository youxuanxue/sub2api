#!/usr/bin/env python3
"""Four-state controller for a zero-loss Lightsail-to-EC2 Edge migration."""

from __future__ import annotations

import argparse
import contextlib
import dataclasses
import datetime as dt
import fcntl
import ipaddress
import json
import os
import pathlib
import re
import shlex
import subprocess
import sys
import time
from collections.abc import Callable
from typing import Any, Protocol

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from ops.migration.edge_ec2_remote import write_receipt_atomic


PUBLIC_STATES = {"prepared", "cutting_over", "observing", "stable"}
CUTOVER_DEADLINE_SECONDS = 120
TARGET_ENABLE_HEADROOM_SECONDS = 15
OBSERVATION_SECONDS = 600
OBSERVATION_PROBE_INTERVAL_SECONDS = 30
OBSERVATION_PROBE_TIMEOUT_SECONDS = 20
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
URL_RE = re.compile(r"https?://[^\s'\"<>]+")


class MigrationError(RuntimeError):
    pass


@dataclasses.dataclass(frozen=True)
class Binding:
    edge_id: str
    source_region: str
    source_instance_id: str
    target_region: str
    target_instance_id: str
    target_eip: str
    domain: str
    commit: str
    manifest_digest: str
    source_public_ip: str = ""

    def validate(self) -> None:
        if not re.fullmatch(r"[a-z0-9][a-z0-9-]{1,30}", self.edge_id):
            raise MigrationError("invalid edge_id")
        if not self.source_instance_id.startswith("mi-"):
            raise MigrationError("source must be a Lightsail SSM managed instance")
        if re.fullmatch(r"i-[0-9a-f]{17}", self.target_instance_id) is None:
            raise MigrationError("target must be an EC2 instance id")
        if not self.domain or "." not in self.domain:
            raise MigrationError("invalid Edge domain")
        try:
            target_ip = ipaddress.ip_address(self.target_eip)
            if target_ip.version != 4 or str(target_ip) != self.target_eip:
                raise ValueError
        except ValueError as exc:
            raise MigrationError("target_eip must be a canonical IPv4 address") from exc
        if self.source_public_ip:
            try:
                source_ip = ipaddress.ip_address(self.source_public_ip)
                if source_ip.version != 4 or str(source_ip) != self.source_public_ip:
                    raise ValueError
            except ValueError as exc:
                raise MigrationError("source_public_ip must be a canonical IPv4 address") from exc
        if self.manifest_digest != "auto" and not SHA256_RE.fullmatch(self.manifest_digest):
            raise MigrationError("manifest_digest must be SHA-256")
        if re.fullmatch(r"[0-9a-f]{40}", self.commit) is None:
            raise MigrationError("commit must be a full git SHA")


class RemoteRunner(Protocol):
    def preflight(self, command: str, *, require_reverse: bool = False) -> None: ...

    def run(self, endpoint: str, action: str, **kwargs: object) -> dict[str, Any]: ...


def dns_confirmation_token(binding: Binding, *, rollback: bool = False) -> str:
    value = binding.source_public_ip if rollback else binding.target_eip
    direction = "rollback" if rollback else "cutover"
    return f"confirm-dns-{direction}:{binding.edge_id}:{binding.domain}:{value}"


def _utc(epoch: float) -> str:
    return dt.datetime.fromtimestamp(epoch, tz=dt.UTC).isoformat().replace("+00:00", "Z")


def _redact_urls(value: str) -> str:
    return URL_RE.sub("[REDACTED_URL]", value)


def _initial_state(binding: Binding) -> dict[str, Any]:
    return {
        "state_version": 1,
        "state": "prepared",
        "binding": dataclasses.asdict(binding),
        "rehearsal_seconds": 0,
        "target_accepts_writes_at": None,
        "observing_started_at": None,
        "checkpoints": {},
    }


class MigrationOrchestrator:
    def __init__(
        self,
        state_path: pathlib.Path,
        *,
        runner: RemoteRunner,
        now: Callable[[], float] = time.time,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        self.state_path = pathlib.Path(state_path)
        self.runner = runner
        self.now = now
        self.sleep = sleep

    def _read(self) -> dict[str, Any] | None:
        if not self.state_path.exists():
            return None
        payload = json.loads(self.state_path.read_text(encoding="utf-8"))
        if (
            not isinstance(payload, dict)
            or payload.get("state_version") != 1
            or payload.get("state") not in PUBLIC_STATES
            or not isinstance(payload.get("binding"), dict)
            or not isinstance(payload.get("checkpoints"), dict)
            or "target_accepts_writes_at" not in payload
            or "observing_started_at" not in payload
        ):
            raise MigrationError("invalid migration state file")
        return payload

    def _write(self, state: dict[str, Any]) -> None:
        write_receipt_atomic(self.state_path, state)

    @contextlib.contextmanager
    def _execution_lock(self) -> Any:
        self.state_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        lock_path = self.state_path.with_name(f"{self.state_path.name}.lock")
        descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
        try:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise MigrationError(
                    f"migration execution already running for state file: {self.state_path}"
                ) from exc
            yield
        finally:
            os.close(descriptor)

    @staticmethod
    def _assert_binding(state: dict[str, Any], binding: Binding) -> None:
        if state.get("binding") != dataclasses.asdict(binding):
            raise MigrationError("migration binding mismatch")

    @staticmethod
    def _steps(command: str, state: dict[str, Any] | None) -> list[dict[str, Any]]:
        if command == "prepare":
            return [
                {"endpoint": "source", "action": "prepare-source"},
                {"endpoint": "target", "action": "restore-target"},
            ]
        if command == "cutover":
            return [
                {"endpoint": "source", "action": "freeze-source"},
                {"endpoint": "target", "action": "restore-target"},
                {"endpoint": "target", "action": "enable-target"},
                {"endpoint": "source", "action": "proxy-source"},
            ]
        if command == "observe":
            return [
                {"external": "confirm-dns"},
                {"local": "continuous-600-second-health-window"},
                {"endpoint": "target", "action": "verify-target"},
                {"endpoint": "target", "action": "verify-source-proxy"},
            ]
        if command == "mark-stable":
            return [
                {"local": "require-completed-continuous-observation"},
                {"endpoint": "target", "action": "verify-target"},
                {"endpoint": "target", "action": "verify-source-proxy"},
            ]
        if command == "rollback":
            if state and state.get("target_accepts_writes_at"):
                return [
                    {"endpoint": "target", "action": "freeze-target"},
                    {"endpoint": "source", "action": "restore-source"},
                    {"endpoint": "source", "action": "resume-source"},
                    {"endpoint": "target", "action": "proxy-target"},
                    {"external": "confirm-dns-rollback"},
                ]
            return [
                {"endpoint": "target", "action": "release-target-candidate"},
                {"endpoint": "source", "action": "resume-source"},
            ]
        raise MigrationError(f"unsupported command: {command}")

    def run(
        self,
        command: str,
        binding: Binding,
        *,
        execute: bool,
        confirm_dns: str = "",
        observed_dns_ip: str = "",
    ) -> dict[str, Any]:
        binding.validate()
        if not execute:
            state = self._read()
            if state is not None:
                self._assert_binding(state, binding)
            return {"mode": "plan", "command": command, "steps": self._steps(command, state)}
        with self._execution_lock():
            return self._run_execute(
                command,
                binding,
                confirm_dns=confirm_dns,
                observed_dns_ip=observed_dns_ip,
            )

    def _run_execute(
        self,
        command: str,
        binding: Binding,
        *,
        confirm_dns: str,
        observed_dns_ip: str,
    ) -> dict[str, Any]:
        state = self._read()
        if state is not None:
            self._assert_binding(state, binding)
        if command == "prepare":
            return self._prepare(binding, state)
        if state is None:
            required = "cutting_over" if command == "observe" else "prepared"
            raise MigrationError(f"{command} requires state {required}")
        if command == "cutover":
            return self._cutover(binding, state)
        if command == "observe":
            return self._observe(binding, state, confirm_dns, observed_dns_ip)
        if command == "mark-stable":
            return self._mark_stable(binding, state)
        if command == "rollback":
            return self._rollback(binding, state, confirm_dns, observed_dns_ip)
        raise MigrationError(f"unsupported command: {command}")

    def _prepare(self, binding: Binding, state: dict[str, Any] | None) -> dict[str, Any]:
        if state is not None:
            checkpoints = state.get("checkpoints", {})
            if state["state"] != "prepared" or not checkpoints.get("prepare_complete"):
                raise MigrationError("prepare requires no state or an already completed prepared state")
            if checkpoints.get("rollback_source_ready"):
                raise MigrationError(
                    "prepare refused while target is a retained rollback proxy; "
                    "run rollback again after the old EIP drain is no longer needed"
                )
            if not checkpoints.get("last_rollback"):
                return {"mode": "noop", "state": "prepared"}
        self.runner.preflight("prepare")
        started = self.now()
        source = self.runner.run("source", "prepare-source")
        bundle = str(source.get("bundle_sha256") or "")
        if not SHA256_RE.fullmatch(bundle):
            raise MigrationError("prepare-source returned no verified bundle SHA-256")
        manifest_digest = str(source.get("manifest_digest") or "")
        if not SHA256_RE.fullmatch(manifest_digest):
            raise MigrationError("prepare-source returned no verified manifest digest")
        if binding.manifest_digest != "auto" and binding.manifest_digest != manifest_digest:
            raise MigrationError("source manifest digest mismatch")
        effective_binding = dataclasses.replace(binding, manifest_digest=manifest_digest)
        self.runner.run("target", "restore-target", bundle_sha256=bundle, rehearsal=True)
        state = _initial_state(effective_binding)
        state["rehearsal_seconds"] = round(self.now() - started, 3)
        state["checkpoints"]["prepare_complete"] = {"at": _utc(self.now())}
        self._write(state)
        return {
            "mode": "executed",
            "state": "prepared",
            "rehearsal_seconds": state["rehearsal_seconds"],
            "manifest_digest": manifest_digest,
        }

    def _remaining(self, deadline: float) -> int:
        remaining = int(deadline - self.now())
        if remaining <= 0:
            raise MigrationError("120-second source write deadline exceeded")
        return remaining

    def _direct_rollback(self, state: dict[str, Any]) -> None:
        self.runner.run("target", "release-target-candidate")
        self.runner.run("source", "resume-source")
        state["state"] = "prepared"
        state["target_accepts_writes_at"] = None
        prepare_complete = state.get("checkpoints", {}).get("prepare_complete")
        state["checkpoints"] = {
            **({"prepare_complete": prepare_complete} if prepare_complete else {}),
            "last_rollback": {"path": "pre_write_direct", "at": _utc(self.now())},
        }
        self._write(state)

    def _reverse_rollback(self, state: dict[str, Any]) -> None:
        reverse = self.runner.run("target", "freeze-target")
        bundle = str(reverse.get("bundle_sha256") or "")
        if not SHA256_RE.fullmatch(bundle):
            raise MigrationError("freeze-target returned no verified reverse bundle SHA-256")
        self.runner.run("source", "restore-source", bundle_sha256=bundle)
        state["state"] = "cutting_over"
        state["checkpoints"]["rollback_source_restored"] = {
            "at": _utc(self.now()),
            "bundle_sha256": bundle,
        }
        self._write(state)
        self.runner.run("source", "resume-source")
        self.runner.run("target", "release-target-candidate")
        state["state"] = "prepared"
        state["target_accepts_writes_at"] = None
        state["checkpoints"]["last_rollback"] = {"path": "post_write_reverse_sync", "at": _utc(self.now())}
        self._write(state)

    def _cutover(self, binding: Binding, state: dict[str, Any]) -> dict[str, Any]:
        if state["state"] == "cutting_over" and state.get("checkpoints", {}).get("source_proxy_ready"):
            return {"mode": "noop", "state": "cutting_over", "dns_change": self._dns_change(binding)}
        if state["state"] == "cutting_over":
            raise MigrationError("interrupted cutover requires explicit rollback before retry")
        if state["state"] != "prepared":
            raise MigrationError("cutover requires state prepared")
        if not binding.source_public_ip:
            raise MigrationError("cutover requires source_public_ip for old-IP drain and rollback")
        if state.get("rehearsal_seconds", CUTOVER_DEADLINE_SECONDS + 1) > CUTOVER_DEADLINE_SECONDS:
            raise MigrationError("rehearsal exceeded the 120-second cutover ceiling")
        self.runner.preflight("cutover", require_reverse=True)
        prepare_complete = state.get("checkpoints", {}).get("prepare_complete")
        state["checkpoints"] = {"prepare_complete": prepare_complete} if prepare_complete else {}
        state["observing_started_at"] = None
        state["state"] = "cutting_over"
        state["checkpoints"]["cutover_started"] = {"at": _utc(self.now())}
        self._write(state)
        deadline = self.now() + CUTOVER_DEADLINE_SECONDS
        try:
            source = self.runner.run(
                "source",
                "freeze-source",
                timeout_seconds=self._remaining(deadline),
            )
            bundle = str(source.get("bundle_sha256") or "")
            final_manifest_digest = str(source.get("manifest_digest") or "")
            if not SHA256_RE.fullmatch(bundle):
                raise MigrationError("freeze-source returned no verified bundle SHA-256")
            if not SHA256_RE.fullmatch(final_manifest_digest):
                raise MigrationError("freeze-source returned no verified manifest digest")
            state["checkpoints"]["source_frozen"] = {
                "at": _utc(self.now()),
                "bundle_sha256": bundle,
                "manifest_digest": final_manifest_digest,
            }
            self._write(state)
            self.runner.run(
                "target",
                "restore-target",
                bundle_sha256=bundle,
                timeout_seconds=self._remaining(deadline),
            )
            state["checkpoints"]["target_verified"] = {"at": _utc(self.now())}
            self._write(state)
            remaining = self._remaining(deadline)
            if remaining <= TARGET_ENABLE_HEADROOM_SECONDS:
                raise MigrationError("insufficient deadline headroom to enable target safely")
            state["target_accepts_writes_at"] = _utc(self.now())
            self._write(state)
            self.runner.run("target", "enable-target", timeout_seconds=remaining)
            self._remaining(deadline)
            state["checkpoints"]["target_enabled"] = {"at": _utc(self.now())}
            self._write(state)
            self.runner.run(
                "source",
                "proxy-source",
                target_eip=binding.target_eip,
                timeout_seconds=self._remaining(deadline),
            )
            self._remaining(deadline)
            state["checkpoints"]["source_proxy_ready"] = {"at": _utc(self.now())}
            self._write(state)
        except Exception as exc:
            try:
                if state.get("target_accepts_writes_at"):
                    self._reverse_rollback(state)
                else:
                    self._direct_rollback(state)
            except Exception as rollback_exc:
                raise MigrationError(f"cutover failed ({exc}); rollback also failed ({rollback_exc})") from rollback_exc
            if isinstance(exc, MigrationError):
                raise
            raise MigrationError(str(exc)) from exc
        return {
            "mode": "executed",
            "state": "cutting_over",
            "final_manifest_digest": state["checkpoints"]["source_frozen"]["manifest_digest"],
            "dns_change": self._dns_change(binding),
        }

    @staticmethod
    def _dns_change(binding: Binding, *, rollback: bool = False) -> dict[str, str]:
        value = binding.source_public_ip if rollback else binding.target_eip
        return {
            "action": "UPSERT",
            "type": "A",
            "name": binding.domain,
            "value": value,
            "confirmation": dns_confirmation_token(binding, rollback=rollback),
            "executed": "false",
        }

    def _observe(
        self,
        binding: Binding,
        state: dict[str, Any],
        confirm_dns: str,
        observed_dns_ip: str,
    ) -> dict[str, Any]:
        if state["state"] == "observing" and state.get("checkpoints", {}).get("observation_complete"):
            return {"mode": "noop", "state": "observing"}
        if state["state"] not in {"cutting_over", "observing"} or not state.get("checkpoints", {}).get("source_proxy_ready"):
            raise MigrationError("observe requires state cutting_over with source proxy ready")
        expected = dns_confirmation_token(binding)
        if confirm_dns != expected:
            raise MigrationError(f"DNS confirmation mismatch; expected {expected}")
        if observed_dns_ip != binding.target_eip:
            raise MigrationError(f"{binding.domain} does not resolve to target EIP")
        if not binding.source_public_ip:
            raise MigrationError("observe requires source_public_ip to verify the old public path")
        started = self.now()
        state["state"] = "observing"
        state["observing_started_at"] = started
        state["checkpoints"].pop("observation_complete", None)
        state["checkpoints"]["dns_observed"] = {"at": _utc(self.now()), "value": observed_dns_ip}
        self._write(state)

        deadline = started + OBSERVATION_SECONDS
        next_probe_at = started
        while True:
            wait_seconds = next_probe_at - self.now()
            if wait_seconds > 0:
                self.sleep(wait_seconds)
            self.runner.run(
                "target",
                "verify-target",
                timeout_seconds=OBSERVATION_PROBE_TIMEOUT_SECONDS,
            )
            self.runner.run(
                "target",
                "verify-source-proxy",
                target_eip=binding.source_public_ip,
                timeout_seconds=OBSERVATION_PROBE_TIMEOUT_SECONDS,
            )
            if self.now() >= deadline:
                break
            next_probe_at = min(
                next_probe_at + OBSERVATION_PROBE_INTERVAL_SECONDS,
                deadline,
            )

        completed = self.now()
        state["checkpoints"]["observation_complete"] = {
            "at": _utc(completed),
            "duration_seconds": round(completed - started, 3),
        }
        self._write(state)
        return {"mode": "executed", "state": "observing"}

    def _mark_stable(self, binding: Binding, state: dict[str, Any]) -> dict[str, Any]:
        if state["state"] == "stable":
            return {"mode": "noop", "state": "stable"}
        if state["state"] != "observing" or state.get("observing_started_at") is None:
            raise MigrationError("mark-stable requires state observing")
        observation = state.get("checkpoints", {}).get("observation_complete")
        if not isinstance(observation, dict) or float(observation.get("duration_seconds", 0)) < OBSERVATION_SECONDS:
            raise MigrationError("mark-stable requires a completed continuous observation window")
        self.runner.run("target", "verify-target")
        self.runner.run(
            "target",
            "verify-source-proxy",
            target_eip=binding.source_public_ip,
        )
        state["state"] = "stable"
        state["checkpoints"]["stable"] = {"at": _utc(self.now())}
        self._write(state)
        return {"mode": "executed", "state": "stable"}

    def _rollback(
        self,
        binding: Binding,
        state: dict[str, Any],
        confirm_dns: str,
        observed_dns_ip: str,
    ) -> dict[str, Any]:
        if not state.get("checkpoints", {}).get("rollback_source_ready"):
            self.runner.preflight("rollback", require_reverse=bool(state.get("target_accepts_writes_at")))
        if not state.get("target_accepts_writes_at"):
            self._direct_rollback(state)
            return {"mode": "executed", "state": "prepared"}
        if not binding.source_public_ip:
            raise MigrationError("post-write rollback requires source_public_ip")
        checkpoints = state.get("checkpoints", {})
        if not checkpoints.get("rollback_source_ready"):
            if not checkpoints.get("rollback_source_restored"):
                reverse = self.runner.run("target", "freeze-target")
                bundle = str(reverse.get("bundle_sha256") or "")
                if not SHA256_RE.fullmatch(bundle):
                    raise MigrationError("freeze-target returned no verified reverse bundle SHA-256")
                self.runner.run("source", "restore-source", bundle_sha256=bundle)
                state["state"] = "cutting_over"
                state["checkpoints"]["rollback_source_restored"] = {
                    "at": _utc(self.now()),
                    "bundle_sha256": bundle,
                }
                self._write(state)
            self.runner.run("source", "resume-source")
            self.runner.run("target", "proxy-target", target_eip=binding.source_public_ip)
            state["state"] = "cutting_over"
            state["checkpoints"]["rollback_source_ready"] = {"at": _utc(self.now())}
            self._write(state)
            return {"mode": "executed", "state": "cutting_over", "dns_change": self._dns_change(binding, rollback=True)}
        expected = dns_confirmation_token(binding, rollback=True)
        if confirm_dns != expected:
            raise MigrationError(f"DNS rollback confirmation mismatch; expected {expected}")
        if observed_dns_ip != binding.source_public_ip:
            raise MigrationError(f"{binding.domain} does not resolve to source IP")
        state["state"] = "prepared"
        state["target_accepts_writes_at"] = None
        state["checkpoints"]["last_rollback"] = {"path": "post_write_reverse_sync", "at": _utc(self.now())}
        self._write(state)
        return {"mode": "executed", "state": "prepared"}


class SSMRemoteRunner:
    """Send one fixed helper action through SSM and poll the same CommandId."""

    def __init__(
        self,
        binding: Binding,
        *,
        helper_get_url: str,
        helper_sha256: str,
        forward_put_url: str,
        forward_get_url: str,
        reverse_put_url: str = "",
        reverse_get_url: str = "",
        run: Callable[..., subprocess.CompletedProcess[str]] = subprocess.run,
        sleep: Callable[[float], None] = time.sleep,
        epoch: Callable[[], float] = time.time,
    ) -> None:
        self.binding = binding
        self.helper_get_url = helper_get_url
        self.helper_sha256 = helper_sha256
        self.forward_put_url = forward_put_url
        self.forward_get_url = forward_get_url
        self.reverse_put_url = reverse_put_url
        self.reverse_get_url = reverse_get_url
        self._run = run
        self._sleep = sleep
        self._epoch = epoch

    def preflight(self, command: str, *, require_reverse: bool = False) -> None:
        if not self.helper_get_url:
            raise MigrationError(f"{command} requires a helper transfer URL")
        if not SHA256_RE.fullmatch(self.helper_sha256):
            raise MigrationError(f"{command} requires the helper SHA-256")
        if command in {"prepare", "cutover"} and not (self.forward_put_url and self.forward_get_url):
            raise MigrationError(f"{command} requires forward PUT and GET transfer URLs")
        if require_reverse and not (self.reverse_put_url and self.reverse_get_url):
            raise MigrationError(f"{command} requires reverse PUT and GET transfer URLs")

    def _aws_json(self, args: list[str]) -> dict[str, Any]:
        result = self._run(args, check=True, capture_output=True, text=True)
        payload = json.loads(result.stdout)
        if not isinstance(payload, dict):
            raise MigrationError("AWS response is not a JSON object")
        return payload

    def run(self, endpoint: str, action: str, **kwargs: object) -> dict[str, Any]:
        if endpoint not in {"source", "target"}:
            raise MigrationError(f"invalid endpoint: {endpoint}")
        region = self.binding.source_region if endpoint == "source" else self.binding.target_region
        instance_id = self.binding.source_instance_id if endpoint == "source" else self.binding.target_instance_id
        timeout = int(kwargs.get("timeout_seconds", 900))
        if timeout <= 0:
            raise MigrationError("remote action timeout must be positive")
        delivery_timeout = max(30, timeout)
        action_deadline_epoch = int(self._epoch()) + timeout
        transfer_url = ""
        if action in {"prepare-source", "freeze-source"}:
            transfer_url = self.forward_put_url
        elif action == "restore-target":
            transfer_url = self.forward_get_url
        elif action == "freeze-target":
            transfer_url = self.reverse_put_url
        elif action == "restore-source":
            transfer_url = self.reverse_get_url
        if action not in {
            "enable-target", "proxy-source", "proxy-target",
            "resume-source", "release-target-candidate", "verify-target", "verify-source-proxy",
        } and not transfer_url:
            raise MigrationError(f"remote {action} requires a transfer URL")
        if not self.helper_get_url:
            raise MigrationError("remote action requires a helper transfer URL")
        if not SHA256_RE.fullmatch(self.helper_sha256):
            raise MigrationError("remote action requires the helper SHA-256")
        work = f"/var/lib/tokenkey/migration/{self.binding.edge_id}"
        lock = "/var/lib/tokenkey/migration/.action.lock"
        action_args = [
            "python3 /tmp/edge_ec2_remote.py action",
            action,
            f"--work-dir {work}",
            "--execute",
        ]
        if kwargs.get("bundle_sha256"):
            action_args.append(f"--expected-bundle-sha256 {shlex.quote(str(kwargs['bundle_sha256']))}")
        if kwargs.get("target_eip"):
            action_args.append(f"--target-eip {shlex.quote(str(kwargs['target_eip']))}")
        if kwargs.get("rehearsal"):
            action_args.append("--rehearsal")
        remote = (
            "set -euo pipefail; umask 077; "
            f"install -d -m 0700 {shlex.quote(work)}; "
            f"exec 9>{shlex.quote(lock)}; "
            "flock -n 9 || { echo 'another migration action is still running' >&2; exit 75; }; "
            f"ACTION_DEADLINE_EPOCH={action_deadline_epoch}; "
            "[ \"$(date +%s)\" -lt \"$ACTION_DEADLINE_EPOCH\" ] || { echo 'migration action delivery exceeded deadline' >&2; exit 124; }; "
            "curl -fsS --max-time 60 \"$TK_HELPER_URL\" -o /tmp/edge_ec2_remote.py; "
            f"test \"$(sha256sum /tmp/edge_ec2_remote.py | awk '{{print $1}}')\" = {self.helper_sha256}; "
            + " ".join(action_args)
        )
        parameters = {
            "commands": [
                "export TK_HELPER_URL=" + shlex.quote(self.helper_get_url),
                "export TK_MIGRATION_TRANSFER_URL=" + shlex.quote(transfer_url),
                remote,
            ],
            "executionTimeout": [str(timeout)],
        }
        sent = self._aws_json([
            "aws", "ssm", "send-command", "--region", region,
            "--instance-ids", instance_id,
            "--document-name", "AWS-RunShellScript",
            "--comment", f"TokenKey edge migration {self.binding.edge_id} {action}",
            "--timeout-seconds", str(delivery_timeout),
            "--parameters", json.dumps(parameters, separators=(",", ":")),
            "--output", "json",
        ])
        command_id = ((sent.get("Command") or {}).get("CommandId"))
        if not isinstance(command_id, str) or not command_id:
            raise MigrationError("SSM send-command returned no CommandId")
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                invocation = self._aws_json([
                    "aws", "ssm", "get-command-invocation", "--region", region,
                    "--command-id", command_id, "--instance-id", instance_id,
                    "--output", "json",
                ])
            except subprocess.CalledProcessError as exc:
                if "InvocationDoesNotExist" not in str(exc.stderr or ""):
                    raise
                self._sleep(2)
                continue
            if invocation.get("CommandId") != command_id or invocation.get("InstanceId") != instance_id:
                raise MigrationError("SSM invocation identity mismatch")
            status = invocation.get("Status")
            if status in {"Pending", "InProgress", "Delayed"}:
                self._sleep(2)
                continue
            if status != "Success" or invocation.get("ResponseCode") != 0:
                detail = _redact_urls(str(invocation.get("StandardErrorContent") or ""))[:600]
                raise MigrationError(f"remote {action} failed status={status}: {detail}")
            stdout = str(invocation.get("StandardOutputContent") or "")
            match = re.search(r"bundle_sha256=([0-9a-f]{64})", stdout)
            manifest = re.search(r"manifest_digest=([0-9a-f]{64})", stdout)
            return {
                "ok": True,
                "bundle_sha256": match.group(1) if match else None,
                "manifest_digest": manifest.group(1) if manifest else None,
            }
        raise MigrationError(f"remote {action} did not finish before timeout")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--state-file", type=pathlib.Path, required=True)
    common.add_argument("--edge-id", required=True)
    common.add_argument("--source-region", required=True)
    common.add_argument("--source-instance-id", required=True)
    common.add_argument("--source-public-ip", default="")
    common.add_argument("--target-region", required=True)
    common.add_argument("--target-instance-id", required=True)
    common.add_argument("--target-eip", required=True)
    common.add_argument("--domain", required=True)
    common.add_argument("--commit", required=True)
    common.add_argument("--manifest-digest", required=True)
    common.add_argument("--execute", action="store_true")
    common.add_argument("--helper-get-url", default=os.environ.get("TK_MIGRATION_HELPER_GET_URL", ""))
    common.add_argument("--helper-sha256", default=os.environ.get("TK_MIGRATION_HELPER_SHA256", ""))
    common.add_argument("--forward-put-url", default=os.environ.get("TK_MIGRATION_FORWARD_PUT_URL", ""))
    common.add_argument("--forward-get-url", default=os.environ.get("TK_MIGRATION_FORWARD_GET_URL", ""))
    common.add_argument("--reverse-put-url", default=os.environ.get("TK_MIGRATION_REVERSE_PUT_URL", ""))
    common.add_argument("--reverse-get-url", default=os.environ.get("TK_MIGRATION_REVERSE_GET_URL", ""))
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("prepare", "cutover", "mark-stable"):
        sub.add_parser(name, parents=[common])
    for name in ("observe", "rollback"):
        child = sub.add_parser(name, parents=[common])
        child.add_argument("--confirm-dns", default="")
        child.add_argument("--observed-dns-ip", default="")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    binding = Binding(
        edge_id=args.edge_id,
        source_region=args.source_region,
        source_instance_id=args.source_instance_id,
        source_public_ip=args.source_public_ip,
        target_region=args.target_region,
        target_instance_id=args.target_instance_id,
        target_eip=args.target_eip,
        domain=args.domain,
        commit=args.commit,
        manifest_digest=args.manifest_digest,
    )
    runner = SSMRemoteRunner(
        binding,
        helper_get_url=args.helper_get_url,
        helper_sha256=args.helper_sha256,
        forward_put_url=args.forward_put_url,
        forward_get_url=args.forward_get_url,
        reverse_put_url=args.reverse_put_url,
        reverse_get_url=args.reverse_get_url,
    )
    orchestrator = MigrationOrchestrator(args.state_file, runner=runner)
    result = orchestrator.run(
        args.command,
        binding,
        execute=args.execute,
        confirm_dns=getattr(args, "confirm_dns", ""),
        observed_dns_ip=getattr(args, "observed_dns_ip", ""),
    )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (MigrationError, json.JSONDecodeError, subprocess.CalledProcessError) as exc:
        print(f"edge_ec2_migration: {exc}", file=sys.stderr)
        raise SystemExit(1)
