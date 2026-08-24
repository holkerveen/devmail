package main

import (
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// maxAuthAttempts drops a connection after this many failures. go-smtp routes
// auth failures through writeError rather than protocolError, so its own
// errCount never increments and a client would otherwise get unlimited guesses
// on a single connection.
const maxAuthAttempts = 3

// errAuthFailed is the ONLY thing an unauthenticated client is ever told.
//
// go-smtp's default for a SASL error is 454, a TEMPORARY failure, which every
// mail library reads as "retry later" -- so a typo'd password produces an
// infinite reconnect loop instead of a loud permanent failure. 535 is
// permanent. The message is fixed: writeError sends err.Error() verbatim over
// the wire, so a detailed error here would become a user-enumeration oracle.
var errAuthFailed = &smtp.SMTPError{
	Code:         535,
	EnhancedCode: smtp.EnhancedCode{5, 7, 8},
	Message:      "Authentication credentials invalid",
}

// NewSMTPServer builds the SMTPS listener. It accepts mail for any recipient
// and relays nothing: there is no outbound SMTP client anywhere in this binary.
func NewSMTPServer(cfg *Config, st *Store, tlsCfg *tls.Config, log *slog.Logger) *smtp.Server {
	s := smtp.NewServer(&backend{cfg: cfg, store: st, log: log})
	s.Addr = ":" + strconv.Itoa(cfg.SMTPPort)
	s.Domain = cfg.SMTPDomain
	s.TLSConfig = tlsCfg
	s.MaxRecipients = cfg.MaxRecipients
	s.MaxMessageBytes = cfg.MaxMessageBytes
	// Explicit, and the single most important line in this file: NewServer
	// defaults MaxLineLength to 2000, and that limit wraps the raw connection,
	// so it applies to DATA body bytes. An unwrapped base64 attachment line --
	// what PHP's base64_encode without chunk_split emits -- trips it, Data()
	// receives ZERO bytes, and the message is lost with no log line at all
	// (handleConn returns nil for ErrTooLongLine, so ErrorLog never fires).
	s.MaxLineLength = cfg.MaxLineBytes
	s.ReadTimeout = cfg.ReadTimeout
	s.WriteTimeout = cfg.WriteTimeout
	// Auth over cleartext is never permitted. With an implicit-TLS listener
	// the connection is always TLS, so this only matters as a guard against a
	// future plaintext listener being added by mistake.
	s.AllowInsecureAuth = false
	s.ErrorLog = slogAdapter{log}
	return s
}

type backend struct {
	cfg   *Config
	store *Store
	log   *slog.Logger
}

// NewSession is called ONCE PER CONNECTION, not once per message.
func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	remote := ""
	if addr := c.Conn().RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	// The Conn is retained solely so check() can hang up on a client that
	// keeps guessing: go-smtp offers no other way to terminate a connection
	// from inside a SASL callback.
	return &session{be: b, conn: c, remote: remote}, nil
}

type session struct {
	be     *backend
	conn   *smtp.Conn
	remote string

	// authed persists for the lifetime of the CONNECTION. See Reset.
	authed       bool
	authAttempts int

	// locked is set once the attempt limit is hit. It closes the window
	// between scheduling the hang-up and the socket actually closing, during
	// which a fast client could otherwise still be guessing.
	locked bool

	// Per-message state, cleared by Reset.
	from string
	to   []string
}

func (s *session) AuthMechanisms() []string {
	return []string{sasl.Plain, sasl.Login}
}

func (s *session) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(identity, username, password string) error {
			return s.check(username, password)
		}), nil
	case sasl.Login:
		return NewLoginServer(s.check), nil
	default:
		return nil, smtp.ErrAuthUnknownMechanism
	}
}

// check compares in constant time and returns only errAuthFailed, never a
// reason. The reason goes to the log.
func (s *session) check(username, password string) error {
	if s.locked {
		return errAuthFailed
	}
	s.authAttempts++
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.be.cfg.SMTPUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(s.be.cfg.SMTPPassword)) == 1
	if userOK && passOK {
		s.authed = true
		s.be.log.Info("smtp: authenticated", "remote", s.remote, "user", username)
		return nil
	}
	s.be.log.Warn("smtp: authentication failed",
		"remote", s.remote, "user", username, "attempt", s.authAttempts)
	if s.authAttempts >= maxAuthAttempts {
		// Returning an error is not enough to stop a guesser: go-smtp routes
		// auth failures through writeError rather than protocolError, so its
		// own errCount never increments and the client keeps its connection.
		// We have to hang up ourselves.
		//
		// The final rejection is written straight to the socket before the
		// close, because go-smtp writes its response only AFTER this callback
		// returns -- closing here first would truncate our own rejection into
		// a bare EOF, which tells whoever is on the other end nothing. Writing
		// directly is safe at exactly this point: the previous response was
		// already flushed and go-smtp has nothing buffered until we return.
		// Its subsequent write to the closed socket fails harmlessly.
		s.locked = true
		s.be.log.Warn("smtp: dropping connection after repeated auth failures",
			"remote", s.remote, "attempts", s.authAttempts)
		if c := s.conn; c != nil {
			if raw := c.Conn(); raw != nil {
				_, _ = io.WriteString(raw, "535 5.7.8 Too many authentication failures\r\n")
			}
			_ = c.Close()
		}
		return errAuthFailed
	}
	return errAuthFailed
}

// Mail, Rcpt and Data each gate on authed. go-smtp does NOT do this: its
// handleMail checks only whether HELO was sent, so without these guards the
// trap would accept anonymous mail from anyone who can reach the port.
func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authed {
		return smtp.ErrAuthRequired
	}
	s.from = from
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !s.authed {
		return smtp.ErrAuthRequired
	}
	// Every recipient is accepted regardless of domain -- swallowing all mail
	// is the entire point of a trap.
	s.to = append(s.to, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	if !s.authed {
		return smtp.ErrAuthRequired
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		// A READ error is a rejection, not a stored message. ErrDataTooLarge
		// hands us a complete-looking truncated prefix alongside the error;
		// storing that would show a silently truncated message and, if we
		// returned nil, ACK it 250. Parse errors are different and DO get
		// stored -- see parse.go.
		s.be.log.Warn("smtp: message rejected",
			"remote", s.remote, "from", s.from, "rcpt", len(s.to),
			"bytes", len(raw), "err", err)
		return err
	}

	m := &Message{
		ReceivedAt: time.Now().UTC(),
		From:       s.from,
		To:         append([]string(nil), s.to...),
		Subject:    scanSubject(raw),
		Raw:        raw,
	}
	s.be.store.Add(m)

	count, bytes, evicted := s.be.store.Stats()
	s.be.log.Info("smtp: message accepted",
		"remote", s.remote, "id", m.ID, "from", m.From, "rcpt", len(m.To),
		"bytes", m.Size, "subject", m.Subject,
		"stored", count, "storedBytes", bytes, "evictedTotal", evicted)
	return nil
}

// Reset clears per-message state and DELIBERATELY PRESERVES authed.
//
// go-smtp calls this after every message (a deferred c.reset() in handleData),
// on RSET, and on a repeated EHLO -- while NewSession runs only once per
// connection. Clearing authed here would reject the second and every later
// message on a pooled connection, which is how Nodemailer pool:true, Symfony
// and Laravel queue workers all send: the first mail arrives and the rest
// silently vanish. Conversely, failing to clear the envelope would make
// message N display message N-1's recipients, because go-smtp resets its own
// recipient list but not ours.
func (s *session) Reset() {
	s.from = ""
	s.to = nil
}

func (s *session) Logout() error { return nil }

// scanSubject reads only the header block. The list needs a subject, and a
// header-only scan is what lets the rest of the message stay unparsed until
// somebody actually opens it.
func scanSubject(raw []byte) string {
	// A message with no header/body separator still has its headers read.
	hdr, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	msg, err := mail.ReadMessage(bytes.NewReader(append(hdr, '\r', '\n', '\r', '\n')))
	if err != nil {
		return ""
	}
	dec := new(mime.WordDecoder)
	dec.CharsetReader = charset.Reader
	subject, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		return msg.Header.Get("Subject")
	}
	return subject
}

// slogAdapter lets go-smtp's Printf-style logger write into slog.
type slogAdapter struct{ log *slog.Logger }

func (s slogAdapter) Printf(format string, v ...interface{}) {
	s.log.Warn("smtp: " + strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func (s slogAdapter) Println(v ...interface{}) {
	s.log.Warn("smtp: " + strings.TrimSpace(fmt.Sprintln(v...)))
}
