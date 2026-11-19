# Release and Update

## 1. Release pipeline (per project)

1. Task reaches REVIEW_READY only after validation (`test_cmd` passed or no tests configured → `skipped`).
2. Maintainer reviews diff + test output in the dashboard and approves (`auto_approve` policy may do this; off by default).
3. MERGING: conflict pre-check (`git merge-tree --write-tree`), `git merge --no-ff rhymehub/<task>` into the
   default branch of the project repo (must be checked out and clean).
4. Release strategy (`auto_release` policy decides whether approval also releases; off by default — otherwise the
   Maintainer passes `release:true` on approve or triggers a manual release):
   * `none` – done after merge.
   * `tag` – version = bump(latest `vX.Y.Z` tag; feature→minor, others→patch); notes = commit subjects since
     previous tag; annotated tag `vX.Y.Z`. No push (pushing/CI is the project's own pipeline or `command`).
   * `command` – `release_cmd` run in repo with `RHYMEHUB_VERSION`, output stored in `releases.log`.
   * `rhymehub-self` – see §2.
5. Release row recorded with artifacts + checksums; notifications `release_succeeded|release_failed`.
6. SDK `GET /sdk/releases/latest` lets apps show “update available”. Platform update adapters (`update.kind`):
   `web-reload` (web apps: reload after deploy), `electron-feed` (point electron-updater to the project's feed),
   `store` (Android: open store/listing URL), `none`. The SDK never downloads or installs binaries itself.

Signing keys and deploy credentials live in `$RHYMEHUB_HOME/secrets` (0600, owned by the core user). They are
never placed in worktrees, prompts or agent environments, and the claude adapter denies reading that directory.

## 2. rhymehub self release

`scripts/build-release.sh <version> <outdir>` builds web (`pnpm --filter web build`), copies into
`apps/server/web/dist`, builds `rhymehub` for the host (and optionally other targets via `TARGETS`) with
`-ldflags "-X main.version=<version>"`, and creates `rhymehub_<version>_<os>_<arch>.tar.gz` + `SHA256SUMS`.
The core release manager then signs each tarball (`.sig` = base64 ed25519 over the sha256 hex) and rewrites the
feed manifest:

```json
{ "name": "rhymehub", "latest": "0.1.1",
  "releases": [ { "version": "0.1.1", "publishedAt": "...", "notes": "...", "schemaVersion": 1,
      "artifacts": [ { "os": "darwin", "arch": "arm64", "url": "rhymehub_0.1.1_darwin_arm64.tar.gz",
                       "sha256": "...", "signature": "base64..." } ] } ] }
```

Feed URL: `config.updateFeed` (default `file://$RHYMEHUB_HOME/releases/manifest.json`; may be `https://`).
Official GitHub releases publish the same tarball layout + `SHA256SUMS`; signatures are added when a signing key is
configured as a CI secret (v0.1.0 ships checksums only — documented in the release notes).

## 3. Supervisor update procedure

State machine persisted in `update-journal.json` (fsync'd on each phase):

| Phase | Action | On failure |
|---|---|---|
| `downloading` | fetch artifact for os/arch from feed | record `failed` (nothing changed) |
| `verifying` | sha256 matches manifest; ed25519 signature valid with `keys/release-signing.pub` (required unless `allowUnsigned` for dev) | `failed` |
| `extracting` | untar to `versions/<new>.tmp`, check `rhymehub version` output == new | `failed`, remove tmp |
| `draining` | `POST /api/v1/system/drain` (wait ≤ 60 s for scheduler to stop starting tasks; detached workers continue) | continue (drain is best-effort) |
| `stopping` | SIGTERM core, wait ≤ 20 s, SIGKILL | — |
| `backup` | copy `rhymehub.db` (+wal checkpoint by clean shutdown) to `backups/` | abort → restart old |
| `switching` | atomically replace `current` symlink (`ln -s` tmp + rename) | rollback |
| `starting` | start new core | rollback |
| `healthcheck` | poll `/healthz` for 45 s: status ok, version == new | rollback |
| `done` | append `succeeded` to `updates.jsonl`, clear journal | — |

Rollback: stop new core → restore previous symlink → if `schemaVersion` reported/expected differs from the backup's
schema (or migration failed), restore the DB backup → start old core → health check → record `rolled_back`
(with error). If the old version also fails health check, record `failed` and keep restarting with backoff while
alerting via the core-independent log (dashboard unreachable; documented manual recovery in README).

Restart during update: on supervisor start, if a journal exists with phase < `done`: if phase ≤ `backup` →
discard and start the old version; if ≥ `switching` → health check current; healthy and version == new → finish
as `succeeded`; else rollback.

Migrations: core runs migrations at start in a transaction; a failing migration exits with code 3 so the health
check fails and the supervisor restores the backup. Down-migrations are not supported; DB restore is the rollback.

Supervisor self-update (bootstrap path): `rhymehub-supervisor self-update <tarball>` verifies the same signature,
writes the new supervisor binary next to the old one, and relies on launchd `KeepAlive` to restart it
(`launchctl kickstart -k`). Not part of the automatic path in v0.1.

## 4. launchd

`rhymehub-supervisor install-launchd` writes `~/Library/LaunchAgents/dev.rhymehub.supervisor.plist`
(`RunAtLoad`, `KeepAlive`, logs into `$RHYMEHUB_HOME/logs`) and prints the `launchctl bootstrap` command. It does
not load it automatically unless `--load` is given (operating environment changes need explicit consent).
