package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// notifyLoggedOut POSTs a JSON alert to LOGOUT_NOTIFY_URL (if set) when the
// linked device is logged out, so the operator learns they need to re-scan
// the QR. Best-effort: a missing URL or a failed POST never blocks the
// restart path.
//
// The payload carries both FormSubmit-style fields (_subject, message) and
// Slack/Discord-style fields (text), so a single URL works whether it points
// at FormSubmit (https://formsubmit.co/ajax/<token>), a chat webhook, or any
// JSON handler. FormSubmit rejects requests without an Origin/Referer, so
// LOGOUT_NOTIFY_ORIGIN (the bridge's own public URL) is sent as both headers
// when set; other receivers ignore them.
func notifyLoggedOut(reason any, logger waLog.Logger) {
	url := os.Getenv("LOGOUT_NOTIFY_URL")
	if url == "" {
		return
	}
	msg := "WhatsApp bridge was logged out and needs re-pairing. Open the /pair link and scan the QR again."
	payload := map[string]any{
		"_subject": "⚠️ WhatsApp bridge logged out — re-scan needed",
		"message":  msg,
		"text":     msg,
		"event":    "whatsapp_logged_out",
		"reason":   reason,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if origin := os.Getenv("LOGOUT_NOTIFY_ORIGIN"); origin != "" {
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if logger != nil {
			logger.Warnf("logout notification POST failed: %v", err)
		}
		return
	}
	_ = resp.Body.Close()
}
