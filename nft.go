// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// nft.go — thin wrapper around the nft binary.
//
// Why not the netlink API directly (google/nftables)?
//
//	Applying a ruleset through `nft -f <file>` is already a single netlink
//	batch transaction: it either commits whole or rolls back whole. That is
//	exactly the atomicity the shell version relied on, and it keeps the rule
//	semantics byte-for-byte identical to the validated original. Going straight
//	to netlink structs would mean re-encoding every expression and re-deriving
//	kernel constants — a large, silent-failure surface for zero behavioural
//	gain on the apply path.
//
//	What we *did* move into Go is everything around it: config, state,
//	diffing, verification, counters and the control loop. That is where the
//	shell was actually fragile.
//
// If you want true event-driven updates (no polling), the only piece to
// replace is WatchVPSSource in daemon.go — see the note there.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// exec helpers
// ---------------------------------------------------------------------------

func nft(args ...string) ([]byte, error) {
	out, err := runCmd(10*time.Second, "nft", args...)
	if err != nil {
		return nil, fmt.Errorf("nft %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func nftJSON(args ...string) ([]byte, error) {
	full := append([]string{"-j"}, args...)
	return nft(full...)
}

func nftApplyFile(path string) error {
	_, err := nft("-f", path)
	return err
}

func nftDestroy(family, name string) error {
	_, err := nft("destroy", "table", family, name)
	return err
}

func nftTableExists(family, name string) bool {
	_, err := nft("list", "table", family, name)
	return err == nil
}

func nftResetCounters(family, table string) error {
	_, err := nft("reset", "counters", "table", family, table)
	return err
}

// ---------------------------------------------------------------------------
// temp files
// ---------------------------------------------------------------------------

// writeRulesFile writes a ruleset into a private temp dir and returns its
// path. The caller must remove it.
func writeRulesFile(dir, prefix, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// /tmp is world-writable on OpenWrt; refuse to follow a symlink planted
	// by someone else (the shell version had this race too).
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink, refusing", dir)
	}
	f, err := os.CreateTemp(dir, prefix+".*.nft")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// ---------------------------------------------------------------------------
// minimal nftables JSON model
// ---------------------------------------------------------------------------
//
// `nft -j list table ...` returns {"nftables":[ {...}, ... ]} where each
// element is a single-key object: table / chain / rule / set / counter / ...
// We decode lazily per key instead of defining the full schema, so a newer nft
// that adds fields cannot break us.

type nftDoc struct {
	Nftables []map[string]json.RawMessage `json:"nftables"`
}

func parseNFTDoc(b []byte) (*nftDoc, error) {
	var d nftDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("parse nft json: %w", err)
	}
	return &d, nil
}

type nftChain struct {
	Family string `json:"family"`
	Table  string `json:"table"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Hook   string `json:"hook"`
	Prio   int    `json:"prio"`
	Policy string `json:"policy"`
}

type nftRule struct {
	Family  string `json:"family"`
	Table   string `json:"table"`
	Chain   string `json:"chain"`
	Handle  int    `json:"handle"`
	Comment string `json:"comment"`
}

type nftSet struct {
	Family string            `json:"family"`
	Table  string            `json:"table"`
	Name   string            `json:"name"`
	Type   interface{}       `json:"type"` // string or []string in some versions
	Elem   []json.RawMessage `json:"elem"`
}

type nftCounter struct {
	Family  string `json:"family"`
	Table   string `json:"table"`
	Name    string `json:"name"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// decode pulls a typed value out of one document element if the key exists.
func decode(raw json.RawMessage, v interface{}) bool {
	if len(raw) == 0 {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// ---------------------------------------------------------------------------
// set element extraction
// ---------------------------------------------------------------------------

type setRange struct {
	Range []string `json:"range"`
}
type setPrefix struct {
	Prefix struct {
		Addr string `json:"addr"`
		Len  int    `json:"len"`
	} `json:"prefix"`
}

// setElementsFromJSON converts a raw `elem` array into flat strings:
// "1.2.3.4", "1.2.3.0/24" or "a-b" ranges.
func setElementsFromJSON(elems []json.RawMessage) []string {
	out := make([]string, 0, len(elems))
	for _, raw := range elems {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			var s string
			if json.Unmarshal(trimmed, &s) == nil {
				out = append(out, s)
			}
			continue
		}
		var r setRange
		if json.Unmarshal(trimmed, &r) == nil && len(r.Range) == 2 {
			out = append(out, r.Range[0]+"-"+r.Range[1])
			continue
		}
		var p setPrefix
		if json.Unmarshal(trimmed, &p) == nil && p.Prefix.Addr != "" {
			out = append(out, fmt.Sprintf("%s/%d", p.Prefix.Addr, p.Prefix.Len))
			continue
		}
	}
	return out
}

// ReadSetElements returns the elements of an existing set, and whether the
// set itself was found.
func ReadSetElements(family, table, set string) ([]string, bool, error) {
	b, err := nftJSON("list", "set", family, table, set)
	if err != nil {
		return nil, false, err
	}
	d, err := parseNFTDoc(b)
	if err != nil {
		return nil, false, err
	}
	var found bool
	var elems []string
	for _, item := range d.Nftables {
		raw, ok := item["set"]
		if !ok {
			continue
		}
		var s nftSet
		if !decode(raw, &s) {
			continue
		}
		if s.Name != set {
			continue
		}
		found = true
		elems = append(elems, setElementsFromJSON(s.Elem)...)
	}
	return elems, found, nil
}

// ReadCounters returns every named counter object in a table.
func ReadCounters(family, table string) (map[string]uint64, error) {
	b, err := nftJSON("list", "counters", "table", family, table)
	if err != nil {
		return nil, err
	}
	d, err := parseNFTDoc(b)
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, item := range d.Nftables {
		raw, ok := item["counter"]
		if !ok {
			continue
		}
		var c nftCounter
		if decode(raw, &c) {
			out[c.Name] = c.Packets
		}
	}
	return out, nil
}

// ReadTable returns chains, rules, sets and counters of one table.
func ReadTable(family, table string) ([]nftChain, []nftRule, []nftSet, []nftCounter, error) {
	b, err := nftJSON("list", "table", family, table)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	d, err := parseNFTDoc(b)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var chains []nftChain
	var rules []nftRule
	var sets []nftSet
	var counters []nftCounter
	for _, item := range d.Nftables {
		if raw, ok := item["chain"]; ok {
			var c nftChain
			if decode(raw, &c) {
				chains = append(chains, c)
			}
		}
		if raw, ok := item["rule"]; ok {
			var r nftRule
			if decode(raw, &r) {
				rules = append(rules, r)
			}
		}
		if raw, ok := item["set"]; ok {
			var s nftSet
			if decode(raw, &s) {
				sets = append(sets, s)
			}
		}
		if raw, ok := item["counter"]; ok {
			var c nftCounter
			if decode(raw, &c) {
				counters = append(counters, c)
			}
		}
	}
	return chains, rules, sets, counters, nil
}

// runCmd runs a command with a timeout. Returns stdout on success, or an
// error if the command fails or the timeout expires. stderr is captured
// and appended to the error for diagnostics.
func runCmd(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.Bytes(), fmt.Errorf("%s: timeout after %s", name, timeout)
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w (stderr: %s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// ---------------------------------------------------------------------------
// conntrack
// ---------------------------------------------------------------------------

// ConntrackFlush drops every conntrack entry.
//
// Deliberately a full flush: `conntrack -D -o/-i <iface>` does not work for
// NAT entries because the interface is not part of the tuple (the shell
// version discovered this the hard way and documented it).
func ConntrackFlush() error {
	out, err := runCmd(30*time.Second, "conntrack", "-F")
	if err != nil {
		return fmt.Errorf("conntrack -F: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func conntrackAvailable() bool {
	_, err := exec.LookPath("conntrack")
	return err == nil
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

func ifaceExists(name string) bool {
	_, err := os.Lstat(filepath.Join("/sys/class/net", name))
	return err == nil
}
