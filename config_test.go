package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setRequiredCreds sets the two env vars Load() cannot proceed without, so
// tests that aren't specifically exercising the credential checks don't have
// to repeat this boilerplate.
func setRequiredCreds(t *testing.T) {
	t.Helper()
	t.Setenv("DEVMAIL_SMTP_USER", "trapuser")
	t.Setenv("DEVMAIL_SMTP_PASSWORD", "trappass")
}

func TestLoadFailsWithoutSMTPUser(t *testing.T) {
	t.Setenv("DEVMAIL_SMTP_USER", "")
	t.Setenv("DEVMAIL_SMTP_PASSWORD", "trappass")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded with no DEVMAIL_SMTP_USER; boot must fail loudly")
	}
	if !strings.Contains(err.Error(), "DEVMAIL_SMTP_USER") {
		t.Errorf("error %q does not mention DEVMAIL_SMTP_USER", err.Error())
	}
}

func TestLoadFailsWithoutSMTPPassword(t *testing.T) {
	t.Setenv("DEVMAIL_SMTP_USER", "trapuser")
	t.Setenv("DEVMAIL_SMTP_PASSWORD", "")
	t.Setenv("DEVMAIL_SMTP_PASSWORD_FILE", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded with no DEVMAIL_SMTP_PASSWORD; boot must fail loudly")
	}
	if !strings.Contains(err.Error(), "DEVMAIL_SMTP_PASSWORD") {
		t.Errorf("error %q does not mention DEVMAIL_SMTP_PASSWORD", err.Error())
	}
}

func TestSMTPPasswordFileTakesPrecedenceAndTrimsTrailingNewline(t *testing.T) {
	t.Setenv("DEVMAIL_SMTP_USER", "trapuser")
	// Plain var is set too, to prove the file wins.
	t.Setenv("DEVMAIL_SMTP_PASSWORD", "plain-password-should-be-ignored")

	dir := t.TempDir()
	path := filepath.Join(dir, "smtp_password")
	if err := os.WriteFile(path, []byte("file-password\n"), 0o600); err != nil {
		t.Fatalf("writing temp password file: %v", err)
	}
	t.Setenv("DEVMAIL_SMTP_PASSWORD_FILE", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.SMTPPassword != "file-password" {
		t.Errorf("SMTPPassword = %q, want %q (file should win and newline should be trimmed)", cfg.SMTPPassword, "file-password")
	}
}

func TestConfigStringRedactsSecrets(t *testing.T) {
	cfg := &Config{
		SMTPPort:     465,
		HTTPPort:     80,
		SMTPUser:     "trapuser",
		SMTPPassword: "super-secret-password",
		HTTPToken:    "super-secret-token",
	}
	s := cfg.String()
	if strings.Contains(s, "super-secret-password") {
		t.Errorf("Config.String() leaked the password: %s", s)
	}
	if strings.Contains(s, "super-secret-token") {
		t.Errorf("Config.String() leaked the token: %s", s)
	}
	// The username is not a secret and should still be visible for debugging.
	if !strings.Contains(s, "trapuser") {
		t.Errorf("Config.String() dropped the (non-secret) username: %s", s)
	}
}

func TestConfigGoStringRedactsSecrets(t *testing.T) {
	cfg := &Config{SMTPPassword: "super-secret-password", HTTPToken: "super-secret-token"}
	s := cfg.GoString()
	if strings.Contains(s, "super-secret-password") || strings.Contains(s, "super-secret-token") {
		t.Errorf("Config.GoString() leaked a secret: %s", s)
	}
}

func TestLoadRejectsNonNumericPort(t *testing.T) {
	setRequiredCreds(t)
	t.Setenv("DEVMAIL_SMTP_PORT", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with a non-numeric SMTP port")
	}
}

func TestLoadRejectsPortOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		port string
	}{
		{"zero", "0"},
		{"negative", "-1"},
		{"too_large", "70000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredCreds(t)
			t.Setenv("DEVMAIL_SMTP_PORT", tc.port)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() succeeded with DEVMAIL_SMTP_PORT=%s", tc.port)
			}
		})
	}
}

func TestLoadRejectsMaxMessageBytesGreaterThanMaxTotalBytes(t *testing.T) {
	setRequiredCreds(t)
	t.Setenv("DEVMAIL_MAX_TOTAL_BYTES", "1000")
	t.Setenv("DEVMAIL_MAX_MESSAGE_BYTES", "2000")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded with MaxMessageBytes > MaxTotalBytes")
	}
	if !strings.Contains(err.Error(), "MAX_MESSAGE_BYTES") {
		t.Errorf("error %q does not mention MAX_MESSAGE_BYTES", err.Error())
	}
}

func TestLoadRejectsSMTPPortEqualsHTTPPort(t *testing.T) {
	setRequiredCreds(t)
	t.Setenv("DEVMAIL_SMTP_PORT", "2525")
	t.Setenv("DEVMAIL_HTTP_PORT", "2525")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded with SMTPPort == HTTPPort")
	}
}

func TestLoadRejectsUnparseableDuration(t *testing.T) {
	setRequiredCreds(t)
	t.Setenv("DEVMAIL_READ_TIMEOUT", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with an unparseable DEVMAIL_READ_TIMEOUT")
	}
}

func TestLoadRejectsNonPositiveDuration(t *testing.T) {
	setRequiredCreds(t)
	t.Setenv("DEVMAIL_WRITE_TIMEOUT", "0s")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with a zero DEVMAIL_WRITE_TIMEOUT")
	}
}

func TestLoadDefaults(t *testing.T) {
	setRequiredCreds(t)
	// Explicitly clear every var with a default so this test is not at the
	// mercy of whatever happens to be in the ambient environment.
	for _, v := range []string{
		"DEVMAIL_SMTP_PORT", "DEVMAIL_HTTP_PORT", "DEVMAIL_SMTP_DOMAIN", "DEVMAIL_HTTP_TOKEN",
		"DEVMAIL_MAX_MESSAGES", "DEVMAIL_MAX_RECIPIENTS", "DEVMAIL_MAX_TOTAL_BYTES",
		"DEVMAIL_MAX_MESSAGE_BYTES", "DEVMAIL_MAX_LINE_BYTES", "DEVMAIL_READ_TIMEOUT",
		"DEVMAIL_WRITE_TIMEOUT", "DEVMAIL_TLS_CERT", "DEVMAIL_TLS_KEY", "DEVMAIL_TLS_HOSTS",
	} {
		t.Setenv(v, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	checkInt := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want default %d", name, got, want)
		}
	}
	checkInt("SMTPPort", cfg.SMTPPort, 465)
	checkInt("HTTPPort", cfg.HTTPPort, 80)
	checkInt("MaxMessages", cfg.MaxMessages, 100)
	checkInt("MaxRecipients", cfg.MaxRecipients, 100)
	checkInt("MaxLineBytes", cfg.MaxLineBytes, 1<<20)

	if cfg.MaxTotalBytes != 16<<20 {
		t.Errorf("MaxTotalBytes = %d, want default %d", cfg.MaxTotalBytes, 16<<20)
	}
	if cfg.MaxMessageBytes != 2<<20 {
		t.Errorf("MaxMessageBytes = %d, want default %d", cfg.MaxMessageBytes, 2<<20)
	}
	if cfg.ReadTimeout != 60_000_000_000 {
		t.Errorf("ReadTimeout = %v, want default 60s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 60_000_000_000 {
		t.Errorf("WriteTimeout = %v, want default 60s", cfg.WriteTimeout)
	}
	if cfg.SMTPDomain != "devmail" {
		t.Errorf("SMTPDomain = %q, want default %q", cfg.SMTPDomain, "devmail")
	}
	if cfg.HTTPToken != "" {
		t.Errorf("HTTPToken = %q, want empty default", cfg.HTTPToken)
	}
	wantHosts := []string{"localhost", "127.0.0.1", "::1", "devmail"}
	if len(cfg.TLSHosts) != len(wantHosts) {
		t.Fatalf("TLSHosts = %v, want %v", cfg.TLSHosts, wantHosts)
	}
	for i, h := range wantHosts {
		if cfg.TLSHosts[i] != h {
			t.Errorf("TLSHosts[%d] = %q, want %q", i, cfg.TLSHosts[i], h)
		}
	}
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		t.Errorf("TLSCertFile/TLSKeyFile should default empty, got %q/%q", cfg.TLSCertFile, cfg.TLSKeyFile)
	}
}
