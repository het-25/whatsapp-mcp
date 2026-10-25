package main

// Admin token-management REST endpoints.
//
//   POST   /api/tokens        {label, send}  -> {token, ...}   mint a scoped token
//   GET    /api/tokens                        -> [{label, send, token_prefix}]
//   DELETE /api/tokens?id=<label-or-token>                     revoke
//
// "send" is "all", "none", or a JSON array of chat identifiers (phone numbers
// or JIDs). All three require the admin token. Read access is implicit for
// every minted token; only send scope is configurable.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

func registerTokenAdmin(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/tokens", auth(func(w http.ResponseWriter, r *http.Request) {
		caller := policyFromCtx(r.Context())
		if caller == nil || !caller.Admin {
			http.Error(w, "Forbidden: admin token required", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleListTokens(w)
		case http.MethodPost:
			handleMintToken(w, r)
		case http.MethodDelete:
			handleRevokeToken(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
}

func handleListTokens(w http.ResponseWriter) {
	type row struct {
		Label       string   `json:"label"`
		Send        string   `json:"send"`
		AllowedList []string `json:"allowed_chats,omitempty"`
		TokenPrefix string   `json:"token_prefix"`
	}
	var rows []row
	for _, p := range access.listPolicies() {
		if p.Admin {
			continue
		}
		r := row{Label: p.Label, Send: string(p.Send), TokenPrefix: prefix(p.Token)}
		if p.Send == sendList {
			for t := range p.List {
				r.AllowedList = append(r.AllowedList, t)
			}
		}
		rows = append(rows, r)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tokens": rows})
}

func handleMintToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string          `json:"label"`
		Send  json.RawMessage `json:"send"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Label) == "" {
		http.Error(w, "label is required", http.StatusBadRequest)
		return
	}
	if len(body.Send) == 0 {
		body.Send = json.RawMessage(`"none"`)
	}
	tok := newToken()
	mode, list := resolveSend(body.Send, false)
	p := &TokenPolicy{Token: tok, Label: body.Label, Send: mode, List: list, SendRaw: body.Send}
	if err := access.upsertToken(p); err != nil {
		http.Error(w, "failed to persist token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": tok,
		"label": body.Label,
		"send":  string(mode),
	})
}

func handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id query param (label or token) is required", http.StatusBadRequest)
		return
	}
	removed, err := access.removeToken(id)
	if err != nil {
		http.Error(w, "failed to persist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"removed": removed})
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func prefix(tok string) string {
	if len(tok) <= 8 {
		return tok
	}
	return tok[:8] + "…"
}
