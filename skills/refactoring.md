# Skill: Refactoring

Use this skill to change the structure of code without changing its behavior.

## When to use

- A task asks for cleanup, renaming, extraction, or consolidation, or a change would be hard without one.

## Procedure

1. Confirm a green baseline. Run the tests for the affected packages and record the result.
2. Write down the behavior that must stay the same. If no test covers it, add a characterization test first.
3. Make one structural move at a time: rename, extract, inline, or move. Keep each move compilable.
4. After each move, re-run the affected tests.
5. Remove dead code that your change made unused. Do not delete code you do not understand.
6. Keep the public API and file ownership stable unless the task says otherwise. Report any API change.
7. Report: what moved, why, and the test results before and after.

## Guardrails

- Do not mix behavior changes into a refactor. If a bug is found, record it and fix it separately.
- Do not touch contract documents or shared build files to make a refactor easier.
