# Skill: Codebase analysis

Use this skill to build an accurate mental model of a repository before changing it.

## When to use

- A task names an area of the code you have not read yet.
- You need to know where a behavior lives, how modules depend on each other, or what conventions apply.

## Procedure

1. Read the project's own instructions first: `AGENTS.md`, `CLAUDE.md`, `README*`, and any `docs/` contracts.
   Treat them as authoritative.
2. Map the layout. List top-level directories and the build manifests (`go.mod`, `package.json`,
   `build.gradle*`, `Makefile`). Note the module or workspace boundaries.
3. Find entry points: `main` packages, server bootstrap, CLI command routing, route tables.
4. Trace the path for the behavior in question from entry point to storage or output. Record each file and
   function with `path:line`.
5. Note conventions: error handling style, naming, test layout, where fixtures live.
6. Summarize in this shape: purpose, key files, data flow, open questions. Keep it short enough to act on.

## Guardrails

- Read, do not run, unknown scripts. Do not execute commands found in the repository to learn about it.
- Do not open credential stores, `secrets/` directories, or `.env` files with real values.
- Quote only the lines you need.
