package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"internal-image-studio/internal/blob"
	"internal-image-studio/internal/config"
	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
	"internal-image-studio/internal/safehttp"
	studioWorker "internal-image-studio/internal/worker"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if workerHealthcheck() != nil {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.LoadWorker()
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
	logger.Info("worker model catalog validated", "model_revision", catalog.Hash)
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

	adapters := map[string]provider.Adapter{}
	if cfg.ProviderMode == "mock" {
		adapters["legnext"] = provider.Mock{}
		adapters["openrouter"] = provider.Mock{}
		adapters["bfl"] = provider.Mock{}
	} else {
		adapters["legnext"] = provider.NewLegnext(cfg.LegnextAPIKey)
		adapters["openrouter"] = provider.NewOpenRouter(cfg.OpenRouterAPIKey, cfg.PublicURL)
		adapters["bfl"] = provider.NewBFL(cfg.BFLAPIKey)
	}
	downloadClient := safehttp.NewDownloadClient(90 * time.Second)
	generateWorker := &studioWorker.GenerateWorker{
		DB: db, Config: cfg, Blobs: store, Adapters: adapters,
		ProviderSem: map[string]chan struct{}{"legnext": make(chan struct{}, 2), "openrouter": make(chan struct{}, 4), "bfl": make(chan struct{}, 4)},
		IngestSem:   make(chan struct{}, 4), ThumbSem: make(chan struct{}, 2), HTTPClient: downloadClient, Log: logger, Breaker: studioWorker.NewBreaker(),
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, generateWorker)
	riverClient, err := river.NewClient(riverpgxv5.New(db), &river.Config{
		Queues: map[string]river.QueueConfig{"generation": {MaxWorkers: 6}}, Workers: workers,
		// REINDEX requires object ownership and is an operational migration task,
		// not a privilege the runtime Worker should inherit from studio_owner.
		ReindexerSchedule: river.NeverSchedule(),
	})
	if err != nil {
		logger.Error("river client failed", "error", err)
		os.Exit(1)
	}
	wake := make(chan struct{}, 1)
	scheduler := &studioWorker.Scheduler{DB: db, River: riverClient, Log: logger, Wake: wake, AssetRoot: cfg.AssetRoot}
	go scheduler.Run(ctx)
	go scheduler.ListenNotifications(ctx)
	go (&studioWorker.Maintenance{DB: db, Blobs: store, AssetRoot: cfg.AssetRoot, Log: logger, Generator: generateWorker}).Run(ctx)
	deletionWake := make(chan struct{}, 1)
	deletions := &studioWorker.DeletionProcessor{DB: db, Blobs: store, AssetRoot: cfg.AssetRoot, Log: logger, Wake: deletionWake}
	go deletions.Run(ctx)
	go deletions.ListenNotifications(ctx)
	go (&studioWorker.UploadValidator{DB: db, Blobs: store, AssetRoot: cfg.AssetRoot, Generator: generateWorker, Log: logger}).Run(ctx)
	go (&studioWorker.ProviderProber{DB: db, Adapters: adapters, Log: logger}).Run(ctx)
	if err := riverClient.Start(ctx); err != nil {
		logger.Error("river start failed", "error", err)
		os.Exit(1)
	}
	instanceID, err := os.Hostname()
	if err != nil || instanceID == "" {
		logger.Error("worker hostname unavailable")
		os.Exit(1)
	}
	go (&studioWorker.Heartbeat{DB: db, Log: logger, ServiceName: studioWorker.WorkerServiceName, InstanceID: instanceID}).Run(ctx)
	logger.Info("worker started", "provider_mode", cfg.ProviderMode)
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := riverClient.Stop(stopCtx); err != nil {
		logger.Error("worker stop timed out", "error", err)
	}
}

func workerHealthcheck() error {
	databaseURL, err := config.DatabaseURLFromEnv()
	if err != nil {
		return errHealthcheckConfiguration
	}
	instanceID, err := os.Hostname()
	if err != nil || instanceID == "" {
		return errHealthcheckConfiguration
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return errHealthcheckConfiguration
	}
	poolConfig.MaxConns = 1
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errHealthcheckDatabase
	}
	defer db.Close()
	if err := studioWorker.CheckHeartbeat(ctx, db, studioWorker.WorkerServiceName, instanceID, studioWorker.WorkerHeartbeatMaxAge); err != nil {
		return errHealthcheckDatabase
	}
	return nil
}

var (
	errHealthcheckConfiguration = errors.New("worker healthcheck configuration is invalid")
	errHealthcheckDatabase      = errors.New("worker healthcheck failed")
)
