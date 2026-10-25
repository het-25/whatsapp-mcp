package main

// MCP-proxy send-scope enforcement.
//
// enforceMCPSendScope buffers the JSON-RPC request, and if it is a tools/call
// for a gated outbound tool whose target chat is outside the caller's
// allowlist, it answers with a JSON-RPC error and returns false (so the proxy
// does not forward). All other requests — reads, initialize, notifications,
// batches — are restored and allowed through.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

const maxMCPBody = 8 << 20 // 8 MiB; tool-call payloads are tiny, media goes by path

func enforceMCPSendScope(w http.ResponseWriter, r *http.Request) bool {
	policy := policyFromCtx(r.Context())
	if policy == nil || policy.Send == sendAll {
		return true // admin / unrestricted: nothing to check
	}
	if r.Body == nil {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPBody))
	_ = r.Body.Close()
	if err != nil {
		writeJSONRPCError(w, nil, "failed to read request body")
		return false
	}
	// Always restore the body for the forward path.
	restore := func() {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}

	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		// Not a single JSON-RPC object (e.g. a batch array). Let it through;
		// MCP clients send tool calls as single objects, and reads are safe.
		restore()
		return true
	}
	if req.Method != "tools/call" {
		restore()
		return true
	}
	argName, gated := gatedTools[req.Params.Name]
	if !gated {
		restore() // a read tool
		return true
	}
	target := extractStringArg(req.Params.Arguments, argName)
	if target == "" {
		// Can't determine a target for a gated tool — fail closed.
		writeJSONRPCError(w, req.ID, "send access denied: could not determine target chat for "+req.Params.Name)
		return false
	}
	if !policy.canSendTo(target) {
		writeJSONRPCError(w, req.ID, "send access denied for this token: "+req.Params.Name+" to "+target+" is not in the allowed-chats list")
		return false
	}
	restore()
	return true
}

func extractStringArg(args json.RawMessage, key string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// writeJSONRPCError returns a JSON-RPC error object. Content-Type application/
// json is valid for a single streamable-http response, and MCP clients render
// the message back to the model/user.
func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    -32001,
			"message": message,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
