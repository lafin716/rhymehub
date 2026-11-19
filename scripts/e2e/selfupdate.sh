#!/usr/bin/env bash
# E2E scenarios D (self-update succeeds) and E (bad updates are rejected or rolled back) against the
# real core and supervisor binaries built by scripts/build-release.sh. Every run uses a fresh
# RHYMEHUB_HOME, so ~/.rhymehub is never read or written and launchctl is never called.
# Owner: E2E-2a. Run: bash scripts/e2e/selfupdate.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

trap e2e_cleanup EXIT

START=$SECONDS
V_BASE="0.0.1-e2e"
V_NEW="0.0.2-e2e"
V_E1="0.0.3-e2e" # broken build: serve exits at startup
V_E2="0.0.4-e2e" # broken build: serve never listens (health check fails)
V_E3="0.0.5-e2e" # artifact corrupted in the feed after signing
V_E4="0.0.6-e2e" # signature invalid in the feed manifest
V_E5="0.0.7-e2e" # broken build: migrate exits 3
E2E_PROJECT="e2e-project"

# fatal runs a setup step through e2e_check and stops the script when it fails.
fatal() {
  e2e_check "setup: $1" "${@:2}"
  if ((E2E_FAIL > 0)); then
    echo "setup failed; stopping" >&2
    exit 1
  fi
}

# --- assertion helpers (each runs through e2e_check) ---

code_is() { [[ "$E2E_HTTP_CODE" == "$1" ]]; }

healthz_is() { [[ "$(e2e_healthz_version)" == "$1" ]]; }

current_is() {
  local target
  target="$(readlink "$E2E_HOME/current" 2>/dev/null || true)"
  [[ "$target" == *"versions/$1" ]]
}

# rec_is VERSION STATUS: the last updates.jsonl record for VERSION has the given status.
rec_is() {
  local rec
  rec="$(e2e_last_record_for "$1")"
  if [[ -z "$rec" ]]; then
    E2E_LAST_DETAIL="no updates.jsonl record with to=$1"
    return 1
  fi
  if ! printf '%s' "$rec" | jq -e --arg s "$2" '.status == $s' >/dev/null; then
    E2E_LAST_DETAIL="expected status $2; record: $rec"
    return 1
  fi
}

# rec_from_is VERSION FROM: the record for VERSION was an update from FROM.
rec_from_is() {
  local rec
  rec="$(e2e_last_record_for "$1")"
  printf '%s' "$rec" | jq -e --arg f "$2" '.from == $f' >/dev/null
}

rec_has_error() {
  local rec
  rec="$(e2e_last_record_for "$1")"
  printf '%s' "$rec" | jq -e '(.error // "") | length > 0' >/dev/null
}

admin_login_ok() {
  local body
  body="$(jq -cn --arg u "$E2E_ADMIN_USER" --arg p "$E2E_ADMIN_PASS" '{username:$u,password:$p}')"
  e2e_core POST /api/v1/auth/login "$body"
  code_is 200
}

project_present() {
  e2e_core GET /api/v1/projects
  if [[ "$E2E_HTTP_CODE" != 200 ]]; then
    E2E_LAST_DETAIL="GET /api/v1/projects returned HTTP $E2E_HTTP_CODE"
    return 1
  fi
  printf '%s' "$E2E_HTTP_BODY" | jq -e --arg n "$E2E_PROJECT" \
    '[.. | objects | select(.name == $n)] | length > 0' >/dev/null
}

# stream_ok_at_least COUNT TIMEOUT waits until the SSE stream has answered 200 at least COUNT times.
stream_ok_at_least() {
  local want="$1" timeout="$2" got=0
  local deadline=$((SECONDS + timeout))
  while ((SECONDS < deadline)); do
    got="$(e2e_stream_ok_count)"
    if ((got >= want)); then
      return 0
    fi
    sleep 0.5
  done
  E2E_LAST_DETAIL="stream ok connections: $got (wanted >= $want)"
  return 1
}

# sup_update VERSION posts /update to the supervisor. The supervisor runs the update synchronously,
# so this waits for the outcome. It sets E2E_HTTP_CODE and E2E_HTTP_BODY and is not a check itself.
sup_update() {
  local v="$1" body
  body="$(jq -cn --arg v "$v" '{version:$v}')"
  E2E_HTTP_CODE="$(curl -sS -m 900 -X POST -o "$E2E_WORK/update-$v.body" -w '%{http_code}' \
    -H "Authorization: Bearer $(e2e_sup_token)" -H 'Content-Type: application/json' \
    --data "$body" "http://$E2E_SUPERVISOR_ADDR/update" 2>"$E2E_WORK/update-$v.err")" || E2E_HTTP_CODE="000"
  E2E_HTTP_BODY="$(cat "$E2E_WORK/update-$v.body" 2>/dev/null || true)"
}

# reject_case TAG VERSION STATUS CODE checks an update that must not leave the core on VERSION.
# The caller has already published VERSION. The core must keep serving $V_NEW, the previous good version.
reject_case() {
  local tag="$1" v="$2" status="$3" code="$4"
  sup_update "$v"
  e2e_check "$tag POST /update $v returns HTTP $code" code_is "$code"
  e2e_check "$tag updates.jsonl records $v as $status" rec_is "$v" "$status"
  e2e_check "$tag the record carries an error" rec_has_error "$v"
  e2e_check "$tag core still serving $V_NEW (healthz)" e2e_wait_version "$V_NEW" 120
  e2e_check "$tag current still points at $V_NEW" current_is "$V_NEW"
}

# --- release preparation ---

# prep_release VERSION builds a real release and publishes it, signed, in the feed.
prep_release() {
  local tarball
  tarball="$(e2e_build_release "$1")" || return 1
  e2e_sign_release "$1" "$tarball"
}

# prep_broken VERSION MODE builds VERSION, repacks it with a broken serve or migrate, and publishes
# the broken build, signed, in the feed.
prep_broken() {
  local v="$1" mode="$2" tarball dir out
  tarball="$(e2e_build_release "$v")" || return 1
  dir="$E2E_WORK/broken/$v-$mode"
  mkdir -p "$dir"
  out="$dir/rhymehub-$v-$E2E_GOOS-$E2E_GOARCH.tar.gz"
  e2e_repack_broken "$tarball" "$v" "$mode" "$out" || return 1
  e2e_sign_release "$v" "$out"
}

create_project() {
  local repo="$E2E_WORK/e2e-repo" body
  rm -rf "$repo"
  mkdir -p "$repo"
  git -C "$repo" init -q || return 1
  git -C "$repo" -c user.name=e2e -c user.email=e2e@example.invalid commit -q --allow-empty -m init || return 1
  body="$(jq -cn --arg n "$E2E_PROJECT" --arg p "$repo" '{name:$n, repoPath:$p}')"
  e2e_core POST /api/v1/projects "$body"
  code_is 200 || code_is 201
}

# --- run ---

echo "selfupdate e2e: scenario D (update succeeds) and scenario E (bad updates rejected or rolled back)"

e2e_require_tools || exit 1
for port in "$E2E_CORE_PORT" "$E2E_SUPERVISOR_PORT" "$E2E_FEED_PORT"; do
  e2e_port_free "$port" || exit 1
done
e2e_init

# Build the base release (the first install) with build-release.sh. Build failures stop the run.
BASE_TAR="$(e2e_build_release "$V_BASE")" || { echo "build $V_BASE failed" >&2; exit 1; }

# Install the base version. The supervisor binary comes from the same tarball, copied out so the
# harness signs and runs the supervisor that matches the core.
e2e_extract_to "$E2E_HOME/versions/$V_BASE" "$BASE_TAR"
E2E_SUP="$E2E_WORK/bin/rhymehub-supervisor"
cp "$E2E_HOME/versions/$V_BASE/bin/rhymehub-supervisor" "$E2E_SUP" || exit 1
chmod 755 "$E2E_SUP"

fatal "bootstrap the home layout" "$E2E_SUP" bootstrap --home "$E2E_HOME"
fatal "generate the release signing key" "$E2E_SUP" keygen --home "$E2E_HOME"
fatal "point current at $V_BASE" ln -sfn "versions/$V_BASE" "$E2E_HOME/current"
fatal "write config (core, supervisor, feed addresses)" e2e_write_config

e2e_start_feed
fatal "feed server listens on $E2E_FEED_ADDR" nc -z 127.0.0.1 "$E2E_FEED_PORT"
fatal "sign and publish $V_BASE in the feed" e2e_sign_release "$V_BASE" "$BASE_TAR"

e2e_start_supervisor
fatal "supervisor API answers on $E2E_SUPERVISOR_ADDR" e2e_wait_until 20 e2e_sup_ready
fatal "core comes up on $V_BASE (healthz)" e2e_wait_version "$V_BASE" 60
fatal "setup and login as the admin" e2e_core_setup_and_login
fatal "create a project" create_project

e2e_start_stream
fatal "SSE stream connected" stream_ok_at_least 1 20

echo "== D: self-update $V_BASE -> $V_NEW"
fatal "build and publish $V_NEW in the feed" prep_release "$V_NEW"
sup_update "$V_NEW"
e2e_check "D POST /update $V_NEW returns HTTP 200" code_is 200
e2e_check "D core healthz reports $V_NEW" e2e_wait_version "$V_NEW" 120
e2e_check "D updates.jsonl records $V_BASE -> $V_NEW as succeeded" rec_is "$V_NEW" succeeded
e2e_check "D updates.jsonl record is from $V_BASE" rec_from_is "$V_NEW" "$V_BASE"
e2e_check "D SSE stream reconnected (>= 2 ok connections)" stream_ok_at_least 2 30
e2e_check "D current points at $V_NEW" current_is "$V_NEW"
e2e_check "D admin user survived the update (login)" admin_login_ok
e2e_check "D project survived the update" project_present

echo "== E1: broken build exits on serve"
fatal "build and publish broken $V_E1 (serve exits)" prep_broken "$V_E1" exit
reject_case E1 "$V_E1" rolled_back 409

echo "== E2: broken build hangs on serve (health check fails)"
fatal "build and publish broken $V_E2 (serve hangs)" prep_broken "$V_E2" hang
reject_case E2 "$V_E2" rolled_back 409

echo "== E3: artifact corrupted in the feed (rejected before switch)"
fatal "build and publish $V_E3" prep_release "$V_E3"
fatal "corrupt the feed copy of $V_E3" e2e_tamper_feed_artifact "$V_E3"
reject_case E3 "$V_E3" failed 500

echo "== E4: bad signature (rejected before switch)"
fatal "build and publish $V_E4" prep_release "$V_E4"
fatal "replace the signature of $V_E4 in the manifest" e2e_tamper_signature "$V_E4"
reject_case E4 "$V_E4" failed 500

echo "== E5: migration failure (migrate exits 3)"
fatal "build and publish broken $V_E5 (migrate exits 3)" prep_broken "$V_E5" migrate
reject_case E5 "$V_E5" rolled_back 409

echo
echo "PASS $E2E_PASS FAIL $E2E_FAIL"
echo "runtime: $((SECONDS - START))s"
if ((E2E_FAIL > 0)); then
  exit 1
fi
exit 0
