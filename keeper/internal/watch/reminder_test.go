package watch

import (
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

var (
	day  = 24 * time.Hour
	base = time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
)

func vault(t *testing.T, lastCheckIn time.Time, timeout time.Duration) *dms.Vault {
	t.Helper()
	key, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	owner, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	return &dms.Vault{
		Address:     key.PublicKey(),
		Owner:       owner.PublicKey(),
		LastCheckIn: lastCheckIn,
		Timeout:     timeout,
	}
}

func tracker() *Tracker {
	return NewTracker([]time.Duration{14 * day, 7 * day, day, time.Hour})
}

func TestQuietWhileTheDeadlineIsFarOff(t *testing.T) {
	tr := tracker()
	v := vault(t, base, 30*day)

	if r := tr.Due(v, base.Add(day)); r != nil {
		t.Fatalf("29 days left should be quiet, got %+v", r)
	}
}

func TestOneReminderPerThreshold(t *testing.T) {
	tr := tracker()
	v := vault(t, base, 30*day)
	deadline := base.Add(30 * day)

	// Crossing 14 days.
	first := tr.Due(v, deadline.Add(-13*day))
	if first == nil {
		t.Fatal("crossing the 14-day threshold should remind")
	}
	if first.Status != dms.StatusDueSoon {
		t.Errorf("status = %s, want due_soon", first.Status)
	}

	// Still inside the same threshold — silence, however often we poll.
	for i := range 5 {
		if r := tr.Due(v, deadline.Add(-12*day).Add(time.Duration(i)*time.Minute)); r != nil {
			t.Fatalf("poll %d re-sent the same reminder: %+v", i, r)
		}
	}

	// Crossing 7 days is new information.
	second := tr.Due(v, deadline.Add(-6*day))
	if second == nil {
		t.Fatal("crossing the 7-day threshold should remind again")
	}

	// And so is the last day.
	if r := tr.Due(v, deadline.Add(-2*time.Hour)); r == nil {
		t.Fatal("crossing the 1-day threshold should remind again")
	}
}

// A keeper that was down for a week must not dump one message per missed
// threshold the moment it comes back.
func TestCatchingUpSendsOnlyTheMostUrgentReminder(t *testing.T) {
	tr := tracker()
	v := vault(t, base, 30*day)
	deadline := base.Add(30 * day)

	first := tr.Due(v, deadline.Add(-30*time.Minute))
	if first == nil {
		t.Fatal("want one reminder")
	}
	if got := first.TimeLeft.Round(time.Minute); got != 30*time.Minute {
		t.Errorf("time left = %s, want 30m", got)
	}

	if r := tr.Due(v, deadline.Add(-20*time.Minute)); r != nil {
		t.Fatalf("the wider thresholds were already overtaken: %+v", r)
	}
}

func TestExpiryRemindsExactlyOnce(t *testing.T) {
	tr := tracker()
	v := vault(t, base, 30*day)
	deadline := base.Add(30 * day)

	// Walk past the thresholds first so expiry is not their doing.
	tr.Due(v, deadline.Add(-30*time.Minute))

	overdue := tr.Due(v, deadline.Add(time.Minute))
	if overdue == nil {
		t.Fatal("passing the deadline should remind")
	}
	if overdue.Status != dms.StatusExpired {
		t.Errorf("status = %s, want expired", overdue.Status)
	}
	if overdue.TimeLeft >= 0 {
		t.Errorf("time left = %s, want a negative duration", overdue.TimeLeft)
	}

	if r := tr.Due(v, deadline.Add(2*day)); r != nil {
		t.Fatalf("an overdue vault should not nag daily: %+v", r)
	}
}

// A check-in moves the deadline, which makes every past reminder stale.
func TestACheckInReArmsEveryThreshold(t *testing.T) {
	tr := tracker()
	v := vault(t, base, 30*day)
	deadline := base.Add(30 * day)

	if r := tr.Due(v, deadline.Add(-12*time.Hour)); r == nil {
		t.Fatal("want a reminder before the check-in")
	}

	// The owner checks in the next moment.
	v.LastCheckIn = deadline.Add(-12 * time.Hour)
	newDeadline := v.Deadline()

	if r := tr.Due(v, newDeadline.Add(-20*day)); r != nil {
		t.Fatalf("a fresh timer should be quiet again: %+v", r)
	}
	if r := tr.Due(v, newDeadline.Add(-12*time.Hour)); r == nil {
		t.Fatal("the thresholds should arm again for the new deadline")
	}
}

func TestATriggeredVaultIsNeverRemindedAbout(t *testing.T) {
	tr := tracker()
	v := vault(t, base, 30*day)
	v.IsClaimed = true

	if r := tr.Due(v, base.Add(90*day)); r != nil {
		t.Fatalf("nothing to warn about once an heir has claimed: %+v", r)
	}
	if _, ok := tr.state[v.Address]; ok {
		t.Error("state for a triggered vault should be dropped, not kept forever")
	}
}

func TestForgetDropsClosedVaults(t *testing.T) {
	tr := tracker()
	gone := vault(t, base, 30*day)
	kept := vault(t, base, 30*day)

	tr.Due(gone, base.Add(29*day))
	tr.Due(kept, base.Add(29*day))

	tr.Forget(map[solana.PublicKey]struct{}{kept.Address: {}})

	if _, ok := tr.state[gone.Address]; ok {
		t.Error("a closed vault should be forgotten")
	}
	if _, ok := tr.state[kept.Address]; !ok {
		t.Error("a live vault should be remembered")
	}
}

func TestReminderMessageSaysWhatToDo(t *testing.T) {
	tr := tracker()
	v := vault(t, base, 30*day)
	deadline := base.Add(30 * day)

	due := tr.Due(v, deadline.Add(-3*day))
	if due == nil {
		t.Fatal("want a reminder")
	}
	if msg := due.Message(); msg == "" {
		t.Fatal("a reminder with no text is useless")
	}
}
