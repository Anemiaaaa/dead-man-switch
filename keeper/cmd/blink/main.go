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
		listen   = listenAddr()
		// Empty on purpose: behind a proxy the server reads its public origin
		// from the forwarded headers, so the deployment does not have to know
		// its own hostname before it has one.
		baseURL = strings.TrimSuffix(os.Getenv("DMS_BLINK_BASE_URL"), "/")
		cluster = env("DMS_CLUSTER", "devnet")
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

	origin := baseURL
	if origin == "" {
		origin = "from the forwarded headers"
	}
	logger.Info("blink server starting",
		"program", programID.String(),
		"rpc", endpoint,
		"cluster", cluster,
		"listen", listen,
		"origin", origin,
	)
	if baseURL != "" {
		logger.Info("share these once the host is reachable",
			"check_in", "https://dial.to/?action=solana-action:"+baseURL+"/api/actions/check-in",
			"claim", "https://dial.to/?action=solana-action:"+baseURL+"/api/actions/claim",
		)
	}

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

// listenAddr picks the address to bind.
//
// Render, Koyeb, Cloud Run, Railway and Heroku all inject PORT and expect the
// process to bind exactly that. Honouring it means the same image deploys to
// any of them with no per-platform override — and an explicit
// DMS_BLINK_LISTEN_ADDR still wins for the cases where the platform is wrong
// or there is no platform at all.
func listenAddr() string {
	if addr := os.Getenv("DMS_BLINK_LISTEN_ADDR"); addr != "" {
		return addr
	}
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8081"
}
