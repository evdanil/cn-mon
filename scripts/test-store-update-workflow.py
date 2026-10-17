#!/usr/bin/env python3
# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]


class StoreUpdateWorkflowTests(unittest.TestCase):
    def test_store_workflow_uses_exact_main_sha_and_native_source(self):
        workflow = (ROOT / '.github/workflows/store-update.yml').read_text(encoding='utf-8')
        self.assertIn('workflow_dispatch:', workflow)
        self.assertIn("github.ref == 'refs/heads/main'", workflow)
        self.assertIn('ref: ${{ github.sha }}', workflow)
        self.assertIn('uses: ./.github/workflows/native.yml', workflow)
        self.assertIn('source_ref: ${{ needs.resolve-release.outputs.release_commit }}', workflow)
        self.assertIn('source_repository: evdanil/cn-mon', workflow)
        self.assertIn('group: cn-mon-store-update', workflow)
        self.assertIn('cancel-in-progress: false', workflow)
        self.assertIn('environment: msix-packaging', workflow)
        self.assertLess(workflow.index('Validate reviewed launch, alias, and Repair behavior baseline'),
                        workflow.index('Prepare read-only Store state and runtime metadata'))
        self.assertNotIn('pull_request_target', workflow)
        self.assertNotRegex(workflow, r'ref:\s*\$\{\{\s*(inputs|github\.event\.pull_request)')

    def test_credentials_are_limited_to_preflight_and_submit_steps(self):
        workflow = (ROOT / '.github/workflows/store-update.yml').read_text(encoding='utf-8')
        self.assertEqual(workflow.count('CNMON_STORE_CLIENT_SECRET:'), 2)
        self.assertEqual(workflow.count('CNMON_MSIX_IDENTITY_NAME:'), 5)
        self.assertIn("steps.prepare.outputs.status != 'already_published'", workflow)
        self.assertIn('Remove private Store and package state', workflow)
        uploads = re.findall(r'(?ms)^\s*- uses: actions/upload-artifact@.*?(?=^\s{6}- |\Z)', workflow)
        self.assertEqual(len(uploads), 1)
        self.assertIn('cn-mon-store-public.json', uploads[0])
        for private_name in ('private-state.json', 'runtime-release.json', 'msix-receipt.json', '.msix'):
            self.assertNotIn(private_name, uploads[0])

    def test_release_dispatcher_is_public_and_dispatches_only_main(self):
        dispatcher = (ROOT / '.github/workflows/store-release-dispatch.yml').read_text(encoding='utf-8')
        self.assertIn('types: [published]', dispatcher)
        self.assertIn("github.repository == 'evdanil/cn-mon'", dispatcher)
        self.assertIn('RELEASE_DRAFT:', dispatcher)
        self.assertIn('RELEASE_PRERELEASE:', dispatcher)
        self.assertIn("'--ref', 'main'", dispatcher)
        self.assertIn('actions: write', dispatcher)
        self.assertNotIn('environment:', dispatcher)

    def test_publication_job_explicitly_dispatches_gh_token_release(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text(encoding='utf-8')
        publish = workflow.split('\n  publish:', 1)[1]
        self.assertIn('actions: write', publish)
        self.assertIn('Dispatch Store update after verified publication', publish)
        self.assertIn('gh workflow run store-update.yml', publish)
        self.assertIn('--ref main', publish)

    def test_only_reviewed_action_sha_pins_are_used(self):
        for name in ('store-update.yml', 'store-release-dispatch.yml'):
            workflow = (ROOT / '.github/workflows' / name).read_text(encoding='utf-8')
            for action in re.findall(r'uses:\s*([^\s]+)', workflow):
                if action.startswith('actions/'):
                    self.assertRegex(action, r'actions/[a-z-]+@[0-9a-f]{40}')

    def test_credential_free_ci_runs_mocked_checks_on_both_systems(self):
        workflow = (ROOT / '.github/workflows/store-update-ci.yml').read_text(encoding='utf-8')
        self.assertIn('pull_request:', workflow)
        self.assertIn('runs-on: ubuntu-latest', workflow)
        self.assertIn('runs-on: windows-2025', workflow)
        self.assertGreaterEqual(workflow.count('scripts/test-store-update.py'), 2)
        self.assertGreaterEqual(workflow.count('scripts/test-store-update-workflow.py'), 2)
        self.assertIn("$PSNativeCommandUseErrorActionPreference = $true", workflow)
        self.assertNotIn('secrets.', workflow)

    def test_windows_package_failure_diagnostics_are_synthetic_and_allowlisted(self):
        workflow = (ROOT / '.github/workflows/store-update-ci.yml').read_text(encoding='utf-8')
        self.assertIn('Print allowlisted synthetic package diagnostics', workflow)
        diagnostics = workflow.split('Print allowlisted synthetic package diagnostics', 1)[1]
        for setting in (
            'CNMON_MSIX_IDENTITY_NAME',
            'CNMON_MSIX_IDENTITY_PUBLISHER',
            'CNMON_MSIX_PUBLISHER_DISPLAY_NAME',
            'CNMON_MSIX_EXPECTED_PFN',
            'Example.CnMon',
            'CN=Example, O=Example',
            'Example Publisher',
            'Example.CnMon_518g7wxbxvqf6',
            'package-tests.log',
            'package-build.log',
        ):
            self.assertIn(setting, diagnostics)
        self.assertNotIn('Get-ChildItem', diagnostics)
        self.assertNotIn('Get-Content -Path', diagnostics)


if __name__ == '__main__':
    unittest.main()
