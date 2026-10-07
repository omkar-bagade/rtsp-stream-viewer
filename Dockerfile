# All-in-one image: React UI + Go streaming server (+ optional demo RTSP
# streams via MediaMTX). Used for Render/Railway/Fly deployments.

# ---------- 1. frontend ----------
FROM node:22-alpine AS web
WORKDIR /web
COPY frontend/package*.json ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY frontend/ ./
# Same-origin: the Go server serves the UI, so no backend URL is needed.
ENV VITE_BACKEND_URL=""
RUN npm run build

# ---------- 2. backend ----------
FROM golang:1.24-alpine AS api
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---------- 3. MediaMTX (demo streams only) ----------
FROM alpine:3.20 AS mediamtx
ARG MEDIAMTX_VERSION=1.15.1
ARG TARGETARCH=amd64
RUN apk add --no-cache curl tar && \
    case "$TARGETARCH" in arm64) ARCH=arm64 ;; *) ARCH=amd64 ;; esac && \
    curl -fsSL "https://github.com/bluenviron/mediamtx/releases/download/v${MEDIAMTX_VERSION}/mediamtx_v${MEDIAMTX_VERSION}_linux_${ARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin mediamtx

# ---------- 4. runtime ----------
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tini && adduser -D -H app
WORKDIR /app

# Pre-encode short demo clips at build time so the running container only
# re-publishes them (-c copy) and stays within tiny free-tier CPU budgets.
RUN mkdir clips && \
    ffmpeg -hide_banner -loglevel error -f lavfi -i "testsrc2=size=1280x720:rate=25" -t 30 \
      -c:v libx264 -preset veryfast -profile:v main -g 50 -pix_fmt yuv420p -b:v 1500k clips/testsrc.mp4 && \
    ffmpeg -hide_banner -loglevel error -f lavfi -i "testsrc=size=854x480:rate=25" -t 30 \
      -c:v libx264 -preset veryfast -profile:v main -g 50 -pix_fmt yuv420p -b:v 800k clips/bars.mp4 && \
    ffmpeg -hide_banner -loglevel error -f lavfi -i "mandelbrot=size=640x360:rate=25" -t 30 \
      -c:v libx264 -preset veryfast -profile:v baseline -g 50 -pix_fmt yuv420p -b:v 700k clips/mandelbrot.mp4

COPY --from=mediamtx /usr/local/bin/mediamtx /usr/local/bin/mediamtx
COPY --from=api /out/server /app/server
COPY --from=web /web/dist /app/public
COPY deploy/mediamtx-demo.yml /app/mediamtx-demo.yml
COPY deploy/start.sh /app/start.sh

ENV STATIC_DIR=/app/public \
    PORT=8080 \
    DEMO_MODE=true
USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:${PORT}/healthz || exit 1
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/app/start.sh"]
