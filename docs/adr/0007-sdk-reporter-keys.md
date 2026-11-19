# ADR 0007 — Per-project reporter keys for SDK auth
Status: accepted
Decision: apps authenticate with revocable, origin-restricted, rate-limited project keys that only carry the
App Reporter role and see only their installation's requests. No admin secrets in apps.
