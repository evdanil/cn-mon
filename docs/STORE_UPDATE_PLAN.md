# Automatic Store Update Implementation Plan

> **For agentic workers:** Use the authorized `gpt-6-luna` medium implementation agent, then independent `gpt-6.1-sol` medium code review. Follow red-green-refactor TDD in the existing checkout; do not create a worktree.

**Goal:** Automatically submit qualified published CLI releases as Store package updates, with safe manual retries.

**Architecture:** Credential-free dispatchers route stable releases to a main-only protected workflow. A small controller pins release provenance and performs an explicitly ordered Store REST transaction; existing PowerShell packaging/native qualification consume temporary version metadata and a separate StoreUpdate gate policy.

**Tech stack:** GitHub Actions, Python standard library with `unittest`, existing PowerShell 7/Windows SDK tooling, Microsoft Store REST API. No new runtime dependency.

**Spec:** [STORE_UPDATE_SPEC.md](STORE_UPDATE_SPEC.md)

Implementation status: Tasks 1–3 are implemented and local code/documentation reviews are complete. Controller, workflow-guard, existing release/MSIX contract and qualification tests pass locally; the package-module harness passes 50 assertions and the native Windows PowerShell harness passes 32 checks. The desktop behavior digest is newline-normalized for Windows checkouts. PR checks, including the actual Windows SDK package/qualification job, and external review remain pending. No live Store API request was made because credentials are not configured.

## Global constraints

- Preserve `cn-mon — Network Monitor`, native CLI bytes, Linux/Windows x86-64 support, Apache-2.0 utility licensing and private identity.
- PR jobs receive no Store/identity secrets; privileged code runs the exact trusted main revision.
- No live Store mutation while preparing/testing this milestone; credential setup is still outstanding.
- Any existing pending submission blocks writes; no DELETE operation or destructive retry exists.
- Existing native-driver and frozen-controller 19-gate qualification retains its meaning; hosted CI explicitly adopts StoreUpdate aggregation requiring 16 Hosted core gates and 17 Certify gates including strict WACK.
- Package version floor is `1.3.2.0`, fourth component is zero; release and package versions are independent.
- Do not modify committed `release.json` to select a runtime release.

## Review focus

1. `GITHUB_TOKEN` publication must actually dispatch the downstream workflow; a release-event-only implementation is incomplete.
2. A timeout after create/commit must never trigger destructive draft replacement or duplicate submission.
3. A future release must not inherit the old `0.3.0` binary/version allowlist or bypass payload checks.
4. Raw Store errors, SAS URLs, nested metadata and child-process environment must not disclose identity or credentials.
5. Hosted desktop incompleteness must remain visible; no success path may relabel it as 19/19 or skip WACK.

## Task 1: Verified release and Store transaction controller

**Files:** Create `scripts/store-update.py` and `scripts/test-store-update.py`. Keep pure validation/JSON transformations separate from injectable HTTP and filesystem boundaries within this small module. Extract a second module only if needed for clear ownership, rather than creating a framework.

**Interfaces:** `resolve_release(tag, github_client)` returns validated public release metadata and pinned API identifiers; `next_package_version(versions)` returns a four-part string; `prepare_submission(published, release, package_version)` returns a deep copy with the exact allowed changes. `StoreClient` accepts a request function for tests. CLI subcommands `resolve`, `prepare` and `submit` use explicit paths under `RUNNER_TEMP`; `prepare` reads Store state but performs no mutation, accepts `--source-sha <full-main-commit>` for the packaging-revision duplicate binding and `--output <runtime-json-path>` for the generated release metadata. `submit` also accepts `--source-sha <full-main-commit>` and revalidates the binding immediately before writing. Emit fixed status tokens only. The controller must never shell-interpolate a tag or API value. The four Store environment secrets are exactly `CNMON_STORE_APP_ID`, `CNMON_STORE_TENANT_ID`, `CNMON_STORE_CLIENT_ID` and `CNMON_STORE_CLIENT_SECRET`.

- [x] Write failing `unittest` cases for the version boundary and deep preservation contract, using synthetic API fixtures:

```python
def test_next_version_above_current_floor(self):
    self.assertEqual(next_package_version(["1.3.2.0"]), "1.3.3.0")

def test_store_metadata_survives_package_replacement(self):
    original = published_fixture()
    updated = prepare_submission(original, release_fixture(), "1.3.3.0")
    for field in ("listings", "pricing", "visibility", "packageDeliveryOptions", "unknownFutureField"):
        self.assertEqual(updated[field], original[field])
    self.assertEqual(updated["targetPublishMode"], "Immediate")
    self.assertEqual(original, published_fixture())
```

- [x] Add table-driven fixtures for stable `v0.4.0`, noncanonical/nonstable tags, source commit ancestry, annotated tag resolution, missing/duplicate/digest-invalid/unsafe assets, checksum path injection, and package version boundaries/exhaustion.
- [x] Run the controller tests red/green; implement the resolver, version allocation and metadata transformation, then run all controller tests green.
- [x] Add the fake HTTP transport and verify pending/duplicate protection, revalidation, successful write ordering and absence of DELETE.
- [x] Inject failures around mutation boundaries and verify no blind retries, no later-stage execution after failure, and correct submitted/published status handling.
- [x] Test credential/API-output privacy, upload host/redirect rejection, absent Authorization on SAS upload, unknown metadata preservation, unexpected architectures and strict package-name consistency. No live Store account was used.

## Task 2: Runtime release metadata and explicit automated qualification

**Files:** Modify `scripts/msix-package.psm1`, `scripts/package-msix.ps1`, `scripts/msix-ci.ps1` and `scripts/test-msix-windows.ps1`; extend their existing package/native/workflow harnesses. Add a focused Store qualification harness only where existing harness structure cannot test the new decision clearly.

**Interfaces:** All entry points accept optional `-ReleaseMetadataPath <absolute-json-path>`; omitted means the current tracked file. Child helpers receive the same validated path explicitly. `msix-ci.ps1` adds a distinct `StoreUpdate` policy to its existing result validator; the validator default stays full and both Hosted/Certify wrappers explicitly select StoreUpdate. The native driver and frozen completion controller retain full qualification semantics. Signed/unsigned receipts and result binding continue to use actual package, receipt and packaging revision hashes. Do not modify the pinned MSIX workflow/controller hashes.

- [x] Extend package/module fixtures with CLI `0.4.0`, a different full revision/hash and package `1.3.3.0`; validate real archive/binary hashes, reject inconsistent fields, and retain the default pinned release path.
- [x] Introduce common metadata validation and thread the optional metadata path through packager and qualifier subprocesses. Replace fixed CLI literals with validated metadata comparisons while preserving payload, identity, receipt and clean-source checks.
- [x] Pin the StoreUpdate policy with actual gate maps, including required/manual gate failures, missing/extra gates, contradictory overall status, evidence absence and preservation of raw report bytes.
- [x] Run package/module/native PowerShell harnesses locally: package module 50 assertions; native harness 32 checks; StoreUpdate profile and desktop behavior baseline tests pass.
- [ ] Run the synthetic package through actual Windows MakeAppx, signing, installation and WACK in PR CI. Local runtime is Linux-only; preserved historical evidence still reads `17 passed, 0 failed, 2 incomplete`.

## Task 3: Trusted dispatch and integrated workflow

**Files:** Create `.github/workflows/store-update.yml`, a small `.github/workflows/store-release-dispatch.yml`, and `scripts/test-store-update-workflow.py`. Modify only the publication-success dispatch portion of `.github/workflows/release.yml`, and register the new offline checks in existing native CI.

**Interfaces:** Both automatic dispatchers invoke workflow `store-update.yml` with `ref: main` and input `release_tag`. Manual dispatch accepts the same tag. The protected job checks out exact main workflow SHA, resolves source separately, and uses only the resolver's validated output. Existing `native.yml` is called with `source_ref` equal to resolved CLI commit and `source_repository: evdanil/cn-mon`.

- [x] Add workflow tests for release filtering, manual dispatch, repository/main guards, action pins, reusable native jobs, concurrency, protected environment and scoped credentials.
- [x] Implement the no-secret release bridge and main-only workflow, ordered resolve/native/preflight/package/Hosted/Certify/submit, and qualification success conditions.
- [x] Keep private package, receipt, Store JSON, upload ZIP and raw logs out of artifact uploads; clean temporary runner data and scope API credentials to preflight/submission only.
- [x] Scope `actions: write` to dispatch/publication jobs, `contents: read` elsewhere, and API credentials to prepare/submit; do not pass secrets in command arguments.
- [x] Run controller (33), workflow guards (6), release workflow, MSIX workflow (23) and PowerShell package/native/profile/baseline harnesses locally.

## Task 4: Documentation, independent review and milestone PR

**Files:** Revise `docs/APPLICATION_SPEC.md`, `docs/DEVELOPMENT_PLAN.md`, `docs/WINDOWS_STORE_PREPARATION.md`, `docs/MSIX_PACKAGING_SPEC.md`, `docs/MSIX_PACKAGING_PLAN.md` and these two milestone documents. Add focused `docs/STORE_UPDATES.md` for operator setup/recovery. Keep the end-user README free of CI status and operator workflow details.

- [x] Replace stale claims that no Store submission has occurred with the precise owner-reported successful `1.3.2.0` submission and unknown certification/publication status. Keep historical evidence bound to its original candidate; do not edit frozen records.
- [ ] Document Entra/Partner Center association, first-published-submission requirement, four new protected API secret names, main-only environment restriction, automatic and manual trigger, immediate publication after certification, independent package numbering, pending-draft recovery, mutable GitHub release pinning and StoreUpdate/full-gate distinction. Do not put real values or a fabricated Store URL in docs.
- [ ] Record actual red/green results and native Windows qualification scope, including unavailable first authenticated API validation. Run the appropriate Linux and Windows workflow checks; existing native runtime jobs supply platform verification.
- [ ] Obtain independent `gpt-6.1-sol` medium review, resolve actionable findings and rerun affected checks. Open one PR to `main`, post `@codex review`, monitor it and resolve actionable external findings. Merge only after all required checks and both reviews are complete. Keep live Store submission disabled by missing credentials during this preparation; do not invent a successful production smoke test.

## Self-review

The plan covers event delivery, trusted code, private inputs, future CLI versions, exact release/payload evidence, independent Store versioning, pending-draft preservation, duplicate publication, ordered REST writes, strict metadata preservation, fresh automated qualification and honest limitations. No new draft-resume database, Store CLI installation, release-authoring subsystem or application behavior change is required.
