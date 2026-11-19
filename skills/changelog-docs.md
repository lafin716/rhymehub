# Skill: Changelog and documentation updates

Use this skill to record a change in the changelog and to update the docs it affects.

## When to use

- A change is merged or ready for release and users or maintainers need to know about it.
- A public behavior, command, endpoint, or setting changed.

## Procedure

1. Collect the changes since the last entry: commit subjects, merged task titles, and the diff summary.
2. Group them under Added, Changed, Fixed, Security, and Removed. Omit empty groups.
3. Write each entry as one line in the user's terms: what changed and why it matters. Link the task or issue id
   when one exists.
4. Mark breaking changes clearly and state the migration step.
5. Update the docs that describe the changed behavior: command help, API reference, and setup guides. Change
   only the sentences that became wrong.
6. Check that every example still matches the current code and flags.
7. Report: entries added, docs changed, and anything you could not verify.

## Guardrails

- Do not write entries for changes you did not read.
- Do not record secrets, tokens, internal hostnames, or personal data.
- Do not edit contract documents that are owned by the leader without reporting the change.
