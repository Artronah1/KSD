// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// ruleset.go — declarative ruleset model.
//
// The two ideas that make this simpler than the shell version:
//
//  1. Every rule carries a comment, and the comments are the contract.
//     Verification (verify.go) is derived from this model instead of being a
//     second, hand-written description of the rules (the old jq program). The
//     model cannot drift from its own verification.
//
//  2. The parts that actually change at runtime — WAN device and default
//     gateway — live in nft *sets*, not in the rule text. A WAN change is now
//     a set-element update, not a full ruleset rebuild. It also makes multi-WAN
//     a matter of adding elements rather than generating N rules.
//
// Rule expression text is intentionally kept identical to the validated shell
// version: this is a port, not a rewrite of the firewall semantics.

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// model
// ---------------------------------------------------------------------------

type Rule struct {
	Comment string // required; the verification contract
	Expr    string // nft match expression (may be empty for a catch-all)
	Counter string // named counter, rendered as `counter name <x>`
	Anon    bool   // append a bare `counter`
	Log     string // e.g. `limit rate 3/minute burst 5 packets log prefix "x" level warn`
	Verdict string // accept / drop / "" (empty = implicit continue)
}

type Chain struct {
	Name     string
	Type     string // filter
	Hook     string // output / forward / postrouting
	Priority int
	Policy   string // drop / accept
	Rules    []Rule
}

type Set struct {
	Name     string
	Type     string // ipv4_addr / ifname / ether_addr
	Flags    []string
	Quote    bool     // quote elements (ifname, ether_addr)
	Elements []string // rendered at install time; updated separately afterwards
}

type Table struct {
	Family   string
	Name     string
	Counters []string
	Sets     []Set
	Chains   []Chain
}

// ---------------------------------------------------------------------------
// builders
// ---------------------------------------------------------------------------

var allCounters = []string{
	"dns_leak_drops", "invalid_state_drops", "quic_drops",
	"allow_mark", "allow_vps", "allow_established", "output_leak_drops",
	"dns_leak_drops_fwd", "invalid_state_drops_fwd", "quic_drops_fwd",
	"allow_mark_fwd", "allow_vps_fwd", "allow_established_fwd",
	"forward_leak_drops",
}

const (
	OutputChain  = "killswitch_output"
	ForwardChain = "killswitch_forward"
	MangleChain  = "KS_POSTROUTING"
)

// BuildFor returns the model for the given mode. Used everywhere we need to
// compare "what should be installed" against the kernel (verify.go), so the
// generator and the verifier can never disagree.
func BuildFor(c *Config, mode, wan, gw string, vps []string) []Table {
	if mode == "full" {
		return BuildFull(c, wan, gw, vps)
	}
	return BuildBaseline(c, wan)
}

// BuildFull returns the full ruleset: everything the shell's `full` mode
// installed, plus the fixes from the review (NTP in full mode, counter/log
// split on the terminal drop, symmetric counters in both modes).
func BuildFull(c *Config, wan, gw string, vps []string) []Table {
	marks := strings.Join(c.AllowedMarks, ", ")
	gwElems := []string{"255.255.255.255"}
	if gw != "" {
		gwElems = append(gwElems, gw)
	}
	wanElems := []string{}
	if wan != "" && wan != "unknown" {
		wanElems = append(wanElems, wan)
	}

	t := Table{
		Family:   "inet",
		Name:     c.Table,
		Counters: allCounters,
		Sets: []Set{
			{Name: c.Set, Type: "ipv4_addr", Flags: []string{"interval"}, Elements: vps},
			{Name: c.WANSet, Type: "ifname", Quote: true, Elements: wanElems},
			{Name: c.GWSet, Type: "ipv4_addr", Flags: []string{"interval"}, Elements: gwElems},
			{Name: c.TrustedSet, Type: "ifname", Quote: true, Elements: c.AllowedIfaces},
		},
	}

	if c.DoHBlock && len(c.DOHServers) > 0 {
		t.Sets = append(t.Sets, Set{
			Name: c.DOHSet, Type: "ipv4_addr", Flags: []string{"interval"}, Elements: c.DOHServers,
		})
	}
	if len(c.NTPServers) > 0 {
		t.Sets = append(t.Sets, Set{
			Name: c.NTPSet, Type: "ipv4_addr", Flags: []string{"interval"}, Elements: c.NTPServers,
		})
	}
	out := Chain{
		Name: OutputChain, Type: "filter", Hook: "output",
		Priority: c.FilterPriority, Policy: "drop",
	}
	//Local loopback accept
	out.Rules = append(out.Rules, Rule{
		Comment: "ks-loopback", Expr: `ip daddr 127.0.0.0/8`,
		Anon: true, Verdict: "accept",
	})

	// 1. invalid first — everything else assumes a sane conntrack state
	out.Rules = append(out.Rules, Rule{
		Comment: "ks-invalid", Expr: "ct state invalid",
		Counter: "invalid_state_drops", Verdict: "drop",
	})

	// 2-3. DHCP: must survive at all times or the WAN lease is lost
	out.Rules = append(out.Rules, Rule{
		Comment: "ks-dhcpv6",
		Expr:    "oifname @" + c.WANSet + " meta nfproto ipv6 udp sport 546 udp dport 547",
		Anon:    true, Verdict: "accept",
	})
	out.Rules = append(out.Rules, Rule{
		Comment: "ks-dhcpv4",
		Expr:    "oifname @" + c.WANSet + " udp sport 68 udp dport 67 ip daddr @" + c.GWSet,
		Anon:    true, Verdict: "accept",
	})

	// 4. NTP — FIX (review P1-8): the shell only emitted this in baseline, so
	// sysntpd lost sync the moment the daemon reached full mode.
	if len(c.NTPServers) > 0 {
		out.Rules = append(out.Rules, Rule{
			Comment: "ks-ntp",
			Expr:    "oifname @" + c.WANSet + " meta nfproto ipv4 ip daddr @" + c.NTPSet + " udp dport 123",
			Anon:    true, Verdict: "accept",
		})
	}

	// 5-6. PMTU: related ICMP/ICMPv6 errors
	out.Rules = append(out.Rules,
			   Rule{Comment: "ks-icmp-err-v4",
				   Expr: "ct state related icmp type { destination-unreachable, time-exceeded }",
				   Anon: true, Verdict: "accept"},
				   Rule{Comment: "ks-icmp-err-v6",
					   Expr: "ct state related icmpv6 type { destination-unreachable, packet-too-big, time-exceeded }",
					   Anon: true, Verdict: "accept"},
	)

	// 7-10. leak classes — unconditional drop, before any identification
	out.Rules = append(out.Rules, leakBlocks(c, "oifname @"+c.WANSet, "")...)

	// 11-13. identification
	out.Rules = append(out.Rules,
			   Rule{Comment: "ks-ident-mark",
				   Expr:    "meta nfproto ipv4 meta mark { " + marks + " }",
				   Counter: "allow_mark", Verdict: "accept"},
				   Rule{Comment: "ks-ident-vps",
					   Expr:    "meta nfproto ipv4 ip daddr @" + c.Set,
					   Counter: "allow_vps", Verdict: "accept"},
					   Rule{Comment: "ks-established",
						   Expr:    "meta nfproto ipv4 ct state established,related",
						   Counter: "allow_established", Verdict: "accept"},
	)

	// 14. IPv6
	if c.IPv6Block {
		out.Rules = append(out.Rules, Rule{
			Comment: "ks-block-ipv6",
			Expr:    "meta nfproto ipv6 oifname != @" + c.TrustedSet,
			Anon:    true, Verdict: "drop",
		})
	} else {
		out.Rules = append(out.Rules, Rule{
			Comment: "ks-ident-v6",
			Expr:    "meta nfproto ipv6 meta mark { " + marks + " }",
			Anon:    true, Verdict: "accept",
		})
	}

	// 15. trusted interfaces (lo for TPROXY loopback + LAN)
	out.Rules = append(out.Rules, Rule{
		Comment: "ks-trusted-out", Expr: "oifname @" + c.TrustedSet,
		Anon: true, Verdict: "accept",
	})

	// 16. QUIC — only what survived identification
	if c.QUICBlock {
		out.Rules = append(out.Rules, Rule{
			Comment: "ks-block-quic",
			Expr:    "oifname @" + c.WANSet + " udp dport 443",
			Counter: "quic_drops", Verdict: "drop",
		})
	}

	// 17-18. terminal drop.
	// FIX (review P1-7): counting and rate-limited logging are now separate
	// rules. Previously the counter sat behind `limit rate 3/minute`, so every
	// packet above the limit fell through to the chain policy and went
	// uncounted — leak volume was systematically under-reported.
	out.Rules = append(out.Rules, terminalRules(c, "", "out")...)
	t.Chains = append(t.Chains, out)

	// ---------------- forward ----------------
	if c.ForwardProtect {
		fwd := Chain{
			Name: ForwardChain, Type: "filter", Hook: "forward",
			Priority: c.ForwardFilterPriority, Policy: "drop",
		}
		fwd.Rules = append(fwd.Rules, Rule{
			Comment: "ks-invalid-fwd", Expr: "ct state invalid",
			Counter: "invalid_state_drops_fwd", Verdict: "drop",
		})
		fwd.Rules = append(fwd.Rules, Rule{
			Comment: "ks-lan2lan",
			Expr:    "iifname @" + c.TrustedSet + " oifname @" + c.TrustedSet,
			Anon:    true, Verdict: "accept",
		})
		fwd.Rules = append(fwd.Rules,
				   Rule{Comment: "ks-icmp-err-v4-fwd",
					   Expr: "ct state related icmp type { destination-unreachable, time-exceeded }",
					   Anon: true, Verdict: "accept"},
		     Rule{Comment: "ks-icmp-err-v6-fwd",
			     Expr: "ct state related icmpv6 type { destination-unreachable, packet-too-big, time-exceeded }",
			     Anon: true, Verdict: "accept"},
		)
		fwd.Rules = append(fwd.Rules, leakBlocks(c, "oifname @"+c.WANSet, "_fwd")...)

		// DNAT/port-forward: accept after the leak blocks, so fw4 remains the
		// authority for inbound policy. (DNAT to 53/853/8853 is still dropped
		// on the first packet by design.)
		fwd.Rules = append(fwd.Rules, Rule{
			Comment: "ks-dnat",
			Expr:    "iifname @" + c.WANSet + " ct status dnat",
			Anon:    true, Verdict: "accept",
		})
		fwd.Rules = append(fwd.Rules,
				   Rule{Comment: "ks-ident-mark-fwd",
					   Expr:    "meta nfproto ipv4 meta mark { " + marks + " }",
					   Counter: "allow_mark_fwd", Verdict: "accept"},
		     Rule{Comment: "ks-ident-vps-fwd",
			     Expr:    "meta nfproto ipv4 ip daddr @" + c.Set,
			     Counter: "allow_vps_fwd", Verdict: "accept"},
		     Rule{Comment: "ks-established-fwd",
			     Expr:    "meta nfproto ipv4 ct state established,related",
			     Counter: "allow_established_fwd", Verdict: "accept"},
		)
		if c.QUICBlock {
			fwd.Rules = append(fwd.Rules, Rule{
				Comment: "ks-block-quic-fwd",
				Expr:    "oifname @" + c.WANSet + " udp dport 443",
				Counter: "quic_drops_fwd", Verdict: "drop",
			})
		}
		if c.IPv6Block {
			fwd.Rules = append(fwd.Rules, Rule{
				Comment: "ks-block-ipv6-fwd",
				Expr:    "meta nfproto ipv6 oifname != @" + c.TrustedSet,
				Anon:    true, Verdict: "drop",
			})
		}
		fwd.Rules = append(fwd.Rules, terminalRules(c, "oifname @"+c.WANSet, "fwd")...)
		t.Chains = append(t.Chains, fwd)
	}

	tables := []Table{t}
	if m := buildMangle(c, "@"+c.WANSet); m != nil {
		tables = append(tables, *m)
	}
	if a := BuildARP(c, wan); a != nil {
		tables = append(tables, *a)
	}
	return tables
}

// BuildBaseline returns the emergency fail-close ruleset. It does not depend on
// a known WAN device (DHCP is allowed without oifname), exactly like the shell
// baseline.
func BuildBaseline(c *Config, wan string) []Table {
	marks := strings.Join(c.AllowedMarks, ", ")
	wanElems := []string{}
	if wan != "" && wan != "unknown" {
		wanElems = append(wanElems, wan)
	}

	t := Table{
		Family:   "inet",
		Name:     c.Table,
		Counters: allCounters,
		Sets: []Set{
			{Name: c.Set, Type: "ipv4_addr", Flags: []string{"interval"}},
			{Name: c.WANSet, Type: "ifname", Quote: true, Elements: wanElems},
			{Name: c.TrustedSet, Type: "ifname", Quote: true, Elements: c.AllowedIfaces},
		},
	}
	if c.DoHBlock && len(c.DOHServers) > 0 {
		t.Sets = append(t.Sets, Set{
			Name: c.DOHSet, Type: "ipv4_addr", Flags: []string{"interval"}, Elements: c.DOHServers,
		})
	}
	if len(c.NTPServers) > 0 {
		t.Sets = append(t.Sets, Set{
			Name: c.NTPSet, Type: "ipv4_addr", Flags: []string{"interval"}, Elements: c.NTPServers,
		})
	}

	out := Chain{
		Name: OutputChain, Type: "filter", Hook: "output",
		Priority: c.FilterPriority, Policy: "drop",
	}
	out.Rules = append(out.Rules, Rule{
		Comment: "ks-invalid", Expr: "ct state invalid",
		Counter: "invalid_state_drops", Verdict: "drop",
	})
	out.Rules = append(out.Rules,
			   Rule{Comment: "ks-dhcpv4",
				   Expr: "meta nfproto ipv4 udp sport 68 udp dport 67", Anon: true, Verdict: "accept"},
		    Rule{Comment: "ks-dhcpv6",
			    Expr: "meta nfproto ipv6 udp sport 546 udp dport 547", Anon: true, Verdict: "accept"},
	)
	if len(c.NTPServers) > 0 {
		out.Rules = append(out.Rules, Rule{
			Comment: "ks-ntp",
			Expr:    "meta nfproto ipv4 ip daddr @" + c.NTPSet + " udp dport 123",
			Anon:    true, Verdict: "accept",
		})
	}
	out.Rules = append(out.Rules,
			   Rule{Comment: "ks-icmp-err-v4",
				   Expr: "ct state related icmp type { destination-unreachable, time-exceeded }",
				   Anon: true, Verdict: "accept"},
		    Rule{Comment: "ks-icmp-err-v6",
			    Expr: "ct state related icmpv6 type { destination-unreachable, packet-too-big, time-exceeded }",
			    Anon: true, Verdict: "accept"},
	)
	// Baseline leak blocks match "not a trusted interface" because the WAN
	// device is not known yet.
	out.Rules = append(out.Rules, leakBlocks(c, "oifname != @"+c.TrustedSet, "")...)

	// FIX (review P2-7): baseline now uses the same named counters as full.
	// Previously these were anonymous, so after a downgrade `status` still
	// showed stale full-mode numbers and looked healthy.
	out.Rules = append(out.Rules,
			   Rule{Comment: "ks-ident-mark",
				   Expr:    "meta nfproto ipv4 meta mark { " + marks + " }",
				   Counter: "allow_mark", Verdict: "accept"},
		    Rule{Comment: "ks-ident-vps",
			    Expr:    "meta nfproto ipv4 ip daddr @" + c.Set,
			    Counter: "allow_vps", Verdict: "accept"},
		    Rule{Comment: "ks-established",
			    Expr:    "meta nfproto ipv4 ct state established,related",
			    Counter: "allow_established", Verdict: "accept"},
	)
	if c.IPv6Block {
		out.Rules = append(out.Rules, Rule{
			Comment: "ks-block-ipv6",
			Expr:    "meta nfproto ipv6 oifname != @" + c.TrustedSet,
			Anon:    true, Verdict: "drop",
		})
	}
	out.Rules = append(out.Rules, Rule{
		Comment: "ks-trusted-out", Expr: "oifname @" + c.TrustedSet,
		Anon: true, Verdict: "accept",
	})
	out.Rules = append(out.Rules, terminalRules(c, "", "out")...)
	t.Chains = append(t.Chains, out)

	if c.ForwardProtect {
		fwd := Chain{
			Name: ForwardChain, Type: "filter", Hook: "forward",
			Priority: c.ForwardFilterPriority, Policy: "drop",
		}
		fwd.Rules = append(fwd.Rules, Rule{
			Comment: "ks-invalid-fwd", Expr: "ct state invalid",
			Counter: "invalid_state_drops_fwd", Verdict: "drop",
		})
		fwd.Rules = append(fwd.Rules, Rule{
			Comment: "ks-lan2lan",
			Expr:    "iifname @" + c.TrustedSet + " oifname @" + c.TrustedSet,
			Anon:    true, Verdict: "accept",
		})
		fwd.Rules = append(fwd.Rules,
				   Rule{Comment: "ks-icmp-err-v4-fwd",
					   Expr: "ct state related icmp type { destination-unreachable, time-exceeded }",
					   Anon: true, Verdict: "accept"},
		     Rule{Comment: "ks-icmp-err-v6-fwd",
			     Expr: "ct state related icmpv6 type { destination-unreachable, packet-too-big, time-exceeded }",
			     Anon: true, Verdict: "accept"},
		     Rule{Comment: "ks-dnat",
			     Expr: "iifname @" + c.WANSet + " ct status dnat", Anon: true, Verdict: "accept"},
		     Rule{Comment: "ks-ident-mark-fwd",
			     Expr:    "meta nfproto ipv4 meta mark { " + marks + " }",
			     Counter: "allow_mark_fwd", Verdict: "accept"},
		     Rule{Comment: "ks-ident-vps-fwd",
			     Expr:    "meta nfproto ipv4 ip daddr @" + c.Set,
			     Counter: "allow_vps_fwd", Verdict: "accept"},
		     Rule{Comment: "ks-established-fwd",
			     Expr:    "meta nfproto ipv4 ct state established,related",
			     Counter: "allow_established_fwd", Verdict: "accept"},
		)
		fwd.Rules = append(fwd.Rules, terminalRules(c, "", "fwd")...)
		t.Chains = append(t.Chains, fwd)
	}

	tables := []Table{t}
	if a := BuildARP(c, wan); a != nil {
		tables = append(tables, *a)
	}
	return tables
}

// BuildARP returns the optional ARP anti-spoofing table.
//
// Policy is `accept`: we only add explicit drops on the WAN interface, never on
// trusted/LAN interfaces (a policy=drop there would break ARP for the whole
// LAN). If no gateway MAC is configured we can only validate the gateway IP,
// which is a much weaker guarantee — config.Validate() warns about it.
func BuildARP(c *Config, iface string) *Table {
	if !c.ARPProtection || len(c.ARPGateways) == 0 {
		return nil
	}
	if iface == "" || iface == "unknown" {
		return nil // no interface to bind to yet
	}

	t := Table{
		Family: "arp",
		Name:   "killswitch_arp",
		Sets: []Set{
			{Name: "allowed_gw", Type: "ipv4_addr", Flags: []string{"interval"}, Elements: c.ARPGateways},
		},
	}
	hasMAC := len(c.ARPGatewayMACs) > 0
	if hasMAC {
		t.Sets = append(t.Sets, Set{
			Name: "allowed_gw_mac", Type: "ether_addr",
			Elements: c.ARPGatewayMACs,
		})
	}

	ch := Chain{Name: "input", Type: "filter", Hook: "input", Priority: 0, Policy: "accept"}
	in := "iifname " + fmt.Sprintf("%q", iface)

	req := in + " arp operation 1"
	rep := in + " arp operation 2"
	if hasMAC {
		ch.Rules = append(ch.Rules,
				  Rule{Comment: "ks-arp-req-gw",
					  Expr: req + " arp saddr ip @allowed_gw arp saddr ether @allowed_gw_mac",
					  Log:  "limit rate 5/second burst 10 packets", Anon: true, Verdict: "accept"},
		    Rule{Comment: "ks-arp-req-other", Expr: req, Anon: true, Verdict: "drop"},
		    Rule{Comment: "ks-arp-reply-gw",
			    Expr: rep + " arp saddr ip @allowed_gw arp saddr ether @allowed_gw_mac",
			    Log:  "limit rate 5/second burst 10 packets", Anon: true, Verdict: "accept"},
		    Rule{Comment: "ks-arp-spoof-mac",
			    Expr: rep + " arp saddr ip @allowed_gw arp saddr ether != @allowed_gw_mac",
			    Anon: true, Verdict: "drop"},
		    Rule{Comment: "ks-arp-spoof-nongw",
			    Expr: rep + " arp saddr ip != @allowed_gw", Anon: true, Verdict: "drop"},
		)
	} else {
		ch.Rules = append(ch.Rules,
				  Rule{Comment: "ks-arp-req-gw",
					  Expr: req + " arp saddr ip @allowed_gw",
					  Log:  "limit rate 5/second burst 10 packets", Anon: true, Verdict: "accept"},
		    Rule{Comment: "ks-arp-req-other", Expr: req, Anon: true, Verdict: "drop"},
		    Rule{Comment: "ks-arp-reply-gw",
			    Expr: rep + " arp saddr ip @allowed_gw",
			    Log:  "limit rate 5/second burst 10 packets", Anon: true, Verdict: "accept"},
		    Rule{Comment: "ks-arp-spoof-nongw",
			    Expr: rep + " arp saddr ip != @allowed_gw", Anon: true, Verdict: "drop"},
		)
	}
	ch.Rules = append(ch.Rules, Rule{
		Comment: "ks-arp-unknown-op", Expr: in, Anon: true, Verdict: "drop",
	})
	t.Chains = append(t.Chains, ch)
	return &t
}

// leakBlocks returns the DNS/DoT/DoQ/DoH drop rules in fixed causal order.
func leakBlocks(c *Config, match, suffix string) []Rule {
	var out []Rule
	if c.DNSBlock {
		out = append(out,
			     Rule{Comment: "ks-block-dns",
				     Expr: match + " udp dport { 53, 853, 8853 }",
				     Counter: "dns_leak_drops" + suffix, Verdict: "drop"},
	       Rule{Comment: "ks-block-dns-tcp",
		       Expr: match + " tcp dport { 53, 853, 8853 }",
		       Counter: "dns_leak_drops" + suffix, Verdict: "drop"},
		)
	}
	if c.DoQBlock {
		out = append(out, Rule{Comment: "ks-block-doq",
			Expr:    match + " udp dport 784",
			Counter: "dns_leak_drops" + suffix, Verdict: "drop"})
	}
	if c.DoHBlock && len(c.DOHServers) > 0 {
		out = append(out, Rule{Comment: "ks-block-doh",
			Expr:    match + " meta nfproto ipv4 ip daddr @" + c.DOHSet + " tcp dport 443",
			Counter: "dns_leak_drops" + suffix, Verdict: "drop"})
	}
	return out
}

// terminalRules returns the end-of-chain counting/drop pair.
func terminalRules(c *Config, extra, which string) []Rule {
	if which == "out" {
		if c.LogDrops {
			return []Rule{
				{Comment: "ks-terminal-out", Counter: "output_leak_drops"},
				{Comment: "ks-log-out",
					Log: `limit rate 3/minute burst 5 packets log prefix "ks-drop-out: " level warn`,
					Verdict: "drop"},
			}
		}
		return []Rule{{Comment: "ks-terminal-out", Counter: "output_leak_drops", Verdict: "drop"}}
	}
	counter := "forward_leak_drops"
	expr := extra
	if c.LogDrops {
		return []Rule{
			{Comment: "ks-terminal-fwd", Expr: expr, Counter: counter},
			{Comment: "ks-log-fwd", Expr: expr,
				Log:     `limit rate 3/minute burst 5 packets log prefix "ks-drop-fwd: " level warn`,
				Verdict: "drop"},
		}
	}
	return []Rule{{Comment: "ks-terminal-fwd", Expr: expr, Counter: counter, Verdict: "drop"}}
}

func buildMangle(c *Config, wanRef string) *Table {
	if !c.MSSClamp && !c.TTLSet {
		return nil
	}
	ch := Chain{
		Name: MangleChain, Type: "filter", Hook: "postrouting",
		Priority: c.ManglePriority, Policy: "accept",
	}
	if c.MSSClamp {
		ch.Rules = append(ch.Rules, Rule{
			Comment: "ks-mss-clamp",
			Expr:    "oifname " + wanRef + " tcp flags syn tcp option maxseg size set rt mtu",
			Anon:    true,
		})
	}
	if c.TTLSet {
		ch.Rules = append(ch.Rules, Rule{
			Comment: "ks-ttl-clamp",
			Expr:    "oifname " + wanRef + " ip ttl set 64",
			Anon:    true,
		})
	}
	return &Table{
		Family: "inet",
		Name:   c.MangleTable,
		Sets: []Set{
			{Name: c.WANSet, Type: "ifname"},
		},
		Chains: []Chain{ch},
	}
	}
	// ---------------------------------------------------------------------------
	// rendering
	// ---------------------------------------------------------------------------

	// Render produces a complete nft script. `nft -f` on this file is one atomic
	// transaction: either the whole ruleset lands or nothing changes.
	func Render(tables []Table) string {
		var b strings.Builder
		for _, t := range tables {
			fmt.Fprintf(&b, "add table %s %s\n", t.Family, t.Name)
			fmt.Fprintf(&b, "flush table %s %s\n", t.Family, t.Name)
			for _, c := range t.Counters {
				fmt.Fprintf(&b, "add counter %s %s %s\n", t.Family, t.Name, c)
			}
			for _, s := range t.Sets {
				b.WriteString(renderSet(t.Family, t.Name, s))
			}
			for _, ch := range t.Chains {
				fmt.Fprintf(&b, "add chain %s %s %s { type %s hook %s priority %d; policy %s; }\n",
					    t.Family, t.Name, ch.Name, ch.Type, ch.Hook, ch.Priority, ch.Policy)
				for _, r := range ch.Rules {
					b.WriteString(renderRule(t.Family, t.Name, ch.Name, r))
				}
			}
			b.WriteString("\n")
		}
		return b.String()
	}

	func renderSet(family, table string, s Set) string {
		var b strings.Builder
		fmt.Fprintf(&b, "add set %s %s %s { type %s", family, table, s.Name, s.Type)
		if len(s.Flags) > 0 {
			b.WriteString("; flags " + strings.Join(s.Flags, ","))
		}
		if len(s.Elements) > 0 {
			b.WriteString("; elements = { " + renderElements(s) + " }")
		}
		b.WriteString("; }\n")
		return b.String()
	}

	func renderElements(s Set) string {
		parts := make([]string, 0, len(s.Elements))
		for _, e := range s.Elements {
			if s.Quote {
				parts = append(parts, fmt.Sprintf("%q", e))
			} else {
				parts = append(parts, e)
			}
		}
		return strings.Join(parts, ", ")
	}

	func renderRule(family, table, chain string, r Rule) string {
		var b strings.Builder
		fmt.Fprintf(&b, "add rule %s %s %s", family, table, chain)
		if r.Expr != "" {
			b.WriteString(" " + r.Expr)
		}
		if r.Counter != "" {
			b.WriteString(" counter name " + r.Counter)
		}
		if r.Anon {
			b.WriteString(" counter")
		}
		if r.Log != "" {
			b.WriteString(" " + r.Log)
		}
		if r.Verdict != "" {
			b.WriteString(" " + r.Verdict)
		}
		fmt.Fprintf(&b, " comment %q\n", r.Comment)
		return b.String()
	}

	// RenderSetUpdate produces the minimal transaction that replaces a set's
	// contents: flush, then re-add. Used for WAN / gateway / VPS updates so we
	// never rebuild the whole ruleset for a data change.
	func RenderSetUpdate(family, table string, s Set) string {
		var b strings.Builder
		fmt.Fprintf(&b, "flush set %s %s %s\n", family, table, s.Name)
		if len(s.Elements) > 0 {
			fmt.Fprintf(&b, "add element %s %s %s { %s }\n",
				    family, table, s.Name, renderElements(s))
		}
		return b.String()
	}
