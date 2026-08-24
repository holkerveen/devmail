// Command send connects to a devmail instance over authenticated SMTPS and
// delivers one multipart/alternative fixture message.
//
// It is deliberately dual-purpose: `devmail.sh send` uses it as a smoke test
// against the dev stack, and the e2e specs shell out to it as their fixture
// generator -- for a mailtrap, the act of sending mail IS the fixture, so
// there is no separate seed step.
package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"mime"
	"net/smtp"
	"os"
	"time"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	host := env("SMTP_HOST", "127.0.0.1")
	port := env("SMTP_PORT", "465")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASSWORD")
	from := env("FROM", "sender@example.test")
	to := env("TO", "recipient@example.test")
	subject := env("SUBJECT", "devmail fixture "+time.Now().UTC().Format(time.RFC3339))

	// AUTH=none skips authentication entirely so the e2e suite can prove the
	// server refuses unauthenticated mail. Any other value authenticates
	// normally; pass a wrong SMTP_PASSWORD to exercise the rejection path.
	authMode := env("AUTH", "plain")

	if authMode != "none" && (user == "" || pass == "") {
		log.Fatal("send: SMTP_USER and SMTP_PASSWORD are required")
	}

	addr := host + ":" + port

	// The dev/test cert is self-signed (ephemeral, regenerated on every
	// restart), so verification is off. InsecureSkipVerify makes the
	// ServerName irrelevant to the TLS handshake itself, but it still
	// matters below.
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         host,
	})
	if err != nil {
		log.Fatalf("send: tls dial %s: %v", addr, err)
	}
	defer conn.Close()

	// IMPORTANT: net/smtp.PlainAuth refuses to hand over credentials unless
	// its own "host" argument matches the name passed to smtp.NewClient --
	// see PlainAuth.Start, which checks server.Name == a.host. Pass the same
	// `host` string to both smtp.NewClient and smtp.PlainAuth below, or auth
	// fails with a "wrong host name" error that has nothing to do with the
	// server or the certificate (ask me how I know).
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		log.Fatalf("send: smtp client: %v", err)
	}
	defer client.Close()

	if authMode != "none" {
		auth := smtp.PlainAuth("", user, pass, host)
		if err := client.Auth(auth); err != nil {
			log.Fatalf("send: auth: %v", err)
		}
	}

	if err := client.Mail(from); err != nil {
		log.Fatalf("send: mail from: %v", err)
	}
	if err := client.Rcpt(to); err != nil {
		log.Fatalf("send: rcpt to: %v", err)
	}

	w, err := client.Data()
	if err != nil {
		log.Fatalf("send: data: %v", err)
	}
	if _, err := w.Write(buildMessage(from, to, subject)); err != nil {
		log.Fatalf("send: write body: %v", err)
	}
	if err := w.Close(); err != nil {
		log.Fatalf("send: close data: %v", err)
	}

	if err := client.Quit(); err != nil {
		log.Fatalf("send: quit: %v", err)
	}

	fmt.Printf("sent: from=%s to=%s subject=%q via %s\n", from, to, subject, addr)
}

// buildMessage returns a raw RFC 5322 message with a multipart/alternative
// body: a text/plain part and a text/html part, the latter carrying a
// remote image so the mailbox UI's remote-content-stripping control has
// something to strip.
func buildMessage(from, to, subject string) []byte {
	const boundary = "devmail-fixture-boundary"
	encodedSubject := mime.QEncoding.Encode("UTF-8", subject)

	const plain = "This is a fixture message sent by devmail's tests/send tool.\r\n" +
		"It exercises the text/plain view of the mailbox UI.\r\n"

	const html = "<html><body>\r\n" +
		"<p>This is a fixture message sent by <strong>devmail's</strong> tests/send tool.</p>\r\n" +
		"<p>It exercises the text/html view, including a remote image that a\r\n" +
		"mailtrap must strip by default:</p>\r\n" +
		"<img src=\"https://tracker.example/pixel.gif\" alt=\"tracking pixel\">\r\n" +
		"</body></html>\r\n"

	var b []byte
	line := func(s string) { b = append(b, s...); b = append(b, '\r', '\n') }

	line("From: " + from)
	line("To: " + to)
	line("Subject: " + encodedSubject)
	line("MIME-Version: 1.0")
	line(`Content-Type: multipart/alternative; boundary="` + boundary + `"`)
	line("")
	line("--" + boundary)
	line("Content-Type: text/plain; charset=utf-8")
	line("Content-Transfer-Encoding: 8bit")
	line("")
	b = append(b, plain...)
	line("")
	line("--" + boundary)
	line("Content-Type: text/html; charset=utf-8")
	line("Content-Transfer-Encoding: 8bit")
	line("")
	b = append(b, html...)
	line("")
	line("--" + boundary + "--")

	return b
}
