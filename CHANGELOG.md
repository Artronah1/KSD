# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
