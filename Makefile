# Makefile — skygate build / run / test / deploy helpers
#
# Usage:
#   make build       — compile ./skygate binary (CGO sqlite, ~3-5 min first time)
#   make run         — build + run ./skygate locally (uses ./skygate.db, ./data/)
#   make smoke       — run scripts/smoke.sh against running skygate
#   make check-nodes — run scripts/check_exit_nodes.py
#   make audit-routes — run scripts/audit_routes.py (static: main.go vs handlers)
#   make test        — alias for go-test + audit-routes + smoke + check-nodes
#   make verify-pre  — scripts/verify_pre_deploy.sh (build-time guarantees B1-B10)
#   make verify-post — scripts/verify_post_deploy.sh (runtime guarantees R1-R25, on VM)
#   make verify      — verify-pre + verify-post
#   make rebuild-deploy     — git pull + docker compose build + recreate + wait /healthz on the VM
#   make reconcile-snapshots — write an acl_snapshots row matching the live headscale policy
#                             (one-off reconciliation; fixes R9 after a direct headscale edit)
#   make clean       — remove built binary
#   make deploy      — run deploy/deploy.sh
#   make backup      — run deploy/backup.sh
#   make restart     — docker compose restart skygate (in-place reload)
#   make logs        — tail skygate container logs
#   make tailscale-update-telegram-routes \
#                    — SSH to the relay (RELAY=emilia) and re-derive
#                      its advertised Telegram IP ranges from DNS.
#                      See docs/telegram-relay.md for the manual
#                      headscale approve-routes step that follows.
#
# All targets are no-ops if their dependencies are missing (deploy/
# scripts/ may be empty in some checkouts).
#
# 2026-07-25: v0.28.5 — added verify-pre / verify-post / verify. The
# full guarantee catalog is in AGENTS.md "v0.28.5 guarantee
# catalog" (B1-B10 build-time, R1-R25 runtime). Run verify-pre
# before `git push`; run verify-post after `docker compose up -d
# skygate` on the VM. The catalog was introduced after the
# v0.28.5 incident where three independent bugs (migration
# re-backfill, tagged-device ACL gap, stale Tailscale exit-node
# state) each passed the old `make test` and `make smoke` checks
# but together took the system down for ~6 hours.

GO       ?= go
GIT      ?= git
BINARY   ?= ./skygate
PKG      ?= ./cmd/skygate
# 2026-07-25: v0.28.5 — force bash as the recipe shell so that
# `[ -x ... ]` and `[[ ... ]]` work on Windows. Without this, make
# on Windows uses cmd.exe (via Git for Windows' shim) and the
# bash-isms in the verify-* recipes fail with "was unexpected at
# this time".
SHELL    ?= bash

.PHONY: build run smoke check-nodes audit-routes test clean deploy backup restart logs tailscale-update-telegram-routes verify-pre verify-post verify rebuild-deploy reconcile-snapshots help

help:
	@echo "Targets:"
	@echo "  build        - compile $(BINARY)"
	@echo "  run          - build + run locally"
	@echo "  smoke        - run scripts/smoke.sh (HTTP smoke against running skygate)"
	@echo "  check-nodes  - run scripts/check_exit_nodes.py (headscale API)"
	@echo "  audit-routes - run scripts/audit_routes.py (static: main.go vs handlers)"
	@echo "  test         - go-test + audit-routes + smoke + check-nodes"
	@echo "  verify-pre   - scripts/verify_pre_deploy.sh (build-time guarantees B1-B10)"
	@echo "  verify-post  - scripts/verify_post_deploy.sh (runtime guarantees R1-R25)"
	@echo "  verify       - verify-pre + verify-post"
	@echo "  rebuild-deploy    - scripts/rebuild_deploy.sh (git pull + build + recreate + wait healthz on VM)"
	@echo "  reconcile-snapshots - scripts/reconcile_snapshots.sh (one-off R9 fix after direct headscale edit)"
	@echo "  restart      - docker compose restart skygate"
	@echo "  logs         - tail skygate container logs"
	@echo "  clean        - remove built binary"

build:
	GOTOOLCHAIN=local $(GO) build -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

smoke:
	@if [ -x scripts/smoke.sh ]; then \
		bash scripts/smoke.sh; \
	else \
		echo "scripts/smoke.sh not found"; \
		exit 1; \
	fi

check-nodes:
	@if [ -x scripts/check_exit_nodes.py ]; then \
		. ./.env 2>/dev/null && export HEADSCALE_API_KEY && export HEADSCALE_URL=http://localhost:50444 && \
		python3 scripts/check_exit_nodes.py; \
	else \
		echo "scripts/check_exit_nodes.py not found"; \
		exit 1; \
	fi

# 2026-07-15: v0.13.0 — strict variant. Default check-nodes
# is warn-only (offline exit-nodes produce a WARN line and
# exit 0); check-nodes-strict hard-fails so CI / automated
# deploys can enforce "no deploy with an offline exit-node".
check-nodes-strict:
	@if [ -x scripts/check_exit_nodes.py ]; then \
		. ./.env 2>/dev/null && export HEADSCALE_API_KEY && export HEADSCALE_URL=http://localhost:50444 && \
		python3 scripts/check_exit_nodes.py --strict; \
	else \
		echo "scripts/check_exit_nodes.py not found"; \
		exit 1; \
	fi

# 2026-07-15: v0.15.0 — HTTPS health check. Verifies
# SKYGATE_CONTROL_URL is reachable over HTTPS with a
# valid cert (SAN matches), HTTP→HTTPS redirect works
# on port 80, and HSTS is sent on /login. Default is
# warn-only (matching check-nodes); check-https-strict
# is the CI variant.
check-https:
	@if [ -x scripts/check_https.py ]; then \
		. ./.env 2>/dev/null && export SKYGATE_CONTROL_URL && \
		python3 scripts/check_https.py; \
	else \
		echo "scripts/check_https.py not found"; \
		exit 1; \
	fi

check-https-strict:
	@if [ -x scripts/check_https.py ]; then \
		. ./.env 2>/dev/null && export SKYGATE_CONTROL_URL && \
		python3 scripts/check_https.py --strict; \
	else \
		echo "scripts/check_https.py not found"; \
		exit 1; \
	fi

audit-routes:
	@if [ -f scripts/audit_routes.py ]; then \
		python3 scripts/audit_routes.py; \
	else \
		echo "scripts/audit_routes.py not found"; \
		exit 1; \
	fi

test: go-test audit-routes smoke check-nodes check-https check-bundles

go-test:
	@if command -v go >/dev/null 2>&1; then 		go test -count=1 ./... 2>&1; 	else 		echo "go not installed; skipping go test"; 	fi

# 2026-07-25: v0.28.5 — pre-deploy guarantee verification. Runs
# before `git push` / `docker build` / `docker compose up -d`. The
# catalog is in AGENTS.md ("v0.28.5 guarantee catalog"). Every
# guarantee is a fast check (~3-5 min total) that can run on
# Windows (Git Bash) or Linux. Hard-fails so the deploy
# pipeline aborts.
verify-pre:
	@bash scripts/verify_pre_deploy.sh

# 2026-07-25: v0.28.5 — post-deploy guarantee verification. SSHes
# into the VM (SSH_HOST env var, default skyadmin@192.168.13.69)
# and runs the 25-check catalog. Should be invoked immediately
# after `docker compose up -d skygate` (or after `make
# restart`). Exits non-zero if any guarantee fails.
verify-post:
	@bash scripts/verify_post_deploy.sh

# 2026-07-25: v0.28.5 — both pre and post in one. Pre runs
# locally; post SSHes into the VM. If pre fails, post still
# runs (so the operator can see what state the VM is in) but
# the overall target fails.
verify: verify-pre verify-post

# 2026-07-30 — rebuild skygate container on the production VM.
# Wraps the 6-step canonical procedure in scripts/rebuild_deploy.sh:
#   1. chown data/ts/ (fix root-owned tailscale dirs)
#   2. git pull --ff-only
#   3. docker compose build skygate (3-5 min)
#   4. docker compose up -d --force-recreate --no-deps skygate
#   5. wait for /healthz (up to 5 min)
#   6. print new build label
#
# Runs on the operator workstation (not the VM). SSHes to
# SSH_HOST (default skyadmin@192.168.13.69) with the
# auto-detected key from ~/.ssh/id_ed25519 (or override with
# SSH_KEY). Replaces the manual 6-step procedure that was
# previously 4 separate bash invocations.
#
# After this runs, the operator should follow up with
# 'make verify-post' to confirm all 27 R-checks pass.
rebuild-deploy:
	@if [ -x scripts/rebuild_deploy.sh ]; then \
		bash scripts/rebuild_deploy.sh; \
	else \
		echo "scripts/rebuild_deploy.sh not found or not executable"; \
		exit 1; \
	fi

# 2026-07-30 — write an acl_snapshots row matching the live
# headscale policy's updatedAt. One-off R9 fix used when
# the live policy was edited outside skygate's normal flow
# (operator manual headscale PUT, crash mid-reapply, etc.).
#
# Normal flow: /my/exit-rules write triggers SetPolicy + writes
# the snapshot in one transaction. R9 stays aligned. This
# target is for the abnormal case where the two have diverged.
#
# After this runs, 'make verify-post' will pass R9.
reconcile-snapshots:
	@if [ -x scripts/reconcile_snapshots.sh ]; then \
		bash scripts/reconcile_snapshots.sh; \
	else \
		echo "scripts/reconcile_snapshots.sh not found or not executable"; \
		exit 1; \
	fi

# v0.24.2: keep the embed copies of setup.sh and
# README.md in internal/handlers/bundles/ in sync with
# the canonical sources in deploy/subnet-router/. The
# check is a fast `cmp` — fails the make target if the
# copies drift. Run `make sync-bundles` to refresh.
sync-bundles:
	cp -p deploy/subnet-router/setup.sh internal/handlers/bundles/setup.sh
	cp -p deploy/subnet-router/README.md internal/handlers/bundles/README.md
	@echo "synced."

check-bundles:
	@git diff --no-index --quiet deploy/subnet-router/setup.sh internal/handlers/bundles/setup.sh || \
		(echo "FAIL: internal/handlers/bundles/setup.sh is out of sync with deploy/subnet-router/setup.sh — run 'make sync-bundles'" && exit 1)
	@git diff --no-index --quiet deploy/subnet-router/README.md internal/handlers/bundles/README.md || \
		(echo "FAIL: internal/handlers/bundles/README.md is out of sync with deploy/subnet-router/README.md — run 'make sync-bundles'" && exit 1)
	@echo "bundles in sync."

clean:
	rm -f $(BINARY)

restart:
	docker compose restart skygate

logs:
	docker logs --tail 100 -f skygate

# 2026-07-14: Этап 14 v2 — refresh the relay's Telegram IP routes.
# REQUIRES: ssh access to the relay host with sudo, and the
# update-routes.sh script present there (deployed via deploy.sh or
# copied manually). See docs/telegram-relay.md.
#
# After this runs, the operator must still execute the
# `headscale nodes approve-routes` command printed at the end of
# update-routes.sh. This Makefile target does NOT automate the
# headscale admin step — that requires the headscale API key
# and lives in deploy/, not here.
tailscale-update-telegram-routes:
	@if [ -z "$(RELAY)" ]; then \
		echo "RELAY=<hostname> required, e.g. make tailscale-update-telegram-routes RELAY=emilia"; \
		exit 1; \
	fi
	@if [ ! -x deploy/tailscale-relay/update-routes.sh ]; then \
		echo "deploy/tailscale-relay/update-routes.sh not found or not executable"; \
		exit 1; \
	fi
	ssh -t $(RELAY) "sudo /opt/skygate/deploy/tailscale-relay/update-routes.sh"
