// Package admin — user_subnet_download.go owns
// GET /admin/users/{id}/subnet/download.
//
// refactor-v0.30 Phase B step 3b.5 (2026-07-29):
// moved from internal/handlers/admin_user_subnet_download.go.
// The handler is a method on *Service now.
//
// refactor-v0.30 Phase D1 (2026-07-29): the local
// sanitizeFilename helper was removed — it now lives in
// internal/httputil/SanitizeFilename and is shared with
// feature/my/audit.go and the (now re-export) handlers.SanitizeFilename.

package admin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"time"

	"skygate/internal/handlers/bundles"
	"skygate/internal/httputil"
)

// GetAdminUserSubnetDownload returns a tar.gz bundle
// containing the per-user subnet-router setup. Issues a
// fresh preauth key on each call (same shape as
// PostAdminUserSubnetProvision) and embeds it in
// commands.txt so the user can scp the bundle to their
// router host and run `sudo bash commands.txt`.
func (s *Service) GetAdminUserSubnetDownload(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/download")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	if s.Sidecar == nil {
		http.Error(w, "sidecar manager not configured (check SKYGATE_SIDECAR_SYNC_PERIOD env)", 500)
		return
	}

	// Look up the username and the user's CIDR.
	var (
		username string
		cidr     string
	)
	if err := s.DB.QueryRow(
		`SELECT username, COALESCE(subnet_cidr, '') FROM portal_users WHERE id = ?`, id,
	).Scan(&username, &cidr); err != nil {
		http.Error(w, fmt.Sprintf("user not found: %v", err), 404)
		return
	}
	if cidr == "" {
		http.Error(w, "user has no subnet allocated — click Allocate on /admin/users/{id}/subnet first", 400)
		return
	}

	// Issue a fresh preauth key. Same TTL / shape as
	// PostAdminUserSubnetProvision.
	key, exp, err := s.Sidecar.GeneratePreauth(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Sprintf("issue preauth: %v", err), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "subnet_download",
		fmt.Sprintf("user_id=%d expires=%s bundle=tar.gz", id, exp.Format(time.RFC3339)))

	// Build the commands.txt content. We use the same
	// template as the admin UI's "Issue preauth key"
	// page, so the bundle is interchangeable with the
	// page-rendered command.
	commandsTxt := s.renderBundleCommandsTxt(username, cidr, key, exp)

	// Build the tar.gz in memory. Each file is a
	// tar.Header + body. Order matters only for the
	// user's "ls -l" sanity check — the setup.sh should
	// come first.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	addFile := func(name, body string, mode int64) {
		_ = tw.WriteHeader(&tar.Header{
			Name:    name,
			Mode:    mode,
			Size:    int64(len(body)),
			ModTime: time.Now(),
		})
		_, _ = tw.Write([]byte(body))
	}
	addFile("setup.sh", bundles.SetupScript, 0755)
	addFile("README.md", bundles.BundleReadme, 0644)
	addFile("commands.txt", commandsTxt, 0755)
	addFile("CIDR.txt", cidr+"\n", 0644)
	_ = tw.Close()
	_ = gz.Close()

	// Content-Disposition: the browser saves the file
	// as `skygate-subnet-router-bundle-<username>-<ts>.tar.gz`.
	// The username is sanitized to ASCII alphanumerics
	// + dashes (no path traversal if some operator
	// picks a weird name).
	safeUser := httputil.SanitizeFilename(username)
	filename := fmt.Sprintf("skygate-subnet-router-bundle-%s-%s.tar.gz",
		safeUser, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))

	// (Phase D1: the local sanitizeFilename helper was
	// removed — it now lives in internal/httputil/.)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// renderBundleCommandsTxt builds the commands.txt content
// (a runnable bash script) for the bundle. The same
// template is used by the admin UI's "Issue preauth key"
// page (see BuildPreauthInfo) so the bundle is a
// drop-in replacement for the page-rendered command.
//
// # Why bash and not just tailscale up directly
// We wrap the command in a bash script so the user can
// `bash commands.txt` and have it work — `sudo` is
// inside, so they don't need to remember to run as root.
// IP-forwarding setup is intentionally NOT in the
// script: that's a one-time sysctl that belongs in the
// host's own provisioning (Ansible, cloud-init, etc.),
// not in the per-user bundle.
func (s *Service) renderBundleCommandsTxt(username, cidr, preauth string, exp time.Time) string {
	// hostname must match what the sidecar expects:
	// "skygate-subnet-<username>".
	hostname := "skygate-subnet-" + username
	return fmt.Sprintf(`#!/bin/bash
# Skygate subnet-router setup for %s
# Generated: %s
# CIDR:      %s
# Preauth:   tskey-auth-... (1h TTL, single-use)
# Expires:   %s
#
# To use this bundle:
#   1. scp this file to your router host
#   2. ssh to the router host
#   3. Run: sudo bash commands.txt
#   4. Within ~30s, skygate's auto-approver will pick
#      up the new tag:subnet-router node and flip the
#      status to 'router_active'.
#
# This command is identical to the one the admin sees
# on /admin/users/{id}/subnet. Either source is fine.
sudo tailscale up \
  --accept-routes \
  --netfilter-mode=off \
  --login-server=https://head.example.com \
  --hostname=%s \
  --advertise-routes=%s \
  --authkey=%s
`,
		username,
		time.Now().UTC().Format(time.RFC3339),
		cidr,
		exp.UTC().Format(time.RFC3339),
		hostname,
		cidr,
		preauth,
	)
}
