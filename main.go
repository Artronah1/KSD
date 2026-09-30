// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// main.go — CLI entry point.

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const ksdVersion = "1.1.2"

func usage() {
	fmt.Fprintf(os.Stderr, `ksd %s — nftables killswitch daemon for OpenWrt

usage:
  ksd run                     run in foreground (used by procd)
  ksd install                 install baseline, then full if WAN is ready
  ksd baseline                install the emergency fail-close ruleset only
  ksd status                  show detailed status
  ksd dry-run [full|baseline] print the generated ruleset, apply nothing
  ksd self-test               verify the live ruleset and probe for DNS leaks
  ksd reset-counters          zero all named counters
  ksd remove-all              delete every nft object ksd owns (force_stop)
  ksd version

flags:
  -config <path>   config file (default /etc/config/killswitch)
`, ksdVersion)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	// Global flags are parsed per subcommand (see load()), so that
	// `ksd dry-run baseline` does not confuse a positional arg with a flag.
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "install":
		cmdInstall(os.Args[2:])
	case "baseline":
		cmdBaseline(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "dry-run", "dryrun":
		cmdDryRun(os.Args[2:])
	case "self-test", "selftest":
		cmdSelfTest(os.Args[2:])
	case "reset-counters":
		cmdResetCounters(os.Args[2:])
	case "remove-all":
		cmdRemoveAll(os.Args[2:])
	case "version":
		fmt.Println(ksdVersion)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// load prepares config + logger + state. It never exits: on invalid config it
// returns nil and the caller should fall back to baseline.
func load(args []string) (*Config, *State, bool) {
	fs := flag.NewFlagSet("cmd", flag.ExitOnError)
	configPath := fs.String("config", DefaultConfigPath, "config file")
	debug := fs.Bool("debug", false, "debug logging")
	_ = fs.Parse(args)

	cfg, err := LoadConfig(*configPath)
	fromFile := true
	if err != nil {
		Errorf("cannot load config: %v — using built-in defaults", err)
		cfg, fromFile = DefaultConfig(), false
	} else if err := cfg.Validate(); err != nil {
		Errorf("invalid configuration: %v — using built-in defaults", err)
		cfg, fromFile = DefaultConfig(), false
	}

	if *debug {
		L.SetLevel(LevelDebug)
	}
	if cfg.LogFile != "" {
		L.SetFile(cfg.LogFile, cfg.LogMaxKB)
	}
	return cfg, LoadState(cfg.StatePath), fromFile
}

// ---------------------------------------------------------------------------
// subcommands
// ---------------------------------------------------------------------------

func cmdRun(args []string) {
	cfg, st, ok := load(args)
	if !ok {
		// FIX (review P2-3): a broken config must not mean "no firewall".
		// Install the fail-close baseline from the built-in defaults and stop.
		if cfg != nil {
			Errorf("configuration unusable — installing baseline only")
			st := LoadState(cfg.StatePath)
			wan := DetectWAN(cfg)
			if err := InstallBaseline(cfg, st, wan.Device); err != nil {
				Errorf("baseline install failed: %v", err)
			}
		}
		os.Exit(1)
	}

	if cfg.ForwardProtect {
		if fo, fohw := FlowOffloading(DefaultFirewall); fo || fohw {
			Errorf("CRITICAL: flow_offloading=%v hw=%v while forward_protect=1 — forwarded traffic bypasses killswitch_forward",
				fo, fohw)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { s := <-sigCh; Infof("signal %v", s); cancel() }()
	if st.Mode == "full" {
		selfHealOnStart(cfg, st)
	}

	NewDaemon(cfg, st).Run(ctx)
	if cfg.ForceStop {
		Warnf("force_stop=1 — removing all killswitch objects")
		_ = RemoveAll(cfg)
		st.Mode, st.LastVPS, st.FullFail = "removed", nil, 0
		_ = st.Save()
	}
}

// selfHealOnStart verifies the live ruleset against the model and, if it
// diverges, reinstalls full mode in place (single nft transaction, no
// RemoveAll, no conntrack flush). If reinstall fails, falls back to
// baseline.
func selfHealOnStart(cfg *Config, st *State) {
	wan := DetectWAN(cfg)
	if wan.Device == "" || !wan.Ready {
		Debugf("self-heal skipped: WAN not ready")
		return
	}

	src := ReadVPSSource(cfg)
	vps, _ := NormalizeVPS(src.Elements, cfg.MaxVPSElements)
	if cfg.MergeStaticVPS && len(cfg.StaticVPS) > 0 {
		vps = append(vps, cfg.StaticVPS...)
		if nv, err := NormalizeVPS(vps, cfg.MaxVPSElements); err == nil {
			vps = nv
		}
	}

	tables := BuildFull(cfg, wan.Device, wan.GW, vps)
	if err := Verify(cfg, tables, wan.Device, wan.GW, vps); err == nil {
		Debugf("self-heal: live ruleset matches model")
		return
	} else {
		Warnf("self-heal: live ruleset does not match model (%v); reinstalling in-place", err)
	}

	if err := InstallFull(cfg, st, wan.Device, wan.GW, vps); err != nil {
		Errorf("self-heal: in-place reinstall failed: %v; falling back to baseline", err)
		if berr := InstallBaseline(cfg, st, wan.Device); berr != nil {
			Errorf("self-heal: baseline fallback also failed: %v", berr)
		}
	}
}

func cmdInstall(args []string) {
	cfg, st, ok := load(args)
	if !ok {
		os.Exit(1)
	}
	wan := DetectWAN(cfg)
	if err := InstallBaseline(cfg, st, wan.Device); err != nil {
		Errorf("baseline install failed: %v", err)
		os.Exit(1)
	}
	if wan.Ready {
		d := NewDaemon(cfg, st)
		d.tryFull(wan)
	} else {
		Warnf("WAN not ready; staying in baseline")
	}
	fmt.Printf("mode=%s wan=%s\n", st.Mode, orUnknown(wan.Device))
}

func cmdBaseline(args []string) {
	cfg, st, ok := load(args)
	if !ok {
		os.Exit(1)
	}
	wan := DetectWAN(cfg)
	if err := InstallBaseline(cfg, st, wan.Device); err != nil {
		Errorf("baseline install failed: %v", err)
		os.Exit(1)
	}
	fmt.Println("baseline installed")
}

func cmdDryRun(args []string) {
	// Separate positional (mode) from flags.
	mode := "full"
	flagArgs := []string{}
	for _, a := range args {
		if a == "baseline" || a == "full" {
			mode = a
		} else {
			flagArgs = append(flagArgs, a)
		}
	}

	cfg, _, ok := load(flagArgs)
	if !ok {
		os.Exit(1)
	}

	wan := DetectWAN(cfg)
	var script string
	if mode == "baseline" {
		script = Render(BuildBaseline(cfg, wan.Device))
	} else {
		if wan.Device == "" {
			fmt.Fprintln(os.Stderr, "WAN not found; try: ksd dry-run baseline")
			os.Exit(1)
		}
		src := ReadVPSSource(cfg)
		elements, err := NormalizeVPS(src.Elements, cfg.MaxVPSElements)
		if err != nil {
			Warnf("%v (rendering with an empty set)", err)
		}
		script = Render(BuildFull(cfg, wan.Device, wan.GW, elements))
	}
	fmt.Print(script)
	fmt.Fprintln(os.Stderr, "\n--- rules are NOT applied ---")
}

func cmdResetCounters(args []string) {
	cfg, st, ok := load(args)
	if !ok {
		os.Exit(1)
	}
	if err := nftResetCounters("inet", cfg.Table); err != nil {
		Errorf("reset counters failed: %v", err)
		os.Exit(1)
	}
	// Drop the delta baseline too, otherwise the next poll computes a negative
	// delta and reports nothing.
	st.Counters = map[string]uint64{}
	st.CountersInit = false
	_ = st.Save()
	fmt.Println("counters reset")
}

func cmdRemoveAll(args []string) {
	cfg, st, ok := load(args)
	if !ok {
		os.Exit(1)
	}
	if err := RemoveAll(cfg); err != nil {
		Errorf("%v", err)
		os.Exit(1)
	}
	st.Mode = "baseline"
	st.LastVPS = nil
	_ = st.Save()
	fmt.Println("killswitch objects removed")
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func cmdStatus(args []string) {
	cfg, st, ok := load(args)
	if !ok {
		fmt.Println("=== ksd Status ===")
		fmt.Println("[ERROR] invalid configuration")
		os.Exit(1)
	}

	fmt.Println("=== ksd Status ===")
	if nftTableExists("inet", cfg.Table) {
		fmt.Println("[OK]   Killswitch ACTIVE")
	} else {
		fmt.Println("[FAIL] Killswitch INACTIVE")
	}

	wan := DetectWAN(cfg)
	counters, _ := ReadCounters("inet", cfg.Table)
	vps, _, _ := ReadSetElements("inet", cfg.Table, cfg.Set)

	fmt.Printf("  Mode             : %s\n", st.Mode)
	fmt.Printf("  WAN device       : %s (exists=%v, l3-ready=%v)\n",
		orUnknown(wan.Device), wan.Device != "", wan.Ready)
	fmt.Printf("  Default gateway  : %s\n", orUnknown(wan.GW))
	fmt.Printf("  Table            : inet %s\n", cfg.Table)
	fmt.Printf("  Priorities       : out=%d fwd=%d mangle=%d\n",
		cfg.FilterPriority, cfg.ForwardFilterPriority, cfg.ManglePriority)
	fmt.Printf("  Trusted ifaces   : %s\n", strings.Join(cfg.AllowedIfaces, " "))
	fmt.Printf("  VPS set elements : %d\n", len(vps))
	fmt.Printf("  Poll / verify    : %ds / %ds\n", cfg.PollIntervalSec, cfg.VerifyIntervalSec)

	src := ReadVPSSource(cfg)
	switch {
	case src.Missing:
		fmt.Printf("  Source (%s %s) : DOWN\n", cfg.SourceTable, cfg.SourceSet)
	case src.OK:
		fmt.Printf("  Source (%s %s) : ok (%d elements)\n",
			cfg.SourceTable, cfg.SourceSet, len(src.Elements))
	default:
		fmt.Printf("  Source (%s %s) : UNUSABLE\n", cfg.SourceTable, cfg.SourceSet)
	}
	if !st.EmptySince.IsZero() {
		fmt.Printf("  Source unhealthy : %.0fs\n", time.Since(st.EmptySince).Seconds())
	}

	fmt.Println("  Counters:")
	for _, name := range []string{
		"allow_mark", "allow_vps", "allow_established",
		"dns_leak_drops", "quic_drops", "invalid_state_drops", "output_leak_drops",
		"allow_mark_fwd", "allow_vps_fwd", "allow_established_fwd",
		"dns_leak_drops_fwd", "quic_drops_fwd", "invalid_state_drops_fwd",
		"forward_leak_drops",
	} {
		fmt.Printf("    %-24s %d\n", name, counters[name])
	}

	fmt.Println("  Configuration:")
	fmt.Printf("    strict         : %v\n", cfg.StrictMode)
	fmt.Printf("    dns / doq / doh: %v / %v / %v (%d doh IPs)\n",
		cfg.DNSBlock, cfg.DoQBlock, cfg.DoHBlock, len(cfg.DOHServers))
	fmt.Printf("    quic / ipv6    : %v / %v\n", cfg.QUICBlock, cfg.IPv6Block)
	fmt.Printf("    mss / ttl      : %v / %v\n", cfg.MSSClamp, cfg.TTLSet)
	fmt.Printf("    forward protect: %v\n", cfg.ForwardProtect)
	fmt.Printf("    marks          : %s\n", strings.Join(cfg.AllowedMarks, " "))
	fmt.Printf("    empty grace    : %ds\n", cfg.EmptySourceGraceSec)
	fmt.Printf("    max elements   : %d\n", cfg.MaxVPSElements)
	fmt.Printf("    full fails     : %d / %d\n", st.FullFail, cfg.FullFailMax)

	if cfg.ForwardProtect {
		if fo, fohw := FlowOffloading(DefaultFirewall); fo || fohw {
			fmt.Printf("    [WARN] flow offloading enabled (sw=%v hw=%v) — forward_protect may be bypassed\n", fo, fohw)
		}
	}
	if cfg.StrictMode && len(vps) == 0 {
		fmt.Println("    [WARN] strict mode + empty VPS set = all non-marked traffic blocked (by design)")
	}
}

// ---------------------------------------------------------------------------
// self-test
// ---------------------------------------------------------------------------

func cmdSelfTest(args []string) {
	cfg, st, ok := load(args)
	if !ok {
		os.Exit(1)
	}
	fail := 0
	report := func(ok bool, format string, a ...interface{}) {
		tag := "[OK]  "
		if !ok {
			tag = "[FAIL]"
			fail++
		}
		fmt.Printf("%s %s\n", tag, fmt.Sprintf(format, a...))
	}

	fmt.Println("=== ksd Self-Test ===")

	if fo, fohw := FlowOffloading(DefaultFirewall); fo || fohw {
		fmt.Printf("[WARN] flow_offloading=%v hw=%v — forward_protect may be bypassed\n", fo, fohw)
	}

	report(nftTableExists("inet", cfg.Table), "table inet %s present", cfg.Table)

	wan := DetectWAN(cfg)
	fmt.Printf("       WAN: %s (ready=%v)\n", orUnknown(wan.Device), wan.Ready)

	tables := BuildFor(cfg, st.Mode, wan.Device, wan.GW, st.LastVPS)
	err := Verify(cfg, tables, wan.Device, wan.GW, st.LastVPS)
	report(err == nil, "live ruleset matches model (%v)", err)

	vps, found, _ := ReadSetElements("inet", cfg.Table, cfg.Set)
	report(found, "VPS set %s present", cfg.Set)
	if len(vps) == 0 {
		fmt.Printf("[WARN] VPS set empty — outbound is fully blocked (strict fail-close)\n")
	}

	if conntrackAvailable() {
		report(true, "conntrack utility available")
	} else {
		fmt.Printf("[WARN] conntrack utility missing — full mode will be refused\n")
	}

	// Live DNS leak probe.
	if cfg.DNSBlock {
		before, _ := ReadCounters("inet", cfg.Table)
		leaked := probeDNS()
		after, _ := ReadCounters("inet", cfg.Table)
		report(!leaked, "DNS query to 1.1.1.1 blocked (expected)")
		// FIX (review P2-5): the probe itself increments dns_leak_drops, which
		// the daemon would otherwise report as a real leak on the next poll.
		st.Counters["dns_leak_drops"] = after["dns_leak_drops"]
		st.MarkCountersInitialized()
		_ = st.Save()
		if d := after["dns_leak_drops"] - before["dns_leak_drops"]; d > 0 {
			fmt.Printf("       (probe consumed %d counter hit(s), delta baseline rebased)\n", d)
		}
	}

	fmt.Println("===========================")
	if fail == 0 {
		fmt.Println("Result: PASS (see WARN lines if any)")
		return
	}
	fmt.Printf("Result: FAIL (%d critical check(s))\n", fail)
	os.Exit(1)
}

// probeDNS tries a real DNS lookup that the killswitch should drop.
// Returns true if the query succeeded, i.e. a DNS leak.
func probeDNS() bool {
	type probe struct {
		bin  string
		args []string
	}
	candidates := []probe{
		{"dig", []string{"@1.1.1.1", "example.com", "+time=2", "+tries=1", "+noall", "+answer"}},
		{"nslookup", []string{"example.com", "1.1.1.1"}},
	}
	for _, p := range candidates {
		path, err := exec.LookPath(p.bin)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, p.args...)
		out, err := cmd.CombinedOutput()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			return true // query succeeded => leak
		}
		return false
	}
	fmt.Println("[SKIP] live DNS probe (install bind-dig or busybox nslookup)")
	return false
}
