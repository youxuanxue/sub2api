# Keep the PostgreSQL major, libc and data layout compatible with Stage0.
# The base digest and apk version are intentionally pinned together.
FROM postgres:18-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873
RUN apk add --no-cache pgbackrest=2.58.0-r0 \
    && install -d -o postgres -g postgres -m 0750 /var/log/pgbackrest /var/run/pgbackrest
