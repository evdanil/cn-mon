package target

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadObjectsPreservesInputOrderAndReadsExplicitStdinOnce(t *testing.T) {
	var stdin countingReader
	stdin.data = []byte("# stdin comment\n second\n\nthird # inline\n")
	got, err := ReadObjects([]string{"first", "-", "second", "-", "first"}, nil, &stdin)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "third"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ReadObjects() = %#v, want %#v", got, want)
	}
	if stdin.reads != 1 {
		t.Fatalf("stdin reads = %d, want 1", stdin.reads)
	}
}

func TestReadObjectsFileDashAppendsTheSameSingleStdinRead(t *testing.T) {
	stdin := &countingReader{data: []byte("from-stdin\n")}
	file := "-"
	got, err := ReadObjects([]string{"before"}, &file, stdin)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[before from-stdin]" || stdin.reads != 1 {
		t.Fatalf("objects=%v stdin reads=%d", got, stdin.reads)
	}
}

func TestReadObjectsFileUTF8BOMCommentsAndUnreadableInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.txt")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbf  # heading\n192.0.2.1 # host\n192.0.2.2,foo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := path
	got, err := ReadObjects([]string{"192.0.2.9", "-"}, &file, strings.NewReader("192.0.2.8\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.9", "192.0.2.8", "192.0.2.1", "192.0.2.2,foo"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ReadObjects() = %#v, want %#v", got, want)
	}
	stdinPath := "-"
	if _, err := ReadObjects([]string{stdinPath}, nil, bytes.NewReader([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 stdin accepted")
	}
	badPath := filepath.Join(t.TempDir(), "invalid.txt")
	if err := os.WriteFile(badPath, []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadObjects(nil, &badPath, strings.NewReader("")); err == nil {
		t.Fatal("invalid UTF-8 file accepted")
	}
}

func TestParsePortsValidationAndNormalization(t *testing.T) {
	got, err := ParsePorts("443, 22,443,65535")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[22 443 65535]" {
		t.Fatalf("ParsePorts() = %v", got)
	}
	for _, bad := range []string{"", "0", "65536", "+22", "٢٢", "1,2,3,4,5,6", "1,,2"} {
		if _, err := ParsePorts(bad); err == nil {
			t.Errorf("ParsePorts(%q) accepted", bad)
		}
	}
}

func TestCanonicalRequestGoldenAndEquivalentIPv4Masks(t *testing.T) {
	got, rejected, err := Build([]string{"10.1.2.5/30", "Web01.EXAMPLE.NET"}, []int{443, 22})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("Build() error/rejections = %v / %#v", err, rejected)
	}
	if fmt.Sprint(got.Tokens) != "[10.1.2.4/30 web01.example.net]" || fmt.Sprint(got.Hosts) != "[10.1.2.5 10.1.2.6 web01.example.net]" {
		t.Fatalf("canonical request = %#v", got)
	}
	if got.Key != "1f48106300cacd9a109b91a8ddc79c64920f0feb58e7840459ff80b639b7cb46" {
		t.Fatalf("key = %s", got.Key)
	}
	for _, spelling := range []string{"10.1.2.5/255.255.255.0", "10.1.2.5/0.0.0.255", "10.1.2.5/0.0.0.3", "10.1.2.99/24"} {
		masked, rejects, err := Build([]string{spelling}, nil)
		wantToken := "[10.1.2.0/24]"
		if spelling == "10.1.2.5/0.0.0.3" {
			wantToken = "[10.1.2.4/30]"
		}
		if err != nil || len(rejects) != 0 || fmt.Sprint(masked.Tokens) != wantToken {
			t.Errorf("Build(%q) = %#v, %#v, %v", spelling, masked, rejects, err)
		}
		cidrToken := "10.1.2.0/24"
		if spelling == "10.1.2.5/0.0.0.3" {
			cidrToken = "10.1.2.4/30"
		}
		cidr, _, _ := Build([]string{cidrToken}, nil)
		if masked.Key != cidr.Key || fmt.Sprint(masked.Hosts) != fmt.Sprint(cidr.Hosts) {
			t.Errorf("%q differs from equivalent CIDR", spelling)
		}
	}
	for _, malformed := range []string{"10.1.2.5/255.0.255.0", "10.1.2.5/0.255.0.255"} {
		_, rejects, err := Build([]string{malformed}, nil)
		if err != nil || len(rejects) != 1 {
			t.Errorf("malformed mask %q: rejection count %d, error %v", malformed, len(rejects), err)
		}
	}
	if _, rejects, err := Build([]string{"10.1.2.5/0.0.0.0"}, nil); err != nil || len(rejects) != 1 {
		t.Fatalf("all-zero hostmask should normalize to an oversized /0: rejects=%#v err=%v", rejects, err)
	}
	_, rejects, err := Build([]string{"10.1.2.5/127.255.255.255"}, nil)
	if err != nil || len(rejects) != 1 || !strings.Contains(rejects[0].Reason, "more than 65,534 hosts") {
		t.Fatalf("valid hostmask should normalize to oversized /1: rejects=%#v err=%v", rejects, err)
	}
}

func TestHostSizedPrefixesAndAddressListCanonicalizeToAddresses(t *testing.T) {
	got, rejected, err := Build([]string{"192.0.2.5/32", "2001:db8::5/128", "bad,192.0.2.9 192.0.2.10"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("Build() error/rejections = %v / %#v", err, rejected)
	}
	want := "[192.0.2.10 192.0.2.5 192.0.2.9 2001:db8::5]"
	if fmt.Sprint(got.Tokens) != want || fmt.Sprint(got.Hosts) != want {
		t.Fatalf("tokens=%v hosts=%v, want %s", got.Tokens, got.Hosts, want)
	}
}

func TestSubnetExpansionUsesPython312HostAndReservedRules(t *testing.T) {
	got, rejected, err := Build([]string{"192.0.2.0/30", "127.0.0.0/30", "224.0.0.0/30", "240.0.0.0/30", "2001:db8::/127"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("Build() = %v, %#v", err, rejected)
	}
	want := "[192.0.2.1 192.0.2.2 2001:db8:: 2001:db8::1]"
	if fmt.Sprint(got.Hosts) != want {
		t.Fatalf("hosts = %v, want %s", got.Hosts, want)
	}
	for _, tc := range []struct{ token, want string }{{"192.0.2.4/31", "[192.0.2.4 192.0.2.5]"}, {"2001:db8::2/127", "[2001:db8::2 2001:db8::3]"}} {
		r, _, err := Build([]string{tc.token}, nil)
		if err != nil || fmt.Sprint(r.Hosts) != tc.want {
			t.Errorf("Build(%q) hosts = %v, err=%v; want %s", tc.token, r.Hosts, err, tc.want)
		}
	}
}

func TestIPv6ReservedRangeFixtureSet(t *testing.T) {
	for _, block := range []string{
		"::/8", "100::/8", "200::/7", "400::/6", "800::/5", "1000::/4", "4000::/3", "6000::/3",
		"8000::/3", "a000::/3", "c000::/3", "e000::/4", "f000::/5", "f800::/6", "fe00::/9",
	} {
		prefix := netip.MustParsePrefix(block)
		if !reserved(prefix.Addr()) || !reserved(prefix.Addr().Next()) {
			t.Errorf("Python 3.12 reserved IPv6 block %s was not excluded", block)
		}
	}
	if reserved(netip.MustParseAddr("2001:db8::1")) {
		t.Fatal("documentation IPv6 address was excluded")
	}
}

func TestPerNetworkExpansionBoundariesAndLargerNetworkRejections(t *testing.T) {
	v4, rejected, err := Build([]string{"10.0.0.0/16"}, nil)
	if err != nil || len(rejected) != 0 || len(v4.Hosts) != 65534 {
		t.Fatalf("IPv4 /16 hosts=%d rejected=%#v err=%v", len(v4.Hosts), rejected, err)
	}
	v6, rejected, err := Build([]string{"2001:db8::/112"}, nil)
	if err != nil || len(rejected) != 0 || len(v6.Hosts) != 65535 {
		t.Fatalf("IPv6 /112 hosts=%d rejected=%#v err=%v", len(v6.Hosts), rejected, err)
	}
	for _, larger := range []string{"10.0.0.0/15", "2001:db8::/111"} {
		_, rejected, err := Build([]string{larger}, nil)
		if err != nil || len(rejected) != 1 {
			t.Errorf("larger subnet %q rejected=%#v err=%v", larger, rejected, err)
		}
	}
}

func TestIPv6ScopesMappedAddressesAndHostnameRules(t *testing.T) {
	got, rejected, err := Build([]string{"FE80::1%eth0", "::ffff:192.0.2.1", "fe80::/127", "bad_name", "A.", "999.1"}, nil)
	if err != nil || fmt.Sprint(got.Tokens) != "[a. fe80::1%eth0]" {
		t.Fatalf("tokens=%v rejections=%#v err=%v", got.Tokens, rejected, err)
	}
	if len(rejected) != 4 {
		t.Fatalf("rejections = %#v", rejected)
	}
	if !strings.Contains(rejected[0].Reason, "192.0.2.1") {
		t.Fatalf("mapped address rejection lacks IPv4 hint: %#v", rejected[0])
	}
	_, mappedNetwork, err := Build([]string{"::ffff:192.0.2.5/120"}, nil)
	if err != nil || len(mappedNetwork) != 1 || !strings.Contains(mappedNetwork[0].Reason, "192.0.2.0/24") {
		t.Fatalf("mapped network lacks IPv4 prefix hint: %#v err=%v", mappedNetwork, err)
	}
	if r, rejects, err := Build([]string{"example"}, nil); err != nil || len(rejects) != 0 || fmt.Sprint(r.Tokens) != "[example]" {
		t.Fatalf("single-label name rejected: %#v %#v %v", r, rejects, err)
	}
}

func TestScopedIPv6ValidationMatchesPythonAddressAndNetworkParsing(t *testing.T) {
	for _, invalid := range []string{
		"fe80::1/128", "fe80::1%eth0/128", "fe80::1%eth0/112", "fe80::1%eth0%other", "fe80::1%a/b", "fe80::1%",
	} {
		request, rejected, err := Build([]string{invalid}, nil)
		if err != nil || len(rejected) != 1 || request.Key != "" || len(request.Hosts) != 0 {
			t.Errorf("invalid scoped target %q: request=%#v rejected=%#v err=%v", invalid, request, rejected, err)
		}
	}
	for _, valid := range []string{"fe80::1%eth0", "fe80::1%é", "2001:db8::1%eth0", "2001:db8::1%é"} {
		request, rejected, err := Build([]string{valid}, nil)
		if err != nil || len(rejected) != 0 || fmt.Sprint(request.Hosts) != "["+valid+"]" {
			t.Errorf("valid scoped literal %q: hosts=%v rejected=%#v err=%v", valid, request.Hosts, rejected, err)
		}
	}
	mapped, rejected, err := Build([]string{"::ffff:192.0.2.1%eth0"}, nil)
	if err != nil || len(rejected) != 1 || !strings.Contains(rejected[0].Reason, "use 192.0.2.1") || mapped.Key != "" {
		t.Fatalf("scoped mapped address needs the unscoped IPv4 hint: request=%#v rejected=%#v err=%v", mapped, rejected, err)
	}
	request, rejected, err := Build([]string{"fe80::1%eth0/128 192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 || fmt.Sprint(request.Hosts) != "[192.0.2.1]" {
		t.Fatalf("invalid scoped CIDR was readmitted from an address list: hosts=%v rejected=%#v err=%v", request.Hosts, rejected, err)
	}
	request, rejected, err = Build([]string{"fe80::1%eth0,2001:db8::1%é,192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 || fmt.Sprint(request.Tokens) != "[192.0.2.1 2001:db8::1%é fe80::1%eth0]" {
		t.Fatalf("valid scoped literals were lost from an address list: tokens=%v rejected=%#v err=%v", request.Tokens, rejected, err)
	}
}

func TestScopedUnicodeZoneUsesPythonCompatibleASCIIIdentityJSON(t *testing.T) {
	got, rejected, err := Build([]string{"fe80::1%é"}, nil)
	if err != nil || len(rejected) != 0 || got.Key != "4d69bf7f6806df4d4ddbb8e400296ec3a0b38b5b2d4ec76ca15d09fe91b56762" {
		t.Fatalf("request=%#v rejected=%#v err=%v", got, rejected, err)
	}
	if encoded := identityJSON([]string{"fe80::1%é"}, nil, []string{"fe80::1%é"}); encoded != `[["fe80::1%\u00e9"],[],["fe80::1%\u00e9"]]` {
		t.Fatalf("identity JSON = %s", encoded)
	}
	if encoded := identityJSON([]string{"<>&"}, nil, nil); encoded != `[["<>&"],[],[]]` {
		t.Fatalf("identity JSON unexpectedly HTML-escaped text: %s", encoded)
	}
	controlZone, rejected, err := Build([]string{"fe80::1%a\x00b"}, nil)
	if err != nil || len(rejected) != 0 || controlZone.Key != "c094cfec42799ab7e42e1b8775588bf308ff66f823c40cd0ca95acdc09a5f45f" {
		t.Fatalf("ASCII control in Python-compatible zone encoding: request=%#v rejected=%#v err=%v", controlZone, rejected, err)
	}
}

func TestRequestIdentitySortsCanonicalTokensButKeepsOverlaps(t *testing.T) {
	a, _, err := Build([]string{"192.0.2.1", "192.0.2.0/30", "A.example", "a.EXAMPLE"}, []int{443, 22})
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := Build([]string{"a.example", "192.0.2.0/30", "192.0.2.1"}, []int{22, 443})
	if a.Key != b.Key || fmt.Sprint(a.Tokens) != "[192.0.2.0/30 192.0.2.1 a.example]" {
		t.Fatalf("identity not canonical: %#v vs %#v", a, b)
	}
	c, _, _ := Build([]string{"a.example."}, []int{22, 443})
	if c.Key == b.Key {
		t.Fatal("trailing dot did not distinguish identity")
	}
}

func TestNoUsableTargetsReturnsZeroRequestAndPreservesRejections(t *testing.T) {
	request, rejected, err := Build([]string{"bad_name", "999.1"}, nil)
	if err != nil || len(rejected) != 2 || request.Key != "" || request.Tokens != nil || request.Ports != nil || request.Hosts != nil {
		t.Fatalf("request=%#v rejected=%#v err=%v", request, rejected, err)
	}
	empty, rejected, err := Build(nil, nil)
	if err != nil || len(rejected) != 0 || empty.Key != "" || empty.Tokens != nil || empty.Ports != nil || empty.Hosts != nil {
		t.Fatalf("empty request=%#v rejected=%#v err=%v", empty, rejected, err)
	}
}

func TestBuildRejectsInvalidDirectPortValues(t *testing.T) {
	for _, ports := range [][]int{{0}, {65536}, {1, 2, 3, 4, 5, 6}} {
		if _, _, err := Build([]string{"192.0.2.1"}, ports); err == nil {
			t.Errorf("Build accepted invalid ports %v", ports)
		}
	}
}

func TestTCPAdmissionIsGreedyBeforeCanonicalization(t *testing.T) {
	cases := []struct {
		objects []string
		tokens  string
		reject  string
		hosts   int
		key     string
	}{
		{[]string{"10.0.0.0/22", "10.0.4.1", "10.0.4.2"}, "[10.0.0.0/22 10.0.4.1 10.0.4.2]", "", 1024, "23fa8108eae6b8e943340ecdf476aefc3572eee18971523ee8822e74bfae63dd"},
		{[]string{"10.0.4.2", "10.0.4.1", "10.0.0.0/22"}, "[10.0.0.0/22 10.0.4.1 10.0.4.2]", "", 1024, "23fa8108eae6b8e943340ecdf476aefc3572eee18971523ee8822e74bfae63dd"},
		{[]string{"10.0.0.0/22", "10.0.4.1", "10.0.4.2", "10.0.4.3"}, "[10.0.0.0/22 10.0.4.1 10.0.4.2]", "10.0.4.3", 1024, "23fa8108eae6b8e943340ecdf476aefc3572eee18971523ee8822e74bfae63dd"},
		{[]string{"10.0.4.3", "10.0.4.2", "10.0.4.1", "10.0.0.0/22"}, "[10.0.4.1 10.0.4.2 10.0.4.3]", "10.0.0.0/22", 3, "3242fd9d5a9b6a2ef1e306ef4dcab9cb491a8ce1bc3e1a70f552d52b9c0d8615"},
		{[]string{"10.0.0.0/22", "HOST01", "host01", "HOST02"}, "[10.0.0.0/22 host01]", "HOST02", 1023, "c510bb4c77002ff5a52a086cfd556936d1e79c33990ff2f1ed58692aeff6821f"},
	}
	for _, tc := range cases {
		r, rejected, err := Build(tc.objects, []int{443})
		if err != nil || fmt.Sprint(r.Tokens) != tc.tokens || len(r.Hosts) != tc.hosts {
			t.Errorf("Build(%v) tokens/hosts/error = %v/%d/%v", tc.objects, r.Tokens, len(r.Hosts), err)
		}
		if (tc.reject == "" && len(rejected) != 0) || (tc.reject != "" && (len(rejected) != 1 || rejected[0].Object != tc.reject)) {
			t.Errorf("Build(%v) rejections = %#v", tc.objects, rejected)
		}
		if tc.key != "" && r.Key != tc.key {
			t.Errorf("Build(%v) key = %s, want %s", tc.objects, r.Key, tc.key)
		}
	}
}

func TestTCPAdmissionUsesSourceHostStringsBeforeCanonicalization(t *testing.T) {
	cases := []struct {
		name    string
		objects []string
		wantKey string
	}{
		{
			name:    "address list preserves IPv6 spelling for admission",
			objects: []string{"10.0.0.0/22", "2001:DB8::1 2001:db8::1", "HOST02"},
			wantKey: "f01826db611d0fa2813510e77344030a40529dc4e9746edfc86ee172bc6e3071",
		},
		{
			name:    "scoped single addresses preserve IPv6 spelling for admission",
			objects: []string{"10.0.0.0/22", "FE80::1%eth0", "fe80::1%eth0", "HOST02"},
			wantKey: "d896918d284d44b207dd1b715efbe493beb16c7e1c82310c8bea81db3dcd5b79",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, rejected, err := Build(tc.objects, []int{443})
			if err != nil || len(rejected) != 1 || rejected[0].Object != "HOST02" {
				t.Fatalf("admission rejected=%#v request hosts=%d err=%v; want HOST02 rejected", rejected, len(request.Hosts), err)
			}
			if len(request.Hosts) != 1023 || request.Key != tc.wantKey {
				t.Fatalf("canonical request hosts=%d key=%s, want 1023 / %s", len(request.Hosts), request.Key, tc.wantKey)
			}
		})
	}
}

func TestICMPHasNoAggregateHostCap(t *testing.T) {
	r, rejected, err := Build([]string{"10.0.0.0/16", "10.1.0.0/16"}, nil)
	if err != nil || len(rejected) != 0 || len(r.Hosts) != 131068 {
		t.Fatalf("host count=%d rejected=%#v err=%v, want 131068", len(r.Hosts), rejected, err)
	}
}

type countingReader struct {
	data  []byte
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, io.EOF
}
