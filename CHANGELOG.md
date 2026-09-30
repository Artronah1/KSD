# Changelog

## [1.1.0] — fail-close hardening

Four correctness fixes in the fail-close path. No functional changes to the
ruleset itself; all changes make sure the daemon cannot silently drift away
from a protected state.

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
  data. Repeats now form a proper list (previous value + new value).

- **verify**: the VPS set was only checked for existence, not for contents.
  Combined with the no-change short-circuit in `UpdateVPSSet`, a silently
  flushed or tampered set diverged forever — VPN traffic was dropped while
  `status` reported healthy full mode. Contents are now compared against
  the model.

### Notes

- Threat model is non-adversarial (root can always kill the daemon).
- Poll-based updates; a netlink subscription is a possible future
  improvement.
