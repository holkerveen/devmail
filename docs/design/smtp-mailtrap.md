Status: Complete — implemented, unit tests and e2e green on both the dev and prod stacks, CI wired.

## Brief

Implement a barebones mailtrap container that intercepts all mail sent through an SMTP server

Checkout github.com/holkerveen/dbadmin. That is my in-k8s-namespace sql admin page. It prevents me having to forward database ports and stuff whenever I need to check or change something. It runs alongside the dev setup

NOw, I want an smtp server that does not send mail! Instead I want it to display all received mail in a mailbox like setup, can be quite barebones just like dbadmin. Make sure the whole thing is just as lightweight. Better yet, make it even lighter weight by foregoing the node server an\d use go for the backend. And an alpine image, so that the image should be no more than 25 mb in production mode.

Do make sure the smtp server requires some username, password, and secure connection. (not starttls but that other one, that is more secure right?). Cause I want to be sure things work _with_ authentication. The username, password, listen port and stuff should all be configurable by env settings so our consuming app can set these.

Take your cues from the dbadmin project.

## Project conventions

This repo is **empty** (`/home/henk/devmail`, freshly `git init`ed). There is no existing code, no e2e infrastructure, no CI. Conventions below are therefore *adopted from dbadmin* (cloned to scratch and read in full), not detected here.

### Inherited from dbadmin (github.com/holkerveen/dbadmin @ 0.2.0)

- **Entrypoint script owns every workflow.** `./dbadmin.sh <cmd>` with `setup|dev|seed|reset|down|test:dev|test:prod|test|lint|typecheck`. Never call the underlying runner directly — `npm run test:e2e` fails without `PLAYWRIGHT_BASE_URL`, which only the script sets. devmail gets `./devmail.sh` with the same command vocabulary.
- **Three compose stacks**, distinct project names and non-colliding host ports so they can run simultaneously: `docker-compose.yml` (dev, source-mounted, reads `.env`), `docker-compose.test-dev.yml` (dev image, hardcoded throwaway creds, no `.env`), `docker-compose.test-prod.yml` (**built prod image, no volume mounts** — proves what actually ships).
- **`./devmail.sh test` = test:dev then test:prod**, exits non-zero if either fails; that single command is the CI step.
- **Multi-stage Dockerfile with named targets** `dev` and `prod`; compose selects via `build.target`. `HEALTHCHECK` hits `/healthz` with `wget -qO-`; compose gates readiness with `condition: service_healthy`; the entrypoint script polls `/healthz` with `curl` before running specs.
- **Specs:** `tests/e2e/specs/*.spec.js`, Playwright, `testDir: './specs'`, `workers: 1`, `fullyParallel: false`, `reporter: 'list'`, `trace: 'retain-on-failure'`, `baseURL` from `PLAYWRIGHT_BASE_URL` (config *throws* if unset).
- **Helper layer is mandatory** (`tests/e2e/helpers/api.js`): specs never inline selectors, they go through helpers that wrap stable element ids.
- **Fixture constants are shared** between seeder and specs so they cannot drift (`db/seed-constants.js`).
- **Frontend is one static file**, vanilla JS + CSS, no framework, no bundler, dark `color-scheme`, ids as the DOM contract (`#sql`, `#result`, `#pager`, …).
- **URL is the state.** Every navigable affordance is a real `<a href>`; one delegated click handler intercepts plain left-clicks and calls `history.pushState`; modifier/middle clicks fall through to the browser.
- **CI** (`.github/workflows/ci.yml`): jobs `test` → `assert-version` → `build` (per-platform, push-by-digest, amd64 + arm64 native runners) → `merge` (manifest list) → `release`. Triggers: push `main`, tags `v*.*.*`, PRs to `main`. Publishes to `ghcr.io/<owner>/<repo>` with tags `edge`, `sha-<short>`, and semver on tags.
- **Docs:** `README.md` with a prominent security warning, an env-var table, an Architecture section, a Tags table, and `docs/releasing.md`. Design docs live in `docs/design/<slug>.md`.
- **Version lives in a manifest** and a CI job asserts it matches the git tag. devmail has no `package.json`; the equivalent is `VERSION` (a plain file) — see Open questions.

### New for devmail (no dbadmin precedent)

- **Language:** Go. Lint = `go vet ./...` + `gofmt -l .`; "typecheck" is the compiler (`go build ./...`). `./devmail.sh lint` runs all three.
- **Go unit tests** (`go test ./...`) are available here in a way dbadmin never had; they are *additional to*, not a substitute for, the Playwright e2e suite.

## Context

Nothing exists yet. Reference points from dbadmin worth quoting verbatim because devmail mirrors them:

- `dbadmin/Dockerfile` — 4 targets (`deps`, `deps-prod`, `dev`, `prod`); `prod` copies only `src` + prod `node_modules`, drops to `USER node`, `EXPOSE 80`, `HEALTHCHECK … wget -qO- http://127.0.0.1:${PORT}/healthz`.
- `dbadmin/dbadmin.sh:run_test_stack()` — `trap 'docker compose … down -v' EXIT`, `up -d --build`, `wait_for_healthz`, seed, `npx playwright test`, capture exit code, teardown, `return "$exit_code"`. devmail's `run_test_stack` is the same shape with the seeder swapped.
- `dbadmin/src/index.js:181-192` — signal handling: `process.once('SIGTERM'|'SIGINT')` → close server, drain pool. Go equivalent: `signal.NotifyContext` + `http.Server.Shutdown` + `smtp.Server.Shutdown`.
- `dbadmin/src/public/index.html:214-221` — the delegated click handler with modifier-key guards. Copied structurally.
- `dbadmin/tests/e2e/helpers/api.js` — helper module shape; devmail's equivalent wraps mailbox ids.

### Verified facts about the chosen dependencies

Read from the module cache, not from docs:

- `go-smtp@v0.25.0/server.go:242` — `(*Server).ListenAndServeTLS()` calls `tls.Listen(...)` then `Serve(l)`. That is **implicit TLS (SMTPS)**, the thing the brief asks for. It is a separate method from `ListenAndServe()`; STARTTLS is a different code path (`conn.go:919`).
- `go-smtp@v0.25.0/conn.go:259-260` — STARTTLS is advertised only when `TLSConfig != nil && !isTLS`. On an implicit-TLS listener the connection is already TLS, so **STARTTLS is never advertised** — no downgrade surface, no extra config needed.
- `go-smtp@v0.25.0/conn.go:204-206` — `authAllowed()` returns true iff the connection is TLS or `AllowInsecureAuth` is set. Implicit TLS satisfies this; `AllowInsecureAuth` stays false.
- **`go-smtp does not enforce authentication.`** `conn.go:handleMail` (line ~318) checks only `helo` and `bdatPipe` — there is no `didAuth` check anywhere in the MAIL/RCPT/DATA path (`didAuth` appears only at `conn.go:823, 895, 952`, all inside AUTH/STARTTLS handling). **The backend `Session` must refuse `Mail()` itself when unauthenticated**, or the server silently accepts anonymous mail. This is the single most important correctness point in the design.
- `go-smtp@v0.25.0/backend.go` — AUTH requires implementing the optional `AuthSession` interface: `AuthMechanisms() []string` and `Auth(mech string) (sasl.Server, error)`.
- `go-sasl@b788ff22d5a6/plain.go:75` — `NewPlainServer(PlainAuthenticator)` exists. **`login.go` has `NewLoginClient` only — there is no `NewLoginServer`.** Supporting AUTH LOGIN (which Symfony Mailer, PHPMailer and older Nodemailer configurations pick) requires ~30 lines implementing `sasl.Server` (`sasl.go:37`: `Next(response []byte) (challenge []byte, done bool, err error)`).

## Design

A single static Go binary in an Alpine image. Two listeners: **SMTPS** (implicit TLS, authentication mandatory, accepts and stores mail, never relays) and **HTTP** (mailbox UI + JSON API). Messages are held as **raw bytes only** in a bounded in-memory ring and are lost on restart — this is a dev tool, not a mail store.

### Process layout

```
main.go              config, store, TLS, both listeners, signal shutdown, GOMEMLIMIT
config.go            env parsing + validation (fails fast at boot), redacting String()
store.go             Message, ring bounded by BOTH count and total bytes
smtp.go              go-smtp Backend/Session: auth gate, Reset contract, size cap, store
sasllogin.go         dual-entry AUTH LOGIN sasl.Server (go-sasl has no server side)
parse.go             lazy MIME parse: raw bytes -> ParsedMessage
tlscert.go           load cert from env paths, else generate ephemeral self-signed
web.go               JSON API, bearer-token middleware, embedded UI
public/index.html    the whole frontend (go:embed)
```

Flat `package main` at the repo root, mirroring dbadmin's flat `src/`. No `internal/` tree.

### SMTP

- **Implicit TLS only** — `smtp.Server.ListenAndServeTLS()`. `AllowInsecureAuth = false`. There is no cleartext listener, so STARTTLS is structurally unreachable (`conn.go:259-260`).
- **Auth is enforced by our `Session`, not by go-smtp.** `Mail()`, `Rcpt()` and `Data()` each return `smtp.ErrAuthRequired` unless `s.authed`. go-smtp's `handleMail` has no `didAuth` check (V-verified).
- **`Reset()` contract (V11):** clears `from`, `to`, and per-message state; **preserves `authed`**. go-smtp calls it after *every* message (`conn.go:979`→`1347`), on `RSET`, and on a repeated `EHLO`, while `NewSession` runs once per connection. Getting this wrong either drops every message after the first on a pooled connection, or leaks message N-1's recipients into message N.
- **Auth failure returns a fixed 535** (`*smtp.SMTPError{Code:535, EnhancedCode:{5,7,8}, Message:"Authentication credentials invalid"}`), never go-smtp's default 454, which mailers read as "retry later" and loop on forever. The authenticator's own error text is logged server-side and **never** written to the wire (V1).
- **Mechanisms:** `PLAIN` (via `sasl.NewPlainServer`) and `LOGIN` (in-repo, `sasllogin.go`). The LOGIN server is **dual-entry** (V13): if `Next` is first called with a non-nil response, that response *is* the username; if nil, it emits `Username:` first. The password challenge must be byte-exactly `Password:` or go-sasl's own client rejects it. Both compare with `crypto/subtle.ConstantTimeCompare`.
- **Failed-auth attempts are counted per connection** and the connection is dropped after 3. go-smtp routes auth failures through `writeError`, not `protocolError`, so its own `errCount` never increments — unlimited guesses otherwise (V13).
- **Every recipient is accepted.** `Rcpt()` never rejects on address; swallowing all mail is the point.
- **Nothing is ever relayed.** No outbound SMTP client exists in the binary.
- **Explicit limits:** `MaxMessageBytes`, `MaxRecipients`, `MaxLineLength` (**must be set explicitly — the 2000 default silently destroys any message with a long base64 or DKIM line, handing `Data()` zero bytes**, V2), `ReadTimeout`, `WriteTimeout`.
- **Read errors reject; parse errors store** (V12). `ErrDataTooLarge` hands `Data()` a complete-looking truncated prefix alongside the error — that prefix is discarded and the client gets 552. Only *parsing* problems produce a stored message with `ParseError` set.

### Storage

`Store` is a mutex-guarded `[]*Message` ring bounded by **both** `MaxMessages` and `MaxTotalBytes`; `Add` evicts oldest until both hold. `[]*Message` (not `[]Message`) so eviction concurrent with a reader is safe rather than tearing.

A stored `Message` holds **only** `{ID, ReceivedAt, From, To, Size, Subject, Raw}`. `Subject` is lifted at receive by a header-only scan (cheap, and it is the one field the list must show). Everything else — text, HTML, attachments, full headers — is parsed **on demand** in `GET /api/messages/{id}`.

**Ids are opaque random strings**, not a counter (V16): decimal-string ids sort lexicographically wrong at ten messages, and a per-process counter makes `/?id=5` point at unrelated mail after any restart.

**Insertion order under the store mutex is the total order.** Nothing sorts by `ReceivedAt` — that would reintroduce a tie. The list is labelled with `receivedAt` (arrival), not the sender's `Date:` header; the header `Date` appears only in the detail pane (V17).

Order matters for eviction vs. auth: a message is added to the store only after `Data()` has fully succeeded, and the client is ACKed 250 only after `Add` returns.

### HTTP API

```
GET    /healthz                              -> 200 {"status":"ok","version":"..."}   NO TOKEN
GET    /                                     -> the UI shell                          NO TOKEN
GET    /api/messages                         -> [{id, receivedAt, from, to[], subject, size}]  newest first
GET    /api/messages/{id}                    -> parsed detail (see Interfaces)
GET    /api/messages/{id}/raw                -> text/plain, bytes as received
GET    /api/messages/{id}/attachments/{n}    -> the attachment bytes
DELETE /api/messages/{id}                    -> 204, or 404 if unknown
DELETE /api/messages                         -> 204, clears the mailbox
```

`net/http` with Go 1.22 pattern routing. No router dependency. The list refreshes by **polling** (`setInterval`, 2s); there is no SSE, so `Store` carries no subscribers and cannot be wedged by a suspended browser tab.

**`/healthz` reports SMTP liveness, not just HTTP** (V5). `main` records successful SMTP bind in an atomic; `/healthz` returns 503 until it is set, and either listener exiting is fatal to the process. Otherwise a devmail with a dead SMTP port stays green and the whole test harness — Docker `HEALTHCHECK`, compose `service_healthy`, `devmail.sh`'s poll — starts against it anyway.

### HTTP authentication

`DEVMAIL_HTTP_TOKEN`, **unset by default** (open, like dbadmin, with the same loud README warning). When set:

- `/api/*` requires `Authorization: Bearer <token>`, compared with `crypto/subtle.ConstantTimeCompare`. 401 otherwise.
- `/` and `/healthz` are always exempt — the shell carries no data, and gating `/healthz` breaks k8s probes and the Docker `HEALTHCHECK`.
- The browser bootstraps from `?token=…`: the page stores it in `sessionStorage` and immediately `history.replaceState`s it out of the URL, so it leaves no history entry. It still appears once in the server access log of that first request — stated in the README.
- **Every browser sub-resource goes through `fetch()` with the header.** An `<iframe src>` or `<a href download>` cannot send `Authorization`, so the HTML body rides inside the detail JSON, and an attachment download is `fetch` → `blob:` URL → `<a href>`. This is why there is no `/html` sub-resource endpoint.
- `#authRequired` is shown when `/api/*` answers 401, instead of a blank pane.

### Frontend

One embedded `public/index.html`: vanilla JS, dark theme, two panes — list left, message right — following dbadmin's visual language and its "URL is the state" rule. `?id=<id>` selects a message; list rows are real `<a href="/?id=…">` behind one delegated click handler with modifier-key guards. Back/Forward work.

A **`renderSeq` guard** (ported from `dbadmin/src/public/index.html:49-51`) covers the click × auto-refresh race, and **the list refresh never touches the detail pane** (V20). When `?id=` names a message that was evicted or deleted, `#notFound` is shown — a distinct state from `#empty`.

**HTML bodies render in `<iframe sandbox="" srcdoc="…">`**, where:
- The iframe carries `sandbox=""` **in the static HTML**, and the body is assigned via the **DOM property** (`el.srcdoc = html`), never string-interpolated into markup. Building it as markup lets a body containing `"` break out of the attribute into the mailbox document, outside the sandbox (V19).
- **Remote subresources are stripped server-side by default** — `sandbox=""` does not block image loads, so a tracking pixel would fire on click and leak "opened" plus the developer's IP, from a tool whose premise is that it sends nothing. Stripping is the primary control because the iframe `csp=` attribute is Chrome-only; `csp="default-src 'none'; img-src data:"` is set as well, belt-and-braces.
- A per-message **"Load remote images"** toggle re-fetches with `?remote=1` and renders unstripped, for when you genuinely need to check a template.

### TLS certificate

1. `DEVMAIL_TLS_CERT` + `DEVMAIL_TLS_KEY` set → load that pair; failure is fatal at boot.
2. Otherwise generate an **ephemeral** self-signed P-256 cert at startup for `DEVMAIL_TLS_HOSTS`, and log the SHA-256 fingerprint **labelled as ephemeral** so nobody pins it.

Consuming apps must disable verification (`rejectUnauthorized:false`, `verify_peer:false`, …). **A wildcard SAN cannot cover the k8s service FQDN** — `*.svc.cluster.local` matches exactly one label, so it does not match `devmail.myns.svc.cluster.local`. The default is therefore `localhost,127.0.0.1,::1,devmail`, and the README instructs operators to set `DEVMAIL_TLS_HOSTS=devmail.myns.svc.cluster.local,devmail.myns,devmail` for their namespace. JVM/JavaMail clients, which cannot skip verification, must mount a stable cert via `DEVMAIL_TLS_CERT`.

### Logging

One structured line per event, because five distinct failures otherwise all present as "empty mailbox, zero output": accepted (remote addr, from, rcpt count, bytes, id), auth-failed, rejected-oversize, rejected-long-line, parse-error, evicted. go-smtp's own `ErrorLog` covers none of these — `handleConn` returns `nil` for EOF, `ErrTooLongLine` and idle timeout alike (`server.go:232-244`).

### Shutdown

`signal.NotifyContext` → stop accepting, `http.Server.Shutdown(ctx)` and `smtp.Server.Shutdown(ctx)` with a bounded context, then **`smtp.Server.Close()` on expiry** — go-smtp's `Shutdown` waits on a WaitGroup and never closes idle connections, and unlike `http.Server` it has no fallback, so a pooled idle connection blocks SIGTERM until `ReadTimeout` (V3). The README sets `terminationGracePeriodSeconds` above `ReadTimeout`.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `DEVMAIL_SMTP_PORT` | `465` | SMTPS listen port |
| `DEVMAIL_SMTP_USER` | — **required** | AUTH username |
| `DEVMAIL_SMTP_PASSWORD` | — **required** | AUTH password |
| `DEVMAIL_SMTP_PASSWORD_FILE` | empty | read the password from a mounted Secret instead |
| `DEVMAIL_SMTP_DOMAIN` | `devmail` | greeting banner |
| `DEVMAIL_HTTP_PORT` | `80` | mailbox UI + API |
| `DEVMAIL_HTTP_TOKEN` | empty | empty = no HTTP auth; set = Bearer required on `/api/*` |
| `DEVMAIL_MAX_MESSAGES` | `100` | ring bound by count |
| `DEVMAIL_MAX_TOTAL_BYTES` | `16777216` | ring bound by total raw bytes (16 MiB) |
| `DEVMAIL_MAX_MESSAGE_BYTES` | `2097152` | per-message cap (2 MiB) |
| `DEVMAIL_MAX_LINE_BYTES` | `1048576` | **must be set explicitly**; go-smtp's 2000 default eats real mail |
| `DEVMAIL_MAX_RECIPIENTS` | `100` | per-message RCPT cap |
| `DEVMAIL_READ_TIMEOUT` / `DEVMAIL_WRITE_TIMEOUT` | `60s` | armed per readLine, so it governs the whole DATA body (V21) |
| `DEVMAIL_TLS_CERT` / `DEVMAIL_TLS_KEY` | empty | PEM paths; empty = ephemeral self-signed |
| `DEVMAIL_TLS_HOSTS` | `localhost,127.0.0.1,::1,devmail` | SANs; set per namespace in k8s |

**Boot fails loudly** if user or password is empty. `Config` implements a redacting `String()`/`GoString()` so no future `%+v` can leak the password into logs (V7-adjacent).

`GOMEMLIMIT` is set from `MaxTotalBytes` × a headroom factor at startup, so Go's GC targets a bound consistent with the ring rather than the default `GOGC=100` doubling.

### Image

```
FROM golang:1.24-alpine AS build   # CGO_ENABLED=0, -trimpath, -ldflags="-s -w -X main.version=..."
FROM golang:1.24-alpine AS dev     # source mounted, `go run .`
FROM alpine:3.21       AS prod     # nonroot user, the binary, nothing else
```

No `ca-certificates` — there is no outbound TLS client in the binary, so the CA bundle would never be read. Measured (both roasts independently): `alpine:3.21` 7.83 MB + binary ~5.6 MB (with `go-message/charset`) → **≈14 MB uncompressed, ≈6 MB compressed**, against a 25 MB ceiling. `./devmail.sh size` asserts the **uncompressed `docker image inspect .Size`** figure, stated explicitly because compressed and uncompressed differ by ~2.2× and the doc was previously ambiguous.

`_ "github.com/emersion/go-message/charset"` is blank-imported. Without it, latin-1 bodies land in a Go string as raw bytes and `encoding/json` silently replaces them with U+FFFD **returning no error**, while subjects decode fine — mojibake bodies with no diagnostic (V15). Measured cost 0.86 MB against ~11 MB of headroom.

## Interfaces

```go
// config.go
type Config struct {
    SMTPPort, HTTPPort              int
    SMTPUser, SMTPPassword          string
    SMTPDomain                      string
    HTTPToken                       string
    MaxMessages, MaxRecipients      int
    MaxTotalBytes, MaxMessageBytes  int64
    MaxLineBytes                    int
    ReadTimeout, WriteTimeout       time.Duration
    TLSCertFile, TLSKeyFile         string
    TLSHosts                        []string
}
func Load() (*Config, error)
func (c *Config) String() string   // redacts SMTPPassword and HTTPToken

// store.go  -- stored form: raw bytes only
type Message struct {
    ID         string    `json:"id"`
    ReceivedAt time.Time `json:"receivedAt"`
    From       string    `json:"from"`      // envelope MAIL FROM
    To         []string  `json:"to"`        // envelope RCPT TO
    Subject    string    `json:"subject"`   // header-only scan at receive
    Size       int       `json:"size"`
    Raw        []byte    `json:"-"`
}
type Store struct{ /* mu, msgs []*Message, bytes int64, maxMsgs int, maxBytes int64 */ }
func NewStore(maxMsgs int, maxBytes int64) *Store
func (s *Store) Add(m *Message)          // assigns opaque random ID; evicts until BOTH bounds hold
func (s *Store) List() []Message         // VALUES, newest first, Raw nil -- never leaks pointers
func (s *Store) Get(id string) (*Message, bool)
func (s *Store) Delete(id string) bool
func (s *Store) Clear()

// parse.go  -- on demand, never at receive
type Attachment struct {
    Filename    string `json:"filename"`     // "attachment-N" when the part names none
    ContentType string `json:"contentType"`
    Size        int    `json:"size"`
}
type ParsedMessage struct {
    Message
    Headers     map[string][]string `json:"headers"`   // []string: a message has many Received:
    Date        *time.Time          `json:"date,omitempty"`
    Text        string              `json:"text,omitempty"`
    HTML        string              `json:"html,omitempty"`
    Attachments []Attachment        `json:"attachments,omitempty"`
    ParseError  string              `json:"parseError,omitempty"`
}
// Parse MUST NOT dereference the Reader when CreateReader returns nil (V10):
//   mr, err := mail.CreateReader(bytes.NewReader(raw))
//   if mr == nil { return ParsedMessage{Message: m, ParseError: err.Error()}, nil }
// Part classification is by CONTENT-TYPE, not by go-message's Inline/Attachment
// header type (V14): only text/plain -> Text, only text/html -> HTML, everything
// else -> Attachments regardless of disposition. An inline cid: PNG is an
// attachment, not body text.
func Parse(m *Message, stripRemote bool) ParsedMessage
func ParseAttachment(m *Message, n int) (Attachment, []byte, error)

// smtp.go
func NewSMTPServer(cfg *Config, st *Store, tlsCfg *tls.Config, log *slog.Logger) *smtp.Server

// sasllogin.go
func NewLoginServer(auth func(user, pass string) error) sasl.Server   // dual-entry, see V13

// tlscert.go
func TLSConfig(cfg *Config, log *slog.Logger) (*tls.Config, error)

// web.go
func NewHandler(cfg *Config, st *Store, smtpReady *atomic.Bool, log *slog.Logger) http.Handler
```

Attachment downloads are served `Content-Type: application/octet-stream`, `X-Content-Type-Options: nosniff`, and `Content-Disposition: attachment; filename="…"` with the filename sanitized and quoted — **never** the email's declared Content-Type, which would make a `text/html` attachment stored XSS on the mailbox origin.

DOM contract the specs and helpers may rely on:

| id / selector | element |
|---|---|
| `#messageList` | `<ul>`; rows `li[data-id]` containing `a[href^="/?id="]` |
| `#messageList li.active` | the selected row |
| `.msg-from`, `.msg-subject`, `.msg-date` | fields within a row (`.msg-date` renders `receivedAt`) |
| `#count` | message count |
| `#detail` | right pane wrapper |
| `#detailSubject`, `#detailFrom`, `#detailTo`, `#detailDate` | header fields |
| `#tabText`, `#tabHtml`, `#tabRaw` | tab buttons |
| `#bodyText` | `<pre>` for the plain-text part |
| `#bodyHtml` | the `<iframe sandbox="">`, static in the HTML |
| `#loadRemote` | "Load remote images" toggle |
| `#attachments` | `<ul>`; rows `li[data-index]` with a download link |
| `#deleteMessage` | per-message delete button |
| `#clear` | clear-mailbox button |
| `#empty` | "no messages" placeholder |
| `#notFound` | "that message is gone" placeholder (evicted or deleted) |
| `#authRequired` | shown when `/api/*` returns 401 |

## Work breakdown

1. **Config + store** — `config.go`, `store.go`, `config_test.go`, `store_test.go`. Dual-bound ring, opaque ids, redacting `String()`. Depends on: nothing.
2. **SMTP server + auth** — `smtp.go`, `sasllogin.go`, `tlscert.go`, `smtp_test.go`, `sasllogin_test.go`. Auth gate, `Reset()` contract, 535, dual-entry LOGIN, failed-attempt counter, explicit limits. Depends on: 1. **Escalated to opus** — it is the security boundary and a protocol state machine.
3. **Parsing** — `parse.go`, `parse_test.go`. Lazy parse, nil-Reader guard, Content-Type classification, remote stripping. Depends on: 1.
4. **Web API + UI** — `web.go`, `public/index.html`, `web_test.go`. Bearer middleware, routes, SMTP-aware `/healthz`, the two-pane UI, `renderSeq`, srcdoc property assignment. Depends on: 1, 3.
5. **Container + entrypoint** — `Dockerfile`, `docker-compose*.yml`, `devmail.sh`, `.env.example`, `.dockerignore`, `.gitignore`, `package.json`. Depends on: 1-4 for env/port names only (fixed by the skeleton).
6. **E2E + CI + docs** — `tests/e2e/**`, `tests/fixtures.json`, `.github/workflows/ci.yml`, `README.md`, `docs/releasing.md`. Depends on: 1-5.

## E2E acceptance

Browser specs (Playwright) prove what only a browser can; Go tests prove the protocol.

1. **Authenticated SMTPS delivery renders in the mailbox.** `./devmail.sh send` delivers a multipart fixture over implicit TLS with valid credentials. The browser shows the row in `#messageList` with the expected sender and subject; clicking it fills `#detailSubject`/`#detailFrom`, renders the plain part in `#bodyText`, and the HTML tab sets `#bodyHtml`'s `srcdoc` **attribute** while `sandbox` is present and empty.
   *Asserting the attribute, not the frame's contents:* `sandbox=""` grants no `allow-scripts`, and Playwright's `frameLocator()` runs its selector engine inside the target frame — so a `frameLocator` assertion would likely time out. The attribute assertion is also what catches the srcdoc-escaping defect (V19). **This behaviour must be confirmed against a real Playwright run before the spec is finalised** — it is reasoned, not yet observed.
2. **Rejected mail never reaches the mailbox, proven with a positive barrier.** Send (a) with no AUTH, (b) with a wrong password, then (c) one *authenticated* message. Assert `#count` is exactly **1** and the single row is (c). Asserting only "the list is empty" after (a) and (b) is vacuous — it passes on a build with no auth gate at all, because the assertion can run before any delivery would have appeared (V18).

Go tests additionally cover, none of which a browser can reach cleanly: **two messages on one connection** (the `Reset()`/`authed` contract, invisible to any test opening a fresh connection per message), a **header-less malformed message** (stored with `ParseError`, not a 421 that kills the connection), a **line over 2000 bytes** (must be accepted, proving `MaxLineLength` was set), wrong-password returning **535 not 454**, and AUTH LOGIN driven by go-sasl's own client (proving the dual-entry state machine).

## Open questions

Merged from the ops/security and simplicity roasts. **Verified by me against source** unless marked otherwise. The correctness roast had not reported when this was written — re-merge before closing Phase 2.

### Verified defects in the design (not questions — these must change)

- **V1. Wrong password returns 454, not 535.** `conn.go:853,862` — go-smtp calls `writeError(454, {4,7,0}, err)` for any SASL error. 4xx means *retry later*, so a typo'd password gives consuming mailers an infinite reconnect loop instead of a hard failure. It also echoes the authenticator's `err.Error()` to an unauthenticated client. Fix: return `*smtp.SMTPError{Code: 535, EnhancedCode: {5,7,8}, Message: "Authentication credentials invalid"}` and log detail server-side only. E2E acceptance #2 as written asserts 535 and would fail on day one.
- **V2. `MaxLineLength` defaults to 2000 and silently destroys messages.** `server.go:97` sets it in `NewServer`; the limit wraps the raw connection so it applies to DATA body bytes. When it trips, `Session.Data()` receives **zero bytes** — so the doc's "malformed mail is still stored with ParseError" guarantee cannot fire, the message vanishes whole, and `handleConn` returns `nil` (`server.go:238-241`) so `ErrorLog` never fires either. PHP `base64_encode()` without `chunk_split()`, long DKIM/References headers, and inline `data:` URIs all exceed 2000 routinely.
- **V3. `Shutdown` never closes idle connections.** `server.go:293-315` closes listeners then blocks on the waitgroup; pooled idle SMTP connections keep it blocked until `ReadTimeout` (30s) — a dead heat with k8s' default 30s grace period. On ctx expiry it returns `ctx.Err()` and leaves connections open; unlike `http.Server` there is **no `Close()` fallback**, so an explicit one is required.
- **V4. `List() []*Message` cannot produce the specced list JSON.** Only `Raw` has `json:"-"`; `Text`/`HTML` are `omitempty`, so the list endpoint serialises full bodies. And `hasHTML`/`hasAttachments` (line ~97) are not fields on `Message` at all. Needs a separate list DTO.
- **V5. `/healthz` proves nothing about SMTP.** It is served by the HTTP mux only. If `ListenAndServeTLS()` fails (port in use, bad cert), `/healthz` still returns 200 — so the Docker HEALTHCHECK, compose `service_healthy`, and `devmail.sh`'s poll all go green against a devmail with no SMTP listener, and the e2e suite starts anyway.
- **V6. go-smtp has no connection cap.** No `MaxConn` field on `Server`; `Serve` does an unconditional `go handleConn` per accept.
- **V7. `package.json` is unavoidable.** Playwright needs `package.json` + lockfile (`npm ci`, `npx playwright install`), which contradicts the doc's "no package.json, use a VERSION file" convention. Either drop the browser or use `package.json` version exactly as dbadmin does.
- **V8. Measured image sizes** (both roasts, independently, agreeing): `alpine:3.21` = 7.83 MB; prod-shaped image = **13.8–14.3 MB uncompressed, ~6.2 MB compressed**. The doc's 18–20 MB estimate overstates the binary ~2x. The 25 MB ceiling is cleared by ~11 MB and will never fire. `ca-certificates` is dead weight — there is no outbound TLS client in the binary. `FROM scratch` measures **5.91 MB**.
- **V9. `go vet ./...` subsumes `go build ./...`.** Verified: on `var s string = 42`, `go vet` prints the identical type error and exits 1. A separate `typecheck` command can never fail after `lint` passes.

### Questions for the user

All resolved — see `## Decisions`.

### Roast claims logged but NOT yet verified by me

- go-message parse behaviour on malformed input / nested multiparts (correctness roast pending).
- The measured 4 GiB/8 GiB heap figure (ops roast ran it; I have not reproduced). Corroborated independently by the correctness roast's 2 GiB-of-`Raw`-alone arithmetic.
- ~~`go-message/charset` disagreement~~ — **resolved, see V15: import it.** The correctness roast's evidence (silent U+FFFD corruption with no error, subjects decoding correctly while bodies do not) is decisive against the simplicity roast's byte-count objection.
- mailpit measured at 35.3 MB uncompressed — over the ceiling, so no drop-in replacement beats the budget.

## Decisions

Append-only. Question -> answer -> consequence.

1. **Mailbox HTTP auth?** -> **Optional `DEVMAIL_HTTP_TOKEN`, unset by default.** Unset behaves exactly like dbadmin (open, loud README warning); set requires `Authorization: Bearer` on `/api/*`. *Consequence:* the hook ships in v1, so turning auth on later is not a breaking change for scripted consumers. `/` and `/healthz` stay exempt — gating `/healthz` would break the k8s probe and the Docker `HEALTHCHECK`.
2. **Memory model?** -> **Store raw bytes only; parse on demand.** *Consequence:* `Text`/`HTML`/`Attachments`/`ParseError`/`Headers` leave the stored struct entirely, which also dissolves the `List()`-DTO defect (V4) — `List()` now returns *values* with `Raw` nil and cannot leak pointers into the store. `Subject` is still lifted at receive by a header-only scan, because the list must show it. `GET /api/messages/{id}` re-parses on every request; acceptable for a dev tool at a 2 MiB cap.
3. **Test strategy?** -> **Playwright for the UI + Go tests for the protocol.** *Consequence:* `package.json` enters the repo, which **kills the VERSION-file plan** (V7) — `assert-version` reads `package.json` exactly as dbadmin's does. `./devmail.sh setup` gains npm install + `npx playwright install --with-deps chromium`, and CI gains `setup-node`. The specs are not ESLint-ed; `./devmail.sh check` covers Go only (`gofmt -l`, `go vet`, `go test`), and there is **no separate `typecheck`** because `go vet` subsumes `go build` (V9, verified).
4. **Scope cuts?** -> **SSE cut, replaced by 2s polling. Attachments, per-message DELETE and `?id=` URL state all kept.** *Consequence:* `Store` carries no subscribers, so the fan-out-under-mutex deadlock is unreachable by construction and a suspended browser tab cannot wedge SMTP. Keeping `?id=` requires the `renderSeq` guard and a `#notFound` state (V20); keeping per-message DELETE requires a UI affordance (`#deleteMessage`) and a defined 404.
5. **Attachments vs lazy parse (follow-up to 2 and 4)?** -> **Detail view only, plus a real download endpoint.** *Consequence:* `hasAttachments`/`hasHTML` leave the list payload — they were the only thing forcing an eager MIME walk, so decisions 2 and 4 stop conflicting. `GET /api/messages/{id}/attachments/{n}` is served `application/octet-stream` + `nosniff` + a sanitized quoted filename, **never** the email's declared Content-Type, which would make a `text/html` attachment stored XSS on the mailbox origin.
6. **Token carrier (follow-up to 1)?** -> **Bearer on `/api/*`; the page itself open; `?token=` bootstraps into `sessionStorage` and is stripped via `replaceState`.** *Consequence:* an `<iframe src>` and an `<a href download>` cannot send `Authorization`, so **every browser sub-resource must go through `fetch()`** — the HTML body rides inside the detail JSON rather than getting its own URL, and attachment downloads are `fetch` → `blob:` → `<a href>`. The token still appears once in the access log of the bootstrap request; stated in the README.
7. **TLS cert lifetime?** -> **Ephemeral, regenerated per restart; SANs widened.** *Consequence, and it corrects the premise of the answer:* **a wildcard SAN cannot cover the k8s service FQDN** — `*.svc.cluster.local` matches exactly one label, so it never matches `devmail.myns.svc.cluster.local`. "Widen" is therefore a sane default (`localhost,127.0.0.1,::1,devmail`) plus a documented per-namespace `DEVMAIL_TLS_HOSTS`. JVM/JavaMail clients, which cannot disable verification, must mount a stable cert via `DEVMAIL_TLS_CERT`. The logged fingerprint is labelled ephemeral so nobody pins it.
8. **Remote content in the HTML view?** -> **Blocked by default, with a per-message "Load remote images" toggle.** *Consequence, correcting the mechanism:* the iframe `csp=` attribute is **Chrome-only**, so it cannot be the primary control — remote `src`/`href`/`url()` are **stripped server-side** during parse (`?remote=1` renders unstripped), with `csp=` set as belt-and-braces.
9. **Memory budget?** -> **16 MiB total / 100 messages / 2 MiB per message.** Chosen with the "wrong default if your app sends invoices" caveat visible. *Consequence not on the card:* base64 inflates attachments ~33%, so the effective attachment ceiling is **~1.5 MB** — a larger PDF is rejected with 552. One env var (`DEVMAIL_MAX_MESSAGE_BYTES`) raises it. `GOMEMLIMIT` is derived from `MaxTotalBytes` so the GC targets a consistent bound.
10. **`devmail.sh` vocabulary?** -> **Drop `seed`/`reset`/`down --volumes`; add `send`.** *Consequence:* `send` fires a fixture message at the running dev stack over authenticated SMTPS and doubles as the e2e seeder, so specs and the manual smoke test cannot drift. Final surface: `setup, dev, down, send, test:dev, test:prod, test, check, size`.
11. **Resolved without asking — `go-message/charset` is blank-imported.** The two roasts disagreed; the correctness evidence was decisive. Without it, latin-1 bodies reach a Go string as raw bytes and `encoding/json` replaces them with U+FFFD **returning no error**, while subjects decode correctly — mojibake bodies with no diagnostic (V15). Measured cost 0.86 MB against ~11 MB of headroom.
12. **Resolved without asking — ids are opaque random strings.** Decimal-string ids sort lexicographically wrong at ten messages (`["1","2","10"]` → `[1 10 2]`), and a per-process counter makes a bookmarked `/?id=5` point at unrelated mail after any restart. Insertion order under the store mutex is the total order; nothing sorts by `ReceivedAt`, which would reintroduce a tie.
13. **Resolved without asking — no `ca-certificates` in the prod image.** There is no outbound TLS client in the binary, so the CA bundle would never be read. `./devmail.sh size` asserts the **uncompressed** `docker image inspect .Size`, stated explicitly because compressed and uncompressed differ by ~2.2×.

## Risks

- **In-memory only.** A container restart — including an OOMKill — loses every trapped message. Nothing warns the user that this happened; the mailbox simply comes back empty.
- **Single instance is a hard constraint, and nothing enforces it.** `replicas: 2` behind a Service, or the default `RollingUpdate` with `maxSurge: 1` even at `replicas: 1`, splits mail across pods: SMTP lands on A while the browser's poll round-robins to B, so the mailbox flickers between "3 messages" and "empty". Reads exactly like a bug in the app under test. The manifest must specify `replicas: 1` + `strategy: Recreate`, and the README must say why.
- **The 2 MiB per-message default rejects ordinary invoice mail** (see Decision 9). The failure is a 552 at DATA plus a log line — visible, but only if someone reads the logs.
- **`ReadTimeout` governs the entire DATA body**, not just one line (V21), because it is armed per-`readLine`. 60 s for 2 MiB is ample, but raising `MaxMessageBytes` without raising `ReadTimeout` reintroduces the cliff.
- **The credentials are still plain env vars by default.** `DEVMAIL_SMTP_PASSWORD_FILE` and the redacting `String()` mitigate the log and `%+v` paths, but `kubectl describe pod` still prints anything passed via `env:`.
- **The HTML view is the main attack surface**, and `sandbox=""` plus server-side stripping is the whole mitigation. A stripping bug is a same-origin script execution in the mailbox.
- **Playwright's behaviour against `sandbox=""` is reasoned, not observed** (V18). If `frameLocator()` turns out to work, criterion 1's assertion can be strengthened; if the attribute assertion turns out to be unstable, the spec needs rework. Confirm on the first real run.
- **`./devmail.sh size` guards a constraint already won** — measured ~14 MB against a 25 MB ceiling. It will not fire. The constraint that actually needs a gate is memory, which is now bounded by `MaxTotalBytes` + `GOMEMLIMIT` rather than by the size check.
- **Ephemeral certs break anything that pins**, on every restart, by design (Decision 7).
