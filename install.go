// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// install.go — building and applying rulesets.

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// apply renders a model and commits it in one nft transaction.
func apply(c *Config, tables []Table, tag string) error {
	script := Render(tables)
	path, err := writeRulesFile(c.RulesDir, tag, script)
	if err != nil {
		return err
	}
	defer os.Remove(path)

	if err := nftApplyFile(path); err != nil {
		return fmt.Errorf("%s apply failed: %w", tag, err)
	}
	return nil
}

// applyDynamicSets writes the contents of the sets whose values change at
// runtime (VPN endpoints, WAN device, default gateway, ARP gateways).
//
// Why this exists: `nft flush table` only removes *rules* — chains, sets and
// stateful objects survive it (nftables documents this explicitly). That means
// `add set ... elements = { ... }` in a re-applied ruleset file is a no-op
// once the set already exists. Relying on inline elements would silently keep
// stale WAN/endpoint data after every reinstall, so we push the contents
// explicitly with flush + add element, which is unconditionally applied.
func applyDynamicSets(c *Config, mode, wan, gw string, vps []string) error {
	var b strings.Builder

	fmt.Fprintf(&b, "flush set inet %s %s\n", c.Table, c.Set)
	if len(vps) > 0 {
		fmt.Fprintf(&b, "add element inet %s %s { %s }\n", c.Table, c.Set, strings.Join(vps, ", "))
	}

	fmt.Fprintf(&b, "flush set inet %s %s\n", c.Table, c.WANSet)
	if wan != "" && wan != "unknown" {
		fmt.Fprintf(&b, "add element inet %s %s { %q }\n", c.Table, c.WANSet, wan)
	}

	// Mirror ks_wan into the mangle table: its rules reference @ks_wan, and a
	// set defined in killswitch_table is not visible from another nft table.
	// Only touch it when the mangle table exists (mss/ttl enabled) — a flush
	// on a missing set would abort the whole batch.
	if mode == "full" && (c.MSSClamp || c.TTLSet) {
		fmt.Fprintf(&b, "flush set inet %s %s\n", c.MangleTable, c.WANSet)
		if wan != "" && wan != "unknown" {
			fmt.Fprintf(&b, "add element inet %s %s { %q }\n", c.MangleTable, c.WANSet, wan)
		}
	}

	if mode == "full" {
		gwElems := []string{"255.255.255.255"}
		if gw != "" {
			gwElems = append(gwElems, gw)
		}
		fmt.Fprintf(&b, "flush set inet %s %s\n", c.Table, c.GWSet)
		fmt.Fprintf(&b, "add element inet %s %s { %s }\n", c.Table, c.GWSet, strings.Join(gwElems, ", "))
	}

	if c.ARPProtection && len(c.ARPGateways) > 0 {
		fmt.Fprintf(&b, "flush set arp killswitch_arp allowed_gw\n")
		fmt.Fprintf(&b, "add element arp killswitch_arp allowed_gw { %s }\n", strings.Join(c.ARPGateways, ", "))
		if len(c.ARPGatewayMACs) > 0 {
			fmt.Fprintf(&b, "flush set arp killswitch_arp allowed_gw_mac\n")
			fmt.Fprintf(&b, "add element arp killswitch_arp allowed_gw_mac { %s }\n", strings.Join(c.ARPGatewayMACs, ", "))
		}
	}

	path, err := writeRulesFile(c.RulesDir, "dynsets", b.String())
	if err != nil {
		return err
	}
	defer os.Remove(path)
	return nftApplyFile(path)
}

// InstallBaseline installs the emergency fail-close ruleset. It never depends
// on knowing the WAN device.
func InstallBaseline(c *Config, st *State, wan string) error {
	// The mangle table only exists in full mode. Dropping it here (outside the
	// transaction) mirrors the shell's behaviour: including `destroy` for a
	// table that may not exist would ENOENT the whole batch.
	_ = nftDestroy("inet", c.MangleTable)
	if !c.ARPProtection {
		_ = nftDestroy("arp", "killswitch_arp")
	}

	tables := BuildBaseline(c, wan)
	if err := apply(c, tables, "baseline"); err != nil {
		return err
	}
	// Contents, not just definitions: see applyDynamicSets.
	if err := applyDynamicSets(c, "baseline", wan, "", st.LastVPS); err != nil {
		return err
	}

	st.Mode = "baseline"
	st.WAN = wan
	if err := st.Save(); err != nil {
		Warnf("could not persist state: %v", err)
	}

	// Fail-close must also invalidate pre-existing sessions, otherwise they
	// keep flowing through ks-established.
	if conntrackAvailable() {
		if err := ConntrackFlush(); err != nil {
			Errorf("baseline conntrack flush failed: %v", err)
		}
	}
	// The boot-time emergency table (Layer 0) is now redundant. Destroy it
	// *after* our own ruleset is live, never before: it is the only thing
	// standing between the box and a fail-open window.
	_ = nftDestroy("inet", EmergencyTable)

	Warnf("baseline fail-close ruleset installed (wan=%s)", orUnknown(wan))
	return nil
}

// InstallFull installs the complete ruleset and verifies it. On verification
// failure it rolls back to baseline — the same fail-close contract as before.
func InstallFull(c *Config, st *State, wan, gw string, vps []string) error {
	if !c.ARPProtection {
		_ = nftDestroy("arp", "killswitch_arp")
	}
	tables := BuildFull(c, wan, gw, vps)
	if err := apply(c, tables, "full"); err != nil {
		return err
	}
	if err := applyDynamicSets(c, "full", wan, gw, vps); err != nil {
		return err
	}

	if err := Verify(c, tables, wan, gw, vps); err != nil {
		st.FullFail++
		_ = st.Save()
		Errorf("post-apply verification failed (%d/%d): %v",
		       st.FullFail, c.FullFailMax, err)
		if berr := InstallBaseline(c, st, wan); berr != nil {
			Errorf("rollback to baseline ALSO failed: %v", berr)
		}
		return err
	}

	// Mode transition invalidates established sessions: they were accepted
	// under the previous ruleset.
	if conntrackAvailable() {
		if err := ConntrackFlush(); err != nil {
			Errorf("conntrack flush after full install failed: %v", err)
		}
	}

	st.Mode = "full"
	st.WAN = wan
	st.GW = gw
	st.LastVPS = vps
	st.FullFail = 0
	if err := st.Save(); err != nil {
		Warnf("could not persist state: %v", err)
	}
	_ = nftDestroy("inet", EmergencyTable)

	Infof("full ruleset installed (wan=%s gw=%s vps=%d)", orUnknown(wan), orUnknown(gw), len(vps))
	return nil
}

func UpdateWANSet(c *Config, wan, gw string, withGW bool) error {
	if wan != "" && wan != "unknown" {
		s := Set{Name: c.WANSet, Type: "ifname", Quote: true, Elements: []string{wan}}
		if err := applySetUpdate(c, s); err != nil {
			return err
		}
		// Mirror into the mangle table when it exists — otherwise MSS/TTL
		// would keep clamping the previous WAN interface after a WAN change.
		if c.MSSClamp || c.TTLSet {
			if err := applySetUpdateTo(c, c.MangleTable, s); err != nil {
				return err
			}
		}
	}
	if withGW {
		elems := []string{"255.255.255.255"}
		if gw != "" {
			elems = append(elems, gw)
		}
		s := Set{Name: c.GWSet, Type: "ipv4_addr", Elements: elems}
		if err := applySetUpdate(c, s); err != nil {
			return err
		}
	}
	return nil
}

// applySetUpdate updates a set in the main table (killswitch_table).
func applySetUpdate(c *Config, s Set) error {
	return applySetUpdateTo(c, c.Table, s)
}

// applySetUpdateTo updates a set in an explicit table. Used for the mangle
// table, which mirrors ks_wan but lives in its own nft table.
func applySetUpdateTo(c *Config, table string, s Set) error {
	script := RenderSetUpdate("inet", table, s)
	path, err := writeRulesFile(c.RulesDir, "set", script)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	return nftApplyFile(path)
}

// UpdateVPSSet replaces the VPN endpoint set. Returns whether it narrowed.
func UpdateVPSSet(c *Config, st *State, vps []string) (bool, error) {
	prevLen := len(st.LastVPS)
	narrowing := IsNarrowing(st.LastVPS, vps)
	if sameStrings(st.LastVPS, vps) {
		return false, nil
	}

	s := Set{Name: c.Set, Type: "ipv4_addr", Flags: []string{"interval"}, Elements: vps}
	if err := applySetUpdate(c, s); err != nil {
		return narrowing, err
	}
	st.LastVPS = vps

	// FIX (review P1-4/P1-6): flushing conntrack is mandatory when the set
	// shrinks — otherwise stale sessions to a now-forbidden endpoint keep
	// passing through ks-established. For a pure widening there is no security
	// reason to interrupt LAN and SSH sessions, so we skip it by default.
	if narrowing || !c.FlushOnNarrowOnly {
		if !conntrackAvailable() {
			Errorf("CRITICAL: VPS set narrowed but conntrack utility missing — stale sessions persist")
			return narrowing, fmt.Errorf("conntrack unavailable during narrowing update")
		}
		if err := ConntrackFlush(); err != nil {
			// A failed flush right after a narrowing update is a real leak
			// window, not a cosmetic problem. Downgrade to fail-close.
			Errorf("CRITICAL: conntrack flush failed after narrowing update — downgrading to baseline")
			_ = InstallBaseline(c, st, st.WAN)
			return narrowing, err
		}
		Infof("VPS set narrowed (%d -> %d elements), conntrack flushed",
		      prevLen, len(vps))
	} else {
		Infof("VPS set widened (%d elements), conntrack preserved", len(vps))
	}
	return narrowing, nil
}

// VPSSourceResult is the outcome of reading passwall2's endpoint set.
type VPSSourceResult struct {
	Elements []string
	OK       bool   // source readable and typed correctly
	Missing  bool   // source table/set does not exist
	Type     string // nft set type, when known
}

// ReadVPSSource reads the upstream VPN endpoint set.
func ReadVPSSource(c *Config) VPSSourceResult {
	var res VPSSourceResult

	if !nftTableExists(c.SourceFamily, c.SourceTable) {
		res.Missing = true
		return res
	}
	b, err := nftJSON("list", "set", c.SourceFamily, c.SourceTable, c.SourceSet)
	if err != nil {
		Debugf("read source set: %v", err)
		res.Missing = true
		return res
	}
	d, err := parseNFTDoc(b)
	if err != nil {
		return res
	}
	for _, item := range d.Nftables {
		raw, ok := item["set"]
		if !ok {
			continue
		}
		var s nftSet
		if !decode(raw, &s) || s.Name != c.SourceSet {
			continue
		}
		if t, ok := s.Type.(string); ok && t != "" {
			res.Type = t
			if t != "ipv4_addr" {
				Errorf("source set %s has type %q, expected ipv4_addr; refusing to use it",
				       c.SourceSet, t)
				return res
			}
		}
		res.Elements = setElementsFromJSON(s.Elem)
		res.OK = true
	}
	return res
}

// EmptyGraceElapsed reports whether an unhealthy source has stayed unhealthy
// longer than the configured grace period.
func (s *State) EmptyGraceElapsed(c *Config, now time.Time) bool {
	if s.EmptySince.IsZero() {
		return false
	}
	return now.Sub(s.EmptySince) >= c.EmptyGrace()
}

func (s *State) MarkEmpty(now time.Time) {
	if s.EmptySince.IsZero() {
		s.EmptySince = now
	}
}

func (s *State) ClearEmpty() {
	s.EmptySince = time.Time{}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// RemoveAll deletes every nft object ksd owns (force_stop path).
func RemoveAll(c *Config) error {
	var errs []string
	_ = nftDestroy("inet", EmergencyTable)
	if err := nftDestroy("inet", c.Table); err != nil {
		errs = append(errs, err.Error())
	}
	_ = nftDestroy("inet", c.MangleTable)
	_ = nftDestroy("arp", "killswitch_arp")
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
