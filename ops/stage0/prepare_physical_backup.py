#!/usr/bin/env python3
"""Create a reviewable, immutable host/recovery bundle; never upload or deploy."""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import tarfile
import tempfile
from pathlib import Path

from postgres_backup import configuration, run


def validate_image_export(archive: tarfile.TarFile, selected: str) -> str:
    """Docker's classic store identifies configs; containerd identifies OCI roots."""
    manifest = json.load(archive.extractfile("manifest.json"))
    if len(manifest) != 1:
        raise ValueError("export exactly one runtime image")
    config = archive.extractfile(manifest[0]["Config"]).read()
    config_id = "sha256:" + hashlib.sha256(config).hexdigest()
    if selected == config_id:
        return config_id
    try:
        index = json.load(archive.extractfile("index.json"))
        if selected not in [item["digest"] for item in index["manifests"]]:
            raise ValueError("selected image is absent from export")

        def contains_config(digest: str, depth: int = 0) -> bool:
            if depth > 4 or not digest.startswith("sha256:"):
                raise ValueError("invalid OCI image descriptor")
            raw = archive.extractfile("blobs/sha256/" + digest[7:]).read()
            if "sha256:" + hashlib.sha256(raw).hexdigest() != digest:
                raise ValueError("OCI image descriptor checksum differs")
            item = json.loads(raw)
            return item.get("config", {}).get("digest") == config_id or any(
                contains_config(child["digest"], depth + 1)
                for child in item.get("manifests", [])
            )

        if not contains_config(selected):
            raise ValueError("exported image references another config")
    except KeyError as exc:
        raise ValueError("image export differs from selected image") from exc
    return config_id


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="tokenkey-postgres-backup:dev")
    parser.add_argument("--image-tar", type=Path, required=True)
    parser.add_argument("--billing-check", type=Path, required=True)
    parser.add_argument("--bucket", required=True)
    parser.add_argument("--region", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    source = Path(__file__).resolve().parent
    image = json.loads(run(["docker", "image", "inspect", args.image]))[0]
    # Validate both Docker storage formats without confusing config/index IDs.
    with tarfile.open(args.image_tar) as archive:
        config_id = validate_image_export(archive, image["Id"])
    if image["Architecture"] != "arm64":
        raise ValueError("production bundle requires ARM64")
    with tempfile.TemporaryDirectory(prefix="tk-physical-bundle-") as temp:
        root = Path(temp)
        for name in (
            "postgres_backup.py",
            "install_postgres_backup.py",
            "restore_postgres_backup.py",
        ):
            shutil.copy2(source / name, root / name)
        shutil.copy2(
            source.parents[1] / "deploy/aws/stage0/docker-compose.prod-pg.yml",
            root / "docker-compose.prod-pg.yml",
        )
        shutil.copy2(args.billing_check, root / "backup-billing-check")
        (root / "config").mkdir()
        (root / "config/pgbackrest.conf").write_text(
            configuration(args.bucket, args.region)
        )
        (root / "manifest.json").write_text(
            json.dumps(
                {
                    "image": image["Id"],
                    "config_image_id": config_id,
                    "architecture": image["Architecture"],
                }
            )
        )
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(args.output, "w:gz") as archive:
            for path in sorted(root.iterdir()):
                archive.add(path, arcname=path.name)
            archive.add(args.image_tar, arcname="image.tar")
    digest = hashlib.file_digest(args.output.open("rb"), "sha256").hexdigest()
    print(
        json.dumps(
            {
                "bundle": str(args.output),
                "sha256": digest,
                "image": image["Id"],
                "config_image_id": config_id,
                "suggested_key": f"artifacts/{digest}.tar.gz",
                "uploaded": False,
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()
