package auth

import (
	_ "embed"
	"net/http"
)

//go:embed web_client.html
var webClientPage []byte

// WebClientHandler serves a self-contained browser client for the auth
// endpoints. It is a development aid, mounted without auth; it drives the
// public /api/v1/auth/* JSON endpoints from the browser.
func WebClientHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(webClientPage)
	}
}
