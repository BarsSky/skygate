// Headscale ACL policy operations: get + set.
//
// The API path works in `policy.mode: database` deployments. For a
// `file`-mode headscale the API answers "update is disabled for modes other
// than database" (500) and we write the policy file + reload headscale
// instead — on a containerised install through the config volume, on a
// native/systemd install straight to disk (B272).
package headscale

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ACLPolicy is the /api/v1/policy response. headscale 0.29.x
// populates `policy` as a NESTED OBJECT (the parsed HuJSON),
// not a stringified blob. Older headscale versions used a
// string. We honour both in GetACL — the nested-object case
// is re-marshalled to a string before caching.
type ACLPolicy struct {
	Policy json.RawMessage `json:"policy"`
	Data   json.RawMessage `json:"data"`
}

// PolicyBody is the request/response for headscale policy API.
type PolicyBody struct {
	Policy string `json:"policy"`
}

// GetACL returns the headscale ACL policy. Falls back to docker exec CLI.
// Result is cached for cacheTTL - the policy rarely changes during a session.
func (c *Client) GetACL() (string, error) {
	c.cacheMu.RLock()
	if c.cacheACL != "" && time.Since(c.cacheACLAt) < c.cacheTTL {
		out := c.cacheACL
		c.cacheMu.RUnlock()
		return out, nil
	}
	c.cacheMu.RUnlock()

	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cacheACL != "" && time.Since(c.cacheACLAt) < c.cacheTTL {
		return c.cacheACL, nil
	}

	var p ACLPolicy
	err := c.do("GET", "/api/v1/policy", nil, &p)
	if err != nil && isTransientReadError(err) {
		// B295 (2026-09-23): retry a TRANSIENT failure before falling back.
		//
		// WHY: on a `policy.mode: file` host every policy apply RESTARTS headscale
		// (0.29 re-reads the file only at startup), so a read that lands in the
		// restart window answers `connect: connection refused` although the daemon
		// is perfectly healthy one second later. Live on `aro` that turned into
		// «состояние политики неизвестно» plus «никто не объявляет: 19» on the
		// prefix page — and, worse, into an UNCONDITIONAL re-apply (see
		// exit_rules.applyACLIfDrifted; the unattended paths now go through
		// applyACLIfDriftedChurn), i.e. another write and restart: the
		// observation was feeding the outage.
		//
		// Two short retries cover the window without making a genuinely dead
		// headscale wait long: the file/CLI fallbacks still run afterwards.
		for attempt := 1; attempt <= aclReadRetries; attempt++ {
			time.Sleep(aclReadRetryDelay)
			if rerr := c.do("GET", "/api/v1/policy", nil, &p); rerr == nil {
				err = nil
				break
			} else {
				err = rerr
				if !isTransientReadError(rerr) {
					break
				}
			}
		}
	}
	if err == nil {
		// Resolve which field wins: Policy (current shape,
		// object or stringified) takes precedence over Data
		// (legacy stringified hujson blob). Empty values
		// (including the JSON literal `""` of length 2)
		// are skipped — we fall through to the other field.
		if raw := bytes.TrimSpace(p.Policy); isNonEmptyPolicyField(raw) {
			if raw[0] == '{' || raw[0] == '[' {
				// headscale 0.29.x returns the
				// policy as a JSON OBJECT (the
				// parsed HuJSON), not a stringified
				// blob. Store as-is so we don't
				// double-encode on the PUT side.
				// EnsureTagOwner does its own
				// json.Marshal of the parsed map.
				c.cacheACL = string(raw)
				c.cacheACLAt = time.Now()
				return c.cacheACL, nil
			}
			// B282 (2026-09-22): the `policy` field can ALSO be a
			// JSON STRING literal — `{"policy":"{…escaped…}"}` —
			// which is what the live native `aro` host answers.
			// Pre-B282 this branch stored the QUOTED text in the
			// cache, so every consumer that json.Unmarshal'd it
			// failed: /admin/headscale/acl answered
			// `unmarshal policy: json: cannot unmarshal string into
			// Go value of type admin.ACLView`, and the
			// exit_rules.all_in_headscale_acl system test died the
			// same way. Unquote here (the same helper the legacy
			// `data` field below and the B251 tag path use) so the
			// cache always holds the policy DOCUMENT.
			if unquoted, uerr := unquotePolicyIfStringified(raw); uerr == nil {
				c.cacheACL = string(unquoted)
				c.cacheACLAt = time.Now()
				return c.cacheACL, nil
			}
			s := strings.TrimSpace(string(raw))
			c.cacheACL = s
			c.cacheACLAt = time.Now()
			return s, nil
		}
		if raw := bytes.TrimSpace(p.Data); isNonEmptyPolicyField(raw) {
			// The legacy `data` field carries a
			// JSON-stringified policy blob (the value
			// is wrapped in `"…"`). Unquote it before
			// caching so the rest of the pipeline
			// (EnsureTagOwner, SetPolicy) sees the
			// raw policy document.
			s := string(raw)
			if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
				var unq string
				if err := json.Unmarshal([]byte(s), &unq); err == nil {
					s = unq
				}
			}
			c.cacheACL = s
			c.cacheACLAt = time.Now()
			return s, nil
		}
	}
	// B294: the API is unreachable or unusable. Walk the two privilege-free-ish
	// rungs before giving up — the policy FILE headscale serves in `file` mode,
	// then the CLI through the install-kind ladder (docker exec OR the local
	// `headscale` binary). Pre-B294 the tail here was a docker-only `exec.Command`
	// that bailed out immediately when no container was configured, and reported
	// "cli: all variants failed" about a CLI it never ran.
	fileReason := ""
	if body, reason := c.readPolicyFileAsACL(); body != "" {
		return body, nil
	} else {
		fileReason = reason
	}
	cliReason := ""
	if body, reason := c.readPolicyViaCLIAsACL(); body != "" {
		return body, nil
	} else {
		cliReason = reason
	}
	hint := aclReadHint(c.BaseURL, err)
	msg := fmt.Sprintf("api: %v; policy file: %s; %s", err, fileReason, cliReason)
	if hint != "" {
		msg += " — " + hint
	}
	return "", errors.New(msg)
}

// readPolicyFileAsACL reads headscale's policy straight from the FILE it serves
// when `policy.mode: file` (B294).
//
// WHY this rung exists: the live-policy READ used to have exactly two paths — the
// API and a docker-only `headscale policy get`. On a native install whose API is
// unreachable (live `aro`: `Get "http://127.0.0.1:8081/api/v1/policy": dial tcp
// 127.0.0.1:8081: connect: connection refused`) the reader answered
// `api: …; cli: all variants failed` — a sentence blaming a CLI that was never
// tried, because neither docker nor a container exists there. With no way to read
// the live policy, `/admin/exit-nodes` reported «состояние политики неизвестно» and
// the prefix table could never converge: the comparison that drives the resync had
// nothing to compare against. Returns (policy, reason-it-failed).
func (c *Client) readPolicyFileAsACL() (string, string) {
	path := strings.TrimSpace(c.PolicyPath)
	if path == "" {
		if p, err := DiscoverPolicyPath(); err == nil && p != "" {
			path = p
			c.PolicyPath = p
		}
	}
	if path == "" {
		return "", "no policy file path (set SKYGATE_HEADSCALE_POLICY_PATH, or let skygate find headscale's config.yaml)"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Sprintf("read %s: %v", path, err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return "", fmt.Sprintf("%s is empty", path)
	}
	c.cacheACL = body
	c.cacheACLAt = time.Now()
	return body, ""
}

// readPolicyViaCLIAsACL reads the policy through the headscale CLI, using the same
// install-kind ladder as every other CLI call (B267): `docker exec <container>
// headscale …` when docker and a container name are available, the local
// `headscale` binary otherwise — including the variants older releases needed.
// Returns (policy, reason-it-failed).
func (c *Client) readPolicyViaCLIAsACL() (string, string) {
	var attempts []string
	for _, args := range [][]string{{"policy", "get"}, {"policy", "show"}, {"policy"}} {
		out, err := c.runHeadscaleCLI(args...)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%v: %v", args, err))
			continue
		}
		if body := strings.TrimSpace(string(out)); body != "" {
			c.cacheACL = body
			c.cacheACLAt = time.Now()
			return body, ""
		}
		attempts = append(attempts, fmt.Sprintf("%v: empty output", args))
	}
	return "", "headscale CLI: " + strings.Join(attempts, "; ")
}

// ACLReadHintFor returns the actionable advice for a failed READ of the headscale
// API (B294): the page renders it next to the error so the operator knows which
// knob to check. Returns "" for errors that are not reachability problems.
func ACLReadHintFor(c *Client, err error) string {
	base := ""
	if c != nil {
		base = c.BaseURL
	}
	return aclReadHint(base, err)
}

// aclReadRetries / aclReadRetryDelay bound the retry of a transient read failure
// (B295). Package-level so tests can shorten them to zero.
var (
	aclReadRetries    = 2
	aclReadRetryDelay = 1200 * time.Millisecond
)

// isTransientReadError reports whether a failed policy read is worth retrying:
// the daemon is restarting, not misconfigured. Uses the same spellings as
// aclReadHint (POSIX + Windows).
func isTransientReadError(err error) bool {
	if err == nil {
		return false
	}
	// A real HTTP answer (401/500/404) means the daemon is UP — retrying cannot
	// help, and the caller's file/CLI fallbacks are the interesting part.
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return false
	}
	msg := err.Error()
	for _, needle := range []string{
		"connection refused",
		"actively refused",
		"no connection could be made",
		"connectex",
		"i/o timeout",
		"connection reset by peer",
		"connection timed out",
		"deadline exceeded",
		"timeout", // a hang then success is exactly the restart/GC window class
		"EOF",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// aclReadHint turns an unreachable-API error into the operator's next step.
//
// B294: this is a CONFIGURATION class, not a policy class — the address skygate
// talks to must be reachable from the skygate PROCESS (a container sees its own
// loopback, not the host's, so `127.0.0.1:<port>` is the classic wrong answer
// there). Returns "" when the error is not a reachability failure.
func aclReadHint(baseURL string, err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	unreachable := false
	// Both platform spellings: POSIX says "connection refused"/"no such host",
	// Windows says "No connection could be made because the target machine
	// actively refused it" / "connectex". A hint that only fires on Linux would
	// silently vanish in a developer's Windows run.
	for _, needle := range []string{
		"connection refused",
		"actively refused",
		"no connection could be made",
		"connectex",
		"no such host",
		"i/o timeout",
		"network is unreachable",
		"connection timed out",
	} {
		if strings.Contains(msg, needle) {
			unreachable = true
			break
		}
	}
	if !unreachable {
		return ""
	}
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("headscale's API at %s is not reachable from the skygate process — check HEADSCALE_URL (inside a container this must be the headscale SERVICE NAME or its address on the shared network, never 127.0.0.1) "+
		"and the address headscale itself listens on (`listen_addr` in its config.yaml); verify with: curl -sf -H \"Authorization: Bearer $HEADSCALE_API_KEY\" %s/api/v1/node >/dev/null && echo api-ok",
		base, base)
}

// SetPolicy sets the ACL policy.
// Tries REST API first (database mode), then file-mode fallback:
// write ACL to config volume + update config.yaml + restart headscale.
//
// 2026-07-13: the file-mode fallback is now strictly gated on http.StatusNotFound/http.StatusMethodNotAllowed
// from the headscale API. A 5xx (or any non-2xx other than http.StatusNotFound/http.StatusMethodNotAllowed) is
// treated as a real failure and returned to the caller. The previous
// "any error → fallback" heuristic silently masked transient headscale
// failures (e.g. policy rejected mid-restart) by writing to
// acl_policy.hujson while the running headscale was still using the
// database policy — the operator saw "ok" in the audit log while the
// new rules never reached Tailscale clients. http.StatusNotFound/http.StatusMethodNotAllowed are the
// documented headscale signals for "policy endpoint not available in
// this mode" and the only codes the fallback should react to.
func (c *Client) SetPolicy(policy string) error {
	// B283 (2026-09-22): never hand headscale (or its policy FILE) a document
	// it cannot parse. On a `policy.mode: file` host a malformed write is not
	// a rejected API call — it is a file that headscale reads at startup, so
	// the daemon crash-loops and the whole tailnet loses its control plane
	// (live: `hujson: line 302, column 23: invalid character ']' after object
	// name`, restart counter 248, all devices gone from the portal).
	// Parse first: a bad document is OUR bug and must be reported as one,
	// while the previous policy stays in force.
	if _, perr := PolicyJSON(policy); perr != nil {
		return fmt.Errorf("refusing to set a policy that does not parse: %w", perr)
	}
	// B283: and never a policy headscale will REFUSE TO START ON. A grant that
	// names a tag missing from tagOwners makes the whole document invalid for
	// headscale's parser ("tag not found"), which on a file-mode host means a
	// crash-looping daemon, not a rejected request — see policy_validate.go for
	// the live incident.
	if undeclared, uerr := UndeclaredTags(policy); uerr != nil {
		return fmt.Errorf("refusing to set a policy that does not parse: %w", uerr)
	} else if len(undeclared) > 0 {
		return fmt.Errorf("refusing to set a policy that references %d tag(s) missing from tagOwners: %s — headscale rejects such a document and will not start on it",
			len(undeclared), strings.Join(undeclared, ", "))
	}
	var out PolicyBody
	err := c.do("PUT", "/api/v1/policy", PolicyBody{Policy: policy}, &out)
	if err == nil {
		c.clearACLCache()
		return nil
	}

	if !isFileModePolicyError(err) {
		return err
	}

	return c.setPolicyViaFile(policy, err)
}

// isFileModePolicyError reports whether a failed PUT /api/v1/policy means
// "this headscale keeps its policy in a FILE and refuses API writes" rather
// than a transient failure (B272).
//
// headscale versions disagree on how they say it:
//
//   - 404 / 405 — the endpoint does not exist in this mode (the only codes
//     the pre-B272 code accepted);
//   - 500 with `update is disabled for modes other than database`
//     (code 2 = gRPC ErrInternal) — headscale 0.29.2, verified live on a
//     native install whose config.yaml says `policy.mode: file`. The old
//     check treated this as a real failure, so the file fallback never ran
//     and every EnsureTagOwner died with the 500 while the operator saw
//     only "are invalid or not permitted" from AddTag.
func isFileModePolicyError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	}
	body := strings.ToLower(apiErr.Body)
	// Only a 5xx whose body names the policy mode counts — a 500 from a
	// broken policy (parse error) must stay a real failure.
	if apiErr.StatusCode < 500 {
		return false
	}
	return strings.Contains(body, "update is disabled") ||
		strings.Contains(body, "policy mode") ||
		strings.Contains(body, "modes other than database")
}

// setPolicyViaFile writes the policy to headscale's policy FILE and reloads
// headscale (B272). Two install kinds:
//
//   - CONTAINERISED: `docker run -i --rm -v <policyVolume> alpine sh -c 'cat >
//     /config/<file>'` then `docker restart <container>` (the historical path,
//     with the volume now configurable instead of hardcoded);
//   - NATIVE (systemd / bare binary): write the resolved PolicyPath directly,
//     then restart the unit (SIGHUP is not enough — headscale re-reads the
//     policy file only at startup in 0.29).
//
// When the path is unknown the error says exactly what to set, instead of
// silently doing nothing.
func (c *Client) setPolicyViaFile(policy string, apiErr error) error {
	path := c.PolicyPath
	if path == "" {
		if p, err := DiscoverPolicyPath(); err == nil && p != "" {
			path = p
			c.PolicyPath = p
		}
	}
	if path == "" {
		return fmt.Errorf("api: %w; headscale keeps its policy in a FILE (policy.mode: file) and the policy path is unknown — set SKYGATE_HEADSCALE_POLICY_PATH to the file named by `policy.path` in headscale's config.yaml (e.g. /etc/headscale/policy.hujson) and restart skygate", apiErr)
	}

	if !c.forceNativePolicyWrite {
		if _, err := exec.LookPath("docker"); err == nil {
			return c.setPolicyViaFileDocker(policy, path, apiErr)
		}
	}
	return c.setPolicyViaFileNative(policy, path, apiErr)
}

// setPolicyViaFileDocker writes through the headscale config volume.
func (c *Client) setPolicyViaFileDocker(policy, path string, apiErr error) error {
	// Inside the helper container the policy file is addressed by its
	// container-side path; the volume prefix maps host dir → container dir.
	containerPath := filepath.Base(path)
	mount := c.policyVolume
	if mount == "" {
		mount = "/home/admin/headscale/config:/config"
	}
	writeCmd := exec.Command("docker", "run", "-i", "--rm",
		"-v", mount,
		"alpine", "sh", "-c", "cat > /config/"+containerPath)
	writeCmd.Stdin = strings.NewReader(policy)
	if out, cerr := writeCmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("api: %w; write policy file %s via docker volume %s: %v (%s)", apiErr, path, mount, cerr, strings.TrimSpace(string(out)))
	}
	if out, e := exec.Command("docker", "restart", c.containerOr("headscale")).CombinedOutput(); e != nil {
		return fmt.Errorf("api: %w; wrote %s but restarting headscale failed: %v (%s)", apiErr, path, e, strings.TrimSpace(string(out)))
	}
	c.clearACLCache()
	return nil
}

// setPolicyViaFileNative writes the policy file on the local filesystem and
// restarts the headscale unit.
//
// B272.3 — order of attempts, cheapest and least privileged first:
//
//  1. direct write (works when skygate is allowed to write the file);
//  2. the root-owned helper (`/usr/local/lib/skygate/skygate-apply-policy.sh`,
//     driven by `skygate-policy.path`). This is the supported path on a native
//     install: the skygate unit runs with ProtectSystem=strict and
//     ReadWritePaths=${data_dir} ${etc_dir}, so /etc/headscale is read-only for
//     it BY DESIGN — a live host answered "read-only file system" for
//     /etc/headscale/policy.hujson.skygate.tmp even after the file itself was
//     made group-writable. Widening the unit's mount namespace was rejected in
//     favour of the existing helper pattern (B261): the unprivileged service
//     drops a DATA-ONLY request and root does the privileged step.
//
// The error text names the exact refusal and the alternative (apply the policy
// by hand), because this is the last link of a chain that otherwise looks like
// "tags silently do not apply".
func (c *Client) setPolicyViaFileNative(policy, path string, apiErr error) error {
	prev, readErr := os.ReadFile(path)
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	// B283 (2026-09-22): a file-mode write is not just a write — the applier
	// restarts headscale to make it re-read the file, so a redundant write
	// costs a control-plane restart. Live on `aro` the policy was rewritten
	// every ~5 minutes with 7100/7109/7126/7135-byte variants of the SAME
	// policy (the tag path re-marshals the document, the B276 drift check then
	// regenerates it, and so on), restarting headscale each time — and every
	// extra write is another chance to catch the applier mid-request.
	// Compare semantically (comments/indentation/order do not matter) and do
	// nothing when the document already says the same thing.
	if readErr == nil && len(bytes.TrimSpace(prev)) > 0 {
		if same, cmpErr := PolicyEquivalent(string(prev), policy); cmpErr == nil && same {
			log.Printf("policy: %s already describes this policy (semantically equal) — not rewriting, headscale is not restarted", path)
			c.clearACLCache()
			return nil
		}
	}
	tmp := path + ".skygate.tmp"
	var directErr error
	if err := os.WriteFile(tmp, []byte(policy), mode); err != nil {
		directErr = err
	} else if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		directErr = err
	} else if err := c.restartHeadscaleUnit(); err != nil {
		// Roll back: a half-applied policy is worse than none, and the
		// operator needs the previous rules back.
		if readErr == nil {
			_ = os.WriteFile(path, prev, mode)
		}
		directErr = fmt.Errorf("wrote %s but restarting %s failed: %v", path, c.headscaleUnit, err)
	}
	if directErr == nil {
		c.clearACLCache()
		return nil
	}

	// Direct write refused — ask the privileged helper to do it.
	if helperErr := RequestPolicyApply(path, policy); helperErr == nil {
		log.Printf("policy: %s is not writable by the skygate service user (%v) — the policy was handed to the privileged helper; headscale will re-read it within ~30s", path, directErr)
		c.clearACLCache()
		return nil
	} else if !errors.Is(helperErr, ErrPolicyHelperUnavailable) {
		return fmt.Errorf("api: %w; direct write of %s failed (%v); privileged helper failed too: %v — apply this policy by hand: %s", apiErr, path, directErr, helperErr, oneLinePolicy(policy))
	}

	return fmt.Errorf("api: %w; write policy file %s: %v (the skygate service user cannot write it — the unit runs with ProtectSystem=strict + ReadWritePaths=${data_dir} ${etc_dir}, so /etc/headscale is read-only for it; install the privileged policy helper (deploy/skygate-apply-policy.sh + the skygate-policy.path/.service units, see docs/troubleshooting.md 8.0.4) or apply this policy by hand: %s)", apiErr, path, directErr, oneLinePolicy(policy))
}

// restartHeadscaleUnit restarts the local headscale service (native install).
func (c *Client) restartHeadscaleUnit() error {
	unit := c.headscaleUnit
	if unit == "" {
		unit = "headscale"
	}
	// Look up the binaries (not just systemctl) so the same code works on a
	// host with systemd, on OpenRC, and under a test shim on PATH.
	if _, err := exec.LookPath("systemctl"); err == nil {
		out, err := exec.Command("systemctl", "restart", unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl restart %s: %v (%s)", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// OpenRC / bare binary host: rc-service is the Alpine equivalent.
	if _, err := exec.LookPath("rc-service"); err == nil {
		out, rcErr := exec.Command("rc-service", unit, "restart").CombinedOutput()
		if rcErr != nil {
			return fmt.Errorf("rc-service %s restart: %v (%s)", unit, rcErr, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("neither systemctl nor rc-service is available to restart %q (start headscale manually so it re-reads the policy file)", unit)
}

// PolicyAuditStatus is the admin-facing verdict of the file-mode policy check
// (B272.2).
type PolicyAuditStatus string

const (
	// PolicyAuditNotApplicable — the API works (policy.mode: database), or no
	// policy file is involved at all.
	PolicyAuditNotApplicable PolicyAuditStatus = "not_applicable"
	// PolicyAuditOK — the file exists and both sides can do their job.
	PolicyAuditOK PolicyAuditStatus = "ok"
	// PolicyAuditUnreadableByHeadscale — skygate can reach the file, but the
	// headscale service user cannot: its API answers 500 for every policy
	// call, so no tag can ever become permitted. The live case (host `aro`).
	PolicyAuditUnreadableByHeadscale PolicyAuditStatus = "unreadable_by_headscale"
	// PolicyAuditUnreadableBySkygate — the file is not readable by the skygate
	// service user, so it cannot compute the new tagOwners.
	PolicyAuditUnreadableBySkygate PolicyAuditStatus = "unreadable_by_skygate"
	// PolicyAuditMissing — the path is configured but no such file exists.
	PolicyAuditMissing PolicyAuditStatus = "missing"
	// PolicyAuditAPIError — the policy API itself failed while skygate could
	// read the file (e.g. headscale is down, or its own error is unrelated to
	// permissions).
	PolicyAuditAPIError PolicyAuditStatus = "api_error"
)

// PolicyAudit is the operator-facing result of auditing the headscale policy
// file (B272.2). It exists because the failure is otherwise a nested one:
//
//	PUT /api/v1/policy → 500 update is disabled for modes other than database
//	GET /api/v1/policy → 500 reading policy from path "/etc/headscale/policy.hujson":
//	                          open …: permission denied
//
// i.e. headscale cannot read its OWN policy file, so nothing (its API, skygate's
// tagOwners repair, any node tag) can work — and the only clue was a 500 body.
type PolicyAudit struct {
	Status PolicyAuditStatus `json:"status"`
	// Path is the resolved policy file ("" when the mode is database).
	Path string `json:"path,omitempty"`
	// Detail is the one-line explanation shown to the operator.
	Detail string `json:"detail"`
	// CurrentUserReadable / CurrentUserWritable describe skygate's own access.
	CurrentUserReadable bool `json:"current_user_readable"`
	CurrentUserWritable bool `json:"current_user_writable"`
	// HeadscaleUser / HeadscaleGroup are the unit's service identity (empty
	// when it could not be determined).
	HeadscaleUser  string `json:"headscale_user,omitempty"`
	HeadscaleGroup string `json:"headscale_group,omitempty"`
	// HeadscaleCanRead is the verdict for the headscale service user: a real
	// probe when sudo is available, otherwise a conservative model based on
	// the file's owner/group/mode.
	HeadscaleCanRead bool `json:"headscale_can_read"`
	// ProbeMethod is "sudo" (an actual `sudo -u <user> test -r`) or "mode"
	// (derived from owner/group/other bits).
	ProbeMethod string `json:"probe_method,omitempty"`
	// Fixes are ready-to-paste shell commands that resolve the problem, in
	// preference order. Empty when Status is ok / not_applicable.
	Fixes []string `json:"fixes,omitempty"`
}

// PolicyAuditOK reports whether the policy is usable end to end.
func (a PolicyAudit) OK() bool {
	return a.Status == PolicyAuditOK || a.Status == PolicyAuditNotApplicable
}

// AuditHeadscalePolicy audits the file-mode policy's accessibility for both
// parties: the skygate service user (which must write tagOwners) and the
// headscale service user (which must read the file at every API call).
//
// It is deliberately read-only: no chown, no chmod. The operator gets the exact
// commands instead (Fixes), because guessing the headscale service account and
// changing ownership of another service's config from inside skygate is a
// privilege decision, not a bug fix.
func AuditHeadscalePolicy() PolicyAudit {
	a := PolicyAudit{Status: PolicyAuditNotApplicable, HeadscaleCanRead: true}

	// Is this a file-mode policy at all?
	path := os.Getenv("SKYGATE_HEADSCALE_POLICY_PATH")
	if path == "" {
		if p, err := DiscoverPolicyPath(); err == nil {
			path = p
		} else {
			// no file-mode policy discovered — the API path is in charge
			a.Detail = "headscale policy is served through the API (policy.mode is not 'file'), so file permissions do not apply"
			return a
		}
	}
	a.Path = path

	user, group := currentServiceIdentity()
	a.HeadscaleUser, a.HeadscaleGroup = headscaleServiceIdentity()

	// Ask the OS directly whether we can write: the kernel is the authority
	// here (it already accounts for the real uid/gid), unlike a mode-bit model
	// built from environment variables.
	if unixAccess(path, 0x2) { // W_OK
		a.CurrentUserWritable = true
	}

	// skygate's own view.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			a.Status = PolicyAuditMissing
			a.Detail = fmt.Sprintf("policy file %s does not exist (headscale's policy.path points at it)", path)
			a.Fixes = []string{
				fmt.Sprintf("sudo install -m 0640 -o %s -g %s /dev/null %s", orDefault(a.HeadscaleUser, "headscale"), user, path),
				"# then write the policy (headscale policy get > file, or let skygate apply it once readable)",
			}
			return a
		}
		a.Status = PolicyAuditUnreadableBySkygate
		a.Detail = fmt.Sprintf("cannot stat %s: %v", path, err)
		return a
	}
	if f, err := os.Open(path); err == nil {
		a.CurrentUserReadable = true
		_ = f.Close()
	} else {
		a.Detail = fmt.Sprintf("skygate cannot read %s: %v", path, err)
	}
	if !a.CurrentUserWritable {
		a.CurrentUserWritable = unixWritable(path)
	}

	// headscale's view: prefer a real probe, fall back to a mode model.
	if a.HeadscaleUser != "" && canSudoRead(path, a.HeadscaleUser) {
		a.HeadscaleCanRead = true
		a.ProbeMethod = "sudo"
	} else {
		a.HeadscaleCanRead = modeModelAllows(path, a.HeadscaleUser, a.HeadscaleGroup, user, group)
		a.ProbeMethod = "mode"
	}

	switch {
	case !a.HeadscaleCanRead:
		a.Status = PolicyAuditUnreadableByHeadscale
		a.Detail = fmt.Sprintf("the headscale service user (%s) cannot read %s — its policy API answers 500 and NO node tag can ever be permitted",
			orDefault(a.HeadscaleUser, "headscale"), path)
		a.Fixes = policyPermissionFixes(path, a.HeadscaleUser, a.HeadscaleGroup, user, group, false)
	case !a.CurrentUserWritable:
		a.Status = PolicyAuditOK
		a.Detail = fmt.Sprintf("headscale can read %s, but the skygate service user cannot write it — skygate will report the exact policy to apply by hand", path)
		a.Fixes = policyPermissionFixes(path, a.HeadscaleUser, a.HeadscaleGroup, user, group, true)
	default:
		a.Status = PolicyAuditOK
		a.Detail = fmt.Sprintf("%s is readable by headscale and writable by the skygate service user", path)
	}
	return a
}

// policyPermissionFixes builds the copy-paste commands for a permission problem.
// With writableByCurrent=false the goal is headscale readability; with true it
// is preserving that readability while granting skygate write access.
func policyPermissionFixes(path, hsUser, hsGroup, curUser, curGroup string, writableByCurrent bool) []string {
	owner := orDefault(hsUser, "headscale")
	group := orDefault(hsGroup, owner)
	if curGroup == "" {
		curGroup = group
	}
	if !writableByCurrent {
		return []string{
			"# owner = headscale (it must READ the policy), group = skygate (it must WRITE it)",
			fmt.Sprintf("sudo chown %s:%s %s", owner, curGroup, path),
			fmt.Sprintf("sudo chmod 0640 %s", path),
			"sudo systemctl restart headscale",
			fmt.Sprintf("# verify: sudo -u %s test -r %s && sudo -u %s test -w %s", owner, path, orDefault(curUser, "skygate"), path),
		}
	}
	return []string{
		"# keep headscale able to read it, let skygate write it too",
		fmt.Sprintf("sudo chmod 0660 %s && sudo chgrp %s %s", path, orDefault(curGroup, "skygate"), path),
		fmt.Sprintf("# or, if the group differs: sudo setfacl -m u:%s:rw %s", orDefault(curUser, "skygate"), path),
		fmt.Sprintf("# verify: sudo -u %s test -w %s", orDefault(curUser, "skygate"), path),
		"# NOTE: on a native install the skygate unit runs with ProtectSystem=strict and",
		"#       ReadWritePaths=${data_dir} ${etc_dir}, so /etc/headscale is read-only for it",
		"#       BY DESIGN (live error: \"open …policy.hujson.skygate.tmp: read-only file system\").",
		"#       Either install the privileged policy helper (deploy/skygate-apply-policy.sh +",
		"#       skygate-policy.path/.service) or apply the policy by hand:",
		fmt.Sprintf("sudo headscale policy get | head -30   # what headscale currently reads from %s", path),
		"# then add the missing tagOwners entries (skygate logs the ready policy on failure)",
		fmt.Sprintf("sudo systemctl restart %s", orDefault(hsUser, "headscale")),
	}
}

// headscaleServiceIdentity returns the user/group the headscale unit runs as.
// Empty strings when it cannot be determined (no systemd, a container, …).
func headscaleServiceIdentity() (user, group string) {
	unit := getenvDefault("SKYGATE_HEADSCALE_UNIT", "headscale")
	for _, prop := range []string{"User", "Group"} {
		out, err := exec.Command("systemctl", "show", unit, "-p", prop, "--value").Output()
		v := strings.TrimSpace(string(out))
		if err == nil && v != "" {
			if prop == "User" {
				user = v
			} else {
				group = v
			}
		}
	}
	if user == "" {
		user = os.Getenv("SKYGATE_HEADSCALE_USER")
	}
	if group == "" {
		group = os.Getenv("SKYGATE_HEADSCALE_GROUP")
	}
	return user, group
}

// currentServiceIdentity is who skygate runs as (usually "skygate").
func currentServiceIdentity() (user, group string) {
	user = os.Getenv("SKYGATE_SERVICE_USER")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "skygate"
	}
	group = os.Getenv("SKYGATE_SERVICE_GROUP")
	return user, group
}

// canSudoRead reports whether `sudo -n -u <user> test -r <path>` succeeds. It
// returns false when sudo is unavailable or needs a password — the caller then
// uses the mode-based model, which is conservative (it never claims readability
// it cannot prove).
func canSudoRead(path, user string) bool {
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	return exec.Command("sudo", "-n", "-u", user, "test", "-r", path).Run() == nil
}

// unixWritable reports whether the current process can write the path, using
// the same owner/group/other bit logic as the kernel (no ACLs — those are
// reported by the operator's own `getfacl`, and a false negative here only
// downgrades a status line, never blocks anything).
func unixWritable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	perm := fi.Mode().Perm()
	if perm&0o200 != 0 { // owner write
		return true
	}
	if perm&0o020 != 0 && sameGroup(path) {
		return true
	}
	return false
}

// modeModelAllows decides whether user/group can read the file from the mode
// bits alone (used when sudo is unavailable).
func modeModelAllows(path, hsUser, hsGroup, curUser, curGroup string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	perm := fi.Mode().Perm()
	owner, group := fileOwnerGroup(path)
	if owner == "" {
		// Unknown ownership: assume the world-readable bit is what matters.
		return perm&0o004 != 0
	}
	if hsUser != "" && owner == hsUser {
		return perm&0o400 != 0
	}
	if group != "" {
		if hsGroup != "" && group == hsGroup {
			return perm&0o040 != 0
		}
		if curGroup != "" && group == curGroup {
			return perm&0o040 != 0
		}
	}
	return perm&0o004 != 0
}

// fileOwnerGroup resolves a path's numeric owner/group to names. Best-effort:
// uses `stat` when present and falls back to the numeric ids.
func fileOwnerGroup(path string) (owner, group string) {
	if out, err := exec.Command("stat", "-c", "%U %G", path).Output(); err == nil {
		parts := strings.Fields(strings.TrimSpace(string(out)))
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
	}
	if out, err := exec.Command("stat", "-f", "%Su %Sg", path).Output(); err == nil { // BSD/macOS
		parts := strings.Fields(strings.TrimSpace(string(out)))
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
	}
	return "", ""
}

// sameGroup reports whether the current process's primary group owns the file.
func sameGroup(path string) bool {
	_, fileGroup := fileOwnerGroup(path)
	if fileGroup == "" {
		return false
	}
	out, err := exec.Command("id", "-gn").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == fileGroup
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// DiscoverPolicyPath reads the local headscale configuration and returns the
// `policy.path` value (B272). It searches the documented locations and
// understands the flat YAML shapes headscale ships, including the
// `policy:\n  mode: file\n  path: /etc/headscale/policy.hujson` block.
//
// Deliberately dependency-free (no YAML library): the value is a single
// scalar and headscale's own config template always writes it as
// `path: <value>` under `policy:`.
func DiscoverPolicyPath() (string, error) {
	candidates := []string{
		os.Getenv("SKYGATE_HEADSCALE_CONFIG"),
		"/etc/headscale/config.yaml",
		"/etc/headscale/config.yml",
		"/var/lib/headscale/config.yaml",
		"/opt/headscale/config.yaml",
	}
	var lastErr error
	for _, p := range candidates {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			lastErr = err
			continue
		}
		if path, mode := parseHeadscalePolicyConfig(string(b)); path != "" {
			// Only a file-mode policy has a path worth writing; in database
			// mode `path` may still be set as a seed and must NOT be treated
			// as authoritative.
			if mode == "" || mode == "file" {
				return path, nil
			}
			lastErr = fmt.Errorf("%s declares policy.mode=%s — the API path should have worked", p, mode)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no headscale config.yaml found in the standard locations")
	}
	return "", lastErr
}

// parseHeadscalePolicyConfig extracts (policy.path, policy.mode) from a
// headscale config.yaml. Only the `policy:` block is considered, so a `path:`
// belonging to another section (database, derp, …) can never be mistaken for
// the policy file.
func parseHeadscalePolicyConfig(cfg string) (path, mode string) {
	inPolicy := false
	policyIndent := -1
	for _, raw := range strings.Split(cfg, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if !inPolicy {
			if strings.HasPrefix(trimmed, "policy:") {
				inPolicy = true
				policyIndent = indent
			}
			continue
		}
		// A non-indented key ends the policy block.
		if indent <= policyIndent && !strings.HasPrefix(trimmed, "policy:") {
			inPolicy = false
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "path:"):
			path = yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "path:")))
		case strings.HasPrefix(trimmed, "mode:"):
			mode = yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "mode:")))
		}
	}
	return path, mode
}

// yamlScalar strips surrounding quotes and any trailing comment from a
// single-line YAML scalar.
func yamlScalar(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	v = strings.Trim(v, `"'`)
	return v
}

// oneLinePolicy renders a policy for an error message so the operator can
// apply it by hand when skygate cannot write the file.
func oneLinePolicy(policy string) string {
	flat := strings.Join(strings.Fields(policy), " ")
	const max = 400
	if len(flat) > max {
		flat = flat[:max] + "…"
	}
	return flat
}

// containerOr returns the configured container name or the given default.
func (c *Client) containerOr(def string) string {
	if c.ExecContainer != "" {
		return c.ExecContainer
	}
	return def
}

// clearACLCache drops the cached ACL string. Called by SetPolicy on
// success so the next GetACL re-reads the new policy. (The other
// caches — cacheAll/cacheUsers — are cleared by InvalidateCache in
// client.go when nodes or users change.)
func (c *Client) clearACLCache() {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cacheACL = ""
	c.cacheACLAt = time.Time{}
}

// isNonEmptyPolicyField returns true if the raw policy field
// carries a usable policy. The JSON literal `""` (two double-
// quotes, length 2) is an explicit empty marker from headscale
// and is treated the same as a missing field.
func isNonEmptyPolicyField(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if len(raw) == 2 && raw[0] == '"' && raw[1] == '"' {
		return false
	}
	// Bare whitespace also means "no policy".
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !(len(trimmed) == 2 && trimmed[0] == '"' && trimmed[1] == '"')
}
