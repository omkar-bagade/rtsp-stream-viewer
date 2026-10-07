#!/usr/bin/env bash
# Starts MediaMTX and publishes live FFmpeg test patterns to it.
#
#   ./test-streams/start-test-streams.sh            # 3 streams
#   NUM_STREAMS=8 ./test-streams/start-test-streams.sh
#
# Needs: ffmpeg. MediaMTX is used from PATH, else Docker, else it is
# downloaded automatically into test-streams/.bin (one-time, ~20 MB).
#
# Streams: rtsp://localhost:8554/cam1 … camN  (+ cam-hevc, an H.265 stream
# that exercises the backend's automatic transcoding).
set -euo pipefail
cd "$(dirname "$0")"

NUM_STREAMS="${NUM_STREAMS:-3}"
MEDIAMTX_VERSION="1.15.1"
PIDS=()

cleanup() {
  echo
  echo "Stopping…"
  # Length check avoids `set -u` errors on an empty array (macOS bash 3.2)
  if [ ${#PIDS[@]} -gt 0 ]; then kill "${PIDS[@]}" 2>/dev/null || true; fi
}
trap cleanup EXIT INT TERM

if ! command -v ffmpeg >/dev/null; then
  echo "FFmpeg is required. Install it with: brew install ffmpeg  (or apt install ffmpeg)" >&2
  exit 1
fi

# Download the MediaMTX binary for this OS/CPU into test-streams/.bin
download_mediamtx() {
  local os arch url
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) echo "Unsupported OS for auto-download; install MediaMTX manually." >&2; return 1 ;;
  esac
  case "$(uname -m)" in
    arm64 | aarch64) arch=arm64 ;;
    x86_64 | amd64) arch=amd64 ;;
    *) echo "Unsupported CPU for auto-download; install MediaMTX manually." >&2; return 1 ;;
  esac
  url="https://github.com/bluenviron/mediamtx/releases/download/v${MEDIAMTX_VERSION}/mediamtx_v${MEDIAMTX_VERSION}_${os}_${arch}.tar.gz"
  echo "MediaMTX not found — downloading v${MEDIAMTX_VERSION} (${os}/${arch})…"
  mkdir -p .bin
  curl -fsSL "$url" | tar -xz -C .bin mediamtx
  chmod +x .bin/mediamtx
}

if command -v mediamtx >/dev/null; then
  MEDIAMTX="mediamtx"
elif [ -x .bin/mediamtx ]; then
  MEDIAMTX="./.bin/mediamtx"
elif command -v docker >/dev/null && docker info >/dev/null 2>&1; then
  MEDIAMTX="docker"
else
  download_mediamtx
  MEDIAMTX="./.bin/mediamtx"
fi

if [ "$MEDIAMTX" = "docker" ]; then
  docker run --rm --name rtsp-test-mediamtx -p 8554:8554 \
    -v "$PWD/mediamtx.yml:/mediamtx.yml:ro" "bluenviron/mediamtx:${MEDIAMTX_VERSION}" &
else
  "$MEDIAMTX" ./mediamtx.yml &
fi
PIDS+=($!)
sleep 2

SOURCES=("testsrc2=size=1280x720:rate=25" "smptehdbars=size=854x480:rate=25" "mandelbrot=size=640x360:rate=25" "testsrc=size=640x360:rate=25")
for i in $(seq 1 "$NUM_STREAMS"); do
  src="${SOURCES[$(( (i - 1) % ${#SOURCES[@]} ))]}"
  ffmpeg -hide_banner -loglevel error -re -f lavfi -i "$src" \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 50 -pix_fmt yuv420p -b:v 1500k \
    -f rtsp -rtsp_transport tcp "rtsp://localhost:8554/cam$i" &
  PIDS+=($!)
  echo "▶ rtsp://localhost:8554/cam$i  ($src)"
done

if ffmpeg -hide_banner -h encoder=libx265 2>/dev/null | grep -q "Encoder libx265"; then
  ffmpeg -hide_banner -loglevel error -re -f lavfi -i "testsrc2=size=640x360:rate=25" \
    -c:v libx265 -preset ultrafast -x265-params log-level=error -g 50 -pix_fmt yuv420p -b:v 600k \
    -f rtsp -rtsp_transport tcp "rtsp://localhost:8554/cam-hevc" &
  PIDS+=($!)
  echo "▶ rtsp://localhost:8554/cam-hevc  (H.265 → transcoded by backend)"
fi

echo
echo "Test streams running. Press Ctrl+C to stop."
wait
