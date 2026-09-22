// Package api serves the read-only vault index.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/store"
)

// API exposes the cached vault state over HTTP.
//
// Read-only, and served from the cache rather than from RPC: a public endpoint
// that forwarded every request to a node would burn its rate limit on the
// first crawler.
type API struct {
	Store   store.Store
	DueSoon time.Duration
	Logger  *slog.Logger

	// Now is swappable for tests.
	Now func() time.Time
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now().UTC()
}

// Routes builds the handler. Patterns use the method-aware ServeMux, so a GET
// route answers 405 rather than 404 when someone POSTs to it.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /v1/vaults", a.listVaults)
	mux.HandleFunc("GET /v1/vaults/{address}", a.getVault)
	mux.HandleFunc("GET /v1/owners/{owner}/vaults", a.listByOwner)

	return mux
}

type vaultView struct {
	Address       string            `json:"address"`
	Owner         string            `json:"owner"`
	VaultID       uint64            `json:"vault_id"`
	Asset         string            `json:"asset"`
	Mint          *string           `json:"mint,omitempty"`
	Status        dms.Status        `json:"status"`
	LastCheckIn   time.Time         `json:"last_check_in"`
	Deadline      time.Time         `json:"deadline"`
	SecondsLeft   int64             `json:"seconds_left"`
	TimeoutSecs   int64             `json:"timeout_seconds"`
	Lamports      uint64            `json:"lamports"`
	ClaimPool     uint64            `json:"claim_pool"`
	Beneficiaries []beneficiaryView `json:"beneficiaries"`
}

type beneficiaryView struct {
	Address      string  `json:"address"`
	ShareBPS     uint16  `json:"share_bps"`
	SharePercent float64 `json:"share_percent"`
	Claimed      bool    `json:"claimed"`
}

func (a *API) view(v *dms.Vault, now time.Time) vaultView {
	asset := "SOL"
	var mint *string
	if !v.IsSOL() {
		asset = "SPL"
		key := v.Mint.String()
		mint = &key
	}

	heirs := make([]beneficiaryView, 0, len(v.Beneficiaries))
	for _, heir := range v.Beneficiaries {
		heirs = append(heirs, beneficiaryView{
			Address:      heir.Address.String(),
			ShareBPS:     heir.ShareBPS,
			SharePercent: heir.SharePercent(),
			Claimed:      heir.Claimed,
		})
	}

	return vaultView{
		Address:       v.Address.String(),
		Owner:         v.Owner.String(),
		VaultID:       v.VaultID,
		Asset:         asset,
		Mint:          mint,
		Status:        v.Status(now, a.DueSoon),
		LastCheckIn:   v.LastCheckIn,
		Deadline:      v.Deadline(),
		SecondsLeft:   int64(v.TimeLeft(now) / time.Second),
		TimeoutSecs:   int64(v.Timeout / time.Second),
		Lamports:      v.Lamports,
		ClaimPool:     v.ClaimPool,
		Beneficiaries: heirs,
	}
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	vaults, err := a.Store.List(r.Context())
	if err != nil {
		a.fail(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	a.respond(w, http.StatusOK, map[string]any{"status": "ok", "vaults": len(vaults)})
}

func (a *API) listVaults(w http.ResponseWriter, r *http.Request) {
	vaults, err := a.Store.List(r.Context())
	if err != nil {
		a.fail(w, http.StatusInternalServerError, "could not list vaults")
		return
	}
	a.respondVaults(w, vaults, r)
}

func (a *API) getVault(w http.ResponseWriter, r *http.Request) {
	address, err := solana.PublicKeyFromBase58(r.PathValue("address"))
	if err != nil {
		a.fail(w, http.StatusBadRequest, "address is not a valid public key")
		return
	}

	vault, err := a.Store.Get(r.Context(), address)
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, http.StatusNotFound, "no such vault")
		return
	case err != nil:
		a.fail(w, http.StatusInternalServerError, "could not read vault")
		return
	}

	a.respond(w, http.StatusOK, a.view(vault, a.now()))
}

func (a *API) listByOwner(w http.ResponseWriter, r *http.Request) {
	owner, err := solana.PublicKeyFromBase58(r.PathValue("owner"))
	if err != nil {
		a.fail(w, http.StatusBadRequest, "owner is not a valid public key")
		return
	}

	vaults, err := a.Store.ByOwner(r.Context(), owner)
	if err != nil {
		a.fail(w, http.StatusInternalServerError, "could not list vaults")
		return
	}
	a.respondVaults(w, vaults, r)
}

// respondVaults optionally narrows the list to one status, which is what a
// dashboard asking "show me everything overdue" wants.
func (a *API) respondVaults(w http.ResponseWriter, vaults []*dms.Vault, r *http.Request) {
	wanted := dms.Status(r.URL.Query().Get("status"))
	now := a.now()

	views := make([]vaultView, 0, len(vaults))
	for _, vault := range vaults {
		view := a.view(vault, now)
		if wanted != "" && view.Status != wanted {
			continue
		}
		views = append(views, view)
	}

	a.respond(w, http.StatusOK, map[string]any{"vaults": views, "count": len(views)})
}

func (a *API) respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil && a.Logger != nil {
		a.Logger.Error("writing response", "error", err)
	}
}

func (a *API) fail(w http.ResponseWriter, status int, message string) {
	a.respond(w, status, map[string]string{"error": message})
}
