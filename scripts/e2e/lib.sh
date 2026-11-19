#!/usr/bin/env bash
# Shared helpers for the real-binary e2e harness. Source this file; do not execute it.
# Owner: E2E-2 (scripts/e2e/lib.sh). Used by scripts/e2e/selfupdate.sh.
#
# Isolation rules enforced here:
#   - RHYMEHUB_HOME is a fresh mktemp directory; ~/.rhymehub is never read or written.
#   - launchctl is never called.
#   - Ports come from the environment (defaults 17780 core, 17781 supervisor, 17782 feed).
#   - Every process started by the harness is tracked and killed by e2e_cleanup.

E2E_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
E2E_REPO_ROOT="$(cd "$E2E_LIB_DIR/../.." && pwd)"

E2E_CORE_PORT="${E2E_CORE_PORT:-17780}"
E2E_SUPERVISOR_PORT="${E2E_SUPERVISOR_PORT:-17781}"
E2E_FEED_PORT="${E2E_FEED_PORT:-17782}"
E2E_CORE_ADDR="127.0.0.1:${E2E_CORE_PORT}"
E2E_SUPERVISOR_ADDR="127.0.0.1:${E2E_SUPERVISOR_PORT}"
E2E_FEED_ADDR="127.0.0.1:${E2E_FEED_PORT}"

E2E_HOME=""            # isolated RHYMEHUB_HOME
E2E_WORK=""            # scratch: builds, feed, logs, cookie jar, bodies
E2E_SUP=""             # supervisor binary used by the harness (copied into E2E_WORK/bin)
E2E_GOOS=""
E2E_GOARCH=""
E2E_PASS=0
E2E_FAIL=0
E2E_SUP_PID=""
E2E_FEED_PID=""
E2E_STREAM_PID=""
E2E_HTTP_CODE=""
E2E_HTTP_BODY=""
E2E_LAST_DETAIL=""
E2E_ADMIN_USER=""
E2E_ADMIN_PASS=""

# e2e_require_tools fails fast when a dependency is missing.
e2e_require_tools() {
  local t missing=0
  for t in curl jq python3 nc tar go; do
    if ! command -v "$t" >/dev/null 2>&1; then
      echo "missing required tool: $t" >&2
      missing=1
    fi
  done
  if command -v shasum >/dev/null 2>&1 || command -v sha256sum >/dev/null 2>&1; then :; else
    echo "missing required tool: shasum or sha256sum" >&2
    missing=1
  fi
  return "$missing"
}

# e2e_port_free fails when something already listens on 127.0.0.1:PORT.
e2e_port_free() {
  local port="$1"
  if nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
    echo "port $port is already in use on 127.0.0.1; set E2E_*_PORT to free ports" >&2
    return 1
  fi
  return 0
}

# e2e_init creates the isolated home and scratch dirs and records the Go platform used for artifacts.
e2e_init() {
  local tmp="${TMPDIR:-/tmp}"
  E2E_WORK="$(mktemp -d "${tmp%/}/rhymehub-e2e-work.XXXXXX")"
  E2E_HOME="$(mktemp -d "${tmp%/}/rhymehub-e2e-home.XXXXXX")"
  case "$E2E_HOME" in
    "$HOME/.rhymehub"|"$HOME/.rhymehub"/*)
      echo "refusing to use $E2E_HOME: it is inside the real ~/.rhymehub" >&2
      exit 1 ;;
  esac
  mkdir -p "$E2E_WORK/feed/artifacts" "$E2E_WORK/bin" "$E2E_WORK/dist"
  E2E_GOOS="$(go env GOOS)"
  E2E_GOARCH="$(go env GOARCH)"
  export RHYMEHUB_HOME="$E2E_HOME"
}

# e2e_sha256 prints the sha256 of a file.
e2e_sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

# e2e_pass / e2e_fail_line print one result line and update counters.
e2e_pass_line() {
  E2E_PASS=$((E2E_PASS + 1))
  printf 'PASS  %s\n' "$1"
}

e2e_fail_line() {
  E2E_FAIL=$((E2E_FAIL + 1))
  printf 'FAIL  %s\n' "$1"
  if [[ -n "$E2E_LAST_DETAIL" ]]; then
    printf '      %s\n' "$E2E_LAST_DETAIL"
  fi
}

# e2e_check LABEL COMMAND... runs COMMAND in the current shell (so helpers can set E2E_HTTP_* and
# E2E_LAST_DETAIL). PASS when it returns 0. Its stdout/stderr is shown on FAIL.
e2e_check() {
  local label="$1"
  shift
  E2E_LAST_DETAIL=""
  local out="$E2E_WORK/check.out"
  if "$@" >"$out" 2>&1; then
    e2e_pass_line "$label"
  else
    if [[ -s "$out" ]]; then
      E2E_LAST_DETAIL="${E2E_LAST_DETAIL:+$E2E_LAST_DETAIL; }$(tail -n 5 "$out" | tr '\n' ' ')"
    fi
    e2e_fail_line "$label"
  fi
}

# e2e_wait_until SECONDS COMMAND... polls until COMMAND succeeds or the timeout passes.
e2e_wait_until() {
  local timeout="$1"
  shift
  local deadline=$((SECONDS + timeout))
  while ((SECONDS < deadline)); do
    if "$@" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  return 1
}

# e2e_http METHOD URL [curl args...] sets E2E_HTTP_CODE (000 on transport error) and E2E_HTTP_BODY.
e2e_http() {
  local method="$1" url="$2"
  shift 2
  local code
  code="$(curl -sS -m 30 -X "$method" -o "$E2E_WORK/http.body" -w '%{http_code}' "$@" "$url" 2>"$E2E_WORK/http.err")" || code="000"
  E2E_HTTP_CODE="$code"
  E2E_HTTP_BODY="$(cat "$E2E_WORK/http.body" 2>/dev/null || true)"
}

# --- supervisor API (127.0.0.1:E2E_SUPERVISOR_PORT, bearer token from the home) ---

e2e_sup_token() {
  tr -d '[:space:]' <"$E2E_HOME/secrets/supervisor.token"
}

# e2e_sup METHOD PATH [json] calls the supervisor loopback API.
e2e_sup() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-H "Authorization: Bearer $(e2e_sup_token)")
  if [[ -n "$body" ]]; then
    args+=(-H 'Content-Type: application/json' --data "$body")
  fi
  e2e_http "$method" "http://$E2E_SUPERVISOR_ADDR$path" "${args[@]}"
}

e2e_sup_ready() {
  e2e_sup GET /status
  [[ "$E2E_HTTP_CODE" == 200 ]]
}

# --- core API (127.0.0.1:E2E_CORE_PORT, cookie session + CSRF header) ---

# e2e_core METHOD PATH [json] calls the core with the admin session cookie.
e2e_core() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-b "$E2E_WORK/cookies.txt" -c "$E2E_WORK/cookies.txt" -H 'X-Rhymehub-CSRF: 1')
  if [[ -n "$body" ]]; then
    args+=(-H 'Content-Type: application/json' --data "$body")
  fi
  e2e_http "$method" "http://$E2E_CORE_ADDR$path" "${args[@]}"
}

# e2e_core_setup_and_login creates the admin on a fresh home (setup) and logs in for a cookie.
# Credentials are random per run, kept in shell variables only, and never written to logs.
e2e_core_setup_and_login() {
  E2E_ADMIN_USER="e2e-admin"
  E2E_ADMIN_PASS="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 24)"
  local body
  body="$(jq -cn --arg u "$E2E_ADMIN_USER" --arg p "$E2E_ADMIN_PASS" '{username:$u,password:$p}')"
  e2e_core POST /api/v1/setup "$body"
  if [[ "$E2E_HTTP_CODE" != 200 && "$E2E_HTTP_CODE" != 201 ]]; then
    E2E_LAST_DETAIL="setup returned HTTP $E2E_HTTP_CODE: $E2E_HTTP_BODY"
    return 1
  fi
  e2e_core POST /api/v1/auth/login "$body"
  if [[ "$E2E_HTTP_CODE" != 200 ]]; then
    E2E_LAST_DETAIL="login returned HTTP $E2E_HTTP_CODE: $E2E_HTTP_BODY"
    return 1
  fi
  if ! grep -q 'rh_session' "$E2E_WORK/cookies.txt" 2>/dev/null; then
    E2E_LAST_DETAIL="login did not set the rh_session cookie"
    return 1
  fi
  return 0
}

# e2e_healthz_version prints the version reported by /healthz, or nothing.
e2e_healthz_version() {
  e2e_http GET "http://$E2E_CORE_ADDR/healthz"
  if [[ "$E2E_HTTP_CODE" == 200 ]]; then
    printf '%s' "$E2E_HTTP_BODY" | jq -r '.version // empty' 2>/dev/null
  fi
}

# e2e_wait_version VERSION TIMEOUT waits until /healthz reports VERSION.
e2e_wait_version() {
  local want="$1" timeout="${2:-60}" got=""
  local deadline=$((SECONDS + timeout))
  while ((SECONDS < deadline)); do
    got="$(e2e_healthz_version)"
    if [[ "$got" == "$want" ]]; then
      return 0
    fi
    sleep 0.5
  done
  E2E_LAST_DETAIL="healthz did not report version $want within ${timeout}s (last version: '${got:-none}')"
  return 1
}

# e2e_core_pid prints the pid recorded in core.pid, or nothing.
e2e_core_pid() {
  tr -d '[:space:]' <"$E2E_HOME/core.pid" 2>/dev/null || true
}

# e2e_pid_alive PID.
e2e_pid_alive() {
  [[ -n "$1" ]] && kill -0 "$1" 2>/dev/null
}

# e2e_last_record_for VERSION prints the last updates.jsonl record whose "to" is VERSION.
e2e_last_record_for() {
  jq -cs --arg v "$1" 'map(select(.to == $v)) | last // empty' "$E2E_HOME/updates.jsonl" 2>/dev/null
}

# e2e_wait_record VERSION TIMEOUT waits for a terminal record for VERSION in updates.jsonl.
e2e_wait_record() {
  local version="$1" timeout="${2:-180}"
  local deadline=$((SECONDS + timeout)) rec=""
  while ((SECONDS < deadline)); do
    rec="$(e2e_last_record_for "$version")"
    if [[ -n "$rec" ]]; then
      E2E_HTTP_BODY="$rec"
      return 0
    fi
    sleep 0.5
  done
  E2E_LAST_DETAIL="no update record for $version in updates.jsonl within ${timeout}s"
  return 1
}

# --- release artifacts ---

# e2e_build_release VERSION builds through scripts/build-release.sh and prints the tarball path.
e2e_build_release() {
  local version="$1"
  local outdir="$E2E_WORK/dist/$version" lines
  mkdir -p "$outdir"
  lines="$("$E2E_REPO_ROOT/scripts/build-release.sh" "$version" "$outdir")" || return 1
  printf '%s\n' "$lines" | sed -n '1p'
}

# e2e_extract_version VERSION TARBALL unpacks a tarball into a directory under E2E_WORK/unpacked.
e2e_extract_to() {
  local dest="$1" tarball="$2"
  mkdir -p "$dest"
  tar -xzf "$tarball" -C "$dest"
}

# e2e_install_version VERSION TARBALL places the core under versions/<VERSION> in the home.
e2e_install_version() {
  e2e_extract_to "$E2E_HOME/versions/$1" "$2"
}

# e2e_repack_broken SRC_TARBALL VERSION MODE OUT_TARBALL builds a broken core from a real tarball.
# The shell stub keeps `version` and `migrate` working (so the supervisor's pre-switch checks pass)
# and replaces `serve`:
#   exit  serve exits 1 immediately
#   hang  serve never listens (sleeps), so the health check times out
e2e_repack_broken() {
  local src="$1" version="$2" mode="$3" out="$4"
  local stage="$E2E_WORK/broken-$version-$mode" serve_line
  rm -rf "$stage"
  mkdir -p "$stage"
  tar -xzf "$src" -C "$stage" || return 1
  mv "$stage/bin/rhymehub" "$stage/bin/rhymehub.real" || return 1
  case "$mode" in
    exit) serve_line="echo \"rhymehub $version: serve failed at startup (e2e broken build)\" >&2; exit 1" ;;
    hang) serve_line='exec sleep 3600' ;;
    *) echo "unknown broken mode: $mode" >&2; return 1 ;;
  esac
  cat >"$stage/bin/rhymehub" <<EOF
#!/bin/sh
# E2E broken build ($mode). Only serve is replaced; everything else runs the real binary.
case "\$1" in
  serve) $serve_line ;;
  *) exec "\$(dirname "\$0")/rhymehub.real" "\$@" ;;
esac
EOF
  chmod 755 "$stage/bin/rhymehub"
  COPYFILE_DISABLE=1 tar -C "$stage" -czf "$out" bin VERSION
}

# e2e_sign_release VERSION TARBALL signs TARBALL with the home key, copies it into the feed, and
# records it in the feed manifest.
e2e_sign_release() {
  local version="$1" tarball="$2" name
  name="$(basename "$tarball")"
  cp "$tarball" "$E2E_WORK/feed/artifacts/$name"
  "$E2E_SUP" sign-release --home "$E2E_HOME" --version "$version" --artifact "$tarball" \
    --os "$E2E_GOOS" --arch "$E2E_GOARCH" \
    --url "http://$E2E_FEED_ADDR/artifacts/$name" \
    --manifest "$E2E_WORK/feed/manifest.json" --notes "e2e $version" \
    >"$E2E_WORK/sign-$version.log" 2>&1
}

# e2e_tamper_feed_artifact VERSION appends one byte to the published copy so its sha256 no longer
# matches the manifest.
e2e_tamper_feed_artifact() {
  local name="rhymehub-$1-$E2E_GOOS-$E2E_GOARCH.tar.gz"
  printf 'x' >>"$E2E_WORK/feed/artifacts/$name"
}

# e2e_tamper_signature VERSION changes the first character of the artifact signature in the manifest.
e2e_tamper_signature() {
  local tmp="$E2E_WORK/manifest.tmp"
  jq --arg v "$1" \
    '(.releases[] | select(.version == $v) | .artifacts[].signature) |= ("A" + .[1:])' \
    "$E2E_WORK/feed/manifest.json" >"$tmp" && mv "$tmp" "$E2E_WORK/feed/manifest.json"
}

# --- feed server ---

# e2e_start_feed serves E2E_WORK/feed on 127.0.0.1:E2E_FEED_PORT with python's http.server (isolated mode).
e2e_start_feed() {
  python3 -I -m http.server "$E2E_FEED_PORT" --bind 127.0.0.1 --directory "$E2E_WORK/feed" \
    >"$E2E_WORK/feed.log" 2>&1 &
  E2E_FEED_PID=$!
  e2e_wait_until 10 nc -z 127.0.0.1 "$E2E_FEED_PORT"
}

# --- config and supervisor ---

# e2e_write_config points the home at this run's core port and feed.
e2e_write_config() {
  local tmp="$E2E_WORK/config.tmp"
  jq --arg listen "$E2E_CORE_ADDR" --arg feed "http://$E2E_FEED_ADDR/manifest.json" \
    '.listen = $listen | .updateFeed = $feed' "$E2E_HOME/config.json" >"$tmp" && mv "$tmp" "$E2E_HOME/config.json"
}

# e2e_start_supervisor runs the supervisor in the foreground-mode process in the background; it
# launches the core from the current symlink.
e2e_start_supervisor() {
  "$E2E_SUP" run --home "$E2E_HOME" --addr "$E2E_SUPERVISOR_ADDR" \
    >"$E2E_WORK/supervisor.log" 2>&1 &
  E2E_SUP_PID=$!
}

# --- SSE stream with reconnect ---

# e2e_start_stream keeps GET /api/v1/events/stream open. Each time curl exits (for example when the
# core restarts) it records the HTTP status line and reconnects.
e2e_start_stream() {
  local log="$E2E_WORK/stream.log" jar="$E2E_WORK/cookies.txt" pidf="$E2E_WORK/stream.curl.pid"
  : >"$log"
  (
    while :; do
      local hdr="$E2E_WORK/stream.hdr"
      : >"$hdr"
      curl -sN -D "$hdr" -b "$jar" -H 'Accept: text/event-stream' -o /dev/null \
        "http://$E2E_CORE_ADDR/api/v1/events/stream" &
      echo $! >"$pidf"
      wait $! 2>/dev/null
      printf '%s status=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(head -n 1 "$hdr" 2>/dev/null | tr -d '\r')" >>"$log"
      sleep 0.5
    done
  ) &
  E2E_STREAM_PID=$!
}

# e2e_stream_ok_count counts connections that answered 200 (HTTP/1.0 or HTTP/1.1).
e2e_stream_ok_count() {
  local n
  n="$(grep -c 'status=HTTP/1\.[01] 200' "$E2E_WORK/stream.log" 2>/dev/null || true)"
  printf '%s' "${n:-0}"
}

# --- cleanup ---

# e2e_kill_pid TERM, then KILL after 5s, for one pid.
e2e_kill_pid() {
  local pid="$1" i
  [[ -n "$pid" ]] || return 0
  kill -0 "$pid" 2>/dev/null || return 0
  kill -TERM "$pid" 2>/dev/null || true
  for i in $(seq 1 50); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -KILL "$pid" 2>/dev/null || true
}

# e2e_cleanup stops every process the harness started, then removes the temp dirs (unless E2E_KEEP=1).
# Order matters: stop the stream and the supervisor first so the supervisor does not relaunch the core
# while it is being stopped.
e2e_cleanup() {
  trap - EXIT INT TERM
  local curl_pid=""
  if [[ -n "$E2E_WORK" && -f "$E2E_WORK/stream.curl.pid" ]]; then
    curl_pid="$(cat "$E2E_WORK/stream.curl.pid" 2>/dev/null || true)"
  fi
  e2e_kill_pid "$curl_pid"
  e2e_kill_pid "$E2E_STREAM_PID"
  e2e_kill_pid "$E2E_SUP_PID"
  if [[ -n "$E2E_HOME" ]]; then
    e2e_kill_pid "$(e2e_core_pid)"
  fi
  e2e_kill_pid "$E2E_FEED_PID"
  if [[ "${E2E_KEEP:-0}" == 1 ]]; then
    echo "kept: home=$E2E_HOME work=$E2E_WORK" >&2
  else
    [[ -n "$E2E_WORK" ]] && rm -rf "$E2E_WORK"
    [[ -n "$E2E_HOME" ]] && rm -rf "$E2E_HOME"
  fi
}
