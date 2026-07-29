// Package admin — backup.go owns the legacy /admin/backup
// page (create / restore / list / download) and the
// /admin/settings admin page (URL + API key + exit policy
// + .env editor).
//
// refactor-v0.30 Phase B step 3b.6 (2026-07-29): moved
// from internal/handlers/admin_backup.go. The /admin/backup
// config half (Destination & schedule card) lives in
// backup_config.go in the same package. The settings page
// was bundled into this file historically; it's moved
// here too because it shares the same admin surface
// (and the same ControlURL/JWTSecret/HeadscaleKey
// dependencies).

package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"skygate/internal/backup"
)

const backupDir = "/tmp/skygate-backup"

// GetAdminBackup serves /admin/backup. Admin-only.
//
// The page has two halves:
//
//   - Legacy: "Create / Restore / List / Download". Lives
//     in this file. Backups are produced by the
//     scripts/backup.sh script and stored under
//     backupDir (/tmp/skygate-backup).
//
//   - Config (Этап 14 v6): "Destination & schedule".
//     Lives in backup_config.go. Form values are
//     persisted via backup.Config and a "Run now"
//     button calls backup.RunBackup. The two halves
//     share the same admin/backup.html template; this
//     handler populates the legacy fields + delegates
//     the config card to backup.Load.
func (s *Service) GetAdminBackup(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	data := map[string]any{}

	// Flash messages
	if s := r.URL.Query().Get("success"); s != "" {
		data["FlashSuccess"] = s
	}
	if e := r.URL.Query().Get("error"); e != "" {
		data["FlashError"] = e
	}

	// 2026-07-14: Этап 14 v6 — load the persistent
	// backup config so the "Destination & schedule"
	// card renders on the same page (the new
	// /admin/backup/config handler is a thin wrapper
	// that does the same thing and exposes the same
	// template). We do this here so the legacy
	// /admin/backup URL keeps working — admins can
	// bookmark either.
	if cfg, err := backup.Load(s.DB); err == nil {
		data["Config"] = cfg
		data["Protocols"] = backup.AllProtocols
	}

	// List existing backups
	os.MkdirAll(backupDir, 0755)
	entries, _ := os.ReadDir(backupDir)
	var backups []map[string]string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		fi, _ := e.Info()
		if fi == nil {
			continue
		}
		be := map[string]string{
			"Name": e.Name(),
			"Size": formatSize(fi.Size()),
		}
		if f, err := os.Open(filepath.Join(backupDir, e.Name())); err == nil {
			h := sha256.New()
			io.Copy(h, f)
			f.Close()
			be["SHA256"] = hex.EncodeToString(h.Sum(nil))[:16] + "..."
		}
		backups = append(backups, be)
	}
	data["Backups"] = backups

	s.Backend.RenderWithLayout(w, r, "admin-backup", c, data)
}

// formatSize converts a byte count to a short human
// string ("1.5 MB", "300 KB", etc.). Used by the
// /admin/backup page to render the size column.
func formatSize(b int64) string {
	switch {
	case b > 1024*1024*1024:
		return strings.TrimRight(strconv.FormatFloat(float64(b)/1024/1024/1024, 'f', 1, 64), "0.") + " GB"
	case b > 1024*1024:
		return strings.TrimRight(strconv.FormatFloat(float64(b)/1024/1024, 'f', 1, 64), "0.") + " MB"
	case b > 1024:
		return strings.TrimRight(strconv.FormatFloat(float64(b)/1024, 'f', 1, 64), "0.") + " KB"
	default:
		return strings.TrimRight(strconv.FormatFloat(float64(b), 'f', 1, 64), "0.") + " B"
	}
}

// PostAdminBackupSave fires scripts/backup.sh and
// either downloads the produced archive (success) or
// redirects back with the captured error.
func (s *Service) PostAdminBackupSave(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	os.MkdirAll(backupDir, 0755)

	backupScript := ""
	for _, try := range []string{
		"/app/scripts/backup.sh",
		"/home/skyadmin/skygate/scripts/backup.sh",
	} {
		if _, err := os.Stat(try); err == nil {
			backupScript = try
			break
		}
	}
	if backupScript == "" {
		http.Redirect(w, r, "/admin/backup?error=backup.sh+not+found", http.StatusFound)
		return
	}

	cmd := exec.Command("bash", backupScript, backupDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Cap the error preview at 200 bytes so a giant
		// log doesn't blow the URL length. The legacy
		// redirect also had this cap (the slice used to
		// be `output[:200]` unconditionally — that
		// would panic on short outputs, so we use min).
		preview := string(output)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		http.Redirect(w, r, "/admin/backup?error=backup+failed:"+urlSafe(preview), http.StatusFound)
		return
	}

	// Find archive name
	lines := strings.Split(string(output), "\n")
	archiveName := ""
	for _, l := range lines {
		if strings.Contains(l, ".tar.gz") && strings.Contains(l, "skygate-full-") {
			parts := strings.Fields(l)
			for _, p := range parts {
				if strings.Contains(p, "skygate-full-") && strings.HasSuffix(p, ".tar.gz") {
					archiveName = p
				}
			}
		}
	}
	if archiveName == "" {
		// Try latest file
		entries, _ := os.ReadDir(backupDir)
		var latestName string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tar.gz") && e.Name() > latestName {
				latestName = e.Name()
			}
		}
		archiveName = latestName
	}

	if archiveName != "" {
		http.Redirect(w, r, "/admin/backup/download?name="+archiveName, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/backup?success=backup+created", http.StatusFound)
}

// GetAdminBackupDownload serves an existing archive by
// name. The name is sanitized to prevent path traversal
// (we reject any name with `..`, `/`, or `\`).
func (s *Service) GetAdminBackupDownload(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(backupDir, name)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, path)
}

// PostAdminBackupRestore accepts a multipart upload of
// an archive and runs scripts/restore.sh against it.
// Feeds "8\n" on stdin (the auto-confirm answer for
// restore.sh's interactive prompt).
func (s *Service) PostAdminBackupRestore(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	r.ParseMultipartForm(100 << 20)
	file, _, err := r.FormFile("archive")
	if err != nil {
		http.Redirect(w, r, "/admin/backup?error=upload+failed", http.StatusFound)
		return
	}
	defer file.Close()

	os.MkdirAll(backupDir, 0755)
	dest := filepath.Join(backupDir, "uploaded-restore.tar.gz")
	dst, err := os.Create(dest)
	if err != nil {
		http.Redirect(w, r, "/admin/backup?error=write+failed", http.StatusFound)
		return
	}
	io.Copy(dst, file)
	dst.Close()

	restoreScript := ""
	for _, try := range []string{
		"/app/scripts/restore.sh",
		"/home/skyadmin/skygate/scripts/restore.sh",
	} {
		if _, err := os.Stat(try); err == nil {
			restoreScript = try
			break
		}
	}
	if restoreScript == "" {
		http.Redirect(w, r, "/admin/backup?error=restore.sh+not+found", http.StatusFound)
		return
	}

	cmd := exec.Command("bash", restoreScript, dest, "/home/skyadmin/skygate")
	cmd.Stdin = strings.NewReader("8\n")
	cmd.CombinedOutput()

	http.Redirect(w, r, "/admin/backup?success=restore+complete!+Check+/admin/settings+to+update+URLs", http.StatusFound)
}

// GetAdminSettings serves /admin/settings. Admin-only.
// Renders the URL / API key / password / migration form.
// The form action is /admin/settings (POST).
//
// The page also reads the global exit_policy and renders
// it in a dropdown. The .env editor accepts changes but
// only some of them actually take effect (exit_policy
// is the only one we round-trip to the DB; the rest are
// a UX placeholder so the admin doesn't have to ssh in
// to update the file).
func (s *Service) GetAdminSettings(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	var exitPolicy string
	s.DB.QueryRow("SELECT value FROM global_settings WHERE key = 'exit_policy'").Scan(&exitPolicy)
	if exitPolicy == "" {
		exitPolicy = "allow_all"
	}

	data := map[string]any{
		"HeadscaleURL":    s.ControlURL,
		"ExitPolicy":      exitPolicy,
		"PublicDomain":    s.ControlURL,
		"JWTSecretMask":   maskSecret(s.JWTSecret),
		"HeadscaleAPIKey": maskSecret(s.HeadscaleKey),
	}
	if succ := r.URL.Query().Get("success"); succ != "" {
		data["FlashSuccess"] = succ
	}
	if e := r.URL.Query().Get("error"); e != "" {
		data["FlashError"] = e
	}

	s.Backend.RenderWithLayout(w, r, "admin-settings", c, data)
}

// PostAdminSettings persists the exit_policy into
// global_settings. The .env-touching code path is a
// no-op kept as a UX placeholder (the file is read +
// written back unchanged — operators who want to
// actually change settings should ssh + edit + docker
// restart).
func (s *Service) PostAdminSettings(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	r.ParseForm()
	if ep := r.FormValue("exit_policy"); ep == "allow_all" || ep == "deny_all" {
		s.DB.Exec("INSERT OR REPLACE INTO global_settings (key, value) VALUES ('exit_policy', ?)", ep)
	}
	_ = r.FormValue("headscale_url")
	_ = r.FormValue("headscale_api_key")
	_ = r.FormValue("public_domain")
	_ = r.FormValue("admin_password")

	// Read .env and update
	envPath := "/home/skyadmin/skygate/.env"
	if _, err := os.Stat(envPath); err != nil {
		envPath = "/app/.env"
	}

	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		http.Redirect(w, r, "/admin/settings?error=cannot+read+.env", http.StatusFound)
		return
	}

	if err := os.WriteFile(envPath, envBytes, 0644); err != nil {
		http.Redirect(w, r, "/admin/settings?error=write+failed", http.StatusFound)
		return
	}

	http.Redirect(w, r, "/admin/settings?success=Saved!+Restart+skygate:+docker+restart+skygate", http.StatusFound)
}

// maskSecret returns a redacted form of a secret
// ("••••••••abcd") so the admin can confirm a value
// is set without exposing it. Strings of 8 chars or
// fewer are fully masked.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "••••••••"
	}
	return "••••••••" + s[len(s)-4:]
}

// urlSafe escapes a string for use in a redirect
// query parameter. Spaces become '+' (matching the
// application/x-www-form-urlencoded form). The legacy
// /admin/backup?error=… messages use this encoding
// because the template reads r.URL.Query().Get("error")
// which expects URL-encoded values.
func urlSafe(s string) string {
	return strings.ReplaceAll(s, " ", "+")
}
