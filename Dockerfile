# syntax=docker/dockerfile:1
#
# devmail is a single static Go binary with no runtime dependencies (no DB,
# no volume -- messages live in memory). That shapes every stage below:
# the prod image is FROM alpine only to get a shell for wget's healthcheck
# and a place to put the binary, not because devmail needs a distro.

# ---- build: compiles the static binary used by the prod stage -------------
FROM golang:1.24-alpine AS build
WORKDIR /app

# Separate `go mod download` layer, cached by go.sum, so editing a .go file
# doesn't invalidate the module download. Cache mounts here are safe to use
# freely: this stage's only output is the /out/devmail binary copied into
# prod, so nothing about the cache mount's contents needs to survive into
# any image layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/devmail .

# ---- dev: source bind-mounted by compose, runs `go run .` -----------------
# There is no live-reload tool wired up (no air/CompileDaemon). The edit loop
# is: edit a .go file, then `docker compose restart app`.
FROM golang:1.24-alpine AS dev
WORKDIR /app

# Unlike the `build` stage, this download must NOT use a cache mount: cache
# mounts are ephemeral and never land in the final image layer. `go run .`
# at container start needs the modules already sitting in this layer's
# /go/pkg/mod, or every `docker compose up`/`restart` would re-hit the
# network. A plain RUN bakes them in.
COPY go.mod go.sum ./
RUN go mod download
COPY . .

EXPOSE 80 465
HEALTHCHECK --interval=3s --timeout=2s --start-period=5s --retries=30 \
  CMD wget -qO- "http://127.0.0.1:${DEVMAIL_HTTP_PORT:-80}/healthz" || exit 1
CMD ["go", "run", "."]

# ---- prod: just the binary, non-root, under a 25 MB hard budget -----------
FROM alpine:3.21 AS prod

# No ca-certificates: this binary makes no outbound TLS connections (it only
# terminates inbound SMTPS with its own cert), so a CA bundle would never be
# read -- it would be pure image weight against the size budget.
RUN adduser -D -H -u 10001 devmail
COPY --from=build /out/devmail /usr/local/bin/devmail
USER devmail

EXPOSE 80 465
# alpine's busybox already provides wget, so this needs no extra package --
# same trick dbadmin's Dockerfile uses.
HEALTHCHECK --interval=3s --timeout=2s --start-period=5s --retries=30 \
  CMD wget -qO- "http://127.0.0.1:${DEVMAIL_HTTP_PORT:-80}/healthz" || exit 1
ENTRYPOINT ["/usr/local/bin/devmail"]
