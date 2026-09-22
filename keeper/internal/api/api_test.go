package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/store"
)

var (
	day  = 24 * time.Hour
	base = time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
)

func key(t *testing.T) solana.PublicKey {
	t.Helper()
	k, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	return k.PublicKey()
}

func serve(t *testing.T, vaults ...*dms.Vault) http.Handler {
	t.Helper()
	cache := store.NewMemory()
	if err := cache.Replace(context.Background(), vaults); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	return (&API{
		Store:   cache,
		DueSoon: 14 * day,
		Now:     func() time.Time { return base },
	}).Routes()
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	var body map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v (body %q)", path, err, rec.Body.String())
		}
	}

	return rec, body
}

func solVault(t *testing.T, owner solana.PublicKey, lastCheckIn time.Time, timeout time.Duration) *dms.Vault {
	t.Helper()
	return &dms.Vault{
		Address:     key(t),
		Owner:       owner,
		VaultID:     1,
		LastCheckIn: lastCheckIn,
		Timeout:     timeout,
		Lamports:    5_000_000_000,
		Beneficiaries: []dms.Beneficiary{
			{Address: key(t), ShareBPS: 7_000},
			{Address: key(t), ShareBPS: 3_000},
		},
	}
}

func TestListVaultsReportsStatusAndTimeLeft(t *testing.T) {
	owner := key(t)
	// Twenty days in on a thirty-day timer: ten days left, outside the
	// fourteen-day urgency window is false — ten is inside it.
	v := solVault(t, owner, base.Add(-20*day), 30*day)
	h := serve(t, v)

	rec, body := get(t, h, "/v1/vaults")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if count := body["count"].(float64); count != 1 {
		t.Fatalf("count = %v, want 1", count)
	}

	item := body["vaults"].([]any)[0].(map[string]any)
	if got := item["status"]; got != string(dms.StatusDueSoon) {
		t.Errorf("status = %v, want due_soon", got)
	}
	if got := item["asset"]; got != "SOL" {
		t.Errorf("asset = %v, want SOL", got)
	}
	if got := int64(item["seconds_left"].(float64)); got != int64(10*day/time.Second) {
		t.Errorf("seconds_left = %d, want %d", got, int64(10*day/time.Second))
	}
	if heirs := item["beneficiaries"].([]any); len(heirs) != 2 {
		t.Errorf("got %d heirs, want 2", len(heirs))
	}
}

func TestListVaultsFiltersByStatus(t *testing.T) {
	owner := key(t)
	calm := solVault(t, owner, base, 90*day)
	overdue := solVault(t, owner, base.Add(-60*day), 30*day)
	h := serve(t, calm, overdue)

	_, all := get(t, h, "/v1/vaults")
	if count := all["count"].(float64); count != 2 {
		t.Fatalf("unfiltered count = %v, want 2", count)
	}

	_, filtered := get(t, h, "/v1/vaults?status=expired")
	if count := filtered["count"].(float64); count != 1 {
		t.Fatalf("expired count = %v, want 1", count)
	}
	item := filtered["vaults"].([]any)[0].(map[string]any)
	if item["address"] != overdue.Address.String() {
		t.Errorf("filtered to %v, want %s", item["address"], overdue.Address)
	}
}

func TestATriggeredVaultReportsTriggered(t *testing.T) {
	v := solVault(t, key(t), base.Add(-60*day), 30*day)
	v.IsClaimed = true
	v.ClaimPool = 5_000_000_000
	v.Beneficiaries[0].Claimed = true

	_, body := get(t, serve(t, v), "/v1/vaults")
	item := body["vaults"].([]any)[0].(map[string]any)

	if got := item["status"]; got != string(dms.StatusTriggered) {
		t.Errorf("status = %v, want triggered", got)
	}
	if got := uint64(item["claim_pool"].(float64)); got != 5_000_000_000 {
		t.Errorf("claim_pool = %d, want 5000000000", got)
	}
}

func TestGetVaultByAddress(t *testing.T) {
	v := solVault(t, key(t), base, 30*day)
	h := serve(t, v)

	rec, body := get(t, h, "/v1/vaults/"+v.Address.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body["address"] != v.Address.String() {
		t.Errorf("address = %v, want %s", body["address"], v.Address)
	}
}

func TestGetVaultRejectsSomethingThatIsNotAKey(t *testing.T) {
	rec, _ := get(t, serve(t), "/v1/vaults/not-a-public-key")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestGetVaultIsNotFoundWhenItIsNotCached(t *testing.T) {
	rec, _ := get(t, serve(t), "/v1/vaults/"+key(t).String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestListByOwnerReturnsOnlyThatOwnersVaults(t *testing.T) {
	mine, theirs := key(t), key(t)
	h := serve(t,
		solVault(t, mine, base, 30*day),
		solVault(t, theirs, base, 30*day),
		solVault(t, mine, base, 60*day),
	)

	_, body := get(t, h, "/v1/owners/"+mine.String()+"/vaults")
	if count := body["count"].(float64); count != 2 {
		t.Fatalf("count = %v, want 2", count)
	}
	for _, raw := range body["vaults"].([]any) {
		if got := raw.(map[string]any)["owner"]; got != mine.String() {
			t.Errorf("leaked a vault owned by %v", got)
		}
	}
}

func TestSPLVaultCarriesItsMint(t *testing.T) {
	mint := key(t)
	v := solVault(t, key(t), base, 30*day)
	v.Mint = &mint

	_, body := get(t, serve(t, v), "/v1/vaults")
	item := body["vaults"].([]any)[0].(map[string]any)

	if got := item["asset"]; got != "SPL" {
		t.Errorf("asset = %v, want SPL", got)
	}
	if got := item["mint"]; got != mint.String() {
		t.Errorf("mint = %v, want %s", got, mint)
	}
}

func TestHealthz(t *testing.T) {
	rec, body := get(t, serve(t, solVault(t, key(t), base, 30*day)), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if count := body["vaults"].(float64); count != 1 {
		t.Errorf("vaults = %v, want 1", count)
	}
}

func TestWritesAreNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	serve(t).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/vaults", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 — the index is read-only", rec.Code)
	}
}
