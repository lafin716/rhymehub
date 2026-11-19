#!/usr/bin/env bash
# Real-binary e2e scenarios A, B and C (docs/MVP_PLAN.md, validation scenarios).
#
#   A  register a sample git project, create a task through the API, let the agent run to REVIEW_READY,
#      then assert the worktree branch changed, tests passed, and the diff touches calc.go.
#   B  create an SDK key, file a request with X-Rhymehub-Key, create a task from it, run the agent to
#      REVIEW_READY, then read the request back through the SDK view.
#   C  on A's task: releaseStrategy "tag", approve with release=true, wait for COMPLETED, then assert the
#      git tag, the merge commit on the default branch, and the release listing.
#
# Agent: E2E_AGENT=claude (default) uses the real Claude Code CLI. If the claude CLI is not on PATH the
# script falls back to E2E_AGENT=fake. E2E_AGENT=fake forces the deterministic fake agent.
#
# Isolation: RHYMEHUB_HOME is a fresh temp dir (see lib.sh). ~/.rhymehub, launchctl and claude/codex
# credential stores are never touched. Secrets (admin password, SDK key) are kept in shell variables and
# files with mode 600 under the scratch dir and are never printed.
#
# Usage: scripts/e2e/scenarios-abc.sh          (E2E_KEEP=1 keeps the temp dirs)
set -uo pipefail

E2E_SCEN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Scenario ports: core 17790 (this run), supervisor and feed moved off the selfupdate defaults.
export E2E_CORE_PORT="${E2E_CORE_PORT:-17790}"
export E2E_SUPERVISOR_PORT="${E2E_SUPERVISOR_PORT:-17791}"
export E2E_FEED_PORT="${E2E_FEED_PORT:-17792}"
# shellcheck source=scripts/e2e/lib.sh
source "$E2E_SCEN_DIR/lib.sh"

SAMPLE_ID="sample-go"
INSTALLATION_ID="e2e-installation-1"
TASK_A_TIMEOUT=600
TASK_B_TIMEOUT=600
RELEASE_TIMEOUT=900

RH=""
SAMPLE=""
SDK_KEY=""
TASK_A=""
TASK_B=""
REQ_ID=""
LAST_STATE=""
START_SECONDS=$SECONDS

# --- small helpers -------------------------------------------------------------------------------

# expect_code WANT: HTTP status check that does not echo the body (for responses that carry secrets).
expect_code() {
  [[ "$E2E_HTTP_CODE" == "$1" ]] || { E2E_LAST_DETAIL="HTTP $E2E_HTTP_CODE, want $1"; return 1; }
}

# expect_http WANT: like expect_code, but includes the (truncated) response body in the failure detail.
expect_http() {
  [[ "$E2E_HTTP_CODE" == "$1" ]] || {
    E2E_LAST_DETAIL="HTTP $E2E_HTTP_CODE, want $1: $(printf '%s' "$E2E_HTTP_BODY" | head -c 300)"
    return 1
  }
}

# expect_http_in "A B": accepts any of the listed status codes.
expect_http_in() {
  case " $1 " in
    *" $E2E_HTTP_CODE "*) return 0 ;;
    *) E2E_LAST_DETAIL="HTTP $E2E_HTTP_CODE, want one of $1: $(printf '%s' "$E2E_HTTP_BODY" | head -c 300)"; return 1 ;;
  esac
}

# task_state ID: GET the task; sets LAST_STATE (empty when the request fails).
task_state() {
  e2e_core GET "/api/v1/tasks/$1"
  LAST_STATE=""
  if [[ "$E2E_HTTP_CODE" == 200 ]]; then
    LAST_STATE="$(jq -r '(.task // .).state // empty' <<<"$E2E_HTTP_BODY")"
  fi
  [[ -n "$LAST_STATE" ]]
}

# grant_permission ID: plays the human reviewer when the agent asks for a tool outside the defaults
# (WAITING_APPROVAL). Calls POST /tasks/{id}/permission {allow:true}, at most E2E_MAX_GRANTS times per task,
# and logs the tools now granted. Globally forbidden tools can never be granted (server-side check).
E2E_MAX_GRANTS="${E2E_MAX_GRANTS:-3}"
grant_permission() {
  local id="$1" var="GRANTS_${1//[^A-Za-z0-9]/_}"
  local n="${!var:-0}"
  (( n < E2E_MAX_GRANTS )) || return 1
  printf -v "$var" '%s' "$((n + 1))"
  e2e_core POST "/api/v1/tasks/$id/permission" '{"allow":true}'
  echo "INFO  reviewer granted permission #$((n + 1)) on $id: HTTP $E2E_HTTP_CODE allowedTools=$(jq -c '(.task // .).allowedTools // []' <<<"$E2E_HTTP_BODY" 2>/dev/null)" >>"$E2E_WORK/grants.log"
}

# task_settled ID WANT: true when the task reached WANT or a terminal failure state. A WAITING_APPROVAL
# task (when not the target) is approved through grant_permission and polling continues.
task_settled() {
  task_state "$1" || return 1
  if [[ "$LAST_STATE" == WAITING_APPROVAL && "$2" != WAITING_APPROVAL ]]; then
    grant_permission "$1" || return 0 # out of grants: settle so the failure is reported
    return 1
  fi
  [[ "$LAST_STATE" == "$2" || "$LAST_STATE" == FAILED || "$LAST_STATE" == CANCELLED ]]
}

# await_state ID WANT TIMEOUT: polls until the task settles. Fails with the last observed state.
await_state() {
  local id="$1" want="$2" timeout="$3"
  if e2e_wait_until "$timeout" task_settled "$id" "$want"; then
    return 0
  fi
  E2E_LAST_DETAIL="last state=${LAST_STATE:-unknown} after ${timeout}s"
  return 1
}

# expect_state WANT: checks the state from the last poll; on failure reports the task's failure reason.
expect_state() {
  if [[ "$LAST_STATE" == "$1" ]]; then
    return 0
  fi
  E2E_LAST_DETAIL="state=$LAST_STATE, want $1; failureReason=$(jq -r '(.task // .).failureReason // ""' <<<"$E2E_HTTP_BODY") error=$(jq -r '(.task // .).error // ""' <<<"$E2E_HTTP_BODY" | head -c 300)"
  return 1
}

# task_field ID FIELD WANT: GET the task and compare one field.
task_field() {
  e2e_core GET "/api/v1/tasks/$1"
  expect_http 200 || return 1
  local got
  got="$(jq -r --arg f "$2" '(.task // .)[$f] // empty' <<<"$E2E_HTTP_BODY")"
  if [[ "$got" != "$3" ]]; then
    E2E_LAST_DETAIL="$2=$got, want $3"
    return 1
  fi
}

# log_agent_usage ID LABEL prints cost and turns for a finished task (no secrets, no prompts).
log_agent_usage() {
  [[ -s "$E2E_WORK/grants.log" ]] && grep -F "$1" "$E2E_WORK/grants.log"
  e2e_core GET "/api/v1/tasks/$1"
  [[ "$E2E_HTTP_CODE" == 200 ]] || return 0
  jq -r --arg l "$2" --arg a "$E2E_AGENT" \
    '(.task // .) | "INFO  \($l): agent=\($a) state=\(.state) turns=\(.turns // "n/a") costUsd=\(.costUsd // "n/a") attempt=\(.attempt // "n/a")"' \
    <<<"$E2E_HTTP_BODY"
}

# make_sample_repo copies the fixture into a fresh git repo on branch main (one commit, with the gap).
make_sample_repo() {
  SAMPLE="$E2E_WORK/sample-go"
  rm -rf "$SAMPLE"
  cp -R "$E2E_SCEN_DIR/fixtures/sample-go" "$SAMPLE" || return 1
  (
    cd "$SAMPLE" && git init -q -b main \
      && git add -A \
      && git -c user.name=rhymehub-e2e -c user.email=e2e@example.invalid commit -q -m "sample: Add, with a deliberate Mul gap"
  )
}

# repo_has_gap: the sample repo must fail `go test` before any agent work (the gap is real).
repo_has_gap() {
  ( cd "$SAMPLE" && go test ./... >/dev/null 2>&1 ) && return 1
  return 0
}

# check_branch_changed ID: the task's branch exists in the sample repo and differs from main.
check_branch_changed() {
  e2e_core GET "/api/v1/tasks/$1"
  expect_http 200 || return 1
  local branch
  branch="$(jq -r '(.task // .).branch // empty' <<<"$E2E_HTTP_BODY")"
  if [[ -z "$branch" ]]; then
    E2E_LAST_DETAIL="task has no branch"
    return 1
  fi
  git -C "$SAMPLE" rev-parse --verify -q "$branch" >/dev/null || { E2E_LAST_DETAIL="branch $branch missing in sample repo"; return 1; }
  if [[ -z "$(git -C "$SAMPLE" diff --name-only "main...$branch")" ]]; then
    E2E_LAST_DETAIL="branch $branch has no changes against main"
    return 1
  fi
}

# check_diff ID FILE: GET /tasks/{id}/diff is non-empty and touches FILE.
check_diff() {
  e2e_core GET "/api/v1/tasks/$1/diff"
  expect_http 200 || return 1
  jq -e --arg f "$2" \
    '((.files // []) | map(tostring) | join(" ") | contains($f)) and ((.patch // "") | length > 0)' \
    <<<"$E2E_HTTP_BODY" >/dev/null || {
      E2E_LAST_DETAIL="diff does not touch $2 or patch is empty: $(printf '%s' "$E2E_HTTP_BODY" | head -c 200)"
      return 1
    }
}

# sdk_get PATH: GET an SDK endpoint with the reporter key. The key is read from a 600-mode header file.
sdk_get() {
  e2e_http GET "http://$E2E_CORE_ADDR$1" -H "@$E2E_WORK/sdk.headers"
}

# finish prints the summary line and exits non-zero on any failure.
finish() {
  local elapsed=$((SECONDS - START_SECONDS))
  printf '\nagent=%s runtime=%ss\n' "$E2E_AGENT" "$elapsed"
  printf 'PASS %d FAIL %d\n' "$E2E_PASS" "$E2E_FAIL"
  if ((E2E_FAIL > 0)); then
    exit 1
  fi
  exit 0
}

# --- main ------------------------------------------------------------------------------------------

e2e_init
trap e2e_cleanup EXIT

e2e_require_tools || finish
e2e_port_free "$E2E_CORE_PORT" || finish

# Agent selection. The real claude CLI is used when present; otherwise the deterministic fake agent.
if [[ "${E2E_AGENT:-claude}" == claude ]]; then
  if command -v claude >/dev/null 2>&1; then
    echo "agent: claude CLI $(claude --version 2>/dev/null | head -n 1)"
  else
    echo "agent: claude CLI not found on PATH; falling back to E2E_AGENT=fake"
    E2E_AGENT=fake
  fi
fi
E2E_AGENT="${E2E_AGENT:-claude}"
export E2E_AGENT
echo "scenario ports: core=$E2E_CORE_PORT supervisor=$E2E_SUPERVISOR_PORT feed=$E2E_FEED_PORT"
echo "home=$E2E_HOME (temp, isolated)"

# Prompts. The fake agent takes its instructions from a FAKE: directive; the real agent takes plain text.
if [[ "$E2E_AGENT" == fake ]]; then
  PROMPT_A='FAKE:write calc.go package calc; func Add(a, b int) int { return a + b }; func Mul(a, b int) int { return a * b }'
  PROMPT_B='FAKE:write calc.go package calc; func Add(a, b int) int { return a + b }; func Mul(a, b int) int { return a * b }; func Div(a, b int) int { return a / b }'
else
  PROMPT_A='In the Go package calc (calc.go), add Mul(a, b int) int that returns a*b, so that TestMul in calc_test.go compiles and passes. Do not modify calc_test.go and do not change Add.'
  PROMPT_B='In calc.go, add Div(a, b int) int that returns integer a / b. TestMul in calc_test.go needs Mul(a, b int) int too, so add it as well, and keep go test ./... passing. Do not modify any *_test.go file.'
fi
REQ_B_TITLE="Add Div and Mul to the calc package"
REQ_B_DESCRIPTION="$PROMPT_B"

# 1. Build the real binary and create the isolated home.
RH="$E2E_WORK/bin/rhymehub"
e2e_check "build rhymehub binary" bash -c "cd '$E2E_REPO_ROOT' && go build -o '$RH' ./apps/server/cmd/rhymehub"
((E2E_FAIL == 0)) || finish

e2e_check "rhymehub init (isolated home)" "$RH" init
e2e_check "write e2e config (ports, feed)" e2e_write_config
((E2E_FAIL == 0)) || finish

# 2. Start the core in the background. The script records its pid in core.pid so e2e_cleanup stops it.
"$RH" serve >"$E2E_WORK/serve.log" 2>&1 &
echo $! >"$E2E_HOME/core.pid"
e2e_check "core healthz answers" e2e_wait_until 30 curl -sf "http://$E2E_CORE_ADDR/healthz"
e2e_check "setup admin and login (cookie session)" e2e_core_setup_and_login
((E2E_FAIL == 0)) || finish

# 3. Sample project: a real git repo with a test command and the deliberate gap.
e2e_check "sample git repo created with the Mul gap" make_sample_repo
e2e_check "sample repo tests fail before the agent runs (gap is real)" repo_has_gap
((E2E_FAIL == 0)) || finish

project_body="$(jq -cn --arg rp "$SAMPLE" --arg agent "$E2E_AGENT" --arg id "$SAMPLE_ID" '{
  id: $id, name: "Sample Go", repoPath: $rp, defaultBranch: "main",
  testCmd: "go test ./...", releaseStrategy: "none", defaultAgent: $agent
}')"
e2e_core POST /api/v1/projects "$project_body"
e2e_check "register sample project (201)" expect_http 201
((E2E_FAIL == 0)) || finish

# --- Scenario A -----------------------------------------------------------------------------------
echo
echo "== Scenario A: task through the API =="
task_body="$(jq -cn --arg p "$PROMPT_A" --arg agent "$E2E_AGENT" '{
  projectId: "sample-go", title: "Add Mul to calc", prompt: $p, agent: $agent
}')"
e2e_core POST /api/v1/tasks "$task_body"
e2e_check "A: task created (201)" expect_http 201
TASK_A="$(jq -r '(.task // .).id // empty' <<<"$E2E_HTTP_BODY")"
e2e_check "A: task id returned" test -n "$TASK_A"
if [[ -n "$TASK_A" ]]; then
  e2e_check "A: task reaches REVIEW_READY (<=${TASK_A_TIMEOUT}s)" await_state "$TASK_A" REVIEW_READY "$TASK_A_TIMEOUT"
  e2e_check "A: final state is REVIEW_READY" expect_state REVIEW_READY
  e2e_check "A: worktree branch changed vs main" check_branch_changed "$TASK_A"
  e2e_check "A: tests passed (testStatus=passed)" task_field "$TASK_A" testStatus passed
  e2e_check "A: diff is non-empty and touches calc.go" check_diff "$TASK_A" calc.go
  log_agent_usage "$TASK_A" "A"
fi

# --- Scenario B -----------------------------------------------------------------------------------
echo
echo "== Scenario B: SDK request to task to review =="
key_body='{"name":"e2e-sdk-reporter","allowedOrigins":[]}'
e2e_core POST /api/v1/projects/sample-go/keys "$key_body"
e2e_check "B: SDK key created (201)" expect_code 201
SDK_KEY="$(jq -r '.key // empty' <<<"$E2E_HTTP_BODY")"
e2e_check "B: key has the rhk_ prefix" test "${SDK_KEY#rhk_}" != "$SDK_KEY"
# Header file with mode 600 so the key never appears on a command line.
( umask 077 && printf 'X-Rhymehub-Key: %s\nContent-Type: application/json\n' "$SDK_KEY" >"$E2E_WORK/sdk.headers" )

sdk_body="$(jq -cn --arg d "$REQ_B_DESCRIPTION" --arg t "$REQ_B_TITLE" --arg inst "$INSTALLATION_ID" '{
  type: "feature", title: $t, description: $d, installationId: $inst,
  appVersion: "e2e", platform: "e2e"
}')"
e2e_http POST "http://$E2E_CORE_ADDR/api/v1/sdk/requests" -H "@$E2E_WORK/sdk.headers" --data "$sdk_body"
e2e_check "B: SDK request accepted (201)" expect_http 201
REQ_ID="$(jq -r '.id // empty' <<<"$E2E_HTTP_BODY")"
e2e_check "B: SDK request has an id" test -n "$REQ_ID"

if [[ -n "$REQ_ID" ]]; then
  e2e_core GET "/api/v1/requests?projectId=sample-go"
  e2e_check "B: request listed in GET /api/v1/requests" bash -c '
    jq -e --arg id "$1" "(.requests // .) | map(.id) | index(\$id) != null" <<<"$2" >/dev/null
  ' _ "$REQ_ID" "$E2E_HTTP_BODY"

  e2e_core POST "/api/v1/requests/$REQ_ID/task" '{}'
  e2e_check "B: task created from request (201)" expect_http 201
  TASK_B="$(jq -r '(.task // .).id // empty' <<<"$E2E_HTTP_BODY")"
fi

if [[ -n "$TASK_B" ]]; then
  e2e_check "B: task reaches REVIEW_READY (<=${TASK_B_TIMEOUT}s)" await_state "$TASK_B" REVIEW_READY "$TASK_B_TIMEOUT"
  e2e_check "B: final state is REVIEW_READY" expect_state REVIEW_READY
  e2e_check "B: diff is non-empty and touches calc.go" check_diff "$TASK_B" calc.go
  log_agent_usage "$TASK_B" "B"
  sdk_get "/api/v1/sdk/requests/$REQ_ID?installationId=$INSTALLATION_ID"
  e2e_check "B: SDK view shows status and result" expect_http 200
  e2e_check "B: SDK view reports REVIEW_READY progress and result" bash -c '
    jq -e "(.status // \"\") != \"\" and (.progress // \"\") != \"\" and .result != null and .result.testStatus == \"passed\"" <<<"$1" >/dev/null
  ' _ "$E2E_HTTP_BODY"
  sdk_get "/api/v1/sdk/requests/$REQ_ID?installationId=someone-else"
  e2e_check "B: SDK view hides the request from another installation (404)" expect_code 404
fi

# --- Scenario C -----------------------------------------------------------------------------------
echo
echo "== Scenario C: tag release on A's task =="
if [[ -n "$TASK_A" ]]; then
  e2e_core PATCH /api/v1/projects/sample-go '{"releaseStrategy":"tag"}'
  e2e_check "C: project releaseStrategy set to tag (200)" expect_http 200

  e2e_core POST "/api/v1/tasks/$TASK_A/approve" '{"release":true}'
  e2e_check "C: approve with release=true accepted" expect_http_in "200 202"

  e2e_check "C: task reaches COMPLETED (<=${RELEASE_TIMEOUT}s)" await_state "$TASK_A" COMPLETED "$RELEASE_TIMEOUT"
  e2e_check "C: final state is COMPLETED" expect_state COMPLETED

  REL_TAG=""
  e2e_core GET "/api/v1/releases?projectId=sample-go"
  e2e_check "C: GET /api/v1/releases lists a release" bash -c '
    jq -e ".releases | length >= 1" <<<"$1" >/dev/null
  ' _ "$E2E_HTTP_BODY"
  REL_TAG="$(jq -r '.releases[0] | (.tag // ("v" + (.version // "")))' <<<"$E2E_HTTP_BODY" 2>/dev/null)"
  e2e_check "C: release has a tag name" test -n "$REL_TAG" -a "$REL_TAG" != "v"
  e2e_check "C: git tag exists in the sample repo" bash -c '
    [[ "$(git -C "$1" tag --list "$2")" == "$2" ]]
  ' _ "$SAMPLE" "$REL_TAG"
  e2e_check "C: merge commit exists on main" bash -c '
    [[ -n "$(git -C "$1" rev-list --merges --max-count=1 main)" ]]
  ' _ "$SAMPLE"
  e2e_check "C: main contains the Mul implementation" bash -c '
    git -C "$1" show main:calc.go | grep -q "func Mul"
  ' _ "$SAMPLE"
  e2e_check "C: main tests pass after merge" bash -c '
    cd "$1" && git checkout -q main && go test ./... >/dev/null 2>&1
  ' _ "$SAMPLE"
  log_agent_usage "$TASK_A" "C (A task after release)"
fi

echo
finish
