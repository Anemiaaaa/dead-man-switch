// Package store keeps the last known state of every vault so the API can
// answer without hitting an RPC node on each request.
package store

import (
	"context"
	"errors"
	"sync"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

// ErrNotFound is returned when a vault is not in the cache.
var ErrNotFound = errors.New("store: vault not found")

// Store is the keeper's view of the chain.
//
// `Replace` takes the complete set from one scan rather than individual
// upserts: `GetProgramAccounts` returns everything the program owns, so
// anything missing from that set has been closed and should leave the cache
// too.
type Store interface {
	Replace(ctx context.Context, vaults []*dms.Vault) error
	List(ctx context.Context) ([]*dms.Vault, error)
	Get(ctx context.Context, address solana.PublicKey) (*dms.Vault, error)
	ByOwner(ctx context.Context, owner solana.PublicKey) ([]*dms.Vault, error)
}

// Memory is the default store: enough to run the keeper with nothing else
// installed, and what the tests use.
type Memory struct {
	mu     sync.RWMutex
	vaults map[solana.PublicKey]*dms.Vault
	order  []solana.PublicKey
}

func NewMemory() *Memory {
	return &Memory{vaults: make(map[solana.PublicKey]*dms.Vault)}
}

func (m *Memory) Replace(_ context.Context, vaults []*dms.Vault) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.vaults = make(map[solana.PublicKey]*dms.Vault, len(vaults))
	m.order = make([]solana.PublicKey, 0, len(vaults))
	for _, vault := range vaults {
		m.vaults[vault.Address] = vault
		m.order = append(m.order, vault.Address)
	}

	return nil
}

func (m *Memory) List(_ context.Context) ([]*dms.Vault, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*dms.Vault, 0, len(m.order))
	for _, address := range m.order {
		out = append(out, m.vaults[address])
	}

	return out, nil
}

func (m *Memory) Get(_ context.Context, address solana.PublicKey) (*dms.Vault, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	vault, ok := m.vaults[address]
	if !ok {
		return nil, ErrNotFound
	}

	return vault, nil
}

func (m *Memory) ByOwner(_ context.Context, owner solana.PublicKey) ([]*dms.Vault, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []*dms.Vault
	for _, address := range m.order {
		if vault := m.vaults[address]; vault.Owner == owner {
			out = append(out, vault)
		}
	}

	return out, nil
}
