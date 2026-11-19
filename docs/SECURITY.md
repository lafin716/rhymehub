# Security

rhymehub drives coding agents against local repositories, so it is treated as a privileged system.

## Threat model (v0.1)
* Untrusted: SDK callers (keys are extractable from apps), request text, diagnostics, attachments, agent output.
* Semi-trusted: agents (official CLIs running with the user's login) — confined to the task worktree as far as
  each CLI's permission system allows.
* Trusted: authenticated Maintainer/Admin, the supervisor, the local OS user.

## Controls
| # | Principle | Implementation |
|---|---|---|
| 1 | App text is never executed as OS commands | Requests are stored as data and passed to the agent only as quoted untrusted text inside the prompt. Only `test_cmd`/`build_cmd`/`release_cmd`, configured by Developer/Maintainer, are executed by rhymehub. |
| 2 | Inputs are untrusted | Size limits (6 MiB body, 5 MiB attachment, 64 KiB description), MIME allowlist (png/jpeg/webp/text), filenames sanitized, attachments stored outside worktrees, never rendered as HTML. |
| 3 | Isolated working dirs | One git worktree per task under `$RHYMEHUB_HOME/worktrees/<project>/<task>`; base repos are never edited by agents. Merge is done by rhymehub after approval. |
| 4 | No access outside project root | Agent cwd = worktree; claude: `acceptEdits` + explicit `allowedTools`, denies for network/git push/secrets; codex: `-s workspace-write` sandbox. Paths from the API are validated (`filepath.Clean`, must stay under allowed roots). |
| 5 | No git credentials in prompts | Prompts are assembled only from task/request text + skills; the agent environment is scrubbed (allowlist: PATH, HOME, USER, LANG, TERM, TMPDIR, SHELL + CLI-specific config dirs); `GIT_ASKPASS`, `GH_TOKEN`, `GITHUB_TOKEN`, `*_TOKEN`, `*_SECRET`, `AWS_*` are removed. rhymehub never pushes from worktrees. |
| 6 | Deploy/signing keys separated from agents | `secrets/` (0700) is outside every worktree, denied to agents, not in their env; signing happens in core after merge. |
| 7 | SDK holds no admin token | SDK uses per-project reporter keys (`rhk_`), hashed at rest, revocable, origin-restricted, rate-limited, can only create/read own installation requests and answer questions. |
| 8 | Auth + TLS for external access | All APIs require auth except `/healthz`, `/api/v1/meta`, first-run `/api/v1/setup`. Plain-HTTP from non-loopback peers is refused unless the peer is in `insecureAllowedCIDRs` (default Tailscale ranges, which are WireGuard-encrypted). Native TLS via `tlsCert/tlsKey` config or `tailscale serve`. Passwords: bcrypt cost 12; login rate-limited (5/min/IP). Session cookies HttpOnly + SameSite=Lax + Secure on TLS; CSRF header required for cookie writes. |
| 9 | Audit log | Login, token/key creation, project policy changes, task create/cancel/approve/reject/permission decisions, releases, updates → `audit_log`. |
| 10 | Auto-release ≠ auto-approve | Two independent per-project flags, both default off; self project flags admin-only. |
| 11 | Official CLI auth only | Adapters execute `claude`/`codex` binaries with the user's existing login; rhymehub never reads their credential stores or reuses subscription tokens. |
| 12 | Agents cannot change policy | Policies live in the DB, editable only via authenticated API by Maintainer/Admin. Agent permission escalations require a human decision (WAITING_APPROVAL). Hard-denied tools can never be granted. |
| 13 | Diagnostics redaction | `internal/redact` removes emails, phone numbers, credit-card-like numbers (Luhn), JWTs, bearer tokens, AWS keys, GitHub/Slack/OpenAI/Anthropic-style keys, private key blocks, `password=`/`secret=`/`token=` values, cookies; count stored in `requests.redactions`. Screenshots are not passed to agents in v0.1 (metadata only). |

## Known limitations (v0.1)
* Agents run as the same OS user; Claude's permission rules are enforced by the CLI, not the kernel. Use a
  dedicated macOS user or container for stronger isolation (planned).
* SDK keys embedded in client apps can be extracted; impact is limited to spam requests for one project
  (rate limiting + revocation).
* v0.1.0 GitHub artifacts are checksum-verified; signature verification is enforced for the local release feed.
