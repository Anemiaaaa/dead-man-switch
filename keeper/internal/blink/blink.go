// Package blink serves the Dead Man's Switch as a Solana Action.
//
// An Action is a plain HTTP API in a fixed JSON dialect. A client — a wallet
// extension, a Discord bot, a QR scanner — fetches it with GET to learn what
// can be done, POSTs the user's public key, and gets back an *unsigned*
// transaction to hand to that user's wallet.
//
// The server therefore holds no keys and signs nothing: the same boundary the
// keeper keeps, for the same reason. The shapes below follow the Solana
// Actions spec v2 (`@solana/actions-spec`) field for field.
package blink

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Chain identifiers in CAIP-2 form: `solana:` followed by the first 32
// characters of the cluster's genesis hash.
const (
	ChainDevnet  = "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"
	ChainMainnet = "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"

	// ActionVersion is the spec version this server answers with.
	ActionVersion = "2.4"
)

// ActionsJSON tells clients which paths on this domain speak Actions. It is
// served from the domain root, and is to blinks roughly what robots.txt is to
// crawlers.
type ActionsJSON struct {
	Rules []Rule `json:"rules"`
}

type Rule struct {
	PathPattern string `json:"pathPattern"`
	APIPath     string `json:"apiPath"`
}

// GetResponse is what a client renders as a card.
type GetResponse struct {
	Type        string `json:"type"`
	Icon        string `json:"icon"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Label       string `json:"label"`
	// Disabled greys out the buttons; pair it with Error to say why.
	Disabled bool   `json:"disabled,omitempty"`
	Links    *Links `json:"links,omitempty"`
	Error    *Error `json:"error,omitempty"`
}

type Links struct {
	Actions []LinkedAction `json:"actions"`
}

type LinkedAction struct {
	Type       string      `json:"type"`
	Href       string      `json:"href"`
	Label      string      `json:"label"`
	Parameters []Parameter `json:"parameters,omitempty"`
}

type Parameter struct {
	Type               string `json:"type,omitempty"`
	Name               string `json:"name"`
	Label              string `json:"label,omitempty"`
	Required           bool   `json:"required,omitempty"`
	Pattern            string `json:"pattern,omitempty"`
	PatternDescription string `json:"patternDescription,omitempty"`
}

// Error is the spec's non-fatal error shape, and also what clients show when
// a request fails.
type Error struct {
	Message string `json:"message"`
}

// PostRequest is what the client sends once the user picks a button.
type PostRequest struct {
	Type string `json:"type,omitempty"`
	// Account is the base58 public key that will sign.
	Account string `json:"account"`
}

// PostResponse carries the unsigned transaction back.
type PostResponse struct {
	Type string `json:"type"`
	// Transaction is base64-encoded and unsigned.
	Transaction string `json:"transaction"`
	Message     string `json:"message,omitempty"`
}

// chainID maps a cluster name to its CAIP-2 identifier.
func chainID(cluster string) string {
	if cluster == "mainnet-beta" || cluster == "mainnet" {
		return ChainMainnet
	}
	return ChainDevnet
}

// withActionHeaders adds the CORS and version headers every Action response
// needs, and answers preflight requests.
//
// Without these a blink fails in a way that looks like a bug in the client:
// the card simply never appears, because the browser blocked the cross-origin
// request before this server saw it.
func withActionHeaders(cluster string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers",
			"Content-Type, Authorization, Content-Encoding, Accept-Encoding, "+
				"X-Accept-Action-Version, X-Accept-Blockchain-Ids")
		h.Set("Access-Control-Expose-Headers", "X-Action-Version, X-Blockchain-Ids")
		h.Set("X-Action-Version", ActionVersion)
		h.Set("X-Blockchain-Ids", chainID(cluster))

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil && logger != nil {
		logger.Error("writing response", "error", err)
	}
}

// writeError returns the shape clients display to the user verbatim, so the
// text has to read as an explanation rather than as a stack trace.
func writeError(w http.ResponseWriter, logger *slog.Logger, status int, message string) {
	writeJSON(w, logger, status, Error{Message: message})
}

// icon is served by this same process so the blink has no external
// dependency: an icon URL that 404s takes the whole card down with it.
const icon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" width="512" height="512">
  <rect width="512" height="512" rx="96" fill="#0b1020"/>
  <g stroke="#14f195" stroke-width="18" stroke-linecap="round" fill="none">
    <path d="M176 112h160M176 400h160"/>
    <path d="M176 112c0 72 80 100 80 144 0 44-80 72-80 144"/>
    <path d="M336 112c0 72-80 100-80 144 0 44 80 72 80 144"/>
  </g>
  <circle cx="256" cy="256" r="26" fill="#9945ff"/>
</svg>
`
