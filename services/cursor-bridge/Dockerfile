ARG NODE_IMAGE=node:24.20.0-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e
FROM ${NODE_IMAGE}
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund
COPY upstream/package.json upstream/package-lock.json ./upstream/
RUN npm --prefix upstream ci --omit=dev --ignore-scripts --no-audit --no-fund
COPY upstream ./upstream
COPY *.mjs package.json VENDORED_FROM.md ./
ENV CURSOR_AGENT_SIDECAR_HOST=0.0.0.0 \
    CURSOR_AGENT_SIDECAR_PORT=3927 \
    CURSOR_AGENT_WORKSPACE=/tmp/cursor-workspace
USER node
EXPOSE 3927
HEALTHCHECK --interval=30s --timeout=3s CMD node -e "fetch('http://127.0.0.1:3927/health').then(async r=>{if(!r.ok || !(await r.json()).accepting)process.exit(1)}).catch(()=>process.exit(1))"
CMD ["node", "server.tk.mjs"]
