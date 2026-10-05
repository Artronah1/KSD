# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.5.0] — 2026-10-05

### Added

- **Decimal marks and masked marks in `allowed_mark`.** The option now
  accepts:
  - `0x50535732` (hex, unchanged)
  - `6666` (decimal, normalised to `0x1a0a`)
  - `0xff/0xff` (value/mask, rendered as `meta mark and 0xff == 0xff`)

  This unblocks Mihomo/OpenClash (`routing-mark` is decimal) and
  mwan3 (mask-based marks), which previously required the user to
  hand-convert values.

- **Comment hashing in the ruleset model.** Every rule comment now
  carries a short `@<hash>` suffix computed from the full rule text
  (Expr, Counter, Anon, Log, Verdict). Verification compares comments,
  so any change to the match expression — a different `allowed_mark`,
  a new `dport`, a toggled `ttl_set_enabled` — now changes the comment
  and forces reinstall.

  **Bug fixed:** previously, a config change that only affected the
  match expression left the old ruleset in the kernel, because the
  comment was unchanged and Verify saw no divergence. This is why
  changing `allowed_mark` from `0x50535732` to `6666` had no effect
  until a manual flush.

### Changed

- `verify.go` no longer needs to strip the `@hash` suffix: the model
  itself now carries the hashed comment.

## [1.4.0] — 2026-10-05

### Added

- **`source_set none` mode.** With `option source_set 'none'`, ksd
  does not read any upstream nft set. The VPS set is populated only
  from `static_vps` (or left empty). Traffic is trusted by `allowed_mark`
  and `static_vps` only — no `SourceDown` handling, no conntrack flush,
  no set clearing on grace. Intended for clients (Mihomo, OpenClash,
  Nikki) that route traffic by mark but do not expose an nft set of
  endpoint addresses.

  **Advanced mode.** Without a VPS set, the killswitch trusts the mark
  alone. Documented as such in README.

### Fixed

- **README** install steps: previously only copied `openwrt/ksd-boot`
  (Layer 0), but step 6 tried to enable `/etc/init.d/ksd` (Layer 1)
  which the installer never placed. Now copies both `openwrt/ksd` and
  `openwrt/emergency.nft` explicitly.
- **README** badge and status: were still `v4.0-dev`, now `v1.4.x`.

## [1.3.3] — 2026-10-03

### Fixed

- **`install.sh` works standalone.** Previously the installer looked for
  `openwrt/ksd`, `openwrt/ksd-boot` and `openwrt/emergency.nft` only in
  `./openwrt` and `./`. A user who downloaded just `install.sh` (as the
  README instructs) hit `required file not found: ksd` and got nothing
  installed. The installer now falls back to
  `raw.githubusercontent.com/${REPO}/main/openwrt/${file}` when the
  files are not present locally.

## [1.3.2] — 2026-10-03

### Fixed

- **`ks_emergency` could linger after `install.sh` + `ksd restart`.**
  The Layer 0 emergency table is created by `ksd-boot` at START=15. On a
  fresh install, `install.sh` runs `ksd install` (baseline → full, which
  destroys `ks_emergency`), then `/etc/init.d/ksd restart`. The restart
  goes through `start_service`, which sees no `ks_emergency` and applies
  `ksd-boot drop` again. The new daemon then starts with `st.Mode=full`,
  `selfHealOnStart` verifies the ruleset and returns, `Run` skips both
  `InstallBaseline` and `tryFull` — and nobody destroys `ks_emergency`.
  Result: all output and forward traffic was dropped by Layer 0 rules
  while `ksd status` reported `full`. Reported by a real user right after
  the v1.3.1 install.

  Fix: `Run` and `selfHealOnStart` now unconditionally destroy
  `inet ks_emergency` once our own ruleset is verified live, regardless
  of which code path brought us there.

## [1.3.1] — 2026-10-03

### Fixed

- **`openwrt/ksd` was never in git** — the `.gitignore` rule `ksd`
  (no leading slash) hid it in every directory. The Layer 1 init script
  is now committed, and binaries are anchored to the repo root
  (`/ksd`, `/ksd-arm64`, `/ksd-*`).
- **`install.sh` now fails on a missing required file** instead of
  silently skipping it. Previously, if `openwrt/ksd` was absent (which
  it was), the installer would copy `ksd-boot` and `emergency.nft` but
  not `/etc/init.d/ksd`, then fail to enable/start the service with
  a warning.
- **`tryFull` requires `conntrack` only for a baseline → full
  transition** — a full → full reinstall (self-heal) does not flush,
  so it must not require the utility.
- **`verify` now counts every rule in the chain**, not only the ones
  with a comment. A stray `accept` with an unrecognised comment — or
  none — is a divergence.
- **`verifyTick` re-checks after `tryFull`** and degrades to baseline
  if full could not be restored (breaker open, WAN not L3-ready,
  normalize rejected).
- **`ruleset_test.go`** — asserts every rule in every mode has a
  non-empty, unique-in-chain comment, and that the comment survives
  rendering. Catches future regressions before they reach a router.

## [1.3.0] — 2026-10-03

### Fixed

- **Conntrack flush only on real mode transitions.** Baseline install no
  longer flushed conntrack on a repeated call (procd respawn self-DoS
  guard did not actually work: `st.Mode` was assigned before the check).
  Full install no longer flushes when it is a self-heal reinstall of an
  already-full ruleset — the VPN tunnel is not interrupted unnecessarily.
- **VPS set is no longer wiped when the source is unreadable.** `tryFull`
  and `selfHealOnStart` reused the last known set instead of installing an
  empty `vps_ipv4`.
- **`Run` no longer overrides `selfHealOnStart`.** If self-heal already
  installed full, `Run` skips the baseline reinstall.
- **`tickFull` no longer commits WAN/gateway state on a failed set update.**
  Two unconditional lines after the `if/else` were removed; the update is
  now retried on the next tick.
- **Baseline is now verified.** `verifyTick` used to return early in
  non-full mode with the assumption that `tryFull` covers it; on a wiped
  table with the breaker open or conntrack missing, nothing restored it.
- **`tick` at unknown WAN still verifies** (with WAN empty), so an
  externally wiped table is restored even without a WAN device.

### Added

- **`verify` rejects rules without a `comment`.** A stray `nft insert
  rule ... accept` ahead of the terminal drop would have bypassed the
  killswitch silently; it is now flagged and the ruleset is reinstalled.
- **Set diff hints** in verification errors: `(+extra -missing)`, at most
  five of each.

### Changed

- Installer waits up to 15s for full mode before printing status.

## [1.2.1] — 2026-09-30

### Changed

- Renamed `ksd-configurator` to **`ksdc`** — shorter, mirrors the daemon
  name. Installed to `/usr/bin/ksdc`.
- The installer removes legacy `/usr/bin/ksd-configurator` and
  `/usr/bin/ksd-config` on upgrade.
- The installer waits up to 15 seconds for the daemon to reach full mode
  before printing the status.
- Installer next-steps mention `ksdc`.

## [1.2.0] — 2026-09-30

### Added

- **Multilingual TUI configurator** (`scripts/ksd-configurator`):
  interactive UCI editor with Russian, English and Chinese interface,
  matching the rest of the documentation.

## [1.1.4] — 2026-09-30

### Added

- **Universal installer** (`scripts/install.sh`): detects architecture,
  WAN device, LAN bridges, gateway+MAC, and VPS source set; generates
  `/etc/config/killswitch`; downloads the matching binary from GitHub
  Releases; installs init scripts; enables the service.

### Fixed

- **`ks-loopback`**: matches `ip daddr 127.0.0.0/8` now — `oifname "lo"`
  is not populated in the `output` hook for locally-generated loopback
  traffic, so xray FIN packets hit `invalid_state_drops`.
- **Subprocess timeouts**: `nft`, `ubus`, `conntrack`, `ip`.
- **`SIGHUP`** handled.
- **`InstallBaseline`**: skip conntrack flush on repeated baseline.
- **CLI (`self-test`, `reset-counters`)**: no longer writes state.
- **`install.sh`**: `restart` instead of `start`.
- **Config validation**: `full_fail_max`, `log_max_kb`,
  `source_set` family, `wan_interface`.
- **Flow offloading**: re-checked on every verify tick (was only at
  startup).

## [1.1.3] — 2026-09-30

### Added

- **Universal installer** (`scripts/install.sh`): detects architecture,
  WAN device, LAN bridges, gateway and its MAC, and the VPS source set;
  generates `/etc/config/killswitch`; downloads the matching binary from
  GitHub Releases; installs init scripts and enables the service.

### Fixed

- **`ks-loopback` rule** now matches `ip daddr 127.0.0.0/8` instead of
  `oifname "lo"` — `oifname` is not populated in the `output` hook for
  locally-generated loopback traffic, so the rule never matched and
  xray FIN packets hit `invalid_state_drops`.
- **`nft`, `ubus`, `conntrack`, `ip` subprocess calls** now run with a
  timeout — a hung child no longer freezes the control loop.
- **`SIGHUP`** is now handled (previously the default was `terminate`,
  so a stray HUP killed the daemon).
- **`InstallBaseline`** skips the conntrack flush on a repeated baseline
  install — with a broken config and procd respawn, this otherwise
  flushed every stateful session every few seconds.
- **CLI (`self-test`, `reset-counters`)** no longer writes the state
  file — the live daemon holds its own in-memory copy and would either
  overwrite it or read a stale copy after restart.

## [1.1.2] — 2026-09-30

### Added

- **In-place self-heal on startup**: if the daemon starts in `full` mode
  but the live ruleset no longer matches the model (binary upgraded,
  kernel flushed externally, config changed), it is reinstalled in a
  single nft transaction. No `RemoveAll`, no conntrack flush — so there
  is no window without rules, no window without full-mode filtering, and
  no broken VPN sessions. On failure, the daemon falls back to baseline.

## [1.1.1] — 2026-09-30

### Fixed

- **UCI option parser (critical)** — the first `option` with a given key
  was silently dropped: a missing `else` branch meant only *repeated*
  options survived (and ended up in `Lists`, not `Options`). As a result,
  every `option` in `/etc/config/killswitch` was ignored, and the daemon
  ran on built-in defaults. This was masked by defaults that happened to
  match the intended configuration for most settings (e.g. `mss_clamp`,
  `source_set`, `wan_device` via ubus fallback). Fix: restore the
  `else { cur.Options[k] = v }` branch.
- **VPS ranges `A-B`** — the full range is now passed to nft verbatim
  instead of being expanded into just its two endpoints.
- **`flags interval` on `ifname` / `ether_addr` sets** — removed; the
  flag is only meaningful for `ipv4_addr` sets and older nft/kernel
  combinations rejected the whole apply.
- **Bool option parsing** — unrecognized values (typos, trailing
  whitespace, `"True"`) now fall back to the built-in default with a
  warning instead of silently evaluating to `false`.
- **`dry-run` flag order** — `-config` after a positional argument
  (`dry-run baseline -config X`) is now honored.

## [1.1.0] — 2026-09-30

Fail-close hardening. Four correctness fixes in the fail-close path.
No functional changes to the ruleset itself; all changes make sure the
daemon cannot silently drift away from a protected state.

### Fixed

- **daemon**: WAN/gateway state was committed to disk even when the WAN set
  update failed. The next tick then saw "no change" and never retried.
  State now commits only on success, so the update is retried on the next
  poll.

- **main**: a missing or invalid config file caused `load()` to return `nil`,
  and `run` exited without installing any rules — leaving the box in a
  fail-open window. `load()` now falls back to built-in defaults and reports
  whether the config came from a file.

- **main**: with `force_stop=1`, `RemoveAll` was called from the signal
  handler before the control loop was cancelled, so an in-flight tick could
  re-install the ruleset after removal. Removal now runs after `Run()` has
  fully returned, and the state is reset to `removed`.

- **uci**: a repeated `option` with the same key split its values between the
  scalar and list maps — `getStr` and `getList` saw different halves of the
  data. Repeats now form a proper list.

- **verify**: the VPS set was only checked for existence, not for contents.
  Combined with the no-change short-circuit in `UpdateVPSSet`, a silently
  flushed or tampered set diverged forever — VPN traffic was dropped while
  `status` reported healthy full mode. Contents are now compared against
  the model.

### Notes

- Threat model is non-adversarial (root can always kill the daemon).
- Poll-based updates; a netlink subscription is a possible future
  improvement.

## [1.0.1] — 2026-09-19

First release built via GitHub Actions.

### Added

- **VPS range support `A-B`**: elements like `2.27.28.105-2.27.28.106` from
  the source set are expanded into two separate addresses and added to
  `vps_ipv4`.
- **Multilingual documentation**: README, DESCRIPTION, and OPERATOR in
  Russian, English, and Chinese.
- **SPDX license headers** (`MulanPubL-2.0`) across all source and config
  files.
- **GitHub Actions release workflow**: builds `ksd-arm64`, `ksd-amd64`,
  `ksd-mipsle`, `ksd-mips` on release.

### Fixed

- **Warning deduplication**: the `skipping invalid VPS element` warning is
  now emitted once per unique element per process lifetime, not on every
  poll.
- **Loopback pass-through**: new `ks-loopback` rule placed before the
  `ct state invalid` drop, so local traffic (e.g. xray on `127.0.0.1:2009`)
  no longer hits `invalid_state_drops`.
- **Mangle table set**: `buildMangle` now creates the `ks_wan` set inside
  `killswitch_mangle`; previously mangle rules referenced a set that did
  not exist in that table.
- **Baseline DoH set**: `BuildBaseline` now creates the `doh_servers` set;
  previously the `ks-block-doh` rule referenced a missing set.
- **License clarity**: switched from MIT to Mulan PubL v2 consistently
  across all files.

## [1.0.0] — 2026-09-18

Initial public release.

### Added

- Go implementation of a fail-close killswitch for OpenWrt on top of
  nftables.
- Layered fail-close: emergency (`ksd-boot`, START=15) → baseline →
  full.
- Comment-based verification: the model and the verifier cannot drift.
- WAN and gateway in nft sets (`ks_wan`, `ks_wan_gw`).
- DNS / DoT / DoQ / DoH leak blocking.
- Optional QUIC (UDP/443) blocking.
- ARP spoofing protection.
- MSS clamp and optional TTL set.
- Periodic verification of the rules in the kernel.
- Zero external dependencies (CGO_ENABLED=0, static build).

[Unreleased]: https://github.com/Artronah1/KSD/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/Artronah1/KSD/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/Artronah1/KSD/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/Artronah1/KSD/releases/tag/v1.0.0
