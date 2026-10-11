// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package target

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf8"
)

// Restore validates a saved request and returns a detached copy suitable for
// execution. The saved host expansion is part of its identity and is preserved
// exactly; it must not be regenerated from Tokens.
func Restore(saved Request) (Request, error) {
	for i, token := range saved.Tokens {
		if !utf8.ValidString(token) {
			return Request{}, errors.New("saved target token is not valid UTF-8")
		}
		if !canonicalSavedToken(token) {
			return Request{}, fmt.Errorf("invalid canonical saved target token %q", token)
		}
		if i > 0 && saved.Tokens[i-1] >= token {
			return Request{}, errors.New("saved target tokens must be sorted and unique")
		}
	}
	if len(saved.Ports) > maxTCPPorts {
		return Request{}, fmt.Errorf("too many saved TCP ports: maximum is %d", maxTCPPorts)
	}
	for i, port := range saved.Ports {
		if port < 1 || port > 65535 {
			return Request{}, fmt.Errorf("invalid saved TCP port %d", port)
		}
		if i > 0 && saved.Ports[i-1] >= port {
			return Request{}, errors.New("saved TCP ports must be sorted and unique")
		}
	}
	if len(saved.Ports) > 0 && len(saved.Hosts) > maxTCPHosts {
		return Request{}, fmt.Errorf("too many saved TCP hosts: maximum is %d", maxTCPHosts)
	}
	seenHosts := make(map[string]struct{}, len(saved.Hosts))
	for _, host := range saved.Hosts {
		if !utf8.ValidString(host) || host == "" {
			return Request{}, errors.New("saved host is empty or not valid UTF-8")
		}
		if _, exists := seenHosts[host]; exists {
			return Request{}, fmt.Errorf("duplicate saved host %q", host)
		}
		seenHosts[host] = struct{}{}
		if !validProbeHost(host) {
			return Request{}, fmt.Errorf("invalid saved probe host %q", host)
		}
	}
	if len(saved.Key) != sha256.Size*2 {
		return Request{}, errors.New("saved request key must be a 64-digit SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(saved.Key); err != nil {
		return Request{}, errors.New("saved request key must be a 64-digit SHA-256 hex digest")
	}
	digest := sha256.Sum256([]byte(identityJSON(saved.Tokens, saved.Ports, saved.Hosts)))
	if !strings.EqualFold(saved.Key, hex.EncodeToString(digest[:])) {
		return Request{}, errors.New("saved request key does not match its arrays")
	}
	return Request{
		Tokens: copyStrings(saved.Tokens),
		Ports:  copyInts(saved.Ports),
		Hosts:  copyStrings(saved.Hosts),
		Key:    saved.Key,
	}, nil
}

func validProbeHost(host string) bool {
	if strings.ContainsAny(host, ", \t\r\n/") || strings.Count(host, "%") > 1 {
		return false
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return !address.Is4In6() && (!address.Is6() || !address.IsLinkLocalUnicast() || address.Zone() != "")
	}
	if onlyDigitsDots(host) {
		return false
	}
	return validHostname(host)
}

func copyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}

func copyInts(values []int) []int {
	if values == nil {
		return nil
	}
	return append(make([]int, 0, len(values)), values...)
}

func canonicalSavedToken(token string) bool {
	if prefix, reason, matched := parseNetwork(token); matched {
		if reason != "" {
			return false
		}
		canonical := prefix.String()
		if prefix.Bits() == prefix.Addr().BitLen() {
			canonical = prefix.Addr().String()
		}
		return token == canonical
	}
	parsed := parseObject(token)
	return parsed.reason == "" && len(parsed.tokens) == 1 && parsed.tokens[0] == token
}
