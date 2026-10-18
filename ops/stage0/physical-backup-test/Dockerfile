# Only the isolated protocol rehearsal needs an object-store server.
FROM tokenkey-postgres-backup:dev
RUN apk add --no-cache minio=0.20251015.172955-r8 \
    && mkdir -p /etc/minio/certs
