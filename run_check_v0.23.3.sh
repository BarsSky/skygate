#!/bin/bash
# Helper that scp's check_v0.23.3.sh to the VM and runs it.
# Mirrors run_check_cross_subnet_v0.23.1.sh / run_check_v0.23.0.sh
# pattern from prior releases.

set -euo pipefail
SCRIPT="check_v0.23.3.sh"
LOCAL_PATH="C:/Projects/skygate/${SCRIPT}"
VM_PATH="/tmp/${SCRIPT}"
VM="skyadmin@192.168.13.69"

# scp the script to the VM (this is the only step that needs
# quoting; the rest is a single ssh call).
echo "Copying ${LOCAL_PATH} to ${VM}:${VM_PATH} ..."
scp -q "${LOCAL_PATH}" "${VM}:${VM_PATH}"
echo "Running ${VM_PATH} on VM ..."
ssh "${VM}" "chmod +x ${VM_PATH} && bash ${VM_PATH}"
