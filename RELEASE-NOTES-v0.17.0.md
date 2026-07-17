# v0.17.0 — exit-node mesh + tag:subnet-router in ACL

2026-07-17

The v0.16.0+ per-user subnets roadmap promised v0.17.0:
cross-subnet exit-node sharing (the original use case
that motivated the whole feature) + `tag:subnet-router`
in ACL `tagOwners` so headscale accepts the v0.16.7
sidecar nodes. This release delivers both.

## What changed

### 1. `internal/db/queries.go` + `internal/db/portal_users.go`

  - New query `qSelectUserSubnetsForPlane` — LEFT JOIN
    of `portal_users` and `user_subnets` (NULL/empty
    cidr for users without a subnet).
  - New helper `GetUserSubnetsForPlane(d, planeURL)
    ([]UserSubnet, error)` — returns
    `(username, cidr)` pairs on the given plane.
  - Used by `GenerateACLForPlane` to extend the
    per-user rule with the CIDR (next item).

### 2. `internal/acl/acl.go` — `GenerateACLForPlane`

  - Pulls the `(username, cidr)` map in addition to
    the identities list.
  - **Per-user rule extension**: if the user has a
    subnet, the rule's `dst` becomes
    `["<user>@tsnet.skynas.ru:*", "10.0.<uid>.0/24:*"]`
    (the CIDR is unique per user, so alice can
    reach 10.0.1.0/24 but not 10.0.2.0/24 — first-match
    semantics handle the isolation).
  - **`tag:subnet-router` in tagOwners**: owned by
    every portal user. Without this entry, headscale
    rejects the policy with "tag not found:
    tag:subnet-router" when the v0.16.7 sidecar
    registers. The auto-approver in `internal/sidecar`
    already issues preauth keys with this tag — the
    ACL change makes headscale accept the resulting
    node + the 10.0.<uid>.0/24 route it advertises.

### 3. Exit-node mesh (regression guard)

  - The `* → tag:exit-node:*` and `* → tag:public:*`
    rules are already in place from v0.12.0.1/v0.14.0
    v7. v0.17.0 adds a dedicated test
    (`TestGenerateACL_ExitNodeMeshStillGlobal`) to
    ensure a future ACL refactor doesn't accidentally
    scope exit-node or public-relay rules to per-user
    identities and break the operator's existing
    exit-node routing (emilia, sharlotta, karolina)
    for users who haven't yet allocated a subnet.

### 4. `/admin/acls` page

  - Added a v0.17.0 info card explaining the new
    `tag:subnet-router` entry + the per-user CIDR
    extension. Operator-visible: clicking the new
    "Per-user subnets" button takes you straight to
    the v0.16.10 overview page.

### 5. i18n

  - 4 new keys × 2 langs: `acls.import_btn_short`,
    `acls.v0_17_0_note_title`, `acls.v0_17_0_note_body`.
  - Parity test green.

### 6. Test schema

  - Added the `user_subnets` table to
    `internal/acl/acl_test.go`'s minimal in-memory
    schema so the per-user CIDR test can run.

## Tests green

  - 12/12 packages
  - 2 new ACL tests:
    - `TestGenerateACL_PerUserSubnetCIDR` — users
      with a subnet get the extended rule; users
      without don't. The CIDR is unique per user
      (verified by checking that bob's CIDR never
      appears in alice's rule).
    - `TestGenerateACL_ExitNodeMeshStillGlobal` —
      regression guard for `* → tag:exit-node:*`
      and `* → tag:public:*`.
  - `TestGenerateACLValidJSONShape` extended to
    check for `tag:subnet-router` in tagOwners.
  - All other ACL tests still green.

## Architecture note

The per-user subnet dst rule is added by the ACL
builder, not by a headscale runtime config. This
keeps the policy in one place (regenerated on every
`/add_rule`, `/delrule`, `/admin/acls/reapply`) and
ensures a fresh `ApplyACLForAllPlanes` push picks up
newly-allocated subnets. The downside: if a user
gets a new subnet via `POST /admin/users/{id}/subnet/allocate`,
the ACL isn't re-pushed automatically (the v0.16.6
admin handler doesn't call `ApplyACLPipelineForPlane`).
A future patch (v0.17.1?) could add a "sync ACL"
hook to the Allocate handler so the new subnet is
immediately routable. For v0.17.0, operators can
click "Re-apply ACL" on `/admin/exit-rules` (the
v0.14.0 v7 button) to push.

## Live verification

  - `GenerateACL` (called via `/admin/exit-rules/reapply`
    on the live VM) now includes
    `tag:subnet-router` in tagOwners + the
    per-user CIDR dst rule for users with a
    subnet. The `* → tag:exit-node:*` mesh rule
    is unchanged.
  - Smoke 118/118.
  - `/admin/acls` page renders the v0.17.0 info
    card with the right text in both RU and EN.

Deployed to VM, live at build `79c5951`.
