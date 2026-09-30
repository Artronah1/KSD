// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// state.go — durable runtime state.
//
// The shell scattered this across a dozen /var/run files (mode, wan_if,
// empty_count, counter_*.prev, counter_*.initialized, ...), each written with
// a hand-rolled atomic-write helper. One JSON document, one atomic rename.
//
// Everything here is reconstructible: if the file is lost, ksd re-learns it
// within one poll cycle. It is a cache, not a source of truth.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type State struct {
	Mode     string    `json:"mode"` // "baseline" | "full"
	WAN      string    `json:"wan"`
	GW       string    `json:"gw"`
	LastVPS  []string  `json:"last_vps"`
	Counters map[string]uint64 `json:"counters"`
	// CountersInit guards the first read after boot: without it, everything
	// accumulated since boot would be reported as a fresh leak burst.
	CountersInit bool      `json:"counters_init"`
	FullFail     int       `json:"full_fail"`
	EmptySince   time.Time `json:"empty_since"` // zero = source healthy
	SourceDown   bool      `json:"source_down"` // passwall2 table gone

	path string
}

func LoadState(path string) *State {
	s := &State{
		Mode:     "baseline",
		Counters: map[string]uint64{},
		path:     path,
	}
	b, err := os.ReadFile(path)
	if err != nil {
		Infof("no state file at %s, starting fresh", path)
		return s
	}
	if err := json.Unmarshal(b, s); err != nil {
		Warnf("state file %s corrupt (%v), starting fresh", path, err)
		*s = State{Mode: "baseline", Counters: map[string]uint64{}, path: path}
	}
	if s.Counters == nil {
		s.Counters = map[string]uint64{}
	}
	return s
}

// Save writes atomically: temp file in the same directory, fsync, rename.
func (s *State) Save() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

// CounterDelta returns how many packets a counter gained since the last call.
// The first call after start only records the baseline and reports 0.
func (s *State) CounterDelta(name string, cur uint64) uint64 {
	prev, ok := s.Counters[name]
	s.Counters[name] = cur
	if !s.CountersInit {
		return 0
	}
	if !ok || cur < prev {
		// counter reset (table recreated) or wrap: treat as no delta
		return 0
	}
	return cur - prev
}

func (s *State) MarkCountersInitialized() { s.CountersInit = true }

// ---------------------------------------------------------------------------
// VPS set diffing
// ---------------------------------------------------------------------------

// IsNarrowing reports whether the new set removes endpoints that the previous
// set allowed. Only a narrowing update creates a real leak window: established
// sessions to a now-forbidden endpoint keep flowing through ks-established
// until conntrack is flushed.
func IsNarrowing(prev, next []string) bool {
	if len(prev) == 0 {
		return false
	}
	nextSet := map[string]bool{}
	for _, n := range next {
		nextSet[n] = true
	}
	for _, p := range prev {
		if !nextSet[p] {
			return true
		}
	}
	return false
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := map[string]int{}
	mb := map[string]int{}
	for _, x := range a {
		ma[x]++
	}
	for _, x := range b {
		mb[x]++
	}
	if len(ma) != len(mb) {
		return false
	}
	for k, v := range ma {
		if mb[k] != v {
			return false
		}
	}
	return true
}

// warnedElems remembers the last set of invalid elements we already warned
// about, so repeated polls don't spam the log with the same message.
var warnedElems = map[string]bool{}

func warnOnce(e string) {
	if warnedElems[e] {
		return
	}
	warnedElems[e] = true
	Warnf("skipping invalid VPS element %q", e)
}

// NormalizeVPS dedups, drops invalid entries and enforces the size cap.
// A non-nil error means "reject the whole update": the caller must leave the
// current kernel set untouched rather than installing a truncated one.
func NormalizeVPS(in []string, max int) ([]string, error) {
	seen := map[string]bool{}
	var ok []string
	for _, e := range in {
		e = trimSpace(e)
		if e == "" {
			continue
		}
		// Ranges "A-B" are passed to nft verbatim: the set is
		// declared with `flags interval`, so the kernel expands
		// them itself. Validate the element as a whole.
		if strings.Contains(e, "-") && !strings.Contains(e, "/") {
			if _, _, ok2 := splitRange(e); ok2 {
				if seen[e] {
					continue
				}
				seen[e] = true
				if validVPSElem(e) {
					ok = append(ok, e)
				} else {
					warnOnce(e)
				}
				continue
			}
		}
		if seen[e] {
			continue
		}
		seen[e] = true
		if validVPSElem(e) {
			ok = append(ok, e)
		} else {
			warnOnce(e)
		}
	}
	if max > 0 && len(ok) > max {
		return nil, fmt.Errorf("%d elements exceeds max_vps_elements=%d", len(ok), max)
	}
	return ok, nil
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
