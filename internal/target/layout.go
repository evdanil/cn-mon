// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package target

import "net/netip"

// LayoutInput describes one retained subnet grid or the newly contributed
// literal/list host labels from one token.
type LayoutInput struct {
	Network netip.Prefix
	Hosts   []string
}

// LayoutInputs preserves token order while omitting already-covered inputs.
func LayoutInputs(tokens []string) []LayoutInput {
	inputs := make([]LayoutInput, 0, len(tokens))
	seenHosts := make(map[string]struct{})
	for _, token := range tokens {
		parsed := parseObject(token)
		if parsed.reason != "" || len(parsed.hosts) == 0 {
			continue
		}
		newHosts := make([]string, 0, len(parsed.hosts))
		for _, host := range parsed.hosts {
			if _, exists := seenHosts[host]; exists {
				continue
			}
			seenHosts[host] = struct{}{}
			newHosts = append(newHosts, host)
		}
		if len(newHosts) == 0 {
			continue
		}
		if parsed.kind == "subnet" {
			prefix, err := netip.ParsePrefix(parsed.tokens[0])
			if err != nil {
				continue
			}
			prefix = prefix.Masked()
			inputs = append(inputs, LayoutInput{Network: prefix})
			continue
		}
		inputs = append(inputs, LayoutInput{Hosts: newHosts})
	}
	return inputs
}
