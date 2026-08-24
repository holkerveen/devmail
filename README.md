# devmail

An SMTP server that **does not send mail**. It accepts everything over authenticated SMTPS and shows it in a plain web mailbox, so a dev stack can send real mail with real credentials and you can read it without a mail account, a relay, or a port-forward.

Runs alongside your dev setup the way [dbadmin](https://github.com/holkerveen/dbadmin) does — one container, in your namespace, no external dependencies.

> [!WARNING]
> **The mailbox is unauthenticated by default, and it holds every secret your app emails.**
> Password-reset links, magic links, signup tokens, one-time codes — all of it, in plaintext, to anything that can reach the HTTP port. Kubernetes pod networking is flat unless you have a NetworkPolicy, so by default that means *every pod in the cluster*.
>
> Set `DEVMAIL_HTTP_TOKEN` (see [HTTP access](#http-access)), publish the port to loopback only, or both. Never put this behind a public ingress without a token.
>
> Note the asymmetry: the SMTP side requires credentials, but SMTP is the *write* path — the worst an attacker does there is put mail in. The read path is where the secrets are.

## What it does

- **SMTPS on port 465** — implicit TLS, mandatory `AUTH PLAIN` / `AUTH LOGIN`. No cleartext listener exists, so there is no STARTTLS downgrade to strip.
- **Accepts every recipient**, relays nothing. There is no outbound SMTP client anywhere in the binary.
- **Web mailbox on port 80** — message list, text/HTML/raw views, attachment downloads.
- **In memory only.** Messages are lost on restart. This is a dev tool.
- **~15 MB image**, single static Go binary on Alpine.

## Quickstart

```sh
./devmail.sh setup   # copies .env, npm install, installs Playwright's browser
./devmail.sh dev     # builds and starts the stack
./devmail.sh send    # in another terminal: deliver a fixture message
```

Open <http://localhost:8080>.

## Using the published image

```yaml
services:
  devmail:
    image: ghcr.io/holkerveen/devmail:0.1
    environment:
      DEVMAIL_SMTP_USER: app
      DEVMAIL_SMTP_PASSWORD: ${MAIL_PASSWORD}
    ports:
      - '127.0.0.1:8080:80'    # mailbox -- loopback only, see the warning above
      - '127.0.0.1:465:465'    # SMTPS
```

Point your app at it:

```
MAILER_DSN=smtps://app:${MAIL_PASSWORD}@devmail:465
```

**Your app must disable certificate verification**, because the default certificate is self-signed and regenerated on every restart — `rejectUnauthorized: false` (Nodemailer), `verify_peer: false` (Symfony), `MAIL_ENCRYPTION=ssl` with verification off (Laravel). To avoid that, mount a real certificate with `DEVMAIL_TLS_CERT` / `DEVMAIL_TLS_KEY`.

### In Kubernetes

Two things that are not optional:

```yaml
spec:
  replicas: 1              # the store is in-memory and per-pod
  strategy:
    type: Recreate         # RollingUpdate transiently runs two pods even at replicas: 1
```

With two pods behind one Service, SMTP lands on pod A while your browser polls pod B, and the mailbox appears to flicker between full and empty. It reads exactly like a bug in the app under test.

Set the SANs for your namespace, since a wildcard cannot cover a k8s service FQDN (`*.svc.cluster.local` matches one label, not `devmail.myns.svc.cluster.local`):

```yaml
DEVMAIL_TLS_HOSTS: devmail.myns.svc.cluster.local,devmail.myns,devmail
```

Give it a memory limit consistent with the ring budget, and a grace period longer than `DEVMAIL_READ_TIMEOUT` so pooled idle connections don't get SIGKILLed mid-shutdown:

```yaml
terminationGracePeriodSeconds: 90
resources:
  limits:
    memory: 128Mi
```

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `DEVMAIL_SMTP_USER` | — **required** | AUTH username |
| `DEVMAIL_SMTP_PASSWORD` | — **required** | AUTH password |
| `DEVMAIL_SMTP_PASSWORD_FILE` | empty | read the password from a mounted Secret instead |
| `DEVMAIL_SMTP_PORT` | `465` | SMTPS listen port |
| `DEVMAIL_SMTP_DOMAIN` | `devmail` | greeting banner |
| `DEVMAIL_HTTP_PORT` | `80` | mailbox UI and API |
| `DEVMAIL_HTTP_TOKEN` | empty | empty = **no HTTP auth**; set = Bearer required on `/api/*` |
| `DEVMAIL_MAX_MESSAGES` | `100` | ring bound by count |
| `DEVMAIL_MAX_TOTAL_BYTES` | `16777216` | ring bound by total bytes (16 MiB) |
| `DEVMAIL_MAX_MESSAGE_BYTES` | `2097152` | per-message cap (2 MiB) |
| `DEVMAIL_MAX_LINE_BYTES` | `1048576` | max length of a single line in DATA |
| `DEVMAIL_MAX_RECIPIENTS` | `100` | per-message RCPT cap |
| `DEVMAIL_READ_TIMEOUT` | `60s` | governs the whole DATA transfer, not just one line |
| `DEVMAIL_WRITE_TIMEOUT` | `60s` | |
| `DEVMAIL_TLS_CERT` / `DEVMAIL_TLS_KEY` | empty | PEM paths; empty = ephemeral self-signed |
| `DEVMAIL_TLS_HOSTS` | `localhost,127.0.0.1,::1,devmail` | SANs for the generated certificate |

Boot fails immediately if the user or password is unset. That is deliberate: a mailtrap that silently accepts anonymous mail because an env var was misspelled is the failure this tool exists to prevent.

> [!NOTE]
> **The 2 MiB per-message default rejects a lot of ordinary invoice mail.** Base64 inflates attachments by about a third, so the effective attachment ceiling is roughly 1.5 MB; anything larger is rejected with `552`. Raise `DEVMAIL_MAX_MESSAGE_BYTES` (and `DEVMAIL_MAX_TOTAL_BYTES` with it) if your app sends PDFs.

## HTTP access

With `DEVMAIL_HTTP_TOKEN` set, `/api/*` requires `Authorization: Bearer <token>`. `/` and `/healthz` are never gated — gating `/healthz` would break the Kubernetes probe and the Docker `HEALTHCHECK`, and the page itself is an empty shell.

In a browser, open `http://host:8080/?token=<token>` once. The page moves the token into `sessionStorage` and strips it from the URL, so it leaves no history entry — but it does appear once in the access log of that first request.

Scripted use:

```sh
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/messages
```

## API

```
GET    /healthz                              status, version, message count
GET    /api/messages                         list, newest first
GET    /api/messages/{id}                    parsed: headers, text, html, attachments
GET    /api/messages/{id}?remote=1           as above, remote content not stripped
GET    /api/messages/{id}/raw                the bytes as received
GET    /api/messages/{id}/attachments/{n}    one attachment
DELETE /api/messages/{id}                    delete one
DELETE /api/messages                         clear the mailbox
```

Polling `/api/messages` for a token is the usual way to drive a signup flow from a CI test.

`/healthz` returns 503 until the SMTP listener has actually bound, so a healthy response means both listeners are up — not just the web one.

## Security notes

- **HTML bodies render in `<iframe sandbox="">`** with no allow-tokens, so a trapped email cannot run script in the mailbox origin.
- **Remote content is stripped by default.** A tracking pixel in a trapped email would otherwise fire the moment you opened the message, telling a third party the mail was read and revealing your IP — from a tool whose whole premise is that it sends nothing. "Load remote images" re-renders unstripped, per message.
- **Attachments are always served `application/octet-stream` with `nosniff`**, never the declared type, so an HTML attachment cannot execute in the mailbox origin.
- **Credentials are compared in constant time**, and the connection is dropped after three failed attempts.
- `Config`'s `String()` redacts secrets, so a stray `%+v` cannot leak the password into your log aggregator. Prefer `DEVMAIL_SMTP_PASSWORD_FILE` over the plain variable — `kubectl describe pod` prints anything passed in `env:`.

## Development

```sh
./devmail.sh check       # gofmt -l, go vet, go test
./devmail.sh size        # build the prod image, fail if over 25 MB
./devmail.sh test        # e2e against both the dev container and the prod image
./devmail.sh test:dev    # just the dev container (source mounted)
./devmail.sh test:prod   # just the prod image (no mounts -- what actually ships)
```

There is no separate `typecheck`: in Go, `go vet` already reports everything `go build` would.

Ports — dev 8080/5465, test-dev 8081/5466, test-prod 8082/5467, so all three stacks can run at once.

### Tests

Go tests cover the protocol surface a browser cannot reach cleanly: the auth gate, two messages on one pooled connection, oversize rejection, long-line acceptance, and AUTH LOGIN interop against go-sasl's own client.

Playwright specs (`tests/e2e/specs/`) cover the two user-visible behaviours: an authenticated message arriving and rendering, and rejected mail never appearing. The specs send their own mail via `go run ./tests/send` — for a mailtrap, the act of sending *is* the fixture, so there is no seeding step.

## Releasing

See [`docs/releasing.md`](docs/releasing.md). CI runs lint, tests, the size gate and the full e2e suite on every push and PR; nothing reaches the registry unless that passes.

## License

Apache-2.0.
