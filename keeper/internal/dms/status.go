package dms

import "time"

// Status is the one-word summary the API and the reminders work from.
type Status string

const (
	// StatusActive means the owner has checked in recently enough.
	StatusActive Status = "active"

	// StatusDueSoon means the deadline is close enough to be worth a nudge.
	StatusDueSoon Status = "due_soon"

	// StatusExpired means heirs could claim right now, but none has yet.
	//
	// The program still lets the owner check in here: being late is not the
	// same as being dead. That makes this the loudest state the keeper can
	// report — the owner has not lost the vault, but they are one transaction
	// away from it.
	StatusExpired Status = "expired"

	// StatusTriggered means at least one heir has claimed. The owner is now
	// locked out for good and the keeper has nothing left to warn about.
	StatusTriggered Status = "triggered"
)

// Status classifies a vault as of `now`.
//
// `dueSoon` is how far ahead of the deadline the keeper starts calling a vault
// urgent, and comes from configuration rather than from the chain.
func (v *Vault) Status(now time.Time, dueSoon time.Duration) Status {
	switch {
	case v.IsClaimed:
		return StatusTriggered
	case !v.Deadline().After(now):
		return StatusExpired
	case v.TimeLeft(now) <= dueSoon:
		return StatusDueSoon
	default:
		return StatusActive
	}
}

// NeedsAttention reports whether a status is one the owner should hear about.
func NeedsAttention(s Status) bool {
	return s == StatusDueSoon || s == StatusExpired
}
