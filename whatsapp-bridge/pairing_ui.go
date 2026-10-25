package main

// Browser-based pairing UI.
//
// While the bridge has no WhatsApp session it serves a small web page at
// /pair showing the current pairing QR code, so linking can be done from a
// browser — useful when the bridge runs on a remote host (Railway, a VPS)
// where nobody can see the terminal QR. The page is protected by HTTP Basic
// Auth and the whole feature is enabled only when both PAIR_UI_USER and
// PAIR_UI_PASS are set.
//
// Lifecycle: startPairingUI binds the same host:port the REST API will use
// later; once pairing succeeds, main() calls stopPairingUI before
// startRESTServer takes over the port. The rotating QR codes are pushed in
// via pairingSetCode from the QR event loop.

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"
)

// bridgeBindHost returns the address the bridge binds on. Loopback by
// default; remote deployments behind a TLS proxy (Railway, a VPS) set
// WHATSAPP_BRIDGE_BIND=0.0.0.0.
func bridgeBindHost() string {
	if v := strings.TrimSpace(os.Getenv("WHATSAPP_BRIDGE_BIND")); v != "" {
		return v
	}
	return "127.0.0.1"
}

var pairingUI struct {
	mu     sync.Mutex
	code   string
	server *http.Server
}

func pairingSetCode(code string) {
	pairingUI.mu.Lock()
	pairingUI.code = code
	pairingUI.mu.Unlock()
}

func pairingClearCode() { pairingSetCode("") }

// pairingUIEnabled reports whether credentials for the pairing page are set.
func pairingUIEnabled() bool {
	return os.Getenv("PAIR_UI_USER") != "" && os.Getenv("PAIR_UI_PASS") != ""
}

// pairingBasicAuth wraps a handler with auth against PAIR_UI_USER /
// PAIR_UI_PASS. Three accepted forms, so pairing works without typing
// credentials into the browser's native dialog:
//  1. a ?key=<PAIR_UI_PASS> query parameter — sets a cookie and redirects to
//     the clean URL, giving a single clickable pairing link;
//  2. the cookie set by (1);
//  3. classic HTTP Basic Auth as a fallback.
func pairingBasicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := os.Getenv("PAIR_UI_USER")
		pass := os.Getenv("PAIR_UI_PASS")
		if key := r.URL.Query().Get("key"); key != "" {
			if subtle.ConstantTimeCompare([]byte(key), []byte(pass)) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name: "pair_key", Value: key, Path: "/",
					HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
					SameSite: http.SameSiteLaxMode, MaxAge: 86400,
				})
				clean := *r.URL
				q := clean.Query()
				q.Del("key")
				clean.RawQuery = q.Encode()
				http.Redirect(w, r, clean.String(), http.StatusFound)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if c, err := r.Cookie("pair_key"); err == nil &&
			subtle.ConstantTimeCompare([]byte(c.Value), []byte(pass)) == 1 {
			next(w, r)
			return
		}
		if u, p, ok := r.BasicAuth(); ok &&
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1 &&
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1 {
			next(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="whatsapp-bridge pairing"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

const pairPageHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Link WhatsApp</title>
<style>
  body { font-family: -apple-system, "Segoe UI", sans-serif; background: #111b21; color: #e9edef;
         display: flex; flex-direction: column; align-items: center; justify-content: center;
         min-height: 100vh; margin: 0; text-align: center; }
  h1 { font-size: 1.25rem; font-weight: 600; margin: 0 16px; }
  .qr { background: #fff; padding: 14px; border-radius: 12px; margin: 20px; min-width: 300px; min-height: 300px;
        display: flex; align-items: center; justify-content: center; }
  .qr img { display: block; width: 300px; height: 300px; image-rendering: pixelated; }
  p { color: #8696a0; max-width: 420px; line-height: 1.5; margin: 4px 16px; }
  b { color: #e9edef; }
  #status { min-height: 1.2em; color: #8696a0; font-size: 0.9rem; }
</style>
</head>
<body>
<h1>Link your WhatsApp</h1>
<div class="qr"><img id="qr" alt="QR code loading…"></div>
<div id="status">loading…</div>
<p>On your phone: <b>WhatsApp → Settings → Linked Devices → Link a Device</b>, then scan this code.</p>
<p>The code rotates automatically. Once WhatsApp shows the device as linked, you're done — this page will stop responding shortly after.</p>
<script>
  const img = document.getElementById('qr');
  const status = document.getElementById('status');
  async function refresh() {
    try {
      const r = await fetch('/pair/qr.png?t=' + Date.now(), {cache: 'no-store'});
      if (r.ok) {
        const blob = await r.blob();
        img.src = URL.createObjectURL(blob);
        status.textContent = '';
      } else if (r.status === 404) {
        status.textContent = 'waiting for a fresh code… (can take up to a minute)';
      }
    } catch (e) {
      status.textContent = 'bridge restarting — hang on…';
    }
  }
  refresh();
  setInterval(refresh, 5000);
</script>
</body>
</html>
`

// startPairingUI serves the pairing page on addr until stopPairingUI is
// called. No-op when the PAIR_UI_USER/PAIR_UI_PASS credentials are unset.
func startPairingUI(addr string) {
	if !pairingUIEnabled() {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pair", pairingBasicAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, pairPageHTML)
	}))
	mux.HandleFunc("/pair/qr.png", pairingBasicAuth(func(w http.ResponseWriter, r *http.Request) {
		pairingUI.mu.Lock()
		code := pairingUI.code
		pairingUI.mu.Unlock()
		if code == "" {
			http.Error(w, "no active pairing code yet", http.StatusNotFound)
			return
		}
		img, err := qr.Encode(code, qr.M)
		if err != nil {
			http.Error(w, "failed to render QR", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(img.PNG())
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pair", http.StatusFound)
	})
	pairingUI.mu.Lock()
	pairingUI.server = &http.Server{
		Addr:         addr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		Handler:      mux,
	}
	server := pairingUI.server
	pairingUI.mu.Unlock()
	fmt.Printf("Pairing UI available at http://%s/pair\n", addr)
	go func() { _ = server.ListenAndServe() }()
}

// stopPairingUI shuts the pairing page down so the REST API can take the
// port. Safe to call when the UI never started.
func stopPairingUI() {
	pairingUI.mu.Lock()
	server := pairingUI.server
	pairingUI.server = nil
	pairingUI.mu.Unlock()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
	pairingClearCode()
}
