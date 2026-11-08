---
title: A red Store run in the submit step can still be an accepted submission
author: NetSecTech
date: 2026-11-08
branch: fix/store-commit-poll
cost: { minutes: 10, tokens: 42121 }
id: l-4c86f49b
scope: repo
status: active
replaces: l-90c128fe
src: agent
touches:
  - docs/STORE_UPDATES.md
  - scripts/test-store-update.py
  - scripts/store-update.py
  - docs/STORE_UPDATE_SPEC.md
---

## Decided
When store-update.yml fails in "Submit qualified unsigned package", check Partner Center (or a read-only API read of the pending submission) before treating it as a failed submission or dispatching again. Every controller failure prints the same store_update_failed, so the log cannot say whether the commit already happened.

## Tried and ruled out
Reading the outcome from the run. The first live v0.4.0 submission (run 42430749592, package 1.3.3.0) ended red after a 2m56s submit step while Partner Center showed it accepted and in pre-processing; the old 120-second post-commit poll had most likely expired while the commit was still CommitStarted. The poll now checks every 10 seconds for up to 15 minutes and returns on the first accepted status, but a commit slower than that, or a transient error on a status read, still ends red after a successful commit.

## Watch out
Do not dispatch the Store workflow again while Partner Center shows a submission for that release: it stops at pending_submission, or after main moves it creates a duplicate submission.

In the code: scripts/store-update.py submit() poll loop, STATUS_POLL_INTERVAL and poll_seconds=900, ACCEPTED_STATUSES and FAILED_STATUSES; docs/STORE_UPDATE_SPEC.md transaction step 6; docs/STORE_UPDATES.md recovery guidance.
