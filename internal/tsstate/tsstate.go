// Package tsstate answers ONE question the operator keeps asking twice: why is the
// in-container Tailscale not running?
//
// B318 — WHY THIS PACKAGE EXISTS. Two admin pages describe the same daemon and they
// disagreed on the reference host (2026-09-24):
//
//	/admin/exit-nodes  «skygate НЕ в tailnet … tailscaled is not running: failed to
//	                    connect to local tailscaled; it doesn't appear to be running»
//	/admin/tailscale   looked ENABLED (green card, clickable Start), because the
//	                    global_settings override `tailscale.auth_key_path` pointed at
//	                    a real key file while the CONTAINER ENV still carried
//	                    `SKYGATE_TS_AUTHKEY_FILE=/dev/null`
//
// Both pages were internally honest and both pointed at the wrong thing: the daemon
// is not running because (a) the entrypoint SKIPPED it at container start (it reads
// the env var, never the DB override — the override only changes what the page and
// its Start button do) and (b) the last recorded start attempt died with
// `CreateTUN("tailscale0") failed; /dev/net/tun does not exist` in an older
// container instance. Neither fact was on either page.
//
// So the facts live here, in a package both features can import (internal/feature/
// admin → internal/feature/exit_rules would be a cycle), and each page renders them
// in its own words.
package tsstate

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvKey is the variable the CONTAINER ENTRYPOINT reads at start. Its value is
// frozen when the container is created (AGENTS trap #3 for `docker compose restart`),
// which is exactly why a DB override cannot change it.
const EnvKey = "SKYGATE_TS_AUTHKEY_FILE"

// DefaultStateDir mirrors the page's TailscaleState.StateDir default.
const DefaultStateDir = "/var/lib/tailscale"

// TunDevice is the kernel device tailscaled needs to create its interface.
const TunDevice = "/dev/net/tun"

// State is what the pages need to explain the daemon's situation.
type State struct {
	// AuthKeyFileEnv is SKYGATE_TS_AUTHKEY_FILE as the entrypoint sees it.
	AuthKeyFileEnv string
	// EnvDisabled is true when that value makes the entrypoint skip tailscaled:
	// unset, empty, or the `/dev/null` sentinel (entrypoint.sh tests `-f`, and
	// /dev/null is a character device, so the test is false).
	EnvDisabled bool
	// TunPresent is true when /dev/net/tun exists in THIS container.
	TunPresent bool
	// LastDaemonError is the last error-looking line of the tailscaled log, if any
	// (best effort; "" when there is nothing to read).
	LastDaemonError string
	// LastDaemonErrorAt is when that line was written (zero when unknown). The log
	// is a PERSISTENT file (the state dir is a bind mount), so a failure can easily
	// belong to an EARLIER container instance — the reference host had a
	// `CreateTUN(...): /dev/net/tun does not exist` from before the device was
	// mapped, which would be a lie about the container that is running now.
	LastDaemonErrorAt time.Time
}

// Detect reads the live facts. Every field is best effort: the function never fails,
// because its callers are page renders.
func Detect(stateDir string) State {
	if strings.TrimSpace(stateDir) == "" {
		stateDir = DefaultStateDir
	}
	st := State{
		AuthKeyFileEnv: strings.TrimSpace(os.Getenv(EnvKey)),
		TunPresent:     tunPresent(),
	}
	switch st.AuthKeyFileEnv {
	case "", "/dev/null":
		st.EnvDisabled = true
	}
	st.LastDaemonError, st.LastDaemonErrorAt = lastDaemonError(stateDir)
	return st
}

// tunPresent reports whether the TUN device exists (os.Stat, not a capability
// probe: a container without the device mapping cannot create the interface even
// with NET_ADMIN).
func tunPresent() bool {
	_, err := os.Stat(TunDevice)
	return err == nil
}

// lastDaemonError returns the last line of the tailscaled log that looks like a
// failure, plus when it was written (zero when the line carries no timestamp).
// tailscaled writes JSON-lines through logtail's local file transport
// (`.log1.txt` / `.log2.txt`), so the "text" field is what a human needs; the raw
// line is used when the JSON shape is not what we expect.
func lastDaemonError(stateDir string) (string, time.Time) {
	// Newest rotation first: log1 is the active file, log2 the previous one, and a
	// stale failure may only exist in either.
	for _, name := range []string{"tailscaled.log1.txt", "tailscaled.log2.txt"} {
		if text, at := scanForError(filepath.Join(stateDir, name)); text != "" {
			return text, at
		}
	}
	return "", time.Time{}
}

func scanForError(path string) (string, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}
	}
	defer f.Close()
	var last string
	var lastAt time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 512*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec struct {
			Text    string `json:"text"`
			Logtail struct {
				ClientTime string `json:"client_time"`
			} `json:"logtail"`
		}
		text := line
		if err := json.Unmarshal([]byte(line), &rec); err == nil && strings.TrimSpace(rec.Text) != "" {
			text = strings.TrimSpace(rec.Text)
		}
		if looksLikeFailure(text) {
			last = strings.TrimSpace(text)
			lastAt = time.Time{}
			if rec.Logtail.ClientTime != "" {
				if ts, terr := time.Parse(time.RFC3339Nano, rec.Logtail.ClientTime); terr == nil {
					lastAt = ts
				}
			}
		}
	}
	return firstLine(last), lastAt
}

// looksLikeFailure is deliberately narrow: the log is full of routine lines that
// mention "error" inside iptables output, and quoting one of those as THE reason
// would be worse than saying nothing.
func looksLikeFailure(text string) bool {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "createtun"),
		strings.Contains(low, "does not exist"),
		strings.Contains(low, "getlocalbackend error"),
		strings.Contains(low, "createengine"),
		strings.Contains(low, "modprobe"),
		strings.Contains(low, "needslogin"),
		strings.Contains(low, "logged out"),
		strings.Contains(low, "auth key"):
		return true
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Explain renders the facts as one operator-facing sentence, or "" when there is
// nothing special to say (the daemon is either running or simply never configured).
//
// The order matters: the entrypoint's decision is the ROOT cause of "nothing is
// running at all", so it comes first; the TUN device explains a start attempt that
// died immediately; the recorded daemon error is the last word.
func (s State) Explain() string {
	var parts []string
	if s.EnvDisabled {
		v := s.AuthKeyFileEnv
		if v == "" {
			v = "(unset)"
		}
		parts = append(parts, "the container was created with "+EnvKey+"="+v+
			", so the entrypoint SKIPS tailscaled at boot — that value is frozen at container creation and the saved path on /admin/tailscale only affects the Start button there; press Start (it works in the running container) or recreate the container without the disabled sentinel")
	}
	if !s.TunPresent {
		parts = append(parts, "this container has no "+TunDevice+
			", so tailscaled cannot create its interface — add the device (and NET_ADMIN, SYS_ADMIN) to the service in docker-compose.yml and recreate it")
	}
	if s.LastDaemonError != "" {
		line := s.LastDaemonError
		// A failure that names the TUN device while the device IS present cannot be
		// about this container — the state dir (and its log) is a bind mount, so say
		// which instance it belongs to instead of accusing the running one.
		staleTun := s.TunPresent && mentionsTun(line)
		when := ""
		if !s.LastDaemonErrorAt.IsZero() {
			when = " (" + s.LastDaemonErrorAt.UTC().Format("2006-01-02 15:04:05 MST") + ")"
		}
		switch {
		case staleTun:
			parts = append(parts, "an EARLIER container instance failed to start tailscaled"+when+": "+line+
				" — this container does have "+TunDevice+", so that failure is history, not a current blocker")
		default:
			parts = append(parts, "the last recorded tailscaled start failed"+when+": "+line)
		}
	}
	return strings.Join(parts, "; ")
}

// mentionsTun reports whether an error line is about the TUN device.
func mentionsTun(line string) bool {
	low := strings.ToLower(line)
	return strings.Contains(low, "/dev/net/tun") || strings.Contains(low, "createtun")
}
