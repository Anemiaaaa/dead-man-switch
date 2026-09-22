package notify

import (
	"context"
	"log/slog"
)

// Log writes reminders to the structured log.
//
// The default notifier: it makes the keeper useful before anyone has set up a
// bot token, and it doubles as the audit trail for the other channels.
type Log struct {
	Logger *slog.Logger
}

func NewLog(logger *slog.Logger) *Log {
	return &Log{Logger: logger}
}

func (l *Log) Notify(_ context.Context, r Reminder) error {
	l.Logger.Warn("vault needs attention",
		"vault", r.Vault.String(),
		"owner", r.Owner.String(),
		"status", string(r.Status),
		"deadline", r.Deadline,
		"time_left", r.TimeLeft.String(),
		"message", r.Message(),
	)
	return nil
}
