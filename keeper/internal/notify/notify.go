// Package notify delivers deadline reminders to whoever the owner is reachable
// at.
package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

// Reminder is one thing the owner needs to hear about one vault.
type Reminder struct {
	Vault    solana.PublicKey
	Owner    solana.PublicKey
	Status   dms.Status
	Deadline time.Time
	TimeLeft time.Duration
}

// Notifier delivers a reminder. Implementations should be safe to call from
// several goroutines.
type Notifier interface {
	Notify(ctx context.Context, r Reminder) error
}

// Message is the human-readable text a notifier sends.
func (r Reminder) Message() string {
	if r.Status == dms.StatusExpired {
		return fmt.Sprintf(
			"Your Dead Man's Switch vault %s passed its deadline %s ago. "+
				"Your heirs can claim it right now. Check in to stop that — "+
				"a check-in still works until the first claim lands.",
			short(r.Vault), dms.HumanDuration(-r.TimeLeft),
		)
	}

	return fmt.Sprintf(
		"Your Dead Man's Switch vault %s unlocks for your heirs in %s (%s UTC). "+
			"Check in to reset the timer.",
		short(r.Vault), dms.HumanDuration(r.TimeLeft), r.Deadline.Format(time.DateTime),
	)
}

// short abbreviates a key the way explorers do.
func short(key solana.PublicKey) string {
	s := key.String()
	if len(s) <= 12 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// Multi fans a reminder out to several notifiers, and reports every failure
// rather than stopping at the first.
type Multi []Notifier

func (m Multi) Notify(ctx context.Context, r Reminder) error {
	var failures []error
	for _, n := range m {
		if err := n.Notify(ctx, r); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("notify: %d of %d channels failed: %w", len(failures), len(m), failures[0])
}
