#!/usr/bin/env bash
# scripts/lib/acl_pin_expectation.sh — B437 (2026-10-28)
#
# WHY THIS EXISTS. Three live contracts (B188.2 S, V and W) each hardcoded one
# number — "this device has exactly ONE pinned autogroup:internet grant", "the total
# equals the number of via_enabled rows" — and every one of those numbers was true
# on the day it was written and wrong afterwards, because the GENERATOR's own rule
# changed twice under them:
#
#   * B398 (2026-10-18): a DERIVED preference (`set_by_user_id = 0`, written by
#     skygate's reconciler) must not become a permission filter. Measured live:
#     `via` on an `autogroup:internet` grant is a filter, so a guessed pin locked
#     the device out of every exit node but one.
#   * B284: the device's tag comes from `node_owner_map`, not from a synthesised
#     `tag:dev-<user>-<host>` (the synthesised form once referenced an undeclared
#     tag and headscale rejected the WHOLE policy).
#   * B356: the preferred relay must be USABLE (B273's predicate: `online` |
#     `untagged`; "never measured" counts as usable).
#
# A contract that restates the rule in its own words drifts from the rule. This
# helper states it ONCE, in SQL, in the same shape the generator applies, so the
# contract and the product can only disagree when the product is wrong.
#
# CONTRACT. Callers must have run `skygate_live_db_probe` first (AGENTS rule 1:
# "no rows" and "no connection" must never look alike). Every function here then
# prints a plain list and returns 0, so a caller distinguishes:
#   probe failed          → skip, name skygate_live_db_reason
#   probe ok, empty list  → the honest answer "nothing should be pinned"
#
# USAGE
#   . scripts/lib/db_credentials.sh
#   . scripts/lib/acl_pin_expectation.sh
#   skygate_live_db_probe || skip "live DB: $(skygate_live_db_reason)"
#   skygate_pinned_catchall_tags            # tag:dev-michail-basic\ntag:dev-...
#   skygate_pin_expectation_for_tag <tag>   # prints 1 or 0

# The devices that MUST carry a pinned `autogroup:internet` grant right now, one
# `tag|hostname` row per device. Mirrors internal/acl/acl_generate_via.go exactly:
#
#   SELECT ... device_exit_node_prefs p
#     JOIN node_owner_map r ON lower(hostname) = lower(device_hostname)  -- B284
#    WHERE p.via_enabled = 1 AND exit_node_tag <> ''                     -- opt-in
#      AND p.set_by_user_id > 0                                          -- B398
#      AND the named relay is not known-unusable                          -- B356
skygate_pinned_catchall_devices() {
    skygate_live_db_query "
        SELECT r.tag || '|' || LOWER(p.device_hostname)
          FROM device_exit_node_prefs p
          JOIN node_owner_map r
            ON LOWER(r.hostname) = LOWER(p.device_hostname)
           AND COALESCE(r.tag, '') <> ''
          LEFT JOIN node_owner_map rn ON rn.tag = p.exit_node_tag
         WHERE p.via_enabled = 1
           AND COALESCE(p.exit_node_tag, '') <> ''
           AND p.set_by_user_id > 0
           AND NOT EXISTS (
                 SELECT 1 FROM exit_node_health h
                  WHERE LOWER(h.hostname) = LOWER(rn.hostname)
                    AND h.state NOT IN ('online','untagged'))
         ORDER BY r.tag" '|'
}

# skygate_pinned_catchall_tags — just the tags, one per line.
skygate_pinned_catchall_tags() {
    skygate_pinned_catchall_devices | while IFS='|' read -r tag _host; do
        [ -n "${tag:-}" ] && printf '%s\n' "$tag"
    done
}

# skygate_pin_expectation_for_tag <tag> — 1 when that device must carry the pin,
# 0 when it must not. Prints 0 (never nothing) so a caller can compare numbers.
skygate_pin_expectation_for_tag() {
    local want="$1" tags
    tags="$(skygate_pinned_catchall_tags)"
    case "
$tags
" in
        *"
$want
"*) printf '1' ;;
        *)   printf '0' ;;
    esac
}

# skygate_pinned_catchall_total — how many devices must carry the pin in total.
skygate_pinned_catchall_total() {
    local n
    n="$(skygate_pinned_catchall_devices | grep -c '.' || true)"
    printf '%s' "${n:-0}"
}
