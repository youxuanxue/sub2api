#!/usr/bin/env bash
# Shared by the local deploy renderer and the delivered host script.
# Values are coordinates or private file paths, never credential contents.
# The live QA raw archive is Hetzner Object Storage. Keep these coordinates in
# one owner so the blue/green renderer and its delivered host script enforce
# the same provider contract.
QA_LIVE_ARCHIVE_STORAGE_DRIVER="s3"
QA_LIVE_ARCHIVE_STORAGE_REGION="fsn1"
QA_LIVE_ARCHIVE_STORAGE_BUCKET="tokenkey-qa-raw-fsn1-dd2ca9019b22"
QA_LIVE_ARCHIVE_STORAGE_ENDPOINT="https://fsn1.your-objectstorage.com"
QA_LIVE_ARCHIVE_STORAGE_PREFIX="raw/v1"

QA_RUNTIME_KEYS=(
  QA_ARCHIVE_STORAGE_ENDPOINT QA_ARCHIVE_STORAGE_FORCE_PATH_STYLE QA_ARCHIVE_STORAGE_CREDENTIALS_FILE
  QA_ARCHIVE_STORAGE_REGION QA_ARCHIVE_STORAGE_BUCKET QA_ARCHIVE_STORAGE_PREFIX
)
