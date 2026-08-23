Status: Phase 2 — roast merged (2 of 3 reports in; correctness roast outstanding). Next: verify remaining claims, then Phase 3 question loop.

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

A single static Go binary in an Alpine image. It listens on two ports: **SMTPS** (implicit TLS, authenticated, accepts and stores mail, never relays) and **HTTP** (the mailbox UI + JSON API). Messages live in a bounded in-memory ring buffer and are lost on restart — this is a dev tool, not a mail store.

### Process layout

```
main.go              config from env, build store, start SMTP + HTTP, signal shutdown
config.go            env parsing + validation (fails fast at boot)
store.go             Message type, in-memory ring buffer, subscriber fan-out
smtp.go              go-smtp Backend/Session: auth gate, size cap, parse, store
tlscert.go           load cert from env paths, else generate a self-signed one
web.go               JSON API handlers + embedded UI
public/index.html    the whole frontend (go:embed)
```

Flat `package main` at the repo root, mirroring dbadmin's flat `src/`. No `internal/` tree — the whole thing is ~800 lines.

### SMTP

- **Implicit TLS only.** `smtp.Server.ListenAndServeTLS()`. `AllowInsecureAuth = false`. STARTTLS is structurally unreachable (see verified facts). There is no cleartext SMTP listener at all.
- **Auth is mandatory and enforced by our `Session`.** `Session.Mail()`, `Rcpt()` and `Data()` each return `smtp.ErrAuthRequired` unless `s.authed` is true. go-smtp will not do this for us.
- **Mechanisms:** `PLAIN` and `LOGIN`. PLAIN via `sasl.NewPlainServer`; LOGIN via a small in-repo `sasl.Server` implementation, because go-sasl ships no LOGIN server. Both compare with `crypto/subtle.ConstantTimeCompare`.
- **Every recipient is accepted.** `Rcpt()` never rejects on address — the point is to swallow all mail regardless of domain.
- **Nothing is ever relayed.** There is no outbound SMTP client anywhere in the binary.
- **Size cap:** `smtp.Server.MaxMessageBytes` = `DEVMAIL_MAX_MESSAGE_BYTES` (default 10 MiB). Also `MaxRecipients` (default 100) and `ReadTimeout`/`WriteTimeout` (default 30 s) so a stuck client cannot pin a goroutine forever.

### Storage

`store.Store` is a mutex-guarded slice used as a ring: appending past `DEVMAIL_MAX_MESSAGES` (default 200) drops the oldest. Ids are a monotonically increasing counter rendered as a decimal string, so ordering is total and ids are never reused within a process lifetime.

A `Message` is parsed **once, at receive time** — envelope, headers, text part, HTML part, attachment metadata — and the raw bytes are kept alongside so `/raw` and re-parsing stay possible. Parsing failures are not dropped: the message is stored with `ParseError` set and the raw body still viewable, because a mailtrap that hides malformed mail is useless for debugging exactly the case you care about.

### HTTP API

```
GET    /healthz              -> 200 {"status":"ok"}
GET    /api/messages         -> [{id, from, to[], subject, date, size, hasHTML, hasAttachments}]  newest first
GET    /api/messages/{id}    -> {id, from, to[], subject, date, size, headers{}, text, html, attachments[{filename,contentType,size}], parseError}
GET    /api/messages/{id}/raw-> text/plain, the RFC 5322 bytes as received
DELETE /api/messages/{id}    -> 204
DELETE /api/messages         -> 204, clears the mailbox
GET    /api/events           -> text/event-stream, one event per new message (see Open questions)
```

`net/http` with Go 1.22+ pattern routing (`GET /api/messages/{id}`). No router dependency.

### Frontend

One embedded `public/index.html`: vanilla JS, dark theme, two panes — message list on the left, selected message on the right — following dbadmin's visual language and its "URL is the state" rule. `?id=<id>` selects a message; the list items are real `<a href="/?id=…">` and one delegated click handler pushes state. Back/Forward work.

**HTML bodies render inside `<iframe sandbox="" srcdoc="…">`.** A trapped email is attacker-controlled input; rendering its HTML into the mailbox document would let any mail sent to the trap run script in the mailbox origin. `sandbox=""` with no allow-tokens blocks scripts, forms, top-level navigation and same-origin access. A Text / HTML / Raw tab strip selects the view.

The list refreshes as mail arrives (mechanism = open question: SSE vs polling).

### TLS certificate

1. If `DEVMAIL_TLS_CERT` and `DEVMAIL_TLS_KEY` are set, load that pair; a failure is fatal at boot.
2. Otherwise generate a self-signed P-256 certificate **in memory at startup**, valid for `DEVMAIL_TLS_HOSTS` (default `localhost,devmail,127.0.0.1`), and log its SHA-256 fingerprint.

The self-signed default means consuming apps must disable certificate verification (`rejectUnauthorized: false`, `verify_peer: false`, `TLS_SKIP_VERIFY`, …). That is normal for a dev mailtrap and is called out loudly in the README. The cert is regenerated on every restart, so anything that pinned it breaks — see Open questions.

### Configuration

All via env, all with defaults except the credentials:

| Variable | Default | Meaning |
|---|---|---|
| `DEVMAIL_SMTP_PORT` | `465` | SMTPS listen port |
| `DEVMAIL_SMTP_USER` | — **required** | AUTH username |
| `DEVMAIL_SMTP_PASSWORD` | — **required** | AUTH password |
| `DEVMAIL_SMTP_DOMAIN` | `devmail` | greeting banner name |
| `DEVMAIL_HTTP_PORT` | `80` | mailbox UI + API |
| `DEVMAIL_MAX_MESSAGES` | `200` | ring buffer size |
| `DEVMAIL_MAX_MESSAGE_BYTES` | `10485760` | per-message cap |
| `DEVMAIL_MAX_RECIPIENTS` | `100` | per-message RCPT cap |
| `DEVMAIL_TLS_CERT` / `DEVMAIL_TLS_KEY` | empty | PEM paths; empty = self-signed |
| `DEVMAIL_TLS_HOSTS` | `localhost,devmail,127.0.0.1` | SANs for the generated cert |

**Boot fails loudly** if `DEVMAIL_SMTP_USER` or `DEVMAIL_SMTP_PASSWORD` is empty. A mailtrap that silently accepts anonymous mail because an env var was misspelled is the exact failure the brief is trying to prevent.

### Image

```
FROM golang:1.24-alpine AS build   # CGO_ENABLED=0, -trimpath, -ldflags="-s -w"
FROM golang:1.24-alpine AS dev     # source mounted, `go run .`
FROM alpine:3.21       AS prod     # ca-certificates, nonroot user, the binary, nothing else
```

Budget: `alpine:3.21` ≈ 8.3 MB + a static binary with go-smtp/go-message ≈ 9–11 MB → **≈ 18–20 MB**, inside the 25 MB ceiling. `./devmail.sh size` prints the built prod image size and exits non-zero above 25 MB, so the budget is enforced rather than hoped for.

## Interfaces

```go
// config.go
type Config struct {
    SMTPPort, HTTPPort           int
    SMTPUser, SMTPPassword       string
    SMTPDomain                   string
    MaxMessages, MaxRecipients   int
    MaxMessageBytes              int64
    TLSCertFile, TLSKeyFile      string
    TLSHosts                     []string
}
func Load() (*Config, error)   // reads os.Getenv, validates, never panics

// store.go
type Attachment struct {
    Filename    string `json:"filename"`
    ContentType string `json:"contentType"`
    Size        int    `json:"size"`
}
type Message struct {
    ID          string            `json:"id"`
    ReceivedAt  time.Time         `json:"receivedAt"`
    From        string            `json:"from"`        // envelope MAIL FROM
    To          []string          `json:"to"`          // envelope RCPT TO
    Subject     string            `json:"subject"`
    Date        string            `json:"date"`
    Size        int               `json:"size"`
    Headers     map[string]string `json:"headers,omitempty"`
    Text        string            `json:"text,omitempty"`
    HTML        string            `json:"html,omitempty"`
    Attachments []Attachment      `json:"attachments,omitempty"`
    ParseError  string            `json:"parseError,omitempty"`
    Raw         []byte            `json:"-"`
}
type Store struct { /* mu, msgs []*Message, nextID uint64, max int, subs … */ }
func NewStore(max int) *Store
func (s *Store) Add(m *Message)            // assigns ID, evicts oldest past max
func (s *Store) List() []*Message          // newest first, without Raw/Headers/bodies
func (s *Store) Get(id string) (*Message, bool)
func (s *Store) Delete(id string) bool
func (s *Store) Clear()

// smtp.go
func NewSMTPServer(cfg *Config, st *Store, tlsCfg *tls.Config) *smtp.Server

// tlscert.go
func TLSConfig(cfg *Config) (*tls.Config, error)

// web.go
func NewHandler(st *Store) http.Handler
```

DOM contract the specs and helpers may rely on:

| id | element |
|---|---|
| `#messageList` | `<ul>`; items `li[data-id]` containing `a[href^="/?id="]` |
| `#messageList li.active` | the selected item |
| `.msg-from`, `.msg-subject`, `.msg-date` | fields inside a list item |
| `#detail` | right pane wrapper |
| `#detailSubject`, `#detailFrom`, `#detailTo` | header fields |
| `#tabText`, `#tabHtml`, `#tabRaw` | the tab buttons |
| `#bodyText` | `<pre>` for the plain-text part |
| `#bodyHtml` | the sandboxed `<iframe>` |
| `#empty` | the "no messages" placeholder |
| `#clear` | clear-mailbox button |
| `#count` | message count |

## Work breakdown

1. **Config + store** — `config.go`, `store.go`, `config_test.go`, `store_test.go`. Depends on: nothing.
2. **SMTP server** — `smtp.go`, `tlscert.go`, `smtp_test.go`. Depends on: 1.
3. **Web API + UI** — `web.go`, `public/index.html`, `web_test.go`. Depends on: 1.
4. **Container + entrypoint** — `Dockerfile`, `docker-compose*.yml`, `devmail.sh`, `.env.example`, `.dockerignore`, `.gitignore`. Depends on: 1–3 for the port/env names only (fixed by the skeleton).
5. **E2E + CI + docs** — `tests/e2e/**`, `tests/seed/main.go`, `tests/fixtures/messages.json`, `.github/workflows/ci.yml`, `README.md`, `docs/releasing.md`. Depends on: 1–4.

## E2E acceptance

1. **Authenticated SMTPS delivery shows up in the mailbox.** A client connects to the SMTPS port over implicit TLS, authenticates with the configured credentials, and sends a multipart message. The browser shows it in `#messageList` with the right sender and subject; clicking it renders the text body in `#bodyText` and the HTML part in the sandboxed iframe.
2. **Unauthenticated and wrong-password delivery is refused, and the mailbox stays empty.** A client that connects over TLS and issues `MAIL FROM` without authenticating gets a 502/530-class rejection; a client that authenticates with a wrong password gets a 535. Neither message appears in `#messageList`.

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

1. **Mailbox HTTP auth.** Ops roast rates this CRITICAL and I agree: the brief put auth on the *write* path (SMTP) and left the *read* path — which holds every password-reset link and magic token — fully open. In a flat k8s namespace any pod can `GET /api/messages`, harvest a reset token it triggered itself, then `DELETE` the evidence. dbadmin's "no auth, warn loudly" precedent does not transfer: dbadmin's blast radius is one dev database, devmail's is any account reachable by the app under test. Options: (a) optional `DEVMAIL_HTTP_TOKEN`, off by default; (b) required token; (c) no auth + README warning, matching dbadmin. Note adding auth later is a breaking change for scripted consumers.
2. **Memory bound.** Defaults of 200 messages x 10 MiB, keeping raw *and* parsed, measured at **4 GiB live heap / 8 GiB GC target** at `GOGC=100`. A pod with a sane 512Mi limit gets OOMKilled — SIGKILL, so no shutdown path runs, the whole mailbox evaporates, and it restarts into a green `/healthz`. Anyone who can authenticate can trigger it deterministically. Options: lower defaults, store raw only (lazy parse), set `GOMEMLIMIT`, or a total-bytes cap instead of a message count.
3. **Lazy parse / store raw only.** Simplicity roast: deleting eager parse removes `Text`, `HTML`, `Attachments`, `ParseError`, `Headers` from the stored struct, halves memory, and resolves V4. Parse on `GET /api/messages/{id}` instead. (Parsing in the *browser* was considered and rejected — 150+ lines of hand-rolled JS MIME.)
4. **Scope cuts with no consumer in the design:** `DELETE /api/messages/{id}` + `Store.Delete`, the `Attachment` type + `hasAttachments`, `hasHTML`. Cut or keep?
5. **SSE vs polling.** SSE is strictly additive — the ring evicts and has no `Last-Event-ID` replay, so the client needs the full-refetch path regardless; polling needs only that path. SSE is also the sole reason `Store` carries `subs`, and fan-out under the mutex is a real wedge risk (a suspended laptop tab blocks `Add`, deadlocking SMTP). dbadmin has no push machinery.
6. **Self-signed cert regenerated per restart**, and default SANs (`localhost,devmail,127.0.0.1`) omit `devmail.<ns>.svc.cluster.local`. JVM/JavaMail clients cannot "just disable verification" — they need a truststore import, which breaks on every restart. Option: persist the generated pair to `DEVMAIL_TLS_DIR` and reuse.
7. **Credentials as plain env vars** are printed by `kubectl describe pod`, `docker inspect`, and any `%+v` of `Config`. Add `DEVMAIL_SMTP_PASSWORD_FILE` + a redacting `String()`?
8. **Ids are a per-process counter**, so after a restart `/?id=5` points at unrelated mail. Random/time-prefixed ids cost nothing now and cannot be changed later.
9. **`?id=` URL-state machinery**: dbadmin's justification was durable, shareable state. devmail's ids evict at 200 and die on restart, so bookmarking is impossible by construction — it buys only in-session Back. Keep as house style, or drop for a plain click handler?
10. **Test scope.** Only E2E criterion #1 genuinely needs a browser (the `iframe sandbox srcdoc` render path). Criterion #2's browser half asserts *that nothing rendered*, which also passes if the UI is entirely broken — it was proven as a 0.21s Go test with no container. Is ~150 MB of Chromium in `setup` and every CI run earned for one assertion?
11. **`devmail.sh` vocabulary:** `seed`/`reset`/`down --volumes` are Postgres-volume verbs with no meaning here. Drop them?
12. **Logging.** The design specifies none, and go-smtp's `ErrorLog` fires for neither auth failures, size rejections, long lines, nor EOF. Five distinct failures (wrong password, no auth, oversize, long line, ring eviction) all present as "empty mailbox, zero server output".
13. **k8s single-instance constraint** is unstated: `replicas: 2` or the default `RollingUpdate` (maxSurge 1) splits mail across pods and the UI flickers between "3 messages" and "empty".

### Roast claims logged but NOT yet verified by me

- go-message parse behaviour on malformed input / nested multiparts (correctness roast pending).
- The measured 4 GiB/8 GiB heap figure (ops roast ran it; I have not reproduced).
- `go-message/charset` costing 0.85 MB and pulling `golang.org/x/text` — the two roasts **disagree on whether to import it** (ops says import it, real mail is labelled ISO-8859-1; simplicity says skip it, your dev app will never send that). Needs a decision.
- mailpit measured at 35.3 MB uncompressed — over the ceiling, so no drop-in replacement beats the budget.

## Decisions

## Risks

- In-memory only: a container restart loses every trapped message.
- The self-signed certificate changes on every restart.
- Rendering attacker-controlled HTML is the main security surface; `sandbox=""` is the whole mitigation.
