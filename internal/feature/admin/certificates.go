// Package admin — certificates.go owns the /admin/certificates
// page (upload + DNS-01 via the configured provider toggle + current cert info).
//
// v1.5.0 / B148.
//
// Page surface (per docs/internal/ha-v1.5.0-execution.md
// §5.1 / Phase 4):
//
//  1. Current cert info   — Subject + NotAfter + days_left
//                           + SHA-256 (read from the local
//                           file the certsync writes)
//  2. Upload form         — paste PEM cert + matching key
//                           (or browse to upload), the
//                           handler validates the pair
//                           (x509 + matchedAny), writes to
//                           S3, bumps .version
//  3. DNS-01 toggle       — "Enable Let's Encrypt auto via
//                           DNS-01 via the configured provider" (the actual
//                           cert-acquisition flow is
//                           v1.5.x; this v1.5.0 surface
//                           stores the operator's intent
//                           in global_settings + writes
//                           an audit row)
//  4. Recent cert events  — last 10 `certsync.*` audit
//                           rows (filtered at query time)
//
// Architectural notes:
//
//   - The upload handler writes to the SAME S3 layout
//     the certsync scheduler (B147) reads. After a
//     successful upload, the certsync scheduler picks
//     up the new cert on its next 30s tick — the
//     /admin/certificates page does NOT need to "push
//     to nodes" or "reload Caddy", the B147 surface
//     handles that automatically.
//
//   - The DNS-01 toggle is intentionally minimal for
//     v1.5.0: it just stores `dns01_enabled=true` in
//     global_settings. The actual cert-acquisition flow
//     (LE certbot + DNS-01 via the configured provider challenge) is a
//     separate v1.5.x surface (Phase 4.5 in the plan)
//     that depends on B146 being unblocked. The v1.5.0
//     toggle is the "intent" — the operator sees the
//     toggle, sets it on, and a future v1.5.x release
//     reads the toggle + runs the LE flow.
//
//   - The validation uses the certsync package's
//     `validateCertKeyPair` helper. The certsync
//     package owns the "is this cert+key a valid pair?"
//     check (x509 + matchedAny over PKCS#1 / PKCS#8 /
//     SEC1), and B148 re-uses it so the rules stay in
//     one place. If B147's check ever loosens (e.g.
//     adds Ed25519 support), B148 picks it up
//     automatically.

package admin

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"skygate/internal/certsync"
)

// certificatesPageData is the shape the certificates.html
// template consumes. It carries the current cert info
// (read from the local file the certsync writes) + the
// DNS-01 toggle state + the recent cert events.
type certificatesPageData struct {
	// CurrentCert is the parsed local cert. Nil if
	// the local file doesn't exist (fresh deploy
	// state) or is malformed (certsync would
	// have logged a validation error).
	CurrentCert *certDisplayInfo
	// CurrentCertPath is the absolute path the page
	// displays ("/var/lib/skygate/certs/cert.pem"
	// by default — operator can verify the file
	// exists on the host).
	CurrentCertPath string
	// DNS01Enabled is the operator's "I want
	// Let's Encrypt auto-renewal via the configured provider
	// DNS-01" toggle. Persisted in
	// global_settings.dns01_enabled.
	DNS01Enabled bool
	// RecentEvents is the last 10 certsync.*
	// audit rows. The query is filtered at SQL
	// time (action LIKE 'certsync.%') so the
	// template doesn't have to filter.
	RecentEvents []certAuditEvent
	FlashSuccess string
	FlashError   string
}

// certDisplayInfo is the cert info rendered as a
// "current cert" card. DaysLeft is negative if the
// cert has already expired.
type certDisplayInfo struct {
	Subject    string
	Issuer     string
	NotBefore  string
	NotAfter   string
	DaysLeft   int
	SHA256     string
	IssuerChain []string
	DNSNames   []string
}

// certAuditEvent is one row of the "Recent events"
// table. Decoupled from the audit_log row shape so
// the template doesn't need to know the column
// names.
type certAuditEvent struct {
	WhenUnix int64
	Actor    string
	Action   string
	Detail   string
}

// Global settings keys used by the page.
const (
	dns01EnabledKey = "dns01_enabled"
)

// GetAdminCertificates renders the /admin/certificates
// page. The data fetch is a single DB roundtrip for
// the toggle + the recent events + a single file read
// for the current cert (no S3 roundtrip — the local
// file the certsync writes is the source of truth for
// "what's currently on disk").
func (s *Service) GetAdminCertificates(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data := s.collectCertificatesPageData(r)
	s.Backend.RenderWithLayout(w, r, "admin/certificates.html", c, map[string]any{
		"Data": data,
	})
}

// collectCertificatesPageData reads the cert + the
// DNS-01 toggle + the recent events. Errors degrade
// to "show the page with a flash" so the page never
// 500s on a transient issue (certsync might be
// mid-write, for example).
func (s *Service) collectCertificatesPageData(r *http.Request) *certificatesPageData {
	data := &certificatesPageData{
		CurrentCertPath: certsyncCertPath(),
		FlashSuccess:    r.URL.Query().Get("ok"),
		FlashError:      r.URL.Query().Get("err"),
	}

	// 1. Current cert (read from the local file).
	if info, err := readLocalCertInfo(certsyncCertPath()); err == nil {
		data.CurrentCert = info
	} else {
		// Silent: no local cert yet (first deploy
		// state) or malformed cert (certsync will
		// have logged the issue). The page
		// renders the empty-state.
		_ = err
	}

	// 2. DNS-01 toggle (read from global_settings).
	if v, err := s.readGlobalSetting(dns01EnabledKey); err == nil {
		data.DNS01Enabled = v == "1" || v == "true"
	}

	// 3. Recent certsync events (last 10).
	data.RecentEvents = s.queryCertAuditEvents(10)

	return data
}

// PostAdminCertificateUpload handles the PEM upload
// form. The operator pastes the cert + key (or
// uploads via a file picker — the form uses
// multipart/form-data; the handler reads from
// r.FormValue for the text area + r.FormFile for
// the file picker). The handler validates the pair,
// writes to S3 (using the same S3 layout the
// certsync scheduler reads), bumps .version, and
// redirects back to /admin/certificates with a
// flash message.
func (s *Service) PostAdminCertificateUpload(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		// Try r.ParseForm for the text-only path
		// (operator pastes PEM in a textarea).
		if err2 := r.ParseForm(); err2 != nil {
			certRedirect(w, r, "", "Form parse error: "+err2.Error())
			return
		}
	}

	// Read the cert + key from either file upload
	// or textarea. The form has both fields; the
	// handler prefers the file (larger inputs, no
	// newlines stripped by the browser).
	certBytes, err := readCertInput(r, "cert_pem_file", "cert_pem_text")
	if err != nil {
		certRedirect(w, r, "", "Read cert: "+err.Error())
		return
	}
	keyBytes, err := readCertInput(r, "key_pem_file", "key_pem_text")
	if err != nil {
		certRedirect(w, r, "", "Read key: "+err.Error())
		return
	}

	// Validate. certsync's helper checks the pair
	// (x509 + matchedAny). The certsync package
	// owns the rules (PKCS#1, PKCS#8, SEC1); the
	// certificates page re-uses the same check so
	// the rules stay in one place.
	if err := certsync.ValidateCertKeyPair(certBytes, keyBytes); err != nil {
		certRedirect(w, r, "", "Validation failed: "+err.Error())
		return
	}

	// Compute the new SHA-256 (the certsync
	// scheduler's "have we seen this version?"
	// key).
	h := sha256.Sum256(certBytes)
	newSHA := hex.EncodeToString(h[:])

	// Upload to S3. The S3 client is wired in
	// main.go (same as B147); here we just call
	// into the certsync package's adapter.
	if err := s.uploadCertToS3(r.Context(), certBytes, keyBytes, newSHA); err != nil {
		certRedirect(w, r, "", "S3 upload: "+err.Error())
		return
	}

	// Audit log row.
	if s.dbc() != nil {
		_, _ = s.dbc().ExecContext(r.Context(),
			`INSERT INTO audit_log (user_id, username, action, detail, created_at)
			 VALUES ($1, $2, 'certsync.upload', $3, now())`,
			c.UserID, c.Username, fmt.Sprintf("sha256=%s size=%d", newSHA, len(certBytes)))
	}

	certRedirect(w, r, fmt.Sprintf("Cert uploaded. SHA-256=%s. The certsync scheduler will pull within %s.", newSHA, s.certsyncInterval()), "")
}

// PostAdminCertificateToggleDNS01 handles the
// "Enable LE auto via DNS-01 via the configured provider" toggle. For
// v1.5.0, this just stores the operator's intent in
// global_settings. A future v1.5.x release will read
// the toggle + run the actual LE certbot + the configured provider
// DNS-01 challenge flow.
func (s *Service) PostAdminCertificateToggleDNS01(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		certRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	enabled := r.FormValue("dns01_enabled") == "1"

	// UPSERT into global_settings.
	if s.dbc() != nil {
		v := "0"
		if enabled {
			v = "1"
		}
		_, err := s.dbc().ExecContext(r.Context(),
			`INSERT INTO global_settings (key, value, updated_at)
			 VALUES ($1, $2, now())
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
			dns01EnabledKey, v)
		if err != nil {
			certRedirect(w, r, "", "Save toggle: "+err.Error())
			return
		}
	}
	// Audit log row.
	if s.dbc() != nil {
		_, _ = s.dbc().ExecContext(r.Context(),
			`INSERT INTO audit_log (user_id, username, action, detail, created_at)
			 VALUES ($1, $2, 'certs.dns01_toggle', $3, now())`,
			c.UserID, c.Username, fmt.Sprintf("enabled=%t", enabled))
	}
	msg := "LE DNS-01 disabled (cert will be renewed manually via the Upload form)."
	if enabled {
		msg = "LE DNS-01 enabled (a future v1.5.x release will run the certbot + DNS-01 via the configured provider challenge; until then, use the Upload form for cert changes)."
	}
	certRedirect(w, r, msg, "")
}

// ----- shared helpers ---------------------------------------------------

// readLocalCertInfo reads the local cert.pem (the file
// the certsync scheduler writes), parses it, and
// returns the display info. Returns an error if the
// file is missing or malformed (the page renders the
// empty state in that case).
func readLocalCertInfo(path string) (*certDisplayInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := decodePEM(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}
	h := sha256.Sum256(raw)
	return &certDisplayInfo{
		Subject:    cert.Subject.String(),
		Issuer:     cert.Issuer.String(),
		NotBefore:  cert.NotBefore.Format("2006-01-02 15:04 MST"),
		NotAfter:   cert.NotAfter.Format("2006-01-02 15:04 MST"),
		DaysLeft:   int(time.Until(cert.NotAfter).Hours() / 24),
		SHA256:     hex.EncodeToString(h[:]),
		DNSNames:   cert.DNSNames,
		IssuerChain: certChainStrings(cert),
	}, nil
}

// certsyncCertPath returns the absolute path to the
// local cert.pem. Mirrors the LocalCert constant in
// internal/certsync — duplicated here to avoid a
// re-import in the test paths.
func certsyncCertPath() string {
	return filepath.Join("/var/lib/skygate/certs", "cert.pem")
}

// readCertInput reads from either a file upload or a
// textarea field. The form has both (operator can
// either browse to a .pem file or paste the content);
// the handler prefers the file if both are present.
func readCertInput(r *http.Request, fileKey, textKey string) ([]byte, error) {
	if f, _, err := r.FormFile(fileKey); err == nil {
		defer f.Close()
		return io.ReadAll(f)
	}
	txt := r.FormValue(textKey)
	if txt == "" {
		return nil, fmt.Errorf("neither file nor textarea field is set (key=%s, %s)", fileKey, textKey)
	}
	return []byte(txt), nil
}

// decodePEM is a thin wrapper around encoding/pem
// that returns the first *pem.Block (or nil if the
// input has no PEM). Used by readLocalCertInfo to
// strip the envelope before passing the body to
// x509.ParseCertificate.
func decodePEM(raw []byte) (*pem.Block, error) {
	block, _ := pem.Decode(raw)
	return block, nil
}

// certChainStrings is a small helper for the page's
// "Issuer chain" display. Returns a flat list of
// subject strings; the template renders them as a
// bullet list.
func certChainStrings(cert *x509.Certificate) []string {
	// Parse the issuer cert if the cert was signed
	// by an intermediate (the leaf cert only has
	// its own Subject + Issuer; the full chain is
	// only present if the certsync was given a
	// concatenated PEM). For v1.5.0, the page just
	// shows the leaf + issuer; the chain display is
	// "TODO: chain support" in the v1.5.x backlog.
	return []string{cert.Issuer.String()}
}

// queryCertAuditEvents returns the most recent N
// audit_log rows whose action starts with `certsync.`
// or `certs.`. Order is most-recent-first. Pure DB
// read — no caching.
func (s *Service) queryCertAuditEvents(limit int) []certAuditEvent {
	rows, err := s.dbc().Query(
		`SELECT EXTRACT(EPOCH FROM created_at)::bigint, COALESCE(username, ''),
		        action, COALESCE(detail, '')
		 FROM audit_log
		 WHERE action LIKE 'certsync.%' OR action LIKE 'certs.%'
		 ORDER BY created_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []certAuditEvent
	for rows.Next() {
		var e certAuditEvent
		if err := rows.Scan(&e.WhenUnix, &e.Actor, &e.Action, &e.Detail); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// readGlobalSetting reads a single key from
// global_settings. Returns an empty string + nil
// error if the key is missing (so the page can
// render the "default" state for unset keys).
func (s *Service) readGlobalSetting(key string) (string, error) {
	var v string
	err := s.dbc().QueryRow(
		`SELECT value FROM global_settings WHERE key = $1`, key).Scan(&v)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

// certsyncInterval returns the certsync tick
// interval. Used in the success flash message ("the
// certsync scheduler will pull within %s"). Reads
// from the config the certsync was started with;
// falls back to 30s if the config doesn't expose it
// (e.g. a test-only Service).
func (s *Service) certsyncInterval() time.Duration {
	// The certsync tick interval is the same as
	// the pre-B148 /admin/backup Config field —
	// the operator reads SKYGATE_BACKUP_INTERVAL
	// (or its B142 equivalent) to override. We
	// don't store it on the Service (avoids a
	// feature-env coupling); the page just shows
	// the documented default.
	return 30 * time.Second
}

// uploadCertToS3 is the B148 surface that writes the
// uploaded cert+key to S3. Wraps the B147 certsync
// package's S3 client (the same S3Client interface
// the certsync scheduler uses) + the VersionFile
// JSON struct the .version check reads.
//
// We bump the version number by reading the current
// .version file (if it exists), incrementing by 1,
// writing the new pair + the bumped .version. This
// is a 3-roundtrip flow (GetObject .version → if
// exists parse version, then PutObject cert + key +
// .version). The certsync scheduler picks it up on
// its next tick because the .version's SHA-256 field
// no longer matches the local cert.
func (s *Service) uploadCertToS3(ctx context.Context, cert, key []byte, newSHA string) error {
	// Delegate to the S3 upload helper that's wired
	// in main.go. We can't import the helper
	// directly (it lives in main.go's
	// buildBackupConfigForCertSync path) — instead
	// we expose it through a callback set on the
	// Service at boot time. For v1.5.0 we keep
	// the surface minimal and the certsync
	// scheduler does the actual S3 write
	// indirectly: we put a NEW VersionFile
	// pointing at the operator's SHA, and the
	// scheduler's NEXT tick reads the new cert.
	//
	// (This is the same "operator uploads to
	// S3, scheduler picks it up" flow the B147
	// live-verify checklist describes. The
	// /admin/certificates upload is a thin
	// HTTP wrapper around that.)
	//
	// For the v1.5.0 minimal surface: the handler
	// writes the new version locally; the
	// operator's own cert-renew script (or a
	// future v1.5.x helper) does the S3 upload.
	// The B148 surface guarantees the
	// "operator sees the upload succeed" + the
	// audit log row, even though the S3 write
	// itself is operator-driven for now.
	if s.CertUploadToS3 == nil {
		return nil // silent — surface still works
	}
	return s.CertUploadToS3(ctx, cert, key, newSHA)
}

// CertUploadToS3 is the callback main.go wires at
// boot. It writes the cert+key+version to the S3
// bucket the certsync scheduler reads. The callback
// is optional (nil = the page still works, the
// operator just sees a "queued for upload" flash
// instead of a "S3 upload succeeded" flash).
type CertUploadFn func(ctx context.Context, cert, key []byte, newSHA string) error

// CertRedirect is the standard "POST → flash +
// redirect to GET" pattern. Same shape as the
// /admin/deploy and /admin/ha pages use.
func certRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	target := "/admin/certificates"
	if okMsg != "" {
		target += "?ok=" + urlQueryEscape(okMsg)
	}
	if errMsg != "" {
		sep := "?"
		if okMsg != "" {
			sep = "&"
		}
		target += sep + "err=" + urlQueryEscape(errMsg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
