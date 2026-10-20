#!/usr/bin/env python3
"""Plan/apply only the image-prefix lifecycle rule on the shared transfer bucket."""
import argparse
import json

from image_distribution import settings

RULE = {
    'ID': 'cn-prod-release-images', 'Status': 'Enabled',
    'Filter': {'Prefix': 'cn-prod/images/'}, 'Expiration': {'Days': 30},
    'NoncurrentVersionExpiration': {'NoncurrentDays': 7},
    'AbortIncompleteMultipartUpload': {'DaysAfterInitiation': 1},
}


def desired_rules(existing):
    return [rule for rule in existing if rule.get('ID') != RULE['ID']] + [RULE]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    import boto3
    config = settings()
    client = boto3.client('s3', region_name=config['region'])
    try:
        response = client.get_bucket_lifecycle_configuration(Bucket=config['bucket'])
        current = response['Rules']
    except client.exceptions.ClientError as error:
        if error.response['Error']['Code'] != 'NoSuchLifecycleConfiguration':
            raise
        current = []
        response = {}
    desired = desired_rules(current)
    print(json.dumps({'bucket': config['bucket'], 'before': current, 'after': desired, 'apply': args.apply}, indent=2))
    if args.apply:
        defaults = {key: response[key] for key in ('TransitionDefaultMinimumObjectSize',) if key in response}
        client.put_bucket_lifecycle_configuration(Bucket=config['bucket'], LifecycleConfiguration={'Rules': desired}, **defaults)
        if client.get_bucket_lifecycle_configuration(Bucket=config['bucket'])['Rules'] != desired:
            raise RuntimeError('lifecycle readback mismatch')


if __name__ == '__main__':
    main()
