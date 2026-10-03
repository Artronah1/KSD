// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestAllRulesHaveComments ensures every rule in every mode has a non-empty,
// unique (within its chain) comment, and that the comment actually survives
// rendering. Verify reads comments back from the kernel; a rule without one
// would be invisible to it, and an empty comment in the model would make the
// daemon reject its own ruleset on every verify.
func TestAllRulesHaveComments(t *testing.T) {
	configs := map[string]*Config{
		"defaults": DefaultConfig(),
		"quic": func() *Config { c := DefaultConfig(); c.QUICBlock = true; return c }(),
		"arp": func() *Config { c := DefaultConfig(); c.ARPProtection = true; c.ARPGateways = []string{"192.168.0.1"}; c.ARPGatewayMACs = []string{"aa:bb:cc:dd:ee:ff"}; return c }(),
		"no-mss-ttl": func() *Config {
			c := DefaultConfig()
			c.MSSClamp = false
			c.TTLSet = false
			return c
		}(),
		"dns-off": func() *Config { c := DefaultConfig(); c.DNSBlock = false; return c }(),
		"ipv6-off": func() *Config { c := DefaultConfig(); c.IPv6Block = false; return c }(),
	}
	modes := []string{"full", "baseline"}

	for name, cfg := range configs {
		for _, mode := range modes {
			tables := BuildFor(cfg, mode, "eth1", "192.168.0.1",
				[]string{"1.2.3.4", "2.27.28.105-2.27.28.106"})

			for _, tbl := range tables {
				for _, ch := range tbl.Chains {
					seen := map[string]bool{}
					for _, r := range ch.Rules {
						if r.Comment == "" {
							t.Errorf("%s/%s/%s: rule without comment: %+v",
								name, mode, ch.Name, r)
							continue
						}
						if seen[r.Comment] {
							t.Errorf("%s/%s/%s: duplicate comment %q",
								name, mode, ch.Name, r.Comment)
						}
						seen[r.Comment] = true
					}
				}
			}

			// Verify the comment actually survives rendering: verify reads
			// comments back from the kernel via `nft -j list`, so a comment
			// that gets mangled by the renderer would be invisible.
			script := Render(tables)
			for _, tbl := range tables {
				for _, ch := range tbl.Chains {
					for _, r := range ch.Rules {
						if r.Comment == "" {
							continue
						}
						want := fmt.Sprintf("comment %q", r.Comment)
						if !strings.Contains(script, want) {
							t.Errorf("%s/%s/%s: comment %q not in rendered script",
								name, mode, ch.Name, r.Comment)
						}
					}
				}
			}
		}
	}
}
