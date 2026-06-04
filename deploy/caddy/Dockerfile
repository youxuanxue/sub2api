# TokenKey custom Caddy image — stock Caddy + the per-IP rate-limit module.
#
# WHY this exists: the standard `caddy:2-alpine` image does NOT ship
# github.com/mholt/caddy-ratelimit, so the `rate_limit` directive in our prod
# Caddyfile (deploy/aws/stage0/Caddyfile) would be an "unknown directive" and
# Caddy would refuse to start. We bake the module in via xcaddy.
#
# Built + pushed by .github/workflows/caddy-image.yml as a MULTI-ARCH manifest
# (linux/amd64 + linux/arm64). arm64 is mandatory: prod / Edge Stage0 run on
# AWS Graviton (arm64); a single-arch amd64 image would crash with
# `exec format error` on those hosts (same trap as release.yml rule §9.1).
#
# Pin the Caddy version explicitly so the image is reproducible; bump CADDY_VERSION
# and re-run the workflow to take a new Caddy release (e.g. for a CVE).
ARG CADDY_VERSION=2.11.4

FROM caddy:${CADDY_VERSION}-builder AS builder
RUN xcaddy build \
    --with github.com/mholt/caddy-ratelimit

FROM caddy:${CADDY_VERSION}-alpine
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
