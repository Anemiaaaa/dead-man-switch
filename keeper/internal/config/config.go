// Package config reads the keeper's settings from the environment.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
)

// Config is everything the keeper needs to start.
type Config struct {
	RPCEndpoint string
	ProgramID   solana.PublicKey

	ScanInterval time.Duration

	// Thresholds are how far ahead of a deadline to warn, one reminder each.
	Thresholds []time.Duration

	// DueSoon is when the API starts calling a vault urgent. Defaults to the
	// widest threshold so the two agree by default.
	DueSoon time.Duration

	ListenAddr string

	// PostgresDSN is optional; without it the keeper caches in memory.
	PostgresDSN string

	TelegramToken  string
	TelegramChatID string
}

// Default values chosen so `go run ./cmd/keeper` works against devnet with no
// environment at all.
const (
	defaultRPC      = "https://api.devnet.solana.com"
	defaultProgram  = "9tfSr7zg9bGBfpSqqdwCACiSfAqsbdE4rNnwezFe5Ldm"
	defaultInterval = 5 * time.Minute
	defaultListen   = ":8080"
)

var defaultThresholds = []time.Duration{
	14 * 24 * time.Hour,
	7 * 24 * time.Hour,
	24 * time.Hour,
	time.Hour,
}

// Load reads the environment and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		RPCEndpoint:    env("DMS_RPC_ENDPOINT", defaultRPC),
		ListenAddr:     env("DMS_LISTEN_ADDR", defaultListen),
		PostgresDSN:    os.Getenv("DMS_POSTGRES_DSN"),
		TelegramToken:  os.Getenv("DMS_TELEGRAM_TOKEN"),
		TelegramChatID: os.Getenv("DMS_TELEGRAM_CHAT_ID"),
	}

	programID, err := solana.PublicKeyFromBase58(env("DMS_PROGRAM_ID", defaultProgram))
	if err != nil {
		return nil, fmt.Errorf("config: DMS_PROGRAM_ID: %w", err)
	}
	cfg.ProgramID = programID

	if cfg.ScanInterval, err = duration("DMS_SCAN_INTERVAL", defaultInterval); err != nil {
		return nil, err
	}
	if cfg.ScanInterval < time.Second {
		return nil, fmt.Errorf("config: DMS_SCAN_INTERVAL must be at least 1s, got %s", cfg.ScanInterval)
	}

	if cfg.Thresholds, err = thresholds(); err != nil {
		return nil, err
	}
	cfg.DueSoon = cfg.Thresholds[0]

	if (cfg.TelegramToken == "") != (cfg.TelegramChatID == "") {
		return nil, fmt.Errorf("config: set both DMS_TELEGRAM_TOKEN and DMS_TELEGRAM_CHAT_ID, or neither")
	}

	return cfg, nil
}

// thresholds parses a comma-separated list like "14d" — written as Go
// durations, so "336h,168h,24h".
func thresholds() ([]time.Duration, error) {
	raw := os.Getenv("DMS_REMIND_AT")
	if raw == "" {
		return defaultThresholds, nil
	}

	var out []time.Duration
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		d, err := time.ParseDuration(part)
		if err != nil {
			return nil, fmt.Errorf("config: DMS_REMIND_AT: %q: %w", part, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: DMS_REMIND_AT: %q must be positive", part)
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("config: DMS_REMIND_AT is set but lists no durations")
	}

	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })

	return out, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return d, nil
}
