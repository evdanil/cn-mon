#!/usr/bin/env python3
"""Exercise release workflow guards offline using only Python and local Git."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import textwrap

ROOT = Path(__file__).resolve().parent.parent
WORKFLOW = (ROOT / '.github/workflows/release.yml').read_text(encoding='utf-8')


def extract_run(marker):
    start = WORKFLOW.index(marker)
    run_at = WORKFLOW.index('        run: |\n', start)
    body_start = run_at + len('        run: |\n')
    lines = WORKFLOW[body_start:].splitlines(keepends=True)
    body = []
    for line in lines:
        if line.strip() and len(line) - len(line.lstrip(' ')) < 10:
            break
        body.append(line[10:] if line.startswith('          ') else line)
    return ''.join(body)


def run(script, cwd, env):
    return subprocess.run(['bash', '-euo', 'pipefail', '-c', script], cwd=cwd,
                          env=env, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT)


selector = extract_run('      - id: select\n')
assembler = extract_run('      - name: Verify both native receipts and assemble the exact three assets\n')
publish_check = extract_run('      - name: Revalidate receipts and asset bytes\n')

with tempfile.TemporaryDirectory(prefix='cnmon-workflow-') as temp:
    base = Path(temp)
    bare, seed, clone = base / 'origin.git', base / 'seed', base / 'clone'
    subprocess.run(['git', 'init', '--bare', '--initial-branch=main', str(bare)], check=True, capture_output=True)
    subprocess.run(['git', 'clone', str(bare), str(seed)], check=True, capture_output=True)
    subprocess.run(['git', '-C', str(seed), 'config', 'user.name', 'Fixture'], check=True)
    subprocess.run(['git', '-C', str(seed), 'config', 'user.email', 'fixture@example.invalid'], check=True)
    (seed / 'source.txt').write_text('fixture\n', encoding='utf-8')
    subprocess.run(['git', '-C', str(seed), 'add', 'source.txt'], check=True)
    subprocess.run(['git', '-C', str(seed), 'commit', '-m', 'fixture'], check=True, capture_output=True)
    subprocess.run(['git', '-C', str(seed), 'push', 'origin', 'main'], check=True, capture_output=True)
    revision = subprocess.check_output(['git', '-C', str(seed), 'rev-parse', 'HEAD'], text=True).strip()
    subprocess.run(['git', 'clone', '--branch', 'main', str(bare), str(clone)], check=True, capture_output=True)
    output = base / 'github-output'

    def select(**overrides):
        output.write_text('', encoding='utf-8')
        env = os.environ.copy()
        env.update({'GITHUB_OUTPUT': str(output), 'EVENT_NAME': 'workflow_dispatch',
                    'PR_REVISION': '', 'PR_REPOSITORY': '', 'INPUT_REVISION': revision,
                    'INPUT_VERSION': '0.2.0', 'PUBLISH': 'false',
                    'REPOSITORY': 'fixture/repo', 'WORKFLOW_REF': 'refs/heads/main'})
        env.update(overrides)
        return run(selector, clone, env), output.read_text(encoding='utf-8')

    result, outputs = select()
    assert result.returncode == 0 and f'revision={revision}' in outputs, result.stdout
    result, _ = select(INPUT_VERSION='0.1.0')
    assert result.returncode != 0 and 'unsupported release version' in result.stdout, result.stdout
    print('source selector rejects obsolete 0.1.0 release version: pass')
    subprocess.run(['git', '-C', str(clone), 'remote', 'set-url', 'origin',
                    str(base / 'missing-private-origin.git')], check=True)
    result, outputs = select()
    assert result.returncode == 0 and f'revision={revision}' in outputs, result.stdout
    print('source selector accepts fetched main ancestry without unauthenticated refetch: pass')
    result, _ = select(INPUT_REVISION='a' * 40)
    assert result.returncode != 0 and 'Not a valid object name' in result.stdout, result.stdout
    print('source selector rejects uncommitted SHA: pass')
    result, _ = select(EVENT_NAME='pull_request', PR_REVISION=revision,
                       PR_REPOSITORY='fixture/fork', PUBLISH='true')
    assert result.returncode != 0 and 'pull requests cannot publish' in result.stdout, result.stdout
    print('source selector rejects pull-request publication: pass')
    result, _ = select(PUBLISH='true', WORKFLOW_REF='refs/heads/feature')
    assert result.returncode != 0 and 'publication must be dispatched from main' in result.stdout, result.stdout
    print('source selector rejects publication dispatch off main: pass')
    subprocess.run(['git', '-C', str(clone), 'update-ref', '-d', 'refs/remotes/origin/main'], check=True)
    result, _ = select()
    assert result.returncode != 0 and 'checkout did not fetch origin/main' in result.stdout, result.stdout
    print('source selector rejects checkout missing fetched main ancestry ref: pass')

filenames = ['cn-mon_0.2.0_linux_amd64.tar.gz', 'cn-mon_0.2.0_windows_amd64.zip']


def populate_qualifications(root, mutate=False, missing=False, mixed_version=False):
    for target, name, dirname in [('linux_amd64', filenames[0], 'linux'),
                                 ('windows_amd64', filenames[1], 'windows')]:
        directory = root / dirname
        directory.mkdir(parents=True)
        archive = directory / name
        archive.write_bytes((target + ' bytes').encode())
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        version = '0.1.0' if mixed_version and dirname == 'windows' else '0.2.0'
        receipt = {'version': version, 'revision': 'a' * 40, 'target': target,
                   'archive': name, 'archive_sha256': digest}
        if not (missing and dirname == 'windows'):
            (directory / 'qualification.json').write_text(json.dumps(receipt), encoding='utf-8')
    if mutate:
        (root / 'linux' / filenames[0]).write_bytes(b'changed')


def assemble_fixture(root, **options):
    populate_qualifications(root, **options)
    env = os.environ.copy()
    env.update({'REVISION': 'a' * 40, 'VERSION': '0.2.0',
                'LINUX_DIR': str(root / 'linux'), 'WINDOWS_DIR': str(root / 'windows'),
                'ASSET_DIR': str(root / 'assets')})
    return run(assembler, ROOT, env)

with tempfile.TemporaryDirectory(prefix='cnmon-assets-') as temp:
    root = Path(temp)
    result = assemble_fixture(root)
    assert result.returncode == 0, result.stdout
    assert sorted(p.name for p in (root / 'assets').iterdir()) == sorted(filenames + ['SHA256SUMS'])
    print('asset assembler accepts exactly two receipt-matched archives: pass')
with tempfile.TemporaryDirectory(prefix='cnmon-assets-mutated-') as temp:
    result = assemble_fixture(Path(temp), mutate=True)
    assert result.returncode != 0 and 'receipt does not match' in result.stdout, result.stdout
    print('asset assembler rejects archive changed after receipt: pass')
with tempfile.TemporaryDirectory(prefix='cnmon-assets-missing-') as temp:
    result = assemble_fixture(Path(temp), missing=True)
    assert result.returncode != 0 and 'missing required' in result.stdout, result.stdout
    print('asset assembler rejects missing platform receipt: pass')
with tempfile.TemporaryDirectory(prefix='cnmon-assets-mixed-version-') as temp:
    root = Path(temp)
    result = assemble_fixture(root, mixed_version=True)
    assert result.returncode != 0 and 'receipt does not match' in result.stdout, result.stdout
    print('asset assembler rejects mixed 0.2.0/0.1.0 receipts: pass')


def publication_fixture(root, tamper=False):
    assets, linux, windows = root / 'assets', root / 'linux', root / 'windows'
    assets.mkdir(); linux.mkdir(); windows.mkdir()
    lines = []
    for target, name, directory in [('linux_amd64', filenames[0], linux),
                                    ('windows_amd64', filenames[1], windows)]:
        data = (target + ' bytes').encode()
        (directory / name).write_bytes(data); (assets / name).write_bytes(data)
        digest = hashlib.sha256(data).hexdigest()
        lines.append(f'{digest}  {name}')
        (directory / 'qualification.json').write_text(json.dumps({
            'version': '0.2.0', 'revision': 'a' * 40, 'target': target,
            'archive': name, 'archive_sha256': digest}), encoding='utf-8')
    lines.sort(key=lambda value: value.split('  ', 1)[1])
    (assets / 'SHA256SUMS').write_text('\n'.join(lines) + '\n', encoding='ascii')
    if tamper:
        (assets / filenames[1]).write_bytes(b'tampered')
    env = os.environ.copy()
    env.update({'REVISION': 'a' * 40, 'VERSION': '0.2.0', 'ASSET_DIR': str(assets),
                'LINUX_DIR': str(linux), 'WINDOWS_DIR': str(windows)})
    return run(publish_check, ROOT, env)

with tempfile.TemporaryDirectory(prefix='cnmon-publication-') as temp:
    result = publication_fixture(Path(temp))
    assert result.returncode == 0, result.stdout
    print('publication input guard accepts sorted checksums and exact receipts: pass')
with tempfile.TemporaryDirectory(prefix='cnmon-publication-tampered-') as temp:
    result = publication_fixture(Path(temp), tamper=True)
    assert result.returncode != 0 and 'digest mismatch' in result.stdout, result.stdout
    print('publication input guard rejects tampered archive bytes: pass')

# Reproduce the pack/verify receipt mismatch regression against the actual
# qualification script's embedded Python guard, including the extracted bytes.
wrapper = (ROOT / 'scripts/qualify-release-linux.sh').read_text(encoding='utf-8')
start = wrapper.index("python3 - \"$pack_receipt\"")
start = wrapper.index("<<'PY'\n", start) + len("<<'PY'\n")
end = wrapper.index('\nPY\n', start)
receipt_guard = textwrap.dedent(wrapper[start:end])
with tempfile.TemporaryDirectory(prefix='cnmon receipt fixture ') as temp:
    root = Path(temp)
    built_binary, extracted_binary, archive = root / 'built input' / 'cn-mon', root / 'fresh extraction' / 'cn-mon', root / 'archive'
    built_binary.parent.mkdir(); extracted_binary.parent.mkdir()
    built_binary.write_bytes(b'extracted executable'); extracted_binary.write_bytes(b'extracted executable'); archive.write_bytes(b'archive')
    binary_sha = hashlib.sha256(extracted_binary.read_bytes()).hexdigest()
    archive_sha = hashlib.sha256(archive.read_bytes()).hexdigest()
    pack = {'version': '0.2.0', 'revision': 'a' * 40, 'target': 'linux_amd64',
            'archive_sha256': archive_sha, 'binary_path': str(built_binary), 'binary_sha256': '0' * 64}
    verify = dict(pack, binary_path=str(extracted_binary), binary_sha256=binary_sha)
    pack_path, verify_path = root / 'pack.json', root / 'verify.json'
    pack_path.write_text(json.dumps(pack)); verify_path.write_text(json.dumps(verify))
    args = [str(pack_path), str(verify_path), str(archive), str(built_binary), str(extracted_binary), 'a' * 40]
    result = subprocess.run(['python3', '-c', receipt_guard, *args], text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    assert result.returncode != 0 and 'pack binary receipt hash: FAIL' in result.stdout, result.stdout
    print('receipt guard rejects pack binary digest inconsistent with extracted bytes: pass')
    pack['binary_sha256'] = binary_sha
    pack_path.write_text(json.dumps(pack))
    result = subprocess.run(['python3', '-c', receipt_guard, *args], text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    assert result.returncode == 0, result.stdout
    print('receipt guard accepts matching pack/verify/extracted binary hashes and paths: pass')

# Exercise checkout-index under core.autocrlf=true; explicit eol=lf must keep
# each distributed document's checked-out bytes equal to its Git blob.
with tempfile.TemporaryDirectory(prefix='cnmon-docbytes-') as temp:
    root = Path(temp)
    subprocess.run(['git', 'init', str(root)], check=True, capture_output=True)
    subprocess.run(['git', '-C', str(root), 'config', 'user.name', 'Fixture'], check=True)
    subprocess.run(['git', '-C', str(root), 'config', 'user.email', 'fixture@example.invalid'], check=True)
    shutil.copyfile(ROOT / '.gitattributes', root / '.gitattributes')
    for name in ['README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md']:
        (root / name).write_bytes((name + '\nline\n').encode())
    subprocess.run(['git', '-C', str(root), 'add', '.'], check=True)
    subprocess.run(['git', '-C', str(root), 'commit', '-m', 'fixture'], check=True, capture_output=True)
    for name in ['README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md']:
        (root / name).unlink()
    subprocess.run(['git', '-C', str(root), '-c', 'core.autocrlf=true', '-c', 'core.eol=lf',
                    'checkout-index', '--force', '--', 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md'], check=True)
    for name in ['README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md']:
        expected = subprocess.check_output(['git', '-C', str(root), 'rev-parse', f'HEAD:{name}'], text=True).strip()
        actual = subprocess.check_output(['git', '-C', str(root), 'hash-object', '--no-filters', '--', name], text=True).strip()
        assert actual == expected, name
    print('Windows-style autocrlf checkout preserves all distributed text blobs: pass')

# The README's Linux example must stop before extraction when the checksum
# verifier reports a mismatch.
readme = (ROOT / 'README.md').read_text(encoding='utf-8')
linux_example_start = readme.index('```sh\n', readme.index('On Linux, for a cn-mon 0.2.0 archive:')) + len('```sh\n')
linux_example_end = readme.index('\n```', linux_example_start)
linux_example = readme[linux_example_start:linux_example_end]
with tempfile.TemporaryDirectory(prefix='cnmon-readme-checksum-') as temp:
    root = Path(temp); fake_bin = root / 'bin'; fake_bin.mkdir()
    (root / 'SHA256SUMS').write_text('0' * 64 + '  cn-mon_0.2.0_linux_amd64.tar.gz\n', encoding='ascii')
    (root / 'cn-mon_0.2.0_linux_amd64.tar.gz').write_bytes(b'not the expected archive')
    invoked = root / 'sha256sum.invoked'
    sha = fake_bin / 'sha256sum'; sha.write_text(f'#!/bin/sh\nprintf called > {invoked}\ncat >/dev/null\nexit 1\n', encoding='ascii'); sha.chmod(0o755)
    extracted = root / 'extracted.marker'
    tar = fake_bin / 'tar'; tar.write_text(f'#!/bin/sh\nprintf called > {extracted}\n', encoding='ascii'); tar.chmod(0o755)
    env = os.environ.copy(); env['PATH'] = str(fake_bin) + os.pathsep + env.get('PATH', '')
    result = subprocess.run(['bash', '-c', linux_example], cwd=root, env=env,
                            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    assert result.returncode != 0 and invoked.exists() and not extracted.exists(), result.stdout
    print('README 0.2.0 checksum instructions invoke verification and stop before extraction on failure: pass')

# Match the PowerShell wrapper's normalized `go version -m` setting guard
# against an actual Windows-target Go build, not hand-written example lines.
with tempfile.TemporaryDirectory(prefix='cnmon-buildinfo-') as temp:
    build_root = Path(temp) / 'clean-source'
    subprocess.run(['git', 'clone', '--no-hardlinks', str(ROOT), str(build_root)],
                   check=True, capture_output=True)
    executable = Path(temp) / 'cn-mon.exe'
    build_env = os.environ.copy()
    build_env.update({'CGO_ENABLED': '0', 'GOOS': 'windows', 'GOARCH': 'amd64',
                      'GOAMD64': 'v1', 'GOFLAGS': '-mod=readonly', 'GOTOOLCHAIN': 'local'})
    subprocess.run(['go', 'build', '-trimpath', '-buildvcs=true', '-mod=readonly',
                    '-o', str(executable), './cmd/cn-mon'], cwd=build_root, env=build_env, check=True)
    build_info = subprocess.check_output(['go', 'version', '-m', str(executable)],
                                         cwd=build_root, env=build_env, text=True)
    normalized = [line.strip() for line in build_info.splitlines()]
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=build_root, text=True).strip()
    for setting in [f'vcs.revision={revision}', 'vcs.modified=false', 'GOOS=windows',
                    'GOARCH=amd64', 'CGO_ENABLED=0']:
        assert any(re.fullmatch(r'build\s+' + re.escape(setting), line) for line in normalized), setting
    print('Windows-target Go build metadata characterization accepts actual tab-prefixed settings: pass (PowerShell execution remains hosted-only)')

print('CNMON_RELEASE_WORKFLOW_GUARDS source=pass assets=pass publication=pass receipts=pass docbytes=pass')
