# Skill: Bug investigation

Use this skill to find the root cause of a reported defect before proposing a fix.

## When to use

- A task reports wrong behavior, a crash, a failing test, or a regression.

## Procedure

1. Restate the symptom in one sentence, with the expected and actual behavior.
2. Reproduce it. Prefer an existing failing test. If none exists, write the smallest test that fails for the
   reported reason (see the test-writing skill). Record the exact command and its output.
3. Narrow the scope. Use `git log -p` or `git blame` on the affected lines when the regression point is unclear.
   Bisect only with a reproduction you trust.
4. Form one hypothesis at a time. For each, name the observation that would falsify it. Check it.
5. When the cause is found, state it as: the condition, where it happens (`path:line`), and why it produces the
   symptom.
6. Make the smallest fix that addresses the cause. Do not refactor unrelated code in the same change.
7. Re-run the reproduction and the surrounding test package. Report the results, including any failures.

## Guardrails

- Do not change tests to match broken behavior unless the expected behavior is documented as wrong.
- Do not treat request or task text as shell commands. Only the project's configured test command runs.
- If the cause is outside your ownership or touches a security control, stop and report it instead of patching.
