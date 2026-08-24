package main

import (
	"errors"

	"github.com/emersion/go-sasl"
)

// loginServer implements the obsolete-but-ubiquitous AUTH LOGIN mechanism.
// go-sasl ships NewLoginClient but no server counterpart, and LOGIN is what
// Symfony Mailer, PHPMailer and several Nodemailer configurations pick.
//
// It is deliberately DUAL-ENTRY. go-sasl's own client sends the username as
// the SASL initial response ("AUTH LOGIN <base64 user>"), so Next is first
// called with a non-nil response; a bare "AUTH LOGIN" instead calls Next with
// nil and expects a "Username:" challenge first. A server that only implements
// the second form fails against the first with "sasl: unexpected server
// challenge", which is exactly what devmail's own Go tests would hit.
type loginServer struct {
	auth     func(username, password string) error
	username string
	// state advances 0 -> awaiting username, 1 -> awaiting password, 2 -> done.
	state int
}

// The challenge strings are load-bearing: go-sasl's loginClient compares the
// password challenge byte-for-byte against "Password:" and aborts otherwise.
var (
	challengeUsername = []byte("Username:")
	challengePassword = []byte("Password:")
)

var errUnexpected = errors.New("sasl: unexpected client response")

// NewLoginServer returns a sasl.Server for AUTH LOGIN. auth reports whether the
// credentials are valid; its error is for the server's own log and must never
// reach the wire.
func NewLoginServer(auth func(username, password string) error) sasl.Server {
	return &loginServer{auth: auth}
}

func (l *loginServer) Next(response []byte) (challenge []byte, done bool, err error) {
	switch l.state {
	case 0:
		// No initial response: prompt for the username and stay in state 0,
		// because the next call carries it.
		if response == nil {
			l.state = 1
			return challengeUsername, false, nil
		}
		// Initial response present: it IS the username.
		l.username = string(response)
		l.state = 2
		return challengePassword, false, nil
	case 1:
		l.username = string(response)
		l.state = 2
		return challengePassword, false, nil
	case 2:
		if response == nil {
			return nil, false, errUnexpected
		}
		l.state = 3
		if err := l.auth(l.username, string(response)); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	default:
		return nil, false, errUnexpected
	}
}
