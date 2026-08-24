package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

//go:embed public
var publicFS embed.FS

// NewHandler builds the mailbox API and UI.
//
// smtpReady is set by main once the SMTP listener has actually bound. /healthz
// consults it, because a health endpoint served purely by the HTTP mux proves
// only that the HTTP goroutine is alive: if ListenAndServeTLS fails on a port
// clash or a bad cert, an HTTP-only /healthz still returns 200, the Kubernetes
// readiness probe passes, the Docker HEALTHCHECK passes, compose's
// service_healthy gate opens, and the whole e2e harness starts against a
// devmail with no SMTP listener at all.
func NewHandler(cfg *Config, st *Store, smtpReady *atomic.Bool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Never gated: the k8s probe and the Docker HEALTHCHECK have no token.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		count, bytes, evicted := st.Stats()
		if !smtpReady.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "error", "error": "smtp listener not ready", "version": version,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "version": version,
			"messages": count, "bytes": bytes, "evicted": evicted,
		})
	})

	// Never gated: the shell is an empty template and carries no data. The
	// browser bootstraps its token from ?token= into sessionStorage.
	//
	// Registered without a method: Go 1.22's mux refuses to register "GET /"
	// alongside "/api/", because /api/ is the more specific path yet matches
	// more methods. http.FileServer answers 405 for anything but GET/HEAD.
	mux.Handle("/", http.FileServer(http.FS(publicRoot())))

	api := http.NewServeMux()

	api.HandleFunc("GET /api/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, st.List())
	})

	api.HandleFunc("GET /api/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := st.Get(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such message"})
			return
		}
		// Remote content is stripped unless explicitly requested, so opening a
		// trapped message cannot fire a tracking pixel at a third party.
		stripRemote := r.URL.Query().Get("remote") != "1"
		writeJSON(w, http.StatusOK, Parse(m, stripRemote))
	})

	api.HandleFunc("GET /api/messages/{id}/raw", func(w http.ResponseWriter, r *http.Request) {
		m, ok := st.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "no such message", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(m.Raw)
	})

	api.HandleFunc("GET /api/messages/{id}/attachments/{n}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := st.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "no such message", http.StatusNotFound)
			return
		}
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 0 {
			http.Error(w, "bad attachment index", http.StatusBadRequest)
			return
		}
		att, body, err := ParseAttachment(m, n)
		if err != nil {
			http.Error(w, "no such attachment", http.StatusNotFound)
			return
		}
		// Always octet-stream, never the declared Content-Type: serving a
		// text/html attachment as text/html would be stored XSS on the
		// mailbox's own origin.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(sanitizeFilename(att.Filename)))
		w.Write(body)
	})

	api.HandleFunc("DELETE /api/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !st.Delete(r.PathValue("id")) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such message"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	api.HandleFunc("DELETE /api/messages", func(w http.ResponseWriter, r *http.Request) {
		st.Clear()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.Handle("/api/", requireToken(cfg.HTTPToken, api))

	return securityHeaders(mux)
}

// requireToken gates /api/* on a bearer token when one is configured. An empty
// token means no HTTP auth at all -- the dbadmin-compatible default.
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="devmail"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The mailbox renders attacker-controlled HTML; a framed page must not
		// be clickjackable.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already written; nothing useful is left to do.
		_ = err
	}
}

// sanitizeFilename keeps a Content-Disposition filename from carrying path
// separators, quotes or control characters out of attacker-controlled headers.
func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

// publicRoot re-roots the embedded FS at public/ so index.html is served from
// "/" rather than "/public/index.html".
func publicRoot() fs.FS {
	sub, err := fs.Sub(publicFS, "public")
	if err != nil {
		// The directory is embedded at compile time; a failure here is a
		// build-time mistake, not a runtime condition.
		panic("devmail: embedded public/ missing: " + err.Error())
	}
	return sub
}
