package dms

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
)

// The golden files are written by the Anchor program's own serializer, in
// `programs/dead-man-switch/tests/test_golden_layout.rs`. Decoding them here is
// the only test in this package that proves the hand-written decoder agrees
// with the program itself rather than with a mirror of my own assumptions.
//
// Regenerate both sides together:
//
//	UPDATE_GOLDEN=1 cargo test --test test_golden_layout
func golden(t *testing.T, name string) []byte {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\nrun `UPDATE_GOLDEN=1 cargo test --test test_golden_layout` first", path, err)
	}
	if len(data) != AccountSize {
		t.Fatalf("%s is %d bytes, want %d — the account layout has changed", path, len(data), AccountSize)
	}

	return data
}

// fixedKey mirrors the Rust side's `Pubkey::new_from_array([b; 32])`, so the
// expectations here read the same as the values in the generator.
func fixedKey(b byte) solana.PublicKey {
	var raw [32]byte
	for i := range raw {
		raw[i] = b
	}
	return solana.PublicKeyFromBytes(raw[:])
}

func TestGoldenSOLVault(t *testing.T) {
	vault, err := DecodeVault(fixedKey(0x99), golden(t, "vault_sol.bin"))
	if err != nil {
		t.Fatalf("DecodeVault: %v", err)
	}

	if vault.Owner != fixedKey(0x11) {
		t.Errorf("owner = %s, want the 0x11 key", vault.Owner)
	}
	if vault.VaultID != 7 {
		t.Errorf("vault id = %d, want 7", vault.VaultID)
	}
	if !vault.IsSOL() {
		t.Errorf("mint = %v, want none", vault.Mint)
	}
	if got, want := vault.LastCheckIn.Unix(), int64(1_800_000_000); got != want {
		t.Errorf("last check-in = %d, want %d", got, want)
	}
	if got, want := vault.Timeout, 30*24*time.Hour; got != want {
		t.Errorf("timeout = %s, want %s", got, want)
	}
	if vault.IsClaimed {
		t.Error("is_claimed should be false")
	}
	if vault.ClaimPool != 0 {
		t.Errorf("claim pool = %d, want 0", vault.ClaimPool)
	}
	if vault.Bump != 254 {
		t.Errorf("bump = %d, want 254", vault.Bump)
	}

	if len(vault.Beneficiaries) != 2 {
		t.Fatalf("got %d heirs, want 2", len(vault.Beneficiaries))
	}
	if vault.Beneficiaries[0].Address != fixedKey(0xA1) || vault.Beneficiaries[0].ShareBPS != 7_000 {
		t.Errorf("first heir = %+v", vault.Beneficiaries[0])
	}
	if vault.Beneficiaries[1].Address != fixedKey(0xB2) || vault.Beneficiaries[1].ShareBPS != 3_000 {
		t.Errorf("second heir = %+v", vault.Beneficiaries[1])
	}
}

func TestGoldenSPLVault(t *testing.T) {
	vault, err := DecodeVault(fixedKey(0x99), golden(t, "vault_spl.bin"))
	if err != nil {
		t.Fatalf("DecodeVault: %v", err)
	}

	if vault.Owner != fixedKey(0x22) {
		t.Errorf("owner = %s, want the 0x22 key", vault.Owner)
	}
	// A vault id past the top of int64: the decoder and the Postgres column
	// both have to keep it intact.
	if vault.VaultID != ^uint64(0) {
		t.Errorf("vault id = %d, want %d", vault.VaultID, ^uint64(0))
	}
	if vault.IsSOL() {
		t.Fatal("want an SPL vault")
	}
	if *vault.Mint != fixedKey(0x33) {
		t.Errorf("mint = %s, want the 0x33 key", vault.Mint)
	}
	if got, want := vault.Timeout, 7*24*time.Hour; got != want {
		t.Errorf("timeout = %s, want %s", got, want)
	}
	if !vault.IsClaimed {
		t.Error("is_claimed should be true")
	}
	if vault.ClaimPool != 123_456_789_012 {
		t.Errorf("claim pool = %d, want 123456789012", vault.ClaimPool)
	}
	if vault.Bump != 250 {
		t.Errorf("bump = %d, want 250", vault.Bump)
	}

	if len(vault.Beneficiaries) != 3 {
		t.Fatalf("got %d heirs, want 3", len(vault.Beneficiaries))
	}
	if !vault.Beneficiaries[0].Claimed {
		t.Error("the first heir should be marked as claimed")
	}
	if vault.Claimed() != 1 {
		t.Errorf("claimed heirs = %d, want 1", vault.Claimed())
	}
	if got := vault.Beneficiaries[2].SharePercent(); got != 20 {
		t.Errorf("third heir holds %v%%, want 20%%", got)
	}
}

// The status a keeper reports has to follow from bytes the program actually
// wrote, not from a struct assembled in a Go test.
func TestGoldenVaultStatuses(t *testing.T) {
	sol, err := DecodeVault(fixedKey(0x99), golden(t, "vault_sol.bin"))
	if err != nil {
		t.Fatalf("DecodeVault: %v", err)
	}
	deadline := sol.Deadline()

	cases := []struct {
		name string
		at   time.Time
		want Status
	}{
		{"fresh", deadline.Add(-29 * 24 * time.Hour), StatusActive},
		{"a day out", deadline.Add(-24 * time.Hour), StatusDueSoon},
		{"on the deadline", deadline, StatusExpired},
		{"long overdue", deadline.Add(90 * 24 * time.Hour), StatusExpired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sol.Status(c.at, 14*24*time.Hour); got != c.want {
				t.Errorf("status = %s, want %s", got, c.want)
			}
		})
	}

	spl, err := DecodeVault(fixedKey(0x99), golden(t, "vault_spl.bin"))
	if err != nil {
		t.Fatalf("DecodeVault: %v", err)
	}
	if got := spl.Status(spl.Deadline().Add(time.Hour), time.Hour); got != StatusTriggered {
		t.Errorf("a claimed vault reports %s, want triggered", got)
	}
}
