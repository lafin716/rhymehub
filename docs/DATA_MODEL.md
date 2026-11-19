# Data Model

SQLite (WAL, `busy_timeout=5000`, `foreign_keys=ON`). Migrations are numbered SQL files embedded in
`internal/store/migrations/NNNN_name.sql`, recorded in `schema_migrations`. `GET /healthz` reports the schema
version. Timestamps are RFC3339 UTC text. IDs are prefixed sortable random strings
(`domain.NewID("tsk")` → `tsk_01JD...` – 26 char ULID-like base32 time+random).

## Schema (migration 0001_init)

```sql
CREATE TABLE users (
  id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('reporter','developer','maintainer','admin')),
  disabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL);

CREATE TABLE sessions (          -- browser sessions; token stored as sha256 hex
  token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL);

CREATE TABLE api_tokens (        -- CLI/automation tokens: "rht_<random>"
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT);

CREATE TABLE projects (
  id TEXT PRIMARY KEY,                 -- slug, e.g. "rhymehub", "droidpad"
  name TEXT NOT NULL, repo_path TEXT NOT NULL, git_url TEXT, default_branch TEXT NOT NULL DEFAULT 'main',
  type TEXT NOT NULL DEFAULT 'generic',  -- generic|web|electron|android|spring|rhymehub
  stack TEXT NOT NULL DEFAULT '',
  build_cmd TEXT NOT NULL DEFAULT '', test_cmd TEXT NOT NULL DEFAULT '',
  release_strategy TEXT NOT NULL DEFAULT 'none',  -- none|tag|command|rhymehub-self
  release_cmd TEXT NOT NULL DEFAULT '',
  default_agent TEXT NOT NULL DEFAULT 'claude', default_model TEXT NOT NULL DEFAULT '',
  max_concurrent INTEGER NOT NULL DEFAULT 1, timeout_minutes INTEGER NOT NULL DEFAULT 30,
  auto_task_from_requests INTEGER NOT NULL DEFAULT 1,
  auto_approve INTEGER NOT NULL DEFAULT 0, auto_release INTEGER NOT NULL DEFAULT 0,
  allowed_tools TEXT NOT NULL DEFAULT '[]',         -- JSON array of extra agent tool permissions
  sdk_status TEXT NOT NULL DEFAULT 'none',          -- none|configured|active (active = request seen)
  installed_version TEXT NOT NULL DEFAULT '', latest_version TEXT NOT NULL DEFAULT '',
  is_self INTEGER NOT NULL DEFAULT 0, archived INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);

CREATE TABLE project_keys (      -- SDK "reporter" keys: "rhk_<projectid>_<random>"; role is always reporter
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL, key_hash TEXT NOT NULL UNIQUE, key_prefix TEXT NOT NULL,
  allowed_origins TEXT NOT NULL DEFAULT '["*"]',
  created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT);

CREATE TABLE requests (          -- Development Inbox
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  source TEXT NOT NULL CHECK (source IN ('dashboard','sdk','cli','api')),
  type TEXT NOT NULL CHECK (type IN ('bug','feature','improvement','refactor','performance','security','docs','release')),
  title TEXT NOT NULL DEFAULT '', description TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 2,             -- 0 low,1 normal-,2 normal,3 high,4 urgent
  status TEXT NOT NULL DEFAULT 'new',              -- new|triaged|in_progress|waiting_input|review|done|rejected|failed
  installation_id TEXT, app_version TEXT, platform TEXT,
  reporter_user_id TEXT, reporter_key_id TEXT,
  diagnostics TEXT NOT NULL DEFAULT '[]',          -- JSON, already redacted
  redactions INTEGER NOT NULL DEFAULT 0,           -- number of redacted items
  task_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX idx_requests_project ON requests(project_id, created_at);
CREATE INDEX idx_requests_install ON requests(project_id, installation_id);

CREATE TABLE attachments (
  id TEXT PRIMARY KEY, request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('screenshot','log','file')), filename TEXT NOT NULL, mime TEXT NOT NULL,
  size INTEGER NOT NULL, path TEXT NOT NULL, redacted INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL);

CREATE TABLE tasks (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), request_id TEXT,
  title TEXT NOT NULL, prompt TEXT NOT NULL,
  agent TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 2,
  branch TEXT NOT NULL DEFAULT '', worktree_path TEXT NOT NULL DEFAULT '', base_commit TEXT NOT NULL DEFAULT '',
  head_commit TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',             -- agent session/thread id for resume
  next_input TEXT NOT NULL DEFAULT '',             -- pending user message/answer for the next turn
  allowed_tools TEXT NOT NULL DEFAULT '[]',        -- granted extra permissions (JSON)
  pending_question TEXT NOT NULL DEFAULT '', pending_approval TEXT NOT NULL DEFAULT '',  -- JSON of denials
  worker_pid INTEGER NOT NULL DEFAULT 0, heartbeat_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 2, turns INTEGER NOT NULL DEFAULT 0,
  test_status TEXT NOT NULL DEFAULT '',            -- ''|passed|failed|skipped
  test_output TEXT NOT NULL DEFAULT '',            -- tail, max 16 KiB
  diff_stat TEXT NOT NULL DEFAULT '', result_summary TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '', failure_reason TEXT NOT NULL DEFAULT '',  -- e.g. tests_failed|timeout|merge_conflict|agent_error
  cost_usd REAL NOT NULL DEFAULT 0, release_id TEXT,
  created_by TEXT NOT NULL, approved_by TEXT,
  version INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, started_at TEXT, finished_at TEXT);
CREATE INDEX idx_tasks_state ON tasks(state, priority, created_at);
CREATE INDEX idx_tasks_project ON tasks(project_id, created_at);

CREATE TABLE task_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL, type TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}',
  idem_key TEXT, created_at TEXT NOT NULL,
  UNIQUE(task_id, seq));
CREATE UNIQUE INDEX idx_task_events_idem ON task_events(task_id, idem_key) WHERE idem_key IS NOT NULL;

CREATE TABLE releases (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  version TEXT NOT NULL, tag TEXT NOT NULL DEFAULT '', strategy TEXT NOT NULL,
  task_ids TEXT NOT NULL DEFAULT '[]', notes TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,              -- building|published|failed|rolled_back
  artifacts TEXT NOT NULL DEFAULT '[]',   -- JSON [{name,path,sha256,size,signature}]
  log TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL, approved_by TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE(project_id, version));

CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, actor TEXT NOT NULL, actor_role TEXT NOT NULL,
  action TEXT NOT NULL, target_type TEXT NOT NULL DEFAULT '', target_id TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '{}', ip TEXT NOT NULL DEFAULT '');

CREATE TABLE push_subscriptions (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  endpoint TEXT NOT NULL UNIQUE, p256dh TEXT NOT NULL, auth TEXT NOT NULL, created_at TEXT NOT NULL);

CREATE TABLE notifications (
  id TEXT PRIMARY KEY, event_key TEXT NOT NULL UNIQUE,   -- dedupe key, e.g. "task:<id>:REVIEW_READY:<version>"
  kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, url TEXT NOT NULL,
  project_id TEXT, task_id TEXT, status TEXT NOT NULL DEFAULT 'pending',  -- pending|sent|failed|skipped
  attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', read_at TEXT,
  created_at TEXT NOT NULL, next_attempt_at TEXT NOT NULL);

CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
```

## Domain notes

* **Request ↔ task**: a request gets one task when `auto_task_from_requests` is on or a Developer clicks
  “Start task”. The request status mirrors the task state:
  QUEUED..RUNNING→`in_progress`, WAITING_INPUT→`waiting_input`, REVIEW_READY..RELEASING→`review`,
  COMPLETED→`done`, FAILED→`failed`, CANCELLED→`rejected`.
* **Event types** (`task_events.type`): `state_changed {from,to,reason}`, `user_message {text,author}`,
  `agent_text {text}`, `tool_use {name,input}`, `tool_result {ok,output}`, `agent_error {message}`,
  `usage {cost_usd,input_tokens,output_tokens,turns}`, `question {text}`, `answer {text,author}`,
  `permission_request {denials:[{tool,input}]}`, `permission_decision {allow,tools,author}`,
  `test_result {status,exit_code,output_tail,duration_ms}`, `diff {stat,files}`, `merge {commit,branch}`,
  `release {release_id,version,status}`, `log {level,message}`.
* **Roles**: reporter < developer < maintainer < admin (ordered). Project keys always act as `reporter` scoped
  to one project and only see requests from their own `installation_id`.
* **Self project**: on startup core ensures a project with `is_self=1` exists (id `rhymehub`, repo from
  config `self_repo`; type `rhymehub`, release strategy `rhymehub-self`).
