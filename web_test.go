package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestHandlerServer(t *testing.T, cfg *Config, st *Store, smtpReady *atomic.Bool) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewHandler(cfg, st, smtpReady, log))
	t.Cleanup(srv.Close)
	return srv
}

func doRequest(t *testing.T, method, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do(%s %s): %v", method, url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestHealthzReturns503WhileSMTPNotReadyAnd200OnceReady(t *testing.T) {
	cfg := &Config{}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/healthz", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/healthz status = %d before smtpReady, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}

	ready.Store(true)

	resp = doRequest(t, http.MethodGet, srv.URL+"/healthz", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d after smtpReady, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHealthzAndRootReachableWithoutTokenEvenWhenConfigured(t *testing.T) {
	cfg := &Config{HTTPToken: "secret-token"}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	ready.Store(true)
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/healthz", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d without a token (token IS configured), want %d -- gating this breaks k8s/Docker health probes", resp.StatusCode, http.StatusOK)
	}

	resp = doRequest(t, http.MethodGet, srv.URL+"/", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/ status = %d without a token (token IS configured), want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestAPIMessagesRequiresTokenWhenConfigured(t *testing.T) {
	cfg := &Config{HTTPToken: "secret-token"}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/api/messages", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/messages without header status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	resp = doRequest(t, http.MethodGet, srv.URL+"/api/messages", "secret-token")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/api/messages with correct token status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp = doRequest(t, http.MethodGet, srv.URL+"/api/messages", "wrong-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/messages with wrong token status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestAPIMessagesOpenWhenNoTokenConfigured(t *testing.T) {
	cfg := &Config{HTTPToken: ""}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/api/messages", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/api/messages with no configured token status = %d, want %d (dbadmin-compatible open default)", resp.StatusCode, http.StatusOK)
	}
}

func TestGetUnknownMessageIs404(t *testing.T) {
	cfg := &Config{}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/api/messages/does-not-exist", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown message status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestDeleteUnknownMessageIs404AndKnownMessageIs204(t *testing.T) {
	cfg := &Config{}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodDelete, srv.URL+"/api/messages/does-not-exist", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE unknown message status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	m := &Message{From: "sender@example.com", Raw: []byte("body")}
	st.Add(m)

	resp = doRequest(t, http.MethodDelete, srv.URL+"/api/messages/"+m.ID, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE known message status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	if _, ok := st.Get(m.ID); ok {
		t.Error("message still present in store after DELETE returned 204")
	}
}

func TestRootServesUIShellWithMessageListElement(t *testing.T) {
	cfg := &Config{}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), `id="messageList"`) {
		t.Errorf("GET / body does not contain %q; the embedded FS may not be re-rooted at public/", `id="messageList"`)
	}
}

// TestAttachmentEndpoint404sWhileParseAttachmentIsStubbed exercises the
// attachment endpoint against the current code, where ParseAttachment is
// stubbed to always return an error. It asserts today's actual behaviour
// (404) rather than the eventual one.
//
// NOTE for whoever lands the real ParseAttachment: once it works, this
// endpoint's success response MUST set
//
//	Content-Type: application/octet-stream
//	X-Content-Type-Options: nosniff
//
// and NEVER the email's declared Content-Type -- serving a text/html
// attachment as text/html would be stored XSS on the mailbox's own origin.
// Add that assertion here once ParseAttachment has a real implementation.
func TestAttachmentEndpoint404sWhileParseAttachmentIsStubbed(t *testing.T) {
	cfg := &Config{}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	m := &Message{From: "sender@example.com", Raw: []byte("Content-Type: text/plain\r\n\r\nbody")}
	st.Add(m)

	resp := doRequest(t, http.MethodGet, srv.URL+"/api/messages/"+m.ID+"/attachments/0", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("attachment endpoint status = %d, want %d (ParseAttachment is stubbed to always error)", resp.StatusCode, http.StatusNotFound)
	}
}

func TestSecurityHeadersXFrameOptionsDenyIsPresent(t *testing.T) {
	cfg := &Config{}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/", "")
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "DENY")
	}
}

// TestHealthzResponseIsValidJSON is a light smoke test that /healthz's body
// decodes, exercising the map[string]any encoding path alongside the status
// code assertions above.
func TestHealthzResponseIsValidJSON(t *testing.T) {
	cfg := &Config{}
	st := NewStore(100, 1<<20)
	var ready atomic.Bool
	ready.Store(true)
	srv := newTestHandlerServer(t, cfg, st, &ready)

	resp := doRequest(t, http.MethodGet, srv.URL+"/healthz", "")
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding /healthz body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf(`/healthz body["status"] = %v, want "ok"`, body["status"])
	}
}
