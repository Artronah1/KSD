# ksd — Killswitch daemon for OpenWrt
<p align="left">
  <a href="./README.md"><img src="https://img.shields.io/badge/lang-Русский-blue.svg" alt="Русский"></a>
  <a href="./README.en.md"><img src="https://img.shields.io/badge/lang-English-brightgreen.svg" alt="English"></a>
  <a href="./README.zh.md"><img src="https://img.shields.io/badge/lang-中文-blue.svg" alt="中文"></a>
</p>
<p align="center">
  <img src="Project.png" alt="KSD" width="350">
</p>
<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/Platform-OpenWrt-orange" alt="OpenWrt">
  <img src="https://img.shields.io/badge/Status-v4.0--dev-yellow" alt="Status">
</p>
A Go daemon that implements a fail-close killswitch for OpenWrt on top of nftables.
Intended for use with Passwall2: all unmarked traffic is blocked
unless it is headed to an allowed VPS address.

## Features

- Atomic application of nftables rules (nft transaction)
- Automatic rollback to baseline on errors
- Blocking of DNS / DoT / DoQ / DoH leaks
- Optional QUIC (UDP/443) blocking
- LAN client protection via the forward hook
- ARP spoofing protection
- MSS clamp and optional TTL set
- Periodic verification of the rules in the kernel
- Zero external dependencies (CGO_ENABLED=0, static build)

## Building

    go build -o ksd ./...

Cross-compiling for OpenWrt (ARM64 example):

    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ksd-arm64 ./...

## Installation

1. Copy the binary to the router:
       scp ksd root@192.168.1.1:/usr/sbin/ksd

2. Install the UCI config:
       scp openwrt/killswitch.config root@192.168.1.1:/etc/config/killswitch

3. Install the init script:
       scp openwrt/ksd-boot root@192.168.1.1:/etc/init.d/ksd
       ssh root@192.168.1.1 'chmod +x /etc/init.d/ksd && /etc/init.d/ksd enable'

4. Start it:
       ssh root@192.168.1.1 '/etc/init.d/ksd start'

## Configurator

Interactive TUI configurator for UCI:

    scripts/ksd-configurator

Lets you manage all options in `/etc/config/killswitch` without running `uci set` by hand.
After making changes, use the "Apply and restart" menu item.

## Documentation

- [CONFIG.md](CONFIG.md) — description of all UCI options
- [BUILD.md](BUILD.md) — build details
- [docs/OPERATOR.en.md](docs/OPERATOR.en.md) — operator guide
- [docs/DESCRIPTION.en.md](docs/DESCRIPTION.en.md) — project description

## License

MIT License — see [LICENSE](LICENSE) for details.

## Status

- v3.5 (shell) — reference implementation, frozen 2026-08-30
- v4.0 (Go) — active development
