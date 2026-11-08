---
title: A red Store run after the commit step can still be an accepted submission
author: NetSecTech
date: 2026-11-08
branch: main
cost: { minutes: 50, tokens: 115511 }
id: l-90c128fe
scope: repo
status: active
src: agent
touches:
  - docs/APPLICATION_SPEC.md
  - docs/DEVELOPMENT_PLAN.md
  - docs/MSIX_PACKAGING_PLAN.md
  - docs/MSIX_PACKAGING_SPEC.md
  - docs/STORE_UPDATES.md
  - docs/STORE_UPDATE_PLAN.md
  - docs/STORE_UPDATE_SPEC.md
  - docs/WINDOWS_STORE_LISTING.md
  - docs/WINDOWS_STORE_PREPARATION.md
  - scripts/store-update.py
---

## Decided
When store-update.yml fails in "Submit qualified unsigned package" after roughly two minutes, check Partner Center (or a read-only API read of the pending submission) before treating it as a failure or retrying.

## Tried and ruled out
Reading the outcome from the run: the controller polls only 120 seconds after commit and treats a commit still in CommitStarted at the deadline as failure (store_update_failed), as spec step 6 defines. The first live v0.4.0 submission (run 42430749592, package 1.3.3.0) ended red this way while Partner Center showed it accepted and in pre-processing.

## Watch out
Never re-dispatch the Store workflow for a tag whose submission is pending or published: it either stops at pending_submission or, after main moves, creates a duplicate submission. A longer post-commit poll would remove the false red.

In the code: scripts/store-update.py submit() poll loop and poll_seconds=120 default, ACCEPTED_STATUSES; docs/STORE_UPDATE_SPEC.md transaction step 6.
