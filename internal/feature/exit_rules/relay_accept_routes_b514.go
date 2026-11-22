package exit_rules

// relay_accept_routes_b514.go — B514 (2026-11-22)
//
// THE LOOP THAT MADE EVERY NON-PRIMARY EXIT NODE FAIL.
//
// Live case, measured end to end: a 5 MB download with `exit=emilia` took 25 s and left with
// KAROLINA's public address, while emilia's AND karolina's interface counters both grew
// (emilia +7.8 MB, karolina +16.3 MB). With `exit=karolina` the same download took 1 s and
// only karolina's counters moved. That is a two-hop loop, not a slow relay.
//
// Cause: every relay had `accept_routes=1` in `exit_servers`, and the route sync faithfully
// passed `--accept-routes=true` to it on every pass. A relay that ACCEPTS routes installs the
// tailnet's `0.0.0.0/0` route into its own routing table, so the exit traffic that arrives at
// a non-primary relay is forwarded BACK INTO THE TAILNET to whichever relay headscale serves
// `0.0.0.0/0` from instead of leaving through its own uplink. The nodes that suffered most
// were exactly the ones behind a non-primary relay: "connects, media does not load, then the
// connection drops". It also explains the earlier unexplained measurement in this project in
// which a client that had selected one relay reported another relay's egress address.
//
// THE RULE THIS FILE ENFORCES: a relay that ADVERTISES routes must never ACCEPT them. Its job
// is to carry traffic out, not to hand it back. The stored tri-state is therefore not trusted
// for relays — the sync always passes `--accept-routes=false` (the -1 tri-state), so a
// database value of 1, a provisioning snippet or a manual `tailscale set` cannot reintroduce
// the loop. Clients are untouched: their accept-routes is their own setting.
//
// B514 also fixed the two places that WROTE the value in the first place: the provisioning
// and registration snippets now carry `--accept-routes=false`.

// relayAcceptRoutesSetting returns the tri-state the sync must pass for a RELAY.
//
// It deliberately ignores the stored value: `exit_servers.accept_routes` is operator-facing
// data about what a relay's own settings should be, but "a relay accepts routes" is never a
// valid state — it is the loop above. -1 is the `--accept-routes=false` flag in
// headscale.AcceptRoutesFlag.
func relayAcceptRoutesSetting() int { return -1 }
