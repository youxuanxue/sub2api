#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
  printf 'docker runtime resources test failed: %s\n' "$1" >&2
  exit 1
}

assert_line() {
  file=$1
  line=$2
  grep -Fqx "$line" "$file" || fail "$file is missing: $line"
}

assert_goreleaser_docker_extra_file() {
  file=$1
  awk '
    /^dockers_v2:/ { in_dockers = 1 }
    in_dockers && /^    extra_files:/ { in_extra_files = 1; next }
    in_extra_files && $0 == "      - backend/resources" { found = 1 }
    in_extra_files && !/^      - / { in_extra_files = 0 }
    END { exit found ? 0 : 1 }
  ' "$file" || fail "$file dockers_v2.extra_files is missing backend/resources"
}

test -s backend/resources/model-pricing/model_prices_and_context_window.json || \
  fail 'fallback pricing data is missing or empty'

assert_line Dockerfile.goreleaser 'COPY --chown=sub2api:sub2api backend/resources /app/resources'
assert_line deploy/Dockerfile 'COPY --from=backend-builder --chown=sub2api:sub2api /app/backend/resources /app/resources'
assert_goreleaser_docker_extra_file .goreleaser.yaml
assert_goreleaser_docker_extra_file .goreleaser.simple.yaml

printf 'docker runtime resources test passed\n'
