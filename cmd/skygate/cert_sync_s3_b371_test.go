// B371 — certsync must read the S3 configuration the OPERATOR actually set, and a
// configuration it cannot work with must be refused by name.
//
// MEASURED LIVE, 2026-10-09 (reference deployment). The operator configured the
// S3/MinIO settings on /admin/backup, which writes global_settings, and the panel's
// own backup worked — but the journal carried, every 30 seconds, forever:
//
//	certsync: get .version: Get "http://s3..amazonaws.com/skygate-backups/?location=":
//	no such host
//
// The cause: B147 built certsync's config from the SKYGATE_S3_* ENV VARS alone
// ("same source the backup subsystem uses"), a premise that stopped being true when
// the backup subsystem moved to global_settings. With the variables unset, both the
// endpoint and the region were empty and `fmt.Sprintf("https://s3.%s.amazonaws.com",
// "")` produced the double-dotted host above (minio-go normalised it to http).
//
// These tests pin the three properties of the fix:
//  1. the DB (the panel) is the FIRST source, env vars only fill the gaps;
//  2. a config that cannot work is refused with a message naming the missing
//     setting — never a host that cannot exist;
//  3. the startup line names the endpoint the client will really use.
package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"skygate/internal/backup"
	"skygate/internal/config"

	skygatedb "skygate/internal/db"
)

// b371DB is a migrated SQLite database with the panel's S3 settings, exactly as
// /admin/backup writes them (backup.Load reads `backup.%` keys).
func b371DB(t *testing.T, kv map[string]string) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b371.db")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	for k, v := range kv {
		if err := skygatedb.SetGlobalSetting(d, k, v); err != nil {
			t.Fatalf("SetGlobalSetting(%s): %v", k, err)
		}
	}
	return d
}

// clearS3Env removes the env fallback for the duration of one test, so a value in
// the developer's shell cannot make a test pass.
func clearS3Env(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SKYGATE_S3_ENDPOINT", "SKYGATE_S3_REGION", "SKYGATE_S3_ACCESS_KEY", "SKYGATE_S3_SECRET_KEY"} {
		old, had := os.LookupEnv(k)
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv(%s): %v", k, err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, old)
			}
		})
	}
}

// TestB371_ThePanelIsTheFirstSource is the live case: the endpoint lives in
// global_settings and the env vars are unset, and certsync must use the panel's
// value — not the empty env value that produced s3..amazonaws.com.
func TestB371_ThePanelIsTheFirstSource(t *testing.T) {
	clearS3Env(t)
	dbx := b371DB(t, map[string]string{
		"backup.s3_endpoint":   "https://minio.example.com",
		"backup.s3_region":     "us-east-1",
		"backup.s3_access_key": "panel-key",
		"backup.s3_secret_key": "panel-secret",
	})
	cfg := &config.Config{CertSyncBucket: "skygate-backups"}

	got, err := certSyncS3Config(dbx, cfg)
	if err != nil {
		t.Fatalf("certSyncS3Config with a fully configured panel: %v", err)
	}
	if got.S3Endpoint != "https://minio.example.com" {
		t.Errorf("S3Endpoint = %q, want the panel's value", got.S3Endpoint)
	}
	if got.S3Region != "us-east-1" {
		t.Errorf("S3Region = %q, want us-east-1", got.S3Region)
	}
	if got.S3AccessKey != "panel-key" || got.S3SecretKey != "panel-secret" {
		t.Errorf("credentials did not come from the panel: %+v", got)
	}
	if got.S3Bucket != "skygate-backups" {
		t.Errorf("bucket = %q, want the certsync-specific bucket", got.S3Bucket)
	}
	// And the nonsense host cannot be produced from this config.
	if strings.Contains(certSyncS3EndpointLog(got), "s3..") {
		t.Errorf("the endpoint log carries the double-dotted host: %q", certSyncS3EndpointLog(got))
	}
}

// TestB371_EnvIsTheFallbackNotTheSource keeps the documented alternative working:
// a deployment that sets SKYGATE_S3_* and has nothing in the DB still gets a client.
func TestB371_EnvIsTheFallbackNotTheSource(t *testing.T) {
	clearS3Env(t)
	t.Setenv("SKYGATE_S3_ENDPOINT", "https://env.example.com")
	t.Setenv("SKYGATE_S3_ACCESS_KEY", "env-key")
	t.Setenv("SKYGATE_S3_SECRET_KEY", "env-secret")
	// A DB with no S3 rows at all: backup.Load returns its defaults.
	dbx := b371DB(t, nil)
	cfg := &config.Config{CertSyncBucket: "certs-bucket"}

	got, err := certSyncS3Config(dbx, cfg)
	if err != nil {
		t.Fatalf("certSyncS3Config with env-only configuration: %v", err)
	}
	if got.S3Endpoint != "https://env.example.com" {
		t.Errorf("S3Endpoint = %q, want the env value", got.S3Endpoint)
	}
	if got.S3AccessKey != "env-key" || got.S3SecretKey != "env-secret" {
		t.Errorf("credentials did not fall back to the env: %+v", got)
	}
	if got.S3Region == "" {
		t.Error("region is empty — Load's default (us-east-1) must survive")
	}
}

// TestB371_AnUnusableConfigIsRefusedByName is the guard: no host that cannot exist
// may reach minio-go, and the error must name the setting to fix.
func TestB371_AnUnusableConfigIsRefusedByName(t *testing.T) {
	cases := []struct {
		name string
		cfg  *backup.Config
		want string
	}{
		{
			"no endpoint and no region (the live nonsense host)",
			&backup.Config{S3Bucket: "b", S3AccessKey: "k", S3SecretKey: "s"},
			"s3..amazonaws.com",
		},
		{
			"no credentials",
			&backup.Config{S3Bucket: "b", S3Endpoint: "https://minio.example.com", S3Region: "us-east-1"},
			"access key",
		},
		{
			"no bucket",
			&backup.Config{S3Endpoint: "https://minio.example.com", S3Region: "us-east-1", S3AccessKey: "k", S3SecretKey: "s"},
			"bucket",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := certSyncS3ConfigProblem(tc.cfg)
			if err == nil {
				t.Fatal("an unusable certsync S3 config was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name %q: %v", tc.want, err)
			}
		})
	}
	// A config with an endpoint but no region is legitimate: the endpoint decides.
	if err := certSyncS3ConfigProblem(&backup.Config{
		S3Bucket: "b", S3Endpoint: "https://minio.example.com", S3AccessKey: "k", S3SecretKey: "s",
	}); err != nil {
		t.Errorf("an endpoint-only config must be accepted (the endpoint is enough): %v", err)
	}
}

// TestB371_TheStartupLogNamesTheRealEndpoint: "which S3 is certsync using?" must be
// answerable from the journal without waiting for a failure.
func TestB371_TheStartupLogNamesTheRealEndpoint(t *testing.T) {
	if got := certSyncS3EndpointLog(&backup.Config{S3Endpoint: "https://minio.example.com"}); got != "https://minio.example.com" {
		t.Errorf("endpoint log = %q, want the configured endpoint", got)
	}
	// When the operator really wants AWS (no endpoint), the log must mirror what
	// newS3Client is about to build instead of printing an empty string.
	got := certSyncS3EndpointLog(&backup.Config{S3Region: "eu-central-1"})
	if !strings.Contains(got, "s3.eu-central-1.amazonaws.com") {
		t.Errorf("AWS-default log = %q, want the host newS3Client derives", got)
	}
	if strings.Contains(got, "s3..") {
		t.Errorf("AWS-default log carries an empty region: %q", got)
	}
}
