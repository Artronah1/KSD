# ksd — Go Killswitch Daemon

The complete project has been generated and packaged in `ksd.zip`.

---

## Building (for your device)

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ksd .
```

Zero dependencies (`go.mod` has no `require`). The binary is about 2.5 MB, which is nothing for a device with 512 MB of RAM.

---

## Code structure (12 .go files, about 2,900 lines)

| File | Responsibility |
|---|---|
| `config.go` | Typed configuration plus validation; replaces roughly 250 lines of `config_get` and regexes |
| `uci.go` | Parses the UCI file directly, without exec'ing `uci` |
| `nft.go` | `nft` wrapper plus a minimal JSON model (decoded on demand, so it doesn't break if the schema changes) |
| `ruleset.go` | Declarative rule model plus rendering |
| `verify.go` | Verification **derived from the model** |
| `wan.go` | Probing via ubus and `ip route` |
| `state.go` | A single JSON state file, replacing a dozen scattered `/var/run` files |
| `install.go` / `daemon.go` | Installation and the resident loop |
| `main.go` | CLI |

---

## Three key design decisions

### 1. Rules as a contract

Every rule carries a `comment`, and verification compares the comment sequence in the model against the kernel:

- chain parameters match
- all comments are present
- **relative order is exactly the same**
- counters exist
- dynamic set contents match expectations

The relative-order check alone covers every "A must come before B" causal assertion from the old jq code.

When you change a rule you change it in one place, and verification follows automatically. The old problem of writing the generator and the verifier separately and letting them drift apart no longer exists.

### 2. WAN and gateway live in sets

`ks_wan` / `ks_wan_gw` are nft sets, and the rule text only says `oifname @ks_wan`.

When the WAN changes, only the set elements are updated: the ruleset is not rebuilt and conntrack is not flushed. Multi-WAN also goes from "generate N rules" to "add one element".

### 3. Layered fail-close

| START | Component | Behavior |
|---|---|---|
| 15 | `ksd-boot` | Installs `/etc/ksd/emergency.nft` (a static file, no logic, no dependencies) |
| 60 | `ksd run` | Installs baseline, then tries full; on success it destroys the emergency table |
| — | Safety net | If the ksd binary is deleted or fails to start (wrong architecture, SIGILL), the emergency table stays in force: the device has no connectivity, but **nothing leaks** |

---

## Review fixes already landed

- **P0-1** — the resident process reads the source directly every 5 s, so cron is not needed
- **P0-2** — baseline still goes through `nft -f`, and only a failure triggers the fallback
- **P0-3** — `full_fail_max` circuit breaker
- **P1-4 / P1-5 / P1-6** — conntrack is flushed only when the set **narrows**, and failures degrade immediately
- **P1-7** — counting and rate-limited logging are split into two rules
- **P1-8** — NTP is allowed in full mode as well
- **P1-10** — a flow-offload conflict is reported as CRITICAL at startup
- **P2-1 – P2-7** — all of them

The following options were **removed**:

```
cache_ttl_hours
empty_source_threshold   (count-based semantics)
monitoring_*
semantic_verify_enabled
```

With a resident process, the problems they compensated for no longer exist. `CONFIG.md` has the full old-to-new mapping table.

---

## One thing you must be aware of

### A pitfall with `nft flush table` (already present in the shell version)

`nft flush table` **only clears rules**. Chains, sets, and counters are all preserved (the nftables documentation states this explicitly).

So when the ruleset is applied repeatedly, `add set ... elements = {...}` is a no-op.

For this reason ksd **does not rely** on the elements inlined in the ruleset file: WAN, gateway, and endpoint contents are always pushed separately via `flush set` plus `add element` (see `applyDynamicSets`).

Section 8 of `BUILD.md` records this and the fallback change.

---

## First-time rollout order

Follow section 3 of `BUILD.md`:

1. `ksd dry-run` to inspect the rules
2. `ksd baseline` to confirm you haven't locked yourself out
3. Only then `service ksd start`

Ideally keep a serial console or another management channel that doesn't go through the killswitch for the whole process.
