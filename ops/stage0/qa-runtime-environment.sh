#!/usr/bin/env bash
# Shared by the local deploy renderer and the delivered host script.
# Values are coordinates or private file paths, never credential contents.
# New AWS prod archives new raw data to regional AWS S3. Keep coordinates in
# one owner so the blue/green renderer and its delivered host script enforce
# the same provider contract.
QA_LIVE_ARCHIVE_STORAGE_DRIVER="s3"
QA_LIVE_ARCHIVE_STORAGE_REGION="us-east-1"
QA_LIVE_ARCHIVE_STORAGE_BUCKET="tokenkey-prod-qa-raw-archive-682751977094"
QA_LIVE_ARCHIVE_STORAGE_ENDPOINT="https://s3.us-east-1.amazonaws.com"
QA_LIVE_ARCHIVE_STORAGE_PREFIX="raw/v1"

QA_RUNTIME_KEYS=(
  QA_ARCHIVE_STORAGE_ENDPOINT QA_ARCHIVE_STORAGE_FORCE_PATH_STYLE QA_ARCHIVE_STORAGE_CREDENTIALS_FILE
  QA_ARCHIVE_STORAGE_REGION QA_ARCHIVE_STORAGE_BUCKET QA_ARCHIVE_STORAGE_PREFIX
)
