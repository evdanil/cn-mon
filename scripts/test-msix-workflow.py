#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Check the Windows MSIX workflow contract without third-party packages."""

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
WORKFLOW_PATH = ROOT / '.github' / 'workflows' / 'msix.yml'
CI_HELPER_PATH = ROOT / 'scripts' / 'msix-ci.ps1'
SYNTHETIC_IDENTITY = {
    'name': 'Example.CnMon',
    'publisher': 'CN=Example, O=Example',
    'publisher_display_name': 'Example Publisher',
    'expected_pfn': 'Example.CnMon_518g7wxbxvqf6',
}
IDENTITY_ENV = {
    'CNMON_MSIX_IDENTITY_NAME',
    'CNMON_MSIX_IDENTITY_PUBLISHER',
    'CNMON_MSIX_PUBLISHER_DISPLAY_NAME',
    'CNMON_MSIX_EXPECTED_PFN',
}


def workflow_text():
    return WORKFLOW_PATH.read_text(encoding='utf-8')


def job_block(text, name):
    lines = text.splitlines()
    marker = f'  {name}:'
    try:
        start = lines.index(marker)
    except ValueError as error:
        raise AssertionError(f'missing workflow job: {name}') from error
    end = start + 1
    while end < len(lines):
        if re.match(r'^  [A-Za-z0-9_-]+:\s*$', lines[end]):
            break
        end += 1
    return '\n'.join(lines[start:end])


def step_blocks(job):
    lines = job.splitlines()
    starts = [i for i, line in enumerate(lines)
              if line.startswith('      - name: ') or line.startswith('      - uses: ')]
    result = []
    for position, start in enumerate(starts):
        end = starts[position + 1] if position + 1 < len(starts) else len(lines)
        result.append('\n'.join(lines[start:end]))
    return result


def named_step(job, name):
    marker = f'      - name: {name}'
    for block in step_blocks(job):
        if block.startswith(marker):
            return block
    raise AssertionError(f'missing workflow step: {name}')


def upload_steps(job):
    return [block for block in step_blocks(job)
            if re.search(r'^\s+uses: actions/upload-artifact@', block, re.MULTILINE)]


def upload_name(step):
    match = re.search(r'^\s+name: (.+)$', step, re.MULTILINE)
    if not match:
        raise AssertionError('artifact upload has no name')
    return match.group(1)


def path_entries(step):
    lines = step.splitlines()
    try:
        start = lines.index('          path: |') + 1
    except ValueError:
        match = next((line.strip()[len('path: '):] for line in lines
                      if line.strip().startswith('path: ')), None)
        return [match] if match else []
    entries = []
    for line in lines[start:]:
        if line.strip() and len(line) - len(line.lstrip(' ')) < 12:
            break
        if line.strip():
            entries.append(line.strip())
    return entries


def collect_public_candidates(candidates, allowlist, receipt_path):
    """Model the workflow's exact path allowlist and synthetic receipt guard."""
    expected = {Path(path).resolve() for path in allowlist}
    selected = []
    for candidate in candidates:
        resolved = Path(candidate).resolve()
        if resolved not in expected or resolved.suffix.lower() in {'.pfx', '.p12', '.key'}:
            raise ValueError('candidate is outside the public artifact allowlist')
        selected.append(resolved)
    if len(selected) != len(expected) or set(selected) != expected:
        raise ValueError('public artifact allowlist is incomplete or duplicated')

    receipt = json.loads(Path(receipt_path).read_text(encoding='utf-8'))
    for field, value in SYNTHETIC_IDENTITY.items():
        if receipt.get(field) != value:
            raise ValueError('receipt identity does not match the synthetic fixture')
    packages = [path for path in selected if path.suffix.lower() == '.msix']
    if len(packages) != 1 or receipt.get('purpose') != 'candidate':
        raise ValueError('public candidate set must contain one package and its receipt')
    actual_hash = hashlib.sha256(packages[0].read_bytes()).hexdigest()
    if receipt.get('package_sha256') != actual_hash:
        raise ValueError('receipt package hash does not match the uploaded bytes')
    return selected


class MsixWorkflowTests(unittest.TestCase):
    def test_workflow_exists_and_only_uses_read_only_pr_and_manual_triggers(self):
        self.assertTrue(WORKFLOW_PATH.is_file(), 'MSIX workflow has not been added')
        text = workflow_text()
        self.assertIn('on:\n  pull_request:', text)
        self.assertIn('  workflow_dispatch:', text)
        self.assertNotIn('\n  push:', text)
        self.assertIn('permissions:\n  contents: read', text)

    def test_public_pr_work_is_split_into_package_and_qualification_jobs(self):
        text = workflow_text()
        package = job_block(text, 'synthetic-package')
        qualify = job_block(text, 'synthetic-qualification')
        self.assertIn("if: github.event_name == 'pull_request'", package)
        self.assertIn("if: github.event_name == 'pull_request'", qualify)
        self.assertIn('needs: synthetic-package', qualify)
        self.assertIn('actions/download-artifact@', qualify)
        self.assertIn('cn-mon-msix-synthetic-package-${{ github.run_id }}-${{ github.run_attempt }}', qualify)
        self.assertIn('run: python scripts/test-msix-workflow.py', package)
        self.assertIn('run: python scripts/test-msix-workflow.py', qualify)

    def test_all_jobs_use_exact_source_checkouts_and_pinned_actions(self):
        text = workflow_text()
        for job_name in ('synthetic-package', 'synthetic-qualification', 'trusted-manual'):
            job = job_block(text, job_name)
            self.assertIn('persist-credentials: false', job)
            self.assertIn('runs-on: windows-2025', job)
            self.assertIn('timeout-minutes: 60', job)
            self.assertIn("go-version: '1.27.2'", job)
        for job_name in ('synthetic-package', 'synthetic-qualification'):
            job = job_block(text, job_name)
            self.assertIn('github.event.pull_request.head.repo.full_name', job)
            self.assertIn('github.event.pull_request.head.sha', job)
            self.assertIn('github.sha', job)
        manual = job_block(text, 'trusted-manual')
        self.assertIn('repository: ${{ github.repository }}', manual)
        self.assertIn('ref: ${{ github.sha }}', manual)
        self.assertRegex(text, r'uses: actions/checkout@[0-9a-f]{40}')
        self.assertRegex(text, r'uses: actions/setup-go@[0-9a-f]{40}')
        self.assertRegex(text, r'uses: actions/download-artifact@[0-9a-f]{40}')
        self.assertRegex(text, r'uses: actions/upload-artifact@[0-9a-f]{40}')

    def test_package_and_trusted_jobs_run_harness_safety_checks_before_qualification(self):
        text = workflow_text()
        safety_command = 'pwsh -NoProfile -File ./scripts/test-msix-windows-harness.ps1'
        for job_name, work_step in (
                ('synthetic-package', 'Build synthetic unsigned MSIX'),
                ('trusted-manual', 'Build trusted unsigned MSIX')):
            job = job_block(text, job_name)
            safety = named_step(job, 'Test native MSIX harness safety')
            self.assertIn(safety_command, safety)
            self.assertLess(job.index('Test native MSIX harness safety'), job.index(work_step))

    def test_pr_jobs_use_only_the_four_synthetic_identity_variables(self):
        text = workflow_text()
        for job_name in ('synthetic-package', 'synthetic-qualification'):
            job = job_block(text, job_name)
            self.assertNotIn('secrets.', job)
            self.assertNotIn('vars.', job)
            step = named_step(job, 'Configure synthetic identity')
            names = set(re.findall(r'CNMON_MSIX_[A-Z_]+', step))
            self.assertEqual(names, IDENTITY_ENV)
            self.assertIn(SYNTHETIC_IDENTITY['name'], step)
            self.assertIn(SYNTHETIC_IDENTITY['publisher'], step)
            self.assertIn(SYNTHETIC_IDENTITY['publisher_display_name'], step)
            self.assertIn(SYNTHETIC_IDENTITY['expected_pfn'], step)
            self.assertNotIn('vars.', step)
            self.assertNotIn('secrets.', step)

    def test_trusted_manual_job_uses_environment_secrets_and_masks_before_tools(self):
        text = workflow_text()
        manual = job_block(text, 'trusted-manual')
        self.assertIn("if: github.event_name == 'workflow_dispatch'", manual)
        self.assertIn('environment: msix-packaging', manual)
        self.assertNotIn("refs/heads/main", manual)
        step = named_step(manual, 'Configure trusted identity')
        self.assertIn('::add-mask::', step)
        for name in sorted(IDENTITY_ENV):
            self.assertIn(f'{name}: ${{{{ secrets.{name} }}}}', step)
        self.assertNotIn('${{ secrets.', step.split('        run: |\n', 1)[1])
        self.assertLess(manual.index('Configure trusted identity'), manual.index('Initialize Windows SDK'))
        self.assertLess(manual.index('Configure trusted identity'), manual.index('Download and hash the published'))
        uploads = upload_steps(manual)
        self.assertEqual(len(uploads), 1)
        self.assertEqual(path_entries(uploads[0]), [
            r'${{ runner.temp }}\cn-mon-msix-job\summary\msix-public-summary.json',
        ])

    def test_sdk_download_and_wack_prerequisites_are_explicit(self):
        helper = CI_HELPER_PATH.read_text(encoding='utf-8')
        self.assertIn(r'C:\Program Files (x86)\Windows Kits\10\bin\10.0.26100.0\x64', helper)
        self.assertIn('makeappx.exe', helper.lower())
        self.assertIn('signtool.exe', helper.lower())
        self.assertIn('10.0.26100.0', helper)
        self.assertIn('archive_sha256', helper)
        self.assertIn('Get-FileHash', helper)
        self.assertIn('FileVersionInfo', helper)
        self.assertIn('ImageOS', helper)
        self.assertIn('Win32_OperatingSystem', helper)
        windows_test = (ROOT / 'scripts' / 'test-msix-windows.ps1').read_text(encoding='utf-8')
        self.assertIn(r'C:\Program Files (x86)\Windows Kits\10\App Certification Kit\appcert.exe', windows_test)
        self.assertIn("& $appcert '/?'", windows_test)
        self.assertIn('WTSGetActiveConsoleSessionId', windows_test)
        workflow = workflow_text()
        for job_name in ('synthetic-package', 'synthetic-qualification', 'trusted-manual'):
            job = job_block(workflow, job_name)
            self.assertIn('scripts/msix-ci.ps1 -Mode Initialize', job)
            self.assertIn('scripts/msix-ci.ps1 -Mode Summary', job)
        self.assertEqual(workflow.count('scripts/msix-ci.ps1 -Mode DownloadRelease'), 2)
        self.assertEqual(workflow.count('scripts/msix-ci.ps1 -Mode Build'), 2)

    def test_synthetic_qualification_hash_checks_and_certifies_in_same_job(self):
        text = workflow_text()
        qualify = job_block(text, 'synthetic-qualification')
        hosted = named_step(qualify, 'Qualify the downloaded package bytes')
        self.assertIn('scripts/msix-ci.ps1 -Mode Hosted -PackageDirectory input', hosted)
        certify = named_step(qualify, 'Run WACK certification gate')
        self.assertIn('scripts/msix-ci.ps1 -Mode Certify', certify)
        evidence = named_step(qualify, 'Upload synthetic qualification evidence')
        self.assertIn("if: always() && !cancelled()", evidence)
        self.assertNotIn("steps.qualify.outcome", evidence)
        helper = CI_HELPER_PATH.read_text(encoding='utf-8')
        for requirement in ('scripts/test-msix-windows.ps1', '-Mode Hosted', '-Mode Certify',
                            'package_sha256', 'archive_sha256', 'binary_sha256',
                            'packaging_revision', 'Get-SourceRevision', 'Read-NativeResults',
                            'Assert-NativeResults'):
            self.assertIn(requirement, helper)
        self.assertIn("$results.mode -cne $ExpectedMode", helper)
        certify_branch = re.search(r"(?ms)^    'Certify' \{(.*?)^    'Summary' \{", helper).group(1)
        self.assertLess(certify_branch.index('Read-NativeResults'),
                        certify_branch.index('Write-GateOutput'))
        self.assertLess(certify_branch.index('Write-GateOutput'),
                        certify_branch.index('Assert-NativeResults'))

    def test_public_artifact_paths_are_exact_and_manual_uploads_summary_only(self):
        text = workflow_text()
        package = job_block(text, 'synthetic-package')
        qualify = job_block(text, 'synthetic-qualification')
        manual = job_block(text, 'trusted-manual')
        package_uploads = upload_steps(package)
        qualify_uploads = upload_steps(qualify)
        manual_uploads = upload_steps(manual)
        self.assertEqual(len(package_uploads), 2)
        self.assertEqual(len(qualify_uploads), 3)
        self.assertEqual(len(manual_uploads), 1)
        artifact_names = [upload_name(upload) for upload in
                          package_uploads + qualify_uploads + manual_uploads]
        self.assertEqual(len(artifact_names), len(set(artifact_names)))
        self.assertEqual(set(path_entries(package_uploads[0])), {
            r'${{ runner.temp }}\cn-mon-msix-job\package\cn-mon_1.3.0.0_x64.msix',
            r'${{ runner.temp }}\cn-mon-msix-job\package\msix-receipt.json',
        })
        self.assertEqual(set(path_entries(qualify_uploads[0])), {
            r'${{ runner.temp }}\cn-mon-msix-job\evidence\cn-mon_1.3.0.0_x64-signed.msix',
            r'${{ runner.temp }}\cn-mon-msix-job\evidence\msix-signed-receipt.json',
            r'${{ runner.temp }}\cn-mon-msix-job\evidence\msix-native-results.json',
        })
        summary = r'${{ runner.temp }}\cn-mon-msix-job\summary\msix-public-summary.json'
        self.assertEqual(path_entries(package_uploads[1]), [summary])
        self.assertEqual(path_entries(qualify_uploads[1]), [summary])
        self.assertEqual(path_entries(manual_uploads[0]), [summary])
        self.assertIn("if: steps.package.outcome == 'success'", package_uploads[0])
        self.assertIn("if: always() && !cancelled()", qualify_uploads[0])
        self.assertNotIn("steps.qualify.outcome", qualify_uploads[0])
        for upload in package_uploads[1:] + qualify_uploads[1:] + manual_uploads:
            self.assertIn('if: always()', upload)
        self.assertIn("steps.package.outcome == 'success'", package_uploads[0])
        for upload in package_uploads + qualify_uploads + manual_uploads:
            self.assertNotRegex(upload.lower(), r'\.pfx|\.p12|\.key|wack\.xml|wack\.html|\.log')

    def test_synthetic_signature_diagnostic_is_guarded_and_exactly_allowlisted(self):
        text = workflow_text()
        qualify = job_block(text, 'synthetic-qualification')
        diagnostic = named_step(qualify, 'Upload synthetic signature diagnostic')
        self.assertIn("if: always() && steps.qualify.outcome == 'failure'", diagnostic)
        self.assertEqual(path_entries(diagnostic), [
            r'${{ runner.temp }}\cn-mon-msix-job\evidence\msix-signature-diagnostic.json',
        ])
        self.assertIn('if-no-files-found: ignore', diagnostic)
        self.assertNotRegex(diagnostic.lower(), r'\.pfx|\.p12|\.key|\.log|private')

        for golden_value in SYNTHETIC_IDENTITY.values():
            self.assertIn(golden_value, qualify)
        windows_test = (ROOT / 'scripts' / 'test-msix-windows.ps1').read_text(encoding='utf-8')
        for golden_value in SYNTHETIC_IDENTITY.values():
            self.assertIn(golden_value, windows_test)
        for guard in ('Test-CnMonMsixSyntheticPrDiagnostic', 'Save-CnMonMsixSignatureFailureEvidence',
                      'CurrentDiagnosticPhase', 'GITHUB_EVENT_NAME', 'pull_request',
                      'GITHUB_ACTIONS', 'ImageOS', 'msix-signature-diagnostic.json'):
            self.assertIn(guard, windows_test)

        self.assertNotIn('msix-signature-diagnostic-status.json', text)
        helper = CI_HELPER_PATH.read_text(encoding='utf-8')
        self.assertIn('function Write-CnMonMsixSignatureDiagnosticStatus', helper)
        for field in ('identity_publisher_env_matches', 'expected_pfn_matches', 'mode_user',
                      'phase', 'trust_presence_checked', 'trusted_certificate_present',
                      'operation', 'error_category', 'tool_name', 'catalog_footprint_present',
                      'sdk_exit_captured', 'private_log_retained', 'save_error_kind',
                      'public_diagnostic_written'):
            self.assertIn(field, helper)
        self.assertIn('MSIX signature diagnostic status: invalid', helper)
        hosted = re.search(r"(?ms)^    'Hosted' \{(.*?)^    'Certify' \{", helper).group(1)
        self.assertLess(hosted.index('Write-CnMonMsixSignatureDiagnosticStatus'),
                        hosted.index('hosted installed-package qualification did not pass'))

        manual = job_block(text, 'trusted-manual')
        self.assertEqual(len(upload_steps(manual)), 1)
        self.assertEqual(path_entries(upload_steps(manual)[0]), [
            r'${{ runner.temp }}\cn-mon-msix-job\summary\msix-public-summary.json',
        ])

    def test_build_creates_an_empty_output_directory_and_rejects_stale_contents(self):
        pwsh = shutil.which('pwsh')
        self.assertIsNotNone(pwsh, 'PowerShell 7 is required for the Windows workflow tests')
        source_revision = 'a' * 40
        archive_hash = '3e5ef313b4034202430256f14ac31bfdfc9ae4e7d2d23a9f4c74602cd6c74388'
        binary_hash = '8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d'
        release = {
            'archive_name': 'approved-release.zip',
            'package_version': '1.3.0.0',
            'cli_version': '0.3.0',
            'cli_revision': 'bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed',
            'archive_sha256': archive_hash,
            'binary_sha256': binary_hash,
        }
        with tempfile.TemporaryDirectory(prefix='cnmon-msix-build-') as temporary:
            root = Path(temporary) / 'repo'
            scripts = root / 'scripts'
            release_dir = root / 'packaging' / 'windows'
            scripts.mkdir(parents=True)
            release_dir.mkdir(parents=True)
            (scripts / 'msix-ci.ps1').write_text(CI_HELPER_PATH.read_text(encoding='utf-8'), encoding='utf-8')
            (release_dir / 'release.json').write_text(json.dumps(release), encoding='utf-8')
            wrapper = Path(temporary) / 'build-test.ps1'
            wrapper.write_text(r'''$ErrorActionPreference = 'Stop'
function Get-CimInstance { [pscustomobject]@{ Version = '10.0.26100.0' } }
function git { $global:LASTEXITCODE = 0; 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' }
function pwsh {
    if ($args[2] -like '*test-msix-package.ps1') { $global:LASTEXITCODE = 0; return }
    if ($args[2] -notlike '*package-msix.ps1') { throw 'unexpected child script' }
    $outputIndex = [Array]::IndexOf([object[]]$args, '-OutputDirectory')
    $output = [string]$args[$outputIndex + 1]
    if (@(Get-ChildItem -LiteralPath $output -Force).Count -ne 0) { throw 'packager did not receive an empty output directory' }
    [IO.File]::WriteAllText((Join-Path $env:RUNNER_TEMP 'packager-saw-empty-directory.txt'), 'true')
    $package = Join-Path $output 'cn-mon_1.3.0.0_x64.msix'
    [IO.File]::WriteAllBytes($package, [Text.Encoding]::UTF8.GetBytes('synthetic package bytes'))
    $receipt = [ordered]@{
        purpose = 'candidate'; package_version = '1.3.0.0'; cli_version = '0.3.0'
        cli_revision = 'bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed'
        packaging_revision = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
        archive_sha256 = '3e5ef313b4034202430256f14ac31bfdfc9ae4e7d2d23a9f4c74602cd6c74388'
        binary_sha256 = '8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d'
        name = $env:CNMON_MSIX_IDENTITY_NAME; publisher = $env:CNMON_MSIX_IDENTITY_PUBLISHER
        publisher_display_name = $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME
        expected_pfn = $env:CNMON_MSIX_EXPECTED_PFN
        package_sha256 = (Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    [IO.File]::WriteAllText((Join-Path $output 'msix-receipt.json'), (ConvertTo-Json $receipt -Compress))
    $global:LASTEXITCODE = 0
}
& (Join-Path $env:FAKE_REPOSITORY 'scripts/msix-ci.ps1') -Mode Build
if (-not (Test-Path -LiteralPath (Join-Path $env:RUNNER_TEMP 'packager-saw-empty-directory.txt'))) {
    throw 'packager was not called with the prepared output directory'
}
''', encoding='utf-8')
            environment = os.environ.copy()
            environment.update({
                'RUNNER_TEMP': str(Path(temporary) / 'runner'),
                'FAKE_REPOSITORY': str(root),
                'CNMON_MSIX_IDENTITY_NAME': SYNTHETIC_IDENTITY['name'],
                'CNMON_MSIX_IDENTITY_PUBLISHER': SYNTHETIC_IDENTITY['publisher'],
                'CNMON_MSIX_PUBLISHER_DISPLAY_NAME': SYNTHETIC_IDENTITY['publisher_display_name'],
                'CNMON_MSIX_EXPECTED_PFN': SYNTHETIC_IDENTITY['expected_pfn'],
            })
            result = subprocess.run(
                [pwsh, '-NoProfile', '-File', str(wrapper)],
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)

            occupied = Path(temporary) / 'runner-stale' / 'cn-mon-msix-job' / 'package'
            occupied.mkdir(parents=True)
            (occupied / 'stale.msix').write_bytes(b'stale output')
            environment['RUNNER_TEMP'] = str(occupied.parents[1])
            result = subprocess.run(
                [pwsh, '-NoProfile', '-File', str(wrapper)],
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('MSIX package output directory must be empty', result.stderr + result.stdout)
            self.assertFalse((occupied.parents[1] / 'packager-saw-empty-directory.txt').exists())

    def test_native_gate_requires_a_complete_passing_result_even_when_child_exits_zero(self):
        pwsh = shutil.which('pwsh')
        self.assertIsNotNone(pwsh, 'PowerShell 7 is required for the Windows workflow tests')
        gate_names = (
            'identity', 'package_hash', 'signature', 'account', 'package_registration',
            'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli',
            'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair',
            'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack',
        )
        valid_gates = {name: ('incomplete' if name == 'wack' else 'pass') for name in gate_names}
        valid = {'schema_version': 1, 'mode': 'Hosted', 'overall': 'pass', 'gates': valid_gates}
        empty = {**valid, 'gates': {}}
        incomplete_gates = dict(valid_gates, native_cli='incomplete')
        incomplete = {**valid, 'gates': incomplete_gates}
        failed_gates = dict(valid_gates, native_cli='fail')
        failed = {**valid, 'gates': failed_gates}
        wrong_mode = {
            **valid, 'mode': 'Certify', 'gates': {name: 'pass' for name in gate_names},
        }
        valid_certification = {
            'schema_version': 1, 'mode': 'Certify', 'overall': 'pass',
            'gates': {name: 'pass' for name in gate_names},
        }
        incomplete_certification = {
            **valid_certification,
            'gates': dict(valid_certification['gates'], wack='incomplete'),
        }
        fixtures = (
            ('Hosted', 'empty', empty, False),
            ('Hosted', 'incomplete', incomplete, False),
            ('Hosted', 'fail', failed, False),
            ('Hosted', 'wrong-mode', wrong_mode, False),
            ('Hosted', 'valid-full-map', valid, True),
            ('Certify', 'wrong-mode', valid, False),
            ('Certify', 'incomplete', incomplete_certification, False),
            ('Certify', 'valid-full-map', valid_certification, True),
        )
        release = {
            'archive_name': 'approved-release.zip',
            'package_version': '1.3.0.0',
            'cli_version': '0.3.0',
            'cli_revision': 'bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed',
            'archive_sha256': '3e5ef313b4034202430256f14ac31bfdfc9ae4e7d2d23a9f4c74602cd6c74388',
            'binary_sha256': '8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d',
        }
        source_revision = 'a' * 40
        unsigned_bytes = b'synthetic unsigned package'
        signed_bytes = b'synthetic signed package'
        with tempfile.TemporaryDirectory(prefix='cnmon-msix-native-gates-') as temporary:
            base = Path(temporary)
            root = base / 'repo'
            scripts = root / 'scripts'
            release_dir = root / 'packaging' / 'windows'
            scripts.mkdir(parents=True)
            release_dir.mkdir(parents=True)
            (scripts / 'msix-ci.ps1').write_text(CI_HELPER_PATH.read_text(encoding='utf-8'), encoding='utf-8')
            (release_dir / 'release.json').write_text(json.dumps(release), encoding='utf-8')
            wrapper = base / 'hosted-test.ps1'
            wrapper.write_text(r'''$ErrorActionPreference = 'Stop'
function git { $global:LASTEXITCODE = 0; 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' }
function pwsh {
    if ($args[2] -notlike '*test-msix-windows.ps1') { throw 'unexpected child script' }
    $receiptIndex = [Array]::IndexOf([object[]]$args, '-ReceiptPath')
    $artifactIndex = [Array]::IndexOf([object[]]$args, '-ArtifactDirectory')
    $modeIndex = [Array]::IndexOf([object[]]$args, '-Mode')
    $childMode = [string]$args[$modeIndex + 1]
    if ($childMode -eq 'Hosted') {
        $inputReceipt = Get-Content -LiteralPath ([string]$args[$receiptIndex + 1]) -Raw | ConvertFrom-Json
        $signedPackage = Join-Path ([string]$args[$artifactIndex + 1]) 'cn-mon_1.3.0.0_x64-signed.msix'
        [IO.File]::WriteAllBytes($signedPackage, [Convert]::FromBase64String($env:SIGNED_PACKAGE_BASE64))
        $signedHash = (Get-FileHash -LiteralPath $signedPackage -Algorithm SHA256).Hash.ToLowerInvariant()
        $signedReceipt = [ordered]@{
            purpose = 'candidate'; package_version = '1.3.0.0'; cli_version = '0.3.0'
            cli_revision = 'bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed'
            packaging_revision = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
            archive_sha256 = '3e5ef313b4034202430256f14ac31bfdfc9ae4e7d2d23a9f4c74602cd6c74388'
            binary_sha256 = '8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d'
            name = $env:CNMON_MSIX_IDENTITY_NAME; publisher = $env:CNMON_MSIX_IDENTITY_PUBLISHER
            publisher_display_name = $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME
            expected_pfn = $env:CNMON_MSIX_EXPECTED_PFN; package_sha256 = $signedHash
            signing = [ordered]@{
                status = 'test'; certificate_thumbprint = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
                unsigned_package_sha256 = $inputReceipt.package_sha256; signed_package_sha256 = $signedHash
            }
        }
        [IO.File]::WriteAllText((Join-Path ([string]$args[$artifactIndex + 1]) 'msix-signed-receipt.json'), (ConvertTo-Json $signedReceipt -Depth 4 -Compress))
    }
    [IO.File]::WriteAllText((Join-Path ([string]$args[$artifactIndex + 1]) 'msix-native-results.json'), $env:NATIVE_RESULT_JSON)
    $global:LASTEXITCODE = 0
}
& (Join-Path $env:FAKE_REPOSITORY 'scripts/msix-ci.ps1') -Mode $env:TEST_MODE -PackageDirectory input
''', encoding='utf-8')

            for case_index, (mode, fixture_name, results, should_pass) in enumerate(fixtures):
                runner = base / f'runner-{case_index}-{fixture_name}'
                input_dir = runner / 'cn-mon-msix-job' / 'input'
                input_dir.mkdir(parents=True)
                unsigned_path = input_dir / 'cn-mon_1.3.0.0_x64.msix'
                unsigned_path.write_bytes(unsigned_bytes)
                unsigned_hash = hashlib.sha256(unsigned_bytes).hexdigest()
                receipt = {
                    'purpose': 'candidate', **SYNTHETIC_IDENTITY,
                    'package_version': release['package_version'],
                    'cli_version': release['cli_version'],
                    'cli_revision': release['cli_revision'],
                    'packaging_revision': source_revision,
                    'archive_sha256': release['archive_sha256'],
                    'binary_sha256': release['binary_sha256'],
                    'package_sha256': unsigned_hash,
                }
                (input_dir / 'msix-receipt.json').write_text(json.dumps(receipt), encoding='utf-8')
                environment = os.environ.copy()
                evidence = runner / 'cn-mon-msix-job' / 'evidence'
                evidence.mkdir()
                output_path = runner / 'github-output.txt'
                environment.update({
                    'RUNNER_TEMP': str(runner),
                    'GITHUB_OUTPUT': str(output_path),
                    'TEST_MODE': mode,
                    'FAKE_REPOSITORY': str(root),
                    'CNMON_MSIX_IDENTITY_NAME': SYNTHETIC_IDENTITY['name'],
                    'CNMON_MSIX_IDENTITY_PUBLISHER': SYNTHETIC_IDENTITY['publisher'],
                    'CNMON_MSIX_PUBLISHER_DISPLAY_NAME': SYNTHETIC_IDENTITY['publisher_display_name'],
                    'CNMON_MSIX_EXPECTED_PFN': SYNTHETIC_IDENTITY['expected_pfn'],
                    'SIGNED_PACKAGE_BASE64': base64.b64encode(signed_bytes).decode('ascii'),
                    'NATIVE_RESULT_JSON': json.dumps(results),
                })
                result = subprocess.run(
                    [pwsh, '-NoProfile', '-File', str(wrapper)],
                    env=environment,
                    check=False,
                    capture_output=True,
                    text=True,
                )
                if should_pass:
                    self.assertEqual(result.returncode, 0, f'{mode}/{fixture_name}: {result.stderr}')
                else:
                    self.assertNotEqual(result.returncode, 0, f'{mode}/{fixture_name} was accepted')

    def test_allowlist_accepts_only_verified_synthetic_receipt_and_rejects_pfx(self):
        with tempfile.TemporaryDirectory(prefix='cnmon-msix-summary-') as temporary:
            root = Path(temporary)
            package = root / 'cn-mon_1.3.0.0_x64.msix'
            receipt_path = root / 'msix-receipt.json'
            summary = root / 'msix-public-summary.json'
            pfx = root / 'test-certificate.pfx'
            package.write_bytes(b'synthetic msix bytes')
            receipt = {
                **SYNTHETIC_IDENTITY,
                'schema_version': 1,
                'purpose': 'candidate',
                'package_version': '1.3.0.0',
                'package_sha256': hashlib.sha256(package.read_bytes()).hexdigest(),
            }
            receipt_path.write_text(json.dumps(receipt), encoding='utf-8')
            summary.write_text('{"schema_version":1}', encoding='utf-8')
            pfx.write_bytes(b'dummy private key candidate')
            allowlist = [package, receipt_path, summary]

            selected = collect_public_candidates(allowlist, allowlist, receipt_path)
            self.assertEqual(set(selected), {path.resolve() for path in allowlist})
            with self.assertRaisesRegex(ValueError, 'outside the public artifact allowlist'):
                collect_public_candidates([*allowlist, pfx], allowlist, receipt_path)
            receipt['publisher'] = 'CN=unapproved publisher'
            receipt_path.write_text(json.dumps(receipt), encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'synthetic fixture'):
                collect_public_candidates(allowlist, allowlist, receipt_path)
            receipt['publisher'] = SYNTHETIC_IDENTITY['publisher']
            receipt['package_sha256'] = 'wrong-package-hash'
            receipt_path.write_text(json.dumps(receipt), encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'package hash'):
                collect_public_candidates(allowlist, allowlist, receipt_path)

    def test_summary_helper_writes_only_enumerated_status_fields(self):
        pwsh = shutil.which('pwsh')
        self.assertIsNotNone(pwsh, 'PowerShell 7 is required for the Windows workflow tests')
        with tempfile.TemporaryDirectory(prefix='cnmon-msix-summary-') as temporary:
            environment = os.environ.copy()
            environment.update({
                'RUNNER_TEMP': temporary,
                'EVENT_NAME': 'workflow_dispatch',
                'JOB_STATUS': 'success',
                'PACKAGE_STATUS': 'success',
                'QUALIFICATION_STATUS': 'success',
                'WACK_STATUS': 'incomplete',
            })
            result = subprocess.run(
                [pwsh, '-NoProfile', '-File', str(CI_HELPER_PATH), '-Mode', 'Summary'],
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            summary_path = Path(temporary) / 'cn-mon-msix-job' / 'summary' / 'msix-public-summary.json'
            summary = json.loads(summary_path.read_text(encoding='utf-8'))
            self.assertEqual(set(summary), {
                'schema_version', 'event', 'job_status', 'package_status',
                'qualification_status', 'wack_status', 'qualification_overall',
                'native_evidence', 'gate_counts', 'gates', 'details_retention',
            })
            self.assertEqual(summary['wack_status'], 'incomplete')
            self.assertEqual(summary['qualification_overall'], 'not_available')
            self.assertEqual(summary['native_evidence'], 'missing')
            self.assertEqual(summary['gate_counts'], {'pass': 0, 'fail': 0, 'incomplete': 19})
            self.assertEqual(set(summary['gates'].values()), {'incomplete'})
            self.assertEqual(summary['details_retention'], 'job-local-only')
            for value in SYNTHETIC_IDENTITY.values():
                self.assertNotIn(value, summary_path.read_text(encoding='utf-8'))

    def test_summary_rejects_untrusted_results_and_counts_all_gates_incomplete(self):
        pwsh = shutil.which('pwsh')
        self.assertIsNotNone(pwsh, 'PowerShell 7 is required for the Windows workflow tests')
        with tempfile.TemporaryDirectory(prefix='cnmon-msix-summary-') as temporary:
            job = Path(temporary) / 'cn-mon-msix-job'
            evidence = job / 'evidence'
            evidence.mkdir(parents=True)
            gates = {
                name: ('incomplete' if name == 'wack' else 'pass')
                for name in (
                    'identity', 'package_hash', 'signature', 'account',
                    'package_registration', 'exact_identity', 'installed_binary_hash',
                    'alias_identity', 'native_cli', 'icmp', 'history_config',
                    'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall',
                    'conflicts', 'fail_injection', 'cleanup', 'wack',
                )
            }
            raw_results = {
                'schema_version': 1,
                'mode': 'Certify',
                'overall': 'incomplete',
                'gates': gates,
                'diagnostic': 'private runtime data must not be copied',
                'runtime_user_id': 'S-1-5-21-private',
            }
            (evidence / 'msix-native-results.json').write_text(json.dumps(raw_results), encoding='utf-8')
            environment = os.environ.copy()
            environment.update({
                'RUNNER_TEMP': temporary,
                'EVENT_NAME': 'pull_request',
                'JOB_STATUS': 'failure',
                'PACKAGE_STATUS': 'success',
                'QUALIFICATION_STATUS': 'success',
                'WACK_STATUS': 'incomplete',
            })
            result = subprocess.run(
                [pwsh, '-NoProfile', '-File', str(CI_HELPER_PATH), '-Mode', 'Summary'],
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            summary_path = job / 'summary' / 'msix-public-summary.json'
            summary_text = summary_path.read_text(encoding='utf-8')
            summary = json.loads(summary_text)
            self.assertEqual(summary['qualification_overall'], 'not_available')
            self.assertEqual(summary['native_evidence'], 'invalid')
            self.assertEqual(summary['gate_counts'], {'pass': 0, 'fail': 0, 'incomplete': 19})
            self.assertEqual(set(summary['gates'].values()), {'incomplete'})
            self.assertNotIn('diagnostic', summary_text)
            self.assertNotIn('runtime_user_id', summary_text)
            self.assertNotIn('S-1-5-21-private', summary_text)

    def test_summary_counts_all_valid_gate_results(self):
        pwsh = shutil.which('pwsh')
        self.assertIsNotNone(pwsh, 'PowerShell 7 is required for the Windows workflow tests')
        with tempfile.TemporaryDirectory(prefix='cnmon-msix-summary-') as temporary:
            job = Path(temporary) / 'cn-mon-msix-job'
            evidence = job / 'evidence'
            evidence.mkdir(parents=True)
            gates = {
                name: ('incomplete' if name == 'wack' else 'pass')
                for name in (
                    'identity', 'package_hash', 'signature', 'account',
                    'package_registration', 'exact_identity', 'installed_binary_hash',
                    'alias_identity', 'native_cli', 'icmp', 'history_config',
                    'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall',
                    'conflicts', 'fail_injection', 'cleanup', 'wack',
                )
            }
            results = {'schema_version': 1, 'mode': 'Hosted', 'overall': 'pass', 'gates': gates}
            (evidence / 'msix-native-results.json').write_text(json.dumps(results), encoding='utf-8')
            environment = os.environ.copy()
            environment.update({
                'RUNNER_TEMP': temporary,
                'EVENT_NAME': 'pull_request',
                'JOB_STATUS': 'success',
                'PACKAGE_STATUS': 'success',
                'QUALIFICATION_STATUS': 'success',
                'WACK_STATUS': 'incomplete',
            })
            result = subprocess.run(
                [pwsh, '-NoProfile', '-File', str(CI_HELPER_PATH), '-Mode', 'Summary'],
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            summary = json.loads((job / 'summary' / 'msix-public-summary.json').read_text(encoding='utf-8'))
            self.assertEqual(summary['qualification_overall'], 'pass')
            self.assertEqual(summary['native_evidence'], 'validated')
            self.assertEqual(summary['gate_counts'], {'pass': 18, 'fail': 0, 'incomplete': 1})
            self.assertEqual(summary['gates'], gates)

    def test_summary_preserves_valid_gates_and_marks_missing_gates_incomplete(self):
        pwsh = shutil.which('pwsh')
        self.assertIsNotNone(pwsh, 'PowerShell 7 is required for the Windows workflow tests')
        with tempfile.TemporaryDirectory(prefix='cnmon-msix-summary-partial-') as temporary:
            job = Path(temporary) / 'cn-mon-msix-job'
            evidence = job / 'evidence'
            evidence.mkdir(parents=True)
            results = {
                'schema_version': 1,
                'mode': 'Hosted',
                'overall': 'pass',
                'gates': {'identity': 'pass', 'package_hash': 'pass'},
            }
            (evidence / 'msix-native-results.json').write_text(json.dumps(results), encoding='utf-8')
            environment = os.environ.copy()
            environment.update({
                'RUNNER_TEMP': temporary,
                'EVENT_NAME': 'pull_request',
                'JOB_STATUS': 'failure',
                'PACKAGE_STATUS': 'success',
                'QUALIFICATION_STATUS': 'failure',
                'WACK_STATUS': 'incomplete',
            })
            result = subprocess.run(
                [pwsh, '-NoProfile', '-File', str(CI_HELPER_PATH), '-Mode', 'Summary'],
                env=environment,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            summary = json.loads((job / 'summary' / 'msix-public-summary.json').read_text(encoding='utf-8'))
            self.assertEqual(summary['native_evidence'], 'validated')
            self.assertEqual(summary['qualification_overall'], 'incomplete')
            self.assertEqual(summary['gates']['identity'], 'pass')
            self.assertEqual(summary['gates']['package_hash'], 'pass')
            self.assertEqual(summary['gate_counts'], {'pass': 2, 'fail': 0, 'incomplete': 17})


if __name__ == '__main__':
    unittest.main(verbosity=2)
