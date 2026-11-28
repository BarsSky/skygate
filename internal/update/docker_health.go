// docker_health.go — B532 (2026-11-27): the health poller and its body parsers, split out of docker.go.
//
// docker.go was 1 515 lines: the Docker upgrader's job runner AND its transport, its shell
// wrappers, its health poller, its ownership helpers and three embedded /bin/sh scripts.
// The audit's complaint was that a reader cannot find what a 1 500-line file does; each
// section keeps its code VERBATIM and its own doc comments, and the package is unchanged —
// no identifier moved, so no caller and no contract had to learn a new name.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// pollHealthz polls the post-deploy skygate /healthz endpoint until the
// build that answers is the build the job asked for (or the timeout fires).
//
// B393: the `expectedBuild` parameter is new and is the whole point. Before
// it, the poll accepted any 200 with `"status":"ok"` — which the OLD
// container answers perfectly well, so an update whose swap never ran was
// verified as successful. Two things are now required:
//
//   - the label's version part equals the job target (BuildSatisfiesTarget),
//   - the body is not a still-booting process: /healthz carries
//     `"ready":false` while a boot phase is unfinished (B269), and a
//     booting process happens to serve the NEW build string, so it must not
//     be mistaken for a finished one.
//
// 2026-07-30 (v0.34 cleanup): the v0.29.0
// `ensureComposeServiceRunning` helper that used to gate
// this poll on `docker ps` state is gone — the v0.29.2
// `container_name:` fix made the race a non-issue. /healthz
// alone is the canonical liveness signal.
func (u *DockerUpgrader) pollHealthz(ctx context.Context, timeout time.Duration, expectedBuild string) error {
	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		attempt++
		if time.Now().After(deadline) {
			return fmt.Errorf("healthz did not report the expected build %q within %s (%d attempts)", expectedBuild, timeout, attempt-1)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Use Go's net/http instead of curl. The
		// skygate container's base image is alpine
		// (v0.32.13+), which doesn't ship curl —
		// exec.CommandContext("curl", ...) silently
		// fails with "executable file not found",
		// which the orchestrator was mistaking for
		// "container not yet healthy" (timed out
		// after 60s, then triggered auto-rollback
		// even though the new container was actually
		// fine). Using net/http here is the same
		// approach v0.32.22 took for the HTTP probes
		// elsewhere in the codebase — no shell
		// dependency, no path surprises across
		// host/container rebuilds.
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:8080/healthz", nil)
		resp, httpErr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if httpErr == nil {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body := string(bodyBytes)
			build := healthzBuild(body)
			switch {
			case resp.StatusCode != 200:
				u.State.Log(LogDebug, fmt.Sprintf("healthz status %d (attempt %d): %s", resp.StatusCode, attempt, body))
			case !strings.Contains(body, `"status":"ok"`):
				u.State.Log(LogDebug, fmt.Sprintf("healthz not ok yet (attempt %d): %s", attempt, body))
			case healthzBootInProgress(body):
				u.State.Log(LogInfo, fmt.Sprintf("healthz 200 but the process is still booting (attempt %d)", attempt))
			case !BuildSatisfiesTarget(build, expectedBuild, u.CurrentVersion):
				u.State.Log(LogInfo, fmt.Sprintf("healthz 200 from build %q, the job targets %q — the swap has not happened yet (attempt %d)", build, expectedBuild, attempt))
			default:
				u.State.Log(LogInfo, fmt.Sprintf("healthz 200 with the expected build %s (attempt %d)", build, attempt))
				return nil
			}
		} else {
			// Connection refused / timeout. Expected
			// during the recreate window. Log only on
			// first failure + every 10th to avoid log
			// spam.
			if attempt == 1 || attempt%10 == 0 {
				u.State.Log(LogDebug, fmt.Sprintf("healthz not ready (attempt %d): %s", attempt, httpErr))
			}
		}
		// Wait before retrying. 5s is short enough that
		// the operator sees frequent updates, long
		// enough that we don't hammer the server.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// healthzBuild extracts the `build` field from a /healthz body. Returns ""
// when the field is absent or the body is not JSON — the verdict treats an
// empty build as "no proof", never as a match.
func healthzBuild(body string) string {
	var probe struct {
		Build string `json:"build"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return ""
	}
	return probe.Build
}

// healthzBootInProgress reports whether the body is the provisional /healthz
// of a process that has not finished booting yet (B269 ships `ready:false`
// plus the current phase). A booting process serves the NEW build string, so
// without this check it would be accepted as a completed swap.
func healthzBootInProgress(body string) bool {
	var probe struct {
		Ready *bool  `json:"ready"`
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return false
	}
	if probe.Ready != nil && !*probe.Ready {
		return true
	}
	return probe.Phase != ""
}
