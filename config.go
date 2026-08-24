package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the whole runtime configuration, read from the environment once at
// boot. Every field has a default except the SMTP credentials: a mailtrap that
// silently accepts anonymous mail because an env var was misspelled is the
// exact failure this project exists to prevent.
type Config struct {
	SMTPPort     int
	HTTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPDomain   string

	// HTTPToken gates /api/*. Empty means no HTTP authentication at all,
	// which is the dbadmin-compatible default. "/" and "/healthz" are never
	// gated: the shell carries no data, and gating /healthz breaks the
	// Kubernetes probe and the Docker HEALTHCHECK.
	HTTPToken string

	MaxMessages     int
	MaxRecipients   int
	MaxTotalBytes   int64
	MaxMessageBytes int64
	// MaxLineBytes must be set explicitly. go-smtp's NewServer defaults it to
	// 2000, and that limit wraps the raw connection, so it applies to DATA
	// body bytes too -- an unwrapped base64 attachment line trips it and the
	// backend's Data() receives ZERO bytes, losing the message entirely.
	MaxLineBytes int

	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	TLSCertFile string
	TLSKeyFile  string
	TLSHosts    []string
}

// String redacts the secrets so that no future "%+v" or "%v" of a Config can
// leak the password into a log aggregator.
func (c *Config) String() string {
	return fmt.Sprintf("Config{SMTPPort:%d HTTPPort:%d SMTPUser:%q SMTPPassword:%s HTTPToken:%s "+
		"MaxMessages:%d MaxTotalBytes:%d MaxMessageBytes:%d MaxLineBytes:%d TLSHosts:%v}",
		c.SMTPPort, c.HTTPPort, c.SMTPUser, redacted(c.SMTPPassword), redacted(c.HTTPToken),
		c.MaxMessages, c.MaxTotalBytes, c.MaxMessageBytes, c.MaxLineBytes, c.TLSHosts)
}

// GoString makes %#v redact too.
func (c *Config) GoString() string { return c.String() }

func redacted(s string) string {
	if s == "" {
		return "(unset)"
	}
	return "(set)"
}

// Load reads and validates the environment. It never panics.
func Load() (*Config, error) {
	c := &Config{}
	var err error

	if c.SMTPPort, err = envInt("DEVMAIL_SMTP_PORT", 465); err != nil {
		return nil, err
	}
	if c.HTTPPort, err = envInt("DEVMAIL_HTTP_PORT", 80); err != nil {
		return nil, err
	}

	c.SMTPUser = os.Getenv("DEVMAIL_SMTP_USER")
	if c.SMTPPassword, err = envSecret("DEVMAIL_SMTP_PASSWORD", "DEVMAIL_SMTP_PASSWORD_FILE"); err != nil {
		return nil, err
	}
	c.SMTPDomain = envStr("DEVMAIL_SMTP_DOMAIN", "devmail")
	c.HTTPToken = os.Getenv("DEVMAIL_HTTP_TOKEN")

	if c.MaxMessages, err = envInt("DEVMAIL_MAX_MESSAGES", 100); err != nil {
		return nil, err
	}
	if c.MaxRecipients, err = envInt("DEVMAIL_MAX_RECIPIENTS", 100); err != nil {
		return nil, err
	}
	if c.MaxTotalBytes, err = envInt64("DEVMAIL_MAX_TOTAL_BYTES", 16<<20); err != nil {
		return nil, err
	}
	if c.MaxMessageBytes, err = envInt64("DEVMAIL_MAX_MESSAGE_BYTES", 2<<20); err != nil {
		return nil, err
	}
	if c.MaxLineBytes, err = envInt("DEVMAIL_MAX_LINE_BYTES", 1<<20); err != nil {
		return nil, err
	}
	if c.ReadTimeout, err = envDuration("DEVMAIL_READ_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if c.WriteTimeout, err = envDuration("DEVMAIL_WRITE_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}

	c.TLSCertFile = os.Getenv("DEVMAIL_TLS_CERT")
	c.TLSKeyFile = os.Getenv("DEVMAIL_TLS_KEY")
	c.TLSHosts = splitHosts(envStr("DEVMAIL_TLS_HOSTS", "localhost,127.0.0.1,::1,devmail"))

	return c, c.validate()
}

func (c *Config) validate() error {
	if c.SMTPUser == "" {
		return fmt.Errorf("DEVMAIL_SMTP_USER is required")
	}
	if c.SMTPPassword == "" {
		return fmt.Errorf("DEVMAIL_SMTP_PASSWORD (or DEVMAIL_SMTP_PASSWORD_FILE) is required")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("DEVMAIL_TLS_CERT and DEVMAIL_TLS_KEY must be set together")
	}
	for _, p := range []struct {
		name string
		v    int
	}{
		{"DEVMAIL_SMTP_PORT", c.SMTPPort},
		{"DEVMAIL_HTTP_PORT", c.HTTPPort},
	} {
		if p.v < 1 || p.v > 65535 {
			return fmt.Errorf("%s must be 1-65535, got %d", p.name, p.v)
		}
	}
	if c.SMTPPort == c.HTTPPort {
		return fmt.Errorf("DEVMAIL_SMTP_PORT and DEVMAIL_HTTP_PORT must differ (both %d)", c.SMTPPort)
	}
	if c.MaxMessages < 1 {
		return fmt.Errorf("DEVMAIL_MAX_MESSAGES must be >= 1, got %d", c.MaxMessages)
	}
	if c.MaxMessageBytes < 1 {
		return fmt.Errorf("DEVMAIL_MAX_MESSAGE_BYTES must be >= 1, got %d", c.MaxMessageBytes)
	}
	// A per-message cap above the whole-ring budget means one message evicts
	// the entire mailbox and still may not fit.
	if c.MaxMessageBytes > c.MaxTotalBytes {
		return fmt.Errorf("DEVMAIL_MAX_MESSAGE_BYTES (%d) must not exceed DEVMAIL_MAX_TOTAL_BYTES (%d)",
			c.MaxMessageBytes, c.MaxTotalBytes)
	}
	if c.MaxLineBytes < 1000 {
		return fmt.Errorf("DEVMAIL_MAX_LINE_BYTES must be >= 1000, got %d", c.MaxLineBytes)
	}
	if len(c.TLSHosts) == 0 {
		return fmt.Errorf("DEVMAIL_TLS_HOSTS must not be empty")
	}
	return nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envSecret prefers the _FILE variant so a Kubernetes Secret can be mounted
// rather than exposed in the pod spec, where "kubectl describe pod" prints it.
func envSecret(key, fileKey string) (string, error) {
	if path := os.Getenv(fileKey); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", fileKey, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return os.Getenv(key), nil
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

func envInt64(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration (try 60s)", key, v)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", key, d)
	}
	return d, nil
}

func splitHosts(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}
