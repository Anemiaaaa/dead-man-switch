// Command blink serves the Dead Man's Switch as a Solana Action, so a
// check-in or a claim is one tap from a link, a QR code or a feed.
//
// It signs nothing and holds no keys: it hands an unsigned transaction to the
// user's own wallet, which is the only thing that ever sees a private key.
//
// A blink has to be reachable over public HTTPS — a client cannot fetch
// localhost. For a demo, point a tunnel at it and set DMS_BLINK_BASE_URL to
// the public address the tunnel gives you.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/blink"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/client"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("blink server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	var (
		endpoint = env("DMS_RPC_ENDPOINT", "https://api.devnet.solana.com")
		program  = env("DMS_PROGRAM_ID", "9tfSr7zg9bGBfpSqqdwCACiSfAqsbdE4rNnwezFe5Ldm")
		listen   = env("DMS_BLINK_LISTEN_ADDR", ":8081")
		baseURL  = strings.TrimSuffix(env("DMS_BLINK_BASE_URL", "http://localhost:8081"), "/")
		cluster  = env("DMS_CLUSTER", "devnet")
	)

	programID, err := solana.PublicKeyFromBase58(program)
	if err != nil {
		return err
	}

	reader := dms.NewClient(endpoint, programID)
	server := &blink.Server{
		Build:     client.NewInstructions(programID),
		Vaults:    reader,
		Blockhash: reader,
		BaseURL:   baseURL,
		Cluster:   cluster,
		Logger:    logger,
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("blink server starting",
		"program", programID.String(),
		"rpc", endpoint,
		"cluster", cluster,
		"listen", listen,
		"base_url", baseURL,
	)
	logger.Info("share this once the host is public",
		"check_in", "https://dial.to/?action=solana-action:"+baseURL+"/api/actions/check-in",
		"claim", "https://dial.to/?action=solana-action:"+baseURL+"/api/actions/claim",
	)

	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
