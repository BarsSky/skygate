# Makefile — skygate build / run / test / deploy helpers
#
# Usage:
#   make build       — compile ./skygate binary (CGO sqlite, ~3-5 min first time)
#   make run         — build + run ./skygate locally (uses ./skygate.db, ./data/)
#   make smoke       — run scripts/smoke.sh against running skygate
#   make check-nodes — run scripts/check_exit_nodes.py
#   make audit-routes — run scripts/audit_routes.py (static: main.go vs handlers)
#   make test        — alias for go-test + audit-routes + smoke + check-nodes
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

GO       ?= go
GIT      ?= git
BINARY   ?= ./skygate
PKG      ?= ./cmd/skygate

.PHONY: build run smoke check-nodes audit-routes test clean deploy backup restart logs tailscale-update-telegram-routes help

help:
	@echo "Targets:"
	@echo "  build        - compile $(BINARY)"
	@echo "  run          - build + run locally"
	@echo "  smoke        - run scripts/smoke.sh (HTTP smoke against running skygate)"
	@echo "  check-nodes  - run scripts/check_exit_nodes.py (headscale API)"
	@echo "  audit-routes - run scripts/audit_routes.py (static: main.go vs handlers)"
	@echo "  test         - go-test + audit-routes + smoke + check-nodes"
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
	@if command -v go >/dev/null 2>&1; then 		go test ./... 2>&1; 	else 		echo "go not installed; skipping go test"; 	fi

# v0.24.2: keep the embed copies of setup.sh and
# README.md in internal/handlers/bundles/ in sync with
# the canonical sources in deploy/subnet-router/. The
# check is a fast `cmp` — fails the make target if the
# copies drift. Run `make sync-bundles` to refresh.
sync-bundles:
	cp deploy/subnet-router/setup.sh internal/handlers/bundles/setup.sh
	cp deploy/subnet-router/README.md internal/handlers/bundles/README.md
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
