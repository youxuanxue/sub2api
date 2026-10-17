"""Explicit domestic targets; existing production is never an implicit fallback."""
import json
from pathlib import Path
import re

INVENTORY = Path(__file__).with_name("targets.json")


def load_target(name, path=INVENTORY):
    data = json.loads(Path(path).read_text())
    if data.get("schema_version") != 1:
        raise ValueError("Unsupported domestic inventory schema")
    target = data["targets"].get(name)
    if not target or target.get("enabled") is not True:
        raise ValueError("Unknown or disabled domestic target")
    if target.get("role") != "prod" or target.get("transport") != "ssh":
        raise ValueError("Only explicitly configured SSH prod targets are supported")
    for field, pattern in {
        "ssh_host": r"[a-zA-Z][a-zA-Z0-9_-]*",
        "machine_id": r"[a-f0-9]{32}",
        "data_uuid": r"[a-f0-9-]{36}",
        "domain": r"[a-z0-9.-]+",
        "admin_email": r"[a-zA-Z0-9@._+-]+",
    }.items():
        if not re.fullmatch(pattern, target.get(field, "")):
            raise ValueError("Invalid target field: " + field)
    if target.get("data_mount") != "/data":
        raise ValueError("This bootstrap only adopts the existing /data mount")
    if target.get("ingress") != "ssh-tunnel" or target.get("tls") != "internal":
        raise ValueError("Public ingress requires a separately reviewed target configuration")
    if target.get("frontend_url") != f'https://{target["domain"]}:18443':
        raise ValueError("Unexpected tunnel frontend URL")
    return dict(target, name=name)
