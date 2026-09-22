// Package watch polls the chain for vaults and decides who needs telling.
package watch

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/notify"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/store"
)

// VaultSource is what the watcher needs from the chain — an interface so the
// tests can hand it a fixed set of vaults instead of an RPC node.
type VaultSource interface {
	Vaults(ctx context.Context) ([]*dms.Vault, error)
}

// Watcher scans on an interval, refreshes the cache, and sends reminders.
//
// It deliberately cannot sign anything. `claim` is permissionless and
// `check_in` belongs to the owner, so there is no transaction a keeper would
// ever need to send — and therefore no key it needs to hold.
type Watcher struct {
	Source   VaultSource
	Store    store.Store
	Notifier notify.Notifier
	Tracker  *Tracker
	Interval time.Duration
	Logger   *slog.Logger

	// Now is swappable so tests can place vaults anywhere along their timers.
	Now func() time.Time
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now().UTC()
}

// Run scans immediately and then on every tick, until the context is done.
//
// A failed scan is logged and retried on the next tick rather than returned:
// an RPC hiccup must not take the keeper — and with it everybody's
// reminders — offline.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()

	for {
		if err := w.Scan(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.Logger.Error("scan failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Scan does one pass: fetch, cache, remind.
func (w *Watcher) Scan(ctx context.Context) error {
	vaults, err := w.Source.Vaults(ctx)
	if err != nil {
		return fmt.Errorf("watch: fetching vaults: %w", err)
	}

	if err := w.Store.Replace(ctx, vaults); err != nil {
		return fmt.Errorf("watch: caching vaults: %w", err)
	}

	live := make(map[solana.PublicKey]struct{}, len(vaults))
	for _, vault := range vaults {
		live[vault.Address] = struct{}{}
	}
	w.Tracker.Forget(live)

	now := w.now()
	var reminders int
	for _, vault := range vaults {
		reminder := w.Tracker.Due(vault, now)
		if reminder == nil {
			continue
		}
		reminders++

		// A channel that is down should not stop the other owners' reminders.
		if err := w.Notifier.Notify(ctx, *reminder); err != nil {
			w.Logger.Error("could not deliver reminder",
				"vault", vault.Address.String(), "error", err)
		}
	}

	w.Logger.Info("scan complete", "vaults", len(vaults), "reminders", reminders)

	return nil
}
