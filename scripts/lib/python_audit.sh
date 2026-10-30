#!/usr/bin/env bash
# scripts/lib/python_audit.sh — WHO RUNS THE PYTHON AUDITS (B446).
#
# Live case (2026-10-30, the reference host): the gate went RED on a checkout that was fine —
#
#	FAIL E1: the markup is imbalanced after the restructure:
#	         scripts/check_b438_exit_rules_table_and_filter.sh: line 154: python: command not found
#	FAIL E5: markup became imbalanced — … line 156: python: command not found
#
# `python3` is installed there, `python` is not. Two contracts called the bare name, so a MISSING
# TOOL was reported as a PRODUCT REGRESSION — the class AGENTS rule 1 forbids ("a check that needs
# live state it does not have must SKIP, never FAIL") one level down: the interpreter is part of
# the environment, not part of the tree under test.
#
# Usage:  . scripts/lib/python_audit.sh
#         PY="$(skygate_python)"
#         if [ -z "$PY" ]; then skip "…no python interpreter…"
#         elif "$PY" scripts/audit_html_balance.py; then ok "…"; else bad "…"; fi
skygate_python() {
	command -v python3 2>/dev/null || command -v python 2>/dev/null || true
}
