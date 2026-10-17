# Automated Microsoft Store updates

This guide covers the owner setup and recovery steps for the automated package-only Store submission workflow. Stable GitHub releases trigger the workflow; the workflow verifies the release, packages and qualifies its Windows x64 archive, then creates a Store submission for certification and immediate publication after certification. A manual retry accepts the same stable release tag. The workflow does not edit listing content.

The owner reported that package `1.3.2.0` was submitted on 2026-10-17. Its certification and publication status has not been independently verified. The automation has not yet been validated against the live Store API.

## One-time credential setup

Complete this one-time setup using Microsoft's [Store services setup and submission guidance](https://learn.microsoft.com/en-us/windows/uwp/monetize/create-and-manage-submissions-using-windows-store-services):

1. In Partner Center, open **Account settings → Users**, add the Microsoft Entra application/service principal, and assign it the **Manager** role. Ensure the Partner Center account is associated with the application's Entra tenant directory.
2. In the Microsoft Entra admin center, open that application registration and copy its **Directory (tenant) ID** and **Application (client) ID**. Create a new client secret and copy its **Value** immediately; the secret's ID is not the credential.
3. In Partner Center, open the Store app's identity details and copy its **Store ID / Product ID** (the Store's product identifier). Use that value for `CNMON_STORE_APP_ID`. The separate **MSA app ID** is a UUID; do not use it or the Entra **Application (client) ID** here.
4. In the repository on GitHub, open **Settings → Environments → `msix-packaging`** and add the following as environment secrets:

| Secret | Value |
| --- | --- |
| `CNMON_STORE_APP_ID` | Store application ID from Partner Center, for the app whose identity matches this package |
| `CNMON_STORE_TENANT_ID` | Microsoft Entra tenant (directory) ID associated with Partner Center |
| `CNMON_STORE_CLIENT_ID` | Client/application ID of the Entra application added to Partner Center |
| `CNMON_STORE_CLIENT_SECRET` | A client-secret value for that Entra application |

The workflow also reuses the four existing protected `CNMON_MSIX_*` identity secrets. Keep all eight values in the protected environment; do not put them in repository files, workflow inputs, release assets, issue text, or logs. The environment is restricted to `main`. No seller ID is required by this API flow.

The Store app ID secret has already been configured privately; its value is intentionally not recorded in this public guide.

Before the first automated update, the app must have a **published** Store submission that provides a published package baseline. A submission that is merely created or pending certification is not enough; complete certification and publication in Partner Center first.

## Releases and package versions

Publish a stable `vMAJOR.MINOR.PATCH` GitHub release after its tag is on `main`. Publishing starts the Store update workflow automatically. If a run needs retrying, use the workflow's manual dispatch on `main` and enter the same release tag; the workflow pins and rechecks the release and its assets before submission.

Store package versions advance independently of the CLI version. The existing Store package version floor is `1.3.2.0`; the first ordinary automated update is `1.3.3.0`. The controller allocates the next available package version from the published Store package versions and records release provenance in the private certification notes. It does not rewrite committed release metadata.

The automation preserves the published submission's pricing unchanged. It accepts a nonblank `pricing.priceId` such as `Free` or `Tier96`, but stops before creating a draft if pricing is missing, malformed, or `Base` (case-insensitive). Resolve pricing in Partner Center; the workflow never changes it automatically.

## Pending submissions and recovery

The workflow stops before creating anything if Partner Center has any pending submission. It also never deletes, cancels, overwrites, or resumes a draft, including a draft left by an interrupted prior run. Inspect and resolve the pending submission manually in Partner Center, then retry the release workflow. If a Store write fails or returns an unknown status, inspect Partner Center before taking action; do not assume the operation rolled back or retry blindly. The workflow reports an accepted submission separately from confirmed publication.

For a repeated release, a matching already-published provenance marker is treated as `already_published` without another Store write. Other ambiguous or mismatched state fails closed for owner review.

## Validation status

Hosted qualification requires 16 automated gates to pass; certification qualification requires 17, including WACK. The original 19-gate report remains intact: `conflicts` and `repair` may be `incomplete` on fresh hosted runs, but may not fail. If package launch, alias, or Repair behavior changes, obtain a fresh visible-desktop review before relying on the automated route; ordinary CLI-only releases do not repeat those manual checks.

The controller and workflow have offline tests, but have not yet made a live Store API call. A green CI result means the release checks and automated package-qualification gates passed; it does not establish Store certification or publication, nor does it turn manually observed desktop gates into passes. Check Partner Center for the actual submission, certification, and publication state.
