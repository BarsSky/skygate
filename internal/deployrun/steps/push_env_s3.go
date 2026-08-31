// internal/deployrun/steps/push_env_s3.go —
// B194 step 4: PushEnvToS3Step.
//
// Pushes the current primary's .env to
// s3://<bucket>/ha/deploy/<hostname>/.env so the
// new node can download it during bootstrap_standby.sh.
//
// The .env is read from the local filesystem
// (/home/skyadmin/skygate/.env on the primary), NOT
// from the running container's env. The container's
// env is what skygate READS at runtime; the file is
// what bootstrap_standby.sh READS at deploy time.
// They SHOULD match (the container's env is built
// from the file via docker compose env_file: .env),
// but reading from the file gives the operator a
// single point of truth.
//
// Failure modes:
//   - S3 not configured (step is marked SKIPPED
//     with a clear hint; the deploy can still
//     succeed if a later phase uses a different
//     distribution mechanism)
//   - .env file missing (operator's home dir is
//     wrong)
//   - S3 PUT fails (network, auth, NoSuchBucket)
//
// Rollback deletes the uploaded object.

package steps

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"skygate/internal/deployrun"
)

func init() {
	deployrun.RegisterStep("PushEnvToS3", &PushEnvToS3Step{})
}

type PushEnvToS3Step struct{}

func (s *PushEnvToS3Step) Name() string        { return "PushEnvToS3" }
func (s *PushEnvToS3Step) Description() string { return "Push .env to s3://<bucket>/ha/deploy/<hostname>/.env" }
func (s *PushEnvToS3Step) IsOptional() bool    { return true } // can be skipped if S3 not configured
func (s *PushEnvToS3Step) DependsOn() []string { return []string{"ValidateInput"} }

func (s *PushEnvToS3Step) Run(ctx *deployrun.DeployContext) (*deployrun.StepResult, error) {
	result := &deployrun.StepResult{Status: deployrun.StepRunning}
	log := ctx.Logger

	// Find the .env file. The framework config can override
	// the path; default to the operator's home + skygate/.env.
	envPath := os.Getenv("SKYGATE_ENV_FILE")
	if envPath == "" {
		envPath = "/home/skyadmin/skygate/.env"
	}
	log.Info("reading .env from %s", envPath)
	data, err := os.ReadFile(envPath)
	if err != nil {
		result.Status = deployrun.StepFailed
		result.Error = fmt.Sprintf("read %s: %v", envPath, err)
		log.Error("%s", result.Error)
		log.Hint("set SKYGATE_ENV_FILE=/path/to/.env in the skygate environment")
		return result, errors.New(result.Error)
	}
	log.Info("read %d bytes from .env", len(data))

	// Find the S3 client. If not configured, skip with
	// a clear hint (the operator may have a different
	// distribution mechanism in mind for Phase 1).
	if ctx.S3Client == nil {
		result.Status = deployrun.StepSkipped
		result.Error = "S3 not configured — operator must push .env manually or wire up S3"
		log.Warn("%s", result.Error)
		log.Hint("set SKYGATE_S3_BUCKET + SKYGATE_S3_ENDPOINT + SKYGATE_S3_ACCESS_KEY + SKYGATE_S3_SECRET_KEY")
		return result, nil
	}

	prefix := ctx.Cfg.S3Prefix
	if prefix == "" {
		prefix = "ha/deploy"
	}
	key := fmt.Sprintf("%s/%s/.env", strings.TrimSuffix(prefix, "/"), ctx.Run.Hostname)
	log.Info("pushing to s3://%s/%s (size=%d)", ctx.Cfg.S3Bucket, key, len(data))

	etag, err := ctx.S3Client.PutObject(ctx.Ctx, key, bytes.NewReader(data), int64(len(data)), "application/octet-stream")
	if err != nil {
		result.Status = deployrun.StepFailed
		result.Error = fmt.Sprintf("S3 PutObject %s/%s: %v", ctx.Cfg.S3Bucket, key, err)
		log.Error("%s", result.Error)
		log.Hint("check that the bucket exists and the access key has PutObject permission")
		return result, errors.New(result.Error)
	}
	log.Info("uploaded: etag=%s", etag)
	result.Status = deployrun.StepSuccess
	result.Metadata = fmt.Sprintf(`{"bucket":%q,"key":%q,"size":%d,"etag":%q}`,
		ctx.Cfg.S3Bucket, key, len(data), etag)
	return result, nil
}

func (s *PushEnvToS3Step) Rollback(ctx *deployrun.DeployContext) error {
	if ctx.S3Client == nil {
		return nil
	}
	prefix := ctx.Cfg.S3Prefix
	if prefix == "" {
		prefix = "ha/deploy"
	}
	key := fmt.Sprintf("%s/%s/.env", strings.TrimSuffix(prefix, "/"), ctx.Run.Hostname)
	return ctx.S3Client.DeleteObject(ctx.Ctx, key)
}
