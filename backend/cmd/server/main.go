// Command server is the RTSP → WebSocket streaming gateway.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/omkarabagade/rtsp-stream-viewer/backend/internal/config"
	"github.com/omkarabagade/rtsp-stream-viewer/backend/internal/server"
	"github.com/omkarabagade/rtsp-stream-viewer/backend/internal/stream"
)

func main() {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	cfg := config.Load()
	if _, err := exec.LookPath(cfg.FFmpegPath); err != nil {
		log.Error("ffmpeg not found; install it or set FFMPEG_PATH", "path", cfg.FFmpegPath)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mgr := stream.NewManager(ctx, stream.HubOptions{
		FFmpegPath:         cfg.FFmpegPath,
		Transcode:          cfg.Transcode,
		TranscodeMaxHeight: cfg.TranscodeMaxHeight,
		ConnectTimeout:     cfg.ConnectTimeout,
		StallTimeout:       cfg.StallTimeout,
		IdleTimeout:        cfg.IdleTimeout,
		MaxViewers:         cfg.MaxViewersPerStream,
	}, cfg.MaxStreams, cfg.AllowedHosts, log)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(cfg, mgr, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", cfg.Addr, "static", cfg.StaticDir, "transcode", cfg.Transcode,
			"max_streams", cfg.MaxStreams, "demo_streams", len(cfg.DemoStreams))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	mgr.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
