# Skill: Code review

Use this skill to review a change for correctness, security, and fit with the project's contracts.

## When to use

- A task asks for a review, or a change is ready and needs a second read before it is marked done.

## Procedure

1. Read the change description and the acceptance criteria. Know what the change is supposed to do.
2. Read the diff in full. For each file, check against the criteria above.
3. Check correctness: edge cases, error paths, concurrency, resource cleanup, and state transitions.
4. Check contracts: API shapes, status codes, error codes, and state machines match the docs.
5. Check security: input validation, authorization checks, secrets in code or logs, command construction, and
   path handling. A change that loosens a policy is a blocker.
6. Check tests: they cover the new behavior, fail without it, and are not weakened.
7. Report findings as a list. Each item gives severity (blocker, major, minor, nit), `path:line`, the problem,
   and a concrete fix. Say explicitly when you found nothing in a category.

## Guardrails

- Review the code, not the author. Keep comments specific and actionable.
- Do not approve a change whose validation you did not run or read.
