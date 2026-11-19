# Skill: Release preparation

Use this skill to prepare a release candidate. It does not publish anything. Publishing is a separate,
approved step.

## When to use

- A task asks to prepare a release, or the project is about to tag a version.

## Procedure

1. Confirm the default branch is clean and every task to include is in a completed state.
2. Run the full validation: build, vet or lint, and the complete test command. Record each result. Stop on the
   first failure and report it.
3. Determine the version from the change set: breaking change means major, new feature means minor, fix means
   patch. Report the choice and its reason.
4. Draft the release notes from merged changes (see the changelog-docs skill). Do not invent items.
5. List the artifacts the release will produce and confirm each build succeeds.
6. Check that no secrets, local paths, or personal data appear in artifacts or notes.
7. Report: version, changes included, validation results, artifacts, and what still needs a human approval.

## Guardrails

- Never change release policy settings (auto-approve, auto-release) as part of preparation.
- Never publish, upload, or tag without the explicit approval the project requires.
- A failing validation means the release is not ready. Do not skip it.
