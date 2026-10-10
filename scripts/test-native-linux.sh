#!/usr/bin/env bash
set -Eeuo pipefail

# Disposable GitHub-hosted runner only. All network changes live in namespaces.
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
artifact_dir=${CNMON_NATIVE_ARTIFACT_DIR:-${RUNNER_TEMP:-/tmp}/cn-mon-native-linux}
mkdir -p "$artifact_dir"
artifact_dir=$(cd "$artifact_dir" && pwd)
chmod 755 "$artifact_dir"
exec > >(tee "$artifact_dir/linux-harness.log") 2>&1
cd "$repo_root"

for tool in ip iptables ip6tables setpriv; do
	command -v "$tool" >/dev/null || { echo "required native test tool missing: $tool" >&2; exit 1; }
done
command -v sudo >/dev/null || { echo "required native test tool missing: sudo" >&2; exit 1; }
ip_cmd() { sudo ip "$@"; }
netns_exec() { sudo ip netns exec "$@"; }

suffix=$(printf '%08x' "$(( $$ ^ RANDOM ))")
ns_probe="cnmp${suffix}"
ns_router="cnmr${suffix}"
ns_target="cnmt${suffix}"
host_p="hp${suffix}"
host_r0="hr${suffix}"
host_r1="hs${suffix}"
host_t="ht${suffix}"
cleanup() {
	status=$?
	trap - EXIT INT TERM
	for link in "$host_p" "$host_r0" "$host_r1" "$host_t"; do
		if ip_cmd link show dev "$link" >/dev/null 2>&1; then
			ip_cmd link del "$link" || status=1
		fi
	done
	for ns in "$ns_probe" "$ns_router" "$ns_target"; do
		if ip_cmd netns list | awk '{print $1}' | grep -Fxq "$ns"; then
			ip_cmd netns del "$ns" || status=1
		fi
	done
	{
		printf 'cleanup_status=%s\n' "$status"
		ip_cmd netns list
	} >> "$artifact_dir/linux-harness.log" 2>&1
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

go test -c -o "$artifact_dir/probe-native.test" ./internal/probe
chmod 755 "$artifact_dir/probe-native.test"
go -C third_party/pro-bing test -mod=readonly -c -tags=native -o "$artifact_dir/pro-bing-native.test" .
chmod 755 "$artifact_dir/pro-bing-native.test"

ip_cmd netns add "$ns_probe"
ip_cmd netns add "$ns_router"
ip_cmd netns add "$ns_target"
ip_cmd link add "$host_p" type veth peer name "$host_r0"
ip_cmd link set "$host_p" netns "$ns_probe"
ip_cmd link set "$host_r0" netns "$ns_router"
ip_cmd link add "$host_r1" type veth peer name "$host_t"
ip_cmd link set "$host_r1" netns "$ns_router"
ip_cmd link set "$host_t" netns "$ns_target"
ip_cmd -n "$ns_probe" link set "$host_p" name p0
ip_cmd -n "$ns_router" link set "$host_r0" name r0
ip_cmd -n "$ns_router" link set "$host_r1" name r1
ip_cmd -n "$ns_target" link set "$host_t" name t0

for ns in "$ns_probe" "$ns_router" "$ns_target"; do
	ip_cmd -n "$ns" link set lo up
done
ip_cmd -n "$ns_probe" link set p0 up
ip_cmd -n "$ns_router" link set r0 up
ip_cmd -n "$ns_router" link set r1 up
ip_cmd -n "$ns_target" link set t0 up

ip_cmd -n "$ns_probe" addr add 198.18.0.1/30 dev p0
ip_cmd -n "$ns_router" addr add 198.18.0.2/30 dev r0
ip_cmd -n "$ns_router" addr add 198.18.0.5/30 dev r1
ip_cmd -n "$ns_target" addr add 198.18.0.6/30 dev t0
ip_cmd -n "$ns_probe" -6 addr add fd42:636e:1::1/64 dev p0
ip_cmd -n "$ns_router" -6 addr add fd42:636e:1::2/64 dev r0
ip_cmd -n "$ns_router" -6 addr add fd42:636e:2::5/64 dev r1
ip_cmd -n "$ns_target" -6 addr add fd42:636e:2::6/64 dev t0

ip_cmd -n "$ns_probe" route add 198.18.0.4/30 via 198.18.0.2 dev p0
ip_cmd -n "$ns_target" route add 198.18.0.0/30 via 198.18.0.5 dev t0
ip_cmd -n "$ns_probe" -6 route add fd42:636e:2::/64 via fd42:636e:1::2 dev p0
ip_cmd -n "$ns_target" -6 route add fd42:636e:1::/64 via fd42:636e:2::5 dev t0
netns_exec "$ns_router" sysctl -q -w net.ipv4.ip_forward=1
netns_exec "$ns_router" sysctl -q -w net.ipv6.conf.all.forwarding=1

wait_ipv6_ready() {
	local ns=$1 dev=$2 address=$3 output
	for _ in $(seq 1 50); do
		output=$(ip_cmd -n "$ns" -6 addr show dev "$dev")
		if grep -Fq "$address/64" <<<"$output" && ! grep -Fq 'tentative' <<<"$output"; then
			return 0
		fi
		sleep 0.1
	done
	printf 'IPv6 address did not become ready: namespace=%s interface=%s address=%s\n%s\n' "$ns" "$dev" "$address" "$output" >&2
	return 1
}
wait_ipv6_ready "$ns_probe" p0 fd42:636e:1::1
wait_ipv6_ready "$ns_router" r0 fd42:636e:1::2
wait_ipv6_ready "$ns_router" r1 fd42:636e:2::5
wait_ipv6_ready "$ns_target" t0 fd42:636e:2::6

ip_cmd -n "$ns_probe" -brief addr show
ip_cmd -n "$ns_router" -brief addr show
ip_cmd -n "$ns_target" -brief addr show
netns_exec "$ns_probe" ip route
netns_exec "$ns_probe" ip -6 route

run_nested_native() {
	local log="$artifact_dir/linux-pro-bing-native.log" status message
	local -a command=(sudo ip netns exec "$ns_probe" "$artifact_dir/pro-bing-native.test"
		-test.v -test.run '^Test(SetInterfaceName|SetSource)Native$' -test.count=1 -test.timeout=30s)
	printf '\n=== nested pro-bing native tests in probe namespace ===\n'
	set +e
	"${command[@]}" 2>&1 | tee "$log"
	status=${PIPESTATUS[0]}
	set -e
	if [[ $status -ne 0 ]] || ! grep -Fq -- '--- PASS: TestSetInterfaceNameNative' "$log" || ! grep -Fq -- '--- PASS: TestSetSourceNative' "$log"; then
		message="nested pro-bing native tests failed (exit $status): $(tail -c 6000 "$log")"
		message=${message//'%'/'%25'}
		message=${message//$'\r'/'%0D'}
		message=${message//$'\n'/'%0A'}
		printf '::error title=cn-mon native Linux pro-bing::%s\n' "$message" >&2
		return 1
	fi
}

run_native() {
	local phase=$1 privilege=${2:-root}
	local -a command=(sudo ip netns exec "$ns_probe" env
		"CNMON_NATIVE=1"
		"CNMON_NATIVE_PHASE=$phase"
		"CNMON_NATIVE_IPV4_TARGET=198.18.0.6"
        "CNMON_NATIVE_IPV4_ROUTER=198.18.0.2"
		"CNMON_NATIVE_IPV6_TARGET=fd42:636e:2::6"
        "CNMON_NATIVE_IPV6_ROUTER=fd42:636e:1::2"
		"CNMON_NATIVE_ARTIFACT_DIR=$artifact_dir")
	if [[ $privilege == denied ]]; then
		command+=(setpriv --reuid=65534 --regid=65534 --clear-groups)
	fi
	command+=("$artifact_dir/probe-native.test" -test.v -test.run '^TestNativeICMPGate$' -test.count=1 -test.timeout=30s)
	printf '\n=== native phase=%s privilege=%s uid=' "$phase" "$privilege"
	if [[ $privilege == denied ]]; then printf '65534 ===\n'; else printf '0 ===\n'; fi
	set +e
	"${command[@]}" 2>&1 | tee "$artifact_dir/linux-${phase}-${privilege}.log"
	status=${PIPESTATUS[0]}
	set -e
	if [[ $status -ne 0 ]] || ! grep -Fq -- '--- PASS: TestNativeICMPGate' "$artifact_dir/linux-${phase}-${privilege}.log"; then
		message="native Linux required phase '$phase' failed (exit $status): $(tail -c 6000 "$artifact_dir/linux-${phase}-${privilege}.log")"
		message=${message//'%'/'%25'}
		message=${message//$'\r'/'%0D'}
		message=${message//$'\n'/'%0A'}
		printf '::error title=cn-mon native Linux::%s\n' "$message" >&2
		return 1
	fi
}

run_nested_native
run_native echo root

netns_exec "$ns_target" iptables -I INPUT 1 -i t0 -p icmp --icmp-type echo-request -j REJECT --reject-with icmp-admin-prohibited
netns_exec "$ns_target" ip6tables -I INPUT 1 -i t0 -p ipv6-icmp --icmpv6-type echo-request -j REJECT --reject-with icmp6-adm-prohibited
netns_exec "$ns_target" iptables -S INPUT
netns_exec "$ns_target" ip6tables -S INPUT
run_native target-error root
netns_exec "$ns_target" iptables -F INPUT
netns_exec "$ns_target" ip6tables -F INPUT

netns_exec "$ns_router" iptables -I FORWARD 1 -i r0 -o r1 -p icmp --icmp-type echo-request -j REJECT --reject-with icmp-admin-prohibited
netns_exec "$ns_router" ip6tables -I FORWARD 1 -i r0 -o r1 -p ipv6-icmp --icmpv6-type echo-request -j REJECT --reject-with icmp6-adm-prohibited
netns_exec "$ns_router" iptables -S FORWARD
netns_exec "$ns_router" ip6tables -S FORWARD
run_native intermediate-error root
netns_exec "$ns_router" iptables -F FORWARD
netns_exec "$ns_router" ip6tables -F FORWARD

run_native denied denied
run_native granted root

echo 'native Linux probe gate completed; namespace teardown follows'
