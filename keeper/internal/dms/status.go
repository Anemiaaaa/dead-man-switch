package dms

import (
	"strconv"
	"time"
)

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

// HumanDuration renders a duration the way a reminder or a card should say it
// out loud: one unit, rounded down, correctly pluralised.
//
// Shared rather than duplicated because both the reminders and the blink cards
// put it in front of a person, and "in 1 minutes" reads as carelessness
// wherever it appears.
func HumanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return plural(int(d.Hours()/24), "day")
	case d >= time.Hour:
		return plural(int(d.Hours()), "hour")
	case d >= time.Minute:
		return plural(int(d.Minutes()), "minute")
	default:
		return "less than a minute"
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
