---
title: "Retry a failed Store update with a fresh dispatch, never a job rerun"
author: NetSecTech
date: 2026-11-07
branch: main
cost: { minutes: 267, tokens: 1621052 }
id: l-b5076a84
scope: repo
status: active
src: agent
touches:
  - AGENTS.md
  - CLAUDE.md
  - opencode.json
  - README.md
  - docs/APPLICATION_SPEC.md
  - docs/DEVELOPMENT_PLAN.md
  - docs/STORE_UPDATES.md
  - docs/WINDOWS_STORE_LISTING.md
  - scripts/test-store-update-workflow.py
  - ".github/workflows/release.yml"
---

## Decided
Retry a stopped Microsoft Store update run by dispatching store-update.yml on main again with the same release tag, and only while no Store submission exists for that tag.

## Tried and ruled out
"Re-run failed jobs" on the Store run: the submit job downloads the metadata artifact under a name that includes the run attempt, so attempt 2 cannot find what attempt 1 uploaded and fails before contacting the Store. It tests nothing.

## Watch out
The controller prints only store_update_failed for any HTTP error, identity mismatch or malformed Store data, by design (no response bodies in logs). A failure in "Prepare read-only Store state" does not say whether sign-in, the app read or the published-submission checks failed. The Store API's publisherName is the CN= Package/Identity/Publisher value, matching CNMON_MSIX_IDENTITY_PUBLISHER.

In the code: .github/workflows/store-update.yml (artifact store-release-metadata-<run_id>-<run_attempt>), scripts/store-update.py request/_api/fail, docs/STORE_UPDATES.md retry guidance.
