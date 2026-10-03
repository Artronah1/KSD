#!/bin/sh
# Copyright (c) 2026 Artronah1
# SPDX-License-Identifier: MulanPubL-2.0
#
# ksd universal installer for OpenWrt.
#
# Run this ON the router (as root). It detects the architecture, downloads
# the matching binary from the latest GitHub release, autodetects the WAN
# and LAN layout, generates /etc/config/killswitch, and installs the
# service.
#
# Usage:
#   sh install.sh                # download + install latest release
#   sh install.sh -v v1.1.2      # pin a specific release tag
#   sh install.sh -f ksd-arm64   # use a local binary instead of downloading
#
# Installs: /usr/sbin/ksd (daemon), /etc/init.d/ksd, /etc/init.d/ksd-boot,
# /etc/ksd/emergency.nft, /usr/bin/ksdc (TUI configurator).

set -eu

REPO="Artronah1/KSD"
VERSION="latest"
LOCAL_BIN=""
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# ---------- args ----------
while [ $# -gt 0 ]; do
    case "$1" in
        -v|--version) VERSION="$2"; shift 2 ;;
        -f|--file)    LOCAL_BIN="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,20p' "$0"; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 1 ;;
    esac
done

# ---------- helpers ----------
log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

# ---------- sanity ----------
[ "$(id -u)" = "0" ] || die "run as root"
need uname
need uci
need nft
need ubus

[ -f /etc/openwrt_release ] || warn "not OpenWrt? continuing anyway"

# ---------- architecture ----------
ARCH="$(uname -m)"
case "$ARCH" in
    aarch64)         BIN="ksd-arm64" ;;
    x86_64)          BIN="ksd-amd64" ;;
    mips)            BIN="ksd-mips" ;;
    mipsel)          BIN="ksd-mipsle" ;;
    *) die "unsupported architecture: $ARCH" ;;
esac
log "architecture: $ARCH → $BIN"

# ---------- obtain binary ----------
if [ -n "$LOCAL_BIN" ]; then
    [ -f "$LOCAL_BIN" ] || die "local binary not found: $LOCAL_BIN"
    cp "$LOCAL_BIN" "$TMPDIR/ksd"
    log "using local binary: $LOCAL_BIN"
else
    if [ "$VERSION" = "latest" ]; then
        URL="https://github.com/${REPO}/releases/latest/download/${BIN}"
    else
        URL="https://github.com/${REPO}/releases/download/${VERSION}/${BIN}"
    fi
    log "downloading $URL"

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$TMPDIR/ksd" "$URL" || die "download failed (curl)"
    elif command -v uclient-fetch >/dev/null 2>&1; then
        uclient-fetch -q -O "$TMPDIR/ksd" "$URL" || die "download failed (uclient-fetch)"
    elif wget --help 2>&1 | grep -qi ssl; then
        wget -q -O "$TMPDIR/ksd" "$URL" || die "download failed (wget-ssl)"
    else
        die "no HTTPS download tool (curl / uclient-fetch / wget-ssl). Use -f with a local binary."
    fi
fi

chmod +x "$TMPDIR/ksd"

# ---------- obtain configurator (best-effort) ----------
CFG_SRC=""
if [ -n "$LOCAL_BIN" ]; then
    # Local-binary mode: look next to the script.
    for cand in "./scripts/ksdc" "./ksdc" \
                "$(dirname "$0")/scripts/ksdc" "$(dirname "$0")/ksdc"; do
        [ -f "$cand" ] && { CFG_SRC="$cand"; break; }
    done
fi
if [ -z "$CFG_SRC" ]; then
    if [ "$VERSION" = "latest" ]; then
        CFG_URL="https://raw.githubusercontent.com/${REPO}/main/scripts/ksdc"
    else
        CFG_URL="https://raw.githubusercontent.com/${REPO}/${VERSION}/scripts/ksdc"
    fi
    log "fetching configurator from $CFG_URL"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$TMPDIR/ksdc" "$CFG_URL" 2>/dev/null && CFG_SRC="$TMPDIR/ksdc"
    elif command -v uclient-fetch >/dev/null 2>&1; then
        uclient-fetch -q -O "$TMPDIR/ksdc" "$CFG_URL" 2>/dev/null && CFG_SRC="$TMPDIR/ksdc"
    fi
    [ -z "$CFG_SRC" ] && warn "configurator download failed (optional)"
fi

# ---------- autodetect WAN ----------
log "detecting WAN"
WAN_IF="wan"
WAN_DEV=""

# Try netifd first (authoritative).
if ubus list network.interface.wan >/dev/null 2>&1; then
    WAN_JSON="$(ubus call network.interface.wan status 2>/dev/null || true)"
    WAN_DEV="$(printf '%s' "$WAN_JSON" \
        | grep -o '"l3_device"[[:space:]]*:[[:space:]]*"[^"]*"' \
        | head -1 | sed 's/.*"\([^"]*\)"$/\1/')"
    [ -n "$WAN_DEV" ] || WAN_DEV="$(printf '%s' "$WAN_JSON" \
        | grep -o '"device"[[:space:]]*:[[:space:]]*"[^"]*"' \
        | head -1 | sed 's/.*"\([^"]*\)"$/\1/')"
fi

# Fallback: interface carrying the default route.
if [ -z "$WAN_DEV" ]; then
    WAN_DEV="$(ip -4 route show default 2>/dev/null \
        | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
fi

[ -n "$WAN_DEV" ] || die "could not detect WAN device"
log "WAN device: $WAN_DEV"

# ---------- autodetect LAN bridge ----------
log "detecting LAN bridge"
LAN_BRIDGES="$(ip -br link show 2>/dev/null | awk '$1 ~ /^br-/ {print $1}')"
if [ -z "$LAN_BRIDGES" ]; then
    warn "no br-* interface found; using br-lan as fallback"
    LAN_IFACES="lo br-lan"
else
    LAN_IFACES="lo"
    for br in $LAN_BRIDGES; do
        LAN_IFACES="$LAN_IFACES $br"
    done
fi
log "trusted interfaces: $LAN_IFACES"

# ---------- autodetect gateway + MAC ----------
log "detecting gateway"
GW="$(ip -4 route show default 2>/dev/null \
    | awk '{for(i=1;i<=NF;i++) if($i=="via"){print $(i+1); exit}}')"
[ -n "$GW" ] || die "could not detect default gateway"

GW_MAC=""
if [ -n "$GW" ]; then
    GW_MAC="$(ip neigh show "$GW" 2>/dev/null \
        | awk '{for(i=1;i<=NF;i++) if($i=="lladdr"){print $(i+1); exit}}')"
fi
log "gateway: $GW (mac: ${GW_MAC:-unknown})"

# ---------- detect VPS source set (best-effort) ----------
log "detecting VPS source set"
SOURCE_SET=""
for tbl in passwall2 passwall sing-box xray v2ray; do
    if nft list table inet "$tbl" >/dev/null 2>&1; then
        SET="$(nft -j list table inet "$tbl" 2>/dev/null \
            | grep -o '"name"[[:space:]]*:[[:space:]]*"[a-zA-Z0-9_]*vps[a-zA-Z0-9_]*"' \
            | head -1 | sed 's/.*"\([^"]*\)"$/\1/')"
        [ -n "$SET" ] && { SOURCE_SET="inet $tbl $SET"; break; }
    fi
done

if [ -n "$SOURCE_SET" ]; then
    log "VPS source: $SOURCE_SET"
else
    warn "no VPS set detected; writing source_set='inet passwall2 psw2_vps' (edit manually)"
    SOURCE_SET="inet passwall2 psw2_vps"
fi

# ---------- install files ----------
log "installing /usr/sbin/ksd"
cp "$TMPDIR/ksd" /usr/sbin/ksd
chmod +x /usr/sbin/ksd

# init scripts: ksd-boot + ksd (init.d/ksd is the same file as ksd-boot? No —
# two distinct scripts in the repo).
#
# Look locally first (./openwrt, ./), then fall back to GitHub raw so that
# a standalone install.sh downloaded to /tmp works without the repo.
HERE="$(dirname "$0")"
for pair in "ksd:/etc/init.d/ksd" "ksd-boot:/etc/init.d/ksd-boot" "emergency.nft:/etc/ksd/emergency.nft"; do
    src="${pair%%:*}"; dst="${pair##*:}"
    found=0
    for cand in "./openwrt/$src" "./$src" "$HERE/openwrt/$src" "$HERE/$src"; do
        if [ -f "$cand" ]; then
            mkdir -p "$(dirname "$dst")"
            cp "$cand" "$dst"
            chmod +x "$dst" 2>/dev/null || true
            log "installed $cand → $dst"
            found=1
            break
        fi
    done
    if [ "$found" = "0" ]; then
        if [ "$VERSION" = "latest" ]; then
            RAW_URL="https://raw.githubusercontent.com/${REPO}/main/openwrt/$src"
        else
            RAW_URL="https://raw.githubusercontent.com/${REPO}/${VERSION}/openwrt/$src"
        fi
        log "fetching openwrt/$src from $RAW_URL"
        if command -v curl >/dev/null 2>&1; then
            curl -fsSL -o "$TMPDIR/$src" "$RAW_URL" 2>/dev/null && found=1
        elif command -v uclient-fetch >/dev/null 2>&1; then
            uclient-fetch -q -O "$TMPDIR/$src" "$RAW_URL" 2>/dev/null && found=1
        fi
        if [ "$found" = "1" ]; then
            mkdir -p "$(dirname "$dst")"
            cp "$TMPDIR/$src" "$dst"
            chmod +x "$dst" 2>/dev/null || true
            log "installed $RAW_URL → $dst"
        fi
    fi
    [ "$found" = "0" ] && die "required file not found: $src (looked in ./openwrt, ./ and tried GitHub raw)"
done

# Configurator (optional — may have been fetched or found locally).
# Remove legacy names from earlier installs.
[ -f /usr/bin/ksd-configurator ] && rm -f /usr/bin/ksd-configurator
[ -f /usr/bin/ksd-config ] && rm -f /usr/bin/ksd-config

if [ -n "$CFG_SRC" ]; then
    cp "$CFG_SRC" /usr/bin/ksdc
    chmod +x /usr/bin/ksdc
    log "installed configurator → /usr/bin/ksdc"
fi

# ---------- config ----------
if [ -f /etc/config/killswitch ]; then
    warn "/etc/config/killswitch already exists — keeping it"
    warn "review it manually (wan_device=$WAN_DEV, allowed_iface=$LAN_IFACES, arp_gateway=$GW)"
else
    log "generating /etc/config/killswitch"
    cat > /etc/config/killswitch <<EOF
config killswitch 'main'
	option table_name 'killswitch_table'
	option set_name 'vps_ipv4'
	option source_set '$SOURCE_SET'
	option wan_interface 'wan'
	option wan_device '$WAN_DEV'
$(for i in $LAN_IFACES; do printf '\tlist allowed_iface %s\n' "'$i'"; done)
	list allowed_mark '0x50535732'
	list allowed_mark '0x000000ff'
	option strict_mode '1'
	option dns_block_enabled '1'
	option doq_block_enabled '1'
	option doh_block_enabled '1'
	option quic_block_enabled '1'
	option ipv6_block_enabled '1'
	option mss_clamp_enabled '1'
	option ttl_set_enabled '1'
	option forward_protect '1'
	option filter_priority '-10'
	option forward_filter_priority '-15'
	option mangle_priority '-150'
	option poll_interval_sec '30'
	option verify_interval_sec '60'
	option empty_source_grace_sec '15'
	option full_fail_max '3'
	option max_vps_elements '5000'
	option flush_on_narrow_only '1'
	option merge_static_vps '1'
	option log_suspicious '1'
	option log_drops '0'
	option log_max_kb '512'
	option force_stop '0'
	option arp_protection '1'
	option arp_interface '$WAN_DEV'
	list arp_gateway '$GW'
$([ -n "$GW_MAC" ] && printf "\tlist arp_gateway_mac '%s'\n" "$GW_MAC")
	list ntp_servers '162.159.200.1'
	list ntp_servers '162.159.200.123'

config doh 'doh'
	list server '1.1.1.1'
	list server '1.0.0.1'
	list server '8.8.8.8'
	list server '8.8.4.4'
	list server '9.9.9.9'
	list server '149.112.112.112'
	list server '94.140.14.14'
	list server '94.140.15.15'
EOF
fi

# ---------- enable + start ----------
log "enabling services"
/etc/init.d/ksd-boot enable 2>/dev/null || warn "could not enable ksd-boot"
/etc/init.d/ksd enable 2>/dev/null || warn "could not enable ksd"

log "installing baseline + full"
/usr/sbin/ksd install -config /etc/config/killswitch || warn "ksd install failed — check the config"

log "restarting service"
/etc/init.d/ksd restart 2>/dev/null || warn "ksd restart failed"

# Wait for full mode. Fresh installs start in baseline, then move to full
# once the WAN is confirmed — which can take a few seconds.
i=0
while [ $i -lt 15 ]; do
    MODE="$(/usr/sbin/ksd status -config /etc/config/killswitch 2>/dev/null \
        | awk '/Mode[[:space:]]*:/ {print $3; exit}')"
    [ "$MODE" = "full" ] && break
    i=$((i + 1))
    sleep 1
done

if [ "$MODE" = "full" ]; then
    log "reached full mode"
else
    warn "still in '$MODE' mode after 15s — check /etc/config/killswitch and WAN"
fi

log "status:"
/usr/sbin/ksd status -config /etc/config/killswitch | head -15 || true

log "done. Next steps:"
echo "  1. review /etc/config/killswitch (or run: ksdc)"
echo "  2. /usr/sbin/ksd self-test -config /etc/config/killswitch"
echo "  3. ksdc              — interactive TUI editor"
echo "  4. keep UART or a second SSH session open if you change firewall rules"
