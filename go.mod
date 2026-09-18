module github.com/Artronah1/KSD

go 1.22

// ksd — killswitch daemon for OpenWrt (nftables).
// Zero external dependencies on purpose:
//   - trivially cross-compiled for any OpenWrt target
//   - CGO_ENABLED=0 by default -> fully static binary
//   - no go.sum / module proxy needed at build time
