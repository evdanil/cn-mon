# Repository workflow

## Required roles and models

- Use a coding agent running `gpt-6-luna` at medium reasoning effort for implementation.
- Use an independent reviewer running `gpt-6.1-sol` at medium reasoning effort for code review.
- Use an architect running `gpt-6-astra` at xhigh reasoning effort for specifications and milestone planning.
- The user has authorized delegation and these model overrides.

## Development process

- Keep changes small and apply KISS, DRY, and YAGNI.
- Use red-green-refactor TDD for behavior changes.
- Work in the existing isolated checkout. Do not create Git worktrees unless the user asks.
- Keep the application specification, development plan, README, and user documentation current after each major milestone. Revise them to describe the current design and behavior; do not append stale progress logs.
- Submit each major milestone in its own pull request to `main`. Complete the independent `gpt-6.1-sol` medium code review first, then request external Codex review with the PR comment `@codex review`. Monitor the review, resolve actionable findings, and rerun affected checks. Treat a pending or unavailable external review as incomplete. Merge only after required checks pass, both reviews are resolved, and no actionable external findings remain.
- Do not begin implementation during a specification-only milestone. The existing `main.go` placeholder remains untouched until an implementation milestone is authorized.
- Preserve all `cn-tool` built-in monitor behavior except its interactive menu, XLSX export, Python `process_data` hooks, and custom Python plugin framework. Python runtime support is out of scope. Supported platforms are Linux x86-64 and Windows x86-64 only. macOS is out of scope.
- Preserve the settled signal behavior in `docs/APPLICATION_SPEC.md` §5: standard Go resets inherited ignored `SIGTERM` before `main`, so pre-run `SIGTERM` follows the default action; active-run `SIGTERM` is graceful, while inherited ignored `SIGHUP` and `SIGINT` remain honored.
- Implement ICMP probes using native Go support; do not invoke a shell `ping` command.
- Validate runtime behavior on both Linux and Windows for later code milestones. Cross-compilation alone does not count as runtime validation.
- Keep production systems and APIs unchanged unless the user authorizes those side effects.

## Licensing

- License new utility code under Apache License, Version 2.0.
- Use only MIT, BSD, Apache-2.0, public-domain, or similarly compatible dependencies; preserve applicable third-party notices. Do not add copyleft dependencies.

<!-- trail:start -->
## Trail — repo context graph

This repo is indexed in `.trail/cache/`: small linked markdown nodes that explain each
system and carry exact file:line spans, kept in sync with the code through git.

For ANY task here — understanding how something works, finding where code lives,
or scoping a change — get context from the graph before grepping or opening
source files. Re-ask freely (it's cheap) and reuse literal identifiers you
already have (symbol, error string, file name) as the query. New to this repo?
Run `trail map` first — a token-budgeted orientation (dir clusters, hubs,
hotspots), no LLM, no key.

- Run `trail ask "<your question>" --source` → ranked nodes with the relevant
  code spans inlined (each hit's ≤8-line crux by default; `--full` for whole
  definitions when the crux isn't enough). Match the tool to the task shape:
  for understanding or editing, the top node IS the answer — cite its
  `covers:` file:line spans and edit straight from `--source`. For
  exhaustive tasks ("every occurrence / every caller of this pattern"), ranked
  results are top-N, not complete — run `trail grep "<literal>"` instead
  (exhaustive over indexed files, grouped by enclosing symbol), falling back
  to raw `grep -rn` only for unindexed files.
- `trail skeleton <file>` → every definition's signature + span, ~10× cheaper
  than reading the file; use it to skim an API surface.
- `trail callers <symbol>` gives precomputed, exact edges — who calls this.
  Add `--direction out` for what it calls, or `--depth N` to walk
  transitively for the full blast radius. For structural questions, skip
  ranking and use this directly.
- Or browse: `.trail/cache/INDEX.md` lists every node; follow the links.
- Monorepos and folders of multiple repos rank fairly across sub-projects —
  hits carry `[scope/]` labels naming which one they're from. Narrow with
  `trail ask "<task>" --in <scope>/` once you know where you're working.

If a returned span is truncated ("+N more lines"), open the file at that exact
range before finalizing. Only open source files when a node genuinely lacks a
needed detail, and then at the exact file:line the node points to — never
re-read whole files.

After big code changes, refresh the graph with `trail build` (deterministic,
no API key, $0).
<!-- trail:end -->
