#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

fail() { printf 'release qualification: %s\n' "$*" >&2; exit 1; }
version=${CNMON_RELEASE_VERSION:-}
revision=${CNMON_RELEASE_REVISION:-}
[[ $version == 0.1.0 ]] || fail 'CNMON_RELEASE_VERSION must be 0.1.0'
[[ $revision =~ ^[0-9a-f]{40}$ ]] || fail 'CNMON_RELEASE_REVISION must be a full lowercase 40-hex commit'
[[ $(git rev-parse HEAD) == "$revision" ]] || fail 'checked out HEAD does not match CNMON_RELEASE_REVISION'
[[ -z $(git status --porcelain --untracked-files=all) ]] || fail 'source checkout is not clean'

for tool in go python3 readelf sha256sum; do
  command -v "$tool" >/dev/null || fail "required tool is missing: $tool"
done
[[ $(go version) == 'go version go1.27.2 '* ]] || fail 'qualification requires Go 1.27.2'

export GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0 GOAMD64=v1
export CNMON_RELEASE_QUALIFY=1 CNMON_NATIVE_COMMAND=1
export CNMON_RELEASE_VERSION=$version CNMON_RELEASE_REVISION=$revision

artifact_dir=${CNMON_RELEASE_ARTIFACT_DIR:-${RUNNER_TEMP:-/tmp}/cn-mon-release-linux}
artifact_dir=$(mkdir -p "$artifact_dir" && cd "$artifact_dir" && pwd)
temp_root=${RUNNER_TEMP:-/tmp}
work_dir=$(mktemp -d "$temp_root/cn-mon-release-linux.XXXXXX")
mkdir -p "$work_dir/cn mon release linux"
work_dir="$work_dir/cn mon release linux"
mkdir -p "$work_dir/source" "$work_dir/extracted with spaces"
binary="$work_dir/source/cn-mon"
archive_dir="$artifact_dir"

commit_unix=$(git show -s --format=%ct "$revision")
[[ $commit_unix =~ ^[0-9]+$ ]] || fail 'selected commit has invalid timestamp'
[[ ${CNMON_RELEASE_COMMIT_UNIX:-} == "$commit_unix" ]] || fail 'workflow commit timestamp does not match the selected checkout'
go mod verify
module_list="$artifact_dir/production-import-modules.txt"
go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/cn-mon | LC_ALL=C sort -u > "$module_list"
main_module=$(go list -m)
notice_inventory=$(awk '/^## Go module dependency inventory$/{inside=1; next} /^The full module graph/{inside=0} inside{print}' THIRD_PARTY_NOTICES.md)
while IFS= read -r module; do
  [[ -n $module && $module != "$main_module" ]] || continue
  grep -Fq "| \`$module\` |" <<<"$notice_inventory" || fail "production import module is missing from notices: $module"
done < "$module_list"
ldflags="-X main.version=$version -X main.revision=$revision"
go build -trimpath -buildvcs=true -mod=readonly -ldflags "$ldflags" -o "$binary" ./cmd/cn-mon
[[ -x $binary ]] || fail 'native product build did not produce an executable'

pack_receipt="$artifact_dir/pack-receipt.json"
go run ./scripts/release-artifact pack -binary "$binary" -source-root "$repo_root" -out-dir "$archive_dir" -version "$version" -revision "$revision" -commit-unix "$commit_unix" -target linux_amd64 > "$pack_receipt"
archive="$archive_dir/cn-mon_0.1.0_linux_amd64.tar.gz"
[[ -f $archive ]] || fail 'pack did not create the expected archive'
archive_sha=$(sha256sum "$archive" | cut -d' ' -f1)

verify_receipt="$artifact_dir/verify-receipt.json"
extract_dir="$work_dir/extracted with spaces"
go run ./scripts/release-artifact verify -archive "$archive" -sha256 "$archive_sha" -source-root "$repo_root" -extract-dir "$extract_dir" -version "$version" -revision "$revision" -target linux_amd64 > "$verify_receipt"
extracted="$extract_dir/cn-mon_0.1.0_linux_amd64/cn-mon"
[[ -x $extracted ]] || fail 'verified extraction did not produce an executable'

python3 - "$pack_receipt" "$verify_receipt" "$archive" "$binary" "$extracted" "$revision" <<'PY'
import hashlib, json, pathlib, sys
pack_path, verify_path, archive, built_binary, extracted_binary, revision = sys.argv[1:]
pack = json.loads(pathlib.Path(pack_path).read_text())
verify = json.loads(pathlib.Path(verify_path).read_text())
def digest(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()
checks = {
    'pack version/revision/target': (pack['version'], pack['revision'], pack['target']) == ('0.1.0', revision, 'linux_amd64'),
    'verify identity': (verify['version'], verify['revision'], verify['target']) == ('0.1.0', revision, 'linux_amd64'),
    'archive receipt hash': pack['archive_sha256'] == digest(archive) == verify['archive_sha256'],
    'pack binary receipt hash': pack['binary_sha256'] == digest(pathlib.Path(pack['binary_path'])) == digest(built_binary),
    'pack binary path': pathlib.Path(pack['binary_path']).resolve() == pathlib.Path(built_binary).resolve(),
    'verify binary receipt hash': verify['binary_sha256'] == digest(extracted_binary) == pack['binary_sha256'],
    'verified binary path': pathlib.Path(verify['binary_path']).resolve() == pathlib.Path(extracted_binary).resolve(),
}
for name, ok in checks.items():
    print(f'{name}: {"pass" if ok else "FAIL"}')
if not all(checks.values()):
    raise SystemExit(1)
PY

go version -m "$extracted" > "$artifact_dir/go-build-info.txt"
grep -Fq "vcs.revision=$revision" "$artifact_dir/go-build-info.txt" || fail 'extracted build info has the wrong source revision'
grep -Fq 'vcs.modified=false' "$artifact_dir/go-build-info.txt" || fail 'extracted build info is dirty'
grep -Fq 'GOOS=linux' "$artifact_dir/go-build-info.txt" || fail 'extracted build info has the wrong GOOS'
grep -Fq 'GOARCH=amd64' "$artifact_dir/go-build-info.txt" || fail 'extracted build info has the wrong GOARCH'
grep -Fq 'CGO_ENABLED=0' "$artifact_dir/go-build-info.txt" || fail 'extracted build info has CGO enabled'
readelf -l "$extracted" > "$artifact_dir/elf-program-headers.txt"
if grep -Eq 'INTERP|Requesting program interpreter' "$artifact_dir/elf-program-headers.txt"; then
  fail 'CGO-free Linux executable unexpectedly has a dynamic interpreter'
fi

version_file="$artifact_dir/version-output.txt"
if ! "$extracted" --version > "$version_file"; then fail 'extracted --version failed'; fi
printf 'cn-mon %s (%s)\n' "$version" "$revision" > "$work_dir/expected-version.txt"
cmp -s "$work_dir/expected-version.txt" "$version_file" || fail 'extracted --version output does not match exact release identity'

export CNMON_NATIVE_COMMAND_BINARY="$extracted"
export CNMON_RELEASE_EXPECTED_SHA256=$(sha256sum "$extracted" | cut -d' ' -f1)
export CNMON_NATIVE_COMMAND_ARTIFACT_DIR="$artifact_dir/native-command"
export CNMON_NATIVE_ARTIFACT_DIR="$artifact_dir/native-probe"

scripts/test-native-linux.sh
scripts/test-native-command-linux.sh

extract_permission_marker() {
  local role=$1 log="$artifact_dir/native-command/linux-command-permission-$1.log" count
  count=$(grep -Ec 'CNMON_NATIVE_COMMAND_LINUX_PERMISSION uid=[0-9]+ euid=[0-9]+ cap_net_raw_eff=(true|false) cap_net_raw_prm=(true|false)( cap_net_raw_amb=(true|false))? role=(granted|denied) icmp=(OK|ERROR) exit=[0-9]+ tcp=open history=finished-round' "$log" || true)
  [[ $count == 1 ]] || fail "expected exactly one actual Linux $role permission-role marker"
  grep -Eo 'CNMON_NATIVE_COMMAND_LINUX_PERMISSION uid=[0-9]+ euid=[0-9]+ cap_net_raw_eff=(true|false) cap_net_raw_prm=(true|false)( cap_net_raw_amb=(true|false))? role=(granted|denied) icmp=(OK|ERROR) exit=[0-9]+ tcp=open history=finished-round' "$log"
}
linux_granted_role=$(extract_permission_marker granted)
linux_denied_role=$(extract_permission_marker denied)
[[ $linux_granted_role == *'uid=0 euid=0 cap_net_raw_eff=true cap_net_raw_prm=true'*'role=granted icmp=OK exit=0 tcp=open history=finished-round' ]] || fail 'actual granted-role marker has an unexpected outcome'
[[ $linux_denied_role == *'role=denied icmp=ERROR exit=3 tcp=open history=finished-round' ]] || fail 'actual denied-role marker has an unexpected outcome'
[[ $linux_denied_role == *'cap_net_raw_eff=false cap_net_raw_prm=false cap_net_raw_amb=false'* ]] || fail 'actual denied-role marker does not prove capabilities are absent'

archive_after=$(sha256sum "$archive" | cut -d' ' -f1)
binary_after=$(sha256sum "$extracted" | cut -d' ' -f1)
[[ $archive_after == "$archive_sha" ]] || fail 'archive changed during native qualification'
[[ $binary_after == "$CNMON_RELEASE_EXPECTED_SHA256" ]] || fail 'extracted binary changed during native qualification'

python3 - "$artifact_dir" "$archive" "$extracted" "$revision" "$commit_unix" "$archive_sha" "$binary_after" "$linux_granted_role" "$linux_denied_role" <<'PY'
import json, os, pathlib, platform, subprocess, sys
out, archive, binary, revision, commit_unix, archive_sha, binary_sha, linux_granted_role, linux_denied_role = sys.argv[1:]
summary = {
    'version': '0.1.0', 'revision': revision, 'target': 'linux_amd64',
    'commit_unix': int(commit_unix), 'archive': os.path.basename(archive),
    'archive_sha256': archive_sha, 'binary_sha256': binary_sha,
    'binary_path': os.path.abspath(binary), 'go': subprocess.check_output(['go', 'version'], text=True).strip(),
    'os': platform.platform(), 'machine': platform.machine(),
    'linux_granted_role': linux_granted_role,
    'linux_denied_role': linux_denied_role,
    'native_probe_log': 'native-probe/linux-harness.log',
    'native_command_log': 'native-command/linux-command-native.log',
    'linux_granted_role_log': 'native-command/linux-command-permission-granted.log',
    'linux_denied_role_log': 'native-command/linux-command-permission-denied.log',
}
pathlib.Path(out, 'qualification.json').write_text(json.dumps(summary, sort_keys=True, indent=2) + '\n')
PY
printf 'CNMON_RELEASE_QUALIFICATION target=linux_amd64 revision=%s archive_sha256=%s binary_sha256=%s native_probe=pass native_command=pass immutable=pass\n' "$revision" "$archive_sha" "$binary_after"
