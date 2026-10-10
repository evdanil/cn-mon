# Repository workflow

## Required roles and models

- Use a coding agent running `gpt-6-luna` at high reasoning effort for implementation.
- Use an independent reviewer running `gpt-6-astra` at xhigh reasoning effort for architecture and code review.
- Use an architect running `gpt-6-astra` at xhigh reasoning effort for specifications and milestone planning.
- The user has authorized delegation and these model overrides.

## Development process

- Keep changes small and apply KISS, DRY, and YAGNI.
- Use red-green-refactor TDD for behavior changes.
- Work in the existing isolated checkout. Do not create Git worktrees unless the user asks.
- Keep the application specification, development plan, README, and user documentation current after each major milestone. Revise them to describe the current design and behavior; do not append stale progress logs.
- Submit each major milestone in its own pull request to `main`. Complete internal Astra review first, then request external Codex review with the PR comment `@codex review`. Monitor the review, resolve actionable findings, and rerun affected checks. Treat a pending or unavailable external review as incomplete. Merge only after required checks pass, both reviews are resolved, and no actionable external findings remain.
- Do not begin implementation during a specification-only milestone. The existing `main.go` placeholder remains untouched until an implementation milestone is authorized.
- Preserve all `cn-tool` monitor behavior except its interactive menu and XLSX export. Supported platforms are Linux x86-64 and Windows x86-64 only. macOS is out of scope.
- Implement ICMP probes using native Go support; do not invoke a shell `ping` command.
- Validate runtime behavior on both Linux and Windows for later code milestones. Cross-compilation alone does not count as runtime validation.
- Keep production systems and APIs unchanged unless the user authorizes those side effects.

## Licensing

- License new utility code under Apache License, Version 2.0.
- Use only MIT, BSD, Apache-2.0, public-domain, or similarly compatible dependencies; preserve applicable third-party notices. Do not add copyleft dependencies.
