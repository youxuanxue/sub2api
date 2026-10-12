#!/usr/bin/env bash
# Shared by the local deploy renderer and the delivered host script.
# Values are coordinates or private file paths, never credential contents.
QA_RUNTIME_KEYS=(
  QA_ARCHIVE_STORAGE_ENDPOINT QA_ARCHIVE_STORAGE_FORCE_PATH_STYLE QA_ARCHIVE_STORAGE_CREDENTIALS_FILE
  QA_ARCHIVE_STORAGE_REGION QA_ARCHIVE_STORAGE_BUCKET QA_ARCHIVE_STORAGE_PREFIX
)
