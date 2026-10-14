#!/usr/bin/env python3
"""Create/verify the private Hetzner pgdump bucket; only synthetic objects are removed."""
import argparse
import hashlib
import json
import os
import re
import urllib.error
import urllib.request
import uuid

def private_acl(acl):
    owner = acl.get("Owner", {}).get("ID")
    grants = acl.get("Grants", [])
    return (bool(owner) and len(grants) == 1 and grants[0].get("Permission") == "FULL_CONTROL"
            and grants[0].get("Grantee", {}).get("Type") == "CanonicalUser"
            and grants[0].get("Grantee", {}).get("ID") == owner)


def lifecycle_valid(rules):
    if len(rules) != 1:
        return False
    rule = rules[0]
    # Hetzner reads an empty Filter back as the equivalent legacy Prefix.
    all_objects = (rule["Filter"] == {"Prefix": ""} and rule.get("Prefix", "") == ""
                   if "Filter" in rule else rule.get("Prefix") == "")
    return (rule.get("Status") == "Enabled" and all_objects
            and rule.get("Expiration") == {"Days": 7}
            and rule.get("AbortIncompleteMultipartUpload") == {"DaysAfterInitiation": 1})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bucket", required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if not re.fullmatch(r"tokenkey-pgdump-fsn1-[0-9a-f]{12}", args.bucket):
        raise SystemExit("dedicated tokenkey pgdump bucket required")
    if not args.apply:
        print("plan: private bucket, TLS only, 7-day dumps, abort incomplete multipart after 1 day; synthetic SSE-C round-trip")
        return 0
    import boto3
    from botocore.config import Config
    from botocore.exceptions import ClientError
    access, secret = (os.environ.get(name) for name in ("HCLOUD_S3_Access_Key", "HCLOUD_S3_Secret_Key"))
    if not access or not secret:
        raise SystemExit("Hetzner S3 credentials missing")
    client = boto3.client("s3", region_name="fsn1", endpoint_url="https://fsn1.your-objectstorage.com",
                          aws_access_key_id=access, aws_secret_access_key=secret,
                          config=Config(signature_version="s3v4", connect_timeout=10, read_timeout=60,
                                        retries={"max_attempts": 2}, s3={"addressing_style": "path"},
                                        request_checksum_calculation="when_required", response_checksum_validation="when_required"))
    prefix = "synthetic/" + uuid.uuid4().hex
    keys = [prefix + "/single.bin", prefix + "/multipart.bin"]
    upload = None
    bucket_ready = False
    failure = None
    cleanup_errors = []
    stage = "bucket"
    try:
        try:
            client.head_bucket(Bucket=args.bucket)
        except ClientError as exc:
            if exc.response["ResponseMetadata"]["HTTPStatusCode"] != 404:
                raise
            client.create_bucket(Bucket=args.bucket, ACL="private", CreateBucketConfiguration={"LocationConstraint": "fsn1"})
        client.get_waiter("bucket_exists").wait(Bucket=args.bucket, WaiterConfig={"Delay": 2, "MaxAttempts": 15})
        bucket_ready = True
        acl = client.get_bucket_acl(Bucket=args.bucket)
        if not private_acl(acl):
            raise RuntimeError("bucket_not_private")
        if client.get_bucket_versioning(Bucket=args.bucket).get("Status") == "Enabled":
            raise RuntimeError("unexpected_versioning")
        stage = "lifecycle_policy"
        rules = [{"ID": "expire-pgdump", "Status": "Enabled", "Filter": {"Prefix": ""},
                  "Expiration": {"Days": 7}, "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 1}}]
        client.put_bucket_lifecycle_configuration(Bucket=args.bucket, LifecycleConfiguration={"Rules": rules})
        observed = client.get_bucket_lifecycle_configuration(Bucket=args.bucket)["Rules"]
        if not lifecycle_valid(observed):
            raise RuntimeError("lifecycle_mismatch")
        policy = {"Version": "2012-10-17", "Statement": [{"Sid": "DenyInsecureTransport", "Effect": "Deny", "Principal": "*", "Action": "s3:*",
                   "Resource": [f"arn:aws:s3:::{args.bucket}", f"arn:aws:s3:::{args.bucket}/*"],
                   "Condition": {"Bool": {"aws:SecureTransport": "false"}}}]}
        client.put_bucket_policy(Bucket=args.bucket, Policy=json.dumps(policy))
        if json.loads(client.get_bucket_policy(Bucket=args.bucket)["Policy"]) != policy:
            raise RuntimeError("tls_policy_mismatch")
        stage = "encrypted_round_trip"
        key = os.urandom(32)
        encryption = {"SSECustomerAlgorithm": "AES256", "SSECustomerKey": key}
        payload = b"synthetic encrypted TokenKey backup check\n"
        client.put_object(Bucket=args.bucket, Key=keys[0], Body=payload, **encryption)
        with client.get_object(Bucket=args.bucket, Key=keys[0], **encryption)["Body"] as body:
            if body.read() != payload:
                raise RuntimeError("round_trip_mismatch")
        for headers in [{}, {"SSECustomerAlgorithm": "AES256", "SSECustomerKey": os.urandom(32)}]:
            try:
                client.get_object(Bucket=args.bucket, Key=keys[0], **headers)
            except ClientError as exc:
                if exc.response["ResponseMetadata"]["HTTPStatusCode"] not in {400, 403}:
                    raise
            else:
                raise RuntimeError("encryption_bypass")
        stage = "multipart_round_trip"
        upload = client.create_multipart_upload(Bucket=args.bucket, Key=keys[1], **encryption)["UploadId"]
        parts, digest = [], hashlib.sha256()
        for number, payload in enumerate((b"x" * (5 * 1024**2), b"synthetic-tail"), 1):
            digest.update(payload)
            response = client.upload_part(Bucket=args.bucket, Key=keys[1], UploadId=upload, PartNumber=number, Body=payload, **encryption)
            parts.append({"PartNumber": number, "ETag": response["ETag"]})
        client.complete_multipart_upload(Bucket=args.bucket, Key=keys[1], UploadId=upload, MultipartUpload={"Parts": parts})
        upload = None
        read = hashlib.sha256()
        stream = client.get_object(Bucket=args.bucket, Key=keys[1], **encryption)["Body"]
        with stream:
            for chunk in stream.iter_chunks(65536):
                read.update(chunk)
        if read.digest() != digest.digest():
            raise RuntimeError("multipart_sha_mismatch")
        stage = "anonymous_read"
        try:
            urllib.request.urlopen(f"https://fsn1.your-objectstorage.com/{args.bucket}/{keys[0]}", timeout=20)
        except urllib.error.HTTPError as exc:
            if exc.code != 403:
                raise RuntimeError("anonymous_status")
        else:
            raise RuntimeError("anonymous_read_accepted")
    except Exception as exc:
        failure = {"ok": False, "stage": stage, "error_type": type(exc).__name__}
    finally:
        # These names are generated in this invocation; never touch any dump.
        if bucket_ready:
            try:
                if upload:
                    client.abort_multipart_upload(Bucket=args.bucket, Key=keys[1], UploadId=upload)
                for name in keys:
                    client.delete_object(Bucket=args.bucket, Key=name)
                if client.list_objects_v2(Bucket=args.bucket, Prefix=prefix).get("KeyCount", 0):
                    raise RuntimeError("synthetic_objects_remain")
            except Exception as exc:
                cleanup_errors.append(type(exc).__name__)
    if failure or cleanup_errors:
        print(json.dumps({**(failure or {"ok": False, "stage": "cleanup"}), "cleanup_errors": cleanup_errors}))
        return 2
    print(json.dumps({"ok": True, "bucket": args.bucket, "endpoint": "https://fsn1.your-objectstorage.com",
                      "retention_days": 7, "encrypted_round_trip": True, "multipart_verified": True,
                      "anonymous_read_denied": True, "synthetic_cleanup": True}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
