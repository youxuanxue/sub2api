#!/usr/bin/env python3
"""SSH transport for isolated prod, reusing the canonical blue/green host payload."""
import argparse
import io
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import tarfile
import tempfile
import uuid

from target import load_target

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def ssh(target, command, **kwargs):
    return run(["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=30",
                "-o", "ServerAliveCountMax=3", target["ssh_host"], command], **kwargs)


def probe(target, require_runtime=False, **kwargs):
    command = "python3 - --target-json " + shlex.quote(json.dumps(target))
    if require_runtime:
        command += " --require-runtime"
    return ssh(target, command, input=(HERE / "verify-host.py").read_text(), text=True, **kwargs)


def verify_release(tag):
    env = dict(os.environ, GITHUB_REPOSITORY="youxuanxue/sub2api")
    if not (env.get("GH_TOKEN") or env.get("GITHUB_TOKEN")):
        env["GH_TOKEN"] = run(["gh", "auth", "token"], capture_output=True, text=True).stdout.strip()
    run(["bash", str(ROOT / "ops/stage0/verify_ghcr_manifest.sh"), tag, "false"], env=env)
    run(["python3", str(ROOT / "scripts/checks/bluegreen-migration-safety.py"),
         "--release-tag", tag], cwd=ROOT)


def render_environment(directory):
    # Do not inherit production QA coordinates, credentials, or staged-release flags.
    env = {key: value for key, value in os.environ.items()
           if key in {"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL"}}
    env.update(STAGE0_RENDER_ONLY="1", STAGE0_DEPLOY_PROFILE="prod",
               STAGE0_SSM_OUTPUT_DIR=directory, QA_ARCHIVE_ENABLED="false",
               QA_CAPTURE_ENABLED="false", TELEMETRY_ARCHIVE_ENABLED="false")
    return env


def bootstrap(target, tag, state):
    if target['ingress'] != 'ssh-tunnel':
        raise ValueError('Production must adopt the reconciled candidate; fresh bootstrap is forbidden')
    probe(target)
    verify_release(tag)
    images_file = state / "images.json"
    if images_file.exists():
        images = json.loads(images_file.read_text())
        if images["tokenkey"]["tag"] != f"ghcr.io/youxuanxue/sub2api:{tag}":
            raise ValueError("Bootstrap image changed; use deploy for an existing runtime")
    else:
        images = {}
        for name, reference in {
            "tokenkey": f"ghcr.io/youxuanxue/sub2api:{tag}",
            "caddy": "caddy:2-alpine", "postgres": "postgres:18-alpine", "redis": "redis:8-alpine",
        }.items():
            ssh(target, "docker pull " + shlex.quote(reference), stdout=subprocess.DEVNULL)
            raw = ssh(target, "docker image inspect " + shlex.quote(reference), capture_output=True, text=True)
            info = json.loads(raw.stdout)[0]
            if info["Architecture"] != "amd64" or info["Os"] != "linux" or not info.get("RepoDigests"):
                raise ValueError("Unverified or incompatible image")
            images[name] = {"source": info["RepoDigests"][0], "image_id": info["Id"],
                            "tag": reference if name == "tokenkey" else info["RepoDigests"][0]}
        images_file.write_text(json.dumps(images, indent=2))
    run(["python3", str(HERE / "prepare-runtime.py"), str(state), "--target", target["name"]])
    runtime = state / "runtime"
    (runtime / "target.json").write_text(json.dumps(target))
    contents = io.BytesIO()
    with tarfile.open(fileobj=contents, mode="w") as archive:
        for path in sorted(runtime.iterdir()):
            if path.is_file():
                archive.add(path, arcname=path.name)
        for name in ["verify-host.py", "bootstrap-ssh-host.sh"]:
            archive.add(HERE / name, arcname=name)
    directory = "/opt/tokenkey/bootstrap/" + uuid.uuid4().hex
    command = f"umask 077; mkdir -p {directory} && tar -xf - -C {directory}"
    ssh(target, command, input=contents.getvalue())
    log = state / ("bootstrap-" + uuid.uuid4().hex + ".log")
    with log.open("w") as output:
        try:
            ssh(target, "flock -n /run/lock/tokenkey-ssh-operation.lock bash " + directory
                + "/bootstrap-ssh-host.sh " + directory, stdout=output, stderr=subprocess.STDOUT)
        except subprocess.CalledProcessError:
            print("Bootstrap failed; protected diagnostic log:", log)
            raise
    print("Bootstrap completed; protected diagnostic log:", log)


def deploy(target, tag, state):
    probe(target, require_runtime=True)
    verify_release(tag)
    # This is the existing host state machine, not a second upgrade implementation.
    with tempfile.TemporaryDirectory(prefix="tk-ssh-render-") as temporary:
        env = render_environment(temporary)
        run(["bash", str(ROOT / "ops/stage0/deploy_via_ssm_bluegreen.sh"),
             tag, "ssh-" + target["name"]], env=env)
        params = json.loads((Path(temporary) / "ssm-params.json").read_text())
        payload = "set -euo pipefail\npython3 /usr/local/lib/tokenkey/verify-host.py --target-file /opt/tokenkey/target.json --require-runtime\n"
        payload += "\n".join(params["commands"]) + "\n"
        # Preserve logs privately: application logs may include sensitive fields.
        log = state / ("deploy-" + uuid.uuid4().hex + ".log")
        with log.open("w") as output:
            try:
                ssh(target, "flock -n /run/lock/tokenkey-ssh-operation.lock bash -s",
                    input=payload, text=True, stdout=output, stderr=subprocess.STDOUT)
            except subprocess.CalledProcessError:
                print("Deployment failed; protected diagnostic log:", log)
                raise
        text = log.read_text()
        cutover = re.search(r"tk_stage0_cutover: timestamp=(\S+)", text)
        if not cutover:
            raise ValueError("Shared deploy did not produce a cutover receipt")
        receipt = {"target": target["name"], "tag": tag, "cutover_at": cutover.group(1), "log": str(log)}
        (state / "last-deploy.json").write_text(json.dumps(receipt, indent=2))
        print(json.dumps(receipt))


def smoke(target):
    probe(target, require_runtime=True)
    domain = target["domain"]
    command = f"set -eu; curl -fsS --max-time 10 --resolve {domain}:443:127.0.0.1 https://{domain}/health; printf '\\n'; cat /var/lib/tokenkey/active-color; docker ps --filter name=tokenkey --format '{{{{.Names}}}}\\t{{{{.Image}}}}\\t{{{{.Status}}}}'"
    ssh(target, command)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument("--operation", required=True, choices=["plan", "bootstrap", "deploy", "smoke-only"])
    parser.add_argument("--tag", default="")
    args = parser.parse_args()
    target = load_target(args.target)
    tag = args.tag.removeprefix("v")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-(?:rc|beta)\.[0-9]+)?", tag):
        parser.error("An explicit released version is required")
    if args.operation == "plan":
        probe(target)
        print(json.dumps({"target": target, "image": f"ghcr.io/youxuanxue/sub2api:{tag}",
                          "runtime_root": "/var/lib/tokenkey", "data_directory": "/data/tokenkey",
                          "existing_stack": "preserve /opt/sub2api", "secrets": "fresh isolated state"}, indent=2))
        return
    os.umask(0o077)
    state = Path.home() / ".local/share/tokenkey/ssh-targets" / target["name"]
    state.mkdir(parents=True, mode=0o700, exist_ok=True)
    state.chmod(0o700)
    if args.operation == "bootstrap":
        bootstrap(target, tag, state)
    elif args.operation == "deploy":
        deploy(target, tag, state)
        smoke(target)
    else:
        smoke(target)


if __name__ == "__main__":
    main()
