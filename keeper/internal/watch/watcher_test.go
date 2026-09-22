package watch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/notify"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/store"
)

type fakeSource struct {
	vaults []*dms.Vault
	err    error
}

func (f *fakeSource) Vaults(context.Context) ([]*dms.Vault, error) {
	return f.vaults, f.err
}

type recorder struct {
	mu   sync.Mutex
	got  []notify.Reminder
	fail error
}

func (r *recorder) Notify(_ context.Context, reminder notify.Reminder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, reminder)
	return r.fail
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func newWatcher(source VaultSource, sent *recorder, now time.Time) (*Watcher, *store.Memory) {
	cache := store.NewMemory()
	return &Watcher{
		Source:   source,
		Store:    cache,
		Notifier: sent,
		Tracker:  tracker(),
		Interval: time.Minute,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return now },
	}, cache
}

func TestScanCachesEveryVault(t *testing.T) {
	calm := vault(t, base, 90*day)
	urgent := vault(t, base, 30*day)
	sent := &recorder{}

	w, cache := newWatcher(&fakeSource{vaults: []*dms.Vault{calm, urgent}}, sent, base.Add(29*day))
	if err := w.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	cached, err := cache.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cached) != 2 {
		t.Fatalf("cached %d vaults, want 2", len(cached))
	}

	// Only the one inside a threshold is worth a message.
	if sent.count() != 1 {
		t.Fatalf("sent %d reminders, want 1", sent.count())
	}
	if sent.got[0].Vault != urgent.Address {
		t.Errorf("reminded about %s, want %s", sent.got[0].Vault, urgent.Address)
	}
}

func TestRepeatedScansDoNotRepeatReminders(t *testing.T) {
	v := vault(t, base, 30*day)
	sent := &recorder{}
	w, _ := newWatcher(&fakeSource{vaults: []*dms.Vault{v}}, sent, base.Add(29*day))

	for range 4 {
		if err := w.Scan(context.Background()); err != nil {
			t.Fatalf("Scan: %v", err)
		}
	}

	if sent.count() != 1 {
		t.Fatalf("sent %d reminders across four scans, want 1", sent.count())
	}
}

func TestScanReportsASourceFailure(t *testing.T) {
	boom := errors.New("rpc is having a day")
	w, _ := newWatcher(&fakeSource{err: boom}, &recorder{}, base)

	if err := w.Scan(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

// One owner's unreachable Telegram must not cost everyone else their
// reminders.
func TestADeliveryFailureDoesNotAbortTheScan(t *testing.T) {
	first := vault(t, base, 30*day)
	second := vault(t, base, 30*day)
	sent := &recorder{fail: errors.New("telegram is down")}

	w, _ := newWatcher(&fakeSource{vaults: []*dms.Vault{first, second}}, sent, base.Add(29*day))
	if err := w.Scan(context.Background()); err != nil {
		t.Fatalf("Scan should survive a delivery failure, got %v", err)
	}
	if sent.count() != 2 {
		t.Fatalf("attempted %d deliveries, want 2", sent.count())
	}
}

func TestScanForgetsVaultsThatDisappeared(t *testing.T) {
	v := vault(t, base, 30*day)
	source := &fakeSource{vaults: []*dms.Vault{v}}
	w, _ := newWatcher(source, &recorder{}, base.Add(29*day))

	if err := w.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := w.Tracker.state[v.Address]; !ok {
		t.Fatal("the tracker should be following this vault")
	}

	// The owner closed it.
	source.vaults = nil
	if err := w.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := w.Tracker.state[v.Address]; ok {
		t.Error("a closed vault should not stay in the tracker")
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	w, _ := newWatcher(&fakeSource{}, &recorder{}, base)
	w.Interval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
}
