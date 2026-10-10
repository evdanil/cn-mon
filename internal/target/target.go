// Package target collects, validates, expands, and identifies monitor targets.
//
// New utility code is licensed under the Apache License, Version 2.0.
package target

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxTCPHosts = 1024
	maxTCPPorts = 5
)

// Rejection records a collected input object that could not be admitted.
type Rejection struct {
	Object string
	Reason string
}

// Request is the stable, DNS-independent identity of an accepted target set.
type Request struct {
	Tokens []string
	Ports  []int
	Hosts  []string
	Key    string
}

// ReadObjects collects positional targets, substituting stdin at each explicit
// positional "-", then appends the selected file's objects. Stdin is read at
// most once, even if "-" occurs more than once. A nil file means no file was
// selected; an empty selected path adds no objects.
func ReadObjects(objects []string, file *string, stdin io.Reader) ([]string, error) {
	var stdinObjects []string
	stdinRead := false
	readStdin := func() error {
		if stdinRead {
			return nil
		}
		if stdin == nil {
			return errors.New("stdin was requested but no input reader is available")
		}
		var err error
		stdinObjects, err = readObjects(stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		stdinRead = true
		return nil
	}
	collected := make([]string, 0, len(objects))
	for _, object := range objects {
		if object != "-" {
			collected = append(collected, object)
			continue
		}
		if err := readStdin(); err != nil {
			return nil, err
		}
		collected = append(collected, stdinObjects...)
	}
	if file != nil && *file != "" {
		if *file == "-" {
			if err := readStdin(); err != nil {
				return nil, err
			}
			collected = append(collected, stdinObjects...)
			return unique(collected), nil
		}
		f, err := os.Open(*file)
		if err != nil {
			return nil, fmt.Errorf("read target file %q: %w", *file, err)
		}
		defer f.Close()
		fileObjects, err := readObjects(f)
		if err != nil {
			return nil, fmt.Errorf("read target file %q: %w", *file, err)
		}
		collected = append(collected, fileObjects...)
	}
	return unique(collected), nil
}

func readObjects(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, errors.New("input is not valid UTF-8")
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	var result []string
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		result = append(result, strings.Fields(line)...)
	}
	return result, nil
}

func unique(objects []string) []string {
	seen := make(map[string]struct{}, len(objects))
	result := make([]string, 0, len(objects))
	for _, object := range objects {
		if _, ok := seen[object]; ok {
			continue
		}
		seen[object] = struct{}{}
		result = append(result, object)
	}
	return result
}

// ParsePorts validates one comma-separated list and returns unique ports in
// ascending order for request construction and output columns.
func ParsePorts(text string) ([]int, error) {
	var ports []int
	seen := make(map[int]struct{})
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if len(part) == 0 || len(part) > 5 {
			return nil, fmt.Errorf("%q is not a port list: use up to 5 ports from 1 to 65535, separated by commas", text)
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return nil, fmt.Errorf("%q is not a port list: use up to 5 ports from 1 to 65535, separated by commas", text)
			}
		}
		port, _ := strconv.Atoi(part)
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("%q is not a port list: use up to 5 ports from 1 to 65535, separated by commas", text)
		}
		if _, ok := seen[port]; !ok {
			seen[port] = struct{}{}
			ports = append(ports, port)
		}
	}
	if len(ports) > maxTCPPorts {
		return nil, fmt.Errorf("%q is not a port list: use up to 5 ports from 1 to 65535, separated by commas", text)
	}
	sort.Ints(ports)
	return ports, nil
}

type parsed struct {
	kind   string
	tokens []string
	hosts  []string
	reason string
}

// Build admits inputs in source order under the pre-canonical TCP host cap,
// then canonicalizes the accepted set and hashes its deterministic expansion.
func Build(objects []string, ports []int) (Request, []Rejection, error) {
	ports = append([]int(nil), ports...)
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return Request{}, nil, fmt.Errorf("invalid TCP port %d", port)
		}
	}
	ports = uniqueInts(ports)
	if len(ports) > maxTCPPorts {
		return Request{}, nil, fmt.Errorf("too many TCP ports: maximum is %d", maxTCPPorts)
	}
	sort.Ints(ports)

	var rejected []Rejection
	acceptedTokens := make(map[string]struct{})
	var admissionHosts []string
	seenHosts := make(map[string]struct{})
	seenObjects := make(map[string]struct{})
	for _, object := range objects {
		if _, ok := seenObjects[object]; ok {
			continue
		}
		seenObjects[object] = struct{}{}
		if !utf8.ValidString(object) {
			rejected = append(rejected, Rejection{Object: object, Reason: "not an IP address, network or host name"})
			continue
		}
		p := parseObject(object)
		if p.reason != "" {
			rejected = append(rejected, Rejection{Object: object, Reason: p.reason})
			continue
		}
		newHosts := make([]string, 0, len(p.hosts))
		local := make(map[string]struct{}, len(p.hosts))
		for _, host := range p.hosts {
			if _, ok := seenHosts[host]; ok {
				continue
			}
			if _, ok := local[host]; ok {
				continue
			}
			local[host] = struct{}{}
			newHosts = append(newHosts, host)
		}
		if len(ports) > 0 && len(admissionHosts)+len(newHosts) > maxTCPHosts {
			reason := "more than 1,024 hosts with --tcp; test at most a /22"
			if strings.Contains(object, ":") {
				reason = "more than 1,024 hosts with --tcp; test at most a /118"
			}
			rejected = append(rejected, Rejection{Object: object, Reason: reason})
			continue
		}
		for _, host := range newHosts {
			seenHosts[host] = struct{}{}
			admissionHosts = append(admissionHosts, host)
		}
		if len(p.hosts) > 0 {
			for _, token := range p.tokens {
				acceptedTokens[token] = struct{}{}
			}
		}
	}

	tokens := make([]string, 0, len(acceptedTokens))
	for token := range acceptedTokens {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	if len(tokens) == 0 {
		return Request{}, rejected, nil
	}
	hosts := make([]string, 0, len(admissionHosts))
	seenHosts = make(map[string]struct{}, len(admissionHosts))
	for _, token := range tokens {
		for _, host := range expandCanonical(token) {
			if _, ok := seenHosts[host]; ok {
				continue
			}
			seenHosts[host] = struct{}{}
			hosts = append(hosts, host)
		}
	}
	payload := identityJSON(tokens, ports, hosts)
	digest := sha256.Sum256([]byte(payload))
	return Request{Tokens: tokens, Ports: ports, Hosts: hosts, Key: hex.EncodeToString(digest[:])}, rejected, nil
}

func uniqueInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func parseObject(text string) parsed {
	if !utf8.ValidString(text) {
		return parsed{reason: "not an IP address, network or host name"}
	}
	if strings.Contains(text, "%") && !strings.ContainsAny(text, ", \t\r\n") {
		if strings.Count(text, "%") != 1 || strings.Contains(text, "/") {
			return parsed{reason: "not an IP address, network or host name"}
		}
		addr, err := netip.ParseAddr(text)
		if err != nil || !addr.Is6() || addr.Zone() == "" {
			return parsed{reason: "not an IP address, network or host name"}
		}
		if addr.Is4In6() {
			return parsed{reason: "IPv4-mapped IPv6 address; use " + addr.Unmap().String()}
		}
		return parsed{kind: "single", tokens: []string{addr.String()}, hosts: []string{text}}
	}
	if p, reason, matched := parseNetwork(text); matched {
		if reason != "" {
			return parsed{reason: reason}
		}
		if p.Addr().Is4() && p.Bits() == 32 || p.Addr().Is6() && p.Bits() == 128 {
			return parsed{kind: "single", tokens: []string{p.Addr().String()}, hosts: []string{p.Addr().String()}}
		}
		return parsed{kind: "subnet", tokens: []string{p.String()}, hosts: expandPrefix(p)}
	}
	if addr, err := netip.ParseAddr(text); err == nil && !strings.ContainsAny(text, ", \t\r\n") {
		if addr.Is4In6() {
			return parsed{reason: "IPv4-mapped IPv6 address; use " + addr.Unmap().String()}
		}
		if addr.Is6() && addr.IsLinkLocalUnicast() && addr.Zone() == "" {
			return parsed{reason: "link-local IPv6 needs an interface: ping one address with %<interface> appended, e.g. %eth0"}
		}
		return parsed{kind: "single", tokens: []string{addr.String()}, hosts: []string{addr.String()}}
	}
	if onlyDigitsDots(text) {
		return parsed{reason: "not an IP address, network or host name"}
	}
	if validHostname(text) {
		return parsed{kind: "single", tokens: []string{strings.ToLower(text)}, hosts: []string{text}}
	}
	words := strings.Fields(strings.NewReplacer(",", " ").Replace(text))
	var tokens []string
	var hosts []string
	for _, word := range words {
		if strings.Contains(word, "/") || strings.Count(word, "%") > 1 {
			continue
		}
		addr, err := netip.ParseAddr(word)
		if err != nil || addr.Is4In6() || (addr.Is6() && addr.IsLinkLocalUnicast() && addr.Zone() == "") {
			continue
		}
		tokens = append(tokens, addr.String())
		hosts = append(hosts, word)
	}
	if len(hosts) == 0 {
		return parsed{reason: "not an IP address, network or host name"}
	}
	return parsed{kind: "list", tokens: tokens, hosts: hosts}
}

func parseNetwork(text string) (netip.Prefix, string, bool) {
	if strings.Contains(text, "%") || strings.Count(text, "/") != 1 {
		return netip.Prefix{}, "", false
	}
	if prefix, err := netip.ParsePrefix(text); err == nil {
		if prefix.Addr().Is4In6() {
			hintPrefix := prefix.Masked()
			hint := hintPrefix.Addr().Unmap().String()
			if prefix.Bits() >= 96 && prefix.Bits() < 128 {
				hint += "/" + strconv.Itoa(prefix.Bits()-96)
			}
			return netip.Prefix{}, "IPv4-mapped IPv6 address; use " + hint, true
		}
		prefix = prefix.Masked()
		return prefix, networkReason(prefix), true
	}
	parts := strings.SplitN(text, "/", 2)
	addr, err := netip.ParseAddr(parts[0])
	if err != nil || !addr.Is4() {
		return netip.Prefix{}, "", false
	}
	mask, err := netip.ParseAddr(parts[1])
	if err != nil || !mask.Is4() {
		return netip.Prefix{}, "", false
	}
	bits, ok := maskPrefix(mask.As4())
	if !ok {
		return netip.Prefix{}, "invalid or noncontiguous IPv4 netmask/hostmask", true
	}
	prefix := netip.PrefixFrom(addr, bits).Masked()
	return prefix, networkReason(prefix), true
}

func maskPrefix(mask [4]byte) (int, bool) {
	value := uint32(mask[0])<<24 | uint32(mask[1])<<16 | uint32(mask[2])<<8 | uint32(mask[3])
	if bits, ok := contiguousOnes(value); ok {
		return bits, true
	}
	return contiguousOnes(^value)
}

func contiguousOnes(value uint32) (int, bool) {
	bits := 0
	zeroSeen := false
	for shift := 31; shift >= 0; shift-- {
		one := value&(uint32(1)<<uint(shift)) != 0
		if one && zeroSeen {
			return 0, false
		}
		if one {
			bits++
		} else {
			zeroSeen = true
		}
	}
	return bits, true
}

func networkReason(prefix netip.Prefix) string {
	addr := prefix.Addr()
	if addr.Is6() && addr.IsLinkLocalUnicast() {
		return "link-local IPv6 needs an interface: ping one address with %<interface> appended, e.g. %eth0"
	}
	if prefix.Addr().Is6() {
		if 128-prefix.Bits() > 16 {
			return "more than 65,535 hosts; ping at most a /112"
		}
	} else if 32-prefix.Bits() > 16 {
		return "more than 65,534 hosts; ping at most a /16"
	}
	return ""
}

func expandCanonical(token string) []string {
	if prefix, err := netip.ParsePrefix(token); err == nil {
		return expandPrefix(prefix.Masked())
	}
	if addr, err := netip.ParseAddr(token); err == nil {
		return []string{addr.String()}
	}
	return []string{token}
}

func expandPrefix(prefix netip.Prefix) []string {
	network := prefix.Masked()
	addr := network.Addr()
	if (addr.Is4() && prefix.Bits() == 32) || (addr.Is6() && prefix.Bits() == 128) {
		return []string{addr.String()}
	}
	count := 1 << uint(addr.BitLen()-prefix.Bits())
	start := addr
	end := addr
	for i := 0; i < count-1; i++ {
		end = end.Next()
	}
	var result []string
	for current := start; current.IsValid(); current = current.Next() {
		if !prefix.Contains(current) {
			break
		}
		isEdge := current == start || current == end
		if addr.Is4() {
			if prefix.Bits() <= 30 && isEdge {
				continue
			}
		} else if prefix.Bits() < 127 && current == start {
			continue
		}
		if reserved(current) {
			continue
		}
		result = append(result, current.String())
	}
	return result
}

func reserved(addr netip.Addr) bool {
	if addr.IsLoopback() || addr.IsMulticast() {
		return true
	}
	if addr.Is4() {
		return netip.MustParsePrefix("240.0.0.0/4").Contains(addr)
	}
	if addr.Is6() {
		for _, block := range ipv6Reserved {
			if block.Contains(addr) {
				return true
			}
		}
	}
	return false
}

var ipv6Reserved = []netip.Prefix{
	netip.MustParsePrefix("::/8"), netip.MustParsePrefix("100::/8"), netip.MustParsePrefix("200::/7"),
	netip.MustParsePrefix("400::/6"), netip.MustParsePrefix("800::/5"), netip.MustParsePrefix("1000::/4"),
	netip.MustParsePrefix("4000::/3"), netip.MustParsePrefix("6000::/3"), netip.MustParsePrefix("8000::/3"),
	netip.MustParsePrefix("a000::/3"), netip.MustParsePrefix("c000::/3"), netip.MustParsePrefix("e000::/4"),
	netip.MustParsePrefix("f000::/5"), netip.MustParsePrefix("f800::/6"), netip.MustParsePrefix("fe00::/9"),
}

func validHostname(name string) bool {
	if len(name) < 2 || len(name) > 252 {
		return false
	}
	body := strings.TrimSuffix(name, ".")
	if body == "" {
		return false
	}
	for _, label := range strings.Split(body, ".") {
		if len(label) < 1 || len(label) > 63 || !asciiAlphaNum(label[0]) || !asciiAlphaNum(label[len(label)-1]) {
			return false
		}
		for i := 1; i < len(label)-1; i++ {
			c := label[i]
			if !asciiAlphaNum(c) && c != '-' {
				return false
			}
		}
	}
	return true
}

func asciiAlphaNum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func onlyDigitsDots(text string) bool {
	if text == "" {
		return false
	}
	for i := range text {
		if (text[i] < '0' || text[i] > '9') && text[i] != '.' {
			return false
		}
	}
	return true
}

func identityJSON(tokens []string, ports []int, hosts []string) string {
	var b strings.Builder
	b.WriteByte('[')
	writeStringArray(&b, tokens)
	b.WriteString(",[")
	for i, port := range ports {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(port))
	}
	b.WriteString("],")
	writeStringArray(&b, hosts)
	b.WriteByte(']')
	return b.String()
}

func writeStringArray(b *strings.Builder, values []string) {
	b.WriteByte('[')
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		writePythonJSONString(b, value)
	}
	b.WriteByte(']')
}

func writePythonJSONString(b *strings.Builder, value string) {
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else if r <= 0x7e {
				b.WriteByte(byte(r))
			} else if r <= 0xffff {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				r -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			}
		}
	}
	b.WriteByte('"')
}
