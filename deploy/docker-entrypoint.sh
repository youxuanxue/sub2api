#!/bin/sh
set -e

# Fix data directory permissions when running as root.
# Docker named volumes / host bind-mounts may be owned by root,
# preventing the non-root sub2api user from writing files.
if [ "$(id -u)" = "0" ]; then
    mkdir -p /app/data
    # Stage0 host bootstrap owns the bind-mount root as uid/gid 1000. Skip the
    # recursive walk there: a large ops_dlq tree can otherwise delay startup by
    # minutes. Other deployments keep the compatibility ownership repair.
    data_owner="$(stat -c '%u:%g' /app/data 2>/dev/null || true)"
    if [ "${SKIP_DATA_CHOWN:-0}" != "1" ] && [ "${data_owner}" != "1000:1000" ]; then
        # Use || true to avoid failure on read-only mounted files (e.g. config.yaml:ro)
        chown -R sub2api:sub2api /app/data 2>/dev/null || true
    fi
    # Re-invoke this script as sub2api so the flag-detection below
    # also runs under the correct user.
    exec su-exec sub2api "$0" "$@"
fi

# Compatibility: if the first arg looks like a flag (e.g. --help),
# prepend the default binary so it behaves the same as the old
# ENTRYPOINT ["/app/sub2api"] style.
if [ "${1#-}" != "$1" ]; then
    set -- /app/sub2api "$@"
fi

exec "$@"
