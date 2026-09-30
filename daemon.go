// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// daemon.go — the control loop.
//
// Replaces the 5-minute cron + `flock` + check_suspicious design. A resident
// process can afford to look every few seconds, which is what lets us delete
// the entire cache/TTL machinery: we no longer have to *guess* whether the
// upstream endpoint set changed between polls, we just look.
//
// Polling interval is configurable (poll_interval_sec, default 5). If you want
// true event-driven updates, replace Watch() below with a netlink subscription
// on the source set (google/nftables' Monitor with MonitorEventTypeNewSetElem)
// — the rest of the loop is unchanged.

import (
	"context"
	"time"
)

type Daemon struct {
	c  *Config
	st *State

	lastVerify  time.Time
	lastFullTry time.Time
	lastSave    time.Time
}

func NewDaemon(c *Config, st *State) *Daemon {
	return &Daemon{c: c, st: st}
}

// Run blocks until ctx is cancelled. It never returns a "success": the ruleset
// must stay installed for as long as ksd lives.
func (d *Daemon) Run(ctx context.Context) {
	c, st := d.c, d.st

	// 1. fail-close immediately, before anything else can go wrong.
	wan := d.waitForWAN(ctx)
	if err := InstallBaseline(c, st, wan.Device); err != nil {
		Errorf("CRITICAL: could not install baseline: %v", err)
	}

	// 2. try to reach full mode straight away (narrows the fail-close window).
	d.tryFull(wan)

	ticker := time.NewTicker(c.PollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			Infof("shutting down; ruleset intentionally retained (fail-close)")
			_ = st.Save()
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Daemon) waitForWAN(ctx context.Context) WANStatus {
	c := d.c
	var last WANStatus
	for i := 0; i < 3; i++ {
		last = DetectWAN(c)
		if last.Device != "" {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(time.Second):
		}
	}
	Warnf("WAN device not resolvable yet; baseline installed without it")
	return last
}

// tick is one poll cycle.
func (d *Daemon) tick(ctx context.Context) {
	c, st := d.c, d.st
	now := time.Now()

	wan := DetectWAN(c)
	if wan.Device == "" {
		// WAN gone: keep whatever ruleset we have (both are fail-close).
		Debugf("WAN device unknown, holding current mode=%s", st.Mode)
		return
	}

	switch st.Mode {
	case "full":
		d.tickFull(wan)
	default:
		// baseline: upgrade as soon as L3 is usable, but not on every 5s poll
		// (otherwise a permanently-failing full install would spam the log and
		// flush conntrack on each rollback).
		if wan.Ready && now.Sub(d.lastFullTry) >= c.VerifyInterval() {
			Warnf("WAN %s is up, attempting full mode", wan.Device)
			d.tryFull(wan)
		}
	}

	d.readCounters(now)

	if now.Sub(d.lastVerify) >= c.VerifyInterval() {
		d.lastVerify = now
		d.verifyTick(wan)
	}
}

func (d *Daemon) tickFull(wan WANStatus) {
	c, st := d.c, d.st

	// --- WAN / gateway changed? just update the sets ---
	if wan.Device != st.WAN || (wan.GW != "" && wan.GW != st.GW) {
		Infof("WAN changed %s -> %s (gw %s -> %s), updating sets",
			orUnknown(st.WAN), wan.Device, orUnknown(st.GW), orUnknown(wan.GW))
		if err := UpdateWANSet(c, wan.Device, wan.GW, true); err != nil {
			Errorf("WAN set update failed: %v", err)
		} else {
			st.WAN, st.GW = wan.Device, wan.GW
			_ = st.Save()
		}
		st.GW = wan.GW
		_ = st.Save()
	}

	// --- upstream VPN endpoints ---
	src := ReadVPSSource(c)
	now := time.Now()

	switch {
	case src.Missing && !src.OK:
		// passwall2 table gone: established sessions are now leaking.
		if !st.SourceDown {
			st.SourceDown = true
			_ = st.Save()
			Warnf("passwall2 table %s gone — flushing conntrack to kill stale direct sessions",
				c.SourceTable)
			if conntrackAvailable() {
				if err := ConntrackFlush(); err != nil {
					Errorf("conntrack flush after passwall2 loss failed: %v", err)
				}
			} else {
				Errorf("passwall2 gone and conntrack missing — stale sessions persist")
			}
		}
		d.handleEmptySource(now, "source unreadable")

	case src.OK:
		if st.SourceDown {
			st.SourceDown = false
			Infof("passwall2 table %s is back", c.SourceTable)
		}
		elems, err := NormalizeVPS(src.Elements, c.MaxVPSElements)
		if err != nil {
			Errorf("VPS update rejected: %v", err)
			return
		}
		if c.MergeStaticVPS && len(c.StaticVPS) > 0 {
			elems = append(elems, c.StaticVPS...)
			if elems, err = NormalizeVPS(elems, c.MaxVPSElements); err != nil {
				Errorf("VPS update rejected: %v", err)
				return
			}
		}
		if len(elems) == 0 {
			d.handleEmptySource(now, "source set explicitly empty")
			return
		}
		st.ClearEmpty()
		if _, err := UpdateVPSSet(c, st, elems); err != nil {
			Errorf("VPS set update failed: %v", err)
		}
		_ = st.Save()
	}
}

// handleEmptySource implements the debounce that used to be
// EMPTY_SOURCE_THRESHOLD (3 consecutive reads). It is now time-based, which is
// easier to reason about and independent of poll frequency.
func (d *Daemon) handleEmptySource(now time.Time, why string) {
	c, st := d.c, d.st
	st.MarkEmpty(now)

	if !st.EmptyGraceElapsed(c, now) {
		Debugf("%s — keeping current set for %.0fs more", why,
			c.EmptyGrace().Seconds()-now.Sub(st.EmptySince).Seconds())
		return
	}

	if !c.StrictMode {
		Warnf("%s for >%ds — non-strict mode, retaining last known set", why,
			c.EmptySourceGraceSec)
		return
	}

	Warnf("%s for >%ds — clearing VPS set (strict mode)", why, c.EmptySourceGraceSec)
	if _, err := UpdateVPSSet(c, st, nil); err != nil {
		Errorf("clearing VPS set failed: %v", err)
	}
	_ = st.Save()
}

func (d *Daemon) verifyTick(wan WANStatus) {
	c, st := d.c, d.st
	if st.Mode != "full" {
		// baseline is verified implicitly by tryFull attempts
		return
	}

	tables := BuildFor(c, st.Mode, st.WAN, st.GW, st.LastVPS)

	if err := Verify(c, tables, st.WAN, st.GW, st.LastVPS); err != nil {
		Warnf("periodic verification failed: %v", err)
		d.tryFull(wan)
		return
	}
	Debugf("verification ok (mode=full wan=%s vps=%d)", st.WAN, len(st.LastVPS))
}

// tryFull attempts to reach full mode, honouring the breaker.
func (d *Daemon) tryFull(wan WANStatus) {
	c, st := d.c, d.st

	if st.FullFail >= c.FullFailMax {
		// Breaker open (FIX review P0-3): without this, a stable verification
		// failure makes the loop reinstall full -> fail -> rollback -> flush
		// conntrack every cycle, i.e. a periodic network outage nobody notices.
		if time.Since(d.lastFullTry) < 10*c.VerifyInterval() {
			Debugf("breaker open (%d failures), not retrying full mode yet", st.FullFail)
			return
		}
		Infof("breaker half-open after %d failures, retrying full mode", st.FullFail)
	}
	d.lastFullTry = time.Now()

	if !wan.Ready {
		Debugf("WAN %s not L3-ready, staying in baseline", orUnknown(wan.Device))
		return
	}
	if !conntrackAvailable() {
		// full mode requires it: without a flush, stale NAT sessions survive
		// endpoint changes.
		Errorf("conntrack utility missing — refusing full mode, staying in baseline")
		return
	}

	src := ReadVPSSource(c)
	var vps []string
	if src.OK {
		var err error
		if vps, err = NormalizeVPS(src.Elements, c.MaxVPSElements); err != nil {
			Errorf("VPS update rejected: %v", err)
			return
		}
	}
	if c.MergeStaticVPS {
		vps = append(vps, c.StaticVPS...)
		var err error
		if vps, err = NormalizeVPS(vps, c.MaxVPSElements); err != nil {
			Errorf("VPS update rejected: %v", err)
			return
		}
	}

	if err := InstallFull(c, st, wan.Device, wan.GW, vps); err != nil {
		Errorf("full install failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// counters
// ---------------------------------------------------------------------------

var leakCounters = []string{
	"dns_leak_drops", "dns_leak_drops_fwd",
	"invalid_state_drops", "invalid_state_drops_fwd",
	"quic_drops", "quic_drops_fwd",
	"output_leak_drops", "forward_leak_drops",
}

func (d *Daemon) readCounters(now time.Time) {
	c, st := d.c, d.st
	vals, err := ReadCounters("inet", c.Table)
	if err != nil {
		Debugf("read counters: %v", err)
		return
	}
	for _, name := range leakCounters {
		cur, ok := vals[name]
		if !ok {
			continue
		}
		delta := st.CounterDelta(name, cur)
		if delta > 0 && c.LogSuspicious {
			Warnf("%s: %d packet(s) since last poll", name, delta)
		}
	}
	// also track accept counters so `status` can show them
	for _, name := range []string{
		"allow_mark", "allow_vps", "allow_established",
		"allow_mark_fwd", "allow_vps_fwd", "allow_established_fwd",
	} {
		if cur, ok := vals[name]; ok {
			st.CounterDelta(name, cur)
		}
	}
	st.MarkCountersInitialized()

	// /var/run is tmpfs on OpenWrt, but throttle anyway: there is no reason to
	// rewrite the state document on every 5s poll.
	if now.Sub(d.lastSave) >= 60*time.Second {
		d.lastSave = now
		_ = st.Save()
	}
}
