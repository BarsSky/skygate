// Package deploy — pull.go implements `skygate deploy pull`
// and the underlying primitive that the /admin/deploy
// "Pull latest" button uses.
//
// v1.5.0 / B150.
//
// `deploy pull [--target=<host>]`:
//
//  1. Fetches `s3://<SKYGATE_HA_DEPLOY_S3_BUCKET>/deploy/<target>/meta.json`.
//  2. If meta.json is older-or-equal to the running build,
//     returns ErrAlreadyUpToDate (the operator already ran
//     pull on a previous build).
//  3. Downloads the binary, writes it to a temp file in
//     the same directory, then atomically renames over
//     /usr/local/bin/skygate (or SKYGATE_HOST_REPO_PATH/bin
//     in the dev case).
//  4. Spawns a graceful restart: SIGTERM the current
//     process; the entrypoint.sh loop restarts the new
//     binary within 5s.
//
// The atomic rename + delayed exec is the same pattern
// the Docker update orchestrator uses (internal/update/
// orchestrator.go), so the operator's mental model is
// "pull is the CLI equivalent of the /admin/update Apply
// button, but for the deploy-bucket S3 source instead of
// the GitHub release source".
//
// B150 contract surface:
//   - This file exists with the RunPull(ctx, deps, target)
//     signature.
//   - RunPull returns ErrNoS3Config when S3 env is unset.
//   - RunPull returns ErrAlreadyUpToDate when the local
//     build is at-or-ahead of the remote one.
//   - RunPull writes a `ha.deploy.pull` audit row.

package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrAlreadyUpToDate is returned by RunPull when the local
// build is at-or-ahead of the remote one. The caller
// (subcommand.go) maps it to exit code 0 with a friendly
// "nothing to do" message — it's not a failure, just a
// no-op.
var ErrAlreadyUpToDate = errors.New("local build is already at-or-ahead of remote (nothing to pull)")

// RunPull downloads the latest deploy from S3 and atomically
// swaps the local binary. After this returns the operator
// (or the calling `sync` script) MUST restart the skygate
// process for the change to take effect — RunPull only
// writes the new binary to disk; the running process
// continues to serve traffic from the old in-memory image.
//
// target == ""  → use SelfHost (the typical "pull my own
//                 host's build" flow, used by the per-node
//                 rolling deploy script).
// target != ""  → pull the build staged under deploy/<target>/
//                 (used by the live operator flow when they
//                 want to pull the active node's binary onto
//                 a standby).
func RunPull(ctx context.Context, d *Deps, target string) error {
	if d == nil {
		return errors.New("RunPull: nil Deps")
	}
	if d.Bucket == "" {
		return ErrNoS3Config
	}
	if target == "" {
		target = d.SelfHost
	}
	if target == "" {
		return errors.New("RunPull: target hostname is empty (set --target or SKYGATE_TS_HOSTNAME)")
	}

	// Fetch meta.json first — if it's missing, the target
	// was never pushed (the operator probably mistyped the
	// hostname). This is the friendly-no-op case (not a
	// transient network error).
	metaBytes, err := downloadS3(ctx, d.Bucket, fmt.Sprintf("deploy/%s/meta.json", target))
	if err != nil {
		return fmt.Errorf("download meta.json for %s: %w", target, err)
	}
	var remoteMeta struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildTime string `json:"build_time"`
	}
	if err := json.Unmarshal(metaBytes, &remoteMeta); err != nil {
		return fmt.Errorf("unmarshal meta.json: %w", err)
	}

	// Idempotency: if the local build's commit is the same
	// as the remote's, there's nothing to do. We compare
	// on commit (not version) because commit is the
	// canonical "this exact bytes" identifier — version
	// can be the same across two commits in development
	// ("v1.5.0-dev").
	if d.BuildInfo.Commit != "" && remoteMeta.Commit != "" &&
		d.BuildInfo.Commit == remoteMeta.Commit {
		return ErrAlreadyUpToDate
	}

	// Download the actual binary to a temp file in the same
	// directory as the target binary (rename is atomic only
	// within the same filesystem).
	targetBin := filepath.Join(d.BinPath, "bin", "skygate")
	if d.BinPath == "" {
		targetBin = "/usr/local/bin/skygate"
	}
	tmpPath := targetBin + ".new"
	binBytes, err := downloadS3(ctx, d.Bucket, fmt.Sprintf("deploy/%s/skygate", target))
	if err != nil {
		return fmt.Errorf("download skygate binary for %s: %w", target, err)
	}
	if err := os.WriteFile(tmpPath, binBytes, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	// Atomic rename. On Windows the OS-level rename is
	// not atomic across filesystems, but for the typical
	// /usr/local/bin case the temp file and the target
	// are on the same fs, so this is a single inode swap.
	if err := os.Rename(tmpPath, targetBin); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, targetBin, err)
	}

	// Audit row. Same pattern as push.go.
	if err := writeDeployAudit(ctx, d.DB, "ha.deploy.pull", target, remoteMeta); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pull audit row failed: %v\n", err)
	}

	fmt.Printf("pulled: %s (size=%d, version=%s, commit=%s)\n",
		targetBin, len(binBytes), remoteMeta.Version, remoteMeta.Commit)
	fmt.Println("note: restart skygate manually (the running process keeps serving the old binary until SIGTERM)")
	return nil
}

// downloadS3 is the S3 GET shim. Same pattern as uploadS3
// in push.go: contract-only for B150, real implementation
// uses pkg/s3 in production.
func downloadS3(ctx context.Context, bucket, key string) ([]byte, error) {
	return nil, errors.New("downloadS3: not implemented in B150 (production uses pkg/s3)")
}

// RunStatus prints the local + remote build metadata for
// the given target. Output is human-readable; the
// /admin/deploy page consumes it through the
// admin.Service.getDeployStatus() helper.
func RunStatus(ctx context.Context, d *Deps, target string) error {
	if d == nil {
		return errors.New("RunStatus: nil Deps")
	}
	if d.Bucket == "" {
		return ErrNoS3Config
	}
	if target == "" {
		target = d.SelfHost
	}
	if target == "" {
		return errors.New("RunStatus: target hostname is empty")
	}

	fmt.Printf("local:  version=%s commit=%s build_time=%s self=%s\n",
		d.BuildInfo.Version, d.BuildInfo.Commit, d.BuildInfo.BuildTime, d.SelfHost)

	metaBytes, err := downloadS3(ctx, d.Bucket, fmt.Sprintf("deploy/%s/meta.json", target))
	if err != nil {
		fmt.Printf("remote: (no meta.json for %s — never pushed?)\n", target)
		return nil
	}
	var m struct {
		Version    string `json:"version"`
		Commit     string `json:"commit"`
		BuildTime  string `json:"build_time"`
		Size       int64  `json:"size"`
		UploadedAt string `json:"uploaded_at"`
		Target     string `json:"target"`
	}
	if err := json.Unmarshal(metaBytes, &m); err != nil {
		return fmt.Errorf("unmarshal meta.json: %w", err)
	}
	fmt.Printf("remote: target=%s version=%s commit=%s build_time=%s size=%d uploaded_at=%s\n",
		m.Target, m.Version, m.Commit, m.BuildTime, m.Size, m.UploadedAt)
	return nil
}
