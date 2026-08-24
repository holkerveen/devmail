package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/emersion/go-sasl"
)

// authFunc builds a check function for NewLoginServer that accepts exactly
// one username/password pair.
func authFunc(wantUser, wantPass string) func(username, password string) error {
	return func(username, password string) error {
		if username == wantUser && password == wantPass {
			return nil
		}
		return errors.New("invalid credentials")
	}
}

// TestLoginServerDualEntryWithInitialResponse drives the server the way
// go-sasl's own NewLoginClient does: Start() returns the username as a
// non-nil initial response, so the SERVER'S FIRST Next call receives that
// response directly rather than nil.
func TestLoginServerDualEntryWithInitialResponse(t *testing.T) {
	srv := NewLoginServer(authFunc("alice", "hunter2"))

	challenge, done, err := srv.Next([]byte("alice"))
	if err != nil {
		t.Fatalf("Next(username) returned error: %v", err)
	}
	if done {
		t.Fatal("Next(username) reported done, want false")
	}
	if !bytes.Equal(challenge, []byte("Password:")) {
		t.Fatalf("Next(username) challenge = %q, want exactly %q", challenge, "Password:")
	}

	challenge, done, err = srv.Next([]byte("hunter2"))
	if err != nil {
		t.Fatalf("Next(password) returned error: %v", err)
	}
	if !done {
		t.Fatal("Next(password) reported done=false, want true")
	}
	if challenge != nil {
		t.Errorf("Next(password) challenge = %q, want nil", challenge)
	}
}

// TestLoginServerDualEntryWithBareAuthLogin drives the server the way a bare
// "AUTH LOGIN" does: the server's first Next call is called with a NIL
// response, and the server must challenge for the username first.
func TestLoginServerDualEntryWithBareAuthLogin(t *testing.T) {
	srv := NewLoginServer(authFunc("alice", "hunter2"))

	challenge, done, err := srv.Next(nil)
	if err != nil {
		t.Fatalf("Next(nil) returned error: %v", err)
	}
	if done {
		t.Fatal("Next(nil) reported done, want false")
	}
	if !bytes.Equal(challenge, []byte("Username:")) {
		t.Fatalf("Next(nil) challenge = %q, want exactly %q", challenge, "Username:")
	}

	challenge, done, err = srv.Next([]byte("alice"))
	if err != nil {
		t.Fatalf("Next(username) returned error: %v", err)
	}
	if done {
		t.Fatal("Next(username) reported done, want false")
	}
	if !bytes.Equal(challenge, []byte("Password:")) {
		t.Fatalf("Next(username) challenge = %q, want exactly %q", challenge, "Password:")
	}

	challenge, done, err = srv.Next([]byte("hunter2"))
	if err != nil {
		t.Fatalf("Next(password) returned error: %v", err)
	}
	if !done {
		t.Fatal("Next(password) reported done=false, want true")
	}
	_ = challenge
}

// TestLoginServerInteropWithGoSASLClient drives the real go-sasl LOGIN client
// against our server implementation, stepping both sides until done. This is
// the assertion that actually proves interoperability: a server that only
// implements the bare-AUTH-LOGIN form fails here with
// "sasl: unexpected server challenge".
func TestLoginServerInteropWithGoSASLClient(t *testing.T) {
	client := sasl.NewLoginClient("alice", "hunter2")
	server := NewLoginServer(authFunc("alice", "hunter2"))

	_, ir, err := client.Start()
	if err != nil {
		t.Fatalf("client.Start() returned error: %v", err)
	}

	// Feed the client's initial response into the server, then keep stepping
	// challenge -> response until the server reports done.
	response := ir
	for i := 0; ; i++ {
		if i > 10 {
			t.Fatal("too many round-trips; something is not converging")
		}
		challenge, done, err := server.Next(response)
		if err != nil {
			t.Fatalf("server.Next() returned error: %v", err)
		}
		if done {
			break
		}
		response, err = client.Next(challenge)
		if err != nil {
			t.Fatalf("client.Next(%q) returned error: %v", challenge, err)
		}
	}
}

func TestLoginServerWrongCredentialsFails(t *testing.T) {
	srv := NewLoginServer(authFunc("alice", "hunter2"))

	if _, _, err := srv.Next([]byte("alice")); err != nil {
		t.Fatalf("Next(username) returned error: %v", err)
	}

	_, done, err := srv.Next([]byte("wrong-password"))
	if err == nil {
		t.Fatal("Next(wrong password) returned nil error, want an error")
	}
	if done {
		t.Error("Next(wrong password) reported done=true, want false")
	}
}

func TestLoginServerCorrectCredentialsSucceeds(t *testing.T) {
	srv := NewLoginServer(authFunc("alice", "hunter2"))

	if _, _, err := srv.Next([]byte("alice")); err != nil {
		t.Fatalf("Next(username) returned error: %v", err)
	}

	_, done, err := srv.Next([]byte("hunter2"))
	if err != nil {
		t.Fatalf("Next(correct password) returned error: %v", err)
	}
	if !done {
		t.Error("Next(correct password) reported done=false, want true")
	}
}
