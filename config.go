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
	SourceFamily string // inet
	SourceTable  string // passwall2
	SourceSet    string // psw2_vps

	// --- network ---
	WANInterface string
	WANDevice    string
	AllowedIfaces []string
	AllowedMarks  []string
	StaticVPS     []string
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
	MaxVPSElements       int
	EmptySourceGraceSec  int // replaces EMPTY_SOURCE_THRESHOLD (count-based)
	PollIntervalSec      int // replaces the 5-minute cron
	VerifyIntervalSec    int
	FullFailMax          int // breaker: N consecutive full-mode failures
	FlushOnNarrowOnly    bool
	LogDrops             bool
	LogSuspicious        bool
	LogFile              string
	LogMaxKB             int64
	ForceStop            bool

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
		return v == "1" || v == "true" || v == "yes" || v == "on" || v == "enabled"
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
		parts := strings.Fields(v)
		if len(parts) == 3 {
			c.SourceFamily, c.SourceTable, c.SourceSet = parts[0], parts[1], parts[2]
		} else {
			Warnf("source_set=%q invalid, expected '<family> <table> <set>'", v)
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
	reMark  = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	reMAC   = regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)
)

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
	if !reName.MatchString(c.SourceFamily) || !reName.MatchString(c.SourceTable) ||
		!reName.MatchString(c.SourceSet) {
		return fmt.Errorf("invalid source_set %q %q %q",
			c.SourceFamily, c.SourceTable, c.SourceSet)
	}

	if len(c.AllowedMarks) == 0 {
		return fmt.Errorf("allowed_mark is empty")
	}
	for _, m := range c.AllowedMarks {
		if !reMark.MatchString(m) {
			return fmt.Errorf("invalid mark %q (expected 0x...)", m)
		}
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

// validVPSElem accepts a bare IPv4 address or a CIDR prefix.
func validVPSElem(s string) bool {
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil && strings.Contains(s, ".")
	}
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}
