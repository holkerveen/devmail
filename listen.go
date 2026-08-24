package main

import (
	"crypto/tls"
	"net"
	"strconv"
)

// tlsListen opens the implicit-TLS (SMTPS) listener.
//
// This is the "other one" the brief asked for: TLS is negotiated at connect
// time rather than upgraded into via STARTTLS. It is not cryptographically
// stronger than a correct STARTTLS -- it is stronger because there is no
// cleartext phase for an attacker to strip. go-smtp only advertises STARTTLS
// when the connection is not already TLS, so serving here makes the downgrade
// path unreachable rather than merely discouraged.
//
// The listener is created here rather than via smtp.Server.ListenAndServeTLS
// so that main can flip smtpReady only after the bind actually succeeds.
func tlsListen(cfg *Config, tlsCfg *tls.Config) (net.Listener, error) {
	return tls.Listen("tcp", ":"+strconv.Itoa(cfg.SMTPPort), tlsCfg)
}
