# API Specification (v1)

Base: `/api/v1`. JSON bodies, **camelCase** fields, RFC3339 timestamps. Errors:
`{"error": {"code": "not_found|bad_request|unauthorized|forbidden|conflict|illegal_transition|tls_required|rate_limited|internal", "message": "..."}}`
with matching HTTP status (404/400/401/403/409/409/403/429/500).

## Authentication

| Caller | Mechanism | Role |
|---|---|---|
| Browser dashboard | `POST /auth/login` → `rh_session` cookie (HttpOnly, SameSite=Lax, Secure when TLS) | user role |
| CLI / automation | `Authorization: Bearer rht_...` (API token) | token owner's role |
| Managed app SDK | `X-Rhymehub-Key: rhk_...` on `/api/v1/sdk/*` only | `reporter`, one project |
| Supervisor | `Authorization: Bearer <secrets/supervisor.token>` on `/api/v1/system/drain` | system |

State-changing cookie requests must send `X-Rhymehub-CSRF: 1` (custom header; blocked cross-site by CORS).
**TLS policy**: requests from non-loopback peers over plain HTTP are rejected with `tls_required` unless the
peer IP is in `config.insecureAllowedCIDRs` (default: `100.64.0.0/10` Tailscale + `fd7a:115c:a1e0::/48`).

Minimum roles are noted per endpoint as `[dev]`, `[maint]`, `[admin]`; `[any]` = any authenticated user.

## Public
* `GET /healthz` → `{"status":"ok","version":"0.1.0","schemaVersion":1,"draining":false,"uptimeSec":12}`
* `GET /api/v1/meta` → `{"version","needsSetup":bool}` (needsSetup = no users exist)
* `POST /api/v1/setup` `{username,password}` – only when no user exists; creates admin.

## Auth
* `POST /auth/login` `{username,password}` → `{user}`; `POST /auth/logout`; `GET /auth/me` → `{user}`
  `User = {id, username, role, createdAt}`
* `GET /tokens` `[any]`, `POST /tokens {name}` → `{token:"rht_...", id}` (shown once), `DELETE /tokens/{id}`
* `GET /users` `[admin]`, `POST /users {username,password,role}` `[admin]`, `PATCH /users/{id} {role?,disabled?,password?}` `[admin]`

## Projects
`Project = {id,name,repoPath,gitUrl,defaultBranch,type,stack,buildCmd,testCmd,releaseStrategy,releaseCmd,
defaultAgent,defaultModel,maxConcurrent,timeoutMinutes,autoTaskFromRequests,autoApprove,autoRelease,allowedTools[],
sdkStatus,installedVersion,latestVersion,isSelf,archived,createdAt,updatedAt,
stats:{openRequests,activeTasks,reviewReady,failed}, git:{branch,head,dirty,ahead?}}`

* `GET /projects` `[any]` (`?q=` search) ; `GET /projects/{id}` `[any]`
* `POST /projects` `[dev]` `{id?,name,repoPath? | gitUrl?, defaultBranch?, type?, testCmd?, buildCmd?, ...}` –
  local path must be a git repo; `gitUrl` clones into `$RHYMEHUB_HOME/repos/<id>`. Detects stack/test cmd
  when omitted (package.json scripts.test, go.mod → `go test ./...`, gradlew → `./gradlew test`).
* `PATCH /projects/{id}` `[dev]` (policies `autoApprove`, `autoRelease`, `releaseStrategy`, `releaseCmd`,
  `testCmd`, `allowedTools` require `[maint]`). Self project policies require `[admin]`.
* `POST /projects/discover` `[dev]` `{root}` → `{candidates:[{path,name,stack,registered}]}` (git repos ≤ depth 2)
* `GET /projects/{id}/keys` `[maint]`, `POST /projects/{id}/keys {name, allowedOrigins?}` → `{id,key,keyPrefix}`
  (key shown once), `DELETE /projects/{id}/keys/{keyId}` `[maint]`
* `GET /projects/{id}/git` → `{branch, head, dirty, recentCommits:[{hash,subject,author,at}]}`

## Requests (Inbox)
`Request = {id,projectId,source,type,title,description,priority,status,installationId,appVersion,platform,
diagnostics:[{kind,name,content?,attachmentId?}],redactions,taskId,task?:TaskSummary,createdAt,updatedAt}`

* `GET /requests` `[any]` `?projectId&status&source&type&limit&cursor`
* `POST /requests` `[dev]` `{projectId,type,title?,description,priority?}` (source=dashboard|cli by auth kind)
* `GET /requests/{id}`, `PATCH /requests/{id}` `[dev]` `{priority?,status?:'triaged'|'rejected'}`
* `POST /requests/{id}/task` `[dev]` `{agent?,model?}` → `Task` (creates task from request)

## Tasks
`TaskSummary = {id,projectId,requestId,title,agent,model,state,priority,branch,testStatus,diffStat,resultSummary,
pendingQuestion,pendingApproval:[{tool,input}]|null,failureReason,error,costUsd,turns,attempt,createdAt,updatedAt,
startedAt,finishedAt}`; `Task = TaskSummary + {prompt,worktreePath,baseCommit,headCommit,sessionId,testOutput,
allowedTools,approvedBy,releaseId}`

* `GET /tasks` `[any]` `?projectId&state&active=1&limit`
* `POST /tasks` `[dev]` `{projectId,prompt,title?,agent?,model?,priority?}` → `Task` (state QUEUED)
* `GET /tasks/{id}`; `GET /tasks/{id}/events?after=<seq>&limit=500` → `{events:[TaskEvent]}`
  `TaskEvent = {id,taskId,seq,type,payload,createdAt}`
* `POST /tasks/{id}/messages` `[dev]` `{text}` – follow-up turn (REVIEW_READY|FAILED|WAITING_INPUT → QUEUED)
* `POST /tasks/{id}/answer` `[dev]` `{text}` – answer pending question (WAITING_INPUT → QUEUED)
* `POST /tasks/{id}/permission` `[dev]` `{allow:bool}` – WAITING_APPROVAL → QUEUED (allow adds denied tools to
  task.allowedTools; tools that are globally forbidden can never be granted)
* `POST /tasks/{id}/cancel` `[dev]`; `POST /tasks/{id}/retry` `[dev]` (FAILED → QUEUED, attempt reset)
* `GET /tasks/{id}/diff` → `{base,head,stat,files:[{path,status,additions,deletions}],patch}` (patch ≤ 1 MiB)
* `POST /tasks/{id}/approve` `[maint]` `{release?:bool}` – REVIEW_READY → APPROVED → MERGING → (RELEASING) →
  COMPLETED; returns updated Task. Fails 409 when `testStatus=failed`.
* `POST /tasks/{id}/reject` `[maint]` `{reason}` → CANCELLED (worktree kept until cleanup)

## Agents
* `GET /agents` → `{agents:[{name,available,version,path,capabilities:{streaming,resume,multiTurn,
  permissionRequests,interactiveApproval,cancel,costReporting,models[]},running,limit,error?}],
  limits:{global,perAgent:{claude:2,codex:2}}}`
* `PATCH /agents/limits` `[admin]` `{global?,perAgent?}`

## Releases
`Release = {id,projectId,version,tag,strategy,taskIds,notes,status,artifacts:[{name,sha256,size,signed}],log,
createdBy,approvedBy,createdAt}`
* `GET /releases?projectId` ; `GET /releases/{id}`
* `POST /projects/{id}/releases` `[maint]` `{version?,notes?}` – manual release of the default branch HEAD

## System / self-update
* `GET /system/status` `[any]` → `{version,schemaVersion,uptimeSec,draining,home,activeWorkers,
  supervisor:{reachable,version,currentVersion,updateInProgress}}`
* `GET /system/updates` `[any]` → `{available:[{version,publishedAt,notes}], history:[UpdateRecord]}`
  `UpdateRecord={id,from,to,status:'succeeded'|'rolled_back'|'failed',phase,error,startedAt,finishedAt,dbBackup}`
* `POST /system/update` `[admin]` `{version}` → forwards to supervisor, `202 {accepted:true}`
* `POST /system/drain` (supervisor token) `{drain:true|false}` → `{draining, activeWorkers}`

## Notifications / push
* `GET /push/vapid-public-key` → `{key}`; `POST /push/subscribe` `{endpoint,keys:{p256dh,auth}}`;
  `DELETE /push/subscribe` `{endpoint}`; `POST /push/test` `[any]`
* `GET /notifications?limit` → `{notifications:[{id,kind,title,body,url,createdAt,readAt}]}`;
  `POST /notifications/read` `{ids}`
* `GET /audit?limit` `[admin]`

## SSE
`GET /events/stream` `[any]` (cookie or bearer; `?token=` not supported). Each message:
`id: <globalEventId>` / `event: <type>` / `data: <json>` with types
* `task_event` → `TaskEvent + {projectId}`
* `task_updated` → `TaskSummary`
* `request_updated` → `Request`
* `notification` → `{id,kind,title,body,url}`
* `system` → `{draining, version}`
Heartbeat comment every 15 s. Clients reconnect with `Last-Event-ID` (task events replayed).

## SDK endpoints (`X-Rhymehub-Key`, CORS enabled per key `allowedOrigins`)
Rate limit: 30 requests/min per key+installation, body ≤ 6 MiB.
* `POST /sdk/requests` `{type,title?,description,installationId,appVersion?,platform?,
  diagnostics?:[{kind:'log'|'error'|'context',name,content}], attachments?:[{kind:'screenshot'|'log',filename,
  mime,dataBase64}]}` → `SdkRequest`
* `GET /sdk/requests?installationId=` → `{requests:[SdkRequest]}` (only this installation)
* `GET /sdk/requests/{id}?installationId=` → `SdkRequest`
* `POST /sdk/requests/{id}/answer` `{installationId,text}` – only when the task is WAITING_INPUT
* `GET /sdk/releases/latest?currentVersion=&platform=` → `{latestVersion,updateAvailable,notes,publishedAt,
  update:{kind:'web-reload'|'store'|'electron-feed'|'none', url?}}`

`SdkRequest = {id,projectId,type,title,description,status,createdAt,updatedAt,
 progress:{state,label,percent}, question?:string, result?:{summary,diffStat,testStatus},
 release?:{version,status}}` – no worktree paths, prompts, diffs or internal errors are exposed to reporters.
