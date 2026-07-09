# Makefile — skygate build / run / test / deploy helpers
#
# Usage:
#   make build      — compile ./skygate binary (CGO sqlite, ~3-5 min first time)
#   make run        — build + run ./skygate locally (uses ./skygate.db, ./data/)
#   make smoke      — run scripts/smoke.sh against running skygate
#   make check-nodes — run scripts/check_exit_nodes.py
#   make test       — alias for smoke + check-nodes
#   make clean      — remove built binary
#   make deploy     — run deploy/deploy.sh
#   make backup     — run deploy/backup.sh
#   make restart    — docker compose restart skygate (in-place reload)
#   make logs       — tail skygate container logs
#
# All targets are no-ops if their dependencies are missing (deploy/
# scripts/ may be empty in some checkouts).

GO       ?= go
GIT      ?= git
BINARY   ?= ./skygate
PKG      ?= ./cmd/skygate

.PHONY: build run smoke check-nodes test clean deploy backup restart logs help

help:
	@echo "Targets:"
	@echo "  build       - compile $(BINARY)"
	@echo "  run         - build + run locally"
	@echo "  smoke       - run scripts/smoke.sh (HTTP smoke against running skygate)"
	@echo "  check-nodes - run scripts/check_exit_nodes.py (headscale API)"
	@echo "  test        - smoke + check-nodes"
	@echo "  restart     - docker compose restart skygate"
	@echo "  logs        - tail skygate container logs"
	@echo "  clean       - remove built binary"

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

test: smoke check-nodes

clean:
	rm -f $(BINARY)

restart:
	docker compose restart skygate

logs:
	docker logs --tail 100 -f skygate
