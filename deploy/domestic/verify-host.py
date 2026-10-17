"""Fail closed before touching an SSH host or its existing data disk."""
import argparse
import json
from pathlib import Path
import subprocess


def verify(target, require_runtime=False):
    if Path("/etc/machine-id").read_text().strip() != target["machine_id"]:
        raise ValueError("SSH host identity mismatch")
    rows = json.loads(subprocess.check_output([
        "findmnt", "--json", "--mountpoint", target["data_mount"],
        "--output", "UUID,TARGET,FSTYPE",
    ], text=True))["filesystems"]
    if len(rows) != 1 or rows[0]["uuid"] != target["data_uuid"] or rows[0]["fstype"] != "ext4":
        raise ValueError("Expected data disk is not mounted")
    root, backing = Path("/var/lib/tokenkey"), Path(target["data_mount"]) / "tokenkey"
    if root.is_symlink() or backing.is_symlink():
        raise ValueError("Runtime roots may not be symlinks")
    if backing.exists() and any(backing.iterdir()):
        ownership = Path("/opt/tokenkey/target.json")
        if not ownership.exists() or json.loads(ownership.read_text()) != target:
            raise ValueError("Refusing an unowned data directory")
    if require_runtime and (not root.is_mount() or not root.samefile(backing)):
        raise ValueError("Runtime is not bound to the verified data disk")
    if root.exists() and any(root.iterdir()) and not root.samefile(backing):
        raise ValueError("Refusing an unrelated existing runtime")
    if target.get('ingress') == 'ucloud-wireguard':
        # Public ownership can only be installed in the approved cutover window.
        # Reuse the migration's approval + exact PG bind guard on every deploy/probe.
        subprocess.run(['python3', '/usr/local/lib/tokenkey/node1_runtime_guard.py'],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return {"host_verified": True, "disk_verified": True, "runtime_mounted": root.is_mount()}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--target-file", type=Path)
    group.add_argument("--target-json")
    parser.add_argument("--require-runtime", action="store_true")
    args = parser.parse_args()
    target = json.loads(args.target_file.read_text() if args.target_file else args.target_json)
    print(json.dumps(verify(target, args.require_runtime)))
