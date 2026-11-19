-- rhymehub schema v1 (docs/DATA_MODEL.md). Timestamps are RFC3339 UTC text.

CREATE TABLE users (
  id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('reporter','developer','maintainer','admin')),
  disabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL);

CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL);

CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT);

CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL, repo_path TEXT NOT NULL, git_url TEXT, default_branch TEXT NOT NULL DEFAULT 'main',
  type TEXT NOT NULL DEFAULT 'generic',
  stack TEXT NOT NULL DEFAULT '',
  build_cmd TEXT NOT NULL DEFAULT '', test_cmd TEXT NOT NULL DEFAULT '',
  release_strategy TEXT NOT NULL DEFAULT 'none',
  release_cmd TEXT NOT NULL DEFAULT '',
  default_agent TEXT NOT NULL DEFAULT 'claude', default_model TEXT NOT NULL DEFAULT '',
  max_concurrent INTEGER NOT NULL DEFAULT 1, timeout_minutes INTEGER NOT NULL DEFAULT 30,
  auto_task_from_requests INTEGER NOT NULL DEFAULT 1,
  auto_approve INTEGER NOT NULL DEFAULT 0, auto_release INTEGER NOT NULL DEFAULT 0,
  allowed_tools TEXT NOT NULL DEFAULT '[]',
  sdk_status TEXT NOT NULL DEFAULT 'none',
  installed_version TEXT NOT NULL DEFAULT '', latest_version TEXT NOT NULL DEFAULT '',
  is_self INTEGER NOT NULL DEFAULT 0, archived INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);

CREATE TABLE project_keys (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL, key_hash TEXT NOT NULL UNIQUE, key_prefix TEXT NOT NULL,
  allowed_origins TEXT NOT NULL DEFAULT '["*"]',
  created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT);

CREATE TABLE requests (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  source TEXT NOT NULL CHECK (source IN ('dashboard','sdk','cli','api')),
  type TEXT NOT NULL CHECK (type IN ('bug','feature','improvement','refactor','performance','security','docs','release')),
  title TEXT NOT NULL DEFAULT '', description TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 2,
  status TEXT NOT NULL DEFAULT 'new',
  installation_id TEXT, app_version TEXT, platform TEXT,
  reporter_user_id TEXT, reporter_key_id TEXT,
  diagnostics TEXT NOT NULL DEFAULT '[]',
  redactions INTEGER NOT NULL DEFAULT 0,
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
  session_id TEXT NOT NULL DEFAULT '',
  next_input TEXT NOT NULL DEFAULT '',
  allowed_tools TEXT NOT NULL DEFAULT '[]',
  pending_question TEXT NOT NULL DEFAULT '', pending_approval TEXT NOT NULL DEFAULT '',
  worker_pid INTEGER NOT NULL DEFAULT 0, heartbeat_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 2, turns INTEGER NOT NULL DEFAULT 0,
  test_status TEXT NOT NULL DEFAULT '',
  test_output TEXT NOT NULL DEFAULT '',
  diff_stat TEXT NOT NULL DEFAULT '', result_summary TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '', failure_reason TEXT NOT NULL DEFAULT '',
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
  status TEXT NOT NULL,
  artifacts TEXT NOT NULL DEFAULT '[]',
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
  id TEXT PRIMARY KEY, event_key TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, url TEXT NOT NULL,
  project_id TEXT, task_id TEXT, status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', read_at TEXT,
  created_at TEXT NOT NULL, next_attempt_at TEXT NOT NULL);

CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
