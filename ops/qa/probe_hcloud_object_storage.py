"""Create a private, disposable-data QA compatibility bucket (explicit opt-in)."""
import argparse
import hashlib
import json
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


def require(value, name):
    if not value:
        raise RuntimeError(name)


def http_status(url, headers=None, method='GET'):
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers or {}, method=method), timeout=20) as response:
            return response.status, {k.lower():v for k,v in response.headers.items()}, response.read()
    except urllib.error.HTTPError as error:
        return error.code, {k.lower():v for k,v in error.headers.items()}, b''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--bucket', help='Reuse an existing dedicated tokenkey-qa-verify-fsn1 bucket')
    args = parser.parse_args()
    if not args.apply:
        print('Plan: create one private unversioned fsn1 QA verification bucket; test synthetic objects; remove all test objects; retain empty bucket.')
        return
    access = os.environ.get('HCLOUD_S3_Access_Key')
    secret = os.environ.get('HCLOUD_S3_Secret_Key')
    require(access and secret, 'credentials_missing')
    client = boto3.client('s3', region_name='fsn1', endpoint_url='https://fsn1.your-objectstorage.com',
                          aws_access_key_id=access, aws_secret_access_key=secret,
                          config=Config(signature_version='s3v4', connect_timeout=10, read_timeout=25,
                                        retries={'max_attempts': 1}, s3={'addressing_style': 'path'},
                                        request_checksum_calculation='when_required', response_checksum_validation='when_required'))
    bucket = args.bucket or ('tokenkey-qa-verify-fsn1-' + uuid.uuid4().hex[:12])
    require(re.fullmatch(r'tokenkey-qa-verify-fsn1-[0-9a-f]{12}', bucket), 'verification_bucket_required')
    prefix = 'synthetic/' + uuid.uuid4().hex + '/'
    key = prefix + 'object.bin'
    multipart_key = prefix + 'multipart.bin'
    checks = {}
    result = {'bucket':bucket, 'endpoint':'https://fsn1.your-objectstorage.com', 'checks':checks, 'production_ready':False}
    created = False
    upload_id = None
    stage = 'create_private_bucket'
    cleanup_errors = []
    try:
        if not args.bucket:
            client.create_bucket(Bucket=bucket, ACL='private', CreateBucketConfiguration={'LocationConstraint':'fsn1'})
        else:
            require(client.list_objects_v2(Bucket=bucket).get('KeyCount',0) == 0, 'verification_bucket_not_empty')
        created = True
        client.get_waiter('bucket_exists').wait(Bucket=bucket, WaiterConfig={'Delay':2, 'MaxAttempts':15})
        acl = client.get_bucket_acl(Bucket=bucket)
        require(all(g['Grantee'].get('Type') == 'CanonicalUser' for g in acl['Grants']), 'public_acl')
        require(client.get_bucket_versioning(Bucket=bucket).get('Status') is None, 'versioning_enabled')
        checks['private_unversioned_bucket'] = True
        stage = 'create_and_read'
        payload = b'Synthetic TokenKey QA compatibility check. No production data.\n'
        first = client.put_object(Bucket=bucket, Key=key, Body=payload, ContentType='application/octet-stream', Metadata={'purpose':'synthetic-qa-compatibility'}, IfNoneMatch='*')
        response = client.get_object(Bucket=bucket, Key=key)
        with response['Body'] as body:
            require(body.read() == payload, 'roundtrip_mismatch')
        require(response['ContentType'] == 'application/octet-stream', 'content_type_mismatch')
        require(response['Metadata']['purpose'] == 'synthetic-qa-compatibility', 'metadata_mismatch')
        checks[stage] = True
        stage = 'anonymous_read_denied'
        require(http_status(f'https://fsn1.your-objectstorage.com/{bucket}/{key}')[0] == 403, 'anonymous_read_accepted')
        checks[stage] = True
        for name, condition in [('immutable_create_conflict', {'IfNoneMatch':'*'}), ('stale_etag_conflict', {'IfMatch':'"00000000000000000000000000000000"'})]:
            stage = name
            try:
                client.put_object(Bucket=bucket, Key=key, Body=payload, **condition)
            except ClientError as error:
                require(error.response['ResponseMetadata']['HTTPStatusCode'] == 412, name + '_unexpected_status')
                checks[name] = True
            else:
                raise RuntimeError(name + '_unexpected_success')
        stage = 'matching_etag_update'
        client.put_object(Bucket=bucket, Key=key, Body=payload, IfMatch=first['ETag'].strip('"'))
        checks[stage] = True
        result['matching_etag_format_tested'] = 'unquoted'
        stage = 'multipart_checksum'
        upload_id = client.create_multipart_upload(Bucket=bucket, Key=multipart_key)['UploadId']
        parts, whole_hash = [], hashlib.sha256()
        for number, body in enumerate((b'x' * (5 * 1024**2), b'synthetic-tail'), 1):
            whole_hash.update(body)
            part = client.upload_part(Bucket=bucket, Key=multipart_key, UploadId=upload_id, PartNumber=number, Body=body)
            parts.append({'PartNumber':number, 'ETag':part['ETag']})
        client.complete_multipart_upload(Bucket=bucket, Key=multipart_key, UploadId=upload_id, MultipartUpload={'Parts':parts})
        upload_id = None
        read_hash = hashlib.sha256()
        stream = client.get_object(Bucket=bucket, Key=multipart_key)['Body']
        with stream:
            for chunk in stream.iter_chunks(65536): read_hash.update(chunk)
        require(read_hash.digest() == whole_hash.digest(), 'multipart_checksum_mismatch')
        checks[stage] = True
        stage = 'signed_download_and_cors'
        client.put_bucket_cors(Bucket=bucket, CORSConfiguration={'CORSRules':[{'AllowedOrigins':['https://api.tokenkey.dev'], 'AllowedMethods':['GET','HEAD'], 'AllowedHeaders':['Range'], 'ExposeHeaders':['ETag','Content-Length'], 'MaxAgeSeconds':60}]})
        url = client.generate_presigned_url('get_object', Params={'Bucket':bucket,'Key':key}, ExpiresIn=60)
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(url).query)
        require(query.get('X-Amz-SignedHeaders') == ['host'], 'download_requires_extra_headers')
        headers = {'Origin':'https://api.tokenkey.dev'}
        status, response_headers, body = http_status(url, headers)
        require(status == 200 and body == payload, 'signed_download_failed')
        # CORS updates propagate asynchronously across the provider frontends.
        cors_ready = False
        for _ in range(15):
            get_status, get_headers, get_body = http_status(url, headers)
            preflight_status, _, _ = http_status(url, {'Origin':'https://api.tokenkey.dev','Access-Control-Request-Method':'GET','Access-Control-Request-Headers':'range'}, method='OPTIONS')
            if get_status == 200 and get_body == payload and get_headers.get('access-control-allow-origin') == 'https://api.tokenkey.dev' and preflight_status in (200,204):
                cors_ready = True
                break
            time.sleep(2)
        require(cors_ready, 'cors_did_not_become_ready')
        require(http_status(url)[0] == 200, 'plain_signed_url_failed')
        checks[stage] = True
        stage = 'expired_signature_denied'
        expired = client.generate_presigned_url('get_object', Params={'Bucket':bucket,'Key':key}, ExpiresIn=1)
        time.sleep(3)
        require(http_status(expired,headers)[0] == 403, 'expired_url_accepted')
        checks[stage] = True
        result['passed'] = True
    except ClientError as error:
        result.update(passed=False, failed_stage=stage, error_code=error.response.get('Error',{}).get('Code'), http_status=error.response.get('ResponseMetadata',{}).get('HTTPStatusCode'))
    except Exception as error:
        # Do not emit exception strings, requests, headers, signed URLs or keys.
        result.update(passed=False, failed_stage=stage, error_type=type(error).__name__)
        if isinstance(error, RuntimeError): result['assertion'] = error.args[0]
    finally:
        if created:
            if upload_id:
                try: client.abort_multipart_upload(Bucket=bucket, Key=multipart_key, UploadId=upload_id)
                except Exception: cleanup_errors.append('abort_multipart')
            for object_key in (key,multipart_key):
                try: client.delete_object(Bucket=bucket, Key=object_key)
                except Exception: cleanup_errors.append('delete_synthetic_object')
            try:
                require(client.list_objects_v2(Bucket=bucket).get('KeyCount',0) == 0, 'objects_remain')
                require(not client.list_multipart_uploads(Bucket=bucket).get('Uploads'), 'uploads_remain')
            except Exception: cleanup_errors.append('verify_empty_bucket')
        result.update(bucket_created=created and not args.bucket, synthetic_cleanup_ok=created and not cleanup_errors, cleanup_errors=cleanup_errors)
    print(json.dumps(result, separators=(',',':')))
    raise SystemExit(0 if result.get('passed') and not cleanup_errors else 1)


if __name__ == '__main__': main()
