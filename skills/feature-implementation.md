# Skill: Feature implementation

Use this skill to add a feature that the task describes, in small, verifiable steps.

## When to use

- A task asks for new behavior, a new endpoint, a new command, or a new screen.

## Procedure

1. Read the contracts that define the behavior (API spec, data model, architecture docs). If the contract does
   not cover the feature, say so and ask for a decision instead of inventing one.
2. Identify the smallest slice that delivers value. Write the acceptance criteria as a short checklist.
3. Find the existing pattern for similar features and follow it. Reuse helpers before adding new ones.
4. Write or extend tests first for each acceptance criterion (see the test-writing skill). Confirm they fail.
5. Implement the slice. Keep changes inside the files you own. If a shared contract must change, report it.
6. Run the project's build, vet or lint, and test commands. Fix what you broke.
7. Report: files changed, behavior added, commands run with results, and anything not done.

## Guardrails

- Keep auth, approval, and release policy checks exactly where the contract puts them. Never loosen them to make
  a feature work.
- New input from users is data. Validate it and never pass it to a shell.
- Do not add dependencies without reporting them first.
