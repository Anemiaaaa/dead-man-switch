// Package dms reads Dead Man's Switch vaults straight off the chain.
//
// The layout below mirrors the Anchor program's `Vault` account byte for byte.
// There is no code generation in the loop, so the one thing that must not drift
// is this decoder — which is why `vault_test.go` builds account bytes by hand
// and asserts on every field.
package dms

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/gagliardetto/solana-go"
)

// AccountDiscriminator is the eight-byte tag Anchor writes at the head of every
// `Vault` account. Taken from the generated IDL, not guessed: it is what
// `GetProgramAccounts` filters on so the keeper never tries to decode some
// other account this program owns.
var AccountDiscriminator = [8]byte{211, 8, 232, 43, 2, 152, 117, 119}

const (
	// MaxBeneficiaries mirrors the program's fixed-size heir table.
	MaxBeneficiaries = 5

	// TotalShareBPS is what every vault's active shares add up to.
	TotalShareBPS = 10_000

	// AccountSize is `8 + Vault::INIT_SPACE`. The account is always this big;
	// a SOL vault simply leaves the tail zeroed, because `Option<Pubkey>`
	// serialises to one byte when it is `None` and thirty-three when it is not.
	AccountSize = 283

	// VaultSeed is the first seed of the vault PDA.
	VaultSeed = "vault"
)

// ErrNotAVault means the bytes handed to the decoder are not a vault account.
var ErrNotAVault = errors.New("dms: account is not a Vault")

// Beneficiary is one heir slot.
type Beneficiary struct {
	Address  solana.PublicKey `json:"address"`
	ShareBPS uint16           `json:"share_bps"`
	Claimed  bool             `json:"claimed"`
}

// SharePercent renders the basis points as a human percentage.
func (b Beneficiary) SharePercent() float64 {
	return float64(b.ShareBPS) / 100
}

// Vault is one switch as the keeper sees it.
type Vault struct {
	Address       solana.PublicKey  `json:"address"`
	Owner         solana.PublicKey  `json:"owner"`
	VaultID       uint64            `json:"vault_id"`
	Mint          *solana.PublicKey `json:"mint"`
	LastCheckIn   time.Time         `json:"last_check_in"`
	Timeout       time.Duration     `json:"-"`
	Beneficiaries []Beneficiary     `json:"beneficiaries"`
	IsClaimed     bool              `json:"is_claimed"`
	ClaimPool     uint64            `json:"claim_pool"`
	Bump          uint8             `json:"bump"`

	// Lamports is the account balance, filled in by the fetcher rather than
	// the decoder — it lives outside the account data.
	Lamports uint64 `json:"lamports"`
}

// IsSOL reports whether this vault holds native SOL rather than an SPL token.
func (v *Vault) IsSOL() bool { return v.Mint == nil }

// Deadline is the moment heirs may start claiming.
func (v *Vault) Deadline() time.Time { return v.LastCheckIn.Add(v.Timeout) }

// TimeLeft is how long the owner has before heirs can claim. It goes negative
// once the deadline is behind us.
func (v *Vault) TimeLeft(now time.Time) time.Duration { return v.Deadline().Sub(now) }

// Index returns where `address` sits in the heir table, or -1 if it is not
// listed at all.
func (v *Vault) Index(address solana.PublicKey) int {
	for i, heir := range v.Beneficiaries {
		if heir.Address.Equals(address) {
			return i
		}
	}
	return -1
}

// Claimed counts heirs who have already taken their share.
func (v *Vault) Claimed() int {
	var n int
	for _, heir := range v.Beneficiaries {
		if heir.Claimed {
			n++
		}
	}
	return n
}

// FindVaultAddress derives the PDA for one of an owner's vaults.
func FindVaultAddress(programID, owner solana.PublicKey, vaultID uint64) (solana.PublicKey, uint8, error) {
	id := make([]byte, 8)
	binary.LittleEndian.PutUint64(id, vaultID)

	return solana.FindProgramAddress(
		[][]byte{[]byte(VaultSeed), owner.Bytes(), id},
		programID,
	)
}

// DecodeVault turns raw account data into a Vault.
//
// Fields are read in order rather than at fixed offsets, because the optional
// mint shifts everything after it by thirty-two bytes.
func DecodeVault(address solana.PublicKey, data []byte) (*Vault, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("%w: %d bytes is too short", ErrNotAVault, len(data))
	}
	if [8]byte(data[:8]) != AccountDiscriminator {
		return nil, fmt.Errorf("%w: discriminator %v", ErrNotAVault, data[:8])
	}

	r := &reader{buf: data, off: 8}

	vault := &Vault{Address: address}
	vault.Owner = r.pubkey()
	vault.VaultID = r.u64()
	vault.Mint = r.optionPubkey()
	vault.LastCheckIn = time.Unix(r.i64(), 0).UTC()
	vault.Timeout = time.Duration(r.i64()) * time.Second

	slots := make([]Beneficiary, MaxBeneficiaries)
	for i := range slots {
		slots[i] = Beneficiary{
			Address:  r.pubkey(),
			ShareBPS: r.u16(),
			Claimed:  r.bool(),
		}
	}

	count := int(r.u8())
	vault.IsClaimed = r.bool()
	vault.ClaimPool = r.u64()
	vault.Bump = r.u8()

	if r.err != nil {
		return nil, fmt.Errorf("dms: decoding vault %s: %w", address, r.err)
	}
	if count > MaxBeneficiaries {
		return nil, fmt.Errorf("dms: vault %s claims %d heirs, the table holds %d",
			address, count, MaxBeneficiaries)
	}

	// Only the occupied slots are meaningful; the rest are zeroed padding.
	vault.Beneficiaries = slots[:count]

	return vault, nil
}

// reader walks a Borsh-encoded buffer, remembering the first error so callers
// can decode a whole struct and check once at the end.
type reader struct {
	buf []byte
	off int
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return make([]byte, n)
	}
	if r.off+n > len(r.buf) {
		r.err = fmt.Errorf("want %d bytes at offset %d, buffer is %d long", n, r.off, len(r.buf))
		return make([]byte, n)
	}
	out := r.buf[r.off : r.off+n]
	r.off += n
	return out
}

func (r *reader) u8() uint8   { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.LittleEndian.Uint16(r.take(2)) }
func (r *reader) u64() uint64 { return binary.LittleEndian.Uint64(r.take(8)) }
func (r *reader) i64() int64  { return int64(r.u64()) }

func (r *reader) bool() bool { return r.u8() != 0 }

func (r *reader) pubkey() solana.PublicKey {
	return solana.PublicKeyFromBytes(r.take(32))
}

// optionPubkey decodes Borsh's `Option<Pubkey>`: a single tag byte, followed by
// the key only when the tag is set.
func (r *reader) optionPubkey() *solana.PublicKey {
	if !r.bool() {
		return nil
	}
	key := r.pubkey()
	return &key
}
