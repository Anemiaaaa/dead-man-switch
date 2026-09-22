package blink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/client"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
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

type fakeVaults struct {
	vaults map[solana.PublicKey]*dms.Vault
}

func (f *fakeVaults) Vault(_ context.Context, address solana.PublicKey) (*dms.Vault, error) {
	v, ok := f.vaults[address]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}

type fakeBlockhash struct{ err error }

func (f fakeBlockhash) LatestBlockhash(context.Context) (solana.Hash, error) {
	if f.err != nil {
		return solana.Hash{}, f.err
	}
	return solana.HashFromBytes(make([]byte, 32)), nil
}

type fixture struct {
	handler   http.Handler
	program   solana.PublicKey
	vault     *dms.Vault
	owner     solana.PublicKey
	heir      solana.PublicKey
	blockhash fakeBlockhash
}

func newFixture(t *testing.T, lastCheckIn time.Time, timeout time.Duration) *fixture {
	t.Helper()

	program := key(t)
	owner := key(t)
	heir := key(t)
	other := key(t)

	vault := &dms.Vault{
		Address:     key(t),
		Owner:       owner,
		VaultID:     1,
		LastCheckIn: lastCheckIn,
		Timeout:     timeout,
		Lamports:    5_000_000_000,
		Beneficiaries: []dms.Beneficiary{
			{Address: heir, ShareBPS: 6_000},
			{Address: other, ShareBPS: 4_000},
		},
	}

	f := &fixture{program: program, vault: vault, owner: owner, heir: heir}
	f.handler = (&Server{
		Build:     client.NewInstructions(program),
		Vaults:    &fakeVaults{vaults: map[solana.PublicKey]*dms.Vault{vault.Address: vault}},
		Blockhash: f.blockhash,
		BaseURL:   "https://dms.example",
		Cluster:   "devnet",
		Now:       func() time.Time { return base },
	}).Routes()

	return f
}

func (f *fixture) get(t *testing.T, path string) (*httptest.ResponseRecorder, GetResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	var body GetResponse
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v (%q)", path, err, rec.Body.String())
		}
	}
	return rec, body
}

func (f *fixture) post(t *testing.T, path, account string) (*httptest.ResponseRecorder, PostResponse, Error) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"account":"`+account+`"}`))
	req.Header.Set("Content-Type", "application/json")
	f.handler.ServeHTTP(rec, req)

	var ok PostResponse
	var bad Error
	_ = json.Unmarshal(rec.Body.Bytes(), &ok)
	_ = json.Unmarshal(rec.Body.Bytes(), &bad)

	return rec, ok, bad
}

// --- spec conformance -----------------------------------------------------

func TestActionsJSONMapsTheDomainToTheAPI(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/actions.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body ActionsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Rules) == 0 {
		t.Fatal("actions.json must carry at least one rule or no blink will resolve")
	}
	if body.Rules[0].PathPattern == "" || body.Rules[0].APIPath == "" {
		t.Errorf("rule = %+v, want both fields set", body.Rules[0])
	}
}

// A blink that fails CORS never renders, and the failure looks like a broken
// client rather than a missing header.
func TestPreflightIsAnswered(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/actions/check-in", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin": "*",
		"X-Action-Version":            ActionVersion,
		"X-Blockchain-Ids":            ChainDevnet,
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	for _, want := range []string{"Content-Type", "Authorization", "Content-Encoding", "Accept-Encoding"} {
		if !strings.Contains(allowed, want) {
			t.Errorf("Access-Control-Allow-Headers is missing %q (got %q)", want, allowed)
		}
	}
}

func TestMainnetReportsItsOwnChainID(t *testing.T) {
	handler := (&Server{Cluster: "mainnet-beta"}).Routes()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/actions/check-in", nil))

	if got := rec.Header().Get("X-Blockchain-Ids"); got != ChainMainnet {
		t.Errorf("X-Blockchain-Ids = %q, want %q", got, ChainMainnet)
	}
}

// --- the card -------------------------------------------------------------

func TestCheckInWithoutAVaultAsksForOne(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec, body := f.get(t, "/api/actions/check-in")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.Type != "action" || body.Icon == "" || body.Title == "" || body.Label == "" {
		t.Fatalf("card is missing required fields: %+v", body)
	}
	if !strings.HasPrefix(body.Icon, "https://dms.example/") {
		t.Errorf("icon = %q, want an absolute URL on the configured origin", body.Icon)
	}
	if body.Links == nil || len(body.Links.Actions) != 1 {
		t.Fatalf("want exactly one linked action, got %+v", body.Links)
	}

	action := body.Links.Actions[0]
	if action.Type != "transaction" {
		t.Errorf("action type = %q, want transaction", action.Type)
	}
	if len(action.Parameters) != 1 || action.Parameters[0].Name != "vault" {
		t.Fatalf("want a single required `vault` parameter, got %+v", action.Parameters)
	}
	if !action.Parameters[0].Required {
		t.Error("the vault parameter must be required")
	}
	if !strings.Contains(action.Href, "{vault}") {
		t.Errorf("href = %q, want the {vault} placeholder", action.Href)
	}
}

// Deployed behind a proxy the app has no way of knowing its own public name
// ahead of time, so an unset BaseURL has to fall back to what the proxy says.
func TestIconURLFollowsTheForwardedHeadersWhenUnconfigured(t *testing.T) {
	handler := (&Server{
		Cluster: "devnet",
		Now:     func() time.Time { return base },
	}).Routes()

	cases := []struct {
		name    string
		headers map[string]string
		host    string
		want    string
	}{
		{
			name:    "terminated TLS in front of us",
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			host:    "dms.fly.dev",
			want:    "https://dms.fly.dev/icon.svg",
		},
		{
			name: "a chain of proxies",
			headers: map[string]string{
				"X-Forwarded-Proto": "https, http",
				"X-Forwarded-Host":  "dms.example, internal",
			},
			host: "internal:8081",
			want: "https://dms.example/icon.svg",
		},
		{
			name: "nothing in front of us at all",
			host: "localhost:8081",
			want: "http://localhost:8081/icon.svg",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/actions/check-in", nil)
			req.Host = c.host
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			var body GetResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if body.Icon != c.want {
				t.Errorf("icon = %q, want %q", body.Icon, c.want)
			}
		})
	}
}

// An explicit setting must win, because a proxy header is attacker-controlled
// in the general case.
func TestAConfiguredBaseURLBeatsTheHeaders(t *testing.T) {
	f := newFixture(t, base, 30*day)

	req := httptest.NewRequest(http.MethodGet, "/api/actions/check-in", nil)
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	var body GetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if body.Icon != "https://dms.example/icon.svg" {
		t.Errorf("icon = %q, want the configured origin", body.Icon)
	}
}

func TestCheckInForAKnownVaultShowsItsDeadline(t *testing.T) {
	f := newFixture(t, base, 30*day)

	_, body := f.get(t, "/api/actions/check-in?vault="+f.vault.Address.String())
	if body.Disabled {
		t.Error("an armed vault should offer the button")
	}
	// The fixture checks in at `base` and the clock is `base`, so the full
	// timeout is still ahead.
	if !strings.Contains(body.Description, "30 days") {
		t.Errorf("description = %q, want the time left spelled out", body.Description)
	}
	if body.Links.Actions[0].Parameters != nil {
		t.Error("the vault is already known, so no parameter should be asked for")
	}
}

func TestClaimIsOfferedButDisabledBeforeTheDeadline(t *testing.T) {
	f := newFixture(t, base, 30*day)

	_, body := f.get(t, "/api/actions/claim?vault="+f.vault.Address.String())
	if !body.Disabled {
		t.Error("claiming early must not look available")
	}
	if body.Error == nil || !strings.Contains(body.Error.Message, "30 days") {
		t.Errorf("error = %+v, want it to say how long is left", body.Error)
	}
}

func TestClaimIsLiveOnceTheDeadlineHasPassed(t *testing.T) {
	f := newFixture(t, base.Add(-40*day), 30*day)

	_, body := f.get(t, "/api/actions/claim?vault="+f.vault.Address.String())
	if body.Disabled {
		t.Errorf("an expired vault should be claimable: %+v", body.Error)
	}
	if !strings.Contains(body.Description, "passed its deadline") {
		t.Errorf("description = %q", body.Description)
	}
}

func TestAnUnknownVaultExplainsItselfInsteadOfBreaking(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec, body := f.get(t, "/api/actions/check-in?vault="+key(t).String())
	// Still a 200 with a valid card: a client shows the message, where a 500
	// would render as a blank or broken blink.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with an explanatory card", rec.Code)
	}
	if !body.Disabled || body.Error == nil {
		t.Fatalf("want a disabled card carrying an error, got %+v", body)
	}
}

// --- the transaction ------------------------------------------------------

func TestCheckInReturnsAnUnsignedTransactionForTheOwner(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec, body, _ := f.post(t, "/api/actions/check-in?vault="+f.vault.Address.String(), f.owner.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if body.Type != "transaction" {
		t.Errorf("type = %q, want transaction", body.Type)
	}
	if body.Message == "" {
		t.Error("the wallet shows Message to the user; it should not be empty")
	}

	raw, err := base64.StdEncoding.DecodeString(body.Transaction)
	if err != nil {
		t.Fatalf("transaction is not valid base64: %v", err)
	}
	tx, err := solana.TransactionFromBytes(raw)
	if err != nil {
		t.Fatalf("a wallet could not parse this transaction: %v", err)
	}

	if got := tx.Message.AccountKeys[0]; !got.Equals(f.owner) {
		t.Errorf("fee payer = %s, want the requesting account %s", got, f.owner)
	}
	if len(tx.Message.Instructions) != 1 {
		t.Fatalf("want exactly one instruction, got %d", len(tx.Message.Instructions))
	}
	if got := tx.Message.AccountKeys[tx.Message.Instructions[0].ProgramIDIndex]; !got.Equals(f.program) {
		t.Errorf("instruction targets %s, want the program %s", got, f.program)
	}

	// Unsigned, but with the signature slot present — a wallet reads the
	// count from the header and would reject a transaction without it.
	if len(tx.Signatures) != 1 {
		t.Fatalf("signature slots = %d, want 1", len(tx.Signatures))
	}
	if !tx.Signatures[0].IsZero() {
		t.Error("the server must not sign; the slot should be empty")
	}
}

func TestCheckInRefusesAnyoneButTheOwner(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec, _, bad := f.post(t, "/api/actions/check-in?vault="+f.vault.Address.String(), f.heir.String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(bad.Message, "owner") {
		t.Errorf("message = %q, want it to explain the problem", bad.Message)
	}
}

func TestClaimReturnsATransactionForANamedHeir(t *testing.T) {
	f := newFixture(t, base.Add(-40*day), 30*day)

	rec, body, _ := f.post(t, "/api/actions/claim?vault="+f.vault.Address.String(), f.heir.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(body.Message, "60.00%") {
		t.Errorf("message = %q, want the heir's share named", body.Message)
	}

	raw, _ := base64.StdEncoding.DecodeString(body.Transaction)
	tx, err := solana.TransactionFromBytes(raw)
	if err != nil {
		t.Fatalf("unparsable transaction: %v", err)
	}
	if got := tx.Message.AccountKeys[0]; !got.Equals(f.heir) {
		t.Errorf("fee payer = %s, want the heir %s", got, f.heir)
	}
}

func TestClaimRefusesAWalletThatIsNotAnHeir(t *testing.T) {
	f := newFixture(t, base.Add(-40*day), 30*day)

	rec, _, bad := f.post(t, "/api/actions/claim?vault="+f.vault.Address.String(), key(t).String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(bad.Message, "beneficiary") {
		t.Errorf("message = %q", bad.Message)
	}
}

func TestClaimRefusesBeforeTheDeadline(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec, _, bad := f.post(t, "/api/actions/claim?vault="+f.vault.Address.String(), f.heir.String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(bad.Message, "Too early") {
		t.Errorf("message = %q", bad.Message)
	}
}

func TestClaimRefusesASecondTime(t *testing.T) {
	f := newFixture(t, base.Add(-40*day), 30*day)
	f.vault.Beneficiaries[0].Claimed = true

	rec, _, bad := f.post(t, "/api/actions/claim?vault="+f.vault.Address.String(), f.heir.String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(bad.Message, "already claimed") {
		t.Errorf("message = %q", bad.Message)
	}
}

func TestAGarbageAccountIsRejectedPolitely(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec, _, bad := f.post(t, "/api/actions/check-in?vault="+f.vault.Address.String(), "not-a-wallet")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if bad.Message == "" {
		t.Error("the client shows this message to a human; it must not be empty")
	}
}

// An RPC outage should read as "try again", not as a broken blink.
func TestABlockhashFailureIsReportedAsTemporary(t *testing.T) {
	program := key(t)
	owner := key(t)
	vault := &dms.Vault{
		Address:     key(t),
		Owner:       owner,
		LastCheckIn: base,
		Timeout:     30 * day,
	}
	handler := (&Server{
		Build:     client.NewInstructions(program),
		Vaults:    &fakeVaults{vaults: map[solana.PublicKey]*dms.Vault{vault.Address: vault}},
		Blockhash: fakeBlockhash{err: errors.New("rpc is down")},
		BaseURL:   "https://dms.example",
		Cluster:   "devnet",
		Now:       func() time.Time { return base },
	}).Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/actions/check-in?vault="+vault.Address.String(),
		strings.NewReader(`{"account":"`+owner.String()+`"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestIconIsServedFromThisProcess(t *testing.T) {
	f := newFixture(t, base, 30*day)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/icon.svg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a 404 icon takes the whole card down", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q", got)
	}
}
