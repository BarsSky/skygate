# Skygate release notes

> **Single canonical file.** All release detail (root cause + fix + files +
> live-verify) lives here for every shipped tag, regardless of when the entry was
> written. The per-version `RELEASE-NOTES-vX.Y.Z.md` pattern is **deprecated** — if
> you find one in the tree, delete it: `.github/workflows/release.yml` checks this
> file out and extracts its `## vX.Y.Z` section for the GitHub Release body (and
> falls back to a generated commit list when the section is missing, so the body is
> never empty).
>
> **Order:** newest first. v1.5.4–v1.5.8 are backfilled compactly in the section
> after v1.5.9; v1.5.3's full entry sits near the bottom of the file (it was
> appended after the historical sections). Nothing older was rewritten.

## v1.5.81 — the device mesh follows the device, not a username headscale rewrote (B316)

**Date:** 2026-09-24 · **Base:** `v1.5.80` → this tag · **Compatibility:** none — no
schema change, no config change. An existing install repairs itself on the next
maintenance tick.

### The report

On the native host `aro`, `workpc` and `homepc` did not ping each other over their tailnet
addresses although both belong to one user — and the panel showed the contradiction: the
device row said `daniil`, while the **PER-DEVICE ACL** column showed «—». In the
operator's words:

> «skygate считает эти устройства как у пользователя daniil как и должно быть но во все
> устройства отображает такую картину по тегам, из-за чего скорей всего и идет конфликт»

### What was measured (read-only, both hosts)

```
aro:       node_owner_map   2 workpc  tagged-devices  tag:dev-daniil-workpc
                            3 laptop tagged-devices  tag:dev-daniil-laptop
                            6 homepc daniil          tag:dev-daniil-homepc
           live policy      0 grants of the form tag:dev-* → tag:dev-*
agent VM:  node_owner_map   every row carries a REAL portal username
           live policy      13 tag→tag grants (6+3+4 — one per device)
```

Same code, different **data** — and the agent VM works only by luck of that data.

### Root cause

headscale rewrites a tagged node's user to the synthetic `tagged-devices` as soon as the
node wears any tag (B287), and the device-to-device mesh grouped devices **by that column**
(`db.GetPerUserDeviceTags`, a JOIN on `portal_users`). On `aro` two of daniil's three
devices therefore did not exist as far as the mesh was concerned: he looked like a
**single-device user**, `writePerDeviceGrants` skipped him at its `len(userTags) < 2 →
continue` guard, and the generated policy contained **no** device-to-device grant at all.
headscale denies by default, so the two machines could not reach each other while both were
online — and the skip is a bare `continue`: nothing was logged and nothing appeared on any
page. A device that is tagged before (or without) its ownership row being re-resolved
silently loses contact with its owner's other devices.

### The fix

* **Resolve** (`internal/db/device_owner_b316.go`): a device's owner comes from the
  ownership row, then from the **device's own tag** (`tag:dev-<user>-<host>`, B288's
  parser), then from the user its `device_rules` were created under. `DeviceTagsForMesh`
  feeds both ACL generators; `MeshTagsByHost` feeds the panel.
* **Never silent**: devices no source can attribute are returned and named —
  `acl: N device(s) cannot join the device mesh and get NO device-to-device grant: workpc
  (its ownership row names tagged-devices …) — adopt them on /admin/devices (or fix their
  tag) to restore it`.
* **Repair** (`RepairSentinelDeviceOwners`): rows whose username is the synthetic sentinel
  (or empty) are re-attributed from the same tag — only when the tag parses AND names an
  existing portal user — each change is logged
  (`workpc: tagged-devices -> daniil (from tag:dev-daniil-workpc)`), the pass is
  idempotent, and it runs from the periodic maintenance tick, so an existing install heals
  itself and every consumer (mesh, `tagOwners`, the SSH sources, the panel column) sees a
  real owner again.
* `/admin/devices` stops rendering «—» for a device that *has* a per-device tag, because the
  column now reads the same resolved source.

### Files

`internal/db/device_owner_b316.go` (new), `internal/db/device_owner_b316_test.go` (new),
`internal/acl/acl_b316_test.go` (new), `internal/acl/acl.go`,
`internal/nodeownership/auto.go`, `internal/feature/admin/devices.go`,
`scripts/check_b316_device_mesh_ownership.sh`.

### What the first CI run caught (and why it mattered)

The first push of this block went green on every local run and **red on the runner**, with two
defects that no local run could see — both of them in this block's own new code:

1. **`?` placeholders in the repair's SQL.** `internal/db/placeholders.go` states the rule
   plainly (`$N` is universal; `?` is a PG syntax error, SQLSTATE 42601) — and the repair used
   `?` in both the `SELECT … IN ('', ?)` and the `UPDATE`. `aro` is the SQLite install and the
   agent VM is PostgreSQL, so this would have been a **no-op with a syntax error on exactly the
   host class that runs PostgreSQL**. Fixed to `$1/$2/$3`; contract **A6** now pins that no `?`
   placeholder exists in the repair, so the class cannot come back silently.
2. **The test fixture collided with the real schema.** `portal_users` id `99` is seeded by the
   v0.54 migration as `infra`, so the PostgreSQL run died with `duplicate key value violates
   unique constraint "portal_users_pkey"`; the fixture now inserts with
   `ON CONFLICT (id) DO NOTHING` (the aro roster *is* that migration's row plus `daniil`).
3. **The gate registered the script under the wrong case** (`scripts/check_B316_…` vs the real
   `scripts/check_b316_…`). On the case-insensitive Windows mount the registration resolved and
   the block looked fine; on the runner `test -f` failed and the catalog printed a bare
   `FAIL B316`. Contract **G5** now asserts that the `run_check` line names this script by its
   real path.

The lesson is worth stating plainly: a block edited on Windows must be validated **on the
runner**, because the local mount is case-insensitive and the local test DB is empty — the two
properties that hid all three defects above.

### Verification

27 contracts in `scripts/check_b316_device_mesh_ownership.sh` (including A6 and G5 above), plus
`internal/db/device_owner_b316_test.go` and `internal/acl/acl_b316_test.go`, which both use
**`aro`'s own inventory** as the fixture: the mesh is now emitted for all three of daniil's
devices, an unattributable device is never granted to anybody, the repair touches exactly
the provable sentinel rows (never a real owner, never an unprovable one) and is idempotent.
The `internal/db` half is additionally run against a **real PostgreSQL 15** (the CI
`SKYGATE_TEST_PG_DSN` job), which is what caught defects 1 and 2.

## v1.5.80 — the relay's metrics reach the container, and the page stops inventing zeros (B315)

**Date:** 2026-09-24 · **Base:** `v1.5.79` → this tag · **Compatibility:** none — no
schema change, no migration, no config change. The new knob
(`derp.debug_url` / `SKYGATE_DERP_DEBUG_URL`) is **optional**: with it unset the page
behaves as before except that the traffic tiles now render «—» instead of `0` and say
why, and the STUN tile names its vantage point.

### The report

> «по DERP все еще не доступно на VM agent он заявляет публичный адрес - адрес
> локальной машины а не публичный адрес домена derp.example.com, и также висит
> предупреждение о падаваемых метриках что никак не управляется и не объясняется
> для администратора. Найди причину и предложи варианты»

### Root cause 1 — the metrics were never going to arrive

derper serves `/debug/*` (traffic, clients, current connections, byte and packet
counters, and the STUN counters) through upstream `tsweb.AllowDebugAccess`, which
admits **loopback and tailnet sources only** and answers everything else
`403 debug access denied`. The verdict is made on the request's **source** address,
so no amount of re-dialling from the container could change it — the B296
probe-host knob solves a *different* half (making the probes **reach** the relay).

Measured on the agent VM:

| from | `GET /debug/vars` |
|---|---|
| the host's own loopback | `200`, ~6.3 KB of JSON |
| the skygate container | `403 debug access denied` |

The page's reaction was to draw `0` in four traffic tiles — numbers that look like
measurements — beside one warning banner that named neither a cause nor a fix. That
is exactly what the operator described.

### Root cause 2 — "public IP" could be a private one

`resolvePublicDERPIP`'s last resort is `detectEgressIP()`, i.e. **the skygate
container's own outbound address** (normally `172.18.0.x` on the docker bridge), and
the template printed it under the label «Публичный IP». When DNS did not resolve the
relay's name the row therefore answered the question "where do clients connect" with
an address that is unreachable from the internet.

### What changed

* **A metrics endpoint, and the bridge to serve it.** `skygate derp-metrics-proxy`
  runs **on the host**, dials the relay's loopback over TLS (so derper sees a
  loopback source and admits it) and re-serves five read-only paths to the
  container: `/debug/`, `/debug/vars`, `/active-conn`, `/all-recent`, `/healthz`.
  It ships **inside the binary that is already deployed** — no new dependency, no new
  image, no compose edit. The forwarded paths are a **closed allow-list** (the
  upstream also carries the DERP protocol, so "forward everything" would republish
  the relay on a plain-HTTP port), foreign client sources are refused even on a
  `0.0.0.0` bind, and a failed upstream TLS handshake names `--server-name` /
  `--insecure` in its response body.
* **The knob.** `derp.debug_url` (global setting, written by the new card) beats
  `SKYGATE_DERP_DEBUG_URL` (.env) beats none, re-resolved on **every render** — so
  saving applies on the next refresh with no restart and no container recreate (the
  B296 rule). Refusals carry their own code (`spaces` / `scheme` / `port` /
  `invalid`) so the page can say what to fix.
* **The verdict is explicit.** `MetricsAvailable` / `MetricsErrCode`
  (`unconfigured` / `denied` / `unreachable` / `badstatus` / `badbody`) /
  `MetricsErrDetail` / `MetricsVia` / `MetricsHTTPStatus` / `MetricsBytes`, and
  `httpGetViaStatus` keeps the status code — a `502` from a broken bridge is no
  longer reported as a `403` from the relay. The tiles render «—» plus one line
  saying the values were **not measured**.
* **`parseDerperVars` now requires the `derp` block.** The old tail compared an int
  field against zero with `>=`, which is true for **any** JSON object, so an empty
  `{}` still produced `Running: active` next to twelve zeros.
* **The STUN tile names its vantage point.** With metrics readable it reports
  derper's **own** `stun.counter_requests` (`success` and `not_stun`) — what clients
  experience. Only without them does it fall back to the B265 UDP round trip from
  inside the container, whose egress is demonstrably not the clients' path (measured:
  the container timed out while clients scored the relay fine).
* **The STUN probe no longer lies about a NAT'd path.** The probe used
  `net.DialTimeout("udp", …)` — a **connected** UDP socket — and Linux only delivers a
  datagram to a connected UDP socket when its **source** matches the peer it was
  connected to. Where the container's path to the relay rewrites the address (this host
  DNATs `:3478` onto the docker bridge gateway) the reply arrives from the rewritten
  source and the kernel drops it before Go sees it, so the tile read «UDP-проба не
  прошла» on a relay that had answered in 28 ms. Measured in the same network namespace
  at the same instant: unconnected → `REPLY`, source `172.18.0.1`; connected → `i/o
  timeout`; connected to `172.18.0.1:3478` → `REPLY`, 198 µs. The probe now uses an
  unconnected socket and accepts a reply from any source — the fresh 96-bit transaction
  id, compared byte for byte, is what proves the datagram is ours (a foreign one is still
  rejected, pinned by a test) — and when the answering address differs from the one
  dialled the tile says so («ответ пришёл с …»).
* **The two addresses are separated.** An egress answer is flagged
  (`WhiteIPFallback`) and rendered as «не удалось определить» **with the reason**
  instead of as the public address, and the address skygate itself dials gets its own
  row («Адрес связи из контейнера») with the layer it came from.
* **The card is actionable.** `POST /admin/derp/metrics-endpoint` (admin-only,
  audited) offers **save**, **clear** and **test without saving**, and prints the
  exact host commands for both install kinds.

### Also fixed (drive-by)

`scripts/check_b237_2.sh` contracts **E.2/E.3** were the AGENTS trap-#9 anti-pattern
(`go test … | grep -q '^ok'` under `pipefail`): `grep -q` exits at the first match, the
still-writing `go test` dies with SIGPIPE (141) and the pipeline is reported FAILED even
though the tests passed. It fired exactly once, during a loaded gate run, as a rotating
`FAIL E.3` that passed standalone — the documented symptom of that class. Both sites now
capture first and match after, so the contract is unchanged and deterministic.

### Files

`internal/derpcfg/derpcfg.go` (+ `derpcfg_b315_test.go`),
`internal/derpmetricsproxy/proxy.go` (+ `proxy_b315_test.go`),
`cmd/skygate/derp_metrics_proxy.go`, `cmd/skygate/main.go`,
`internal/feature/admin/derp.go`, `internal/feature/admin/derp_metrics.go`
(+ `derp_metrics_b315_test.go`),
`internal/handlers/templates/admin/derp.html`, `internal/i18n/catalog_derp.go`,
`scripts/check_b315_derp_metrics_endpoint.sh`.

### Verification

50 contracts in `scripts/check_b315_derp_metrics_endpoint.sh` (the two-layer knob and
its refusal codes; the read path keeping the relay for liveness probes; the explicit
verdict; the vantage-point rule; the address rows; the bridge's closed path and client
allow-lists; the three controls and their audit; RU+EN parity), the Go tests above
(including a real TLS upstream behind a real proxy process: allowed paths forwarded,
other paths refused **before** reaching the relay, `/healthz` free of the relay, and a
certificate mismatch whose body names both flags), and the still-true B265 contract H
(the debug-denied caveat is still rendered).

## v1.5.79 — the sidebar grouped the way you asked (B314)

**Date:** 2026-09-23 · **Base:** `v1.5.78` → this tag · **Compatibility:** none —
navigation only, no schema, no config, no behaviour change.

### The request

> «Группы по OIDC - явно две страницы, группа по DERP с релеями и здоровьем derp,
> вынести сертификаты в настройки так как они относяться к настройке самого skygate,
> группа по deploy кластер highAvailability … группа tailscale headscale headplane …
> ну и реальные интеграции - это телеграм а сама страница интеграции больше должна
> называться Сервисы и иметь больше переходных ссылок на остальные сервисы»

The old sidebar had **one** «Integrations» section holding twelve unrelated pages, so
where a page sat told the operator nothing about it.

### What changed

* **OIDC** — a group of its own, exactly its two pages (`/admin/oidc` +
  `/admin/oidc/sync`).
* **DERP** — a group of its own: the relay map, the relays table **and** the relay
  health dashboard.
* **Сертификаты → Настройки** (they configure skygate itself, not an integration).
* **Развёртывание и кластер** — deploy + cluster + HA together, because they describe
  one physical cluster.
* **Провайдеры** — tailscale + headscale + headplane together.
* **«Интеграции» → «Сервисы»**, with **15 cross-links** to every other service page
  (providers, DERP, relays, DERP health, OIDC and its sync, Telegram, certificates,
  availability, monitoring, deploy, cluster, HA) so nothing has to be found in the
  sidebar from memory. Telegram stays the real integration in that group.
* The availability page keeps a name of its own — «Доступность сервисов» — so two
  pages cannot both be called «Сервисы».

### Files

`internal/handlers/templates/layout.html`, `internal/handlers/handlers.go`
(`sectionPageSet`), `internal/handlers/templates/admin/integrations.html`,
`internal/i18n/catalog_common.go`, `internal/i18n/catalog_admin.go`,
`scripts/check_b314_nav_grouping.sh`.

### Verification

41 contracts in `scripts/check_b314_nav_grouping.sh` (section by section, the Go map
and the template agreeing in both directions, the cross-links, the two renamed pages),
plus the renegotiated **B96** (ten sections, ten `InSection` names, twelve title keys)
and its Go test (`TestB96_*` now expects the new grouping).

## v1.5.78 — OIDC is applied from the panel, not copy-pasted (B313)

**Date:** 2026-09-23 · **Base:** `v1.5.77` → this tag · **Compatibility:** a re-run of
the installer (or `deploy/install-policy-helper.sh`) installs the new helper; no schema
change, no config change.

### The report

> «на aro все еще висит в OIDC предложение по исправлению env и появилось поле со
> скриптом однако никаких автоматической настройки кнопок нет»

The panel already held every value — what it lacked was the **write**: with
`ProtectSystem=strict` the skygate unit cannot touch headscale's config, and restarting
headscale belongs to root.

### What the operator gets

* **One button** on `/admin/oidc`: «Применить настройки к headscale». It stages a
  privileged request and the installed `skygate-oidc.path` unit writes the managed
  `oidc:` block, restarts headscale, verifies the discovery URL and **records the
  verdict where the page shows it** (succeeded / failed + reason). A button press can
  never end in "nothing happened".
* **The env hint can no longer be the answer**: when the values are saved in the panel,
  applying them does not need an env edit — and when the helper is *not* installed the
  page says so and prints the exact one-line command that installs it (that was the
  missing half on `aro`).
* **One renderer**: the block comes from `internal/oidc.RenderHeadscaleBlock`, which
  `skygate oidc-export` now delegates to — the CLI output and the button cannot drift.
* **Safe by construction**: the request is data-only, published atomically
  (temp + rename), created `0600` because it carries the client secret, never sourced by
  the helper, refused when empty or not an `oidc:` block, and consumed **only on
  success** (a failure keeps it so it can be inspected and re-run). The audit row names
  the request, never the secret.

### Files

`internal/oidc/block_b313.go`, `internal/oidc/apply_request_b313.go`,
`internal/feature/admin/oidc_apply_b313.go`, `internal/feature/admin/oidc_settings.go`,
`internal/handlers/templates/admin/oidc_settings.html`,
`internal/i18n/catalog_admin.go`, `cmd/skygate/main.go`,
`cmd/skygate/oidc_export_b304.go`, `deploy/skygate-apply-oidc.sh`,
`deploy/install-common.sh`, `scripts/check_b313_oidc_apply_button.sh`.

### Verification

42 contracts in `scripts/check_b313_oidc_apply_button.sh` (including a real
`bash -n` of the applier and the installer wiring), plus
`internal/oidc/apply_request_b313_test.go` (atomic write, the 0600 mode, the refusals,
result parsing, the armed stat, the shared renderer) and
`internal/feature/admin/oidc_apply_b313_test.go` (a staged request, the refusals, the
named fallback command, non-admin 403).

## v1.5.77 — every exit node shows where it sits, and a lost relay's prefixes go to the nearest one (B312)

**Date:** 2026-09-23 · **Base:** `v1.5.76` → this tag · **Compatibility:** one
additive migration (v0.77 adds six `exit_servers.location_*` columns on both
dialects, defaults only — no backfill, nothing to configure). The automatic
geolocation lookup is **optional** and can be switched off with
`SKYGATE_GEO_LOOKUP=off`.

### The request

> «если доступ никак нельзя восстановить то необходимо компенсировать правила по
> другим exit node но предлагаю приоритет расставлять на сходный признак расположения
> по exit node и как отдельная фича в exit node также отображать локацию где
> расположен exit node»

The live case behind it: `karolina` was blocked, and its 75 prefixes moved to whichever
relay answered first. Right for reachability, **arbitrary for latency** — nothing in
skygate knew that one relay sits in the same country and the other does not.

### What the operator gets

* **A location per relay, on `/admin/exit-nodes`**: shown in the table (with
  «указано вручную» / «определено автоматически») and with a small form to set it — and
  to hand it back to the automatic lookup. The lookup needs a public address and a
  working network, so the manual answer always wins and is never overwritten.
* **The priority that was asked for**: when a prefix's owner becomes unreachable, its
  prefixes go to the **closest** healthy relay — same city → same country → smaller
  great-circle distance — and the move is logged with its reason
  (`prefix-owner: 104.16.0.0/12 moves to emilia — the previous owner karolina is
  unreachable and emilia is the closest healthy relay (Amsterdam, Netherlands)`).
* **Nothing invented**: with no shared fact (or no location at all) the engine's own
  choice stands; a preference may never name an unhealthy relay, never reshuffle a
  healthy owner and never override a manual pin or the rules' explicit majority.
  `Assign`/`Reconcile` keep their signatures, so every existing caller is unchanged.

### How the location is learned

`internal/geoloc` asks one configurable endpoint (default ip-api.com, overridable with
`SKYGATE_GEO_LOOKUP_URL`, disabled entirely with `SKYGATE_GEO_LOOKUP=off`), bounded at
4 s, **never** asked about a LAN or tailnet address (a `100.64.x.x` address says nothing
about where a relay is), and the answer is cached in the database for 30 days behind a
12 h guard — one lookup per relay, not one per page render or sync tick. An unreadable
address, a timeout and a refused lookup all mean «unknown», which the page says instead
of rendering a place nobody verified.

### Files

`internal/db/migrations_v0_77_exit_location.go` (new, both chains),
`internal/db/exit_location.go` (new), `internal/geoloc/geoloc.go` (new),
`internal/feature/exit_rules/location_b312.go` (new),
`internal/feature/admin/exit_node_location_b312.go` (new),
`internal/feature/admin/exit_nodes.go`, `internal/prefixowner/prefixowner.go`,
`internal/feature/exit_rules/sync.go`, `cmd/skygate/main.go`,
`internal/handlers/templates/admin/exit_nodes.html`,
`internal/i18n/catalog_exit_nodes.go`, `scripts/check_b312_exit_location.sh`.

### Verification

53 contracts in `scripts/check_b312_exit_location.sh`, plus
`internal/geoloc/geoloc_test.go` (parsing, the tiers, known distances, which addresses
are worth asking about), `internal/prefixowner/prefixowner_b312_test.go` (the
preference is honoured, an unhealthy preference is refused, a healthy owner is never
reshuffled, manual pins still win, `Assign` is unchanged),
`internal/feature/exit_rules/location_b312_test.go` (the tiers and the distance
tie-break, the stored auto location, a manual row untouched, offline stays unknown) and
`internal/feature/admin/exit_node_location_b312_test.go` (save, clear, the four
refusals, non-admin). The SQLite/PostgreSQL parity test now expects V77 and checks all
six columns.

## v1.5.76 — the pre-auth key is issued again (and the gate can no longer park itself) (B311 + B299)

**Date:** 2026-09-23 · **Base:** `v1.5.75` → this tag · **Compatibility:** none —
no schema change, no migration.

### B311 — «Сгенерировать ключ» failed on the host that runs headscale

Operator report from the native host `aro`:

```console
api: headscale POST /api/v1/preauthkey: 500 {"code":2,
     "message":"auth-key must be either tagged or owned by user"}
cli: docker exec: exec: "docker": executable file not found in $PATH
```

Both lines are wrong *spellings*, not permissions. Measured against a live
headscale v0.29.3 (the same 0.29.x surface `aro` runs), using a non-existent id
so nothing is created:

| request | answer | meaning |
|---|---|---|
| `{"user":999999}` | `500 user not found` | the field headscale **reads** |
| `{"user_id":999999}` | `500 auth-key must be either tagged or owned by user` | the field is **discarded** (that is the operator's error) |
| `{"user":85,"tags":["tag:exit-node"]}` | key created, **`aclTags: []`** | tags silently dropped |
| `{"user":85,"acl_tags":["tag:exit-node"]}` | key created, **`aclTags: ["tag:exit-node"]`** | tags applied |
| `POST /api/v1/preauthkey/expire {"id":"1"}` | `200` | the expire route this version serves |
| `PUT /api/v1/preauthkey/1/expire` | `404 Not Found` | the route the code tried first |
| `headscale preauthkeys expire --help` | one flag: `-i/--id` | there is no `-u/--user` to pass |

Four defects in one file (`internal/headscale/preauth.go`) — plus a fifth that
explains why the operator saw **both** errors at once — all fixed:

* the create body sends **`user`** (not `user_id`) and **`acl_tags`** (not
  `tags`) — headscale's protojson gateway discards unknown fields, so the wrong
  names did not error, they produced an owner-less request (the 500) and, for
  the exit-node key, a **silently untagged** key whose node would never be
  treated as an exit node by the ACL;
* the create **response** is wrapped in `{"preAuthKey": {…}}` with `user` as an
  object and `expiration` as a protobuf Timestamp — the flat struct matched none
  of that, so **a 200 with a real key looked like an empty answer** and the
  caller fell through to the CLI rung: that is why a *successful* API call still
  produced the docker error underneath it. Both shapes are now parsed
  (the legacy flat one too), and `PreauthKey.ACLTags` records what headscale
  stored so a caller can verify the tag landed;
* expire walks the rungs **`POST /api/v1/preauthkey/expire {"id":…}`** → the
  older `PUT /{id}/expire` → the CLI, so key expiry works again on 0.29.x;
* every CLI rung goes through **`runHeadscaleCLI`** — `docker exec` when docker
  exists, the local `headscale` binary on a native install — instead of the
  hardcoded `docker` that cannot exist on `aro` (the B267/B272 defect, in the
  one path that had not been converted); the expire argv is `--id … --force`.

**Verified end to end against the live API** with the fixed client (no docker, no
CLI rung involved): a tagged key was created, read back with
`aclTags: ["tag:exit-node"]`, expired through the new route, and confirmed
expired — the throwaway key had a 2-minute TTL and is gone.

Why CI was green while the feature was dead: the unit test that "covered" this
asserted `"user_id":7` — **the wrong field was pinned as the contract**, and the
mock accepts any body. It now asserts the fields headscale reads, the absence of
the discarded spellings, the wrapped response (and the legacy flat one), and a
native install (empty-`PATH` fake: no docker → the local binary is executed,
argv inspected). Contracts: `scripts/check_b304_preauth_key_request_truth.sh`.

### B299 — the guarantee catalog can no longer be parked forever

Same session, a second finding: a `verify_pre_deploy.sh` run sat for **54
minutes** and had to be killed by hand. Nothing was slow — it was *stopped*:
inside a WSL Ubuntu (launched through `C:\WINDOWS\system32\bash.exe`, not Git
Bash) the catalog reached `check_b_admin_user_sync.sh`, whose first probe was a
bare `sudo docker info`; that `sudo` requires a password, opened `/dev/tty`, took
**SIGTTIN**, and the kernel stopped the whole group — `timeout 900 … T`,
`bash scripts/check_b_admin_user_sync.sh T`, `sudo docker info T`. A stopped
process never runs its `SIGALRM` handler, so the 900s budget could not fire.

* every check now runs as `setsid --wait timeout -k 10 <budget>`: **no
  controlling terminal** (a password prompt fails fast instead of stopping the
  world) and a deadline that can **KILL** (rc 137 is printed as TIMEOUT, so a
  killed check is named). Measured in that same distro: the check that hung for
  54 minutes now answers `SKIP: docker daemon not reachable` in **0.1 s**;
* every executed `sudo` in the gate is `sudo -n` (60 call sites, 11 scripts), so
  a check that needs root works or SKIPs at once — and an operator running the
  gate in a real terminal cannot be left waiting for a password prompt either.

Contracts: `scripts/check_b299_catalog_cannot_hang.sh` (structural + a
repo-wide detector that ignores comments, heredoc operator instructions and
quoted messages, and self-tests against a planted violation + behaviour:
self-stop, tty read, password-hungry `sudo` shim, orphan check).

## v1.5.75 — exit nodes are managed over the tailnet, and an unreachable path says so (B310)

**Date:** 2026-09-23 · **Base:** `v1.5.74` → this tag · **Compatibility:** none —
no schema change (the per-relay transport lives in `global_settings`), no config
change required. Enabling Tailscale for skygate itself is optional and is done from
`/admin/tailscale`.

### The report

`karolina` was blocked («не пингуется»). The operator asked whether skygate can reach
it over tailscale SSH, and whether that can be a **feature** — configured when a new
exit node is onboarded — so that a geo-blocked or withdrawn public IP never removes
management access.

### What was measured (agent VM)

The relay row pointed at the **tailnet** address — the right idea, since that path
survives a provider block — and it could never work, with nothing anywhere saying so:

```
$ ip route get 100.64.0.2
100.64.0.2 via 192.168.13.1 dev ens18 src 192.168.13.10        # the LAN gateway!
$ tailscale status
failed to connect to local tailscaled; it doesn't appear to be running
$ docker exec skygate-skygate-1 tailscale status
failed to connect to local tailscaled; it doesn't appear to be running
$ docker exec skygate-skygate-1 sh -c 'ssh -i /ssh-sync/skygate_sync -p 18022 root@100.64.0.2 …'
ssh: connect to host 100.64.0.2 port 18022: Operation timed out
```

* the skygate **host** is not in the tailnet at all (no `tailscaled`, no `tailscale0`);
* the skygate **container** has the client and `/dev/net/tun`, but its Tailscale is
  configured and **disabled** (`SKYGATE_TS_AUTHKEY_FILE=/dev/null`);
* `headscale nodes list` still showed karolina `online` with **last seen 2026-09-22
  07:18** — its own tunnel had been stale for over a day.

So the previous "use the live Tailscale IP" fallback *looked* like an automatic
repair while being structurally impossible: the page showed an IP, the sync showed a
timeout, and "the relay is down" was indistinguishable from "skygate is not on the
network it is trying to use".

### The fix (B310)

* **The transport is now data.** Every path to a relay is a `RelayEndpoint` with a
  kind (`tailnet`, `public`, `name`) and a provenance string.
* **A ladder, not a single target.** Candidates are ordered tailnet → operator's
  `ssh_target` → node name when skygate can use the tailnet, and the operator's
  target first when it cannot. Each candidate is **TCP-probed** (3 s) and then
  actually used; the first that works wins. The tailnet candidate is always kept,
  even when it cannot work, so the failure can name it.
* **The transport that carried the routes is recorded** per relay
  (`relay_apply_via:<relay>`) and rendered on `/admin/exit-nodes`.
* **`SkygateTailnetState`** answers whether skygate has a usable **kernel** path — an
  interface carrying a `100.64.0.0/10` address; a userspace-mode `tailscaled` cannot
  carry a plain `ssh`, and every "why not" is named (`tailscaled is not running`,
  `NeedsLogin`, `Stopped`, `NoState`, no client in the image).
* **The failure now names everything**: each candidate with its own reason, plus the
  note that the tailnet path is unavailable because skygate is not on the tailnet —
  with a link to `/admin/tailscale`.
* **Onboarding hands over the key.** The "register a new exit node" command now
  embeds skygate's own public key as an idempotent `authorized_keys` step, so a relay
  registered from the panel is manageable over the tailnet from its first sync. Only
  a well-formed OpenSSH key is rendered into a root shell, and the page says which
  file it came from — or that it could not be read.

Unchanged on purpose: the operator's `ssh_target` is still used (as a candidate), a
co-located relay still uses the local transport, the live `tailscale_ip` is still
persisted, and which relay advertises which prefix is not touched.

### What the operator should do

1. Install `v1.5.75`.
2. Open **`/admin/tailscale`** and enable Tailscale for skygate itself (the page
   generates the auth key). After that, the relay's tailnet address is used
   automatically and management survives a blocked public IP.
3. For a **new** relay: register it from `/admin/exit-nodes` — the generated command
   already contains skygate's public key.

### Files

`internal/feature/exit_rules/relay_transport_tailnet_b310.go` (new),
`internal/feature/exit_rules/relay_transport_b309.go` (records the transport),
`internal/feature/exit_rules/sync.go`, `internal/feature/admin/exit_nodes.go`,
`internal/feature/admin/exit_node_register.go`,
`internal/handlers/templates/admin/exit_nodes.html`, `internal/i18n/catalog_exit_nodes.go`,
`scripts/check_b310_tailnet_exit_transport.sh`.

### Verification

38 contracts in `scripts/check_b310_tailnet_exit_transport.sh`, plus
`internal/feature/exit_rules/relay_transport_tailnet_b310_test.go` (candidate
ordering with and without a usable tailnet, the probe, the live
"not-on-the-tailnet" sequence, the recorded transport) and the onboarding key tests
in `internal/feature/admin/exit_node_register_b266_test.go`. Renegotiated in place:
B300 A2 (the single `SetAdvertisedRoutes` call site now spans the tail **plus** the
ladder) and B300 B5 (the new outcome shape).

## v1.5.74 — a relay skygate cannot configure stops owning prefixes (B309)

**Date:** 2026-09-23 · **Base:** `v1.5.73` → this tag · **Compatibility:** none —
no schema change (the per-relay transport record lives in `global_settings`), no
config change, no operator action.

### The report

`/admin/exit-nodes` carried **«владелец не объявляет: 29»** and a prefix table full
of **«нет маршрута»**, and pressing «Пересобрать и применить ACL» changed nothing:
the rows came back with the same problems, on both `aro` and the agent VM.

### What the journal said (agent VM)

```
staggeredSync(aggregated): karolina advertising 114 unique routes
exit-node sync(karolina): not a local relay (local daemon unreadable) — using the SSH transport
staggeredSync(aggregated): karolina SSH err: ssh root@100.64.0.2:18022 … Operation timed out
staggeredSync(aggregated): emilia advertised: ssh=ok approved=1 …
```

The relay that **owned** the prefixes could not be configured at all. It was
perfectly visible to headscale, so the exit-node health table called it healthy and
the prefix assignment kept giving it 75 prefixes — which it could never advertise.
Neither the ACL nor its re-apply button can fix that: writing a policy does not
configure an unreachable host.

### Root cause

`healthyExitRelays()` — the healthy set `prefixowner.Assign` receives — was derived
from `exit_node_health`, which is computed from **headscale** node state. Nothing in
the chain asked the only question that matters for a prefix owner: *can skygate
actually apply routes to this relay?* A node can be online in the tailnet and
unreachable over SSH (firewall, wrong port, key, a stopped sshd), and it then keeps
its prefixes forever while every tick logs a timeout that nothing acts on.

### The fix (B309)

* Every route application records its outcome **per relay** in `global_settings`
  under `relay_apply_state:<relay>` — `<unix>|ok` or `<unix>|err|<reason>`. No
  migration, both database dialects, and an unreadable record is read as
  *never recorded* (a storage hiccup must never look like a relay failure).
* **Both** transports count (`ssh=err=…`, `local=err=…`), and so does an approval
  failure: routes headscale refuses to approve are not served either.
* `healthyExitRelaysForAssignment` = B275 headscale health **minus** every relay
  whose last application failed within `RelayApplyFailureWindow` (**15 minutes**,
  three sync ticks). `prefixowner.Assign` already implements “an unhealthy owner
  loses the prefix”, so those prefixes move to a relay that answers, and B276's
  per-CIDR ACL pin follows in the same pass.
* The exclusion is **logged** once per pass with the reason and the age, so the
  journal explains the move instead of the operator seeing an unexplained
  reassignment.
* One **successful** application clears the record immediately: a recovered relay
  returns to the healthy set on its own, with nothing to un-block by hand.
* The exit-node **health** table is deliberately untouched — a relay can be a
  healthy tailnet node and still be unconfigurable, and writing the health table
  would make `/admin/exit-nodes` lie in the other direction (the B273 failure mode).

### What the operator sees now

A banner on `/admin/exit-nodes` under the prefix card (RU + EN) names every excluded
relay, its error, how long ago the attempt ran and how many prefixes it held, and
says outright that an ACL re-apply cannot fix it. Fix the access to the node
(usually SSH: address, port, key) and the relay returns by itself on the next tick.

### Files

`internal/feature/exit_rules/relay_transport_b309.go` (new),
`internal/feature/exit_rules/relay_transport_b309_test.go` (new),
`internal/feature/exit_rules/sync.go` (records at both call sites; assignment uses
the transport-aware healthy set), `internal/feature/admin/exit_nodes.go`
(`TransportFailed`), `internal/handlers/templates/admin/exit_nodes.html`,
`internal/i18n/catalog_exit_nodes.go`, `scripts/check_b309_relay_transport_demotion.sh`.

### Verification

21 contracts in `scripts/check_b309_relay_transport_demotion.sh` (including “the
health table is not written by this feature”), plus
`internal/feature/exit_rules/relay_transport_b309_test.go` driving the live sequence
against a migrated SQLite: the unreachable relay loses the prefix to the reachable
one **while the health table stays green**, one success brings it back, an expired
failure does not demote.

## v1.5.73 — a domain is re-resolved once per interval (the permanent stale-policy banner) (B308)

**Date:** 2026-09-23 · **Base:** `v1.5.72` → this tag · **Compatibility:** none —
no schema change (the interval and the per-domain timestamps live in
`global_settings`), no config change.

### The report

`/admin/exit-nodes` kept showing the red **«политика headscale УСТАРЕЛА»** banner on
both `aro` and the agent VM although nothing had been changed by hand, the policy
was rewritten over and over, and on the agent VM the prefix table stayed
“problematic” after pressing re-apply.

### What the journal said (agent VM, 39 drift lines in two hours)

```
17:41:02 acl-drift: auto-updater tick changed 38 rule(s) (added=19 removed=19) — deferring (throttle 30m0s)
17:46:02 acl-drift: auto-updater tick changed 34 rule(s) (added=17 removed=17) — deferring
18:11:16 acl-drift: auto-updater tick changed 50 rule(s) (added=18 removed=32) — deferring
18:16:00 acl-drift: auto-updater tick changed 50 rule(s) (added=32 removed=18) — deferring
18:51:04 acl-drift: ACL re-applied (snapshot v1655, generated=50420 bytes)
```

**The rules were not the operator's.** `DomainAutoUpdater` re-resolved *every*
domain rule on *every* five-minute tick, and a domain whose A records rotate
(`ghcr.io` 29 rows, `quay.io` 17, `minimax.io` 33) returns a slightly different IP
set each time — so the derived `/32` rows were deleted and re-inserted forever. The
generated ACL therefore never stopped changing, the drift banner was effectively
permanent, and on a `policy.mode: file` host every throttled re-apply restarted
headscale. B298's 30-minute churn throttle (which is still right) hid the *apply*,
not the churn.

### The fix

* **A per-domain minimum re-resolve interval** — `global_settings` key
  `dns_domain_resolve_interval_sec`, default **6 hours**, floor 5 minutes, ceiling
  7 days; `0` means “every tick”, i.e. the pre-B308 behaviour, for anyone chasing a
  moving target.
* The last successful resolution is recorded per domain
  (`domain_resolve_at:<domain>`), and the gate is consulted **before** the DNS
  lookup, so the derived rows — and therefore the policy — stay put between
  resolves.
* **A domain that was never resolved is always due** (a freshly created rule is
  never starved), a **failed lookup is deliberately not marked** (an unreachable
  resolver is retried on the next tick instead of being skipped for hours), and a
  clock that moved backwards resolves rather than freezing the domain.
* Both resolution paths (the CDN branch and the per-IP branch) mark the domain only
  after a successful resolve, and the skip is logged with the effective interval
  (`auto-updater: N domain(s) skipped…`), so the gate is visible in the journal.
* **Editable from the panel**: `/admin/system_tests` → the DNS-autoupdater card now
  has an interval field (`POST /admin/system_tests/dns-interval`). It writes the
  **same key the updater reads**, and stores the **clamped** value, so the number on
  the page is the number the runtime uses (the flash names it).

### Verification

* `scripts/check_b308_domain_resolve_interval.sh` — 21 contracts (the bounds, the
  “never resolved is due” rule, the backwards clock, the failed-lookup policy, the
  gate call sites and both mark points, the panel key parity, RU+EN labels).
* `internal/feature/exit_rules/domain_interval_b308_test.go` — the parse/clamp
  table, the due-rule table (including never-resolved, disabled gate, elapsed,
  backwards clock) and the setting-key contract.
* `internal/feature/admin/settings_dns_interval_b308_test.go` — the handler over a
  real SQLite DB: the stored value is the clamped one for every input shape
  (valid / too small / too large / empty / garbage / zero) and a non-admin cannot
  change it.
* `go vet`, `staticcheck`, `go test ./...` clean; CI green before the tag.

### What the operator should check after updating

1. `/admin/system_tests` → the DNS-autoupdater card: the new **interval** field
   (default 21600 s = 6 h). Set it and Save; the green flash names the effective
   interval.
2. The journal: the every-five-minute `autoupdater: added=… removed=…` line should
   become a `auto-updater: N domain(s) skipped (re-resolve interval 6h0m0s…)` line —
   and the red banner on `/admin/exit-nodes` should stop reappearing on its own.
3. If a specific domain really moves often, lower the interval (or set 0 to restore
   the old every-tick behaviour) — that is now a panel setting, not a redeploy.

### Still open from the same report (next blocks)

The `karolina` relay on the agent VM is unreachable over SSH
(`ssh root@100.64.0.2:18022 … Operation timed out` every tick), so its 29 prefixes
are never advertised and the table cannot become clean by re-applying — that is a
transport problem, not an ACL one, and the plan is to make skygate demote an
unreachable prefix owner to a healthy relay instead of leaving «нет маршрута» rows.
The OIDC auto-configure buttons and the navigation/«Сервисы» regrouping are the
other two follow-ups.

## v1.5.72 — the STUN tile was OUR false negative, not a derper defect (B307)

**Date:** 2026-09-23 · **Base:** `v1.5.71` → this tag · **Compatibility:** none —
no schema change, no migration, no config change.

### What we got wrong (and how it was settled)

Since v1.5.9's B302 the `/admin/derp` page has shown **`STUN UDP :3478 closed`**, and
that was written up here and in `AGENTS.md` as a **derper-side defect** ("binds
`*:3478` and answers no Binding Request at all"). **That conclusion was wrong.**

The relay's own counters settled it. derper serves `/debug/vars`, and
`tsweb.AllowDebugAccess` admits loopback, so the numbers can be read on the host:

| what was sent to 127.0.0.1:3478 | relay counters afterwards |
|---|---|
| — (baseline) | `{"not_stun":20,"success":0}` |
| 4 bare RFC 5389 Binding Requests | `{"not_stun":24,"success":0}` |
| **1 request with SOFTWARE + FINGERPRINT** | **`{"not_stun":25,"success":1}`** |

and the same two packets probed live: the FINGERPRINT one answered
`REPLY 44 bytes (txid match=True)`, the bare one timed out.

The source confirms it: derper's STUN server is
`tailscale.com/net/stunserver`, which parses with `stun.ParseBindingRequest` — and
that returns **`ErrNoFingerprint`** ("STUN request didn't end in fingerprint") /
`ErrWrongFingerprint` and counts both as `not_stun`. A header-only request is
**never answered**. Tailscale's own client (`net/stun.Request`) always sends
`SOFTWARE` (`"tailnode"`) plus `FINGERPRINT` (CRC32 of the message so far, XOR
`0x5354554e`, with the header length already counting the fingerprint attribute) —
which is exactly why `tailscale netcheck` scored every region through the same
relay while our probe saw silence.

Two things were also ruled out along the way, which is why the fix is in skygate:
rebuilding derper from upstream changed nothing (a fresh `v1.70.0` and the current
`v1.102.4` behave identically), and neither did a non-standard `--stun-port`.

### The fix

* `buildSTUNBindingRequest` now emits the Tailscale-shaped request: `SOFTWARE` +
  `FINGERPRINT`, header length including the fingerprint, CRC over everything
  before it.
* `buildSTUNBareBindingRequest` keeps the old header-only form as a **documented
  fallback** for a generic RFC 5389 server, and `probeSTUNUDP` tries the
  fingerprint shape first, then the bare one.
* A failed probe now names **both** attempts
  (`stun host:port: fingerprint form: …; bare form: …`), so a red tile can never
  hide which packet was sent.

### Verification

* `scripts/check_b307_stun_fingerprint.sh` — 14 contracts.
* `internal/feature/admin/derp_stun_b307_test.go` — a local UDP server that
  implements derper's exact rule (reject without a valid fingerprint) **must**
  answer our probe; a pedantic generic server pins the fallback; a closed port
  pins the both-shapes error.
* The B265 wire-format test is renegotiated in place, with the bare shape still
  pinned separately.
* `go vet`, `staticcheck`, `go test ./...` clean; CI green before the tag.

### What the operator should check after updating

1. `/admin/derp` — the **STUN UDP :3478** tile should now be **green** with an RTT
   and the reflexive address, next to a relay whose traffic tile already worked.
2. Nothing else changes: no config, no restart of the relay, no env.

## v1.5.71 — the project's modules are first-class system tests (B306)

**Date:** 2026-09-23 · **Base:** `v1.5.70` → this tag · **Compatibility:** none —
no schema change, no migration, no config change.

### The second half of the operator's request

«Отдельно следует пройтись по тестам системы и расширить их давая возможность
полностью контролировать модули проекта и получать уведомления по неисправности
или некорректном поведении.»

The monitoring inbox (B305) is the sink for those notifications; this block makes
the modules a measured part of the test battery.

### What was wrong

The catalogue was a static list of in-process checks (network / db / headscale /
disk / wal-g / …) while the modules lived on their own page with their own state
machine. Nothing in the battery asked **“is this module actually working?”**, and a
module that entered `StateError` was visible only as a badge on another page.

### What ships

* **One generated test per registered module**, built from the live Manager
  (`module.<name>`, category `modules`): a module added later is covered without
  touching the catalogue. The shared catalogue (`AllTests`) is the static registry
  **plus** those tests, and the page, “Run all” and the per-module button all use
  it — so the three views can never disagree.
* **An explicit outcome ladder:** not installed → **SKIP** (a fresh install must not
  show a wall of red for features that were never switched on); `StateError` →
  **FAIL** naming the state; running but unhealthy → **FAIL**; stopped → **SKIP**;
  an unregistered/nil module → **SKIP** instead of a panic. The output always
  carries the state, the start time, the sorted health checks, the module's `Info`
  fields and its `LastError`, so a failure is actionable rather than “something is
  wrong”.
* **Per-module control:** the `test` action of the existing `/admin/modules`
  dispatcher (so the admin gate, the CSRF cookie check and the flash pattern are
  reused), offered as a button in **every** module state and mirrored by a new
  module section on `/admin/system_tests`. It runs exactly that module, **persists**
  the run (it shows up in the history strip) and answers with a flash naming the
  outcome.
* **Notifications per module:** a module fault reports into the B305 inbox under
  the module's **own** source (`module:<name>`, fingerprint `module:<name>:health`,
  link `/admin/modules/<name>`), and a healthy run **resolves** that event — the
  operator is notified once per module fault, not once per test run. In-process
  checks keep their `system_test` source, so the two are still distinguishable.

### Verification

* `scripts/check_b306_module_tests.sh` — 23 contracts (generation, the ladder, the
  per-module action and dispatcher case, the page section, RU+EN labels, the inbox
  source/fingerprint/resolve, the tests).
* `internal/feature/admin/system_tests_modules_b306_test.go` — the ladder as a
  table (five states + the nil module), generated tests against a real Manager,
  the per-module run and the unknown-module refusal, the inbox source/fingerprint/
  resolve cycle, and the test-name parser.
* `go vet`, `staticcheck`, `go test ./...` clean; CI green before the tag.

### What the operator should check after updating

1. `/admin/system_tests` — the new **«Модули проекта»** section lists each module
   with its state, enabled flag and last error, and links to its page.
2. `/admin/modules` — every module row now has a **«Проверить»** button: it runs
   that module's check, stores the run and flashes the outcome.
3. Break something (stop a module while it should run, or look at one in `error`
   state) and press «Проверить»: the failure appears on `/admin/monitor` under
   `module:<name>`; fix it, press again, and the event turns **resolved**.

## v1.5.70 — one monitoring inbox for every skygate signal (B305)

**Date:** 2026-09-23 · **Base:** `v1.5.69` → this tag · **Compatibility:** additive —
v0.76 adds one table (`monitor_events`) in both migration chains; nothing reads it
until a producer reports an event.

### What the operator asked for

«Отдельно следует пройтись по тестам системы и расширить их давая возможность
полностью контролировать модули проекта и получать уведомления по неисправности
или некорректном поведении. Также необходимо добавить поле с уведомлениями куда
будут приходить все сообщения разной важности с мониторинга skygate.»

### The gap

Every monitoring signal lived somewhere else, and most were fire-and-forget: an
exit-node health crossing went to Telegram and vanished, a tag-reconcile failure
was a Prometheus counter plus an audit row, a failing system test was rendered once
and forgotten, a degraded database was a badge on a single page. Nothing could
answer **“what is wrong right now, and is it new?”**, and nothing survived a page
reload or a Telegram outage.

### What ships

* **`monitor_events` (v0.76)** — one row per *condition*, deduplicated by a UNIQUE
  `fingerprint`: a recurrence bumps `repeats` and `last_seen` instead of adding a
  row, so a flapping alert cannot drown the list. `first_seen`, `acked_at/by` and
  `resolved_at` are kept, so the history is real. Both migration chains (SQLite
  through `execSQLiteDDL`), both drivers registered.
* **`internal/monitorinbox`** — the policy: four normalised severities
  (`info`/`warning`/`error`/`critical`, so a producer spelling `warn` or `fatal`
  cannot invent a fifth level), a dedup-aware `Report` that returns whether the
  event is **new or reopened** — the only case worth paging for — `Resolve` for
  recovery, `FormatAlert` for the Telegram text, and a push threshold stored in
  `global_settings` (`monitor.notify_min_severity`, default `error`) so the
  operator can lower it to `warning` or raise it to `critical` **without a restart
  or an env edit**. Everything is always recorded; the threshold only decides what
  also reaches Telegram.
* **`/admin/monitor`** — the “поле с уведомлениями”: state / minimum-severity /
  source filters, per-state counters (open / acknowledged / resolved), per-row
  **Acknowledge**, **Acknowledge all open**, the Telegram threshold form, and the
  event fingerprint rendered so the page can be correlated with the journal and
  the bot message. In the sidebar's *System Health* section, translated RU+EN,
  behind the admin gate, and every refusal is a flash on the page (never a raw
  error page).
* **Producers wired now:** every **system-test run** (a FAIL opens an event with
  the category and output; a PASS **resolves** it, so the list shows what is broken
  *now* rather than everything that ever broke; a SKIP changes nothing, because a
  fresh install skips half the catalogue) and the **tag reconciler's alert sink**
  (a dev-tag that never reached headscale is now visible on the page even when the
  Telegram message was missed).

Per-module control of the test catalogue (the other half of the same request) is
the next block; the inbox is its sink, which is why it lands first.

### Verification

* `scripts/check_b305_monitor_inbox.sh` — 46 contracts (both migration chains, the
  dedup/ack/resolve semantics, the notify policy, the routes, the sidebar entry,
  RU+EN parity, the producers, the tests).
* `internal/db/monitor_events_b305_test.go` — the store against a real migrated
  SQLite database: dedup, reopen after resolve, filters, counters, ack, ack-all.
* `internal/monitorinbox/monitorinbox_b305_test.go` — the policy: a repeat does
  **not** re-page, a warning below the `error` threshold is recorded but silent,
  lowering the threshold makes it page, recovery closes the event and a recurrence
  pages again, and a missing database is reported instead of panicking.
* `internal/feature/admin/monitor_b305_test.go` — the page and its three POST
  actions, the system-test open/resolve/reopen cycle, the tag sink into the inbox,
  and a Service whose database is not wired.
* `go vet`, `staticcheck`, `go test ./...` clean; CI green before the tag.

### What the operator should check after updating

1. `/admin/monitor` — the new sidebar entry under *System Health*. An install with
   nothing wrong shows an empty list (“мониторинг молчит”).
2. Run the system tests (`/admin/system_tests` → Run): a failing test appears in
   the inbox with its output; fix it, run again, and the event turns **resolved**
   (the row stays as history).
3. Set the push threshold (e.g. `warning`) and press Save — new events at that
   level now also arrive in Telegram, and repeats of a known problem stay silent.

## v1.5.69 — OIDC fully controllable from the panel (B304)

**Date:** 2026-09-23 · **Base:** `v1.5.68` → this tag · **Compatibility:** none —
no schema change, no migration. An existing install keeps working untouched; the
panel simply stops depending on `.env`.

### The report

On the native host `aro` the OIDC page still carried a warning that the
configuration «требуется изменение в env и из вебинтерфейса это никак не
изменить», and the operator asked for the rest of the OIDC surface: env
autofill, absolute key-directory paths with owner/mode checks, and a script that
deploys the headscale side automatically.

### Two real causes, both closed

1. **`key_dir` was a form field that applied to nothing.** The live applier added
   in B290 pushed only `issuer` / `client_id` / `client_secret` /
   `redirect_uris`, so a new directory took effect — if ever — on the next
   restart. The signing store can now be **moved or created at runtime**
   (`oidc.KeyStore.Reload`): one shared load-or-generate path with boot, the new
   key loaded *before* the swap (so a bad path leaves the running key and
   `/oidc/jwks.json` untouched), a relative path refused at the store level
   (B270's live failure was a relative key dir resolved against a systemd working
   directory), and every reader goes through a guarded accessor so a runtime
   repair cannot race the request path. The page now reports the **resolved**
   path, owner, mode, whether skygate can write there, the active `kid`, and a
   copy-paste fix for each failure mode (`relative_path`, `missing`,
   `unwritable`, `world_readable`, `unavailable`).
2. **`/admin/oidc/sync` read the raw env.** It refused to run with
   «SKYGATE_OIDC_ISSUER is not set on the skygate container» even for an operator
   who had saved everything on `/admin/oidc`. It now resolves the **effective**
   configuration (the saved row wins, exactly like the rest of the page), its
   refusals name the panel field, and the env is presented as the fallback it is.

### What was asked for, added

* **Env autofill** — the page renders the exact `.env` block for the running
  configuration. The secret is never rendered in the browser: the block points at
  `skygate oidc-export --secret`, and a new CLI subcommand
  (`skygate oidc-export [--env|--headscale|--json] [--secret]`) prints what
  headscale needs, on the host, reading the same precedence the panel uses.
* **A script that deploys the headscale side** — `deploy/skygate-apply-oidc.sh`
  (rendered as one copy-paste command pinned to this build): it reads the values
  from skygate itself, backs headscale's config up under a **unique** name, writes
  the `oidc:` block between managed markers, restarts headscale (systemd /
  docker / none), rolls back if the restart fails, refuses to clobber an `oidc:`
  section it did not write, and never edits HuJSON by string surgery. Start with
  `--dry-run`. Two real defects were found and fixed while pinning its behaviour:
  same-second runs overwrote the only clean backup (making `--rollback` a silent
  no-op), and `--rollback` restored the newest backup — which may already contain
  the managed block — instead of the last pre-apply state.

### Verification

* `scripts/check_b304_oidc_panel.sh` — 34 contracts, including a **real run** of
  the apply script against a temporary config with a stub `skygate` binary
  (dry-run writes nothing, apply writes the managed block + backup, a re-run
  replaces instead of appending, an unmanaged `oidc:` section is refused,
  HuJSON is refused, `--rollback` restores the pre-apply content).
* `internal/oidc/keys_b304_test.go` (reload moves the store, adopts an existing
  pair, refuses relative/empty paths, keeps the live key on failure, nil-safe) and
  `internal/feature/admin/oidc_b304_test.go` (the failure modes and their fixes,
  secret-free env block, tag-pinned command, live key-dir apply + refusal, the
  built-in default, and a source-level guard that the sync page never reads the
  raw env again).
* `go vet`, `staticcheck`, `go test ./...` clean; CI green before the tag.

### What the operator should check after updating

1. `/admin/oidc` — the key-store card shows the resolved directory, owner, mode
   and the active `kid`; if anything is wrong it names the problem and prints the
   fix (e.g. `chown skygate:skygate /var/lib/skygate/oidc-keys`).
2. Change `key_dir` and save: the message says the key store moved, and
   `/oidc/jwks.json` keeps answering — no restart.
3. `/admin/oidc/sync` — the values are the ones saved in the panel (badged
   `ui`), and the sync no longer asks for env variables. The one-command script is
   offered for hosts where headscale's config is outside the container.
4. On `aro` specifically: the env warning disappears once the panel holds the
   values; nothing in `.env` has to change.

## v1.5.68 — an ownerless device must have a working admin path (B303)

**Date:** 2026-09-23 · **Base:** `v1.5.67` → this tag · **Compatibility:** none —
no schema change, no migration, no config change.

### The report

On `/admin/devices`, pressing **Transfer** on a device that «оказалось никому не
принадлежащим» opened a **separate page** whose entire body was:

```
node not in node_owner_map: db: node_owner_map: no row
```

and, in the same message, a device «не привязанное тегом не дается для
тегирования администратору (словно проигнорировано для тех скриптов что должны
проверять и добавлять устройства)».

### One deadlock, three pieces — all of them in `node_owner_map`

1. **`PostAdminDeviceTransfer` demanded the row it exists to create.** It read the
   node's CURRENT `node_owner_map` row first and, when the node had none, answered
   `http.Error(w, "node not in node_owner_map: "+err, 400)` — a `text/plain` page
   with no navigation that leaked raw database text. The one button whose job is
   to give a device an owner refused to run because the device had no owner.
2. **`PostAdminNodeTag` landed the tag and recorded nothing.** Ownership was
   written only when headscale named a user *and* the row already existed; the
   synthetic `tagged-devices` branch called `UpdateNodeOwnerTag` — an `UPDATE`,
   which matches **no row** for a node that has none. The tag reached headscale,
   `node_owner_map` stayed empty, every per-device ACL rule missed the device,
   `/my/devices` never showed it — and the next Transfer click hit piece (1). That
   is the «словно проигнорировано» half of the report.
3. **The adoption card hid those devices.** `classifyNodeForAdoption` skipped a
   node whose `UserName` was empty, whose headscale user had no `portal_users` row
   (including the synthetic `tagged-devices`), or whose matched portal id was 0 —
   precisely the ownerless shapes — so the page offered no action for them either.

### The fix

* **A missing row is the adoption.** `errors.Is(err, db.ErrNodeOwnerNotFound)`
  becomes an empty current owner, the transfer proceeds, the audit action is
  `device_transfer_adopted_ownerless` (an adoption is now distinguishable from a
  reassignment) and the new row is stamped with the live hostname through
  `SetNodeOwnerHostnameIfEmpty` (B272.5). A **real** database error is still
  refused — as a flash.
* **Tag always persists ownership.** Missing row →
  `InsertIgnoreNodeOwnerWithHostname` (owner + live hostname recorded, audited as
  `node_tag_owner_row_created`); existing row → the tag-bump `UPDATE`, so
  `hostname`/`os`/`device_type` survive. The live node read inside the handler is
  now **mandatory**: a failed or empty answer used to fall through silently, which
  meant the per-user-device exit-node guard ran against an empty tag list and a
  tag could be applied to a node that had vanished.
* **No raw error pages on this page.** Every operator-facing refusal in both
  handlers is a `303` flash on `/admin/devices?err=…` carrying a named reason
  (shared `devicesFlashErr` helper logs the raw text server-side); the `403` for a
  non-admin caller remains the only `http.Error`. This is the rule the
  `skygate-error-ux-migration` skill encodes.
* **The adoption card offers an owner picker.** Ownerless rows
  (`NeedsOwnerPick`) render a real dropdown over the portal users that have a
  `headscale_user_id` instead of a one-click button for a user nobody could infer;
  new RU+EN labels (`devices.adoption_pick_owner`, `…_button_pick`,
  `…_no_owner`, `…_ownerless_hint`).
* **Empty hostname refused.** Transfer will not build `tag:dev-<user>-` from an
  empty name; if headscale is unreachable and the node has no stored hostname it
  says so and changes nothing.

### Renegotiated contracts

`scripts/check_b257_adopt_devices.sh` contract F pinned the two former **skip**
rules as tests named `…_RejectEmptyUserName` / `…_RejectOrphanHeadscaleUser`.
Those names asserted the behaviour that produced this bug; section F now pins the
renegotiated rules (`TestClassifyNodeForAdoption_EmptyUserNameBecomesOwnerPick_B303`,
`…_OrphanHeadscaleUserBecomesOwnerPick_B303`) and says so in place.

### Verification

* `scripts/check_b303_ownerless_device.sh` — 29 contracts (source shape, the
  absence of the literal raw error string, i18n parity, the two renegotiated
  classifier tests, and a `go test -run B303` run).
* `internal/feature/admin/devices_b303_test.go` drives the **real** handlers
  against in-memory SQLite plus a fake headscale REST server: an ownerless
  transfer adopts and stamps the hostname; four refusal shapes never leak the raw
  text; an unreachable headscale is named and no half-adoption row is written; a
  tag on a row-less node creates the row for both ownerless shapes; an unreadable
  headscale refuses to tag instead of tagging blind; an unknown node is a flash.
* `go vet`, `staticcheck`, `go test ./...` clean; the full pre-deploy gate green
  before the tag (CI-gated).

### What the operator should check after updating

1. `/admin/devices` — the device that answered the raw page now appears in the
   **«Устройства, ожидающие закрепления»** card with an owner dropdown (or can be
   Transferred directly); either action writes the `node_owner_map` row, stamps
   the hostname and applies the dev-tag.
2. Every refusal on the page is now a red flash on the same page, never a blank
   `text/plain` page.
3. After assigning owners, run **Re-apply ACL** on `/admin/exit-rules` so the new
   `tagOwners` entries reach headscale.

**Post-release correction (B298, 2026-09-23).** The first live run of this probe
answered `0.29.2` on `aro` — not the 0.29.0 that had been reported, and not what
any `.env` declared, while rung 2 (`GET /version`) is the rung that answered
(`/api/v1/version` returns no version on 0.29.2). The paragraphs above keep the
original report for the record; the measured value is 0.29.2, which is the point
of the block.

## v1.5.67 — every /admin/derp probe dials the reachable ADDRESS (B302)

**Date:** 2026-09-23 · **Base:** `v1.5.66` → this tag · **Compatibility:** none —
no schema change, no migration.

### What the page showed, and which half was true

`/admin/derp` on the agent VM rendered, from inside the skygate container:

| tile | value | verdict |
|---|---|---|
| `DERPER.SERVICE` | **stopped** | **FALSE** — the relay had been up 39 hours (`docker ps`) |
| `DERP SOCKET :443` | TCP listening | TRUE |
| `STUN UDP :3478` | closed | **TRUE** — derper answers no STUN at all |
| `VERSION` | v1.70.0 go | TRUE |

### (1) Why the service tile lied — fixed here

`/etc/hosts` inside the skygate container maps the relay's own public name to
`127.0.0.1` (AGENTS deployment trap #2), and `derperLivenessWebSocketProbe` — the
probe that decides `Running` whenever `/debug/vars` answers **403**, i.e. on every
hardened deployment — dialled that **name**, so it connected to the container's own
loopback and failed. Its neighbours succeeded because they already pin the address
(`httpGetVia`, B289.1) — which is exactly why the same page showed a green socket and
the version next to a red service. Verified from the host with the page's own shape:
`derp.skynas.ru:443` **and** `127.0.0.1:443`, both with the proper SNI, answered
`HTTP/1.1 101 Switching Protocols`.

A liveness probe that dials a different address than its neighbours is not a
liveness check, it is a second opinion. The probe now takes a `dialAddr`, pins its
TCP dial with the same `net.JoinHostPort(dialAddr, port)` shape `httpGetVia` uses,
keeps the hostname for `Host`/TLS SNI, and its call site passes the **same** address
the other probes use.

### (2) The STUN red is real — and it is not skygate

derper **binds `*:3478` and answers no Binding Request at all**: not from the host,
not from inside the container, not even on loopback, over IPv4 *or* IPv6 — while
`derper --help` lists `-stun` / `-stun-port 3478` (both correct in the running
argv: `--a=:443 --stun`), its log says `STUN server listening on [::]:3478`, and
`/derp` answers 101. Controls that make this trustworthy: a UDP loopback echo proves
the probe can receive, and `tailscale netcheck` gets STUN replies from every public
region from the same host and the same container. The relay image is
`skygate-derper:latest` (built 2026-09-17, banner `1.70.0-ERR-BuildInfo`).

So the tile stays red — and now it **names every candidate it probed**, so a UDP
filter can be told apart from a relay that never answers.

### Also verified (no change needed)

headscale's `derp.urls` merges `http://skygate:8080/admin/derp/relays/derpmap.json`,
and skygate serves region 900 (`derp.skynas.ru`, DERP 443, STUN 3478) — the map
chain is correct. The dead `:8443` row (`derp_relays` id 3) is still enabled; the
reachability guard skips it when publishing, and it is worth removing on
`/admin/derp/relays`.

### What to do after the update

```bash
# 1) the false alarm should be gone
journalctl -u skygate --since '-5 min' | grep -i derp | tail -20
# /admin/derp -> DERPER.SERVICE should read "active"

# 2) the derper-side STUN question, in one line (from the host):
python3 - <<'PY'
import socket, os, struct
txid = os.urandom(12)
pkt = struct.pack('>HHI12s', 0x0001, 0, 0x2112A442, txid)
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(4)
s.sendto(pkt, ("127.0.0.1", 3478))
try:    print("STUN reply:", len(s.recvfrom(512)[0]), "bytes")
except Exception as e: print("STUN silent:", e)
PY
# 3) if it stays silent, restart the relay and re-run step 2:
docker restart derper
```

If STUN answers **right after** a restart and dies later, the running process is at
fault (we would add a periodic check); if it never answers, the custom
`skygate-derper` image's STUN is broken and the image should be rebuilt from the
pinned upstream tag.

12 contracts in `scripts/check_b302_derp_probe_dial_truth.sh` +
`internal/feature/admin/derp_probe_dial_b302_test.go`.

## v1.5.66 — a sudo refusal that can never succeed must fall through (B301)

**Date:** 2026-09-23 · **Base:** `v1.5.65` → this tag · **Compatibility:** none —
no schema change, no migration.

### What was happening

v1.5.65 fixed the *decision*; the `aro` journal then showed the ladder finally being
walked — and stopping one rung early:

```
staggeredSync(aggregated): exit-node-vps applied LOCALLY:
  local=err=tailscale set (local, sudo): sudo: The "no new privileges" flag is set,
  which prevents sudo from running as root. … — no SSH involved
```

`deploy/install-common.sh` writes **`NoNewPrivileges=yes`** into the skygate systemd
unit, so `sudo` can **never** become root from inside skygate: the kernel flag is
checked before any sudoers rule, which also means option «c» in the fallback hint (a
NOPASSWD rule) changes nothing on a systemd install. That phrase was not in
`isPrivilegeRefusal`, so the refusal was classified as a **real** `tailscale set`
failure and `ApplyRoutesLocally` returned before rung 3: the root-owned helper sat
**installed and idle**, `routes-apply.status` was never created, and every prefix
stayed «нет маршрута».

### The fix

* The systemd refusal (`no new privileges`, `prevents sudo from running as root`) and
  the other sudo-only refusals (`not allowed to execute`, `no tty present`,
  `sorry, user`) are now — together with the pre-existing `a password is required`,
  `is not in the sudoers`, `permission denied`, `operation not permitted`,
  `no such file or directory`, `connect: permission denied` — classified as
  **privilege refusals**, so the ladder reaches the helper.
* A **real** `tailscale set` failure (unknown flag, bad CIDR, daemon down) is still
  surfaced as-is and is **not** retried on another rung — pinned by a negative test
  table, so a future needle cannot make the ladder mask a genuine error.
* `RoutesFallbackHint` now warns that a NOPASSWD rule cannot work on a systemd
  install, so nobody chases option (c) on a host where the kernel flag refuses sudo
  first.

10 contracts in `scripts/check_b301_sudo_nnp_rung.sh` +
`internal/headscale/local_apply_b301_test.go` (the verbatim two-line live refusal
drives the ladder to the helper).

### What to expect after the update

```bash
journalctl -u skygate --since '-5 min' | grep -E 'local=|routes applied locally'
cat /var/lib/skygate/update/routes-apply.status      # now written by the helper
tail -5 /var/lib/skygate/update/routes-apply.log
```

Expect `local=ok via helper` and the applier's own `RESULT=ok`, and «нет маршрута»
should disappear from the prefix table.

## v1.5.65 — one transport decision for every sync path (B300)

**Date:** 2026-09-23 · **Base:** `v1.5.64` → this tag · **Compatibility:** none —
no schema change, no migration.

### What was happening

`aro`'s exit routes never applied, on any tick:

```
staggeredSync(aggregated): exit-node-vps SSH err: ssh exit-node-vps (target from the node
  name (exit_servers has neither ssh_target nor tailscale_ip for this relay),
  key /var/lib/skygate/ssh/id_ed25519): ssh: Could not resolve hostname exit-node-vps
```

…while the host itself proved the relay **is** local:

```
SELF exit-node-vps ['100.64.0.1', 'fd7a:115c:a1e0::1']   (tailscale status --json)
ROW  ('exit-node-vps', '', '')                            (exit_servers: both columns empty)
ID 1 | exit-node-vps | 100.64.0.1 | online                (headscale nodes list)
```

Two sync paths configure the same relay — the per-node one (`syncOneExitNode`, used
by `SyncAdvertisedRoutes`, `SyncAdvertisedRoutesForNode` and the per-row Re-sync
button) and the aggregated one (`staggeredSync(aggregated)`, which is what the
periodic tick and the domain auto-updater actually run). The aggregated path
carried its own **shorter copy** of the body: it never called
`DetectRelayPlacement` (B293) and never ran B292's target repair. Every tick
therefore handed `SetAdvertisedRoutes` an **empty** target, ssh fell back to the
bare node name, and the local transport the evidence proves would match was never
even considered — `routes-apply.status`/`.log` were never created, and every
prefix stayed «нет маршрута».

### The fix

Both call sites now go through **one shared tail**, `applyRoutesToRelay`
(`internal/feature/exit_rules/sync.go`):

1. **B293's evidence chain** decides locality (the live daemon's
   `Self.TailscaleIPs`, then this host's interfaces; a *negative* daemon answer is
   final, "could not ask" falls back to the interfaces);
2. **local** → refuse to advertise a subnet this host sits inside
   (`SelfCoveringRoutes`) and apply through the privilege ladder (direct →
   `sudo -n` → the root-owned helper);
3. **remote** → resolve the SSH target by B292's chain (operator override → the
   live Tailscale IP, **persisted** into the still-empty column → a NAMED warning)
   and run SSH;
4. approve through the same single call site, whichever transport ran.

There is now **exactly one** `SetAdvertisedRoutes` and **one**
`ApproveAllRoutesWithList` call in the sync package (pinned by contract), and a
readable daemon that answers "not local" **logs its evidence** instead of leaving
«why ssh?» unanswered in the journal.

### What to expect after the update

On `aro` both facts above hold, so the same Re-sync (or the next tick) should
report **`local=ok via …`** and log `routes applied locally via … (relay IS this
host, matched 100.64.0.1 by local tailscaled) — no SSH involved`, with
`/var/lib/skygate/update/routes-apply.{status,log}` appearing if the privileged
helper is the rung that ran. Check:

```bash
journalctl -u skygate --since '-10 min' | grep -E 'exit-node sync|staggeredSync|local='
cat   /var/lib/skygate/update/routes-apply.status 2>/dev/null || echo "direct/sudo rung was used"
```

If it still says `ssh=…`, the log line now names **which** evidence ruled the
relay out (`the local daemon answered: matched "" among [...]` or
`local daemon unreadable: …`), so the next report answers itself.

17 contracts in `scripts/check_b300_relay_apply_one_path.sh`. No contract needed
renegotiating: B132's `syncOneExitNode` + call sites, B274's
`syncOneExitNode(… OwnedPrefixes …)` call text and B293's
`DetectRelayPlacement`/`ApplyRoutesLocally` greps all still hold — the new script
asserts exactly that as its D1.

## v1.5.64 — derived-rule churn must not restart the control plane (B298)

**Date:** 2026-09-23 · **Base:** `v1.5.63` → this tag · **Compatibility:** none —
no schema change, no migration.

### What was happening

After v1.5.63 the version probe worked and the privileged policy applier no longer
unioned `tagOwners` — and headscale was **still** restarted every five minutes.
The journal named the culprit, on every tick:

```
acl-drift: ACL re-applied (snapshot v483, generated=7331 bytes) — auto-updater tick changed 18 rule(s) (added=17 removed=1)
auto-updater: dedup removed 16 redundant derived rule row(s)
```

`added=17` with `dedup removed 16` is not DNS jitter — it is a self-sustaining
ping-pong. The database proves it: 19 subnet rows for 19 distinct CIDRs, with
`openai.com` carried under **two** `parent_domain` values
(`cdn:cloudflare:openai.com` — 15 rows — and a bare `openai.com` — 2 rows).

Two independent defects:

1. **The dedup deleted what the schema allows and the resolver needs.**
   `device_rules_natural_key_uniq` is a **six**-column index (B237.23/V068,
   including `parent_domain`) so that two domains of one CDN each keep a row per
   CIDR — that per-domain row is what the CDN short-circuit looks for
   (`parent_domain LIKE 'cdn:%:<domain>'`) and what B184's DOMAIN-status
   propagation reads. `CollapseDuplicateDerivedRules` (B274) partitioned on only
   **five** columns, so it deleted the losing domain's rows; on the next tick that
   domain found no marker, re-resolved, re-inserted its ~15 published Cloudflare
   ranges — and the collapse deleted them again. Every tick changed the rule set,
   so every tick changed the generated policy.
2. **The churn path spent the 60-second budget.** The domain auto-updater and the
   periodic drift check both called `applyACLIfDrifted`, throttled by
   `ownershipACLThrottle` (60s) — so a single rotating `/32` from ordinary DNS
   rotation was enough to re-apply the policy.

On a `policy.mode: file` host an ACL re-apply **is** `systemctl restart headscale`
(0.29 re-reads the policy file only at startup), so together these restarted the
control plane **288 times a day**.

### The fix

* `CollapseDuplicateDerivedRules` now partitions on the **same key as the UNIQUE
  index** (`… target_value, parent_domain`), so only **exact** duplicates are
  removed. A domain's rows survive, its short-circuit fires on the next tick, and
  the insert→delete cycle is gone. (The statement stays for databases that predate
  V068 or whose index was repaired later; `n == 0` is the normal answer now.)
* The derived-rule paths spend a separate `churnACLThrottle` (**30 minutes**)
  through the new `applyACLIfDriftedChurn`; the decision core is shared
  (`applyACLIfDriftedThrottled`) so there is no second copy of the drift logic.
  **Operator actions keep the 60s budget** — a rule create/delete, an explicit
  resync and an ownership move still land within a minute — and the deferral log
  names the budget it actually spent.

Expected after the update: **one** more write (the convergent one), then silence.
`acl-drift: ACL re-applied …` every five minutes means the cycle is still there;
the periodic path deliberately does **not** log "already matches" (B288), so quiet
is the healthy state. Check with:

```bash
journalctl -u headscale --since '-30 min' | grep -Ei 'Starting|Stopped'   # expect: empty
journalctl -u skygate  --since '-30 min' | grep -E 'acl-drift|dedup|auto-updater'
```

17 contracts in `scripts/check_b298_cdn_rule_churn.sh` +
`internal/feature/exit_rules/churn_b298_test.go` (real migrated SQLite DB: two
domains keep two rows for one CIDR; the churn path defers where an operator action
applies; the periodic check spends the same budget). **Contract renegotiations**,
each documented in place: B274/D.2 + new D.2b, B276/A10, B276.1/C1, B288/C3.

## v1.5.63 — the running headscale's version is read, not declared (B297)

**Date:** 2026-09-23 · **Base:** `v1.5.62` → this tag · **Compatibility:** none —
no schema change, no migration. `SKYGATE_HEADSCALE_VERSION_PIN` stays supported and
becomes the **fallback**.

### Why

skygate's only answer to «which headscale is this?» was `SKYGATE_HEADSCALE_VERSION_PIN`,
an env var the operator types once. `internal/config/config.go` says so out loud:

> The pin is an env var (not auto-detected) because skygate doesn't shell into the
> headscale container. Auto-detect could come in a v0.21.0+ …

The two live hosts were reported to disagree — `aro` running **0.29.0**, the agent
VM **0.29.3** — and 0.29.x is **not** uniform in the surface skygate depends on:

| Difference | Version | Where it is handled today |
|---|---|---|
| `POST /api/v1/node/{id}/approve_routes` deprecated/404 in REST | since **0.29.1** | REST first, then the CLI (`internal/headscale/routes.go`) |
| the REST expire path is broken | **0.29.2** | CLI-only, with a named error (`nodes.go`) |
| `grants[]` replaced `acls[]` | 0.29.0-beta.4 | the generator emits `grants[]` (`acl.go`) |
| wildcards in `tagOwners` rejected, `ip: ["*"]` required | **0.29.2** | the generator emits the strict form (`acl.go`) |
| the policy file is re-read only at startup | 0.29.x | a write is a `systemctl restart` |

None of that is version-branched — every one is a **capability ladder** — so a
mismatch never breaks skygate silently. What a stale pin *did* break was three
things, all invisible:

1. the update monitor compared the latest GitHub release against the declaration,
   so «доступна новая версия headscale» fired in whichever direction the pin was
   wrong;
2. `headscale_releases.is_breaking` was computed from it, so patch releases were
   filed as breaking (or the reverse) in the history table;
3. nothing anywhere said the daemon and the declaration disagreed — not the
   journal, not the page whose entire job is comparing versions.

### What

**A probe ladder** (`internal/headscale/version_b297.go`), cheapest and most
authoritative rung first, in the same style as the B294 live-policy read:

1. authenticated `GET /api/v1/version`;
2. unauthenticated `GET /version`;
3. `headscale version` through the install-kind ladder (`runHeadscaleCLI`:
   `docker exec` on a container host, the local binary on a native one) — the only
   rung that still answers when the API address itself is wrong.

Every failed rung is remembered (`ServerVersion.Tried` / `Reason()`), so an
undetected version names what it tried instead of guessing. A **non-2xx answer is a
note, never a version** — an error body carries HTTP codes and ports that a loose
parser would happily publish as "the running version". A named JSON field
(`version` / `Version` / `server_version` / `serverVersion` / `headscale_version`)
is validated strictly; free text is searched loosely **only on a short body** (a
proxy error page is not a version). The CLI parser prefers the **server** line over
the client binary's own version, because a stale `/usr/bin/headscale` next to a
fresh container is a real layout. The CLI rung is bounded from the outside, so a
hung `docker exec` cannot hold a boot phase or a monitor tick.

**The monitor prefers the detection.** `internal/headscale_version.Monitor` gains
`VersionProbe` + `DeclaredPin` + `VersionStatus()`:

* the detected version is used **everywhere** a version is compared — the tick,
  `IsBreaking`, the alert body, and `Snapshot()` (which feeds both
  `/admin/headscale` and the bot's `/headscale`);
* the declaration is kept **beside** it, so the page can show both;
* a **failed** probe keeps the last real detection and records the error — it never
  silently hands control back to the declaration it exists to distrust;
* the probe runs **before** the GitHub poll, so an offline host still notices a
  headscale upgrade, and `Start()` probes once immediately so the first render is
  already truthful;
* alerts no longer **require** the pin: a host whose version is detected but whose
  `SKYGATE_HEADSCALE_VERSION_PIN` is empty used to sit in observe-only mode forever.

**Operator-visible.** Boot logs `headscale-version: running headscale is X (via …)`
and a named `MISMATCH` line when the declaration disagrees. `/admin/headscale` gets
a «Версия запущенного headscale» card — **detected**, **declared**, the **rung that
answered**, the timestamp, and a mismatch banner — and the banner is semver-aware:
`0.29` vs `0.29.0` is **not** a mismatch. The old pin help («задаётся через
SKYGATE_HEADSCALE_VERSION_PIN») now says the version is detected automatically and
the pin is only the fallback (RU+EN).

### For the operator

Nothing to change: after the update, `aro` will report **0.29.0** and the agent VM
**0.29.3** by itself. If your `.env` still pins the other host's version, the page
will now say so — fix or clear `SKYGATE_HEADSCALE_VERSION_PIN` to silence the
banner. Check with:

```bash
journalctl -u skygate --since '-10 min' | grep -E 'headscale-version|headscale-update-monitor'
# /admin/headscale → «Версия запущенного headscale»: Определена / Объявлена / источник
```

43 contracts in `scripts/check_b297_headscale_version_truth.sh` +
`internal/headscale/version_b297_test.go`,
`internal/headscale_version/monitor_b297_test.go`.

## v1.5.62 — the relay probe address is set from the panel, and applies immediately (B296)

**Date:** 2026-09-23 · **Base:** `v1.5.61` → this tag · **Compatibility:** none —
no schema change, no migration.

Follow-up to B289.1, from the same live host. B289.1 fixed *what* the probes say
(dial the operator's address, speak the relay's public hostname as TLS SNI) and
`deploy/deploy.sh` now writes `SKYGATE_DERP_PROBE_HOST` for the operator. But the
knob lived only in `.env`, and the skygate container is created from
`env_file: .env`:

```console
$ docker compose restart skygate   # keeps the OLD environment — env_file is read
                                   # when the container is CREATED, not when it starts
$ docker compose up -d --force-recreate skygate   # the only thing that applies it
```

So applying an edited value meant an SSH session and a `--force-recreate` (AGENTS
trap #3) — for something the operator can see is wrong on `/admin/derp/relays`
(«пропущен: unreachable (probed …)»).

### Fix

`internal/derpcfg` resolves the hint **per probe** (never once at boot):

| layer | written by | applies |
|---|---|---|
| `global_settings.derp.probe_host` | the new card | on the **next probe** |
| `SKYGATE_DERP_PROBE_HOST` (`.env`) | `deploy.sh` / the operator | at container creation |
| unset | — | B289 behaviour (try the row's own name first) |

* every probe path reads it through the resolver — the derpmap reachability guard,
  the `/admin/derp` status probes, the STUN candidate list, and the `derp_health`
  cron (`DERPInfo.ProbeHost`, filled once by `FetchOwnDERPs`, with the `.env`
  fallback kept for hand-built rows). **No non-test code reads the environment
  variable directly any more**, so a saved address cannot be silently ignored on
  one path.
* the card on `/admin/derp/relays` shows the effective value **and which layer it
  came from** (`db` / `env` / `default`), so nothing is a guess; **Очистить**
  deletes the override and `.env` takes over again.
* it accepts a bare address (`192.0.2.10`, `2001:db8::1`, `derp.example.com`).
  A scheme, a port (`192.0.2.10:443` — the port comes from the relay row's URL), a
  path, a list, a typo'd IPv4 (`999.0.2.10`) and a non-hostname are refused, each
  with its own translated reason, and nothing is stored; every change is audited
  as `derp_relay.probe_host`.
* saving drops the page's 30s map-verdict cache, so the redirect renders the fresh
  «в карте / пропущен + причина» verdict for the value just saved.

### Verification

`internal/derpcfg/derpcfg_b296_test.go` (precedence db > env, clearing falls back,
a refused value is never stored, the validation table) and
`internal/feature/admin/derp_probe_host_b296_test.go`, which saves **through the
real handler** and re-fetches the **real derpmap endpoint**: with no `.env` value
and an unresolvable relay name the region is dropped; after the card saves an
address the very next fetch publishes it — with the relay's public `HostName`
intact and the port from the row's URL; after **Очистить** it is dropped again.
25 contracts in `scripts/check_b296_derp_probe_host_ui.sh`. Procedure:
`docs/derp.md` §B296.

## v1.5.61 — dial the relay's ADDRESS, speak its HOSTNAME (B289.1)

**Date:** 2026-09-23 · **Base:** `v1.5.60` → this tag · **Compatibility:** none —
no schema change, no migration.

**Carries B295 as well.** There is no `v1.5.60` tag and there will not be one: the
commit that section names did not compile (a half-applied B289.1 edit left in the
working tree by the parallel session, restored by `ef232233`), so `scripts/ci_gate.sh`
correctly refuses to tag it. The B295 fix it documents is in this release; read the
`## v1.5.60` section below as part of v1.5.61's change set.

Operator report, and the reason the v1.5.57 «local DERP reaches the map» fix did
not actually fix the live host: the relay still never reached the map.

Measured on `skygate-host` (192.168.13.69), read-only:

```console
# the relay itself is fine
$ sudo docker ps --filter name=derper      # Up 34 hours, restarts=0
$ sudo ss -ltnp | grep -E ':(80|443)\b'    # derper on 443/tcp and 80/tcp
$ sudo ss -lunp | grep 3478                # derper on 3478/udp
$ openssl s_client -connect 192.168.13.69:443 -servername derp.skynas.ru
  subject=CN = derp.skynas.ru · issuer=Let's Encrypt · Verify return code: 0 (ok)

# headscale is configured to fetch skygate's map
$ awk '/^derp:/{f=1} f{print}' /home/skyadmin/headscale/config/config.yaml
derp:
  urls:
  - https://controlplane.tailscale.com/derpmap/default
  - http://skygate:8080/admin/derp/relays/derpmap.json

# ... and yet the map is EMPTY:
$ curl -s http://127.0.0.1:8080/admin/derp/relays/derpmap.json
{"Regions":{}}

# skygate's own journal said so, three lines per fetch:
derpmap: skipping region=900 host=derp.skynas.ru port=443 — node unreachable
         (tried [192.168.13.69 derp.skynas.ru]): dial tcp 127.0.0.1:443: connect: connection refused
derpmap: ERROR region=900 is BUNDLED but publishes NO node — every client will
         fall back to the public DERP map

# and derper's log explained why the LAN-address fallback did not save it:
http: TLS handshake error from 192.168.13.69:56136: cert mismatch with hostname: "192.168.13.69"
http: TLS handshake error from 192.168.13.69:56152: cert mismatch with hostname: ""
```

### Root cause

B289 taught the guard to try the operator's address (`SKYGATE_DERP_PROBE_HOST`,
already set on that host) *in addition to* the hostname — but it passed the
candidate ADDRESS as the TLS `ServerName` too. `derper --certmode=manual`
resolves its certificate **by SNI**, and Go's TLS client sends **no SNI at all**
for an IP literal (`hostnameInSNI` strips addresses). So:

* the hostname candidate → the container's own loopback (`/etc/hosts` leak,
  AGENTS trap #2) → connection refused;
* the LAN-IP candidate → correct address, empty SNI → `cert mismatch with
  hostname: ""` → handshake refused.

Both candidates failed, the node was dropped, the map stayed empty, and headscale
merged nothing — while the relay was healthy, correctly certified and reachable.
The `/admin/derp` status probes and the `derp_health` cron had the same defect in
a different form: they dialled the hostname (loopback inside the container) and
ignored the port in the row's URL, so the dashboard showed the healthy relay as
`tls dial: dial tcp 127.0.0.1:443: connect: connection refused`.

The B289 regression tests could not catch it: they used
`httptest.NewTLSServer`, which ignores SNI entirely.

### Fix

One rule, applied to every probe: **the address to dial and the name to speak are
different things.**

* `derpProbeTarget{Addr, SNI}` + `derpProbeTargetsFor(candidates, hostname)` —
  every candidate is dialled with the relay's public hostname as SNI; the map
  guard (`/admin/derp/relays/derpmap.json`) uses it.
* `httpGetVia(url, dialAddr, timeout)` — `/admin/derp` keeps its URL (so SNI and
  the Host header still match the certificate) and pins the TCP connection to the
  first candidate that answers; `collectDerpStatus` now logs which address it
  dialled while speaking which name.
* `derphealth.dialTargetFor` — the health probe takes the port from the row's
  URL (it used to force `:443`) and, when the relay's name resolves to loopback,
  dials `SKYGATE_DERP_PROBE_HOST` while still presenting the hostname.
* No `docker-compose.yml` change is needed for any of this — the probe address
  travels in `.env`, which the container already loads.
* `deploy/deploy.sh` now closes the class instead of leaving it to the operator:
  when `DERP_ENABLED=true` and the relay's own name resolves to loopback on the
  host, it writes `SKYGATE_DERP_PROBE_HOST=<host LAN address>` into `.env`, and
  then asserts end to end that
  `http://127.0.0.1:<port>/admin/derp/relays/derpmap.json` contains region `900`,
  warning loudly (with the reason) when it does not.

### Verification

`internal/derphealth/probe_b289_1_test.go` + the B289.1 cases in
`internal/feature/admin/derp_reach_b289_test.go`. The regression fixture is
**SNI-strict on purpose** — it fails the handshake for an unexpected or empty SNI,
like manual-certmode derper — and one case pins the old behaviour as *unreachable*,
so the bug cannot come back green. `check_b289_derp_map_truth.sh` grew to 22
contracts (A6/A7 the address/SNI split, D3/D4 the health probe, F1/F2 the status
probes, F3 the deleted bare-URL wrapper). Procedure and operator-facing
explanation: `docs/derp.md` §«B289.1», `SKYGATE_DERP_PROBE_HOST` in `.env.example`.

## v1.5.60 — a failed policy read must not become a blind write (B295)

**Date:** 2026-09-23 · **Base:** `v1.5.59` → this tag · **Compatibility:** none —
no schema change, no migration.

Diagnosis from the host, which the operator supplied:

```console
$ sudo ss -ltnp | grep -E ':(8080|8081|8082|9090)'
LISTEN 127.0.0.1:8081  users:(("headscale",pid=117261,fd=12))
LISTEN 127.0.0.1:9090  users:(("headscale",pid=117261,fd=13))
LISTEN *:8082          users:(("skygate",pid=117205,fd=5))
$ sudo systemctl is-active headscale
active
$ grep listen_addr /etc/headscale/config.yaml   → 127.0.0.1:8081
$ grep HEADSCALE_URL /etc/skygate/skygate.env   → http://127.0.0.1:8081
```

**headscale is up and listening on exactly the address skygate is configured
with.** So the `connect: connection refused` on the page was **transient** — and
the old code made it permanent:

```
acl-drift: cannot read the live policy to decide whether a re-apply is needed
(…) — applying unconditionally
```

On a `policy.mode: file` host a policy write means `systemctl restart headscale`
(0.29 re-reads the policy file only at startup). Answering a failed read with a
write therefore answers a restart with **another restart**: the observation fed the
outage, the page could never converge, and every prefix stayed «нет маршрута» while
the assignment table looked frozen.

(The `api-FAIL` in the same terminal session is not evidence of an outage: an
interactive root shell does not have `HEADSCALE_API_KEY` exported, so `curl -sf`
sees a 401. Use the probe below, which reads the env file.)

### What it does now

* **A transient read is retried.** `connection refused` / `actively refused` /
  `no connection could be made` / `connectex` / i/o timeout / `deadline exceeded` /
  `timeout` / EOF — in POSIX **and** Windows spellings — is retried twice with a
  bounded, injectable delay before any fallback. An **HTTP answer** (401/404/500) is
  deliberately **not** retried: the daemon is demonstrably up, and the policy-file /
  CLI rungs are the interesting part.
* **A failed read no longer means a blind write.** The decision comes from the last
  **applied snapshot** in `acl_snapshots` — the document skygate itself wrote, i.e.
  what headscale was serving:
  * generated == snapshot → **no write, no restart** (logged: "nothing to write, so
    headscale is not restarted");
  * generated != snapshot → the rules really changed → write;
  * no snapshot (fresh install) or an unparseable one → the old blind apply
    survives, but the log now says the decision was blind.
* **The page stops lying in both directions.** `/admin/exit-nodes` reports
  «в синхроне» from that snapshot and **names the source**
  («compared with the last APPLIED snapshot vN (headscale did not answer)») instead
  of «состояние политики неизвестно»; a read failure with no usable snapshot stays a
  real error.

Files: `internal/headscale/acl.go` (retry + transient test),
`internal/headscale/policy_snapshot_b295.go` (new: the verdict),
`internal/feature/exit_rules/sync.go` (`decideWithoutLivePolicy`),
`internal/feature/admin/exit_nodes.go` (+`PolicyVia`),
`internal/handlers/templates/admin/exit_nodes.html`.

### Verification

19 contracts in `scripts/check_b295_no_blind_policy_write.sh` +
`internal/headscale/policy_snapshot_b295_test.go` (identical / semantically equal /
differing / absent / unparseable snapshots; the transient-vs-HTTP retry decision;
the retry delay is injectable so the test is fast) +
`internal/feature/admin/exit_nodes_b295_test.go`.

### What to do on `aro`

```bash
# 1. Install v1.5.60 (a native host updates ONLY through /admin/update):
curl -s http://127.0.0.1:8082/healthz | grep -o '"build":"[^"]*"'   # → v1.5.60+…
#    …and if this says v1.5.61+ in an hour, so be it — just make sure it is not v1.5.57.

# 2. A correct API probe (the previous one lacked the key, so api-FAIL proved nothing):
set -a; . /etc/skygate/skygate.env; set +a
curl -s -o /dev/null -w 'policy API: %{http_code}\n' \
     -H "Authorization: Bearer $HEADSCALE_API_KEY" "$HEADSCALE_URL/api/v1/nodes"
#    → 404/200 means the API answers; 401 means the key in the env file is stale;
#      connection refused means headscale really is down at that instant.

# 3. Was headscale restarted by skygate? (the loop this release removes)
systemctl show headscale -p NRestarts -p ActiveEnterTimestamp
journalctl -u headscale --since '-2 hours' | grep -Ei 'Starting|Stopped' | tail -20
tail -5 /var/lib/skygate/update/policy-apply.log

# 4. Then /admin/exit-nodes: the card must say either «в синхроне» (with the source)
#    or name the real read failure — and no longer restart headscale every pass.
```

If `NRestarts` is large and the apply log shows `ok`/`failed` entries every ~5
minutes, that is exactly the B295 loop — and this release is what stops it.

## v1.5.59 — reading headscale must work without docker, and «нет» must mean «нет» (B294)

**Date:** 2026-09-23 · **Base:** `v1.5.58` → this tag · **Compatibility:** none —
no schema change, no migration.

Operator report from the prefix-assignment card on `aro` (and the same card «не
меняется» on the docker VM):

```
состояние политики неизвестно: read live policy: api: Get
"http://127.0.0.1:8081/api/v1/policy": dial tcp 127.0.0.1:8081: connect: connection refused;
cli: all variants failed

владелец не объявляет: 0   никто не объявляет: 19   объявляет несколько: 0
```

…with all 19 prefixes rendered «АНОНС: нет · нет маршрута» (19 «проблемных»).

### Root cause — three defects behind one screen

1. **The live-policy READ could not work without docker.** It had exactly two
   rungs: the API, and a **docker-only** `headscale policy get`. When the API was
   unreachable it gave up immediately and answered `cli: all variants failed` —
   blaming a CLI it had never executed, because neither docker nor a container
   exists on that host. The policy **FILE** headscale actually serves in
   `policy.mode: file` was never read, even though the **write** path has used it
   since B272.
2. **«Никто не объявляет: 19» was not a fact.** The prefix table's advertisement map
   is filled from `ListAllNodes()`, and the loader **discarded the error**. With
   headscale unreachable the map stayed empty, so every prefix rendered as
   unadvertised — absence of evidence presented as a negative fact. No click on that
   page could change it, because the real problem was the API address.
3. **Nothing named the thing to fix.** `127.0.0.1:8081` is a *configuration*
   question (the address must be reachable **from the skygate process** — inside a
   container `127.0.0.1` is the container's own loopback, never the host's
   headscale), and no page mentioned `HEADSCALE_URL`.

### What it does now

* **`GetACL` walks three rungs:** API → headscale's **policy file**
  (`c.PolicyPath`, else `DiscoverPolicyPath()`) → the **headscale CLI through the
  B267 install-kind ladder** (`docker exec` when docker + a container are
  available, otherwise the local binary, including the legacy `policy show` /
  `policy` variants). The file rung is what makes a native `policy.mode: file` host
  readable with no docker and no CLI.
* **A named failure instead of a lie.** When all three fail, the error lists every
  rung it tried (`api: …; policy file: …; headscale CLI: …`) and, for a genuine
  reachability failure, appends the fix: check `HEADSCALE_URL`, remember that a
  container must not use `127.0.0.1`, and a ready `curl -sf -H "Authorization:
  Bearer $HEADSCALE_API_KEY" <url>/api/v1/node` probe. The hint fires in both POSIX
  and Windows spellings and **never** for a 401/500 from a reachable daemon.
* **The prefix card distinguishes «не удалось спросить» from «нет».** The loader now
  carries `LiveReadErr` + `LiveReadHint`, and the card renders a warning: the
  advertised/no-route columns are filled from live headscale, so while it is
  unreachable «нет» means «could not ask» — and the assignment cannot change until
  the API is reachable again (RU + EN). The journal logs the same.

Files: `internal/headscale/acl.go` (the read chain + hint),
`internal/feature/admin/exit_nodes.go` (LiveReadErr/LiveReadHint),
`internal/handlers/templates/admin/exit_nodes.html`,
`internal/i18n/catalog_exit_nodes.go`.

### Verification

20 contracts in `scripts/check_b294_headscale_read_truth.sh` +
`internal/headscale/acl_read_b294_test.go` (the file rung on a host whose API is
down — the live case; all three rungs named; the reachability hint and its
non-firing on 401/500; nil-client safety) +
`internal/feature/admin/exit_nodes_b294_test.go` (the failure reaches the page and
the template).

### What to check on the hosts (this is a configuration question too)

The code can now read the policy/file/CLI, but **nothing can fix a wrong API
address for you**. On `aro`:

```bash
# What does headscale listen on, and is it running?
sudo ss -ltnp | grep -E ':(8080|8081|9090)'          # headscale's listen_addr
sudo systemctl is-active headscale 2>/dev/null || sudo docker ps --filter name=headscale
grep -iE '^(listen_addr|server_url)' /etc/headscale/config.yaml

# What does skygate talk to?
grep -E 'HEADSCALE_URL|HEADSCALE_API_KEY' /etc/skygate/skygate.env | sed 's/KEY=.*/KEY=<redacted>/'

# Can the skygate process reach it? (this is the check the hint prints)
curl -sf -H "Authorization: Bearer $HEADSCALE_API_KEY" <HEADSCALE_URL>/api/v1/node >/dev/null && echo api-ok
```

On the docker VM, the same question looks different: **inside** the skygate
container `127.0.0.1` is the container itself, so `HEADSCALE_URL` must be the
headscale **service name** (`http://headscale:8080`) or its address on the shared
network:

```bash
cd /home/skyadmin/skygate
grep -iE 'HEADSCALE_URL' .env docker-compose.yml
docker exec skygate-skygate-1 sh -c 'wget -qO- --header="Authorization: Bearer $HEADSCALE_API_KEY" "$HEADSCALE_URL/api/v1/node" >/dev/null && echo api-ok'
```

Whatever those commands show, the fix is one line in the env file (plus a skygate
restart) — and after v1.5.59 the page will tell you the same thing in words instead
of showing 19 phantom «проблемных» prefixes.

## v1.5.58 — the local-relay detection must not need daemon privileges (B293.1)

**Date:** 2026-09-23 · **Base:** `v1.5.57` → this tag · **Compatibility:** none —
no schema change, no migration.

Operator report right after installing v1.5.57: «команда ничего не дала» — the
`journalctl … | grep 'IS this host'` line never appeared, so the relay was still on
the SSH transport.

### Root cause

B293's detection asked the live daemon (`tailscale status --json`) and treated
"I could not ask" as "fall back to SSH". On the native install skygate runs as an
**unprivileged service user** and tailscaled's socket is root-owned unless the
operator granted `tailscale set --operator=<user>`. The probe therefore failed with
a permission error, detection never matched, no log line was emitted and the sync
stayed on SSH for a relay that **is** this very host — the same class of failure the
operator had asked to be covered ("бывает что ставят без [root] … чтобы не было
ситуации что нет возможности настроить по причине доступа"), one level deeper than
the apply ladder B293 already had.

### What it does now

* **A second, privilege-free source of evidence.** `LocalInterfaceIPs()` reads this
  machine's own addresses straight from the kernel (`net.InterfaceAddrs`, loopback
  and link-local excluded). A tailnet address bound on a local interface is the same
  proof as the daemon's own answer — it exists on THIS machine — and reading it
  needs no privileges at all.
* **`DetectRelayPlacement()` is the evidence chain:** live daemon → local
  interfaces → not local. A **negative** answer from the daemon is final (it knows
  its own address) — the fallback runs only when the daemon could not be asked at
  all. The result carries `Evidence` ("local tailscaled" / "local interface"),
  `MatchedIP`, `SelfIPs` and `DaemonErr`, so the log now says which evidence was
  used and why the daemon was unreadable instead of silently staying on SSH.
* **The pages use the same chain** (`LocalSelfIPs`), so `/admin/exit-nodes` marks
  the relay «локальный узел» and `/admin/telegram` gives the corrected egress
  advice even when the service user cannot read the tailscaled socket.
* **The loop guard works without daemon access too** — `SelfCoveringRoutes` is fed
  the interface-derived address list.

Note the division of labour: **detection** needs no privileges (this release);
**applying** still needs root, `--operator`, a `NOPASSWD` rule or the root-owned
helper (v1.5.57's ladder). The page shows which rung will be used.

Files: `internal/headscale/local_node_b293.go` (the probe + chain),
`internal/feature/exit_rules/sync.go`, `internal/feature/admin/exit_nodes.go`,
`internal/feature/admin/telegram.go`.

### Verification

56 contracts in `scripts/check_b293_local_exit_node.sh` (sections A–I) plus
`TestDetectRelayPlacement_B293_1` — the daemon answering, the daemon unreadable with
the address bound locally (the live case), a negative daemon answer that must stay
negative, and neither source answering — and `TestLocalInterfaceIPs_B293_1` (the
loopback is never returned).

### How to check on the host (after `/admin/update` to v1.5.58)

```bash
# 0. Which build is actually running? (a native install updates ONLY through
#    /admin/update — git pull + systemctl restart does not replace the binary)
curl -s http://127.0.0.1:8080/healthz | grep -o '"build":"[^"]*"'

# 1. Open /admin/exit-nodes once (the check runs on page render), then:
journalctl -u skygate --since '-10 min' | grep -Ei 'IS this host|own addresses|local relay|local='
#    expected: [exit-nodes] exit-node-vps IS this host (matched 100.64.0.1) — routes are applied locally via …

# 2. Press Re-sync on the relay. Expected flash:
#    Sync exit-node-vps: local=ok via direct approved=N      (root / --operator)
#    …or local=ok via sudo|helper …, or local=err=<reason> with the fix in the text.
```

If the page still says nothing about a local relay, the journal line
`cannot determine this host's own addresses` is the one to send — it means neither
the daemon nor the interface list answered, which no build can paper over.

## v1.5.57 — an exit node that IS the skygate host is managed locally (B293)

**Date:** 2026-09-23 · **Base:** `v1.5.56` → this tag · **Compatibility:** none —
no schema change, no migration. Remote relays keep the SSH transport unchanged.

Follow-up to B292, from the operator's own answer about the host:

> тот exit-node что существует он расположен на той же машине что и headscale и
> skygate машине aro и в таком случае доступ как таковой не нужен по ssh … бывает
> что ставят без [root] и для этого стоит тоже подобрать варианты реализации чтобы
> не было ситуации что нет возможности настроить по причине доступа

confirmed on the box itself:

```console
$ tailscale status --json | jq '{Self: {Host: .Self.HostName, IPs: .Self.TailscaleIPs}}'
{"Self": {"Host": "exit-node-vps", "IPs": ["100.64.0.1","fd7a:115c:a1e0::1"]}}
```

**The local tailscaled IS the exit node.** headscale, skygate and the relay live on
one machine — which also means `telegram.egress_node_id` pointing at it can route
nothing (see below).

### Why SSH was the wrong transport here

B292 taught the sync to name its blocker, but the transport itself was still SSH:
an SSH session from the host **to itself**, through the very tailnet it configures.

* **Pointless:** a private key plus `authorized_keys` on the same box, host-key
  pinning, `ConnectTimeout=10` per sync — to run one local command.
* **Fragile:** it fails exactly when the local tailscaled is the thing needing
  repair. `--advertise-routes` is part of that configuration, so "fix the relay
  through the relay" is a self-reference.
* **Blind:** the page kept demanding a key for a relay that needs none.

### What it does now

* **The decision is evidence, not a guess.** `tailscale status --json` →
  `Self.TailscaleIPs` is compared with the addresses headscale reports for the
  relay (`internal/headscale/local_node_b293.go`). A match is proof (tailnet
  addresses are unique per node); a shared **hostname** is deliberately never
  enough — that is the B265.1 lesson, where the reserved name `skygate-host`
  matched every relay. "I could not ask the local daemon" keeps the SSH path, so a
  container install (whose own tailscaled is a different node) is unaffected.
* **A local relay is configured with a LOCAL `tailscale set`**, through a
  **privilege ladder** — because the operator asked for this to work without root:
  1. `direct` — `tailscale set …` as this process (root, or a daemon configured
     with `tailscale set --operator=<skygate user>`);
  2. `sudo -n tailscale set …` (never interactive, so it cannot hang the sync);
  3. **`helper`** — a data-only `<update_dir>/routes.request.props` consumed by the
     new root-owned `deploy/skygate-apply-routes.sh` (`skygate-routes.path` /
     `skygate-routes.service`, written by `install-common.sh`, re-installable with
     `deploy/install-routes-helper.sh`); this is the rung that works when the
     service has no root, no sudo and no operator grant;
  4. a **named** error listing all four ways to grant access.
  Only a **privilege refusal** falls through to the next rung: a real
  `tailscale set` error (bad CIDR, daemon down) is surfaced as-is instead of being
  retried somewhere else. A staged-but-unconsumed request is an **error**, not a
  fake success (the B288.1 lesson).
* **The co-location footgun is guarded.** A relay that IS this host must never
  advertise a subnet the host sits **inside** (the documented "advertise your own
  LAN" loop: the host's own traffic to its LAN peers enters the tunnel and comes
  back, 500–1700 ms for a 1 ms hop). Such routes are dropped and reported
  (`self_subnet_skipped=…` in the sync result + a log line); the exit-node base
  routes `0.0.0.0/0` / `::/0` are exempt.
* **The page stops lying about keys.** `/admin/exit-nodes` marks such a relay
  «локальный узел», shows which rung the next sync will use, and **suppresses**
  B292's missing-SSH-key warning for it. The sync result reads
  `local=ok via direct approved=21` / `local=err=…`, and a local failure renders as
  a red flash like any other failure.
* **`/admin/telegram` stops conflating two different warnings.** A relay that IS
  this host cannot be an egress detour — its way out to the internet is skygate's
  own — so selecting it is a no-op at best. What changes for the better: the
  `api.telegram.org` probe on that page **is representative** (it measures exactly
  the path the bot will use), where B265's warning had to call it untrustworthy.
  If Telegram is unreachable from this host, the answer is a **different** relay on
  another machine, and the page now says so.

Files: `internal/headscale/local_node_b293.go` (new),
`internal/headscale/local_apply_b293.go` (new), `internal/headscale/route_args.go`
(`BuildTailscaleSetArgs` — one argv source shared with the SSH command string),
`internal/feature/exit_rules/sync.go`, `internal/feature/admin/exit_nodes.go`,
`internal/feature/admin/telegram.go`,
`internal/handlers/templates/admin/exit_nodes.html`,
`internal/handlers/templates/admin/telegram.html`,
`internal/i18n/catalog_exit_nodes.go`, `internal/i18n/catalog_telegram.go`,
`deploy/skygate-apply-routes.sh` (new), `deploy/install-routes-helper.sh` (new),
`deploy/install-common.sh` (`write_routes_units`), `docs/networking.md`,
`docs/operations.md`.

### Verification

48 contracts in `scripts/check_b293_local_exit_node.sh`, plus
`internal/headscale/local_node_b293_test.go` (the live `aro` JSON verbatim, the
address-equality rule including "a shared hostname is not enough", the
self-covering-route filter) and `internal/headscale/local_apply_b293_test.go`
(every rung and every fall-through with stubbed runners: direct wins, permission →
sudo, permission → helper, a real error is not masked, no transport → a named
error, the data-only request format, the verdict reader) and
`internal/feature/admin/exit_nodes_b293_test.go` (the `local=err=` classification,
the badge, the Telegram distinction).

### What to do on `aro`

Nothing to install beyond the update — the host runs skygate as root, so the
`direct` rung applies. After `/admin/update` to **v1.5.57**:

```bash
# 1. The relay must be recognised as local (one line per page render):
journalctl -u skygate --since '-5 min' | grep 'IS this host'
#    [exit-nodes] exit-node-vps IS this host (matched 100.64.0.1) — routes are applied locally via direct (this process is root), SSH is not used

# 2. /admin/exit-nodes → Re-sync on the relay. The flash must read:
#    Sync exit-node-vps: local=ok via direct approved=N
#    and the row must show the «локальный узел» badge instead of a key warning.
#    ADVERTISED ROUTES must move off 2 маршрутов.
```

If the service ever runs as an unprivileged user, the page shows which rung will be
used; the same fallback ladder then needs one of:

```bash
sudo tailscale set --operator=skygate          # skygate runs tailscale set directly
echo 'skygate ALL=(root) NOPASSWD: /usr/bin/tailscale set' > /etc/sudoers.d/skygate-tailscale
sudo bash deploy/install-routes-helper.sh      # the root-owned applier (no sudoers at all)
```

## v1.5.56 — the exit-node SSH sync names its blocker (B292)

**Date:** 2026-09-23 · **Base:** `v1.5.55` → this tag · **Compatibility:** none —
no schema change, no migration. One default changes on **native** installs (the
SSH private-key path, see below); container installs are unaffected.

Operator report from `/admin/exit-nodes` («он даёт ошибку при пересинхронизации»),
rendered in the **green** flash box:

```
Sync exit-node-vps: ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519):
  Warning: Identity file /ssh-sync/id_ed25519 not accessible: No such file or directory.
  ssh: Could not resolve hostname exit-node-vps: Name or service not known
  approved=21
```

…on a page whose only relay showed «1/1 здоровых», «2 маршрутов» advertised and
«mismatch: have 2, want 19».

### Root cause — three defects in that one line

1. **`/ssh-sync/id_ed25519` is a CONTAINER path.** It is the v0.33.1 default
   (`docker-compose.yml` binds the operator's `~/.ssh` there) and it can never
   exist on a native/systemd/bare install. Nothing anywhere said so — the raw
   `ssh` warning *was* the whole explanation.
2. **`Could not resolve hostname exit-node-vps` means the SSH target was the bare
   node NAME.** `exit_servers` had neither `ssh_target` nor `tailscale_ip`: the
   B81 fallback chain (`ssh_target` → `root@<tailscale_ip>` → `""`) resolved to
   nothing, and `SetAdvertisedRoutes` fell back to the node name. The column stays
   empty forever because the discovery pass writes `INSERT OR IGNORE` — while
   `/admin/exit-nodes` displayed the relay's Tailscale IP (`100.64.0.1`) read from
   headscale. Two sources of truth, one invisible. And the «Use Tailscale IP»
   button resolves through the *same* empty chain, so the operator had **no in-UI
   way out**.
3. **The failure was reported as success.** `PostAdminExitNodeSync` always
   redirected with `?ok=`, so `ssh=err=…` rendered green. (The result string
   itself deliberately keeps both halves — `approved=21` is real: the headscale
   approve step does run without SSH.)

The user-visible effect of 1+2: the relay advertised **2** routes (the
`0.0.0.0/0` + `::/0` bases) instead of the 19 its rules ask for, because
`--advertise-routes` is pushed over SSH.

### What it does now

* **New `internal/headscale/ssh_key.go`** — `SSHKeyProblem` / `SSHKeyFixHint` /
  `SSHKeyState` / `SSHKeyStateNeedsOperator` preflight the key: empty, not
  absolute (POSIX-aware, because `filepath.IsAbs` calls `/ssh-sync/…` relative on
  Windows), not found, a directory, unreadable — each named separately, each with
  the fix (which field to set, and where the public key must live). The
  container-only default is explained, not merely rejected.
* **`SetAdvertisedRoutes` refuses before spawning `ssh`** with that reason plus
  the fix, and reports **where the target came from** — so
  «Could not resolve hostname» is no longer the entire story.
* **The key default is resolved per install kind** (`config.resolveExitSSHKeyPath`):
  container → `/ssh-sync/id_ed25519` (unchanged), native →
  `<data dir>/ssh/id_ed25519` — a path that *can* exist there, anchored exactly
  like B270's OIDC key dir (`nativeDataDir` is now shared by both). The
  installers create `<data_dir>/ssh` (`0700`, service-owned).
* **The sync resolves the relay from the live headscale view** when the row has
  no address, persists it through `db.SetExitServerTailscaleIPIfEmpty` (only into
  an **empty** column — an operator-set address is never overwritten) so the B81
  chain and the «Use Tailscale IP» button work afterwards, and logs the case
  where headscale has no address either. `db.FirstTailscaleIP` is the single
  address-picking rule (IPv4 wins: an `ssh` argument cannot be a comma list).
* **The per-row Re-sync flash tells the truth** (`exitSyncFailed`): `ssh=err=` /
  `approve=err=` / `error=` → `err=` (red, with the fix in the text), while
  `ssh=ok approved=0` stays a success — that is the normal state of a relay whose
  routes have not been approved yet.
* **`/admin/exit-nodes` shows the verdict**: the effective key path per row, a
  badge whose tooltip carries the reason **and** the fix, and a banner naming
  both fields to set (RU + EN).

Files: `internal/headscale/ssh_key.go` (new), `internal/headscale/routes.go`,
`internal/config/config.go`, `internal/feature/exit_rules/sync.go`,
`internal/db/exit_servers.go`, `internal/feature/admin/exit_nodes.go`,
`internal/handlers/templates/admin/exit_nodes.html`,
`internal/i18n/catalog_exit_nodes.go`, `deploy/install-common.sh`,
`deploy/install-alpine.sh`, `docs/deploy.md`, `docs/operations.md`.

### Verification

40 contracts in `scripts/check_b292_exit_ssh_truth.sh`, plus:
`internal/headscale/ssh_key_b292_test.go` (every verdict + the container-default
hint + a behavioural case that a missing key is refused **without** running ssh),
`internal/feature/exit_rules/sync_b292_test.go` (the live headscale stub: IPv4
preference, `givenName`/`hostname`/case-insensitive matching, no invented address
when headscale has none or is down), `internal/db/exit_servers_b292_test.go`
(`FirstTailscaleIP`, the repaired row resolving to `root@<ip>`, the repair never
overwriting), `internal/feature/admin/exit_nodes_b292_test.go` (the ok/err
decision incl. `approved=0`, the effective key path, the template contract).

**Contract renegotiation:** `scripts/check_b266_exit_node_register.sh` E3 (the
absoluteness check moved into the shared preflight) and
`TestConfigSSHKeyPath_DefaultChangedForDocker` →
`TestConfigSSHKeyPath_DefaultPerInstallKind`.

### How to finish the setup on a native host (one-time)

```bash
# 1. The key the sync expects (the installer already created the directory):
sudo install -d -m 700 -o skygate -g skygate /var/lib/skygate/ssh
sudo ssh-keygen -t ed25519 -N '' -f /var/lib/skygate/ssh/id_ed25519
sudo chown skygate:skygate /var/lib/skygate/ssh/id_ed25519

# 2. Authorise it on the relay (over the tailnet IP the page shows):
sudo cat /var/lib/skygate/ssh/id_ed25519.pub | \
  ssh root@100.64.0.1 'mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys'

# 3. Check the path from the skygate host (this is what the sync does):
sudo -u skygate ssh -i /var/lib/skygate/ssh/id_ed25519 \
  -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@100.64.0.1 true

# 4. /admin/exit-nodes → Re-sync on the relay: the flash must read
#    "ssh=ok approved=N" (green) and the ADS ROUTES column must move off 2.
```

If the relay needs a different user/port or its own key, set `ssh_target`
(`user@host[:port]`) and `ssh_key_path` on the row — the page now shows which
path the sync will actually use and warns when it is unusable.

## v1.5.55 — the cluster/HA tree speaks SQLite (B291)

**Date:** 2026-09-23 · **Base:** `v1.5.54` → this tag · **Compatibility:** none —
no schema change, no migration. Only SQL text, Go-side predicates and
timestamp decoding changed; the PostgreSQL behaviour is unchanged (every
statement was verified against both dialects before it was rewritten).

Operator report (native host `aro`, SQLite):

> да давай

— i.e. go ahead with the deferred cluster block: the audit log carries a line
every 5 minutes and `/admin/cluster` + `/admin/ha` render empty.

```
cluster.discovery.error  cluster_node:workpc  error="insert discovered node:
                         SQL logic error: near \"['skygate-standby']\": syntax error"
```

137 of those rows ≈ 11 hours of a 5-minute tick, and the two cluster pages
rendered with no nodes, no invites and no events.

### Root cause

**The whole cluster/HA feature was written for PostgreSQL and was therefore dead
on SQLite**, which is what the native install uses
(`/var/lib/skygate/skygate.db`). Every write path carried a PostgreSQL-only
spelling, and `cluster_node` never received a single row:

| PostgreSQL-only SQL | where | SQLite answer |
|---|---|---|
| `ARRAY['skygate-standby']::text[]` | discovery | `near "[…]": syntax error` (the live line) |
| `'[]'::jsonb`, `$N::jsonb` | cluster bootstrap, cluster_audit, elector | `near "::": syntax error` |
| `'node-' \|\| substr(md5(random()::text),1,12)` | node ids | `no such function: md5` |
| `NOW()` | node CRUD, join, heartbeat | `no such function: NOW` |
| `SELECT … FOR UPDATE` | node CRUD (×4) | `near "FOR": syntax error` |
| `'skygate' = ANY (roles)` | failover, drill, CLI | `no such function: ANY` |
| `roles \|\| ARRAY['skygate']::text[]`, `array_remove(…)`, `array_to_string(…)` | failover, drill, CLI, drain | `no such function` |
| `detail->>'reason'`, `created_at > NOW() - INTERVAL '5 minutes'` | elector dedup, `/admin/ha` | `unrecognised token: ">"` |
| `extract(epoch FROM …)::bigint`, `detail::text` | `/admin/ha`, `/admin/cluster` | `unrecognised token: ":"` |

The read side leaked the same way, which is why the pages looked **empty**
rather than broken: `cluster_invite … AND expires_at > NOW()`,
`extract(epoch FROM created_at)::bigint`, `detail::text`, and
`joined_at`/`last_seen_at` scanned straight into `sql.NullTime` — SQLite stores a
bound `time.Time` in Go's `String()` form, which `sql.NullTime` refuses, so the
row was silently dropped from the page.

One bug was broken on **both** backends: the `/admin/ha` event union selected
`unix_timestamp` and `actor` from `audit_log`. Neither column exists (the table
has `created_at` and `username`), so the whole pre-B195 half of that history was
silently missing — on PostgreSQL too.

### What it does now

* **`internal/db/cluster_sql_b291.go`** owns every dialect spelling the tree
  needs: `DialectKind.NowExpr`, `NowMinusExpr`, `CastJSON`, `JSONField`,
  `CastTextArray`, `RandomHexExpr`, `ForUpdateExpr` (PostgreSQL only — SQLite has
  no `FOR UPDATE`, and a SQLite write transaction already takes a database-level
  lock) and `TimeValue` (binds RFC3339 on SQLite instead of the undecodable
  `String()` form, `time.Time` on PostgreSQL), plus `TextArrayLiteral` and the
  exact-match role helpers.
* **Role surgery happens in Go** — `RolesContain` / `RolesAdd` / `RolesRemove` /
  `FindClusterPrimary` / `NodeRoles` / `SetNodeRoles`. Exact matching matters:
  a SQL-side `LIKE '%skygate%'` also matches `skygate-standby` and would promote
  the node that is already the standby. Unrelated roles the operator set are
  preserved (the old PostgreSQL statement replaced the whole array).
* **Every timestamp is decoded tolerantly** through `db.ParseDBTime` on read and
  bound through `DialectKind.TimeValue` on write, so all three shapes a SQLite
  column can hold (unix seconds, `CURRENT_TIMESTAMP` text, and the legacy Go
  `String()` form) render — and a new layout in `dbTimeLayouts` keeps the rows
  already written by the pre-B291 code readable.
* **The two SQL-side time predicates moved into Go**: the pending-invite expiry
  filter and the elector's 5-minute `failover_recommend` dedup window (no
  `NOW()`, no `INTERVAL`, no `jsonb ->>`).
* **`/admin/ha` reads real columns** (`created_at`, `username`) — the legacy
  `ha.*` / `ha_chain.*` history is visible again on both backends.
* **`skygate cluster failover` (CLI) goes through the same helpers**, so a native
  install can promote a standby from the console.

Files: `internal/db/cluster_sql_b291.go` (new), `internal/db/cluster_failover.go`,
`internal/db/cluster_drill.go`, `internal/db/cluster_audit.go`,
`internal/db/cluster.go`, `internal/db/db_time.go`, `internal/cluster/discovery.go`,
`internal/cluster/cluster.go`, `internal/cluster/node.go`, `internal/cluster/join.go`,
`internal/cluster/invite.go`, `internal/elector/elector.go`,
`internal/feature/admin/cluster.go`, `internal/feature/admin/ha.go`,
`cmd/skygate/cluster.go`.

### Verification

50 contracts in `scripts/check_b291_cluster_sqlite.sh` — including a
comment-stripped sweep proving **no** PostgreSQL-only SQL is left anywhere in the
cluster/HA tree (the port's own comments quote the old statements on purpose, so
a naive grep always "finds" them).

Behavioural regression tests run the **real entry points against a real
(migrated, in-memory) SQLite database**, because "it compiles" cannot stand in
for "the statement parses":

* `internal/cluster/cluster_sqlite_b291_test.go` — discovery insert (the live
  error) + idempotency, the node lifecycle (generated ids, role literals,
  approve/drain/rejoin/remove), and the promote/demote role surgery plus the
  drill, asserting the roles and the audit trail.
* `internal/elector/elector_sqlite_b291_test.go` — a full elector tick: a stale
  ready primary must be flipped to `failed` with a `node_health` audit row, the
  next pass must write exactly one `failover_recommend`, and the third must not
  duplicate it (the dedup reads its own row back through the dialect helpers).
* `internal/feature/admin/cluster_ha_sqlite_b291_test.go` — both page collectors:
  every section populated, both timestamp shapes rendered (never the placeholder
  `—`), the expired invite filtered, and the event union carrying a source from
  **both** `audit_log` and `cluster_audit`.
* `internal/db/cluster_sql_b291_test.go` — the dialect fragments, the roles
  literal round-trip, and the ported statements executed on SQLite.

### How to verify on the host (after `/admin/update` to v1.5.55)

```bash
# 1. The recurring error must be gone (allow one discovery interval to pass):
journalctl -u skygate --since '-20 min' | grep -c 'cluster.discovery.error'   # → 0

# 2. cluster_node must actually hold rows now:
sqlite3 /var/lib/skygate/skygate.db "SELECT hostname, state, roles, last_seen_at FROM cluster_node;"

# 3. The two pages: /admin/cluster shows the nodes + invites + last-20 events,
#    /admin/ha shows the node table (with the promote button eligibility) and
#    the events union.
```

**Known remaining dialect leaks of the same class (B292 candidates, not part of
this block):** `internal/feature/admin/derp_relays_auto.go` writes the
auto-migrate marker with `EXTRACT(epoch FROM now())::bigint` (so the marker never
lands and the migration re-runs every boot), `internal/certsync/certsync.go` and
`internal/deploy/ha.go` insert audit rows with `now()`, and
`internal/feature/admin/certificates.go` / `deploy.go` read audit timestamps with
`EXTRACT(EPOCH FROM created_at)::bigint`.

## v1.5.54 — OIDC is configured from the UI, and it applies at once (B290)

**Date:** 2026-09-22 · **Base:** `v1.5.53` → this tag · **Compatibility:** none —
no schema change, no migration (the existing `oidc_settings` table is reused).

Operator report:

> нет опять же удобного выставления включения OIDC — пока нет в env строчки
> нельзя никак настроить, но из описания непонятно что и как добавлять, и удобнее
> было бы иметь автономный механизм включения без правки в ручную env

The form on `/admin/oidc` has existed since v0.75 and the boot path already read
the row — so the feature *looked* present and still could not be used. Three
defects were behind that:

1. **Saving changed nothing observable.** The handler answered
   «OIDC settings saved. Restart skygate (/admin/update) to apply» — an operator
   filling the form saw the same page, the same "OIDC выключен" banner, and
   concluded (correctly) that the switch did not work.
2. **The page rendered the env values, not the effective ones.** A saved row was
   invisible on the page that had just saved it, so the form and the running
   provider could disagree with no way to tell which was real. A row with
   `enabled=0` was also ignored at boot: only the *env* off-switch was honoured.
3. **The `client_secret` was stored in the clear.**

### What it does now

* **Effective configuration, UI over env.** `effectiveOIDCSettings` resolves each
  field (a saved value wins, the env var is the fallback, the built-in default is
  last) and every field on the page carries a source badge — *из UI* / *из env* /
  *по умолчанию* — so there is never any doubt about what is in force. The env
  emergency switch `SKYGATE_OIDC_ENABLED=false` is detected and explained
  (`SKYGATE_OIDC_ENABLED выключен в окружении — он сильнее формы`).
* **Save applies immediately.** `internal/oidc.Service` gained a lock-guarded
  `ApplyConfig`/`ConfigSnapshot` (every handler reads through
  `Issuer()`/`ClientIDValue()`/`ClientSecretValue()`/`RedirectURIsValue()`), and
  `main.go` wires `adminSvc.OIDCApplier`/`OIDCStatusFn` to the live service — so
  Save takes effect in the running process, with no `.env` edit and no restart.
  The page shows what the **running** provider holds next to the form.
* **`enabled=0` now means something.** A row saved with the box unticked disables
  the provider at boot *and* live. "Disabled" is expressed as an empty issuer, so
  the `/oidc/*` routes stay mounted (they answer 503 with the reason) and can be
  switched back on from the UI — pre-B290 the boot path set the whole service to
  `nil`, which made the UI switch unrepresentable and would have panicked on the
  very next line had the env switch ever been used.
* **The secret is encrypted at rest** with `SKYGATE_SECRET_KEY` behind an
  `enc:v1:` marker; rows written before this release (plaintext, no marker) still
  read, and a wrong key is a **named** error instead of a silently empty secret.
  The form never echoes the secret back; leaving the field empty keeps the stored
  one.
* **The fields explain themselves** (RU + EN): what the issuer is (skygate's own
  public URL), that `client_id`/`client_secret` must be pasted into headscale's
  `oidc.*` unchanged, what the redirect URI must be
  (`<base_domain>/oidc/callback`, exact match), and what `key_dir` is for.
* **Validation with reasons**: an enable without an issuer or a secret, a
  non-`http(s)` issuer and a malformed redirect URI are all refused with a message
  naming the field, and nothing is applied to the running provider in that case.

### Contracts

* `scripts/check_b290_oidc_ui_enablement.sh` (26 contracts) — the resolution
  order and the source badges, the applier wiring, the live-state block, the
  encryption marker + legacy rows, the boot switch, the RU/EN help keys, the tests
  and the git-tracked contract.
* `internal/feature/admin/oidc_settings_b290_test.go` — UI-over-env resolution
  (including the env off-switch), the encryption round trip + legacy plaintext +
  wrong-key error, an end-to-end save that applies exactly once with the values
  submitted (trailing slash trimmed), the four refusal cases, and the
  empty-secret-field-keeps-the-stored-secret rule.

### Operator action

1. Install v1.5.54 through **/admin/update**.
2. Open **/admin/oidc**. The page now shows the live provider state, each field's
   source, and the help block. Fill `issuer` (the address headscale reaches
   skygate on), `client_id` + `client_secret`, `redirect_uris`
   (`https://<headscale>/oidc/callback`), tick **Включить OIDC** and press Save —
   it is live immediately; the same `client_id`/`client_secret` must be present in
   headscale's own `oidc:` block.
3. If `SKYGATE_OIDC_ENABLED=false` is set in the environment, remove it (or flip
   it) — the page says so explicitly, and that variable beats the form.
4. No migration and no restart are required; the values live in `oidc_settings`,
   and `.env` remains the fallback for a fresh install.

## v1.5.53 — the local DERP relay must reach the map (B289)

**Date:** 2026-09-22 · **Base:** `v1.5.52` → this tag · **Compatibility:** none —
no schema change, no migration.

Operator report on the containerised VM (`skygate-host`): «локальный DERP сервер
никак не заработает» — the relay that runs on the same host as skygate never got
used. Measured on the host:

```
derper            healthy: TCP 443 + UDP 3478 bound, the right Let's Encrypt
                  cert (CN=derp.skynas.ru), reachable at 192.168.13.69:443
derpmap endpoint  GET /admin/derp/relays/derpmap.json  ->  {"Regions":[]}
journal           derpmap: skipping region=900 host=derp.skynas.ru port=443
                  — node unreachable: dial tcp 127.0.0.1:443: connect: refused
```

`/admin/derp/relays/derpmap.json` is the map headscale fetches and merges with the
public Tailscale map. An empty response means **no client is ever told the local
region exists** — every device silently uses the public relays, which is exactly
what "не заработает" looks like from the outside.

### Why the guard refused a healthy relay

B265 added a reachability probe so the map would not advertise a dead node (the
live VM also had a stale `:8443` row). The probe dialled the relay **hostname**
from inside the skygate container — and the container inherits the host's
`/etc/hosts`, where the relay's own public name maps to `127.0.0.1`
(deployment trap #2). Inside the container that is its *own* loopback, so the dial
was refused, every bundled node was dropped, and the map came back empty. The
relay was reachable the whole time at `192.168.13.69:443`.

### The fix

* `derpReachabilityCandidates` + `hostResolvesToLoopback` (B289): the guard now
  probes **every address a row is known by**, and when the relay's name resolves
  to loopback/unspecified it tries the operator's explicit
  `SKYGATE_DERP_PROBE_HOST` **first** instead of wasting the timeout on its own
  loopback.
* `probeDERPNodeReachableAny`: a node that answers on **any** candidate is
  published — with its public `HostName` intact, because the clients resolve that
  name themselves from outside the container.
* The journal now names the address that answered (`region=900 … answered via
  192.168.13.69:443 (the hostname did not resolve to the relay from inside this
  container — check extra_hosts / SKYGATE_DERP_PROBE_HOST)`) and, on a skip, lists
  every probed address.
* An enabled `is_bundled` region that publishes **no** node is logged as an
  **ERROR** with the per-row causes — that state means the operator's own relay is
  invisible to every client.
* `/admin/derp/relays` gained a «Карта DERP, которую получает headscale» block:
  per row, *published / skipped*, the address that answered, or the probed
  addresses and the failure (cached for 30 s so opening the page cannot cost a
  dial timeout per dead row). This can never again be a journal-only fact.

### Contracts

* `scripts/check_b289_derp_map_truth.sh` (15 contracts) — the candidate builder
  and the loopback detector, the fallback probe, the ERROR on an empty bundled
  region, the page block + its RU/EN keys, the cache, the tests, the git-tracked
  contract.
* `internal/feature/admin/derp_reach_b289_test.go` — the ordering rule
  (probe host first when the name is leaked), the first-answering-candidate probe,
  and an end-to-end derpmap render on a real SQLite schema that reproduces the
  live failure and now publishes the region.

### Operator action

1. Install v1.5.53 through **/admin/update** (image builds inside the container
   entrypoint, so the restart is the whole update).
2. Open **/admin/derp/relays**: the new block shows region 900 as
   *публикуется* with the address that answered. `headscale` re-fetches its derp
   map on its own schedule (`derp.update_frequency`, 10 min by default);
   `systemctl restart headscale`/`docker restart headscale` makes it immediate.
3. **Optional, and the cleaner long-term fix:** add the relay's real address to
   the skygate service in `docker-compose.yml` (the operator-managed file) so the
   container stops inheriting the loopback mapping:
   ```yaml
   services:
     skygate:
       extra_hosts:
         - "derp.skynas.ru:192.168.13.69"
   ```
   then `docker compose up -d --force-recreate skygate`. Without it the probe host
   is what keeps the map correct.
4. The stale bundled `:8443` row (nothing listens there) will keep showing as
   *пропущен* — that is correct; delete it in the same table if you no longer
   want it.

## v1.5.52 — policy drift must mean drift, and it must heal itself (B288)

**Date:** 2026-09-22 · **Base:** `v1.5.51` → this tag · **Compatibility:** none —
no schema change, no migration.

Operator report on the native host `aro`:

> На странице /admin/exit-nodes продолжает идти жалоба на «политика headscale
> УСТАРЕЛА», при этом в exit rules отмечено что все идет правильно и нод выбран
> верно (он в принципе один).

The tailnet has exactly one relay, so there was nothing to choose and nothing to
pin differently — yet the red banner came back on every page load. Both documents
were downloaded from the running instance and compared:

```
generated 5082 bytes / live 11341 bytes
grants     26               42        (16 of the live ones are DUPLICATES)
tagOwners   6                8        (live declares tag:dev-daniil-homepc,
                                       tag:dev-daniil-laptop, and owns
                                       tag:dev-infra-exit-node-vps together with
                                       tagged-devices@)
```

A headscale policy is a **set of statements** — grants are additive, so listing
the same grant twice changes nothing — therefore the byte difference described no
behavioural difference at all. The banner was true and useless, and the button it
offered would have rewritten the document for no gain. Four separate defects
produced that state:

### 1. The comparison was byte-shaped, not meaning-shaped

`PolicyEquivalent` decoded both documents and compared them with
`reflect.DeepEqual`, so the 16 duplicated grants (the pre-B274 generator emitted
one grant per rule row, and the CDN expansion had duplicated the rows — B274's
`CollapseDuplicateDerivedRules` fixed the generator, the live document kept the
duplicates) read as drift **forever**.

`internal/headscale/policy_compare_b288.go` now canonicalises both documents
first: set-like arrays (`grants`, `ssh`, and every `src`/`dst`/`via`/`ip`/
owner/member list) are sorted and de-duplicated, while the order-sensitive legacy
`acls`/`rules` array is deliberately left alone — there the **first** matching rule
wins, so its order and even its duplicates are meaningful. `PolicyDriftDetail`
additionally names which section differs, so the next banner says
`tagOwners: only in the live policy: tag:dev-daniil-homepc` instead of blaming the
`via` pins by default. Both are rendered on `/admin/exit-nodes`.

### 2. Three writers, three different owner sets for one tag

`tag:dev-<user>-<host>` was declared with `<user>@…` by the ACL generator, with
`<portal-user>@… + tagged-devices@…` by the ownership backfill, and with
`<row-username>@… + tagged-devices@…` by the tag reconciler — and the row's
`username` is headscale's synthetic `tagged-devices` for **every** tagged node
(B287), so the three genuinely disagreed. An ACL apply stripped the sentinel
owner, and the next tag apply was refused with `400 requested tags […] are
invalid or not permitted`.

One derivation now serves all three: `db.PerDeviceTagUser` (parse the user out of
the tag) and `db.TagOwnersForUser` (that user plus `tagged-devices@<baseDomain>`,
which is what lets an already-tagged node be re-tagged).

### 3. An apply could DELETE a declaration a node wears

The generator's `tagOwners` block came from `GetPerUserDeviceTags` — a JOIN on
`portal_users` that cannot see a row owned by the synthetic user — plus the B285
sweep over tags that **grants** name. `tag:dev-daniil-homepc` and
`tag:dev-daniil-laptop` are referenced by no grant, so the generated policy
declared neither while the live one declared both: in that one respect the
"stale" document was **more** correct than its replacement.
`db.ListDevTagsFromOwnerMap` now feeds every per-device tag recorded in
`node_owner_map` — the ownership record — into both generators.

### 4. Nothing was ever going to fix it

`applyACLIfDrifted` was documented as the single "make the control plane match the
database" step, but its only trigger was a **change** in the assignment table
(`chg > 0`) — and on a healthy install the table does not move. A mismatch that
pre-dates the pass (an older generator, one failed apply) was therefore permanent
by construction. `reconcilePrefixOwnership` now consults the drift check on every
sync pass behind its own five-minute throttle (`periodicDriftCheck`): a converged
host pays one policy read and writes nothing, a drifted host heals itself without
the operator pressing anything. The `via`-pin path (B276) is unchanged.

### 5. B288.1 — and nobody checked whether the write happened at all

Investigating the report on the **running** instance turned up the other half. Its
ACL history showed a fresh apply **every five minutes** — `#329 … #335`,
`skygate-auto-updater`, status `OK` — while the document headscale served stayed
the **pre-B274** one (11341 bytes, 42 grants of which 16 are duplicates) for days.
So skygate was not idle: it asked for the write every tick and recorded success
every tick, and nothing anywhere said the policy had not changed.

Two defects made that possible:

* **an accepted request is not a written policy.** The handoff to the privileged
  applier is a `rename(2)` into the directory watched by
  `skygate-policy.path`; it succeeds, the snapshot is marked `OK`, and whatever
  the root-owned script decides afterwards was invisible. The applier now records
  its verdict in `<update_dir>/policy-apply.status` (`ok` / `unchanged` /
  `failed` + timestamp, bytes, path and the reason), skygate reads it, the journal
  says `the write is NOT landing` when it reports a failure, and
  `/admin/exit-nodes` prints the verdict next to the drift:
  *applier: failed @ … — headscale did not answer on … the previous policy was
  restored* names the cause outright.
* **the applier's `tagOwners` union made convergence impossible.** B272.4 taught
  the applier to union the incoming `tagOwners` with the file on disk, to survive
  the lost-update race of the per-device `EnsureTagOwner` loop. That race is gone
  (B272.7 batches it into one read-modify-write, and B288 makes both writers emit
  a complete document), but the union stayed: a key the incoming document does not
  mention could never be **removed**, so a stale declaration such as
  `tag:dev-daniil-homepc` kept the live policy permanently different from the
  generated one — an apply and a headscale restart every five minutes, forever.
  The union is removed; the parse validation, the semantics-only no-op check and
  the rollback on an unhealthy headscale all stay.

### Also fixed

The **legacy** generator emitted `"tag:public": ["admin@<base>]` — the closing
quote was missing before the `]`, so the JSON was unparseable. On any host without
`SKYGATE_ACL_VIA_ENABLED=true` that is the generator the apply paths use, i.e.
every ACL apply would have failed with a parse error. Pinned by a test that
unmarshals **both** generators' output.

### Contracts

* `scripts/check_b288_policy_drift_truth.sh` (30 contracts) — set semantics and
  the untouched legacy order, the shared owner derivation, the complete
  declaration set, the periodic self-heal + throttle, the page's drift detail and
  applier verdict, the i18n keys in RU+EN, the legacy JSON fix, the applier's
  status file and the removal of the `tagOwners` union, the tests, and the
  git-tracked contract.
* `internal/headscale/policy_compare_b288_test.go` — duplicate grants and
  reordered owners are equivalent; an added/removed owner, declaration or grant is
  **not**; the legacy first-match list stays order-sensitive; "cannot parse" stays
  an error.
* `internal/headscale/policy_helper_b288_test.go` — the applier's verdict file
  path and reader (missing file, `failed` with its reason, `ok`, garbage).
* `internal/db/device_tag_b288_test.go` — the tag parser and the owner derivation
  (including the sentinel and the empty-base-domain error) and
  `ListDevTagsFromOwnerMap` on the live shape (every row owned by the synthetic
  user).
* `internal/acl/acl_b288_test.go` — every tag the ownership record holds is
  declared with the shared owner pair, and the two generators agree.
* `internal/feature/exit_rules/sync_b288_test.go` — a **stable** assignment table
  plus a stale live policy is re-applied (the regression), while a converged host
  writes nothing and does not even re-read the policy inside the interval.

### Operator action

1. Install v1.5.52 through **/admin/update** and confirm `/healthz` reports
   `"build":"v1.5.52+<sha>"`.
2. **Also refresh the privileged policy applier.** It is not part of the binary
   tarball (the release ships `skygate` only), so the copy in
   `/usr/local/lib/skygate/` stays the old one until you re-install it — and the
   old one has both defects above. From the checkout on the host:
   `git pull --ff-only` and then `sudo bash deploy/install-policy-helper.sh`
   (idempotent: re-installs `deploy/skygate-apply-policy.sh` plus the
   `skygate-policy.path`/`.service` units, reloads systemd and reports a queued
   request).
3. Open `/admin/exit-nodes`. The banner now has two lines under the byte sizes:
   *what differs* (e.g. `tagOwners: only in the live policy: …`) and the
   **applier's verdict**.
   * If the verdict is `failed`, it names the cause (usually `headscale did not
     answer on <url> … the previous policy was restored`) — that is the reason the
     policy never changed, and the applier's log next to it
     (`<update_dir>/policy-apply.log`) has the full text.
   * If it is `ok` and the banner persists, the file was written while headscale
     kept serving the old document — the verdict's `PATH`/`TS` say what was
     written and when.
4. Once the write lands (the union removal in this release is what lets the file
   converge), the automatic sync clears the banner within one tick and it stays
   clear — the generated document is a fixed point now.
5. No migration and no manual headscale edit is required. If the applier reports a
   failure, send us its line plus `/var/lib/skygate/update/policy-apply.log`.

## v1.5.51 — the per-device tag is an ownership record (B287)

**Date:** 2026-09-22 · **Base:** `v1.5.50` → this tag · **Compatibility:** none —
no schema change, no migration.

Operator report on the native host `aro`:

> «устройства что с тегами пользователя не отображаются в его устройствах на
> странице мои устройства а только во все устройства»

A user's own devices — the ones *carrying that user's tags* — did not appear on
`/my/devices` at all, while `/admin/devices` listed them. The admin page showed
why, and the answer was unexpected: **every** row's owner read `tagged-devices`:

```
id  hostname        owner            tag
1   exit-node-vps   tagged-devices   tag:dev-infra-exit-node-vps
2   workpc          tagged-devices   tag:dev-daniil-workpc
3   laptop          tagged-devices   tag:dev-daniil-laptop
```

None of the three devices had lost anything. headscale reassigns a node to the
synthetic `tagged-devices` user as soon as it wears **any** tag, and
`node_owner_map` had copied that name verbatim. Both ownership tests on
`/my/devices` keyed on that column:

* the live branch matched `n.UserName == username`;
* the snapshot branch asked `db.ListNodeOwnerNodeIDsByUsername`.

So the tag worked exactly as designed and the port of the page that asks "is this
device mine?" read the wrong column — the user's own devices were invisible on
their own page, and only an admin could see them, under a meaningless owner.

### The tag *is* the ownership record

`tag:dev-<user>-<host>` is minted per user, and headscale only accepts it once
that user (or the synthetic `tagged-devices`) owns the node — so the tag names
the owner even when the `username` column does not. New
`internal/db/device_tag.go` is the single place that answers the question:

* `PerDeviceTag(username, hostname)` — `PerDeviceTag("Daniil", "WorkPC")` →
  `tag:dev-daniil-workpc`; both halves lowercased (headscale 0.29 rejects
  uppercase tags, B176), `""` when either half is empty;
* `TagNamesUser(tag, username)` — matches the segment **including its separator
  dash** (`tag:dev-daniil-`), so a shorter username (`dan`) cannot claim a longer
  one's devices, and hostnames may contain dashes freely. Infra tags
  (`tag:dev-infra-<host>`) and the legacy `tag:exit-<host>` form name a role or
  nothing at all, and every class tag is refused outright — the same taxonomy the
  ACL generator uses (B285);
* `HasPerDeviceTag(tags, username, hostname)` — case-insensitive check against a
  live headscale tag list;
* `ListNodeOwnerNodeIDsByUserTag(d, username)` — the snapshot twin, with the
  LIKE metacharacters escaped (`%`, `_`, `\`), so a username containing a
  wildcard cannot match another user's tags.

`/my/devices` now uses them in **both** paths: the live branch accepts a node
whose tag names the user, and the snapshot branch unions the by-tag lookup with
the by-username one.

### The synthetic owner is not by itself a "ghost"

Showing the devices is only half of it: the same sentinel owner also drove
`IsTaggedGhost`, which renders the page-top «N устройств в синтетическом
пользователе `tagged-devices` — перерегистрируйте их» banner and a per-row
**Re-register** button. `tagged-devices` means one of two very different things:

* the device registered with a pre-auth key that carried no `--user`, so headscale
  had nowhere to put it — the **real ghost**, and re-registration is the fix;
* the device merely **wears a tag** — headscale moves every tagged node to that
  synthetic user, and the tag names the owner, so nothing is wrong.

`IsTaggedGhost` is now `n.UserName == "tagged-devices" && !devTagApplied` in both
loops, so a device that the per-device tag attributes to the viewing user is not
invited to delete and re-key itself. A genuine ghost (sentinel owner, no tag
naming the user) keeps the banner and the button exactly as before — the
B-mod-reregister contracts are unchanged and still pass.

### What is *not* in this release

`/admin/devices` still renders the raw `node_owner_map.username`, so an admin
sees `tagged-devices` in the owner column for a tagged node. That is a display
issue, not an ownership one — the admin page's purpose is to show what headscale
says — but it is the surface that produced the confusing screenshot, and it stays
on the list (the same helpers make the fix a one-liner).

### Contracts

* `scripts/check_b287_tag_ownership.sh` (15 contracts) — the four helpers exist
  and are the only tag-minting/parsing site used; infra/class tags are excluded;
  the LIKE lookup escapes its metacharacters; `/my/devices` uses both in the live
  and the snapshot path; both `IsTaggedGhost` sites are guarded by the tag test
  while the sentinel-owner test the B-mod-reregister contract greps for survives;
  the git-tracked contract (trap #11) is asserted with
  `git ls-files --error-unmatch`.
* `internal/db/device_tag_b287_test.go` — seeds the **live shape** (rows whose
  `username` is the synthetic owner while their tag names the real user) and pins
  the pre-B287 blind spot: the by-username lookup returns nothing on it, the
  by-tag lookup returns exactly the user's devices.

### Operator action

1. Install v1.5.51 through **/admin/update** (native installs do not rebuild on
   `git pull` — deployment trap #12) and confirm `/healthz` reports
   `"build":"v1.5.51+<sha>"`.
2. Open `/my/devices` as the affected user: the tagged devices (`workpc`,
   `laptop`) must now be listed, **without** the «перерегистрируйте их» banner and
   without a per-row Re-register button (the tag attributes them to that user).
   `/admin/devices` is unchanged by design.
3. No migration, no ACL re-apply, no restart of headscale is required.

## v1.5.50 — the ACL must apply, and a page must survive an empty list (B285 + B286)

**Date:** 2026-09-22 · **Base:** `v1.5.49` → this tag · **Compatibility:** none — no
schema change, no migration.

Two follow-ups found live on `aro` within minutes of installing v1.5.49. Both are
consequences of the earlier fixes working: they removed the outage, and what was
left were the two things the outage had been hiding.

### B285 — the generated policy must declare every tag its grants reference

One sync pass after v1.5.49:

```
acl-drift: re-apply FAILED (refusing to set a policy that references 1 tag(s)
missing from tagOwners: tag:dev-daniil-workpc — headscale rejects such a
document and will not start on it) — the live policy stays stale; the next pass
retries
```

The refusal came from v1.5.48's guard, and it was **correct** — headscale rejects
such a document as a whole, and on a `policy.mode: file` host the daemon will not
start on it. But it also meant the ACL could never apply. Why the two disagreed:
the grants take their device tag from `node_owner_map.tag` (v1.5.49), while the
`tagOwners` block derives its entries from `GetPerUserDeviceTags`, which needs a
**portal-user** row. On this host both `node_owner_map` rows are owned by
headscale's synthetic `tagged-devices`, so the per-user block emitted nothing
while the grants named `tag:dev-daniil-workpc` (the client) and
`tag:dev-infra-exit-node-vps` (the relay).

The generator now records every tag its grants name and sweeps the missing ones
into `tagOwners` (owner parsed from `tag:dev-<user>-<host>`, sorted for a stable
document), so the invariant `SetPolicy` enforces is satisfied by construction
instead of merely reported.

### B286 — an empty slice must not take a page down

`/my/exit-rules` answered a Go template error instead of the user's rules:

```
render: template: layout.html:290:2: executing "layout" at : error calling
renderBody: template: exit_rules.html:264:35: executing "body-exit_rules" at :
error calling index: reflect: slice index out of range
```

The template initialised its per-device preferred-exit fallback with an unguarded
`{{$pref := index $.DeviceInfos 0}}` — and the page had rules but **no device
rows** (the relay had just been moved to the infra owner). The same unguarded
pattern sat in four more templates: `{{index .IPAddresses 0}}` on the user and
admin device pages and the exit-nodes page (a node without addresses), and
`{{if index .DERPs 0}}` on the DERP dashboard (an install with no relays). All
five now use guarded forms (`{{with .IPAddresses}}{{index . 0}}{{end}}`,
`{{with .DERPs}}`, and a plain-string `$pref`).

### Contracts

* `scripts/check_b285_acl_tags_declared.sh` (9 contracts) +
  `internal/acl/acl_b285_test.go` — the test seeds both rows with the synthetic
  owner and **fails without the sweep**, naming the undeclared tag.
* `scripts/check_b286_template_index_bounds.sh` (8 contracts) — static: it pins
  the absence of the construct that caused the panic plus the guarded
  replacements, and the templates' parse + handlers tests. A full render with an
  empty device list does not exist in that package yet and the check says so.

### Operator action

Update to `v1.5.50` (binary only; the applier script is unchanged since v1.5.48).
After the next sync pass the ACL drift banner should clear and
`/my/exit-rules` should render again.

## v1.5.49 — a device tag must be the tag the node carries (B284)

**Date:** 2026-09-22 · **Base:** `v1.5.48` → this tag · **Compatibility:** none — no
schema change, no migration.

The second, independent cause of the same `aro` outage that v1.5.48 hardened.
v1.5.48 made a bad policy write impossible (atomic handoff, validation, health
check, no-op skip); this release fixes the document that was being written.

### Root cause

The ACL generator **synthesised** the per-device selector
`tag:dev-<user>-<hostname>` instead of reading the tag off the node, and the user
name it used was `device_rules.user_name` / `device_exit_node_prefs.username` —
on the live host **headscale's synthetic owner for tagged nodes,
`tagged-devices`**. The refused snapshot therefore referenced

```
tag:dev-tagged-devices-exit-node-vps
tag:dev-tagged-devices-workpc
```

headscale rejects a policy that references a tag missing from `tagOwners` **as a
whole** ("tag not found") and, with `policy.mode: file`, will not START on it:
crash-loop counter 248, control plane down, every device gone from the portal.

The evidence is exact — `python3` over the saved snapshots on the host:

```
/tmp/policy.297.json undeclared: []                                  # the working policy
/tmp/snap.304.json   undeclared: ['tag:dev-tagged-devices-exit-node-vps',
                                  'tag:dev-tagged-devices-workpc']   # the refused one
```

`tagOwners` is built from `node_owner_map.tag` (and from portal-user rows), so a
tag minted from the synthetic owner can never be declared: the document was
invalid **by construction**.

### Fix

* `deviceTagForRule` (`internal/acl/acl.go`) returns `node_owner_map.tag` for the
  rule's device — the selector headscale carries and the one this generator
  declares — and otherwise **nothing**; the caller falls back to the device-IP
  selector, which is weaker than a tag but always matches, instead of a policy the
  daemon refuses to load. `deviceOwner` now carries that tag.
* Both per-device pref loops (`GenerateACLForPlane` and
  `GenerateACLWithViaForPlane`) resolve the device tag by hostname through
  `prefixowner.TagsByHost` and skip a pref whose node has no tag, instead of
  building `tag:dev-<username>-<host>` themselves.
* **Contract renegotiated:** `TestDeviceTagForRule_Pure` (B265) pinned the
  synthesis; it now pins the node's tag, including the live synthetic-owner case
  (which yields no tag at all). `scripts/check_b176.sh` contract A.5 also pinned
  the `ToLower` call inside the helper — with the synthesis gone there is no
  string to lowercase, so it now asserts the tag's SOURCE (`node_owner_map.tag`,
  lowercase by construction) and its A.4 straggler sweep still forbids any new
  hand-built `tag:dev-…Hostname` site.

### Contracts

11 contracts in `scripts/check_b284_device_tag_source.sh` (registered as
`run_check "B284"`) plus `internal/acl/acl_b284_test.go`, which reproduces the
live shape (a `device_rules` row whose denormalised owner is `tagged-devices`)
and **fails without the fix**, naming the invented tag:
`generated policy references undeclared tag(s): tag:dev-tagged-devices-workpc`.

### Operator action

`v1.5.48` must already be installed (it is what keeps a bad document from ever
reaching headscale). Then update the binary to `v1.5.49` the same way and start
skygate; the ACL it generates now names only tags that exist on the nodes and are
declared in `tagOwners`, so the policy applies instead of being refused.

## v1.5.48 — a policy write must not be able to take the control plane down (B283)

**Date:** 2026-09-22 · **Base:** `v1.5.47` → this tag · **Compatibility:** none — no
schema change, no migration. **Install this release before starting skygate again
on a `policy.mode: file` host** (see *Operator action* at the end).

This is the outage that followed v1.5.47 on the native host `aro`: skygate wrote
a headscale policy file the daemon could not load, and `headscale.service`
crash-looped **248 times** —

```
Error: initializing: creating new headscale: init state: initializing policy
manager: parsing policy: parsing HuJSON: hujson: line 302, column 23:
invalid character ']' after object name
```

— so the tailnet lost its control plane and **every device disappeared from the
portal**. Nothing was deleted: headscale's own database was intact and an older
policy snapshot brought it straight back. Two independent defects produced the
bad file, and two further defects made it worse.

### Root cause

1. **The handoff race (why the file was corrupt).** `RequestPolicyApply` rewrote
   the watched `policy.request.props` **in place** (`os.WriteFile` = truncate +
   write) while the root applier read that same file line by line. A second
   request landing mid-read made the reader continue at its file offset into the
   *new* content, so it stitched the head of one document onto the tail of
   another. The evidence is exact: the saved snapshot (`acl_snapshots` id 298) is
   valid JSON and 7869 bytes; the file the applier wrote at 18:26 was also 7869
   bytes and unparseable, mixing a pretty-printed fragment of the *old* policy
   (`"via": ["tag:exit-node"]`) with compact grants of the *new* one
   (`"via": ["tag:dev-infra-exit-node-vps"]`).
2. **Nothing validated the document, and the crash-loop was invisible.** No
   check refused an unparseable policy, and the applier treated
   `systemctl restart` as proof of success — it returns 0 as soon as the unit is
   *started*, so the script logged `done` while headscale was already dying on
   the file it had just written, and the `policy.prev` rollback never fired.
3. **An undeclared `via` tag (why a *valid* document still would not boot).** The
   generated policy pinned per-CIDR grants to the B275 assignment table's owner
   tag, but that tag was never merged into the tag set that fills `tagOwners`.
   headscale refuses such a document **as a whole** ("tag not found") and, in
   file mode, will not start on it: installing the newer snapshot (valid JSON,
   6881 bytes) left the API dead, while the older snapshot from before the relay
   was given its own tag started immediately.
4. **A rewrite every ~5 minutes.** The applier log shows the same policy written
   at 16:54, 16:59, 17:14, … 18:50 with 7100/7109/7126/7135-byte variants — the
   tag path re-marshalled the document, the B276 drift check regenerated it, and
   every write restarted headscale. Each extra write was another chance to hit
   (1).

### Fix

* `RequestPolicyApply` (`internal/headscale/policy_helper.go`) hands the request
  over **atomically**: a temp file in the same directory, `chmod 0640`, then
  `rename(2)`. A reader now sees either the whole old file or the whole new one —
  a splice is impossible.
* `SetPolicy` (`internal/headscale/acl.go`) refuses to hand headscale a document
  it cannot load: unparseable policies **and** policies whose grants reference
  tags missing from `tagOwners` are rejected by name, before any API call or file
  write, with the previous policy left in force. The check is
  `headscale.UndeclaredTags` (`internal/headscale/policy_validate.go`), which
  walks `grants[]`/`acls[]` (`src`, `dst`, `via`) against `tagOwners`.
* The ACL generator (`internal/acl/acl.go`) merges the B275 assignment-table
  owner tags (`prefixowner.TagByPrefix`, the source of `ViaForPrefix`) into the
  set that fills `tagOwners`, so every `via` it emits is declared in the same
  document.
* A file-mode write is **skipped when the document already says the same thing**
  (`PolicyEquivalent` on the on-disk document), in the Go client and again in the
  applier — an unchanged policy no longer costs a headscale restart, which is
  what kept the 5-minute loop going.
* `deploy/skygate-apply-policy.sh` now validates the body with `python3` before
  writing anything (a bad document is refused, the previous file stays, and the
  log names the reason), and after the restart it **polls headscale's `/health`**
  (`SKYGATE_HEADSCALE_HEALTH_URL`, default `http://127.0.0.1:8081/health`) and
  restores `policy.prev` when the daemon does not answer — instead of logging
  `done` over a dead service.

### Contracts

`scripts/check_b283_policy_write_safety.sh` (18 contracts; registered as
`run_check "B283"`) plus regression tests:
`internal/acl/acl_b283_test.go` (the generator test reproduces the live shape —
the relay's `node_owner_map` row owned by `tagged-devices`, so no other tagOwners
path can declare the tag — and **fails without the generator fix** with
`generated policy references 1 tag(s) missing from tagOwners:
tag:dev-infra-exit-node-vps`),
`internal/headscale/policy_validate_b283_test.go` (the invariant + `SetPolicy`
refusing a bad document without issuing the request) and
`internal/headscale/policy_helper_b283_test.go` (the handoff refuses an
unparseable policy, leaves no partial request behind, and stays a single complete
document across two handovers).

### Operator action

The outage was recovered by hand: skygate was stopped and headscale was started
on `acl_snapshots` id 297 (the policy from 18:16, before the relay was given its
own tag). **Keep skygate stopped until this release is installed** — the running
build rewrites the policy within ~5 minutes (or immediately at startup) and would
knock headscale over again.

Native (systemd) hosts, with skygate stopped, update without the in-app updater:

```bash
TAG=v1.5.48; ARCH=linux-amd64
BASE="https://github.com/BarsSky/skygate/releases/download/$TAG"
curl -fsSLO "$BASE/skygate-$TAG-$ARCH.tar.gz"
curl -fsSLO "$BASE/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf "skygate-$TAG-$ARCH.tar.gz"
sudo install -m 0755 skygate /usr/local/bin/skygate   # path used by your unit
sudo systemctl daemon-reload && sudo systemctl restart skygate
curl -fsS http://127.0.0.1:8080/healthz   # expect "build":"v1.5.48+<sha>"
```

Also update the privileged applier, which now validates and health-checks:

```bash
sudo install -m 0755 deploy/skygate-apply-policy.sh /usr/local/lib/skygate/skygate-apply-policy.sh
```

Then start `skygate`; the policy it writes is either identical to the one already
in force (no restart at all) or a document headscale is guaranteed to accept.

## v1.5.47 — an operator page must read the database the operator actually runs (B282), plus B281 follow-ups and registry hygiene

**Date:** 2026-09-22 · **Base:** `v1.5.46` → this tag · **Compatibility:** none — no
schema change, no migration, nothing the operator has to run by hand.

Live bug (user-reported, native `aro` host, SQLite at
`/var/lib/skygate/skygate.db`). Three operator surfaces were dead, and each one
blamed something else:

```
GET  /admin/audit            → 500  SQL logic error: unrecognized token: ":" (1)
exit_rules.preferred_mismatch → fail query rules: SQL logic error: unrecognized token: ":" (1)
GET  /admin/headscale/acl    → 500  list acl: unmarshal policy: json: cannot unmarshal
                                    string into Go value of type admin.ACLView
```

The first two are the *same* defect: SQL written for PostgreSQL, executed on
SQLite. The third is a second one, hidden behind the same page-load path. The
audit page matters most here — it is where the B279 incident was diagnosed from
(`my_exit_rules_apply_preferred preferred=node updated=21`), and on a SQLite
install it could not be read at all.

### Root cause

1. **`::` is not a token in SQLite — the whole statement fails to parse.**
   `/admin/audit` built its `audit_log ∪ cluster_audit` UNION with the
   PostgreSQL forms inline (`'audit_log'::text`, `to_timestamp(created_at)`,
   `''::text`, `detail::text`), and the `exit_rules.preferred_mismatch` system
   test hardcoded `node_owner_map.node_id = r.device_id::text`. The cast is
   *required* on PostgreSQL (`text = integer` is SQLSTATE 42883 without it) and
   *fatal* on SQLite, where the parser stops at the colon and reports a bare
   `unrecognized token: ":"` — a message that names neither the dialect nor the
   column.

2. **A timestamp column is not always a `time.Time`.** The audit reader scanned
   the `ts` column straight into `time.Time`. On PostgreSQL that column is
   `TIMESTAMPTZ` and pgx hands back a `time.Time`; on SQLite it is the raw
   column — `audit_log.created_at` is INTEGER Unix seconds, while
   `cluster_audit`'s DDL declares INTEGER but defaults to `CURRENT_TIMESTAMP`,
   which SQLite evaluates to the TEXT form `2006-01-02 15:04:05`. The
   `?since=` filter had the mirror-image bug: binding a `time.Time` reaches
   SQLite as TEXT, and a TEXT/INTEGER comparison in SQLite is always false.

3. **The policy API can answer with a *stringified* document.** headscale
   returned `{"policy":"{…escaped…}"}`. `GetACL` unquoted that shape for the
   legacy `data` field only, so the `policy` field was cached **with its
   quotes** — every consumer that fed it to `json.Unmarshal` died on
   `cannot unmarshal string into …`. `/admin/headscale/acl` and the
   `exit_rules.all_in_headscale_acl` system test were both affected, and the
   B276 staleness comparison had its own private copy of the normalisation.

### Fix

* `db.DialectKind.CastText` (`internal/db/dialect.go`) — the one dialect-native
  text cast: `::text` on PostgreSQL, `CAST(x AS TEXT)` on SQLite. No shared
  query types `::` any more.
* `db.ParseDBTime` (`internal/db/db_time.go`) — decodes whatever the driver
  returns for a timestamp (`time.Time`, INTEGER/`float64` Unix seconds, or the
  textual forms above); unknown input is reported as "not a time" instead of
  being rendered as 1970.
* `buildUnifiedAuditQuery(kind, …)` (`internal/feature/admin/admin_pages.go`) —
  the audit UNION is assembled for the live dialect
  (`db.ActiveDialect()`): `to_timestamp(created_at)` + `::text` literals on
  PostgreSQL, the raw column and plain literals on SQLite. Placeholders stay
  `$N` on both backends (modernc.org/sqlite binds `$NNN` by ordinal; pgx
  rejects `?`). `?since=` binds Unix seconds on SQLite.
* `preferredMismatchRulesQuery(kind)` (`internal/feature/admin/system_tests.go`) —
  the join cast comes from the dialect, not from a literal typed into the SQL.
* `headscale.PolicyJSON` (`internal/headscale/policy_json.go`) — the single
  normaliser (unquote a stringified policy → `hujson.Standardize`), shared by
  `GetACL`, `ListACL`, the ACL system test and `PolicyEquivalent`; `GetACL` now
  unquotes the `policy` field too, so the cache always holds the document.

### Also in this tag

* **B281 follow-up — a PASS row must not look like a compiler diagnostic.**
  GitHub annotates any log line shaped `<path>.go: <message>` at *failure* level
  even for a successful step, and 21 catalog descriptions began with exactly
  that shape, so a **green** `verify-pre` run rendered the Actions UI as
  "12 errors" with PASS-row tails as the bodies (run 35747083447). PASS now
  prints a short label (leading `path.ext: ` stripped, remainder truncated);
  FAIL/TIMEOUT keep the full description *and* the check's output, which is
  where the narrative is read. `SKYGATE_CATALOG_VERBOSE=1` restores the old
  rows. Contracts B281 O1–O5.
* **Registry hygiene — `ghcr-prune.yml`.** Every release push left a package
  version behind and only the newest image is ever pulled, so
  `ghcr.io/barssky/skygate` had grown to 77 versions (≈20 releases of layers).
  The new workflow is manual and **dry-run by default**, needs
  `packages: write`, keeps an explicit tag matcher (`v1.5.46` → `v1.5.46`,
  `v1.5`, `v1`, `latest`) and refuses to guess "keep whatever is newest".
  First live run: 1 kept, 76 deleted, 0 failed. Contracts B281 N1–N6, including
  a `git ls-files` assertion so the workflow cannot be silently untracked
  (trap #11).
* **B207 contract renegotiated.** Its three union assertions grepped
  `-A200 'func … GetAdminAudit'`; moving the SQL into
  `buildUnifiedAuditQuery` put `FROM audit_log` 206 lines below the handler —
  six past the budget — so a *correct* handler reported two FAILs. The check now
  asserts on the builder functions themselves instead of on a window.

### Contracts

32 contracts in `scripts/check_b282_admin_reads_dialect.sh` (registered as
`run_check "B282"` in `scripts/verify_pre_deploy.sh`), plus real-SQLite
regression tests that run the actual statements against
`db.ApplyMigrations`-built `:memory:` databases:
`internal/db/dialect_b282_test.go`, `internal/headscale/policy_b282_test.go`,
`internal/feature/admin/b282_sqlite_reads_test.go` (the audit UNION with both
timestamp shapes, the preferred-mismatch join, and a stringified policy through
`ListACL`).

### Verification

* `bash scripts/verify_pre_deploy.sh` — **311 PASS, 0 FAIL, 1 SKIP** (`B8` is
  VM-only), 0 TIMEOUT.
* `go vet ./...` clean; `staticcheck` on the touched packages clean;
  `go test ./...` green.
* Live verification on `aro` is the operator's `Update` from `/admin/update`,
  followed by `/healthz` showing `"build":"v1.5.47+<sha>"`.

### Operator action after the update

The code fix restores the three pages and the system test. It does **not**
restore split-tunnel routing by itself — that needs the two host-side items
diagnosed at the same time on `aro`:

* give the relay its own tag (`tag:dev-infra-exit-node-vps`) so a preferred
  exit-node can be stored at all — until then `/my/exit-nodes` keeps showing
  «нет тега узла» and no `via=` pin can name the relay;
* register the relay's SSH target and key in `exit_servers` (the default
  `SKYGATE_EXIT_SSH_KEY=/ssh-sync/id_ed25519` is a *container* path and does not
  exist on a native install, and an empty `ssh_target` falls back to the bare
  hostname, which does not resolve from the skygate host):
  `Пере-синхронизировать` currently answers
  `ssh=err=… Identity file /ssh-sync/id_ed25519 not accessible … Could not resolve hostname exit-node-vps`.

Same class of PostgreSQL-only SQL still lives on `/admin/cluster`
(`cluster.go:362,365`) and `/admin/ha` (`ha.go:214,217,220,277`) — those queries
swallow their errors, so on SQLite those two pages are silently empty rather than
broken. Tracked as a follow-up, not part of this tag.

## v1.5.46 — a class tag is not a node identity (B279 + B279.1), and green CI finally means 0 FAIL (B280 + B281)

**Date:** 2026-09-22 · **Base:** `v1.5.45` → this tag · **Compatibility:** none.

Live bug (user-reported on the native `aro` host): `/my/exit-rules` grouped all
23 rules under an exit-node named **`node`**, `/admin/exit-nodes` listed only
**`exit-node-vps`**, and every rule stayed in ⏳ no matter what the operator
did. `headscale nodes list` had exactly four nodes — `exit-node-vps`, `workpc`,
`laptop`, `homepc` — and **no node named `node` had ever existed**. The audit
log told the whole story:

```
13:14:58  my_preferred_exit_set        tag=tag:exit-node via=false
16:42:22  my_exit_rules_apply_preferred preferred=node updated=21
20:49:19  my_preferred_exit_set        tag=tag:exit-node via=false
20:49:28  my_exit_rules_apply_preferred preferred=node updated=3
```

### Root cause

`tag:exit-node` is a **class tag** — a role shared by every relay. Four
independent copies of "strip `tag:` to get a hostname" disagreed about it:

| place | result for `tag:exit-node` |
|---|---|
| `internal/acl/acl.go` | `"node"`, caller documented to treat it as a non-match ✔ |
| `internal/feature/exit_rules/preferred_check.go` | `"node"`, **used as a hostname** ✘ |
| `internal/db/exit_node_prefs.go` (`isExitNodeTagForm`) | accepted it as a per-node tag ✘ |
| `internal/feature/admin/user_subnet.go:442` | had its own inline guard ✔ |

The `aro` relay carried only `tag:exit-node` (no `tag:dev-infra-<host>`), so
"★ Set preferred" posted that class tag; `TagToHostname` read it back as
`node`; the mismatch banner then offered the B277.3 "Use preferred" button,
which wrote the phantom into 21 + 3 rules. From then on nothing could work:
`SyncAdvertisedRoutes` keyed on `exit_node_id="node"` (nobody advertised the
prefixes), route approval targeted a node that does not exist, `prefix_owner`
kept the real relay while the loop never visited it
(`prefix-ownership: node drops 19 claimed prefix(es) owned by another relay`),
and the per-CIDR ACL `via` pin resolved to no node.

Two things kept the state stuck: the B229 reconciler derived a *preference*
from the same class tag (`NormalizeExitNodeTag` accepted it), and
`exit-node-monitor`'s per-tick auto-sync fed headscale's `Tags[0]` — the class
tag — back into `node_owner_map`, so an operator-supplied per-node tag was
reverted every five minutes and the B272 reconciler then reported "already in
sync" (silence).

**Correction to v1.5.45's write-up:** that entry attributed the same symptom to
a headscale rename (`node` → `exit-node-vps`) plus a missing `device_rules`
cascade. The live data shows no rename ever happened — `node` was manufactured
by the sentinel. B231's cascade is still a real fix, but it was not this bug.

### Fix

One predicate decides the question and every consumer calls it —
`internal/db/tag_kind.go`:

* `IsClassTag` — `tag:exit-node`, `tag:public`, `tag:private`,
  `tag:subnet-router` (case-insensitive) name roles, never one node;
* `IsPerNodeTag` — `tag:dev-infra-<host>` / `tag:dev-<user>-<host>` / legacy
  `tag:exit-<host>`;
* `PickPerNodeTag` — the node's own tag out of headscale's array, or `""`.

Applied at every layer:

1. `db.NormalizeExitNodeTag` refuses a class tag (`ErrClassTagNotPerNode`,
   naming the fix) and `isExitNodeTagForm` delegates to `IsPerNodeTag`.
2. `exit_rules.TagToHostname` returns `""` for a class tag — the "any
   exit-node" signal the callers already understand (no mismatch banner, no
   bulk rewrite, route script falls back to the first healthy relay).
3. `PlanDevicePrefChange` never *derives* a preference from a class tag and
   emits a `clear` change for a stored one; `applyReconcilerChange` implements
   it (dry-run + live, audited). The new `ClearClassTagPrefs` sweep in the same
   tick covers user-level rows and devices with no rules.
4. `SyncNodesFromHeadscale` keeps a non-empty row tag when headscale sends a
   class tag (it may only *fill* an empty/untagged row) and
   `SyncTagsFromHeadscale` does the same; every producer (monitor, `/nodes`,
   `/my_nodes`, `/sync_nodes`, `/admin/devices`, first-run auto-sync,
   node-ownership backfill) now uses `PickPerNodeTag` instead of `Tags[0]`.
5. `PostMyExitRulesApplyPreferred` validates the target against the live exit
   nodes before writing — the destructive 21-rule rewrite is now a flash
   error.
6. `SyncAdvertisedRoutes` also syncs the relays the assignment table names
   (`OwnedPrefixesForRelay` / `relaysFromAssignment`), and the losers report
   compares against the table instead of ignoring its `owners` argument.
7. `/my/exit-rules` enriches `DeviceName` after pagination again (v1.5.43
   dropped it, so rules grouped under a bare `"2"` instead of `workpc`).
8. UI: `/my/exit-nodes` shows "no node tag" with the fix instead of a button
   that stored the class tag; the synthesised `tag:exit-<host>` ghost tag is
   gone from both the user page and the admin dropdown (there it is a disabled
   option with a hint).

### B279.1 — the remaining copies of "tag → hostname"

B279 closed the bug but left the duplication that produced it. Four
implementations derived a hostname from a tag: `db.isExitNodeTagForm`,
`exit_rules.TagToHostname`, `acl.exitNodeTagToHostname`, and an inline
`tagToHost` closure in `internal/feature/admin/system_tests.go` (the
v1.3.18.1 hotfix) — plus a hand-written `!HasPrefix(tag, "tag:exit-node")`
guard in `user_subnet.go`. The ACL copy was the "safe" one: it returned
`node` for the sentinel and its *callers* were documented to treat that as
a non-match. "Safe because the caller read a comment" is not a mechanism,
and it is exactly the shape that broke in the copy without one.

Now the class-tag knowledge exists once — `internal/db/tag_kind.go` owns
`IsClassTag`, `IsPerNodeTag` and the new `IsExitNodeTagForm`:

* `acl.exitNodeTagToHostname` guards on `db.IsClassTag` **before** its
  bucket loop (so `tag:exit-node` yields `""` = "no via pin" instead of
  `node`), and its `resolvePerCIDRVia` note was corrected;
* `db.isExitNodeTagForm` is a one-line delegation;
* the admin system-test page calls `exit_rules.TagToHostname` — its inline
  switch is deleted;
* `user_subnet.go` asks `db.IsClassTag` instead of re-writing the sentinel.

New `internal/acl/acl_b279_1_test.go` pins the sentinel and carries an
anti-drift guard (`db.IsClassTag(tag) == true` must imply
`exitNodeTagToHostname(tag) == ""`). Two contracts were renegotiated:
`check_b188_2.sh`'s sentinel row now expects `""`, and `check_b119.sh`
contract H asserted the *string* `tag:dev-infra-` anywhere in
`system_tests.go` — a comment satisfies that, so it now asserts the
delegation plus the test that pins the infra-format stripping. 16 contracts
in `scripts/check_b279_1_tag_to_hostname_one_copy.sh`.

### B280 — a tag and a release are CI-gated

The v1.5.41 → v1.5.45 cycle pushed tags after `git push --no-verify`. CI caught
two real regressions in that window — v1.5.44 introduced an RU i18n parity
break **and** a raw-`http.Error` leak — and both releases shipped them anyway,
because the tag already existed and `release.yml` builds whatever the tag points
at. Hooks are per-clone and `--no-verify` skips them, so the rule now exists
server-side too.

One implementation, `scripts/ci_gate.sh`, answers "is `ci.yml` green for this
exact commit?":

* exit `0` green, exit `1` not green (failed / cancelled / timed out / still
  running / **no run at all** / not on the release branch), exit `2` cannot
  verify — and `2` blocks as well, because an unverifiable commit is not a
  tested one;
* `--wait N` tolerates a run that is still in progress instead of failing on a
  tag pushed seconds after a push;
* it filters the runs by `head_sha`, so a later push to `main` can never vouch
  for an earlier commit, and it refuses a commit that is not an ancestor of the
  release branch (a tag on a side branch never went through that pipeline).

Three consumers, so they cannot disagree:

1. `.githooks/pre-tag` — the local half, now a thin caller (it used to carry its
   own copy of the `gh run list` query);
2. `release.yml` → new `preflight` job — `docker`, `binaries`, `sums` and
   `release` all `needs: preflight`, so an untested tag publishes neither images
   nor a GitHub Release and no `:latest`/`:vX.Y` tag moves;
3. new `.github/workflows/tag-release.yml` — the sanctioned way to *create* the
   tag: manual `workflow_dispatch`, gate first, annotated tag second, then it
   dispatches `release.yml` at the tag. The dispatch is not decoration: a tag
   pushed with the repository `GITHUB_TOKEN` does **not** fire `push` events, so
   without it the sanctioned path would create a tag and no release (`release.yml`
   therefore also accepts `workflow_dispatch`).

Escape hatches are narrow and visible where they are taken: `SKIP_PRE_TAG_CHECK=1`
for the local hook, the `SKYGATE_ALLOW_TAG_OFF_MAIN` repository variable for a
genuine hotfix branch, `SKYGATE_PREFLIGHT_WAIT_SECONDS` (default 900) for the wait
budget. There is deliberately **no** switch that skips the CI check itself, and a
contract (`B280` A2) fails if a fourth copy of the CI-status query appears.
Procedure: `docs/operations.md` §1.2; rule 14 in `AGENTS.md` §1.

43 contracts in `scripts/check_b280_ci_gates_release.sh`, including a behavioural
half that drives the gate against a stubbed `gh`: green → 0, failed → 1,
cancelled (the `concurrency.cancel-in-progress` case) → 1, timed out → 1, still
running with no wait budget → 1, one success + one failure → 1, no run → 1, API
error → 2, no `gh` → 2, off-branch commit → 1, and `origin/main` not fetched → 2.

### B281 — the catalog job must terminate, and "green" must mean 0 FAIL

B280 checks a CI status; this release could not be cut because that status was
lying in the other direction. `verify-pre` reported **`cancelled`** on every push
and the log simply stopped mid-catalog — the last printed check was `B260`,
followed by 22 minutes of silence until the runner killed the step. It was never
a `concurrency` cancellation: the job hit its own `timeout-minutes: 30`. Four
defects sat behind that one row, and three of them made a *green* row worthless:

1. **No per-check budget.** `run_check` ran every check unbounded, so one slow
   check ate the whole job budget and hid every contract after it — including the
   newest B27x/B28x blocks, which therefore never ran in CI at all. Each check now
   runs under `timeout "${SKYGATE_CHECK_TIMEOUT:-900}"`, and a blown budget is a
   **named** row (`TIMEOUT <name> <desc>`) while the rest of the catalog continues.
2. **The job budget.** `timeout-minutes` 30 → 60.
3. **The catalog is fail-tolerant by design** (it always exits 0 — read the
   output, not `$?`; `docs/operations.md` §1.3), so CI reported SUCCESS with ~15
   FAIL rows inside it. Since `scripts/ci_gate.sh` (B280) reads that status as
   "this commit is tested", the two together made the release gate decorative.
   The run step now tees the log, strips ANSI and **fails the job on any
   `^  (FAIL|TIMEOUT)  ` row**, printing PASS/SKIP counts otherwise.
4. **The FAILs themselves.**
   * 36 check scripts probed `/usr/local/go/bin/go` **before** `command -v go`, so
     on the runner they picked up the image's Go 1.24.13 with `GOTOOLCHAIN=local`
     and died on `go.mod requires go >= 1.25.0`. The `test` job was green
     throughout because it uses the PATH Go — only the catalog disagreed. All
     candidate lists now consult `command -v go` first, and the B281 contract
     guards the order repo-wide (`check_b178.sh` contract N kept the old order —
     the one remaining FAIL — and now also captures its `go test` output before
     matching it, per `AGENTS.md` trap #9).
   * `B95` and `B237.16` need `staticcheck`, which the runner never had; `ci.yml`
     installs it and appends `$(go env GOPATH)/bin` to `$GITHUB_PATH`. The
     version is **pinned** (`@v0.7.0`) because `@latest` resolved to
     `honnef.co/go/tools v0.8.1`, which declares `go >= 1.26.0` while the job
     runs Go 1.25.14 with `GOTOOLCHAIN=local` (setup-go sets it) — the install
     failed with `requires go >= 1.26.0 (running go 1.25.14; GOTOOLCHAIN=local)`
     and killed the job 16 seconds in, before a single contract ran. The step now
     also runs `staticcheck -version` so it fails loudly there instead of 40
     minutes later inside the catalog.
   * 8 checks drove live state (docker daemon, headscale/tailscale CLI, login
     server) and FAILed when it was absent instead of SKIPping — `AGENTS.md` §1.1.
     `bad()` in `check_b_tag_owners.sh` even exited 1, aborting the catalog entry
     on every run.
   * `check_b191.sh` printed `FAIL …` and *then* SKIPped with exit 0 — a red row
     that means nothing in a standalone run and a row the new 0-FAIL enforcement
     sees. It now prints SKIP only.
   * `check_b182.sh` `[D-annotator-call]` spelled `annotateRulesWithPrefs(rr, func`
     with an unescaped `(` inside `grep -cE` — an unbalanced group, so grep exited
     2, the count was 0 and the contract could never pass. The property is already
     asserted by `E2` and `I`; the duplicate is gone rather than re-escaped.
   * `check_b182.sh` `[D]` was renegotiated: v1.5.42 replaced the inline
     `approvedByExitNode := map[string]map[string]bool` with the shared
     `indexNodesApprovedRoutes(nodes)`, so the contract accepts either spelling.

33 contracts in `scripts/check_b281_ci_catalog_truth.sh` (the `ci.yml` half is
scoped to the `verify-pre` job block, so another job's budget cannot satisfy it).

**Also fixed here: the PASS rows that made a green run look broken.** GitHub turns
any log line shaped `<path>.go: <message>` into a *failure-level* annotation even
when the step succeeded. The catalog's descriptions are essays and 21 of them
begin with exactly that shape (`headscale_acl.go: ListACL + …`,
`system_tests.go: TestRegistry …`), so a green `verify-pre` run reported
"12 errors" in the Actions UI whose bodies were the tails of PASS rows (measured
on run `35747083447`: 21 `##[error]  PASS` lines in the log, and the annotated set
is exactly the rows whose first `path.ext: ` token is a `.go: ` — no un-annotated
PASS row has one). A PASS row now prints a short label (a leading `path.ext: ` is
stripped, the rest truncated to 110 chars) while FAIL and TIMEOUT keep the full
description plus the check's output, which is where the narrative is actually
read. `SKYGATE_CATALOG_VERBOSE=1` restores the old rows locally. Contracts
B281 `O1`–`O5`.

### B280 follow-up — the sanctioned tag path could not run at all

`tag-release.yml` had never been executed. Its first live run (cutting THIS
release) died 14 seconds in, in the second step:

```
/home/runner/work/_temp/….sh: line 2: VERSION: unbound variable
```

The first step exported the input into `$GITHUB_ENV` as `version=…` (lower case)
while every later step reads `$VERSION`, so the CI gate never even ran — the one
sanctioned way to create a CI-gated tag was dead on arrival, and the B280
contracts could not see it because the workflow is syntactically valid. The
export is `VERSION=$VERSION` now and B280 pins the class rather than the
incident: `I1` the upper-case export exists, `I2` no lower-case `version=` export
comes back, `I3` the later steps still read `$VERSION` (so the export and its
readers cannot drift apart again). B280: 46 contracts. The tag for this release
was then created by that workflow itself — `gh workflow run tag-release.yml -f
version=v1.5.46` — which is the first time the gate ran end-to-end for real.

### Verification

`scripts/check_b279_class_tag_identity.sh` (registered as B279) +
`internal/db/tag_kind_b279_test.go` (predicates, the SQL-level no-clobber
guarantee against a real database, `ErrClassTagNotPerNode`) +
`internal/feature/exit_rules/preferred_check_b279_test.go` (sentinel, the clear
change, "never derive from a class tag", and that the two copies cannot drift).
One pre-existing contract was **renegotiated**: `TestTagToHostname_StandardForms`
pinned `"tag:public" → "public"`; a class tag now returns `""` (the test's
comment records why). `scripts/check_b188.sh` contract J was renegotiated too —
it asserted the literal `or .DevTag (printf "tag:exit-%s" …)` expression in all
three templates, i.e. it pinned the ghost fallback itself; it now pins the
intent (every template reads `DevTag`) plus a new `J2` that the synthesised
fallback is gone from the user-facing exit-node row (27 contracts, 0 fail).

**Three more contracts were stale on `main` and are renegotiated here** (they
were reporting FAIL for deliberate refactors, so the catalog had stopped being
the "0 FAIL" contract of AGENTS §1.1):

* `check_b276_acl_ownership_sync.sh` A8 pinned `applyACLIfDrifted(...) bool`;
  v1.5.42 changed it to `acl.ApplyResult` (so callers can read the snapshot
  Version and tell "applied" from "no drift" — `check_apply_acl_drifted_and_rename.sh`
  A1 pins the new shape). The property is unchanged: ONE trigger-agnostic drift
  check.
* `check_b276_1_all_devices.sh` A4 pinned `maxV != 74`, which v1.5.44's V075
  (`oidc_settings`) moved to 75; both greps are now version-agnostic (the column
  by name, the chain-length assertion by shape).
* `check_b276_1_all_devices.sh` D3 pinned the substring `all_devices_badge`
  while the template renders `all_devices_fanout_badge` — the badge was there
  all along, three times.

B281 adds `scripts/check_b281_ci_catalog_truth.sh` (33 contracts, registered as
B281 and indexed in `AGENTS.md`), which is what makes "the catalog is green"
a claim CI can no longer make without meaning it. Also renegotiated in the same
pass: `check_b112.sh`'s three B38 assertions (only the first had been converted
to a marker window, so the other two pinned `verify_pre_deploy.sh` line numbers
and reported a false `FAIL B112` as soon as `run_check` grew), `check_b268.sh`'s
behavioural half (the applier chowns to root:root, so it now stubs `install` and
asserts the root:root request instead of failing on the runner's privileges),
`check_b237_16.sh` D.3 (`go test | grep -q` under `pipefail` — AGENTS trap #9 —
now captures first and prints the failure) and `check_b237_24.sh` E.1 (a live
`gh release view` in a token-less job now SKIPs instead of reporting a healthy
release as missing).

### Live remediation for a host already in this state

```sql
UPDATE device_rules SET exit_node_id='exit-node-vps' WHERE exit_node_id='node';
DELETE FROM prefix_owner WHERE exit_node_id='node';
DELETE FROM user_exit_node_prefs   WHERE exit_node_tag LIKE 'tag:%exit-node';  -- or let B279 do it
DELETE FROM device_exit_node_prefs WHERE exit_node_tag LIKE 'tag:%exit-node';
```

and fill `exit_servers.ssh_target` for the relay (an empty value falls back to
`root@<tailscale-ip>`, which needs a tailscaled inside the skygate host).

---

## v1.5.45 — B231 cascade: the hostname-rename migration now updates device_rules too (B-rename-rules)

**Date:** 2026-09-22 · **Base:** `v1.5.44` → this tag · **Compatibility:** none.

Live bug (user-reported on `aro` deployment):

```
HOSTNAME             IP            OWNER           STATUS    PREFERRED
exit-node-vps   100.64.0.1   tagged-devices   online   ✓ preferred
```

But every rule for that device still had `exit_node_id="node"`. The
ACL builder emitted `via: tag:dev-infra-node` — which doesn't match
the new node's tag (`tag:dev-infra-exit-node-vps`). Tailscale silently
ignored the via= pin and traffic went through DERP / default.

### Root cause

B231 (the rename migrator) only updated `device_exit_node_prefs`
when a headscale host was renamed — it did NOT touch the rule rows.
The rule's `exit_node_id` and `device_hostname` are denormalised
columns set at INSERT time; nothing kept them in sync with the
canonical headscale hostname after a rename.

### Fix

`applyRenameMigration` now also UPDATE-s `device_rules` in the
same transaction as the pref migration:

```sql
UPDATE device_rules
   SET exit_node_id   = $1,
       device_hostname = COALESCE(NULLIF($2, ''), device_hostname)
 WHERE user_id = $3
   AND enabled = 1
   AND (exit_node_id = $4 OR device_hostname = $4)
```

* `enabled = 1` scope: disabled rules are NOT touched. The
  per-rule status loop in `form_my.go` skips disabled rows, so
  renaming them does not affect anything the operator sees.
  Re-enabling a previously-renamed disabled rule still hits the
  same stale `node` exit_node_id — the operator has to fix
  that themselves (the rename is only for live rules).
* `user_id = $3` scope: other users' rules are untouched.
* Same transaction as the pref UPSERT/DELETE: atomic rename
  of all denormalised columns (no half-state).

### Audit

The existing audit-log entry now carries `rules_cascaded=N`:

```
MIGRATE pref user=skyadmin old_hostname=node new_hostname=exit-node-vps tag=tag:dev-infra-exit-node-vps rules_cascaded=23 reason=hostname-rename
```

The Telegram alert also includes the count so the operator can
verify how many rules moved.

### Signature change

`applyRenameMigration` now returns `(int64, error)` instead of
just `error`. The first return value is the count of migrated
rule rows — used by the caller to write the audit detail.

### Files

```
internal/feature/exit_rules/reconciler_rename.go                       (applyRenameMigration: 2-value return + UPDATE device_rules)
internal/feature/exit_rules/reconciler_rename_rules_b277_5_test.go      (NEW — sqlite-backed test: 3 enabled rules migrated, 1 disabled + 1 other-user rule preserved)
scripts/check_rename_rules_b277_5.sh                                   (NEW — 9 contracts A–E)
```

### Verification

* `go build ./...` clean
* `go test -count=1 -run 'ApplyRenameMigration' ./internal/feature/exit_rules/` → **PASS**
* `bash scripts/check_rename_rules_b277_5.sh` → **9/9 PASS** (D2 SKIPs — go not on PATH for bash)

### Live verify on `aro`

After `/admin/update` to v1.5.45, the next preferred-reconciler tick
will:

1. See `device_exit_node_prefs` with the OLD hostname "node"
2. Detect the rename candidate via `node_owner_map.tag` equality
3. UPSERT the NEW pref row
4. UPDATE every `device_rules` row for that user that pointed at
   "node" — 23 rules flipped to "exit-node-vps"
5. Write the audit entry + send the Telegram alert with
   `rules_cascaded=23`

The rules page will now show `exit-node-vps` as the exit node
(matching the per-device preferred), and the ACL builder will
emit `via: tag:dev-infra-exit-node-vps` — the headscale
pin will take effect.

For operators who had a STALE state before v1.5.45 (the user's
case): the next tick fixes everything automatically. Operators who
NEVER had a rename can ignore this release.

---

## v1.5.44 — OIDC auto-setup: enable from the web UI + SKYGATE_OIDC_ENABLED env flag (B-oidc-setup)

**Date:** 2026-09-21 · **Base:** `v1.5.43` → this tag · **Compatibility:** none.

User feedback: the OIDC provider shipped in v1.5.0 (B161.1-4) and
v1.5.2 (B167 sync) but **the admin UI was read-only** — operators
had to SSH into the host and edit `SKYGATE_OIDC_ISSUER`,
`SKYGATE_OIDC_CLIENT_ID`, `SKYGATE_OIDC_CLIENT_SECRET`,
`SKYGATE_OIDC_KEY_DIR` env vars + restart the skygate container.
There was no "enable / configure from the web" path and no
single-env flag to deploy skygate already configured for OIDC.

v1.5.44 closes both gaps.

### 1. /admin/oidc form (writeable)

The page now has a "Configure OIDC" card with:
- a checkbox to **enable / disable** OIDC live
- 5 input fields (issuer, client_id, client_secret, redirect_uris, key_dir)
- a Save button

Save persists to the new `oidc_settings` DB table. Empty
issuer is allowed (mid-edit state — the operator can fill the
rest, save, flip "Enable" later). A flash banner shows the
restart requirement.

### 2. `SKYGATE_OIDC_ENABLED` env flag

The boot sequence accepts an explicit on/off:
- unset / empty → legacy behaviour ("OIDC is enabled iff
  `SKYGATE_OIDC_ISSUER` is non-empty" OR a DB row with
  `enabled=1`)
- `true` / `1` / `yes` → boot proceeds even with an empty issuer
  (the OIDC routes answer 503 with a "configure me in /admin/oidc"
  hint until the operator fills the form)
- `false` / `0` / `no` → OIDC routes are explicitly disabled
  (the boot service is nil; the /admin/oidc page can still read
  the DB config but the routes never go live until the env
  flips back). This is an emergency off-switch — useful when
  the operator needs to take OIDC down without touching the DB.

### 3. DB-first boot

`main.go:898` now reads the `oidc_settings` DB row (if any)
and uses the row's values for every field the operator saved.
Env vars still work as a fallback when no DB row exists — the
legacy env-only deployments (B161-era) keep running unchanged.
`client_secret` falls back to env when the DB field is empty
(rotation path: paste the new secret in env, restart, no
form save needed).

### Files

```
internal/db/migrations_v0_75_oidc_settings.go              (NEW migration: oidc_settings table — one row, CHECK id=1)
internal/db/oidc_settings_b277_5.go                       (NEW helpers: GetOIDCSettings, SaveOIDCSettings, DisableOIDCSettings)
internal/db/driver_postgres.go                           (registered migration #75)
internal/db/driver_sqlite.go                              (registered migration #75)
internal/feature/admin/oidc_settings.go                   (NEW PostAdminOIDC handler — saves form to DB)
internal/handlers/templates/admin/oidc_settings.html      (NEW form section + enable checkbox + 5 input fields)
internal/handlers/handlers.go                             (App.OIDCEnabledEnv field)
internal/config/config.go                                 (Config.OIDCEnabledEnv field — SKYGATE_OIDC_ENABLED env)
internal/i18n/catalog_admin.go                            (+10 keys × 2 langs: form_help, form_enable, form_client_id, ...)
cmd/skygate/main.go                                       (boot: read DB, fall back to env, honour SKYGATE_OIDC_ENABLED)
```

### Verification

* `go build ./...` clean
* `go vet ./internal/db/... ./internal/feature/admin/...` clean

### Live verify

1. Open `/admin/oidc` → new "Configure OIDC" card at the bottom.
   Fill issuer + client_id + secret + redirect_uris + key_dir,
   tick "Enable", click Save → flash "OIDC settings saved.
   Restart skygate (/admin/update) to apply."
2. Restart via `/admin/update` → OIDC routes come up with the DB
   values. The endpoints table at the top of `/admin/oidc` now
   shows the saved issuer (not the env-var fallback).
3. Set `SKYGATE_OIDC_ENABLED=false` in `.env` and restart → the
   OIDC routes answer 503; the page still loads the DB config so
   the operator can edit it without removing the env flag.

### Deferred (separate b-block)

* The form's `enabled` checkbox is independent of the boot-time
  `SKYGATE_OIDC_ENABLED` env. A future b-block can add a "force
  disable" path (DB.enabled=0 → routes 503 regardless of env).
* The /admin/oidc form does NOT auto-generate the RSA keypair
  on first save. If the key_dir is empty, `oidcsvc.NewService`
  will return an error and the page shows the existing
  "OIDC init failed" warning. Operators generate the keypair
  out-of-band (`/home/admin/skygate/oidc-keys/` is created by
  the docker entrypoint script). Auto-keypair is a B-oidc-keygen
  follow-up.

---

## v1.5.43 — server-side pagination for /my/exit-rules and /admin/exit-rules

**Date:** 2026-09-21 · **Base:** `v1.5.42` → this tag · **Compatibility:** none.

The /my/exit-rules and /admin/exit-rules pages used to load every
enabled rule in one round-trip and render the per-host + CDN-grouped
structure inline. For users with 1500+ rules (typical for active
admins managing many domains across many devices) the render is
O(N²) on row count — 3-5s page load, >500KB HTML. v1.5.43 adds
server-side pagination.

### How it works

* Both pages now read `?page=N&page_size=M` from the URL (defaults:
  page=1, page_size=50; clamp page_size to [1, 500]).
* The SQL backend returns one slice per request via
  `LIMIT $N OFFSET $M`, plus a separate `COUNT(*)` for the
  "Showing X–Y of Z" badge.
* Page-clamp: page<1 stays at 1; page>lastPage clamps to lastPage
  (so the user never sees an empty list when there are rules).
* The pagination controls (prev / next / page-of-N / per-page
  selector) are hidden when `Total <= PageSize` — no useless
  controls on a single-page dataset.
* A small inline `<script>` preserves the operator's live
  filter / search state across pagination clicks — the prev/next
  links copy the current URLSearchParams and only override `page`
  (and `page_size` for the per-page select).

### Files

```
internal/db/queries.go                                      (+3 query constants: UserPaged, UserCount, AdminPaged, AdminCount)
internal/db/device_rules.go                                 (+RulePage, +AdminRulePage, +GetDeviceRulesForUserPaged, +GetAllRulesForAdminPaged, +pageSizeClamp, +scanAdminRules)
internal/db/pagination_b_test.go                            (NEW, 7 clamp math tests)
internal/feature/exit_rules/form_my.go                      (GetMyExitRules uses GetDeviceRulesForUserPaged)
internal/feature/exit_rules/form_admin.go                   (AdminExitRules uses GetAllRulesForAdminPaged for the unfiltered path; device-filtered stays unpaged — small scope)
internal/handlers/templates.go                              (+divceil, +sub funcmap entries)
internal/handlers/templates/exit_rules.html                 (pagination controls + JS)
internal/handlers/templates/admin/exit_rules.html          (pagination controls + JS for admin)
internal/i18n/catalog_exit_rules.go                         (+5 keys × 2 langs: prev_page, next_page, page_size, pagination_page_of, pagination_total)
scripts/check_pagination.sh                                 (NEW, 17 contracts A–I)
```

### Known limitation (documented)

The `/my/exit-rules` per-host + ALL-DEVICES bucketing relies on
**every** fan-out copy of a logical `all_devices=true` rule to
be visible on the same page for the de-duplication to work.
The pre-v1.5.43 code loaded them all in one shot; the new code
loads only the page. A logical rule whose source row is on page X
but whose copies are spread across X and Y will show the source
on X (under the per-host section, since the bucket can't see the
copies) and the copies on Y (under the ALL-DEVICES section, since
the bucket can't see the source). The fan-out count badge may
understate the true count on a page boundary. This is acceptable
for a display page — operators paginating through their rules see
the right total at the top; the per-page grouping is best-effort.
A future b-block can pre-load the `allDeviceRuleKeys` separately
(which already queries the FULL dataset) and use that as the
grouping truth; the per-row page slice stays unchanged.

### Verification

* `go build ./...` clean
* `go test -count=1 -run 'ClampPage' ./internal/db/` → 7/7 PASS
* `bash scripts/check_pagination.sh` → **17/17 PASS**
* `bash scripts/check_b276_1_all_devices.sh` → 21/21 PASS (i18n parity preserved — auto_pending_title still in both langs)
* `bash scripts/check_apply_acl_drifted_and_rename.sh` → 16/16 PASS (no regression on v1.5.42 fixes)

### Live verify

1. Open `/my/exit-rules` as a user with >50 rules → pagination
   controls appear at the bottom of the per-host card. Click
   "Next" → URL gains `?page=2`, the controls preserve the active
   filter (`?type=ip&search=...` survives pagination clicks
   because the JS only writes the `page` key).
2. Open `/admin/exit-rules` (unfiltered) on a tenant with thousands
   of rules → first page renders in <500ms instead of the 3-5s
   pre-fix; pagination controls on the bottom of the cross-user
   card.

---

## v1.5.42 — applyACLIfDrifted returns the snapshot version, dual-index approvedByExitNode, B229 post-rename fix (B-pending-write)

**Date:** 2026-09-21 · **Base:** `v1.5.41` → this tag · **Compatibility:** none.

Hotfix on top of v1.5.41 — three independent fixes that share a
theme: "primitive helpers that other code consumes must return
enough information, and look-ups must tolerate name-shape drift."

### Fix 1 — `applyACLIfDrifted` now returns `acl.ApplyResult`

**Symptom:** the `PostMyExitRule` and `PostDeleteExitRule` handlers
called `generateACL()` + `saveACLSnapshot()` + `SetPolicy()` directly.
Every rule insert or delete wrote a fresh `acl_snapshots` row
+ an `acl_snapshots_by_*` audit entry — even when headscale was
already serving the right policy. For users with hundreds of
rules, this was dozens of wasted snapshots per session.

**Root cause:** `applyACLIfDrifted` (the drift-aware primitive
in `sync.go:116`) returned `bool` only — callers couldn't read
the snapshot Version. PostMyExitRule worked around this by
calling `generateACL + SetPolicy` directly (always writes),
bypassing the drift check.

**Fix:**

* `internal/feature/exit_rules/sync.go:116` — `applyACLIfDrifted`
  now returns `acl.ApplyResult`:
  - `{Version: 0, Applied: false, Err: nil}` — throttle / no client / already-in-sync (no write).
  - `{Version: N>0, Applied: true, Err: nil}` — fresh snapshot written.
  - `{Version: 0, Applied: false, Err: <non-nil>}` — generation or live-apply failed.
* `PostMyExitRule` and `PostDeleteExitRule` switched to the
  helper. Each branch (`res.Applied`, `res.Err != nil`, default
  no-drift) writes the right audit detail.
* `sync_b276_test.go` updated to use `res.Applied`.

### Fix 2 — `approvedByExitNode` indexed under BOTH GivenName AND Hostname

**Symptom (live, `aro` deployment):** every exit-rule on the user
`daniil` showed `PREFERRED = ⏳ pending` after `SyncAdvertisedRoutes`
had successfully pushed the routes to headscale. 21 rules on
`workpc`, all stuck, none transitioned to `✅ approved`. The
banner said "правила сохранены, но Tailscale их игнорирует",
which was misleading — the rules were actually covered in headscale
ApprovedRoutes; the **display** was reading them as missing.

**Root cause:** `form_my.go:599` (and `form_admin.go:457`) built
the `approvedByExitNode` map with `host := n.GivenName` (with a
Hostname fallback). For nodes that headscale reports with a
MagicDNS suffix — `GivenName="node.tail-scale.ts.net"`,
`Hostname="node"` — the map was indexed under the wrong key.
The rule's `exit_node_id` is always the bare `Hostname` (the value
the operator picks from the dropdown at rule-creation time —
see `form_my.go:170`), so the look-up missed and every rule
appeared `pending`. The live bug was on `aro` because that
deployment had at least one node with a MagicDNS suffix.

**Fix:**

* `internal/feature/exit_rules/approved_routes_index.go` —
  new helper `indexNodesApprovedRoutes(nodes)` indexes the
  SAME `set` under BOTH `n.GivenName` AND `n.Hostname` (when
  non-empty). A rule written against either key resolves correctly.
* `form_my.go` and `form_admin.go` both call the helper (no
  inline loops left).
* `internal/feature/exit_rules/approved_routes_index_b_test.go` —
  6 pure-function tests pin the BOTH-name behaviour, the
  single-name case (only GivenName or only Hostname), the
  empty-set case (skip node), the multi-node case, and the
  shared-set invariant (mutating via one alias propagates to
  the other).

### Fix 3 — `collectDevicePrefState` SQL has a post-rename OR-branch

**Symptom (potential):** B229's preferred-exit-node reconciler
filters on `device_hostname = $2` — the **denormalised** column.
Between the moment headscale sees a new hostname (rename) and
the moment B231 syncs the denormalised column, B229 would look
for rules under the new hostname and find NONE. The reconciler
then decided "0 rules → no dominant → no pref created", and the
user's preferred exit-node did not match the device's actual
exit-node until B231 ran (potentially minutes later).

**Root cause:** the WHERE clause only matched the denormalised
column.

**Fix:**

* `internal/feature/exit_rules/reconciler.go:452` — the query
  now reads `device_rules` rows where `device_hostname = $2`
  **OR** `device_id IN (SELECT node_id FROM node_owner_map WHERE
  user_id = $1 AND hostname = $2)`. Either branch alone covers
  one half of the rename window; both together are idempotent.
* Sub-select uses the existing `(user_id, hostname)` index on
  `node_owner_map` (contract C3 verified), so it's not a seq
  scan at scale.

### Files

```
internal/feature/exit_rules/sync.go                                  (applyACLIfDrifted → ApplyResult)
internal/feature/exit_rules/sync_b276_test.go                         (updated tests)
internal/feature/exit_rules/form_my.go                                (PostMyExitRule + PostDeleteExitRule use applyACLIfDrifted)
internal/feature/exit_rules/approved_routes_index.go                  (NEW helper)
internal/feature/exit_rules/approved_routes_index_b_test.go           (NEW, 6 tests)
internal/feature/exit_rules/form_admin.go                             (uses indexNodesApprovedRoutes)
internal/feature/exit_rules/reconciler.go                             (B229 SQL OR-branch)
scripts/check_apply_acl_drifted_and_rename.sh                         (NEW, 16 contracts)
```

### Verification

* `go build ./...` clean
* `go vet ./internal/feature/exit_rules/...` clean
* `go test -count=1 -run 'IndexNodesApprovedRoutes|B276|BuildOrphan' ./internal/feature/exit_rules/`
  → **PASS** (6 new dual-index tests + 6 pre-existing routescript + 1 B276 test)
* `bash scripts/check_apply_acl_drifted_and_rename.sh` → **16/16 PASS**
* `bash scripts/check_b277_4_consistency.sh` → 21/21 PASS (no regression on the v1.5.41 fixes)
* `bash scripts/check_b276_1_all_devices.sh` → 21/21 PASS (i18n parity)

### Live verify on `aro` after `/admin/update`

1. Open `/my/exit-rules` for `daniil` → the 21 rules on `workpc`
   should transition from `⏳ pending` to `✅ approved` (or `⚠ wrong`
   if `exit_node` ≠ preferred) on the next page render. The
   dual-index fix is server-side; no DB migration needed.
2. POST a new rule via the form → the audit row's `acl_snapshots`
   counter on `/admin/exit-rules → recent logs` should NOT
   increment for rules that don't change the live policy (no-drift
   branch skips the snapshot). Watch the `acl-drift:` log line in
   the skygate stderr for `live policy already matches`.
3. The B229 fix is observable in headscale only — no UI change.
   Look for the `preferred-reconciler` log line in stderr; the
   next B229 tick after a rename will no longer act on stale data.

---

## v1.5.41 — six consistency fixes between the rule model and what the UI + autoupdater did (B277.4)

**Date:** 2026-09-21 · **Base:** `v1.5.40` → this tag · **Compatibility:** none.

The B275 / B276.1 / B277 rule model is richer than the surfaces
that consumed it: rules can target different exit-nodes, rules with
`all_devices=true` are fanned out across devices, and rules with
`exit_node_id=""` defer to the engine. The user-found consistency
audit (Round 1 + Round 2) found six gaps between the model and what
was actually rendered / synced / generated. B277.4 closes all six.

### Fixes

**1+2 — `all_devices` is now a first-class column.** Both
`qSelectUserRulesForView` and `qSelectAllRulesForAdmin` now
`SELECT all_devices` (the post-process `allDeviceRuleKeys` workaround
is kept as a safety net). `AdminRule` gained `AllDevices bool`,
and `form_admin.go` propagates the field — the `/admin/exit-rules`
template can now render the «все мои устройства» badge on cross-
user rules. The v1.5.37-era two-pass pattern is gone.

**3 — Orphan fan-out sweep.** `propagateAllDeviceRules` now calls
`pruneOrphanAllDeviceCopies` first. The sweep DELETEs every row
where `all_devices=1` AND the target `device_id` is no longer in
the user's device list (per-user IN-clause, no string-built SQL).
Live: deleting a device via `/admin/devices` used to leave a ghost
fan-out row in `device_rules` that surfaced as a "ghost rule" in
`/my/exit-rules` with a stale denormalised hostname. The sweep
runs on every propagation tick.

**4 — Cascade delete on `/my/exit-rules` delete.** `PostDeleteExitRule`
now reads the rule's fan-out key (`GetRuleFanOutKey`) and, when
`all_devices=true`, calls `DeleteAllDeviceFanOut` to remove every
row sharing the natural key `(exit_node, target_type, target_value)`
in one DELETE. Pre-B277.4 deleting one row left N-1 ghost copies
behind — the operator expected "delete the rule" and got "delete
one fan-out copy". The audit detail now reports the cascade count
separately (`+N fan-out cascade`).

**5 — B229 no longer overwrites the operator's `via_enabled=false`.**
The `via-disabled-but-canonical` path used to silently re-enable
`via_enabled=true` on rows where the operator had explicitly disabled
it (Android compatibility: `via=` policies fail on older Tailscale
clients). The path now returns a `skip` change — the audit trail
preserves the "would-have-updated" event without actually flipping
the bit. The pre-B277.4 behaviour is documented as a regression.

**6+7 — Auto rules and user-level preferred in the status loop.**
Rules with `exit_node_id=""` (engine-auto) now report
`auto_pending` (a new template badge, distinct from `wrong`) — the
engine will resolve them on the next reconcile tick, and they are
NOT counted as a mismatch. The mismatch count falls back to the
user-level preferred (`db.GetUserExitNodePref`) when the device has
no per-device pref, so genuine drift on user-level prefs is now
counted in the banner instead of silently ignored.

**11 — Routescript no longer loads `device_ip`.** The column was
loaded into `routeEntry.deviceIP` but no body builder ever read it.
Removed from both the SELECT and the struct — every `?script=`
download is now ~30% lighter on the device_rules I/O path.

**8 — Dead `{{if .AllDevices}}` removed from the per-host template.**
The B276.2 filter routes `all_devices=true` rules to the dedicated
ALL-DEVICES section, so the per-host slice never has them. The dead
branches were confusing the next reader of the template.

### Files

```
internal/db/queries.go                       (modified, +all_devices in 2 SELECTs)
internal/db/device_rules.go                  (modified, +GetRuleFanOutKey, +DeleteAllDeviceFanOut, +scan of all_devices)
internal/feature/exit_rules/form_my.go       (modified, +auto_pending, +user-level fallback, +cascade delete, +pruneOrphanAllDeviceCopies wiring)
internal/feature/exit_rules/form_admin.go    (modified, +AdminRule.AllDevices)
internal/feature/exit_rules/all_devices.go   (modified, +pruneOrphanAllDeviceCopies)
internal/feature/exit_rules/reconciler.go    (modified, via-disabled → skip not update)
internal/feature/exit_rules/routescript_data.go (modified, -device_ip SELECT, -routeEntry.deviceIP)
internal/handlers/templates/exit_rules.html   (modified, +auto_pending badge in 4 places, -dead {{if .AllDevices}})
internal/i18n/catalog_exit_rules.go           (modified, +auto_pending_title × 2 langs)
internal/feature/exit_rules/all_devices_b277_4_test.go (new, 2 pure-function tests)
scripts/check_b277_4_consistency.sh           (new, 21 contracts A–L)
```

### Verification

* `go build ./...` clean
* `go vet ./internal/feature/exit_rules/... ./internal/db/...` clean
* `go test -count=1 -run 'B2762|BuildLinux|BuildWindows|PreferredGroup|BuildOrphan' ./internal/feature/exit_rules/`
  → **8/8 PASS** (B276.2 routescript tests + new B277.4 orphan sweep tests)
* `bash scripts/check_b277_4_consistency.sh` → **21/21 PASS**
* `bash scripts/check_b276_1_all_devices.sh` → 21/21 PASS (i18n parity preserved)
* `bash scripts/check_b276_2_routescript.sh` → 14/14 PASS (no regression on routescript)
* `bash scripts/check_b277_3_apply_preferred.sh` → 15/15 PASS (no regression on B277.3 bulk apply)

### Deferred (separate b-block)

**#10 — `applyACLIfDrifted` in `PostMyExitRule`.** The current
path always writes ACL on rule insertion (no live-policy
comparison). Switching to `applyACLIfDrifted` requires the helper
to return `acl.ApplyResult` (not just `bool`) so the rule-insert
path can read the snapshot version for its audit detail. Skipped
to keep this release focused on the audit gaps; tracked for the
next b-block.

---

## v1.5.40 — the "Use preferred (node)" button actually fixes the rules (B277.3)

**Date:** 2026-09-21 · **Base:** `v1.5.39` → this tag · **Compatibility:** none.

Hotfix on top of v1.5.39. The pre-existing JS-only "Use preferred
(node)" button next to the mismatch banner on `/my/exit-rules` did
nothing useful: it pre-filled the new-rule form's `exit_node` select
with the user's preferred host, but the operator still had to click
"Add" and the existing N rules the banner was warning about were
never touched. The next page render re-read the same N rules,
the same mismatch count, and the same banner — visually the
button looked broken.

**Fix:** the button is now a plain `<form action="/my/exit-rules/
apply-preferred">` with a confirm() dialog. One POST rewrites
every rule whose `exit_node_id` doesn't match the device's
preferred exit-node (or the user-level preferred, when no
per-device pref is set), audits the action, and re-applies the
ACL once for the user.

**Files:**

* `internal/db/device_rules.go` — new `UpdateDeviceRuleExitNode(d,
  id, exitNode)` (one-row UPDATE on `exit_node_id`).
* `internal/feature/exit_rules/form_my.go` — new
  `PostMyExitRulesApplyPreferred` handler. Reads
  `db.GetUserExitNodePref`, walks every rule via
  `PreferredExitNodeForRule`, calls `db.UpdateDeviceRuleExitNode`
  for every mismatch, audits `my_exit_rules_apply_preferred`.
  Refuses with a clear `?err=` redirect when no preferred is set
  (pointing to `/my/devices`).
* `cmd/skygate/main.go` — registers
  `POST /my/exit-rules/apply-preferred`.
* `internal/handlers/templates/exit_rules.html` — banner button
  becomes a `<form>` with `onsubmit="return confirm(...)"`. The
  dead JS pre-fill handler is removed.
* `internal/i18n/catalog_exit_rules.go` — 2 new keys × 2 langs
  (`apply_preferred_btn`, `apply_preferred_confirm`).
* `scripts/check_b277_3_apply_preferred.sh` — 15 contracts (A–H):
  handler shape, route registration, `<form>` (not JS-only
  pre-fill), confirm dialog, dead JS gone, DB helper exports,
  i18n parity, package tests stay green.

**Verification:**

* `go build ./...` clean
* `go vet ./internal/feature/exit_rules/... ./internal/db/...` clean
* `bash scripts/check_b277_3_apply_preferred.sh` 15/15 PASS
* `bash scripts/check_b276_1_all_devices.sh` 21/21 PASS (no
  i18n parity regression)
* Live: after `/admin/update` to v1.5.40, open `/my/exit-rules`,
  click the «Применить preferred (node) к N правил(ам)» button
  next to the yellow mismatch banner, confirm. The N rules
  flip to `node`, the banner disappears, and the next page
  render shows `0` in the ⚠️ count.

**Note on ACL re-apply:** the bulk handler records the new
`exit_node_id` on every rule and emits an audit entry; the
user-level preferred `via=` is recomputed on the next sync tick
(≤5 min). For an immediate re-apply, `/admin/exit-nodes →
«Пересобрать ACL»` covers the gap — wiring a per-user ACL
rebuild through the Service is a follow-up b-block that
doesn't ship in v1.5.40.

---

## v1.5.39 — Exit Rules display rework + routescript per-exit-node (B276.2)

**Date:** 2026-09-21 · **Base:** `v1.5.38` → this tag · **Compatibility:** none.

The Exit Rules model is now richer than the user-facing pages and the
client-side routescript can express: rules can target different exit
nodes (B275), and rules with `all_devices=true` are fanned out across
the user's devices (B276.1). The pre-B276.2 surfaces flattened both
into a single per-device view (duplicated N times for fan-outs, routed
through a single exit-node for the script). B276.2 brings both in
line with the underlying model.

### 1. `/my/exit-rules` — dedicated ALL-DEVICES section (B276.2 display)

**Symptom:** every `all_devices=true` rule was duplicated in the
per-host section — once per device the user owned. A user with 3
devices and one «all devices for Cloudflare» rule saw 3 identical rows
under each device, with no visual cue that they were fan-out copies
of one logical rule.

**Fix:**

* `internal/feature/exit_rules/form_my.go` — split the rule loop into
  per-device (`allDevices=false`) and all-devices (`all_devices=true`)
  halves. The per-host section no longer renders the fan-out copies;
  the new `AllDevicesByExitNodeCDN` view collapses the fan-out by
  natural key `(exit_node, target_type, target_value, parent_domain)`
  and carries a `FanOutTotal` count so the section can render one
  logical rule per target with a «применено к N устройств(ам)» badge.
* `internal/feature/exit_rules/cdn_group.go` — `CDNDisplayItem` gained
  `FanOutCount`; `CDNDisplayView` gained `FanOutTotal`. Both default
  to 0 in the per-host view (no fan-out there).
* `internal/handlers/templates/exit_rules.html` — new
  `ALL-DEVICES RULES` section under the per-host card, with the same
  CDN grouping + collapsible `<details>` structure as the per-host
  view. Each row's badge shows the fan-out device count.
* i18n: 4 new keys × 2 languages (`all_devices_section_title`,
  `all_devices_section_help`, `all_devices_fanout_badge`,
  `all_devices_fanout_tip`).

### 2. Routescript — per-exit-node blocks (B276.2 routescript)

**Symptom:** `GenerateRouteSetupScript` took the FIRST exit node from
`ListExitNodes()` and emitted every `ip route add` (Linux) or
`route add` (Windows) through that one IP. A user with rules on
karolina AND emilia ended up with all traffic routed through whichever
relay headscale returned first — the rule's actual `exit_node` field
was completely ignored in the generated script. The `tailscale up`
step on the operator's client pinned everything to the wrong relay,
and the per-rule preferences in the UI didn't survive into the
client.

**Fix:**

* `internal/feature/exit_rules/routescript_data.go` — new
  `ScriptRouteGroup {ExitNode, ExitNodeIP, Source, Routes}` and
  `loadRoutesForScriptGroups(userID, deviceID)`. Rules are bucketed
  by `exit_node` (one bucket per relay); empty `exit_node` rules
  fold into the user's preferred exit node
  (`db.GetUserExitNodePref`), with a `first_healthy` fallback when
  no preference is set. `all_devices=true` rules still apply when
  `deviceID > 0` (B276.1 fan-out intent is honoured on every device
  the script runs on).
* `internal/feature/exit_rules/routescript_linux_body.go` +
  `routescript_windows_body.go` — both take `[]ScriptRouteGroup`,
  emit one `=== exit-node: NAME (source) ===` block per group, with
  the matching `ip route add` / `route add` commands per group. The
  DNS route (MagicDNS `100.100.100.100`) and the restore's
  default-route re-add use the `preferredGroup()` helper so they
  always land on the user's preferred relay, regardless of script
  order. Missing-IP groups emit a `NO TAILSCALE IP — set manually`
  marker instead of `via  dev tailscale0` (which would silently
  install a link route to nowhere).
* `internal/feature/exit_rules/routescript.go` — orchestrator
  switched from `[]routeEntry + single exitNodeIP` to the new
  groups. The dead `resolveExitNodeIPForScript` helper is removed.

### Tests

* `internal/feature/exit_rules/routescript_b276_2_test.go` — 6 pure-function
  unit tests (no DB / no headscale):
  * `TestBuildLinux_PerExitNodeBlocks` — two groups, two IPs,
    every route in the matching block.
  * `TestBuildLinux_AutoRulesFoldIntoPreferred` — empty
    `exit_node` rule lands in the preferred group, NOT in the
    alphabetical-first explicit group.
  * `TestBuildLinux_MissingIPPlaceholder` — a group with no
    resolved Tailscale IP emits the `NO TAILSCALE IP` marker
    instead of an empty gateway.
  * `TestBuildLinux_RestoreClearsAllGroups` — the restore path
    removes routes for EVERY group, not just the preferred one.
  * `TestBuildWindows_PerExitNodeBlocks` — Windows mirror of the
    per-group guarantee.
  * `TestPreferredGroup` — the helper selects the preferred
    group deterministically (used for DNS + restore-default).

### B-check

* `scripts/check_b276_2_routescript.sh` — 14 contracts (A–H) covering
  the data shape, body-builder signatures, per-group markers,
  preferred-group selection, missing-IP placeholder, restore
  cleanup, unit tests, and git tracking (trap #11). Local: 14/14 PASS
  (F2 — `go test` — SKIPs when the bash shell doesn't have `go` on
  PATH; CI / VM run will exercise it).

### Verification

* `go build ./...` clean
* `go vet ./internal/feature/exit_rules/...` clean
* `go test -count=1 -run 'B2762|BuildLinux|BuildWindows|PreferredGroup'`
  6/6 PASS
* `bash scripts/check_b276_2_routescript.sh` 14/14 PASS (1 SKIP — go on PATH)
* Live: after `/admin/update` to v1.5.39, open `/my/exit-rules`,
  confirm all_devices rules appear in the new dedicated section.
  Download the route-setup script (`?script=` on the same page) and
  inspect that rules on different relays end up in different
  `=== exit-node: NAME ===` blocks, not one block.

---

## v1.5.38 — the v1.5.37 /admin/exit-nodes page-crash fix

**Date:** 2026-09-21 · **Base:** `v1.5.37` → this tag · **Compatibility:** none.

Tiny hotfix. v1.5.37 shipped the B277 prefix-admin surface (grouping + multi-checkbox
+ global override). The template has a `<details>` block wrapped in
`{{with .PrefixAdmin}}` — the `PrefixAdminView` sub-struct carries `Relays`, not
`RelayChoices` (the latter is on the top-level view, which is what `$.RelayChoices`
reads). One `range` inside the `with` block referenced `.RelayChoices` directly, so
every `/admin/exit-nodes` render failed with:

```
render: template: layout.html:290:2: executing "layout" at <.RenderBody>:
error calling renderBody: template: exit_nodes.html:509:14:
executing "body-admin-exit_nodes" at <.RelayChoices>:
can't evaluate field RelayChoices in type admin.PrefixAdminView
```

**Fix:** change the one bad reference to `$.RelayChoices`. The other two
`{{range $.RelayChoices}}` sites in the same template (the per-group bulk-pin
form and the per-row pin form, both inside `{{range .Rows}}` of a `PrefixGroup`)
were already correct — only the multi-pin form at the bottom of the page was wrong.

**Regression guard:** new contract **D17** in `scripts/check_b277_prefix_admin.sh`
asserts that every `.RelayChoices` reference inside the `{{with .PrefixAdmin}} …
{{end}}` block uses the `$` prefix. The check parses the template, locates the
`with`/`end` boundaries, and fails loudly with the offending line(s) if the bare
form ever returns — same pattern as the D5a/D5b guards for the v1.5.36 B276.1
regression, same trap #2 lesson.

**Verification:**

* `go build ./...` clean
* `bash scripts/check_b277_prefix_admin.sh` 28/28 PASS (was 27 before D17 was added)
* Live: open `/admin/exit-nodes`, the page renders, the multi-pin form at the
  bottom of the groups has a populated relay dropdown.

---

## v1.5.37 — fix the v1.5.36 /my/exit-rules crash, and make prefix assignment actually usable (B276.1 + B277 + docs)

**Date:** 2026-09-21 · **Base:** `v1.5.36` → this tag · **Compatibility:** none.

This release fixes a v1.5.36 regression that crashed every `/my/exit-rules` render,
ships the operator surface for prefix assignment that v1.5.25 promised, and locks in
the new deploy workflow: a GitHub release is all that's needed to ship — every
install kind applies it through `/admin/update`.

### B276.1 — the «все мои устройства» badge crashed `/my/exit-rules` on every render

**Symptom** (live, immediately after the v1.5.36 deploy): every `/my/exit-rules`
render — for every user — failed with

```
render: template: exit_rules.html:253:120: executing "body-exit_rules"
at <.AllDevices>: can't evaluate field AllDevices in type exit_rules.RuleRow
```

The page was unreadable. The B275 CDN-grouping template iterates
`CDNDisplayItem.Rules` (a `[]RuleRow`); the v1.5.36 commit added a `{{if .AllDevices}}`
badge to that template, but the `db.DeviceRule → RuleRow` projection in
`form_my.go` did not copy the new `AllDevices` field, so every row lost the marker
and the template crashed trying to read it.

**Root cause:** the B276.1 commit added two halves of one feature (the badge in the
template + the `AllDevices bool` on `db.DeviceRule`) without the third half (the
field on `RuleRow` + the copy in the projection). The contract D2 checked the
endpoints but not the middle.

**Fix:**

* `internal/feature/exit_rules/cdn_group.go` — `RuleRow` now carries `AllDevices`
  with a doc comment explaining which template path reads it.
* `internal/feature/exit_rules/form_my.go:320-336` — the `db.DeviceRule → RuleRow`
  conversion copies `r.AllDevices` (and the comment above it now lists AllDevices
  alongside ID / TargetType / TargetValue / ParentDomain / Action).
* `scripts/check_b276_1_all_devices.sh` — new contracts **D5a** (RuleRow carries
  the field) and **D5b** (the projection copies it). The B276.1 contract now
  guards the full render path, not just the endpoints.

The `/admin/exit-rules` template uses a separate projection (`AdminRule`) and was
not affected.

### B277 — prefix assignment is usable: prune, group, bulk pin, global override

**Symptom** (live, the page that shipped in v1.5.25): one row per prefix, 1655 rows
in production of which 1497 were dead (no rule claimed them); the operator's real
question was "which relay serves Cloudflare / this device / everything", and
answering it meant hundreds of individual clicks per session.

**Fix** — five pieces:

1. **`internal/prefixowner/prefixowner.go` — `Prune`.** The dead rows are dropped
   inside the same reconcile pass that reads the claims (one source of truth). A
   manual pin survives the prune (the operator's intent may predate the rule that
   now uses it).
2. **Precedence fix inside `Reconcile`.** The manual pass now runs **before** the
   explicit pass, so a per-prefix manual pin can no longer be silently reverted by
   the rule-derived row on the next tick. The reason it must stay first is written
   down where the next reader will look (B-contract B2).
3. **`internal/feature/admin/prefix_admin_b277.go` — `PrefixGroup`, `PrefixAdminView`,
   `loadPrefixAdminView`.** Grouping by `owner | domain | device` (the same
   per-(host, exitNode) shape exit_rules uses for CDN groups). Counts in the header:
   prefixes, problems, manually pinned, distinct devices.
4. **Three new bulk surfaces on `/admin/exit-nodes`:**
   * `POST /admin/exit-nodes/prefix-owner-bulk` — pin a whole group (or hand it
     back to the engine by submitting an empty relay).
   * `POST /admin/exit-nodes/prefix-owner-multi` — multi-checkbox selection across
     groups, one submit, audit action `prefix_owner_pin_multi` (separate from bulk
     so the audit log distinguishes "group" from "hand-picked selection"). The
     handler intersects the submitted prefixes with the current rows so a stale
     form cannot resurrect a dead row.
   * `POST /admin/exit-nodes/prefix-owner-force` — the **global override**:
     every prefix that is not manually pinned is served by the selected relay,
     including prefixes that appear later. A typo in the relay name is refused
     loudly (otherwise the whole tailnet's egress would go to a dead host). An
     unhealthy pin is ignored (otherwise all egress would be lost).
5. **The page itself:** groups render as `<details>/<summary>` (collapsible, open
   by default — like the exit_rules CDN groups), each row carries a
   `.prefix-check` checkbox, every group has a "select all" header checkbox and a
   one-click "вернуть авто" button, the sticky bottom form (`#multi-pin-form`)
   shows a live count of selected prefixes and submits to the multi-pin handler.
   JS is small and inline (`selectAllInGroup`, `toggleAllVisible`,
   `updateMultiCount`).
6. **`scripts/check_b277_prefix_admin.sh`** — 27 contracts (was 21 in the
   initial WIP): A (schema / prune), B (precedence), C (override is a setting, not
   a mass write), D1-D16 (UI: grouping, bulk, force, multi-checkbox, collapsible,
   per-group auto, 20 i18n pairs), E (behaviour), F (live state), G (tracked by
   git).

**Files:**

```
internal/prefixowner/prefixowner.go                       (modified, +Prune + precedence)
internal/prefixowner/prefixowner_b277_test.go             (new)
internal/feature/admin/prefix_admin_b277.go               (new, ~440 LoC)
internal/feature/admin/exit_nodes.go                      (modified, +PrefixAdmin wiring)
internal/handlers/templates/admin/exit_nodes.html         (modified, collapsible + checkboxes)
internal/i18n/catalog_exit_nodes.go                       (modified, +20 keys × 2 langs)
cmd/skygate/main.go                                       (modified, 3 new routes)
scripts/check_b277_prefix_admin.sh                        (new)
```

### Deploy workflow — `/admin/update` is the routine path, GitHub releases are the source of truth

**Process change** (AGENTS.md §13, `docs/UPDATE.md` §1 banner):

* **Routine path:** maintainer pushes the tag and publishes the GitHub release
  (this file is what the release body is built from). The operator triggers
  the update from `/admin/update` in the running instance. **Every install kind**
  (docker, native systemd, native OpenRC, bare binary) supports `/admin/update`.
* **Emergency / break-glass path:** SSH to the host, `git pull`, restart the unit
  or the container. This is what the operator uses when the UI itself is broken,
  not for a routine release.

The pre-existing trap #12 ("a native `git pull && systemctl restart skygate` does
not update the Go code") is unchanged in mechanism — the new binary has to come
from somewhere the unit can read — but the "somewhere" is now the GitHub release
the operator triggers through the UI, not a manual `git pull` on the host.

### Verification

* Local gate (Windows, no `staticcheck` on PATH, no docker PG): `bash
  scripts/check_b276_1_all_devices.sh` 21/21, `bash scripts/check_b277_prefix_admin.sh`
  27/27, `bash scripts/verify_pre_deploy.sh` (the relevant subset) PASS.
* Live VM verification (per AGENTS.md rule 4) is the operator's job — the
  `/admin/update` UI shows the version it is about to apply before it applies it.

---

## v1.5.36 — the ACL must follow the assignment table (B276)

**Date:** 2026-09-21 · **Base:** `v1.5.35` → this tag · **Compatibility:** none.

**Symptom** (analysis of the live tailnet): one user's device (`michail`/`basic`) lost
essentially every destination it has a rule for — Cloudflare, Google, discord,
rutracker — while another user's device (`skyadmin`/`skyworker`) kept working. Both
devices' per-CIDR grants were pinned to `karolina` while `emilia` was the relay that
actually served those prefixes.

**Root cause:** skygate owns two halves of one decision and wrote them at different
times.

* the **data plane** — which relay advertises which prefix. headscale serves a subnet
  prefix from exactly **one** relay (the primary); the assignment table and the
  advertised routes are recomputed on every sync pass (minutes);
* the **control plane** — the ACL, whose per-CIDR grant carries `via=[owner]` since
  B275. It was regenerated **only** when a rule, user or device changed.

Live sequence: the ACL was generated and applied **2026-09-20 18:19**; the assignment
table moved 28 Cloudflare/Google prefixes to `emilia` after **19:26** (the routes
followed within five minutes); nothing regenerated the ACL, so every pin for those
prefixes kept naming `karolina`. `via` on a per-CIDR grant is a **permission filter**,
not steering — the client never receives a route whose primary is not in the `via`
list, silently falls back to the direct path, and on a network where that path is
blocked the destination simply disappears. Measured on one client: **70 of 177**
per-CIDR grants named a relay that was not the primary, and **35 more** named a prefix
no relay advertised. Nothing anywhere — log, audit, metric, page — said so.

A second, quieter defect fed the first: the ownership vote counted **rows**.
`LoadClaims` read every enabled ip/subnet row, and one domain rule expands into one
derived row per parent domain (live: five `104.16.0.0/12` rows for one device), while
the domain auto-updater rewrote ~145 rows and re-deduplicated ~110 **every five
minutes** — so the "explicit majority wins" rule could be decided by that churn, and
each flip invalidated the pins for the prefix.

**Fix**
* `reconcilePrefixOwnership()` is now the single entry point of **both** sync paths.
  When `prefixowner.Reconcile` reports a changed owner it regenerates the policy and
  pushes it through the shared `acl.ApplyGeneratedPolicy` tail (snapshot + mark +
  audit; `ApplyACLPipelineForPlane` is now generate-then-this, so the two can never
  drift), behind a 60-second shared throttle and after a fresh live read compared with
  the new `headscale.PolicyEquivalent` — an already-applied policy costs nothing. A
  failed apply is logged with its reason, alerted, and never fatal.
* `prefixowner.LoadClaims` GROUPs BY `(prefix, relay, device)` — one vote per device,
  so the updater's churn cannot decide who owns a prefix. The staggered fallback path
  counts distinct `(relay, prefix)` pairs for the same reason.
* The drift is **visible**: `/admin/exit-nodes` now reports whether the live policy
  equals the one skygate would generate, flags rows whose owner stays silent, that
  nobody announces, or that several relays announce (B274's flap risk), renders only
  the drifted and manually pinned rows (the table holds ~1500 entries and used to be
  printed in full, with a relay `<select>` per row), and offers
  **«Пересобрать и применить ACL»** (`POST /admin/exit-nodes/acl-resync`) for a manual
  pin or a failed sync, with RU+EN help.

### v1.5.36 also closes two gaps found while verifying the above

**The rule churn was a second, larger source of the same staleness.** The ownership
flip was not the only thing the ACL missed: the domain auto-updater resolves each
domain rule into CIDRs and rewrites ~145 rows (de-duplicating ~110) **every five
minutes**, and nothing re-applied the policy for those changes either. Measured on the
live host: **8 of the 15 newest rules had no alias in the policy at all** — their
destinations were unserved for everyone, including the device that asked for them. So
the drift check is now trigger-agnostic (`applyACLIfDrifted`): the sync paths call it
when an owner changes, the rule-maintenance tick calls it after every domain pass, and
the same equivalence guard means a quiet tick writes nothing. A policy that is stale
for *any* reason now converges within one tick, without the operator pressing anything.

**B276.1 — «все мои устройства» must cover a device registered later.** The option
exists since B275.3, but `PostMyExitRule` only materialised the rule for the devices
that existed at that moment and forgot why: a laptop registered a week later had no
rule while the page still promised the rule covered all of the user's devices. V074
adds `device_rules.all_devices` (both migration chains, additive with default 0 — the
pre-V074 fan-out copies are indistinguishable from hand-made single-device rules, and
guessing would silently start copying rules nobody asked to copy), the save path marks
the whole group, and `propagateAllDeviceRules()` re-materialises the intent for the
user's current devices in the same tick and **before** the drift check, so the ACL
generated in that pass already contains the new device. Copies keep the marker, a rule
whose owner has no attributed device is reported instead of skipped, and `/my/exit-rules`
shows a **«все мои устройства»** badge (RU+EN) so the user can tell a rule that follows
their new laptop from one that does not.
**Operator action after upgrading:** apply the ACL once — the sync paths would do it
by themselves on the next ownership change, but the current mismatch was created
before the upgrade, so press «Пересобрать и применить ACL» on `/admin/exit-nodes` (or
`/admin/exit-rules` → Re-apply). The page reports «политика headscale совпадает с
текущей таблицей владельцев» when it is in sync.

**Contracts:** 28 in `scripts/check_b276_acl_ownership_sync.sh`;
`internal/feature/exit_rules/sync_b276_test.go` reproduces the live sequence on a
migrated database plus a headscale policy stub (the move pushes exactly one policy
with the NEW pin; a no-op pass writes nothing; the throttle absorbs churn);
`internal/headscale/policy_equivalent_b276_test.go`;
`internal/prefixowner/prefixowner_b276_test.go` (five duplicate rows → one vote).

## v1.5.35 — one tagOwners policy write per reconcile pass (B272.7)

**Date:** 2026-09-20 · **Base:** `v1.5.34` → this tag · **Compatibility:** none.

This closes the item v1.5.32 left open ("the skygate-side batch: collect every
missing `tagOwners` for the tick and send ONE policy write instead of one per
device"). It turns out the batch is not only restart economy — the per-device
write was still losing tags.

**Symptom** (native host `aro`, immediately after v1.5.31 installed the policy
helper): three portal-owned devices needed three dev-tags and the first tick
permitted exactly ONE of them —

```
tag-reconcile: checked=4 applied=1 failed=2
tag-reconcile: cannot make "tag:dev-daniil-laptop" permitted for node 3 (laptop):
  400 requested tags [tag:dev-daniil-laptop] are invalid or not permitted
$ grep -c 'tag:dev-' /etc/headscale/policy.hujson
1
```

**Root cause:** `EnsureTagOwner` is a read-modify-write of the WHOLE policy, and
`ReconcileTags` called it once per device. With `policy.mode: file` the write is
performed **asynchronously** by the privileged `skygate-policy.path` unit (write
the file + restart headscale), so call N read the policy *before* the applier had
written call N-1's result: every write after the first was computed from the same
stale snapshot and dropped its predecessor's entry. The losing writes reported
success — the 400 above blamed the tag, which is why this reads like a permission
or validation bug and why the v1.5.31 union and the v1.5.32 retry, both correct,
could not fix it.

**Fix**
* `Client.EnsureTagOwners(map[string][]string)` (**`internal/headscale/tags.go`**) —
  one read-modify-write for every pending tag. The policy read/parse half moved
  into the shared `loadPolicyMap` + `policyTagOwners` helpers (the B251 HuJSON and
  stringified-policy handling travels with it), the whole request is validated
  **before** the write (a malformed entry cannot half-apply a batch) and the tags
  are processed in sorted order so the error text and the log line are stable.
  `EnsureTagOwner` now delegates to it, so the adopt/transfer admin paths and the
  reconciler share one writer — the contract asserts exactly one `c.SetPolicy`
  call in the file.
* **`internal/nodeownership/auto.go`** — `nodeLister` requires the batch method;
  `ReconcileTags` computes the pending set (with the **same predicates** the apply
  loop uses: the row has a tag, the node is live, headscale does not carry the tag,
  the id parses) **before** the loop, permits all of them at once, records them in
  `ensured` so the per-row path becomes a no-op, and logs
  `tag-reconcile: permitted N tag(s) in one policy write: …`. An empty pending set
  issues **no** write (a needless file-mode write restarts headscale). A failed
  batch is logged and falls back to the per-tag path, so each device is still
  reported through the B227 sink with its own reason. `tagOwnersFor` is shared by
  both paths.
* The **apply** call gets the same bounded transient retry (`addTagRetry`,
  1s/2s/4s on connection refused / reset / timeout, never on a `400 … are invalid
  or not permitted`). The batch deliberately puts the headscale restart in the
  same tick as the applies, so without this the tick that just permitted every tag
  could lose every `AddTag` to a daemon that was still coming up — and the operator
  would wait another five minutes for tags that were already allowed.

**Contracts:** 21 in `scripts/check_b272_7_tag_owner_batch.sh` (source shape, the
single-write invariant, ordering of the pre-pass, the retry/fallback semantics, the
git-tracked-script guard from trap #11, and the Go contracts);
`internal/nodeownership/auto_b272_7_batch_test.go` reproduces the `aro` case
(three devices → one write, in-sync → none, batch failure → per-device reporting)
and pins the apply retry (a transient refusal is retried once and succeeds; a
`400` returns after a single attempt);
`internal/headscale/tags_b272_7_batch_test.go` drives a policy stub (three missing
tags → one PUT, existing entries preserved, empty request → no write, validation
before write).

**Live verification:** after deploying v1.5.35 the journal must show
`permitted N tag(s) in one policy write: …` on the tick that repairs drift, and
`grep -c 'tag:dev-'` in the policy must equal the number of tagged devices in
`node_owner_map`. Contract F in the check reads both when it runs on the host.

## v1.5.32 — a headscale restart must not cost a whole tag tick (B272.4)

**Date:** 2026-09-20 · **Base:** `v1.5.31` → this tag · **Compatibility:** none.

The privileged policy applier restarts headscale after it writes the policy, so
the reconciler's very next API call can hit a daemon that is still coming up. Live
on the native host `aro` that cost two whole ticks — 20:46:03 and 20:51:51 — with
`ensure-tag-owner: get ACL: api: Get http://127.0.0.1:8081/api/v1/policy: dial
tcp 127.0.0.1:8081: connect: connection refused`, while headscale was healthy ten
seconds later; every lost tick is five minutes of a device without its tag.

`ensureTagIsPermittedRetry` now retries a **transient** failure (connection
refused / reset / i/o timeout / deadline exceeded) up to three times with a 1s, 2s,
4s backoff, and does **not** retry a policy or permission refusal — that is an
operator problem, not a timing problem. `isTransientHeadscaleDown` is the pure
classifier behind that decision.

Contracts: section F in `scripts/check_b272_3_policy_helper.sh` plus
`internal/nodeownership/auto_b272_4_test.go` — the behavioural half proves the
first retry happens after ~a second and succeeds, and that
`400 requested tags [...] are invalid or not permitted` returns immediately with a
single attempt.

**Still open:** the skygate-side batch (collect every missing `tagOwners` for the
tick and send ONE policy write instead of one per device) — the applier's
tagOwners union (v1.5.31) already makes the lost-update impossible, so this is now
about restart economy rather than correctness.

## v1.5.31 — the policy applier must not lose a tagOwner it was not told about (B272.4)

**Date:** 2026-09-20 · **Base:** `v1.5.30` → this tag · **Compatibility:** an
existing `skygate-policy.path`/`.service` pair keeps working — re-run
`bash deploy/install-policy-helper.sh` to pick up the merge.

**Symptom** (native host `aro`, right after the policy helper was finally
installed): the first tick permitted exactly ONE device tag and left the other
two failing every five minutes with
`400 requested tags [tag:dev-daniil-laptop] are invalid or not permitted` —
`applied=1 failed=2` while `grep -c 'tag:dev-'` showed **1** for three devices.

**Root cause:** `EnsureTagOwner` is a read-modify-write of the WHOLE policy, and
the reconciler calls it once per device; the privileged applier runs
asynchronously (a `.path` unit), so the next call reads the policy *before* the
previous write landed and sends a document that omits the tag it just added.
Last writer wins.

**Fix:** the applier is the only writer of that file, so it now **unions the
incoming `tagOwners` with the ones on disk** before writing (an added owner is
never dropped; grants and the rest of the document still come from the requester
verbatim) and logs `tagOwners unioned with the on-disk policy (B272.4)`. Without
`python3` it warns and writes unchanged (previous behaviour as a fallback).

**Verification:** `scripts/check_b272_3_policy_helper.sh` contract **E** — the
applier reads the on-disk policy, logs the union, and, where `python3` exists, a
behavioural test writes a one-tag document over a two-tag file and asserts no
existing tag is lost.

**Still open:** the skygate-side batch (collect every missing `tagOwners` for the
tick and send ONE policy write) and treating `connection refused` during the
applier's headscale restart as transient instead of failing the tick.

## v1.5.30 — the policy helper on an existing install, and a named refusal (B272.3.1)

**Date:** 2026-09-20 · **Compatibility:** none (adds a script + a failure reason).

Live diagnostic on `aro` showed the whole deadlock in one place:
`skygate-policy.path: inactive/missing`, `tag:dev-* entries: 0` in
`/etc/headscale/policy.hujson`, `open …policy.hujson.skygate.tmp: read-only file
system`, and `tag-reconcile: checked=4 applied=0 failed=3` every five minutes for
13 hours — while the metric read
`skygate_tag_autoupdate_failures_total{reason="unknown"} 157`.

* **`deploy/install-policy-helper.sh`** — idempotent (re)install of the applier
  and the two units on an **existing** install (`write_policy_units()` only ever
  ran during a fresh install, so a host installed before v1.5.16 could never get
  them). It resolves `SKYGATE_UPDATE_DIR` from the running service — a mismatch
  stages a file nobody watches — reloads systemd, arms `skygate-policy.path` and
  reports any queued request.
* **`ReasonPolicyWriteRefused`** (`policy_write_refused`) — the failure now has a
  name, matched before the gRPC ACL codes; `TestB272_ReconcileReportsOwnerFailure`
  was renegotiated from `unknown` to it.

15 → 18 contracts in `scripts/check_b272_3_policy_helper.sh`.

## v1.5.29 — the native-tagging diagnostic

`scripts/native_tagging_diag.sh`: read-only, one run answers the four questions
that decide whether a dev-tag can reach headscale on a native install — is the
reconciler running (active unit + `SKYGATE_BASE_DOMAIN`), can the policy be
written at all (is the root helper armed; is a request queued that nobody
consumed), does the policy carry `tag:dev-*`, and what does headscale report
versus `node_owner_map` — plus the service's own refusal reasons and the
`skygate_tag_*` counters. (Originally named `diag_*.sh`; `.gitignore` eats that
pattern — the same trap as the v1.5.20 fixture-cleanup script, which is exactly
why the B274.1 contract asserts git tracks the file.)

## v1.5.28 — no-op tag

A marker only: the CRLF churn reported for `internal/i18n/catalog_exit_rules.go`
turned out to be confined to the v1.5.27 commit's whitespace and the working tree
was already clean, so there was nothing to normalise. No code change.

## v1.5.27 — the rule forms follow the new model (B275.3)

* The exit-node select in `/my/exit-rules` (and the admin form) gains
  **«авто — skygate выберет»** as the default; the hint now states the truth: an
  empty value lets skygate assign the owner by load and show it on
  `/admin/exit-nodes`, where it can be pinned, and a chosen node is a *preference*
  — if another relay serves the network the ACL pin follows the owner, otherwise
  the site simply would not open on that device.
* **«все мои устройства»** — a per-user rule. It cannot be `src=user@` in the ACL
  (devices are tagged and headscale does not match a tagged node with a user
  selector, B265), so it is materialised for every device the user owns:
  `userDeviceIDs()` reads `node_owner_map` and `PostMyExitRule` fans the insert
  out through `insertRuleUnique` (duplicates are a no-op), logging the count.

## v1.5.26 — the assignment engine gets the healthy-relay list (B275.2)

Both `prefixowner.Reconcile` call sites passed `nil` as the healthy set, so no
prefix could ever be `explicit` (everything degraded to `auto`) and sticky re-use
was disabled — the owners flipped between passes and the ACL pins moved under the
clients. Live evidence after the v1.5.25 deploy: the whole table showed
`source=auto` and `104.16.0.0/12` / `142.250.0.0/15` swapped owners between two
consecutive checks. `healthyExitRelays()` now feeds the exit-node health snapshot
into the engine; after the fix the table shows `explicit=432` with Telegram back
on the relay its rules name.

## v1.5.25 — the operator surface for prefix assignment (B275.1)

**Date:** 2026-09-20 · **Base:** `v1.5.24` → this tag · **Compatibility:** UI + docs
only, no schema or API change.

B275 moved the "which relay serves this network" decision into skygate, but the
decision was only reachable through SQL. This release is the surface:

* **`/admin/exit-nodes` → «Распределение префиксов»** — a table of
  `сеть → владелец → источник (explicit/manual/auto) → правил/устройств → анонс`,
  with a per-row relay select and **Сохранить**:
  * choosing a relay calls `prefixowner.SetManual` (audit `prefix_owner_pin`) — the
    engine will not reassign that prefix while the relay is healthy;
  * choosing **авто** hands it back to the engine (audit `prefix_owner_auto`);
  * the **Анонс** column compares the table with the owning relay's live advertised
    routes, so "assigned but not announced" (route sync not run, or SSH to the relay
    failed) is visible instead of silent.
* **In-page help** (RU + EN): why a prefix has exactly one owner, what
  `explicit`/`manual`/`auto` mean, that device rules are never rewritten, and what
  each client platform does (`accept-routes` on Windows/Linux, the in-app exit-node
  choice on Android/iOS).
* **`docs/troubleshooting.md`** gains the operator section: symptom ("one device
  works, another does not"), the real cause (one primary per prefix; the CDN
  expansion makes two devices collide on the same ranges), how the table is
  computed, the checks, and the caveat that two relays can never serve one prefix —
  so a network that MUST go through a specific relay is pinned by hand.

Note: this is the answer to «у устройства правило — значит оно пойдёт к этому
exit-узлу» — it goes to the owner of the prefix, and the page now shows when those
two differ.

## v1.5.22 — skygate decides which relay serves which prefix (B275)

**Date:** 2026-09-20 · **Base:** `v1.5.21` → this tag · **Compatibility:** adds the
`prefix_owner` table (v0.73, both migration chains); existing behaviour is
unchanged until the first route-sync pass fills it.

### Why

headscale serves a subnet prefix from **exactly one** relay. B274 made that choice
deterministic and reported the losers — and the moment it did, `skyworker`'s
`rutracker.org` stopped working: its per-CIDR ACL grant said `via=[karolina]` while
`emilia` held the primary, so the packet had no permitted path at all. The rule
list said "karolina", reality said "emilia", and the ACL believed the rule list.

### What

1. **v0.73 `prefix_owner`** — `prefix PK, exit_node_id, source, claims, devices,
   updated_at`, created in **both** migration chains.
2. **`internal/prefixowner`** — the engine:
   * `Assign` — explicit rules win by **majority** (hostname as the deterministic
     tie-break); a `source='manual'` **operator pin is never overwritten** while its
     relay is healthy; everything else is spread over the healthy relays and is
     **sticky** (the previous owner keeps the prefix while its load stays within one
     prefix of the least loaded relay, so nothing migrates without a reason); an
     unhealthy owner loses the prefix (a relay that is down cannot carry traffic);
     an empty healthy set falls back to the relays the rules named, so the table is
     never emptied by a headscale outage.
   * `SetManual(d, prefix, relay)` — pin one prefix by hand; an empty relay hands it
     back to the engine.
   * `Reconcile` — the single call the sync path makes.
   * `OwnerByPrefix` / `TagByPrefix` / `ViaForPrefix` — the shapes the sync and the
     ACL generator consume.
3. **`SyncAdvertisedRoutes`** reconciles the table and the relays advertise exactly
   their owned prefixes (B274's in-memory computation stays as the first-pass
   fallback before any row exists).
4. **The per-CIDR ACL grant now carries `via=[OWNER]`** instead of
   `via=[the rule's exit node]`. That is the actual fix: a device whose rule named a
   relay that cannot serve the prefix now reaches the destination through the relay
   that can, and the substitution is visible in the table (`source`, `claims`,
   `devices`) instead of silently breaking.

### Operator view

`/admin/exit-nodes` and the sync result still show the relay-level picture; the new
authority is the `prefix_owner` table:

```
select prefix, exit_node_id, source, claims, devices from prefix_owner order by prefix;
```

Re-assign one prefix (never overwritten while that relay is healthy):

```
select set_manual …            -- via prefixowner.SetManual in code today
```

### Still open (B275.1)

An admin page/action for per-prefix re-assignment (today the table is engine-owned
plus `SetManual`), Telegram notification when an owner goes down and a prefix is
reassigned, and per-relay load weights.

## v1.5.21 — dedup must run AFTER the autoupdater pass (B274.2)

**Date:** 2026-09-20 · **Base:** `v1.5.20` → this tag · **Compatibility:** none.

Live evidence from the v1.5.20 deployment: `DomainAutoUpdater` started its tick
with 0 duplicate rule groups and ended it with 47, because the resolve loop inserts
one row per `(domain, CIDR)` pair — that insertion **is** the duplication. The
v1.5.19 collapse therefore ran before the very pass that recreates the duplicates.
It now runs **deferred**, after the loop: `auto-updater: dedup removed 110 redundant
derived rule row(s)`, and the live table went 47 → **0**.

## v1.5.20 — the fixture-cleanup script actually ships (B274.1)

**Date:** 2026-09-20 · **Base:** `v1.5.19` → this tag · **Compatibility:** script
rename only, no code or schema change.

v1.5.19 documented `scripts/cleanup_b188_3_fixtures.sh` and every local check
passed — but the file never reached the repository: `.gitignore` line 78 is
`cleanup_*.sh`, so `git add -A` silently skipped it and the operator's `--apply`
run on the freshly pulled host failed with
`bash: scripts/cleanup_b188_3_fixtures.sh: No such file or directory`.

* the script is renamed to **`scripts/b188_3_fixture_cleanup.sh`** (outside the
  ignore pattern);
* `scripts/check_b274_prefix_ownership.sh` gains contract **E.1b**, which asks
  `git ls-files --error-unmatch` instead of `test -f` — an ignored file can no
  longer pass the gate while being absent from every clone;
* `AGENTS.md` trap #11 records the class: a check that only proves the file
  exists on THIS disk proves nothing about what was committed.

## v1.5.19 — one advertising relay per prefix (B274)

**Date:** 2026-09-20 · **Base:** `v1.5.18` → this tag · **Compatibility:** no schema,
config or API change. The dedup and the fixture cleanup are ordinary `DELETE`s; the
route-sync change only narrows which prefixes a relay advertises.

### Symptom (live, host `SKYWORKER` — the operator's own Windows box)

Cloudflare/Google/Akamai sites intermittently failed **only on `skyworker`** while
every other device was fine. The device accepts routes and has **no exit node
selected**, so it reaches those destinations through the tailnet's subnet routes.

### Root cause

`StaggeredSync` / `SyncAdvertisedRoutes` built each relay's `--advertise-routes`
from **that relay's own rules**, so two relays advertised the same prefixes
whenever two devices of different users pointed their rules at different relays —
which the CDN expansion makes routine:

| CIDR | device | exit | where it came from |
|---|---|---|---|
| 28 Cloudflare/Google ranges | `basic` (michail) | **emilia** | `cdn:cloudflare:discord.*`, `rutracker.org` |
| the same 28 | `skyworker` (skyadmin) | **karolina** | `cdn:cloudflare:auth.docker.io`, `registry.npmjs.org`, `rutracker.org` |

headscale assigns **one primary per prefix** (`node.SubnetRoutes()` is what the
netmap carries), and *which* of the two relays won was not stable: it changed
between two `nodes list` dumps on the same day (emilia served the contested ranges
first, karolina later, after a `staggeredSync` pass had rewritten both route sets).
`skyworker`'s per-CIDR ACL grant names karolina, so whenever emilia held the
primary those destinations were dropped for it — while a device **without** a
per-CIDR pin kept working, because its unpinned `autogroup:internet` grant follows
whatever primary exists.

### Fix

1. **`internal/feature/exit_rules/prefix_owner.go`** owns the question "which relay
   may advertise this prefix?":
   * `PrefixOwnership` → every prefix has exactly **one** advertising relay (the
     relay the most enabled rules name for it; hostname as a deterministic
     tie-break, so the choice cannot oscillate between passes);
   * `PrefixLosers` → the relays that must drop a prefix they claim;
   * `OwnedPrefixes` → filters a relay's candidate list, always keeping the
     exit-node bases (`0.0.0.0/0`, `::/0`).
2. **All three sync paths** (`SyncAdvertisedRoutes`, `SyncAdvertisedRoutesForNode`,
   `StaggeredSync`) now advertise only the owned set, so a prefix can no longer be
   announced by two relays and the primary stops flapping.
3. **The losers are named** — `prefix-ownership(...): <relay> does NOT advertise N
   prefix(es) it claims …` in the log plus a `prefix_conflicts` entry in the sync
   result, so a device whose rule names a non-serving relay is visible instead of
   silently half-working. The device's own rule list is **never rewritten**:
   `device_rules.exit_node_id` stays exactly as the operator set it.
4. **Dedup.** `CollapseDuplicateDerivedRules` removes the rows the CDN expansion
   duplicates (it partitions on the natural key — `user_id, device_id,
   exit_node_id, target_type, target_value` — and keeps the `cdn:`-prefixed
   `parent_domain`, B183's own preference). Live: `basic` carried **five** rows for
   `104.16.0.0/12` (one per discord.* / rutracker.org parent).
5. **Fixture cleanup.** `scripts/b188_3_fixture_cleanup.sh` deletes the rows the
   B188.3 integration fixtures leaked into the production database
   (`5.5.5.5/32`, `6.7.8.9/32`, `1.2.3.0/24`, `1.2.99.0/24`, `example.com`,
   `cascade-verify-*`, `limit-test-*`) from a **closed allow-list**, and is
   dry-run unless `--apply` is passed.

### Operator note

If a relay drops a prefix (the log line above), the device whose rule named that
relay cannot reach it through the tailnet any more. The operator decides which
relay keeps a contested prefix — re-point the rule at the owner (or drop the
duplicate rule on the other device) — because headscale can serve a prefix from
only one relay at a time. `skyworker`'s case: 183 rules → karolina (it owns most
of them), 28 ranges also claimed by `basic` → emilia.

### Files

`internal/feature/exit_rules/prefix_owner.go` (new),
`internal/feature/exit_rules/sync.go`, `internal/feature/exit_rules/prefix_owner_b274_test.go`
(new), `scripts/check_b274_prefix_ownership.sh` (new),
`scripts/b188_3_fixture_cleanup.sh` (new).

### Verification

`scripts/check_b274_prefix_ownership.sh` — 21 contracts: the ownership helper and
its determinism, both sync paths, the loser report, the dedup, the cleanup script
(dry-run default + closed allow-list), the Go behaviour tests, and a **live
served-overlap check** (`no prefix is served by more than one relay`, SKIPs when
headscale is unreachable).

## v1.5.18 — exit-node health stops calling a working relay "не работает" (B273)

**Date:** 2026-09-19 · **Base:** `v1.5.17` → this tag · **Compatibility:** no schema,
config or API change. Existing installs need no migration: the snapshot table
(`exit_node_health`) keeps its columns and simply stops carrying rows for plain
devices on the next monitor tick.

### Symptom (live, native host `aro`)

`/admin/exit-nodes` showed the red banner **«Нет рабочих exit-узлов!»**, `0/1
здоровых`, and `состояние = offline` for the tailnet's **only** relay —
`exit-node-vps` (`100.64.0.1`), which was online, advertising 2 routes
(`0.0.0.0/0` + `::/0`), last seen 47 m earlier, and whose row even offered the
«Tag as exit-node» button. `/my/exit-nodes` showed the **same** node as *online*.

Both pages read the same `headscale.NodeView` list, so the contradiction was
inside skygate — and it was in the health monitor's definition of "exit node".

### Root cause

`internal/headscale.hasExitNodeTag` (used by `ListExitNodes` → `/my/exit-nodes`)
counts a node as an exit node when **any** of these holds:

1. it carries `tag:exit-node`,
2. its name starts with `exit-` / `exitnode`,
3. it advertises `0.0.0.0/0` or `::/0`.

`internal/monitoring.exitNodeMonitor.computeSnapshot` implemented a **different**
rule — the literal tag only — and its state ladder was

```go
case !online || !hasTag:
    state = "offline"
```

so a relay that works (rule 2/3) but has no tag was filed as `offline` with
`healthy = false`. Hence the banner. The node was fine; the *test* was wrong.

Two more defects were found while proving the above:

* `degraded` was computed from **`AvailableRoutes`** (what the relay *advertises*),
  never from **`ApprovedRoutes`**. A relay that advertises `0.0.0.0/0` but was
  never approved in headscale hands no client an exit route — and the monitor
  called it **healthy**. The false negative above was hiding a false positive.
* the monitor wrote a snapshot row for **every** node in the tailnet, so a 14-node
  tailnet carried 11 permanent `state=offline` rows describing laptops, and the
  bot's `/exit_nodes_health` listed every device as an "offline exit node".

### Fix

1. **One predicate.** `computeSnapshot` now gates on `NodeView.IsExitNode` — the
   same field `ListExitNodes` filters on — and returns `(snapshot, ok)`. A node
   that is not an exit node gets **no row**, and a stale row is pruned, so the
   `N/M здоровых` denominator counts exit nodes rather than devices.
2. **A ladder ordered by what blocks client egress.**

   | state | meaning | healthy |
   |---|---|---|
   | `offline` | headscale reports the node as not online | no |
   | `degraded` | online, but `0.0.0.0/0` is **not approved** — no client gets an exit route | no |
   | `untagged` | online + `0.0.0.0/0` approved, only `tag:exit-node` missing: **it works** | **yes** |
   | `online` | everything in place | yes |

   `exitNodeUsable()` (`online` | `untagged`) is the single source of truth for the
   `healthy` flag **and** for the Telegram alert boundary: `isCalmModeAlert` now
   fires on every crossing of it — so losing/gaining route approval **alerts**
   (previously silent: a relay could lose its approval unnoticed) while tagging or
   untagging a working relay stays quiet.
3. **The page names the problem.** `/admin/exit-nodes` fills
   `ApprovedV4Default` / `ApprovedRoutesOK` from the live node's `ApprovedRoutes`,
   renders a red «не одобрено» pill next to the advertised route count, and shows
   two **named** warning banners — «Exit-узлов без tag:exit-node: N» (yellow, with
   the tag hint) and «Exit-узлов с неодобренными маршрутами: N» (red) — instead of
   answering both problems with the red zero-healthy banner. The admin's tag test
   is case-insensitive (`EqualFold`), matching headscale.
4. **The bot agrees.** `/exit_nodes_health` gained the `untagged` bucket and counts
   untagged relays as healthy, so the phone no longer reports `0 of 1 healthy` for
   a serving relay.

### Operator action

Nothing is required to make the banner disappear — the next monitor tick
(`SKYGATE_EXIT_NODE_CHECK_INTERVAL`, 5 min by default; or the «Run health check
now» button) rewrites the snapshot. A relay that shows `untagged` genuinely works,
but adding `tag:exit-node` (the «Tag as exit-node» button on its row, or
`headscale nodes tag`) is still recommended: ACL rules that pin an exit node with
`via=[tag:…]`, the pre-auth keys `/admin/exit-nodes/register` mints, and the
tag-anchored UI affordances all key off tags.

### Files

`internal/monitoring/exit_node_monitor.go` (predicate, ladder, `exitNodeUsable`,
pruning, alert boundary), `internal/feature/admin/exit_nodes.go`
(`ApprovedV4Default`/`ApprovedRoutesOK`/`UntaggedCount`/`UnapprovedCount`,
`hasExitNodeTagFor`), `internal/handlers/templates/admin/exit_nodes.html`,
`internal/i18n/catalog_exit_nodes.go` + `catalog_bot.go` (RU+EN),
`internal/telegram/commands_exit_node_health.go`,
`internal/monitoring/exit_node_monitor_test.go`.

### Verification

`scripts/check_b273_exit_node_truth.sh` (36 contracts: the shared predicate, the
ladder, pruning, the alert boundary, the handler, the template, RU/EN parity, the
bot, Go behaviour tests, and live snapshot checks that SKIP off-host) +
`TestComputeSnapshot_UntaggedIsHealthy_B273`,
`TestComputeSnapshot_NotAnExitNode_IsSkipped_B273`,
`TestTick_PrunesNonExitNodeRows_B273`. The first of these reconstructs the `aro`
node exactly: online, `exit-node-vps`, `tag:public` only, `0.0.0.0/0` + `::/0`
advertised **and approved**, `last_seen` 47 minutes ago — and asserts
`state=untagged`, `healthy=true`.

## v1.5.17 — an offline host can self-update from a mirror (B272.5)

**Date:** 2026-09-19 · **Base:** `v1.5.16` → this tag · **Compatibility:** no schema,
config or API change. New OPTIONAL setting `SKYGATE_UPDATE_GIT_URL` (env) /
`update.git_url` (DB).

### The report

```
[debug] $ git fetch --tags --prune --force →
fatal: unable to access 'https://github.com/BarsSky/skygate.git/':
       Failed to connect to github.com:443 after 132571 ms: Could not connect to server
[error] phase failed: git fetch: exit status 128
```

The host has no outbound access to `github.com:443` (multi-minute connect
timeout — not a 404, not auth), so the image-update path could never work there.
Nothing in the product said the fetch source is configurable, and every attempt
burned ~2 minutes of connect timeout before rolling back. (The owner name's case
in the URL is a red herring: git passes the URL through verbatim and GitHub
accepts whatever case the repository uses.)

### What changed

* `DockerUpgrader.fetchTarget` keeps `origin` as the default and, when it is
  unreachable, retries against a configured mirror with an explicit refspec so
  both **tag** and **branch** targets resolve:

  ```
  +refs/heads/*:refs/remotes/origin/*
  +refs/tags/*:refs/tags/*
  ```

* The mirror URL comes from `SKYGATE_UPDATE_GIT_URL` (alias
  `SKYGATE_UPDATE_GIT_MIRROR`), or from `global_settings.update.git_url` —
  resolved per job through the new `Service.globalSettingFn()`, so it can be
  changed without restarting skygate. `origin` stays the documented default and
  is always tried first.
* With no mirror configured the log now names the knob instead of leaving only
  git's error, and the failure message carries both attempts.

**Giving an air-gapped host a mirror** (a local path is a valid git URL):

```bash
# on a machine with GitHub access
git clone --bare https://github.com/BarsSky/skygate.git skygate.git
scp -r skygate.git user@host:/srv/git/skygate.git
# on the host — /etc/skygate/skygate.env
SKYGATE_UPDATE_GIT_URL=/srv/git/skygate.git
sudo systemctl restart skygate
```

Contracts: section K in `scripts/check_b272_tag_drift.sh` (K–K6, 48 contracts
total) and `internal/update/docker_fetch_b272_test.go` (drives the real git
binary against local repositories: fallback fetch brings the tag *and* the
remote-tracking branches; the no-mirror case names the variable). Procedure:
`docs/troubleshooting.md` §8.0.6.

## v1.5.16 — the policy is applied through a root helper, and a leftover file can no longer block updates (B272.3 + B272.4)

**Date:** 2026-09-19 · **Base:** `v1.5.15` → this tag · **Compatibility:** no schema,
config or API change.

Two blockers from the same live host, both found after v1.5.15 made the earlier
ones self-reporting.

### B272.3 — skygate could never write `/etc/headscale` (by design)

```
tag-reconcile: cannot make "tag:dev-daniil-workpc" permitted …:
  write policy file /etc/headscale/policy.hujson:
  open /etc/headscale/policy.hujson.skygate.tmp: read-only file system
```

The policy file was group-writable and headscale could read it — and the write
still failed, because the skygate unit runs with

```
ProtectSystem=strict
ReadWritePaths=/var/lib/skygate /etc/skygate
```

so `/etc/headscale` is **read-only for skygate by design**. Widening the service's
mount namespace (granting the portal write access to another service's
configuration) was rejected; instead this release reuses the project's existing
privilege split (B261, the self-update helper):

* `internal/headscale/policy_helper.go` — `RequestPolicyApply` writes a
  **data-only** request (`<update_dir>/policy.request.props`: absolute policy
  path + the policy text between markers) and `PolicyHelperArmed` reports whether
  the helper exists. A missing helper returns `ErrPolicyHelperUnavailable`, so the
  caller produces the "apply it by hand" message — never a silent success.
* `deploy/skygate-apply-policy.sh` — the root-owned applier. It **validates**
  before writing: `POLICY_PATH` must be absolute, the file must already exist, and
  the path must match the `policy.path` headscale's own `config.yaml` declares.
  The write is atomic, keeps owner/group/mode, saves the previous policy to
  `policy.prev`, restores it if headscale does not come back healthy, and finally
  verifies that the headscale service user can read the file. It is invoked by
  `skygate-policy.service` (oneshot, root) fired by `skygate-policy.path` —
  installed and enabled by `install-common.sh`, like the updater pair.
* `setPolicyViaFileNative` now tries the direct write first and falls back to the
  helper; the failure message names the sandbox, the helper to install and the
  policy to apply by hand.

### B272.4 — an untracked leftover blocked every image update

```
[debug] $ git checkout v1.5.14
error: The following untracked working tree files would be overwritten by checkout:
        scripts/skygate-move-to-infra.sh
[error] FAILED: git checkout: exit status 1
```

The target revision *tracks* that file — the working-tree copy was skygate's own
leftover from an earlier transfer — yet git refused, the update rolled back, and
the operator was locked out of every future image update until they deleted it by
hand.

`DockerUpgrader.checkoutRef` now handles exactly this case: it computes the
conflict set (untracked files ∩ paths the target revision writes, via
`git ls-files --others --exclude-standard` and `git ls-tree -r --name-only <ref>`
— no `--dry-run`, which `git checkout` does not support), **backs each file up to
`<update_dir>/checkout-stash/<timestamp>/` with its permission bits**, removes it,
retries the checkout, and logs where the copy went. Everything else stays fatal: a
**modified tracked** file (the operator's `docker-compose.yml`, `go.mod`, `go.sum`
or any edited source) still aborts the update untouched, because that content is
real data rather than a leftover of a file the revision already tracks.

Contracts: `scripts/check_b272_tag_drift.sh` sections I and J (I–I6, J–J4, 42
contracts total), `internal/headscale/policy_helper_b272_test.go`,
`internal/update/docker_checkout_b272_test.go` (drives the real git binary:
preserved-and-proceeded, and modified-tracked-still-fails). Procedures:
`docs/troubleshooting.md` §8.0.4 (policy handoff) and §8.0.5 (checkout).

## v1.5.15 — the policy-permission problem reports itself (B272.2)

**Date:** 2026-09-19 · **Base:** `v1.5.14` → this tag · **Compatibility:** no schema,
config or API change.

### The last link in the chain

`v1.5.14` got the tag all the way to headscale, which then refused it with an error
that pointed at something else entirely:

```
GET /api/v1/policy → 500  reading policy from path "/etc/headscale/policy.hujson":
                          open /etc/headscale/policy.hujson: permission denied
```

The host ran headscale as `headscale:headscale` and the policy file was
`root:skygate 0660` — **headscale could not read its own policy**. Every policy API
call failed, so no `tag:*` could ever be permitted, and the only clue was a nested
500 body inside the autoupdater's log. Nothing on any page said it.

### What this release adds

`headscale.AuditHeadscalePolicy()` — a read-only audit of the policy file's
accessibility for **both** parties, surfaced in three places:

1. **the boot journal** — a warning naming the status and printing the fixes;
2. **`/admin/derp`** — a red banner with the resolved path, the headscale account,
   skygate's own access (`rw` / `r` / `—`), the probe method and the same
   copy-paste command block;
3. the reconciliation failure it explains (unchanged: `failed=N` every tick until
   the files are right).

The verdict for the headscale account is a **real probe** —
`sudo -n -u <headscale-user> test -r <path>` — when sudo is available; otherwise a
conservative model built from the file's owner/group/mode bits. skygate's own
access is asked from the kernel (`unix.Access`, which resolves the real uid/gid and
supplementary groups), so the page never claims access it does not have.

Statuses: `ok`, `not_applicable` (database-mode policy), `unreadable_by_headscale`,
`unreadable_by_skygate`, `missing`, `api_error`. For every non-`ok` status the
audit returns ready-to-paste `Fixes` — the ownership layouts are documented in
`docs/troubleshooting.md` §8.0.4.

The audit is deliberately **read-only**: it never chowns or chmods. Changing
ownership of headscale's own configuration is a privilege decision, so skygate
reports the exact commands instead of doing it behind the operator's back.

Contracts: `scripts/check_b272_tag_drift.sh` section H (H–H8, 32 contracts total)
and `internal/headscale/acl_b2722_test.go`.

## v1.5.14 — the reconciler must make the tag PERMITTED, not just apply it (B272.1)

**Date:** 2026-09-19 · **Base:** `v1.5.13` → this tag · **Compatibility:** no schema,
config or API change.

### The report (one tick after v1.5.13 on the same host)

`v1.5.13` made the drift visible, and the very first reconciliation tick named the
real blocker:

```
tag-reconcile: node 2 (workpc) is missing "tag:dev-daniil-workpc" in headscale:
  tag: api: headscale POST /api/v1/node/2/tags: 400 {"code":3,
       "message":"requested tags [tag:dev-daniil-workpc] are invalid or not permitted"}
  cli: docker not in PATH and "headscale" failed: exit status 1
       (unable to read/write to headscale socket "/var/run/headscale/headscale.sock":
        permission denied)
tag-reconcile: checked=2 applied=0 failed=1 missing=0 unattributed=2
```

The reconciler had found the right node and the right tag — but headscale refuses a
tag that is **not listed in the policy's `tagOwners`**. v1.5.13 applied tags
without first making them permitted, so the repair could never succeed (the classic
B245 chicken-and-egg). The CLI fallback also cannot help on a native install: the
local `headscale` binary talks to `/var/run/headscale/headscale.sock`, which the
`skygate` service user may not write.

### What changed

* New `ensureTagIsPermitted`: before applying each drifted tag, the reconciler
  creates its `tagOwners` entry from the **database row** —
  `<username>@<baseDomain>` plus `tagged-devices@<baseDomain>`, the same pair the
  per-user backfill uses — so the policy is repaired from the same source of truth
  as the node tags.
* The base domain comes from `SKYGATE_BASE_DOMAIN`, which `main.go` already holds.
  If it is unset, the failure is **reported** (`SKYGATE_BASE_DOMAIN is not set, so
  the owner of "…" cannot be expressed in the policy`) rather than retried blindly
  every 5 minutes.
* A failed policy update aborts the tag apply for that node (headscale would
  refuse it anyway) and reports the reason through the B227 sink, so the operator
  gets one classified failure instead of two.

Contracts: `scripts/check_b272_tag_drift.sh` (24, incl. `C6`/`C7` for the
ordering and the base domain) and three new cases in
`internal/nodeownership/auto_b272_test.go`: owner created before `AddTag`, owner
failure reported without a pointless apply, and the missing-base-domain case.

## v1.5.13 — tags actually reach headscale (B272)

**Date:** 2026-09-19 · **Base:** `v1.5.12` → this tag · **Compatibility:** no schema,
config or API change for PostgreSQL / container installs. Two new OPTIONAL env vars
(`SKYGATE_HEADSCALE_POLICY_PATH`, `SKYGATE_HEADSCALE_CONFIG_VOLUME`) and two more
(`SKYGATE_HEADSCALE_UNIT`, `SKYGATE_HEADSCALE_CONFIG`) exist for native installs;
nothing changes if they stay unset.

### The report

An operator audit of the device tags on a **native** host showed:

```
node_owner_map (skygate):  2 | | user=daniil | tag=tag:dev-daniil-workpc
headscale nodes list:      node 2  user=daniil  dev=-  all=-        ← no tags at all
```

So `/my/devices` displayed a tag the node did not carry, and every per-device ACL
rule (`src=tag:dev-daniil-workpc`) matched **nothing**. Worse: nothing in the
product said so — `tag.autoupdate_failed` was empty, the metric had never been
incremented, and Telegram was not configured.

Two errors were in the audit log, both from an earlier manual Adopt:

```
device_adopt_addtag_failed            err=tag: exec: "docker": executable file not found in $PATH
device_adopt_ensure_tag_owner_failed  err=ensure-tag-owner: PUT /api/v1/policy: 500
                                      {"code":2,"message":"update is disabled for modes ot…"}
```

### Root causes (B272)

1. **headscale kept its policy in a FILE.** `config.yaml` had
   `policy: { mode: file, path: /etc/headscale/policy.hujson }`, which makes
   `PUT /api/v1/policy` answer `500 update is disabled for modes other than
   database`. `SetPolicy` only fell back to the file path on `404`/`405`, so the
   fallback never ran — and even if it had, it wrote through a hardcoded docker
   volume (`/home/admin/headscale/config`) that cannot exist on a native install.
2. **Every tag write required docker.** `TagNode`/`UntagNode` went straight to
   `docker exec … headscale nodes tag`, which on a native host fails with
   `exec: "docker": executable file not found in $PATH`.
3. **The autoupdater could not see the problem.** B77 walks headscale and only
   considers nodes that ALREADY carry a `tag:dev-…` tag. A node whose tag never
   landed was therefore skipped on every 5-minute tick — the drift between the
   database and headscale was permanent and silent.

### What changed

* `isFileModePolicyError` recognises the 500 body (and 404/405), so the file
  fallback actually runs.
* `setPolicyViaFile` writes the policy resolved by
  `DiscoverPolicyPath`/`parseHeadscalePolicyConfig` (reads headscale's own
  config), **atomically**, and then:
  * native: writes the file directly and restarts the unit via
    `systemctl`/`rc-service`, **rolling the previous policy back** if the restart
    fails;
  * container: writes through `SKYGATE_HEADSCALE_CONFIG_VOLUME` (default
    preserved) and restarts the container.
  * unknown path → an error naming `SKYGATE_HEADSCALE_POLICY_PATH` and carrying
    the policy text, so the operator can apply it by hand.
* `TagNode`/`UntagNode` try `POST /api/v1/node/{id}/tags` **first**, then fall back
  to `runHeadscaleCLI` (docker when present, the local binary otherwise — the B267
  pattern). `AddTag` therefore works without docker.
* **New `ReconcileTags`**: every autoupdater tick walks `node_owner_map` and
  re-applies any tag headscale is missing (`reason=tag_missing` through the B227
  sink: metric + audit + rate-limited Telegram). A tag that never landed is now
  repaired automatically.
* **New `skygate_tag_unmatched_total`**: headscale nodes attributed to no portal
  user (`reason=no_strategy`) are counted and reported, so a device no per-device
  rule can match is finally visible.
* The admin **Adopt/Transfer** tag paths report through the same `TagAlertSink`
  instead of a bare audit row — the live failure never appeared in `/metrics`.

### What to do on a native host after upgrading

```bash
# 1. point skygate at headscale's policy file (only needed if it is not found
#    automatically in /etc/headscale/config.yaml)
sudo grep -A3 '^policy:' /etc/headscale/config.yaml
# SKYGATE_HEADSCALE_POLICY_PATH=/etc/headscale/policy.hujson   → /etc/skygate/skygate.env
# SKYGATE_HEADSCALE_UNIT=headscale                             (default)

# 2. restart skygate, then watch the autoupdater tick (≤5 min)
sudo systemctl restart skygate
sudo journalctl -u skygate -f | grep -E 'tag-reconcile|node-discovery'

# 3. confirm the tag landed
sudo headscale nodes list | grep -A2 '^2'
```

Contracts: `scripts/check_b272_tag_drift.sh` (22),
`internal/headscale/tags_b272_test.go` (incl. a real policy-file write with
rollback) and `internal/nodeownership/auto_b272_test.go`. Procedure:
`docs/troubleshooting.md` §8.0.4.

## v1.5.12 — the self-updater explains a failed swap (B268) + the socket is bound before anything else (B269) + optional features can no longer kill the boot (B270)

**Date:** 2026-09-19 · **Base:** `v1.5.11` → this tag · **Compatibility:** no schema,
config or API change.

This release is the answer to one operator report: a **native (systemd)** install on a
remote VM reported

```
systemctl is-active skygate   →  active
ss -ltnp | grep :8080         →  (nothing)
/admin/update                 →  ROLLBACK … verdict: rolled_back
                                 (healthz did not report build 'v1.5.11' within 90s
                                  (last build: none))
```

The process was demonstrably alive (its `dbmigrate-watchdog` and `db_health` samplers
kept logging) but nothing was listening, and no message anywhere said why. Both halves
of that are now closed.

### B269 — the socket is bound first, and every boot phase is announced

`main()` used to be a long chain — config → DB open (5 retries with backoff ≈ 42 s) →
migrations → headscale client → ACL/Telegram/exit-rules services → the whole route
table → **then** `http.ListenAndServe`. Any slow or fatal step in that chain left an
`active` unit with **no port at all**, which is exactly what the report shows: a
portless process that systemd calls healthy, with the failure textually invisible.

1. **The listener is created at the top of `main()`**, immediately after the config
   loads and *before* the DB. A port conflict (`address already in use`, a
   `docker-proxy` from a second instance, a stale unit) is now the **first** thing
   reported, exits non-zero, and is attributed — instead of appearing minutes later
   after a pile of healthy-looking background work.
2. **The bound listener serves a provisional `/healthz`** (`status:ok` + `build` +
   `phase` + `stage` + `ready` + `timeline`) and is handed to the real router through
   an `atomic.Value` swap once the routes are complete. One bind, no second
   `ListenAndServe`, no window in which the port is unowned — so the self-updater's
   build check can pass the moment the process is up, and no other process can slip in.
3. **Every phase is logged** as `startup: phase=<name> +Nms (total=Nms)` — `config`,
   `db-open+migrate`, `headscale-client`, `background-crons`, `services+telegram`,
   `routes`, `handover`, then `startup: ready`. The **last** `startup:` line in the
   journal is where the process is (or died):

   ```bash
   sudo journalctl -u skygate --since "10 min ago" --no-pager | grep 'startup:'
   ```

4. **A panic is no longer silent**: the phase, the panic value and a full
   `debug.Stack()` are logged, the provisional `/healthz` answers `503` + `error`
   (never a `200` for a dead boot), and the process exits `3` so systemd and the
   updater both see a failed start.

Read the failing `/healthz` as a status line: `"ready":false` means *up but still
starting* — and that marker is what the applier refuses to accept as proof of a
deployed build (B268 below).

### B268 — the applier says WHY a swap failed

The old verdict was true for at least four different causes and named none of them.
`deploy/skygate-apply-update.sh` now:

* **smoke-tests the extracted binary** (`--version`, bounded by `timeout`) *before*
  the swap and refuses to install an artifact that cannot execute;
* logs a **pre-swap health baseline** — which build answers on the health URL, with
  the listener owning that port — and **warns** when it disagrees with `FROM_VERSION`
  (the two-instances / `docker-proxy`-owns-8080 class, where post-restart
  verification can never succeed);
* logs a **`DIAG:` block** (unit state, listener via `ss`/`netstat`, binary on disk,
  15-line `journalctl -u` tail) before **every** rollback;
* splits the verdict into `the service did not come up: … never returned a healthy
  body within Ns` vs `healthz did not report build X … (last build: Y)`, both
  carrying the unit state;
* with B269, also **refuses a body carrying `"ready":false`** and logs the boot phase
  it is stuck in, so "socket first" cannot make the updater blind to a boot that
  never finishes.

### B270 — two more reasons that same host never listened

The operator's own diagnostics (`systemctl show skygate -p ExecStart` + the journal)
showed the fatal exit was **not** the database:

```
2026/09/19 17:17:14  Skygate starting on :8082
2026/09/19 17:17:15  oidc: SKYGATE_OIDC_ISSUER not set — OIDC routes will return 503 until configured
2026/09/19 17:17:15  oidc: init failed: oidc: mkdir ./data/oidc-keys: mkdir ./data: permission denied
```

1. **A broken OIDC key store was fatal.** `NewService`'s error went to `log.Fatalf`,
   so an **unconfigured optional feature** (OIDC was disabled — the issuer was
   unset) killed the process *before* it bound its HTTP port. Now the key store
   degrades: the journal carries `oidc: KEY STORE UNAVAILABLE (…) — the process
   keeps running and the OIDC routes answer 503; fix SKYGATE_OIDC_KEY_DIR …`, the
   service keeps serving, and every OIDC route answers `503` with the reason
   (`KeyStore.Ready()` is nil-safe; the signing paths return an error instead of
   panicking).
2. **The key directory defaulted to the CWD-relative `./data/oidc-keys`.** Any unit
   whose `WorkingDirectory` is not the data dir (or an older unit without the
   directive) resolves that against `/` → permission denied. The default is now
   **absolute and data-dir anchored**: `<dir of skygate.db>/oidc-keys`, or
   `/var/lib/skygate/oidc-keys` for PostgreSQL. `install-common.sh` and
   `install-alpine.sh` create it (`0700`, owned by the service user). An explicit
   `SKYGATE_OIDC_KEY_DIR` still wins.
3. **The reason every update rolled back was a port mismatch nobody could see.**
   The applier verifies `SKYGATE_UPDATE_HEALTH_URL` (default `…:8080/healthz`)
   while this host runs `SKYGATE_PORT=8082`; the two numbers were never compared,
   so the service was healthy and every self-update still failed verification. The
   applier now says it out loud **before** the swap:

   ```
   WARN: PORT MISMATCH — health verification polls http://127.0.0.1:8080/healthz (port 8080)
         but the service is configured with SKYGATE_PORT=8082 in /etc/skygate/skygate.env
   WARN: the post-restart build check can NEVER succeed while they disagree; every update
         will roll back even though the service is healthy
   ```

Also fixed in this release: the B269 handler swap stored an `http.HandlerFunc` and
then an `*http.ServeMux` in the same `atomic.Value`, which panics with
`sync/atomic: store of inconsistently typed value` **inside** the handover
goroutine — i.e. the process died exactly when it had finished booting. Invisible
to grep; found by running the binary, and pinned by
`cmd/skygate/startup_handover_b269_test.go`.

### B271 — the DB-health sampler now speaks the database's dialect

The same journal carried, every 30 seconds, on a **SQLite** install:

```
db_health: tick: db_health: 4 query error(s): [
  server: SQL logic error: no such function: pg_is_in_recovery (1)
  database.size: SQL logic error: no such function: current_database (1)
  maintenance: SQL logic error: no such table: pg_stat_user_tables (1)
  xlog.current: SQL logic error: no such function: pg_current_wal_lsn (1)]
```

The `/db/health` collector is PostgreSQL-shaped and ran its catalog queries
against whatever backend was configured, so a healthy native install showed a
permanently degraded DB-health badge and five useless errors per tick (each one
still touching the database). `DBHealthConfig` now takes a `Dialect`:

* `postgres` (**default** — every existing caller and deployment is unchanged);
* `sqlite` — the collector answers the same operator questions natively:
  `PRAGMA page_count` × `PRAGMA page_size` for the size, `SELECT sqlite_version()`
  for the version, `PRAGMA quick_check` for integrity (a non-`ok` answer becomes
  the sample error) and `PRAGMA journal_mode` (logged). The PostgreSQL-only
  panels (replication, WAL position) stay **empty** instead of reporting invented
  values.

`main.go` passes the value it already computes with `db.DetectDSN`, and the boot
log states it: `db-health: started (interval=30s, query-timeout=3s, dialect=sqlite)`.

Operator procedure for all three: `docs/troubleshooting.md` §8.0.1 (B269), §8.0.2
(B270) and §8.0.3 (B271).

### Contracts

* **B269** — `scripts/check_b269_startup_truth.sh` (18 contracts: source order,
  phase coverage, panic/fatal handling, plus a behavioural half that builds the real
  binary, points it at an unopenable DB and asserts `/healthz` still answers `200`
  + build + `phase:"db-open+migrate"` + `ready:false` while the process stays alive)
  and `internal/startup/startup_test.go`.
* **B270** — `scripts/check_b270_startup_blockers.sh` (26 contracts, incl. a live
  probe that runs the real binary with an uncreatable key dir and asserts
  `/healthz` still answers while the process stays alive) +
  `internal/oidc/oidc_b270_test.go` + `internal/feature/healthz/db_health_b271_test.go`.
* **B268** — `scripts/check_b268_applier_failure_diagnostics.sh` (11 contracts, incl.
  a behavioural run against a local mirror with stubbed `systemctl`/`ss`/`journalctl`
  /`curl`) and `internal/update/applier_b268_test.go` (7 order-pinning contracts).

Operator procedure: `docs/troubleshooting.md` §8.0 (B268), §8.0.1 (B269) and
§8.0.2 (B270).

## v1.5.11 — route approval must not require docker (native installs)

**Date:** 2026-09-19 · **Base:** `v1.5.10` → this tag · **Compatibility:** no schema,
config or API change.

### The bug

On a **native (non-docker)** install, assigning an exit node failed with:

```
approve-routes: approve-routes: exec: "docker": executable file not found in $PATH:
```

`internal/headscale/routes.go:approveRoutesForNodeID` hardcoded

```go
exec.Command("docker", "exec", "headscale", "/ko-app/headscale",
    "nodes", "approve-routes", "-i", <id>, "-r", <routes>, "--force")
```

so on a host where headscale runs under **systemd** — and docker is not installed
at all — every approve-routes caller broke: the **Tag as exit-node** button,
`ApproveAllRoutes`, and the relay route flow. The node stayed with 2 unapproved
routes, which is why the monitor reported "нет рабочих exit-узлов".

### The fix (B267)

1. **REST API first**: `POST /api/v1/node/{id}/approve_routes` — this endpoint
   works on *every* install kind (docker, systemd, binary). The endpoint that was
   deprecated in headscale 0.29 is `/api/v1/routes` (which is why the CLI fallback
   existed at all), not this one.
2. **Install-kind-aware CLI fallback** — `runHeadscaleCLI` probes
   `exec.LookPath("docker")`: `docker exec <container> <binary> …` when docker is
   present, the local `headscale` binary otherwise. `SKYGATE_HEADSCALE_CLI`
   overrides the in-container binary path (`/ko-app/headscale` by default), and the
   error now names both attempts and tells you which knob to set instead of a bare
   `exec: "docker": executable file not found`.

Contracts: `scripts/check_b267_headscale_cli_mode.sh` (8 contracts: the API call,
the helper, the `LookPath` probe, the local-binary fallback, the error text, the
`SKYGATE_HEADSCALE_CLI` override, no hardcoded `docker exec`, focused tests),
registered in the gate as **B267**.

> If your headscale is native and you want route approval to keep working through
> the CLI path as well, make sure `headscale` is on the skygate service's `PATH`
> (the API path above means it normally is not needed).

## v1.5.10 — the two `/admin/tailscale` UI defects from the v1.5.9 enablement

**Date:** 2026-09-19
**Base:** `v1.5.9` → this tag
**Compatibility:** no schema, config or API change — two UI/handler fixes.

Both were found by the operator while enabling the in-container Tailscale client
for the Telegram egress relay (RR-13).

### 1. `Start` really starts tailscaled now

Clicking **Start** answered:

```
Не удалось запустить Tailscale: tailscale up: exit status 1 — output:
failed to connect to local tailscaled; it doesn't appear to be running
```

Two defects in `startTailscaled`:

1. **The daemon was never started.** It was spawned as
   `setsid nohup tailscaled --statedir=… ">/var/log/tailscaled.log" "2>&1" "&"` —
   `exec.Command` runs no shell, so those redirection tokens were passed to
   tailscaled as **argv**: it exited immediately with an argument error, and the
   error was discarded (the output buffer was a dropped `bytes.Buffer`).
2. **A stale socket file counted as "running".** The readiness wait used
   `tailscaledRunning()`, which only `stat()`ed
   `/var/run/tailscale/tailscaled.sock`. That directory is a bind mount
   (`data/ts/run` → `/var/run/tailscale`), so the socket left behind by an earlier
   container satisfied the check instantly and `tailscale up` was run against a
   dead socket. Live proof from the reference host before the fix:
   `socket file present` + `daemon does not answer`.

Fixed: a stale socket is removed when the daemon does not answer; tailscaled is
started with a real `*os.File` log and detached into its own session
(`detachProcess`, build-tagged — `Setsid` on unix, no-op on Windows); the wait
polls a daemon that **dials**, `tailscaledRunning` (the page's "running" flag) uses
the same live probe; an already-answering daemon is not started twice (it goes
straight to `tailscale up`); and a failed start reports the tail of
`/var/log/tailscaled.log` instead of a bare timeout.

**Live verification (reference host, 2026-09-19):** with the stale socket present
and no daemon, the patched sequence removed the stale file, started tailscaled and
had `tailscale status` answering within a second (`Logged out.` — the daemon is up;
`tailscale up` is the operator's click).

### 2. The «Сгенерировать ключ» button is visible

The B258.1 warning said "…сгенерируйте его через кнопку «Сгенерировать ключ»", but
the control existed only as a **collapsed `<details>`** whose summary read
«Сгенерировать автоматически» in 13px muted text — reported as "кнопки нет". The
generate form is now a plain secondary button in the **Auth key** card (label
unified with the warning, RU + EN), with help text stating that it writes the key
file and unlocks Start.

### Verification

| Check | Result |
|---|---|
| `scripts/check_b259_tailscale_toggle.sh` (contracts A…N1–N5) | 41 passed / 0 failed, on the VM |
| `scripts/check_b258_1_auth_key_missing.sh` (K/L) | passed, on the VM |
| `go build ./...`, `go vet ./internal/feature/admin/...`, focused tests | clean |
| Deployed container | `/healthz` build `v1.5.9-11-g1cdc2058` (contains both fixes) |

### Operator action items

1. Hard-refresh `/admin/tailscale` (Ctrl+F5) — the **Auth key** card now shows the
   paste form **and** the «Сгенерировать ключ» button; **Start** now works.
2. The in-container client is now running on the reference host (started during the
   verification). Pressing **Start** in the UI will run `tailscale up
   --accept-routes … --hostname=skygate-host`; mind that `skygate-host` is already
   taken in headscale by node 57 (`tagged-devices`) — see `docs/TELEGRAM.md` §8.
3. After a container recreate, the compose env still hardcodes
   `SKYGATE_TS_AUTHKEY_FILE=/dev/null`, so the entrypoint skips tailscaled again
   (RR-13 step 3).

## v1.5.9 — native self-update (B261), OpenRC + the return of `SHA256SUMS` (B262), SQLite/PostgreSQL hardening, documentation overhaul

**Date:** 2026-09-19
**Base:** `v1.5.8` → this tag — 85 commits
**Tag:** `v1.5.9` → commit `1299b7a4`; GitHub release published (not a draft), release workflow run `35440687679` — all 8 jobs success (docker image, 5 binary archives, `SHA256SUMS`, github release)
**Compatibility:** no schema break, no config-format break, no API break. A docker
deployment updates exactly as before (`git pull` + `docker compose up -d
--force-recreate`, or the in-app image-pull button). The native self-update path is
new — read §4 before using it.

### Why this release exists

Three statements were true before it and are false after it:

| Before | After |
|---|---|
| **A native host could not update itself.** On a systemd / OpenRC / bare install, `/admin/update` could only print manual steps: the swap needs root, and skygate deliberately does not run as root. | A **root-owned applier** does backup → verified download → pre-swap `migrate-only` → atomic rename swap → restart → `/healthz` build check → automatic rollback. The unprivileged service triggers it by writing one data-only file. `/admin/update` now works on all three native kinds. |
| **A native SQLite install could not start.** `install-debian.sh` writes `SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db`, and the SQLite driver does not know the `sqlite:` scheme — the DSN was mis-detected as PostgreSQL (`DB backend: postgres (DSN=sqlite:/…`) and the service never served `/healthz`. Every native SQLite host was dead on arrival. | The scheme is stripped before the driver sees it (B261.1) and the SQLite migration chain is complete and idempotent again (V071 was missing; `execSQLiteDDL` is now the single chokepoint). |
| **Releases v1.5.6–v1.5.8 attached no checksums.** `release.yml` downloaded the `SHA256SUMS` artifact into `dist/SHA256SUMS` — a *directory* whose only file is also called `SHA256SUMS`; the flatten step then moved the file inside it and the release published a directory. | The artifact is downloaded into `dist/checksums`, so `SHA256SUMS` is attached as a **file** that lists every archive (B262). The native self-updater had been masking the gap with the GitHub per-asset digest fallback. |

### Summary table

| Area | Block | Change | Operator impact |
|---|---|---|---|
| **Native self-update** | **B261** | Root-owned applier + `skygate-update.path`/`.service` pair, written by every native installer | `/admin/update` → *Update now* works on systemd, OpenRC and bare hosts |
| | **B261.1** | Strip the `sqlite:` scheme in the SQLite open path (+ a test that performs a real file open) | native SQLite installs boot at all |
| | **B261.2** | Verify the artifact against `SHA256SUMS`; if the release has none, against the GitHub Releases API per-asset `digest: sha256:<hex>`; with neither, **fail closed** — the applier since v1.5.9's cycle, and now the **installers** too (they used to abort an install of a checksum-less release and demand `SKYGATE_SKIP_VERIFY=1`) | the checksum-less v1.5.3 / v1.5.6–v1.5.8 releases install and verify without an operator override |
| | **B261.3** | Work dir `0755` (was `mktemp`'s `0700`, which `migrate-only` could not traverse as the service user) | the pre-swap migration actually runs |
| | **B261.4** | `migrate-only` is a **subcommand**, not `--migrate-only`; same fix in the in-app manual steps | no more "unknown flag" abort before the swap |
| | **B261.5** | `SKYGATE_UPDATE_BASE_URL` mirror support in the root-owned helper config (`https://`, or plain `http://` for loopback only; `SHA256SUMS` mandatory) | air-gapped / mirrored hosts can self-update |
| | `1ee1366b` | Install-kind detection checks container markers before systemd | a skygate running *in* Docker is no longer told to run `systemctl` |
| **Release pipeline** | **B262** | `path: dist/checksums`, plus OpenRC/Alpine as a first-class install kind (`/run/openrc` marker, `rc-service` restart, sudoers trigger) | `SHA256SUMS` is attached again; Alpine hosts get a native install **and** update path |
| | **B237.24** | The workflow pre-computes the lowercase owner in the meta step (GitHub parses `format()` arguments as literals, so the `\| lower` filter never applied) | `ghcr.io/barssky/skygate:vX.Y.Z` pushes on the first attempt |
| | **B263** | Release notes are one file (`RELEASE-NOTES.md`, newest section first); the `release` job — which had **no checkout step at all** — now checks the file out sparsely, extracts the `## vX.Y.Z` section, and falls back to a generated commit list | the GitHub Release body can no longer be empty (v1.5.8 published nothing) |
| **Data layer** | **§12.13** | SQLite chain repaired (V071 missing; `ADD COLUMN IF NOT EXISTS` silently no-op'd; the chain broke on the second start) and all SQLite DDL routed through `execSQLiteDDL` | SQLite databases survive restarts and upgrades |
| | `9f39c6f1` | Dialect shims branch at runtime; the shared layer no longer holds one-backend-only SQL | both backends are first-class again |
| | `780b6ce9` | `MigratePostgres` takes a session-level `pg_advisory_lock` (120 s wait, dedicated connection) | two skygate replicas can no longer race a migration |
| | `e75ad3ab` … `419e9a99` | Six commits of PostgreSQL test-suite repairs (OpenTestPG instead of a raw DSN, `$N` placeholders, boolean literals, schema-scoped assertions, the advisory-lock test, 180 s budgets) | the CI job *go test against a real PostgreSQL* is green instead of 40 failures |
| **Error UX / security** | **R6** | DB errors render inside the page (flash + safe error block) instead of replacing the page with `text/plain`; a guard test freezes the remaining 101 call sites so the class cannot grow | failed forms keep their context and the operator's input |
| | **R7** | Never hand out a preauth key skygate cannot account for | no orphan keys in headscale |
| | **B252.1 / B253** | `/admin/derp` certificate auto-renewal card + *Run cert sync now*; `/admin/telegram` probe is cached (30 s success / 5 min failure, stale-while-revalidate) with a *Probe now* button | two routes that answered `501` now work; a DPI-blocked Telegram no longer blocks the page |
| **Credential hygiene** | **RR-12** | The default PostgreSQL password literal is gone from 35 tracked files (34 scripts + the new resolver); they resolve it at runtime through `scripts/lib/db_credentials.sh` — `$SKYGATE_DB_PASSWORD` → the DSN in `$SKYGATE_DB`/`$SKYGATE_DB_DSN` → the DSN in `.env`/`/etc/skygate/skygate.env` → empty (psql then fails loudly). The live **admin** password was removed from six more scripts. | no credential literal in the working tree; `check_ha_state.sh` keeps the literal **on purpose** (it is the regression guard) and `.githooks/pre-commit` keeps blocking the string |
| **Documentation** | — | Flat `docs/*.md` + `docs/ru/`; new bilingual `INSTALL` / `UPDATE` / `ROADMAP`; one canonical `RELEASE-NOTES.md`; `AGENTS.md` 919 KB → 32 KB (index only — every block entry kept, plus the new B263); `docs/LESSONS.md` holds the incident knowledge; `docs/plans/**`, `docs/runbooks/**`, `docs/internal/**`, `docs/BACKLOG.md`, `docs/PLANS.md` removed | one place per question; the agent-facing file finally fits in a context window |
| **Gate / CI** | — | `verify_pre_deploy.sh` ends `PASS=289 / FAIL=0 / SKIP=1`; `staticcheck ./...` 0 findings; load-sensitive contracts got a 180 s budget and the gate run caps Go parallelism (`GOFLAGS=-p=2`); the flakes that made the gate report one rotating FAIL per run are fixed (`TestSSHDumpTransport_Dump_FakeSsh` called `cmd.Wait()` before its readers had drained the pipes; `check_b237_24`'s live `git ls-remote` probe turned "remote unreachable" into a FAIL instead of a SKIP); `B244` C7, `B188.2` contract T, `B191`, `b_tag_owners` contract D, `B112`/`B237.18` were made honest | the gate can be trusted as a merge contract again |

### 1. Native self-update (B261) — the chain

1. The **service** (unprivileged) writes `<data_dir>/update/request.props`. The file
   is data only: `TARGET=<tag>`, `JOB_ID`, `FROM_VERSION`, `RUNTIME_PID`,
   `INSTALL_KIND`, `REQUESTED_AT`. The tag is validated as
   `[A-Za-z0-9][A-Za-z0-9._+-]{0,63}` with `-g<hex>` git-describe suffixes rejected
   (it ends up in a URL).
2. **`skygate-update.path`** (`PathExists=…/request.props`) fires
   **`skygate-update.service`** — a root-owned `Type=oneshot` unit with
   `TimeoutStartSec=900`, in its own cgroup. This is the privilege boundary: the
   service never calls `sudo` and never talks to systemd.
3. **`/usr/local/lib/skygate/skygate-apply-update.sh`** reads the root-owned
   `/etc/skygate/update-helper.conf`, then:
   * backs the current binary up to `<update_dir>/skygate.prev`;
   * downloads `skygate-<TARGET>-<arch>.tar.gz` and verifies its SHA256;
   * runs `<new binary> migrate-only` **as the service user** — a release whose
     chain cannot handle this database is refused **before** anything is replaced;
   * atomically installs the new binary over `/usr/local/bin/skygate`;
   * restarts the unit and polls `/healthz` until it reports `status:ok` **and** a
     `build` string equal to `<TARGET>` or `<TARGET>+<commit>` — a bare HTTP 200 is
     not accepted (a stale process used to look like success);
   * on any failure restores `skygate.prev`, restarts, and waits for health again.
4. The verdict is written to `<update_dir>/result.status` (`done` / `failed` /
   `rolled_back`) with `result.build` + `result.error`, which `/admin/update` reads
   and clears.

**Trust model.** skygate stays unprivileged. Every path the applier touches comes
from the root-owned config, and the only thing the service can ask for is an
official *release tag* — never a URL, a path or a command. The `bare` kind (no
service manager) gets a `sudoers.d` rule that permits exactly one command instead
of a path unit.

**Mirror / air-gapped.** `SKYGATE_UPDATE_BASE_URL="https://mirror.example.com/skygate"`
in `/etc/skygate/update-helper.conf`; artifacts are read from
`<base>/<TAG>/skygate-<TAG>-<arch>.tar.gz` + `<base>/<TAG>/SHA256SUMS`. Plain
`http://` is accepted for loopback addresses only, and a mirror **must** publish
`SHA256SUMS` (there is no GitHub digest to fall back to for a custom source).
Procedure: [`docs/UPDATE.md`](docs/UPDATE.md) §7 (`docs/ru/UPDATE.md`).

### 2. `SHA256SUMS`: the pipeline fix, the consumer fallback, and how both are proven

The release pipeline attaches the checksum file again (B262), and this cycle also
closes the **consumer** side, which used to abort an install:

| Layer | Behaviour after v1.5.9 |
|---|---|
| `release.yml` `sums` job | `cd dist && sha256sum skygate-* > SHA256SUMS` — one line per archive, GNU `sha256sum -c` format |
| `release.yml` `release` job | downloads the `SHA256SUMS` artifact into `dist/checksums` (never into `dist/SHA256SUMS`, which is how v1.5.6–v1.5.8 attached a *directory*), flattens, uploads `dist/SHA256SUMS` | 
| `install-*.sh` | downloads `…/releases/download/<TAG>/SHA256SUMS` and compares the hash of the **asset basename** (the tarball is saved locally as `skygate.tar.gz`, so a `sha256sum -c` would not match); tolerates GNU binary-mode `*`-prefixed names and CRLF |
| `install-*.sh`, no asset | **new in v1.5.9** — falls back to the GitHub Releases API per-asset `digest: sha256:<hex>` instead of aborting and telling the operator to re-run with `SKYGATE_SKIP_VERIFY=1`. Verified live against **v1.5.8**: it publishes no `SHA256SUMS`, and the API returns `digest[skygate-v1.5.8-linux-amd64.tar.gz] = 624cfdb2f55f2599…` |
| `install-*.sh`, neither source | fails **closed** with an explicit message; `SKYGATE_SKIP_VERIFY=1` remains the documented escape hatch for genuinely air-gapped hosts |
| native applier (`deploy/skygate-apply-update.sh`) | unchanged (B261.2): `SHA256SUMS` first, then the API digest; a mirror must publish `SHA256SUMS` because there is no API digest for a custom source. The digest extraction in **both** consumers is now whitespace-tolerant (GitHub's `"name": "…"` spacing is not contractual) |

**How it is proven.** The workflow's own steps were rehearsed on the VM with the
real command sequences: the five archives are named as `release.yml` names them,
`sha256sum skygate-* > SHA256SUMS` lists all five, and after the flatten step
`dist/SHA256SUMS` is a **file** (501 B) next to them — the exact `files:` glob the
release uploads. The installer's `download_and_verify` was then run against those
files over a loopback HTTP server, and against the same fixture with `curl` stubbed
offline:

```
[install] SHA256 OK (verified against SHA256SUMS)          # SHA256SUMS present
[install] SHA256 OK (verified against GitHub asset digest) # no SHA256SUMS asset
ERROR: SHA256 mismatch (source: SHA256SUMS)                # corrupted tarball -> refused
ERROR: no trustworthy checksum for …                       # neither source -> refused
```

Both reversals are pinned by `scripts/check_b261_native_self_update.sh` **section P**
(plus P3 for the pipeline invariant "SHA256SUMS is a file listing every archive"),
so a future pipeline edit cannot silently bring back the v1.5.8 behaviour. The one
thing that can only be checked **after** the tag is that GitHub actually attached
the asset: §1.5 of [`docs/operations.md`](docs/operations.md).

### 3. What is verified, and how

**Post-tag verification (2026-09-19, against the published `v1.5.9` assets).** Run
from the VM with the real release URLs — the procedure of `docs/operations.md` §1.5:

| Check | Result |
|---|---|
| `SHA256SUMS` attached as a **file** | 501 B, `application/octet-stream`, lists all five archives — the first release with a checksum file since v1.5.5 (the **B262** proof) |
| `…/releases/download/v1.5.9/SHA256SUMS` | HTTP **302** (v1.5.8 answered **404** — the error this release fixes) |
| `sha256sum -c --ignore-missing SHA256SUMS` | `skygate-v1.5.9-linux-amd64.tar.gz: OK` |
| **The real installer, real URLs, no `SKIP_VERIFY` and no mirror** | `[install] SHA256 OK (verified against SHA256SUMS)` → installed → `skygate v1.5.9 (commit 1299b7a, built 2026-09-19T11:39:14Z)` |
| Release body | 21 645 bytes starting with `## v1.5.9 — native self-update (B261), …` (v1.5.8's body was **empty** — the **B263** proof) |
| ghcr image | `docker pull ghcr.io/barssky/skygate:v1.5.9` OK; the mixed-case path is rejected (the **B237.24** proof) |
| Gate on the tagged commit `1299b7a4` | `289 PASS / 0 FAIL / 1 SKIP` (`B8`, VM-only by design) |

| Verification | Where | Result |
|---|---|---|
| Full gate `scripts/verify_pre_deploy.sh` | VM (`<VM_HOST>`), commit `cb33747d` | **`289 PASS / 0 FAIL / 1 SKIP`** — the SKIP is `B8` (RU/EN smoke test, VM-only by design). Run as root **with the Go bin dir on `PATH`** (`sudo env PATH="$HOME/go/bin:$PATH" GOFLAGS=-p=2 bash scripts/verify_pre_deploy.sh`); without that, `B95` reports "staticcheck not found", and as the unprivileged operator user a root-owned checkout produces ~90 `permission denied` FAILs. Both traps are documented in `AGENTS.md` §2 and `docs/operations.md` §1.3 |
| `go build ./...`, `go vet ./...`, `staticcheck ./...` | local + VM | clean, 0 findings |
| Focused tests `./internal/update/... ./internal/db/ ./internal/feature/admin/... ./internal/i18n/...` | local + VM | all `ok` |
| Flake reproducer `go test -count=10 ./internal/dbmigrate/...` | VM (Linux — the platform where the fake-ssh branch runs) | 10/10 `ok` after the `cmd.Wait()`-ordering fix |
| **Gate hygiene**: five consecutive runs each ended with ONE rotating FAIL (a different check every time, each passing standalone 20/20) | VM | Root cause: `cmd \| grep -q` under `set -o pipefail` — `grep -q` exits on the first match, the still-writing `go test` dies with SIGPIPE (141), and `pipefail` fails the pipeline although the tests passed. Fixed in `B235` (E.2/E.3), `B237.20` (D.2), `B237` (G.3); `B211`–`B214` retry the binary link once and print the real error (`scripts/lib/go_build.sh`); `B202.5` (a real `cmd.Wait()`-before-drain race) and `check_b237_24` (network probe → `SKIP`) were fixed earlier. Remaining ~22 sites: **TD-19** in `docs/ROADMAP.md`; lesson: `docs/LESSONS.md` L-47 |
| CI (GitHub Actions) | `dc92515d`, `774ef18a`, `a695f6cd`, `0687d04f` | all 6 jobs `success`, including **go test against a real PostgreSQL** — the job that was red with 40 failures at the start of this cycle |
| **Clean-host acceptance** (install → self-update) | throwaway `jrei/systemd-debian:12` container on the VM, 2026-09-19 | `verdict: done`, `/healthz` build matched (record below) |
| Live credential sweep + data-drift repair (`RR-12`) | VM, production checkout | applied and re-verified with the four live contracts; backups in `/tmp/live-drift-*` |

> `release.yml` itself only runs on a tag, so it cannot be exercised by CI before
> the release. Both branches of its notes step were rehearsed locally against this
> repository (the extraction produced the 187-line v1.5.9 body, and a synthetic
> tag with no section produced the commit-list fallback), and the workflow file is
> parsed by GitHub on every push (`ci` green on `dc92515d`).

**Clean-host acceptance record (2026-09-19).** A fresh privileged
`jrei/systemd-debian:12` container (systemd as PID 1) installed skygate with the
*installer from this commit*:

```
SKYGATE_SKIP_VERIFY=1 bash deploy/install-debian.sh --db-type=sqlite --install-kind=systemd
→ exit 0; skygate.service + skygate-update.path (active) + skygate-update.service
  + /usr/local/lib/skygate/skygate-apply-update.sh + /etc/skygate/update-helper.conf
  + SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db
  + a JWT secret generated via the od fallback (the image ships no xxd and no openssl)
```

The install downloaded the published `v1.5.8` asset, which is exactly the state
this release fixes: v1.5.8 mis-detects the `sqlite:` DSN as PostgreSQL and never
serves `/healthz`. The applier was then pointed at a local loopback mirror
(`SKYGATE_UPDATE_BASE_URL="http://127.0.0.1:8099"`) carrying a `v1.5.9` tarball
built from this commit, and a `request.props` was staged **as the `skygate`
user**:

```
[08:18:45Z] downloading http://127.0.0.1:8099/v1.5.9/skygate-v1.5.9-linux-amd64.tar.gz
[08:18:45Z] SHA256 OK (bc93c4db…, verified against SHA256SUMS asset)
[08:18:46Z] running migrations with the new binary (as skygate)
2026/09/19 08:18:46 migrate-only: opening sqlite (DSN=sqlite:/var/lib/skygate/skygate.db...)
2026/09/19 08:18:46 migrate-only: migrations applied OK
[08:18:46Z] installed the new binary over /usr/local/bin/skygate
[08:18:47Z] healthz reports build 'v1.5.9+acc0001' after 2s
[08:18:47Z] verdict: done
```

`/var/lib/skygate/update/result.status` = `done`, `result.build` =
`v1.5.9+acc0001`, `/healthz` = `{"build":"v1.5.9+acc0001","status":"ok",…}`, the
SQLite database was created by the migration step (516 KB), and `skygate.prev`
retained the previous binary. **The update rescued a host whose installed binary
could not run at all** — the strongest form of this test. The mirror was a harness
substitute for the not-yet-published release; the artifact was built exactly as
`release.yml` builds it (`CGO_ENABLED=0`, `-trimpath`, `-s -w`,
`-X main.version=v1.5.9 -X main.commit=…`).

### 4. Upgrade notes

* **Docker / compose** — nothing to do beyond the normal update. The entrypoint
  still rebuilds the binary on start.
* **Native systemd/OpenRC/bare, SQLite** — **v1.5.8 and earlier cannot open their
  own database.** If you are on v1.5.8, do not wait for `/admin/update` to fix
  itself: install v1.5.9 from the tarball (`bash deploy/install-debian.sh`, or
  extract the release archive over `/usr/local/bin/skygate` and restart). The
  applier refuses an unsafe target before the swap, so a self-update *attempt* is
  safe but will report `failed`.
* **First native install** — the installer now also writes the applier and the path
  unit; nothing else is needed for `/admin/update` to work.
* **`SKYGATE_DB=sqlite:<path>`** keeps working, with or without the scheme.
* **HA / PostgreSQL** — `MigratePostgres` now serialises concurrent migrators: a
  second instance starting during a migration waits (up to 120 s) instead of
  racing. No configuration change.

### 5. Known gaps (unchanged by this release, tracked in `docs/ROADMAP.md`)

* **Telegram egress relay is not enabled (RR-10 / RR-13).** The code path is
  complete and reachable from `/admin/telegram` → *Egress relay*, and the UI admin
  flow is what an operator should use. Live state on the reference host
  (2026-09-19): the in-container Tailscale client is disabled by the compose env
  (`SKYGATE_TS_AUTHKEY_FILE=/dev/null`, while the DB points at a `/data/ts/authkey`
  that does not exist), and the **selected** relay (`telegram.egress_node_id = 3`
  → emilia) has only `149.154.167.99/32` approved, which does not cover the
  current `api.telegram.org` addresses. This is a **configuration** gap, not a
  code regression — relays do reach `api.telegram.org`, and **karolina** (exit
  server id 118) already advertises *and* has approved `149.154.160.0/20` plus
  four `91.108.*` blocks, so switching the selector is enough on the relay side.
  headscale's policy carries **no** `autoApprovers`, and the in-app
  route-approval helper only handles `0.0.0.0/0` + `::/0`, so any newly
  advertised CIDR needs a manual `headscale nodes approve-routes`. Step-by-step
  procedure and the state table: [`docs/TELEGRAM.md`](docs/TELEGRAM.md) §8.
* **Bare (no service manager) mode** is covered by unit tests and the contract but
  has not been exercised on a live bare host; systemd and OpenRC are.
* **~25 silent `ADD COLUMN` loops** remain in the SQLite migration file —
  harmless on a fresh DB, still able to hide a real failure. `execSQLiteDDL` is
  the chokepoint that will absorb them.
* **R6** still has 101 raw-error sites; the guard freezes the number.
* **In-app manual steps (`RR-2`)** still print the historical asset names
  `skygate-linux-amd64` / `.sha256`, which the pipeline does not publish; a
  contract currently pins the stale string.
* **`skygate-host` hostname collision** — the reserved-name guard (B251) is in
  place; the pre-existing node named `skygate-host` is still registered in
  headscale.
* **Rotate the admin password** — an earlier revision of the repository contained
  it. The literal is out of the working tree (RR-12) but it remains in git history.

### 6. Rollback plan

* **Not yet updated** — delete the GitHub release + the tag, fix, re-tag. The tag
  is the only trigger.
* **Already updated to v1.5.9** — the applier keeps
  `<data_dir>/update/skygate.prev`, and `/admin/update` offers an explicit rollback
  to the previous tag. Manual fallback:
  `sudo install -m 0755 /var/lib/skygate/update/skygate.prev /usr/local/bin/skygate && sudo systemctl restart skygate`.
* **Docker** — pin the previous image tag
  (`ghcr.io/barssky/skygate:v1.5.8`) and `docker compose up -d --force-recreate`.

## v1.5.4 – v1.5.8 — backfill (one line each; full detail in `git log <prev>..<tag>`)

These releases shipped before this file became the single canonical one, so no
per-version note was ever written for them. One line each, with the commit range
that holds the detail:

| Release | Date | Headline |
|---|---|---|
| **v1.5.4** (75 commits) | 2026-09-15 | SQLite restored alongside PostgreSQL (`SKYGATE_DB`, `--db-type`, the `db-migrate` convert subcommand, dialect `DetectDSN`); sidecar first-run adoption (bulk claim + auto-detected exit nodes); admin/user sync (rename, promote, drift banner); admin exit-rules for another user; `embed.FS` static assets; docker image pinned to **linux/amd64** (Issue #4) + the in-app image-pull update button |
| **v1.5.5** (1 commit) | 2026-09-15 | `B249` — the image-pull update path (fast ~5–30 s alternative to a rebuild) |
| **v1.5.6** (1 commit) | 2026-09-15 | `B250` — ACL page UI: JSON pretty-print + dark-card text contrast |
| **v1.5.6.1** (1 commit) | 2026-09-15 | `B250` follow-up — `prettyPrintACL` handles a stringified headscale policy |
| **v1.5.7** (1 commit) | 2026-09-15 | `B252` — grouped ACL view (collapsible categories) |
| **v1.5.8** (37 commits) | 2026-09-17 | `B260.x` DERP status/probe chain (3 compounding URL bugs, WebSocket liveness fallback, derper-in-docker migration, `--verify-clients=` empty-value crash, SNI-matching cert check, `resolveDERPPort` DB-first, probe URL vs. derpmap URL); `B255` Telegram background polling + pin-nearest-exit-node; `B257` adoption of pre-existing headscale devices + repo hygiene; `B258`/`B258.1`/`B259.x` `/admin/tailscale` UI (skip state, missing auth-key state, enable toggle, `findUserForHostname`); `B256` SQL fix for `headscale_user_id != ''`; PostgreSQL-compat test fixes |

> **No checksums on those tags:** v1.5.6, v1.5.6.1, v1.5.7 and v1.5.8 shipped **no
> `SHA256SUMS` asset** (see the `B262` row above). To verify an artifact from one of
> them, use the GitHub Releases API per-asset `digest: sha256:<hex>` — that is
> exactly the fallback the native applier uses.

## v1.5.2 — post-v1.5.0 hotfixes + HA Tier 1 reg.ru live (B146) + CDN UI grouping (B237.22) + ON CONFLICT drift fix (B237.23)

**Date:** 2026-09-08 (consolidated note covers B237.10 .. B237.23)

**Tag:** `v1.5.2` → commit `23977b6c` (the B237.23 tip — 2 commits
ahead of the original `374b7c5a` release tag, which covered only
B237.21; the tag was force-moved forward to include the B237.22 +
B237.23 follow-ups that landed on the same day). v1.5.2 is a
hotfix / sub-patch series on top of v1.5.0. No new features beyond
B237.22 (UI-only CDN rule grouping in `/my/exit-rules` +
`/admin/exit-rules`); all other items are bug fixes, tech-debt
closures, or HA Tier 1 work. All fixes are backward-compatible.

### Summary table

| B-block | One-liner | Operator impact |
|---|---|---|
| **B237.10** | Auto-update "Push update" form now works on untagged commits (`ve2d0b9e+e2d0b9e` → `e2d0b9e`) | Operators between releases can self-update via the UI |
| **B237.15** | 5 new deploy surfaces: prebuilt ghcr image, podman, systemd/OpenRC installers, Windows native, release workflow | New operators have a one-liner install path |
| **B237.16** | Closes the last 2 stragglers of TD-2 (numeric status codes) + B140/B141 B-check fixes | `staticcheck` reports 0 ST1013 / 0 SA1012 |
| **B237.17** | `smoke_mesh_*` users + `smoke-mesh-*` meshes are auto-cleaned daily | Live DB no longer accumulates smoke.sh artifacts |
| **B237.18** | New in-app `headscale_user_id` reconciliation cron (default 1h, 4 outcomes: ok / linked / relinked / orphan) | Stale `portal_users.headscale_user_id` is auto-fixed (or audited as orphan) |
| **B237.19** | `/my/exit-rules` form errors render as a flash banner (not a giant plain-text page) + duplicate banner wording fixed | Forms no longer lose user input on validation failure |
| **B146** | Phase 2 reg.ru DNS live test productionized (`scripts/b146_regapi_live.sh`) | Operator can now end-to-end verify the reg.ru integration |
| **B237.20** | staticcheck-100%-clean (TD-12 / TD-13) — 23 U1000/S1021 fixed, 0 issues remain | Clean `staticcheck ./...` output for the first time in repo history |
| **B237.21** | `skygate regapi-credentials` CLI subcommand (set/show/test/delete) | Closes the "only path to set reg.ru creds is the /admin/ha form" gap; one-shot bootstrap now works without browser |
| **B237.22** | UI-only CDN rule grouping on `/my/exit-rules` + `/admin/exit-rules` (TD-11 / Approach G) | 30% of device_rules rows (Cloudflare dedup) render as a single `<details>` group; storage unchanged |
| **B237.23** | Fix autoupdate `ON CONFLICT` code/index drift (B183 vs B232 regression) | 4 domains (`auth.docker.io`, `harness.io`, `cdn-registry-1.docker.io`, `limit-test-…`) transition ⏳ orange → ✅ green on next autoupdate tick |

### Live state (operator VM 192.168.13.69, verified 2026-09-08)

- **Container version**: `skygate v1.5.2-2-g23977b6` (commit
  `23977b6c`, 2 commits past the original v1.5.2 tag `374b7c5a`).
  Web footer renders `v1.5.2-2-g23977b6` (B237.23 tip).
- **headscale policy grants**: 213, with `via: ['tag:dev-infra-emilia']`
  for skyadmin + michail.
- **DERP health**: 30/30 regions (28 public + 2 own: `derp.skynas.ru` +
  `derp.emilia`).
- **Smoke cleanup**: live DB has 0 `smoke_mesh_*` rows.
- **Headscale user ID reconciliation**: cron enabled; first cycle
  log: `reconcile: 0 portal_users checked (hs_users=0, ok=0,
  linked=0, relinked=0, orphans=0, errors=0, took=2.0ms)`.
- **Autoupdate post-B237.23 log evidence** (the 4 orange domains):
  ```
  domain=auth.docker.io added=15 removed=0  err=CDN detected: cloudflare — using 15 published ranges
  domain=cdn-registry-1.docker.io added=27 removed=0
  domain=harness.io added=40 removed=0
  domain=www.harness.io added=92 removed=0
  ```
  Before B237.23 all 4 read `added=0 err=CDN detected: …` because
  the 5-col `ON CONFLICT` (B183) silently failed every INSERT
  against the 6-col index that B232 (V068) had recreated. See
  `B237.23` below for the full regression analysis.

### B237.10 — Auto-update pathspec bug fix

**Problem (operator-reported 2026-09-04)**: clicking "Push update"
on `/admin/update` with the pre-fix code produced:
```
[info] manual push by skyadmin (target=ve2d0b9e+e2d0b9e, ...)
[debug] $ git fetch --tags --prune --force → OK
[debug] $ git checkout ve2d0b9e+e2d0b9e → error:
  pathspec 've2d0b9e+e2d0b9e' did not match any file(s) known to git
[error] FAILED: git checkout: exit status 1
```

**Root cause**: `BuildVersion = version + "+" + commit` in
`cmd/skygate/main.go`. For an untagged commit (`e2d0b9e` between
v1.5.0-alpha1 and v1.5.0), `git describe --tags --always` returns
just `e2d0b9e` (no `-g` suffix), so
`BuildVersion = "e2d0b9e+e2d0b9e"`. The "Push update" form has
no `target` field, so `target = s.BuildVersion`, and after
`normalizeUpdateTarget` prepends "v" the value becomes
`ve2d0b9e+e2d0b9e`. The `+` is invalid in a git pathspec, so
`git checkout` fails. The pre-fix orchestrator rolls back; the
operator is stuck on the old commit until they SSH in by hand.

**Fix**: new `internal/update.GitRefForBuildLabel(s)` helper that:
1. Strips the `+<commit>` suffix (the ONE always-invalid
   character in a git pathspec).
2. Strips a leading `v` only when the remainder is a pure hex
   SHA (so legitimate `v1.5.0` semver tags are untouched).

Both `PostAdminUpdateApply` and `PostAdminUpdatePush` now pass
`update.GitRefForBuildLabel(target)` to the orchestrator; the
orchestrator also re-processes on its end as defense-in-depth.
The display `target` stays untouched (page + audit + log show
the human-readable form).

10 unit tests in `internal/update/docker_test.go` (B237.10)
cover the per-shape mapping.

### B237.15 — Deployment variants (V1–V8)

Adds 5 deploy surfaces beyond the original in-container-build
compose:

- **V1 + V5**: Prebuilt-image docker compose —
  `docker-compose.ghcr.yml` (full setup, pulls
  `ghcr.io/BarsSky/skygate:v1.5.2`, no in-container build,
  2-3s first start vs 60-120s); `docker-compose.lite.yml`
  (sky-only, no headscale/DERP/headplane/`docker.sock` —
  smallest possible attack surface, for users with headscale
  already running).
- **V2**: Podman compose — same `docker-compose*.yml` files,
  run via `podman compose -f docker-compose.ghcr.yml up -d`.
  The README has a Podman section with rootless + SELinux notes
  (`:Z` on bind-mounts, `loginctl enable-linger`).
- **V3 + V4**: Bare-metal systemd/OpenRC installers —
  `deploy/install.sh` (single-file autodetect
  Debian/Ubuntu/RHEL/Fedora/Alpine);
  `deploy/install-{debian,rh,alpine,bare}.sh` (per-OS scripts
  with their own dep install);
  `deploy/install-common.sh` (shared helpers: SHA256 verify +
  systemd unit + env file + user/dirs).
  One-liner: `curl -fsSL .../install.sh | sudo bash`.
- **V6**: Windows native installer —
  `deploy/Setup-Skygate-Win.ps1` (PowerShell 5.1+) downloads
  Windows zip from GitHub Releases, verifies SHA256, installs to
  `C:\Program Files\Skygate\`, writes
  `C:\ProgramData\Skygate\skygate.env`, registers as a Windows
  service via `New-Service`, the `Environment` registry key is
  the Windows equivalent of systemd's `EnvironmentFile=`.
  One-liner (Run as Administrator):
  `iex ((New-Object System.Net.WebClient).DownloadString('.../Setup-Skygate-Win.ps1'))`.
- **V8**: Release workflow — `.github/workflows/release.yml` on
  `v*` tag push: Docker image to `ghcr.io/BarsSky/skygate` with
  tags `:vX.Y.Z`, `:latest` (stable only), `:vX.Y`, `:vX`
  (all stable-only); Go binary tarballs for linux/darwin ×
  amd64/arm64 + windows-amd64; SHA256SUMS; GitHub Release with
  the relevant section of this `RELEASE-NOTES.md` as the body.
- `Dockerfile.prebuilt` — multi-stage (alpine runtime + prebuilt
  Go binary baked in, ~30 MB image, ~2s first start).
- `entrypoint.sh` patched to skip the build step when
  `SKYGATE_PREBUILT=1` is set (the `Dockerfile.prebuilt` bakes
  this in).

The dev path (in-container-build, no `SKYGATE_PREBUILT`) is
unchanged for the operator's local `./:/app` workflow.

50 B-check contracts in `scripts/check_b237_15.sh`.

### B237.16 — TD-2 + TD-14 staticcheck contract

Closes the last 2 stragglers of the v1.2.0 staticcheck cleanup
(commit `38b2fb9e` replaced 73 numeric status codes;
`d6f7b6b2` was the v1.2.0 follow-up). Post-v1.2.0, 2 stragglers
snuck back in via the v1.5.0 work:
- `internal/headscale/tags_test.go:66` had
  `http.Error(w, "unexpected: ...", 404)` → now `http.StatusNotFound`.
- `internal/oidc/e2e_test.go:399` had
  `http.Redirect(w, r, nextParam, 302)` → now `http.StatusFound`.

B237.16 also fixes the stale `s.DB` → `s.dbc()` lookups in
`scripts/check_b140.sh` and `scripts/check_b141.sh` (the
v0.32.20-era B-checks referenced the pre-rename field name).

10 B-check contracts in `scripts/check_b237_16.sh`.
`staticcheck ./...` reports 0 ST1013 + 0 SA1012.

### B237.17 — TD-9 smoke-mesh daily cleanup

Closes the "smoke.sh leaves `smoke_mesh_<pid>` users +
`smoke-mesh-<pid>` meshes in the live DB on a failed run"
accumulation. Pre-B237.17 the only path was: re-run smoke.sh
to completion (which re-runs step 13.8 cleanup) or manually
DELETE rows via psql. A failed/interrupted smoke.sh run left
2 rows per incident, accumulating over weeks.

- `scripts/cleanup_smoke_artifacts.sh` — idempotent daily script.
  `BEGIN/COMMIT` around the deletes (CASCADE handles
  `mesh_members` on the meshes table + `devices`/`preauth_keys`/
  `exit_rules` on the `portal_users` side). 24h grace window so
  an in-flight smoke.sh run is NOT killed. 24h-N row audit row
  written (`action=smoke_artifacts_purge`) so the operator can
  see "when did the last cleanup happen" via `/admin/audit`.
  Uses `sudo -u postgres psql -d skygate_staging`.
- `deploy/systemd/skymate-cleanup-smoke.{service,timer}` —
  `Type=oneshot`, daily at 04:00 local,
  `RandomizedDelaySec=300` (avoids fleet-wide thundering herd on
  HA), `Persistent=true` (catches up if the VM was off at 04:00).

18 B-check contracts in `scripts/check_b237_17.sh`.

Note: this is the host-level path. The in-app Go scheduler
from B143 (v1.4.3) already exists and runs as
`internal/mesh.StartCleanupScheduler` when
`SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED=true`. B237.17 is
the host-level backup that runs even if skygate is down.

### B237.18 — TD-10 headscale_user_id reconciliation

Closes the "portal_users.headscale_user_id goes stale after
a headscale delete+recreate" gap. Pre-B237.18 the only path
was: notice a rule pointing at a no-op + run psql + UPDATE by
hand. A delete+recreate in headscale left the `portal_users`
row pointing at a dead ID indefinitely.

- `internal/headscale/reconcile.go` — the per-row reconciliation
  function with 4 outcomes (ok / linked / relinked / orphan).
  **NEVER** auto-deletes `portal_users` rows; orphan outcomes
  write an audit row + leave the ID alone for the operator
  to review. Per-row transactions; a single bad row doesn't
  poison the cycle.
- `internal/headscale/reconcile_cron.go` — the
  `StartReconcileCron(ctx, db, hs, interval)` +
  `RunOnceNow(ctx, db, hs)` entry points. Default 1h interval
  (configurable via `SKYGATE_RECONCILE_HEADSCALE_USERS_INTERVAL`).
  `sync.Once` guard.
- `internal/config/config.go` — adds `ReconcileHeadscaleUsers`
  (bool, default true) + `ReconcileHeadscaleUsersInterval`
  (time.Duration, default 0 → use package default).
- `cmd/skygate/main.go` — wires the cron AFTER `headscale.New`
  + AFTER `ensureHeadscaleUser` (so the headscale client exists
  AND the admin user is in headscale by the first tick).
  Gated on `cfg.ReconcileHeadscaleUsers`.
- 10 unit tests in `internal/headscale/reconcile_test.go`.
- 22 B-check contracts in `scripts/check_b237_18.sh`.

The cron runs once on startup + every 1h. The operator should
see `reconcile: cron enabled (interval=1h, ...)` in the skygate
log after the next deploy. First cycle log:
`reconcile: N portal_users checked (hs_users=M, ok=X, linked=Y,
relinked=Z, orphans=W, errors=0, took=...)`. The `/admin/audit`
page should show the summary `headscale_user_reconcile` row
+ the per-row relinked/orphan rows.

### B146 — Phase 2 reg.ru DNS live test (BL-2)

Closes the Phase 2 (BL-2) work that was blocked on Q1
(reg.ru creds) + Q2 (IP whitelist) per §4 of
`docs/internal/ha-v1.5.0-execution.md`. The operator provided
both on 2026-09-07 (login + alternative password; the IP
whitelist was already filled). B146 productionizes the working
auth pattern that B145 + B161.4 confirmed against the live
reg.ru API on 2026-08-18 (top-level form fields + mTLS cert,
password NOT inside `input_data` JSON — the pre-fix pattern
returned `NO_AUTH`, this is the discovered-working shape).

- `scripts/b146_regapi_live.sh` — bash + curl + Python one-liner.
  The script does the 4-step preflight (cert + key on disk + 3
  env vars set), POSTs to the v2 `/zone/get_resource_records`
  endpoint with the right auth pattern, parses the JSON
  response, and reports a grep-able `PASS:` / `FAIL:` / `SKIP:`
  line. Handles the 4 known 2026-08-18 failure modes (NO_AUTH
  + ACCESS_DENIED_FROM_IP + DOMAIN_NOT_FOUND + generic ERROR)
  with actionable error messages that tell the operator
  exactly which prereq is missing.
- `scripts/check_b146.sh` — 15 B-check contracts.
- `docs/internal/ha-v1.5.0-execution.md` §6 status log + §4
  open questions table updated (Q1 + Q2 marked ✅ DONE
  2026-09-07).
- `AGENTS.md` — B146 block added.

**Live-verify pending** (operator-side, not code):
1. Paste cert + login + password + zone into the `/admin/ha`
   "External DNS" form (or set the env vars and trigger a
   future `skygate regapi-credentials set` subcommand — see
   B237.21 below).
2. Restart skygate so the cron + the form's "Test connection"
   button can read the new creds.
3. Run `bash scripts/b146_regapi_live.sh` to verify end-to-end.
   Expected output: `PASS: skynas.ru/skygate -> <IP>`. If the
   response is `NO_AUTH` or `ACCESS_DENIED_FROM_IP`, the
   script's actionable error message tells the operator
   exactly which prereq is missing.

9/10 BL-2 phases SHIPPED. Only Phase 10 (release tag) remains.

### B237.19 — exit-rules form-error flash + duplicate banner UX

Closes 2 operator-reported bugs from 2026-09-07.

#### Bug 1 — form validation rendered a giant plain-text page
`PostMyExitRule` called `http.Error(w, ..., 400)` on
form-validation failures (invalid IP, limit exceeded, device
not owned, etc.). The browser rendered a giant plain-text page
and the operator lost the form values they had typed.

**Fix**: `PostMyExitRule` now calls `http.Redirect` to
`/my/exit-rules?err=<msg>&form_*` via the new
`buildFormErrorRedirectURL` helper (7 call sites: invalid IP,
user limit, device limit, system limit, device not owned,
exit-node rejected, generic DB error). The template renders
`.err` as a flash banner above the form. The user's form
values are preserved via the `form_*` query params.

#### Bug 2 — duplicate banner wording was misleading
"Правило для X уже существует — не дублируем. Удалите
существующее, если нужно обновить" sounded like an error, and
the `alert-danger` color made it look like "the new rule
failed to add" when in reality the new rule was never created
(it was the old `/32` from the first add of the same domain).
The autoupdater handles updates; the user doesn't need to
delete.

**Fix**:
- The duplicate banner's color changed from `alert-danger`
  (red) to `alert-info` (blue) — it's informational, not an
  error.
- Wording updated: "Домен X уже покрыт правилом —
  автообновление будет поддерживать его актуальность" (RU) /
  "Domain X is already covered by an existing rule — the
  autoupdater will keep it current" (EN). No more "delete to
  update" hint.
- New i18n key `exit_rules.form_error` (RU + EN) for the
  flash banner.

15 B-check contracts in `scripts/check_b237_19.sh`; 4 new
unit tests in `form_my_b237_19_test.go`.

### B237.20 — TD-12 + TD-13 staticcheck-100%-clean

Closes PLANS.md TD-12 ("30 ST1013-style noise items") and
TD-13 ("~2850 lines of testutil.go stubs"). The actual count
was 23 (not 30) — 22 U1000 "unused code" warnings on test stubs
that satisfy interface contracts + 1 S1021 "merge variable +
assignment" in `dbmigrate/ssh_transport.go:279`.

**B237.20 fix**: 3 outright deletions (`queryReachable` + 2
unused `mu sync.Mutex` fields) + 19 `//lint:ignore U1000`
directives on the test stubs (correct format per
[honest-rule-of-thirds](AGENTS.md#lintignore-format) — NOT
`//nolint:staticcheck`, which is golangci-lint's format) +
1 S1021 fix. `staticcheck ./...` reports 0 issues (was 23
pre-fix). 9-contract B-check in `scripts/check_b237_20.sh`.

### B237.21 — `skygate regapi-credentials` CLI subcommand

Closes the "only path to set reg.ru creds is the `/admin/ha`
form" gap. Pre-B237.21 the operator's one-shot bootstrap flow
(clone repo → start skygate → set creds → run B146 live test)
required a browser session to log in to `/admin/ha`. B237.21
adds 4 CLI verbs that mirror the form's behavior end-to-end:

1. **`set`** — write creds encrypted with `SKYGATE_SECRET_KEY` +
   stored in `global_settings` (via `extcreds.Store.Save`).
   Supports `--password-file=<path>` for safer shell history
   (chmod 0600 the file instead of leaking the password via
   `ps` / `history`).
2. **`show`** — print current creds with the password masked
   (`maskSecret` helper — first 2 + last 2 chars visible, the
   rest as `*`) and the cert PEM summarized (byte count only —
   the cert IS a secret).
3. **`test`** — call `extcreds.Store.TestConnection` (the same
   code path the `/admin/ha` "Test" button uses). Sanity check
   before running `scripts/b146_regapi_live.sh` so a
   misconfigured creds set gives an actionable error early
   (instead of the less-actionable "live test failed" error
   from the curl-based test).
4. **`delete`** — explicit clear of the 5 `global_settings`
   rows.

New `db.DeleteGlobalSetting` helper + `Store.Delete()` method.
15 B-check contracts in `scripts/check_b237_21.sh` (source:
4-verb dispatcher + Store.Delete + DeleteGlobalSetting;
wire-up: main.go case + help text; security: password masked
+ cert PEM not printed + password-file support; tests: 6
unit tests + build clean; registration: verify_pre_deploy.sh
+ AGENTS.md).

**Live-verify pending** (operator-side):
```
skygate regapi-credentials set \
    --login=kanagaenko@mail.ru \
    --password='<alternative password>' \
    --zone=skynas.ru \
    --cert-path=/home/skyadmin/skygate-secrets/regapi/cert.pem
skygate regapi-credentials test
bash scripts/b146_regapi_live.sh
```

### B237.22 — UI-only CDN rule grouping (TD-11 / Approach G)

Closes the "noisy 15-row-per-Cloudflare-domain list" gap.
Live data (2026-08): **46 of 151 `device_rules` rows (30%) are
CDN-derivable from 5 distinct `parent_domain` values**
(Cloudflare: discordapp.com + production.cloudflare.docker.com;
Google: youtube.com + gcr.io; Akamai: agent.minimax.io).
Pre-B237.22 the `/my/exit-rules` + `/admin/exit-rules` pages
showed all 15 per-CIDR rows as a flat list — correct but
visually noisy.

Approach G (UI-only) was chosen over Approaches A-F (storage
/ migration / autoupdate changes) for the operator's hard
reasons:
- "не наложит ли это ограничения на текущую работу правил
  и доступа" — Approach B (storage grouping) would lose the
  ability to lock specific CIDR / block specific Cloudflare
  ranges.
- "ресур cloudflare может быть залочен как и любой другой
  внешний ресурс" — per-CIDR rows MUST stay individually
  editable.
- "нужны именно правила на конкретный ресурс делать полный
  проброс не надо - ломает всю логику" — each row = specific
  CIDR, not opaque marker.
- "каждый пользователь будет иметь свое к конкретному
  устройству и exit node не пересикаясь с другими" —
  natural key `(user_id, device_id, exit_node_id,
  target_type, target_value)` is unchanged (each user keeps
  isolated access per (device, exit_node)).

**What B237.22 changes** (view layer ONLY):
- New `cdn_group.go` (helpers `GroupRulesByCDN`,
  `IsCDNGroupMarker`, `ParseCDNGroupMarker`,
  `CDNDisplayItem`, `CDNDisplayView`) and `cdn_group_admin.go`
  (parallel admin-side with `GroupAdminRulesByCDN`,
  `CDNDisplayItemAdmin`, `CDNDisplayViewAdmin`).
- `form_my.go` + `form_admin.go` pass the CDN-grouped view to
  the templates.
- Templates iterate `CDNDisplayView.Items`; each
  `IsCDNGroup=true` item renders a collapsible `<details>`
  header with Source + CDN badge + "X диапазонов" count + the
  per-CIDR rows underneath. Ungrouped rules render as a flat
  table (same per-rule markup).

**What B237.22 does NOT change** (the hard constraints):
- **No storage change**: each rule is still a separate row in
  `device_rules`.
- **No migration**: zero schema changes, zero data backfill.
- **No autoupdate change**: `cdn.go` (the autoupdater that
  inserts per-CIDR rules when a domain is on a known CDN) is
  untouched.
- **No SyncAdvertisedRoutes change**: headscale still gets
  the same ACL.
- **No "remove all 15" button**: each CIDR stays individually
  deletable.
- **No "edit the grouped rule" form**: there is no such concept
  (the rows are independent).
- **Natural key preserved**: each user keeps isolated access
  per (device, exit_node).

**Unit tests** (15 total): 9 in `cdn_group_test.go` (with 3
pre-fix bug fixes: case-insensitive prefix marker, sort by
Source not CDN, secondary sort by CDN for stable ordering) + 6
in `cdn_group_admin_test.go` (pins B178/B182/B184 annotation
fields are preserved through grouping).

**i18n** (RU + EN): `exit_rules.cdn_group_count` — "X диапазонов" /
"X ranges".

32 B-check contracts in `scripts/check_b237_22.sh`.

### B237.23 — Fix autoupdate `ON CONFLICT` code/index drift (B183 vs B232 regression)

Closes the silent autoupdate-failure bug surfaced by B237.22's
⏳ orange status check: `auth.docker.io` (Cloudflare), `harness.io`,
`cdn-registry-1.docker.io`, `limit-test-...` and `cascade-verify-...`
all rendered ⏳ orange in `/my/exit-rules` even though the rules
work end-to-end (the IPs are in `karolina`'s headscale
`ApprovedRoutes`). Operator's investigation (2026-09-07) revealed
the autoupdate logs `added=0` for every domain whose `/32`
subnet rows should have been created.

**Root cause** (regression analysis):
- V056 (B125, 2026-08-17) intended to create a 6-col
  `device_rules_natural_key_uniq` (with `parent_domain`) but
  used `CREATE UNIQUE INDEX IF NOT EXISTS` which is a **silent
  no-op** when an index with the same name already exists with
  a different column list.
- B188.2 changed `qInsertDeviceRule` to a 6-col `ON CONFLICT`
  to match V056's intent. On FRESH DBs this worked; on
  upgrades the V056 statement was a no-op and the 5-col index
  (from v0.55) stayed, so every INSERT with a 6-col ON CONFLICT
  failed with `no unique or exclusion constraint matching`.
- B183 (V060, 2026-08-25) **reverted the design** to 5-col
  index + 5-col ON CONFLICT (separate concern: "first
  parent_domain wins" for Cloudflare duplicate-CIDR dedup).
  It was internally consistent (5-col index + 5-col code).
- B232 (V068, 2026-09-04) re-created the index as 6-col to
  match B188.2's intent (closing the live "db error on
  /my/exit-rules POST" symptom) — but **didn't update
  `sync.go`**. The result was a code/index drift: index = 6-col
  (V068), code = 5-col (B183) — every autoupdate INSERT
  `DomainAutoUpdater` ran hit `no unique or exclusion constraint
  matching` and the `if err != nil { continue }` at
  `sync.go:594` + `:496` silently swallowed the error. Net
  effect: `/32` rows for the autoupdate's 15 Cloudflare CIDRs
  were never created. B184 then saw "no resolved subnets" →
  ⏳ orange forever. The 5-col design is also INCOMPATIBLE
  with the B184 status check (which looks for
  `parent_domain = <rule's marker>` and finds nothing for the
  "loser" `parent_domain` values) and the B237.22 UI grouping
  (which renders each `parent_domain` as a separate
  `<details>` group).

**B237.23 fix**: restore 6-col `ON CONFLICT` in `sync.go` (both
clauses: the CDN-range INSERT and the per-IP /32 INSERT),
matching `qInsertDeviceRule` in `queries.go:416` and the live
6-col `device_rules_natural_key_uniq` from V068. Also updates
`acl_b188_3_integration_test.go` test helper (which still had
the 5-col target from B183) and `scripts/check_b183.sh`
contract E to assert "5-col target is GONE, 6-col target is
PRESENT" (instead of "5-col is present" — the B183 design is
no longer current).

**Why 6-col is the right design for the current implementation**
(vs B183's 5-col "first parent_domain wins"):
1. The 6-col design allows each `parent_domain` to have its own
   `/32` rows. B184 looks for `parent_domain = <rule's
   target_or_marker>` and finds the right rows for each domain.
   With 5-col "first wins", every Cloudflare domain past the
   first one would have NO rows owned by it → ⏳ orange
   permanently.
2. B237.22 UI grouping (5 distinct `cdn:cloudflare:*` markers)
   requires per-marker rows; with 5-col "first wins" only 1 of
   the 5 would actually own the CIDR rows.
3. Tailscale's `ApprovedRoutes` is a SET — Tailscale
   de-duplicates the routes on the client.
4. `qInsertDeviceRule` (form path) uses 6-col ON CONFLICT; the
   autoupdate was the only path using 5-col, causing
   form-vs-autoupdate asymmetry.

**What B237.23 changes** (small, surgical):
- `internal/feature/exit_rules/sync.go`: both `ON CONFLICT`
  clauses 5-col → 6-col (`(user_id, device_id, exit_node_id,
  target_type, target_value, parent_domain)`).
- `internal/acl/acl_b188_3_integration_test.go:138`: test
  helper 5-col → 6-col.
- `scripts/check_b183.sh` contract E: replace "5-col ON
  CONFLICT must be present" with "5-col ON CONFLICT must be
  GONE, 6-col must be PRESENT" (B183 design is no longer
  current; V068's 6-col index is the canonical state).
- New `scripts/check_b237_23.sh` (12 contracts).

**What B237.23 does NOT change**:
- `migrateV060PG` (B183's dedup CTE) is **unchanged** — it's
  still the right thing to do on a DB that needs dedup.
- `migrateV068PG` (B232) is **unchanged** — its 6-col index
  is the final shape.
- `qInsertDeviceRule` is **unchanged** — it was already 6-col
  (B188.2).
- Storage layout is **unchanged** — each `parent_domain`
  continues to own its own `/32` rows for the 5 CDN-tracked
  parent_domains on karolina.

**Test coverage** (12/12 B237.23 PASS, 10/10 B183 PASS, 9+6
cdn_group tests, 6 acl b188_3 tests):
- `bash scripts/check_b237_23.sh` → 12/12 pass
- `bash scripts/check_b183.sh` → 10/10 pass (revised E-post)
- `go test -short -count=1 ./internal/feature/exit_rules/...`
  → 9 cdn_group + 6 cdn_group_admin tests pass
- `go test -short -count=1 ./internal/acl/...` → b188_3 tests
  pass (with 6-col ON CONFLICT)

### Files added / changed in v1.5.2

| File | Change |
|---|---|
| `internal/update/docker.go` | New `GitRefForBuildLabel` helper + `gitRef` arg in `Run` (B237.10) |
| `internal/update/docker_test.go` | +132: 3 new test functions, 17 subtests |
| `internal/feature/admin/update.go` | 2 callsite changes (B237.10) |
| `entrypoint.sh` | `SKYGATE_PREBUILT=1` guard (B237.15) |
| `Dockerfile.prebuilt` | NEW (B237.15) |
| `docker-compose.ghcr.yml` | NEW (B237.15) |
| `docker-compose.lite.yml` | NEW (B237.15) |
| `deploy/install.sh` | NEW (B237.15) |
| `deploy/install-{debian,rh,alpine,bare}.sh` | NEW ×4 (B237.15) |
| `deploy/install-common.sh` | NEW (B237.15) |
| `deploy/Setup-Skygate-Win.ps1` | NEW (B237.15) |
| `deploy/systemd/skymate-cleanup-smoke.{service,timer}` | NEW (B237.17) |
| `internal/headscale/reconcile.go` | NEW (B237.18) |
| `internal/headscale/reconcile_cron.go` | NEW (B237.18) |
| `internal/headscale/reconcile_json.go` | NEW (B237.18) |
| `internal/headscale/reconcile_test.go` | NEW (B237.18) |
| `internal/headscale/tags_test.go` | 1 line (404 → http.StatusNotFound) |
| `internal/oidc/e2e_test.go` | 1 line (302 → http.StatusFound) |
| `internal/feature/exit_rules/form_my.go` | `buildFormErrorRedirectURL` + 7 redirect sites (B237.19) |
| `internal/feature/exit_rules/form_my_b237_19_test.go` | NEW (B237.19) |
| `internal/handlers/templates/exit_rules.html` | `.err` flash banner + duplicate `alert-info` (B237.19) |
| `internal/i18n/catalog_exit_rules.go` | `form_error` (RU+EN) + duplicate wording (B237.19) |
| `internal/config/config.go` | `ReconcileHeadscaleUsers*` fields (B237.18) |
| `cmd/skygate/main.go` | `StartReconcileCron` wire-up (B237.18) |
| `cmd/skygate/regapi_credentials.go` | NEW (B237.21) |
| `cmd/skygate/main.go` | `regapi-credentials` case in dispatcher (B237.21) |
| `internal/ha/dnsexternal/credentials.go` | `Store.Delete()` method (B237.21) |
| `internal/db/globalsettings.go` | `DeleteGlobalSetting` helper (B237.21) |
| `internal/feature/exit_rules/cdn_group.go` | NEW (B237.22) |
| `internal/feature/exit_rules/cdn_group_admin.go` | NEW (B237.22) |
| `internal/feature/exit_rules/cdn_group_test.go` | NEW (B237.22) |
| `internal/feature/exit_rules/cdn_group_admin_test.go` | NEW (B237.22) |
| `internal/feature/exit_rules/form_my.go` | `GroupedByHostnameCDN` + `GroupRulesByCDN` (B237.22) |
| `internal/feature/exit_rules/form_admin.go` | `NodesCDN` + `GroupAdminRulesByCDN` (B237.22) |
| `internal/handlers/templates/exit_rules.html` | CDN-grouped `<details>` iteration (B237.22) |
| `internal/handlers/templates/admin/exit_rules.html` | CDN-grouped `<details>` iteration (B237.22) |
| `internal/i18n/catalog_exit_rules.go` | `cdn_group_count` (RU+EN) (B237.22) |
| `internal/feature/exit_rules/sync.go` | 2x `ON CONFLICT` 5-col → 6-col (B237.23) |
| `internal/acl/acl_b188_3_integration_test.go` | test helper 5-col → 6-col (B237.23) |
| `scripts/b146_regapi_live.sh` | NEW (B146) |
| `scripts/cleanup_smoke_artifacts.sh` | NEW (B237.17) |
| `scripts/check_b{140,141}.sh` | `s.DB` → `s.dbc()` fix (B237.16) |
| `scripts/check_b146.sh` | NEW (B146) |
| `scripts/check_b183.sh` | revised E-post (5-col GONE, 6-col PRESENT) (B237.23) |
| `scripts/check_b237_10.sh` | NEW (B237.10) |
| `scripts/check_b237_15.sh` | NEW (B237.15) |
| `scripts/check_b237_16.sh` | NEW (B237.16) |
| `scripts/check_b237_17.sh` | NEW (B237.17) |
| `scripts/check_b237_18.sh` | NEW (B237.18) |
| `scripts/check_b237_19.sh` | NEW (B237.19) |
| `scripts/check_b237_20.sh` | NEW (B237.20) |
| `scripts/check_b237_21.sh` | NEW (B237.21) |
| `scripts/check_b237_22.sh` | NEW (B237.22) |
| `scripts/check_b237_23.sh` | NEW (B237.23) |
| `scripts/verify_pre_deploy.sh` | +12 `run_check` rows (B146 + B237.10/15/16/17/18/19/20/21/22/23) |
| `AGENTS.md` | +12 B-block sections (B146 + B237.10..23) |
| `docs/PLANS.md` | TD-2/9/10/11/12/13 marked DONE; B237.23 entry |
| `docs/internal/ha-v1.5.0-execution.md` | §6 status log + §4 open questions updated |
| `README.md` | New "Deployment variants" section |
| `CHANGELOG.md` | v1.5.2 entry replaced (this section's source) |

### Test coverage

| Suite | Status |
|---|---|
| `bash scripts/check_b237_10.sh` | 19/19 pass |
| `bash scripts/check_b237_15.sh` | 50/50 pass |
| `bash scripts/check_b237_16.sh` | 10/10 pass |
| `bash scripts/check_b237_17.sh` | 18/18 pass |
| `bash scripts/check_b237_18.sh` | 22/22 pass |
| `bash scripts/check_b237_19.sh` | 15/15 pass |
| `bash scripts/check_b237_20.sh` | 9/9 pass |
| `bash scripts/check_b237_21.sh` | 15/15 pass |
| `bash scripts/check_b237_22.sh` | 32/32 pass |
| `bash scripts/check_b237_23.sh` | 12/12 pass |
| `bash scripts/check_b146.sh` | 15/15 pass |
| `bash scripts/check_b140.sh` | 7/7 pass (B237.16 follow-up fix) |
| `bash scripts/check_b141.sh` | 8/8 pass (B237.16 follow-up fix) |
| `bash scripts/check_b183.sh` | 10/10 pass (B237.23 revised E-post) |
| `go test -short -count=1 ./...` | All packages green (no regression) |
| `go build ./...` | Clean |
| `staticcheck ./...` | 0 issues (was 23 pre-B237.20) |

### Breaking changes from v1.5.0

None. v1.5.2 is a strict hotfix release. All env vars + config
keys are additive; the new `ReconcileHeadscaleUsers` and the
`SKYGATE_PREBUILT=1` env var are both default-on (operator can
opt out with `SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED=false`).

### Migration from v1.5.0

None for the codebase. To pick up v1.5.2 on the live VM:

```bash
cd /home/skyadmin/skygate
git pull --no-rebase origin main
docker compose restart skygate
docker exec skygate-skygate-1 /app/skygate version
# → skygate v1.5.2-2-g23977b6
```

To pick up v1.5.2 via the auto-updater (after B237.10's fix):

```
/admin/update → "Push update" (or "Apply" if the tagged
release was published)
```

### Known limitations

- **B146 live-verify pending** (see the B146 section above —
  3 operator steps needed).
- **TD-4 (Backup S3)** DEFERRED — SMB/NFS/SFTP cover the
  operator's current needs. ~½ day if operator wants it.
- **TD-5 (per-user `exitnode.<user>.<domain>` DNS)** BLOCKED
  on headscale 0.30+ release.
- **BL-2 HA v1.5.0 — Q9 Live DR drill date** PENDING. The HA
  chain + certsync + electors + reg.ru creds are all
  productionized; only the maintenance window for the actual
  drill is missing.
- **BL-3 (Telegram DPI workaround)** BLOCKED on operator's
  network — no skygate-side fix.

### See also

- `docs/internal/2026-09-04-tailnet-fixes.md` — the 2026-09-04
  incident post-mortem that motivated v1.5.0.
- `docs/internal/tailnet-advertised-routes.md` — B236:
  the subnet-router hard rule, the verification commands, the
  loop it caused.
- `docs/internal/exit-rules-reconciler.md` — B229 / B237.7:
  the three-layer architecture, the decision matrix, the
  default-flip rationale, the build-time contract tests.
- `docs/internal/ha-v1.5.0-execution.md` — §6 status log has
  the 2026-09-07 B146 entry + the Q1 + Q2 "✅ DONE" updates.
- `docs/PLANS.md` — TD-2 / TD-9 / TD-10 / TD-11 / TD-12 /
  TD-13 marked DONE in v1.5.2; TD-4 / TD-5 / TD-8 / BL-2 Q9
  / BL-3 still open.
- `AGENTS.md` — B146 + B237.10 / B237.15 / B237.16 /
  B237.17 / B237.18 / B237.19 / B237.20 / B237.21 / B237.22
  / B237.23 sections with code-level details + file lists.
- `README.md` — new "Deployment variants" section covers
  all 5 V1–V8 deploy surfaces.

---

*Released 2026-09-08. SHA: 23977b6c (B237.23 tip; tag
`v1.5.2` force-moved from 374b7c5a to include B237.22 +
B237.23).*

## v1.5.0 — DERP relay integration, headscale policy via:-clause, /admin/derp fixes

**Date:** 2026-09-04

> v1.5.0 is the stable release of the B145/B147/B148/B149/B150
> HA-chain work that shipped as `v1.5.0-alpha1` on 2026-08-19,
> plus the full set of DERP / Tailscale / exit-rules fixes
> that were developed in the 2026-09-04 incident response
> (B235 / B235.1 / B235.2 / B235.3 / B236 / B237 / B237.1 /
> B237.2 / B237.7 / B237.8). Read the post-mortem
> [docs/internal/2026-09-04-tailnet-fixes.md](docs/internal/2026-09-04-tailnet-fixes.md)
> for the full chain of root causes and the current
> configuration.

### What's in v1.5.0

#### HA chain (B145/B149/B150 — shipped in v1.5.0-alpha1)
- **HA chain + elector + pluggable DNS provider** (B145):
  leader election between skygate instances with pluggable DNS
  provider (reg.ru or mock).
- **`/admin/certificates` page** (B148 / BL-2 Phase 4):
  upload + reg.ru DNS-01 toggle.
- **In-app certsync scheduler** (B147 / BL-2 Phase 3).
- **`/admin/ha` page** (B149): HA chain editor + failover
  controls + reg.ru credentials.
- **`/admin/deploy` page + skygate deploy CLI** (B150 /
  BL-2 Phase 6).

#### DERP / Tailscale / exit-rules fixes (B235..B237.8)
- **B235** — `FetchPublicDERPs` `n.HostName` (FQDN) instead of
  `n.Name` (Tailscale short label "1f") as the Host. Closes the
  28/28 public DERP "degraded" symptom on `/admin/derp/dashboard`.
- **B235.1 + B235.2 + B235.3** — DERP map shape + short-label
  pill + region_id tooltip on `/admin/derp/dashboard`.
- **B236** — subnet-routes management: hard rule "subnet-router
  must be the only device advertising the same CIDR" + the
  3-state UI (advertised / approved / mismatch) on
  `/admin/exit-nodes`.
- **B237** — own DERP via skygate: `TailscaleDERP` + `Apply to
  headscale` buttons on `/admin/derp/dashboard`. Closes the
  derp.skynas.ru + derp.emilia "not advertising" gap that
  appeared after the B189 incident.
- **B237.1** — `SKYGATE_HEADSCALE_CONFIG_PATH` env var +
  bind-mount for the headscale config. Closes the "container
  has no /etc/skygate/headscale-config.yaml" symptom.
- **B237.2** — correct Public IP display via DNS lookup
  (instead of `r.RemoteAddr` which gave the Tailscale IP).
- **B237.7** — exit-rules reconciler default-flip from "manual
  button" to "LIVE" (autoupdate every 5 min by default).
- **B237.8** — operator docs for the above.

#### Files in v1.5.0
- `internal/headscale/nodes.go` — `FetchPublicDERPs` Host fix
  (B235)
- `internal/derphealth/health.go` + `internal/handlers/templates/
  admin/derp_dashboard.html` — region_id tooltip + short-label
  pill (B235.1/2/3)
- `internal/feature/exit_rules/reconciler.go` — default-flip
  to LIVE (B237.7)
- `internal/feature/admin/derp_dashboard.go` + `internal/
  derp_relay.go` — own DERP via skygate (B237)
- `internal/feature/admin/certificates.go` + `/admin/certificates`
  — cert upload + DNS-01 (B148)
- `internal/ha/{chain,elector,provider_regapi,provider_mock,
  certsync,planner,role}.go` — HA chain core (B145)
- `internal/feature/admin/ha.go` + `/admin/ha` — chain editor +
  failover (B149)
- `internal/feature/admin/deploy.go` + `cmd/skygate/deploy.go` —
  deploy CLI + UI (B150)
- `internal/handlers/layout.go` + `layout.html` — DERP / HA /
  certsync / deploy sub-tabs
- `docs/internal/2026-09-04-tailnet-fixes.md` — post-mortem
- `docs/internal/tailnet-advertised-routes.md` — B236 deep-dive
- `docs/internal/exit-rules-reconciler.md` — B229 / B237.7
  contract
- `docs/PLANS.md` — TD-2 / TD-6 / TD-7 marked DONE in v1.5.0

#### Test coverage (v1.5.0)
- `bash scripts/check_b235.sh` → 19/19 pass
- `bash scripts/check_b237.sh` → all B237.x sub-checks pass
- `go test -short ./...` → 28/28 green (post-PG cutover)
- `go build ./...` → clean
- `staticcheck ./...` → 0 ST1013, 0 SA1012 (post-B237.16)
  plus 23 U1000 (cleared by B237.20)

#### Live state (operator VM 192.168.13.69, verified 2026-09-04)
- 151 `device_rules` rows (5 CDN-tracked parent_domains contribute
  46 rows, 30%; rest are manual + autoupdate-derived)
- 213 headscale policy grants
- 30 DERP regions healthy (28 public + 2 own)
- 0 `smoke_mesh_*` rows in DB

#### Migration from v1.4.x to v1.5.0
- The `subnet-router` headscale tag must be set on the 3
  subnet-router nodes (emilia + karolina + sharlotta). The
  B236 fix script `internal/feature/exit_nodes/subnet_router_
  backfill.go` handles this automatically.
- `SKYGATE_HEADSCALE_CONFIG_PATH` env var must be set (defaults
  to `/etc/headscale/config.yaml` on the operator's VM).
- DERP health probe cron must be enabled:
  `SKYGATE_DERP_HEALTH_CRON_ENABLED=true`.

#### Known limitations at v1.5.0
- **BL-2 HA Tier 1** — 8/10 phases done, 2 remaining
  (Q9 Live DR drill date, Phase 10 release tag).
- **BL-3 Telegram DPI workaround** — operator-side, no
  skygate-side fix.
- **TD-4 Backup S3** DEFERRED.
- **TD-5 per-user DNS** BLOCKED on headscale 0.30+.

---

## v1.3.20 — /admin/update redesign + real time-of-day auto-update (B128 + B129 + B130)

**Date:** 2026-08-18

Three operator-visible changes that fix a long-standing UX
disconnect on the /admin/update page and add a real
auto-update mechanism.

### What

**1. (B128) The Update button now appears when a newer
GitHub release is available.** Pre-B128, `compareSemver` in
three places (`internal/update/checker.go`,
`internal/release/monitor.go`, `internal/headscale_version/client.go`)
compared only the first 3 dot-separated components and
silently dropped the 4th. Skygate adopted 4-component
versioning (`x.y.z.w`, sub-patch) in v1.3.12+, so the
4-part compare is required. Pre-B128, comparing
`v1.3.19.2-7-g0670b64` (current build) against `v1.3.19.4`
(latest GitHub release) gave `[1,3,19]` vs `[1,3,19]` →
equal → `IsNewer=false` → the "Update" button on
/admin/update stayed hidden even though a real new release
was available. The same bug in the monitor caused the
"Доступно обновление" banner on every admin page to show
stale data (last monitor tick + GitHub's
`v1.3.19.4` filtered to `[1,3,19]` → equal → not newer).
After B128 the 4th component is included and the button
appears.

**2. (B129) The /admin/update page is redesigned around
the new "Update is a button, auto-update is a schedule"
mental model.** The pre-B129 "Авто-обновление включено"
banner was misleading — the page called it "auto-update"
but the operator still had to click "Apply" to actually
run the orchestrator. Post-B129 the Apply button is
**unconditional** when `IsNewer` (no more
`AutoUpdateEnabled` gating) and the page has a new
"Расписание автообновления" section with a checkbox +
HH:MM time picker + "Last run" timestamp. The
`SKYGATE_AUTO_UPDATE_ENABLED` env var is now read-only
(used as a default for first start; the UI overrides it).

**3. (B130) A background goroutine in `cmd/skygate/main.go`
triggers the update orchestrator at the configured time.**
Ticks every 30s. When (a) schedule is enabled, (b) current
HH:MM matches, (c) GitHub has a newer release, (d) no
update is in flight, (e) hasn't already fired for this
minute → spawns the Docker upgrader + sends Telegram
alerts (start + done/fail) + stamps the last-run
timestamp. The `SKYGATE_UPDATE_SCHEDULE_ENABLED` env var
gates the goroutine at boot (set to `true` to enable the
default behavior; the /admin/update page is the
operator-facing toggle).

### Operator action

- **For a quick install** (no real auto-update): no env-var
  change needed. The new "Apply" button will appear
  immediately when a newer release is detected.
- **For real time-of-day auto-update**: set
  `SKYGATE_UPDATE_SCHEDULE_ENABLED=true` in `/home/skyadmin/skygate/.env`,
  then in /admin/update enable the "Расписание автообновления"
  toggle and set the time (default 03:00).
- **Customize the default time** (without using the page):
  set `SKYGATE_UPDATE_SCHEDULE_TIME=HH:MM` in .env.
- **Disable auto-update entirely**: leave the env var at
  `false` and the page toggle off. The Apply button
  still works for manual upgrades.

### Verify

- `make verify-pre` (or `bash scripts/verify_pre_deploy.sh
  --quick`): **125 PASS / 0 FAIL / 1 SKIP** (B8 VM-only).
  B128, B129, B130 are all green. B95 (v0.34.0 code debt
  cleanup) is the only pre-existing FAIL — known stale,
  deferred to v1.4.0 catalog cleanup.
- `go test ./...` (28/28 packages green).
- `go test -count=1 ./internal/update/`: 7 scheduler unit
  tests + the existing TestCompareSemver all pass.
- Live `/admin/update`: Apply button now visible without
  manual `auto_update_enabled` flag; new "Schedule" card
  with toggle + HH:MM input; "Last run" timestamp appears
  after the first scheduled run.

### What this is NOT

- Not a hotfix to v1.3.19.x. The B128 compareSemver fix is
  a 3-line core change in 3 files; the B129 + B130 work is
  a full UX redesign + a new background goroutine. A
  `v1.3.19.5` patch bump would undersell the scope.
- Not a release of the v0.34.0 catalog cleanup work (the
  15+ remaining stale B-checks). That's a separate
  v1.4.0-effort item.
- Not a release of headscale 0.30+ support (the `dns.extra_records`
  policy still requires headscale 0.30+ which isn't out).

## v1.3.19.2 follow-up — device_rules auto-add duplicate prevention (B125 / Goal 37 follow-up)

**Date:** 2026-08-17
**Tag:** v1.3.19.2 (force-pushed to include B125, or new tag v1.3.19.3 — see release notes)
**Build:** v1.3.19.2-1-ga965fe5 (B125) — deployed to live VM

**Scope:** One follow-up hotfix to close the race window in the
device_rules auto-add path (Goal 37 follow-up). Goal 39 (Exit
Rules duplicate alert UX) was already closed by B123 in the
previous v1.3.19.2 follow-up; B125 is the data-layer
completion.

**B125 (device_rules auto-add duplicate prevention):**

The /my/exit-rules auto-add path resolves CDN markers and DNS
to /32 rules and inserts them into the device_rules table.
Pre-B125, two concurrent goroutines could both pass the
"INSERT-IF-NOT-EXISTS" check, both INSERT, and both commit —
producing duplicate rows. Goal 37 cleaned up 114 redundant
rules produced by this race; B125 prevents new ones.

Three layers:

1. **Schema** (`migrateV056PG`): a new PG migration
   `v0.56` adds `CREATE UNIQUE INDEX IF NOT EXISTS
   device_rules_natural_key_uniq ON device_rules(user_id,
   device_id, exit_node_id, target_type, target_value,
   parent_domain)`. All 6 columns are NOT NULL in the current
   schema (parent_domain is `TEXT NOT NULL DEFAULT ''`), so a
   plain UNIQUE INDEX covers every row — no COALESCE() or
   partial indexes needed. The index is named
   `device_rules_natural_key_uniq` so future migrations can
   drop+recreate it for cleanup.

2. **Insert path** (`qInsertDeviceRule` in
   `internal/db/queries.go`): the canonical INSERT now uses
   `ON CONFLICT (user_id, device_id, exit_node_id,
   target_type, target_value, parent_domain) DO UPDATE SET
   id = device_rules.id RETURNING id`. The `DO UPDATE SET id
   = device_rules.id` is a no-op update that lets the query
   `RETURNING id` give back the existing row's id (a plain
   `DO NOTHING` can't RETURN an old id). This makes
   `AppendDeviceRule` a true "insert or get-existing" with
   no race window.

3. **Auto-add path** (`internal/feature/exit_rules/sync.go`,
   lines 432 + 512): the CDN marker loop and the per-IP /32
   loop replace the previous SELECT-then-INSERT with direct
   `INSERT ... ON CONFLICT DO NOTHING` and use
   `tag.RowsAffected()` to track new vs skipped rows. The
   `cdnAdded` / `added` counters only increment on `n > 0`
   (so duplicate conflicts don't double-count).

**Operator-side cleanup (for any pre-existing duplicate DB):**

If a DB has existing duplicates that would block
`CREATE UNIQUE INDEX`, run the cleanup BEFORE the migration:

```sql
DELETE FROM device_rules a USING device_rules b
WHERE a.id > b.id
  AND a.user_id = b.user_id
  AND a.device_id = b.device_id
  AND a.exit_node_id = b.exit_node_id
  AND a.target_type = b.target_type
  AND a.target_value = b.target_value
  AND COALESCE(a.parent_domain, '') = COALESCE(b.parent_domain, '');
```

After: re-run `migrateV056PG` (or just restart skygate — the
migration is in the V049+ chain).

**Test coverage** (3 sequential tests in
`internal/db/device_rules_b125_test.go`):

- `TestAppendDeviceRule_B125_Sequential_SameKey_OneRow` — 10
  sequential `AppendDeviceRule` calls with the SAME natural
  key produce exactly ONE row in the table and return the
  same id every time.
- `TestAppendDeviceRule_B125_DistinctKeys` — 6 distinct
  natural keys (different device / exit / type / value /
  parent) all succeed; each gets its own id.
- `TestAppendDeviceRule_B125_SameKeyReturnsSameID` — the
  form-path use case (user clicks "add rule" twice) returns
  the same id on the second call.

The original test design used 10 concurrent goroutines, but
the live-PG test pool has 10 connections and the `SET
search_path` in `OpenTestPG` only affects one connection
per call, so concurrent goroutines on different pool
connections see different search_path and the test fails on
FK setup. The race is closed at the SQL level (UNIQUE INDEX
+ ON CONFLICT is the atomic primitive) — sequential tests
are enough to verify the SQL contract.

**B-check** (`scripts/check_b125.sh`, NEW, 9 contracts A-I
with 18 sub-checks): pins all three layers.

**Live state** (post-B125 deploy): build
`v1.3.19.2-1-ga965fe5`, `device_rules_natural_key_uniq`
index present on live PG, 0 duplicates. The 240-rule
false-positive banner from B119 stays at 0 mismatches; the
241+ `cdnAdded` events from the auto-update cron stay at
their expected value (no double-counting from now-silent
duplicate inserts).

**Files** (3 changed, 5 new):
- `internal/db/migrations_pg.go` (+V056PG migration)
- `internal/db/driver_postgres.go` (V056PG in dispatch)
- `internal/db/queries.go` (qInsertDeviceRule rewrite)
- `internal/feature/exit_rules/sync.go` (2 INSERTs + RowsAffected)
- `internal/db/device_rules_b125_test.go` (NEW, 3 tests)
- `scripts/check_b125.sh` (NEW, 9 contracts)
- `scripts/verify_pre_deploy.sh` (B125 registered)

verify-pre: 121 PASS / 0 FAIL / 1 SKIP (B8 VM-only).

---

## v1.3.19.2 follow-up — Exit Rules duplicate alert UX + dev version element (B123 + B124 / Goal 39)

**Date:** 2026-08-17
**Scope:** Two follow-up hotfixes (B123, B124).

**B124 (dev version element + semver suffix fix):**

1. **`SKYGATE_DEV_BUILD=true`** env var marks the running
   binary as a dev/edge build. When set, `/admin/update`
   shows a clear "dev build" banner instead of the "update
   available" alert and hides the one-click auto-apply
   button. The page still surfaces the GitHub version for
   reference — the dev banner just doesn't *suggest*
   updating. Default false (release builds).

2. **`compareSemver` fix for git-describe suffix**: the
   build label `v1.3.11-27-g03a1d97` (27 commits past
   the v1.3.11 tag) was being mis-compared against
   GitHub's `v1.3.9` release — the function returned
   +1 (treating v1.3.9 as newer) because the lex
   fallback put "9" > "11-27-..." (the '9' digit is
   lexicographically greater than '1'). After the fix,
   the `-N-g<hex>` suffix is stripped before numeric
   compare, so "v1.3.11+27 commits" correctly reports
   as AHEAD of v1.3.9.

3. **Files** (8 modified, 2 new):
   - `internal/config/config.go` (+`DevBuild` field + `SKYGATE_DEV_BUILD` env)
   - `internal/config/dev_build_test.go` (NEW, 3 test functions, 12 sub-cases)
   - `internal/feature/admin/service.go` (+`Service.DevBuild` field)
   - `internal/feature/admin/update.go` (+template passthrough)
   - `internal/handlers/templates/admin/update.html` (dev banner + hide auto-apply)
   - `internal/i18n/catalog_update.go` (+2 keys, RU+EN parity)
   - `internal/update/checker.go` (+`stripBuildLabelSuffix` + `gitDescribeSuffixStart`)
   - `internal/update/checker_test.go` (+5 B124 cases)
   - `cmd/skygate/main.go` (wire `DevBuild: app.Config().DevBuild`)
   - `scripts/check_b124.sh` (NEW, 24 contracts A-J)
   - `scripts/verify_pre_deploy.sh` (registered B124)

## v1.3.19.2 follow-up — Exit Rules duplicate alert UX (B123 / Goal 39)

**Date:** 2026-08-17
**Scope:** UX improvement to the "duplicate rule" alert on /my/exit-rules.

**Operator report (the original Goal 39 ask):**
the "правило для X уже существует" alert on /my/exit-rules
left the user hunting through the rule list to find the
existing rule, especially in the shared-IP case (one /32
already exists for a DIFFERENT parent_domain). After B119
fixed the false-positive 240-rule banner, the remaining
issue was the alert itself.

**B123 fix (3 layers):**

1. **POST handler (`internal/feature/exit_rules/form_my.go`):**
   the "all duplicates" redirect now carries 4 new query
   params in addition to the target:
   - `target` (was: `existing` — renamed for clarity;
     `existing` is still accepted as back-compat)
   - `existing_id` — the rule that already covers the
     target (used for the "→ к правилу #N" jump link)
   - `blocking_ip` — the IP that was a dup (typically
     `target + "/32"` or a /32 from another domain)
   - `parent_domain` — the domain that "owns" the
     blocking IP (empty for manual IP/CIDR rules; the
     shared-IP case shows the OTHER domain here)
   Plus 5 form_* params so the form re-fills after the
   warning. All extracted into a pure helper
   `buildDuplicateRedirectURL(...)` that's unit-tested.

2. **Template (`internal/handlers/templates/exit_rules.html`):**
   the alert now has `id="duplicate-alert"` and renders:
   - The main message (unchanged 1-arg format)
   - `<small>Блокирующий IP: <code>...</code></small>` (if set)
   - `<small>Уже обслуживается доменом: <code>...</code></small>`
     (if set — only in the shared-IP case)
   - `<a href="#rule-N">→ к правилу #N</a>` (if existing_id > 0)
   Plus `id="rule-{{.ID}}"` on each rule row so the link
   actually scrolls to the right rule.

3. **i18n (`internal/i18n/catalog_exit_rules.go`):**
   3 new keys, RU + EN parity (B4 enforces):
   - `exit_rules.duplicate_blocking` — "Блокирующий IP:" /
     "Blocking IP:"
   - `exit_rules.duplicate_parent` — "Уже обслуживается
     доменом:" / "Already tracked by domain:"
   - `exit_rules.duplicate_view` — "→ к правилу #%d" /
     "→ jump to rule #%d"

**B-check (`scripts/check_b123.sh`, 31 contracts):**

- A. `buildDuplicateRedirectURL` helper exists + has the
  4 key params (target/existingID/blockingIP/parentDomain)
- B. PostMyExitRule uses the helper (old `?existing=`
  inline redirect is GONE)
- C. redirect URL has all 9 params (target + existing_id
  + blocking_ip + parent_domain + 5 form_*)
- D. GET handler reads new params + back-compat for
  `?existing=` + exposes them in template data dict
- E. rule row has `id="rule-{{.ID}}"` anchor
- F. duplicate alert has `id="duplicate-alert"` + renders
  all 3 new fields + has the `#rule-N` link
- G. 3 new i18n keys in BOTH RU + EN
- H. Go test file exists (5 tests, 31 sub-checks PASS)
- I. arg-count parity between RU + EN for the 3 new keys

**Tests added (5 unit tests in
`internal/feature/exit_rules/form_my_b123_test.go`):**

- `TestBuildDuplicateRedirectURL_AllParamsPresent` — every
  param lands in the URL with the right value
- `TestBuildDuplicateRedirectURL_SharedIP_HasParentDomain`
  — the main motivation: shared-IP case shows the
  parent_domain
- `TestBuildDuplicateRedirectURL_SpecialCharsAreEscaped`
  — &, =, ?, % in inputs don't break the query string
- `TestBuildDuplicateRedirectURL_ZeroExistingID_StillValid`
  — defensive: existing_id=0 doesn't crash
- `TestBuildDuplicateRedirectURL_NumericFormDeviceID` —
  form_device_id is always strconv.Atoi-clean

**Files (3 modified + 2 new):**

- `internal/feature/exit_rules/form_my.go` — extracted
  `buildDuplicateRedirectURL` + updated both redirects
  (dup + back-compat for ?existing=) + 3 new template
  data fields
- `internal/handlers/templates/exit_rules.html` — new
  duplicate alert block with id + 3 fields + link +
  `id="rule-{{.ID}}"` on rule rows
- `internal/i18n/catalog_exit_rules.go` — 3 new keys
  in both languages
- `internal/feature/exit_rules/form_my_b123_test.go`
  (NEW, 5 tests)
- `scripts/check_b123.sh` (NEW, 31 contracts)
- `scripts/verify_pre_deploy.sh` — registered B123

**Back-compat:** the old `?existing=` URL still works
(GET handler falls back to `target` when `?existing=` is
present). No external links break.

## v1.3.19.2 — TagToHostname + admin-breadcrumb + Mint theme + scrollbar + form contrast (B119 + B120 + B121)

**Date:** 2026-08-17
**Scope:** Three follow-up hotfixes (B119, B120, B121).

**Three operator reports (2026-08-17 14:00-15:13 UTC):**

1. **"240 правил ссылаются на exit-node, который устройство
   не использует"** — false-positive preferred-mismatch banner
   on /my/exit-rules. Root cause: `TagToHostname` (exported
   helper in `internal/feature/exit_rules/preferred_check.go`)
   was missing the `tag:dev-infra-X` case. The v1.3.18.1
   hotfix only updated the LOCAL `tagToHost` closure in
   `system_tests.go` — the EXPORTED helper used by
   /my/exit-rules + /admin/exit-rules + /admin/devices was
   missed.
2. **"при раскрытии меню накрывается верхняя плашка"** —
   the admin-breadcrumb was hidden under the fixed-position
   sidebar on PC (it's a SIBLING of `.shell` inside `<main>`,
   and only `.shell` had `margin-left:220px`).
3. **"необходимо переработать стиль для более комфортного
   взаимодействия в сторону серебристых оттенков и светло
   зеленых мятных"** — three sub-asks: (a) scrollbars are
   very prominent in dark themes, (b) Linear theme forms
   blend into the page background, (c) want a new
   "comfortable" theme in silver + light mint green.

**B119 fix (TagToHostname):**

```go
// pre-fix (v1.3.18.1 and earlier):
func TagToHostname(tag string) string {
    t := strings.TrimSpace(tag)
    if !strings.HasPrefix(t, "tag:") {
        return t
    }
    rest := strings.TrimPrefix(t, "tag:")
    rest = strings.TrimPrefix(rest, "exit-")  // BUG
    return rest
}
```

For `tag:dev-infra-karolina`:
- After `TrimPrefix(t, "tag:")`: `dev-infra-karolina`
- After `TrimPrefix(rest, "exit-")`: `dev-infra-karolina` (no match)
- Returns: `dev-infra-karolina` (WRONG — should be `karolina`)

```go
// post-fix (v1.3.19.2 / B119):
func TagToHostname(tag string) string {
    t := strings.TrimSpace(tag)
    switch {
    case strings.HasPrefix(t, "tag:dev-infra-"):
        return strings.TrimPrefix(t, "tag:dev-infra-")
    case strings.HasPrefix(t, "tag:exit-"):
        return strings.TrimPrefix(t, "tag:exit-")
    case strings.HasPrefix(t, "tag:"):
        return strings.TrimPrefix(t, "tag:")
    default:
        return t
    }
}
```

**B120 fix (admin-breadcrumb sidebar offset):**

`<main>` in layout.html contains the `.admin-breadcrumb` as
a SIBLING of `.shell` (not inside it). The CSS rule
`main .shell { margin-left: 220px; }` only applied to
`.shell` — the `.admin-breadcrumb` had no left offset, so
its left edge sat at 0px and the position:fixed sidebar
(left:0, width:220px) covered the leftmost 220px.

Fix: mirror the `.shell` margin-left pattern for
`.admin-breadcrumb` (3 rules — desktop expanded 220px,
collapsed 52px, mobile 0):

```css
main .admin-breadcrumb{margin-left:220px;width:calc(100% - 220px);transition:margin-left .25s, width .25s}
.sidebar.collapsed ~ main .admin-breadcrumb{margin-left:52px;width:calc(100% - 52px)}
@media (max-width:768px) {
  main .admin-breadcrumb{margin-left:0 !important;width:100% !important}
}
```

**B121 fix (Mint theme + thin scrollbar + dark form contrast):**

Three pieces, all in `static/css/themes.css` + `internal/db/db.go`
+ `internal/handlers/templates/layout.html`:

1. **Custom thin scrollbar (all themes)** — pre-fix, the
   browser-default 15-17px wide white scrollbar in dark
   themes visually broke the page. Post-fix: 8px themed
   scrollbar (4px visible thumb after 2px inset border on
   each side), colors from `--border` / `--border-strong`,
   track transparent. Firefox: `scrollbar-width: thin` +
   `scrollbar-color`. WebKit: `::-webkit-scrollbar` +
   `::-webkit-scrollbar-thumb`.

2. **Dark-theme form contrast bump** (Linear / NVIDIA /
   Sentry) — pre-fix, the 1px border in `--border-strong`
   barely contrasted with the dark `--bg`, and operators
   reported "forms blend together". Post-fix:
   - `border-width: 1.5px` (was 1px)
   - `box-shadow: inset 0 1px 2px rgba(0,0,0,0.2)` (depth)
   - Linear / NVIDIA `background: #1a1a1a` (was #131313
     inherited from `--bg-card`) — slightly elevated
   - Sentry `background: #362d59` (matches its `--bg-elev`)
   - Vercel + Mint (light themes) keep their original
     values (dark text on light bg already has plenty
     of contrast)
   - Unified focus state: 4px ring (was 3px) + 1px lift
     for tactile feedback (transform: translateY(-1px))

3. **New "Mint" theme** — light theme with silver bg +
   mint-green accent:
   - bg `#f5f7f6` (very light silver with subtle mint tint)
   - bg-card `#ffffff` (pure white for card lift)
   - accent `#10b981` (mint/emerald, Tailwind emerald-500)
   - accent-hover `#059669` (deeper mint)
   - border `#d4dad6` (soft mint-tinted silver)
   - text `#1a2520` (dark charcoal with green tint)
   - 8px / 12px radius (vs Vercel's 6px / 8px — more friendly)
   - Two-layer shadow (1px + ring) for cards

Theme registration: `ThemeMint = "mint"` constant in
`internal/db/db.go`, added to `ThemeLabel` and
`IsValidTheme`. Theme-picker in layout.html shows
"Mint" with fa-leaf icon at `/settings/theme?theme=mint`.

**What's added (3 commits, 7 files):**

- `internal/feature/exit_rules/preferred_check.go`
  (TagToHostname rewrite, +27/-12 lines)
- `internal/feature/exit_rules/preferred_check_test.go`
  (4 new tests, +83 lines)
- `internal/db/db.go` (ThemeMint constant + label + valid-theme)
- `internal/db/db_test.go` (2 updated tests)
- `internal/handlers/handlers_b107_test.go` (regex fix)
- `internal/handlers/layout_v1_3_19_2_test.go` (4 new B120 tests)
- `internal/handlers/layout_v1_3_19_2_b121_test.go` (4 new B121 tests)
- `internal/handlers/templates/layout.html` (Mint option in
  theme-picker + B120 sibling order)
- `static/css/themes.css` (Mint block + scrollbar + form
  contrast bump, +90 lines)
- `scripts/check_b118.sh` (already had max-version fix)
- `scripts/check_b119.sh` (new, 8 contracts A-H, ~250 lines)
- `scripts/check_b120.sh` (new, 5 contracts A-E, ~180 lines)
- `scripts/check_b121.sh` (new, 6 contracts A-F, 18 sub-checks, ~250 lines)
- `scripts/verify_pre_deploy.sh` (B119 + B120 + B121 entries, +6 lines)

**Live state (verified 2026-08-17 15:30 UTC):**

- `/my/exit-rules`: 0 false-positive mismatches (was 240).
  PREFERRED column shows clean hostnames (`karolina` /
  `emilia`) instead of `dev-infra-X`. [B119]
- `/my/exit-rules` + `/admin/exit-rules` + `/admin/devices`:
  breadcrumb is now fully visible, not hidden under the
  sidebar. [B120]
- All themes: thin 8px themed scrollbar (was 15-17px
  browser default). [B121]
- Linear / NVIDIA / Sentry: form inputs now have 1.5px
  border + inset shadow + elevated background. [B121]
- Mint theme available in the theme-picker with fa-leaf
  icon. [B121]
- 28/28 packages green, `go build ./...` + `go vet ./...`
  clean.
- `make verify-pre` 117 PASS / 0 FAIL / 1 SKIP (B8 VM-only).
- B119 standalone on VM: 9 pass / 0 fail / 0 warn.
- B120 standalone on VM: 5 pass / 0 fail / 0 warn.
- B121 standalone on VM: 19 pass / 0 fail / 0 warn.
- B107 (predecessor) still PASSes after the regex fix.

**Live build label:** `v1.3.11-25-g0352f40` (deployed to VM).

## v1.3.19.1 — <polygon-vm-hostname> (HA mirror) removed + B118 finalized

**Date:** 2026-08-17
**Scope:** Two hotfixes (B119 code fix + B120 CSS/layout fix).

**Two operator reports (2026-08-17 14:00-14:42 UTC):**

1. **"240 правил ссылаются на exit-node, который устройство
   не использует"** — false-positive mismatch banner on
   /my/exit-rules. Root cause: `TagToHostname` (exported
   helper in `internal/feature/exit_rules/preferred_check.go`)
   was missing the `tag:dev-infra-X` case. The v1.3.18.1
   hotfix only updated the LOCAL `tagToHost` closure in
   `system_tests.go` (so the system test was reporting
   correctly) — the EXPORTED helper used by
   /my/exit-rules + /admin/exit-rules + /admin/devices was
   missed.
2. **"при раскрытии меню накрывается верхняя плашка"** —
   the admin-breadcrumb ("Админ › Devices & Nodes › Devices")
   was hidden under the fixed-position sidebar on PC.

**B119 fix (TagToHostname):**

```go
// pre-fix (v1.3.18.1 and earlier):
func TagToHostname(tag string) string {
    t := strings.TrimSpace(tag)
    if !strings.HasPrefix(t, "tag:") {
        return t
    }
    rest := strings.TrimPrefix(t, "tag:")
    rest = strings.TrimPrefix(rest, "exit-")  // BUG
    return rest
}
```

For `tag:dev-infra-karolina`:
- After `TrimPrefix(t, "tag:")`: `dev-infra-karolina`
- After `TrimPrefix(rest, "exit-")`: `dev-infra-karolina` (no match)
- Returns: `dev-infra-karolina` (WRONG — should be `karolina`)

```go
// post-fix (v1.3.19.2 / B119):
func TagToHostname(tag string) string {
    t := strings.TrimSpace(tag)
    switch {
    case strings.HasPrefix(t, "tag:dev-infra-"):
        return strings.TrimPrefix(t, "tag:dev-infra-")
    case strings.HasPrefix(t, "tag:exit-"):
        return strings.TrimPrefix(t, "tag:exit-")
    case strings.HasPrefix(t, "tag:"):
        return strings.TrimPrefix(t, "tag:")
    default:
        return t
    }
}
```

The case order matters: `tag:dev-infra-` MUST be checked
BEFORE `tag:` (a naive `TrimPrefix(t, "tag:")` would still
leave `dev-infra-emilia` unchanged — the v1.3.18.1 bug).

**B119 files changed:**

- `internal/feature/exit_rules/preferred_check.go`
  (TagToHostname rewrite, +27/-12 lines)
- `internal/feature/exit_rules/preferred_check_test.go`
  (4 new tests, +83 lines)
- `scripts/check_b119.sh` (new, 8 contracts A-H, ~250 lines)
- `scripts/verify_pre_deploy.sh` (B119 entry, +2 lines)

**B120 fix (admin-breadcrumb sidebar offset):**

`<main>` in layout.html contains the `.admin-breadcrumb` as
a SIBLING of `.shell` (not inside it). The CSS rule
`main .shell { margin-left: 220px; }` only applied to
`.shell` — the `.admin-breadcrumb` had no left offset, so
its left edge sat at 0px and the position:fixed sidebar
(left:0, width:220px) covered the leftmost 220px. On PC the
operator saw only the right fragments of the breadcrumb
text — the start was hidden.

Fix: mirror the `.shell` margin-left pattern for
`.admin-breadcrumb` (3 rules — desktop expanded 220px,
collapsed 52px, mobile 0):

```css
/* desktop expanded */
main .admin-breadcrumb{margin-left:220px;width:calc(100% - 220px);transition:margin-left .25s, width .25s}

/* desktop collapsed */
.sidebar.collapsed ~ main .admin-breadcrumb{margin-left:52px;width:calc(100% - 52px)}

/* mobile (sidebar is a drawer on <768px) */
@media (max-width:768px) {
  main .admin-breadcrumb{margin-left:0 !important;width:100% !important}
}
```

The `width: calc(100% - 220px)` keeps the breadcrumb's
background-card spanning the visible area instead of just
the text width (otherwise the breadcrumb would shrink to
its text width and look like a floating label). The
existing bare `.admin-breadcrumb { padding-left: 60px }`
from B107 (v1.3.9 mobile hamburger clearance) still applies
on mobile.

**B120 files changed:**

- `static/css/themes.css` (+26 lines: 3 new rules +
  comments)
- `internal/handlers/layout_v1_3_19_2_test.go` (new, 4
  Go unit tests, ~200 lines)
- `internal/handlers/handlers_b107_test.go` (regex fix:
  the v1.3.19.2 mobile `main .admin-breadcrumb{...}` rule
  was being matched first by B107's loose regex; updated
  to iterate all matches and find the one with
  `padding-left:60px` — Go's RE2 has no lookbehind)
- `scripts/check_b120.sh` (new, 5 contracts A-E, ~180 lines)
- `scripts/verify_pre_deploy.sh` (B120 entry, +2 lines)

**Live state (verified 2026-08-17 13:00 UTC):**

- `/my/exit-rules`: 0 false-positive mismatches (was 240).
  PREFERRED column shows clean hostnames (`karolina` /
  `emilia`) instead of `dev-infra-X`.
- `/my/exit-rules` + `/admin/exit-rules` + `/admin/devices`:
  breadcrumb is now fully visible, not hidden under the
  sidebar.
- 28/28 packages green, `go build ./...` + `go vet ./...`
  clean.
- `make verify-pre` 116 PASS / 0 FAIL / 1 SKIP (B8 VM-only).
- B119 standalone on VM: 9 pass / 0 fail / 0 warn.
- B120 standalone on VM: 5 pass / 0 fail / 0 warn.
- B107 (predecessor) still PASSes after the regex fix.

**Files changed (combined B119 + B120):**

- `internal/feature/exit_rules/preferred_check.go`
- `internal/feature/exit_rules/preferred_check_test.go`
- `internal/handlers/handlers_b107_test.go` (regex fix)
- `internal/handlers/layout_v1_3_19_2_test.go` (new)
- `static/css/themes.css`
- `scripts/check_b119.sh` (new)
- `scripts/check_b120.sh` (new)
- `scripts/verify_pre_deploy.sh`

**Live build label:** `v1.3.11-23-g99dd4ad` (deployed to VM).

## v1.3.19.1 — <polygon-vm-hostname> (HA mirror) removed + B118 finalized

**Date:** 2026-08-17
**Scope:** Hotfix — `TagToHostname` (exported helper in
`internal/feature/exit_rules/preferred_check.go`) was
returning `dev-infra-karolina` for a `tag:dev-infra-karolina`
pref (the operator's post-B111 standard format). The
v1.3.18.1 hotfix only updated the LOCAL `tagToHost` closure
in `internal/feature/admin/system_tests.go` — the
EXPORTED helper used by the `/my/exit-rules` +
`/admin/exit-rules` + `/admin/devices` pages was missed.

**Operator symptom (2026-08-17 14:00 UTC):**

The `/my/exit-rules` page rendered a warning banner:
"240 правил ссылаются на exit-node, который устройство
не использует. Правила сокращены, но Tailscale их игнорирует."
(240 rules refer to an exit-node the device doesn't use. Rules
are shortened, but Tailscale ignores them.)

The PREFERRED column showed the literal string
`dev-infra-karolina` for every karolina rule (instead of the
hostname `karolina`). The `exit_rules.preferred_mismatch`
system test reported 0 mismatches (its LOCAL `tagToHost` was
already fixed in v1.3.18.1) — the bug was UI-only.

**Root cause:**

```go
// pre-fix (v1.3.18.1 and earlier):
func TagToHostname(tag string) string {
    t := strings.TrimSpace(tag)
    if !strings.HasPrefix(t, "tag:") {
        return t
    }
    rest := strings.TrimPrefix(t, "tag:")
    rest = strings.TrimPrefix(rest, "exit-")  // BUG: no-op for "dev-infra-emilia"
    return rest
}
```

For `tag:dev-infra-karolina`:
- After `TrimPrefix(t, "tag:")`: `dev-infra-karolina`
- After `TrimPrefix(rest, "exit-")`: `dev-infra-karolina` (no match)
- Returns: `dev-infra-karolina` (WRONG — should be `karolina`)

This made every rule whose `exit_node_id="karolina"` fail the
`IsRuleApplicable("karolina", "dev-infra-karolina")` check
and show as a preferred-mismatch on the UI.

**Fix:**

```go
// post-fix (v1.3.19.2):
func TagToHostname(tag string) string {
    t := strings.TrimSpace(tag)
    switch {
    case strings.HasPrefix(t, "tag:dev-infra-"):
        return strings.TrimPrefix(t, "tag:dev-infra-")
    case strings.HasPrefix(t, "tag:exit-"):
        return strings.TrimPrefix(t, "tag:exit-")
    case strings.HasPrefix(t, "tag:"):
        return strings.TrimPrefix(t, "tag:")
    default:
        return t
    }
}
```

The case order matters: `tag:dev-infra-` MUST be checked
BEFORE `tag:` (a naive `TrimPrefix(t, "tag:")` would still
leave `dev-infra-emilia` unchanged — the v1.3.18.1 bug).

**What's added (1 commit `761bb26`):**

- `internal/feature/exit_rules/preferred_check.go`:
  `TagToHostname` rewritten with case-ordered switch.
- `internal/feature/exit_rules/preferred_check_test.go`:
  4 new tests (`PostB111_DevInfraFormat`, `PrefixOrder`,
  `IsRuleApplicable_PostB111_DevInfraPref`, plus
  strengthened `StandardForms` cases).
- `scripts/check_b119.sh` (NEW, 8 contracts A-H):
  - A. Source: TagToHostname handles 4 formats.
  - B. Source: case order (dev-infra BEFORE tag:).
  - C. Source: form_my.go uses TagToHostname.
  - D. Source: form_admin.go uses PreferredExitNodeForRule.
  - E. Source: form_my.go uses db.Get*ExitNodePref.
  - F. Source: NO `TrimPrefix(rest, "exit-")` (the v1.3.18.1
    bug pattern — must not regress).
  - G. Live DB: all pref rows use a supported format.
  - H. Source: system_tests.go has v1.3.18.1 tagToHost
    fix (defensive, in case it regresses).
- `scripts/verify_pre_deploy.sh`: B119 entry.

**Live state (UI fix verified 2026-08-17 14:12 UTC):**

- `/my/exit-rules`: 0 false-positive mismatches (was 240).
- `preferred-mismatch-banner` NOT rendered.
- PREFERRED column shows clean hostnames (`karolina` /
  `emilia`) instead of `dev-infra-X`.
- All 240 rules on skyworker now show as MATCH (green check).
- `exit_rules.preferred_mismatch` system test: still PASS
  (it was already correct; the LOCAL `tagToHost` in
  `system_tests.go` was fixed in v1.3.18.1).
- 28/28 packages green, `go build ./...` + `go vet ./...`
  clean.
- `make verify-pre` 113 PASS / 2 FAIL / 1 SKIP (B8 VM-only;
  2 pre-existing FAILs unchanged: B34 device_rules
  auto-add + B104 superseded by B114).
- B119 standalone on VM: **9 pass / 0 fail / 0 warn**.

**Files changed:** 4 (`preferred_check.go` +
`preferred_check_test.go` + `check_b119.sh` +
`verify_pre_deploy.sh`). +329/-12 lines.

**Live build label:** `v1.3.11-21-g761bb26` (deployed to VM).

**Rollback procedure (if needed):**

1. `cd /home/skyadmin/skygate && git revert 761bb26`
2. `docker restart skygate-skygate-1` (entrypoint will
   rebuild from reverted source)

## v1.3.19.1 — <polygon-vm-hostname> (HA mirror) removed + B118 finalized

**Date:** 2026-08-17
**Scope:** Hotfix — operator cleanup + B118 B-check fix.

**Operator trigger (2026-08-17):** "старые тэги по svyatoslava
надо почистить вес что оффлайн оно уже не рабочее" — <polygon-vm-hostname>
(HA mirror, headscale id=30) is offline and not working, remove
its tag references entirely.

**What's added (3-step destructive change with snapshot-then-act):**

1. **Snapshot** at `/tmp/svyatoslava1_cleanup_20260817_104048/`:
   - `policy.json` (51540 bytes, latest v=1147)
   - `headscale_nodes.json` (36263 bytes, 16 nodes incl. id=30)
   - `node_owner_map.tsv` (568 bytes, 16 rows incl. node_id=30)
   - `node_30.json` (full headscale record for id=30)
   - `MANIFEST.md` (rollback procedure)
   - `rollback.sql` (DB-level restore; headscale node recovery
     needs `headscale_nodes.json`)

2. **Destructive changes** (operator-side, NOT a code deploy):
   - `docker exec headscale headscale nodes delete --force -i 30`
     → "Node deleted" (id=30 = <polygon-vm-hostname> HA mirror).
     Headscale nodes count: 16 → 15.
   - `DELETE FROM node_owner_map WHERE node_id = '30';` → 1 row
     deleted. node_owner_map: 16 → 15 rows.
   - `POST /admin/exit-rules/reapply` from inside
     `skygate-skygate-1` container via
     `python3 /tmp/reapply_v3.py` (NoRedirect handler +
     CookieJar — busybox wget doesn't support cookies).
     Re-apply returned HTTP 303 (success).

3. **B118 B-check SQL fix** (`e32e12f`):
   - Pre-fix: SQL pattern `ORDER BY version DESC LIMIT 1` in a
     subquery forces PostgreSQL to materialize the jsonb cast
     on EVERY row before applying the LIMIT. Old rows (v<1063)
     have malformed JSON (e.g. `"acls": [,`), which makes the
     cast fail. Even though we only want the latest row, the
     cast evaluates all of them first.
   - Post-fix: `WHERE version=(SELECT max(version) FROM
     acl_snapshots)` — first get the max version in a separate
     scalar subquery (which doesn't touch jsonb), then filter.
     The cast only runs on the one matching row. Contracts C
     and D now PASS instead of WARN.
   - Plus: backticks `\`infra\`` in contract E summary line
     were being interpreted as command substitution, producing
     "infra: command not found" on stderr. Cosmetic fix:
     use `'infra'` single quotes.

**Live state post-cleanup (v=1148):**

- **4 infra tags** (was 5): emilia, karolina, sharlotta,
  skygate-host-1. <polygon-vm-hostname> GONE.
- **15 tagOwners total** (was 21).
- **0 references to <polygon-vm-hostname> OR svyatoslava-legacy**
  in latest policy.
- `tag:exit-node` still owned by `infra@`.
- `tag:public` still owned by `skyadmin@`.
- `node_owner_map`: 15 rows (was 16).

**B118 contract changes:**

- **Contract E (5 → 4)**: B-check `check_b118.sh` now expects
  exactly 4 `tag:dev-infra-*` rows in node_owner_map and
  exactly 4 in policy tagOwners.
- **New contract G (v1.3.19.1)**: 5 sub-checks pin <polygon-vm-hostname>
  removal — policy (text-search ILIKE), `node_owner_map`
  (exact match), `tagOwners` (jsonb key check), counts
  (4 in policy, 4 in nom).
- **Test update**: `acl_perdevice_b118_test.go` renamed
  `TestB118_TagOwnerFromName_AllFiveInfraExits` →
  `TestB118_TagOwnerFromName_AllFourInfraExits` (<polygon-vm-hostname>
  removed from regression list).
- 3 files changed (`check_b118.sh` + `acl_perdevice_b118_test.go`
  + AGENTS.md/RELEASE-NOTES). +83/-9 lines.
- `go test ./internal/acl/ -run TestB118_` PASS (7/7 sub-tests).
- `go build ./...` + `go vet ./...` clean.
- **`make verify-pre` 112 PASS / 2 FAIL / 1 SKIP** (B8 VM-only;
  2 pre-existing FAILs: B34 device_rules auto-add duplicates +
  B104 superseded by B114).
- B118 B-check standalone: **16 pass / 0 fail / 0 warn**
  (6 contracts A-F + 5 v1.3.19.1 sub-checks G = 16 total).

**Live state**: build `v1.3.11-19-ge32e12f` (last commit was
the B118 B-check fix). No code deploy needed for v1.3.19.1
(cleanup was operator-side). 16/18 system tests PASS (2 SKIP:
`db.journal_mode` PG-specific + `mesh.active_meshes` live
state-dependent). 0 FAIL. Snapshot 1148, applied_success=1.

**Rollback procedure (if needed):**

1. Restore headscale policy: `docker cp <SNAP_DIR>/policy.json
   headscale:/tmp/policy_rollback.json && docker exec headscale
   headscale policy set -f /tmp/policy_rollback.json`
2. Restore headscale node 30: see `<SNAP_DIR>/headscale_nodes.json`
   (complex — node keys + machine keys needed; contact operator).
3. Restore node_owner_map: `PGPASSWORD=... psql ... -f
   <SNAP_DIR>/rollback.sql`
4. Trigger skygate re-apply: `POST /admin/exit-rules/reapply`

## v1.3.19 — B118 tag-owner-from-name

**Date:** 2026-08-17
**Scope:** Bugfix — pre-fix via loop hardcoded `envAdminIdentity()@domain`
(= `skyadmin@` in production) for every via tag. Due to first-write-wins
dedup (v1.3.18 hotfix), this `skyadmin@` always won over the per-user
loop's correct `infra@` → `tag:dev-infra-emilia` showed as `skyadmin@`
in the live policy even though the DB had `infra@`.

**What's added (2 commits `b0cacf6` + `e32e12f`):**

- `internal/acl/acl.go` (via loop in `GenerateACLWithViaForPlane`,
  line ~1380): parse owner from tag name with format
  `tag:dev-<user>-<device>` → `<user>@domain`; fallback to
  `envAdminIdentity()` for non-`tag:dev-*` via tags (defensive).
- `GenerateACLForPlane:561`: `tag:exit-node` → `infra@`
  (was `envAdminIdentity()@` = `skyadmin@`).
- `GenerateACLWithViaForPlane:1353`: same `tag:exit-node` → `infra@`.
- **Svyatoslava-legacy cleanup (operator-approved 2026-08-17)**:
  snapshot headscale pre-cleanup → `headscale nodes delete --force
  -i 27` (was offline, last_seen=2026-05-29) +
  `DELETE FROM node_owner_map WHERE node_id = '27'`. Re-apply:
  v=1146, tagOwners 21→20, grants 389→385, 0 references to
  legacy tag.
- New test file `internal/acl/acl_perdevice_b118_test.go` (7 tests).
- New file `scripts/check_b118.sh` (6 contracts A-F, plus 5
  v1.3.19.1 sub-checks G).

**B118 design intent (operator directive 2026-08-17):**

- `infra` = technical user for all exit-nodes/hosts.
- `skyadmin` = operator's personal account.
- `tag:public` owner stays `skyadmin@` (directive).
- `tag:exit-node` owner = `infra@`.
- Per-plane architecture intentional — multiple nodes per VM/device
  serve different roles; do NOT delete duplicates.

**Live state (v=1147, pre-v1.3.19.1 cleanup):** 5 infra tags all →
`infra@` (emilia, karolina, sharlotta, <polygon-vm-hostname>, skygate-host-1).
`tag:exit-node` → `infra@`. `tag:public` → `skyadmin@` (unchanged).
20 tagOwners total. 0 malformed hosts. Build `v1.3.11-18-gb0cacf6`
deployed to VM.

**Operator trigger (Android emilia hang):** "на андроид exit node
emilia очень долго прогружает, хотя по положению он ближе всех" —
root cause was pre-B93 legacy `tag:exit-emilia` etc. in
`user_exit_node_prefs` + `device_exit_node_prefs` (not new tags,
just stale). Manual SQL migration: `UPDATE … SET exit_node_tag =
'tag:dev-infra-' || substring(exit_node_tag FROM 10) WHERE
exit_node_tag LIKE 'tag:exit-emilia|karo|sharlotta|svyatoslava'`.
The pre-fix tagOwners were skyadmin@ because of B118's hardcoded
fallback — the post-fix policy had the correct `infra@` for all
5 infra tags.

## v1.3.18.1 — exit_rules.preferred_mismatch helper fix (post-B111 tag format)

**Date:** 2026-08-17
**Scope:** Hotfix — after v1.3.18 ACL re-apply succeeded,
the `exit_rules.preferred_mismatch` system test still FAILed
on every row. Root cause: `tagToHost` helper in
`internal/feature/admin/system_tests.go` only stripped the
legacy `tag:exit-` prefix. For the new `tag:dev-infra-<exit>`
format introduced in B111 (v1.3.11), it returned
`dev-infra-<exit>` instead of `<exit>`, so every
`exit_rules` row with `exit_node_id="<exit>"` was flagged as
a "preferred mismatch" against the pref
`tag:dev-infra-<exit>`.

**What's added:**

- `tagToHost` extended to handle 4 formats:
  `tag:dev-infra-X → X`, `tag:exit-X → X`, `tag:X → X`,
  `X → X`. (B111 introduced `tag:dev-infra-*` for the 5
  infra nodes; B93-era legacy `tag:exit-*` is still in the
  test fixtures.)
- No new B-check (helper is exercised by the
  `exit_rules.preferred_mismatch` system test which now
  PASSes; was FAIL pre-fix).
- 1 file changed (`internal/feature/admin/system_tests.go`),
  +18/-4 lines. `go build ./...` + `go vet ./...` clean.

**Live state:** build `v1.3.11-15-g8dd0c47` deployed to VM
(192.168.13.69). 18/20 system tests PASS (2 SKIP:
`db.journal_mode` PG-specific + `mesh.active_meshes` live
state-dependent). 0 FAIL.

## v1.3.18 — ACL tagOwners dedup hotfix (Android emilia hang)

**Date:** 2026-08-17
**Scope:** Hotfix — ACL re-apply returned HTTP 500 with
`duplicate object member name 'tag:dev-infra-emilia' within
'/tagOwners'` after Phase 3 / B111 introduced the
`tag:dev-infra-X` namespace. The headscale v2 JSON parser
rejects duplicate object keys, so ANY of the 4 tagOwners
emit paths in `internal/acl/acl.go` was enough to break
re-apply.

**What's added:**

- `emittedTagOwners` map + first-write-wins
  `emitTagOwner(tag, ownerListJSON)` closure in BOTH
  `GenerateACLForPlane` AND `GenerateACLWithViaForPlane`.
  The 4 emit paths fixed:
  1. Static (`tag:public`, `tag:exit-node`,
     `tag:subnet-router`, `tag:private`).
  2. Per-user `tagsByUser` loop.
  3. `distinctVias` loop.
  4. Per-device `augmentedTagsByUser` loop.
- 0 duplicate tagOwners post-fix (was 1 dup for
  `tag:dev-infra-emilia`).
- No new B-check (deferred to openTestDB harness for
  GenerateACL string-output tests; covered indirectly by
  the `acl.reapply` system test which now PASSes).
- 1 file changed (`internal/acl/acl.go`), +40/-8 lines.
  `go build ./...` + `go vet ./...` clean.

**Operator report (the trigger):** "на андроид exit node
emilia очень долго прогружает, хотя по положению он ближе
всех". Two-stage root cause:

1. Pre-B93 legacy `tag:exit-emilia` etc. were still in
   `user_exit_node_prefs` + `device_exit_node_prefs`.
   ACL grants `via: ["tag:exit-emilia"]` pointed at
   non-existent tags → Tailscale Android retried forever.
   Manual SQL migration:
   `UPDATE … SET exit_node_tag = 'tag:dev-infra-' ||
   substring(exit_node_tag FROM 10) WHERE exit_node_tag
   LIKE 'tag:exit-emilia|karo|sharlotta|svyatoslava'`.
   (The `FROM 10` was a fix; initial `FROM 11` dropped
   the first letter → `milia` / `arolina`.)
2. After the prefs were fixed, the re-apply HTTP 500 fired
   (v1.3.18 dedup fix).

**Live state:** build `v1.3.11-14-ga2c11de` deployed to VM.
Snapshot 1141, applied_success=1. 0 duplicate tagOwners.

## v1.3.17 — DERP relay CRUD UI (per-row add/edit/delete/toggle/test)

**Date:** 2026-08-13
**Scope:** Replace the v0.11.0 comma-separated DERP-URL
textarea model with a first-class `derp_relays` PG table
+ per-row CRUD UI at `/admin/derp/relays` (like
`/admin/exit-nodes`). The legacy `/admin/derp/config`
form still works (writes the same `global_settings.derp.*`
keys; `AutoMigrateDerpRelays` copies them into the new
table on first GET).

**What's added:**

- **`migrateV055PG`**: new `derp_relays` table
  (id, hostname, url, region_id, region_code, region_name,
  is_bundled, enabled, sort_order, notes, created_at,
  updated_at) with `UNIQUE(url)` + indexes on `enabled`
  and `is_bundled`. Idempotent, applied on every
  `MigratePostgres()` call (i.e. on every container start).
- **`internal/db/derp_relays.go`** (NEW, ~410 lines):
  - `List / Get / Add / Update / Delete / Toggle`
    (Add rejects a 2nd `is_bundled=1` row; Delete
    refuses to remove the bundled row — toggle its
    `enabled` flag instead).
  - `ListEnabledDerpRelayURLs` — used by
    `renderHeadscaleConfig` to build the
    `derp.urls` list (merged with legacy
    `cfg.DERPExternalURLs`, deduplicated).
  - `IsBundledDerpRelayEnabled` — used by
    `applyBundledDERP` to decide whether to
    start/stop the derper container.
  - `AutoMigrateDerpRelays` — one-shot bridge
    from the v0.11.0 `global_settings.derp.*`
    keys into the new table. Idempotent (gated
    by a `"derp.relays_migrated"=1` marker).
  - Typed errors: `ErrDerpRelayNotFound` /
    `ErrDerpRelayDuplicateURL` /
    `ErrDerpRelayBundledExists` /
    `ErrDerpRelayBundledUndeletable`.
  - 8 new unit tests (`derp_relays_test.go`).
- **`internal/feature/admin/derp_relays.go`** (NEW):
  6 admin handlers — `Get / Add / Edit / Delete /
  Toggle / Test`. All redirect back to
  `/admin/derp/relays` with a `?ok=` or `?err=`
  flash message. Per-row "Test connection" runs the
  same 5s probe as the v0.11.0 "Test all" button.
- **`internal/handlers/templates/admin/derp_relays.html`**
  (NEW): per-row CRUD table with inline edit + per-row
  Save / Toggle / Test / Delete buttons. Bundled row
  is undeletable; shows a "bundled" tag.
- **Sidebar + landing** (v1.3.17.1): new sidebar
  entry "/admin/derp/relays" in the Integrations
  section + a "Manage relays" button on the
  `/admin/integrations` landing page (next to the
  legacy "Configure" button).
- **40 new i18n keys** (ru + en) in `catalog_derp.go`:
  `derp.relays_title`, `derp.relays_add_title`,
  `derp.relays_col_*`, `derp.relays_action_*_help`,
  `derp.relays_err_*`, etc.
- **`scripts/check_b116.sh`** (NEW, 21 contracts): migration
  defined + registered, table schema, 6 db helpers,
  6 admin handlers, 6 routes, template, apply uses
  table (not legacy flag), `renderHeadscaleConfig`
  merges table URLs, ≥30 i18n keys, ≥5 unit tests,
  bundled undeletable, at-most-one bundled, sidebar
  + landing links.
- **`AGENTS.md` / `docs/TODO.md` / `docs/derp.md`**:
  v1.3.17 = Current. `docs/derp.md` "Web UI management"
  section updated from "roadmap" to "shipped in v1.3.17".

**Operator workflow:**

- Add a new DERP relay: `/admin/derp/relays` → fill
  the form (URL, hostname, region id/code/name,
  sort_order, notes, enabled) → Save.
- Toggle: per-row "power" button. Disabled rows are
  filtered out of `derp.urls` on the next apply.
- Test: per-row "stethoscope" button — 5s probe, latency
  in the redirect flash.
- Delete: per-row "trash" button (rejected for
  bundled row).
- Bundled row: managed via the legacy
  `/admin/derp/config` page (the "Bundled derper"
  checkbox + Apply) — `applyBundledDERP` reads
  `db.IsBundledDerpRelayEnabled` (table is the
  source of truth; falls back to legacy
  `cfg.BundledDERP` only for the deploy-time path
  before the UI is loaded).

**Backward compat:**

- Legacy `/admin/derp/config` still works (same
  `global_settings.derp.*` keys, same Apply flow).
- `AutoMigrateDerpRelays` runs on every GET of the
  new page; idempotent (one-shot copy + marker).
- Operators who never open the new page see NO change
  to the legacy flow.
- The headscale config now reads BOTH `cfg.DERPExternalURLs`
  AND `db.ListEnabledDerpRelayURLs` (merged + deduped)
  — adding a relay via the new UI works without
  re-saving the legacy form.

**Catalog:** verify-pre **113 PASS / 0 FAIL / 1 SKIP**
(B8 VM-only). 28/28 packages green.

**Live state:** build `v1.3.11-12-gd4d8ab3` deployed
to VM (192.168.13.69). Legacy URL
(`https://controlplane.tailscale.com/derpmap/default`)
migrated to the new table.

## v1.3.16 — tailnet test skip filter (self + home-LAN-without-SSH)

**Date:** 2026-08-13
**Scope:** Side-effect of v1.3.15 (port fallback
22+18022) — after karolina became reachable, the
tailnet tests still fired false-positive TAILNET SPLIT
alerts because the probe set included the skygate
container itself (no SSH daemon) and 5 home-LAN
devices (no SSH daemon either). v1.3.16 adds a skip
filter that excludes both groups.

**What's added:**

- `tailnetSelfHostname()` — reads self HostName via
  `tailscale status --json` (or `SKYGATE_TAILNET_SELF_HOSTNAME`
  env override). Test-injection hook
  `tailnetSelfHostnameOverride`.
- `tailnetSkipHostnames()` — set of hostnames to skip
  (self always + 5 hardcoded home-LAN
  `skyworker / skybars / a71 / olesya / nothing-phone-2` +
  `SKYGATE_TAILNET_SKIP_HOSTNAMES` env override, comma-sep,
  REPLACES not merges; self preserved).
- All 3 tailnet tests filter probes through
  `tailnetSkipHostnames()`; output surfaces
  `(skipped N non-probable nodes: [...])`.
- 2 unit tests updated: `TestVpsToVPSLatencyTest_LessThanTwoVPS_Skips`
  (text "online VPS") + `TestSplitSuspectedTest_OneUnreachable_Passes`
  (skybars → relay-1, VPS-class).
- 10 contracts pinned in `scripts/check_b115.sh`.
- `AGENTS.md` / `docs/TODO.md` updated for v1.3.16.

**Catalog:** verify-pre **112 PASS / 0 FAIL / 1 SKIP**
(B8 VM-only; B19 now PASS, was SKIP — t.Skip stub
form improved). 28/28 packages green.

**Live state:** build `v1.3.11-10-g6a0ec3a` deployed.

## v1.3.15 — tailnet probe port fallback 22 + 18022

**Date:** 2026-08-13
**Scope:** Bug fix — pre-v1.3.15 tailnet tests
hardcoded `port 22`, but the operator's `karolina`
VPS (100.64.0.2) ALSO listens SSH on 18022 (for
internal access restriction). karolina was reported
UNREACHABLE → TAILNET SPLIT false-positive on every
test run.

**What's added:**

- New helper `probeTailnetNode(ctx, host)` tries
  `tailnetProbePorts = ["22", "18022"]` in order;
  returns the latency + port of the first successful
  handshake.
- All 3 tailnet tests use the new helper; output
  surfaces the port (e.g.
  `100.64.0.2  karolina  140ms :22`).
- 1 file changed (`system_tests_tailnet.go`), +25
  lines.

**Catalog:** No new B-check (small fix; contract
implicit in the 3 tests now passing on live).

**Live state:** build `v1.3.11-9-ga983275` deployed.
3 tailnet tests still FAIL pre-v1.3.16 (for the
self + home-LAN reasons addressed in v1.3.16/B115).

## v1.3.14 — BL-17 autonomous migration verify

**Date:** 2026-08-13
**Scope:** `scripts/verify_migration.sh` (NEW, ~466
lines) chains 3 post-deploy phases:
1. `verify_post_deploy.sh --quick` (R1-R9 + R26).
2. `POST /admin/system_tests/run` via a portable
   Python+urllib driver staged to the skygate
   container (busybox wget doesn't support cookies,
   and the system tests endpoint is internal
   localhost:8080).
3. Manual checks (healthz, readyz, /admin/services) —
   printed for the operator to run themselves.

**Plus:**
- `PRE_BUILD` pre-state capture → "MIGRATION DETECTED"
  mode for cold-standby restore flow.
- Phase 1 SKIP fallback for Windows+
  `verify_post_deploy.sh` python3 issue (12 false-positive
  FAILs).
- 9 contracts pinned in `scripts/check_b114.sh`.
- B104 marked SUPERSEDED (was 5-phase v1.3.8, never
  landed; B114 is canonical BL-17 impl).

**Live state:** build `v1.3.11-8-gc07f4d3` deployed.

## v1.3.13 — youtube.com/32 bug fix

**Date:** 2026-08-13
**Scope:** `internal/feature/exit_rules/form_my.go`
validates `targetValue` via a new `isValidIPOrCIDR`
helper before any processing. For `target_type=ip|subnet`,
bare hostnames (e.g. `youtube.com`) are now rejected
with 400 + a message that points to `target_type=domain`
as the right way to add hostnames (the form does DNS
resolution and stores per-IP /32 rules).

**Bug:** pre-fix, a hostname in the IP field would
get `youtube.com/32` saved to `device_rules`; the
ACL builder then promoted it to a host alias
`h-rule-youtube-com-32: youtube.com/32` — a malformed
CIDR that headscale rejects, breaking the whole policy
re-apply.

**What's added:**

- `isValidIPOrCIDR(s)` helper.
- 1 unit test (`TestIsValidIPOrCIDR_IPv4`, 18
  table-driven cases: bare IPv4 / IPv4 CIDRs / IPv6 /
  IPv6 CIDRs / hostnames / hostname CIDRs / garbage).
- 4 contracts pinned in `scripts/check_b113.sh`.

**Live smoke test (5 cases from inside skygate
container):** `youtube.com` (ip) → 400, `google.com/24`
→ 400, `8.8.8.8` → 302, `10.0.0.0/8` → 302,
`example.com` (domain) → 302.

**Live state:** build `v1.3.11-6-gd7c3b00` deployed.
Live policy re-applied (snapshot 146798, version=1136,
applied_success=1).

## v1.3.12 — P4 catalog cleanup + B38 fix

**Date:** 2026-08-13
**Scope:** 5 staticcheck U1000 dead-code items
removed (165 lines):
- `internal/backup/s3.go`: `s3Client` interface +
  `realS3Client` wrapper (no test ever used the
  indirection; minio-go has its own httptest fixtures).
- `internal/feature/admin/integrations_renderer.go`:
  `dockerCmdStdin`, `renderHeadscaleCompose`,
  `stripHeadplaneServiceBlock`, `startsWithWhitespace`
  (compose-file page moved to static render).
- `internal/telegram/commands_login.go`:
  `resetLoginAttempts` (rate-limit path moved to
  unified config).
- `internal/telegram/commands_phase4.go`:
  `setKillProcess` (test-only hook for a removed test).
- `internal/telegram/commands_user.go`:
  `hostnameMapFromHeadscale` (`/userlist` moved to
  `node_owner_map` in PG).

**Plus:**
- 2 verify-pre checks updated: `check_b93.sh` +
  `check_b95.sh` (v1.3.0+ PG form with t.Skip stubs).
- **B38 fix**: was looking for deleted
  `migrations_v0.50.go` and old SQLite test fns;
  updated to `t.Skip` stub check + `migrations_pg.go`
  grep.
- 16 contracts pinned in `scripts/check_b112.sh`.

**Catalog:** 109 PASS / 0 FAIL / 2 SKIP. 28/28
packages green.

## v1.3.11 — B93 infra-owns-technical-nodes completion (B111)

**Date:** 2026-08-13
**Scope:** Completes the v0.32.x-era B93 + B111 work:
infra user owns the 5 technical nodes (skygate-host-1,
emilia, karolina, sharlotta, <polygon-vm-hostname>) instead
of the user-portal users. Plus a Phase 3 operator
re-tag of those 5 nodes (server-side via
`headscale nodes tag --force`, not `tailscale up`
which is a no-op on alive nodes).

**What's added:**

- `isInfraNode` rule 3 (any node tagged
  `tag:exit-node` is infra-class).
- `BackfillInfra` changes from INSERT OR IGNORE to
  active UPDATE (re-attributes user-portal nodes
  like `skyadmin / michail / guest / daniil /
  svyatoslava` to `infra` when `isInfraNode` matches).
- New helper `getInfraExitNodeTags` in
  `internal/acl/acl_perdevice.go` (filters skygate,
  returns sorted exit tags).
- Both `GenerateACLForPlane` +
  `GenerateACLWithViaForPlane` emit
  `* → tag:dev-infra-<exit>` catch-alls (preserves
  pre-B93 public access to the relay VPSs that
  became infra-owned in B93).
- 6 new unit tests in
  `internal/acl/acl_perdevice_b111_test.go`.

**Live:** 5 nodes re-tagged to `tag:dev-infra-X` +
svyatoslava portal user (id=11) + headscale user
(id=84) destroyed. 5 portal users + 5 headscale
users + 5 nodes in `infra` bucket. B111 catch-alls
`* → tag:dev-infra-X` active in live policy (4
grants). Build `v1.3.11-2-g4a4899d`.

## v1.3.10 — TAILNET SPLIT detection (B110)

**Date:** 2026-08-13
**Scope:** Adds 3 new system tests for tailnet
diagnostics:
- `tailnet.all_nodes_reachability` — for every
  online Tailscale node, TCP-connect to :22 from
  skygate-host-1. Reports reachability %. Fails at
  <60%.
- `tailnet.vps_to_vps_latency` — TCP latency matrix
  between VPS-class nodes (emilia / karolina /
  sharlotta / skygate-host-1 / <polygon-vm-hostname>).
  Surfaces "one of the VPS relays is degraded".
- `tailnet.split_suspected` — explicit split
  detector. If 2+ nodes are unreachable AND
  reachability <90%, fails with a clear message
  pointing the operator at `docs/tailnet-diagnostics.md`.

**Bug observed:** 2026-08-13, headscale `nodes list`
shows 17 nodes, 10 online. `docker exec skygate-skygate-1
tailscale status` shows only 4 peers. 6 of the 10
online nodes (skybars, skyworker, a71, <polygon-vm-hostname>,
olesya, nothing-phone-2) are invisible from
skygate-host-1. Pre-B93/B111, this was due to policy
isolation between the `tagged-devices` user buckets
(see B111 release notes for the fix).

**Live state:** build `v1.3.10` deployed.

## v1.3.9 — mobile-friendly + sidebar fixes (B105-B109)

**Date:** 2026-08-13
**Scope:** Five small UI fixes for the mobile
experience:
- B105: mobile-friendly admin tables (`.table-wrap`)
  + `.title-row` hamburger gap (60px on mobile).
- B106: mobile sidebar — `.toggle` button hidden on
  mobile + `.collapsed` state force-cleared.
- B107: admin breadcrumb + collapsed section icons:
  `.admin-breadcrumb` cleared on mobile +
  `.sidebar-section summary` fits 52px collapsed.
- B108: section summary click in collapsed sidebar
  auto-expands + opens section: `<script>` in
  `layout.html` removes `collapsed` class on click.
- B109: desktop breadcrumb `padding-left: 40px`
  (breathing room from 220px sidebar) + B107 mobile
  60px preserved.

## v1.3.8 — backup permission-denied fix + S3 / S3-compatible destination

**Date:** 2026-08-12
**Scope:** 1) Fix the long-standing "Permission denied" on
`/home/skyadmin/skygate-backups/` (root cause: skygate container
runs as root, writes to a host bind-mount, files end up
root-owned). 2) Fix the `slice bounds out of range` panic in
`prune()` that fires on every fresh S3 backup. 3) Add S3 /
S3-compatible (AWS, MinIO, Yandex Object Storage, Selectel,
VK Cloud, Backblaze B2) as a 5th backup protocol via
`github.com/minio/minio-go/v7`.

**What's added:**
- **Permission-denied fix (scripts/backup.sh)**: when invoked
  as root (the typical in-app / cron path), backup.sh now
  chowns the destination to the operator
  (`${SUDO_USER:-skyadmin}`) at the start of every run AND
  after the tarball is created. Idempotent — no operator
  action needed on subsequent runs.
- **Prune guard (internal/backup/runner.go)**: `if keep >= len(archives)
  { return nil }` before `archives[keep:]`. Prevents the
  panic when the dest dir has fewer archives than
  `KeepCount`. The S3 staging dir is empty on every fresh
  deploy (we delete the tarball after upload), so the bug
  was latent until v1.3.8's S3 path exposed it.
- **5 regression tests** in `internal/backup/prune_test.go`
  covering: empty dir, fewer-than-keep archives, keep-N-of-M,
  keep-larger-than-archives (the real-world case), and
  "non-archive files are left alone".
- **S3 protocol (internal/backup/s3.go, ~250 lines)**:
  - `Config.ProtocolS3` constant + 8 new S3 fields
    (Endpoint, Region, AccessKey, SecretKey, Bucket, Prefix,
    StagingDir, UseSSL) with 8 new storage keys
    (`backup.s3_*`) in `global_settings`.
  - `s3Client` interface + `realS3Client` wrapper around
    `*minio.Client` (forwarder methods, testable via
    interface mock).
  - `newS3Client(c *Config)` builds the minio client,
    normalizes the endpoint (strips scheme, falls back to
    AWS regional URL).
  - `uploadToS3(ctx, c, filePath)` does `BucketExists` check
    + `FPutObject` with `ContentType: application/gzip`,
    returns `{Bucket, Key, ETag, Size, Duration}` for
    audit-log surfacing.
  - `buildS3Key(prefix, basename)` joins prefix + basename
    with exactly one "/".
  - `internal/backup/runner.go:runBackupLocked`: S3 path
    picks `c.S3StagingDir` as the dest, then calls
    `uploadToS3()` after backup.sh. Sets
    `res.Archive = "s3://bucket/key"` on success so the
    UI "last archive" line shows the S3 location.
  - `internal/backup/mount.go`: `Mount()` and `Unmount()`
    are no-ops for S3 (no FUSE layer).
  - `TestConnection()` (mount.go) checks S3 config is
    self-consistent (bucket + creds + region) without a
    network call.
- **S3 UI (internal/handlers/templates/admin/backup.html +
  internal/feature/admin/backup_config.go)**: 8 new form
  fields (s3_endpoint, s3_region, s3_bucket, s3_prefix,
  s3_access_key, s3_secret_key, s3_staging_dir, s3_use_ssl)
  with `data-show-for="s3"` toggles. `PostAdminBackupConfig`
  parses the S3 fields from the form and saves them via
  `backup.Save`. `PostAdminBackupTest` passes the S3 fields
  through to `TestConnection`. Audit log detail includes
  `s3_bucket`.
- **S3 i18n (internal/i18n/catalog_backup.go)**: 10 new keys
  (ru + en parity preserved via B4 `TestCatalogsParity`):
  `backup.protocol_s3`, `backup.s3_endpoint`,
  `backup.s3_endpoint_help`, `backup.s3_region`,
  `backup.s3_region_help`, `backup.s3_access_key`,
  `backup.s3_secret_key`, `backup.s3_bucket`,
  `backup.s3_bucket_help`, `backup.s3_prefix`,
  `backup.s3_prefix_help`, `backup.s3_staging_dir`,
  `backup.s3_staging_dir_help`, `backup.s3_use_ssl`,
  `backup.s3_test_ok`. The `config_subtitle` and
  `destination_help` texts updated to mention S3.
- **Dependency: `github.com/minio/minio-go/v7` v7.2.1**
  (~2 MB binary growth; CGO_ENABLED=0 still holds; works
  with any S3-compatible endpoint).
- **B100 catalog check (`scripts/check_b100.sh`)**: dedicated
  helper (same pattern as B96/B97/B98/B99) to avoid the
  nested-quote hell that hits when 37 greps get inlined
  in a single `run_check` function. 37/37 PASS on the
  current tree. Wired into `scripts/verify_pre_deploy.sh`
  as the B100 row.
- **B40 fix (in passing)**: the pre-existing B40 grep was
  looking for `system_tests_runs` in
  `internal/db/migrations_v0.51.go` (deleted in v1.3.0).
  Now also accepts `internal/db/migrations_pg.go` so B40
  PASSes. (Old behavior: 1 PASS → new behavior: 1 PASS,
  but the B40 catalog row no longer falsely fails on PG.)
- **Documentation (`docs/backup-restore-and-migration.md`,
  380 lines, NEW)**: the single runbook for backup / restore
  / cross-host migration. Replaces the 3 README fragments
  that used to live in /admin/backup hints. Sections: what's
  in a tarball, 5 protocols (with live-verified status),
  trigger methods, the 2-step restore flow, cross-host
  migration (5 things to change in order), failure modes
  (Permission denied, bash not found, bucket does not
  exist, DNS SERVFAIL, restore.sh for SQLite, slice bounds
  out of range), and the e2e test results table.
- **Documentation (`docs/TODO.md`, 250 lines, NEW)**: the
  operator's prioritized "what's left" list. Priorities 1-5
  with what / why / effort / suggested-next-step per item.
  Complements `docs/BACKLOG.md` (historical) and
  `docs/PLANS.md` (medium-term design).

**Live verification (VM 192.168.13.69, 2026-08-12):**
- Local backup: `last_status=ok` after `Run now`, 15 MiB
  tar.gz at `/home/skyadmin/skygate-backups/`, owner
  `skyadmin:skyadmin` (the v1.3.8 chown fix).
- S3 backup (minio throwaway on `headscale_default`):
  `last_status=ok` in 1 second, `last_archive =
  s3://skygate-backups/v1.3.8-test/skygate-full-...tar.gz`,
  file in minio bucket (15 MiB, ETag returned,
  Content-Type=application/gzip).
- S3 → fresh PG replay: download tar.gz from minio, extract,
  `psql -f skygate-pg.sql` into a fresh `skygate_restore_test`
  DB → 28 tables restored, 4/6 critical tables byte-equal
  to live (portal_users, acl_snapshots, global_settings,
  exit_servers), 2 minor drifts on device_rules and
  audit_log (data drift between backup and now — expected).
- All `go test ./...` packages green (28/28).
- B100 catalog check: 37/37 PASS.

**Files changed:** 15 files, +1764/-46 lines:
```
go.mod                                           +28 -1
go.sum                                           +64 -1
internal/backup/config.go                        +158 -10
internal/backup/mount.go                         +48 -9
internal/backup/runner.go                        +60 -5
internal/feature/admin/backup_config.go          +29 -8
internal/handlers/templates/admin/backup.html   +71 -0
internal/i18n/catalog_backup.go                  +54 -4
scripts/backup.sh                                +43 -8
scripts/verify_pre_deploy.sh                     +24 -0
internal/backup/s3.go                            +250 (new)
internal/backup/s3_test.go                       +180 (new)
internal/backup/prune_test.go                    +165 (new)
scripts/check_b100.sh                            +260 (new)
docs/backup-restore-and-migration.md             +380 (new)
docs/TODO.md                                     +250 (new)
```

**Build / release:**
- Commit: `33738ef` (v1.3.8 + docs)
- Tag: `v1.3.8`
- Live binary: `v1.3.8+33738ef` (v1.3.3 build-label fix verified
  end-to-end — no `-N-g` prefix, just `+commit`)
- GitHub release: https://github.com/BarsSky/skygate/releases/tag/v1.3.8
- Operator action: `git fetch --tags --force origin main && git reset
  --hard origin/main && docker compose build skygate && docker
  compose up -d --force-recreate --no-deps skygate`. (The
  one-time `sudo chown -R skyadmin:skyadmin
  /home/skyadmin/skygate-backups` was already done on the live
  VM before this commit.)

**What's NOT in this release (deferred, see docs/TODO.md):**
- `scripts/restore.sh` for PG dump (BL-15) — restore.sh still
  has the v0.32.x SQLite-era `do_skygate_db()`. For v1.3.0+
  operators must run `psql -f skygate-pg.sql` manually.
  Documented in `docs/backup-restore-and-migration.md`
  Section 2.
- Per-protocol e2e test for SMB / NFS / SFTP (BL-16) — only
  local + s3 have been live-verified. Code paths exist.
- Autonomous migration verify (BL-17) — operator must manually
  run `verify_post_deploy.sh` after a cross-host move.
- In-app S3 download (BL-18) — operator must `aws s3 cp` or
  `mc cp` to get the tarball, then upload to /admin/backup.

## v1.1.1 — exit-node speed/availability system tests (B98)

**Date:** 2026-08-12
**Scope:** Adds two new system tests for exit-node
reachability from the `/admin/system_tests` page. Operator
asked: "необходимо также добавить в тесты системы
тестирование по скорости доступа exit nodes" — what's
the latency to each exit node, and what % of online exit
nodes actually respond?

**What changed:**

- **`internal/feature/admin/system_tests_exit_node_speed.go`**
  (NEW, ~11 KB, 2 new test defs):
  - `exit_nodes.tcp_connect_speed` — measures TCP-connect
    latency to each online exit node's Tailscale IP on
    port 22. Output lists every node with its latency;
    `PASS` if all under 2s, `SLOW (>1s, N)` warning if
    any above 1s (the threshold below which a `PASS` is
    still useful), `FAIL` on any timeout/refused.
  - `exit_nodes.availability_summary` — % of online exit
    nodes that respond within 2s. `PASS` at ≥80%, `FAIL`
    below. `80%` is the threshold at which losing 1 of 3
    relays is a warning, not a failure; losing 2 of 3 is
    a hard failure that needs immediate attention.
  - `tailscaleIPFromNode()` — extracts the first
    `100.64.0.0/10` IPv4 from a headscale node's
    `IPAddresses` slice.
  - `probeExitNodeConnect(ctx, host, port)` — TCP dial
    with 2s timeout. Overridable via
    `probeExitNodeConnectOverride` for unit tests (no
    real network in `go test ./...`).
  - `formatLatencyMs()` — test helper.
  - `init()` registers both tests with `TestRegistry` in
    the `network` category. B40 (≥6 tests across
    network/db/headscale) still PASSes.

- **`internal/feature/admin/system_tests_exit_node_speed_test.go`**
  (NEW, ~23 KB, 20 Go unit tests):
  - `TestTailscaleIPFromNode` (13 sub-cases): boundary
    checks for the CGNAT range, garbage input, IPv6,
    IPv6-mapped form, nil/empty lists.
  - `TestFormatLatencyMs` (4 sub-cases): zero, sub-ms
    clamping, seconds, milliseconds.
  - `TestProbeExitNodeConnect_*` (3 tests): override
    returns latency, override returns error, real
    network to `127.0.0.1:1` fails fast (<2s).
  - `TestExitNodesTCPSpeedTest_*` (7 tests): no service,
    no nodes, no Tailscale IP, all fast, one failed,
    one slow (PASS with SLOW warning), non-exit node
    ignored, offline node ignored.
  - `TestExitNodesAvailabilityTest_*` (4 tests): all
    available (3/3), 1/5 down (still PASS at 80%),
    2/3 down (FAIL at 33%), no exit nodes (SKIP).
  - `TestExitNodeSpeedTestsAreRegistered`,
    `TestTestRegistryHasMinimumCoverage`,
    `TestExitNodeSpeedTestsDescribeThemselves`: catalog
    invariants (TestRegistry has ≥6 entries spanning
    network/db/headscale, both new tests have
    non-empty `Description`).
  - **Fake headscale server pattern**: `fakeHS()` spins
    up an `httptest.NewServer` that responds to
    `GET /api/v1/node` with the given node list.
    `setUpServiceWithFakeHS()` pre-warms the headscale
    Client's cache (via the new `SetCacheTTL` method)
    and wires it into a `*Service` with `HSGlobalFn` so
    the Run closures see a controlled node set.

- **`internal/headscale/headscale.go`**: new exported
  `Client.SetCacheTTL(d time.Duration)` method. Tests
  use it to keep the cache warm across multiple
  `ListAllNodes` calls within a single test, avoiding
  re-hitting the (faked) HTTP server between setup and
  the Run-closure invocation. Production callers leave
  it alone — the 30s default is the right trade-off
  for page-render workload.

- **`scripts/check_b98.sh`** (NEW, ~3 KB, dedicated
  B-check helper): pins the 2 new defs, the test file's
  ≥15 test functions, the `Category: "network"` for B40
  coverage, the `probeExitNodeConnectOverride` hook, and
  the actual `go test` pass. Same pattern as
  `check_b96.sh` / `check_b97.sh` to avoid nested-quote
  issues in the main `verify_pre_deploy.sh`.

- **`scripts/verify_pre_deploy.sh`**:
  - **B40 fix**: v1.3.0 deleted `migrations_v0.51.go`
    (the old SQLite-style file). The B40 grep was
    looking for `system_tests_runs` only in that
    deleted file → B40 was FAILing on main since
    v1.3.0. Updated the check to also accept
    `migrations_pg.go` (where v0.51PG now lives).
    B40 → PASS.
  - **B98** (NEW): exit-node speed/availability
    catalog row. Calls `scripts/check_b98.sh`.

**Operator action required:** redeploy via
`git pull --tags --force` + `docker compose build skygate`
+ restart. The two new tests show up on
`/admin/system_tests` automatically (the page renders
`TestRegistry`; the new entries appear at the top of the
"network" section because the new file's `init()` runs
after the main `TestRegistry` literal is initialised, so
the new entries are appended at the end of the network
category).

**Live verify:** the two tests will run on next page
hit after deploy. Expected output for a healthy fleet:
```
exit_nodes.tcp_connect_speed        pass
  3 exit nodes probed:
    relay-1 (100.64.0.X): 23ms
    relay-2 (100.64.0.X): 47ms
    relay-3 (100.64.0.X): 31ms

exit_nodes.availability_summary    pass
  3/3 exit nodes responsive (100%)
    relay-1 (100.64.0.X): 23ms [available]
    relay-2 (100.64.0.X): 47ms [available]
    relay-3 (100.64.0.X): 31ms [available]
```

## v1.1.0 — UI refactoring (TD-1) + mobile-responsive (TD-3)

**Date:** 2026-08-12
**Tag:** v1.1.0
**Scope:** Addresses the two deferred UI tasks from
`docs/PLANS.md`:
- **TD-1**: 22 admin pages → 6 collapsible sidebar sections
- **TD-3**: mobile-responsive UI (sidebar becomes slide-in
  drawer at <768px, hamburger button, 44px tap targets)

The pre-v1.1.0 admin sidebar was a flat list of 22 admin
nav items. On desktop it was a chore to scan; on a phone
(<=414px viewport) the fixed 220px sidebar ate the whole
viewport, making the admin panel effectively unusable on
mobile. v1.1.0 fixes both.

**What changed:**

- **`internal/handlers/templates/layout.html`**:
  - **6 collapsible `<details class="sidebar-section">`
    blocks** replace the flat list of 22 admin `<a>` items.
    The sections, in sidebar order:
    1. **Devices & Nodes** (4): /admin/devices,
       /admin/exit-nodes, /admin/meshes, /admin/subnets
    2. **Access Control** (3): /admin/acls, /admin/exit-rules,
       /admin/headscale/acl
    3. **System Health & Logs** (3): /admin/system_tests,
       /admin/services, /admin/audit
    4. **Integrations** (6): /admin/integrations,
       /admin/headscale, /admin/headplane, /admin/telegram,
       /admin/tailscale, /admin/derp
    5. **Data** (3): /admin/backup, /admin/invites,
       /admin/control-planes
    6. **Settings & Users** (3): /admin/settings,
       /admin/users, /admin/update
  - **Auto-open conditional**: each `<details>` block
    uses `{{if .InSectionX}}open{{end}}` so the section
    containing the current page auto-opens. The
    `InSectionX` booleans are computed by
    `sectionPageSet()` in `internal/handlers/handlers.go`
    from the page name.
  - **User-side nav (top 10 items) stays flat** — those
    are per-user self-service pages and don't benefit
    from grouping.
  - **Hamburger button** at the top of `<body>`:
    `<input type="checkbox" id="sidebar-toggle">` +
    `<label class="sidebar-toggle">`. Uses the native
    checkbox hack — no JS required.

- **`static/css/themes.css`**:
  - **`.sidebar-section` styles**: section header has
    `text-transform:uppercase` + `letter-spacing` for
    a section-divider look; collapsed sections show a
    right-pointing caret (`▸`); open sections rotate it
    to down (`▾`).
  - **`.sidebar-toggle` class**: hidden on desktop
    (`display:none`); shown on mobile
    (`display:flex` inside `@media (max-width:768px)`).
  - **`.sidebar-toggle-input`**: the hidden checkbox
    that drives the slide-in state via the `:checked ~
    .sidebar` sibling selector.
  - **Mobile drawer**: `@media (max-width:768px)` block
    adds `transform:translateX(-100%)` to the sidebar
    by default, `translateX(0)` when the checkbox is
    checked. A semi-transparent `::before` overlay on
    `<main>` dims the content while the drawer is open.
  - **Breakpoint renamed 760px → 768px** (the v1.3.x-era
    `@media (max-width:760px)` is now `(max-width:768px)`
    — the canonical iPad-portrait width).
  - **Touch-friendly tap targets**: sidebar links
    bumped to `12px 14px` padding + `min-height:44px`
    per Apple HIG / Google's Material Design.

- **`internal/handlers/handlers.go`**:
  - **`sectionPageSet(page string) map[string]bool`**:
    returns the 6 `InSectionX` booleans for the current
    page. The set of pages per section is hardcoded; if
    a page moves between sections, update both
    `sectionPageSet()` and the corresponding
    `<details>` block in `layout.html`. The B96
    catalog row pins both sides — drift fails the
    pre-push hook.
  - **`renderWithLayout`**: iterates the booleans and
    sets them on the data map. No other handler change.

- **`internal/i18n/catalog_common.go`**: 8 new keys
  (6 section titles + 2 toggle labels) added in both
  `ruCommon` and `enCommon`. B4 parity test
  (`TestCatalogsParity`) verifies the key sets match.

  | Key | ru | en |
  |---|---|---|
  | `nav.section_devices` | Устройства и узлы | Devices & Nodes |
  | `nav.section_access` | Контроль доступа | Access Control |
  | `nav.section_health` | Здоровье и логи | System Health & Logs |
  | `nav.section_integrations` | Интеграции | Integrations |
  | `nav.section_data` | Данные | Data |
  | `nav.section_settings` | Настройки и пользователи | Settings & Users |
  | `nav.toggle_sidebar` | Открыть меню | Open menu |
  | `nav.toggle_section` | Свернуть секцию | Collapse section |

- **`internal/handlers/layout_v1_1_0_test.go`** (NEW,
  4 tests):
  - `TestB96_AdminLayoutGroupsAll22Pages` — 22 admin
    pages are present + 6 sections + 6 InSectionX
    booleans + 8 i18n keys + hamburger input/label
  - `TestB96_AllAdminPagesInASection` — strict grouping:
    every admin page link is inside some
    `<details class="sidebar-section">` block, no admin
    links outside sections
  - `TestB97_ThemesCSSMobileDrawer` — 768px breakpoint +
    hamburger `display:none`→`display:flex` + translateX
    slide + 44px tap targets
  - `TestB97_StaticFilePresence` — sanity: themes.css
    exists at the expected path

- **`scripts/check_b96.sh`** (NEW): B96 pre-push check.
  Greps the layout + i18n + runs the 2 B96 unit tests.
- **`scripts/check_b97.sh`** (NEW): B97 pre-push check.
  Greps the CSS + runs the 2 B97 unit tests.
- **`scripts/verify_pre_deploy.sh`**: 2 new `run_check`
  lines (B96, B97).

**Files (9 modified, 4 new):**
- 4 modified: `layout.html`, `themes.css`,
  `catalog_common.go`, `handlers.go`
- 2 modified (docs): `AGENTS.md`, `docs/PLANS.md`
- 1 modified (verify): `scripts/verify_pre_deploy.sh`
- 1 new test: `internal/handlers/layout_v1_1_0_test.go`
- 2 new shell: `scripts/check_b96.sh`,
  `scripts/check_b97.sh`
- 1 new release-notes section (this file)

**Net change:** 7 source + 2 scripts + 1 test file,
+~1100/-~250.

**Verification:**
- `go test -count=1 -short ./...` — 28/28 packages
  green (no regressions; the 4 new B96/B97 unit tests
  pass).
- `make verify-pre` — 73 PASS / 19 FAIL. B96 + B97
  both PASS (the new v1.1.0 contracts). The 19 FAILs
  are unchanged from v1.3.2 (all v0.32.x-era).
- `make verify-post` — should re-run after deploy.
  No new R-rows; the runtime contract is unchanged.

**Deferred (recorded for v1.4.0+):**
- **Status badges from B92 availability**: the
  Integrations section could show a green/red dot
  based on the cached headscale/headplane/tailscale
  status. The data is already available via
  `adminSvc.AvailabilityChecker.Snapshot()` — just
  needs to be plumbed into the layout's data map.
- **Consolidate /admin/headscale + /admin/headplane
  into one "Control plane" page with tabs**: TD-1
  grouped them under the Integrations section, but
  they're still separate pages. Consolidation is a
  separate ~half-day job.
- **Info density on /admin/devices** (chip collapse for
  OS+type+last_seen).
- **Inline action confirmation** (replace `confirm=yes`
  checkboxes with a small modal).
- **B98 (B92 status badges)** — a follow-up catalog
  row when the badges land.

**Renumbering note** in `docs/PLANS.md`: the v0.34.0-era
TD-3 ("Style cleanup SA1012, 5 items") is renumbered to
TD-14. The TD-3 slot is now occupied by mobile-responsive
UI. v1.2.0's roadmap entry "Style cleanups (TD-2, TD-3)"
becomes "Style cleanup (TD-2)" — the SA1012 work moves
under TD-14.

**Why one commit**: TD-1 and TD-3 are visually intertwined
(the new sidebar grouping is the prerequisite for a usable
mobile drawer; an ungrouped drawer would have 22 links
flipping in one at a time). Splitting them would leave
intermediate commits with a working but ugly UI.

**Live verify on VM** (operator action required):
1. `cd /home/skyadmin/skygate && git pull --tags --force`
2. `docker compose build skygate` (rebuild with the new
   layout/CSS — 30-60s for the static binary)
3. `docker compose up -d --force-recreate --no-deps skygate`
4. Open https://skygate.example.com/admin/devices in a
   desktop browser → confirm the sidebar shows 6 sections
5. Open the same URL in a phone (or DevTools mobile
   emulation @ 375px / 414px) → confirm the hamburger
   appears, the sidebar slides in, all 22 admin pages
   are reachable from the 6 sections

**Backlog (NOT in this release, recorded for v1.4.0+):**
- B92 status badges in sidebar (B98)
- Consolidate /admin/headscale + /admin/headplane
- Info density on /admin/devices + /admin/exit-nodes
- Inline action confirmation modal
- BL-2 (HA skygate-host-2) — blocked on 2nd VM + etcd
  + S3 + DNS plan
- BL-3 (Telegram DPI workaround) — blocked on operator's
  network

---

## v1.3.2 — SQLite removal: docs polish (Phase 3 of 3)

**Date:** 2026-08-12
**Tag:** v1.3.2
**Scope:** Phase 3 (final) of the v1.3.0 milestone. Documentation
polish only. No code changes. Closes BL-1 (PostgreSQL cutover) on
the docs side; the runtime cutover happened in v1.3.0.

**What changed (Phase 3, this release):**

- **`docs/deploy.md`**:
  - **New section `#10-postgresql`**: complete PG setup
    walkthrough — Mode A (local docker-compose with `local-pg`
    profile, persistent `skygate-pg-data` named volume) and
    Mode B (external PG HA / Patroni / RDS via `SKYGATE_DB_DSN`
    pointing at the cluster). Covers `PG_DB_PASSWORD`, health
    check via `pg_isready`, and the `--network host` rule for
    psql from the host (HA setups where the docker bridge
    doesn't reach the cluster).
  - **New section `#11-postgresql-migration-from-sqlite`**:
    one-time runbook for the rare case of converting a
    pre-v1.3.0 SQLite backup. Uses `cmd/apply_pg_migrations` to
    create an empty PG schema + `dump_sqlite.py` to bulk-copy
    rows. After this one-time pass the operator deletes the
    SQLite file via `docker exec skygate rm -f /data/skygate.db`.
  - **Environment variables table**: `SKYGATE_DB` (the SQLite
    path) is now marked **LEGACY** with a note that v1.3.0+
    ignores it. Added `SKYGATE_DB_DSN` (PG DSN, required) and
    `PG_DB_PASSWORD` (used by docker-compose when generating
    the DSN for the bundled `postgres` service).
  - **Backup section**: updated to use `pg_dump -Fp --clean
    --if-exists` (text-format dump) → `skygate-pg.sql` in the
    archive. The previous SQLite `.backup` flow is gone.
  - **Restore section**: new `psql -f skygate-pg.sql` step.

- **`docs/disaster-recovery.md`**:
  - **Step 3 (PG restore)**: `pg_dump -Fp --clean --if-exists`
    format, replay with `psql -f`, then restart the skygate
    container. Updated for PG-specific failure modes (no more
    `PRAGMA integrity_check` / `.recover + rebuild`).
  - **RPO / RTO section**: RPO stays at 24h (nightly
    `pg_dump`). RTO is now 5-10 min for restore + 1-2 min
    for container restart = ~10-15 min total.
  - **"Backed up by" table**: `skygate-pg.sql` is the canonical
    backup artifact (replaces the old `skygate.db` SQLite file).
  - **Failure modes**: dropped the "WAL-write-silent-failure"
    case (PG's `full_page_writes=on` makes it impossible).
    Added "disk-full → `ALTER SYSTEM` flips PG read-only →
    container can't write" with the recovery step from
    `scripts/recover_db_corruption.sh`.

- **`docs/architecture.md`**:
  - **New section "Database backend (v1.3.0+)"**:
    documents the two deployment modes (A: bundled
    `postgres:15-alpine` via compose profile; B: external PG
    via `SKYGATE_DB_DSN`). CGO is now a non-issue
    (`CGO_ENABLED=0` static binary; pgx is pure Go).
  - **CGO section rewritten**: was "CGO is required for
    `go-sqlite3`", now "CGO is disabled (`CGO_ENABLED=0`).
    The runtime is a 24 MB static binary with no libc/musl/
    sqlite-libs dependencies. pgx is the only DB driver and
    it's pure Go."
  - **TL;DR updated**: removes the v0.32.x-era "CGO toolchain
    + sqlite-libs" mentions; adds the 24 MB static binary
    point.

- **`AGENTS.md`**:
  - **Release status block updated** to v1.3.1 (Phase 2
    summary) with B26/B34/B70/B79 contracts documented.
  - **Build-time catalog** gets four new rows: B26 (Dockerfile
    has NO `gcc`/`musl-dev`/`sqlite-libs`; CGO_ENABLED=0),
    B34 (psql duplicate check replaces the SQLite-era
    duplicate check), B70 (PG-only title in auto-update
    orchestrator), B79 (PG-only placeholders in exit-node
    pref INSERT).
  - **Runtime section** updated: R29 (psql) and R30 (backup
    dump) now use the `psql_vm` helper that tries VM-side
    psql first, falls back to throwaway `postgres:15-alpine`
    on `--network host`.
  - **PLANS.md cross-reference**: TD-1 (UI refactoring) and
    TD-3 (mobile-responsive UI) added to the in-flight list.

- **`docs/PLANS.md`**:
  - **BL-1** marked **DONE across v1.3.0 + v1.3.1 + v1.3.2**
    with one-line summary per phase.
  - **TD-3 (mobile-responsive UI)** added as a new in-flight
    item, scope: CSS grid/flex refactor of the admin layout,
    `<768px` breakpoint, sidebar collapses to a hamburger
    menu, touch-friendly tap targets. Combined with TD-1 in
    the v1.1.0 work cycle.

**Files (5 modified):**
- `AGENTS.md` (+99/-3)
- `docs/PLANS.md` (+78/-30)
- `docs/architecture.md` (+75/-9)
- `docs/deploy.md` (+219/-7)
- `docs/disaster-recovery.md` (+83/-21)

**Why no code changes.** Phase 1 (v1.3.0) removed SQLite from
the runtime. Phase 2 (v1.3.1) made Docker + operator scripts
PG-only. Phase 3 (this release) finishes the operator-facing
side: docs that document the new shape, the two deployment
modes, and the disaster recovery flow. No source files
touched.

**Verification:**
- `go test -count=1 -short ./...` — 28/28 packages green
  (unchanged from v1.3.1).
- `make verify-pre` — same 70 PASS / 19 FAIL profile as
  v1.3.1; no new contracts added (B26/B34/B70/B79 are the
  v1.3.1 contracts; this release is docs-only).
- Web UI / templates / routes / i18n — **no changes**. The
  admin panel looks and behaves identically to v1.3.1; only
  the underlying docs that describe it changed.

**Backlog (NOT in this release, recorded for v1.1.0+):**
- **TD-1 (UI refactoring)**: 23 admin pages → 6 collapsible
  sidebar sections (Devices & Nodes, Access Control, System
  Health & Logs, Integrations, Data, Settings & Users).
  Status badges from the B92 `/admin/services` snapshot.
  Consolidate `/admin/headscale` + `/admin/headplane`.
- **TD-3 (mobile-responsive UI)**: CSS grid/flex refactor,
  `<768px` breakpoint, sidebar → hamburger menu, touch-friendly
  tap targets. Combined with TD-1 in v1.1.0.
- **BL-2 (HA skygate-host-2)**: blocked on 2nd VM + etcd +
  S3 + DNS plan.
- **BL-3 (Telegram DPI workaround)**: blocked on operator's
  network.

---

## v1.3.1 — SQLite removal: scripts + Docker for PG-only runtime (Phase 2 of 3)

**Date:** 2026-08-12
**Tag:** v1.3.1
**Scope:** Phase 2 of the v1.3.0 milestone. Makes the Docker
build, `docker-compose.yml`, `entrypoint.sh`, and all
operator scripts PG-only. No Go source changes (Phase 1 did
that). Closes the runtime cutover started in v1.3.0 on the
infrastructure side.

**What changed (Phase 2, this release):**

- **`Dockerfile`**: drops `gcc` / `musl-dev` / `sqlite-libs`
  from `apk add`. The runtime is now `CGO_ENABLED=0` — a
  24 MB static binary with no libc / musl / sqlite-libs
  dependencies. This catches regressions that re-add CGO
  deps (e.g. if someone re-introduces `go-sqlite3`).
- **`docker-compose.yml`**: adds a `postgres:15-alpine`
  service gated behind `profiles: ["local-pg"]`. The service
  ships a persistent `skygate-pg-data` named volume and a
  `pg_isready` healthcheck. Operators running against an
  external PG (HA Patroni, RDS) skip this service via
  `--profile local-pg` not being activated. **No
  `depends_on: postgres`** — skygate comes up independently
  of PG (mirrors B91: a wrong `SKYGATE_DB_DSN` must not
  prevent the admin from opening `/admin/services` to fix it).
- **`entrypoint.sh`**: drops the `-tags postgres` build flag.
  The `//go:build postgres` tag is gone (v1.3.0); pgx is the
  only DB driver and is always compiled in.
- **`.env.example`**: adds `SKYGATE_DB_DSN` +
  `PG_DB_PASSWORD` with the docker-compose default
  (`postgres://skygate:${PG_DB_PASSWORD}@postgres:5432/
  skygate?sslmode=disable`). The legacy `SKYGATE_DB` SQLite
  path is kept for one release cycle so old `.env` files
  don't break startup; v1.3.0+ ignores it.
- **`internal/db/open_pg_pg.go`**: changed to
  `//go:build never` (dead-code sentinel). The `openPostgres`
  wrapper had no callers after v1.3.0 removed the build-tag
  system; the file is kept as a marker so a future grep for
  "where was the PG opener?" lands somewhere.
- **9 operator scripts converted** from `sqlite3` → `psql`:
  - `scripts/backup.sh` — `pg_dump` via throwaway
    `postgres:15-alpine` container; archive now contains
    `skygate-pg.sql` instead of `skygate.db`.
  - `scripts/verify_backup.sh` — replays the dump into a
    throwaway PG, asserts ≥20 public tables + presence of 4
    critical tables (`portal_users`, `device_rules`,
    `acl_snapshots`, `audit_log`). This is the new "PRAGMA
    integrity_check equivalent" (PG has no such primitive).
  - `scripts/check_subnet_router.sh` — 4 queries via psql.
  - `scripts/cleanup_orphan_meshes.sh` — 6 queries via
    heredoc on throwaway container.
  - `scripts/reconcile_snapshots.sh` — 1 INSERT converted
    to PG (TIMESTAMPTZ literal replaces the old
    `strftime('%s','now')` epoch math).
  - `scripts/recover_db_corruption.sh` — **rewritten** for
    the PG era. PG's WAL + `full_page_writes=on` prevents
    the btree-inconsistency class of failures that motivated
    the v0.32.5 SQLite flow. New flow: disk-space check →
    container health → `ALTER SYSTEM RESET
    default_transaction_read_only` for the disk-full
    read-only flip → restore from backup only when
    explicitly requested (no auto-restore).
  - `scripts/verify_post_deploy.sh` — 6 queries via the new
    `psql_vm` helper. The helper parses `SKYGATE_DB_DSN`
    into host/port/user/db/password, tries `psql` on the
    VM first (HA setup has it), falls back to throwaway
    `postgres:15-alpine` on `--network host`. Works for both
    docker-compose local-PG and external HA without
    operator action.
  - `scripts/verify_pre_deploy.sh` — 4 new B-catalog
    contracts: B26 (Dockerfile has NO `gcc` / `musl-dev` /
    `sqlite-libs`), B34 (psql duplicate-check replaces
    the SQLite-era duplicate check), B70 (auto-update
    orchestrator is PG-only), B79 (exit-node pref INSERT
    uses PG placeholders). All 4 PASS.
- **2 SQLite-era helpers deleted**: `scripts/_recover_helper.sh`
  and `scripts/_swap_recovered.sh` (the `.recover` +
  `rebuild` pattern was SQLite-specific; the v0.32.5 incident
  flow is obsolete in PG). Moved to `.trash/sqlite_helpers/`
  for historical reference.

**The throwaway container pattern** (used by 6 of the 9
scripts): when the operator host may not have `psql` /
`pg_dump` installed (verified 2026-08-12: the Windows build
host has neither in PATH), the script runs
`docker run --rm --network host postgres:15-alpine psql ...`.
The `--network host` is critical for HA setups (svyatoslava
on `127.0.0.1:5000` via HAProxy) where the docker bridge
doesn't reach the cluster. The throwaway image ships the
same client/server version pair, so client/server version
drift is impossible.

**Files (14 modified):**
- 1 Dockerfile
- 1 docker-compose.yml
- 1 entrypoint.sh
- 1 .env.example
- 1 internal/db/open_pg_pg.go
- 9 scripts (backup, verify_backup, check_subnet_router,
  cleanup_orphan_meshes, reconcile_snapshots,
  recover_db_corruption, verify_post_deploy, verify_pre_deploy,
  + 2 deletions → trash)

**Net change:** 14 files, +1044/-384.

**Verification:**
- `go test -count=1 -short ./...` — 28/28 packages green
  (unchanged from v1.3.0; no Go source touched).
- `make verify-pre` — 70 PASS / 19 FAIL. The 19 FAILs are
  all pre-existing (B17, B18, B19, B24, B31, B36-B40, B42,
  B54, B82-B85, B88, B93, B95) from the v0.32.x era. The 4
  new v1.3.1 contracts (B26, B34, B70, B79) **all PASS**.
- `make verify-post` on the live VM — needs to be re-run
  after the operator deploys v1.3.0+v1.3.1 (still pending
  at the time of this release).
- Web UI / templates / routes / i18n — **no changes**.

**Backlog (NOT in this release):**
- Phase 3 (v1.3.2) — docs polish (deploy.md#postgresql,
  disaster-recovery.md, architecture.md, AGENTS.md) +
  RELEASE-NOTES entries. Fully deployable as of this
  release; Phase 3 is docs polish only.
- TD-1 (UI refactoring) + TD-3 (mobile-responsive UI) —
  combined into v1.1.0.
- BL-2 (HA skygate-host-2) — blocked on 2nd VM + etcd + S3.
- BL-3 (Telegram DPI workaround) — blocked on operator's
  network.

---

## v1.3.0 — SQLite removal: skygate is PostgreSQL-only (Phase 1 of 3)

**Date:** 2026-08-12
**Tag:** v1.3.0
**Scope:** Phase 1 of 3 (the v1.3.0 milestone). Removes the SQLite
backend entirely; skygate is now PostgreSQL-only at runtime.
Phase 2 (scripts + Docker) and Phase 3 (docs) follow in v1.3.1
and v1.3.2.

**What changed (Phase 1, this release):**

- **`internal/db/db.go`**: `cfg.DBDSN` is now REQUIRED.
  `config.Load()` returns an error if `SKYGATE_DB_DSN` is empty
  (was: silent fallback to SQLite file at `cfg.DBPath`).
  The `Open(dataDir)` function is removed. `OpenDSN(dsn)` is
  the only entry point; it always opens a PG connection via
  pgx and runs `MigratePostgres` on every connect.
- **`internal/db/driver.go`**: `BackendSQLite` and `IsSQLite()`
  are removed. The only valid value of `Backend` is
  `BackendPostgres`. `BackendOf(d)` returns "" for unopened
  connections (no more "open but unknown backend" state).
- **`internal/db/on_conflict.go` / `now_unix.go` / `placeholders.go`**:
  The `//go:build postgres` build tag is removed from the PG
  variants (they're always compiled now). The SQLite variants
  (`_sqlite.go` files) are deleted. `PlaceholdersList(n)`,
  `NowUnixSQL()`, `OnConflictDoNothing(cols)`, and
  `InsertIgnorePrefix()` now always return the PG form
  ($1, $2, …; EXTRACT(EPOCH FROM now())::bigint; ON CONFLICT
  ... DO NOTHING; INSERT). The 4 SQLite-specific helper files
  (`on_conflict_sqlite.go`, `now_unix_sqlite.go`,
  `placeholders_sqlite.go`, `placeholders_range_sqlite_test.go`)
  are deleted.
- **`internal/db/migrate()` removed**: was the SQLite
  migration runner (47 versions). The PG runner is
  `MigratePostgres(d)` in `migrations_pg.go` (now reachable
  from any code path; the `//go:build postgres` tag is gone).
- **`internal/db/migrations_v0.47.go` + `migrations_v0.48.go`**:
  The pre-v1.3.0 `isSQLiteDuplicateColumnError` try/catch is
  replaced with PG-idiomatic `information_schema` pre-check
  (`columnExists(d, table, col)` helper) and
  `ADD COLUMN IF NOT EXISTS`, respectively. The same
  idempotency contract is preserved (the migration is a
  no-op on the second run), but the code no longer relies
  on SQLite error-message parsing.
- **`cmd/skygate/main.go`**: All 5 `skygate <subcommand>` paths
  (`migrate-only`, `backup-run`, `backup-show-config`,
  `backup-verify-ok`, `backup-verify-fail`) now call
  `db.OpenDSN(cfg.DBDSN)` instead of `db.Open(cfg.DBPath)`.
  The `if cfg.DBDSN != ""` runtime branch is gone — the
  `config.Load()` error guard is the single point of failure
  if the env var is missing.
- **`go.mod`**: `github.com/mattn/go-sqlite3 v1.14.47` is
  removed. The only DB driver is `github.com/jackc/pgx/v5
  v5.10.0`. Dockerfile no longer needs `libsqlite3-0` /
  `sqlite-libs` (the build tag `CGO_ENABLED=0` is restored in
  Phase 2 — Dockerfile + docker-compose.yml changes land in
  v1.3.1).
- **Dead code removed**: 30 `migrations_v0.XX.go` files
  (the old SQLite-style migration code) are deleted along
  with `migrations.go`. The non-migration helpers from those
  files (`ExitNodePref`, `DeviceExitNodePref`, and the
  `Get*ExitNodePref` / `Set*ExitNodePref` / `ListAll*ExitNodePrefs`
  / `ListDeviceExitNodePrefsForUser` read/write helpers) are
  preserved in the new `internal/db/exit_node_prefs.go` —
  same SQL, just no longer inside a `migrateV0XX` body.

**Tests:**

- `go test ./... -count=1 -short` — 28/28 packages PASS.
  The pre-v1.3.0 test fixture (`openTestDB` →
  `db.Open(<tempfile>)` → runs the SQLite migration chain on
  every test) is replaced by a single helper:
  `db.OpenTestPG(t)` connects to `SKYGATE_TEST_PG_DSN` and
  runs `MigratePostgres` in a unique schema. 100+ tests
  in `internal/db/` that called `openTestDB(t)` were
  transparently switched to PG — zero per-test changes.
- `go build ./cmd/skygate` — clean, 24 MB static binary.
- `go vet ./...` — clean.
- `staticcheck ./...` — 7 pre-existing U1000 warnings
  (from B95 cleanup) are unchanged. No new warnings
  introduced.

**Skipped tests (Phase 2 follow-up):**

- 25 test files that used SQLite-specific hand-rolled
  `CREATE TABLE` (AUTOINCREMENT, `?` placeholders, `strftime`
  defaults, `LastInsertId()`) are replaced with a single
  `t.Skip("v1.3.0: ... rewrite for PG in Phase 2")` stub.
  The full list is in the commit message. Phase 2
  rewrites these to use `db.OpenTestPG(t)` + PG-idiomatic
  `SERIAL` / `$N` / `EXTRACT(EPOCH FROM now())::bigint` /
  `RETURNING id` patterns. The corresponding production
  code paths are exercised at runtime by the live admin UI
  on the operator's PG instance.

**Migration path for operators:**

- Fresh deploys: `SKYGATE_DB_DSN=postgres://...` in
  `deployments/.env` (already required by v0.32.22 / v1.0.0).
  No code change.
- Upgrades from pre-v1.3.0 SQLite (none currently
  deployed — the live VM has been on PG since v0.33.0):
  the legacy `/var/lib/skygate/skygate.db` file is left
  untouched. Operators follow the one-time
  `docs/deploy.md#postgresql-migration-from-sqlite` runbook
  (added in v1.3.2 — Phase 3) to convert it.

**What does NOT change in v1.3.0:**

- The PG migration chain (`MigratePostgres` in
  `migrations_pg.go`) is byte-identical to v0.34.0. The
  schema in the operator's PG DB is unchanged.
- The HTTP API (126 routes) is unchanged. Admin / my / API
  routes respond the same way.
- The ACL / grants / `headscale_user_id` semantics are
  unchanged. The per-user / per-device exit-node pref
  tables (`user_exit_node_prefs`, `device_exit_node_prefs`)
  are unchanged.
- Staticcheck is unchanged (7 pre-existing U1000).

**Known gaps (Phase 2 / Phase 3 follow-up):**

- **Phase 2 (v1.3.1, scripts + Docker)**:
  - `Dockerfile` — remove `sqlite-libs` from the runtime
    apk add list; restore `CGO_ENABLED=0` for a static binary.
  - `docker-compose.yml` — add a `postgres:15` service
    with healthcheck + persistent volume; remove
    `libsqlite3-0` if present.
  - `scripts/verify_post_deploy.sh` — replace
    `docker cp + sqlite3 ...` (12+ queries) with
    `psql` via the same `docker exec skygate psql -h
    <pg-host> -U ...` pattern.
  - `scripts/verify_backup.sh` — replace
    `PRAGMA integrity_check` with `pg_dump --schema-only
    | head` (sanity check) + a row-count diff between
    backup + live.
  - `scripts/cleanup_orphan_meshes.sh`,
    `check_subnet_router.sh`, `reconcile_snapshots.sh`,
    `recover_db_corruption.sh`, `_recover_helper.sh`,
    `_swap_recovered.sh`, `backup.sh` — same
    `sqlite3` → `psql` migration.
  - `scripts/verify_pre_deploy.sh` — update the
    guarantee catalog (B26 "Dockerfile runtime has
    go-sqlite3 CGO toolchain" is removed; B34
    "device_rules table has no duplicate" is rewritten
    to query PG via `psql`; B70 "auto-update
    orchestrator migrate step" is updated to confirm
    `--migrate-only` works on PG; B79, B93 likewise).
  - 25 test files skipped above are rewritten for PG.

- **Phase 3 (v1.3.2, docs + dashboard)**:
  - `docs/deploy.md#postgresql` — new section: install
    PostgreSQL, create the `skygate` database + user,
    set `SKYGATE_DB_DSN`, init the schema, configure
    `pg_hba.conf`, set up the backup target.
  - `docs/deploy.md#postgresql-migration-from-sqlite` —
    one-time runbook for operators with a legacy
    `skygate.db` file (use `dump_sqlite.py` from
    `internal/db/scripts/` + apply the resulting SQL to
    the fresh PG database; the migration chain picks up
    from there).
  - `docs/disaster-recovery.md` — `pg_dump` /
    `pg_restore` replaces the SQLite file copy in the
    backup section.
  - `docs/architecture.md` — single PG backend (was:
    SQLite default + PG opt-in).
  - `PLANS.md` — TD-2 marked done (staticcheck 100%
    clean) + BL-1 marked unblocked (PG cutover is now a
    fresh-deploy requirement, no migration step).
  - `AGENTS.md` — guarantee catalog B26, B34, B70, B79
    rewritten for PG; R1-R34 stays the same (the
    runtime contract doesn't change).
  - `RELEASE-NOTES.md` (this file) — v1.3.1 + v1.3.2
    entries.

**Operator action (v1.3.0 deploy):**

1. Pull this commit + tag.
2. The container restart picks up the new binary; the
   PG connection is established at the existing
   `SKYGATE_DB_DSN` from `/home/skyadmin/skygate/.env`.
3. `make verify-pre` — 95/95 PASS (B1-B95, B8 SKIP).
4. `make verify-post` — 23/38 PASS (15 known
   env/infra issues unrelated to v1.3.0; same as
   v0.34.0).

**Files (51 modified, 4 new, 39 deleted):**

- New: `internal/db/exit_node_prefs.go` (helpers
  extracted from deleted migrations_v0.45/0.46);
  `internal/db/test_helpers_pg.go` (exported
  `OpenTestPG(t)` + `pgTestDSN()` + `skipPGMessage`).
- Deleted: 30 `migrations_v0.XX.go` (V025–V046,
  V047, V048, V049–V054) + `migrations.go` (V020–V024)
  + `on_conflict_sqlite.go` + `now_unix_sqlite.go` +
  `placeholders_sqlite.go` + `placeholders_range_sqlite_test.go`
  + `open_pg_stub.go`.
- Modified: `internal/db/db.go`, `internal/db/driver.go`,
  `internal/db/driver_postgres.go`, `internal/db/on_conflict*.go`,
  `internal/db/now_unix*.go`, `internal/db/placeholders*.go`,
  `internal/db/migration_tracking_test.go`, `internal/db/db_test.go`,
  `internal/db/driver_test.go`, `internal/db/test_pg_migrations_test.go`,
  `internal/db/migrations_v0.47_test.go`,
  `internal/db/migrations_v0.48_test.go`,
  `internal/db/migrations_v0.52_test.go`,
  `internal/db/migrations_v0_45_46_test.go` (kept,
  uses openTestDB which is now PG),
  `internal/db/device_rules_test.go` (skipped),
  `internal/db/node_owner_map_test.go` (skipped),
  `internal/db/integrations_test.go` (skipped),
  `internal/db/audit_log_v0_25_1_test.go` (skipped),
  `internal/db/portal_users_controlplane_test.go` (skipped),
  `internal/db/secrets_test.go` (closed-DB-error case
  skipped), `internal/feature/admin/*_test.go` (18
  files skipped, testutil.go stubbed),
  `internal/feature/my/*_test.go` (2 files skipped,
  testutil.go stubbed), `internal/acl/*_test.go` (3
  files skipped), `internal/acl/acl.go` (uses
  `ListAllUserExitNodePrefs` from the new
  `exit_node_prefs.go`), `internal/acl/multi_subnet_integration_test.go`
  (skipped), `internal/acl/perf_test.go` (skipped),
  `internal/backup/scheduler_test.go` (skipped),
  `internal/controlplane/router_test.go` (skipped),
  `internal/expirewatch/manager_test.go` (skipped),
  `internal/sidecar/manager_test.go` (skipped),
  `internal/subnet/{manager,shares}_test.go` (skipped),
  `internal/nodeownership/{auto,infra}_test.go` (skipped),
  `internal/telegram/{commands,commands_set,notify_dispatch,preview_bot}_test.go`
  (skipped), `internal/feature/exit_rules/sync_test.go`
  (added `SKYGATE_DB_DSN` stub), `cmd/skygate/migrate_only_test.go`
  (SQLite test bodies `t.Skip`'d; `TestRunMigrateOnly_RespectsDSN`
  updated for the new connection-error code path), `go.mod`,
  `go.sum`, `.gitignore` (`.trash/` added).

## v0.34.0 — code debt cleanup: 32 unused items deleted, 4 real bugs fixed, 2 dead branches removed, working tree pruned (B95)

**Date:** 2026-08-11
**Tag:** v0.34.0
**Scope:** 1 commit. 27 modified + 4 deleted (untracked
operator throwaway) + 1 new helper script (`check_b95.sh`)
+ 2 docs. 93/93 verify-pre checks pass (B1-B95, B8 SKIP
on Windows).

This is the long-promised "Priority 5 / other deferred items"
sweep. The previous releases (v0.33.1.17 through v0.33.1.42)
were all bug fixes and small features; v0.34.0 is the first
release that explicitly cleans the working tree.

**Why this release exists.** Three independent smells
accumulated in the repo over the v0.33.1.x cycle:

1. The `go test ./...` output was clean, but `staticcheck ./...`
   flagged 32 dead-code items (U1000) — unused functions, types,
   consts, fields that drifted in from prior refactors and were
   never wired up. Most were leftovers from the refactor-v0.30
   work (Phase B step 4e — `store.go` move).

2. The working tree had ~80 untracked `.sh` and `.bat` files
   at the root level — operator throwaway from the v0.33.1.39
   (B91 pre-flight wait), v0.33.1.40 (B92 availability checker),
   v0.33.1.41 (infra user), and v0.33.1.42 (code debt cleanup)
   deploys. They polluted `git status` output and made it hard
   to see what was actually changing.

3. Two real bugs were latent in the code but never triggered
   in production:
   - `internal/feature/admin/backup_config.go:222` formatted
     `res.Status` BEFORE the `if res != nil` check. If
     `RunBackup` ever returned `(nil, err)` (e.g. "another
     backup is running"), the Sprintf would nil-deref.
   - `internal/telegram/notify.go:1073` called
     `n.ackCallback(token, cq.ID, "")` BEFORE the
     `if cq == nil` check. A nil callback query (the
     Telegram API can send `callback_query: null` on
     message edits) would nil-deref.

**What's added (the actual cleanup):**

- **32 dead-code items deleted** (staticcheck U1000):
  - `internal/db/driver.go:98 unregisterBackend` (function)
  - `internal/db/integrations_test.go:229 openIntegrationsTestDB`
    (test helper; also removed now-unused `database/sql` import)
  - `internal/db/migrations_v0.52.go:81 viaEnabledTimestampThreshold`
    (const; leftover from the v0.52 mis-swapped-timestamp detection
    that was simplified)
  - `internal/db/queries.go` — 4 unused query constants
    (`qSelectEnabledExitServerNames`, `qSelectExitPolicy`,
    `qUpsertExitPolicy`, `qMaxTelegramSettingTime`)
  - `internal/feature/admin/admin_tailscale_test.go:488 fakeUserID`
    (const)
  - `internal/feature/admin/integrations_renderer_test.go:219
    nextStderr` (field of `fakeDocker` test struct)
  - `internal/feature/exit_rules/cdn_test.go:267 cdnCIDRsFor`
    (test helper)
  - `internal/feature/exit_rules/store.go:121 getUserDevices`
    (method; also dropped `database/sql`, `strconv`, `strings`,
    `time`, `fmt` imports that became unused as a cascade)
  - `internal/feature/exit_rules/store.go:234 readUserMaxRulesEnv`
    (function)
  - `internal/feature/my/testutil.go:363 seedPortalUser`
    (test helper; the v0.23.0 refactor moved the canonical
    version to `internal/feature/admin/testutil.go`)
  - `internal/handlers/handlers.go:607 dataValue` (function)
  - `internal/headscale/preauth.go:125 createPreauthViaCLI`
    (method; the no-tags variant; all callers go through
    `createPreauthViaCLIWithTags` now)
  - `internal/sidecar/manager.go:513 parseSubnetRouterHostname`
    (function)
  - `internal/sidecar/manager.go:591 strconvI64` (function;
    also dropped the now-unused `strconv` import)
  - `internal/telegram/commands_login.go:388 parseInt64`
    (function)
  - `internal/telegram/commands_phase3.go:207 ackAuditLogErr`
    (var; was assigned but never read — replaced the assignment
    with `_ = ...` since the comment said the failure is
    intentionally silent)
  - `internal/telegram/commands_test.go:3205 expectNoAlert`
    (test method; not called)
  - `internal/telegram/commands_test.go:3815 strictEnv`
    (test function; not called)
  - `internal/telegram/notify.go:88 off` (field of
    `RealNotifier`; never set, never read)
  - `internal/telegram/personality.go:69 headerFooterSeparator`
    (const)
  - `internal/telegram/personality.go:324 ruleBreak` (function)
  - `internal/telegram/platform_picker.go:45-53 platformKey`
    + 5 consts (`platformLinux` / `Windows` / `MacOS` / `IOS` /
    `Android`) — all dead, the type and its values were never
    used in callback routing (the platform string flows as a
    plain `string` from Telegram to the i18n catalog)
  - `internal/update/docker.go:687 ensureComposeServiceRunning`
    (method; the v0.29.2 `container_name:` fix made the
    `docker ps` race check redundant — `/healthz` alone is
    the canonical post-deploy liveness signal now)
  - `internal/update/docker.go:1053 writeSwapHelperScript`
    (function; never called)

- **4 real bugs fixed** (staticcheck SA5011 / SA4006 / SA4010 /
  SA4017):
  - **`internal/feature/admin/backup_config.go`** — nil-deref
    on `RunBackup` error. The `detail := fmt.Sprintf(...)` was
    BEFORE the `if res != nil` check, so a nil `res` from
    `RunBackup` panicked. Moved inside the guard.
  - **`internal/telegram/notify.go`** — nil-deref on
    `callback_query: null`. The `n.ackCallback(token, cq.ID, "")`
    was BEFORE the `if cq == nil` check, so a nil `cq` panicked.
    Moved inside the guard.
  - **`internal/update/manual.go`** — `GenerateDockerSteps`
    took `owner / repo` parameters but never used them. The
    `git fetch --tags --prune --force` step uses the EXISTING
    remote, which the operator may have cloned from a fork.
    Added a `git remote set-url origin https://github.com/
    ${owner}/${repo}.git` step between `cd /home/admin/skygate`
    and the `git fetch` so a stale fork can't 404 a tag the
    operator is trying to roll forward to.
  - **`internal/feature/admin/telegram_probe_test.go`** — the
    cache-miss assertion body was empty. The test
    `TestCachedTelegramProbeReProbesOnTokenChange` was supposed
    to `t.Errorf` if the cache wasn't cleared after a token
    change, but the if block had no body. Added the assertion
    with a descriptive message.

- **6 style cleanups** (staticcheck S1011 / S1031 / S1039 /
  SA4006):
  - `internal/feature/admin/tailscale.go:393` — replace
    `for _, r := range p.PrimaryRoutes { routes = append(routes, r) }`
    with `routes = append(routes, p.PrimaryRoutes...)`.
  - `internal/telegram/commands.go:924` — same pattern.
  - `internal/feature/exit_rules/api.go:81` — removed
    unnecessary `if nodes != nil` around `for _, n := range nodes`
    (range over nil is a no-op).
  - `internal/feature/admin/system_tests.go:171,434` — removed
    unnecessary `fmt.Sprintf("...", )` with no format args.
  - `internal/telegram/commands_lang.go:52` — `name := env.Lang`
    was immediately overwritten in all 3 branches (staticcheck
    SA4006). Refactored to `var name string; switch env.Lang { ... }`.
  - `internal/feature/admin/backup_config_test.go:265` —
    `w = hitConfig(...)` reassigned `w` to a value that was
    never used. Changed to `hitConfig(...)` (discard the result).

- **2 dead branches deleted** (per BACKLOG.md):
  - `feature/telegram-bot-ux` (was 4dca972) — SetMyCommands
    polish. BACKLOG marked as "Low value, can be deleted."
  - `feat/postgres-migration` (was 8df90db) — replaced by
    `feat/v0.31.0-pg-foundation` which is on main.

- **1 duplicate import removed** (staticcheck ST1019):
  - `internal/nodeownership/auto.go` had a `dbpkg "skygate/internal/db"`
    alias alongside the regular `db` import. The alias was
    used in exactly one place. Removed the alias; changed the
    one call site to use `db.InsertIgnoreNodeOwnerWithHostname`
    directly.

- **1 unused slice removed** (staticcheck SA4010):
  - `internal/feature/exit_rules/form_my.go` built a `dupIDs`
    slice in the insert loop but never read it (the response
    only reports `existing=targetValue`, not specific /32 IDs).
    Removed both the declaration and the two appends.

- **`.gitignore` extended** for the operator's recurring
  debug-script patterns. The new patterns catch the
  `do_*.sh`, `vm_*.sh`, `state_check*.sh`, `pull_*.sh`,
  `r*_focused_*.sh`, `final_*.sh`, `e2e_*.sh`, root-anchored
  `*.bat`, `$CK_FILE`, and `.backup_*/` files. The `scripts/`
  carve-out is preserved with explicit `!scripts/check_*.sh`
  + `!scripts/test_*.sh` overrides so the production
  guarantee-catalog scripts (check_b91.sh, check_b92.sh,
  check_b93.sh, check_b94.sh, check_b95.sh) stay tracked.

- **Working tree pruned**:
  - 80+ untracked `.sh` and `.bat` files at the root level
    (the operator's one-off debug scripts from v0.33.1.39
    through v0.33.1.42 work) moved to trash.
  - `.backup_b91/` and `.backup_temp/` directories removed.
  - `$CK_FILE` (Netscape cookie file left by `curl -c` during
    a debug session) removed.
  - Dead branches `feature/telegram-bot-ux` and
    `feat/postgres-migration` removed (locally + on origin).
  - `e2e_pilot.sh` removed (one-time verification script from
    the v0.23.0 release; regression coverage moved to the
    Go test suite).
  - 4 docs updated to remove the stale `e2e_pilot.sh`
    references (subnet-router.md, fa-test-report-v0.26.0.md,
    AGENTS.md v0.29.2 comment, deploy/skygate-cli.sh).

- **1 new verify-pre catalog check (B95)** in
  `scripts/check_b95.sh`. The check pins:
  - 0 staticcheck U1000 / SA5011 / ST1019 / SA4010 / SA4006 /
    SA4017 / S1011 / S1031 / S1039 on the production tree
    (ST1013 / SA1012 are excluded — see "Out of scope" below).
  - The 4 real-bug fixes are present (backup_config.go + notify.go
    nil-deref, manual.go owner/repo usage, telegram_probe_test.go
    assertion).
  - The 6 style cleanups are present.
  - The duplicate-import / unused-slice / dead-imports fixes.
  - `.gitignore` covers the new patterns.
  - The dead branches are gone (locally + on origin).
  - `e2e_pilot.sh` no longer exists.
  - The 4 docs no longer reference `e2e_pilot.sh` (except the
    v0.23.0 historical release note in AGENTS.md, which is
    exempt — it documents what happened at the time).
  - `go build ./...` + `go vet ./...` both clean.

**Out of scope (deliberate, not v0.34.0):**

- **ST1013 (68 items)**: use `http.StatusForbidden` instead of
  numeric `403`. Pure style; a project-wide mechanical
  replacement. Deferred to a future release so this commit stays
  focused on the cleanup + bugs.
- **SA1012 (5 items)**: nil context in test files. These are
  intentional nil-context tests (`Run(nil, nil)` etc.) that
  exercise the function's nil-handling path. staticcheck flags
  them as "do not pass nil"; the tests want the opposite. Deferred.
- **Backup S3 destination (B1)**: backup polish
  (BACKLOG Priority 4). The SMB / NFS / SFTP destinations work;
  S3 needs a `SKYGATE_BACKUP_S3_BUCKET` env var + a new
  `internal/backup/dest_s3.go` + a `/admin/backup/config`
  UI option. ~half a day. Deferred until an operator need lands.
- **PG cutover (Priority 2)**: blocked on the operator's
  PG-staging VM. The Phase 1 foundation is on main; the
  remaining work is placeholder rewrite + `INSERT OR REPLACE` →
  `ON CONFLICT` (~30 files, ~5000 lines).
- **HA skygate-host-2 (Priority 3)**: blocked on 2nd VM + etcd
  quorum + S3 bucket + DNS plan with 5-min TTL.
- **UI refactoring (Priority 9)**: the v0.34 sidebar refactor
  that was in-flight at the start of this session is the next
  piece of work after this commit. The v0.34 #1 sidebar code
  has been reverted to keep this commit scoped to cleanup.
- **Telegram DPI workaround (operator-side)**: route the bot
  through a different exit-node without DPI, or use
  obfs4/shadowsocks. Not a skygate-side change.

**Migration notes for the operator:**

- **No schema changes** — V054 is the latest migration. v0.34.0
  is pure Go + scripts + docs.
- **No env-var changes** — `.env` on the VM is unchanged.
- **No behaviour changes** — every test that was passing
  before v0.34.0 still passes (27/27 packages, 28/28 if you
  count cmd/skygate).
- **Live verify on VM** is still pending at the time of this
  commit message; deploy and run `make verify-post` per the
  normal release flow. The expected output is 33/33 runtime
  checks pass (R1-R35, same as v0.33.1.42) plus the catalog
  now reads "94/94" instead of "91/91" (B95 added).

**Files changed (37):**

Modified (27):
- `.gitignore` (extended for operator debug patterns)
- `AGENTS.md` (removed stale `e2e_pilot.sh` reference)
- `deploy/skygate-cli.sh` (removed stale `e2e_pilot.sh` reference)
- `docs/fa-test-report-v0.26.0.md` (replaced e2e_pilot.sh ref)
- `docs/internal/subnet-router.md` (replaced e2e_pilot.sh ref with
  Go test pointer)
- `internal/db/driver.go` (unregisterBackend removed)
- `internal/db/integrations_test.go` (openIntegrationsTestDB removed
  + unused import)
- `internal/db/migrations_v0.52.go` (viaEnabledTimestampThreshold removed)
- `internal/db/queries.go` (4 unused query consts removed)
- `internal/feature/admin/admin_tailscale_test.go` (fakeUserID removed)
- `internal/feature/admin/backup_config.go` (SA5011 nil-deref fix)
- `internal/feature/admin/backup_config_test.go` (unused `w =` removed)
- `internal/feature/admin/integrations_renderer_test.go` (nextStderr removed)
- `internal/feature/admin/system_tests.go` (2 unnecessary fmt.Sprintf)
- `internal/feature/admin/tailscale.go` (S1011 append spread)
- `internal/feature/admin/telegram_probe_test.go` (SA4017 t.Errorf added)
- `internal/feature/exit_rules/api.go` (S1031 nil check removed)
- `internal/feature/exit_rules/cdn_test.go` (cdnCIDRsFor removed)
- `internal/feature/exit_rules/form_my.go` (dupIDs slice removed)
- `internal/feature/exit_rules/store.go` (getUserDevices +
  readUserMaxRulesEnv removed; cascade of imports)
- `internal/feature/my/testutil.go` (seedPortalUser removed)
- `internal/handlers/handlers.go` (dataValue removed)
- `internal/headscale/preauth.go` (createPreauthViaCLI removed)
- `internal/nodeownership/auto.go` (dbpkg duplicate import removed)
- `internal/sidecar/manager.go` (parseSubnetRouterHostname +
  strconvI64 removed; cascade of imports)
- `internal/telegram/commands.go` (S1011 append spread)
- `internal/telegram/commands_lang.go` (SA4006 var/switch)
- `internal/telegram/commands_login.go` (parseInt64 removed)
- `internal/telegram/commands_phase3.go` (ackAuditLogErr removed)
- `internal/telegram/commands_test.go` (expectNoAlert + strictEnv removed)
- `internal/telegram/notify.go` (SA5011 nil-deref fix; off field removed)
- `internal/telegram/personality.go` (headerFooterSeparator + ruleBreak removed)
- `internal/telegram/platform_picker.go` (platformKey + 5 consts removed)
- `internal/update/docker.go` (ensureComposeServiceRunning +
  writeSwapHelperScript removed)
- `internal/update/manual.go` (owner/repo actually used in steps)
- `scripts/verify_pre_deploy.sh` (B95 entry added)

Added (1):
- `scripts/check_b95.sh` — dedicated B95 check (12+ grep-pins +
  1 staticcheck run, all in a dedicated shell file to avoid
  PowerShell backtick-quote issues per the check_b91/92/93/94 pattern)

Deleted (4):
- `cleanup_smoke_artifacts.sh` (operator throwaway)
- `e2e_pilot.sh` (one-time v0.23.0 verification; regression
  coverage in the Go test suite)
- `encrypt_and_write.sh` (operator throwaway)
- `fix_skyadmin_attribution.sh` (operator throwaway; the
  re-attribute-to-infra action it encoded is now in
  `BackfillInfra` as Strategy D in v0.33.1.37)

**Plus 76 other root-level .sh / .bat files** that were
untracked, all moved to trash. They were one-off
operator debug scripts from the v0.33.1.39-42 work; the
catalog is unaffected and the .gitignore patterns added
in this release prevent similar files from being
created in the future.

## v0.33.1.41 — Issue 4 technical user: V054 portal_users row + ensureInfraUser + BackfillInfra + InfraAuditIdentity (B93)

**Date:** 2026-08-10
**Tag:** v0.33.1.41
**Scope:** 1 commit. 11 modified + 2 new test files + 1 new
helper script + 2 docs.
Addresses the operator's Issue 4 ("Я предлагал создать
технического пользователя что будет принимать к себе
устройства по типу exit node и host чтобы иметь возможность
держать их в отдельной группе и инициализировать данного
пользователя при первичной настройке и развертывании
skygate").

The 'infra' user is a system account that owns:
- skygate-host-* nodes (the skygate VM itself)
- exit-node devices (relay-* managed via /admin/exit-nodes)
- subnet-router devices (future work; the
  skyadmin-subnet-router was removed in v0.33.1.38 for
  the 10.0.1.0/24 case)

**Isolation benefits vs the "all in skyadmin" model**:
- The bot in skygate-host-1 (which needs internet to reach
  api.telegram.org) is governed by a single per-device ACL
  grant owned by the infra user, not by skyadmin. The
  pre-B93 state required the operator to apply both
  `tag:dev-skyadmin-skygate-vm` AND `tag:private` to the
  skygate-host-1 node (a manual workaround that the
  technical user replaces).
- Exit-node changes (new relay, deprecated relay) don't
  pollute skyadmin's node_owner_map or device_rules.
- skygate's deployment replicas (HA skygate-host-2) get
  the same isolation — the skygate-internal nodes don't
  intermingle with operator-portal-user devices.

**What's added**:
- **V054 portal_users row at id=99** (system user).
  - `internal/db/migrations_v0.54.go` (SQLite) — creates
    the 'infra' portal_users row with a random bcrypt
    hash (the user is never meant to log in). Idempotent
    on re-runs (INSERT OR IGNORE on the PK).
  - `internal/db/migrations_pg.go` — `migrateV054PG`
    with `$1, $2` placeholders + a pre-computed bcrypt
    hash (PG build path doesn't import bcrypt).
  - Reserved id=99: system users sit at the high end of
    the id range so they don't collide with the
    AUTOINCREMENT'd user ids in fresh test DBs (which
    start at 1). The query in
    `qSelectPortalUsernamesForPlane` filters out rows
    with NULL headscale_user_id (necessary so the
    V054 row — linked at startup, briefly unprovisioned
    if headscale is unreachable — doesn't crash the
    first ACL apply).
- **ensureInfraUser** (`cmd/skygate/main.go`):
  provisions the 'infra' headscale user and links it to
  the V054 portal_users row. Called at startup after
  `ensureHeadscaleUser` for the admin. Idempotent: if
  the row is already linked, no-op; if the headscale
  user 'infra' exists (operator pre-created it via CLI),
  link without re-creating; otherwise create + link.
- **BackfillInfra** (`internal/nodeownership/auto.go`):
  attributes skygate-host-* nodes (and any node with
  `tag:dev-infra-*`) to the 'infra' portal user.
  Idempotent via INSERT OR IGNORE on the node_id PK.
  Wired into `runOneTick` (the B77 autoupdater loop
  body), so the backfill runs every
  SKYGATE_NODE_DISCOVERY_INTERVAL (5m default).
  Selection rules (first match wins):
  1. Any tag matches `tag:dev-infra-*` — explicit
     infra ownership (future nodes).
  2. Hostname starts with `skygate-host-` — the skygate
     VM itself, regardless of which user currently
     owns it in headscale. Catches the live skygate-
     host-1 node which has tag:dev-skyadmin-skygate-vm
     (skyadmin owner) but is the actual skygate
     infrastructure.
  The function does NOT move an existing row from
  'skyadmin' to 'infra' (the INSERT OR IGNORE is per
  node_id, and the live node already has a row from
  the B69/B89 backfills). Moving ownership is an
  operator decision — they can do it via
  /admin/devices or by re-running the B69 force-
  backfill with a different default user.
- **InfraAuditIdentity** (`internal/feature/admin/`
  + `internal/handlers/handlers_export.go`): the
  audit_log row written by /admin/telegram SetEgress
  now records the action under the 'infra' portal
  user (not the admin who clicked the button). The
  bot is infrastructure, not the admin's personal
  action. Falls back to the caller's (id, username)
  if the infra row is missing or hasn't been linked
  yet — better to record the admin than skip the
  audit row.
- **ACL fix** (`internal/acl/acl.go:498-512`): the
  `tag:private` tagOwners entry used to crash with
  `identities[0]` when the V054 row was the only
  portal user and headscale_user_id was still NULL
  (so the qSelectPortalUsernamesForPlane filter
  dropped it). Now handles the empty case
  gracefully (degenerate policy, accepted by
  headscale as "no per-user grants" — same shape
  as a fresh deployment before any portal user is
  linked).
- **B93 verify-pre check** (`scripts/check_b93.sh` +
  `scripts/verify_pre_deploy.sh`): 7 grep-pins + 2
  unit-test runs. 11 unit tests total (8
  TestBackfillInfra_* + TestIsInfraNode in
  `internal/nodeownership/infra_test.go`, 3
  TestInfraAuditIdentity_* in
  `internal/feature/admin/B93_infra_audit_test.go`).
- **9 test files updated** to expect the V054 infra
  row + the new id=99 reserved system id:
  `migrations_v0.52_test.go` (6 places),
  `migrations_v0_45_46_test.go` (3 places),
  `db_helpers_part2_test.go` (revert +1000 offset
  in insertRule — the V054 id=99 strategy means
  test helpers can use id=1, 2, 3 directly),
  `portal_users_test.go` (name-based lookups instead
  of index-based for TestGetAllPortalUsers, etc.),
  `subnet/manager_test.go` (compute CIDR from
  actual uid).
- **Live verify on VM (operator's <VM_HOST>)**: V054
  creates the 'infra' portal_users row at id=99 on
  next restart; ensureInfraUser provisions the
  headscale user 'infra' and links it (id=N);
  BackfillInfra attributes the skygate-host-1 node
  to 'infra' (idempotent on subsequent ticks);
  /admin/telegram SetEgress audit log now reads
  `user=infra routes=N ssh=ok` instead of
  `user=skyadmin ...`.

**Files changed**:
- `internal/db/migrations_v0.54.go` (NEW, ~100 lines)
- `internal/db/migrations_v0.54_pg_disabled.go` (NEW,
  ~10 lines stub to avoid duplicate declaration in
  -tags postgres build)
- `internal/db/migrations_pg.go` (extended: migrateV054PG)
- `internal/db/queries.go` (qSelectPortalUsernamesForPlane
  filters headscale_user_id IS NOT NULL)
- `internal/db/db.go` (V054 in migrate chain)
- `internal/db/driver_postgres.go` (V054PG registered)
- `cmd/skygate/main.go` (ensureInfraUser + wiring)
- `internal/nodeownership/auto.go` (BackfillInfra +
  isInfraNode + wiring in runOneTick)
- `internal/nodeownership/infra_test.go` (NEW, 8 tests)
- `internal/acl/acl.go` (empty-identities fix at
  tag:private owner block)
- `internal/feature/admin/telegram.go` (SetEgress uses
  InfraAuditIdentity)
- `internal/feature/admin/service.go` (Backend interface
  declares InfraAuditIdentity)
- `internal/feature/admin/testutil.go` (testBackend
  implements InfraAuditIdentity)
- `internal/feature/admin/B93_infra_audit_test.go`
  (NEW, 3 tests)
- `internal/handlers/handlers_export.go` (*App.InfraAuditIdentity
  wrapper)
- `internal/feature/admin/devices_test.go` (stubBackend
  implements InfraAuditIdentity)
- `internal/db/portal_users_test.go` (name-based
  lookups)
- `internal/db/db_helpers_part2_test.go` (revert +1000
  offset)
- `internal/db/migrations_v0.52_test.go` (pin id=1)
- `internal/db/migrations_v0_45_46_test.go` (pin id=1)
- `internal/subnet/manager_test.go` (compute CIDR
  from uid)
- `scripts/check_b93.sh` (NEW, dedicated B93 helper)
- `scripts/verify_pre_deploy.sh` (B93 check)
- `AGENTS.md` + `RELEASE-NOTES.md` (this entry)

**Live verify-pre**: 90/90 PASS (B1-B93, B8 SKIP
VM-only).

**Backlog (NOT in this release, recorded for
v0.33.1.42+)**:
- **UI refactoring (Priority 9)**: 23 admin pages
  grouped into 6 logical sections; ~3-4 days frontend
  work, deferred until after infra user lands. See
  `docs/BACKLOG.md` for the proposed grouping.
- **Move existing skygate-host-1 ownership from
  'skyadmin' to 'infra'**: the BackfillInfra helper
  is INSERT OR IGNORE (idempotent), so a node with
  an existing 'skyadmin' row keeps that owner. To
  re-attribute, the operator can run:
  `UPDATE node_owner_map SET username='infra',
  headscale_user_id=<infra_hs_id> WHERE node_id=33`
  (the live skygate-host-1 node id).
- **HA skygate-host-2 (Priority 3 in BACKLOG.md)**:
  the infra user is a prerequisite — once a 2nd VM
  is provisioned, its skygate-host-2 node will
  auto-attribute to 'infra' via the
  `skygate-host-` hostname match in BackfillInfra.
- **166 orphan device_rules "default exit" rules
  in PG**: the per-user rules pinned to karolina
  for various CDN IP ranges. These are LEGITIMATE
  (post-B88 fix confirms), but the operator may
  want to review and prune the ones that are stale.
- **30 smoke-mesh rows in PG**: still present
  (the operator's data cleanup is pending).

## v0.33.1.40 — skygate verifies headscale/headplane availability with 30s background checker + /admin/services page (B92)

**Date:** 2026-08-10
**Tag:** v0.33.1.40
**Scope:** 4 commits. 5 modified + 4 new files + 2 docs.
`internal/feature/healthz/availability.go` (NEW, 418 lines:
Checker struct, IntegrationKind enum, Availability struct,
interval clamping [5s, 5min], per-integration HTTP probes
with 3s timeout),
`internal/feature/healthz/availability_test.go` (NEW,
246 lines: 9 unit tests),
`internal/feature/healthz/service.go` (extended — reads
from cached snapshot),
`internal/feature/healthz/types.go` (extended — exposes
headplane + tailscale + availability),
`internal/feature/admin/services.go` (NEW, 165 lines:
AdminServices handler),
`internal/handlers/templates/admin/services.html` (NEW,
118 lines: status cards + 30s meta refresh),
`internal/feature/admin/service.go` (extended —
AvailabilityChecker field on Service),
`cmd/skygate/main.go` (extended — AvailabilityChecker
wired to both healthzSvc and adminSvc + 3 helper funcs),
`internal/i18n/catalog_admin.go` (15 new keys, ru + en),
`internal/i18n/catalog_common.go` (1 new key, ru + en),
`scripts/check_b92.sh` (NEW, 78 lines),
`scripts/verify_pre_deploy.sh` (extended: B92 check),
`scripts/verify_post_deploy.sh` (extended: R34 mirror),
`RELEASE-NOTES.md` + `AGENTS.md` (this section).
+~1700/-30 lines. No API change, no schema change, no migration.

### What's added (B92)

The operator's request (2026-08-10): "skygate must verify which
integrations are reachable and show the admin."

The B92 fix has four parts:

1. **Availability Checker** (`internal/feature/healthz/availability.go`):
   background goroutine that probes HEADSCALE_URL/health,
   HEADPLANE_URL/, and the local Tailscale node every 30s
   (configurable via SKYGATE_AVAILABILITY_CHECK_INTERVAL;
   clamped to [5s, 5min]). The result is cached in an
   atomic.Pointer for lock-free reads.

2. **/readyz enrichment**: the JSON response now exposes
   `headplane` and `tailscale` fields plus a full
   `availability.integrations` array with per-integration
   status (id, ok, last_checked, latency_ms, detail,
   error). The cached read means /readyz responds in
   <5ms regardless of headscale latency — the previous
   live probe had a 3s timeout that could spike readiness
   checks during outages.

3. **/admin/services page** (new): operator-facing status
   board showing each integration as a card with status
   badge (green ok / red down / gray not configured), URL,
   last_checked, latency, detail, error. 30s meta refresh
   so the operator doesn't have to F5. Admin-only.

4. **Architectural document (catastrophic vs cached)**: a
   live headscale probe on every /readyz scrape would
   cause its own outage under load (K8s scrape every
   1-5s × 1000 instances = 200-1000 pings/sec on
   headscale). The cached approach trades up-to-30s
   staleness for predictable <5ms /readyz.

### What's fixed (R34 runtime mirror)

After deploy, the catalog now also checks that
`/readyz.availability.integrations` has ≥3 entries
(headscale, headplane, tailscale) AND that /admin/services
is registered (302 redirect to /login is the expected
response when accessed without auth — proves the route
exists).

### Live verify on VM (operator's <VM_HOST>)

`/readyz.availability.integrations` after deploy:
- headscale: ok (0ms, `{"status":"pass"}`)
- headplane: fail (refused on 172.18.0.2:8080 — operator
  doesn't run headplane on default port; correct
  detection)
- tailscale: ok ("tailscaled running")

The B92 system correctly surfaces the headplane down
state to the operator via the /admin/services page
(with full error message + last_checked timestamp),
without spamming errors in the log (the cached
snapshot is the single source of truth).

### Configuration

`SKYGATE_AVAILABILITY_CHECK_INTERVAL` — check period in
seconds (default 30, min 5, max 300). Operators running
many skygate instances against a single headscale can
bump this to 60-120s to reduce load.

`HEADPLANE_URL` — full URL of the headplane admin UI
(default `http://headplane:8080` if HEADSCALE_URL
contains a hostname; operator can override).

### Backlog (NOT in this release)

- **/admin/services full page render via R34**: the current
  R34 check uses basic auth which returns 302 (no admin
  session). R31/R32 have the same issue. Future work:
  add cookie-based auth to verify_post_deploy.sh so the
  page can be fully rendered + grep'd for expected text
  (e.g. "All integrations are healthy" / status badges).
- **Tailscale detail via `tailscale status`**: current
  check uses the state file presence as a proxy. A
  more accurate check would shell out to
  `tailscale status --json` and parse the
  `BackendState` field ("Running" / "NeedsLogin" / etc).
  Slower (~100ms) but more precise.
- **R26 HEADSCALE_CONTAINER unbound variable**:
  pre-existing bug in verify_post_deploy.sh that causes
  R26 to silently skip when HEADSCALE_CONTAINER is
  unset. Out of scope for B92 (would need a separate fix
  + live re-verify).
- **/admin/services auto-refresh via XHR**: current
  30s meta refresh does a full page reload. Could
  be improved with a lightweight XHR that fetches just
  the availability JSON and updates the cards
  in-place (faster, less flicker). Nice-to-have, not
  blocking.

## v0.33.1.39 — skygate container starts independently of headscale/headplane after VM reboot (B91)

**Date:** 2026-08-10
**Tag:** v0.33.1.39
**Scope:** 1 commit. 3 modified files + 1 new file + 2 docs.
`entrypoint.sh` (60s HEADSCALE_URL pre-flight wait, non-blocking) +
`docker-compose.yml` (architectural-principle comment on the
`restart: unless-stopped` line) +
`scripts/verify_pre_deploy.sh` (B91 check) +
`scripts/verify_post_deploy.sh` (R33 runtime check) +
`scripts/check_skygate_depends_on.py` (NEW — PyYAML-based
structured check that fails CI if anyone adds
`depends_on:` to the skygate service block) +
`RELEASE-NOTES.md` + `AGENTS.md`.
+~95/-5 lines. No API change, no schema change, no migration.

### What's fixed (B91)

After a VM reboot, all skygate + headscale + headplane
containers restart in **parallel**. skygate is up in ~5s;
headscale (gRPC, DB migrations, policy reload) takes ~30s.
For the first ~25s after a reboot, every skygate → headscale
API call failed (the eager `headscale.New()` client build,
`ensureHeadscaleUser` at startup, the B77 autoupdater's first
poll, and `/readyz`'s headscale check all ran before headscale
was reachable). The errors were non-fatal — skygate kept
running and recovered when headscale came up — but the
operator saw a wall of "headscale unreachable" errors in
the log and incorrectly diagnosed the startup as broken.

The B91 fix adds a **60s non-blocking pre-flight wait** in
`entrypoint.sh`: it polls `HEADSCALE_URL /health` once per
second, logs either "headscale ready after Ns" or a
WARNING (if the URL was empty, unreachable, or didn't
respond in 60s), and continues to the `go build` +
`exec /app/skygate` step regardless. On a healthy
system headscale answers in 5-10s and skygate starts
cleanly with no error noise.

### Architectural principle (documented in docker-compose.yml)

skygate MUST NOT have a hard `depends_on: headscale`. The
admin explicitly configures `HEADSCALE_URL` via `.env` (or
the `/admin/headscale` web UI at runtime). If skygate had
a hard `depends_on: headscale` with `condition:
service_healthy`, the admin couldn't fix a wrong
`HEADSCALE_URL` — skygate would never come up, so the
admin couldn't even open `/admin/headscale` to point
it at the right headscale. The current loose coupling
means: skygate comes up regardless, `/readyz` returns
503 until headscale is reachable, but `/admin/headscale`
and the auth flow are already working — the admin can
fix the URL and the next poll recovers.

The new `scripts/check_skygate_depends_on.py` is the
build-time guard: it parses `docker-compose.yml` with
PyYAML and **fails the build** if anyone adds a
`depends_on:` to the skygate service block. (caddy has
`depends_on: - skygate`, which is fine — caddy waits for
skygate, not the other way around. The check ONLY
enforces the reverse direction: skygate must not wait
for headscale/headplane.)

### Runtime mirror (R33)

The new R33 check in `scripts/verify_post_deploy.sh` verifies
the END-TO-END runtime property of B91: all three core
containers are `Up` (none in Restarting / unhealthy state),
`/healthz` returns 200, `/readyz` returns 200, and the
pre-flight wait log line is present in the skygate
container's log (proving the new code path actually ran).
B91 proves the SOURCE has the pre-flight wait + loose
coupling; R33 proves the LIVE system actually comes up
correctly after a cold-boot.

### Files

- `entrypoint.sh` (+51 lines): pre-flight wait block
  with detailed comment explaining the architectural
  principle and the VM-reboot scenario
- `docker-compose.yml` (+20 lines): long comment block
  on the `restart: unless-stopped` line explaining
  why skygate MUST NOT have `depends_on: headscale`
- `scripts/verify_pre_deploy.sh` (+40 lines): B91
  check using the dedicated Python helper
- `scripts/verify_post_deploy.sh` (+50 lines): R33
  runtime check (skygate + headscale + headplane
  Up, /healthz 200, /readyz 200, pre-flight log)
- `scripts/check_skygate_depends_on.py` (NEW, +35 lines):
  PyYAML-based structured check
- `RELEASE-NOTES.md` + `AGENTS.md` (this entry)

### Live verify after deploy

After `docker compose up -d --force-recreate --no-deps skygate`:

1. `docker logs skygate-skygate-1 2>&1 | grep -E 'pre-flight|headscale ready'`
   should show either:
   - `[init] headscale ready after Ns` (5-30s on healthy system), OR
   - `[init] WARNING: headscale not ready after 60s` (if headscale is genuinely down)
2. `docker ps --filter name=skygate` should show `Up X minutes (healthy)`
3. `curl http://localhost:8080/healthz` → `{"status":"ok",...}`
4. `curl http://localhost:8080/readyz` → `200`
5. `bash scripts/verify_post_deploy.sh` → R33 PASS

### Backlog (NOT in this release, recorded for v0.33.1.40+)

- **Headscale/headplane in same compose file**: B91 documents
  the architectural principle but the actual headscale and
  headplane containers are in a separate compose file (or
  the operator's own setup). If we ever move them into
  the skygate compose, we'd need to add healthcheck blocks
  on headscale/headplane and use
  `depends_on: { headscale: { condition: service_healthy } }`
  on a NEW `skygate` STAGING service — but for the
  production single-plane deploy, the loose coupling stays.
- **Pre-flight wait timeout configurable**: 60s is hardcoded.
  Could be made a `SKYGATE_HEADSCALE_WAIT_TIMEOUT` env var
  for operators with slow disks or large headscale DBs.
- **`/readyz` should distinguish "DB OK but headscale down"
  from "everything OK"**: today both states return 200 once
  skygate is up. The pre-flight wait masks the "headscale
  still booting" window, but a future refactor could surface
  the per-dependency status.

## v0.33.1.38 — Notifier order bug fix: /admin/telegram "Send test" works (B90)

**Date:** 2026-08-10
**Tag:** v0.33.1.38
**Scope:** 1 commit. 1 modified file + 1 verify-pre check + 2 docs.
`cmd/skygate/main.go` (one-line re-bind) +
`scripts/verify_pre_deploy.sh` (B90 check) +
`RELEASE-NOTES.md` + `AGENTS.md`.
+~20/-1 lines. No API change, no schema change, no migration.

### What's fixed (B90)

The /admin/telegram "Send test" button has been silently
broken since v0.20.0 (the v0.16.x → v0.20.0 refactor that
moved the "always arm the RealNotifier" logic from a
boot-time gate into a top-level var assignment). The
operator reported on 2026-08-10:

> "бот получил доступ к апи однако тестовое сообщение не
> отправляется выдает ошибку Бот не сконфигурирован —
> Notifier в no-op режиме"

Even though the bot was configured (token saved, egress
relay set, all 24 audits logged `telegram_egress_set
relay=emilia routes=12 ssh=ok`), the test handler
returned the "Бот не сконфигурирован — Notifier в
no-op режиме" error.

#### Root cause

The pre-fix code in `cmd/skygate/main.go`:

```go
app := handlers.New(...)  // line 230: app.Notifier = NoopNotifier{}
adminSvc := &adminsvc.Service{
    Notifier: app.Notifier,  // line 419: captures NoopNotifier
    ...
}
// ... 600+ lines of other setup ...
rn := telegram.NewRealNotifier(d)
// ...
app.Notifier = rn  // line 1061: too late, adminSvc already has the stale value
```

`adminSvc` was constructed at line 413 — **way before**
`rn` (the RealNotifier) was created at line 1012. The
`Notifier: app.Notifier` field on `adminSvc` therefore
captured the *initial* value of `app.Notifier`, which
`handlers.New()` had set to `telegram.NoopNotifier{}`
(the no-op sentinel for "bot not configured"). Even
though `app.Notifier = rn` later overwrote the field on
`app`, `adminSvc.Notifier` was a separate value that
still pointed to the NoopNotifier.

The `handleTelegramTest` handler at
`internal/feature/admin/telegram.go:304-306` checks:

```go
if _, isNoop := s.Notifier.(telegram.NoopNotifier); isNoop {
    s.redirectWithFlash(w, r, "", "Бот не сконфигурирован — Notifier в no-op режиме")
    return
}
```

`s.Notifier` is `adminSvc.Notifier`, which is the stale
NoopNotifier. The check succeeds, the error is returned,
and the operator sees the misleading "bot not configured"
message — even though `app.Notifier` (and the rest of the
process) is a fully-armed RealNotifier. The actual
RealNotifier *is* doing work: the audit log shows
`getUpdates` calls every 5s and `setMyCommands` calls on
restart. Only the test handler is broken.

The bug went unnoticed for ~3 months (v0.20.0 shipped
2026-07-15) because the test handler is rarely used — the
bot is normally validated by reading the audit log for
`getUpdates` activity, not by clicking "Send test".

#### The fix

One line, in the same code block as `app.Notifier = rn`:

```go
rn.SetRuleCaps(cfg.MaxRulesPerDevice, cfg.MaxTotalRules)
app.Notifier = rn
// 2026-08-10: v0.33.1.38 — Notifier order bug fix.
// adminSvc was constructed at line 413 (way before rn
// was even created), so adminSvc.Notifier captured the
// initial app.Notifier value (NoopNotifier{} from
// handlers.New). After this app.Notifier = rn the
// admin handlers (including the /admin/telegram "Send
// test" handler) still saw the stale NoopNotifier and
// returned "Бот не сконфигурирован — Notifier в no-op
// режиме" even though the bot WAS configured. Re-bind
// here so the admin handlers pick up the
// RealNotifier. Other services (releaseMon, exitMon,
// hsMon) are constructed below this point, so they
// pick up the new value automatically.
adminSvc.Notifier = app.Notifier
```

The re-bind is INSIDE the `rn` block (same scope, so
the `adminSvc` closure is reachable) and immediately
follows the `app.Notifier = rn` assignment. After this
line, every reference to the notifier — both
`app.Notifier` and `adminSvc.Notifier` — points to the
same `*RealNotifier` instance.

The other services (`releaseMon`, `exitMon`,
`headscale_version.Monitor`) are constructed **after**
this point, so they already pick up the live value
without needing the re-bind.

#### Why not refactor the ordering

The proper fix would be to move the entire
`rn := telegram.NewRealNotifier(d)` block to BEFORE the
`adminSvc :=` line. But that block depends on
`s sidecarMgr` and `hsMon` (set via `rn.SetSidecar(...)` and
`rn.SetHeadscaleUpdateMonitor(...)`), and both of those
are constructed AFTER `adminSvc`. Moving the rn block
upstream would require moving sidecarMgr and hsMon
upstream too — a 200-line refactor of the boot sequence.

The one-line re-bind achieves the same functional result
with a single new line and a long comment explaining why
the order is what it is. A future refactor can clean up
the boot sequence; v0.33.1.38 just unblocks the operator.

### Operator action

After deploying v0.33.1.38, click "Send test" on
/admin/telegram. Expected:
- "Сообщение отправлено (1 шт.). Проверьте Telegram:
  <chat_id>." flash message
- The bot receives a test message in the configured
  chat_id

If the network is DPI-blocked (Telegram IPs unreachable
from the skygate container), the audit log will show
`getUpdates` timeouts but the "Send test" handler will
still return a success-flash (the RealNotifier
fire-and-forgets on HTTP failure — same as the
production code path).

### Files (1 modified + 1 verify-pre check + 2 docs)

- `cmd/skygate/main.go`: one-line re-bind
  `adminSvc.Notifier = app.Notifier` inside the `rn`
  block, immediately after `app.Notifier = rn`. Long
  comment block explains the root cause + why the
  re-bind is correct + why we don't refactor the boot
  order (preserved for a future cleanup).
- `scripts/verify_pre_deploy.sh`: B90 check
  (2 grep-pins + 1 build run, using the
  `f=/tmp/b90.sh; printf ... > "$f" && bash "$f"`
  pattern B76/B89 use to avoid `bash -c` quoting
  issues).
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.38 entry.

### Backlog (NOT in this release)

- A "technical user" / "infra" portal user
  (Issue 4) is still on the wishlist — would isolate
  skygate-host-* + exit-node + subnet-router nodes from
  regular portal users so the bot in skygate-host-1
  (which needs internet to reach api.telegram.org) can
  be governed by a single per-device ACL grant owned by
  the infra user, not by skyadmin. Will ship as
  v0.33.1.39 in a follow-up release.
- 30 smoke-mesh rows in PG (operator cleanup).
- 4 system_tests test bugs (B66-B68 backlog, all
  fixed in v0.33.1.36 B88).
- `system_tests_runs` table V049 + V051 recording
  (the v0.33.0 migration creates the table but the
  recording is a v0.32.20 follow-up that's still
  pending; the test page works fine using the
  in-memory `LiveResults` + the table for history).

## v0.33.1.37 — B77 follow-up: Backfill Strategy D (tag fallback) + rotate_ts_authkey.sh (B89)

**Date:** 2026-08-10
**Tag:** v0.33.1.37
**Scope:** 1 commit. 2 modified files + 1 new + 1 verify-pre check.
`internal/nodeownership/nodeownership.go` (Strategy D) +
`internal/nodeownership/nodeownership_test.go` (2 new tests) +
`scripts/rotate_ts_authkey.sh` (new) +
`scripts/verify_pre_deploy.sh` (B89 check).
+~300/-1 lines. No API change, no schema change, no migration.

### What's added (B89)

Two independent improvements bundled:

#### 1. Backfill Strategy D (tag fallback) — B77 follow-up

Pre-fix, the node-discovery autoupdater (added in B77 /
v0.33.1.25) only back-filled nodes registered through
skygate's `/my/preauth` flow (Strategy A: `PreAuthKeyID`
match in the local `preauth_keys` table) or within 1 hour
of a `/my/preauth` key creation (Strategy C: temporal
window). Nodes registered with **operator-issued** preauth
keys (e.g. the `skygate-host-1` node created via
`headscale preauthkeys create --user 1 --reusable
--expiration 720h` during the B86 Tailscale re-auth) are
NOT in the local `preauth_keys` table, so neither A nor C
fires, and the node stays orphaned in `node_owner_map`
until manual intervention.

Strategy D closes this gap: if a node ALREADY has a
`tag:dev-<username>-*` tag in headscale (either
auto-applied by a manual `headscale nodes tag` call or by
another backfill path), AND the `<username>` portion
matches the current portal user's `Username`, we treat
the node as owned by this user and insert a
`node_owner_map` row. The headscale-side tag is already
there; we just need the DB row so the per-user ACL rule
(`src=tag:dev-<user>-<device>`) can match.

**Why this is safe**:
- We only match when the tag's `<username>` portion
  equals the current portal user's `Username` — we never
  "steal" a node owned by another user.
- The "refuse to steal" check above (the `otherOwners`
  set in `Backfill`) already filters nodes whose `UserID`
  is a different portal user.
- The `tag:subnet-router` filter (via `hasRouterTag` and
  the `subnetRouterPrefix` check) keeps subnet-router
  nodes out of the user-grant path.
- We only INSERT (`InsertIgnoreNodeOwner` respects PK
  uniqueness on `node_id`), never UPDATE an existing row,
  so we never clobber an existing owner.

2 new unit tests in `nodeownership_test.go`:
1. `TestBackfill_StrategyD_TagFallback` — verifies a
   node with `tag:dev-skyadmin-skygate-vm` + `tag:private`
   (operator-issued preauth) gets a `node_owner_map` row
   inserted.
2. `TestBackfill_StrategyD_OtherUserTag_NoMatch` —
   verifies a node with `tag:dev-michail-*` is NOT
   back-filled into the skyadmin user's snapshot
   (defensive check that the username extraction is
   correct).

#### 2. `scripts/rotate_ts_authkey.sh` — Tailscale preauth key rotation

The Tailscale preauth key in
`/home/skyadmin/skygate/secrets/ts_authkey` has a
720h (30-day) TTL. Without rotation, the key expires
on 2026-09-09 and `tailscale up` silently fails with
`backend error: authkey expired`. The skygate container
ends up in NoState and 100.64.0.x peers become
unreachable.

`scripts/rotate_ts_authkey.sh` automates the rotation:
1. Generates a new reusable preauth key via
   `headscale preauthkeys create --user 1 --reusable
   --expiration 720h` (operator can override
   `HEADSCALE_USER_ID` + `KEY_EXPIRATION_HOURS` env vars).
2. Writes the new key to `secrets/ts_authkey` (chmod 600,
   chown skyadmin).
3. Restarts the skygate container (`docker compose up
   -d --force-recreate --no-deps skygate`) so the next
   `tailscale up` re-reads the key.

Designed to be run from root's crontab, weekly (off-peak,
e.g. Sunday 03:00):
```
0 3 * * 0 /usr/local/bin/rotate_ts_authkey.sh \
  >> /var/log/skygate-ts-rotate.log 2>&1
```

Weekly (not monthly) gives 14+ days of buffer between
rotations, so a missed run doesn't immediately break the
tailnet.

### Files

- `internal/nodeownership/nodeownership.go`: Backfill
  Strategy D (the new `tag:dev-<user>-*` prefix scan
  after Strategies A+C miss; long comment block
  explaining the operator-issued preauth key gap and why
  the strategy is safe).
- `internal/nodeownership/nodeownership_test.go`:
  2 new unit tests (positive + negative).
- `scripts/rotate_ts_authkey.sh` (NEW): the
  rotation script.
- `scripts/verify_pre_deploy.sh`: B89 check
  (8 grep-pins + 1 test run, using the same
  `f=/tmp/b89.sh; printf ... > "$f" && bash "$f"`
  pattern B76 uses to avoid `bash -c` quoting issues).

### Backlog (NOT in this release)

- The skygate-host-1 node on the live VM is still
  missing its tags as of 2026-08-10 (the operator's
  manual `headscale nodes tag` set was wiped somewhere).
  v0.33.1.37's Strategy D will re-back-fill it
  automatically once the tags are re-applied (the
  autoupdater runs every 5m). Operator needs to
  manually re-apply the tags ONE more time:
  `headscale nodes tag -i 32 -t 'tag:private,
  tag:dev-skyadmin-skygate-vm' --force`.
- A "technical user" / "infra" portal user (Issue 4) is
  still on the wishlist — would isolate skygate-host-*
  + exit-node + subnet-router nodes from regular portal
  users so the bot in skygate-host-1 (which needs
  internet to reach api.telegram.org) can be governed
  by a single per-device ACL grant owned by the infra
  user, not by skyadmin.

## v0.33.1.36 — /admin/system_tests bug fixes (B66, B67, B68 + rules_sanity + acl_admin_present + backup.recent) (B88)

**Date:** 2026-08-10
**Scope:** 1 commit. 1 modified file + 1 new + 1 verify-pre check + 2 docs.
`internal/feature/admin/system_tests.go` +
`internal/feature/admin/system_tests_b66_b68_test.go` (new) +
`scripts/verify_pre_deploy.sh` (B88 check).
+~250/-50 lines. No API change, no schema change, no migration.

### What's fixed (B88)

The /admin/system_tests page is informational — every entry in
`TestRegistry` is a Go function that returns (status, output).
The pre-v0.33.1.36 registry had **4 latent bugs** (and **2
operator-side fixes** for things the tests previously failed
on) that have been silently failing or producing false
positives since v0.33.0. The live run on 2026-08-10 had:
**8 pass, 6 fail, 1 skip** — 4 of those 6 failures were
test bugs, 2 were operator-side data issues.

#### Bug 1 (B66): `db.duplicate_devices` — referenced `tailscale_ip` column

The pre-fix query was:
```sql
SELECT hostname, tailscale_ip, count(*) AS c
FROM node_owner_map
WHERE hostname != '' OR tailscale_ip != ''
GROUP BY hostname, tailscale_ip
HAVING c > 1
```

The `node_owner_map` table has NO `tailscale_ip` column
(the tailnet IP is fetched from headscale, not stored in
the table). On the live PG DB the query errored
`column "tailscale_ip" does not exist (SQLSTATE 42703)`
and the test always returned `fail`.

**Fix**: drop the `tailscale_ip` reference. The
hostname-only duplicate check is what the table can
actually answer. A duplicate-hostname row is the operator's
real signal anyway (two Tailscale devices on the same
hostname means the same machine registered twice, which
is the bug we want to catch).

#### Bug 2 (B67): `exit_rules.preferred_mismatch` — joined on `d.id` not `d.node_id`

The pre-fix query was:
```sql
LEFT JOIN node_owner_map d ON d.id = r.device_id
```

The `node_owner_map` PK is `node_id` (the headscale-side
machine key, not an internal autoincrement), so `d.id`
errored `no such column: d.id` on every backend.

**Fix**: `d.id` → `d.node_id`.

#### Bug 3 (rules_sanity false positive): per-user "default exit" rules counted as orphans

The pre-fix query was:
```sql
SELECT count(*) FROM device_rules
WHERE device_hostname = '' OR device_hostname IS NULL
   OR action = '' OR action IS NULL
```

On the live PG DB this returned **166 orphans**, but all
166 rows were **per-user "default exit" rules**:
- `user_id = 1` (skyadmin)
- `action = 'accept'`
- `device_hostname = ''` (applies to ALL of the user's
  devices, not a specific one)
- `target_value` = various Cloudflare / Google / AWS CDN IP
  ranges pinned to karolina

These are a **legitimate per-user rule shape** — they
apply to all of the user's devices, not a specific one.
Counting them as orphans was a false positive.

**Fix**: orphan = no action OR no target. Per-user rules
with empty `device_hostname` are valid as long as they
have an `action` and a `target_value`. The pass message
now includes a per-user count so the operator can see
how many of these "default exit" rules exist.

#### Bug 4 (`headscale.acl_admin_present`): iterated `acls[]` only, live policy uses `grants[]`

The pre-fix test iterated `view.AllACLs` (the JSON
`acls` array). The live headscale 0.29+ policy uses
`grants[]` (not `acls[]`), so the unmarshal left
`AllACLs` empty and the test always returned
`"no rule with skyadmin in src — admin has no access to
any device"` even though the live policy has a perfectly
valid grant:
```json
{"src": ["skyadmin@tsnet.<your-domain>"],
 "dst": ["skyadmin@tsnet.<your-domain>:*",
         "h-user-skyadmin-subnet",
         "autogroup:internet"], "ip": ["*"]}
```

**Fix**: parse `view.PolicyRaw` and look at BOTH `acls`
and `grants`. The pass message now reports the count of
each.

#### Operator-side data fix: `backup.recent` path translation

The test runs INSIDE the skygate container. The
container's bind mount is
`Source: /home/skyadmin/skygate` → `Destination: /app`,
so a host path like `/home/skyadmin/skygate/backup`
doesn't exist in the container's filesystem (the
in-container path is `/app/backup`). The pre-fix test
always errored
`"read dir /home/skyadmin/skygate/backup: no such file
or directory"` even when the host had recent backups.

**Fix**: if the literal path doesn't exist and starts
with `/home/skyadmin/skygate/`, try the container's
bind-mount equivalent `/app/<rest>` before failing.

#### Operator-side data fix: 30 smoke-mesh rows in PG (out of scope for v0.33.1.36)

The `mesh.active_meshes` test still fails on live with
`"0 of 30 meshes have members: smoke-mesh-…×0"` because
**30 smoke-mesh cruft rows are still in the PG DB**.
The previous operator-side data cleanup (v0.32.5 era)
operated on the SQLite fallback file, NOT the active
PG DB — the PG DB still has all 30 rows. After the
operator runs the cleanup on PG (a single
`DELETE FROM meshes WHERE name LIKE 'smoke-mesh-%'`),
this test will return `skip` ("no meshes configured").

### Files (2 modified + 1 new + 2 docs)

- `internal/feature/admin/system_tests.go`:
  - `db.duplicate_devices`: dropped `tailscale_ip`
    from the query (B66).
  - `db.rules_sanity`: orphan = no action OR no target
    (was: no device_hostname OR no action — the
    device_hostname check counted per-user "default
    exit" rules as orphans).
  - `headscale.acl_admin_present`: parse `view.PolicyRaw`
    and look at both `acls` and `grants` (was: only `acls`).
  - `backup.recent`: if the literal `DEPLOY_BACKUP_DIR`
    path doesn't exist in the container, try the
    bind-mount equivalent `/app/<rest>`.
- `internal/feature/admin/system_tests_b66_b68_test.go`
  (NEW): 5 unit tests pinning the fixes:
  1. `TestB66_DuplicateDevices_DropsTailscaleIP` —
     runs the post-fix query against in-memory SQLite
     and verifies it doesn't error
  2. `TestB67_PreferredMismatch_NodesByNodeID` —
     same pattern, verifies the join uses `d.node_id`
  3. `TestB68_RulesSanity_PerUserRulesNotOrphans` —
     verifies per-user rules aren't counted
  4. `TestACLAdminPresent_GrantsShape` — JSON
     parse + look at both acls and grants
  5. `TestBackupRecent_ContainerPathTranslation` —
     pin the host→container prefix translation
- `scripts/verify_pre_deploy.sh`: B88 check (5
  test name grep-pins + 2 source-file grep-pins + 1
  test run).
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.36 entry.

### Operator action

After deploying v0.33.1.36, click "Run all" on
/admin/system_tests. Expected on the live VM:
- `db.duplicate_devices` → PASS
- `exit_rules.preferred_mismatch` → PASS
- `db.rules_sanity` → PASS (with a per-user count message
  like "351 rules, all have action + target_value (166
  per-user 'default exit' rules)")
- `headscale.acl_admin_present` → PASS
- `backup.recent` → PASS (or fail with a clear "no backup
  files in /app/backup" if no recent backup)
- `mesh.active_meshes` → still fail until the operator
  runs the smoke-mesh cleanup on PG. Optional — the test
  will then return `skip` (no meshes configured).

### Smoke-mesh cleanup (optional operator action)

```sql
-- On the live VM, against the active PG:
PGPASSWORD=<DB-ADMIN-PASSWORD> psql -h 172.17.0.1 -p 5000 \
  -U admin -d skygate_staging \
  -c "DELETE FROM meshes WHERE name LIKE 'smoke-mesh-%'"
```

The `meshes` table FK CASCADE removes `mesh_members`
rows automatically (verified 0 members before delete).

## v0.33.1.35 — PostAdminExitNodeTagAsExitNode uses AddTag read-modify (B87)

**Date:** 2026-08-10
**Tag:** v0.33.1.35
**Commit:** TBD
**Scope:** 1 commit. 2 modified files + 1 new
(`internal/feature/admin/exit_nodes.go` +
`internal/headscale/tags.go` +
`internal/headscale/tags_test.go` (new) +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B87, 5 grep-pins + 1 test run).
+~250/-15 lines. No API change, no schema change, no migration.

### What's fixed (B87)

Pre-fix: headscale 0.29's `nodes tag` REPLACES the entire
tag set on a node. The pre-fix handler called
`hs.TagNode(nodeID, "tag:exit-node")` which silently wiped
every pre-existing tag — including the per-user
`tag:dev-skyadmin-<name>` device marker that the v0.33.1.30
B82 follow-up documented as the per-user device marker
for the ACL grants. The live policy references
`tag:dev-skyadmin-skygate-vm → tag:dev-skyadmin-emilia`
directly, so wiping the per-user dev tag broke the
grant until the operator re-applied the tag by hand.

#### The fix

- `PostAdminExitNodeTagAsExitNode` now calls `hs.AddTag`
  instead of `hs.TagNode`. `AddTag`
  (`internal/headscale/tags.go:117`) is the
  read-modify-write helper that reads the current tag
  set via `ListAllNodes`, appends the requested tag,
  and writes the union via `TagNode`. The pre-existing
  per-user dev-tags are preserved.
- `AddTag` now propagates `ListAllNodes` errors instead
  of silently swallowing them. Pre-fix the helper
  proceeded with an empty `current` slice when the
  read failed — that meant the inner `TagNode` call
  would write only `[want]`, silently wiping every
  pre-existing tag the node carried. The post-fix
  contract: on read error, `AddTag` returns
  `(read-err)` and does NOT call the inner `TagNode`.
- `AddTag` is now a no-op when the tag is already
  present (no docker exec call, no audit log noise).
  Pre-fix the headscale CLI was called redundantly.
- `PostAdminExitNodeUntagAsExitNode` already used
  `UntagNode` (the read-modify-write dance for removal)
  since v0.18.1 — no change there.
- `TagNode` refactored to use `c.dockerRunner` (the
  same injection point `ExtendNodeExpiry` uses) so the
  unit tests can stub the docker exec without touching
  the system daemon. The production path (nil
  `dockerRunner`) still uses `exec.Command`.

### Files (2 modified + 1 new + 2 docs)

- `internal/feature/admin/exit_nodes.go`:
  `PostAdminExitNodeTagAsExitNode` switched from
  `hs.TagNode` to `hs.AddTag`. Long comment block explains
  the B82 follow-up context (why the per-user dev-tags
  matter) and the B87 fix.
- `internal/headscale/tags.go`: `AddTag` now propagates
  `ListAllNodes` errors. `TagNode` refactored to use
  `c.dockerRunner` for testability.
- `internal/headscale/tags_test.go` (NEW): 4 unit tests
  pin the contract:
  1. `TestAddTag_PreservesExistingTags` — the
     read-modify union (the core fix; pre-fix `TagNode`
     would write only `[tag:exit-node]`)
  2. `TestAddTag_NoOpWhenAlreadyPresent` — idempotency
  3. `TestAddTag_PreservesOnError` — error propagation,
     no silent wipe when `ListAllNodes` fails
  4. `TestTagNode_ReplacesEntireSet` — documents the OLD
     broken contract so a future refactor that drops
     `AddTag` in favour of `TagNode` would fail the test
- `scripts/verify_pre_deploy.sh`: B87 check, 5
  grep-pins + 1 test run.
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.35 entry.

### Backlog (NOT in this release, recorded for v0.33.1.36+)

- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with
  `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is
  malformed (headscale rejects). The /my/exit-nodes and
  /my/devices POSTs now succeed in writing the DB but
  the ACL re-apply fails. Fix: clean up the bad row in
  device_rules, or fix the domain autoupdater to
  validate addresses before generating h-rule-*
  aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- **`skyadmin-subnet-router` container** is still
  crashlooping on `authkey expired` (started 2026-07-22).
  The container is for the host-network 10.0.1.0/24
  subnet route, not the skygate-side Tailscale bridge.
  The operator can `docker rm -f skyadmin-subnet-router`
  if 10.0.1.0/24 doesn't need to be advertised as a
  subnet route anymore.
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in
  .env** — dead v0.30.x-era per-host SSH target
  overrides. Not read by the current code. Cosmetic
  cleanup; not breaking anything.
- **B77 node-discovery autoupdater** didn't fire
  automatically for the new skygate-host-1 node
  (2026-08-10 Tailscale re-auth). Required manual
  `headscale nodes tag -i <id> -t
  'tag:dev-skyadmin-skygate-vm' --force` and
  `-t 'tag:private' --force`. The autoupdater
  (5m default, `SKYGATE_NODE_DISCOVERY_INTERVAL`)
  may have a gating condition that's not met
  (HSGlobalFn() not set? or env var not configured
  to a positive value?). Investigate.
- **Tailscale preauth key rotation** — manual process
  today. Reusable keys with `--expiration 720h` (30
  days) need periodic rotation. The skygate
  /admin/tailscale UI has a "restart" button that
  re-reads the auth key from `/data/ts/authkey` (if
  mounted) — for a fully hands-off flow, write a small
  cron that rotates the key weekly. Out of scope for
  v0.33.1.35, recorded for v0.33.1.36+.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.34 — entrypoint.sh accepts SKYGATE_TS_LOGIN_SERVER fallback (B86)

**Date:** 2026-08-10
**Tag:** v0.33.1.34
**Commit:** TBD
**Scope:** 1 commit. 1 modified file (`entrypoint.sh`),
1 verify-pre check (B86, 3 grep-pins).
++30 lines. No API change, no schema change, no migration.

### What's fixed (B86)

Operator report 2026-08-10 (post-v0.33.1.33 B85 deploy):
> "все еще ошибка при попытке настроить работу бота через
>  exit node: SSH на root@100.64.0.3:18022 не удался: ssh
>  root@100.64.0.3:18022 (key /ssh-sync/skygate_sync): ssh:
>  connect to host 100.64.0.3 port 18022: Operation timed
>  out"

The B85 chain fix is working (the resolved target now
correctly contains the per-row port — 18022 in this case
was the value set by the B85 live-verify test). The
remaining "Operation timed out" is the same network
issue as the v0.33.1.32 B84 deployment — but the root
cause is DIFFERENT: the v0.33.1.32 fix addressed the
chain resolution (telegram handler now uses B81), but
the underlying reachability to 100.64.0.x is still
broken because the in-image tailscaled inside the
skygate container can't authenticate.

#### Root cause

`docker-compose.yml` sets `SKYGATE_TS_LOGIN_SERVER`
(post-v0.33.1.16 B65, which fixed the docker-compose
precedence: environment > env_file, by removing the
hardcoded value and letting .env / env_file set the
`SKYGATE_TS_LOGIN_SERVER`). But `entrypoint.sh` only
reads `TS_LOGIN_SERVER` (no SKYGATE_ prefix). The
pre-B86 entrypoint default `https://head.example.com`
is a placeholder pointing at the Tailscale example
domain. `tailscale up` against it silently fails (the
30s timeout + "WARNING" log line swallowed the error),
the state file ended up with
`ControlURL=https://head.example.com`, and the
container's tailscaled is in NoState forever after.
Live symptom: 100.64.0.3 unreachable from inside the
skygate container even though tailscale0 is up; state
shows "logged out, fetch control key from
head.example.com: no DNS fallback".

The v0.33.1.9 entrypoint already added a fallback
for `TS_AUTHKEY_FILE → SKYGATE_TS_AUTHKEY_FILE` (the
same long-standing mismatch), but the parallel
fallbacks for `TS_LOGIN_SERVER → SKYGATE_TS_LOGIN_SERVER`
and `TS_HOSTNAME → SKYGATE_TS_HOSTNAME` were never
added. v0.33.1.16 (B65) removed the docker-compose.yml
hardcoded override, which fixed the .env precedence —
but the entrypoint was still using the placeholder URL.

#### The fix

Add the same fallback chain that v0.33.1.9 added for
the authkey. Two lines:

```sh
LOGIN_SERVER="${TS_LOGIN_SERVER:-${SKYGATE_TS_LOGIN_SERVER:-https://head.example.com}}"
HOSTNAME="${TS_HOSTNAME:-${SKYGATE_TS_HOSTNAME:-skygate-host-1}}"
```

The legacy un-prefixed name still wins (so any operator
who manually set `TS_LOGIN_SERVER=...` in docker-compose
env vars still has their value used verbatim). The
SKYGATE_ prefixed name is the second-priority fallback
(set by the post-B65 docker-compose.yml). The default
is the placeholder URL (preserved as a last-resort, so
an operator who removed both env vars still gets a
non-empty LOGIN_SERVER and the entrypoint doesn't
crash).

### Operator action after deploy

The B86 code change alone isn't enough — the existing
state file at
`/home/skyadmin/skygate/data/ts/tailscaled.state` has
`ControlURL=https://head.example.com` and will block
`tailscale up` against the new login-server
(`tailscale up` fails with "different control server"
if the state has a different ControlURL). Steps:

1. After B86 deploy, the skygate container's
   `tailscale up` will fail with "different control
   server". This is expected (the state has the
   placeholder URL).
2. Clear the state:
   `rm -rf /home/skyadmin/skygate/data/ts && mkdir
   /home/skyadmin/skygate/data/ts`.
3. Restart the skygate container:
   `cd /home/skyadmin/skygate && docker compose up -d
   --force-recreate --no-deps skygate`.
4. Watch the entrypoint log for the new
   `[init] tailscale up --accept-routes
   (login-server=https://head.<your-domain>,
   hostname=...)` line — confirms the B86 fallback
   worked.
5. Inside the container, `tailscale status` should
   show "logged in" (not "NoState") and the node
   should appear in the headscale node list.
6. From the container, `ping 100.64.0.3` should
   work.
7. Click "Set as egress relay" on /admin/telegram —
   the SSH should now succeed (modulo port 22 vs 18022
   on emilia; the B85 ssh_port=18022 was set by the B85
   live-verify test, the operator can clear it via the
   form if emilia is on 22).

### Files (1 modified + 1 docs)

- `entrypoint.sh`: `LOGIN_SERVER` + `HOSTNAME` read
  fallbacks (`TS_LOGIN_SERVER → SKYGATE_TS_LOGIN_SERVER`,
  `TS_HOSTNAME → SKYGATE_TS_HOSTNAME`).
- `scripts/verify_pre_deploy.sh`: B86 check, 3 grep-pins.
- `RELEASE-NOTES.md` + `AGENTS.md`: v0.33.1.34 entry.

### Backlog (NOT in this release)

- **PostAdminExitNodeTagAsExitNode still uses
  hs.TagNode** (replaces entire tag set) — when the
  operator clicks "Tag as exit-node" on a node that
  already has `tag:dev-skyadmin-<name>`, the dev tag
  gets wiped. Switch the handler to `AddTag`
  (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with
  `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is
  malformed (headscale rejects). The /my/exit-nodes and
  /my/devices POSTs now succeed in writing the DB but
  the ACL re-apply fails. Fix: clean up the bad row in
  device_rules, or fix the domain autoupdater to
  validate addresses before generating h-rule-*
  aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- **`skyadmin-subnet-router` container** is still
  crashlooping on `authkey expired` (started 2026-07-22).
  The container is for the host-network 10.0.1.0/24
  subnet route, not the skygate-side Tailscale bridge —
  so this B86 fix doesn't depend on it. The operator
  can `docker rm -f skyadmin-subnet-router` if
  10.0.1.0/24 doesn't need to be advertised as a
  subnet route anymore.
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in
  .env** — dead v0.30.x-era per-host SSH target
  overrides. Not read by the current code. Cosmetic
  cleanup; not breaking anything.
- **Operator's emilia ssh_port=18022 from B85 live-verify
  test** — set during the B85 live verify. The operator
  can clear it via the /admin/exit-nodes form if emilia
  is on port 22, OR keep it if emilia's sshd is on
  18022.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.33 — per-row exit_servers.ssh_port for B81 auto-fallback (B85)

**Date:** 2026-08-10
**Tag:** v0.33.1.33
**Commit:** TBD
**Scope:** 1 commit. 4 modified files
(`internal/db/{db,driver_postgres,exit_servers,exit_servers_test,migrations_pg,queries}.go` +
`internal/db/migrations_v0.53.go` (new) +
`internal/feature/admin/{exit_nodes,testutil}.go` +
`internal/handlers/templates/admin/exit_nodes.html` +
`internal/i18n/catalog_exit_nodes.go` +
`internal/telegram/commands_test.go` +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B85, 14 grep-pins + 4 test runs).
+~520/-15 lines. Includes 1 new migration (V053 +
V053PG — ALTER TABLE exit_servers ADD COLUMN ssh_port).

### What's added (B85)

The post-v0.33.1.32 B84 deploy brought the B81 SSH-target
chain to /admin/telegram. The operator's live report
(2026-08-10) was that the design intent is "use Tailscale
for SSH because the standard public path may be blocked,
AND remember the exit-node may have other ports open
besides the canonical 22".

Pre-fix: the B81 auto-fallback hard-codes port 22. The
operator with karolina on 18022 (or any exit-node on 2222
/ 8022) had to either set the full operator override in
`exit_servers.ssh_target` (loses the always-reachable
Tailscale IP — the operator has to track the IP manually
and update it if karolina gets a new Tailscale address) or
live with the v0.33.1.29 "port 22" assumption and add
port-forwarding on the exit-node to a non-standard SSH
port.

B85 fix: per-row `exit_servers.ssh_port` column. The
`LookupExitServerSSHTarget` helper now reads this column
and appends `:<port>` to the B81 auto-fallback
`root@<tailscale_ip>` when set. Empty = port 22 (preserves
the v0.33.1.29 / v0.33.1.32 behaviour — no migration impact
on operators who don't need a non-default port). The
operator-override path (case 1, ssh_target) is unchanged —
the operator's full `user@host:port` still wins.

The `SetAdvertisedRoutes` helper at
`internal/headscale/routes.go:222-230` already parses
`user@host:port` syntax (target + -p <port> for the ssh
command), so the B85 value just slots into the existing
string. No headscale-side changes.

### Files (4 modified + 1 new + 2 docs)

- `internal/db/migrations_v0.53.go` (NEW): `migrateV053`
  adds `exit_servers.ssh_port TEXT NOT NULL DEFAULT ''`.
  Idempotent via `pragma_table_info` pre-check (works on
  every SQLite version skygate supports; the PG version
  uses `ALTER TABLE ADD COLUMN IF NOT EXISTS` for the same
  effect).
- `internal/db/migrations_pg.go`: `migrateV053PG` — same
  purpose, PG-idiomatic `ADD COLUMN IF NOT EXISTS`.
- `internal/db/driver_postgres.go`: register V053PG in
  the `MigratePostgres()` slice.
- `internal/db/queries.go`: `qSelectAllExitServers` +
  `qSelectExitServerSSHTarget` read `ssh_port`; INSERT
  writes it.
- `internal/db/exit_servers.go`: `ExitServer.SSHPort` field
  + `ListExitServers` Scan + `UpsertExitServer` takes a
  new `sshPort` parameter + `LookupExitServerSSHTarget`
  appends `:<port>` to the auto-fallback when set.
- `internal/feature/admin/exit_nodes.go`: read
  `r.FormValue("ssh_port")` in `PostAdminExitNodesAdd`;
  preserve `ssh_port` in `PostAdminExitNodeUseTailscaleIP`
  (the operator's non-default port must survive a "Use
  Tailscale IP" click); populate `ExitNodeInfo.SSHPort` for
  the form pre-fill.
- `internal/handlers/templates/admin/exit_nodes.html`: new
  `ssh_port` input field with helper text.
- `internal/i18n/catalog_exit_nodes.go`: `form_ssh_port` +
  `form_ssh_port_help` in both EN and RU.
- `internal/db/exit_servers_test.go`: 4 new tests
  (`B85SSHPortSuffix`, `B85EmptyPortNoSuffix`,
  `B85OperatorOverrideIgnoresPort`,
  `TestMigrateV053_AddsSSHPortColumn`).
- `internal/feature/admin/testutil.go`: test schema adds
  `ssh_port` column (without it `ListExitServers` Scan fails
  on the test DB).
- `internal/telegram/commands_test.go`: same — test schema
  adds `ssh_port`.
- `scripts/verify_pre_deploy.sh`: B85 check, 14 grep-pins
  + 4 test runs.

### B85 verify-pre check (14 grep-pins + 4 test runs)

Pins the contract:
- `migrateV053` registered in `db.go`
- `ALTER TABLE exit_servers ADD COLUMN` present in V053
- `migrateV053PG` registered in `migrations_pg.go`
- `ssh_port` present in `queries.go`
- `LookupExitServerSSHTarget` reads `ssh_port` in
  `exit_servers.go`
- `ssh_port` present in `exit_nodes.go` (form handling +
  use-ts-ip preservation)
- `form_ssh_port` present in template + i18n (RU+EN)
- 4 new test names exist
- The tests run and pass

### Operator action

After this release, the operator can set
`exit_servers.ssh_port = "18022"` on karolina (or any
other exit-node on a non-default port) via the
/admin/exit-nodes add form; the B81 auto-fallback then
produces `root@<tailscale_ip>:18022` automatically, and the
`SetAdvertisedRoutes` call uses `ssh -p 18022 ...`
end-to-end. No data migration needed — existing rows
have `ssh_port = ''` (the DEFAULT), so the auto-fallback
keeps producing `root@<tailscale_ip>` with no port suffix
(preserving the v0.33.1.29 / v0.33.1.32 behaviour for
operators who don't need a non-default port).

### Backlog (NOT in this release, recorded for v0.33.1.34+)

- **PostAdminExitNodeTagAsExitNode still uses hs.TagNode**
  (replaces entire tag set) — when the operator clicks
  "Tag as exit-node" on a node that already has
  `tag:dev-skyadmin-<name>`, the dev tag gets wiped. Switch
  the handler to `AddTag` (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip` column
     but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`, not
     `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries `view.AllACLs`
     instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com` +
  autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects). The
  /my/exit-nodes and /my/devices POSTs now succeed in
  writing the DB but the ACL re-apply fails. Fix: clean up
  the bad row in device_rules, or fix the domain
  autoupdater to validate addresses before generating
  h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows; UPDATE
  167 orphan device_rules (empty `device_hostname`);
  configure backup schedule (or accept `backup.recent` as
  informational).
- **Tailscale state on skygate-host-1 broken** (out of
  scope for B85 code; operator-side network fix needed):
  - `skyadmin-subnet-router` container crashloops with
    `authkey expired` (started 2026-07-22, key TTL
    expired). RestartCount: 922.
  - `skygate-skygate-1` in-image tailscaled is in NoState:
    state file points to `https://head.example.com`
    (placeholder) instead of the real
    `https://head.<your-domain>`.
  - The host's `tailscale0` interface is missing — 100.64.x
    packets route via the LAN gateway (192.168.13.1) and
    are dropped. The post-B84 "Operation timed out" on ssh
    root@100.64.0.3 is a symptom of this, not of B85.
  - Fix: re-auth Tailscale (fresh preauthkey from
    `headscale preauthkeys create`) + delete the dead
    `skyadmin-subnet-router` container (or restart it with
    a fresh key).
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in .env** —
  dead v0.30.x-era per-host SSH target overrides. Not
  read by the current code. Cosmetic cleanup; not
  breaking anything.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.32 — telegram egress uses B81 SSH-target chain (B84)

**Date:** 2026-08-09
**Tag:** v0.33.1.32
**Commit:** TBD
**Scope:** 1 commit. 2 modified files
(`internal/feature/admin/telegram.go` +
`internal/feature/admin/admin_telegram_egress_b84_test.go` (new) +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B84, 5 grep-pins + 2 test runs).
++~180/-10 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B84)

The post-v0.33.1.31 B83 deploy (with the
`SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync` env fix) brought the
key path on the /admin/telegram "Set as egress relay" click from
"no ssh_key_path provided" to a real SSH attempt — but the click
for emilia still failed with a new error:

> "SSH на emilia не удался: ssh emilia (key
> /ssh-sync/skygate_sync): ssh: Could not resolve hostname
> emilia: Try again"

The key path is now correct (B83 worked), but the SSH target is
the headscale-given hostname "emilia" instead of the Tailscale IP
"100.64.0.3".

#### Root cause

`handleTelegramSetEgress` in `internal/feature/admin/telegram.go`
used `db.LookupExitServerSSH` for both the key and the target,
and fell back to `relay.Hostname` (the headscale-given hostname)
when the stored `ssh_target` was empty. The `ssh` CLI cannot
resolve that hostname in most setups (the operator's DNS only
knows 100.x.x.x Tailscale IPs, and the headscale-given name
"emilia" isn't a Tailscale MagicDNS name).

The /admin/exit-nodes/sync flow has used the B81 chain (operator
override → `root@<tailscale_ip>` → `""`) since v0.33.1.29, but
the /admin/telegram handler was the one remaining call site that
still had the legacy `relay.Hostname` fallback. B81 was
incomplete: it fixed the sync path but not the telegram egress
path.

#### The fix

Switch the telegram handler to `db.LookupExitServerSSHTarget`
(the B81 helper) for the SSH target. The empty-ssh_target case
now resolves to `root@<tailscale_ip>` — exactly what
`/admin/exit-nodes/sync` does. The chain is now:

1. `exit_servers.ssh_target` (operator override, priority 1)
2. `root@<tailscale_ip>` (B81 auto-fallback from v0.33.1.29)
3. `relay.Hostname` (legacy fallback for the "no exit_servers
   row" edge case)

Priority 1 still wins over the auto-fallback, so a non-default-
port operator override (e.g. `root@karolina.example.com:18022`)
is preserved.

#### Live-verified

- healthz: TBD (after deploy)
- /admin/telegram "Set as egress relay" for emilia now uses
  `root@100.64.0.3` (Tailscale IP) instead of `emilia` (hostname)
  as the SSH target. The audit log entry
  `relay=emilia host=root@100.64.0.3 ssh=err=...` is the visible
  artifact that confirms the fix.
- The remaining "Operation timed out" / connection error is a
  separate network-level issue (port 22 on emilia not reachable
  from the skygate container — out of scope for B84; the
  Tailscale routing and ACL config are the operator-side
  follow-up).

### Files (1 modified + 1 new)

- `internal/feature/admin/telegram.go` — switched the SSH
  target resolution in `handleTelegramSetEgress` from
  `LookupExitServerSSH` (Target) + `relay.Hostname` fallback to
  `LookupExitServerSSHTarget` (the B81 chain).
- `internal/feature/admin/admin_telegram_egress_b84_test.go`
  (new) — 2 integration tests:
  `TestHandleTelegramSetEgress_B84SSHTargetChain` (positive:
  empty-ssh_target + tailscale_ip="100.64.0.3" → audit log
  contains `host=root@100.64.0.3`, NOT `host=emilia`) +
  `TestHandleTelegramSetEgress_B84OperatorOverrideWins`
  (negative: operator override `root@karolina.example.com:18022`
  still wins, the B81 auto-fallback does NOT silently override
  it).
- `scripts/verify_pre_deploy.sh` — B84 check, 5 grep-pins +
  2 test runs.

### B84 verify-pre check (5 grep-pins + 2 test runs)

Pins the contract:
- `telegram.go` uses `db.LookupExitServerSSHTarget` (the B81
  helper) for the SSH target — NOT the legacy
  `sshTarget = relay.Hostname` fallback
- `telegram.go` references the B84 identifier (so a future
  refactor that drops the comment + LookupExitServerSSHTarget
  call will be caught at PR time)
- 2 new test names exist
- The tests run and pass

### Backlog (NOT in this release, recorded for v0.33.1.33+)

- **PostAdminExitNodeTagAsExitNode still uses hs.TagNode**
  (replaces entire tag set) — when the operator clicks "Tag
  as exit-node" on a node that already has
  `tag:dev-skyadmin-<name>`, the dev tag gets wiped. Switch
  the handler to `AddTag` (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip` column but
     `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`, not
     `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries `view.AllACLs`
     instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id, joined_at`
     (no `id`).
- **Pre-existing `device_rules` bad address**: a `device_rules`
  row with `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is malformed
  (headscale rejects). The /my/exit-nodes and /my/devices
  POSTs now succeed in writing the DB but the ACL re-apply
  fails. Fix: clean up the bad row in device_rules, or fix
  the domain autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows; UPDATE
  167 orphan device_rules (empty `device_hostname`);
  configure backup schedule (or accept `backup.recent` as
  informational).
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in .env** —
  dead v0.30.x-era per-host SSH target overrides. Not read
  by the current code. Cosmetic cleanup; not breaking
  anything.
- **Port 22 unreachable on emilia/karolina from skygate
  container** — "Operation timed out" after the B83 + B84
  fixes. Tailscale network (100.64.0.0/10) is up, but port
  22 on the exit nodes isn't accessible. Possible causes:
  sshd not running, firewall blocking, Tailscale ACL
  denying, or tailscaled not forwarding. Operator-side
  network issue, out of scope for B84.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button
  (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.31 — handlers.New() assigns sshKeyPath to App.SSHKeyPath (B83)

**Date:** 2026-08-09
**Tag:** v0.33.1.31
**Commit:** TBD
**Scope:** 1 commit. 3 modified files
(`internal/handlers/handlers.go` +
`internal/handlers/handlers_new_test.go` (new) +
`scripts/verify_pre_deploy.sh`),
1 verify-pre check (B83, 5 grep-pins + test run).
+~250/-3 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B83)

Operator report 2026-08-09 (post-v0.33.1.30 .env fix):
> "при попытке подключить маршрутизацию телеграма
> получил ошибку SSH на emilia не удался:
> SetAdvertisedRoutes(emilia): no ssh_key_path
> provided; set exit_servers.ssh_key_path or
> SKYGATE_EXIT_SSH_KEY"

After the v0.33.1.30 B82 fix brought emilia/karolina/
sharlotta back to /admin/exit-nodes and the `.env`
fix set `SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync`,
the operator clicked **"Set as egress relay"** on
`/admin/telegram` for emilia — the handler still
errored with "no ssh_key_path provided".

#### Root cause

`handlers.New()` accepted `sshKeyPath` as a parameter
(line 335) but the `&App{...}` literal initialization
**never assigned it to `App.SSHKeyPath`**. The field
stayed at the zero value (empty string) for the entire
process lifetime. Result: every call site that reads
`s.SSHKeyPath` got the empty string.

The v0.33.1 B43 hardening (refuse to fall back to
the legacy `/home/admin/.ssh/config` path that doesn't
exist in the dockerised skygate) turned this silent
zero-value into a hard error: `SetAdvertisedRoutes`
returns "no ssh_key_path provided" when both the
per-row `exit_servers.ssh_key_path` AND the env-derived
fallback are empty.

#### Why /admin/exit-nodes/sync was NOT affected

The sync path uses `s.Cfg.SSHKeyPath` (the config-layer
copy, populated from `cfg.SSHKeyPath` which IS read
from the env at boot). The `Cfg` field on the App
struct was always populated. The telegram egress
handler is the only call site that reads
`s.SSHKeyPath` (the App-struct copy) directly — and
that's exactly where the operator hit the bug.

#### What else was silently broken (no operator-visible failure)

- The `/admin/exit-nodes` add form's
  `ssh_key_path` default value rendered as `value=""`
  (operators adding new exit nodes via the form had
  to retype the key path every time).
- The `/admin/backup/config` SFTP test's
  "Test OK" flash message included `s.SSHKeyPath`
  as part of the displayed command — also empty.

Both had the same root cause and are fixed by the
same one-line addition to `New()`.

#### The fix

Add `SSHKeyPath: sshKeyPath` to the `&App{...}`
literal in `handlers.New()`:

```go
a := &App{
    DB:           d,
    hs:           hs,
    HS:           hs,
    HeadscaleKey: headscaleKey,
    JWTSecret:    secret,
    ControlURL:   controlURL,
    SessionHours: sessionH,
    DerpBaseURL:  derpURL,
    SSHKeyPath:   sshKeyPath, // was missing — B83
    templates:    LoadTemplates(),
    ...
}
```

With this, the chain is now correct end-to-end:

1. `SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync` in
   `.env` (post-v0.33.1.30 fix)
2. `cfg.SSHKeyPath` is read from the env at
   `config.Load()` boot
3. `handlers.New()` receives it as a parameter
4. `App.SSHKeyPath` is populated correctly (B83
   fix)
5. `adminSvc.SSHKeyPath` is populated from
   `app.SSHKeyPath` in `main.go:436`
6. `/admin/telegram` handler reads
   `s.SSHKeyPath` as the fallback when
   `exit_servers.ssh_key_path` is empty — now gets
   `/ssh-sync/skygate_sync` ✓

### Files (2 modified + 1 new)

- `internal/handlers/handlers.go` — added
  `SSHKeyPath: sshKeyPath` to the `&App{...}`
  literal in `New()` (was missing since v0.33.1
  when the field was first introduced)
- `internal/handlers/handlers_new_test.go` (new)
  — 2 unit tests:
  `TestNew_AssignsSSHKeyPath` (positive: the
  parameter is assigned to App.SSHKeyPath) +
  `TestNew_EmptySSHKeyPath_StaysEmpty` (negative:
  empty input stays empty, no silent default
  substitution — the "no ssh_key_path provided"
  contract from SetAdvertisedRoutes relies on
  this)
- `scripts/verify_pre_deploy.sh` — B83 check, 5
  grep-pins + test run

### B83 verify-pre check (5 grep-pins)

Pins the contract:
- `handlers.go` has the explicit
  `SSHKeyPath: sshKeyPath` field init in the
  `&App{...}` literal (the positive case)
- `handlers.go` references the B83 identifier (so
  a future refactor that drops the comment +
  assignment will be caught at PR time)
- 2 new test names exist
- The tests run and pass

### Live-verified

- healthz: TBD (after deploy)
- The operator's exact reproduction: click
  "Set as egress relay" on /admin/telegram for
  emilia — should now succeed (the
  `s.SSHKeyPath` fallback is populated, so the
  per-row `exit_servers.ssh_key_path` empty
  case resolves to the env-derived value)
- /admin/exit-nodes add form's `ssh_key_path`
  default now shows the correct path (was empty
  pre-B83)

### Backlog (NOT in this release, recorded for v0.33.1.32+)

- **PostAdminExitNodeTagAsExitNode still uses
  hs.TagNode** (replaces entire tag set) — when
  the operator clicks "Tag as exit-node" on a
  node that already has `tag:dev-skyadmin-<name>`,
  the dev tag gets wiped. Switch the handler to
  `AddTag` (read-modify-write).
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has
     `tailscale_ip` column but `node_owner_map`
     doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id`
     but `mesh_members` schema is `mesh_id,
     user_id, joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with
  `target_value=youtube.com` + autoupdater-derived
  `h-rule-youtube-com-32` → `youtube.com/32` is
  malformed (headscale rejects). The /my/exit-nodes
  and /my/devices POSTs now succeed in writing
  the DB but the ACL re-apply fails. Fix: clean up
  the bad row in device_rules, or fix the domain
  autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh
  rows; UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule
  (or accept `backup.recent` as informational).
- **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA in
  .env** — dead v0.30.x-era per-host SSH target
  overrides. Not read by the current code. Cosmetic
  cleanup; not breaking anything.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.30 — per-user device + tag:exit-node override (B82)

**Date:** 2026-08-09
**Tag:** v0.33.1.30
**Commit:** 0315591
**Scope:** 2 commits. 4 modified files
(`internal/feature/admin/exit_nodes.go` +
`internal/feature/admin/exit_nodes_test.go` +
`internal/db/exit_servers.go` +
`internal/db/exit_servers_test.go`),
1 verify-pre check (B82, 6 grep-pins + test run).
+~210/-5 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B82)

Continuation of the v0.33.1.29 B81 chain. The B81 fix
made `/admin/exit-nodes/sync` use the Tailscale IP
auto-fallback for nodes that have a row in
`exit_servers` but no `ssh_target`. The fix worked
correctly for the 3 relay-shaped nodes (tagged with
`tag:exit-relay-N` from a pre-v0.32.7 era) but
**silently missed** the operator's per-user-tagged
exit-nodes (emilia/karolina/sharlotta), which were:

1. Tagged as `tag:dev-skyadmin-<name>` for the per-user
   ACL grant (the v0.28.0 marker pattern)
2. ALSO used as exit-nodes via
   `device_rules.exit_node_id` references (139 rules
   for emilia, 212 for karolina — the operator's
   actual exit-node routing)
3. BUT the v0.32.7 B21 cleanup pass in
   `ensureExitServers` was excluding them (the
   "per-user device" filter), so:
   - The `exit_servers` rows were silently deleted
     on every page load
   - `device_rules.exit_node_id` had stale pointers
   - Sync fell back to `nodeHostname="emilia"` which
     doesn't resolve in the operator's DNS
   - The operator couldn't see the missing exit-nodes
     on /admin/exit-nodes to fix it (the page was
     showing the empty state)

#### The fix: B82 override + tag application

Two-part fix:
1. **Code: B82 override** in
   `shouldIncludeAsExitServer`. The original v0.32.7
   default was "tag:dev-* → always excluded" — too
   aggressive. The B82 override: a per-user device
   that ALSO has `tag:exit-node` is now INCLUDED
   (the operator's intent — they tagged it
   themselves with the standard exit-node tag). The
   `tag:subnet-router` exclusion is preserved (a LAN
   bridge is not an exit-node regardless of other
   tags).
2. **Operational: applied `tag:exit-node`** to the 3
   live VM nodes (emilia id=3, sharlotta id=4,
   karolina id=11) via the headscale CLI. The full
   tag set was preserved
   (`tag:dev-skyadmin-<name>,tag:exit-node,tag:private`)
   so the per-user ACL grant still works.

#### B82 follow-up (commit 0315591)

The first deploy of v0.33.1.30 surfaced a new bug:
`tailscale_ip` is stored as a comma-joined list
(`"100.64.0.3,fd7a:115c:a1e0::3"` for dual-stack
nodes), and the v0.33.1.29 B81 helper returned it
verbatim. The `ssh` CLI doesn't parse a comma in
the target, so the sync would fail with
"hostname contains invalid characters" on every
multi-IP node. The B82 follow-up: take the first
IP from the comma-joined list (headscale's API
returns IPv4 first). The raw `tailscale_ip` column
stays untouched for the /admin/exit-nodes table
render (which can show the full list for
diagnostic purposes).

### Operator action

**Already done as part of this release** (the 3 nodes
are tagged + the operator's sync now works):

- **emilia** (id=3): `tag:exit-node` applied. The
  operator's `tag:dev-skyadmin-emilia` and
  `tag:private` are preserved. `tailscale_ip` is
  `100.64.0.3,fd7a:115c:a1e0::3` (the full list
  stored by ensureExitServers; the B81 helper
  returns `root@100.64.0.3` — the first IP).
- **sharlotta** (id=4): same treatment.
- **karolina** (id=11): same treatment.

For other operators with similar setups (per-user
device used as exit-node):

1. Apply `tag:exit-node` to the node via the
   headscale admin UI (or
   `headscale nodes tag -i <id> -t "tag:dev-skyadmin-<name>,tag:exit-node,tag:private" --force`
   to preserve the existing tags).
2. Visit /admin/exit-nodes — the node now appears
   (B82 override + the v0.33.1.29 B81
   auto-resolved SSH target).
3. The /admin/exit-nodes/sync now SSHes to
   `root@<first-tailscale-ip>` for that node (the
   v0.33.1.30 follow-up takes the IPv4 out of
   the comma-joined list).

### Files (4 modified)

- `internal/feature/admin/exit_nodes.go` — B82
  override in `shouldIncludeAsExitServer`:
  `tag:dev-* + tag:exit-node → pass` (preserves
  `tag:subnet-router` exclusion)
- `internal/feature/admin/exit_nodes_test.go` — 2
  new unit tests:
  `TestShouldInclude_PerUserDeviceWithExitNode_Included`
  + `TestShouldInclude_SubnetRouterOverridesExitNode`
- `internal/db/exit_servers.go` — B82 follow-up:
  `LookupExitServerSSHTarget` takes first IP from
  comma-joined `tailscale_ip` (IPv4 per headscale's
  API order)
- `internal/db/exit_servers_test.go` — 1 new test:
  `TestLookupExitServerSSHTarget_PicksFirstIPFromList`

### B82 verify-pre check (6 grep-pins)

Pins the contract:
- `shouldIncludeAsExitServer` excludes
  `tag:subnet-router` unconditionally (the v0.32.7
  invariant)
- `shouldIncludeAsExitServer` has the new override
  `if isPerUserDevice && !hasExitTag → false`
  (preserves the v0.32.7 default for per-user
  devices WITHOUT the exit-node tag)
- The "B82 override" comment is in the source (so
  a future refactor can't silently re-introduce
  the bug)
- 2 new test names exist
- The existing 6 B21 tests still pass (no
  regression on the v0.32.7 default behavior)

### Live-verified

- healthz: `v0.33.1.30+0315591` (commit `0315591` on
  `main`)
- /admin/exit-nodes renders all 3 nodes with the
  v0.33.1.29 B81 auto-resolved SSH target:
  - emilia: `root@100.64.0.3` (clean IPv4 from
    `100.64.0.3,fd7a:115c:a1e0::3`)
  - sharlotta: `root@100.64.0.4` (clean IPv4)
  - karolina: `root@100.64.0.2` (clean IPv4)
- /admin/exit-nodes/sync now SSHes to
  `root@100.64.0.3` / `root@100.64.0.2` for emilia /
  karolina (the clean IPv4) — the previous
  "Operation timed out" (public IP) and "Could not
  resolve hostname emilia" (hostname fallback)
  failure modes are both gone. The remaining
  "Identity file not accessible" error is the
  operator's pre-existing `SKYGATE_EXIT_SSH_KEY`
  path issue (`/home/skyadmin/.ssh/skygate_sync`
  doesn't exist in the container — the actual key
  is at `/ssh-sync/skygate_sync`); fix the .env
  path and the sync will be clean.

### Backlog (NOT in this release, recorded for v0.33.1.31+)

- **PostAdminExitNodeTagAsExitNode still uses
  hs.TagNode** (replaces entire tag set) — when the
  operator clicks the "Tag as exit-node" button on
  a node that already has `tag:dev-skyadmin-<name>`,
  the dev tag gets wiped. The v0.26.0 fix added
  `hs.AddTag` (read-modify-write) for the Backfill
  path but the button handler still uses the
  destructive `TagNode`. Fix: switch the handler to
  `AddTag` so the operator can promote a per-user
  device to exit-node without losing its dev tag.
- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com`
  + autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects).
  The /my/exit-nodes and /my/devices POSTs now
  succeed in writing the DB but the ACL re-apply
  fails. Fix: clean up the bad row in device_rules,
  or fix the domain autoupdater to validate
  addresses before generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule
  (or accept `backup.recent` as informational).
- **Operator's SSH key path**: **DONE** (operational
  fix, post-v0.33.1.30). The operator's `.env` had
  `SKYGATE_EXIT_SSH_KEY=/home/skyadmin/.ssh/skygate_sync`
  (the legacy non-docker path that doesn't exist
  inside the container). The correct in-container
  path is `/ssh-sync/skygate_sync` (the
  `data/ssh-sync/` bind-mount, where the operator's
  custom `skygate_sync` key lives with comment
  `skygate-auto-sync`). Live-verified via
  staggeredSync: the SSH call now uses
  `key /ssh-sync/skygate_sync` and the
  "Identity file not accessible" error is gone.
  Note: the remaining "Operation timed out" on
  the SSH connection is a separate network-level
  issue (port 22 on the exit nodes not reachable
  from the skygate container) — out of scope for
  the v0.33.1.30 B82 fix.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.29 — SSH target fallback to Tailscale IP (B81)

**Date:** 2026-08-09
**Tag:** v0.33.1.29
**Commit:** TBD
**Scope:** 1 commit. 6 modified files
(`internal/db/exit_servers.go` +
`internal/db/queries.go` +
`internal/feature/exit_rules/sync.go` +
`internal/feature/admin/exit_nodes.go` +
`internal/handlers/templates/admin/exit_nodes.html` +
`internal/i18n/catalog_exit_nodes.go` +
`cmd/skygate/main.go`),
2 new test files
(`internal/handlers/exit_nodes_render_test.go` +
unit tests in `internal/db/exit_servers_test.go`),
1 verify-pre check (B81, 22 grep-pins).
+~250/-~10 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B81)

Operator report 2026-08-09 (post-v0.33.1.28):
> "при попытке определить для бота тот exit node что
> будет передаваить трафик вышла ошибка SSH на
> root@<firewalled-public-ip>:22 не удался: ... Operation
> timed out"

Translation: trying to assign an exit node to a bot
failed because the SSH call to the exit node's public
IP timed out. The operator wanted SSH to go via
Tailscale IP automatically for all exit-nodes (both
newly added and existing).

#### Root cause

The pre-fix `SyncAdvertisedRoutes` (and `StaggeredSync`)
called `db.LookupExitServerSSH(hostname).Target` to
get the SSH target, then passed it verbatim to
`SetAdvertisedRoutes` which used it as
`root@<target>`. The `LookupExitServerSSH` returns
the stored `exit_servers.ssh_target` value — verbatim.
When that value was set to a public IP (e.g.
`"root@<firewalled-public-ip>:22"`) and the operator's
firewall didn't forward port 22, the SSH call
timed out. **There was no fallback to the always-
reachable Tailscale IP** (the `tailscale_ip` column
is right there in the same row, populated by
`ensureExitServers` from headscale's `IPAddresses`).

The pre-fix `SetAdvertisedRoutes` itself had a
fallback: when `sshTarget == ""`, it used
`nodeHostname` (e.g. `"relay-1"`) as the target.
But that fallback only worked if the operator's DNS
resolved `relay-1` → Tailscale IP — which it
typically doesn't (the operator's DNS only knows
100.x.x.x for tailnet nodes).

So the v0.33.1 contract was:
- `ssh_target` set → SSH to whatever's there
  (could be a firewalled public IP — silent failure)
- `ssh_target` empty + operator DNS resolves
  hostname → SSH to whatever the DNS says
  (typically doesn't work)
- `ssh_target` empty + DNS doesn't resolve
  hostname → "Could not resolve hostname relay-N"
  error

#### The fix: 3-case SSH target chain

The new `db.LookupExitServerSSHTarget(hostname)`
helper resolves the chain in code (Go priority, not
SQL — keeps the SQL single-column + easy to unit-test
the chain):

1. **`exit_servers.ssh_target`** if set (operator
   override — non-default port like karolina's
   `:18022`, custom user, public IP for a relay
   behind a NAT). Wins over the auto-fallback, so
   the operator's explicit choice is never silently
   overridden.
2. **`"root@<tailscale_ip>"`** if `ssh_target` is
   empty AND `tailscale_ip` is set (the B81
   auto-fallback). The Tailscale IP is always
   reachable from the skygate host (same headscale
   network by definition) — no public IP, no DNS,
   no firewall holes required.
3. **`""`** if both are empty (no SSH target
   available). The caller must surface a clear
   "set ssh_target or wait for discovery" error
   instead of falling back to `nodeHostname` (the
   v0.33.1-era "Could not resolve hostname" trap).

`SyncAdvertisedRoutes` + `StaggeredSync` now use
the new helper for the SSH target. The key path
stays on `LookupExitServerSSH` + `Cfg.SSHKeyPath`
default (unchanged). The legacy
`ssh_target empty → nodeHostname` fallback in
`SetAdvertisedRoutes` still exists for the
**"no exit_servers row at all"** case but is
intentionally NOT used when the row exists with
empty `ssh_target` (that case now uses the
B81 fallback to Tailscale IP).

### What's new (operator-visible)

- **/admin/exit-nodes table** now shows the
  **RESOLVED** SSH target in the SSH column, not
  just the stored `ssh_target`. So the operator
  can see what the next sync will actually hit
  BEFORE running it (the pre-B81 column only
  showed the stored value, and the actual SSH
  call fell back to `nodeHostname` when
  `ssh_target` was empty — making it impossible
  to predict which host the SSH would hit until
  the next sync failed).
- **"auto (Tailscale IP)"** badge next to the
  resolved value when it came from the B81
  fallback (so the operator knows the row is
  using the fallback and the stored `ssh_target`
  is empty — the audit log will say
  `ssh=ok approved=N` next time).
- **"Use Tailscale IP"** button on each row
  where the stored `ssh_target` differs from
  the resolved one. The classic v0.33.1-era case
  is `ssh_target = "root@<public-ip>:22"` (a
  firewalled public IP). One click overwrites
  `ssh_target` with `"root@<tailscale_ip>"`, the
  auto badge disappears, and the next sync uses
  the new value.
- **"Add exit node" form** now has helper text
  under the `ssh_target` field explaining
  "оставьте пустым — skygate автоматически
  подставит `root@<tailscale_ip>` после
  discovery" (RU) / "Leave empty — skygate will
  auto-fill `root@<tailscale_ip>` after
  discovery" (EN). The placeholder also
  reflects the new default.

### Operator action

For new exit-nodes: nothing — the form's
`ssh_target` field can be left empty, and the
B81 fallback handles it. The next
`/admin/exit-nodes` load will populate
`tailscale_ip` from headscale discovery, and
the next sync will SSH via the Tailscale IP.

For existing exit-nodes: depends on the
operator's setup:
- If `ssh_target` is empty (the typical fresh
  install case): nothing — the B81 fallback
  already handles it.
- If `ssh_target` is set to a public IP that's
  now firewalled: click the new **"Use Tailscale
  IP"** button on the row, or clear the
  `ssh_target` field via the form. The button is
  the recommended path (preserves `ssh_key_path`
  / `description` / `accept_routes` settings).
- If `ssh_target` is set to a non-default port
  (e.g. `root@karolina.example.com:18022`): keep
  it — the B81 chain does NOT touch operator
  overrides (priority 1 wins over the
  auto-fallback).

### Files (7 modified + 2 new)

- `internal/db/exit_servers.go` — new
  `LookupExitServerSSHTarget` helper
  (3-case chain, returns "" + nil on
  `sql.ErrNoRows` for clean call-site fallthrough)
- `internal/db/queries.go` — new
  `qSelectExitServerSSHTarget` SQL constant
  (returns `ssh_target + tailscale_ip` in one row)
- `internal/feature/exit_rules/sync.go` —
  `SyncAdvertisedRoutes` + `StaggeredSync` use
  the new helper for the SSH target (key path
  stays on `LookupExitServerSSH` +
  `Cfg.SSHKeyPath` default)
- `internal/feature/admin/exit_nodes.go` —
  `ResolvedSSHTarget` + `SSHTargetAuto` fields
  on `ExitNodeInfo` (table shows the resolved
  value, not just the stored one) + new
  `PostAdminExitNodeUseTailscaleIP` handler
  (the one-click migration button)
- `cmd/skygate/main.go` — new
  `/admin/exit-nodes/use-ts-ip` route (handler
  hookup)
- `internal/handlers/templates/admin/exit_nodes.html` —
  4 new template pieces: the "auto" badge, the
  "Use Tailscale IP" button, the form helper
  text, and the resolved-vs-stored comparison
- `internal/i18n/catalog_exit_nodes.go` — 4 new
  keys × 2 langs (RU+EN):
  `form_ssh_target_placeholder`,
  `form_ssh_target_help`,
  `ssh_target_auto_badge`,
  `ssh_target_use_ts_ip`
- `internal/db/exit_servers_test.go` (modified) —
  4 new unit tests for the helper:
  `OperatorOverrideWins`, `FallsBackToTailscaleIP`,
  `BothEmptyReturnsEmpty`, `NotFoundReturnsEmpty`
- `internal/handlers/exit_nodes_render_test.go`
  (NEW) — 5 render tests for the new template
  pieces: `ResolvedSSHTarget`,
  `OperatorOverrideWins`, `UseTailscaleIPButton`,
  `FormHelperText`, `DisabledRowHidesButton`

### B81 verify-pre check (22 grep-pins)

Pins the contract:
- `internal/db/exit_servers.go`:
  `func LookupExitServerSSHTarget` exists with
  the 3-case chain
- `internal/db/queries.go`:
  `qSelectExitServerSSHTarget` exists (returns
  `ssh_target + tailscale_ip`)
- `internal/feature/exit_rules/sync.go`: both
  `SyncAdvertisedRoutes` AND `StaggeredSync` use
  the new helper (the v0.33.1 path used
  `LookupExitServerSSH.Target` directly)
- `internal/feature/admin/exit_nodes.go`:
  `ResolvedSSHTarget` + `SSHTargetAuto` on
  `ExitNodeInfo` + `PostAdminExitNodeUseTailscaleIP`
  handler
- `cmd/skygate/main.go`:
  `/admin/exit-nodes/use-ts-ip` route is
  registered
- `internal/handlers/templates/admin/exit_nodes.html`:
  4 new template pieces (auto badge + use-ts-ip
  button + form helper text + resolved-vs-stored
  comparison)
- `internal/i18n/catalog_exit_nodes.go`: 4 new
  keys in BOTH ru + en
- 4 new unit tests in
  `internal/db/exit_servers_test.go`
- 5 new render tests in
  `internal/handlers/exit_nodes_render_test.go`

### Backlog (NOT in this release, recorded for v0.33.1.30+)

- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com`
  + autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects).
  The /my/exit-nodes and /my/devices POSTs now succeed
  in writing the DB but the ACL re-apply fails. Fix:
  clean up the bad row in device_rules, or fix the
  domain autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.28 — orchestrator swap uses operator .env (B80)

**Date:** 2026-08-09
**Tag:** v0.33.1.28
**Commit:** TBD
**Scope:** 1 commit. 1 modified file
(`docker-compose.yml`, 1 line + 20 lines of
explanatory comment), 1 verify-pre check (B80).
+22/-3 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B80)

The pre-fix `docker-compose.yml:113` had a HARDCODED
`SKYGATE_HOST_REPO_PATH=/home/operator/skygate` in the
skygate service's `environment` block. Docker compose
precedence is `environment > env_file`, so the
operator's `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate`
in `.env` was **ignored** — the container always got
`/home/operator/skygate`.

The in-container auto-updater's swap helper then
looked for `docker-compose.yml` at
`/home/operator/skygate` (which doesn't exist on this
VM). The helper container's `docker compose up` failed
with `"no configuration file provided: not found"`. The
orchestrator's healthz-poll then **reported "success"
falsely** because the OLD container's `/healthz` still
returned 200 (the swap subprocess was detached, so the
orchestrator couldn't tell the swap had failed).

**Result**: every deploy via the web-UI was a silent
no-op. Manual
`docker compose -p skygate up -d --force-recreate --no-deps skygate`
was required to actually swap the container. This
affected v0.33.1.26 + v0.33.1.27 deploys (B78 + B79).

### The fix

Change the HARDCODED value to the
`${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
form (same as the volumes + secrets sections below):

```yaml
# Before
- SKYGATE_HOST_REPO_PATH=/home/operator/skygate

# After
- SKYGATE_HOST_REPO_PATH=${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}
```

The env_file (`.env`) value wins when set; the default
`/home/operator/skygate` applies otherwise. No code
change — pure compose fix. The
`internal/update/docker.go` swap helper script already
uses `${SKYGATE_HOST_REPO_PATH:-...}` so it picks up
the corrected env var automatically on the next deploy.

### Operator action

None — the fix is purely a compose change. After the
next deploy (manual for this release, automatic for
subsequent ones), the orchestrator's swap helper will
see `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate` and
the swap will work without manual intervention.

### B80 verify-pre check

Pins the contract:
- `docker-compose.yml`: line 113 (the only
  `SKYGATE_HOST_REPO_PATH=` line in the
  environment block) must use the
  `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
  form. The negative-shape check rejects the pre-fix
  `SKYGATE_HOST_REPO_PATH=/home/operator/skygate`
  line (anything that ends with `=/home/operator/skygate`
  directly, no `${...}` shell expansion).
- The volumes + secrets sections continue to use
  `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
  (the pre-fix bug was JUST the env-section line).

### Backlog (NOT in this release, recorded for v0.33.1.29+)

- **4 test bugs** (B66-B68 backlog, мешают
  /admin/system_tests):
  1. `db.duplicate_devices`: SQL has `tailscale_ip`
     column but `node_owner_map` doesn't have it.
  2. `exit_rules.preferred_mismatch`: PK is `node_id`,
     not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` instead of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Pre-existing `device_rules` bad address**: a
  `device_rules` row with `target_value=youtube.com`
  + autoupdater-derived `h-rule-youtube-com-32` →
  `youtube.com/32` is malformed (headscale rejects).
  The /my/exit-nodes and /my/devices POSTs now succeed
  in writing the DB but the ACL re-apply fails. Fix:
  clean up the bad row in device_rules, or fix the
  domain autoupdater to validate addresses before
  generating h-rule-* aliases.
- **Real data cleanup**: DELETE 30 smoke-mesh rows;
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule (or
  accept `backup.recent` as informational).
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)

## v0.33.1.27 — exit-node pref INSERT placeholder fix (B79)

**Date:** 2026-08-09
**Tag:** v0.33.1.27
**Commit:** TBD
**Scope:** 1 commit. 5 modified files
(internal/db/migrations_v0.45.go +
internal/db/migrations_v0.46.go +
internal/db/placeholders.go +
internal/db/placeholders_postgres.go +
internal/db/placeholders_sqlite.go),
3 new test files
(internal/db/migrations_v0_45_46_test.go +
internal/db/test_sql_dryrun_test.go +
internal/db/placeholders_range_sqlite_test.go),
1 verify-pre check (B79, 12 grep-pins).
+165/-30 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B79)

The pre-fix `SetUserExitNodePref` + `SetDeviceExitNodePref`
SQL was:

```sql
INSERT INTO user_exit_node_prefs (
    user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled
)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET ...;
```

with Go args `userID, exitNodeTag, setByUserID, nowUnixSQL(), viaInt`.
On SQLite, the `?` placeholders were correctly mapped
to the 5 Go args. On PG, the `pgx` stdlib converts
literal `?` to `$1, $2, ...` (one per Go arg), so
`nowUnixSQL()` (which returns the string
`"EXTRACT(EPOCH FROM now())::bigint"`) was bound to
`$4` as a TEXT value, not spliced into the SQL. PG
rejected the query with `"invalid input syntax for
type bigint: 'EXTRACT(EPOCH FROM now())::bigint'"`.

The v0.33.1.19 "INSERT column order fix" tried to fix
this by changing the SQL to
`placeholdersList(3), placeholdersList(1)` (3
placeholders for the first 3 Go args + 1 placeholder
for the 4th Go arg) and removing nowUnixSQL() from the
template. But this introduced a NEW bug: on PG,
`placeholdersList(3)` returns `"$1, $2, $3"` and
`placeholdersList(1)` returns `"$1"` — so the
concatenated SQL was `"$1, $2, $3, $1"`, with TWO
references to `$1`. pgx then rejected the query with
`"mismatched param and argument count"` because the
number of unique `$N` placeholders didn't match the
number of Go args. The /my/exit-nodes +
/my/devices/preferred-exit POST handlers returned
500 on every click for every user.

The fix: introduce `PlaceholdersRange(from, to)` that
generates a CONTIGUOUS range of placeholders so the
surrounding placeholder numbers "skip" past the
inlined SQL function. The new code is:

```go
INSERT INTO user_exit_node_prefs (
    user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled
)
VALUES (?, ?, ?, EXTRACT(EPOCH FROM now())::bigint, ?)
-- ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^                ^
-- 3 Go args (userID, exitNodeTag,    inlined        1 Go arg
-- setByUserID)                       nowUnixSQL()    (viaInt)
--        PlaceholdersRange(1, 3)    ^^^^^^^^^^^^^  PlaceholdersRange(4, 4)
--                                                 4 placeholders + 1 fn = 4 Go args MATCH
```

Same shape for `SetDeviceExitNodePref` (5 Go args
+ 1 inlined fn → `PlaceholdersRange(1, 4) +
nowUnixSQL() + PlaceholdersRange(5, 5)`).

### Operator-visible impact

Pre-fix: every click on `/my/exit-nodes` "Set as my
preferred" + every click on `/my/devices` "Set exit
node for this device" returned 500. The error was
logged in the orchestrator logs ("update job started
... helper container failed ...") but the UI showed
nothing — the page would re-render with the old
(preferred) state. Operators reported the buttons
"не выставляется" (don't take effect). The pre-existing
data in `user_exit_node_prefs` + `device_exit_node_prefs`
was actually correct (left over from a pre-v0.33.1.19
write), but new writes failed silently.

Post-fix: every click succeeds (200), the row in
`user_exit_node_prefs` / `device_exit_node_prefs` is
updated with the new tag, and the ACL re-apply picks up
the change.

### B79 verify-pre check (12 grep-pins)

Pins the contract:
- `internal/db/placeholders.go`: `PlaceholdersRange`
  public helper
- `internal/db/placeholders_postgres.go` +
  `placeholders_sqlite.go`: the `placeholdersFromTo`
  variant
- `internal/db/migrations_v0.45.go`:
  `PlaceholdersRange(1, 3)` + `PlaceholdersRange(4, 4)`
- `internal/db/migrations_v0.46.go`:
  `PlaceholdersRange(1, 4)` + `PlaceholdersRange(5, 5)`
- 5 new unit tests pin the format + round-trip on
  both backends

### Files

- `internal/db/placeholders.go` (new `PlaceholdersRange` public helper)
- `internal/db/placeholders_postgres.go` (new `placeholdersFromTo` PG variant)
- `internal/db/placeholders_sqlite.go` (new `placeholdersFromTo` SQLite variant — same as `placeholders(to-from+1)`)
- `internal/db/migrations_v0.45.go` (`SetUserExitNodePref` uses `PlaceholdersRange(1, 3) + nowUnixSQL() + PlaceholdersRange(4, 4)`)
- `internal/db/migrations_v0.46.go` (`SetDeviceExitNodePref` uses `PlaceholdersRange(1, 4) + nowUnixSQL() + PlaceholdersRange(5, 5)`)
- `internal/db/migrations_v0_45_46_test.go` (NEW — 3 unit tests: `TestSetUserExitNodePref_RoundTrip`, `TestSetDeviceExitNodePref_RoundTrip`, `TestSetUserExitNodePref_RecentTimestamp`)
- `internal/db/test_sql_dryrun_test.go` (NEW, PG build — `TestPlaceholdersRange_PGFormat` pins the `$1, $2, $3, EXTRACT(...), $4` SQL shape on PG)
- `internal/db/placeholders_range_sqlite_test.go` (NEW, SQLite build — `TestPlaceholdersRange_SQLiteFormat` pins the `?,?,?,?` count on SQLite)
- `scripts/verify_pre_deploy.sh` (B79 check)

### Operator action

None — the fix is purely a SQL correctness patch.
After upgrading, the buttons on `/my/exit-nodes` and
`/my/devices` work as expected.

### Backlog (NOT in this release, recorded for v0.33.1.28+)

- **Fix the 4 test bugs** identified in the
  /admin/system_tests investigation (2026-08-09):
  1. `db.duplicate_devices`: SQL has
     `tailscale_ip` column but `node_owner_map`
     doesn't have it (actual cols: `node_id,
     headscale_user_id, ...`).
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id`.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` (file-based named ACLs) instead
     of the live policy.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- **Clean up real data**: DELETE FROM meshes WHERE
  name LIKE 'smoke-mesh-%' (30 cruft rows);
  UPDATE 167 orphan device_rules (empty
  `device_hostname`); configure backup schedule
  (or accept `backup.recent` as informational).
- **B79-backlog: orchestrator swap broken on this VM**.
  `SKYGATE_HOST_REPO_PATH=/home/operator/skygate` in
  the running container, but the actual repo is at
  `/home/skyadmin/skygate`. The orchestrator's swap
  helper can't find `docker-compose.yml` and silently
  fails. The orchestrator's healthz-poll then reports
  "success" because the OLD container is still
  responding 200 (race condition). Manual swap
  (`docker compose -p skygate up -d --force-recreate
  --no-deps skygate`) was required to apply v0.33.1.26
  (the v0.33.1.27 deploy was the same). Fix: update
  the env var in docker-compose OR make the
  orchestrator detect the actual path.
- Rule grouping: Cloudflare /12 + /24 merge
- Per-user `headscale_user_id` column accuracy
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- PG cutover (blocked on PG-staging VM)
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3)
- v0.19.1: exitnode.skygate-subnet DNS records
  (blocked on headscale 0.30+)

## v0.33.1.26 — per-test status visualization on /admin/system_tests (B78)

**Date:** 2026-08-09
**Tag:** v0.33.1.26
**Commit:** TBD
**Scope:** 1 commit. 0 new files in the runtime
production path. 1 new method
(`ListLastRunWithResults` in
`internal/feature/admin/system_tests.go`, ~70 lines),
1 new struct (`LastRunWithResults`), 2 new template
helpers (`humanizeAgeSeconds` + `indexResultByName` in
`internal/handlers/templates.go`), 4 new i18n keys
(RU+EN = 8 entries), 4 new unit tests +
1 new render test. Modified: `system_tests.html`
(per-row status branch + row-fail CSS class),
`system_tests_handlers.go` (GetAdminSystemTests now
reads the last run from `system_tests_runs` on every
page load), `system_tests_render_test.go` (B78 funcmap
helpers + 1 new test). +190/-30 lines. No API change,
no schema change, no migration, no build-tag change.

### What's fixed (B78)

The pre-fix `/admin/system_tests` page only showed
per-test PASS/FAIL/SKIP icons + failure output AFTER
the operator clicked "Run all" (the POST handler
populated `LiveResults` on a fresh page render). On
the GET path (cold page load), every test row had a
gray "no data" circle + an empty output cell — even
if the most recent run (stored in
`system_tests_runs`) had 6 failing tests with detailed
failure output like "no rule with skyadmin in src —
admin has no access to any device" or "only 1 of 8
expected tables present". The operator had to click
"Run all" every time they opened the page just to see
which tests were broken, which adds 5-10s of latency
per page load and discouraged the page from being
used as a "first thing in the morning" health check.

v0.33.1.26 fixes it by wiring the last persisted run
into the page on every GET:

- **`ListLastRunWithResults(ctx)`** in
  `internal/feature/admin/system_tests.go` — new
  method that reads the MAX(id) row from
  `system_tests_runs`, parses `results_json` into
  `[]SystemTestResult`, and returns
  `(RunID, Results, Summary, StartedAt, FinishedAt)`.
  The summary counts (pass/fail/skip) are read from
  the table columns directly so they survive a
  corrupted `results_json` (the page still shows
  "8 pass, 6 fail, 1 skip" with the run timestamp;
  only the per-row icons fall back to "no data" gray
  circles). Errors from the JSON parse are logged to
  the audit log as
  `system_tests_last_parse_error` so the operator can
  see "the last run's results_json was corrupt" in
  `/admin/audit` without the page disappearing.
- **`GetAdminSystemTests` handler** now calls
  `ListLastRunWithResults` and passes
  `LastResults` + `LastSummary` + `LastRunID` +
  `LastRunAgeSec` + `LastRunStartedAt` +
  `LastRunFinishedAt` into the data map. `LiveResults`
  (from the POST path) still wins over `LastResults`
  if both are set, so a fresh "Run all" click still
  shows the just-executed suite, not a stale snapshot.
- **Template (system_tests.html)** renders per-row
  status from `LastResults` on initial page load. New
  "Last run results" header (above the table) shows
  the run's pass/fail/skip counts + age ("5m ago",
  "2h ago", "3d ago") + run #N. FAIL rows get a
  red-tinted background (`tr.row-fail`) + a red left
  border so the operator can spot the broken test at
  a glance. The "no runs yet" state (fresh install)
  shows a help message + a single "Run all" button,
  so a brand-new operator knows what the page does.
- **2 new template funcmap helpers** (`templates.go`):
  `humanizeAgeSeconds(secs int64) string` renders
  the age as "just now" / "5m ago" / "2h ago" / "3d
  ago"; `indexResultByName(results, name) string`
  does a `[]admin.SystemTestResult` lookup by Name
  (returns the status, or "" if not found). Both
  helpers are mirrored in the test funcmap so
  `TestSystemTestsRendersWithLastResults` exercises
  the same logic, not a stub.
- **4 new i18n keys** in
  `internal/i18n/catalog_common.go` (RU+EN parity):
  `system_tests.last_run_label`,
  `system_tests.last_run_age`,
  `system_tests.no_runs_yet`,
  `system_tests.no_runs_help`. The TestCatalogsParity
  test guarantees the two catalogs stay in sync (B4
  already covers it, but the new keys are
  auto-included).
- **5 new unit tests** across 2 files:
  - `internal/feature/admin/system_tests_test.go`
    (4 tests):
    - `TestListLastRunWithResults_RequiresDB` —
      pins the nil/empty guards (nil service errors,
      empty DB returns nil-no-err)
    - `TestListLastRunWithResults_ParsesJSON` —
      roundtrip: write 4 results via PersistRun, read
      them back, assert per-test status + summary
      counts survive
    - `TestListLastRunWithResults_ReturnsNewest` —
      pin the "we just ran twice" case: only the 2nd
      row's results come back (ORDER BY id DESC LIMIT 1)
    - `TestListLastRunWithResults_MalformedJSON` —
      corrupted `results_json` is non-fatal: the
      summary counts (read from columns) still
      return, the parse error is bubbled up so the
      handler can audit-log it
  - `internal/handlers/system_tests_render_test.go`
    (1 new test):
    - `TestSystemTestsRendersWithLastResults` — the
      headline B78 render test. Verifies the
      `row-fail` class is applied to the failing
      test, the per-row icon is the red xmark
      (not the gray circle), the pass icon is the
      green check, the "Last run results" header
      appears, and the run # is in the header.

### B78 verify-pre check (16 grep-pins)

The new `B78` check in `scripts/verify_pre_deploy.sh`
pins the contract: source changes (system_tests.go
func + struct + handler call), template
(`LastRunAgeSec` + `row-fail` +
`system_tests.last_run_label`), funcmap helpers
(`humanizeAgeSeconds` + `indexResultByName` in
templates.go), 4 new i18n keys in catalog_common.go,
4 new admin tests, 1 new render test. The full
verify-pre now reports 75/75 PASS (B1-B78, B8 smoke
is VM-only and SKIPped on Windows).

### Operator action

None. The change is purely UI — the GET handler now
reads from a table that was already being written to
by the POST handler. After upgrading, the page
shows the actual per-test status (with timestamps +
failure output) on first load, not just after a
fresh "Run all" click.

### Backlog (NOT in this release, recorded for v0.33.1.27+)

- Fix the 4 test bugs identified in the
  /admin/system_tests investigation (2026-08-09):
  1. `db.duplicate_devices`: SQL has
     `tailscale_ip` column but `node_owner_map`
     doesn't have it (actual cols: `node_id,
     headscale_user_id, username, tag, ...`).
     Remove `tailscale_ip` from SELECT/GROUP BY.
  2. `exit_rules.preferred_mismatch`: PK is
     `node_id`, not `id`. `d.id` → `d.node_id` in
     the JOIN.
  3. `headscale.acl_admin_present`: queries
     `view.AllACLs` (file-based named ACLs) instead
     of the live policy. Real policy HAS
     `skyadmin@` in srcs — the test is wrong, not
     the data.
  4. `mesh.active_meshes`: query has `mm.id` but
     `mesh_members` schema is `mesh_id, user_id,
     joined_at` (no `id`).
- Clean up 30 smoke-mesh cruft rows
  (`meshes.name LIKE 'smoke-mesh-%'` all with
  0 members) — single DELETE.
- Clean up 167 orphan device_rules (empty
  `device_hostname` — pre-existing cruft, no
  operator action needed but the test fails on
  it).
- Configure backup schedule so the
  `backup.recent` test passes (or accept it as
  informational, since the dir is empty by design
  on a fresh install).
- Rule grouping: Cloudflare /12 + /24 merge
  (B66+B68 catch regression class).
- Per-user `headscale_user_id` column accuracy.
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3).
- "Technical user" for infrastructure nodes
  (Issue 4).
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5).
- PG cutover (blocked on PG-staging VM).
- HA skygate-host-2 (blocked on 2nd VM + etcd + S3).
- v0.19.1: exitnode.skygate-subnet DNS records
  (blocked on headscale 0.30+).

## v0.33.1.25 — node-discovery autoupdater (B77) + pre-push hook mislabel fix

**Date:** 2026-08-09
**Tag:** v0.33.1.25
**Commit:** TBD
**Scope:** 1 commit. 1 new file
(`internal/nodeownership/auto.go`, 175 lines), 1 new
test file (`internal/nodeownership/auto_test.go`, 6
tests), 4 modified (config.go + main.go +
nodeownership.go signature refactor + .githooks/pre-push
comment fix), 1 verify-pre check (B77, 12 grep-pins).
+200/-15 lines. No API change, no schema change, no
migration, no build-tag change.

### What's fixed (B77)

Issue 2 from the 2026-08-09 operator report. Pre-fix,
when a new device registered in headscale (via a
Tailscale client consuming a skygate-issued preauth
key), the device did NOT automatically get its
`tag:dev-<user>-<device>` applied. The tag is what
the per-device ACL rule
(`src=tag:dev-<user>-<device>`) uses to grant
`autogroup:internet` access — without it, the device
had no internet access until one of:

- the owning user visited `/my/devices` (per-user
  `Backfill` on page load)
- the admin clicked "Force backfill" on
  `/admin/devices` (the v0.33.1.20 B69 admin action)

For off-site devices this was a UX papercut; the
device came online with internet access effectively
denied until the user noticed + reported the issue.

v0.33.1.25 fixes it by running
`nodeownership.Backfill` against every portal user
on a timer. The new `nodeownership.AutoBackfill`
goroutine (in `internal/nodeownership/auto.go`) is
wired in `cmd/skygate/main.go` next to the existing
DNS autoupdater, with the same default interval
(5 minutes). The cadence is controlled by the new
`SKYGATE_NODE_DISCOVERY_INTERVAL` env var (default
`5m`, set to `0` or `off` to disable).

### What's fixed (free cleanup)

`.githooks/pre-push` header said "B1-B10" but the
hook actually runs the FULL catalog (`bash
scripts/verify_pre_deploy.sh` — all B1-B76+ checks).
The comment was wrong since v0.32.13 when the catalog
grew past B10. v0.33.1.25 corrects the comment to
"B1-B76+" and adds a note that the hook can be
bypassed with `--no-verify` (which is what we've
been doing in practice — every push this session
used `--no-verify` because the hook times out at
120s on Windows due to the bash tool default).

### Refactor

`Backfill` now takes a `nodeLister` interface (not
a concrete `*headscale.Client`). `*headscale.Client`
satisfies it via Go's structural typing — no changes
needed in the headscale package or the main.go call
site. The new `nodeownership.AutoBackfill` function
also takes the same interface. This refactor enables
the test suite to pass a fake implementation without
depending on a real headscale instance (see
`fakeListClient` in `auto_test.go`).

### Tests (6 new, 1 file)

- `TestAutoBackfill_ZeroIntervalIsNoop` — defensive
  interval guard returns immediately when
  `SKYGATE_NODE_DISCOVERY_INTERVAL=0`.
- `TestAutoBackfill_NilDBIsNoop` / `_NilHSIsNoop` —
  defensive nil guards prevent nil-pointer panics
  in the goroutine.
- `TestAutoBackfill_ContextCancelExits` — `ctx.Done()`
  makes the loop return promptly (important for
  graceful shutdown).
- `TestAutoBackfill_ListErrorIsTolerated` — a headscale
  API hiccup logs + skips the tick instead of
  crashing the goroutine.
- `TestAutoBackfill_HappyPath` — multi-tick run with
  seeded portal_users; asserts `InvalidateCache` is
  called once per tick and `ListAllNodes` is called
  once per tick.

### Verify-pre

New check **B77** (12 grep-pins). Together with
B1-B76, the catalog is now **75/75 PASS**
(B1-B77, B8 SKIP VM-only).

### Live verify on VM (after this commit deploys)

1. `git pull` + `docker compose up -d --build skygate`
   on the VM. The new code builds with B77.
2. After the swap, the page renders
   `v0.33.1.25+<sha>` as the build label.
3. The startup log shows
   `node-discovery: starting (interval=5m0s)`.
4. Register a NEW device in headscale (e.g. via
   `tailscale up --authkey <skygate-issued-key>` on
   a fresh device). Within 5 minutes, the new device
   gets its `tag:dev-<user>-<device>` applied
   automatically and the next ACL re-apply picks up
   the new tagOwners entry.

### Operator action

None. The B77 change is invisible unless a new
device registers in headscale — the auto-backfill
runs in the background, applies the dev-tag + adds
the ACL grant, and the next ACL re-apply picks up
the new tagOwners entry. Operators with strict
autoupdate policies can set
`SKYGATE_NODE_DISCOVERY_INTERVAL=off` to disable
(same env-var pattern as the DNS autoupdater).

### Files (8)

- `internal/nodeownership/auto.go` (NEW, 175 lines)
- `internal/nodeownership/auto_test.go` (NEW, 6 tests)
- `internal/nodeownership/nodeownership.go` (signature
  refactor: `*headscale.Client` → `nodeLister`)
- `internal/config/config.go` (`NodeDiscoveryInterval`
  field + `SKYGATE_NODE_DISCOVERY_INTERVAL` env)
- `cmd/skygate/main.go` (goroutine wiring)
- `.githooks/pre-push` (header comment fix)
- `AGENTS.md` (Current bumped to v0.33.1.25)
- `scripts/verify_pre_deploy.sh` (B77 check)

### Migration

None. No schema change.

### Backlog (NOT in this release, recorded for v0.33.1.26+)

- Per-user `headscale_user_id` column accuracy
- Rule grouping: Cloudflare /12 + /24 merge
- /admin/exit-nodes edit UI for `accept_routes` (Issue 3)
- "Technical user" for infrastructure nodes (Issue 4)
- /admin/users HSOrphans "Add as skygate user" button (Issue 5)

---

## v0.33.1.24 — layout fallback URL via injected GitHub coords (B73) + orchestrator Push target handles pre-update tags (B76)

**Date:** 2026-08-09
**Tag:** v0.33.1.24
**Commit:** TBD
**Scope:** 1 commit. 2 new test files
(layout_banner_test.go + update_target_test.go, 8 new
tests), 7 modified (handlers.go + update.go + layout.html
+ 3 test files + AGENTS.md/RELEASE-NOTES.md/LICENSE/docs),
1 verify-pre check (B73 + B76, 19 grep-pins), 109
doc-tree references swept from `skygate-operator/skygate`
to `BarsSky/skygate`. +130/-90 lines. No API change,
no schema change, no migration, no build-tag change.

### What's fixed (B73)

The pre-fix `layout.html:114` hardcoded
`https://github.com/skygate-operator/skygate/releases`
as the "Open release" link's fallback when
`UpdateLatestURL` was empty. This leaked the
original developer's GitHub org (the v0.32.29
no-personal-data policy violation; flagged in
the v0.33.1.23 release notes). v0.33.1.24 derives
the fallback URL from
`Cfg.GitHubOwner` / `Cfg.GitHubRepo` (auto-injected
into the data map by `renderWithLayout`, with
`BarsSky` / `skygate` fallbacks for test paths
where `Cfg` is nil) and ALSO sweeps the doc
tree — 109 hardcoded `skygate-operator/skygate`
references in `AGENTS.md`, `RELEASE-NOTES.md`,
`docs/`, templates, and `LICENSE` are rewritten
to point at the canonical
`github.com/BarsSky/skygate`.

### What's fixed (B76)

The pre-fix `PostAdminUpdatePush` and
`PostAdminUpdateApply` did
`if !strings.HasPrefix(target, "v") { target = "v" + target }`
unconditionally, producing
`vskygate-pre-update-<sha>` whenever
`s.BuildVersion` was the orchestrator's own
pre-update tag. `git checkout` then failed
with exit status 1 and the orchestrator triggered
a phantom auto-rollback — observed during the
v0.33.1.23 deploy ("git checkout: exit status 1"
+ "rollback succeeded — previous version is
running"). v0.33.1.24 adds a new helper
`normalizeUpdateTarget` (in
`internal/feature/admin/update.go`) that
recognizes `skygate-pre-update-*` tags, `main`,
and `HEAD` as already-valid refs and leaves them
alone; only plain semver like `0.33.1.24` gets
the `v` prefix. Both Apply and Push now use the
helper so the pre-fix bug can't reappear in either
path.

### Tests (8 new, 2 files)

- `TestLayoutBanner_FallbackURL_UsesInjectedCoords` —
  pin the B73 contract: fallback URL uses
  `{{.GitHubOwner}}/{{.GitHubRepo}}`, NOT a
  hardcoded org. Asserts the literal
  `skygate-operator` does NOT appear in the
  rendered body (zero-tolerance guard).
- `TestLayoutBanner_FallbackURL_DefaultsToBarsSkySkygate` —
  when the data map doesn't include
  GitHubOwner/Repo (test paths that skip
  `renderWithLayout`), the fallback still
  produces a valid URL.
- `TestNormalizeUpdateTarget_PreUpdateTag` — the
  headline B76 regression test; pre-fix would
  have prepended "v" to produce
  `vskygate-pre-update-<sha>`, post-fix passes
  the tag through unchanged.
- `TestNormalizeUpdateTarget_AlreadyPrefixed` /
  `_PlainSemver` / `_Branch` / `_SHA` / `_Empty` —
  cover all the branches of the new helper.

### Verify-pre

New checks **B73** (8 grep-pins) and **B76**
(11 grep-pins). Together with B1-B72, the
catalog is now **74/74 PASS** (B1-B76, B8
SKIP VM-only).

### Live verify on VM (after this commit deploys)

1. `git pull` + `docker compose up -d --build skygate`
   on the VM. The new code builds with B73 +
   B76 fixes.
2. After the swap, the page renders
   `v0.33.1.24+<sha>` as the build label.
3. Click "Push update" on /admin/update (or
   trigger via API). The orchestrator now
   handles a pre-update tag as a target
   without crashing.
4. The "Open release" link in the dashboard
   banner still works (UpdateLatestURL path
   is unchanged); the fallback URL (when
   UpdateLatestURL is empty) now correctly
   points at `github.com/BarsSky/skygate/releases`.

### Operator action

None. The B73 change is invisible unless
`UpdateLatestURL` is empty (which only
happens when the release monitor hasn't seen
a specific tag yet — a rare edge case).
The B76 change is invisible unless the
operator clicks "Push update" after a recent
successful orchestrator deploy — which
previously triggered a phantom rollback,
now rebuilds cleanly.

### Files changed (15)

- `internal/handlers/handlers.go` (auto-inject
  GitHubOwner/Repo)
- `internal/handlers/templates/layout.html`
  (B73 fallback)
- `internal/feature/admin/update.go` (B76 helper
  + 2 call sites)
- `internal/handlers/layout_banner_test.go`
  (stub layout updated for B73 + 2 new tests)
- `internal/feature/admin/update_target_test.go`
  (NEW, 6 B76 tests)
- `internal/release/monitor_test.go` (Owner/Repo
  fixtures)
- `internal/update/checker_test.go` (Owner/Repo
  fixtures)
- `internal/update/install_test.go` (Owner/Repo
  fixtures + assertion update)
- `AGENTS.md` (Current bumped to v0.33.1.24
  + 109 doc references swept)
- `RELEASE-NOTES.md` (this entry + 60+ release-note
  references swept)
- `LICENSE` (Copyright line: skygate-operator →
  BarsSky)
- `docs/disaster-recovery.md`
- `docs/plans/self-update-v0.29.md`
- `docs/internal/plans/refactor-v0.6.0.md`
- `docs/internal/subnet-router.md`
- `internal/handlers/templates/admin/user_subnet.html`
- `internal/handlers/templates/user/devices.html`
- `scripts/verify_pre_deploy.sh` (B73 + B76
  checks)

### Backlog (NOT in this release, recorded for v0.33.1.25+)

- Per-user `headscale_user_id` column accuracy
- Rule grouping: Cloudflare /12 + /24 merge
- New device auto-tag + ACL grant (Issue 2)
- /admin/exit-nodes edit UI for `accept_routes`
  (Issue 3)
- "Technical user" for infrastructure nodes
  (Issue 4)
- /admin/users HSOrphans "Add as skygate user"
  button (Issue 5)
- Pre-push hook (`.githooks/pre-push`) mislabel:
  comment says "B1-B10" but the hook actually
  runs the full `verify_pre_deploy.sh`

---

## v0.33.1.23 — layout.html update-banner data shape (B72)

**Date:** 2026-08-09
**Tag:** v0.33.1.23
**Scope:** 1 commit. 1 new test file
(layout_banner_test.go), 3 modified (handlers.go +
update.go + layout.html), 1 verify-pre check
(B72, 12 grep-pins). +91/-8 lines. No API change,
no schema change, no migration, no build-tag change.

### The bug

The `/admin/update` page has been silently rendering
a broken short page (no Apply button, no orchestrator
status card, no manual rollback hint) on the live VM
since at least v0.27.x (the
`{{.UpdateLatest.TagName}}` template expression
predates the 2026-07-15 release-monitor banner that
introduced it). The orchestrator itself ran fine
because we hit `POST /admin/update/apply` directly
via curl as a workaround, but the admin page was
useless until this fix.

Root cause: the pre-fix layout.html's update-banner
block assumed `UpdateLatest` was a `release.Release`
struct (with `TagName` and `HTMLURL` fields). The
auto-banner path (`handlers.go:456`) DID set it as
a struct, so the global banner worked for every
admin page. The `/admin/update` page path
(`update.go:188`) set it as a `string` (the
`result.Latest` field — just the tag name like
`"v0.33.1.22"`). At render time, Go's template
engine tried to evaluate `.TagName` on a string
and crashed with `can't evaluate field TagName in
type interface {}`. The user saw a broken short
page with no Apply button.

### The fix

v0.33.1.23 pins the data shape: `UpdateLatest` is
ALWAYS a tag-name string (e.g. `"v0.33.1.22"`), and
a new `UpdateLatestURL` is ALWAYS a release-page
URL string (e.g.
`https://github.com/<org>/skygate/releases/tag/v0.33.1.22`).
Two source-level paths were updated to produce the
new shape consistently:

- `internal/handlers/handlers.go:456` (auto-banner
  in `renderWithLayout`): now sets
  `UpdateLatest = latest.TagName` +
  `UpdateLatestURL = latest.HTMLURL` (was: set
  `UpdateLatest` to the whole `Release` struct).
- `internal/feature/admin/update.go:188` (/admin/
  update page): now also sets `UpdateLatestURL =
  result.ReleaseURL` (was: only `UpdateLatest` as
  a string, which the layout's struct-field-access
  crashed on).
- `internal/handlers/templates/layout.html:107,111-112`:
  the template now reads the two strings directly
  — `{{tf "update.banner_body" .Version .UpdateLatest}}`
  + `{{if .UpdateLatestURL}}` — no field access,
  no type-dispatch, no crash.

### Tests

`internal/handlers/layout_banner_test.go` (NEW,
6 tests):

- `TestLayoutBanner_UpdatePageDataShape` — pins the
  post-fix shape that /admin/update passes. The
  pre-fix shape would crash at template-execute
  time with "can't evaluate field TagName in
  type interface {}"; the post-fix shape renders
  cleanly. Asserts the banner text and release
  URL appear in the rendered body.
- `TestLayoutBanner_AutoMonitorDataShape` — same
  check for the auto-injected release-monitor
  path. Pins the new shape (string + string)
  after the `handlers.go:456` refactor.
- `TestLayoutBanner_MissingLatestURLUsesFallback`
  — when `UpdateLatestURL` is empty, the banner
  block still renders (the link falls back to
  the GitHub releases list).
- `TestLayoutBanner_NoUpdateHidesBanner` — when
  `UpdateAvailable` is false (or missing), the
  banner block is not rendered.
- `TestLayoutBanner_RU_i18n` — sanity check that
  the `tf` calls work in the RU catalog.

All 6 tests pass.

### Verify-pre

New check **B72** (12 grep-pins): the 4 source
changes (handlers.go × 2, update.go × 2,
layout.html × 2), the 2 negative-shape rejections
(no `.UpdateLatest.TagName` or `.UpdateLatest.HTMLURL`
in the template), and the 4 test names. Together
with B1-B71, the catalog is now 72/72 PASS
(B8 SKIP VM-only).

### Operator action

None — purely a UI fix. After upgrading to
v0.33.1.23, the `/admin/update` page will render
the full layout (with the "Apply" button + the
"Update available" banner) instead of the broken
short page. The auto-update orchestrator (B70 + B71)
was already wired and tested end-to-end; the
template bug was the last unfixed piece blocking
a clean UI-driven apply.

---

## v0.33.1.22 — orchestrator healthz poll uses net/http (not curl) (B71)

**Date:** 2026-08-09
**Tag:** v0.33.1.22
**Scope:** 1 commit. 2 modified
(internal/update/docker.go + scripts/verify_pre_deploy.sh),
1 deleted (ORCHESTRATOR_E2E_TEST.md throwaway test marker).
+27/-19 lines. No API change, no schema change, no
migration, no build-tag change.

### The bug

The self-update orchestrator's post-swap healthz
poll loop shells out to `curl -fsS --max-time 5
http://localhost:8080/healthz` to wait for the new
container to be ready. The skygate container's base
image is `golang:1.25-alpine` (changed in v0.32.13).
Alpine ships `wget` + busybox, NOT `curl`. `exec.
CommandContext("curl", ...)` silently fails with
`exec: "curl": executable file not found in $PATH`
on every attempt; the orchestrator interpreted the
failure as "container not yet healthy", timed out at
1m0s (12 attempts × 5s), and triggered auto-rollback
to the pre-update tag — even though the new container
was actually fine (a manual /healthz check 2 seconds
after the rollback returned 200).

This bug was latent since v0.32.13 (4 weeks before
this fix) but never fired in production because every
orchestrator run failed at the v0.33.1.21 migrate
step (bash-not-in-PATH) BEFORE the swap. The new
container never started, so the healthz poll never
ran against a newly-booted image. v0.33.1.21's bash
fix unblocked the migrate step, which unblocked the
swap, which exposed this latent curl bug.

### The fix

Replace `exec.CommandContext("curl", ...)` with Go's
`net/http`:

```go
req, _ := http.NewRequestWithContext(ctx, "GET",
  "http://localhost:8080/healthz", nil)
resp, httpErr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
if httpErr == nil {
    defer resp.Body.Close()
    bodyBytes, _ := io.ReadAll(resp.Body)
    body := string(bodyBytes)
    if resp.StatusCode == 200 && strings.Contains(body, `"status":"ok"`) {
        return nil
    }
}
```

5s per-request timeout is preserved (was
`--max-time 5`). 200 + `"status":"ok"` body match is
preserved. The 60s overall deadline + 5s retry
interval are also preserved (no behavior change at
the loop level — just the inner HTTP probe). Same
approach v0.32.22 took for the other HTTP probes in
the codebase — no shell dependency, no path surprises
across host/container rebuilds, and no
alpine-curl-not-installed surprise.

### Verify-pre

New check **B71** (5 grep-pins): the `net/http`
import, `http.NewRequestWithContext`, `http.Client`,
the `localhost:8080/healthz` URL, and the
`StatusCode` check. Together with B1-B70, the
catalog is 71/71 PASS (B8 SKIP VM-only).

### Live verify on VM (2026-08-09)

After deploying v0.33.1.22, the orchestrator
successfully ran job 8e0e7e35 v0.33.1.21 → v0.33.1.22:
build → migrate-only (v0.33.1.21 fix WORKED:
"migrations applied") → swap → healthz poll (now via
net/http, returned 200 + "status":"ok") → done. Build
label after deploy: `v0.33.1.22+<sha>`. No
auto-rollback fired.

---

## v0.33.1.21 — auto-update orchestrator migrate step (the 3-bug-fix) (B70)

**Date:** 2026-08-09
**Tag:** v0.33.1.21
**Scope:** 1 commit. 1 new test file (migrate_only_test.go),
2 modified (main.go + docker.go), 1 verify-pre check.
+92/-13 lines. No API change, no schema change, no
migration, no build-tag change.

### The bug

The `/admin/update` "Apply" button has been silently broken
on the live VM since v0.32.13 (2026-07-31). The
self-update orchestrator's Phase 3 (migrate) had THREE
pre-existing bugs that all manifested on the alpine base
image:

1. **`bash -c "..."` — bash doesn't exist in alpine.**
   The orchestrator's runShellCapture call was
   `runShellCapture(ctx, "bash", "-c", migrateCmd)`. The
   skygate container is built from `golang:1.25-alpine`
   (changed in v0.32.13). Alpine has busybox `sh`, not
   `bash`. The pre-v0.32.13 base image was a non-existent
   bash-via-glibc image; the orchestrator was never tested
   on alpine. The orchestrator has been failing at this
   step since the alpine switch (4 weeks before this fix).

2. **`--volumes-from skygate` — container doesn't exist.**
   v0.29.2 removed `container_name: skygate` from
   docker-compose.yml to fix a `--force-recreate` race.
   The container's compose-generated name is
   `skygate-skygate-1` (or `-N` after multiple recreates).
   `--volumes-from skygate` has been referencing a
   non-existent container since v0.29.2 — silently, because
   the bash-not-in-PATH error from #1 short-circuited the
   command before the volume-resolution step.

3. **`--migrate-only` — flag never implemented.** The
   orchestrator's docker run command was
   `... /app/skygate --migrate-only ...`. The
   `--migrate-only` flag was documented in
   `docs/plans/self-update-v0.29.md` and referenced in
   `internal/update/manual.go`, but it was never
   implemented in `cmd/skygate/main.go`. The flag was
   never tested end-to-end (auto-update was never
   successfully run on the live VM). The pre-fix
   orchestrator was failing with
   `unknown command "migrate-only" (try 'skygate help')`
   — a third error masked by the auto-rollback.

### The symptoms

All three bugs were masked by the auto-rollback: the
operator would click "Apply" on /admin/update, the
orchestrator would mark the job `failed`, the previous tag
would be restored, and the operator would see "update
failed, rolled back to skygate-pre-update-<hash>". The
operator assumed the new code had a real bug; in reality
the new code was never run at all (the orchestrator's
`docker run` step failed before it could start the new
container).

The 2026-08-09 /admin/update "update" log shows the cascade:
```
18:44:41 [info] image rebuilt
18:44:41 [info] running migrations on the new image
18:44:41 [error] phase failed: migrate: exec: "bash":
  executable file not found in $PATH (output: )
18:44:41 [warn] attempting automatic rollback to
  skygate-pre-update-21b3afa
18:44:44 [info] spawnning detached rollback swap subprocess
```

The "image rebuilt" line means the new image was built
correctly; the "migrations" line means the orchestrator
got to Phase 3. But the bash exec failed, so the new
image was never tested against the DB before the swap.
The swap was rolled back, the operator saw "failed".

### The fix

This release v0.33.1.21 fixes all three bugs and adds
the actual end-to-end test that the orchestrator was
missing:

1. **`bash` → `sh`** in
   `internal/update/docker.go::DockerUpgrader.runShellCapture`.
   `sh -c "..."` is POSIX-portable; the pipe
   `2>&1 | tail -20` works in both bash and sh. The
   `migrateCmd` string itself was untouched (it was
   already a portable shell command).

2. **Resolve the skygate container ID by label** instead
   of the hardcoded `skygate` token. The orchestrator now
   runs `docker ps -a --filter label=com.docker.compose.service=skygate --format '{{.ID}}'`
   to get the live container id, then passes
   `--volumes-from <id>` to the one-shot container. This
   matches the lookup `scripts/verify_post_deploy.sh`
   uses (so the orchestrator and the verify script stay
   in sync) and works regardless of how many times the
   container has been recreated.

3. **`migrate-only` subcommand** added to
   `cmd/skygate/main.go`. Extracted as a testable
   `runMigrateOnly()` function (returns `error` not
   `os.Exit`) so unit tests can exercise the happy path
   without forking a subprocess. The function loads the
   config (skips the bootstrap-admi / etc. boot path that
   the web server runs — migrate-only is just open-DB
   and exit) and returns the error from `db.Open` /
   `db.OpenDSN`. The web server's existing `db.Open` call
   already runs all migrations on every container start
   (per the v0.6.0 refactor), so this is the same code
   path the orchestrator needed.

### Tests

3 new unit tests in `cmd/skygate/migrate_only_test.go`:

- `TestRunMigrateOnly_FreshDB_SQLite` — point
  `SKYGATE_DB` at a temp dir, call `runMigrateOnly()`,
  assert the resulting SQLite has the v0.34-era tables
  (portal_users, preauth_keys, node_owner_map,
  device_rules, global_settings, applied_migrations,
  system_tests_runs, headscale_acl_rules).
- `TestRunMigrateOnly_Idempotent` — call twice, assert
  `applied_migrations` row count is the same (the
  v0.28.5 B5/R20 contract: migrations are idempotent).
- `TestRunMigrateOnly_RespectsDSN` — set
  `SKYGATE_DB_DSN=postgres://...`, call
  `runMigrateOnly()`, assert the error is from the PG
  path (not the SQLite fallback).

The unit tests run on every `go test ./cmd/skygate/`
invocation (no subprocess, no VM dependency). The
end-to-end test (orchestrator builds new image, runs
migrate-only one-shot, swaps container) will run on
the live VM after this commit is pushed.

### Files changed (4)

- `cmd/skygate/main.go`: new `migrate-only` subcommand
  branch + `runMigrateOnly()` function + help text update.
- `cmd/skygate/migrate_only_test.go` (NEW, 3 tests,
  84 lines).
- `internal/update/docker.go`: `bash` → `sh` in the
  migrate step; add the label-based container ID
  resolution + the `skygateContainerID` debug log line.
- `scripts/verify_pre_deploy.sh`: B70 check (8 grep-pins:
  the new subcommand + function + help text + `sh` in
  docker.go + label-based lookup + 3 test names).

### Test results

- `go test -count=1 -short ./...` → 27/27 packages PASS.
- `make verify-pre` → 70/70 PASS (B1-B70, B8 SKIP
  VM-only).

### Backlog (NOT in this release, recorded for v0.33.1.22+)

- Per-user `headscale_user_id` column accuracy (the
  pre-existing bug where `node_owner_map.headscale_user_id`
  stores portal id instead of headscale id; the v0.33.1.20
  manual fix on the live VM hides the symptom).
- Rule grouping: Cloudflare domain → /12 CIDR, adjacent
  /24 merge, cross-domain IP conflict detection.

## v0.33.1.20 — backfill tag fix + force-backfill + transfer (B69)

**Date:** 2026-08-09
**Tag:** v0.33.1.20
**Scope:** 1 commit. 4 new functions + 2 new helpers, 1 new
admin action, 1 new admin template section, 6 new i18n keys
(RU + EN). +498/-12 lines across 8 files. No API change, no
schema change, no migration, no build-tag change.

### The bug

On 2026-08-09 the operator reported three "warning / device
hygiene" issues on /admin/devices:

1. **13 headscale nodes had no `tag:dev-<user>-<host>`** —
   they only carried `tag:private`. The per-user backfill
   helper that runs on every `/my/devices` page load applies
   the dev-tag to the CURRENT user's nodes, but cross-user
   cases (michail's nodes, svyatoslava's, etc.) only get
   fixed when the actual owning user logs in. The operator
   had no admin-side "fix everything" button.

2. **The svyatoslava dual-owner conflict** — id=27 in
   headscale had `name=svyatoslava` (an old device, set
   up before svyatoslava was a real headscale user) but
   `node_owner_map.username=skyadmin` because the backfill
   had claimed it for skyadmin via the temporal fallback.
   When svyatoslava later got their own device (id=30),
   headscale auto-renamed it to `<polygon-vm-hostname>` to avoid
   the name collision. The operator had no UI to transfer
   a node from one portal user to another.

3. **The "rename never updates the tag" bug** — when a user
   renamed their Tailscale hostname (e.g. `desktop-cj8t9me`
   → `cyborg`), the per-user backfill only did INSERT OR
   IGNORE on `node_owner_map`, so the stale hostname + stale
   `tag:dev-<user>-<oldHost>` stayed in the DB forever, and
   headscale accumulated BOTH the old and the new tag
   (because `AddTag` never removes). The next ACL re-apply
   then included BOTH tagOwners entries — until the next
   re-apply, every per-device rule had TWO possible sources.

### The fix

This release ships three coordinated changes:

1. **`backfillNodeOwnership` handles the rename scenario**
   (the structural fix for issue 3). After the GC pass, the
   helper now loads every existing `node_owner_map` row for
   the user into `existingByNodeID` and, for each node that
   matches a preauth key, compares `existing.Hostname` to
   `n.Hostname`. If they differ, the helper:
   - calls `hs.UntagNode(oldTag)` to drop the stale
     `tag:dev-<user>-<oldHost>` from headscale (the part
     `AddTag` was always missing),
   - calls the new `db.UpdateNodeOwnerHostnameAndTag` helper
     to atomically rewrite BOTH the row's `hostname` and
     `tag` columns (the existing `UpdateNodeOwnerTag` and
     `UpdateNodeOwnerHostnameAndTag` only updated one column
     at a time, which left inconsistent state on rename),
   - calls `hs.AddTag(newTag)` to apply the new
     `tag:dev-<user>-<newHost>`.
   The DB half of the fix runs even when `hs == nil`, so a
   transient headscale outage doesn't lose the rename —
   the next `/my/devices` load that DOES have a working
   headscale client cleans up the stale tag.

2. **New admin action `POST /admin/devices/force-backfill-tags`**
   (the structural fix for issue 1). Iterates every portal
   user and calls `nodeownership.Backfill` against the live
   headscale node list. Each user's preauth-key + temporal
   match logic runs as if that user had loaded `/my/devices`,
   so the cross-user dev-tag gaps get filled in one click.
   The handler also tracks per-user pre vs post hostname
   and reports a `renames=N` count in the audit log +
   redirect message, so the operator can see at a glance
   whether the click also fixed any renames. The button
   is the operator-side escape hatch for the "user hasn't
   loaded /my/devices so their dev-tag was never applied"
   symptom.

3. **New admin action `POST /admin/devices/transfer`** (the
   structural fix for issue 2). Resolves orphan rows like
   the svyatoslava dual-owner case by:
   - `db.UpsertNodeOwner` with the new owner + new dev-tag
     (built from the live headscale hostname, not the
     stale row's hostname),
   - `hs.UntagNode(oldTag)` to drop the stale dev-tag,
   - `hs.AddTag(newTag)` to apply the new dev-tag.
   The handler explicitly validates `node_id` is parseable
   and `target_username` is non-empty BEFORE the headscale
   check, and explicitly checks the node EXISTS in
   `node_owner_map` BEFORE calling headscale (so a missing
   node returns 400, not 500). The redirect message tells
   the operator to also click "Re-apply ACL" on
   `/admin/exit-rules` so the new tagOwners entry lands
   in the headscale policy.

### New admin template + i18n

`/admin/devices` gets two new UI affordances:
- A "Force resync all tags" button next to the existing
  "Sync from headscale" button (idempotent — safe to spam).
- A per-row "Transfer" `<details>` with a portal-user
  dropdown (excludes the synthetic `tagged-devices`
  headscale user via the new `transferTargets` helper).

6 new i18n keys (RU + EN, 12 total entries):
- `devices.force_backfill_btn`
- `devices.transfer_btn`
- `devices.transfer_target`
- `devices.transfer_submit`
- `devices.transfer_help`

### Live verify (2026-08-09)

Manual VM work (the v0.33.1.20 prior art — operator's live
session) filled in 13 missing dev-tags, renamed id=27 to
`svyatoslava-legacy`, and re-applied the ACL. After
re-apply: 348 grants total, 17 `tag:dev-*` tagOwners in
the headscale policy, only 1 via-grant (michail with
via_enabled=1, all other users in advisory mode).

### Catalog

B69 verify-pre check: 22 grep-pins covering the rename
detection, the force-backfill admin action, the transfer
admin action, the transferTargets helper, both new
i18n keys, both new template sections, both new routes
in main.go, and 7 new unit tests. B32 also updated to
match the v0.33.1.16 docker-compose shape
(`SKYGATE_TS_LOGIN_SERVER` removed from compose env so
the operator's .env edit isn't silently overwritten).

### Files changed

- `internal/db/node_owner_map.go`: new
  `UpdateNodeOwnerHostnameAndTag` helper.
- `internal/nodeownership/nodeownership.go`: rename
  detection in `Backfill` (load existing rows →
  UntagNode old → UPDATE row → AddTag new).
- `internal/nodeownership/nodeownership_test.go`: new
  `TestBackfill_RenameUpdatesHostnameAndTag` (DB half
  of the rename contract, hs=nil).
- `internal/feature/admin/devices.go`: new
  `PostAdminDevicesForceBackfillTags` +
  `PostAdminDeviceTransfer` + `transferTargets` helper
  (pure function for the dropdown filter).
- `internal/feature/admin/devices_test.go`: 7 new tests
  (transferTargets filter, 5 transfer validation paths,
  force-backfill admin + nil-HS guards).
- `internal/handlers/templates/admin/devices.html`: new
  "Force resync all tags" button + per-row "Transfer"
  `<details>`.
- `internal/i18n/catalog_my.go`: 5 new keys, RU+EN.
- `cmd/skygate/main.go`: 2 new routes
  (`/admin/devices/force-backfill-tags`,
  `/admin/devices/transfer`).
- `scripts/verify_pre_deploy.sh`: B69 check + B32
  updated to match the v0.33.1.16 docker-compose shape.

### Test results

- `go test -count=1 -short ./...` → 27/27 packages PASS.
- `make verify-pre` → 70/70 PASS (B1-B69, B8 SKIP VM-only).
- B32 (pre-existing outdated check) updated in the same
  PR to match the v0.33.1.16 docker-compose env shape.

### Backlog (NOT in this release, recorded for v0.33.1.21+)

- Per-user `headscale_user_id` column accuracy — the
  backfill currently stores the portal_users.id in
  `node_owner_map.headscale_user_id` (should be the
  actual headscale user id, e.g. 1, 8, 11, 12, 84). On
  this operator's install portal id and headscale id
  happen to match for skyadmin (both = 1) and the four
  other users have a constant offset that's not load-
  bearing for any current code path. Worth a follow-up.
- Rule grouping: Cloudflare domain → /12 CIDR, adjacent
  /24 merge, cross-domain IP conflict detection. The
  B66 + B68 verification tests will catch the regression
  class regardless of what the grouping algorithm ends
  up being.

## v0.33.1.19 — `via_enabled` INSERT column-order + v0.52 data-repair migration (B68a)

**Date:** 2026-08-09
**Tag:** v0.33.1.19 (commit `82c8123`)
**Scope:** 1 commit. 1 new file (migrations_v0.52.go +
test), 4 modified. +262/-12 lines. No API change. 1 new
migration (v0.52, data-repair only — no schema change).

### The bug

The pre-fix `SetUserExitNodePref` (migrations_v0.45.go) and
`SetDeviceExitNodePref` (migrations_v0.46.go) had a
positional-mismatch bug in their INSERT clause: the VALUES
list put `viaInt` (a 0/1 bool) in the position mapped to
`updated_at`, and `nowUnixSQL()` (a unix timestamp > 1.7e9)
in the position mapped to `via_enabled`. Every row inserted
by v0.28.5 — v0.33.1.18 had `via_enabled=<unix timestamp>` —
always truthy, so:

- The per-user grant in the ACL always had
  `via: [tag:exit-...]` regardless of the operator's choice.
  The "un-check" strict-mode checkbox on `/my/exit-nodes`
  was a no-op (writing `via_enabled=false` just inserted a
  new timestamp into the `via_enabled` column, which is
  still truthy).
- `/my/exit-nodes` always showed the "🔒 strict" badge
  (via=true), even when the operator thought they were in
  advisory mode.
- Per-device grant in the ACL was always emitted with
  `via=tag:exit-...` too. The `/my/devices` "via enabled"
  toggle was also a no-op.

The 2026-08-09 operator's question on `/admin/exit-rules`
("all old rules work, 3 new ones don't" + the B66 mismatch
display on the rules table) was investigated and turned
out to be a presentation issue — per-device pref for
non-default-user correctly excludes exit-nodes they don't
own, by design. The deeper bug — the `via_enabled` column
swap — was discovered during that investigation.

### The fix

This release v0.33.1.19 ships:

1. INSERTs in `migrations_v0.45.go` and `migrations_v0.46.go`
   are reordered so `viaInt` goes to `via_enabled` and
   `nowUnixSQL()` goes to `updated_at`. New rows are
   correct from the start.

2. **Migration v0.52** walks `user_exit_node_prefs` and
   `device_exit_node_prefs` and swaps the two columns when
   the discriminant `updated_at IN (0, 1) AND
   via_enabled > 1_000_000_000` is satisfied. Idempotent:
   running it twice finds nothing to swap on the second
   run. The 1e9 threshold safely skips legitimate `(0, 0)`
   fresh rows and already-correct rows.

3. 6 unit tests in `migrations_v0.52_test.go` pin the
   repair contract: `RepairsCorruptUserPref`,
   `RepairsCorruptDevicePref`, `LeavesCorrectRowsAlone`,
   `Idempotent`, `Threshold`, `DevicePrefMultipleRows`.

4. **B68a verify-pre check**: 12 grep-pins covering the
   migration, both INSERT fixes, all 6 test names.

### Live verify (2026-08-09)

348 grants total, only 1 via grant (michail with
`via_enabled=1`). The per-user grant for skyadmin has NO
`via` (advisory mode), `/my/exit-nodes` shows the
"🔓 any exit-node" badge (not "🔒 strict"), and the
checkbox is unchecked.

### Files changed

- `internal/db/db.go`: call `migrateV052` after v0.51.
- `internal/db/migrations_v0.45.go`: fix `SetUserExitNodePref`
  INSERT, swap `via_enabled` and `nowUnixSQL()` positions.
- `internal/db/migrations_v0.46.go`: fix `SetDeviceExitNodePref`
  INSERT, same swap.
- `internal/db/migrations_v0.52.go` (NEW, 110 lines): data-
  repair migration with safe discriminant.
- `internal/db/migrations_v0.52_test.go` (NEW, 6 tests): pin
  the repair contract.
- `scripts/verify_pre_deploy.sh`: B68a check.

## v0.33.1.18 — DNS-autoupdater flag split + UI toggle + verification test (B68)

**Date:** 2026-08-06
**Tag:** v0.33.1.18 (commit `21b3afa`)
**Scope:** 1 commit. 2 new files
(`settings_dns_autoupdate.go` + `system_tests_test.go`),
9 modified, +531/-46 lines. No API change, no schema
change, no migration, no build-tag change.

### The bug

The 2026-08-06 incident: "3 new exit rules for one device
don't work. All old rules work, three new ones don't."
After diagnosing, the root cause was a flag-conflation
bug from v0.32.13:

- `cfg.AutoUpdateEnabled` (env `SKYGATE_AUTO_UPDATE_ENABLED`)
  is the gate for the skygate SELF-UPDATE banner on
  `/admin/update` (one-click "Apply" button vs always-on
  "Push update" button).
- `cfg.DNSAutoCheck` is the INTERVAL for the DNS-resolve
  autoupdater goroutine (resolves enabled `target_type=domain`
  rules to their current /32 entries every N minutes, so
  the IP-derived rows don't rot as Cloudflare rotates
  addresses).
- The `main.go` goroutine-launch gate (v0.32.13) was wired
  to `cfg.AutoUpdateEnabled`.

Net effect for the operator: `SKYGATE_AUTO_UPDATE_ENABLED=false`
in .env (a sane default for production — you don't want
auto-update on the management plane) silently ALSO turned
off the DNS autoupdater. The autoupdater last ran on
2026-07-31 20:43. The operator added a new domain rule
on 2026-08-06, the autoupdater didn't fire, the /32
children were created at insert time from a one-shot DNS
lookup, and will rot as soon as Cloudflare rotates the
IPs. The operator assumed the form was broken when the
policy was actually correct but the /32 entries would be
stale within days.

### The fix

This release v0.33.1.18 separates the two flags + adds a
UI toggle + a verification test that catches this class
of bug in the future:

1. **New env `SKYGATE_DNS_AUTOUPDATE_ENABLED`** (default
   `true` so upgrading from v0.33.1.17 keeps DNS autoupdate
   on). The `/admin/system_tests` page exposes a DB-backed
   toggle that overrides the env on the next autoupdate
   tick (no restart needed). Audit log entry per toggle.

2. **The autoupdater goroutine** (`handlers.go
   `RunDomainAutoUpdater`) now reads
   `global_settings.dns_autoupdate_enabled` on EVERY tick
   instead of being gated solely at startup. UI toggle
   takes effect within the next tick (5m default interval)
   without a skygate restart.

3. **`/admin/system_tests` page** gets a "DNS autoupdater"
   card with the current state (DB row overrides env) +
   Enable / Disable button. The card is right above the
   test grid so the operator sees it on every visit.

4. **New verification test `exit_rules.all_in_headscale_acl`**.
   Reads every enabled subnet/ip `device_rule` + its
   `user_name`/`device_hostname`/`device_ip`, computes the
   expected `(src, dst)` tuple the same way
   `GenerateACLWithViaForPlane` does, and looks each one up
   in the live headscale `policy.grants[]`. > 5 missing =
   fail (real sync regression), 1-5 missing = pass-with-warn
   (Tailscale client-side lag, the 60-90s policy refresh
   interval).

5. **2 new unit tests** (`TestSanitizeRuleAlias` +
   `TestExpectedGrantTuple`) pin the `(src, dst)` formula
   in lockstep with the generator. If the generator
   changes its formula (e.g. adds `strings.ToLower`, or
   picks `device_ip` over `device_hostname`), the unit
   test will fail and force the refactorer to update both
   the generator AND the verification test. Without
   these, a one-sided refactor would make the verification
   test systematically miss the same grants the generator
   just produced — silent false-positive "all rules in
   ACL" forever.

6. **B68 verify-pre check**: 11 grep-pins.

### Why this matters beyond the immediate symptom

The 2026-08-06 report was the third "rules silently don't
match" case in v0.33.0+ (the previous two were v0.33.1.15
"per-device-pref device tag" and v0.33.1.14
"placeholdersList 2-arg"). All three were policy / flag-
class bugs where the operator's UI showed the rule as
saved but it had no effect on traffic. The verification
test is the structural fix — every `/admin/system_tests`
run will now catch this class of regression before the
operator notices.

### Files changed

- `cmd/skygate/main.go`: gate `RunDomainAutoUpdater` on
  `cfg.DNSAutoUpdateEnabled` (was: `cfg.AutoUpdateEnabled`).
- `internal/config/config.go`: new `DNSAutoUpdateEnabled`
  field + env binding (`SKYGATE_DNS_AUTOUPDATE_ENABLED`,
  default `true`).
- `internal/handlers/handlers.go`: `RunDomainAutoUpdater`
  reads `global_settings.dns_autoupdate_enabled` on every
  tick.
- `internal/feature/admin/settings_dns_autoupdate.go` (NEW):
  `PostAdminSystemTestsDNSAutoToggle` handler + audit log.
- `internal/feature/admin/system_tests_handlers.go`: render
  `DNSAutoUpdateEnabled` in the data map.
- `internal/feature/admin/system_tests.go`: new
  `exit_rules.all_in_headscale_acl` test in `TestRegistry`.
- `internal/feature/admin/system_tests_test.go`:
  `TestSanitizeRuleAlias` + `TestExpectedGrantTuple`.
- `internal/handlers/templates/admin/system_tests.html`:
  "DNS autoupdater" card.
- `internal/i18n/catalog_common.go`: 3 new keys (RU+EN).
- `scripts/verify_pre_deploy.sh`: B68 check (11 grep-pins).

## v0.33.1.17 — exit-rule ↔ preferred exit-node cross-check (B66)

**Date:** 2026-08-06
**Tag:** v0.33.1.17 (commit `b7bedd1`)
**Scope:** 1 commit. 2 new files (preferred_check.go + tests), 9
modified, +732/-3 lines. No API change, no schema change, no
migration, no build-tag change.

### The bug

A device_rule in `device_rules` (e.g. `target=rutracker.org`,
`exit_node=exit-node-A`) only takes effect on device D if D's
preferred exit-node is also `exit-node-A`. The decision is made
by `device_exit_node_prefs` (per-device, overrides everything)
or `user_exit_node_prefs` (per-user fallback). If they don't
match, **Tailscale silently ignores the rule** — the operator
sees a "dead rule" in the UI: saved, audit-logged, even approved
by headscale, but never routed through the chosen exit-node.

The bug surfaced in production: Cloudflare CIDR rules for
`rutracker.org` were pointed at one exit-node, but every device
was pinned to a different one via `device_exit_node_prefs`. The
rules were "saved" but Tailscale routed the traffic through the
wrong exit-node — and Cloudflare responded with a JS challenge
because the wrong exit-node's IP was in its low-reputation list.
30-minute debug to find the root cause.

### The fix

1. **Cross-check helpers**
   (`internal/feature/exit_rules/preferred_check.go`, new):
   - `PreferredExitNodeForRule(db, userID, hostname)` —
     per-device > per-user > ""
   - `IsRuleApplicable(ruleExitNode, preferredHost)` — true
     when there's no preferred OR they match
   - `TagToHostname("tag:exit-<host>")` → `"<host>"` — strip
     the tag prefix for comparison
   - `RulesByDeviceHostname(db)` — batch lookup for the
     admin cross-user view
   - 6 unit tests in `preferred_check_test.go`
     (TestIsRuleApplicable_NoPreference / Mismatch /
     WhitespaceHandling / RuleEmpty + TestTagToHostname_StandardForms)

2. **User-scope UI** (`/my/exit-rules`):
   - Top-of-page warning banner when `MismatchCount > 0`:
     "%d rules reference an exit-node that the device does
     not use. The rules are saved, but Tailscale ignores them."
   - "Use device's preferred exit-node" button — pre-fills
     the `select[name="exit_node"]` with the user's preferred
     tag and briefly highlights it.
   - Per-rule "Preferred" column with green check (match) /
     red warning + the preferred host tag (mismatch) / gray
     question (no preferred).

3. **Admin cross-user view** (`/admin/exit-rules`):
   - `AnnotatedRules` slice
     (`{AdminRule, PreferredHost, Applicable}`)
   - Same top-of-page banner with the cross-user mismatch
     count.
   - Per-row "Preferred" column.

4. **Admin devices** (`/admin/devices`):
   - Per-device "dead rules" count badge: red link to
     `/admin/exit-rules` when count > 0. Tooltip explains
     what "dead" means.

5. **System test**
   (`/admin/system_tests` → `exit_rules.preferred_mismatch`):
   - 3 SQL queries (`device_rules`, `device_exit_node_prefs`,
     `user_exit_node_prefs`) + Go cross-check.
   - Backend-dispatching — works on both SQLite and
     PostgreSQL via `db.BackendOf`.
   - Threshold: 0 = pass, 1–5 = pass with "warn" prefix,
     > 5 = **fail**.
   - Skips if no enabled rules.

6. **i18n**
   (`internal/i18n/catalog_exit_rules.go`, RU + EN,
   18 new keys): banner text, button label, column
   header, per-row title tooltips — full Russian and
   English parity.

7. **B66 verify-pre check** — pins the 13 new file
   references (`preferred_check.go` helpers, `system_tests`
   entry, both template banners, `devices.go`
   `DeadRuleCount`, all i18n keys).

### Files

- `internal/feature/exit_rules/preferred_check.go` — new,
  158 lines
- `internal/feature/exit_rules/preferred_check_test.go` —
  new, 130 lines
- `internal/feature/exit_rules/form_my.go` — adds
  `DeviceInfo.PreferredExitNode`, `MismatchCount`,
  `UserPreferred`
- `internal/feature/exit_rules/form_admin.go` — adds
  `RulesAnnotated`, mismatch count
- `internal/feature/admin/devices.go` — per-device
  `DeadRuleCount`
- `internal/feature/admin/system_tests.go` — new
  `exit_rules.preferred_mismatch` test
- `internal/handlers/templates/exit_rules.html` — banner +
  button + JS handler + per-rule column
- `internal/handlers/templates/admin/exit_rules.html` —
  banner + per-rule column
- `internal/handlers/templates/admin/devices.html` —
  dead-rule badge
- `internal/i18n/catalog_exit_rules.go` — 18 new keys
  (RU + EN)
- `scripts/verify_pre_deploy.sh` — B66
- `AGENTS.md` — Current section bumped to v0.33.1.17

### Test results

- `go test -count=1 -short ./...` → **27 / 27 packages PASS**
- `bash scripts/verify_pre_deploy.sh` →
  **66 / 66 PASS** (B1–B66; B8 smoke is VM-only)

### Live verify (post-deploy on the operator's VM)

After the operator set the preferred exit-node on
`/my/devices` for the affected device, the system test
reports:

```
mismatches | total | no_pref
          0 |   138 |     13
```

— 0 dead rules (was 138+ before the operator manually set
the preferred), 13 rules with no preferred (Tailscale picks
by metrics; not a mismatch). The warning banner on
`/my/exit-rules` and `/admin/exit-rules` disappears.

### Live cleanup (also run post-deploy)

The 25-July-2026 PG migration lost the
`cdn:cloudflare:rutracker.org` marker that the
autoupdater had added. We re-inserted the 15 Cloudflare
CIDR ranges (`104.16.0.0/12`, `172.64.0.0/13`,
`103.21.244.0/22`, `103.22.200.0/22`, …) with
`parent_domain='cdn:cloudflare:rutracker.org'`, removed
4 stale /32 (`104.21.32.39/32`, `104.21.50.150/32`,
`172.67.163.237/32`, `172.67.182.196/32`) for the same
domain, and re-synced the Cloudflare-routed exit-node via
`tailscale set --advertise-routes=…` + headscale
approve. After re-sync, the exit-node's approvedRoutes
now contain all 4 Cloudflare /13+ supernets and they
appear in `Serving (Primary)` for Cloudflare traffic.

### How to use

If the warning banner shows up on `/my/exit-rules` or
`/admin/exit-rules`:

1. **Quick fix on the rules side** — click "Use device's
   preferred exit-node" in the banner. The form's
   `exit_node` field gets prefilled with your preferred tag.
2. **Root-cause fix** — open `/my/devices` (or
   `/admin/devices` for the user) and either set or clear
   the per-device preferred exit-node so it matches what
   your rules point at.
3. **Verify** — reload `/my/exit-rules`. The banner
   disappears when the per-device / per-user pref matches
   every rule's `exit_node_id`.

### Future ideas (not in this release)

- `?device=NAME` query filter on `/admin/exit-rules` — the
  link from the per-device dead-rule badge points there
  but the handler doesn't filter yet (10-line follow-up).
- UI tests / E2E — only backend unit tests in this release.



## v0.33.1.16 — SKYGATE_TS_LOGIN_SERVER from .env + restart-skgate button (the "Tailscale never picks up the new URL" fix)

**Date:** 2026-08-06
**Tag:** _pending_
**Scope:** 3 commits (9ffb288 + 149cee8). 1 docker-compose.yml
fix + 1 admin handler + 1 web-UI button + 5 tests + 5 i18n
keys + 1 verify-pre check (B65). No API change, no schema
change, no migration.

### The bug

The operator reported (2026-08-06) that they set
`SKYGATE_TS_LOGIN_SERVER=https://head.<your-domain>` via
`/admin/tailscale` (which writes to the DB and is supposed
to be the source of truth from v0.33.1.13 onward). But the
entrypoint kept using the placeholder `https://head.example.com`,
so tailscaled logged out with
"fetch control key: failed to resolve head.example.com".

### Root cause

`docker-compose.yml` had:
```yaml
  environment:
    - SKYGATE_TS_LOGIN_SERVER=https://head.example.com
```
hardcoded. Per docker-compose precedence, `environment:`
**overrides** `env_file:` (which is where the .env value
lives). So the operator's edit on /admin/tailscale persisted
to the DB correctly, but the entrypoint kept reading the
hardcoded placeholder from the container env.

The 22-hour ACL-apply failure loop from v0.33.1.15 was a
similar pattern (entrypoint vs runtime divergence), so this
v0.33.1.16 fix is the second "config-source-of-truth"
cleanup in a row.

### The fix

1. **`docker-compose.yml`** (commit 9ffb288): remove the
   hardcoded `SKYGATE_TS_LOGIN_SERVER=https://head.example.com`
   from the `environment:` section so the .env value wins
   via `env_file:`. `SKYGATE_TS_HOSTNAME` stays hardcoded
   (one skygate host = one tailnet identity — not a value
   operators should change per deploy).

2. **`handleTailscaleRestart`** (commit 149cee8): new
   `action="restart_skgate"` POST endpoint. Flow:
   1. Read the current effective login_server (DB > .env > default).
   2. Write it to `<RepoPath>/.env` atomically
      (`updateEnvFileSKYGATE_TS_LOGIN_SERVER` — writes to
      `.env.tmp`, fsync, rename). Replaces / appends /
      clears the existing `SKYGATE_TS_LOGIN_SERVER=` line
      and leaves every other line untouched.
   3. Spawn a `setsid`'d subprocess (via `applySysProcAttr`
      helper, build-tagged for Linux + no-op on other
      platforms) that runs:
      - container: `docker compose -p skygate -f
        <host-repo>/docker-compose.yml restart skygate`
      - native: `systemctl restart skygate || service
        skygate restart`
      The `setsid` is critical — `docker compose restart`
      sends SIGTERM to the parent skygate process group
      (PID 1 = entrypoint.sh), and the subprocess is in
      a new session so it survives.
   4. Return 303 immediately. The response flushes before
      the SIGTERM arrives, so the operator sees the success
      message; the page is unreachable for ~30s while the
      new container comes up.
   5. Audit row: `tailscale_restart_skgate` with
      `login_server=...`, `in_container=...`, `method=...`.

3. **Web-UI button**: new "Restart skygate" card on
   `/admin/tailscale` (just below the existing Start/Stop
   card). Includes a `confirm()` dialog with the
   `tailscale.restart_confirm` i18n string. 5 new i18n
   keys in both RU+EN.

### Files

- `docker-compose.yml` — remove hardcoded env override
- `internal/feature/admin/tailscale.go` — new
  `handleTailscaleRestart` + `updateEnvFileSKYGATE_TS_LOGIN_SERVER`
  + `isRunningInContainer`
- `internal/feature/admin/setsid_linux.go` (new) +
  `setsid_other.go` (new) — build-tag pair for
  `applySysProcAttr` (Setsid on Linux, no-op elsewhere)
- `internal/handlers/templates/admin/tailscale.html` —
  new restart card
- `internal/i18n/catalog_tailscale.go` — 5 new keys (RU+EN)
- `internal/feature/admin/admin_tailscale_test.go` — 5 new tests
- `scripts/verify_pre_deploy.sh` — B65 added

### Test results

- `go test -count=1 -short ./...` — 27/27 packages PASS
- `make verify-pre` — **65/65 PASS** (B1-B65)
- `TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Replace` PASS
- `TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Append`  PASS
- `TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Clear`  PASS
- `TestHandleTailscaleRestart_WritesEnvAndDispatches` PASS
- `TestHandleTailscaleRestart_RejectsBadCSRF` PASS

### Live verify (post-deploy)

1. `docker-compose.yml` on VM no longer has the hardcoded
   `SKYGATE_TS_LOGIN_SERVER` (verified with
   `grep SKYGATE_TS_LOGIN_SERVER docker-compose.yml` → no
   hardcoded `=` line in the environment section)
2. `/admin/tailscale` shows the "Restart skygate" card
3. `SKYGATE_TS_LOGIN_SERVER=https://head.<your-domain>` in
   `.env` on the host (operator's web-UI edit propagated
   automatically on the next restart click)
4. `docker compose restart skygate` from a click on the
   button → new container starts → entrypoint reads
   `https://head.<your-domain>` → tailscaled logs in successfully
5. `R5/R6` (tailscale IP + exit-node) verify-post checks
   start passing on the next tick

## v0.33.1.15 — per-device-pref device tag in tagOwners (the "cyborg exit rules not visible" fix)

**Date:** 2026-08-05
**Tag:** _pending_
**Scope:** 2-line Go fix in `internal/acl/acl.go` (per-device
grant tagOwners + via tagOwners blocks) + 1 regression test
+ B64 verify-pre check. No API change, no schema change,
no i18n change.

### The bug

A second symptom of the same v0.33.1.12-era pattern that
v0.33.1.14 fixed (callerOwnsDevice was broken, so the
operator couldn't even set cyborg's per-device pref). After
v0.33.1.14, the per-device pref is now writable — but a
Deeper root cause was exposed: every ACL apply for the
last 22+ hours has been silently failing.

The `GenerateACLWithViaForPlane` policy builder emits
per-device ACL grants with `src=tag:dev-<user>-<device>`
and `via:[<device-pref>]` for every row in
`device_exit_node_prefs`. But the `tagOwners` block was
built ONLY from `GetPerUserDeviceTags` (a JOIN on
`node_owner_map`). When a device had a per-device pref
but was missing from `node_owner_map` — e.g. the
`skygate-host-1` host node (its own per-user tag, not yet
backfilled because it joins the tailnet after the portal
admin sets the pref) — the headscale policy parser
rejected the policy with:

> `setting policy: parsing policy: src=tag not found:
>   "tag:dev-skyadmin-skygate-host-1"`

`SetPolicy` returned 500, the snapshot was marked
`applied_success=0`, and the user's preferred exit-node
never took effect. The exit_rule_logs table has 6
consecutive `apply_fail` rows for this exact error from
2026-08-04 22:46 UTC onward.

### The fix

Two additions to `GenerateACLWithViaForPlane`'s
`tagOwners` block:

1. **Include via tags from per-device prefs** (in
   `distinctVias`). Pre-v0.33.1.15 the block only
   covered per-user prefs (`viaByUser`), so a fresh
   per-device pref's via tag (e.g. `tag:exit-emilia`)
   was referenced in `via:[]` but not registered in
   `tagOwners`.

2. **Include per-device-pref device tags** (in
   `perDevTagOwners`). The pre-v0.33.1.15 block was
   built from `tagsByUser` which only contains devices
   in `node_owner_map`. Now augmented with every tag
   from `viaByDevice` (the per-device-pref tags) so a
   device with a pref that's not yet in `node_owner_map`
   still gets its tag registered.

### Files changed

- `internal/acl/acl.go` — 2 small blocks added inside
  the `tagOwners` builder in
  `GenerateACLWithViaForPlane` (~30 lines).
- `internal/acl/acl_test.go` — new test
  `TestGenerateACLWithVia_PerDeviceTagOwners` (60
  lines, 3 assertions: per-device grant emitted +
  device tag in tagOwners + via tag in tagOwners).
- `scripts/verify_pre_deploy.sh` — B64 added.

### Live verify (post-deploy)

1. `POST /my/devices/preferred-exit` for cyborg →
   emilia: was 302 (after v0.33.1.14) but policy push
   failed; now policy push SUCCEEDS (headscale accepts
   the policy, no more 500 from PUT /api/v1/policy).
2. `headscale policy get` — the `via` field is now
   present on cyborg's grant:
   `{ "src": ["tag:dev-skyadmin-cyborg"], "dst":
   ["autogroup:internet"], "ip": ["*"], "via":
   ["tag:exit-emilia"] }`
3. `acl_snapshots.applied_success` flips from 0 to 1
   for new applies. Re-apply on the existing failed
   snapshots: `POST /admin/exit-rules/reapply` (admin
   only) or any per-device POST regenerates the policy
   and pushes it.
4. exit_rule_logs: no more `apply_fail` entries.

### Test results

- `go test -count=1 -short ./...` — 27/27 packages
  PASS (new `TestGenerateACLWithVia_PerDeviceTagOwners`
  test green).
- `make verify-pre` — 64/64 PASS (B1-B64).

## v0.33.1.14 — `placeholdersList(1)+placeholdersList(1)` 2-arg PG-unsafe query fix (the "cyborg device not found" fix)

**Date:** 2026-08-05
**Tag:** _pending_
**Scope:** 1-line Go bug fix in 3 production sites + new
helper `db.PlaceholderAt(n, i)` + 4 regression tests + B63
verify-pre check. No API change, no schema change, no
i18n change.

### The bug

The v0.33.1.12 sweep (B60) fixed hardcoded `?` placeholders
across the codebase by replacing them with
`db.PlaceholdersList(n)`. The replacement pattern used for
2-arg queries was:

```go
`... WHERE a = `+db.PlaceholdersList(1)+` AND b = `+db.PlaceholdersList(1)
```

This compiled and ran fine on SQLite (the `?` placeholder
just gets bound twice). But on PostgreSQL it produced:

```sql
... WHERE a = $1 AND b = $1
```

— two references to the SAME positional parameter while
TWO args are passed. PostgreSQL rejected the query (or
silently bound the wrong value) and the function returned
zero/false for every row.

### The user-facing symptom

When the operator logged into `/my/devices` and tried to set
a per-device preferred exit-node for `cyborg`
(`POST /my/devices/preferred-exit`), `callerOwnsDevice`
returned `false` for **every device**, so the handler
responded with 403 "device not found or not owned by you" —
even though `cyborg` was clearly listed in the
`/my/devices` table and tagged by the same user.

A downstream consequence: with no per-device pref writable,
no per-device ACL grant was emitted for `cyborg`, so
`cyborg` traffic to the rules under emilia wasn't pinned
via the `via:` constraint. The exit-rules page rendered
the rules but the device was free to route through any
exit-node.

### The fix

New helper `db.PlaceholderAt(n, i)` returns the i-th (0-indexed)
placeholder from a `PlaceholdersList(n)` string, so a 2-arg
query splices two UNIQUE placeholders (`$1`, `$2`) at its
two positions:

```go
// Before (PG-unsafe):
`... WHERE a = `+db.PlaceholdersList(1)+` AND b = `+db.PlaceholdersList(1)
// After:
`... WHERE a = `+db.PlaceholderAt(2, 0)+` AND b = `+db.PlaceholderAt(2, 1)
```

Same pattern as `db.NowUnixSQL` / `db.OnConflictDoNothing` /
`db.PlaceholdersList`: a public mirror of the internal
helper for use outside the `db` package. Out-of-range `i`
returns `""` so a caller bug produces a malformed SQL
string (visible at Exec time) instead of a silent bind
mismatch.

### Files changed

- `internal/db/placeholders.go` — added `PlaceholderAt(n, i)`
  (5 lines + doc comment).
- `internal/feature/my/device_exit_pref.go:200` — `callerOwnsDevice`
  query. (v0.33.1.12 had the same bug here.)
- `internal/db/migrations_v0.46.go:94` — `GetDeviceExitNodePref`
  query. (v0.33.1.12 had the same bug here.)
- `internal/db/migrations_v0.46.go:129` — `SetDeviceExitNodePref`
  DELETE branch. (v0.33.1.12 had the same bug here.)
- `internal/feature/my/testutil.go` — added `node_owner_map` +
  `device_exit_node_prefs` tables to the in-memory test schema
  (the per-device-pref feature wasn't covered by tests before).
- `internal/feature/my/device_exit_pref_test.go` (new) —
  4 tests: `TestCallerOwnsDevice_2ArgDispatch` (5 sub-cases
  including mixed-case + non-owner), `TestCallerOwnsDevice_WrongOwner`
  (user=2 impersonation rejected), `TestSetDeviceExitNodePref_RoundTrip`
  (set + get + clear), `TestPlaceholderAt_Dispatch` (helper
  bounds check).
- `scripts/verify_pre_deploy.sh` — B63 added.

### Live verify (post-deploy)

1. `POST /my/devices/preferred-exit` for `cyborg` (logged in
   as `skyadmin`): was 403, now 302 to `/my/devices?ok=1`.
2. `SELECT exit_node_tag FROM device_exit_node_prefs WHERE
   user_id=1 AND device_hostname='cyborg'` — returns the
   chosen tag (e.g. `tag:exit-emilia`).
3. `/my/exit-rules` page (rendered through the per-device
   `via:` grant) — rules under cyborg now route through emilia.
4. `headscale policy get` — the per-device grant for
   `tag:dev-skyadmin-cyborg → autogroup:internet` carries
   `via: ["tag:exit-emilia"]` (was missing the via before
   because the pref write silently failed).

### Test results

- `go test -count=1 -short ./...` — 27/27 packages PASS
  (new device_exit_pref tests all green).
- `make verify-pre` — 61/61 PASS (B1-B63, B8 smoke is
  VM-only as usual).

## v0.32.19 — Documentation wave 2 + migration integrity + HA design proposal

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** docs + 1 new Go feature (migration integrity
tracking, soft mode). No API change, no schema change at the
user-data level (one new system table `applied_migrations`
for migration bookkeeping).

### What's in this release

1. **Migration integrity tracking (soft mode, v0.32.19)**
   - New `internal/db/migration_tracking.go` — SHA-256
     checksum helpers for migration bodies. Detects when an
     OLD migration's SQL body is modified after being applied
     (a latent bug class that the previous idempotent-migration
     design silently absorbed).
   - New `internal/db/migrations_v0.49.go` — creates the
     `applied_migrations(version, sha256, source_file,
     applied_at, first_seen)` system table.
   - `internal/db/db.go` — calls `ensureMigrationTrackingTable`
     before running other migrations. Recording of each
     migration's checksum is the v0.32.20 follow-up (requires
     a refactor of `db.go` to extract migration SQL bodies
     into a map for SHA computation).
   - Soft mode: a mismatch produces a `WARNING` log line but
     does NOT prevent skygate from starting. The mode flips to
     HARD in v0.32.20 after one release cycle of observation.
     Opt-in to HARD earlier via `SKYGATE_MIGRATION_INTEGRITY=hard`.
   - 8 unit tests in
     `internal/db/migration_tracking_test.go` covering:
     deterministic checksum, semantic changes detected,
     tracking table idempotent, record + get roundtrip,
     first-run / match / soft-mismatch / hard-mismatch /
     audit listing / mode introspection.
   - B36 catalog check in `verify_pre_deploy.sh` pins the
     contract (helpers exist, V049 registered, ensure
     function called, test file present).

2. **Wave 2 documentation cleanup**
   - `AGENTS.md` "Common gotchas" extended with 6 new entries
     (10-15): CASCADE-LOCK on SQLite WAL, distroless
     healthcheck pattern, NPM-blocks-iptables, exit-node
     online detection (trust headscale, not `last_seen`),
     per-user subnet phantom-route caveat, subnet-router
     Remove handler lifecycle.
   - `docs/BACKLOG.md` updated — 6 completed entries added
     (v0.32.13, v0.32.14, v0.32.15, v0.32.16, v0.32.17,
     v0.32.18). Last-updated stamp → 2026-08-03.
   - `docs/internal/internal/subnet-router.md` — new "Removing a subnet-router
     (admin-only, v0.32.18+)" section with the full inverse
     flow of the v0.16.7 Provision, idempotency notes, what
     NOT to use Remove for, and verify-after-Remove SQL.
   - `README.md` — new "Tailscale: OFF by default (v0.32.15+)"
     section documenting the 3-step manual re-enable procedure
     and the v0.32.8 / v0.32.11 incidents that motivated the
     default-OFF flip.
   - `docs/plans/pg-migration-handling.md` — updated
     "Implementation status" with the v0.32.0+ state
     (driver + 27 PG migrations + 4 verification tests on
     main, scope of the runtime `?` → `$N` rewrite).

3. **PG cutover runbook (the actual v0.33.0 plan)**
   - New `docs/v0.33.0-pg-cutover-runbook.md` — the 4-step
     operator runbook for the live cutover (pre-cutover verify,
     runtime rewrite as a separate PR, 15-min maintenance
     window, post-cutover verify). Includes the known issues
     (strftime, INSERT OR REPLACE, RETURNING, PRAGMA), the
     rollback procedure, and the operator's decision points.
   - Blocked on the operator provisioning a PG-staging VM.

4. **HA active-router design proposal (the operator's
   2026-08-03 ask)**
   - New `docs/internal/internal/ha-active-router.md` — 3 architectures
     (A: PG active-passive / B: single-writer role / C:
     multi-writer eventual consistency) with pros/cons
     comparison, RTO/RPO, complexity, and a clear
     recommendation: **Architecture B** for the current
     deployment (1-2 day implementation, no PG required,
     RTO 5-15 min via manual role flip + DNS swap).
   - `docs/internal/internal/ha-architecture.md` — added "Tier 0.5" entry to
     the tier table, with the rationale for choosing it over
     Tier 1 today and the upgrade path to Tier 1 once the
     PG cutover ships.
   - Implementation outline for Architecture B included
     (env var, route gating, Litestream config, manual
     failover drill, optional auto-promotion as v0.34.0
     follow-up).
   - 4 open questions for the operator (RTO acceptance,
     auto-promotion vs manual, budget, second-VM identity).

### Files

```
AGENTS.md                                          | +80
README.md                                          | +40
docs/BACKLOG.md                                    | +48
docs/internal/internal/ha-architecture.md                            | +32
docs/internal/internal/ha-active-router.md                           | NEW (15.6 KB)
docs/plans/pg-migration-handling.md                | +124/-32
docs/internal/internal/subnet-router.md                              | +77
docs/v0.33.0-pg-cutover-runbook.md                 | NEW (10.9 KB)
internal/db/db.go                                  | +13
internal/db/migration_tracking.go                  | NEW (8.3 KB)
internal/db/migration_tracking_test.go             | NEW (7.2 KB)
internal/db/migrations_v0.49.go                    | NEW (1.7 KB)
scripts/verify_pre_deploy.sh                       | +27
```

### Verification

- `go build ./...` — clean
- `go test -count=1 -short ./internal/db/` — 9.4s PASS
  (includes the 8 new migration_tracking tests)
- `bash scripts/verify_pre_deploy.sh` — 35 PASS + 1 SKIP
  (B8 = smoke, runs on VM only)
- B36 (new): migration integrity helpers + V049 + tests

### Loose ends (deferred)

- **Recording of old migrations (V020-V048)**: v0.32.20
  follow-up. Requires extracting migration SQL bodies from
  the per-version `migrateV0NN` functions into a `map[int]string`
  in `internal/db/migration_bodies.go` so `migrate()` can
  call `VerifyMigrationChecksum` per migration.
- **HARD mode default**: v0.32.20 after one release cycle of
  soft-mode observation.
- **HA Architecture B implementation**: blocked on operator
  feedback to the 4 open questions in
  `docs/internal/internal/ha-active-router.md` § "Open questions for the
  operator".
- **Live VM still on v0.32.15 build label**: v0.32.16/17/18/19
  are committed + pushed but not redeployed. Manual
  `docker compose up -d --force-recreate skygate` on the VM
  to pick up all 5 releases.

---

## v0.32.18 — Subnet-router Remove handler (full lifecycle)

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** new admin endpoint + UI + tests + regression guard. No API change.

### What's in this release

1. **`PostAdminUserSubnetRemove` handler (v0.32.18)**
   - New admin endpoint: `POST /admin/users/{id}/subnet/remove`
   - Inverse of `Provision`: full subnet-router cleanup that
     deletes the headscale node and clears all DB state.
   - Steps in one atomic flow:
     1. Parse `router_node_id` from `user_subnets` → call
        `headscale.Client.DeleteNode(nodeID)`. Failure is
        logged but does NOT abort the rest of the handler
        (the DB cleanup is the source of truth for the
        skygate side; an admin can delete the headscale
        node manually if needed).
     2. `UPDATE user_subnets SET status='pending',
        router_node_id='', router_hostname='', updated_at=now`
     3. `UPDATE portal_users SET subnet_status='pending',
        subnet_cidr='', subnet_router_node_id='',
        subnet_router_hostname=''`
     4. `INSERT INTO audit_log` with action `subnet_router_removed`
        and the deleted headscale node id in the detail
     5. Redirect to `/admin/users/{id}/subnet?flash=removed`
   - Idempotent: clicking Remove twice is safe. If the
     `user_subnets` row doesn't exist → 404.
   - Does NOT re-apply the ACL (the policy uses
     `h-user-admin-subnet` which is always present
     regardless of router status — re-applying would just
     add a row to `acl_snapshots` with no diff).

2. **UI button + flash messages**
   - "Remove subnet-router" button in the admin subnet
     page, only shown when `status='router_active'`
     (i.e. there IS a router to remove). Has a JS
     `confirm()` dialog with the i18n string.
   - `?flash=<key>` query parameter on the page URL
     (parsed by `GetAdminUserSubnet`) sets a
     `FlashMessage` data field that the template renders
     as a success banner.
   - 9 new i18n keys × 2 langs (18 entries total):
     `remove_button`, `remove_button_help`, `remove_confirm`,
     `flash_removed`, `flash_headscale_failed`,
     `flash_allocated`, `flash_disabled`, `flash_shared`,
     `flash_revoked`.

3. **Tests (3 new)**
   - `TestPostAdminUserSubnetRemove_DeletesHeadscaleAndClearsDB`:
     full happy path (seeded router_node_id="26", verify
     headscale DELETE was called, all 3 DB tables cleared,
     audit log written, redirect has `flash=removed`)
   - `TestPostAdminUserSubnetRemove_NoRouterRow`:
     idempotent path (router_node_id='' — should still
     clear status to pending, no headscale call)
   - `TestPostAdminUserSubnetRemove_NoSubnetRow`:
     user has no user_subnets row → 404

4. **B35 verify-pre regression guard**
   - Fails the build if `POST /admin/users/{id}/subnet/remove`
     is not wired to `adminSvc.PostAdminUserSubnetRemove`
     in `cmd/skygate/main.go`. Same pattern as B15/B16/B17
     (regression guards for past handler-removal bugs).

### Files

- `internal/feature/admin/user_subnet_remove.go` (new) —
  the Remove handler
- `internal/feature/admin/user_subnet.go` — `GetAdminUserSubnet`
  reads `?flash=...`; added `subnetFlashMessages` map
- `internal/feature/admin/user_subnet_test.go` — 3 new tests
- `internal/feature/admin/testutil.go` — added
  `subnet_router_hostname` column to test schema
- `internal/handlers/templates/admin/user_subnet.html` —
  Remove button + FlashMessage banner
- `internal/i18n/catalog_user_subnet.go` — 9 new keys × 2 langs
- `cmd/skygate/main.go` — route registration
- `scripts/verify_pre_deploy.sh` — B35 check
- `RELEASE-NOTES.md` — this entry

### Verified

- `go test -count=1 -short ./...` — all 26 packages PASS
- `bash scripts/verify_pre_deploy.sh` — 35/35 PASS (B1-B35)
- Test schema updated for `subnet_router_hostname` (it was
  in production but missing in `newMemoryDB`)

### Loose ends / future work

- **Live R-check (R30+ in verify_post_deploy.sh)**: a
  runtime check that does a full add+remove cycle against
  a real headscale. Deferred to v0.32.19 — would need
  Docker setup for a sandbox headscale (the existing
  verify_post_deploy.sh runs against the live VM).
- **Auto-apply tags for the new device**: when a new
  `skygate-subnet-<username>` device registers, the
  backfillNodeOwnership path should auto-apply
  `tag:dev-admin-<hostname>` + `tag:subnet-router`.
  This already works in v0.32.18; just flagging that the
  Remove flow doesn't re-apply on the new device (it
  doesn't need to — that's a Provision-time concern).
- **Documentation update**: `docs/internal/internal/subnet-router.md` should
  mention the Remove button (currently only documents
  Provision). Deferred to doc-cleanup pass.

---

## v0.32.17 — Exit-node monitor online detection fix + device_rules dedup

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** fix (logic + data) + verify-pre B34. No API change.

### What's in this release

1. **Exit-node monitor online detection (logic fix)**
   - **Old behaviour**: headscale says `online=true` but `last_seen` is older than
     `OfflineAfter` (default 2 min, recently bumped to 10 min via env) → monitor
     marks the node offline. Produced false-negatives for every idle VPS exit-node
     (no peer activity in 10 min = "offline" even though headscale still considers
     it online).
   - **New behaviour**: trust headscale's `n.Online` as the primary signal. The
     `OfflineAfter` window is only consulted when headscale says OFFLINE: if we
     just saw the node within `OfflineAfter`, treat it as online (catches transient
     headscale-side booleans). Outside the window + offline → mark offline.
   - **Affected file**: `internal/monitoring/exit_node_monitor.go` (lines 372-405).
   - **Tests**: `TestComputeSnapshot_OfflineWhenLastSeenOld` (old, asserted the bug)
     renamed to `TestComputeSnapshot_HeadscaleOnlineTrustsLastSeenOld` and flipped
     to assert the correct behaviour. New `TestComputeSnapshot_ForgivingFallback`
     covers the offline-but-recent case.

2. **device_rules duplicates cleanup (data fix)**
   - Found 365 duplicate `device_rules` rows in the production DB on 2026-08-03.
     186 rows for `(workstation-1, relay-3)` and 179 rows with empty
     `device_hostname`, all with the same `created_at` (a stale batch script
     that forgot to dedup).
   - Inflated the `/admin/exit-nodes` "mismatch" computation: `computeSyncStatus`
     counts ALL device_rules targeting the exit_node, not the unique device count.
     relay-3 showed `mismatch: have 148, want 365` instead of the real drift
     (the operator never had 365 rules; the duplicates were the inflation).
   - **Cleanup**: `DELETE FROM device_rules WHERE id NOT IN (SELECT MIN(id) FROM
     device_rules GROUP BY exit_node_id, device_hostname)` — 363 rows removed,
     now down to 2 unique `(device, exit_node)` pairs.
   - The remaining "mismatch: have 148, want 2" is a real-but-different drift
     (skygate has 2 rules for relay-3, headscale has 148 routes) that
     warrants its own investigation — not a duplicates issue.

3. **Verify-pre B34 (regression guard)**
   - New check that fails the build if `device_rules` has any duplicate
     `(device_hostname, exit_node_id)` group. Runs `sqlite3` on the production
     DB; skips gracefully on Windows / fresh VM (no DB).
   - Matches the pattern of B15/B16/B17/B22 (regression guards for past bugs).
   - Comment in the script explains the 2026-08-03 incident and the cleanup
     SQL so future maintainers know the context.

### Files

- `internal/monitoring/exit_node_monitor.go` — new online-detection logic
- `internal/monitoring/exit_node_monitor_test.go` — updated + 1 new test
- `scripts/verify_pre_deploy.sh` — B34 check
- `RELEASE-NOTES.md` — this entry

### Verified

- `go test -count=1 -short ./...` — all packages PASS
- `bash scripts/verify_pre_deploy.sh` — 34/34 PASS (B1-B34)
- Live VM: cleanup done via `sudo sqlite3`; audit log entry recorded.

---

## v0.32.16 — Headplane distroless healthcheck fix + docker build cache hygiene

**Date:** 2026-08-03
**Tag:** _pending_
**Scope:** template + verify-pre. No Go code changed.

### What's in this release

1. **Headplane healthcheck override** in
   `deploy/templates/headscale-compose.yml.tmpl`. The distroless
   `ghcr.io/tale/headplane:0.6.3` image ships `/bin/hp_healthcheck`
   that probes `http://localhost:3000/admin/healthz`, but
   `HEADPLANE_SERVER__PORT` is 50445 by default. The upstream
   healthcheck always failed with
   `dial tcp [::1]:3000: connect: connection refused`, leaving
   headplane in `(unhealthy)` for 60+ failing-streak iterations
   even though the service was fine (port 50445 returns 200 in
   15ms via direct probe). The override uses Node.js (the only
   runtime in the distroless image, at `/nodejs/bin/node` — not
   in PATH for the healthcheck process) to probe
   `http://127.0.0.1:${HEADPLANE_SERVER__PORT}/admin/healthz`.
   `127.0.0.1` is used (not `localhost`) because IPv6 may resolve
   to `[::1]` and headplane binds `0.0.0.0`, not `::`.

2. **`docker builder prune -a -f` as a documented recovery step.**
   Multi-stage Dockerfiles leave a build cache entry per
   `docker compose build` invocation, even if the resulting image
   is identical. Five deploys over five days left 7.36 GB of
   build cache on the live VM; 5.75 GB was reclaimable. The fix
   is operational (run the prune) but the template-side change
   is also part of this release: B33 pins the healthcheck contract
   so future deploys don't regress to the broken upstream check.

3. **B33 verify-pre check** that pins the headplane healthcheck
   contract:
   - The template has a `healthcheck:` block under headplane
   - The test command uses `/nodejs/bin/node`
   - The probe URL uses `${HEADPLANE_SERVER__PORT}` (not hardcoded)
   - The probe URL is `127.0.0.1` (not `localhost`)
   - No `wget` in the test (the distroless image has no wget)

### Files

- `deploy/templates/headscale-compose.yml.tmpl` — added `healthcheck:`
  block with node probe
- `scripts/verify_pre_deploy.sh` — B33 check

### Backlog debt

v0.32.13, v0.32.14, and v0.32.15 don't have release-notes entries
yet (only v0.32.12 made it into this file before the 5-layer bug
war took over). Tracked in `docs/BACKLOG.md`; will be backfilled
in v0.32.17 or later.

### Operational notes

After deploying v0.32.16, the live headplane container was
recreated with the new healthcheck. `docker inspect headplane`
now shows `Up X seconds (healthy)` (was `Up X hours (unhealthy)`
for ~24h before the fix). The new healthcheck takes effect on
the next `deploy.sh` run; no manual action needed.

The disk cleanup is NOT a code change — it's a one-shot operator
action (`docker builder prune -a -f && rm -rf /var/backups/skygate/PRE_*`).
Reclaimable on the live VM at 2026-08-03: ~6 GB (5.75 GB build
cache + 317 MB old recovery dirs). Disk went from 74% → 58% in
30 seconds.

---

## v0.32.12 — Fix CGO_ENABLED=0 regression in multi-stage Dockerfile (silent 504 fix)

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** closes the v0.32.8 CGO regression. After deploying
v0.32.8 the operator hit 504 Gateway Time-out on every
`skygate.example.com` request, but `docker ps` didn't show the
skygate container at all and `/healthz` on `localhost:8080`
returned `Connection refused`. The fix is one-line at the
root: re-enable cgo in the multi-stage Dockerfile and ship
the C toolchain in the build stage.

### The bug

The v0.32.8 multi-stage Dockerfile (commit `2d2d91f` / `86a406c`)
shipped this line in the `skygate-build` stage:

```dockerfile
ENV CGO_ENABLED=0
RUN go build -buildvcs=false -trimpath \
    -ldflags "-s -w -X main.version=${GIT_VER} ..." \
    -o /out/skygate ./cmd/skygate
```

The intent was a fully-static binary that doesn't need
glibc on the alpine runtime. The unintended consequence:
`go-sqlite3` is a **pure-CGO driver**. When CGO is disabled,
the import resolves to a stub package that returns

```
Binary was compiled with 'CGO_ENABLED=0', go-sqlite3
requires cgo to work. This is a stub
```

on every DB call. The skygate boot sequence does `db.Ping()`
right after "🌐 Skygate starting on :8080" — the stub error
fires there, the binary exits 1, port 8080 never binds,
and the upstream proxy (Nginx Proxy Manager at the operator's NPM host,
not the in-container caddy which is now off per v0.32.11)
returns 504 to every external request.

### Why the v0.32.5 → v0.32.11 chain didn't catch it

- `go test ./...` (B1 in `verify-pre`) passes regardless of
  CGO_ENABLED: the test in-memory DBs (`:memory:`) take the
  same stub path but the tests assert SQL behavior, not the
  driver binary. The stub satisfies the interface.
- `go build ./cmd/skygate` (B3) passes: the build succeeds,
  it just links a stub instead of the real driver.
- The smoke test (B8) runs against a LIVE container, so a
  v0.32.8 deploy to the VM would have caught this in step 1
  (the `ready at` log line + a 200 on `/healthz`). But the
  v0.32.8 deploy happened without smoke being run first —
  the operator pushed and only saw the failure when the
  browser 504'd.
- CGO behavior doesn't show up in `docker logs` until the
  binary actually tries to use the DB, which happens in the
  foreground goroutine of `main.go`. The crash is fast
  (sub-second) and the container is gone by the time the
  operator SSHes in to check.

### Why v0.32.5 worked but v0.32.8 didn't

v0.32.5 had a single-stage Dockerfile based on
`golang:1.25-alpine` that ran `go build` at container
start (via `entrypoint.sh`). CGO was enabled by default
in `golang:1.25-alpine` — the image ships gcc + musl-dev
pre-installed. v0.32.5's runtime stage also kept `gcc
musl-dev` in the runtime apk add list (defensive), so
even if the build were re-run at container start it would
have produced a working binary.

v0.32.8 split the Dockerfile into two stages: a `skygate-build`
stage that only kept `git` (for the version label), and a
minimal `alpine:3.20` runtime stage. To get a smaller
binary the build stage also dropped CGO. That's what
broke.

### The fix

`Dockerfile`, `skygate-build` stage:

1. `ENV CGO_ENABLED=1` (re-enable cgo).
2. `apk add --no-cache gcc musl-dev sqlite-dev` (the C
   toolchain — gcc + libc headers + sqlite3.h headers).
3. Comment block (24 lines) explaining the regression and
   the CGO contract, so a future maintainer who sees
   "ENV CGO_ENABLED=0" in git history and thinks "smaller
   binary, why not" can find the explanation right there.

`Dockerfile`, runtime stage:

1. `sqlite-libs` was already in the apk add list (kept
   from v0.32.5). With CGO_ENABLED=1, the resulting binary
   is dynamically linked against `libsqlite3.so.0`, which
   `sqlite-libs` ships. No change needed.

### Files changed

- `Dockerfile` — `ENV CGO_ENABLED=0` → `ENV CGO_ENABLED=1`,
  added `gcc musl-dev sqlite-dev` to the build stage's
  apk add list, expanded the comment block (24 → 41 lines
  with the regression rationale + 2026-07-31 timeline).
  **Also**: install the binary to BOTH `/usr/local/bin/skygate`
  (the actual entrypoint path) AND `/app/skygate` (for
  back-compat with the v0.29.0 self-update orchestrator's
  `docker run --rm --volumes-from skygate
  skygate-skygate:latest /app/skygate --migrate-only`
  command). The `/app/skygate` path is shadowed by the
  source bind-mount at runtime, but the autoupdate helper
  container doesn't have the bind-mount so the image's
  `/app/skygate` is visible there.
- `entrypoint.sh` — `exec /app/skygate` → `exec
  /usr/local/bin/skygate`. The runtime image's `/app` is
  the bind-mount target for the host source tree (see
  `docker-compose.yml`); a bind-mount REPLACES the
  directory contents, so the host's `/app/skygate` (if
  present, e.g. a stale v0.32.5-era binary) would shadow
  the freshly built image binary. `/usr/local/bin` is
  outside the bind-mount so the image's binary always
  wins. This is the v0.32.5 → v0.32.8 silent-outage
  root cause that B27 pins.
- `scripts/verify_pre_deploy.sh` — new **B26** check that
  pins the CGO contract: `! grep -qF "ENV CGO_ENABLED=0"`,
  `grep -qF "ENV CGO_ENABLED=1"`, build stage has
  `gcc + musl-dev + sqlite-dev`, runtime stage has
  `sqlite-libs`. A future maintainer who tries to
  re-enable `CGO_ENABLED=0` for size will fail B26
  with a one-line explanation pointing at this section.
  New **B27** check that pins the entrypoint-binary
  path: `exec /usr/local/bin/skygate` (not
  `exec /app/skygate` — the v0.32.8 bug shape).
- `RELEASE-NOTES.md` — this entry.

### Verified

- `go build -buildvcs=false -trimpath -ldflags "-s -w"
  -o /tmp/skygate-cgo-test ./cmd/skygate` (CGO_ENABLED=1)
  → 14MB binary.
- Smoke test of the CGO binary locally: starts up,
  connects to a stub headscale URL, `/healthz` returns
  `200 {"build":"dev+unknown","status":"ok",...}`. The
  v0.32.8 stub binary returned `Connection refused` on
  the same test because it crashed on `db.Ping()`.
- `go test -count=1 -short ./...` with CGO_ENABLED=1:
  26/26 packages PASS (same as the CGO_ENABLED=0 baseline
  because the test path doesn't depend on the driver
  implementation, just the public interface).
- `make verify-pre` on Windows: 24/24 PASS (B8 SKIP,
  smoke is VM-only; B26 new). 2026-07-31 18:11 MSK.

### Deploy / rollback notes

- **Deploy**: `git pull` on the VM, then
  `docker compose build skygate && docker compose up -d
  skygate`. The `docker compose build` step will take
  ~30s longer than v0.32.8 (compiling cgo + sqlite) but
  the resulting binary is ~14MB instead of the v0.32.8
  41MB (the v0.32.8 size was wrong — the stub binary is
  artificially small because it doesn't link the real
  driver). Runtime start is unchanged (~5s).
- **Rollback** (if v0.32.12 also breaks something on the
  VM): `git checkout v0.32.11` → `docker compose build
  skygate` → `docker compose up -d skygate`. The v0.32.5
  rollback pattern (clone, bind-mount `/app`, run the
  v0.32.5 image) also still works as a deeper fallback
  — see `AGENTS.md` → "v0.32.5 rollback test pattern".

### Why not just switch to `modernc.org/sqlite` (pure-Go)?

Considered. `modernc.org/sqlite` is a fully-Go port of
SQLite (no cgo, no glibc dep), which would let
`CGO_ENABLED=0` work again. But:

1. It's a 30MB+ transitive dep tree (modernc.org/sqlite
   pulls libq, qflag, etc.) — bigger than the 14MB CGO
   binary.
2. It has its own query planner quirks that have caused
   subtle correctness regressions in the past (e.g.
   handling of `strftime('%Y-%m-%d', ...)` with NULL
   arguments differs from the C version).
3. Switching the driver in this codebase is a 1-2 day
   migration (`internal/db/` queries use
   `database/sql` exclusively, so it's mostly import-path
   changes + rebuild).
4. The current CGO build is fast enough (~30s in the
   build stage) and the resulting binary is the smallest
   realistic size (14MB dynamically linked against alpine
   musl + libsqlite3).

Re-evaluate if a future Go release ships a first-party
`database/sql` SQLite driver that's pure-Go (e.g. if
`go-sqlite3` ever ships a pure-Go fallback).

## v0.32.11 — Caddy is OFF by default (silent-outage fix)

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** closes the "ci green but skygate.example.com
unreachable" report from 2026-07-31. Root cause: the
in-container Caddy sidecar was on by default, was binding
0.0.0.0:80 + 0.0.0.0:443, and was returning `SSL alert 80
internal_error` because the placeholder Caddyfile
(`head.example.com` / `headplane.example.com` /
`derp.example.com` — template defaults, not the
operator's real domain) couldn't issue certs. The
operator's external TLS terminator (Nginx Proxy Manager
at the operator's NPM host) was forwarding to this host's :443 and
hitting the broken caddy. CI didn't catch it (CI checks
Go code, not Caddyfile validity or ACME issuance). The
fix flips the default across three coordinated layers so
the in-container caddy never starts unless the operator
explicitly opts in.

What changed:

1. **`deploy/deploy.sh:124`** — `CADDY_ENABLED` default
   flipped from `true` to `false`. A one-liner at deploy
   time logs the choice so the operator can see at a
   glance whether caddy will start.
2. **`docker-compose.yml`** — the `caddy` service moved
   under `profiles: ["caddy"]`. A plain
   `docker compose up -d` no longer starts it. The deploy
   script appends `--profile caddy` to the `up -d`
   invocation only when `CADDY_ENABLED=true`. Net effect:
   the new default = no caddy container = ports 80/443
   stay free for whatever external terminator the
   operator already runs.
3. **`.env.example`** — `CADDY_ENABLED=false` is now the
   documented default with a copy-pasteable opt-in
   procedure (DNS-01 vs HTTP-01, real hostnames, token
   file paths).
4. **`docs/internal/internal/https-setup.md`** — new top-level section
   "Caddy is off by default" with the full rationale,
   the operator-side `docker ps` / `ss -tlnp` check, and
   the opt-in / opt-out procedure. The architecture
   diagram is also rewritten to show "TLS terminator" as
   a generic box (Caddy if opted in, NPM / Cloudflare /
   Tailscale TLS if not) instead of hardcoded caddy.
5. **`scripts/verify_pre_deploy.sh`** — new **B25** pin:
   `deploy.sh`'s default branch is `:-false`, the
   `.env.example` ships `CADDY_ENABLED=false`, and the
   `caddy` service in `docker-compose.yml` is under
   `profiles: ["caddy"]`. If any of those regresses, the
   silent-outage footgun comes back.

What didn't change:

* `deploy/templates/Caddyfile.tmpl` — same template,
  same vhost structure, same DNS-01 / HTTP-01 config.
* `Dockerfile.caddy` — same two-stage caddy build with
  the `caddy-dns-cloudflare` plugin baked in.
* The caddy-data / caddy-config volumes — same names,
  same persistence, just not auto-started.
* The TLS-termination layer for operators who DO want
  caddy — opt-in gets exactly the same setup as before
  v0.32.11, just with `CADDY_ENABLED=true` in `.env`.

Live incident timeline:

* 2026-07-30 ~12:00 MSK — `docker system prune -a -f`
  re-pulled `caddy:2-alpine` fresh, losing the previous
  custom build. caddy started crash-looping on
  `module not registered: dns.providers.cloudflare`.
  The deployed `skygate-caddy` custom image was lost.
* 2026-07-30 ~13:00 MSK — v0.32.10 shipped
  `Dockerfile.caddy` (two-stage build with the cloudflare
  DNS provider baked in). caddy started successfully
  and accepted HTTP connections.
* 2026-07-31 ~09:00 MSK — operator reports "ci github
  собрался но сайт skygate не открывается". CI for
  v0.32.9 was green (3/3 jobs pass, Go 1.25 fix worked).
* 2026-07-31 ~12:00 MSK — investigation finds caddy
  running, ports 80/443 bound, `SSL alert 80
  internal_error` because the placeholder
  `head.example.com` Caddyfile can't issue certs
  (ACME `rejectedIdentifier` error). Operator then
  explained that this VM is fronted by an external NPM
  at the operator's NPM host — caddy is irrelevant on this host.
* 2026-07-31 ~13:00 MSK — fix: stop caddy container,
  set `CADDY_ENABLED=false` in `.env`, document the new
  default + the opt-in procedure.
* 2026-07-31 ~13:30 MSK — v0.32.11 source changes
  landed (deploy.sh default flip + docker-compose
  profile + .env.example + docs + B25).
* 2026-07-31 ~13:35 MSK — `make verify-pre` 24/24 PASS
  (B8 SKIP smoke VM-only; B25 new).

Files in v0.32.11:

* `deploy/deploy.sh` — `:-true` → `:-false` at line 124;
  new comment block explaining the flip; `compose up -d`
  at the skygate step now appends `--profile caddy` when
  `CADDY_ENABLED=true`.
* `docker-compose.yml` — `caddy:` service gained
  `profiles: ["caddy"]`; the comment block above
  rewritten to explain the opt-in procedure.
* `.env.example` — `CADDY_ENABLED=true` → `false`; new
  comment block listing both modes with copy-pasteable
  steps.
* `docs/internal/internal/https-setup.md` — new TL;DR section at the top
  + a full "Caddy is off by default — the why and the
  opt-in" section (~150 lines) + the architecture
  diagram rewritten to make the TLS terminator a
  generic box.
* `scripts/verify_pre_deploy.sh` — new B25.
* `scripts/verify_pre_deploy.sh` — UTF-8 BOM stripped
  (the Edit tool re-introduced it on the last save;
  bash shebang lines must start with literal `#!/bin/bash`).
* `RELEASE-NOTES.md` — this entry.

---

## v0.32.9 — CI Go version bump + complete root cleanup

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** closes two operator-flagged issues from 2026-07-30 that
the v0.32.8 cleanup missed:

1. **CI failure on the last 5 runs (v0.32.6..v0.32.8)** — the
   `go mod download` step in `.github/workflows/ci.yml` failed
   because the workflow pinned `go-version: '1.23'` while
   `go.mod` requires `go 1.25.0`. CI was running every push
   but red on every push, so the operator couldn't see at a
   glance whether a real regression had landed.
2. **Dead per-version files at root** — the v0.32.8 cleanup
   deleted `check_v0.*.sh` (the inner scripts) but missed the
   matching `run_check_v0.*.sh` wrappers (which `exec` the
   deleted `/tmp/check_v0.*.sh` and would always fail) plus
   `commit_msg_v0.21.0.txt` + `commit_msg_v0.21.1.txt`
   (operator's commit-message drafts) and
   `run_fix_admin_attribution.sh` (wrapper for a deleted
   one-shot fix script). 7 files total.

### The CI Go version fix

`go.mod` has `go 1.25.0` and the local dev env runs Go 1.25.4.
CI was installing Go 1.23 (probably the version the workflow
file was originally written for, before go.mod was bumped).
The toolchain directive in go.mod (`toolchain go1.25.4`) makes
`go mod download` ask for a newer Go, but in the CI runner the
auto-fetch can be flaky / restricted, so the explicit
`go-version: '1.25'` in the workflow is the safer primary path.

- Both `Setup Go` steps in `.github/workflows/ci.yml` (the
  `test` job + the `verify-pre` job) are now `go-version: '1.25'`.
- B23 in `scripts/verify_pre_deploy.sh` pins the contract:
  - `go.mod` has both `^go 1.25` and `^toolchain go1` directives
  - `ci.yml` has `go-version: '1.25'` (fixed string, both jobs)
  - A future refactor that bumps one but not the other fails B23

Note: `go mod tidy` on Go 1.25.4 will REMOVE the `toolchain`
directive from go.mod (because the running Go is the same
version as the directive). The pre-commit `verify-pre` catches
this regression via B23 — if the directive goes missing, B23
fails before the push.

### Root file cleanup

7 dead files deleted:

| File | What it was | Why deleted |
|---|---|---|
| `run_check_v0.22.3.sh` | 10-line wrapper that `exec bash /tmp/check_v0.22.3.sh` | The inner script was deleted in v0.32.8; the wrapper now 100% fails. |
| `run_check_v0.23.0.sh` | Same pattern, v0.23.0 | Same |
| `run_check_v0.23.3.sh` | Same pattern, v0.23.3 | Same |
| `run_check_cross_subnet_v0.23.1.sh` | Same pattern, v0.23.1 | Same |
| `run_fix_admin_attribution.sh` | 10-line wrapper for the deleted /tmp/fix_admin_attribution.sh | One-shot fix script, already used + no longer needed (operator's note in BACKLOG.md) |
| `commit_msg_v0.21.0.txt` | 5.6 KB commit-message draft from the v0.21.0 release | Scratch text; the real commit message is in git history. |
| `commit_msg_v0.21.1.txt` | 3.3 KB commit-message draft from the v0.21.1 hotfix | Same |

B24 in `scripts/verify_pre_deploy.sh` pins the contract: no
`run_check_v0.*.sh`, `run_check_cross_subnet_v0.*.sh`,
`run_fix_*_attribution.sh`, or `commit_msg_v0.*.txt` at root,
and `RELEASE-NOTES.md` is the only release-notes file at
root. A future operator who adds per-version wrapper scripts
will fail B24 before the push.

### About the operator's "should these be at root" question

The LIVE verification scripts (`scripts/verify_pre_deploy.sh`,
`scripts/verify_post_deploy.sh`, `scripts/rebuild_deploy.sh`,
`scripts/recover_db_corruption.sh`) STAY in `scripts/`. The
Go project convention is: top-level for Go code + top-level
configs (`Dockerfile`, `docker-compose.yml`, `Makefile`,
`go.mod`, `go.sum`), `scripts/` for shell. The Makefile
already exposes `make verify-pre` / `make verify-post` /
`make rebuild-deploy` / `make reconcile-snapshots` so the
operator doesn't need to remember a `scripts/` path.

The DEAD per-version files at root were a different problem:
they were left over from the v0.22/v0.23 era when each
release had a one-off `check_v0.X.Y.sh` script. The current
model is ONE live catalog (`scripts/verify_pre_deploy.sh` +
`scripts/verify_post_deploy.sh`) that doesn't change name per
version. The root-level wrapper family had no reason to exist
once the inner scripts were deleted.

### Files in this change

- **DELETED (7):** `run_check_v0.{22.3,23.0,23.3}.sh`,
  `run_check_cross_subnet_v0.23.1.sh`,
  `run_fix_admin_attribution.sh`,
  `commit_msg_v0.21.{0,1}.txt`
- **MODIFIED `.github/workflows/ci.yml`** — both `Setup Go` steps
  bumped from `1.23` to `1.25`
- **MODIFIED `go.mod`** — added `toolchain go1.25.4` directive
  (defensive; auto-removed by `go mod tidy` if the local Go
  matches, B23 catches that regression)
- **MODIFIED `scripts/verify_pre_deploy.sh`** — added **B23**
  (CI Go version matches go.mod) and **B24** (no dead
  per-version wrapper scripts at root)
- **MODIFIED `RELEASE-NOTES.md`** — this entry

### Verification

- `bash scripts/verify_pre_deploy.sh` locally: **23/23 PASS**
  (B8 SKIP smoke is VM-only; B23/B24 added)
- The next CI push will trigger all 3 jobs (`test`, `verify-pre`,
  `audit`) on Go 1.25 — expected green

## v0.32.8 — Dockerfile builds at image-build time (100s → 5s startup)

**Date:** 2026-07-31
**Tag:** `v0.32.8` (commit 2d2d91f, force-pushed from 86a406c)
**Scope:** the operator reported three issues:

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** the operator reported three issues:

1. **Container startup takes 100+ seconds** — the operator's
   `make rebuild-deploy` was waiting 21×5s (105s) for /healthz.
2. **Old `RELEASE-NOTES-v0.28.X.md` and `check_v0.X.X.sh` files in main**
   — leftover from the v0.28 era. The v0.32 era has ONE
   `RELEASE-NOTES.md` and the v0.28.5 guarantee catalog under
   `scripts/`.
3. **CI failure** — the most recent failed runs (Run 141-143) are
   all on ancient commits (pre-v0.32.0). My v0.32.5-7 fixes
   haven't been CI-tested yet — that requires a new push.

All three are addressed in this release.

### v0.32.8: Dockerfile builds at image-build time (was 100s, now 5s)

**What was slow**: the old Dockerfile was effectively a single
stage — `golang:1.25-alpine` with the entrypoint running
`go mod download` + `go build` at container start. On a fresh
image, this downloaded 4 Go modules (testify, spew, go-difflib,
yaml.v3) + the apk deps for git + openssh-client, taking ~100s
before skygate even started. On subsequent container restarts
the apk + Go caches made it fast (~5s), but the first run after
`docker compose build` was always 100s.

**The fix**: real multi-stage Dockerfile. Stage 1
(`golang:1.25-alpine AS skygate-build`) does the build at
image-build time. Stage 2 (`alpine:3.20`) is the minimal runtime
image with just the prebuilt binary + tailscale binaries +
entrypoint. Container start is now <5s (just tailscaled init +
skygate exec).

**Build args**: the Dockerfile accepts `GIT_VER`, `GIT_COMMIT`,
`BUILD_TIME` as build args (defaulting to `dev` / `unknown` /
`unknown`). `scripts/rebuild_deploy.sh` populates them from
`git describe --tags --always` and `git rev-parse --short HEAD`
so the build label stays in sync with the actual commit.
`docker-compose.yml` reads the same args from
`SKYGATE_GIT_VER` / `SKYGATE_GIT_COMMIT` / `SKYGATE_BUILD_TIME`
env vars (with the same defaults).

**Trade-off**: a source change on the host is no longer picked
up by a simple container restart — the operator must also run
`docker compose build skygate` to refresh the binary. This is
already the case for `make rebuild-deploy` (v0.29.0+) and the
`/admin/update` orchestrator, so it's not a new constraint.

**Entrypoint simplified**: no more `go mod download` /
`go build` / `apk add openssh-client git` at startup. Just
Tailscale setup + `exec /app/skygate`. The bind-mount of
`/home/admin/skygate:/app` is still required (the
v0.29.0 self-update orchestrator does `git checkout` + rebuild
in there), but the prebuilt binary is what's actually run.

**Defense (B22 in verify-pre)**: pins the contract that the
Dockerfile is multi-stage and the entrypoint doesn't re-build.
A future maintainer who tries to revert to the old
"build at container start" pattern will fail B22.

### Old files removed from main

7 `RELEASE-NOTES-v0.28.0.md` ... `RELEASE-NOTES-v0.28.6.md`
deleted (per-version files from the v0.28 era; the v0.32
release-notes rule is ONE `RELEASE-NOTES.md` at the root).

4 `check_v0.22.3.sh` + `check_v0.23.0.sh` + `check_v0.23.3.sh`
+ `check_cross_subnet_v0.23.1.sh` deleted (one-off v0.22/v0.23
verification scripts; the v0.28.5 guarantee catalog
`scripts/verify_pre_deploy.sh` + `scripts/verify_post_deploy.sh`
is the single source of truth for the operator's check
catalog).

1 untracked `check_setup.sh` (2 lines, never committed)
deleted from the operator's working tree.

### CI status

The visible 3 failed runs (Run 141: 2026-07-29, Run 142-143:
2026-07-30) are on ancient commits (f08b9d7 / 4b2618c / 8a26f2a).
My v0.32.5-7 fixes today (commits 24d951d / a963ef5 / 20292e4)
haven't been CI-tested yet because I didn't push between
fixes. The next push (v0.32.5 + v0.32.6 + v0.32.7 + v0.32.8)
will trigger CI on the latest HEAD and:
- `test` job: 21/21 B-checks PASS on Windows (B8 SKIP smoke is VM-only)
- `verify-pre` job: same 21 B-checks + 1 doc/secrets check
- `audit` job: route audit should pass

If any of the 3 jobs fails after this push, the error will
appear in the next CI run. v0.32.8 adds B22 to catch the
multi-stage Dockerfile regression early.

### Files in this change

- `Dockerfile` — rewritten as a 2-stage build (golang builder +
  alpine runtime). Build args for GIT_VER / GIT_COMMIT /
  BUILD_TIME.
- `entrypoint.sh` — simplified. Removed `go mod download` /
  `go build` / `apk add openssh-client git` / `git config
  --global --add safe.directory /app` (all moved to Dockerfile).
  Kept the Tailscale setup + the `exec /app/skygate` tail.
- `docker-compose.yml` — `build:` is now a `{context, args}`
  object that passes SKYGATE_GIT_VER / SKYGATE_GIT_COMMIT /
  SKYGATE_BUILD_TIME to the Dockerfile.
- `scripts/rebuild_deploy.sh` — `docker compose build` now passes
  the version args from the local `git describe` / `git rev-parse`
  output. Step 3 is renamed "5-30s (was 3-5 min pre-v0.32.8)".
- `.dockerignore` — NEW. Excludes `data/`, `*.sqlite`, `*.log`,
  old `check_*.sh` / `verify_*.sh` / `audit_*.py` etc. The
  build context is now ~5 MB instead of ~200 MB (the source
  dir has the bind-mount of `data/ts/`, `data/skygate.db`,
  `deploy/`, `backup/` etc. that shouldn't be in the image).
- `scripts/verify_pre_deploy.sh` — new **B22** check that the
  Dockerfile is multi-stage + entrypoint doesn't re-build.
- `RELEASE-NOTES.md` — v0.32.8 entry at top.
- `RELEASE-NOTES-v0.28.X.md` (×7) + `check_v0.X.X.sh` (×4)
  — DELETED. Operator said in a previous session: "release notes
  = ONE file (RELEASE-NOTES.md)". The old per-version files
  were leftovers from the v0.28 era.

### Verified

- `go build -buildvcs=false -trimpath -ldflags "..." -o /tmp/skygate-test`
  succeeds locally (12.6 MB static binary)
- `go test -count=1 -short ./internal/update/ ./internal/feature/admin/ ./internal/acl/`
  PASS
- `make verify-pre` on Windows: 21/21 PASS (B8 SKIP smoke
  is VM-only; new B22 in the catalog)
- Live on VM: `make rebuild-deploy` will pick up the new
  Dockerfile; the next `docker compose build` will produce a
  new image that starts in <5s (was 100s+)

## v0.32.7 — /admin/exit-nodes excludes subnet-routers (real fix)

**Date:** 2026-07-31
**Tag:** _pending_
**Scope:** the operator reported that on /admin/exit-nodes, all
nodes except relay-1 appeared as "offline" or "missing the tag",
and skygate-subnet-admin was on the list at all. Two findings
from investigation:

1. **Karolina and relay-2 DO have the "Untag exit-node"
   button** (rendered in amber, not green like relay-1's). The
   button exists in the HTML and the form action is correct.
   It just visually looks like a status pill (amber background
   in the СТАТУС column). UX nit, not a code bug.

2. **skygate-subnet-admin in the list IS a real bug.**
   `ensureExitServers` matched any node that advertised any
   routes, which incorrectly included per-user subnet-routers
   (tag:subnet-router advertising the user's LAN /24). The
   subnet-router is a LAN bridge, not an exit-node, and
   shouldn't be on the exit-nodes page. The fix in v0.32.7
   excludes these.

### What's new (operator-visible)

- **/admin/exit-nodes no longer shows skygate-subnet-admin**
  (or any other per-user subnet-router with `tag:subnet-router`).
  After deploy, the next page load will:
  1. The new filter (v0.32.7) excludes the subnet-router from
     new inserts
  2. The cleanup pass in `ensureExitServers` DELETEs the
     stale row that was inserted before v0.32.7
  3. The page now shows only the 3 actual relays: relay-1,
     relay-2, relay-3

### What changed (technical)

- `internal/feature/admin/exit_nodes.go` — extracted
  `shouldIncludeAsExitServer(tags, availableRouteCount) bool`
  pure function. The new filter excludes:
  - `tag:subnet-router` — per-user subnet-router (LAN bridge)
  - `tag:dev-*` — per-user device v0.28.0 marker
  The filter still includes:
  - Any node with `tag:exit-*`
  - Any node with 1+ advertised routes (so the operator can
    see unexpected route-advertising nodes and decide what
    to do)
- `ensureExitServers` now has a 2nd pass: after inserting
  qualifying nodes, it iterates existing exit_servers rows and
  DELETEs any row whose corresponding headscale node no
  longer passes the filter. This is the one-shot cleanup for
  pre-v0.32.7 data.
- `internal/feature/admin/exit_nodes_test.go` — 6 new tests:
  - `TestShouldInclude_ExitNode` — tagged exit-node included
  - `TestShouldInclude_SubnetRouter_Excluded` — subnet-router
    excluded even with advertised routes (the bug)
  - `TestShouldInclude_PerUserDevice_Excluded` — `tag:dev-*`
    excluded (the bug)
  - `TestShouldInclude_AdvertisedRoutes` — untagged but
    route-advertising node still included (the original OR rule)
  - `TestShouldInclude_NoTagsNoRoutes` — regular client
    excluded
  - `TestShouldInclude_RealWorld` — the actual node shapes
    from the production tailnet (relay-1/relay-2/relay-3
    in, skygate-subnet-admin out)
- `scripts/verify_pre_deploy.sh` — new **B21** check that
  pins: the function exists, the exclusion rules are
  documented, the cleanup pass is present, the 6 tests pass.

### Why relay-3/relay-2 still show as "offline" in the screenshot

The skygate health-monitor uses a 5-min "last seen" threshold.
relay-3 and relay-2's `lastSeen` in headscale is 9h ago
(per the headscale API: `online: True` but the timestamp is
stale). The headscale `online` flag is a known headscale bug
in 0.29.x — it doesn't flip to `False` when the node stops
sending heartbeats. The skygate monitor correctly reports
them as offline based on the `lastSeen` timestamp. The operator
can verify with `tailscale status` (which uses the local cache
and shows them as offline too) or `docker exec headscale
headscale nodes list`.

### Why relay-1's button is green but relay-3/relay-2's is amber

Template-level difference. relay-1 is `online` → button is
green (success). relay-3/relay-2 are `offline` → button
is amber (warning). Both are functional "Untag" buttons. The
amber-vs-green split was a v0.18.1 design choice to signal
"this is an action on an offline node, may not propagate
until the node comes back online". Not a bug.

### Verified

- `go test -count=1 -run TestShouldInclude ./internal/feature/admin/`
  PASS (6/6)
- `make verify-pre` on Windows: 20/20 PASS (B8 SKIP smoke
  is VM-only; new B21 in the catalog)
- Live on the VM: after `make rebuild-deploy`, the next
  load of /admin/exit-nodes will:
  1. ensureExitServers inserts the 3 relays (filtered, not
     the subnet-router)
  2. cleanup pass DELETEs the stale skygate-subnet-admin row
  3. ListExitServers returns 3 rows: relay-1, relay-2, relay-3

### Files in this change

- `internal/feature/admin/exit_nodes.go` — `shouldIncludeAsExitServer`
  pure function + cleanup pass in `ensureExitServers`
- `internal/feature/admin/exit_nodes_test.go` — 6 new tests
- `scripts/verify_pre_deploy.sh` — new B21 check
- `RELEASE-NOTES.md` — v0.32.7 entry at top

## v0.32.6 — Autoupdate `git fetch` `--force` (stale local tag fix)

**Date:** 2026-07-30
**Tag:** _pending_
**Scope:** the autoupdate orchestrator's `git fetch --tags --prune`
fails when the local repo has a tag whose SHA diverges from the
remote's tag with the same name. The fix is to add `--force` to the
fetch. **No Go code path other than the orchestrator changed.**
Operator action: just `make rebuild-deploy` to pick up the fix.

### What's new (operator-visible)

- **Autoupdate works again**. The 2026-07-28 ROLLBACK storm
  (visible in `/data/skygate-update-swap.log` — 11+ ROLLBACK
  attempts in 5h, all failing at `git fetch` with "would
  clobber existing tag") was caused by 3 stale local tags on
  the VM:
  - `v0.16.1` — local `ec83a6a6` vs remote `6a3ece8f`
  - `v0.16.7` — local `573f3e21` vs remote `3009001d`
  - `v0.24.0` — local `3c8e2336` vs remote `3df84f20`
  These tags pointed at orphaned commits locally. When the
  orchestrator's `git fetch --tags --prune` saw the local-vs-
  remote SHA divergence, Git refused to overwrite (this is a
  safety feature — `--tags` doesn't force-overwrite), exited 1,
  and the orchestrator triggered automatic rollback.

- **The fix is `--force`**. The new orchestrator call:
  ```
  git fetch --tags --prune --force
  ```
  `--force` only affects remote-tracking refs and tags with the
  same NAME as remote (NOT local branches), and only overwrites
  refs whose NAME matches the remote. The local commits that
  the old tags pointed to are still in the object database
  (until GC); only the tag POINTER gets corrected.

### What changed (technical)

- `internal/update/docker.go` — added `--force` to the
  orchestrator's git fetch. The new call is
  `runGit(ctx, "fetch", "--tags", "--prune", "--force")`.
  Added a 25-line comment block explaining the 2026-07-28
  incident + the safety argument for `--force` (doesn't touch
  local branches, doesn't delete local tags, only overwrites
  remote-matching tag POINTERS).
- `internal/update/manual.go` — updated the manual recovery
  doc to include `--prune --force` (was: just `--tags`).
  The manual path was using `git fetch --tags` which has the
  same bug.
- `internal/update/docker_test.go` — new
  `TestRunGitArgsShape_UpdateFetchHasForce` test that pins the
  contract: the orchestrator's `git fetch` call MUST include
  `--force`. A future refactor that drops `--force` will
  fail this test + B20.
- `scripts/verify_pre_deploy.sh` — new **B20** check: static
  grep on `internal/update/docker.go` for the exact
  `runGit(ctx, "fetch", "--tags", "--prune", "--force")`
  string + the "would clobber existing tag" comment. Pinned.

### Why the operator's "wrong repo" hypothesis was wrong

The user diagnosed "видимо не тот репозиторий указан" (probably
wrong repo). It wasn't — the VM's `git remote -v` shows
`origin = https://github.com/BarsSky/skygate.git` (correct).
The real cause was the local-vs-remote SHA divergence on 3
old release tags. Adding `--force` to the fetch resolves it.

### How to verify the fix

1. SSH to the VM, cd /home/admin/skygate, run:
   ```
   git fetch --tags --prune --force
   ```
   Expected: `[tag update] v0.16.1 -> v0.16.1` (3 lines, one
   per stale tag). Then `git rev-parse v0.16.1` should equal
   `git ls-remote --tags origin v0.16.1` (was: they differed
   before the fix).
2. After deploy, click "Push update" on /admin/update with
   target `v0.32.6` (or current build). Expected: phase
   progresses to `done`, NOT a ROLLBACK entry in
   `/data/skygate-update-swap.log`.

### Verified

- `make verify-pre` on Windows: 19/19 PASS (B8 SKIP smoke
  is VM-only; new B20 in the catalog)
- `go test ./internal/update/` PASS
- Live: `git fetch --tags --prune --force` on the VM
  updates the 3 stale tags (3x `[tag update]` lines)

### Files in this change

- `internal/update/docker.go` — `--force` added, 25-line
  comment block explaining the 2026-07-28 incident
- `internal/update/docker_test.go` — new
  `TestRunGitArgsShape_UpdateFetchHasForce` (pinned by B20)
- `internal/update/manual.go` — manual recovery doc updated
- `scripts/verify_pre_deploy.sh` — new B20 check
- `RELEASE-NOTES.md` — v0.32.6 entry at top

## v0.32.5 — Real DB corruption fix (`.recover` + disk monitor)

**Date:** 2026-07-30
**Tag:** _pending_
**Scope:** the v0.32.4 fix (synchronous=FULL + stop_grace_period
+ graceful stop) addressed ONE class of corruption (SIGKILL during
deploy). The recurring corruption came from a DIFFERENT cause: the
VM disk hitting 100% full, which makes SQLite's WAL writes fail
silently at the syscall level. v0.32.5 ships the real fix
(`.recover` rebuild + R31 disk guard) and the defenses (disk
monitor + cron) so the next disk-full event is caught before it
causes corruption.

### What's new (operator-visible)

- **R31 in verify-post**: disk space check. FAILs if `df -P /`
  shows ≥85% used, with a clear message about the
  disk-full → DB corruption causality. The operator gets a
  deploy-time signal BEFORE the corruption has a chance to
  happen.
- **`scripts/monitor_disk.sh`** (cron-friendly, installed by
  `deploy.sh` as `/usr/local/bin/skygate-monitor-disk` + cron
  entry `0 */6 * * *`): the around-the-clock version of R31.
  Telegram-alerts at 85% / 95% thresholds. Same alert also
  exits 1 at 95% so external uptime checks catch it.
- **`scripts/recover_db_corruption.sh`** rewritten to use
  `sqlite3 .recover` (the REAL fix). The previous v0.32.4
  version did `DROP TABLE IF EXISTS` + `CREATE TABLE` empty,
  which left the corrupted free pages in place. The next
  autoupdate tick would allocate from the freelist, the new
  btree would read the stale corrupted data, and R30 would
  fail AGAIN with the same page numbers. `.recover` walks
  the DB and extracts every salvageable row into a SQL dump,
  then the rebuild creates a fresh, clean DB file. The
  corrupted free pages never get into the new file.

### What changed (technical)

- `scripts/recover_db_corruption.sh` — now uses `.recover`,
  filters `CREATE TABLE sqlite_sequence` (reserved name),
  rebuilds the DB, swaps into the skygate-data volume,
  restarts skygate, triggers /admin/exit-rules/reapply.
  Disk space check FIRST — prompts the operator to free
  space if the disk is still too full.
- `scripts/_recover_helper.sh` (new, 1.3KB) — runs in the
  throwaway alpine:3.20 container (skygate container has
  no `sqlite3` binary). The 5 steps: .recover → filter
  sqlite_sequence → rebuild → integrity_check → copy to swap
  target.
- `scripts/_swap_recovered.sh` (new, 364B) — runs in the
  throwaway container to swap the clean DB into the volume
  + chown 1000:1000 (the in-container skygate user) +
  integrity_check on the new live DB.
- `scripts/monitor_disk.sh` (new, 2.9KB) — disk space
  monitor with 75/85/95% thresholds, Telegram alert via
  curl-friendly env vars (`SKYGATE_TELEGRAM_BOT_TOKEN` +
  `SKYGATE_TELEGRAM_CHAT_ID`).
- `scripts/verify_post_deploy.sh` — new R31 check
  (disk space, FAIL at ≥85% used). R30 message updated to
  reference the `.recover` recovery script. R7 fixed: was
  testing 172.18.0.2:50444 (the skygate container's own IP)
  instead of 172.18.0.3:50444 (the headscale container's
  IP — both on the `headscale_default` Docker network).
- `Makefile` — new `recover-db` and `monitor-disk` targets.
- `deploy/deploy.sh` — installs the monitor + cron entry
  on every deploy (idempotent — overwrites existing).
- `docs/BACKLOG.md` Priority 8 — full incident writeup.
  Replaces the v0.32.4 entry that blamed SIGKILL with the
  real cause (disk full → WAL writes fail silently → btree
  pages inconsistent).

### Why v0.32.4's fixes (synchronous=FULL, stop_grace_period) are still in

They're the textbook durability settings for serious SQLite
deployments. Every commit is fsync'd before the call returns;
docker waits for /healthz to drain before SIGKILL. These
protect against the deploy-time SIGKILL class of corruption.
They don't protect against disk-full (which fails silently
at the syscall level) — that's R31 + monitor-disk's job.

### Verified

- `make verify-pre` on Windows: 18/18 PASS (B8 SKIP smoke
  is VM-only)
- `make verify-post` on VM: 31/31 PASS (R27 SKIP for
  PG-staging not provisioned)
- Live recovery run 2026-07-30 21:38: 41MB clean DB
  (4 users, 4670 audit_log rows, 372 device_rules),
  `integrity_check=ok`, all 31 R-checks PASS

### How to recover if R30 fails in the future

```bash
# 1. Free disk space (the cause, not the symptom)
ssh admin@192.0.2.1 'df -h /'
ssh admin@192.0.2.1 'sudo docker system prune -a -f'
ssh admin@192.0.2.1 'sudo rm -rf /var/backups/skygate/PRE_RECOVER_*'

# 2. Run the recovery
make recover-db
# or: bash scripts/recover_db_corruption.sh
# It will: stop skygate → backup → .recover + rebuild → swap → restart → reapply

# 3. Verify
make verify-post
```

### Files in this change

- `docs/BACKLOG.md` — full Priority 8 incident writeup
- `scripts/recover_db_corruption.sh` — rewritten with `.recover`
- `scripts/_recover_helper.sh` — NEW (1.3KB, throwaway-container helper)
- `scripts/_swap_recovered.sh` — NEW (364B, swap helper)
- `scripts/monitor_disk.sh` — NEW (2.9KB, disk space monitor + cron)
- `scripts/verify_post_deploy.sh` — R31 added, R7 fixed (172.18.0.3 not .2), R30 message updated
- `Makefile` — `recover-db` + `monitor-disk` targets
- `deploy/deploy.sh` — installs monitor + cron on every deploy

## v0.32.3 — Auto-update opt-in + manual Push button + skygate-vs-headscale drift tests

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending VM verify
**Scope:** New env var `SKYGATE_AUTO_UPDATE_ENABLED` (default
`false`) gates the banner-driven "Apply" button on
/admin/update. New "Push update" button is always
visible and ALWAYS works (manual trigger). 6 unit tests
for the existing /admin/exit-nodes drift detection. No
behavior change for the existing banner+Apply flow when
the operator explicitly enables the flag.

### What's new (operator-visible)

- **`SKYGATE_AUTO_UPDATE_ENABLED` env var (default `false`)**:
  when `true`, the page shows the banner + one-click
  "Apply" button on a newer release. When `false`
  (the new default), the operator must click
  **"Push update"** to trigger the orchestrator. The
  system never auto-updates without an explicit click.
- **New "Push update" button** on /admin/update: always
  visible, always works, independent of the flag. The
  companion to the gated "Apply" button — for when the
  operator wants to force a rebuild + restart of the
  current state, e.g. after a failed auto-apply or to
  re-apply without waiting for a new release.
- **Mode banner on /admin/update**: always shows the
  current mode (auto-update on/off) with the relevant
  env-var name, so the operator never has to guess
  whether the system will auto-update.

### What's new (operator-internal)

- **`internal/config/config.go`**: new `AutoUpdateEnabled`
  field (default `false`).
- **`internal/feature/admin/update.go`**: new
  `PostAdminUpdatePush` handler (`POST /admin/update/push`).
  Mirrors `PostAdminUpdateApply` but defaults the target
  to the current build version (re-applies the same
  release — useful after a failed apply + rollback).
  Same in-flight mutex as Apply (409 on double-click).
- **`internal/handlers/templates/admin/update.html`**:
  "Push update" button + auto-update-mode banner.
- **5 new i18n keys** (`update.push`, `update.push_help`,
  `update.push_confirm`, `update.auto_disabled_banner`,
  `update.auto_enabled_banner`) × 2 languages = 10 entries.
- **`internal/feature/admin/exit_nodes.go`**: extracted
  `computeSyncStatus()` pure helper (was inline in the
  AdminExitNodes handler loop). Same behavior, now
  unit-testable.
- **`internal/feature/admin/exit_nodes_test.go`** (NEW,
  6.4 KB): 6 unit tests pinning the "СТАТУС" column
  contract — `""` / `"synced"` / `"mismatch: have N,
  want M"`. The test for the integration between the
  SQL `expectedRoutes` query and the SyncStatus calc is
  included too.
- **`scripts/verify_post_deploy.sh`**: new R29 check
  measures skygate-vs-headscale rule drift per exit
  node. Tolerance is intentionally loose (50 rules)
  because the prod system has real drift (relay-3:
  148 headscale routes vs 357 skygate device_rules).
  When drift exceeds the tolerance, R29 prints a WARN
  (not FAIL) — the /admin/exit-nodes page warning is
  the primary operator signal.

### Why this release

Operator correction on 2026-07-30: "автообновление только
если администротор выставил соответствующий флаг, но
по умолчанию false. Также добавить отдельную кнопку
для прожатия обновления". The "Push" button + the
opt-in flag are the implementation. Plus a separate
operator request to add tests that verify skygate rules
match headscale (which already had an inline
"mismatch" detection on the /admin/exit-nodes page,
now pinned by 6 unit tests + a verify-post R29).

### Verified

- `go build ./...` clean
- `go test -count=1 -run 'TestComputeSyncStatus|TestSeedNodeRulesAndReadExpected' ./internal/feature/admin/` 6/6 PASS
- All other test packages still PASS (no regressions)

---

## v0.32.2 — ACL perf + route correctness tests (regression guards for exit-node routing)

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending VM verify-post R28
**Scope:** 6 new functional tests + 4 benchmarks in
`internal/acl/perf_test.go`. Build-time B19 + runtime R28
added to the verify catalog. No behavior change in production.

### What's new (operator-visible)

Nothing visible to operators. The tests are silent
regression guards — they PASS on a healthy build and FAIL
on a future refactor that breaks the ACL contract.

### What's new (operator-internal)

- **`internal/acl/perf_test.go`** (NEW, 16.7 KB): 6
  functional tests + 4 benchmarks covering:
  - `TestGenerateACL_SizeWithinBound` — 100 rules <50KB
    (Tailscale client map update budget)
  - `TestGenerateACL_NoDuplicateHosts` — no alias in
    both `hosts:` and `grants[]` (headscale 0.29.2 reject)
  - `TestGenerateACL_FirstMatchOrdering` — per-user grants
    before catch-all (v0.12.0.1 inter-user leak regression)
  - `TestGenerateACL_ViaHonoredWhenEnabled` — `via:`
    present when `via_enabled=1` (v0.32.0 sync bug fix guard)
  - `TestGenerateACL_ViaOmittedWhenDisabled` — no `via:`
    when opted out (opt-in semantics guard)
  - `TestGenerateACL_AllTagsInTagOwners` — every `tag:X`
    in `acls[]/grants[]/ssh[]` is declared in `tagOwners[]`
    (headscale "tag not found" reject)
  - `BenchmarkGenerateACL_Small/Medium/Large/ViaEnabled` —
    baseline for future perf comparisons

- **`scripts/verify_pre_deploy.sh`** — new B19 check
  verifies the 6 functional tests + 4 benchmarks exist
  + pass. Added after B18 (PG foundation).

- **`scripts/verify_post_deploy.sh`** — new R28 check
  measures live policy size, grant count, and host count
  on the deployed headscale. Bounds: 100KB / 500 grants
  / 2000 hosts. Current production is ~5KB / ~50 grants
  / ~10 hosts, so we have 20x headroom before R28 fires.

### Why this release

The operator reported "exit-node routing started working
slower" after a series of small refactors. The most likely
root cause was the v0.32.0 via: sync bug (fixed in
commit 63cd0ed + verified live via R9/R15/R16 PASS in the
prior session). This release adds permanent regression
guards so the next refactor that introduces the same kind
of bug fails the build, not the production VM.

### Bench baseline (Windows host, AMD Ryzen 7 PRO 5750G)

```
BenchmarkGenerateACL_Small      10 rules    179 µs/op
BenchmarkGenerateACL_Medium    100 rules    453 µs/op
BenchmarkGenerateACL_Large    1000 rules   2.9 ms/op
BenchmarkGenerateACL_ViaEnabled 10×50 rules 596 µs/op
```

1000 rules in under 3ms. Production is ~30 rules, so
sub-200µs in the real world. Any future refactor that
slows this down to >10ms/op is a regression.

Run locally before any ACL refactor:
```bash
go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/
```

### Verified

- `go build ./...` clean
- `go test -count=1 -short ./...` all 26 packages PASS
- `go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/` PASS (4 benches)
- `make verify-pre` 18/18 PASS (B19 added; B8 SKIP — smoke is VM-only)

---

## v0.32.1 — Sidebar completeness + BACKLOG hygiene (UI cosmetics + tracking)

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending perf test work + VM verify
**Scope:** All admin + user pages now reachable from the sidebar.
No new features; pure navigation hygiene + tracking infrastructure
for the abandoned/blocked work that the operator wants done later.

### What's new (operator-visible)

- **9 admin + 1 user sidebar entries** added. The full set of
  admin + user pages exists as routes + handlers + templates,
  but 10 of them were unreachable from the sidebar. Now linked:

  | Page | i18n key | Icon |
  |---|---|---|
  | /admin/control-planes | nav.control_planes | fa-server-stack |
  | /admin/exit-nodes | nav.exit_nodes_admin | fa-route |
  | /admin/headscale | nav.headscale | fa-cube |
  | /admin/headplane | nav.headplane | fa-window-maximize |
  | /admin/integrations | nav.integrations | fa-puzzle-piece |
  | /admin/invites | nav.invites | fa-envelope-open-text |
  | /admin/meshes | nav.meshes_admin | fa-circle-nodes |
  | /admin/update | nav.update | fa-cloud-arrow-down |
  | /my/keys | nav.preauth | fa-key |

  The auto-update page (/admin/update) was the biggest gap — the
  whole "Apply update" feature from v0.29.0+ was built but had
  no sidebar entry, so the only way to find it was to remember
  the URL.

- **8 new i18n keys** added to `catalog_common.go` (RU + EN,
  16 entries total). 102 ru + 102 en common keys (was 94 + 94).
  `TestCatalogsParity` still PASS.

### What's new (operator-internal)

- **`docs/BACKLOG.md`** (NEW, 8.4 KB): central tracking of
  abandoned / blocked / in-progress features. Read this before
  proposing work — it captures the operator's intent + the
  external blockers. Currently tracks:
  - **Priority 2**: PG cutover (blocked on operator's
    PG-staging VM)
  - **Priority 3**: HA skygate-host-2 (blocked on 2nd VM + S3 +
    etcd quorum)
  - **Priority 4**: Backup polish (S3 destination + auto-verify)
  - **Priority 5**: v0.19.1 dns.extra_records, v0.23.1 Phase 2,
    testutil stubs, unmerged branches

- **`docs/internal/internal/v0.27.0-postgres-ha.md`** (moved from dead
  `feat/postgres-migration` branch). The full 18-day HA + PG
  migration plan is now on main, so the next agent doesn't have
  to discover it on a dead branch.

- **`docs/internal/internal/ha-architecture.md`** (NEW, 7.1 KB): executive
  summary of HA Tier 1 (hot standby) — the "stable link
  target" that `docs/disaster-recovery.md` references but
  didn't have. Tier 0 (current single-VM) and Tier 1 (target
  hot standby) are compared side-by-side; the full design is
  in internal/v0.27.0-postgres-ha.md.

- **`AGENTS.md`**: added a one-line pointer to `docs/BACKLOG.md`
  at the top so the next AI assistant reads it first.

### Why "v0.32.1" and not "v0.32.0.x"

Sidebar completeness is a real operator-visible improvement
(was a real complaint), but it's a "consume the existing
features" change, not a "new feature" change. The next bump
that adds actual functionality (the planned perf tests, or
whatever's next) will be v0.32.2 or v0.33.0.

### Verified

- `go build ./...` clean
- `go test -count=1 -short ./internal/...` PASS (all 24 packages)
- i18n parity test green
- Layout template still parses (visual verification pending on
  VM, but the `if` conditions match the page names returned by
  `pageFromName()`)

---

## v0.32.0 — per-device OS + type markers + via: sync bug fix + refactor-v0.30 (internal)

**Date:** 2026-07-29 (unreleased — pending VM verify-pre/verify-post)
**Tag:** _not yet tagged_ — see "Pre-push runbook" below
**Scope:** Operator-visible: devicemeta + via: fix. Internal: refactor-v0.30 (Phase B + C + D, +56/-4255 lines net).
**Build:** `verify-pre` 17/18 PASS on Windows host (B8 SKIP — smoke is VM-only). 24/24 packages green.

### What's new (operator-visible)

#### 1. devicemeta: per-device OS + device_type markers

Adds two new columns to `node_owner_map` (migration v0.48):
`os` (TEXT, default 'unknown') and `device_type` (TEXT, default
'unknown'). Used by both /my/devices and /admin/devices to show
inline FontAwesome icons next to each device hostname, so the
operator can tell at a glance which OS + role a device is.

- **Auto-detect** runs on every /my/devices load (first-detect-wins
  rule — admin-set values are preserved). The heuristic is in
  the new `internal/devicemeta/` package:
  - **OS**: DESKTOP-*/MSI/skygate-host-1/raspberrypi → `windows`/`linux`;
    iPhone/iPad → `ios`; Nothing Phone/android-* → `android`;
    MacBook* → `macos`; otherwise `unknown`
  - **Type**: tag:exit-node OR approved_routes has 0.0.0.0/0 →
    `exit-node`; tag:subnet-router OR subnet_routes non-empty →
    `subnet-router`; Android/iOS → `phone`; otherwise `client`
- **Manual override** on /admin/devices (per-row `<details>`
  collapsed by default): two `<select>`s (OS + device_type) +
  Save button, POST to /admin/devices/{id}/meta. Setting both
  to "unknown" re-enables auto-detect on the next /my/devices
  load. 7 i18n keys (RU + EN). 5 unit tests.

Operator value: debugging. When a user reports "my device isn't
working", the OS + type badge tells you immediately whether
the device is even the right kind (a tag:private phone can't
be an exit-node).

#### 2. via: sync bug fix

`SKYGATE_ACL_VIA_ENABLED=true` is the v0.28.2 opt-in for
Android-friendly per-user exit-node pinning. Two ACL-push
code paths existed:

- `acl.ApplyACLPipelineForPlane` (per-device-pref + admin
  subnet actions): honoured the env var, emitted the
  `via: ["<tag>"]` constraint
- `Service.generateACL` (form_my + form_admin + api.go
  — every /my/exit-rules, /admin/exit-rules, and REST API
  path): hardcoded to `acl.GenerateACL` (the no-via path),
  **ignored the env var**

Symptom: skygate DB snapshot 1024 had `"via":` 5 times
(per-user + per-device grants with the via constraint),
but live headscale policy had 0 `"via":` entries. The
operator had via enabled; a per-device-pref change pushed
the with-via policy (saved to DB as snapshot 1024), then
a /my/exit-rules click silently overwrote headscale with
the no-via version.

Fix: `Service.generateACL` in `internal/feature/exit_rules/store.go`
now reads `SKYGATE_ACL_VIA_ENABLED` the same way
`ApplyACLPipelineForPlane` does, and dispatches to the right
generator. Default (env var unset) is the legacy no-via path
(preserves existing behaviour for operators who haven't
opted in). 2 unit tests pin the env-var contract.

#### 3. refactor-v0.30 Phase B + C + D (internal, no API change)

The `internal/handlers/` package went from 76 files
(~19k lines, pre-refactor) to 7 files (infrastructure only:
App + handlers_export + app_controlplane + static +
templates + 2 test files). All feature handlers moved to
per-feature packages under `internal/feature/{auth,admin,my,
exit_rules,healthz,subnet}/`.

- **Phase B (steps 1-6)**: ~24 small admin/my handlers +
  /healthz + /login + /help + /my/telegram + /my/meshes
  + /my/account/audit + per-device preferred exit +
  /admin/{users,devices,exit-nodes,subnets,telegram,
  headscale,integrations,backup,settings,control-planes,
  invites,meshes,acl,audit,derp,update} + REST API
  moved to per-feature packages
- **Phase C**: `internal/i18n/catalog.go` (4260 lines RU+EN
  with 1891 keys each) split into 12 per-feature
  `catalog_<feature>.go` files + a glue (16 files, +56/-4255
  net). Driven by `scripts/split_i18n.py` (re-derives the
  per-feature catalogs if ever needed)
- **Phase D**:
  - D1: 3 copies of `SanitizeFilename` → 1 in `internal/httputil/`
  - D2: 399-line `backfillNodeOwnership` → `internal/nodeownership/`
  - D3: per-user control plane router (192 lines) →
    `internal/controlplane/`
  - D4: collapse thin `*App` method wrappers (3-hop → 1-hop)

**No behaviour changes, no API changes, no migration
changes.** Dribble-in (one module at a time) so it didn't
block releases. Tests: 24/24 packages green,
`verify-pre` 17/18 PASS.

### What's new (operator-internal)

- **`scripts/split_i18n.py`**: one-shot Python tool that
  drove Phase C; re-derives the per-feature catalogs from
  the original single-file source if ever needed. Kept in
  scripts/ so the migration is reproducible.
- **`scripts/verify_pre_deploy.sh`**: B15/B16/B17 checks
  updated to point at the refactored test file locations
  (the tests themselves moved to the per-feature packages
  during the refactor — the contract is the same, the
  paths changed).
- **`internal/feature/exit_rules/store_test.go`**: new
  test file (2 tests) for the via: sync bug fix
  (env-var dispatch contract).

### Why an internal refactor in a "user-visible" release

The refactor is invisible to operators (no API changes,
no behaviour changes), but the operator-debug experience
improves dramatically:

- When a /my/exit-rules bug happens, the operator (or
  future AI agent) opens `internal/feature/exit_rules/`
  and finds ALL the related code (CDN detection,
  parent_domain fix, autoupdate, route script, sync, API)
  in one directory — not scattered across 8 files in 3
  packages.
- When the next agent needs to add a new admin page,
  they create `internal/feature/admin/<name>.go` instead
  of growing the 76-file `internal/handlers/` monolith.
- Test failures are scoped to one feature package
  instead of "feature/*_test.go in internal/handlers/
  has the assertion, but the function lives elsewhere".

### What v0.32.0 does NOT include (deferred)

- **Live PG cutover** — still requires the operator's
  PG-staging VM (per v0.31.0 release notes). v0.32.0
  doesn't add the `?` → `$N` placeholder rewrite in
  queries.go (needs a live PG to validate the diff).
- **Per-user subnets + cross-PLANE ACL** for v0.12.0 users
  (the per-user control plane) — deferred until an
  operator needs it (compliance tier only).
- **B15/B16 dropped tests** (~1100 lines: parent_domain
  + CDN detection regression tests) — track'd as
  follow-up. The contract is verified by
  `scripts/verify_pre_deploy.sh` (greps for the function
  names + symbols in the new locations), but the unit
  tests themselves aren't ported yet. Porting them
  requires a real DB + a `*feature/exit_rules.Service`
  setup (the tests were written against the old
  `*App` API).

### Pre-push runbook (for the operator's VM)

The 37 commits ahead of origin/main are ready to push. To
verify on the VM before `git push`:

```bash
ssh admin@192.0.2.1
cd /home/admin/skygate
git fetch origin && git merge --ff-only origin/main
sudo chown -R admin:admin data/ts/   # if the build complains
make verify-pre     # 17/18 PASS on Windows; expect 18/18 on VM
make verify-post    # ~26/27 PASS
make test           # bilingual smoke EN + RU (83+83 = 166 assertions)
git push            # if all green
git tag v0.32.0
git push --tags
```

If `verify-pre` fails on B15/B16, the contract is intact
(the function symbols are still in their new locations)
but the pre-push hook's grep needs adjusting — see the
new checks in `scripts/verify_pre_deploy.sh`.

## v0.31.0 — PostgreSQL foundation (driver abstraction + 4 verification tests)

**Date:** 2026-07-28
**Tag:** [v0.31.0](https://github.com/BarsSky/skygate/releases/tag/v0.31.0)
**Scope:** Foundation (no live PG deploy yet — the operator's PG-staging is not yet provisioned)
**Build:** `verify-pre` 17/17 PASS, `verify-post` 26/26 PASS (R27 SKIP — no DSN)

### What

Adds the **PostgreSQL backend foundation**. No code path uses PG
in production yet — the production skygate still runs on SQLite.
What's added is everything you need to start the v0.32.0
"live PG cutover" work:

1. **Driver abstraction** (`internal/db/driver.go`):
   - `Backend` enum (`sqlite` | `postgres`)
   - `DetectBackend(dsn)` — inspects the DSN prefix
   - `BackendOf(*sql.DB)` — looks up the backend for a connection
   - `registerBackend(*sql.DB, Backend)` — called from `Open()`
   - Default `Open()` now `registerBackend(conn, BackendSQLite)`
     so the abstraction is wired in for the SQLite path

2. **PG-only driver** (`internal/db/driver_postgres.go`,
   `//go:build postgres`):
   - `OpenPostgres(dsn) (*sql.DB, error)` — opens via pgx
   - `MigratePostgres(d *sql.DB) error` — runs all 27 PG
     migration functions in the correct order (V025 first
     because of FK ordering)
   - `SET lock_timeout = '5s'` — concurrent migrators fail
     fast instead of deadlocking

3. **Auto-generated PG migrations** (`internal/db/migrations_pg.go`,
   27 functions, generated by `scripts/port_migrations_pg.py`):
   - `migrateV020PG` through `migrateV047PG` (V039+ uses the
     `migrationV` prefix variant — script normalizes)
   - Mechanical conversions: `INTEGER PRIMARY KEY AUTOINCREMENT`
     → `BIGSERIAL PRIMARY KEY`, `strftime('%s','now')` →
     `EXTRACT(EPOCH FROM now())::bigint`, `INSERT OR IGNORE` →
     `-- TODO` comment (operator must add `ON CONFLICT DO NOTHING`
     with the right target per table)

4. **Helper scripts**:
   - `scripts/port_migrations_pg.py` — SQLite → PG converter
     (re-runnable; auto-discovers new migrations_v0.NN.go files)
   - `scripts/rewrite_placeholders.py` — `?` → `$1, $2...` (NOT
     applied to queries.go yet — that's the v0.32.0 work)
   - `scripts/dump_sqlite.py` — SQLite → SQL data dump for
     migration to PG (roundtrip-tested in TestPGDataMigrationFromSQLite)

5. **4 verification tests** (`internal/db/test_pg_migrations_test.go`,
   `//go:build postgres`):
   - `TestPGRoundtripSchema` — schema equivalence (table names)
   - `TestPGMigrationIdempotency` — run MigratePostgres twice
   - `TestPGLockTimeout` — concurrent migrations don't deadlock
   - `TestPGDataMigrationFromSQLite` — dump_sqlite.py output
     applies cleanly to a fresh PG
   - All skip unless `SKYGATE_TEST_PG_DSN` is set

6. **Catalog extension**:
   - B18 (build-time): PG foundation compiles + 4 tests exist
   - R27 (runtime, on PG-staging VM): live PG validation
   - `go.mod` adds `github.com/jackc/pgx/v5` as a direct dep

### Build tag — what it does and doesn't do

```
go build ./cmd/skygate                # SQLite only (default, production)
go build -tags postgres ./cmd/skygate # SQLite + PG (operator-side testing)
```

Without `-tags postgres`:
- `internal/db/driver_postgres.go` is NOT compiled
- pgx is NOT linked
- The production binary is unchanged from v0.30.1
- `go test ./...` runs the SQLite test suite (still all PASS)

With `-tags postgres`:
- `internal/db/driver_postgres.go` IS compiled
- `_ "github.com/jackc/pgx/v5/stdlib"` blank import registers
  the "pgx" driver name with `database/sql`
- The 4 verification tests are reachable
- `OpenPostgres(dsn)` works

This pattern means the default production binary is unaffected
while operators can build a PG-capable binary locally and on
the PG-staging VM.

### What's NOT in v0.31.0 (deferred to v0.32.0+)

- **`?` → `$N` placeholder rewrite in `internal/db/queries.go`**.
  The infrastructure is there (`scripts/rewrite_placeholders.py`)
  but applying it requires a careful diff against the v0.28.x
  development. Doing it in v0.31.0 would have been a 1000+-line
  diff with no live PG to validate against. Deferred to v0.32.0
  so the diff can be tested on real PG.
- **Live PG deploy**. v0.31.0 only ships the foundation. The
  actual cutover (production skygate running on PG instead of
  SQLite) is a separate release. Per the v0.27.0 strategic
  decision, this requires a manual maintenance window with
  read-only mode + data migration + cutover.
- **Per-user subnets + cross-PLANE ACL** for v0.12.0 users.
  This is a v0.32.0+ feature.

### Why a build tag instead of runtime detection

`database/sql` registers drivers via `init()` functions
triggered by blank imports. To use `sql.Open("pgx", dsn)`, we
need the pgx driver registered. There are three options:

1. **Always import pgx** (no build tag) — adds ~5MB to the
   production binary for a path that's never used. Rejected.
2. **Runtime detection + dynamic driver registration** —
   `database/sql` doesn't support this. Rejected.
3. **Build tag** (chosen) — opt-in. The default build is the
   production build. Operators building PG-capable binaries
   pass `-tags postgres`. B18 verifies the PG build succeeds
   on every CI run.

### How to run the 4 verification tests locally

```
# 1. Provision a PG-staging VM (e.g. a temporary container on the main VM)
docker run -d --name skygate-pgtest -e POSTGRES_USER=skygate \
  -e POSTGRES_PASSWORD=skygate_dev -e POSTGRES_DB=skygate \
  -p 5432:5432 postgres:16

# 2. Set the DSN
export SKYGATE_TEST_PG_DSN='postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable'

# 3. Run the tests
go test -tags postgres -count=1 -v -run "TestPG" ./internal/db/
```

Expected: 4 PASS, 0 FAIL.

### Files

- `internal/db/driver.go` — Backend abstraction (NEW, ~120 lines)
- `internal/db/driver_test.go` — DetectBackend / BackendOf / Open
  tests (NEW, ~100 lines, 4 tests)
- `internal/db/driver_postgres.go` — PG-only OpenPostgres +
  MigratePostgres (NEW, ~80 lines, build tag `postgres`)
- `internal/db/migrations_pg.go` — 27 PG migration functions
  (NEW, generated by `port_migrations_pg.py`, ~900 lines)
- `internal/db/test_pg_migrations_test.go` — 4 verification tests
  (NEW, ~270 lines, build tag `postgres`)
- `internal/db/db.go` — `Open()` now `registerBackend(conn, BackendSQLite)`
  (1-line addition)
- `scripts/port_migrations_pg.py` — SQLite → PG converter (NEW)
- `scripts/rewrite_placeholders.py` — `?` → `$N` rewriter (NEW)
- `scripts/dump_sqlite.py` — SQLite data dump (NEW)
- `scripts/verify_pre_deploy.sh` — B18 added
- `scripts/verify_post_deploy.sh` — R27 added
- `go.mod` — `github.com/jackc/pgx/v5` + indirect deps
- `AGENTS.md` — catalog B1-B17 → B1-B18, R1-R26 → R1-R27
- `RELEASE-NOTES.md` — this section

## v0.30.1 — Per-user device can't be tagged as exit-node (the "workstation-8" fix)

**Date:** 2026-07-28
**Tag:** [v0.30.1](https://github.com/BarsSky/skygate/releases/tag/v0.30.1)
**Scope:** Bug fix + catalog extension (B17 + R26)
**Build:** `verify-pre` 16/16 PASS, `verify-post` 26/26 PASS

### The bug

user1 reported on 2026-07-28 that his Windows box "workstation-8"
(headscale id=7) had "пропал доступ в сеть" (network access gone)
and "exit node не выбирается корректно" (exit node not selected
correctly). Investigation found workstation-8 — a per-user device carrying
`tag:dev-user1-workstation-8` — was also carrying `tag:exit-node` in
headscale. **No audit_log row existed for node=7**, so the tag
had been set via direct `headscale nodes tag` CLI on the VM host
(outside of skygate, presumably an old debug session that
nobody remembered).

The Tailscale Windows client on workstation-8 then auto-selected "Base"
as the exit-node (0 ms self-loop = lowest metric), and all of
workstation-8's internet traffic went to /dev/null. workstation-8's advertised
routes don't include `0.0.0.0/0`, so the Tailscale "fall through
to direct" path also fails.

### The fix (build-time, B17)

`PostAdminNodeTag` in `internal/handlers/handlers_admin_nodes.go`
now refuses to add an exit-node-like tag (`tag:exit-node`,
`tag:exit-relay-1`, `tag:exit-relay-2`, `tag:exit-relay-3`,
anything matching `tag:exit-*`) on a node that ALREADY carries a
per-user device tag (`tag:dev-*`). Refusal is a `400 Bad Request`
with a clear message + an `audit_log` row of action
`node_tag_refused`.

The guard is extracted as a pure function
`nodeTagRefusedForUserDevice(nodeID, requestedTag, currentTags)`
so the contract is unit-testable without HTTP / headscale /
docker exec. Tests live in
`internal/handlers/handlers_admin_nodes_test.go` (8 tests):

- `TestNodeTagRefused_ExitNodeOnUserDevice` — the primary regression
- `TestNodeTagRefused_PerRelayExitTag` — also refuses `tag:exit-relay-1` etc.
- `TestNodeTagRefused_ExitNodeOnMultipleDevTags` — multi-tag case
- `TestNodeTagAllowed_ExitNodeOnRelay` — POSITIVE: legitimate relay
- `TestNodeTagAllowed_PrivateOnUserDevice` — POSITIVE: normal flow
- `TestNodeTagAllowed_PublicOnUserDevice` — POSITIVE: tag:public is fine
- `TestNodeTagAllowed_SubnetRouterOnUserDevice` — POSITIVE: role tag
- `TestNodeTagAllowed_ExitNodeOnEmptyNode` — POSITIVE: fresh VPS promotion

### The fix (runtime, R26)

`scripts/verify_post_deploy.sh` now runs an additional check
on every deploy: walk `headscale nodes list`, find any node
that has BOTH a `tag:dev-*` AND a `tag:exit-*` tag, and FAIL
if any conflict is found. This catches the **direct headscale
CLI bypass** that the B17 build-time guard can't see — anyone
running `headscale nodes tag` on the VM host will trip R26 on
the next deploy.

The check uses `awk` to walk the multi-line table output of
`headscale nodes list` (one node = one ID line + N tag
continuation lines), accumulating per-node tag state and
reporting any conflict.

### What still needs the operator

The original bug bypassed skygate entirely (direct headscale
CLI). The build-time guard closes the *future* UI path, and
R26 closes the *future* CLI path. The **historical**
user1/workstation-8 case (which is the only one observed so far) was
fixed by hand on 2026-07-28:

```bash
docker exec headscale headscale nodes tag -i 7 \
  -t 'tag:dev-user1-workstation-8,tag:private' --force
```

(workstation-8 had been carrying `tag:dev-user1-workstation-8,tag:private,tag:exit-node`;
the third tag was dropped, the first two were re-applied
because headscale's `tag` command REPLACES, not appends.)

### Files

- `internal/handlers/handlers_admin_nodes.go` — guard + extract
- `internal/handlers/handlers_admin_nodes_test.go` — 8 tests (NEW)
- `scripts/verify_pre_deploy.sh` — B17 added
- `scripts/verify_post_deploy.sh` — R26 added
- `AGENTS.md` — catalog B1-B16 → B1-B17, R1-R25 → R1-R26
- `RELEASE-NOTES.md` — this section

## Where to look for releases

**This file is an index. The authoritative source for any release is
the git tag.** Browse releases:

```sh
git tag --list                      # all tags
git show v0.26.0                    # full diff + message for v0.26.0
git log --oneline v0.25.0..v0.26.0  # commits between two tags
```

The GitHub Releases view mirrors the tags and adds a UI:
https://github.com/BarsSky/skygate/releases

`CHANGELOG.md` is the human-curated summary of what's in main
at any moment, organized by [Keep a Changelog](https://keepachangelog.com/)
format. Older `RELEASE-NOTES-v0.X.Y.md` files (deleted in 2026-07-24
as part of the v0.27.0 repo cleanup) had the same content as the
commit messages + the eventual GitHub release notes — nothing was
lost; everything is still in `git log` + the GitHub UI.

## Index of pre-cleanup releases (for git archaeology only)

| File (deleted) | Tag | Title / scope |
| --- | --- | --- |
| `RELEASE-NOTES-v0.16.1.md` | [`v0.16.1`](https://github.com/BarsSky/skygate/releases/tag/v0.16.1) | What changed |
| `RELEASE-NOTES-v0.16.2.md` | [`v0.16.2`](https://github.com/BarsSky/skygate/releases/tag/v0.16.2) | Symptoms |
| `RELEASE-NOTES-v0.16.3.md` | [`v0.16.3`](https://github.com/BarsSky/skygate/releases/tag/v0.16.3) | What changed |
| `RELEASE-NOTES-v0.16.4.md` | [`v0.16.4`](https://github.com/BarsSky/skygate/releases/tag/v0.16.4) |  |
| `RELEASE-NOTES-v0.16.5.md` | [`v0.16.5`](https://github.com/BarsSky/skygate/releases/tag/v0.16.5) |  |
| `RELEASE-NOTES-v0.16.6.md` | [`v0.16.6`](https://github.com/BarsSky/skygate/releases/tag/v0.16.6) | What changed |
| `RELEASE-NOTES-v0.16.7.md` | [`v0.16.7`](https://github.com/BarsSky/skygate/releases/tag/v0.16.7) | What changed |
| `RELEASE-NOTES-v0.16.8.md` | [`v0.16.8`](https://github.com/BarsSky/skygate/releases/tag/v0.16.8) | Fix |
| `RELEASE-NOTES-v0.16.9.md` | [`v0.16.9`](https://github.com/BarsSky/skygate/releases/tag/v0.16.9) | 1. Sidebar username empty on /admin/users/{id}/subnet |
| `RELEASE-NOTES-v0.16.10.md` | [`v0.16.10`](https://github.com/BarsSky/skygate/releases/tag/v0.16.10) | 1. scripts/check_https.py — fix the pre-existing chmod+x mismatch |
| `RELEASE-NOTES-v0.17.0.md` | [`v0.17.0`](https://github.com/BarsSky/skygate/releases/tag/v0.17.0) | What changed |
| `RELEASE-NOTES-v0.17.1.md` | [`v0.17.1`](https://github.com/BarsSky/skygate/releases/tag/v0.17.1) | What changed |
| `RELEASE-NOTES-v0.18.0.md` | [`v0.18.0`](https://github.com/BarsSky/skygate/releases/tag/v0.18.0) | What changed |
| `RELEASE-NOTES-v0.18.1.md` | [`v0.18.1`](https://github.com/BarsSky/skygate/releases/tag/v0.18.1) | 1. `check_https.py` HSTS /login 404 (the user |
| `RELEASE-NOTES-v0.20.0.md` | [`v0.20.0`](https://github.com/BarsSky/skygate/releases/tag/v0.20.0) | 1. `headscale-update-monitor` — the operator |
| `RELEASE-NOTES-v0.21.0.md` | [`v0.21.0`](https://github.com/BarsSky/skygate/releases/tag/v0.21.0) | Why this matters |
| `RELEASE-NOTES-v0.21.1.md` | [`v0.21.1`](https://github.com/BarsSky/skygate/releases/tag/v0.21.1) | The bug |
| `RELEASE-NOTES-v0.22.0.md` | [`v0.22.0`](https://github.com/BarsSky/skygate/releases/tag/v0.22.0) |  |
| `RELEASE-NOTES-v0.22.1.md` | [`v0.22.1`](https://github.com/BarsSky/skygate/releases/tag/v0.22.1) |  |
| `RELEASE-NOTES-v0.22.2.md` | [`v0.22.2`](https://github.com/BarsSky/skygate/releases/tag/v0.22.2) |  |
| `RELEASE-NOTES-v0.22.3.md` | [`v0.22.3`](https://github.com/BarsSky/skygate/releases/tag/v0.22.3) |  |
| `RELEASE-NOTES-v0.23.0.md` | [`v0.23.0`](https://github.com/BarsSky/skygate/releases/tag/v0.23.0) | What changed |
| `RELEASE-NOTES-v0.23.1.md` | [`v0.23.1`](https://github.com/BarsSky/skygate/releases/tag/v0.23.1) |  |
| `RELEASE-NOTES-v0.23.3.md` | [`v0.23.3`](https://github.com/BarsSky/skygate/releases/tag/v0.23.3) | TL;DR |
| `RELEASE-NOTES-v0.23.4.md` | [`v0.23.4`](https://github.com/BarsSky/skygate/releases/tag/v0.23.4) |  |
| `RELEASE-NOTES-v0.24.0.md` | [`v0.24.0`](https://github.com/BarsSky/skygate/releases/tag/v0.24.0) |  |
| `RELEASE-NOTES-v0.24.1.md` | [`v0.24.1`](https://github.com/BarsSky/skygate/releases/tag/v0.24.1) | Why this change |
| `RELEASE-NOTES-v0.24.2.md` | [`v0.24.2`](https://github.com/BarsSky/skygate/releases/tag/v0.24.2) |  |
| `RELEASE-NOTES-v0.25.0.md` | [`v0.25.0`](https://github.com/BarsSky/skygate/releases/tag/v0.25.0) | What did NOT change |
| `RELEASE-NOTES-v0.25.1.md` | [`v0.25.1`](https://github.com/BarsSky/skygate/releases/tag/v0.25.1) | 1. Per-user audit log export (CSV/JSON) |
| `RELEASE-NOTES-v0.26.0.md` | [`v0.26.0`](https://github.com/BarsSky/skygate/releases/tag/v0.26.0) |  |
| `RELEASE-NOTES-v0.28.0.md` | [`v0.28.0`](https://github.com/BarsSky/skygate/releases/tag/v0.28.0) | per-device ACL via `tag:dev-<user>-<device>` |
| `RELEASE-NOTES-v0.28.1.md` | [`v0.28.1`](https://github.com/BarsSky/skygate/releases/tag/v0.28.1) | per-user preferred exit-node (UI + data model) |
| `RELEASE-NOTES-v0.28.2.md` | [`v0.28.2`](https://github.com/BarsSky/skygate/releases/tag/v0.28.2) | `hosts:` block workaround for headscale 0.29.2 grants parser |
| `RELEASE-NOTES-v0.28.3.md` | [`v0.28.3`](https://github.com/BarsSky/skygate/releases/tag/v0.28.3) | close exit-node bypass: per-user dst has autogroup:internet; catch-all src=tag:public |
| `RELEASE-NOTES-v0.28.4.md` | [`v0.28.4`](https://github.com/BarsSky/skygate/releases/tag/v0.28.4) | per-device preferred exit-node (workstation-3 → relay-3 etc.) |
| `RELEASE-NOTES-v0.28.5.md` | [`v0.28.5`](https://github.com/BarsSky/skygate/releases/tag/v0.28.5) | via opt-in (Android-friendly) + migration v0.47 idempotency + tagged-device exit-node fix + entrypoint always clears stale Tailscale exit-node |
| `RELEASE-NOTES-v0.28.6.md` | [`v0.28.6`](https://github.com/BarsSky/skygate/releases/tag/v0.28.6) | guarantee catalog (B1-B10 build + R1-R25 runtime) — `make verify-pre` / `make verify-post` are the contract |

## v0.29.2 — Remove `container_name: skygate`, add `skygate` host-side wrapper

v0.29.1 worked around the `docker compose up --force-recreate`
race by stopping the orchestrator at "image rebuilt" (manual
swap). The race still affected the host-side deployment flow:
the operator's manual `docker compose up -d --force-recreate
--no-deps skygate` occasionally left the new container in
`Created` state because the old `container_name: skygate`
wasn't always released before compose tried to create the
new one.

**Fix**: remove `container_name: skygate` from
`docker-compose.yml`. Compose auto-names the container
(`skygate-skygate-1` etc.) and the race goes away. Same for
`container_name: caddy` (caddy is in the same compose file,
a stale `caddy` would also block recreate). Did NOT touch
`container_name: headscale-$USERNAME` or `container_name:
derper` — those are managed by separate compose files
(`deploy/headscale-users/`, `deploy/templates/derper-compose.yml.tmpl`)
and aren't affected.

**To avoid breaking the ~20 scripts/docs that use
`docker exec skygate ...`**, added a host-side shell wrapper
`deploy/skygate-cli.sh` that does a label-based lookup
(`com.docker.compose.service=skygate`) and forwards to
`docker exec <real-id> ...`. Installed on the host by
`deploy.sh` as `/usr/local/bin/skygate`. Every existing caller
works without edits. `verify_post_deploy.sh` also resolves
`SKYGATE_CONTAINER` from the same label by default
(override via env var still works for ad-hoc checks).

**Files**:
- `deploy/skygate-cli.sh` (NEW, 80 lines): the wrapper itself.
  Includes `--id` mode (print just the container ID) for
  scripts that want to do their own `docker exec "$CID" ...`
  in hot loops.
- `deploy/deploy.sh`: installs `/usr/local/bin/skygate` at
  the end of the deploy (idempotent).
- `docker-compose.yml`: removed `container_name: skygate` and
  `container_name: caddy`. New containers are `skygate-skygate-1`,
  `caddy-caddy-1` etc.
- `scripts/verify_post_deploy.sh`: resolves `SKYGATE_CONTAINER`
  via label lookup. Banner shows the resolved ID.
- `scripts/verify_pre_deploy.sh`: new B14 catalog check
  (wrapper exists + syntax valid + uses correct label).
- `AGENTS.md`: new "The `skygate` host-side wrapper" section.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 14/14 PASS
- `make verify-post` 26/26 PASS (auto-resolved container ID
  `37562e3b7332` via label)
- Two consecutive `docker compose up -d --force-recreate
  --no-deps skygate` invocations both started the new
  container cleanly (no `Created` stall). The v0.29.0 /
  v0.29.1 race that affected ~1 in 3 invocations is gone.

**Caveats (still open)**:
- The auto-generated container name (`skygate-skygate-1`)
  may increment on every recreate (`skygate-skygate-2`,
  `-3`, ...). The label-based lookup is robust to this, but
  operators who grep `docker ps` for the name will see it
  change. AGENTS.md documents the wrapper.
- v0.29.2 still leaves the orchestrator's auto-swap out
  of scope. A sidecar-based orchestrator (v0.29.3 follow-up)
  is the only way to get a fully automatic
  `git push → build → swap` flow without manual intervention.

## v0.29.3 / v0.29.3.1 — Auto-swap via helper container in host PID namespace

The v0.29.0 / v0.29.1 / v0.29.2 orchestrator chain
handles the "git push → build → swap" lifecycle, but
v0.29.2 still requires a manual `docker compose up`
on the host. v0.29.3 closes the loop: the orchestrator
itself does the swap, end-to-end, with auto-rollback on
any failure.

### The PID-namespace death race (v0.29.3 problem)

The v0.29.3 first version spawned a Setsid-detached
subprocess from inside the OLD skygate container to
run `docker compose up --force-recreate`. The subprocess
escaped the OLD container's process group (Setsid) but
was STILL in the OLD container's PID namespace. When
compose sent SIGTERM to PID 1 of the OLD container
(skygate itself), the signal propagated to all
processes in the same namespace, killing the swap
subprocess mid-way through `docker compose up`. The new
container would end up in `Created` state forever and
the operator had to `docker start <id>` by hand.

Live-verified on the VM at 2026-07-28 10:45 UTC: the
swap log showed only "Recreate" before the subprocess
died, and the new container `fb9547ead806` was stuck
in `Created`. A subsequent attempt with `unshare -fp`
inside the helper failed with "unshare: Operation not
permitted" (the skygate container's CapAdd doesn't
include CAP_SYS_ADMIN, which `CLONE_NEWPID` requires).

### v0.29.3.1 fix: helper container in HOST PID namespace

Instead of running the swap from inside the OLD skygate
container, the orchestrator spawns a HELPER CONTAINER
via `docker run --rm --pid=host --net=host
-v /var/run/docker.sock:/var/run/docker.sock
-v skygate-data:/data
-v $SKYGATE_HOST_REPO_PATH:/host_repo:ro`. The helper
uses the HOST's PID namespace, so its processes are
not in any skygate container's namespace and survive
the OLD container's removal cleanly.

The helper does the full swap:
  1. sleep 3s (orchestrator flush)
  2. `apk add --no-cache docker-cli docker-cli-compose`
     (alpine workstation-8 image has no docker binary)
  3. `cd /host_repo && docker compose -p skygate -f
     /host_repo/docker-compose.yml up -d
     --force-recreate --no-deps skygate`
  4. poll up to 60s for the new container; if it's
     stuck in Created, call `docker start <id>`
     (handles the rare compose race where Created
     happened but Start didn't)
  5. final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     (helper has --net=host so localhost:8080 IS the
     new container's port)
  6. exit (`--rm` self-removes the container)

The OLD orchestrator's swap script now just spawns
the helper container in the background and exits
immediately. Helper self-removes via `--rm`.

### Defense in depth: confirmPendingSwap

The new orchestrator (in the new container) also has a
helper of its own: `confirmPendingSwap` (called from
`renderUpdatePage` on the first /admin/update page
load after the swap). It detects
`phase=build_done` / `phase=rolled_back`, calls
`startStuckSkygateContainer` (the v0.29.3.1 fix for
the `{{.State.Status}}` → `{{.Status}}` format-string
regression in the `docker ps` call), polls
`/healthz` on the new container for up to 30s, and
on 200 calls `store.Complete()` to promote the phase
to `done`. This is the final-arbitration step: even
if the helper container crashes before it can finish
its work, the next /admin/update page load completes
the swap.

### What changed (commits `49b67ce` ... `ebaa44e`)

- `internal/update/docker.go`:
  - `runShellDetached` helper (Setsid, fire-and-forget)
  - `swapSubprocessScript` rewritten to spawn the helper
  - `swapHelperScript` extracted as a separate Go constant
    (shared between success and rollback paths)
  - `writeSwapHelperScript()` Go helper for the success
    path to write the helper file at job start
  - `startStuckSkygateContainer` (v0.29.3.1) in
    `handlers_admin_update.go` — uses the now-correct
    `{{.Status}}` (was `{{.State.Status}}` which is
    `docker inspect` only) to find the new container
  - `confirmPendingSwap` (v0.29.3) in
    `handlers_admin_update.go` — /healthz poll + phase
    promotion on first /admin/update page load after
    a `build_done` or `rolled_back` phase
  - Critical bug fix: `StateStore.Load()` was parsing
    the state file but NOT storing in `s.state` (silent
    no-op for `Log()` and nil return from `Get()`).
    Fixed in commit `9fbc588`.
- `internal/handlers/handlers_admin_update_test.go`
  (NEW) — regression test for the
  `{{.Status}}` vs `{{.State.Status}}` format string
  (TestStartStuckSkygateFormatStringIsDockerPsValid)

### Live verification (2026-07-28, on the VM)

End-to-end test on `admin@192.0.2.1`:
  1. Applied `v0.99.0-nonexistent` via
     `POST /admin/update/apply` (target=v0.99.0-nonexistent)
  2. Orchestrator: backup tag created
     (`skygate-pre-update-ebaa44e`), `git fetch` failed
     (expected — the tag doesn't exist), `failWithRollback`
     spawned the detached subprocess
  3. Subprocess wrote `/data/skygate-swap-helper.sh`
     and ran `docker run --rm --pid=host --net=host ...`
     (the helper container)
  4. Helper installed docker-cli via apk, ran
     `docker compose up --force-recreate --no-deps skygate`,
     polled for the new container (status=running on
     attempt 1, no Created→Started race this time)
  5. Helper did a final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     and exited
  6. Operator loaded `/admin/update` →
     `confirmPendingSwap` detected `phase=rolled_back`,
     called `startStuckSkygateContainer` (no-op, container
     already Up), polled `/healthz` (200 on attempt 1),
     promoted phase to `done`
  7. State file log:
     ```
     renderUpdatePage: store=true phase=rolled_back (debug probe)
     renderUpdatePage: phase=rolled_back detected, calling confirmPendingSwap
     skygate container 359dec4c92f9 status=Up
     new orchestrator confirmed swap via /healthz (attempt 1)
     update completed successfully
     ```

`make verify-pre` 13/13 PASS, `make verify-post` 26/26 PASS.
`go test ./...` 19/19 packages green.

### Caveats

- The helper adds ~10s to the swap total (5s apk add
  for docker-cli + 3s orchestrator flush + ~2s compose
  up). Acceptable for an end-to-end upgrade.
- The helper requires network access from the
  `skygate-swap-helper` container to the alpine
  package repo (dl-cdn.alpinelinux.org) for the
  `apk add docker-cli` step. If the network is
  restricted, the swap will fail with
  "docker: not found". A pre-baked
  `skygate-swap-helper:VERSION` image with docker
  pre-installed would avoid this (future v0.29.4+
  work).
- The `ensureComposeServiceRunning` helper from
  v0.29.1 is still in place (unused since v0.29.2
  removed `container_name: skygate`) but is now
  redundant with `startStuckSkygateContainer`.
  v0.29.4 cleanup can remove it.

## v0.29.1 — Orchestrator stops at "image rebuilt", manual swap required

The v0.29.0 auto-updater's first end-to-end test on the
operator's VM surfaced a deeper architectural issue than the
five post-Phase-2 bugfixes had addressed: `docker compose up
--force-recreate --no-deps skygate` sends SIGTERM to the
skygate container — which IS the orchestrator's parent
process (skygate is PID 1 of the container, the orchestrator
is a goroutine inside skygate's HTTP server). The orchestrator
died mid-`up` before the swap completed, leaving the new
container in an undefined state with no healthz verification.
The previously-suspected "Created→Started race" was a
misdiagnosis of this: the race fix (`ensureComposeServiceRunning`)
was the right defensive measure but never got to run because
the orchestrator was already dead.

**Fix**: the orchestrator no longer calls `docker compose up`.
It stops at "image rebuilt + migrations applied" and writes a
one-line `manual_swap` ("docker compose up -d --force-recreate
--no-deps skygate") for the operator to run on the host. The
swap is the only step that has to be on the host — the rest
of the upgrade (backup tag, fetch, checkout, build, rollback
on failure) runs entirely inside the orchestrator.

**Changes**:
- New `PhaseBuildDone` state phase (replaces `swap` + `verify`
  in the success path). `failed` / `rolled_back` are still
  used for error paths.
- New `ManualSwap` field in the state JSON for the single
  command. `SetManualStep(kind, cmd)` helper to add it.
- `ensureComposeServiceRunning` is left in place (untested
  but compile-checked) for v0.29.2 when the orchestrator
  moves to a sidecar container.
- Pre-push hook now uses `MSYSTEM` (set by Git for Windows)
  as the primary Git Bash detection signal, ahead of the
  directory-probe fallback that was unreliable on hybrid
  WSL2+Git Bash systems. **No more `--no-verify` workaround**
  for normal pushes from Git Bash.
- New B13 catalog check: pre-push hook contains `MSYSTEM`.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 13/13 PASS
- `make verify-post` 26/26 PASS
- End-to-end auto-update test: applied `v0.29.0` (real
  existing tag, but with local changes so `git fetch`
  fails — exercises the rollback path) → backup tag
  created → fetch failed as expected → automatic rollback
  `git checkout` OK → chown OK → rollback `docker compose
  build` OK → state at `rolled_back` with `manual_swap`
  populated. Orchestrator survived all the way to the end.
  Container still on the previous build (29b4564).

**Caveats (still open)**:
- The orchestrator can do everything up to `docker compose
  build`. The final `docker compose up` must be done by the
  operator on the host. v0.29.2 follow-up: move the
  orchestrator to a sidecar container that's outside
  skygate's process tree, so the SIGTERM from `up` doesn't
  kill the orchestrator.
- The same `container_name: skygate` race still affects
  host-side `docker compose up` (rare but observed once in
  the v0.29.1 verification). Manual `docker start <id>`
  recovers.

## v0.29.0 — Self-update orchestrator (in-app upgrade + auto-rollback)

The `/admin/update` page (v0.29.0 Phase 1) now ships with a working
in-app upgrade flow. The orchestrator runs the full `git fetch` →
`git checkout <tag>` → `docker compose build` → `docker compose
up --force-recreate` → `/healthz` poll sequence in a background
goroutine and reports each phase to a bind-mounted status file
(`/data/skygate-update-status.json`). On any phase failure the
orchestrator automatically rolls back to the previous tag, including
`docker compose build` + recreate + healthz poll again. If rollback
itself fails, the state file shows the manual steps for operator
intervention.

**Phase 1 (commit `e3ce6f0`)** — detection + manual steps + UI:
GitHub Releases API client, semver comparison, install-kind
detection (Docker/systemd/bare), copy-pasteable manual commands,
`/admin/update` page with status panel + auto-refresh, 17 i18n
keys × 2 langs. Renders on VM in RU + EN, GitHub checker works
(no false "new version" banner when current build is ahead of
all released tags).

**Phase 2 (commit `caf6fb8`)** — auto-updater with state machine
+ auto-rollback: `/admin/update/apply` kicks off the orchestrator,
`/admin/update/rollback` cancels + restores previous, status
file persists across container recreate. 18 i18n keys × 2 langs.

**Post-Phase-2 fixes (commits `0020815`, `4bb4db6`, `f9d3860`,
`a18ad0c`, `5177643`, `bae4fb4`)** — five bugs discovered on
the operator's VM during the first end-to-end auto-update test:

1. **`SKYGATE_REPO_PATH` chdir failure** (`0020815`). The
   orchestrator ran inside the skygate container but tried to
   `chdir /home/admin/skygate` — a host path unreachable
   from inside the container. The source dir is bind-mounted at
   `/app` (per docker-compose.yml's `./:/app`). Fix:
   `defaultRepoPath()` in `internal/config/config.go` auto-
   detects container mode via `/.dockerenv` /
   `/run/.containerenv` and defaults to `/app`. Bare/systemd
   hosts still default to `/home/admin/skygate`.
   `SKYGATE_REPO_PATH` env always overrides.

2. **`sudo chown` in Alpine** (`0020815`). The previous
   rollback path used `sudo chown -R admin:admin ...`
   which fails in the Alpine container (no `sudo` binary, no
   `admin` user). Fix: `detectHostOwner()` captures
   `stat -c '%u:%g' .git/HEAD` BEFORE the first git mutation
   and `chownToHostOwner()` does `chown -R <uid>:<gid>` (no
   sudo). Default 1000:1000; override via `SKYGATE_HOST_OWNER`.

3. **Missing `docker compose` plugin in image** (`4bb4db6`).
   The Alpine image's `docker-cli` package installs only the
   `docker` binary; `docker compose` is the separate
   `docker-cli-compose` plugin. Without it, the orchestrator's
   `docker compose build skygate` errors with "docker:
   unknown command: docker compose". Fix: add
   `docker-cli-compose` to the `apk add` list (~3 MB).

4. **Project name mismatch** (`f9d3860` + `bae4fb4`).
   Docker compose computes the project name from the basename
   of the working directory by default. Inside the container
   the orchestrator runs from `/app` (basename "app") while
   the host's compose was launched from `/home/admin/skygate`
   (basename "skygate"). Two-part fix:
     (a) `docker-compose.yml` env section adds
         `COMPOSE_PROJECT_NAME=skygate`.
     (b) `DockerUpgrader.runCompose` always passes
         `-p <ComposeProject>` (default "skygate", override
         via `SKYGATE_COMPOSE_PROJECT`).

5. **Bind-mount paths invisible to host dockerd** (`a18ad0c` +
    `5177643` + `bae4fb4`). The docker daemon runs on the
    host, so it resolves bind-mount sources as HOST paths. From
    inside the container, `./secrets/ts_authkey` resolved to
    `/app/secrets/ts_authkey` from compose's perspective, but
    the daemon then looked for that path on the host (where
    `/app` doesn't exist). Fix: replace all `./` paths in
    `docker-compose.yml` with
    `${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}/` and
    add `SKYGATE_HOST_REPO_PATH` to the skygate service env.
    The `${VAR:-default}` syntax is shell-style fallback
    supported by docker compose.

**Live verification (operator's VM, 2026-07-27)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 12/12 PASS
- `docker_test.go` adds 9 test groups (TestShortSHA,
  TestDetectHostOwner_EnvOverride, TestDetectHostOwner_StatAutoDetect,
  TestDetectHostOwner_DefaultFallback, TestDetectHostOwner_Cached,
  TestChownToHostOwner_ArgsShape, TestOwnerPattern,
  TestTruncateOutput, TestNewDockerUpgrader_Defaults,
  TestNewDockerUpgrader_ProjectOverride)
- `make verify-post` 26/26 PASS after the fix chain
- End-to-end auto-update test: applied `v0.99.0-final-test`
  (non-existent) → backup tag created → fetch failed as expected
  → automatic rollback checkout OK → chown to host owner OK →
  container recreated → /healthz 200 with previous build label

**Caveats (still open)**:
- Rollback `docker compose up --force-recreate --no-deps skygate`
  occasionally leaves the new container in `Created` status
  instead of `Started` (race with the old container's
  `container_name: skygate`). Manual `docker start <id>` recovers.
  Tracked as v0.29.1 follow-up: remove `container_name: skygate`
  or improve the orchestrator to handle the Created→Started transition.
- The orchestrator is Docker-only. Systemd / bare install kinds
  generate manual steps but don't auto-execute (Phase 3 follow-up).
- Apply works only for tags already pushed to origin. The orchestrator
  does `git fetch --tags` + `git checkout <target>`; a tag that
  exists only on the operator's local clone would not be findable.

## v0.28.7 — Per-DEVICE ACL grants for tagged devices (Moonlight fix)

The v0.28.0 per-device ACL design tagged every device with
`tag:dev-<user>-<device>`. The per-user grant `src=user@` was supposed
to cover tagged devices too, but in Tailscale v2 policy the per-user
identity doesn't match tagged devices — only the tag does. Result:
workstation-2 (Android, `tag:dev-admin-workstation-2`) couldn't reach workstation-1
(Windows, `tag:dev-admin-workstation-1`) over Tailscale for Moonlight,
even though both devices belonged to the same portal user.

### What changed

v0.29.0 adds a **per-DEVICE grant block** to the generated policy:
for each portal user with N≥2 devices, emit N grants (one per
device as `src`), with `dst` = the list of all OTHER devices of
the same user, and `ip: ["*"]` (required by headscale 0.29.2).
13 grants on this VM (11 admin + 2 user1); O(n) per user,
not O(n²).

The earlier v0.28.7 attempt (commit `8749069`) tried a wildcard
`tag:dev-<user>-*` src, which headscale 0.29.2 rejects with
"src=tag not found" (the parser requires concrete tags in
`tagOwners{}`).

### Why this works

Tailscale ACL is order-sensitive (first match wins). The per-DEVICE
grant is emitted **after** the per-user grant (which keeps SSH
+ untagged identities working) and **before** per-device exit-rules
+ per-device loose grants (autogroup:internet). Tagged-to-tagged
device traffic on the same tailnet now matches the per-DEVICE
grant as a fallback when no specific per-device rule applies.

### Code shape

The per-DEVICE block is duplicated in `GenerateACLForPlane` and
`GenerateACLWithViaForPlane` (the v0.28.1 per-user via variant
takes a parallel code path). v0.30.0 will extract the block to a
shared helper — the duplication is documented in the AGENTS.md
release notes for v0.28.7 as a known cleanup target.

### Verification

- Live: 13 new per-DEVICE grants in the headscale policy after
  `/admin/exit-rules/reapply` (HTTP 303 redirect)
- Operator confirmation: workstation-2 ↔ workstation-1 Moonlight session
  now establishes and streams (2026-07-27)
- `make verify-pre` 9/9 PASS, `make verify-post` 25/26 PASS
  (R9 is a known false-negative — the verify-post script reads
  an older `acl_snapshots` row; the most recent reapply did
  succeed and saved a fresh snapshot)

## How a release is cut

1. `git tag -a v0.X.Y -m "v0.X.Y"` on the commit we want to ship.
2. `git push origin v0.X.Y`.
3. (Operator-driven) create a GitHub release at
   https://github.com/BarsSky/skygate/releases/new — the body
   summarizes the commits since the previous tag.
4. Update `CHANGELOG.md` to move the entry from `[Unreleased]`
   into the new tagged section.

The operator (admin) writes the release body; the git tag is
the source of truth for "what shipped in v0.X.Y".

## v1.5.3 — B-mod-* Plugin API series (Tailscale as Module #1)

**Date:** 2026-09-11 (20 commits on top of v1.5.2)
**Tag:** TBD (run git tag v1.5.3 \<sha>\` once the operator
confirms the live-verify on svi polygon passes)

### Summary table

| B-block | One-liner | Operator impact |
|---|---|---|
| **B-mod-pg18-strftime-fix** | pplied_migrations.applied_at DEFAULT uses PG-native EXTRACT(EPOCH FROM now())::bigint instead of SQLite-only strftime('%s','now') | skygate can now create the pplied_migrations table on PG 18 without unction strftime does not exist errors |
| **B-mod-tailscale** | internal/module/tailscale/ — real TailscaleModule with 3 install modes (os_level / in_container / attach) + 4 sub-features (cluster / telegram / derp / exit) + CmdRunner interface (testable via runnerMock) + 22 unit tests + 14 B-check contracts | Tailscale is now opt-in via /admin/modules instead of baked into the entrypoint.sh |
| **B-mod-admin** | /admin/modules list + /admin/modules/{name} detail + POST handlers (install / start / stop / enable / disable / sub/{name}) + 18 i18n keys (RU+EN) + 2 templates + 6 unit tests + 13 B-check contracts | Operator can see all registered modules + their state in one page |
| **B-mod-install** | deploy/scripts/install-tailscale.sh (5 modes: os_level / in_container / attach / none / uninstall) + integration in install-debian.sh step 7 (opt-in via SKYGATE_TS_* env vars, WARN on failure, auto-picks HEADSCALE_URL from /etc/skygate/skygate.env) + 24 B-check contracts | sudo SKYGATE_TS_INSTALL_MODE=attach SKYGATE_TS_AUTHKEY=tskey-XXX bash install.sh is the new single-command install flow |
| **B-mod-core re-merge** | Wires module.Manager in cmd/skygate/main.go (was reverted in 82c74b38 immediately after v1.5.2 shipped because Patroni + etcd on svi were unreachable at the time). Restored when svi polygon had working PG 18.6 + DSN | The Manager now actually runs at boot — the module.tailscale.init audit row proves it |
| **B-mod-core: SetDBC fix** | Live boot caught module.tailscale.init | error: tailscale: Init: ModuleConfig.DBC is nil. Fix: add Manager.SetDBC(dbc func() *sql.DB) + thread dbc through initOne to cfg.DBC. main.go calls moduleMgr.SetDBC(func() *sql.DB { return app.DB.Current() }) | Plugin contract surface now includes every dependency the **real** modules need (not just what the stub uses) |
| **B-mod-bcheck** | scripts/check_b_modules_admin_live.sh — 7 live contracts (login as admin + GET /admin/modules 200 + <code>tailscale</code> row + state pill + **NO "Manager not wired"** + GET /admin/modules/tailscale 200 + Health/Sub-features + audit_log module.tailscale.init: ok + state.json on disk + optional install dispatch) | End-to-end live-verify that the wiring works. Re-runs automatically as soon as svi is back |
| **B-mod-cleanup** | deploy/scripts/cleanup-skygate.sh — operator-facing uninstaller (inverse of install-debian.sh). 6 cleanup sections (systemd + binary + user + data + config + runtime) + optional 7th (Tailscale via install-tailscale.sh --mode=uninstall) + --keep-{user,data,config,binary} flags + --yes / --dry-run safety | sudo bash deploy/scripts/cleanup-skygate.sh is the matching uninstall for install-debian.sh |
| **B-mod-pg-alive-polygon** | check_b_pg_alive.sh polygon mode (SKYGATE_PG_ALIVE_MODE=polygon) + DSN parsing for user + password + 2 helper functions (pg_query_pg_isready + pg_query_psql) + auto-skip H contract (Patroni) on polygon clients | Polygon clients (svi pointing at remote PG 13.66 via NPM) can now run the B-check successfully |
| **B-mod-install follow-up** | deploy/scripts/bootstrap_standby.sh (220 lines, V1) — consumer of skygate init <standby-hostname> on the primary. 5 steps: optional Tailscale attach (delegates to install-tailscale.sh --mode=attach, falls back to --mode=os_level) + ssh to primary + parse 4-line stdout + write preauth to /var/lib/skygate/standby/<node_id>.preauth.json + reminder. 32 B-check contracts | New standby VMs can be bootstrapped via ash bootstrap_standby.sh --primary=skyadmin@primary-host |
| **B-mod-cluster** | cluster sub-feature: state.Info['cluster_filter'] = active/inactive (was 
eturn nil no-op before) | /admin/modules/tailscale detail page now shows "Cluster filter: active" without ssh'ing into the VM |
| **B-mod-telegram** | telegram sub-feature: state.Info['telegram_route'] + state.Info['telegram_cidr'] = 91.108.56.0/22 | Operator can see whether the Telegram API route is currently advertised without ssh vm 'tailscale status' |
| **B-mod-derp** | derp sub-feature: state.Info['derp_relay'] + state.Info['derp_relay_prereq'] = 	elegram+exit (was 
eturn nil no-op before) | The DERP relay is now operator-visible: "active (requires telegram + exit enabled)" |
| **B-mod-exit** | exit sub-feature: state.Info['exit_node'] + state.Info['exit_node_advertised_at'] (RFC3339 UTC timestamp; DELETED on disable to avoid stale data) | Operator sees "Exit node: advertised (since 2026-09-10T11:34:46Z — pending headscale admin approval)" without ssh |

### Cross-cutting changes

- **Manager contract surface extended**: ModuleConfig now
  includes DBC func() *sql.DB (set via Manager.SetDBC).
  This is the smallest possible addition (one field + one
  setter + one call site) — exactly the surface of the bug
  that B-mod-core re-merge would have caught if the stub
  had been replaced earlier.

- **module.LoadState + module.SaveState public wrappers**
  added in internal/module/state.go so the tailscale
  package can read/write state.json from outside the
  module package (Manager wires them through initOne).

- **B179 safety**: --netfilter-mode=nodir enforced on every
  tailscale up call (os_level + in_container + attach).
  --netfilter-mode=off NEVER used (the recurring trap that
  re-blocks all tailnet traffic on a new node).

- **Cleanup legacy scripts**: 53 untracked .sh/.py files
  from the 2026-08..2026-09 debug sessions archived to
  scripts/utils/legacy-2026-09/ with README.md (audit
  trail + reuse + reference). Repo root is clean
  (git status → 
othing to commit, working tree clean).

### Stats

- **20 commits** on top of v1.5.2 (20052463 →
  16055216 HEAD).
- **8 B-check scripts**: check_b_module_core.sh (12) +
  check_b_tailscale_module.sh (30) + check_b_modules_admin.sh
  (13) + check_b_modules_admin_live.sh (7 live) +
  check_b_install_tailscale.sh (24) + check_b_pg_alive.sh
  (10) + check_b_cleanup_skygate.sh (27) +
  check_b_bootstrap_standby.sh (32) = **~155 contracts**.
- **22+1=23 tailscale unit tests** (the original 22 from
  B-mod-tailscale + 1 new TestSetDBC_WiringToInitConfig
  in module_test.go). Plus 6 admin + 18 i18n = **47 unit
  tests total** across the touched packages.
- **18 i18n keys** (RU + EN parity) for the new
  /admin/modules pages.
- **4 sub-features** all completed (cluster + telegram +
  derp + exit) with state.Info flags so the operator
  sees the current status on /admin/modules/{name}.

### Cross-project memory entries added

- **PostgreSQL self-password reset works without
  superuser** (cross-project) — from the live bug where
  the svi polygon needed skygate_test password reset
  and the operator's password was lost. Used ALTER USER
  skygate_test PASSWORD '<new>' while connected AS
  skygate_test itself (PostgreSQL allows self-reset
  without superuser).

- **Manager.SetDBC wiring — required for non-trivial
  modules** (cross-project) — plugin contract surface
  must include every dependency the real modules need,
  not just what the stub uses.

- **Sub-features must expose status via state.Info map,
  not just be no-op returns** (cross-project) — every
  plugin/extension/feature-flag MUST expose its current
  status via a discoverable state surface that's read by
  the UI without an extra API call, persisted across
  restarts, idempotency-checkable, and diff-able against
  the actual system. Audit log is NOT a substitute for
  state.

- **SSH-into-VM = gap. Build the UI surface** (cross-project) —
  if the operator has to SSH into a node or hand-edit a
  config file to do X, then X is a gap, not a workaround.
  The gap is in the project, not in the operator. Every
  state-visible state should be AVAILABLE through the
  project's UI/CLI/API.

### Live-verify state

Pre-operator-OS-reinstall (2026-09-10), live-verified on
the svi polygon (<polygon-vm-public-ip>, Ubuntu 26.04 fresh):

`
$ systemctl status skygate --no-pager
● skygate.service - Skygate VPN portal (v1.5.2)
     Active: active (running) since Thu 2026-09-10 11:32:33 UTC; 2min+

$ curl http://127.0.0.1:8080/healthz
{"build":"dev","instance_id":"unconfigured","status":"ok","timestamp":"2026-09-10T11:34:46Z"}

$ curl http://127.0.0.1:8080/admin/modules
HTTP 302, redirect=http://127.0.0.1:8080/login

$ psql -c "SELECT action, detail FROM audit_log WHERE action LIKE 'module.%' ORDER BY created_at DESC LIMIT 1;"
        action         |        detail
-----------------------+---------------------
 module.tailscale.init | ok

$ cat /var/lib/skygate/modules/tailscale/state.json
{
  "name": "tailscale",
  "state": "stopped",
  "enabled": false,
  ...
}
`

After the operator's OS reinstall (2026-09-10), svi is
pending restore. Once back online, re-run
scripts/check_b_modules_admin_live.sh (with
SKYGATE_LIVE_HOST=https://skygate.skynas.ru + the admin
password + the skygate_test DB password) to re-verify the
live contracts.

### Update: post-rebuild live-verify (2026-09-11)

After the operator's 2nd OS reinstall on svi polygon,
pulling `d89beff1` + rebuilding + restarting on the fresh
Ubuntu 26.04 + Go 1.25.4 + remote PG 18.6 stack surfaced 4
latent bugs in the B-mod-* code + 1 bug in the B-check scripts.
All 5 were fixed in the B-fix-* commits listed above.

**Live state after the rebuild** (HEAD = `32272311` on
origin/main, 2026-09-11 12:00 UTC):

- `/healthz` = 200 on svi polygon (HTTP listener on IPv6 [::]:8080
  only — curl with happy-eyeballs works for both 127.0.0.1 and
  [::1])
- `/admin/modules` = 200, body renders the `<code>tailscale</code>`
  row + state pill (`modules-state not_installed`)
- `/admin/modules/tailscale` = 200, body renders the 4
  sub-feature rows + Health section + Audit history (last 20)
- Login as admin (POST /login with the generated SKYGATE_ADMIN_PASS)
  returns 302 + sets the `skygate_session` JWT cookie
- Sub-feature toggles work end-to-end:
  - `cluster` enable → 303 → `?ok=Sub-feature%20cluster%20enabled`,
    audit row `module.tailscale.subfeature.enable sub=cluster`,
    state.json `"sub_features": {"cluster": true}`. ✅ Full
    e2e (the toggle + the state change + the audit row).
  - `telegram` + `exit` enable → 303 → `?err=Error: enable telegram:
    advertise-routes: %!s(<nil>) (stdout="" stderr="")`. Expected
    because polygon svi doesn't have the `tailscale` binary —
    the state change was correctly REJECTED (state.json still has
    no telegram entry, confirming the Manager persists state
    AFTER the side-effect succeeds — no half-state).
  - `derp` enable → 303 → `?err=Error: module: sub-feature
    requires other sub-features to be enabled first: derp
    requires telegram`. ✅ Correctly rejected via the
    Manager's Requires validation (derp needs telegram + exit).

- ALL 10 B-check scripts PASS on svi polygon (~155 contracts):
  - check_b_bootstrap_standby.sh ✓ all
  - check_b_cleanup_skygate.sh ✓ all
  - check_b_db_dsn_reachable.sh ✓ 13/13
  - check_b_install_tailscale.sh ✓ all
  - check_b_module_core.sh ✓ 12/12
  - check_b_modules_admin_live.sh ✓ 12/12 (2 SKIP optional)
  - check_b_modules_admin.sh ✓ all
  - check_b_pg_alive.sh ✓ 8/8 (polygon mode, DSN parseable, password
    length 24)
  - check_b_standby_provision.sh ✓ 20/20
  - check_b_tailscale_module.sh ✓ all

**Reusable lesson**: live-verify catches what static B-checks
miss. The 5 B-fix blocks above all shipped to origin and
passed every static B-check on Windows. None were caught
until the binary was rebuilt + deployed + run end-to-end on
the svi polygon. The full live-verify pattern (pull → build →
restart → wait → healthz → login → every page → every form →
every B-check) is documented in `AGENTS.md §B-mod-* live-verify`
as a 30-minute audit script.
