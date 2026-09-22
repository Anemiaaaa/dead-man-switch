package watch

import (
	"sort"
	"sync"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/notify"
)

// Tracker decides when a vault is worth a reminder, and remembers what it has
// already said.
//
// The keeper polls on a short interval, so without this every tick would
// re-send the same warning. Deduplication is per vault *and* per threshold, so
// a vault that crosses "14 days left" and later "1 day left" produces two
// reminders rather than one or a hundred.
type Tracker struct {
	// thresholds is how far ahead of the deadline to speak up, descending.
	thresholds []time.Duration

	mu    sync.Mutex
	state map[solana.PublicKey]*vaultState
}

type vaultState struct {
	// deadline the fired set belongs to. A check-in moves the deadline, which
	// makes every past reminder stale — the owner is demonstrably alive and
	// starts the cycle over.
	deadline time.Time
	fired    map[time.Duration]bool
}

// expired is the sentinel threshold for "the deadline is already behind us".
const expired = time.Duration(0)

// NewTracker sorts the thresholds so the most urgent one always wins.
func NewTracker(thresholds []time.Duration) *Tracker {
	sorted := make([]time.Duration, 0, len(thresholds))
	for _, t := range thresholds {
		if t > 0 {
			sorted = append(sorted, t)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] > sorted[j] })

	return &Tracker{
		thresholds: sorted,
		state:      make(map[solana.PublicKey]*vaultState),
	}
}

// Due returns a reminder if this vault needs one right now, or nil.
func (t *Tracker) Due(v *dms.Vault, now time.Time) *notify.Reminder {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Once an heir has claimed, the owner is locked out and there is nothing
	// left to warn about. Drop the state so the map does not grow forever.
	if v.IsClaimed {
		delete(t.state, v.Address)
		return nil
	}

	deadline := v.Deadline()
	state, ok := t.state[v.Address]
	if !ok || !state.deadline.Equal(deadline) {
		state = &vaultState{deadline: deadline, fired: make(map[time.Duration]bool)}
		t.state[v.Address] = state
	}

	timeLeft := v.TimeLeft(now)

	if timeLeft <= 0 {
		if state.fired[expired] {
			return nil
		}
		state.fired[expired] = true
		return t.reminder(v, dms.StatusExpired, deadline, timeLeft)
	}

	// Every threshold the vault has already crossed counts as reached; firing
	// only the most urgent one keeps a keeper that was offline for a week from
	// sending four messages at once.
	var crossed time.Duration
	for _, threshold := range t.thresholds {
		if timeLeft <= threshold {
			crossed = threshold
		}
	}
	if crossed == 0 || state.fired[crossed] {
		return nil
	}
	for _, threshold := range t.thresholds {
		if timeLeft <= threshold {
			state.fired[threshold] = true
		}
	}

	return t.reminder(v, dms.StatusDueSoon, deadline, timeLeft)
}

// Forget drops state for vaults that no longer exist, so a long-running keeper
// does not hold on to closed ones.
func (t *Tracker) Forget(live map[solana.PublicKey]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for address := range t.state {
		if _, ok := live[address]; !ok {
			delete(t.state, address)
		}
	}
}

func (t *Tracker) reminder(
	v *dms.Vault,
	status dms.Status,
	deadline time.Time,
	timeLeft time.Duration,
) *notify.Reminder {
	return &notify.Reminder{
		Vault:    v.Address,
		Owner:    v.Owner,
		Status:   status,
		Deadline: deadline,
		TimeLeft: timeLeft,
	}
}
