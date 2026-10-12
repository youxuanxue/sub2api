#!/usr/bin/env python3
"""Bounded, resumable QA pre-copy; never deletes AWS objects or switches producers.

The private SQLite ledger records source versions, SHA256 and original expiry.
Final cutover still requires a frozen inventory, per-object reconciliation and raw restore.
"""

import argparse
import hashlib
import json
import os
import re
import sqlite3
import time
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from pathlib import Path
from urllib.parse import urlencode

SOURCES = {
    "raw": (
        "tokenkey-prod-qa-raw-archive-682751977094",
        "tokenkey-qa-raw-fsn1-dd2ca9019b22",
        "raw/v1/",
    ),
}
PART_SIZE = 8 * 1024**2


def expiry(head):
    # AWS computes lifecycle expiry from source creation, rounded to midnight UTC.
    # Do not approximate it from the time of migration or silently extend retention.
    value = head.get("Expiration", "")
    match = re.search(r'expiry-date="([^"]+)"', value)
    if not match:
        raise ValueError("source object has no authoritative lifecycle expiry")
    result = parsedate_to_datetime(match[1]).astimezone(timezone.utc)
    if any((result.hour, result.minute, result.second, result.microsecond)):
        raise ValueError("source lifecycle expiry must be midnight UTC")
    return result


def safe_head(client, bucket, key):
    try:
        return client.head_object(Bucket=bucket, Key=key)
    except Exception as exc:
        if (
            getattr(exc, "response", {})
            .get("ResponseMetadata", {})
            .get("HTTPStatusCode")
            == 404
        ):
            return None
        raise


def ledger_open(path):
    if path.is_symlink() or (path.exists() and path.stat().st_mode & 0o077):
        raise ValueError("migration ledger must be a private regular file")
    if not path.exists():
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
    db = sqlite3.connect(path)
    db.execute("PRAGMA synchronous=FULL")
    db.execute("""CREATE TABLE IF NOT EXISTS objects (
      source_bucket TEXT NOT NULL, object_key TEXT NOT NULL, source_etag TEXT NOT NULL,
      target_bucket TEXT NOT NULL, target_etag TEXT NOT NULL, size INTEGER NOT NULL,
      sha256 TEXT NOT NULL, expires_at TEXT NOT NULL, verified_at TEXT NOT NULL,
      PRIMARY KEY(source_bucket,object_key))""")
    return db


def stream_hash(body, rate):
    digest, count, start = hashlib.sha256(), 0, time.monotonic()
    with body:
        while chunk := body.read(PART_SIZE):
            digest.update(chunk)
            count += len(chunk)
            delay = count / rate - (time.monotonic() - start)
            if delay > 0:
                time.sleep(delay)
    return count, digest.hexdigest()


def copy_one(
    source, target, db, source_bucket, target_bucket, key, rate, expected_expiries=None
):
    head = source.head_object(Bucket=source_bucket, Key=key)
    deadline = expiry(head)
    if deadline <= datetime.now(timezone.utc):
        return "expired"
    if (
        expected_expiries is not None
        and deadline.date().isoformat() not in expected_expiries
    ):
        raise ValueError(
            "source expiry changed since lifecycle preparation; re-inventory"
        )
    previous = db.execute(
        "SELECT source_etag,target_etag,size,sha256,expires_at FROM objects WHERE source_bucket=? AND object_key=?",
        (source_bucket, key),
    ).fetchone()
    dest = safe_head(target, target_bucket, key)
    if dest:
        same_source = dest.get("Metadata", {}).get("tk-migration-source-etag") == head[
            "ETag"
        ].strip('"')
        same_expiry = (
            dest.get("Metadata", {}).get("tk-migration-expires-at")
            == deadline.isoformat()
        )
        same_size = dest["ContentLength"] == head["ContentLength"]
        if same_source and same_expiry and same_size:
            if previous and previous[:3] == (
                head["ETag"],
                dest["ETag"],
                head["ContentLength"],
            ):
                verify_expiry_tag(target, target_bucket, key, deadline)
                return "verified"
            # Recover a crash between successful upload/readback and ledger commit.
            # Metadata alone is not proof; compare the entire current source and target.
            original = source.get_object(
                Bucket=source_bucket, Key=key, IfMatch=head["ETag"]
            )
            source_size, source_hash = stream_hash(original["Body"], rate)
            copied = target.get_object(
                Bucket=target_bucket, Key=key, IfMatch=dest["ETag"]
            )
            target_size, target_hash = stream_hash(copied["Body"], rate)
            if (source_size, source_hash) != (target_size, target_hash):
                raise ValueError("uncommitted target differs from source")
            if (
                source.head_object(Bucket=source_bucket, Key=key)["ETag"]
                != head["ETag"]
            ):
                raise ValueError("source changed during recovery")
            publish_expiry_tags(
                source,
                target,
                source_bucket,
                target_bucket,
                key,
                deadline,
                dest["ETag"],
            )
            db.execute(
                "INSERT OR REPLACE INTO objects VALUES(?,?,?,?,?,?,?,?,?)",
                (
                    source_bucket,
                    key,
                    head["ETag"],
                    target_bucket,
                    dest["ETag"],
                    target_size,
                    target_hash,
                    deadline.isoformat(),
                    datetime.now(timezone.utc).isoformat(),
                ),
            )
            db.commit()
            return "recovered"
        if previous is None or dest["ETag"] != previous[1]:
            raise ValueError("target object is not owned by this migration ledger")
        verify_expiry_tag(
            target, target_bucket, key, datetime.fromisoformat(previous[4])
        )
        # Only the mutable raw commit pointer may change during pre-copy.
        if not (source_bucket == SOURCES["raw"][0] and key.endswith("/commit.json")):
            raise ValueError("source immutable object changed")
    metadata = dict(head.get("Metadata", {}))
    for field in ("tk-migration-source-etag", "tk-migration-expires-at"):
        if field in metadata:
            raise ValueError("source has reserved migration metadata")
    metadata.update(
        {
            "tk-migration-source-etag": head["ETag"].strip('"'),
            "tk-migration-expires-at": deadline.isoformat(),
        }
    )
    fields = {
        k: head[k]
        for k in (
            "ContentType",
            "ContentEncoding",
            "ContentDisposition",
            "CacheControl",
            "ContentLanguage",
            "Expires",
        )
        if k in head
    }
    tags = {
        t["Key"]: t["Value"]
        for t in source.get_object_tagging(Bucket=source_bucket, Key=key).get(
            "TagSet", []
        )
    }
    if "tk-original-expiry" in tags or len(tags) >= 10:
        raise ValueError("source object tags conflict with migration expiry")
    tags["tk-original-expiry"] = deadline.date().isoformat()
    fields.update(
        Bucket=target_bucket, Key=key, Metadata=metadata, Tagging=urlencode(tags)
    )
    upload = None
    response = None
    start, size, digest = time.monotonic(), 0, hashlib.sha256()
    try:
        response = source.get_object(
            Bucket=source_bucket, Key=key, IfMatch=head["ETag"]
        )
        # All objects use multipart for bounded memory, including >5GiB objects.
        upload = target.create_multipart_upload(**fields)["UploadId"]
        parts = []
        with response["Body"] as body:
            while chunk := body.read(PART_SIZE):
                digest.update(chunk)
                size += len(chunk)
                number = len(parts) + 1
                if number > 10000:
                    raise ValueError("source exceeds bounded multipart size")
                part = target.upload_part(
                    Bucket=target_bucket,
                    Key=key,
                    UploadId=upload,
                    PartNumber=number,
                    Body=chunk,
                )
                parts.append({"PartNumber": number, "ETag": part["ETag"]})
                delay = size / rate - (time.monotonic() - start)
                if delay > 0:
                    time.sleep(delay)
        if (
            size != head["ContentLength"]
            or source.head_object(Bucket=source_bucket, Key=key)["ETag"] != head["ETag"]
        ):
            raise ValueError("source changed while copying")
        if not parts:
            part = target.upload_part(
                Bucket=target_bucket, Key=key, UploadId=upload, PartNumber=1, Body=b""
            )
            parts = [{"PartNumber": 1, "ETag": part["ETag"]}]
        condition = (
            {"IfMatch": dest["ETag"].strip('"')} if dest else {"IfNoneMatch": "*"}
        )
        output = target.complete_multipart_upload(
            Bucket=target_bucket,
            Key=key,
            UploadId=upload,
            MultipartUpload={"Parts": parts},
            **condition,
        )
        upload = None
        downloaded = target.get_object(
            Bucket=target_bucket, Key=key, IfMatch=output["ETag"]
        )
        target_size, target_hash = stream_hash(downloaded["Body"], rate)
        if target_size != size or target_hash != digest.hexdigest():
            raise ValueError("target readback SHA256 mismatch")
        publish_expiry_tags(
            source, target, source_bucket, target_bucket, key, deadline, output["ETag"]
        )
        db.execute(
            "INSERT OR REPLACE INTO objects VALUES(?,?,?,?,?,?,?,?,?)",
            (
                source_bucket,
                key,
                head["ETag"],
                target_bucket,
                output["ETag"],
                size,
                target_hash,
                deadline.isoformat(),
                datetime.now(timezone.utc).isoformat(),
            ),
        )
        db.commit()
        return "copied"
    finally:
        if response is not None:
            response["Body"].close()
        if upload:
            target.abort_multipart_upload(
                Bucket=target_bucket, Key=key, UploadId=upload
            )


def publish_expiry_tags(
    source, target, source_bucket, target_bucket, key, deadline, etag
):
    # Hetzner accepts Tagging on multipart initiation but drops it at completion.
    # Publish tags explicitly only after source/target SHA256 equality is proven.
    tags = source.get_object_tagging(Bucket=source_bucket, Key=key).get("TagSet", [])
    if len(tags) >= 10 or any(t["Key"] == "tk-original-expiry" for t in tags):
        raise ValueError("source object tags conflict with migration expiry")
    tags.append({"Key": "tk-original-expiry", "Value": deadline.date().isoformat()})
    if target.head_object(Bucket=target_bucket, Key=key)["ETag"] != etag:
        raise ValueError("target changed before tag publication")
    target.put_object_tagging(Bucket=target_bucket, Key=key, Tagging={"TagSet": tags})
    verify_expiry_tag(target, target_bucket, key, deadline)
    if target.head_object(Bucket=target_bucket, Key=key)["ETag"] != etag:
        raise ValueError("target changed during tag publication")


def verify_expiry_tag(client, bucket, key, deadline):
    tags = {
        t["Key"]: t["Value"]
        for t in client.get_object_tagging(Bucket=bucket, Key=key).get("TagSet", [])
    }
    if tags.get("tk-original-expiry") != deadline.date().isoformat():
        raise ValueError("target original-expiry tag differs")


def install_lifecycle(target, bucket, kind, deadlines):
    """Use absolute-date tag rules so migration never restarts object retention."""
    if kind != "raw":
        raise ValueError("only raw archive migration is supported")
    prefix = "raw/v1/"
    rules = [
        {
            "ID": "expire-qa-runtime-objects",
            "Status": "Enabled",
            "Filter": {"Prefix": prefix},
            "Expiration": {"Days": 7},
            "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 1},
        }
    ]
    for day in sorted(set(deadlines)):
        deadline = datetime.fromisoformat(day).replace(tzinfo=timezone.utc)
        if deadline.time().isoformat() != "00:00:00":
            raise ValueError("source lifecycle expiry is not midnight UTC")
        rules.append(
            {
                "ID": "migration-original-expiry-" + day,
                "Status": "Enabled",
                "Filter": {"Tag": {"Key": "tk-original-expiry", "Value": day}},
                "Expiration": {"Date": deadline},
            }
        )
    if len(rules) > 1000:
        raise ValueError("migration exceeds lifecycle rule limit")
    try:
        previous = target.get_bucket_lifecycle_configuration(Bucket=bucket).get(
            "Rules", []
        )
    except Exception as error:
        if (
            getattr(error, "response", {}).get("Error", {}).get("Code")
            != "NoSuchLifecycleConfiguration"
        ):
            raise
        previous = []
    # Retain prior absolute expiry dates across incremental pre-copy runs.
    owned = {rule["ID"]: rule for rule in rules}
    for rule in previous:
        name = rule.get("ID", "")
        if name.startswith("migration-original-expiry-"):
            owned.setdefault(name, rule)
        elif name != "expire-qa-runtime-objects":
            raise ValueError(
                "target has an unowned lifecycle rule; reconcile before copying"
            )
    if len(owned) > 1000:
        raise ValueError("migration exceeds lifecycle rule limit")
    target.put_bucket_lifecycle_configuration(
        Bucket=bucket, LifecycleConfiguration={"Rules": list(owned.values())}
    )
    actual = target.get_bucket_lifecycle_configuration(Bucket=bucket).get("Rules", [])
    expected = sorted(owned.values(), key=lambda rule: rule["ID"])
    if sorted(actual, key=lambda rule: rule["ID"]) != expected:
        raise ValueError("target lifecycle readback differs")


def clients():
    import boto3
    from botocore.config import Config

    config = Config(
        signature_version="s3v4",
        connect_timeout=10,
        read_timeout=60,
        retries={"max_attempts": 2},
        s3={"addressing_style": "path"},
        request_checksum_calculation="when_required",
        response_checksum_validation="when_required",
    )
    if not os.environ.get("HCLOUD_S3_Access_Key") or not os.environ.get(
        "HCLOUD_S3_Secret_Key"
    ):
        raise ValueError("existing Hetzner S3 credentials required")
    return (
        boto3.client("s3", region_name="us-east-1", config=config),
        boto3.client(
            "s3",
            region_name="fsn1",
            endpoint_url="https://fsn1.your-objectstorage.com",
            config=config,
            aws_access_key_id=os.environ["HCLOUD_S3_Access_Key"],
            aws_secret_access_key=os.environ["HCLOUD_S3_Secret_Key"],
        ),
    )


def frozen_inventory(source, bucket, prefix):
    objects = []
    for page in source.get_paginator("list_objects_v2").paginate(
        Bucket=bucket, Prefix=prefix
    ):
        for item in page.get("Contents", []):
            head = source.head_object(Bucket=bucket, Key=item["Key"])
            if (item["ETag"], item["Size"]) != (head["ETag"], head["ContentLength"]):
                raise ValueError("source changed while freezing inventory")
            objects.append(
                {
                    "key": item["Key"],
                    "etag": head["ETag"],
                    "size": head["ContentLength"],
                    "expires_at": expiry(head).isoformat(),
                }
            )
    return sorted(objects, key=lambda row: row["key"])


def verify_one(source, target, db, source_bucket, target_bucket, item, rate):
    key = item["key"]
    head = source.head_object(Bucket=source_bucket, Key=key)
    if (head["ETag"], head["ContentLength"], expiry(head).isoformat()) != (
        item["etag"],
        item["size"],
        item["expires_at"],
    ):
        raise ValueError("source differs from frozen inventory")
    dest = target.head_object(Bucket=target_bucket, Key=key)
    ledger = db.execute(
        "SELECT source_etag,target_bucket,target_etag,size,sha256,expires_at FROM objects "
        "WHERE source_bucket=? AND object_key=?",
        (source_bucket, key),
    ).fetchone()
    if not ledger or ledger[:4] != (
        item["etag"],
        target_bucket,
        dest["ETag"],
        item["size"],
    ):
        raise ValueError("target differs from migration ledger")
    if ledger[5] != item["expires_at"]:
        raise ValueError("ledger expiry differs from frozen inventory")
    original = source.get_object(Bucket=source_bucket, Key=key, IfMatch=item["etag"])
    original_hash = stream_hash(original["Body"], rate)
    copied = target.get_object(Bucket=target_bucket, Key=key, IfMatch=dest["ETag"])
    copied_hash = stream_hash(copied["Body"], rate)
    expected = (item["size"], ledger[4])
    if original_hash != expected or copied_hash != expected:
        raise ValueError("full readback SHA256 mismatch")
    for field in (
        "ContentType",
        "ContentEncoding",
        "ContentDisposition",
        "CacheControl",
        "ContentLanguage",
        "Expires",
    ):
        if head.get(field) != dest.get(field):
            raise ValueError("target content headers differ")
    metadata = dict(head.get("Metadata", {}))
    metadata.update(
        {
            "tk-migration-source-etag": item["etag"].strip('"'),
            "tk-migration-expires-at": item["expires_at"],
        }
    )
    if dest.get("Metadata", {}) != metadata:
        raise ValueError("target metadata differs")
    verify_expiry_tag(
        target, target_bucket, key, datetime.fromisoformat(item["expires_at"])
    )
    if source.head_object(Bucket=source_bucket, Key=key)["ETag"] != item["etag"]:
        raise ValueError("source changed during full readback")
    if target.head_object(Bucket=target_bucket, Key=key)["ETag"] != dest["ETag"]:
        raise ValueError("target changed during full readback")
    return {"key": key, "size": expected[0], "sha256": expected[1]}


def private_json(path, value):
    # Exclusive creation keeps prior inventories/receipts immutable and refuses symlinks.
    with os.fdopen(
        os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600),
        "w",
        encoding="utf-8",
    ) as handle:
        json.dump(value, handle, sort_keys=True, indent=2)
        handle.write("\n")


def verify_frozen(
    source, target, db, source_bucket, target_bucket, prefix, inventory, rate
):
    if not inventory or len({item["key"] for item in inventory}) != len(inventory):
        raise ValueError("frozen inventory is empty or has duplicate keys")
    if any(not item["key"].startswith(prefix) for item in inventory):
        raise ValueError("inventory key is outside raw prefix")
    if frozen_inventory(source, source_bucket, prefix) != inventory:
        raise ValueError("source inventory changed before verification")
    objects = [
        verify_one(source, target, db, source_bucket, target_bucket, item, rate)
        for item in inventory
    ]
    if frozen_inventory(source, source_bucket, prefix) != inventory:
        raise ValueError("source inventory changed during verification")
    return objects


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kind", choices=SOURCES, required=True)
    parser.add_argument("--ledger", type=Path, required=True)
    parser.add_argument("--mib-per-second", type=int, default=10, choices=range(1, 21))
    operation = parser.add_mutually_exclusive_group()
    operation.add_argument("--apply", action="store_true")
    operation.add_argument("--freeze-inventory", type=Path)
    operation.add_argument(
        "--verify-only",
        type=Path,
        help="frozen inventory; reread every object without writes",
    )
    parser.add_argument(
        "--receipt", type=Path, help="new private full verification receipt"
    )
    args = parser.parse_args()
    os.umask(0o077)
    source, target = clients()
    src, dst, prefix = SOURCES[args.kind]
    if args.freeze_inventory:
        inventory = frozen_inventory(source, src, prefix)
        if frozen_inventory(source, src, prefix) != inventory:
            raise ValueError("source inventory changed while freezing")
        private_json(
            args.freeze_inventory,
            {"source_bucket": src, "target_bucket": dst, "objects": inventory},
        )
        print(json.dumps({"inventory_objects": len(inventory), "cutover_ready": False}))
        return
    if args.verify_only:
        if not args.receipt or not args.ledger.is_file():
            parser.error(
                "--verify-only requires an existing ledger and a new --receipt"
            )
        frozen = json.loads(args.verify_only.read_text())
        if (frozen.get("source_bucket"), frozen.get("target_bucket")) != (src, dst):
            raise ValueError("frozen inventory bucket mismatch")
        db = ledger_open(args.ledger)
        try:
            verified = verify_frozen(
                source,
                target,
                db,
                src,
                dst,
                prefix,
                frozen["objects"],
                args.mib_per_second * 1024**2,
            )
        finally:
            db.close()
        private_json(
            args.receipt,
            {
                "source_bucket": src,
                "target_bucket": dst,
                "inventory_sha256": hashlib.sha256(
                    args.verify_only.read_bytes()
                ).hexdigest(),
                "verified_at": datetime.now(timezone.utc).isoformat(),
                "objects": verified,
                "cutover_ready": False,
                "remaining_gate": "raw restore and maintenance acceptance",
            },
        )
        print(
            json.dumps(
                {"fully_verified_objects": len(verified), "cutover_ready": False}
            )
        )
        return
    if args.receipt:
        parser.error("--receipt requires --verify-only")
    count, total, stats = 0, 0, {}
    inventory, deadlines = [], set()
    for page in source.get_paginator("list_objects_v2").paginate(
        Bucket=src, Prefix=prefix
    ):
        for row in page.get("Contents", []):
            inventory.append(row)
            if args.apply:
                deadlines.add(
                    expiry(source.head_object(Bucket=src, Key=row["Key"]))
                    .date()
                    .isoformat()
                )
    if args.apply:
        install_lifecycle(target, dst, args.kind, deadlines)
    db = ledger_open(args.ledger) if args.apply else None
    try:
        for row in inventory:
            count += 1
            total += row["Size"]
            if args.apply:
                outcome = copy_one(
                    source,
                    target,
                    db,
                    src,
                    dst,
                    row["Key"],
                    args.mib_per_second * 1024**2,
                    deadlines,
                )
                stats[outcome] = stats.get(outcome, 0) + 1
                print(
                    json.dumps(
                        {"objects_seen": count, "bytes_seen": total, "results": stats}
                    ),
                    flush=True,
                )
        print(
            json.dumps(
                {
                    "apply": args.apply,
                    "objects": count,
                    "bytes": total,
                    "results": stats,
                    "cutover_ready": False,
                }
            )
        )
    finally:
        if db:
            db.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # SDK exception bodies can contain identifiers and signed request details.
        print(
            json.dumps(
                {
                    "status": "stopped",
                    "error_type": type(error).__name__,
                    "cutover_ready": False,
                }
            )
        )
        raise SystemExit(1) from None
