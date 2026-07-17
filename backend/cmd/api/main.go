package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/blob"
	"internal-image-studio/internal/config"
	"internal-image-studio/internal/httpapi"
	"internal-image-studio/internal/modelconfig"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://127.0.0.1:8081/health/ready")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		response.Body.Close()
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadAPI()
	if err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	catalog, err := modelconfig.Load(cfg.ModelConfigPath)
	if err != nil {
		logger.Error("model config invalid", "error", err)
		os.Exit(1)
	}
	store, err := blob.NewLocal(cfg.AssetRoot)
	if err != nil {
		logger.Error("storage unavailable", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		logger.Error("database URL invalid", "error", err)
		os.Exit(1)
	}
	poolConfig.MaxConns = 20
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	// The request body timeout must cover a 25 MiB streamed upload on an
	// ordinary remote connection. Header parsing remains tightly bounded and
	// Nginx separately caps upload connections and body idle time.
	server := &http.Server{Addr: ":8081", Handler: httpapi.New(ctx, cfg, db, catalog, store, logger).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 0, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10}
	go func() {
		logger.Info("api started", "address", server.Addr, "model_revision", catalog.Hash)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("api stopped unexpectedly", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("api graceful shutdown failed", "error", err)
	}
}
