// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// verify.go — is what we think is installed actually installed?
//
// The old version answered this with a hand-written jq program that matched on
// `.comment == "ks-ident-mark"` and inspected expression trees. It was a second
// description of the ruleset, so it could (and did) drift from the generator.
//
// Here the check is derived from the same model that generated the rules:
//
//	chain parameters equal  AND  every model comment is present  AND  the
//	comments appear in the same relative order  AND  the counters exist  AND
//	the dynamic sets hold the values we expect
//
// Rule *ordering* is the causal-alignment invariant (invalid -> leaks ->
// identification -> established -> QUIC -> terminal); asserting full ordering
// subsumes all the individual "a before b" checks from the old jq.

import (
	"fmt"
	"sort"
	"strings"
)

type verifyError []string

func (v *verifyError) add(format string, args ...interface{}) {
	*v = append(*v, fmt.Sprintf(format, args...))
}

func (v verifyError) Error() string {
	return "ruleset verification failed: " + strings.Join(v, "; ")
}

func (v verifyError) empty() bool { return len(v) == 0 }

// Verify checks the live kernel state against the model.
func Verify(c *Config, tables []Table, wantWAN, wantGW string, wantVPS []string) error {
	var errs verifyError

	for _, t := range tables {
		chains, rules, sets, counters, err := ReadTable(t.Family, t.Name)
		if err != nil {
			errs.add("table %s %s unreadable: %v", t.Family, t.Name, err)
			continue
		}

		// --- chains ---
		chainByName := map[string]nftChain{}
		for _, ch := range chains {
			chainByName[ch.Name] = ch
		}
		for _, want := range t.Chains {
			got, ok := chainByName[want.Name]
			if !ok {
				errs.add("chain %s missing", want.Name)
				continue
			}
			if got.Hook != want.Hook || got.Prio != want.Priority || got.Policy != want.Policy {
				errs.add("chain %s mismatch: got hook=%s prio=%d policy=%s, want hook=%s prio=%d policy=%s",
					want.Name, got.Hook, got.Prio, got.Policy,
					want.Hook, want.Priority, want.Policy)
			}
		}

		// --- rules: presence + order ---
		rulesByChain := map[string][]string{}
		for _, r := range rules {
			if r.Comment == "" {
				continue
			}
			rulesByChain[r.Chain] = append(rulesByChain[r.Chain], r.Comment)
		}
		for _, want := range t.Chains {
			live := rulesByChain[want.Name]
			idx := map[string]int{}
			for i, cm := range live {
				idx[cm] = i
			}
			var wantOrder []string
			for _, r := range want.Rules {
				wantOrder = append(wantOrder, r.Comment)
				if _, ok := idx[r.Comment]; !ok {
					errs.add("rule %q missing from chain %s", r.Comment, want.Name)
				}
			}
			// relative order must match the model
			prev := -1
			bad := false
			for _, cm := range wantOrder {
				i, ok := idx[cm]
				if !ok {
					continue
				}
				if i < prev {
					bad = true
				}
				prev = i
			}
			if bad {
				errs.add("chain %s rule order differs from model: live=%v", want.Name, live)
			}
			// unknown rules are only a warning: fw4 or another agent may
			// legitimately add things, and we do not want to thrash on that.
			known := map[string]bool{}
			for _, cm := range wantOrder {
				known[cm] = true
			}
			var unknown []string
			for _, cm := range live {
				if !known[cm] {
					unknown = append(unknown, cm)
				}
			}
			if len(unknown) > 0 {
				sort.Strings(unknown)
				Warnf("chain %s has %d rule(s) not in the model: %v",
				      want.Name, len(unknown), unknown)
			}

			// Any rule without a comment is not part of our model. In a
			// non-adversarial setting (root can kill us anyway) this is
			// still worth flagging: a stray `accept` ahead of the terminal
			// drop would bypass the killswitch silently.
			uncommented := 0
			for _, r := range rules {
				if r.Chain == want.Name && r.Comment == "" {
					uncommented++
				}
			}
			if uncommented > 0 {
				errs.add("chain %s has %d rule(s) without comment — not part of the model",
					 want.Name, uncommented)
			}
		}

		// --- counters ---
		have := map[string]bool{}
		for _, ctr := range counters {
			have[ctr.Name] = true
		}
		for _, name := range t.Counters {
			if !have[name] {
				errs.add("counter %s missing", name)
			}
		}

		// --- dynamic sets ---
		// Only meaningful for the main table: the mangle/arp tables do not
		// carry the WAN, gateway or VPN sets.
		if t.Name != c.Table {
			continue
		}
		setByName := map[string]nftSet{}
		for _, s := range sets {
			setByName[s.Name] = s
		}
		if wantWAN != "" {
			if s, ok := setByName[c.WANSet]; ok {
				got := setElementsFromJSON(s.Elem)
				if len(got) != 1 || got[0] != wantWAN {
					errs.add("%s holds %v, want [%s]", c.WANSet, got, wantWAN)
				}
			} else {
				errs.add("set %s missing", c.WANSet)
			}
		}
		if wantGW != "" {
			if s, ok := setByName[c.GWSet]; ok {
				got := setElementsFromJSON(s.Elem)
				if !containsAll(got, []string{"255.255.255.255", wantGW}) {
					errs.add("%s holds %v, want 255.255.255.255 + %s", c.GWSet, got, wantGW)
				}
			}
		}
		if wantVPS != nil {
			s, ok := setByName[c.Set]
			if !ok {
				errs.add("set %s missing", c.Set)
			} else {
				got := setElementsFromJSON(s.Elem)
				if !sameStrings(got, wantVPS) {
					errs.add("%s holds %d elements, want %d%s",
						 c.Set, len(got), len(wantVPS), diffHint(got, wantVPS))
				}
			}
		}
	}

	if errs.empty() {
		return nil
	}
	return errs
}

// diffHint returns a short "(+X -Y)" summary of set differences, or "" if
// the sets are equal. Used for diagnostics only.
func diffHint(have, want []string) string {
	h := map[string]bool{}
	for _, x := range have {
		h[x] = true
	}
	w := map[string]bool{}
	for _, x := range want {
		w[x] = true
	}
	var extra, missing []string
	for x := range h {
		if !w[x] {
			extra = append(extra, x)
		}
	}
	for x := range w {
		if !h[x] {
			missing = append(missing, x)
		}
	}
	if len(extra) == 0 && len(missing) == 0 {
		return ""
	}
	sort.Strings(extra)
	sort.Strings(missing)
	if len(extra) > 5 {
		extra = append(extra[:5], "...")
	}
	if len(missing) > 5 {
		missing = append(missing[:5], "...")
	}
	return fmt.Sprintf(" (+%v -%v)", extra, missing)
}

func containsAll(have, want []string) bool {
	m := map[string]bool{}
	for _, h := range have {
		m[h] = true
	}
	for _, w := range want {
		if !m[w] {
			return false
		}
	}
	return true
}
