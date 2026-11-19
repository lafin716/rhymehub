# Skill: Test writing

Use this skill to add tests that prove a behavior and fail when it breaks.

## When to use

- A feature or fix needs coverage, or an existing area has none.

## Procedure

1. Pick the public surface to test: an exported function, a command, or an endpoint. Test behavior through it,
   not through private helpers.
2. Use table-driven tests when several inputs share one assertion. Give each case a name that states the
   expected outcome.
3. Isolate state. Use a temporary directory for any file or database the test touches. Use temporary git
   repositories for workspace tests. Never read or write the user's real home directory.
4. Assert the outcome exactly: return values, error kinds (`errors.Is`), persisted state, and exit codes.
5. Cover the failure paths as well as the happy path: invalid input, missing data, and permission errors.
6. Run only the package under test first, then the wider suite. Report the command and result.
7. Check the test fails for the right reason: break the code once on purpose or confirm the reproduction fails.

## Guardrails

- Do not hit the network or real services. Use fakes.
- Do not weaken an assertion to make a test pass.
- Do not commit fixtures that contain real tokens, keys, or personal data.
