#!/bin/bash
# Check that the new admin/user sidebar pages render.
# Curl from the VM host (the skygate container is alpine
# and has no curl).
set -u

PATH_LIST="/admin/update /admin/exit-nodes /admin/headscale /admin/headplane /admin/integrations /admin/control-planes /admin/invites /admin/meshes /my/keys"

echo "=== new sidebar pages (HTTP status, anonymous request) ==="
echo "  (300/302 = route exists, requires auth — that's expected for these)"
echo

for path in $PATH_LIST; do
    code=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:8080$path" 2>&1)
    echo "  $path -> $code"
done
