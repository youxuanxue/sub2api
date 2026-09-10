#!/usr/bin/env python3
"""Install a stopped image/config pin under the already drained QA timer lock."""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import tempfile
import uuid


def command(*args: str) -> str:
    return subprocess.check_output(args, text=True).strip()


def install(image: str, template: str, *, keep_previous: bool = False) -> None:
    match = re.fullmatch(r"ghcr.io/youxuanxue/sub2api:([0-9]+\.[0-9]+\.[0-9]+(?:-(?:rc|beta)\.[0-9]+)?)", image)
    if not match:
        raise ValueError("maintenance requires an immutable TokenKey release image")
    networks = json.loads(command("docker", "inspect", "tokenkey-postgres"))[0]["NetworkSettings"]["Networks"]
    if len(networks) != 1:
        raise ValueError("database network is ambiguous")
    os.environ.update(QA_MAINTENANCE_IMAGE=image, QA_MAINTENANCE_TAG=match[1], QA_RUNTIME_NETWORK=next(iter(networks)))
    compose = ["docker", "compose", "--project-name", "tokenkey-qa", "--env-file",
               "/var/lib/tokenkey/.env", "-f", template]
    # Resolve first, so missing config cannot destroy an existing runtime pin.
    config = json.loads(command(*compose, "config", "--format", "json"))
    if config["services"]["runtime"]["image"] != image:
        raise ValueError("QA runtime image resolution drift")
    service = config["services"]["runtime"]
    command("docker", "pull", image)
    candidate = "tokenkey-qa-runtime-" + uuid.uuid4().hex
    previous = candidate + "-previous"
    old = command("docker", "ps", "-aq", "--filter", "name=^/tokenkey-qa-runtime$")
    old_renamed = False
    promoted = False
    try:
        with tempfile.NamedTemporaryFile(mode="w", prefix="qa-runtime-env-") as env:
            for key, value in sorted(service["environment"].items()):
                if "\n" in str(value) or "\r" in str(value):
                    raise ValueError("multiline QA environment values are unsupported")
                env.write(f"{key}={value}\n")
            env.flush()
            arguments = ["docker", "create", "--name", candidate, "--network", next(iter(networks)),
                         "--env-file", env.name, "--entrypoint", "/bin/true"]
            for key, value in service["labels"].items():
                arguments.extend(["--label", f"{key}={value}"])
            arguments.extend(["--volume", "/var/lib/tokenkey/app:/app/data", image])
            command(*arguments)
        facts = json.loads(command("docker", "inspect", candidate))[0]
        if facts["State"]["Running"] or facts["Config"]["Image"] != image:
            raise ValueError("QA runtime pin verification failed")
        if old:
            command("docker", "rename", "tokenkey-qa-runtime", previous)
            old_renamed = True
        command("docker", "rename", candidate, "tokenkey-qa-runtime")
        promoted = True
    finally:
        if not promoted:
            if old_renamed:
                command("docker", "rename", previous, "tokenkey-qa-runtime")
            subprocess.run(["docker", "rm", candidate], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if old_renamed and not keep_previous:
        command("docker", "rm", previous)
    print(json.dumps({"tag": match[1], "id": facts["Id"], "image_id": facts["Image"]}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--template", required=True)
    parser.add_argument("--keep-previous", action="store_true")
    args = parser.parse_args()
    install(args.image, args.template, keep_previous=args.keep_previous)
