# ksd (Killswitch v4.0, Go) — Operator Guide

**Version:** v4.0-dev  
**Freeze date:** not frozen (reference implementation: shell v3.5, frozen 2026-08-30)  
**Binary SHA-256:** `TODO` — compute after building: `sha256sum ksd`  
**Target:** Routerich AX3000 v1 (MT7981, aarch64), OpenWrt 25.12.5

> **Draft status.** Places that need real data from the router or a decision from you are marked `TODO`. The checklist at the end of the file collects them; delete it before committing.

---

## What it is

ksd is a resident Go daemon that implements a fail-close firewall for OpenWrt on top of nftables. All outbound traffic from the router is either:

- marked by Passwall2 (`allowed_mark`: `0x50535732`, `0x000000ff`);
- headed to an allowed VPS address (dynamic set `vps_ipv4`);
- leaving through a trusted interface (`allowed_iface`: `lo`, `br-lan`);
- part of an established/related connection;
- or blocked.

**Key properties:**

- Atomic ruleset application (a single nft transaction).
- Rollback to baseline on errors; `full_fail_max` (default 3) is a circuit breaker against periodic connectivity drops.
- Verification is derived from the rule model: every rule carries a `comment`, and the kernel state is compared against the model (chain parameters, presence and **relative order** of rules, counters, dynamic set contents). There is no separate "validator" any more, so it cannot drift from the generator.
- Protection against DNS/DoT/DoQ/DoH leaks; optional QUIC (udp/443) blocking.
- LAN client protection through the forward hook.
- Optional ARP protection on the WAN port.
- Layered fail-close: `ks_emergency` (Layer 0) → baseline → full.
- Zero external dependencies, static build (`CGO_ENABLED=0`).

---

## What changed compared to shell v3.5

| Before (shell) | Now (Go) |
|---|---|
| cron monitoring (`monitoring_*`) | resident loop: `poll_interval_sec` = 5 (endpoint set), `verify_interval_sec` = 60 (rule integrity) |
| on-disk VPS-set cache, `cache_ttl_hours`, `cache_file` | removed: the daemon reads the source directly every 5 s |
| `empty_source_threshold` (N times in a row) | `empty_source_grace_sec` = 15 (seconds, independent of poll frequency) |
| `semantic_verify_enabled` | removed: verification is always on and derived from the model |
| `reset_checksum_on_change`, `checksum.md5` | removed: integrity comes from binary distribution, no runtime self-check |
| `monitor_after_stop` | removed: the daemon is resident |
| `ntp_servers` effective in baseline only | now effective in **full** as well |
| `max_vps_elements`: silent truncation | exceeding it **rejects the whole update** |
| conntrack flush on every change | by default only when the endpoint set **narrows** (`flush_on_narrow_only=1`) |
| `dry_run`, `test`, `logread -e killswitch` | `dry-run`, `self-test`, `logread -e ksd` |
| a dozen files under `/var/run/` | a single `/var/run/ksd-state.json` (pure cache, relearned within one poll cycle) |
| WAN interface and gateway hardcoded into rules | moved into the `ks_wan` / `ks_wan_gw` sets: a WAN change updates set elements and does not rebuild the ruleset |
| `filter`/`forward`/`mangle` priorities | defaults `-10` / `-15` / `-150` (fw4 uses 0, so ksd runs first) |

> **TODO:** check which priorities shell v3.5 used in production and adjust the priority row if needed. Also confirm that the lock file (`/var/run/killswitch.lock`) is no longer needed.

---

## Quick diagnostics

### Current state

```bash
ksd status
```

From the state file and the status output, look at:

- **Mode:** `full` — full protection, `baseline` — minimal.
- **Source:** `DOWN` — the Passwall2 source table is missing (ksd flushes conntrack and counts it toward the debounce).
- **full_fail:** count of failed attempts to enter full mode. When it reaches `full_fail_max`, the circuit breaker has tripped.
- **Counters:** how many packets were blocked (`output_leak_drops`, `dns_leak_drops`, etc.).

> **TODO:** paste the real `ksd status` output and describe the fields accordingly. The description above is based on the structure of `/var/run/ksd-state.json`, not on the command's actual output.

### Quick check

```bash
ksd self-test
```

Expected output (example from BUILD.md; verify against a real build):

```
[OK]   table inet killswitch_table present
[OK]   live ruleset matches model (<nil>)
[OK]   VPS set vps_ipv4 present
[OK]   conntrack utility available
[OK]   DNS query to 1.1.1.1 blocked (expected)
Result: PASS
```

### Generate rules without applying

```bash
ksd dry-run full        # show the full ruleset
ksd dry-run baseline    # show the baseline ruleset
```

### Logs and counters

```bash
logread -e ksd | tail -n 50
nft list counters table inet killswitch_table
nft list set inet killswitch_table vps_ipv4 | head
```

Logs go to stderr and procd forwards them to syslog. You can additionally enable a log file with `log_file` and `log_max_kb` (default 512, one rotated generation).

---

## What to do when things change

### 1. Configuration changes (UCI)

```bash
uci set killswitch.main.<option>=<value>
uci commit killswitch
/etc/init.d/ksd restart
```

> **TODO:** find out whether the daemon re-reads its config on `reload` (SIGHUP / procd trigger) or only after `restart`. For now the guide uses `restart` as the known-working option. `ksd` parses the UCI file directly (it does not call `uci`), so `uci commit` is mandatory: without it nothing changes on disk.

Full description of all options and defaults: `CONFIG.md`.

### 2. Code changes (building a new version)

```bash
# 1. Edit the source
cd ~/ksd
nano daemon.go

# 2. Check
go vet ./...
go build ./...

# 3. Cross-build
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w" -o ksd-arm64 .
sha256sum ksd-arm64

# 4. Copy to the router
scp ksd-arm64 root@192.168.1.1:/tmp/ksd-new

# 5. On the router: replace the binary
cp /tmp/ksd-new /usr/sbin/ksd.new
chmod 755 /usr/sbin/ksd.new
mv /usr/sbin/ksd.new /usr/sbin/ksd

# 6. Check before applying
ksd dry-run full
ksd self-test

# 7. Restart
/etc/init.d/ksd restart
ksd status
```

> **Why `mv` and not a direct `cp` over `/usr/sbin/ksd`:** while the daemon is running, writing to its executable fails with `Text file busy`. Copy next to it first, then rename atomically (a rename within one filesystem is safe with a running process).

When the daemon stops, the rules stay in the kernel (`force_stop=0` by default), so fail-close is preserved even while the binary is being replaced.

If you need a full reinstall of the rules:

```bash
/etc/init.d/ksd stop
/usr/sbin/ksd install -config /etc/config/killswitch
/etc/init.d/ksd start
```

> **TODO:** what exactly does `ksd install` do? BUILD.md does not list this command (it has `run`, `dry-run`, `baseline`, `status`, `self-test`, `reset-counters`). If it is not needed, remove this block.

> **TODO (contract):** does the version stay bound to a sha256 in `ASSURANCE.md`? If so, after step 3 update the "Artifact Identity" section (version, hash, Go version, build flags `-trimpath -ldflags="-s -w"`) and commit it together with the code.

### 3. Rule verification (semantic check)

There is no toggle any more: the check is always on. To change the rules, edit **only the model** (`ruleset.go`). Generation and verification come from the same source and change together.

---

## First run and boot

Do not start with `service ksd start`. Follow this order:

```bash
ksd dry-run baseline
ksd dry-run full
ksd baseline          # install baseline only, make sure SSH/DHCP are alive
ksd status
service ksd start
sleep 5
ksd status
ksd self-test
```

Do all of this with a fallback management channel (serial console or a path that bypasses the killswitch). If `br-lan` is not in `allowed_iface`, SSH from the LAN is blocked by the output chain.

### Boot order

| START | Component | What it does |
|---|---|---|
| 15 | `/etc/init.d/ksd-boot` | installs `ks_emergency` (static file, no logic, no dependencies) |
| 20 | netifd | WAN comes up |
| 60 | `/etc/init.d/ksd` | procd launches `ksd run`: baseline → try full → destroy `ks_emergency` |
| — | ksd (resident) | every 5 s checks the endpoint set, every 60 s verifies the rules |

If ksd fails to start (binary deleted, wrong architecture, SIGILL), `ks_emergency` stays active: no connectivity, but no leaks either.

> **TODO:** the README installs `openwrt/ksd-boot` as `/etc/init.d/ksd`, while BUILD.md treats `ksd-boot` and `ksd` as two separate init scripts. This guide uses the BUILD.md scheme. Verify and align the README.

---

## Known limitations (what is NOT protected)

ksd protects **only within its stated threat model**.

### 1. Privileged local attacks
Root or `CAP_NET_ADMIN` can forge marks, change rules, or replace the binary. There is no runtime self-check of the binary. This is a deliberate limitation.

### 2. Established/related traffic
Established connections are trusted by design.

### 3. Unknown DoH servers and ECH
Only tcp/443 to IPs from `config doh` is blocked. Arbitrary DoH, ECH, and CDN-fronted DoH are not detected. This is a design limitation, not a bug.

### 4. DNS/DoQ on non-standard ports
Explicitly blocked: 53, 853, 8853 (TCP+UDP) and udp/784 (DoQ). Everything else that is unmarked is cut by fail-close.

### 5. QUIC
With `quic_block_enabled=1`, only udp/443 that survives the identification stage is blocked. Blocking is off by default.

### 6. ICMP covert channels
> **TODO:** confirm that the Go version still allows ICMP error messages for PMTU discovery. If so, keep the previous wording (covert channels are theoretically possible).

### 7. Hardware/software offload
If fw4 flow offloading is enabled while `forward_protect=1`, forwarded traffic can bypass `killswitch_forward`. ksd reports this as **CRITICAL** at startup and in `self-test`, but does not fix it. Hardware NIC offload is covered the same way.

> **TODO:** clarify whether hardware flow offload (`flow_offloading_hw`) is specifically checked, as in shell v3.5.

### 8. ARP protection without `gateway_mac`
Without `gateway_mac` only the gateway IP is checked and ARP spoofing is not blocked (ksd warns about it).

### 9. Dependence on time
Debounce and timers rely on `time.Now()`. Without clock synchronization the logic degrades, so set `ntp_servers` (see below).

### 10. Kernel compromise
If the kernel is compromised, counters, the verifier, and recovery may be unreliable.

### 11. fw4
Ingress (WAN→LAN) filtering and DNAT are fw4's responsibility, not ksd's.

---

## Files and paths

| File | Purpose |
|------|---------|
| `/usr/sbin/ksd` | daemon binary |
| `/etc/init.d/ksd` | procd init script (START=60) |
| `/etc/init.d/ksd-boot` | Layer 0 installation at boot (START=15) |
| `/etc/ksd/emergency.nft` | static emergency ruleset (mode 600) |
| `/etc/config/killswitch` | UCI configuration |
| `/var/run/ksd-state.json` | state (tmpfs, cache, relearned) |
| `CONFIG.md` | description of all options |
| `BUILD.md` | building and deployment |
| `docs/DESCRIPTION.zh.md` | Chinese description |
| `ASSURANCE.md` | assurance contract (`TODO`: status for v4.0) |

Removed relative to the shell version: `/etc/killswitch_vps_elements.cache`, `/etc/killswitch/checksum.md5`, `/var/run/killswitch_mode`, `/var/run/killswitch_wan_if`.

### nft objects owned by ksd

```
inet killswitch_table
  ├── counters: dns_leak_drops, invalid_state_drops, quic_drops,
  │             allow_mark, allow_vps, allow_established, output_leak_drops,
  │             (+ _fwd variants), forward_leak_drops
  ├── sets:     vps_ipv4, ks_wan, ks_wan_gw   (dynamic)
  │             trusted_ifaces, doh_servers, ntp_bootstrap   (static)
  ├── chain killswitch_output   (filter/output/-10, policy drop)
  └── chain killswitch_forward  (filter/forward/-15, policy drop)
inet killswitch_mangle   → chain KS_POSTROUTING (-150, accept)
inet ks_emergency        → Layer 0, exists during startup
arp  killswitch_arp      → optional
```

All of this belongs to ksd; do not add anything to these tables by hand.

---

## NTP

Always set `ntp_servers`. In full mode, outbound udp/123 from sysntpd carries no mark and is not in the VPS set, so without an explicit allowance the terminal rule drops it, the clock stops syncing, and the debounce logic depends on time.

```bash
uci add_list killswitch.main.ntp_servers='129.6.15.28'
uci add_list killswitch.main.ntp_servers='132.163.97.1'
uci commit killswitch
/etc/init.d/ksd restart
```

---

## Troubleshooting

### System is in baseline instead of full

```bash
logread -e ksd | grep -i "conntrack utility missing\|verification failed"
ksd status
```

Possible causes:
- the `conntrack` utility is missing;
- WAN is not ready;
- verification failed after applying.

If `full_fail` reached `full_fail_max` (default 3), the circuit breaker has tripped: retries happen once every 10 verification cycles (about 10 minutes at 60 s).

### Verification failed

```bash
logread -e ksd | grep -i "verification failed"
ksd dry-run full
nft list chain inet killswitch_table killswitch_output
```

Checked: chain hook/priority/policy, presence of every `comment` from the model, **relative rule order**, presence of counters, and dynamic set contents. Compare the live ruleset with the `dry-run` output.

### VPS set empty / `Source: DOWN`

```bash
nft list sets table inet passwall2
nft list set inet passwall2 psw2_vps
```

- The set name must match `source_set` (default `inet passwall2 psw2_vps`) and the type must be `ipv4_addr`, otherwise ksd refuses to use it.
- Check `static_vps` and `merge_static_vps`.
- With `strict_mode=1`, an empty set blocks all unmarked traffic. This is expected.
- Before clearing the set, ksd waits `empty_source_grace_sec` (15 s).

### `output_leak_drops` growing slowly

Most often this is NTP: set `ntp_servers`, run `ksd reset-counters`, and observe.

### Forward counters always zero

Most likely fw4 flow offloading bypasses `killswitch_forward`. Look for CRITICAL messages in `logread -e ksd` and in `ksd self-test`.

### Lost SSH from the LAN

`br-lan` must be in `allowed_iface`, otherwise the output chain blocks the replies. Recovery via serial console or a fallback channel:

```bash
ksd baseline          # downgrade to baseline
```

or a full teardown:

```bash
service ksd stop
nft destroy table inet killswitch_table
nft destroy table inet killswitch_mangle
nft destroy table inet ks_emergency
nft destroy table arp killswitch_arp
```

Config-based equivalent: `uci set killswitch.main.force_stop=1`, then `service ksd stop` (the daemon removes all nft objects itself).

---

## Philosophy

ksd v4.0 is not absolute security but a **local homeostatic mechanism** that:
- restricts the space of permissible trajectories within the chosen trust domain;
- uses one set of causal laws (nftables) to make another (leaks) harder;
- keeps rules, verification, and documentation in one model so they cannot drift apart;
- honestly documents its boundaries.

---

## When to consult the full documentation

- `CONFIG.md`: all options, defaults, the ARP section, the DoH section.
- `BUILD.md`: building, architectures, deployment, emergency removal.
- `ASSURANCE.md`: claims, evidence, assumptions, residual risks (`TODO`: is it still current for Go).

---

## Draft: open items (delete before committing)

1. Real output of `ksd status` and `ksd self-test`; describe the status fields.
2. How the config is re-read: `reload` or only `restart`.
3. What `ksd install` does, and whether the block using it is needed.
4. ASSURANCE.md for v4.0: binding to the binary's sha256, Go version, document version.
5. Check priorities (-10/-15/-150) against production shell v3.5.
6. Confirm the ICMP/PMTU rule and the `flow_offloading_hw` check in Go.
7. Align README and BUILD.md on the init-script scheme (`ksd-boot` + `ksd`).
8. Confirm there is no lock file any more.
9. At the time of writing the code had not been compiled or tested on the router (BUILD.md, section 8): remove or update this note after `go vet` and the first run.
