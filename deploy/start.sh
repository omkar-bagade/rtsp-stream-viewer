#!/bin/sh
# Entrypoint for the all-in-one image: optionally starts MediaMTX + looping
# FFmpeg publishers that provide demo RTSP streams, then runs the Go server.
set -eu

if [ "${DEMO_MODE:-true}" = "true" ]; then
  mediamtx /app/mediamtx-demo.yml &
  sleep 1

  publish() { # $1 = clip file, $2 = path
    while true; do
      # -c copy: re-publishing a pre-encoded clip costs almost no CPU.
      ffmpeg -hide_banner -loglevel error -nostdin -re -stream_loop -1 -i "/app/clips/$1" \
        -c copy -f rtsp -rtsp_transport tcp "rtsp://127.0.0.1:8554/$2" || true
      sleep 2
    done
  }
  publish testsrc.mp4 test-pattern &
  publish bars.mp4 color-bars &
  publish mandelbrot.mp4 mandelbrot &

  : "${DEMO_STREAMS:=Test pattern 720p|rtsp://127.0.0.1:8554/test-pattern,Color bars 480p|rtsp://127.0.0.1:8554/color-bars,Mandelbrot 360p|rtsp://127.0.0.1:8554/mandelbrot}"
  export DEMO_STREAMS
fi

exec /app/server
