---
name: init
description: Write or improve AGENTS.md, the instructions every session in this project starts from.
disable-model-invocation: true
argument-hint: "[focus]"
arguments:
  - focus
---
Write AGENTS.md at the root of the project: the instructions coding agents, codebot among them, read at the start of every session here. If it exists, improve it in place and keep what is still true; if only a CLAUDE.md exists, start from that.

If the user gave a focus, weigh it:
$ARGUMENTS

1. Explore before writing: the README, the build and package files (go.mod, package.json, Makefile, pyproject.toml, Cargo.toml and the like), CI workflows, linter and formatter configs, and the layout of the source tree. Read a few central files to learn how the parts fit.
2. Write down only what an agent can't quickly derive and gets wrong without:
   - The exact commands to build, test (everything, and a single test), lint and format.
   - The architecture in a few lines: the main packages or modules, how they depend on each other, and where to start reading.
   - Conventions that differ from the language's defaults: naming, error handling, test style, generated code not to edit.
   - Pitfalls: required environment, slow or flaky steps, things that must change together.
3. Leave out generic advice ("write clean code"), what a file listing shows, and anything you have not checked in the repository.
4. Keep it short, well under 200 lines of plain markdown with headings, so it stays worth reading every session.

When done, say in a few lines what you wrote or changed.
