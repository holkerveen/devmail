#!/usr/bin/env bash
# devmail dev/test entrypoint. Run `./devmail.sh help` for subcommands.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

usage() {
  cat <<'EOF'
Usage: ./devmail.sh <command>

  setup       One-time dev setup: .env, npm install, Playwright's browser
  dev         Start the dev stack in the foreground
  down        Stop the dev stack
  send        Send a fixture message to the running dev stack over authenticated SMTPS
  test:dev    Run the e2e suite against the built dev container
  test:prod   Run the e2e suite against the built prod image (no mounts)
  test        Both of the above; non-zero if either fails

  Add --trace to any test command to force tracing on for every test (not
  just failures) and archive the trace output after the run, since the next
  run would otherwise wipe it.

  check       gofmt -l . && go vet ./... && go test ./...
  size        Build the prod image and fail if it exceeds 25 MB
EOF
}

require_docker_compose() {
  if ! docker compose version >/dev/null 2>&1; then
    echo "error: 'docker compose' (v2 plugin) is required but not found" >&2
    exit 1
  fi
}

wait_for_healthz() {
  local url="$1"
  local deadline=$((SECONDS + 60))
  until curl -fsS "$url" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "error: timed out waiting for $url" >&2
      exit 1
    fi
    sleep 1
  done
}

cmd_setup() {
  require_docker_compose
  if [ ! -f .env ]; then
    cp .env.example .env
    echo "Created .env from .env.example"
  fi
  npm install
  echo "Installing Playwright's Chromium OS dependencies via apt (needs root; may prompt for sudo password)..."
  npx playwright install --with-deps chromium
  echo "Setup complete. Next: ./devmail.sh dev"
}

cmd_dev() {
  require_docker_compose
  docker compose -f docker-compose.yml up --build
}

cmd_down() {
  require_docker_compose
  # No --volumes flag, unlike dbadmin: devmail has no database and no
  # volumes at all -- messages live in memory and vanish with the container.
  docker compose -f docker-compose.yml down
}

cmd_send() {
  require_docker_compose
  if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    source .env
    set +a
  fi
  SMTP_HOST=127.0.0.1 \
  SMTP_PORT="${SMTP_HOST_PORT:-5465}" \
  SMTP_USER="${DEVMAIL_SMTP_USER:?set DEVMAIL_SMTP_USER in .env (see .env.example)}" \
  SMTP_PASSWORD="${DEVMAIL_SMTP_PASSWORD:?set DEVMAIL_SMTP_PASSWORD in .env (see .env.example)}" \
    go run ./tests/send
}

# run_test_stack mirrors dbadmin's shape exactly: trap-based teardown that
# runs even if the suite fails, up -d --build, wait for /healthz, run
# Playwright with PLAYWRIGHT_BASE_URL, capture the runner's exit code, tear
# down, return that code so CI sees it.
#
# Unlike dbadmin there is no seeding step here: devmail has no database, and
# for a mailtrap the act of sending mail over SMTPS IS the fixture -- the
# specs send their own via `go run ./tests/send` (or `devmail.sh send`'s
# equivalent), so nothing needs to be pre-loaded before the suite starts.
run_test_stack() {
  local compose_file="$1" http_port="$2" smtp_port="$3" label="$4" keep_trace="$5"
  require_docker_compose

  local exit_code=0
  trap 'docker compose -f "'"$compose_file"'" down' EXIT

  docker compose -f "$compose_file" up -d --build
  wait_for_healthz "http://localhost:${http_port}/healthz"

  local trace_args=()
  if [ "$keep_trace" = "1" ]; then
    trace_args=(--trace=on)
  fi

  PLAYWRIGHT_BASE_URL="http://localhost:${http_port}" \
  SMTP_HOST=127.0.0.1 \
  SMTP_PORT="$smtp_port" \
  SMTP_USER=devmail-test \
  SMTP_PASSWORD=devmail-test \
    npx playwright test -c tests/e2e/playwright.config.js "${trace_args[@]}" || exit_code=$?

  if [ "$keep_trace" = "1" ]; then
    archive_trace "$label"
  fi

  trap - EXIT
  docker compose -f "$compose_file" down
  return "$exit_code"
}

archive_trace() {
  local label="$1"
  local src="test-results"
  if [ ! -d "$src" ]; then
    echo "no trace output found in $src" >&2
    return
  fi
  local dest="trace-archive/${label}-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$dest"
  cp -r "$src"/. "$dest"/
  local traces
  traces=$(find "$dest" -name 'trace.zip')
  if [ -z "$traces" ]; then
    echo "trace output archived to $dest (no trace.zip files found)" >&2
    return
  fi
  echo "traces archived to $dest, inspect with:"
  while IFS= read -r trace; do
    echo "  npx playwright show-trace $trace"
  done <<< "$traces"
}

cmd_test_dev() {
  local keep_trace=0
  [ "${1:-}" = "--trace" ] && keep_trace=1
  run_test_stack docker-compose.test-dev.yml 8081 5466 test-dev "$keep_trace"
}

cmd_test_prod() {
  local keep_trace=0
  [ "${1:-}" = "--trace" ] && keep_trace=1
  run_test_stack docker-compose.test-prod.yml 8082 5467 test-prod "$keep_trace"
}

cmd_test() {
  local trace_arg="${1:-}"
  local dev_exit=0 prod_exit=0
  cmd_test_dev "$trace_arg" || dev_exit=$?
  cmd_test_prod "$trace_arg" || prod_exit=$?
  if [ "$dev_exit" -ne 0 ] || [ "$prod_exit" -ne 0 ]; then
    echo "test:dev exit=$dev_exit test:prod exit=$prod_exit" >&2
    exit 1
  fi
}

# check replaces dbadmin's separate `lint` + `typecheck` with a single
# command. In Go, `go vet` already reports everything `go build` would catch
# (it can't run without the package compiling first), so a standalone
# typecheck step could never fail once vet has passed -- there is nothing
# for it to add.
cmd_check() {
  local unformatted
  unformatted="$(gofmt -l .)"
  if [ -n "$unformatted" ]; then
    echo "gofmt: the following files are not formatted:" >&2
    echo "$unformatted" >&2
    exit 1
  fi
  go vet ./...
  go test ./...
}

# size builds the prod image and enforces the 25 MB hard budget.
#
# `docker image inspect .Size` reports the image's UNCOMPRESSED on-disk
# size. A registry pull instead transfers compressed layers, which for a Go
# binary + alpine base lands at roughly HALF this number (the two commonly
# differ by ~2.2x) -- so both figures are printed below, but the budget is
# checked against the uncompressed one, matching what `docker image inspect`
# actually gives us.
cmd_size() {
  local limit=$((25 * 1024 * 1024))
  local tag="devmail:size-check"

  docker build --target prod -t "$tag" .
  local size
  size="$(docker image inspect --format '{{.Size}}' "$tag")"

  local size_mb=$((size / 1024 / 1024))
  local limit_mb=$((limit / 1024 / 1024))
  echo "prod image size: ${size} bytes uncompressed (~${size_mb} MiB); budget: ${limit} bytes (${limit_mb} MiB uncompressed)"

  if [ "$size" -gt "$limit" ]; then
    echo "error: prod image (${size} bytes) exceeds the ${limit_mb} MiB uncompressed budget" >&2
    exit 1
  fi
}

case "${1:-help}" in
  setup) cmd_setup ;;
  dev) cmd_dev ;;
  down) cmd_down ;;
  send) cmd_send ;;
  test:dev) shift; cmd_test_dev "${1:-}" ;;
  test:prod) shift; cmd_test_prod "${1:-}" ;;
  test) shift; cmd_test "${1:-}" ;;
  check) cmd_check ;;
  size) cmd_size ;;
  help|-h|--help) usage ;;
  *) echo "unknown command: $1" >&2; usage; exit 1 ;;
esac
