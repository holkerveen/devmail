package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// newTestSMTPConfig returns a Config suitable for standing up a real SMTPS
// listener in-process. MaxLineBytes is set explicitly and large (not left at
// go-smtp's 2000-byte default) so tests can prove the code's own override
// took effect.
func newTestSMTPConfig() *Config {
	return &Config{
		SMTPUser:        "trapuser",
		SMTPPassword:    "trappass",
		SMTPDomain:      "devmail-test",
		MaxRecipients:   100,
		MaxMessageBytes: 2 << 20,
		MaxLineBytes:    1 << 20,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		TLSHosts:        []string{"127.0.0.1", "localhost"},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startTestSMTPServer starts NewSMTPServer's server on an ephemeral
// 127.0.0.1 port over TLS (the code's own TLSConfig) and returns the dial
// address, the bare host (for smtp.PlainAuth, which refuses to send
// credentials unless its host argument matches what was passed to
// smtp.NewClient), and the backing Store.
func startTestSMTPServer(t *testing.T, cfg *Config) (addr, host string, store *Store) {
	t.Helper()

	log := testLogger()
	tlsCfg, err := TLSConfig(cfg, log)
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}

	store = NewStore(1000, 64<<20)
	srv := NewSMTPServer(cfg, store, tlsCfg, log)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}

	go func() {
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
	})

	addr = ln.Addr().String()
	host, _, err = net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("net.SplitHostPort(%q): %v", addr, err)
	}
	return addr, host, store
}

// dialTestClient connects over TLS (skipping verification -- the server uses
// an ephemeral self-signed cert) and returns a *smtp.Client built with the
// SAME host string used for dialing, per net/smtp's PlainAuth requirement.
func dialTestClient(t *testing.T, addr, host string) *smtp.Client {
	t.Helper()

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		t.Fatalf("smtp.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
	})
	return c
}

func authenticateTestClient(t *testing.T, c *smtp.Client, host string, cfg *Config) {
	t.Helper()
	auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, host)
	if err := c.Auth(auth); err != nil {
		t.Fatalf("Auth() with correct credentials failed: %v", err)
	}
}

func sendTestMessage(t *testing.T, c *smtp.Client, from string, to []string, raw string) {
	t.Helper()
	if err := c.Mail(from); err != nil {
		t.Fatalf("Mail(%q): %v", from, err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			t.Fatalf("Rcpt(%q): %v", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		t.Fatalf("Data(): %v", err)
	}
	if _, err := w.Write([]byte(raw)); err != nil {
		t.Fatalf("writing DATA: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing DATA: %v", err)
	}
}

// TestMailFromWithoutAuthIsRefused proves the backend's own gate works.
// go-smtp does NOT enforce auth itself -- its handleMail checks only whether
// HELO was sent -- so a passing test here depends entirely on session.Mail's
// own authed check.
func TestMailFromWithoutAuthIsRefused(t *testing.T) {
	cfg := newTestSMTPConfig()
	addr, host, store := startTestSMTPServer(t, cfg)
	c := dialTestClient(t, addr, host)

	if err := c.Mail("sender@example.com"); err == nil {
		t.Fatal("MAIL FROM succeeded without AUTH; want it refused")
	}

	count, _, _ := store.Stats()
	if count != 0 {
		t.Errorf("store has %d messages after an unauthenticated MAIL FROM, want 0", count)
	}
}

// TestWrongPasswordReturns535PermanentFailure asserts the override from
// go-smtp's default 454 (temporary, endlessly retried by mail libraries) to
// 535 (permanent) actually reaches the wire, and that the message is the
// fixed string -- never anything containing the attempted username.
func TestWrongPasswordReturns535PermanentFailure(t *testing.T) {
	cfg := newTestSMTPConfig()
	addr, host, store := startTestSMTPServer(t, cfg)
	c := dialTestClient(t, addr, host)

	const distinctiveUsername = "distinctive-probe-username"
	auth := smtp.PlainAuth("", distinctiveUsername, "wrong-password", host)
	err := c.Auth(auth)
	if err == nil {
		t.Fatal("Auth() with a wrong password succeeded, want an error")
	}

	if !strings.Contains(err.Error(), "535") {
		t.Errorf("error = %q, want the permanent code 535, not go-smtp's temporary 454 default", err.Error())
	}
	if strings.Contains(err.Error(), distinctiveUsername) {
		t.Errorf("error %q echoes the attempted username; the wire message must be the fixed string only", err.Error())
	}
	if !strings.Contains(err.Error(), "Authentication credentials invalid") {
		t.Errorf("error = %q, want it to contain the fixed message %q", err.Error(), "Authentication credentials invalid")
	}

	count, _, _ := store.Stats()
	if count != 0 {
		t.Errorf("store has %d messages after a failed auth, want 0", count)
	}
}

// TestTwoMessagesOnOneConnectionBothStoredAndEnvelopesDoNotLeak is the
// highest-value test in this file. go-smtp calls Session.Reset() after every
// message while NewSession runs once per connection: clearing "authed" in
// Reset would silently break every pooled sender (Nodemailer pool:true,
// Symfony, Laravel queue workers) after their first message, and failing to
// clear the envelope would leak message N-1's recipients into message N.
func TestTwoMessagesOnOneConnectionBothStoredAndEnvelopesDoNotLeak(t *testing.T) {
	cfg := newTestSMTPConfig()
	addr, host, store := startTestSMTPServer(t, cfg)
	c := dialTestClient(t, addr, host)
	authenticateTestClient(t, c, host, cfg)

	msg := func(from, to, subject string) string {
		return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\nbody\r\n", from, to, subject)
	}

	sendTestMessage(t, c, "alice@example.com", []string{"rcpt1@example.com"},
		msg("alice@example.com", "rcpt1@example.com", "first"))
	sendTestMessage(t, c, "bob@example.com", []string{"rcpt2@example.com"},
		msg("bob@example.com", "rcpt2@example.com", "second"))

	list := store.List() // newest first
	if len(list) != 2 {
		t.Fatalf("store has %d messages, want 2 (second message on a pooled connection must not be silently dropped)", len(list))
	}

	second, first := list[0], list[1]

	if first.From != "alice@example.com" {
		t.Errorf("first message From = %q, want %q", first.From, "alice@example.com")
	}
	if second.From != "bob@example.com" {
		t.Errorf("second message From = %q, want %q", second.From, "bob@example.com")
	}

	if len(first.To) != 1 || first.To[0] != "rcpt1@example.com" {
		t.Errorf("first message To = %v, want [rcpt1@example.com]", first.To)
	}
	if len(second.To) != 1 || second.To[0] != "rcpt2@example.com" {
		t.Errorf("second message To = %v, want exactly [rcpt2@example.com] -- any trace of rcpt1 means the envelope leaked across messages", second.To)
	}
}

// TestDataLineLongerThan2000BytesIsAccepted proves MaxLineBytes stayed set.
// go-smtp's NewServer defaults MaxLineLength to 2000 and that limit applies
// to DATA body bytes, handing the backend zero bytes when tripped.
func TestDataLineLongerThan2000BytesIsAccepted(t *testing.T) {
	cfg := newTestSMTPConfig() // MaxLineBytes = 1<<20, well above 2000.
	addr, host, store := startTestSMTPServer(t, cfg)
	c := dialTestClient(t, addr, host)
	authenticateTestClient(t, c, host, cfg)

	longLine := strings.Repeat("A", 5000)
	raw := "From: sender@example.com\r\nTo: rcpt@example.com\r\nSubject: long line\r\n\r\n" + longLine + "\r\n"

	sendTestMessage(t, c, "sender@example.com", []string{"rcpt@example.com"}, raw)

	list := store.List()
	if len(list) != 1 {
		t.Fatalf("store has %d messages, want 1", len(list))
	}
	if list[0].Size != len(raw) {
		t.Errorf("stored Size = %d, want %d (the full unwrapped line, not truncated by the 2000-byte default)", list[0].Size, len(raw))
	}
}

// TestMessageExceedingMaxMessageBytesIsRejectedAndNotStored proves an
// oversized message never gets a truncated prefix stored and never gets
// ACKed 250.
func TestMessageExceedingMaxMessageBytesIsRejectedAndNotStored(t *testing.T) {
	cfg := newTestSMTPConfig()
	cfg.MaxMessageBytes = 100
	addr, host, store := startTestSMTPServer(t, cfg)
	c := dialTestClient(t, addr, host)
	authenticateTestClient(t, c, host, cfg)

	body := strings.Repeat("B", 1000)
	raw := "From: sender@example.com\r\nTo: rcpt@example.com\r\nSubject: too big\r\n\r\n" + body + "\r\n"

	if err := c.Mail("sender@example.com"); err != nil {
		t.Fatalf("Mail(): %v", err)
	}
	if err := c.Rcpt("rcpt@example.com"); err != nil {
		t.Fatalf("Rcpt(): %v", err)
	}
	w, err := c.Data()
	if err != nil {
		t.Fatalf("Data(): %v", err)
	}
	_, _ = w.Write([]byte(raw))
	if err := w.Close(); err == nil {
		t.Fatal("Close() succeeded (250 ACK) for a message over MaxMessageBytes, want it rejected")
	}

	count, _, _ := store.Stats()
	if count != 0 {
		t.Errorf("store has %d messages after an oversized message was rejected, want 0 (no truncated prefix ever stored)", count)
	}
}

// TestRcptAcceptsAnyRecipientDomain proves the trap swallows everything.
func TestRcptAcceptsAnyRecipientDomain(t *testing.T) {
	cfg := newTestSMTPConfig()
	addr, host, _ := startTestSMTPServer(t, cfg)
	c := dialTestClient(t, addr, host)
	authenticateTestClient(t, c, host, cfg)

	if err := c.Mail("sender@example.com"); err != nil {
		t.Fatalf("Mail(): %v", err)
	}
	for _, rcpt := range []string{
		"a@example.com",
		"b@totally-different-domain.test",
		"c@yet-another.example",
	} {
		if err := c.Rcpt(rcpt); err != nil {
			t.Errorf("Rcpt(%q) = %v, want accepted (the trap accepts any recipient domain)", rcpt, err)
		}
	}
}

// TestConnectionDroppedAfterThreeFailedAuthAttempts guards maxAuthAttempts.
// go-smtp routes auth failures through writeError rather than
// protocolError, so its own errCount never increments; without our own
// enforcement a client gets unlimited password guesses on one connection.
//
// This is deliberately NOT built on net/smtp's Client.Auth: its doc comment
// says plainly "A failed authentication closes the connection" -- on ANY
// auth error it calls c.Quit() and gives up itself, which would make every
// attempt after the first run against a connection the TEST closed, not one
// the SERVER closed, and the assertion would pass for the wrong reason. This
// drives the wire protocol directly with net/textproto so the three AUTH
// attempts genuinely share one connection that only the server can drop.
func TestConnectionDroppedAfterThreeFailedAuthAttempts(t *testing.T) {
	cfg := newTestSMTPConfig()
	addr, host, _ := startTestSMTPServer(t, cfg)

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	tp := textproto.NewConn(conn)
	if _, _, err := tp.ReadResponse(220); err != nil {
		t.Fatalf("reading greeting: %v", err)
	}
	if err := tp.PrintfLine("EHLO %s", host); err != nil {
		t.Fatalf("sending EHLO: %v", err)
	}
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("reading EHLO response: %v", err)
	}

	for i := 0; i < maxAuthAttempts; i++ {
		ir := "\x00" + cfg.SMTPUser + "\x00wrong-password"
		b64 := base64.StdEncoding.EncodeToString([]byte(ir))
		if err := tp.PrintfLine("AUTH PLAIN %s", b64); err != nil {
			t.Fatalf("attempt %d: sending AUTH PLAIN: %v", i+1, err)
		}
		code, msg, err := tp.ReadResponse(0)
		if err != nil {
			t.Fatalf("attempt %d: reading AUTH response: %v", i+1, err)
		}
		if code/100 == 2 {
			t.Fatalf("attempt %d: AUTH with a wrong password succeeded (%d %s)", i+1, code, msg)
		}
	}

	// The connection must now be dropped. NOOP needs no auth and always gets
	// a 250 from a live connection, so it only fails to do so if the
	// connection was actually closed by the server.
	if err := tp.PrintfLine("NOOP"); err == nil {
		if code, msg, err := tp.ReadResponse(0); err == nil {
			t.Errorf("connection accepted NOOP (%d %s) after 3 failed AUTH attempts; want the connection dropped", code, msg)
		}
	}
}
