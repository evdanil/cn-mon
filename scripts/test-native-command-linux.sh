#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
artifact_dir=${CNMON_NATIVE_COMMAND_ARTIFACT_DIR:-${RUNNER_TEMP:-/tmp}/cn-mon-native-command-linux}
mkdir -p "$artifact_dir"
artifact_dir=$(cd "$artifact_dir" && pwd)
command_binary="$artifact_dir/cn-mon"
test_binary="$artifact_dir/cn-mon-command.test"
main_log="$artifact_dir/linux-command-native.log"
cd "$repo_root"

for tool in go sudo setpriv getcap; do
	command -v "$tool" >/dev/null || { echo "required native command test tool missing: $tool" >&2; exit 1; }
done

CGO_ENABLED=0 go build -o "$command_binary" ./cmd/cn-mon
CGO_ENABLED=0 go test -c -o "$test_binary" ./cmd/cn-mon
chmod 755 "$command_binary" "$test_binary"
if [[ -n "$(getcap -n "$command_binary")" || -n "$(getcap -n "$test_binary")" ]]; then
	message='fresh native command test executables unexpectedly carry file capabilities'
	echo "::error title=cn-mon Linux permission setup::$message" >&2
	exit 1
fi

set +e
CGO_ENABLED=0 CNMON_NATIVE_COMMAND=1 CNMON_NATIVE_COMMAND_BINARY="$command_binary" \
	go test -tags native -v -timeout=10m -run '^TestNativeCommand(LoopbackHistoryJSON|LinuxPTYQuitRestoresTerminal|LinuxPTYKeysRemainActiveWithRedirectedStderr|LinuxRestoresScreenBeforeBlockedFinalReport|LinuxPTYForegroundSuspendLifecycle|LinuxPTYLossWithInheritedHangupIgnored|LinuxPTYHardInterruptRestoresWhileUIWritesBlocked|LinuxPTYBackgroundCleanup|INIHistoryRetention|PreInputInterrupt|RunSignalsAndSecondInterrupt|PreRunTerminationAndInheritedIgnores|PauseResume|ClosedStdoutPipeIsQuiet)$' ./cmd/cn-mon >"$main_log" 2>&1
result=$?
set -e
cat "$main_log"
if [[ $result -ne 0 ]]; then
	failure_context=$(awk '
		/^=== RUN[[:space:]]+/ { current_test = $3 }
		/^[[:space:]]+[^[:space:]]+\.go:[0-9]+:/ && $0 !~ /:[[:space:]]*CNMON_[A-Za-z0-9_]+([[:space:]]|$)/ {
			if (diagnostic_count[current_test] == 5) {
				for (i = 1; i < 5; i++) diagnostic[current_test, i] = diagnostic[current_test, i + 1]
				diagnostic_count[current_test] = 4
			}
			diagnostic[current_test, ++diagnostic_count[current_test]] = $0
		}
		/^[[:space:]]*--- FAIL:/ {
			failed_test = $3
			for (i = 1; i <= diagnostic_count[failed_test]; i++) {
				key = failed_test SUBSEP i
				if (key in diagnostic) print diagnostic[key]
			}
			print
			failures++
			if (failures == 5) exit
		}
	' "$main_log" | cut -c1-1200 || true)
	result_context=$({ grep -E '^--- (FAIL|PASS):|^FAIL |^CNMON_' "$main_log" || true; } | tail -n 60 | cut -c1-300)
	if [[ -z $failure_context ]]; then
		failure_context=$(tail -n 40 "$main_log" | cut -c1-300)
	fi
	message="native Linux command gate failed (exit $result); initiating failures: ${failure_context}; result markers: ${result_context}"
	message=${message//'%'/'%25'}
	message=${message//$'\r'/'%0D'}
	message=${message//$'\n'/'%0A'}
	echo "::error title=cn-mon native Linux command::$message" >&2
	exit "$result"
fi

required=(
	'--- PASS: TestNativeCommandLoopbackHistoryJSON'
	'--- PASS: TestNativeCommandLinuxPTYQuitRestoresTerminal'
	'--- PASS: TestNativeCommandLinuxPTYKeysRemainActiveWithRedirectedStderr'
	'--- PASS: TestNativeCommandLinuxRestoresScreenBeforeBlockedFinalReport'
	'--- PASS: TestNativeCommandLinuxPTYForegroundSuspendLifecycle'
	'--- PASS: TestNativeCommandLinuxPTYLossWithInheritedHangupIgnored'
	'--- PASS: TestNativeCommandLinuxPTYHardInterruptRestoresWhileUIWritesBlocked'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/normal/tostop=false'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/normal/tostop=true'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/graceful/tostop=false'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/graceful/tostop=true'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/report-error/tostop=false'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/report-error/tostop=true'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/hard/tostop=false'
	'--- PASS: TestNativeCommandLinuxPTYBackgroundCleanup/hard/tostop=true'
	'--- PASS: TestNativeCommandINIHistoryRetention'
	'--- PASS: TestNativeCommandPreInputInterrupt'
	'--- PASS: TestNativeCommandRunSignalsAndSecondInterrupt'
	'--- PASS: TestNativeCommandPreRunTerminationAndInheritedIgnores'
	'--- PASS: TestNativeCommandPauseResume'
	'--- PASS: TestNativeCommandClosedStdoutPipeIsQuiet'
	'CNMON_NATIVE_COMMAND_RESULT'
	'CNMON_NATIVE_COMMAND_CONFIG ini90=retained cli30=pruned-old-seed recent-round-retained history-path=ini log-path=ini'
	'CNMON_NATIVE_COMMAND_PREINPUT_INTERRUPT exit=130 side-effects=none'
	'CNMON_NATIVE_COMMAND_SECOND_INTERRUPT exit=130 finalizer=write-locked'
	'CNMON_NATIVE_COMMAND_RUN_SIGNAL signal=interrupt '
	'CNMON_NATIVE_COMMAND_RUN_SIGNAL signal=terminated '
	'CNMON_NATIVE_COMMAND_RUN_SIGNAL signal=hangup '
	'CNMON_NATIVE_COMMAND_PRE_RUN_SIGNAL signal=terminated disposition=default-propagated'
	'CNMON_NATIVE_COMMAND_PRE_RUN_SIGNAL signal=hangup disposition=default-propagated'
	'CNMON_NATIVE_COMMAND_INHERITED_IGNORE signal=hangup result=preserved'
	'CNMON_NATIVE_COMMAND_INHERITED_IGNORE signal=interrupt result=preserved'
	'CNMON_NATIVE_COMMAND_INHERITED_IGNORE signal=hangup active-run=preserved'
	'CNMON_NATIVE_COMMAND_INHERITED_TERM pre-input=runtime-default'
	'CNMON_NATIVE_COMMAND_INHERITED_TERM active-run=graceful history=stopped'
	'CNMON_NATIVE_COMMAND_PAUSE_RESUME paused=no-round-growth resumed=next-round'
	'CNMON_NATIVE_COMMAND_LINUX_PTY keys=q-v-escape-arrow-unicode resize=tiny-restored no-color=attrs-only report=complete termios=restored cursor=visible history=finished'
	'CNMON_NATIVE_COMMAND_LINUX_PTY redirected-stderr=keys-active q=graceful termios=restored'
	'CNMON_NATIVE_COMMAND_LINUX_PTY blocked-report prefilled='
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-start=unchanged foreground-activation=pass background-owner-mode=unchanged suspend=restored resume=rearmed q=graceful cursor=restored'
	'CNMON_NATIVE_COMMAND_LINUX_PTY terminal-loss=inherited-hup-ignore useful-round=complete report=complete history=finished post-loss-cpu-ticks-per-2s='
	'CNMON_NATIVE_COMMAND_LINUX_PTY blocked-ui=second-interrupt exit=130 termios=restored'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=normal tostop=false owner-mode=unchanged frame=none cursor-show=1'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=normal tostop=true owner-mode=unchanged frame=none cursor-show=0'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=graceful tostop=false owner-mode=unchanged frame=none cursor-show=1'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=graceful tostop=true owner-mode=unchanged frame=none cursor-show=0'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=report-error tostop=false owner-mode=unchanged frame=none cursor-show=1'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=report-error tostop=true owner-mode=unchanged frame=none cursor-show=0'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=hard tostop=false owner-mode=unchanged frame=none cursor-show=1'
	'CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=hard tostop=true owner-mode=unchanged frame=none cursor-show=0'
	'CNMON_NATIVE_COMMAND_CLOSED_STDOUT'
)
for marker in "${required[@]}"; do
	if ! grep -Fq -- "$marker" "$main_log"; then
		message="native Linux command gate omitted required result: $marker"
		message=${message//'%'/'%25'}
		message=${message//$'\r'/'%0D'}
		message=${message//$'\n'/'%0A'}
		echo "::error title=cn-mon native Linux command::$message" >&2
		exit 1
	fi
done

run_permission_role() {
	local role=$1 log="$artifact_dir/linux-command-permission-$1.log" result marker message
	shift
	set +e
	"$@" -test.v -test.run '^TestNativeCommandLinuxProbePermissionRole$' -test.count=1 -test.timeout=90s >"$log" 2>&1
	result=$?
	set -e
	cat "$log"
	if [[ $role == granted ]]; then
		marker='CNMON_NATIVE_COMMAND_LINUX_PERMISSION uid=0 euid=0 cap_net_raw_eff=true cap_net_raw_prm=true'
		grep -Fq "$marker" "$log" && grep -Eq 'role=granted icmp=OK exit=0 tcp=open history=finished-round' "$log" || result=1
	else
		marker='CNMON_NATIVE_COMMAND_LINUX_PERMISSION uid=65534 euid=65534 cap_net_raw_eff=false cap_net_raw_prm=false cap_net_raw_amb=false role=denied icmp=ERROR exit=3 tcp=open history=finished-round'
		grep -Fq "$marker" "$log" || result=1
	fi
	if [[ $result -ne 0 ]] || ! grep -Fq -- '--- PASS: TestNativeCommandLinuxProbePermissionRole' "$log"; then
		message="native Linux built-command permission role '$role' failed (exit $result): $(tail -c 6000 "$log")"
		message=${message//'%'/'%25'}
		message=${message//$'\r'/'%0D'}
		message=${message//$'\n'/'%0A'}
		echo "::error title=cn-mon Linux command permission::$message" >&2
		return 1
	fi
}

permission_dir=$(mktemp -d /tmp/cn-mon-native-command-role.XXXXXX)
trap 'rm -rf -- "$permission_dir"' EXIT
chmod 755 "$permission_dir"
install -m 755 "$command_binary" "$permission_dir/cn-mon"
install -m 755 "$test_binary" "$permission_dir/cn-mon-command.test"
if [[ -n "$(getcap -n "$permission_dir/cn-mon" "$permission_dir/cn-mon-command.test")" ]]; then
	message='staged native command role executables unexpectedly carry file capabilities'
	echo "::error title=cn-mon Linux permission setup::$message" >&2
	exit 1
fi

run_permission_role granted sudo -n env CGO_ENABLED=0 CNMON_NATIVE_COMMAND=1 \
	CNMON_NATIVE_COMMAND_EXPECT_PROBE=granted CNMON_NATIVE_COMMAND_BINARY="$permission_dir/cn-mon" \
	"$permission_dir/cn-mon-command.test"
(
	cd /tmp
	run_permission_role denied sudo -n setpriv --reuid=65534 --regid=65534 --clear-groups \
		--bounding-set=-net_raw --inh-caps=-net_raw --ambient-caps=-net_raw env \
		TMPDIR=/tmp CGO_ENABLED=0 CNMON_NATIVE_COMMAND=1 CNMON_NATIVE_COMMAND_EXPECT_PROBE=denied \
		CNMON_NATIVE_COMMAND_BINARY="$permission_dir/cn-mon" "$permission_dir/cn-mon-command.test"
)

echo '::notice title=cn-mon native Linux command::linux/amd64 CGO=0 built-command history/JSON, INI/CLI retention, signals/private processes, pause/resume, closed stdout, and explicit CAP_NET_RAW granted/denied ICMP roles passed'
