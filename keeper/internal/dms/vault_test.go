package dms

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
)

// account assembles vault bytes the way the Anchor program writes them.
//
// Hand-rolled on purpose: if this mirror and the decoder were generated from
// the same source, the test would only prove the generator is self-consistent.
type account struct{ buf []byte }

func (a *account) u8(v uint8)   { a.buf = append(a.buf, v) }
func (a *account) bool(v bool)  { a.u8(map[bool]uint8{false: 0, true: 1}[v]) }
func (a *account) u16(v uint16) { a.buf = binary.LittleEndian.AppendUint16(a.buf, v) }
func (a *account) u64(v uint64) { a.buf = binary.LittleEndian.AppendUint64(a.buf, v) }
func (a *account) i64(v int64)  { a.u64(uint64(v)) }

func (a *account) pubkey(key solana.PublicKey) { a.buf = append(a.buf, key.Bytes()...) }

func (a *account) optionPubkey(key *solana.PublicKey) {
	if key == nil {
		a.u8(0)
		return
	}
	a.u8(1)
	a.pubkey(*key)
}

func (a *account) beneficiary(b Beneficiary) {
	a.pubkey(b.Address)
	a.u16(b.ShareBPS)
	a.bool(b.Claimed)
}

// padToAccountSize appends the zero bytes the runtime leaves at the tail.
func (a *account) padToAccountSize() []byte {
	for len(a.buf) < AccountSize {
		a.buf = append(a.buf, 0)
	}
	return a.buf
}

type vaultSpec struct {
	owner         solana.PublicKey
	vaultID       uint64
	mint          *solana.PublicKey
	lastCheckIn   int64
	timeout       int64
	beneficiaries []Beneficiary
	isClaimed     bool
	claimPool     uint64
	bump          uint8
}

func encodeVault(spec vaultSpec) []byte {
	a := &account{}
	a.buf = append(a.buf, AccountDiscriminator[:]...)
	a.pubkey(spec.owner)
	a.u64(spec.vaultID)
	a.optionPubkey(spec.mint)
	a.i64(spec.lastCheckIn)
	a.i64(spec.timeout)

	slots := make([]Beneficiary, MaxBeneficiaries)
	copy(slots, spec.beneficiaries)
	for _, slot := range slots {
		a.beneficiary(slot)
	}

	a.u8(uint8(len(spec.beneficiaries)))
	a.bool(spec.isClaimed)
	a.u64(spec.claimPool)
	a.u8(spec.bump)

	return a.padToAccountSize()
}

func mustKey(t *testing.T) solana.PublicKey {
	t.Helper()
	key, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	return key.PublicKey()
}

func TestDecodeSOLVault(t *testing.T) {
	owner := mustKey(t)
	address := mustKey(t)
	alice, bob := mustKey(t), mustKey(t)

	data := encodeVault(vaultSpec{
		owner:       owner,
		vaultID:     7,
		mint:        nil,
		lastCheckIn: 1_800_000_000,
		timeout:     30 * 24 * 3600,
		beneficiaries: []Beneficiary{
			{Address: alice, ShareBPS: 7_000},
			{Address: bob, ShareBPS: 3_000},
		},
		bump: 254,
	})

	if len(data) != AccountSize {
		t.Fatalf("account is %d bytes, want %d", len(data), AccountSize)
	}

	vault, err := DecodeVault(address, data)
	if err != nil {
		t.Fatalf("DecodeVault: %v", err)
	}

	if vault.Owner != owner {
		t.Errorf("owner = %s, want %s", vault.Owner, owner)
	}
	if vault.VaultID != 7 {
		t.Errorf("vault id = %d, want 7", vault.VaultID)
	}
	if !vault.IsSOL() {
		t.Errorf("mint = %v, want a SOL vault", vault.Mint)
	}
	if got, want := vault.LastCheckIn.Unix(), int64(1_800_000_000); got != want {
		t.Errorf("last check-in = %d, want %d", got, want)
	}
	if got, want := vault.Timeout, 30*24*time.Hour; got != want {
		t.Errorf("timeout = %s, want %s", got, want)
	}
	if got, want := vault.Deadline().Unix(), int64(1_800_000_000+30*24*3600); got != want {
		t.Errorf("deadline = %d, want %d", got, want)
	}
	if vault.Bump != 254 {
		t.Errorf("bump = %d, want 254", vault.Bump)
	}

	if len(vault.Beneficiaries) != 2 {
		t.Fatalf("got %d heirs, want 2 — the empty slots must be trimmed", len(vault.Beneficiaries))
	}
	if vault.Beneficiaries[0].Address != alice || vault.Beneficiaries[0].ShareBPS != 7_000 {
		t.Errorf("first heir = %+v", vault.Beneficiaries[0])
	}
	if got := vault.Beneficiaries[1].SharePercent(); got != 30 {
		t.Errorf("second heir holds %v%%, want 30%%", got)
	}
}

// The optional mint shifts every field after it, which is exactly the bug a
// fixed-offset decoder would ship with.
func TestDecodeSPLVaultReadsPastTheLongerMint(t *testing.T) {
	mint := mustKey(t)
	owner := mustKey(t)
	heir := mustKey(t)

	data := encodeVault(vaultSpec{
		owner:         owner,
		vaultID:       1,
		mint:          &mint,
		lastCheckIn:   1_700_000_000,
		timeout:       7 * 24 * 3600,
		beneficiaries: []Beneficiary{{Address: heir, ShareBPS: 10_000, Claimed: true}},
		isClaimed:     true,
		claimPool:     123_456_789,
		bump:          250,
	})

	if len(data) != AccountSize {
		t.Fatalf("account is %d bytes, want %d", len(data), AccountSize)
	}

	vault, err := DecodeVault(mustKey(t), data)
	if err != nil {
		t.Fatalf("DecodeVault: %v", err)
	}

	if vault.IsSOL() {
		t.Fatal("want an SPL vault")
	}
	if *vault.Mint != mint {
		t.Errorf("mint = %s, want %s", vault.Mint, mint)
	}
	if !vault.IsClaimed {
		t.Error("is_claimed should have survived the shift")
	}
	if vault.ClaimPool != 123_456_789 {
		t.Errorf("claim pool = %d, want 123456789", vault.ClaimPool)
	}
	if vault.Bump != 250 {
		t.Errorf("bump = %d, want 250", vault.Bump)
	}
	if vault.Claimed() != 1 {
		t.Errorf("claimed heirs = %d, want 1", vault.Claimed())
	}
}

func TestDecodeRejectsAnotherAccountType(t *testing.T) {
	data := encodeVault(vaultSpec{owner: mustKey(t)})
	data[0] ^= 0xff // any other account this program might own

	_, err := DecodeVault(mustKey(t), data)
	if !errors.Is(err, ErrNotAVault) {
		t.Fatalf("err = %v, want ErrNotAVault", err)
	}
}

func TestDecodeRejectsTruncatedData(t *testing.T) {
	data := encodeVault(vaultSpec{
		owner:         mustKey(t),
		beneficiaries: []Beneficiary{{Address: mustKey(t), ShareBPS: 10_000}},
	})

	if _, err := DecodeVault(mustKey(t), data[:60]); err == nil {
		t.Fatal("decoding a truncated account should fail")
	}
	if _, err := DecodeVault(mustKey(t), nil); !errors.Is(err, ErrNotAVault) {
		t.Fatalf("err = %v, want ErrNotAVault", err)
	}
}

// A count past the table would otherwise make the decoder slice out of range.
func TestDecodeRejectsAnImpossibleHeirCount(t *testing.T) {
	data := encodeVault(vaultSpec{
		owner:         mustKey(t),
		beneficiaries: []Beneficiary{{Address: mustKey(t), ShareBPS: 10_000}},
	})
	// beneficiary_count sits right after the heir table.
	countOffset := 8 + 32 + 8 + 1 + 8 + 8 + MaxBeneficiaries*35
	data[countOffset] = 9

	if _, err := DecodeVault(mustKey(t), data); err == nil {
		t.Fatal("a heir count of 9 should be rejected, not sliced")
	}
}

func TestFindVaultAddressIsDeterministic(t *testing.T) {
	programID := mustKey(t)
	owner := mustKey(t)

	first, bump, err := FindVaultAddress(programID, owner, 1)
	if err != nil {
		t.Fatalf("FindVaultAddress: %v", err)
	}
	again, _, err := FindVaultAddress(programID, owner, 1)
	if err != nil {
		t.Fatalf("FindVaultAddress: %v", err)
	}
	if first != again {
		t.Errorf("same inputs gave %s then %s", first, again)
	}
	if bump == 0 {
		t.Error("bump should not be zero")
	}

	other, _, err := FindVaultAddress(programID, owner, 2)
	if err != nil {
		t.Fatalf("FindVaultAddress: %v", err)
	}
	if other == first {
		t.Error("a different vault id must give a different address")
	}
}
