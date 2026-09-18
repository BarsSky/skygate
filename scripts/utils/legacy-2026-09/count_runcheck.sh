#!/usr/bin/env bash
set +e
# Count B-check rows in verify_pre_deploy.sh
grep -c '^run_check' scripts/verify_pre_deploy.sh
