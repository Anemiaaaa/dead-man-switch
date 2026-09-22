package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

// Schema is applied on connect. Small enough that a migration tool would be
// more machinery than the problem deserves — the table is a cache of on-chain
// state and can be dropped and refilled by one scan.
const Schema = `
CREATE TABLE IF NOT EXISTS vaults (
    address         TEXT PRIMARY KEY,
    owner           TEXT        NOT NULL,
    vault_id        NUMERIC(20) NOT NULL,
    mint            TEXT,
    last_check_in   TIMESTAMPTZ NOT NULL,
    timeout_seconds BIGINT      NOT NULL,
    is_claimed      BOOLEAN     NOT NULL,
    claim_pool      NUMERIC(20) NOT NULL,
    lamports        NUMERIC(20) NOT NULL,
    bump            SMALLINT    NOT NULL,
    beneficiaries   JSONB       NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS vaults_owner_idx ON vaults (owner);
CREATE INDEX IF NOT EXISTS vaults_deadline_idx
    ON vaults ((last_check_in + make_interval(secs => timeout_seconds)));
`

// Postgres caches vault state so the read API survives an RPC outage and does
// not spend rate limit on every request.
type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: connecting: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if _, err := pool.Exec(ctx, Schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: applying schema: %w", err)
	}

	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

// Replace writes one whole scan atomically: every vault is upserted and
// anything not in the scan is deleted, so a closed vault disappears from the
// index rather than lingering as a stale row.
func (p *Postgres) Replace(ctx context.Context, vaults []*dms.Vault) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	addresses := make([]string, 0, len(vaults))
	for _, vault := range vaults {
		heirs, err := json.Marshal(vault.Beneficiaries)
		if err != nil {
			return fmt.Errorf("store: encoding heirs of %s: %w", vault.Address, err)
		}

		var mint *string
		if vault.Mint != nil {
			key := vault.Mint.String()
			mint = &key
		}

		_, err = tx.Exec(ctx, `
            INSERT INTO vaults (address, owner, vault_id, mint, last_check_in,
                                timeout_seconds, is_claimed, claim_pool,
                                lamports, bump, beneficiaries, updated_at)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now())
            ON CONFLICT (address) DO UPDATE SET
                owner           = EXCLUDED.owner,
                vault_id        = EXCLUDED.vault_id,
                mint            = EXCLUDED.mint,
                last_check_in   = EXCLUDED.last_check_in,
                timeout_seconds = EXCLUDED.timeout_seconds,
                is_claimed      = EXCLUDED.is_claimed,
                claim_pool      = EXCLUDED.claim_pool,
                lamports        = EXCLUDED.lamports,
                bump            = EXCLUDED.bump,
                beneficiaries   = EXCLUDED.beneficiaries,
                updated_at      = now()`,
			vault.Address.String(),
			vault.Owner.String(),
			// Vault ids and balances are u64; NUMERIC keeps the ones that do
			// not fit a signed 64-bit column honest.
			strconv.FormatUint(vault.VaultID, 10),
			mint,
			vault.LastCheckIn,
			int64(vault.Timeout/time.Second),
			vault.IsClaimed,
			strconv.FormatUint(vault.ClaimPool, 10),
			strconv.FormatUint(vault.Lamports, 10),
			int16(vault.Bump),
			heirs,
		)
		if err != nil {
			return fmt.Errorf("store: upserting %s: %w", vault.Address, err)
		}

		addresses = append(addresses, vault.Address.String())
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM vaults WHERE NOT (address = ANY($1))`, addresses,
	); err != nil {
		return fmt.Errorf("store: pruning closed vaults: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}

	return nil
}

const selectVault = `
    SELECT address, owner, vault_id, mint, last_check_in, timeout_seconds,
           is_claimed, claim_pool, lamports, bump, beneficiaries
      FROM vaults`

func (p *Postgres) List(ctx context.Context) ([]*dms.Vault, error) {
	rows, err := p.pool.Query(ctx, selectVault+` ORDER BY last_check_in + make_interval(secs => timeout_seconds)`)
	if err != nil {
		return nil, fmt.Errorf("store: list: %w", err)
	}
	return scanVaults(rows)
}

func (p *Postgres) Get(ctx context.Context, address solana.PublicKey) (*dms.Vault, error) {
	rows, err := p.pool.Query(ctx, selectVault+` WHERE address = $1`, address.String())
	if err != nil {
		return nil, fmt.Errorf("store: get: %w", err)
	}
	vaults, err := scanVaults(rows)
	if err != nil {
		return nil, err
	}
	if len(vaults) == 0 {
		return nil, ErrNotFound
	}

	return vaults[0], nil
}

func (p *Postgres) ByOwner(ctx context.Context, owner solana.PublicKey) ([]*dms.Vault, error) {
	rows, err := p.pool.Query(ctx, selectVault+` WHERE owner = $1 ORDER BY vault_id`, owner.String())
	if err != nil {
		return nil, fmt.Errorf("store: by owner: %w", err)
	}
	return scanVaults(rows)
}

func scanVaults(rows pgx.Rows) ([]*dms.Vault, error) {
	defer rows.Close()

	var out []*dms.Vault
	for rows.Next() {
		var (
			address, owner               string
			vaultID, claimPool, lamports string
			mint                         *string
			lastCheckIn                  time.Time
			timeoutSeconds               int64
			isClaimed                    bool
			bump                         int16
			heirs                        []byte
		)

		if err := rows.Scan(&address, &owner, &vaultID, &mint, &lastCheckIn,
			&timeoutSeconds, &isClaimed, &claimPool, &lamports, &bump, &heirs); err != nil {
			return nil, fmt.Errorf("store: scan: %w", err)
		}

		vault := &dms.Vault{
			LastCheckIn: lastCheckIn.UTC(),
			Timeout:     time.Duration(timeoutSeconds) * time.Second,
			IsClaimed:   isClaimed,
			Bump:        uint8(bump),
		}

		var err error
		if vault.Address, err = solana.PublicKeyFromBase58(address); err != nil {
			return nil, fmt.Errorf("store: address %q: %w", address, err)
		}
		if vault.Owner, err = solana.PublicKeyFromBase58(owner); err != nil {
			return nil, fmt.Errorf("store: owner %q: %w", owner, err)
		}
		if mint != nil {
			key, err := solana.PublicKeyFromBase58(*mint)
			if err != nil {
				return nil, fmt.Errorf("store: mint %q: %w", *mint, err)
			}
			vault.Mint = &key
		}
		if vault.VaultID, err = strconv.ParseUint(vaultID, 10, 64); err != nil {
			return nil, fmt.Errorf("store: vault id %q: %w", vaultID, err)
		}
		if vault.ClaimPool, err = strconv.ParseUint(claimPool, 10, 64); err != nil {
			return nil, fmt.Errorf("store: claim pool %q: %w", claimPool, err)
		}
		if vault.Lamports, err = strconv.ParseUint(lamports, 10, 64); err != nil {
			return nil, fmt.Errorf("store: lamports %q: %w", lamports, err)
		}
		if err := json.Unmarshal(heirs, &vault.Beneficiaries); err != nil {
			return nil, fmt.Errorf("store: heirs of %s: %w", address, err)
		}

		out = append(out, vault)
	}

	if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: rows: %w", err)
	}

	return out, nil
}
