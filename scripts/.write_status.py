#!/usr/bin/env python3
"""Helper for backup.sh: write JSON status file.

Reads required fields from environment variables so bash can hand them off
without any quoting issues. Always emits valid JSON.
"""
import datetime
import json
import os
import sys

OUT = os.environ["BACKUP_OUT"]
os.makedirs(os.path.dirname(OUT), exist_ok=True)

payload = {
    "status":       os.environ["BACKUP_STATUS"],
    "timestamp":    datetime.datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%SZ"),
    "host":         os.environ["BACKUP_HOST"],
    "backup_dir":   os.environ["BACKUP_BDIR"],
    "backup_path":  os.environ["BACKUP_BPATH"],
    "archive":      os.environ["BACKUP_BFILE"],
    "archive_size": int(os.environ["BACKUP_BSIZE"]) if os.environ["BACKUP_BSIZE"].isdigit() else 0,
    "sha256":       os.environ["BACKUP_SHA"],
    "integrity":    os.environ["BACKUP_INT"],
    "error":        os.environ["BACKUP_ERR"],
}

with open(OUT, "w") as f:
    json.dump(payload, f, indent=2, sort_keys=True)
    f.write("\n")

sys.exit(0)
