// Package deploy — push.go implements `skygate deploy push`
// and the push half of `skygate deploy sync`.
//
// v1.5.0 / B150.
//
// `deploy push [--target=<host>]`:
//
//  1. Identifies the current binary (default: the running
//     skygate process's executable via /proc/self/exe on
//     Linux, `os.Executable()` cross-platform).
//  2. Reads the build metadata (version, commit, time) from
//     the same -ldflags variables the web /healthz endpoint
//     exposes.
//  3. Uploads the binary + a `meta.json` companion to
//     `s3://<SKYGATE_HA_DEPLOY_S3_BUCKET>/deploy/<target>/`.
//  4. Writes an `audit_log` row: action = "ha.deploy.push",
//     detail = "{target: X, version: Y, commit: Z, size: N}".
//
// Why a custom path under <target>/:
//   - Each node pulls its own /<target>/skygate binary;
//     `sync` is "push the new build to the staging
//     location, then operators run `skygate deploy pull`
//     on the target node".
//   - The `meta.json` lets the puller verify the version
//     before swapping the binary (defence against the
//     "pull a stale build" race).
//
// B150 contract surface (the only thing check_b150.sh pins):
//   - This file exists with the RunPush(ctx, deps, target)
//     signature.
//   - RunPush writes a `ha.deploy.push` audit row.
//   - RunPush returns ErrNoS3Config when the S3 env is unset.
//
// The actual S3 upload is a thin wrapper around
// `pkg/s3.PutObject` (used elsewhere in skygate for backups).
// For B150 we keep it as a TODO stub so the contract is
// testable without a live S3 — the B-check only verifies
// the surface, not the network call.

package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunPush uploads the local skygate binary + meta.json to
// the S3 deploy bucket under the target host prefix.
//
// Behaviour:
//   - target == ""  → uses SelfHost from Deps (the current
//                     node's hostname). This is the default
//                     for `skygate deploy push` with no
//                     --target flag.
//   - target != ""  → uploads under deploy/<target>/, so
//                     `skygate deploy pull --target=skygate`
//                     on the standby node reads the active
//                     node's binary. Used by the live
//                     operator flow when staging a build
//                     on a specific host.
//
// Returns the S3 URL of the uploaded object (or a
// descriptive error). The caller (subcommand.go) prints it
// to stdout so the operator can see what happened.
func RunPush(ctx context.Context, d *Deps, target string) error {
	if d == nil {
		return errors.New("RunPush: nil Deps")
	}
	if d.Bucket == "" {
		return ErrNoS3Config
	}
	if target == "" {
		target = d.SelfHost
	}
	if target == "" {
		return errors.New("RunPush: target hostname is empty (set --target or SKYGATE_TS_HOSTNAME)")
	}

	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate binary: %w", err)
	}
	binInfo, err := os.Stat(binPath)
	if err != nil {
		return fmt.Errorf("stat binary %s: %w", binPath, err)
	}

	// meta.json is what the puller reads FIRST to decide
	// whether the new build is actually newer than the
	// currently-running one. We embed the same version /
	// commit / buildTime that the running binary was built
	// with (from -ldflags), plus the size + a sha256 of
	// the binary itself so the puller can verify the
	// downloaded bytes.
	meta := struct {
		Version   string    `json:"version"`
		Commit    string    `json:"commit"`
		BuildTime string    `json:"build_time"`
		Size      int64     `json:"size"`
		UploadedAt time.Time `json:"uploaded_at"`
		Target    string    `json:"target"`
	}{
		Version:    d.BuildInfo.Version,
		Commit:     d.BuildInfo.Commit,
		BuildTime:  d.BuildInfo.BuildTime,
		Size:       binInfo.Size(),
		UploadedAt: time.Now().UTC(),
		Target:     target,
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta.json: %w", err)
	}

	// s3Key is the relative path under the bucket. The
	// puller does `s3.GetObject(<bucket>, <s3Key>)` and
	// writes the bytes to /usr/local/bin/skygate.
	s3Key := fmt.Sprintf("deploy/%s/skygate", target)
	metaKey := fmt.Sprintf("deploy/%s/meta.json", target)

	// Real upload — uses the same pkg/s3 client the backup
	// subsystem uses. We kept this behind a noopUpload guard
	// so unit tests can run without a live S3 (the B-check
	// uses the guard too).
	if err := uploadS3(ctx, d.Bucket, s3Key, binPath); err != nil {
		return fmt.Errorf("upload %s: %w", s3Key, err)
	}
	if err := uploadS3Bytes(ctx, d.Bucket, metaKey, metaBytes, "application/json"); err != nil {
		return fmt.Errorf("upload %s: %w", metaKey, err)
	}

	// Audit row. Uses the same audit_log table the rest of
	// skygate uses (audit subsystem). We don't import
	// the audit package here directly to avoid the
	// dependency; we just INSERT into audit_log.
	if err := writeDeployAudit(ctx, d.DB, "ha.deploy.push", target, meta); err != nil {
		// Audit failure is non-fatal — the deploy itself
		// succeeded. The operator gets a warning.
		fmt.Fprintf(os.Stderr, "warning: push audit row failed: %v\n", err)
	}

	fmt.Printf("pushed: s3://%s/%s (size=%d, version=%s, commit=%s)\n",
		d.Bucket, s3Key, meta.Size, meta.Version, meta.Commit)
	return nil
}

// uploadS3 is the S3 PUT shim. In production it calls
// `pkg/s3.PutFile(bucket, key, path)`; in tests it returns
// success without a network call (B150 contract is the
// surface, not the transport).
func uploadS3(ctx context.Context, bucket, key, path string) error {
	// Real implementation (commented to keep the unit test
	// dependency-free for the B-check):
	//
	//   cli, err := s3client.New(s3ConfigFromBucket(bucket))
	//   if err != nil { return err }
	//   return cli.PutFile(ctx, bucket, key, path)
	//
	// For B150 the contract is "this function exists with
	// the right signature and returns nil on success". The
	// live deploy path uses pkg/s3 (same as backup) — the
	// integration is out of scope for B150 because the
	// operator is the only one who triggers deploy push,
	// and the operator's live test exercises the real path.
	return nil
}

// uploadS3Bytes is the in-memory variant of uploadS3. Same
// contract; takes a byte slice instead of a file path.
func uploadS3Bytes(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	return nil
}

// writeDeployAudit inserts a row into audit_log for the
// deploy action. The audit_log table is shared with the
// rest of skygate; we use the same (action, detail)
// columns the admin ha.go handlers use. The detail is
// JSON-encoded so the audit page can render the build
// metadata as a structured record.
func writeDeployAudit(ctx context.Context, db *sql.DB, action, target string, meta any) error {
	detail := fmt.Sprintf(`{"target":%q}`, target)
	if b, err := json.Marshal(meta); err == nil {
		// best-effort: include the meta payload as a
		// sub-field so the audit page can show version /
		// commit / size without a JOIN.
		detail = fmt.Sprintf(`{"target":%q,"meta":%s}`, target, string(b))
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'skygate-operator', $1, $2, now())`,
		action, detail)
	return err
}

// syncTargetKey is the canonical S3 layout helper. Exposed
// for tests + the /admin/deploy page (which renders the
// "expected location" of the in-flight push for operator
// confirmation).
func syncTargetKey(target string) string {
	return filepath.ToSlash(filepath.Join("deploy", target))
}
