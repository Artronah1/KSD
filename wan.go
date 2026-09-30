// Copyright (c) 2026 Artronah1
// SPDX-License-Identifier: MulanPubL-2.0

package main

// wan.go — where is the WAN, and is it usable?
//
// Uses ubus (netifd's own view of the interface) instead of the shell's
// network_get_device chain, and falls back to the kernel routing table for the
// gateway. The shell version had a comment admitting that ubus JSON sometimes
// lacks the default-route fields; reading `ip -4 route` directly avoids that
// class of silent fallback entirely.

import (
	"encoding/json"
	"strings"
	"time"
)

type WANStatus struct {
	Device string
	Ready  bool // L3 up with at least one address
	GW     string
	Up     bool
}

// ubusIfStatus is the subset of `ubus call network.interface.X status` we need.
type ubusIfStatus struct {
	Up      bool `json:"up"`
	Device  string `json:"device"`
	L3Dev   string `json:"l3_device"`
	IPv4    []struct {
		Address string `json:"address"`
	} `json:"ipv4-address"`
	IPv6 []struct {
		Address string `json:"address"`
	} `json:"ipv6-address"`
}

func DetectWAN(c *Config) WANStatus {
	var st WANStatus

	if c.WANDevice != "" {
		if ifaceExists(c.WANDevice) {
			st.Device = c.WANDevice
		} else {
			Errorf("configured wan_device %q does not exist", c.WANDevice)
			return st
		}
	} else {
		st.Device = ubusWANDevice(c.WANInterface)
	}

	if st.Device == "" {
		return st
	}

	info, err := ubusInterfaceStatus(c.WANInterface)
	if err != nil {
		Debugf("ubus status for %s: %v", c.WANInterface, err)
		// ubus unavailable: fall back to "device exists" only.
		st.Ready = ifaceExists(st.Device)
	} else {
		st.Up = info.Up
		st.Ready = info.Up && (len(info.IPv4) > 0 || len(info.IPv6) > 0)
	}

	st.GW = defaultGateway()
	return st
}

func ubusWANDevice(ifname string) string {
	info, err := ubusInterfaceStatus(ifname)
	if err != nil {
		return ""
	}
	// netifd exposes both the L2 device and the L3 device (e.g. pppoe-wan vs
	// eth1). Prefer L3, exactly like network_get_device did.
	if info.L3Dev != "" && ifaceExists(info.L3Dev) {
		return info.L3Dev
	}
	if info.Device != "" && ifaceExists(info.Device) {
		return info.Device
	}
	return ""
}

func ubusInterfaceStatus(ifname string) (*ubusIfStatus, error) {
	out, err := runCmd(3*time.Second, "ubus", "-t", "1", "call",
			   "network.interface."+ifname, "status")
	if err != nil {
		return nil, err
	}
	var s ubusIfStatus
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// defaultGateway parses the kernel's default route. Returns "" for
// point-to-point / on-link default routes (no `via`), which is fine — the DHCP
// rule then falls back to broadcast-only matching.
func defaultGateway() string {
	out, err := runCmd(3*time.Second, "ip", "-4", "route", "show", "default")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "via" && i+1 < len(fields) {
				gw := fields[i+1]
				if strings.Count(gw, ".") == 3 {
					return gw
				}
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// flow offloading
// ---------------------------------------------------------------------------

// FlowOffloading reports whether fw4 flow offloading is enabled. When it is,
// forwarded traffic can bypass killswitch_forward entirely, which silently
// defeats forward_protect.
func FlowOffloading(path string) (bool, bool) {
	u, err := LoadUCI(path)
	if err != nil || u == nil {
		return false, false
	}
	d := u.Lookup("defaults", "")
	if d == nil {
		return false, false
	}
	return d.Options["flow_offloading"] == "1", d.Options["flow_offloading_hw"] == "1"
}
