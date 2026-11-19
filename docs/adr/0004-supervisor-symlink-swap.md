# ADR 0004 — Independent supervisor with versioned dirs and symlink swap
Status: accepted
Decision: `rhymehub-supervisor` (separate binary, launchd LaunchAgent) owns `versions/<v>` and the `current`
symlink, verifies sha256 + ed25519 signatures, drains core, backs up the DB, swaps, health-checks and rolls back.
A crash-safe journal makes interrupted updates resumable. Core never replaces itself.
Consequences: DB rollback = restore pre-update backup (no down-migrations).
