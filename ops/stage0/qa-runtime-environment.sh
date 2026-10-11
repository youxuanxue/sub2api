#!/usr/bin/env bash
# Shared by the local deploy renderer and the delivered host script.
# Values are coordinates or private file paths, never credential contents.
QA_RUNTIME_KEYS=(
  QA_BUNDLE_QUEUE_DRIVER QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE
  QA_BUNDLE_STORAGE_ENDPOINT QA_BUNDLE_STORAGE_FORCE_PATH_STYLE QA_BUNDLE_STORAGE_CREDENTIALS_FILE
  QA_ARCHIVE_STORAGE_ENDPOINT QA_ARCHIVE_STORAGE_FORCE_PATH_STYLE QA_ARCHIVE_STORAGE_CREDENTIALS_FILE
  QA_ARCHIVE_STORAGE_REGION QA_ARCHIVE_STORAGE_BUCKET QA_ARCHIVE_STORAGE_PREFIX
)

validate_qa_queue_environment() {
  case "${QA_BUNDLE_QUEUE_DRIVER:-sqs}" in
    sqs)
      [[ -n "${QA_BUNDLE_QUEUE_URL:-}" ]] || { echo 'QA_BUNDLE_QUEUE_URL is required for sqs' >&2; return 1; }
      [[ -z "${QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE:-}" ]] || { echo 'postgres DSN is invalid for sqs' >&2; return 1; }
      ;;
    postgres)
      [[ -z "${QA_BUNDLE_QUEUE_URL:-}" ]] || { echo 'QA_BUNDLE_QUEUE_URL must be empty for postgres' >&2; return 1; }
      [[ "${QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE:-}" = /run/secrets/tokenkey-qa/queue.dsn ]] || {
        echo 'postgres requires /run/secrets/tokenkey-qa/queue.dsn' >&2; return 1;
      }
      ;;
    *) echo 'unsupported QA_BUNDLE_QUEUE_DRIVER' >&2; return 1 ;;
  esac
}
