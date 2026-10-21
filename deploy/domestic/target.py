"""Explicit domestic targets; existing production is never an implicit fallback."""
import argparse
import json
from pathlib import Path
import re

INVENTORY = Path(__file__).with_name("targets.json")


def load_target(name, path=INVENTORY):
    data = json.loads(Path(path).read_text())
    if data.get("schema_version") != 1:
        raise ValueError("Unsupported domestic inventory schema")
    return validate_target(name, data["targets"].get(name))


def validate_target(name, target):
    if not target or target.get("enabled") is not True:
        raise ValueError("Unknown or disabled domestic target")
    if target.get("role") != "prod" or target.get("transport") != "ssh":
        raise ValueError("Only explicitly configured SSH prod targets are supported")
    if target.get("preparation_only") is not True:
        raise ValueError("Production target must be preparation-only until cutover approval")
    if target.get("source_environment") != "ucloud-cn-prod":
        raise ValueError("Node1 target source must be UCloud cn-prod")
    if target.get("public_ip") != "106.75.29.139":
        raise ValueError("Node1 target must preserve the approved public IP")
    for field, pattern in {
        "source_instance_id": r"uhost-[a-z0-9]+",
        "source_private_ip": r"(?:\d{1,3}\.){3}\d{1,3}",
        "source_eip_id": r"eip-[a-z0-9]+",
        "source_data_disk_id": r"bsm-[a-z0-9]+",
        "source_zone": r"cn-bj2-\d{2}",
        "candidate_instance_id": r"uhost-[a-z0-9]+",
        "candidate_public_ip": r"(?:\d{1,3}\.){3}\d{1,3}",
    }.items():
        if not re.fullmatch(pattern, target.get(field, "")):
            raise ValueError("Invalid read-only source inventory field: " + field)
    if target.get("candidate_inventory_status") != "candidate-observed-not-approved":
        raise ValueError("Node1 candidate inventory must remain unapproved")
    if target.get("candidate_identity_status") != "pending-readonly-verification":
        raise ValueError("Node1 identity must remain pending readonly verification")
    if target.get("public_ip_mode") not in ("pending", "eip-move", "wireguard-forward"):
        raise ValueError("Public IP ownership model is not approved")
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
    mode = (target.get("ingress"), target.get("tls"))
    if mode == ("ssh-tunnel", "internal"):
        expected_url = f'https://{target["domain"]}:18443'
    elif mode == ("ucloud-wireguard", "acme"):
        expected_url = f'https://{target["domain"]}'
    else:
        raise ValueError("Unsupported ingress and TLS combination")
    if target.get("frontend_url") != expected_url:
        raise ValueError("Frontend URL does not match the selected ingress")
    return dict(target, name=name)


def production_inventory(name, domain, admin_email=None):
    """Render a pending ownership/config handoff; never install or activate it."""
    target = load_target(name)
    target.update(domain=domain, frontend_url='https://' + domain,
                  ingress='ucloud-wireguard', tls='acme')
    if admin_email is not None:
        target['admin_email'] = admin_email
    validate_target(name, target)
    target.pop('name')
    return {'schema_version': 1, 'targets': {name: target}}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Render pending production inventory only; no host changes')
    parser.add_argument('--target', required=True)
    parser.add_argument('--production-domain', required=True)
    parser.add_argument('--admin-email')
    args = parser.parse_args()
    print(json.dumps(production_inventory(args.target, args.production_domain, args.admin_email), indent=2))
