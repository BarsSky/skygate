# v0.18.0 — MagicDNS for personal subnets

2026-07-17

The v0.16.0+ per-user subnets roadmap step 5: each
user's sidecar gets a stable, auto-resolving
DNS name so tailnet clients can reach the user's
`10.0.<uid>.0/24` subnet without remembering the
sidecar's tailnet IP.

## What changed

### 1. `internal/subnet/magicdns.go` (new)

  - `subnet.BaseDomain = "tsnet.skynas.ru"` —
    hardcoded to match the tailnet's
    `dns.base_domain` (operator config in
    `headscale.yaml`). `TestBaseDomain_ConsistentWithACL`
    guards against drift from the
    `internal/acl/acl.go` `baseDomain` constant.
  - `subnet.SidecarHostnamePrefix = "skygate-subnet-"`
    — matches the v0.16.7 Provision handler
    convention (`tailscaled --hostname=skygate-subnet-<username>`).
    `TestSidecarHostnamePrefix_MatchesV0_16_7`
    regression guard so a future v0.16.7 refactor
    can't silently break the v0.18.0 FQDN contract.
  - `subnet.MagicDNSNames` struct with
    `Sidecar` (full FQDN), `SidecarShort` (hostname
    only), and `UserWildcard` (the search-domain
    pattern that v0.19.0 will document per-device).
  - `ComputeMagicDNSNames(username)` is a pure
    function — no DB, no allocations beyond
    `strings.Builder`. Same input → same FQDN.
  - `FormatMagicDNSNames(names, labels)` is the
    multi-line renderer for the admin UI / bot
    reply. Caller passes the labels (i18n keys)
    so this package stays free of i18n deps.

### 2. Admin UI

  - `/admin/users/{id}/subnet` now has a
    "DNS имена" / "DNS names" `<details>` next to
    the existing "Issue preauth key" button. Three
    lines: `Sidecar FQDN`, `Short`, `Per-device
    pattern`. Plus an italic note explaining the
    headscale config requirement
    (`dns.magic_dns: true` + `dns.base_domain`).
  - `/admin/subnets` adds a new
    "DNS (MagicDNS)" column showing the same FQDN
    for every row. Always populated (no DB lookup
    needed — pure username → FQDN).
  - 7 new admin i18n keys (RU+EN):
    `user_subnet.magicdns_button`,
    `magicdns_sidecar_label`, `magicdns_short_label`,
    `magicdns_wildcard_label`, `magicdns_note`,
    `admin.subnets.col_dns`.

### 3. Bot

  - `/mysubnet` reply appends a "MagicDNS" section
    with the sidecar's auto-resolving FQDN + short
    form. New key: `bot.mysubnet.section_magicdns`.
  - Same note about headscale config in the
    section body.
  - 5 new bot i18n keys (RU+EN): `section_magicdns`,
    `magicdns_sidecar_label`, `magicdns_short_label`,
    `magicdns_note`. (Wildcard label exists in
    `user_subnet.*` but the bot doesn't render
    it — only the admin UI does, because the
    wildcard is the operator's `dns.search_domains`
    config, not a per-user thing.)

### 4. Tests

  - 12/12 packages green
  - 4 new `magicdns_test.go` tests:
    - `TestComputeMagicDNSNames_KnownUsernames` —
      alice / michail_42 / guest all produce
      the expected FQDN + short + wildcard
    - `TestFormatMagicDNSNames_AllThreeLines` —
      the multi-line renderer includes all three
      label/value pairs in order
    - `TestBaseDomain_ConsistentWithACL` —
      regression guard: `BaseDomain` must equal
      `internal/acl/acl.go`'s `baseDomain`
      constant
    - `TestSidecarHostnamePrefix_MatchesV0_16_7` —
      regression guard: prefix must equal
      `skygate-subnet-` (the v0.16.7 Provision
      handler convention)
  - `TestCatalogsParity` green (12 new i18n keys
    across 2 langs, no orphans)
  - `TestTemplateArgsMatchCatalog` green (new
    `col_dns` key used in `subnets.html` line 45
    has 0 placeholders, no arg count mismatch)

## Architecture note

MagicDNS works automatically when the operator
sets `dns.magic_dns: true` and
`dns.base_domain: tsnet.skynas.ru` in headscale
(v0.18.0 doesn't touch headscale config — this
is operator-side). The v0.16.7 sidecar's
`--hostname=skygate-subnet-<username>` argument
is the only thing that makes the FQDN
`skygate-subnet-<username>.tsnet.skynas.ru`
resolve. **No code change in headscale or
tailscaled is needed for the FQDN itself.**

The "exitnode.skygate-subnet-<user>" record (the
special one from the v0.16.0 roadmap — points
to the user's chosen exit-node, reachable
cross-subnet) is **NOT** shipped in v0.18.0.
headscale 0.29 doesn't support per-user service
records; v0.19.0 is the planned home (will use
headscale's `dns.extra_records` or a future
service-records feature). Documented in
`magicdns.go` so future readers don't think it
was forgotten.

The wildcard form (`<device>.skygate-subnet-<username>.tsnet.skynas.ru`)
is documented as a hint for the operator to
configure `dns.search_domains`. The actual
per-device records will be populated in v0.19.0
when the per-user device registry is built.

The base domain is hardcoded to `tsnet.skynas.ru`
to match `internal/acl/acl.go`'s `baseDomain`
constant. If/when skygate supports multiple
bases (per-plane), `ComputeMagicDNSNames` becomes
a method on a `MagicDNSResolver` that takes the
plane's base domain as a parameter. Not a v0.18.0
concern.

## Live verification

  - `/admin/users/1/subnet` shows the FQDN in the
    new "DNS имена" / "DNS names" card.
  - `/admin/subnets` shows the FQDN for every row
    in the new "DNS (MagicDNS)" column.
  - Bot `/mysubnet` reply ends with a "MagicDNS"
    section containing the FQDN + short form.
  - All 4 unit tests pass. 12/12 packages green.
  - Smoke 118/118.

Deployed to VM, live at the v0.18.0 build.
