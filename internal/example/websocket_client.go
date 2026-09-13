package example

import (
	_ "embed"
	"net/http"
)

//go:embed websocket_client.html
var websocketClientPage []byte

// WebsocketClientHandler serves a self-contained browser client for the room
// websocket endpoint. It is a development aid, mounted without auth; the
// socket itself is still authenticated with the token pasted into the page.
func WebsocketClientHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(websocketClientPage)
	}
}
