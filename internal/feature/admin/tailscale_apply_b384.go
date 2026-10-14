package admin

// tailscale_apply_b384.go — B384: the client's settings must actually APPLY, and the
// node itself must be able to say whether they did.
//
// # THE LIVE DEFECT (reference host, 2026-10-14, v1.5.122)
//
//	tailscale-autostart: boot: start FAILED: tailscale up: exit status 1
//	  (out=Error: changing settings via 'tailscale up' requires mentioning all
//	   non-default flags. To proceed, either re-run your command with --reset or
//	   use the command below to explicitly mention the current va…)
//
// …on the boot pass AND on every 5-minute tick, forever. The node itself was healthy
// (`skygate-host`, `tag:dev-infra-skygate-host`, BackendState=Running), so nothing
// looked broken — but NOTHING the invocation asked for was ever applied. `tailscale up`
// refuses to change ANY setting unless the invocation lists EVERY persisted non-default
// pref, and this container's state carries `NoStatefulFiltering=true` and
// `NetfilterMode=2`, which no caller ever mentioned. Consequence: an operator who edits
// the hostname or the tags in the panel waits forever, and the journal carries a
// permanent failure line that hides real ones.
//
// The entrypoint had the same class of bug plus the retired name: its advertise-tags
// fallback was literally `tag:dev-infra-skygate-host-1,tag:private` — the
// `skygate-host-<n>` shape B320/B321 exist to remove, which can name a node that no
// longer exists.
//
// # WHAT B384 DOES
//
//  1. `tailscale set` is tried FIRST whenever the daemon already answers. Upstream
//     documents it as "does not require the complete set of desired settings" — the one
//     property `up` cannot offer on a drifted state, and the reason this class kept
//     coming back.
//  2. `tailscale up` stays for the FIRST join (no state yet) and as the fallback.
//  3. A failure whose text is the full-flag refusal is retried ONCE with `up --reset`
//     (the remedy upstream itself prints), with a named log line saying why.
//  4. The advertise-tags default is DERIVED from the resolved hostname
//     (`AdvertiseTagForHostname`), and an existing tag set is PRESERVED: the derived tag
//     is added to what the daemon already carries, never used to replace it, unless the
//     operator sets SKYGATE_TS_ADVERTISE_TAGS explicitly (then that wins verbatim).
//  5. After applying, the intent is VERIFIED against the daemon's own view
//     (`.Self.HostName` + `.Self.Tags` of `tailscale status --json`) and reported in one
//     named line: `tailscale-apply: OK …` or a WARN that names both sides. The operator
//     asked for exactly this: «проверить, что мой узел реально применил свои настройки».
//
// The shell half of the same ladder lives in entrypoint.sh between the
// `B384 helpers` markers, and scripts/check_b384_tailscale_settings_apply.sh pins BOTH
// (plus the two pure helpers here, which are unit-tested).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// tailscaleApplyTimeout bounds a single `tailscale set`/`up` on this path. The
// entrypoint uses the same ceiling for its own invocation.
const tailscaleApplyTimeout = 30 * time.Second

// AdvertiseTagForHostname derives the infra tag that describes a hostname:
// `tag:dev-infra-<name>` (lower-cased, trimmed). Pure — the table is unit-tested.
//
// The hostname is canonicalised FIRST through NormalizeTailscaleHostname, so the
// retired placeholder shape (`skygate-host-1` and friends) can never come back out of
// this function: B320/B321 made `skygate-host` canonical, and a tag built from the
// suffixed form names a node that does not exist while the real node stays untagged.
func AdvertiseTagForHostname(hostname string) string {
	if canon, _ := NormalizeTailscaleHostname(hostname); canon != "" {
		hostname = canon
	}
	n := strings.ToLower(strings.TrimSpace(hostname))
	if n == "" {
		return ""
	}
	return "tag:dev-infra-" + n
}

// tailscalePrefDriftKind classifies the output of a failed `tailscale set`/`up`.
// Pure. The caller uses it to decide whether a retry can help and what to log:
//   - "drift"              the persisted prefs the invocation did not mention
//     (upstream's own words; `--reset` is its printed remedy)
//   - "bad-flag"           this CLIENT does not know a flag we passed (1.102.3 has no
//     `set --advertise-tags`; the live text is
//     `flag provided but not defined: -advertise-tags`)
//   - "tags-not-permitted" headscale refused a tag (no tagOwners entry for it)
//   - "logged-out"         the node needs (re)authentication
//   - "timeout"            the 30s ceiling fired
//   - ""                   anything else — reported verbatim, never guessed at
//
// The whitespace is COLLAPSED before matching because tailscale WRAPS this message:
// the live text is
//
//	Error: changing settings via 'tailscale up' requires mentioning all
//	non-default flags. To proceed, …
//
// so a plain substring search for the sentence never matches — the first version of
// this classifier (and of the shell one) missed exactly that, which is the whole
// reason the drift was never detected in production.
func tailscalePrefDriftKind(out string) string {
	o := strings.Join(strings.Fields(strings.ToLower(out)), " ")
	switch {
	case strings.Contains(o, "requires mentioning all non-default flags"):
		return "drift"
	case strings.Contains(o, "flag provided but not defined"):
		return "bad-flag"
	case strings.Contains(o, "invalid or not permitted"), strings.Contains(o, "not permitted"):
		return "tags-not-permitted"
	case strings.Contains(o, "logged out"), strings.Contains(o, "not logged in"):
		return "logged-out"
	case strings.Contains(o, "context deadline exceeded"), strings.Contains(o, "timed out"):
		return "timeout"
	}
	return ""
}

// B384.1 (v1.5.124) — the `set` rung must not pass a flag this CLIENT does not have.
//
// Measured on the reference host one tick after v1.5.123 was applied (Tailscale 1.102.3,
// the version the image ships):
//
//	tailscale-apply: 'tailscale set' failed (unclassified):
//	  flag provided but not defined: -advertise-tags
//
// `tailscale set --help` on that client lists --hostname, --accept-routes, --accept-dns,
// --exit-node … and NO --advertise-tags, while `tailscale up` does have it. So the first
// rung of the ladder could never succeed there: it burned a failed invocation and printed
// a permanent red line on every boot and every 5-minute tick — the exact noise class B384
// exists to remove — and the tags were only ever applied because the ladder fell through
// to `up`.
//
// Two rules follow, and both are pinned by contracts:
//  1. ask the client whether `set` supports the flag (once per process) and omit it when
//     it does not, instead of discovering it by failing;
//  2. `set` is trusted ONLY when the daemon's own view then matches the intent — tags it
//     cannot carry are the `up` rung's job, and on a version with the flag `set` alone is
//     still enough.
var tailscaleSetAdvertiseTags = struct {
	once sync.Once
	ok   bool
}{}

// parseSetSupportsAdvertiseTags is the PURE half of the capability probe: does this
// `tailscale set --help` text advertise --advertise-tags? Split out so the decision is
// unit-tested without a tailscale binary on the machine.
func parseSetSupportsAdvertiseTags(help string) bool {
	return strings.Contains(help, "--advertise-tags")
}

// tailscaleSetSupportsAdvertiseTags reports whether `tailscale set` on THIS client accepts
// --advertise-tags. Probed once and cached; a missing client or an unreadable help means
// "no", which only means the tag is left to `up` (the rung that certainly has it).
func tailscaleSetSupportsAdvertiseTags() bool {
	tailscaleSetAdvertiseTags.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tailscale", "set", "--help").CombinedOutput()
		if err != nil && len(out) == 0 {
			return
		}
		tailscaleSetAdvertiseTags.ok = parseSetSupportsAdvertiseTags(string(out))
	})
	return tailscaleSetAdvertiseTags.ok
}

// tailscaleSettingsMatch is the QUIET half of verifyTailscaleSettings: one read of the
// daemon's own view, no log line, no polling. It is the gate that decides whether the
// `set` rung may be accepted as final (an intermediate verdict must not print
// "SETTINGS NOT APPLIED" for a mismatch the next rung is about to fix).
func tailscaleSettingsMatch(wantHost string, wantTags []string) bool {
	gotHost, gotTags := tailscaleSelfStatus()
	return gotHost != "" && hostAndTagsMatch(wantHost, wantTags, gotHost, gotTags)
}

// runTailscale runs one tailscale verb under the shared timeout and returns its
// combined output.
func runTailscale(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tailscaleApplyTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", args...).CombinedOutput()
	return string(out), err
}

// tailscaleSelfStatus reads the daemon's own view of itself in ONE call:
// `.Self.HostName` (falling back to the first label of `.Self.DNSName`) and
// `.Self.Tags`. Returns ("", nil) when the daemon does not answer — the caller then
// reports "cannot verify" rather than inventing a verdict.
func tailscaleSelfStatus() (string, []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return "", nil
	}
	var st struct {
		Self struct {
			HostName string   `json:"HostName"`
			DNSName  string   `json:"DNSName"`
			Tags     []string `json:"Tags"`
		} `json:"Self"`
	}
	if json.Unmarshal(out, &st) != nil {
		return "", nil
	}
	host := strings.TrimSpace(st.Self.HostName)
	if host == "" {
		host = strings.TrimSuffix(strings.TrimSpace(st.Self.DNSName), ".")
		if i := strings.IndexByte(host, '.'); i > 0 {
			host = host[:i]
		}
	}
	return host, st.Self.Tags
}

// splitTagList normalises a comma/space separated tag list from the environment.
func splitTagList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// desiredAdvertiseTags is the tag set the apply path asks for:
//
//	SKYGATE_TS_ADVERTISE_TAGS (explicit operator intent — used verbatim), else
//	the daemon's CURRENT tags plus the derived tag for this hostname.
//
// The union matters: `tailscale set --advertise-tags=X` REPLACES the set, so a naive
// "derived only" would silently drop anything else the operator had put there
// (`tag:private` on the reference deployment). Additive keeps B185's preservation
// property while still guaranteeing the tag that describes the node.
func desiredAdvertiseTags(hostname string) []string {
	if v := strings.TrimSpace(os.Getenv("SKYGATE_TS_ADVERTISE_TAGS")); v != "" {
		return splitTagList(v)
	}
	want := AdvertiseTagForHostname(hostname)
	_, cur := tailscaleSelfStatus()
	set := map[string]bool{}
	for _, t := range cur {
		if t = strings.TrimSpace(t); t != "" {
			set[t] = true
		}
	}
	if want != "" {
		set[want] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// verifyTailscaleSettings compares the INTENT with the daemon's own view and logs one
// named line. It polls briefly first: a `set` right after a rename can take a moment to
// appear in `status --json`, and a false WARN would be worse than no check at all.
func verifyTailscaleSettings(wantHost string, wantTags []string) bool {
	var gotHost string
	var gotTags []string
	for attempt := 0; attempt < 6; attempt++ {
		gotHost, gotTags = tailscaleSelfStatus()
		if gotHost != "" && hostAndTagsMatch(wantHost, wantTags, gotHost, gotTags) {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if gotHost == "" {
		log.Printf("tailscale-apply: cannot verify — the daemon did not answer 'tailscale status --json' "+
			"(wanted hostname=%s tags=%v)", wantHost, wantTags)
		return false
	}
	if hostAndTagsMatch(wantHost, wantTags, gotHost, gotTags) {
		log.Printf("tailscale-apply: OK hostname=%s tags=%v", gotHost, gotTags)
		return true
	}
	log.Printf("tailscale-apply: SETTINGS NOT APPLIED — the daemon reports hostname=%q tags=%v, "+
		"the configuration asks for hostname=%q tags=%v (the panel's Start/Rename actions, or "+
		"SKYGATE_TS_HOSTNAME / SKYGATE_TS_ADVERTISE_TAGS, decide those)",
		gotHost, gotTags, wantHost, wantTags)
	return false
}

// hostAndTagsMatch is the pure comparison behind verifyTailscaleSettings: the hostname
// case-insensitively, the tags as SETS (order is not part of the intent, and
// `tailscale status` does not promise one).
func hostAndTagsMatch(wantHost string, wantTags []string, gotHost string, gotTags []string) bool {
	if !strings.EqualFold(strings.TrimSpace(wantHost), strings.TrimSpace(gotHost)) {
		return false
	}
	want := map[string]bool{}
	for _, t := range wantTags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			want[t] = true
		}
	}
	got := map[string]bool{}
	for _, t := range gotTags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			got[t] = true
		}
	}
	if len(want) != len(got) {
		return false
	}
	for t := range want {
		if !got[t] {
			return false
		}
	}
	return true
}

// applyTailscaleSettings is the ladder described at the top of this file. It is what
// `tailscaleUp` now delegates to, so every existing caller (the boot pass, the
// 5-minute tick, the panel's Start/Rename/Save actions) gets the drift-proof path.
func (s *Service) applyTailscaleSettings(key string) (string, error) {
	host, source, rewritten := s.tailscaleHostnameResolved()
	if host == "" {
		host = tailscaleHostnameDefault()
	}
	if rewritten {
		log.Printf("tailscale-apply: the configured hostname (source=%s) is the legacy placeholder shape "+
			"— applying the canonical name %q (B320/B321)", source, host)
	}
	tags := desiredAdvertiseTags(host)
	tagArg := strings.Join(tags, ",")
	loginServer := s.tailscaleLoginServer()
	common := []string{"--accept-routes", "--accept-dns=false", "--exit-node="}

	// 1. The daemon already answers: `set` changes only what we name, and upstream
	//    guarantees it does NOT require the complete set of desired settings.
	//
	//    B384.1: the tag flag is passed ONLY when this client's `set` knows it (1.102.3
	//    does not), and `set` is accepted only when the daemon then matches the intent —
	//    otherwise the tags are left to the `up` rung, which carries them.
	if tailscaleDaemonAnswers() {
		args := append([]string{"set", "--hostname=" + host}, common...)
		tagCarriedBySet := tagArg != "" && tailscaleSetSupportsAdvertiseTags()
		if tagCarriedBySet {
			args = append(args, "--advertise-tags="+tagArg)
		}
		out, err := runTailscale(args...)
		switch {
		case err == nil && tailscaleSettingsMatch(host, tags):
			verifyTailscaleSettings(host, tags)
			return out, nil
		case err == nil:
			reason := "has no --advertise-tags, so the tags are not part of what it can set"
			if tagCarriedBySet {
				reason = "has --advertise-tags, but the tag set still did not land"
			}
			log.Printf("tailscale-apply: 'tailscale set' applied the flags it can, but the daemon still does "+
				"not match the intent (this client's `set` %s) — continuing with 'tailscale up', which carries them", reason)
		default:
			log.Printf("tailscale-apply: 'tailscale set' failed (%s): %s — falling back to 'tailscale up'",
				orOther(tailscalePrefDriftKind(out)), truncate(out, 200))
		}
	}

	// 2. First join (no state) or the fallback: `up` with every flag this container owns.
	upArgs := append([]string{"up", "--login-server=" + loginServer, "--authkey=" + key,
		"--hostname=" + host}, common...)
	if tagArg != "" {
		upArgs = append(upArgs, "--advertise-tags="+tagArg)
	}
	out, err := runTailscale(upArgs...)
	if err == nil {
		verifyTailscaleSettings(host, tags)
		return out, nil
	}

	// 3. The full-flag refusal means the persisted state drifted from this invocation.
	//    `--reset` is the remedy upstream prints, and it is safe here because this call
	//    names every pref the container owns.
	if tailscalePrefDriftKind(out) == "drift" {
		log.Printf("tailscale-apply: the persisted prefs drifted from this invocation "+
			"(upstream: %s) — retrying ONCE with 'tailscale up --reset'", truncate(out, 160))
		resetArgs := append([]string{"up", "--reset", "--login-server=" + loginServer,
			"--authkey=" + key, "--hostname=" + host}, common...)
		if tagArg != "" {
			resetArgs = append(resetArgs, "--advertise-tags="+tagArg)
		}
		out2, err2 := runTailscale(resetArgs...)
		verifyTailscaleSettings(host, tags)
		if err2 == nil {
			log.Printf("tailscale-apply: 'tailscale up --reset' applied the settings "+
				"(hostname=%s tags=%v) — the drift is cleared for good", host, tags)
			return out2, nil
		}
		return out2, fmt.Errorf("tailscale up --reset: %w (%s)", err2, truncate(out2, 200))
	}

	verifyTailscaleSettings(host, tags)
	return out, fmt.Errorf("tailscale up: %w (%s)", err, truncate(out, 200))
}

// orOther names an empty classification instead of printing nothing.
func orOther(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "unclassified"
	}
	return kind
}
