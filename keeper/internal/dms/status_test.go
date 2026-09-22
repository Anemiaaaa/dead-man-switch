package dms

import (
	"testing"
	"time"
)

func TestHumanDurationPluralises(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "less than a minute"},
		{time.Minute, "1 minute"},
		{90 * time.Second, "1 minute"},
		{2 * time.Minute, "2 minutes"},
		{59 * time.Minute, "59 minutes"},
		{time.Hour, "1 hour"},
		{90 * time.Minute, "1 hour"},
		{5 * time.Hour, "5 hours"},
		{47 * time.Hour, "47 hours"},
		{48 * time.Hour, "2 days"},
		{30 * 24 * time.Hour, "30 days"},
	}

	for _, c := range cases {
		if got := HumanDuration(c.in); got != c.want {
			t.Errorf("HumanDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStatusFollowsTheClock(t *testing.T) {
	start := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	vault := &Vault{LastCheckIn: start, Timeout: 30 * 24 * time.Hour}
	dueSoon := 14 * 24 * time.Hour

	cases := []struct {
		name string
		at   time.Time
		want Status
	}{
		{"just opened", start, StatusActive},
		{"a fortnight out, to the second", vault.Deadline().Add(-dueSoon), StatusDueSoon},
		{"one second before the deadline", vault.Deadline().Add(-time.Second), StatusDueSoon},
		{"on the deadline", vault.Deadline(), StatusExpired},
		{"well past it", vault.Deadline().Add(365 * 24 * time.Hour), StatusExpired},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vault.Status(c.at, dueSoon); got != c.want {
				t.Errorf("status = %s, want %s", got, c.want)
			}
		})
	}
}

// A claimed vault is beyond warning about, whatever its timer says.
func TestAClaimedVaultIsAlwaysTriggered(t *testing.T) {
	start := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	vault := &Vault{LastCheckIn: start, Timeout: 30 * 24 * time.Hour, IsClaimed: true}

	for _, at := range []time.Time{start, vault.Deadline(), vault.Deadline().Add(time.Hour)} {
		if got := vault.Status(at, time.Hour); got != StatusTriggered {
			t.Errorf("at %s status = %s, want triggered", at, got)
		}
	}
	if NeedsAttention(StatusTriggered) {
		t.Error("a triggered vault should not be flagged for attention")
	}
}
