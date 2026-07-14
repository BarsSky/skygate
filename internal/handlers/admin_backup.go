package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"strconv"

	"skygate/internal/backup"
)

const backupDir = "/tmp/skygate-backup"

func (a *App) GetAdminBackup(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
	if cfg, err := backup.Load(a.DB); err == nil {
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

	a.renderWithLayout(w, r, "admin-backup", c, data)
}

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

func (a *App) PostAdminBackupSave(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
		http.Redirect(w, r, "/admin/backup?error=backup+failed:"+urlSafe(string(output[:200])), http.StatusFound)
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

func (a *App) GetAdminBackupDownload(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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

func (a *App) PostAdminBackupRestore(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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

func (a *App) GetAdminSettings(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

		var exitPolicy string
	a.DB.QueryRow("SELECT value FROM global_settings WHERE key = 'exit_policy'").Scan(&exitPolicy)
	if exitPolicy == "" { exitPolicy = "allow_all" }

	data := map[string]any{
		"HeadscaleURL":   a.ControlURL,
		"ExitPolicy":     exitPolicy,
		"PublicDomain":   a.ControlURL,
		"JWTSecretMask":  maskSecret(a.JWTSecret),
		"HeadscaleAPIKey": maskSecret(a.HeadscaleKey),
	}
	if s := r.URL.Query().Get("success"); s != "" {
		data["FlashSuccess"] = s
	}
	if e := r.URL.Query().Get("error"); e != "" {
		data["FlashError"] = e
	}

	a.renderWithLayout(w, r, "admin-settings", c, data)
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "••••••••"
	}
	return "••••••••" + s[len(s)-4:]
}

func (a *App) PostAdminSettings(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
		r.ParseForm()
	if ep := r.FormValue("exit_policy"); ep == "allow_all" || ep == "deny_all" {
		a.DB.Exec("INSERT OR REPLACE INTO global_settings (key, value) VALUES ('exit_policy', ?)", ep)
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

func urlSafe(s string) string {
	return strings.ReplaceAll(s, " ", "+")
}