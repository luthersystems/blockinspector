package explorer

import (
	"embed"
	"io/fs"
	"net/http"
)

// uiFiles is the web UI (luthersystems/blockinspector#30): static files that
// read only the API, served on / by the same server.
//
//go:embed ui
var uiFiles embed.FS

// uiCSP lets the UI load only its own files and call only its own origin.
// The UI puts ledger values in the page as text, and the policy is a second
// line of defence against a value that would run as script.
const uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// uiHandler serves the embedded UI files.
func uiHandler() http.Handler {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err) // the embed pattern above guarantees the directory
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
