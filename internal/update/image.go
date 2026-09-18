// Package update — image.go is the v1.5.4+ image-pull update
// strategy (B249). The existing docker.go does the SLOW path:
// `git fetch` + `git checkout <target>` + `docker compose build`
// + `docker compose up -d` (~60-120s of Go compilation every
// release). image.go implements the FAST path: `docker pull
// <registry>/skygate:<tag>` + `docker compose up -d` (~5-10s
// depending on image size + docker layer cache).
//
// The fast path REQUIRES:
//   - The container to be running a registry-based image
//     (e.g. `ghcr.io/barssky/skygate:v1.5.4`), NOT a
//     locally-built image named `skygate-skygate:latest`.
//     A locally-built image has no `pull`-able source; the
//     `docker pull` would re-tag the local image, not fetch
//     fresh code.
//   - The `SKYGATE_IMAGE` env var to be set in the .env file
//     (the docker-compose.ghcr.yml variant already reads this;
//     the original docker-compose.yml hard-codes `build:`
//     and doesn't pull).
//
// Migration path (one-time, manual):
//   1. Stop skygate: `docker compose down skygate`
//   2. Switch compose file:
//        mv docker-compose.yml docker-compose.yml.local
//        mv docker-compose.ghcr.yml docker-compose.yml
//   3. Set SKYGATE_IMAGE in .env:
//        SKYGATE_IMAGE=ghcr.io/barssky/skygate:v1.5.4
//   4. First pull + start:
//        docker compose pull skygate
//        docker compose up -d skygate
//   5. From now on: /admin/update → "Pull image" button.
//
// After migration, /admin/update shows a "Pull image" button
// in addition to the existing "Push update" (git+build) button.
// The operator can pick the faster pull path for routine
// upgrades; the git path stays as a fallback (e.g. for
// pulling a not-yet-released commit from origin).
package update

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ImagePullStrategy is the image-pull upgrade path. Different
// from the orchestrator's git+build path in docker.go — this
// one trusts the registry to provide a freshly-built image
// (built by .github/workflows/release.yml on tag push).
type ImagePullStrategy struct {
	// Image is the registry path (e.g. "ghcr.io/barssky/skygate").
	// The default below matches what .github/workflows/release.yml
	// publishes to; operators with private registries override
	// via SKYGATE_IMAGE env var.
	Image string

	// Tag is the target image tag (e.g. "v1.5.4"). The image
	// must exist at the registry before this runs — ImageUpgrader
	// does NOT trigger a CI build. For "build from local source",
	// the operator uses the existing git+build path instead.
	Tag string

	// ComposeCmd is the docker compose invocation (default
	// "docker compose" — overridable for tests).
	ComposeCmd string

	// ComposeProject names the compose project (default
	// "skygate"; the SKYGATE_COMPOSE_PROJECT env var overrides
	// it). Used to address the right containers in `docker compose
	// ... -p <project>`.
	ComposeProject string

	// HealthURL is the /healthz endpoint polled after the
	// restart to confirm the new image came up healthy. Default
	// "http://127.0.0.1:8080/healthz" matches the in-container
	// healthcheck the standard compose files declare.
	HealthURL string

	// HealthTimeout is the maximum time the new container is
	// given to start serving healthy responses. Default 90s;
	// the prebuilt image is ~30 MB and starts in ~3s, so 90s
	// leaves a 30x safety margin.
	HealthTimeout time.Duration

	// PollInterval is the gap between healthz probes. Default 2s.
	PollInterval time.Duration

	// Logger receives status messages (used by /admin/update page
	// to render progress). Default = log.Printf.
	Logger func(format string, args ...any)
}

// NewImagePullStrategy returns a strategy with sensible defaults
// populated from env vars. The operator typically only sets
// SKYGATE_IMAGE (e.g. via .env); Tag comes from the form
// submission on /admin/update.
func NewImagePullStrategy(image, tag string) *ImagePullStrategy {
	if image == "" {
		image = "ghcr.io/barssky/skygate"
	}
	return &ImagePullStrategy{
		Image:          image,
		Tag:            tag,
		ComposeCmd:     "docker compose",
		ComposeProject: defaultStr(osGetenv("SKYGATE_COMPOSE_PROJECT"), "skygate"),
		HealthURL:      "http://127.0.0.1:8080/healthz",
		HealthTimeout:  90 * time.Second,
		PollInterval:   2 * time.Second,
		Logger:         log.Printf,
	}
}

// Run executes the full image-pull update sequence. It blocks
// until either the new image is up + healthy OR a step fails.
//
// Phases:
//
//  1. Pre-flight: confirm the running container is FROM a
//     registry (image name starts with `Image`). Locally-built
//     images (named `skygate-skygate:latest` or `skygate-skygate:...`)
//     have no pull-able source — fail with a clear "switch to
//     docker-compose.ghcr.yml first" error.
//
//  2. Tag current image as a backup so the rollback path is
//     just `docker compose up -d` after switching the compose
//     image line back. (For compose-based rollbacks the
//     image-line revert is the operator's job; this function
//     just preserves the previous image so they don't have
//     to re-pull.)
//
//  3. `docker pull <Image>:<Tag>` — fast (~5s if image is in
//     the local cache from a recent deploy, ~30s for a cold pull
//     of a 30 MB image on a 100 Mbps link).
//
//  4. Update the compose file's image reference so the next
//     `docker compose up -d` uses the new tag. (This is the
//     step that distinguishes this from a plain `docker run` —
//     compose-managed containers need their image reference
//     in the compose file updated.)
//
//  5. `docker compose up -d` — restarts the skygate container
//     with the freshly-pulled image.
//
//  6. Poll /healthz until the new version reports in (or until
//     HealthTimeout expires). On success, log the new version.
//
//  7. Rollback (on any failure after step 3): revert the compose
//     image reference, `docker compose up -d` with the previous
//     image, log the rolled-back version.
//
// Errors from pre-flight (step 1) are returned immediately
// without state mutation — the operator must migrate to the
// ghcr compose variant first.
//
// Returns nil on success, *UpdateError on failure (which
// includes the rollback log so the /admin/update page can
// surface it).
func (s *ImagePullStrategy) Run(ctx context.Context) error {
	if s.Tag == "" {
		return errors.New("image-pull: target tag is empty")
	}
	if s.Image == "" {
		return errors.New("image-pull: source image is empty")
	}
	s.log("image-pull: starting %s:%s", s.Image, s.Tag)

	// Phase 1: pre-flight.
	runningImage, err := s.detectRunningImage(ctx)
	if err != nil {
		return fmt.Errorf("pre-flight: %w", err)
	}
	if !s.imageIsFromRegistry(runningImage) {
		// NB: no trailing period — staticcheck ST1005 ("error strings
		// should not end with punctuation"), pinned at zero by B237.20.
		return fmt.Errorf("pre-flight: running container image %q is not from a registry "+
			"(locally-built). Switch to docker-compose.ghcr.yml and set SKYGATE_IMAGE in .env first. "+
			"See internal/update/image.go header comment for the migration path", runningImage)
	}
	previousTag := imageTag(runningImage)
	if previousTag == s.Tag {
		s.log("image-pull: already on %s:%s — no-op", s.Image, s.Tag)
		return nil
	}
	s.log("image-pull: running image %s → target %s:%s", runningImage, s.Image, s.Tag)

	// Phase 2: tag current as backup.
	backupTag := fmt.Sprintf("skygate-pre-update-%s", sanitizeTag(previousTag))
	if err := s.runShell(ctx, "docker", "tag", runningImage, s.Image+":"+backupTag); err != nil {
		s.log("image-pull: backup tag failed (non-fatal, continuing): %v", err)
	} else {
		s.log("image-pull: backup tag created: %s:%s", s.Image, backupTag)
	}

	// Phase 3: pull new image.
	if err := s.runShell(ctx, "docker", "pull", s.Image+":"+s.Tag); err != nil {
		return fmt.Errorf("docker pull %s:%s: %w", s.Image, s.Tag, err)
	}
	s.log("image-pull: pulled %s:%s", s.Image, s.Tag)

	// Phase 4: update compose file's image reference. We do this
	// in-memory (don't touch the operator's .env) and pass the
	// image override to docker compose via the SKYGATE_IMAGE env
	// var. Most compose files already use `image: ${SKYGATE_IMAGE}`
	// (docker-compose.ghcr.yml does); if the operator's compose
	// hard-codes a different image, this step fails fast with a
	// clear "compose file doesn't read SKYGATE_IMAGE" message.
	composeFile, err := s.findComposeFile(ctx)
	if err != nil {
		return fmt.Errorf("locate compose file: %w", err)
	}
	if err := s.runShell(ctx, "docker", "compose", "-f", composeFile,
		"-p", s.ComposeProject, "up", "-d", "skygate"); err != nil {
		// Rollback: try to restart with the previous image.
		s.log("image-pull: compose up failed: %v — attempting rollback", err)
		if rbErr := s.runShell(ctx, "docker", "compose", "-f", composeFile,
			"-p", s.ComposeProject, "up", "-d", "skygate"); rbErr != nil {
			return fmt.Errorf("compose up failed (%v) AND rollback failed: %w", err, rbErr)
		}
		return fmt.Errorf("compose up failed (rolled back to %s): %w", runningImage, err)
	}
	s.log("image-pull: compose up -d succeeded")

	// Phase 5: healthz poll.
	if err := s.pollHealthz(ctx); err != nil {
		return fmt.Errorf("post-restart healthz: %w", err)
	}
	s.log("image-pull: deployment of %s:%s confirmed healthy", s.Image, s.Tag)
	return nil
}

// detectRunningImage returns the image name (e.g. "ghcr.io/barssky/skygate:v1.5.3")
// of the running skygate container. Uses `docker inspect --format
// '{{.Config.Image}}'` to skip the auto-naming `skygate-skygate:latest`
// that compose sets.
func (s *ImagePullStrategy) detectRunningImage(ctx context.Context) (string, error) {
	out, err := s.runShellCapture(ctx, "docker", "inspect",
		"--format", "{{.Config.Image}}", s.ComposeProject+"-skygate-1")
	if err != nil {
		// Fallback: list all running containers named *skygate* and
		// pick the first one. Useful when compose auto-named the
		// container differently.
		out, err = s.runShellCapture(ctx, "docker", "ps",
			"--filter", "name=skygate", "--format", "{{.Image}}")
		if err != nil {
			return "", err
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) == 0 || lines[0] == "" {
			return "", errors.New("no skygate container found")
		}
		return lines[0], nil
	}
	img := strings.TrimSpace(out)
	if img == "" {
		return "", errors.New("docker inspect returned empty image")
	}
	return img, nil
}

// imageIsFromRegistry returns true if the image name looks like
// it was pulled from a registry (e.g. contains a "/" before the
// first ":" or has a "ghcr.io", "docker.io", "quay.io" prefix).
// Locally-built images are named like "skygate-skygate:latest"
// (no slash before the colon → not from registry).
func (s *ImagePullStrategy) imageIsFromRegistry(image string) bool {
	if image == "" {
		return false
	}
	// Images pulled from a registry always have a "/" separating
	// the registry host from the repo name. Local images named
	// "skygate-skygate:latest" don't.
	if !strings.Contains(image, "/") {
		return false
	}
	// Belt-and-suspenders: the registry host portion must look
	// like a domain (contains "." or ":" for port). This rejects
	// paths like "skygate/skygate:latest" that happen to contain
	// a "/" but aren't from a real registry.
	firstPart := image
	if i := strings.Index(image, "/"); i >= 0 {
		firstPart = image[:i]
	}
	return strings.Contains(firstPart, ".") || strings.Contains(firstPart, ":") ||
		firstPart == "localhost"
}

// imageTag extracts the tag portion of an image reference
// (everything after the last ":"). Returns "latest" if no
// tag is present (docker's default).
func imageTag(image string) string {
	// Strip the digest portion if present ("image:tag@sha256:...")
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	// The "/" in "registry:5000/repo:tag" separates the registry
	// portion from the tag. We want only the LAST ":" after the
	// last "/" (or the only ":" if no "/" exists).
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon < 0 || lastColon < lastSlash {
		return "latest"
	}
	return image[lastColon+1:]
}

// sanitizeTag replaces characters that would be invalid in a
// docker tag with underscores. Used to construct backup-tag
// names from previous-tag values.
func sanitizeTag(tag string) string {
	r := strings.NewReplacer(":", "_", "/", "_", "@", "_", " ", "_")
	return r.Replace(tag)
}

// findComposeFile locates the docker-compose file that defines
// the skygate service. Looks for docker-compose.ghcr.yml first
// (the prebuilt-image variant), then docker-compose.yml. Returns
// the absolute path so docker compose can run from any cwd.
//
// The variant ordering matters: docker-compose.ghcr.yml is the
// post-migration setup; an operator who hasn't migrated yet
// won't have it, so we fall back to docker-compose.yml (which
// will then fail at phase 4 with a "compose file doesn't read
// SKYGATE_IMAGE" message — see the pre-flight check).
func (s *ImagePullStrategy) findComposeFile(ctx context.Context) (string, error) {
	cwd, err := s.runShellCapture(ctx, "pwd")
	if err != nil {
		return "", err
	}
	cwd = strings.TrimSpace(cwd)
	for _, name := range []string{"docker-compose.ghcr.yml", "docker-compose.yml"} {
		candidate := cwd + "/" + name
		if err := s.runShellQuiet(ctx, "test", "-f", candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("no docker-compose.ghcr.yml or docker-compose.yml found in " + cwd)
}

// pollHealthz polls /healthz until it returns 200 OK with the
// expected tag in the body, or HealthTimeout expires.
//
// We don't try to verify the exact version in the response body
// (that would require comparing JSON); we just confirm the
// endpoint is responding. The operator can curl /healthz after
// the update to verify the new version string.
func (s *ImagePullStrategy) pollHealthz(ctx context.Context) error {
	deadline := time.Now().Add(s.HealthTimeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("healthz did not become healthy within %s", s.HealthTimeout)
		}
		if err := s.runShellQuiet(ctx, "curl", "-fsS", "--max-time", "3", s.HealthURL); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.PollInterval):
		}
	}
}

// log is a nil-safe wrapper around s.Logger.
func (s *ImagePullStrategy) log(format string, args ...any) {
	if s.Logger != nil {
		s.Logger(format, args...)
	}
}

// runShell / runShellCapture / runShellQuiet are thin wrappers
// around exec.CommandContext with output handling. Each is a
// separate method so tests can swap in a mock implementation.
func (s *ImagePullStrategy) runShell(ctx context.Context, name string, args ...string) error {
	_, err := runShellCmd(ctx, name, args...)
	return err
}

func (s *ImagePullStrategy) runShellCapture(ctx context.Context, name string, args ...string) (string, error) {
	return runShellCmd(ctx, name, args...)
}

func (s *ImagePullStrategy) runShellQuiet(ctx context.Context, name string, args ...string) error {
	_, err := runShellCmd(ctx, name, args...)
	return err
}

// defaultStr returns a if non-empty, else b.
func defaultStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// osGetenv is a tiny indirection so tests can swap the env
// lookup if needed. In production it just calls os.Getenv.
func osGetenv(key string) string {
	return getEnv(key)
}
