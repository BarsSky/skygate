// B304 (v1.5.69) — OIDC must be configurable entirely from the panel.
//
// Live report from the native host aro: the page still carried a warning that the
// configuration «требуется изменение в env и из вебинтерфейса это никак не
// изменить». Two real causes, both pinned here:
//
//  1. key_dir was saved and applied to NOTHING (the live applier only pushed the
//     four protocol fields), so a new directory took effect — if ever — on the
//     next restart. It is applied live now, and the page renders what the running
//     store actually is: resolved path, owner, mode, writability, active kid, and
//     a copy-paste fix for each failure mode.
//  2. /admin/oidc/sync read the RAW env and refused to run without it, even when
//     the panel held the configuration. It uses the effective settings now.
package admin

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"skygate/internal/config"
	"skygate/internal/db"
)

// loc0 decodes the ?ok= flash out of a redirect Location.
func loc0(rec interface{ Header() http.Header }) string {
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		return rec.Header().Get("Location")
	}
	return u.Query().Get("ok")
}

// TestOIDCKeyDirStateReportsProblems_B304 pins the four failure shapes the card
// must name (each with a fix), and the healthy case.
func TestOIDCKeyDirStateReportsProblems_B304(t *testing.T) {
	t.Run("relative-path", func(t *testing.T) {
		st := oidcKeyDirStateOf("./data/oidc-keys", "./data/oidc-keys", false, "", "")
		if st.Issue != "relative_path" {
			t.Errorf("Issue = %q, want relative_path", st.Issue)
		}
		if !strings.Contains(st.Fix, "/var/lib/skygate/oidc-keys") {
			t.Errorf("Fix = %q, want an absolute example path", st.Fix)
		}
	})

	t.Run("missing-dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "absent")
		st := oidcKeyDirStateOf(dir, dir, false, "", "")
		if st.Issue != "missing" || st.Exists {
			t.Errorf("Issue = %q Exists = %v, want missing/false", st.Issue, st.Exists)
		}
		if !strings.Contains(st.Fix, "mkdir -p -m 700") {
			t.Errorf("Fix = %q, want the mkdir command", st.Fix)
		}
	})

	t.Run("world-readable-private-key", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatalf("chmod dir: %v", err)
		}
		priv := filepath.Join(dir, "oidc-signing.pem")
		if err := os.WriteFile(priv, []byte("x"), 0644); err != nil {
			t.Fatalf("write private key: %v", err)
		}
		if err := os.Chmod(priv, 0644); err != nil {
			t.Fatalf("chmod private key: %v", err)
		}
		st := oidcKeyDirStateOf(dir, dir, true, "kid123", "")
		if st.Issue != "world_readable" {
			t.Fatalf("Issue = %q, want world_readable (mode %s)", st.Issue, st.PrivateMode)
		}
		if !strings.Contains(st.Fix, "chmod 600") {
			t.Errorf("Fix = %q, want chmod 600", st.Fix)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "oidc-signing.pem"), []byte("x"), 0600); err != nil {
			t.Fatalf("write private key: %v", err)
		}
		st := oidcKeyDirStateOf(dir, dir, true, "abcd1234", "")
		if runtime.GOOS == "windows" {
			// os.WriteFile's perm argument is advisory on Windows (the file reports
			// 0666), so the mode complaint is expected there; the Linux hosts this
			// ships to see 0600 and stay quiet.
			t.Logf("windows reports mode %s — the strict check runs on Linux/CI", st.PrivateMode)
		} else if st.Issue != "" || st.Problem != "" {
			t.Errorf("healthy store reported Issue=%q Problem=%q", st.Issue, st.Problem)
		}
		if st.Issue != "" && runtime.GOOS != "windows" {
			t.Errorf("Issue = %q, want none", st.Issue)
		}
		if !st.Writable || !st.Exists {
			t.Errorf("Exists=%v Writable=%v, want true/true", st.Exists, st.Writable)
		}
		if st.KID != "abcd1234" {
			t.Errorf("KID = %q, want the live kid", st.KID)
		}
	})

	t.Run("no-key-loaded", func(t *testing.T) {
		dir := t.TempDir()
		st := oidcKeyDirStateOf(dir, dir, false, "", "mkdir /root/x: permission denied")
		if st.Issue != "unavailable" {
			t.Errorf("Issue = %q, want unavailable for a store that is not loaded", st.Issue)
		}
		if !strings.Contains(st.Problem, "permission denied") {
			t.Errorf("Problem = %q, want the boot-time reason included", st.Problem)
		}
	})
}

// TestBuildOIDCEnvBlockNeverCarriesTheSecret_B304: the block is rendered in the
// browser, so it must reference the secret, never print it.
func TestBuildOIDCEnvBlockNeverCarriesTheSecret_B304(t *testing.T) {
	eff := EffectiveOIDCSettings{
		Issuer:       "https://gate.example.com/",
		ClientID:     "headscale",
		ClientSecret: "s3cr3t-value",
		RedirectURIs: "https://head.example.com/oidc/callback",
		KeyDir:       "/var/lib/skygate/oidc-keys",
	}
	block := buildOIDCEnvBlock(eff)
	for _, want := range []string{
		"SKYGATE_OIDC_ISSUER=https://gate.example.com\n",
		"SKYGATE_OIDC_CLIENT_ID=headscale\n",
		"SKYGATE_OIDC_REDIRECT_URIS=https://head.example.com/oidc/callback\n",
		"SKYGATE_OIDC_KEY_DIR=/var/lib/skygate/oidc-keys\n",
		"skygate oidc-export --secret",
		"WIN over these",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("env block is missing %q\n%s", want, block)
		}
	}
	if strings.Contains(block, "s3cr3t-value") {
		t.Errorf("the env block leaked the client secret:\n%s", block)
	}
}

// TestOIDCApplyScriptCommandIsPinnedToTheBuild_B304: the operator runs a script
// that must match the panel they are looking at, so a release build pins its tag
// and a source build falls back to main.
func TestOIDCApplyScriptCommand_B304(t *testing.T) {
	cmd := oidcApplyScriptCommand("v1.5.69+7b614757")
	if !strings.Contains(cmd, "/v1.5.69/deploy/skygate-apply-oidc.sh") {
		t.Errorf("command = %q, want the v1.5.69 tag pinned", cmd)
	}
	if !strings.Contains(cmd, "--dry-run") {
		t.Errorf("command = %q, want the dry-run first step", cmd)
	}
	if dev := oidcApplyScriptCommand("dev"); !strings.Contains(dev, "/main/deploy/") {
		t.Errorf("dev command = %q, want the main branch", dev)
	}
}

// TestPostAdminOIDCAppliesKeyDirLive_B304 is the core of the report: saving a new
// key_dir must MOVE the running store (the applier is called), and a path that
// cannot work must be refused BEFORE anything is written — so the stored row can
// never claim a directory the provider is not using.
func TestPostAdminOIDCAppliesKeyDirLive_B304(t *testing.T) {
	cfg := &config.Config{}
	svc, src, _ := newOIDCTestService(t, cfg)

	called := []string{}
	svc.OIDCKeyDirApplier = func(dir string) error {
		called = append(called, dir)
		return nil
	}
	svc.OIDCDefaultKeyDir = "/var/lib/skygate/oidc-keys"
	svc.OIDCKeyDirFn = func() (string, string, bool) { return "/var/lib/skygate/oidc-keys", "kid1", true }

	newDir := filepath.Join(t.TempDir(), "oidc-keys")
	rec := b303PostForm(t, svc.PostAdminOIDC, "/admin/oidc", map[string][]string{
		"enabled":       {"1"},
		"issuer":        {"https://gate.example.com"},
		"client_id":     {"headscale"},
		"client_secret": {"s3cret"},
		"redirect_uris": {"https://head.example.com/oidc/callback"},
		"key_dir":       {newDir},
	})
	if rec.Code != 303 {
		t.Fatalf("status = %d, want 303 (body=%q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("save refused: %s", loc)
	}
	if len(called) != 1 || called[0] != newDir {
		t.Fatalf("key-dir applier calls = %v, want exactly [%s]", called, newDir)
	}
	row, err := db.GetOIDCSettingsDecrypted(src.db, "")
	if err != nil {
		t.Fatalf("read back the saved row: %v", err)
	}
	if row.KeyDir != newDir {
		t.Errorf("stored key_dir = %q, want %q", row.KeyDir, newDir)
	}
	if !strings.Contains(loc0(rec), "oidc-keys") {
		t.Errorf("the success flash should mention the key directory move: %s", rec.Header().Get("Location"))
	}

	// A relative path is refused, nothing is applied, and the stored row keeps the
	// previous value.
	called = nil
	rec = b303PostForm(t, svc.PostAdminOIDC, "/admin/oidc", map[string][]string{
		"enabled":       {"1"},
		"issuer":        {"https://gate.example.com"},
		"client_id":     {"headscale"},
		"client_secret": {"s3cret"},
		"redirect_uris": {"https://head.example.com/oidc/callback"},
		"key_dir":       {"./data/oidc-keys"},
	})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "absolute") {
		t.Fatalf("relative key_dir Location = %q, want an absolute-path refusal", loc)
	}
	if len(called) != 0 {
		t.Errorf("the applier ran for a refused path: %v", called)
	}
	row, err = db.GetOIDCSettingsDecrypted(src.db, "")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.KeyDir != newDir {
		t.Errorf("stored key_dir changed to %q after a refusal, want %q", row.KeyDir, newDir)
	}
}

// TestPostAdminOIDCEmptyKeyDirUsesTheDefault_B304: an empty field means "use the
// built-in default", and the stored row must say so explicitly (the page and the
// host then agree on one path instead of one of them inventing "./data/…").
func TestPostAdminOIDCEmptyKeyDirUsesTheDefault_B304(t *testing.T) {
	svc, src, _ := newOIDCTestService(t, &config.Config{})
	svc.OIDCDefaultKeyDir = "/var/lib/skygate/oidc-keys"

	applied := []string{}
	svc.OIDCKeyDirApplier = func(dir string) error { applied = append(applied, dir); return nil }

	rec := b303PostForm(t, svc.PostAdminOIDC, "/admin/oidc", map[string][]string{
		"enabled":       {"1"},
		"issuer":        {"https://gate.example.com"},
		"client_id":     {"headscale"},
		"client_secret": {"s3cret"},
		"redirect_uris": {"https://head.example.com/oidc/callback"},
		"key_dir":       {""},
	})
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("save refused: %s", loc)
	}
	row, err := db.GetOIDCSettingsDecrypted(src.db, "")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.KeyDir != "/var/lib/skygate/oidc-keys" {
		t.Errorf("stored key_dir = %q, want the built-in default", row.KeyDir)
	}
	if len(applied) != 1 || applied[0] != "/var/lib/skygate/oidc-keys" {
		t.Errorf("applier calls = %v, want the default path", applied)
	}
}

// TestOIDCSyncUsesEffectiveConfig_B304 is the second half of the operator's
// complaint, pinned at the source level: the sync page must not fall back to the
// raw env, because an operator who configured everything in the panel was told
// "SKYGATE_OIDC_ISSUER is not set on the skygate container".
func TestOIDCSyncUsesEffectiveConfigSource_B304(t *testing.T) {
	raw, err := os.ReadFile("oidc_sync.go")
	if err != nil {
		t.Fatalf("read oidc_sync.go: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, "s.Cfg.OIDCIssuerURL") || strings.Contains(src, "s.Cfg.OIDCClientSecret") {
		t.Error("oidc_sync.go reads the raw env again — a panel-configured install would be told to edit .env")
	}
	if !strings.Contains(src, "s.effectiveOIDCSettings()") {
		t.Error("oidc_sync.go no longer resolves the effective settings")
	}
	// The refusal messages must point at the panel field, not only at the env var.
	if strings.Contains(src, "is not set on the skygate container") {
		t.Error("the sync page still refuses with the env-only wording")
	}
}
