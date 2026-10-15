#!/usr/bin/env bash
# scripts/lib/post_deploy_truth.sh — B387: the verdict helpers that keep
# scripts/verify_post_deploy.sh from turning a PLUMBING failure into a PRODUCT verdict.
#
# WHY THIS FILE EXISTS (measured 2026-10-15, right after the v1.5.127 update)
# -------------------------------------------------------------------------
# On the operator's docker-compose host the post-deploy catalog printed:
#
#   FAIL  R30 skygate.db integrity_check FAILED: psql:error:couldnottranslatehostname
#         "skygate-pg-local"toaddress:Temporaryfailureinnameresolution
#         This is the corruption that triggered the R9 SKIP on 2026-07-30.
#         Run scripts/recover_db_corruption.sh (the .recover-based recovery)
#         to extract the data and rebuild a clean DB.
#
# ...about a database that was demonstrably healthy: pg_isready answered
# "accepting connections", acl_snapshots held 2071 rows, the panel served a 611 KB
# /admin/exit-rules, /readyz said healthy:true — and the SAME R30 query answered
# "ok" when it was run from a container on the docker network.
#
# The mechanism was two lines:
#
#   INTEGRITY=$(psql_vm "SELECT ..." 2>/dev/null | tr -d '[:space:]')
#   INTEGRITY="${INTEGRITY:-fail}"
#
# psql_vm prefers the host's psql when it exists, and hands it the DSN hostname
# verbatim. On a docker-compose install that hostname is a COMPOSE SERVICE NAME
# ("skygate-pg-local") that only resolves inside the docker network, so the host's
# resolver fails, psql prints an error, the error text (or emptiness) flows into
# INTEGRITY, and every non-"ok" value meant "your database is corrupt". Every
# DB-dependent contract in the catalog (R9, R10, R19, R30, R-B351) inherited it.
#
# That is two of this project's own rules broken at once:
#   * AGENTS rule 1 — a check whose live dependency is unavailable must SKIP,
#     never FAIL;
#   * AGENTS trap #15 — a host transient must never masquerade as a product
#     regression.
#
# The fix has two halves, and BOTH are needed:
#   1. verify_post_deploy.sh's psql_vm now prefers the container path whenever the
#      checker's host cannot resolve the DSN host, so the checker gets a real answer;
#   2. the callers classify the answer here — "the database did not answer" is a
#      SKIP naming the reason, and only a genuine answer that is not "ok" may reach
#      the corruption verdict.
#
# These helpers are deliberately PURE (no ssh, no docker, no network) so
# scripts/check_b387_post_deploy_truth.sh can execute them against planted inputs
# instead of asserting their text.

# vpd_is_transport_failure <text> — 0 when <text> is a CONNECTION/plumbing failure
# rather than an answer produced by the database. Matched with the `case` builtin:
# no fork, no pipe, so nothing the host does to a pipeline can change the verdict
# (AGENTS trap #9 and trap #15).
vpd_is_transport_failure() {
  case "$1" in
    "") return 0 ;;
    *"could not translate host name"*) return 0 ;;
    *"Temporary failure in name resolution"*) return 0 ;;
    *"Name or service not known"*) return 0 ;;
    *"could not connect to server"*) return 0 ;;
    *"Connection refused"*) return 0 ;;
    *"No route to host"*) return 0 ;;
    *"Network is unreachable"*) return 0 ;;
    *"connection timed out"*) return 0 ;;
    *"timeout expired"*) return 0 ;;
    *"password authentication failed"*) return 0 ;;
    *"too many clients"*) return 0 ;;
    *"psql: error"*) return 0 ;;
    *"psql: command not found"*) return 0 ;;
    *"SKYGATE_DB_DSN not set"*) return 0 ;;
    *"docker: command not found"*) return 0 ;;
    *"Cannot connect to the Docker daemon"*) return 0 ;;
    *"Unable to find image"*) return 0 ;;
    *"no matching manifest"*) return 0 ;;
    *"Permission denied"*) return 0 ;;
    *"Host key verification failed"*) return 0 ;;
  esac
  return 1
}

# vpd_query_reached_db <rc> <answer> — 0 only when the DATABASE really answered.
# A non-zero status, an empty answer, or a transport failure all mean "no answer",
# and the caller must report SKIP instead of inventing a verdict about the product.
vpd_query_reached_db() {
  local rc="${1:-1}" ans="${2:-}"
  [ "$rc" -eq 0 ] 2>/dev/null || return 1
  [ -n "$ans" ] || return 1
  vpd_is_transport_failure "$ans" && return 1
  return 0
}

# vpd_no_answer_reason <rc> <answer> — one line an operator can act on, naming
# whether the read was empty (a fact about the CHECK) or carried a reason.
# The raw first line of the answer is always included when there is one: the
# translation says WHY it is not a database verdict, the raw text lets the operator
# confirm the diagnosis and search for it.
vpd_no_answer_reason() {
  local rc="${1:-1}" ans="${2:-}" raw=""
  if [ -n "$ans" ]; then
    raw="$(printf '%s' "$ans" | head -1 | cut -c1-200)"
    case "$ans" in
      *"could not translate host name"*)
        printf '%s' "the checker's host cannot resolve the DSN hostname — it is a container name, reachable only inside the docker network (this is NOT a database verdict); raw: $raw"; return 0 ;;
      *"psql: command not found"*|*"SKYGATE_DB_DSN not set"*)
        printf '%s' "psql is unavailable at the checked path with this DSN; raw: $raw"; return 0 ;;
    esac
    printf 'the read returned: %s' "$raw"
    return 0
  fi
  printf 'the read produced EMPTY output (rc=%s) — a fact about the checker, not about the database' "$rc"
  return 0
}

# vpd_ok_answer <answer> — 0 when the answer is the expected literal "ok".
vpd_ok_answer() {
  case "$1" in
    ok|OK) return 0 ;;
  esac
  return 1
}

# vpd_is_tailnet_ipv4 <addr> — 0 for an address inside 100.64.0.0/10, the CGNAT
# range Tailscale allocates from. Replaces the hardcoded reference-deployment
# address (100.64.100.10) that made R5/R6 assert somebody else's topology.
vpd_is_tailnet_ipv4() {
  local a="${1:-}" o1 o2
  case "$a" in
    [0-9]*.[0-9]*.[0-9]*.[0-9]*) ;;
    *) return 1 ;;
  esac
  o1="${a%%.*}"
  o2="${a#*.}"; o2="${o2%%.*}"
  [ "$o1" = "100" ] || return 1
  [ "$o2" -ge 64 ] 2>/dev/null && [ "$o2" -le 127 ] 2>/dev/null || return 1
  return 0
}

# vpd_pick_self_ipv4 — read multi-line `tailscale ip -4` output and print the first
# tailnet IPv4. Pure, so the check can feed it planted output.
vpd_pick_self_ipv4() {
  local line
  while IFS= read -r line; do
    line="${line%%[[:space:]]*}"
    if vpd_is_tailnet_ipv4 "$line"; then
      printf '%s' "$line"
      return 0
    fi
  done
  return 1
}

# vpd_headscale_health_url <configured-base-url> — the URL the skygate container
# should be able to reach, derived from the address the APP is configured with
# rather than a hardcoded bridge IP. Falls back to the compose service name.
vpd_headscale_health_url() {
  local base="${1:-}"
  base="${base%"${base##*[![:space:]]}"}"
  if [ -z "$base" ]; then
    printf '%s' "http://headscale:50444/health"
    return 0
  fi
  base="${base%/}"
  case "$base" in
    */health) printf '%s' "$base" ;;
    *) printf '%s/health' "$base" ;;
  esac
}
