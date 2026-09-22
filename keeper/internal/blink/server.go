package blink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/client"
	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

// VaultReader is the slice of the chain this server needs. An interface, so
// the tests can describe a vault instead of standing up an RPC node.
type VaultReader interface {
	Vault(ctx context.Context, address solana.PublicKey) (*dms.Vault, error)
}

// BlockhashSource supplies the recent blockhash every transaction needs.
type BlockhashSource interface {
	LatestBlockhash(ctx context.Context) (solana.Hash, error)
}

// Server answers the two things a vault owner and their heirs actually want
// to do from a phone: check in, and claim.
type Server struct {
	Build     client.Instructions
	Vaults    VaultReader
	Blockhash BlockhashSource

	// BaseURL is this server's public origin. Icons must be absolute URLs, so
	// a blink served from a tunnel or a staging host needs to know its own
	// address; it cannot infer it from a proxied request.
	BaseURL string
	Cluster string
	Logger  *slog.Logger

	// Now is swappable for tests.
	Now func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /actions.json", s.actionsJSON)
	mux.HandleFunc("GET /icon.svg", s.iconSVG)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, s.Logger, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/actions/check-in", s.getCheckIn)
	mux.HandleFunc("POST /api/actions/check-in", s.postCheckIn)
	mux.HandleFunc("GET /api/actions/claim", s.getClaim)
	mux.HandleFunc("POST /api/actions/claim", s.postClaim)

	// The spec requires preflight to succeed on every Action route.
	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, _ *http.Request) {})

	return withActionHeaders(s.Cluster, mux)
}

func (s *Server) actionsJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.Logger, http.StatusOK, ActionsJSON{Rules: []Rule{
		{PathPattern: "/*", APIPath: "/api/actions/*"},
		{PathPattern: "/api/actions/**", APIPath: "/api/actions/**"},
	}})
}

func (s *Server) iconSVG(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(icon))
}

func (s *Server) iconURL() string { return s.BaseURL + "/icon.svg" }

// --- check in -------------------------------------------------------------

func (s *Server) getCheckIn(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("vault")

	// Without a vault in the URL the card asks for one, which is what a blink
	// posted on its own has to do.
	if raw == "" {
		writeJSON(w, s.Logger, http.StatusOK, GetResponse{
			Type:        "action",
			Icon:        s.iconURL(),
			Title:       "Dead Man's Switch — check in",
			Description: "Tell your vault you are still here. The timer resets and your heirs stay locked out for another full period.",
			Label:       "Check in",
			Links: &Links{Actions: []LinkedAction{{
				Type:  "transaction",
				Label: "Check in",
				Href:  "/api/actions/check-in?vault={vault}",
				Parameters: []Parameter{{
					Name:     "vault",
					Label:    "Vault address",
					Required: true,
				}},
			}}},
		})
		return
	}

	vault, err := s.load(r.Context(), raw)
	if err != nil {
		writeJSON(w, s.Logger, http.StatusOK, s.unavailable("Check in", err.Error()))
		return
	}

	response := GetResponse{
		Type:        "action",
		Icon:        s.iconURL(),
		Title:       "Dead Man's Switch — check in",
		Description: s.describe(vault),
		Label:       "Check in",
		Links: &Links{Actions: []LinkedAction{{
			Type:  "transaction",
			Label: "Check in",
			Href:  "/api/actions/check-in?vault=" + vault.Address.String(),
		}}},
	}

	// A tripped vault can never be checked in again, so say so rather than
	// letting the owner sign something the program will reject.
	if vault.IsClaimed {
		response.Disabled = true
		response.Error = &Error{Message: "This vault has already been claimed. Checking in is no longer possible."}
	}

	writeJSON(w, s.Logger, http.StatusOK, response)
}

func (s *Server) postCheckIn(w http.ResponseWriter, r *http.Request) {
	account, vault, ok := s.prepare(w, r)
	if !ok {
		return
	}

	// None of these checks are security — the program enforces every one of
	// them on chain. They exist so the user is told why before they sign,
	// instead of watching a transaction fail in their wallet.
	if !account.Equals(vault.Owner) {
		writeError(w, s.Logger, http.StatusBadRequest,
			"Only the vault's owner can check in, and this vault belongs to someone else.")
		return
	}
	if vault.IsClaimed {
		writeError(w, s.Logger, http.StatusBadRequest,
			"This vault has already been claimed, so the timer can no longer be reset.")
		return
	}

	s.respondWithTransaction(w, r,
		s.Build.CheckIn(account, vault.Address),
		account,
		fmt.Sprintf("Checking in resets the timer; your heirs stay locked out until %s UTC.",
			s.now().Add(vault.Timeout).Format(time.DateTime)),
	)
}

// --- claim ----------------------------------------------------------------

func (s *Server) getClaim(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("vault")

	if raw == "" {
		writeJSON(w, s.Logger, http.StatusOK, GetResponse{
			Type:        "action",
			Icon:        s.iconURL(),
			Title:       "Dead Man's Switch — claim",
			Description: "If you are named in a vault whose owner has gone quiet past their deadline, take your share.",
			Label:       "Claim",
			Links: &Links{Actions: []LinkedAction{{
				Type:  "transaction",
				Label: "Claim my share",
				Href:  "/api/actions/claim?vault={vault}",
				Parameters: []Parameter{{
					Name:     "vault",
					Label:    "Vault address",
					Required: true,
				}},
			}}},
		})
		return
	}

	vault, err := s.load(r.Context(), raw)
	if err != nil {
		writeJSON(w, s.Logger, http.StatusOK, s.unavailable("Claim", err.Error()))
		return
	}

	response := GetResponse{
		Type:        "action",
		Icon:        s.iconURL(),
		Title:       "Dead Man's Switch — claim",
		Description: s.describe(vault),
		Label:       "Claim my share",
		Links: &Links{Actions: []LinkedAction{{
			Type:  "transaction",
			Label: "Claim my share",
			Href:  "/api/actions/claim?vault=" + vault.Address.String(),
		}}},
	}

	if left := vault.TimeLeft(s.now()); left > 0 {
		response.Disabled = true
		response.Error = &Error{Message: fmt.Sprintf(
			"The owner still has %s before this vault unlocks.", dms.HumanDuration(left))}
	}

	writeJSON(w, s.Logger, http.StatusOK, response)
}

func (s *Server) postClaim(w http.ResponseWriter, r *http.Request) {
	account, vault, ok := s.prepare(w, r)
	if !ok {
		return
	}

	index := vault.Index(account)
	if index < 0 {
		writeError(w, s.Logger, http.StatusBadRequest,
			"This wallet is not named as a beneficiary of that vault.")
		return
	}
	if vault.Beneficiaries[index].Claimed {
		writeError(w, s.Logger, http.StatusBadRequest,
			"You have already claimed your share of this vault.")
		return
	}
	if left := vault.TimeLeft(s.now()); left > 0 {
		writeError(w, s.Logger, http.StatusBadRequest, fmt.Sprintf(
			"Too early — the owner still has %s to check in.", dms.HumanDuration(left)))
		return
	}

	share := vault.Beneficiaries[index].SharePercent()
	s.respondWithTransaction(w, r,
		s.Build.ClaimSOL(account, vault.Address),
		account,
		fmt.Sprintf("Claiming your %.2f%% share of vault %s.", share, shorten(vault.Address)),
	)
}

// --- plumbing -------------------------------------------------------------

// prepare parses the POST body and the vault query, reporting to the client on
// failure. The bool says whether the caller may continue.
func (s *Server) prepare(w http.ResponseWriter, r *http.Request) (solana.PublicKey, *dms.Vault, bool) {
	var body PostRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, s.Logger, http.StatusBadRequest, "Could not read the request.")
		return solana.PublicKey{}, nil, false
	}

	account, err := solana.PublicKeyFromBase58(body.Account)
	if err != nil {
		writeError(w, s.Logger, http.StatusBadRequest, "That is not a valid wallet address.")
		return solana.PublicKey{}, nil, false
	}

	vault, err := s.load(r.Context(), r.URL.Query().Get("vault"))
	if err != nil {
		writeError(w, s.Logger, http.StatusBadRequest, err.Error())
		return solana.PublicKey{}, nil, false
	}

	return account, vault, true
}

func (s *Server) load(ctx context.Context, raw string) (*dms.Vault, error) {
	if raw == "" {
		return nil, fmt.Errorf("No vault address was given")
	}
	address, err := solana.PublicKeyFromBase58(raw)
	if err != nil {
		return nil, fmt.Errorf("That is not a valid vault address")
	}
	vault, err := s.Vaults.Vault(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("No vault found at that address")
	}

	return vault, nil
}

func (s *Server) respondWithTransaction(
	w http.ResponseWriter,
	r *http.Request,
	instruction solana.Instruction,
	payer solana.PublicKey,
	message string,
) {
	blockhash, err := s.Blockhash.LatestBlockhash(r.Context())
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("fetching a blockhash", "error", err)
		}
		writeError(w, s.Logger, http.StatusServiceUnavailable,
			"Could not reach the network just now. Try again in a moment.")
		return
	}

	tx, err := client.UnsignedTransaction(instruction, payer, blockhash)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("building a transaction", "error", err)
		}
		writeError(w, s.Logger, http.StatusInternalServerError, "Could not build the transaction.")
		return
	}

	raw, err := tx.MarshalBinary()
	if err != nil {
		writeError(w, s.Logger, http.StatusInternalServerError, "Could not encode the transaction.")
		return
	}

	writeJSON(w, s.Logger, http.StatusOK, PostResponse{
		Type:        "transaction",
		Transaction: base64.StdEncoding.EncodeToString(raw),
		Message:     message,
	})
}

// unavailable renders a card that explains itself instead of a broken one.
func (s *Server) unavailable(label, message string) GetResponse {
	return GetResponse{
		Type:        "action",
		Icon:        s.iconURL(),
		Title:       "Dead Man's Switch",
		Description: message,
		Label:       label,
		Disabled:    true,
		Error:       &Error{Message: message},
	}
}

func (s *Server) describe(v *dms.Vault) string {
	left := v.TimeLeft(s.now())
	switch {
	case v.IsClaimed:
		return fmt.Sprintf("Vault %s has been claimed; %d of %d heirs have taken their share.",
			shorten(v.Address), v.Claimed(), len(v.Beneficiaries))
	case left <= 0:
		return fmt.Sprintf("Vault %s passed its deadline %s ago. Its heirs can claim right now — "+
			"a check-in still works until the first one does.",
			shorten(v.Address), dms.HumanDuration(-left))
	default:
		return fmt.Sprintf("Vault %s unlocks for its %d heir(s) in %s, on %s UTC.",
			shorten(v.Address), len(v.Beneficiaries), dms.HumanDuration(left),
			v.Deadline().Format(time.DateTime))
	}
}

func shorten(key solana.PublicKey) string {
	s := key.String()
	if len(s) <= 12 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}
