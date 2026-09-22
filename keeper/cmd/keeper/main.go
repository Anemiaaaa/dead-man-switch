// Command keeper watches Dead Man's Switch vaults, reminds owners before their
// deadlines, and serves a read-only index of what it sees.
//
// It never signs a transaction. `claim` is permissionless and `check_in`
// belongs to the owner, so the protocol works whether or not this process is
// running — the keeper only solves the UX problem of forgetting to check in.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/api"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/config"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/notify"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/store"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/watch"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("keeper stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	vaultStore, closeStore, err := openStore(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeStore()

	notifier := buildNotifier(cfg, logger)

	watcher := &watch.Watcher{
		Source:   dms.NewClient(cfg.RPCEndpoint, cfg.ProgramID),
		Store:    vaultStore,
		Notifier: notifier,
		Tracker:  watch.NewTracker(cfg.Thresholds),
		Interval: cfg.ScanInterval,
		Logger:   logger,
	}

	server := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: (&api.API{
			Store:   vaultStore,
			DueSoon: cfg.DueSoon,
			Logger:  logger,
		}).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("keeper starting",
		"program", cfg.ProgramID.String(),
		"rpc", cfg.RPCEndpoint,
		"interval", cfg.ScanInterval.String(),
		"listen", cfg.ListenAddr,
	)

	errs := make(chan error, 2)

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	go func() { errs <- watcher.Run(ctx) }()

	select {
	case err := <-errs:
		stop()
		shutdown(server, logger)
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdown(server, logger)
		return nil
	}
}

func openStore(ctx context.Context, cfg *config.Config, logger *slog.Logger) (store.Store, func(), error) {
	if cfg.PostgresDSN == "" {
		logger.Info("caching vaults in memory; set DMS_POSTGRES_DSN to persist")
		return store.NewMemory(), func() {}, nil
	}

	pg, err := store.NewPostgres(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("caching vaults in postgres")

	return pg, pg.Close, nil
}

func buildNotifier(cfg *config.Config, logger *slog.Logger) notify.Notifier {
	// The log channel always stays on: it is the record of what the other
	// channels were asked to send.
	channels := notify.Multi{notify.NewLog(logger)}

	if cfg.TelegramToken != "" {
		channels = append(channels, notify.NewTelegram(cfg.TelegramToken, cfg.TelegramChatID))
		logger.Info("telegram reminders enabled")
	}

	return channels
}

func shutdown(server *http.Server, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("http shutdown", "error", err)
	}
}
