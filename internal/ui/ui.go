// Package ui serves the embedded management page: one self-contained HTML
// file, no build system, no external assets — the binary stays the whole
// product (RabbitMQ-management style, sierpe-web aesthetic).
//
// The page is a pure consumer of the public and admin HTTP APIs: it holds
// no state and adds no surface of its own. Reads work without credentials
// (matching the open-reads access model); mutations prompt for the admin
// token and send it as the same bearer header curl would.
//
// Only "/" is served, so the page's views live in the URL fragment
// (#/?q=… for the contract list, #/c/C…/tab?filters for a contract) and
// deep links survive a reload. Values decoded from the chain are
// attacker-controlled: the page renders them as DOM text, never as HTML,
// and builds outbound links only from validated ids.
package ui

import (
	_ "embed"
	"net/http"
)

//go:embed index.html
var indexHTML []byte

// Register mounts the UI at the root path. "GET /{$}" matches exactly "/",
// so it never shadows the API routes.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The page links out to Stellar Expert; the instance's address
		// (often a private tailnet name) must not travel along as Referer.
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Write(indexHTML)
	})
}
