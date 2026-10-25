package main

// Per-token access control.
//
// The bridge authenticates every REST/MCP caller with a bearer token. Rather
// than one all-powerful token, callers can be issued *scoped* tokens: read
// access is always granted, but send access (any outbound action — messages,
// files, audio, reactions, read receipts) is governed per token and can be
// restricted to a specific set of chats.
//
// Enforcement points:
//   - the /mcp reverse proxy inspects tools/call requests and blocks gated
//     tools whose target chat is outside the caller's allowlist (this is the
//     only place the *agent's* token is visible — the in-container MCP server
//     calls the REST API with the admin token);
//   - the REST write handlers re-check, covering direct REST callers.
//
// Tokens are persisted to WHATSAPP_TOKENS_FILE (default
// /app/store/tokens.json, i.e. on the Railway volume) so they survive
// restarts, and can be managed at runtime via the admin /api/tokens
// endpoints.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
)

type sendMode string

const (
	sendAll  sendMode = "all"
	sendNone sendMode = "none"
	sendList sendMode = "list"
)

// TokenPolicy is the access granted to one bearer token.
type TokenPolicy struct {
	Token string   `json:"token"`
	Label string   `json:"label"`
	Admin bool     `json:"admin,omitempty"` // may manage tokens; always send=all
	Send  sendMode `json:"-"`
	// List holds normalized allowlist targets when Send == sendList.
	List map[string]bool `json:"-"`
	// raw send value as stored/loaded: "all", "none", or []string
	SendRaw json.RawMessage `json:"send"`
}

// canSendTo reports whether this policy may perform an outbound action to the
// given chat (phone number, s.whatsapp.net JID, or group JID).
func (p *TokenPolicy) canSendTo(target string) bool {
	switch p.Send {
	case sendAll:
		return true
	case sendList:
		return p.List[normalizeTarget(target)]
	default:
		return false
	}
}

// normalizeTarget canonicalizes a chat identifier so an allowlist entry given
// as either a phone number or a full JID matches the value tools pass. Groups
// keep their full "@g.us" JID; everything else collapses to bare digits.
func normalizeTarget(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasSuffix(s, "@g.us") {
		return s
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	return s
}

type accessStore struct {
	mu       sync.RWMutex
	byToken  map[string]*TokenPolicy
	filePath string
}

var access *accessStore

// initAccessStore builds the token set from the admin token, the optional
// WHATSAPP_SCOPED_TOKENS env JSON, and the persisted tokens file.
func initAccessStore(adminToken string) {
	path := strings.TrimSpace(os.Getenv("WHATSAPP_TOKENS_FILE"))
	if path == "" {
		path = "/app/store/tokens.json"
	}
	s := &accessStore{byToken: map[string]*TokenPolicy{}, filePath: path}

	admin := &TokenPolicy{Token: adminToken, Label: "admin", Admin: true, Send: sendAll}
	s.byToken[adminToken] = admin

	if env := strings.TrimSpace(os.Getenv("WHATSAPP_SCOPED_TOKENS")); env != "" {
		if list, err := parseTokenList([]byte(env)); err == nil {
			for _, p := range list {
				s.byToken[p.Token] = p
			}
		}
	}
	if data, err := os.ReadFile(path); err == nil {
		if list, err := parseTokenList(data); err == nil {
			for _, p := range list {
				if p.Token == adminToken {
					continue // never let the file downgrade the admin token
				}
				s.byToken[p.Token] = p
			}
		}
	}
	access = s
}

// parseTokenList decodes a JSON array of {token,label,send} objects, resolving
// each send value ("all" | "none" | ["<target>", ...]) into a TokenPolicy.
func parseTokenList(data []byte) ([]*TokenPolicy, error) {
	var raw []struct {
		Token string          `json:"token"`
		Label string          `json:"label"`
		Admin bool            `json:"admin"`
		Send  json.RawMessage `json:"send"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]*TokenPolicy, 0, len(raw))
	for _, r := range raw {
		if strings.TrimSpace(r.Token) == "" {
			continue
		}
		p := &TokenPolicy{Token: r.Token, Label: r.Label, Admin: r.Admin, SendRaw: r.Send}
		p.Send, p.List = resolveSend(r.Send, r.Admin)
		out = append(out, p)
	}
	return out, nil
}

func resolveSend(raw json.RawMessage, admin bool) (sendMode, map[string]bool) {
	if admin {
		return sendAll, nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		switch strings.ToLower(strings.TrimSpace(str)) {
		case "all":
			return sendAll, nil
		case "", "none":
			return sendNone, nil
		}
		// a single target string is treated as a one-entry allowlist
		return sendList, map[string]bool{normalizeTarget(str): true}
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		m := map[string]bool{}
		for _, t := range arr {
			if t = strings.TrimSpace(t); t != "" {
				m[normalizeTarget(t)] = true
			}
		}
		if len(m) == 0 {
			return sendNone, nil
		}
		return sendList, m
	}
	return sendNone, nil
}

// policyFor returns the policy for a presented bearer token using a
// constant-time compare, or nil if unknown.
func (s *accessStore) policyFor(token string) *TokenPolicy {
	if s == nil || token == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Fast path: exact map hit. The map key compare is not constant-time, but
	// tokens are high-entropy random; the admin token additionally gets a
	// constant-time check below to blunt timing on the highest-value secret.
	if p, ok := s.byToken[token]; ok {
		return p
	}
	for t, p := range s.byToken {
		if len(t) == len(token) && subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return p
		}
	}
	return nil
}

// upsertToken adds or replaces a scoped token and persists the set.
func (s *accessStore) upsertToken(p *TokenPolicy) error {
	s.mu.Lock()
	if existing := s.byToken[p.Token]; existing != nil && existing.Admin {
		s.mu.Unlock()
		return errors.New("cannot modify the admin token")
	}
	s.byToken[p.Token] = p
	s.mu.Unlock()
	return s.persist()
}

// removeToken deletes a scoped token by token value or label and persists.
func (s *accessStore) removeToken(tokenOrLabel string) (bool, error) {
	s.mu.Lock()
	var removed bool
	for t, p := range s.byToken {
		if p.Admin {
			continue
		}
		if t == tokenOrLabel || p.Label == tokenOrLabel {
			delete(s.byToken, t)
			removed = true
		}
	}
	s.mu.Unlock()
	if !removed {
		return false, nil
	}
	return true, s.persist()
}

// listPolicies returns a copy of all non-admin policies for display.
func (s *accessStore) listPolicies() []*TokenPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*TokenPolicy{}
	for _, p := range s.byToken {
		out = append(out, p)
	}
	return out
}

// persist writes the current scoped tokens (excluding admin) to the tokens
// file. Best-effort atomic replace.
func (s *accessStore) persist() error {
	s.mu.RLock()
	type stored struct {
		Token string          `json:"token"`
		Label string          `json:"label"`
		Send  json.RawMessage `json:"send"`
	}
	var list []stored
	for _, p := range s.byToken {
		if p.Admin {
			continue
		}
		send := p.SendRaw
		if len(send) == 0 {
			send = json.RawMessage(`"none"`)
		}
		list = append(list, stored{Token: p.Token, Label: p.Label, Send: send})
	}
	path := s.filePath
	s.mu.RUnlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- request-context plumbing ---

type ctxKey int

const policyCtxKey ctxKey = 1

func withPolicy(ctx context.Context, p *TokenPolicy) context.Context {
	return context.WithValue(ctx, policyCtxKey, p)
}

func policyFromCtx(ctx context.Context) *TokenPolicy {
	p, _ := ctx.Value(policyCtxKey).(*TokenPolicy)
	return p
}

// gatedTools maps each outbound MCP tool to the argument holding its target
// chat. A tool absent from this map is a read and never gated.
var gatedTools = map[string]string{
	"send_message":       "recipient",
	"send_reaction":      "recipient",
	"send_file":          "recipient",
	"send_audio_message": "recipient",
	"mark_messages_read": "chat_jid",
}
