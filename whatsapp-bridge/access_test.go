package main

import (
	"encoding/json"
	"testing"
)

func policyFromJSON(t *testing.T, send string) *TokenPolicy {
	t.Helper()
	list, err := parseTokenList([]byte(`[{"token":"tok","label":"x","send":` + send + `}]`))
	if err != nil || len(list) != 1 {
		t.Fatalf("parse failed: %v", err)
	}
	return list[0]
}

func TestCanSendTo(t *testing.T) {
	cases := []struct {
		name   string
		send   string
		target string
		want   bool
	}{
		{"all allows anything", `"all"`, "919876543210", true},
		{"none blocks", `"none"`, "919876543210", false},
		{"list allows listed phone", `["919876543210"]`, "919876543210", true},
		{"list allows listed via JID", `["919876543210"]`, "919876543210@s.whatsapp.net", true},
		{"list allows JID entry via phone", `["919876543210@s.whatsapp.net"]`, "919876543210", true},
		{"list blocks unlisted", `["919876543210"]`, "919999999999", false},
		{"list group match", `["120363000000000001@g.us"]`, "120363000000000001@g.us", true},
		{"list group vs phone no match", `["120363000000000001@g.us"]`, "919876543210", false},
		{"empty list is none", `[]`, "919876543210", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := policyFromJSON(t, c.send)
			if got := p.canSendTo(c.target); got != c.want {
				t.Fatalf("canSendTo(%q) = %v, want %v (send=%s)", c.target, got, c.want, c.send)
			}
		})
	}
}

func TestAdminAlwaysSendsAll(t *testing.T) {
	list, err := parseTokenList([]byte(`[{"token":"a","label":"adm","admin":true,"send":"none"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if !list[0].canSendTo("919999999999") {
		t.Fatal("admin should send to anything regardless of send value")
	}
}

func TestExtractStringArg(t *testing.T) {
	args := json.RawMessage(`{"recipient":"919876543210","message":"hi"}`)
	if got := extractStringArg(args, "recipient"); got != "919876543210" {
		t.Fatalf("got %q", got)
	}
	if got := extractStringArg(args, "chat_jid"); got != "" {
		t.Fatalf("expected empty for missing key, got %q", got)
	}
}

func TestNormalizeTarget(t *testing.T) {
	cases := map[string]string{
		"919876543210":                "919876543210",
		"919876543210@s.whatsapp.net": "919876543210",
		"+91 98765 43210":             "919876543210",
		"120363000000000001@g.us":     "120363000000000001@g.us",
	}
	for in, want := range cases {
		if got := normalizeTarget(in); got != want {
			t.Fatalf("normalizeTarget(%q) = %q, want %q", in, got, want)
		}
	}
}
