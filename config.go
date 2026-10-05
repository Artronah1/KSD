// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// config.go — typed configuration for ksd, loaded from /etc/config/killswitch.
//
// This replaces ~250 lines of shell `config_get` + hand-rolled regex
// validation. Every field is a real Go type, validated once at load time, so
// the rest of the program can assume it is well-formed.

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultConfigPath = "/etc/config/killswitch"
	DefaultFirewall   = "/etc/config/firewall"

	// EmergencyTable is installed by the Layer-0 boot script before ksd even
	// starts. ksd destroys it as soon as its own ruleset is live.
	EmergencyTable = "ks_emergency"
)

type Config struct {
	// --- tables / sets ---
	Table       string
	Set         string
	WANSet      string // dynamic: holds the current WAN device
	GWSet       string // dynamic: holds {255.255.255.255, <default gw>}
	TrustedSet  string
	DOHSet      string
	NTPSet      string
	MangleTable string

	// --- source of truth for VPN endpoints ---
	SourceFamily   string // inet
	SourceTable    string // passwall2
	SourceSet      string // psw2_vps
	SourceDisabled bool   // source_set = "none": trust marks + static_vps only

	// --- network ---
	WANInterface   string
	WANDevice      string
	AllowedIfaces  []string
	AllowedMarks   []string
	StaticVPS      []string
	MergeStaticVPS bool

	DOHServers []string
	NTPServers []string

	// --- feature switches ---
	StrictMode     bool
	DNSBlock       bool
	DoQBlock       bool
	DoHBlock       bool
	QUICBlock      bool
	IPv6Block      bool
	MSSClamp       bool
	TTLSet         bool
	ForwardProtect bool
	ARPProtection  bool

	// --- ARP ---
	ARPInterface   string
	ARPGateways    []string
	ARPGatewayMACs []string

	// --- priorities ---
	FilterPriority        int
	ForwardFilterPriority int
	ManglePriority        int

	// --- behaviour ---
	MaxVPSElements      int
	EmptySourceGraceSec int // replaces EMPTY_SOURCE_THRESHOLD (count-based)
	PollIntervalSec     int // replaces the 5-minute cron
	VerifyIntervalSec   int
	FullFailMax         int // breaker: N consecutive full-mode failures
	FlushOnNarrowOnly   bool
	LogDrops            bool
	LogSuspicious       bool
	LogFile             string
	LogMaxKB            int64
	ForceStop           bool

	// --- paths ---
	StatePath string
	RulesDir  string
}

func DefaultConfig() *Config {
	return &Config{
		Table:       "killswitch_table",
		Set:         "vps_ipv4",
		WANSet:      "ks_wan",
		GWSet:       "ks_wan_gw",
		TrustedSet:  "trusted_ifaces",
		DOHSet:      "doh_servers",
		NTPSet:      "ntp_bootstrap",
		MangleTable: "killswitch_mangle",

		SourceFamily: "inet",
		SourceTable:  "passwall2",
		SourceSet:    "psw2_vps",

		WANInterface:  "wan",
		AllowedIfaces: []string{"lo", "br-lan"},
		AllowedMarks:  []string{"0x50535732", "0x000000ff"},

		StrictMode:     true,
		DNSBlock:       true,
		DoQBlock:       true,
		DoHBlock:       true,
		QUICBlock:      false,
		IPv6Block:      true,
		MSSClamp:       true,
		TTLSet:         false,
		ForwardProtect: true,

		FilterPriority:        -10,
		ForwardFilterPriority: -15,
		ManglePriority:        -150,

		MaxVPSElements:      5000,
		EmptySourceGraceSec: 15,
		PollIntervalSec:     5,
		VerifyIntervalSec:   60,
		FullFailMax:         3,
		FlushOnNarrowOnly:   true,
		LogSuspicious:       true,

		StatePath: "/var/run/ksd-state.json",
		RulesDir:  "/tmp/ksd",
	}
}

// ---------------------------------------------------------------------------
// loading
// ---------------------------------------------------------------------------

func LoadConfig(path string) (*Config, error) {
	c := DefaultConfig()

	u, err := LoadUCI(path)
	if err != nil {
		if os.IsNotExist(err) {
			Warnf("config %s missing, using built-in defaults", path)
			return c, nil
		}
		return nil, err
	}

	main := u.Lookup("killswitch", "main")
	if main == nil {
		Warnf("no 'config killswitch' section in %s, using defaults", path)
		return c, nil
	}

	getBool := func(key string, def bool) bool {
		v, ok := main.Options[key]
		if !ok {
			return def
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on", "enabled":
			return true
		case "0", "false", "no", "off", "disabled":
			return false
		default:
			Warnf("option %s=%q is not a boolean, using default (%v)", key, v, def)
			return def
		}
	}
	getInt := func(key string, def int) int {
		v, ok := main.Options[key]
		if !ok {
			return def
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			Warnf("option %s=%q is not an integer, using %d", key, v, def)
			return def
		}
		return n
	}
	getStr := func(key, def string) string {
		if v, ok := main.Options[key]; ok && v != "" {
			return v
		}
		return def
	}
	getList := func(key string, def []string) []string {
		if v, ok := main.Lists[key]; ok && len(v) > 0 {
			return v
		}
		if v, ok := main.Options[key]; ok && v != "" {
			return strings.Fields(v)
		}
		return def
	}

	c.Table = getStr("table_name", c.Table)
	c.Set = getStr("set_name", c.Set)
	c.WANInterface = getStr("wan_interface", c.WANInterface)
	c.WANDevice = getStr("wan_device", c.WANDevice)

	c.StrictMode = getBool("strict_mode", c.StrictMode)
	c.DNSBlock = getBool("dns_block_enabled", c.DNSBlock)
	c.DoQBlock = getBool("doq_block_enabled", c.DoQBlock)
	c.DoHBlock = getBool("doh_block_enabled", c.DoHBlock)
	c.QUICBlock = getBool("quic_block_enabled", c.QUICBlock)
	c.IPv6Block = getBool("ipv6_block_enabled", c.IPv6Block)
	c.MSSClamp = getBool("mss_clamp_enabled", c.MSSClamp)
	c.TTLSet = getBool("ttl_set_enabled", c.TTLSet)
	c.ForwardProtect = getBool("forward_protect", c.ForwardProtect)
	c.ARPProtection = getBool("arp_protection", false)
	c.LogDrops = getBool("log_drops", false)
	c.LogSuspicious = getBool("log_suspicious", c.LogSuspicious)
	c.ForceStop = getBool("force_stop", false)
	c.MergeStaticVPS = getBool("merge_static_vps", false)
	c.FlushOnNarrowOnly = getBool("flush_on_narrow_only", c.FlushOnNarrowOnly)

	c.FilterPriority = getInt("filter_priority", c.FilterPriority)
	// legacy alias kept from the shell script
	if v, ok := main.Options["forward_filter_prio"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ForwardFilterPriority = n
		}
	}
	c.ForwardFilterPriority = getInt("forward_filter_priority", c.ForwardFilterPriority)
	c.ManglePriority = getInt("mangle_priority", c.ManglePriority)

	c.MaxVPSElements = getInt("max_vps_elements", c.MaxVPSElements)
	c.EmptySourceGraceSec = getInt("empty_source_grace_sec", c.EmptySourceGraceSec)
	c.PollIntervalSec = getInt("poll_interval_sec", c.PollIntervalSec)
	c.VerifyIntervalSec = getInt("verify_interval_sec", c.VerifyIntervalSec)
	c.FullFailMax = getInt("full_fail_max", c.FullFailMax)

	c.LogFile = getStr("log_file", "")
	c.LogMaxKB = int64(getInt("log_max_kb", 512))

	c.AllowedMarks = getList("allowed_mark", c.AllowedMarks)
	c.AllowedIfaces = getList("allowed_iface", c.AllowedIfaces)
	c.StaticVPS = splitCSV(getList("static_vps", nil))
	c.NTPServers = getList("ntp_servers", nil)

	if v, ok := main.Options["source_set"]; ok && v != "" {
		switch {
		case v == "none":
			c.SourceDisabled = true
			c.SourceFamily, c.SourceTable, c.SourceSet = "", "", ""
		default:
			parts := strings.Fields(v)
			if len(parts) == 3 {
				c.SourceFamily, c.SourceTable, c.SourceSet = parts[0], parts[1], parts[2]
			} else {
				Warnf("source_set=%q invalid, expected '<family> <table> <set>' or 'none'", v)
			}
		}
	}

	// DoH servers live in their own section (matches the original layout).
	if doh := u.Lookup("doh", ""); doh != nil {
		if v, ok := doh.Lists["server"]; ok {
			c.DOHServers = v
		} else if v, ok := doh.Options["server"]; ok && v != "" {
			c.DOHServers = strings.Fields(v)
		}
	}

	if arp := u.Lookup("arp", ""); arp != nil {
		c.ARPInterface, _ = arp.Options["interface"]
		c.ARPGateways = append(c.ARPGateways, arp.Lists["gateway"]...)
		c.ARPGatewayMACs = append(c.ARPGatewayMACs, arp.Lists["gateway_mac"]...)
	}
	if v, ok := main.Options["arp_interface"]; ok {
		c.ARPInterface = v
	}
	c.ARPGateways = append(c.ARPGateways, getList("arp_gateway", nil)...)
	c.ARPGatewayMACs = append(c.ARPGatewayMACs, getList("arp_gateway_mac", nil)...)

	return c, nil
}

func splitCSV(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

var (
	reName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	reIface = regexp.MustCompile(`^[a-zA-Z0-9._:@-]+$`)
	reMark  = regexp.MustCompile(`^(0x[0-9a-fA-F]+|\d+)(/(0x[0-9a-fA-F]+|\d+))?$`)
	reMAC   = regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)
)

// normalizeMark converts a user-supplied mark to canonical form:
//
//	"6666"        → "0x1a0a"
//	"0xff"        → "0xff"
//	"0xff/0xff"   → "0xff/0xff"
//	"255/255"     → "0xff/0xff"
func normalizeMark(s string) (string, error) {
	var valStr, maskStr string
	if i := strings.Index(s, "/"); i >= 0 {
		valStr, maskStr = s[:i], s[i+1:]
	} else {
		valStr = s
	}

	parse := func(x string) (uint64, error) {
		x = strings.TrimSpace(x)
		if x == "" {
			return 0, fmt.Errorf("empty")
		}
		if strings.HasPrefix(x, "0x") || strings.HasPrefix(x, "0X") {
			return strconv.ParseUint(x[2:], 16, 32)
		}
		return strconv.ParseUint(x, 10, 32)
	}

	val, err := parse(valStr)
	if err != nil {
		return "", fmt.Errorf("invalid mark value %q", valStr)
	}
	if maskStr == "" {
		return fmt.Sprintf("0x%x", val), nil
	}
	mask, err := parse(maskStr)
	if err != nil {
		return "", fmt.Errorf("invalid mask %q", maskStr)
	}
	return fmt.Sprintf("0x%x/0x%x", val, mask), nil
}

func (c *Config) Validate() error {
	if !reName.MatchString(c.Table) {
		return fmt.Errorf("invalid table_name %q", c.Table)
	}
	if !reName.MatchString(c.Set) {
		return fmt.Errorf("invalid set_name %q", c.Set)
	}
	if c.WANInterface == "" {
		return fmt.Errorf("wan_interface is empty")
	}
	if !reIface.MatchString(c.WANInterface) {
		return fmt.Errorf("invalid wan_interface %q", c.WANInterface)
	}
	if !c.SourceDisabled {
		if !reName.MatchString(c.SourceFamily) || !reName.MatchString(c.SourceTable) ||
			!reName.MatchString(c.SourceSet) {
			return fmt.Errorf("invalid source_set %q %q %q",
				c.SourceFamily, c.SourceTable, c.SourceSet)
		}
	}

	if len(c.AllowedMarks) == 0 {
		return fmt.Errorf("allowed_mark is empty")
	}
	for i, m := range c.AllowedMarks {
		nm, err := normalizeMark(m)
		if err != nil {
			return fmt.Errorf("invalid mark %q: %v", m, err)
		}
		c.AllowedMarks[i] = nm
	}
	if len(c.AllowedIfaces) == 0 {
		return fmt.Errorf("allowed_iface is empty")
	}
	for _, i := range c.AllowedIfaces {
		if !reIface.MatchString(i) {
			return fmt.Errorf("invalid iface %q", i)
		}
	}
	if c.WANDevice != "" && !reIface.MatchString(c.WANDevice) {
		return fmt.Errorf("invalid wan_device %q", c.WANDevice)
	}
	for _, ip := range c.StaticVPS {
		if !validVPSElem(ip) {
			return fmt.Errorf("invalid static_vps %q", ip)
		}
	}
	for _, ip := range c.DOHServers {
		if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
			return fmt.Errorf("invalid doh server %q", ip)
		}
	}
	for _, ip := range c.NTPServers {
		if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
			return fmt.Errorf("invalid ntp server %q", ip)
		}
	}
	if c.MaxVPSElements <= 0 {
		return fmt.Errorf("max_vps_elements must be > 0")
	}
	if c.FullFailMax < 1 {
		return fmt.Errorf("full_fail_max must be >= 1")
	}
	if c.LogMaxKB < 16 {
		return fmt.Errorf("log_max_kb must be >= 16")
	}
	if !c.SourceDisabled {
		switch c.SourceFamily {
		case "inet", "ip", "ip6", "arp", "bridge", "netdev":
		default:
			return fmt.Errorf("invalid source_set family %q", c.SourceFamily)
		}
	}
	if c.PollIntervalSec <= 0 {
		return fmt.Errorf("poll_interval_sec must be > 0")
	}
	if c.VerifyIntervalSec < c.PollIntervalSec {
		return fmt.Errorf("verify_interval_sec must be >= poll_interval_sec")
	}
	if c.EmptySourceGraceSec < 0 {
		return fmt.Errorf("empty_source_grace_sec must be >= 0")
	}
	if c.ARPProtection {
		for _, ip := range c.ARPGateways {
			if net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
				return fmt.Errorf("invalid arp gateway %q", ip)
			}
		}
		for _, m := range c.ARPGatewayMACs {
			if !reMAC.MatchString(m) {
				return fmt.Errorf("invalid arp gateway_mac %q", m)
			}
		}
		if len(c.ARPGatewayMACs) == 0 {
			Warnf("arp_protection=1 but no gateway_mac: only gateway IP is checked, spoofing is NOT mitigated")
		}
	}
	if c.LogFile != "" && !strings.HasPrefix(c.LogFile, "/") {
		return fmt.Errorf("log_file must be an absolute path")
	}
	if c.DoHBlock && len(c.DOHServers) == 0 {
		Warnf("doh_block_enabled=1 but no doh servers configured — DoH blocking is a no-op")
	}
	return nil
}

func (c *Config) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalSec) * time.Second
}

func (c *Config) VerifyInterval() time.Duration {
	return time.Duration(c.VerifyIntervalSec) * time.Second
}

func (c *Config) EmptyGrace() time.Duration {
	return time.Duration(c.EmptySourceGraceSec) * time.Second
}

// / validVPSElem accepts a bare IPv4 address, a CIDR prefix, or a range "A-B".
func validVPSElem(s string) bool {
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil && strings.Contains(s, ".")
	}
	if strings.Contains(s, "-") {
		lo, hi, ok := splitRange(s)
		if !ok {
			return false
		}
		a := net.ParseIP(lo)
		b := net.ParseIP(hi)
		return a != nil && a.To4() != nil && b != nil && b.To4() != nil
	}
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// splitRange splits "A-B" into ("A", "B", true). Returns ok=false if the
// string does not contain exactly one '-' or either side is empty.
func splitRange(s string) (string, string, bool) {
	i := strings.Index(s, "-")
	if i <= 0 || i >= len(s)-1 {
		return "", "", false
	}
	lo := strings.TrimSpace(s[:i])
	hi := strings.TrimSpace(s[i+1:])
	if lo == "" || hi == "" {
		return "", "", false
	}
	return lo, hi, true
}
