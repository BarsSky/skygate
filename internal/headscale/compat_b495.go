// compat_b495.go — what this headscale CAN do, answered from its version, in one place.
//
// WHY THIS EXISTS (B495, preparing TD-5 / RR-5)
//
// The project needs a capability answer before it needs a capability. RR-5 is the per-user DNS record
// (`exitnode.<user>.<domain>`), which headscale can only serve through `dns.extra_records`; that
// facility is not in the 0.29 line, so the feature has been blocked on "0.30" for months. Measured
// 2026-11-18: the host runs v0.29.3 and upstream's newest release is **v0.29.7** — 0.30 does not exist
// yet. The block is therefore real, but it was being carried as folklore: nothing in the tree said
// WHICH version unlocks which capability, so every reader had to re-derive it.
//
// This file is that table, in code, as a pure function: parse a version, answer the capabilities, and
// name the minimum that unlocks each one. When 0.30 ships, `ExtraRecords` flips by itself and the
// writer of the per-user records only has to ask `caps.ExtraRecords` — the older lines keep working,
// which is the point: skygate must be able to drive 0.29.x and 0.30+ from the same binary.
package headscale

import (
	"strings"
)

// Capability minimums. Written as "major.minor" so the comparison is a version compare, never a
// guess about patch releases: a capability that appears in 0.31 would bump the constant, not the call
// sites.
const (
	// MinVersionExtraRecords is the headscale release line that serves `dns.extra_records`, which is
	// what a per-user `exitnode.<user>.<domain>` record needs (TD-5 / RR-5).
	MinVersionExtraRecords = "0.30"
	// MinVersionPolicyDatabase is the mode skygate's policy writes require: with `policy.mode: file`
	// the REST policy endpoint answers 500 and skygate falls back to the privileged file applier
	// (B272). Documented here because it is the OTHER capability the panel cares about today.
	MinVersionPolicyDatabase = "0.26"
)

// Capabilities is what the detected version unlocks. Zero value = unknown version, nothing claimed.
type Capabilities struct {
	// Version is the normalised version that produced this answer ("0.29.3"); empty when unknown.
	Version string
	// Known is false when no version was detected, in which case every capability is false rather
	// than assumed. A feature must never be enabled on a guess.
	Known bool
	// ExtraRecords: `dns.extra_records` is available (per-user DNS records, RR-5).
	ExtraRecords bool
	// PolicyDatabase: the REST policy write path exists (older versions only had the file).
	PolicyDatabase bool
}

// CapabilitiesFor answers from a version string. Pure: no probing, no I/O, so a test can pin the table
// and a caller can reason about it without a live headscale.
//
// Unparseable input returns the zero value (Known=false) — deliberately NOT "assume the newest". A
// capability enabled on a version nobody could read is exactly how a panel breaks an older daemon.
func CapabilitiesFor(version string) Capabilities {
	v := normalizeServerVersion(version)
	if v == "" {
		return Capabilities{}
	}
	caps := Capabilities{Version: v, Known: true}
	caps.ExtraRecords = versionAtLeast(v, MinVersionExtraRecords)
	caps.PolicyDatabase = versionAtLeast(v, MinVersionPolicyDatabase)
	return caps
}

// versionAtLeast reports whether have >= want, comparing major and minor only (patch releases do not
// change capabilities, and a pre-release suffix of the same major.minor does not either).
func versionAtLeast(have, want string) bool {
	hm, hp := majorMinor(have)
	wm, wp := majorMinor(want)
	if hm < 0 || wm < 0 {
		return false
	}
	if hm != wm {
		return hm > wm
	}
	return hp >= wp
}

// majorMinor parses "M.m[.p][-pre]" into (M, m). Anything else is (-1, -1).
func majorMinor(v string) (int, int) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return -1, -1
	}
	major, ok := atoi(parts[0])
	if !ok {
		return -1, -1
	}
	minor, ok := atoi(parts[1])
	if !ok {
		return -1, -1
	}
	return major, minor
}

// atoi is a tiny strict parser: digits only, no sign, no space. strconv would do, but keeping this
// file dependency-free makes the table readable in isolation (and it is five lines).
func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 1000 { // nothing in a version is this large; refuse rather than overflow
			return 0, false
		}
	}
	return n, true
}

// Summary is the one line the panel/journal shows: what the running version can and cannot do, with
// the minimum named for anything it cannot. Never empty.
func (c Capabilities) Summary() string {
	if !c.Known {
		return "headscale version unknown — no capability is assumed (per-user DNS records need " + MinVersionExtraRecords + "+)"
	}
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	return "headscale " + c.Version + ": policy database writes " + yes(c.PolicyDatabase) +
		" (needs " + MinVersionPolicyDatabase + "+), per-user DNS records " + yes(c.ExtraRecords) +
		" (needs " + MinVersionExtraRecords + "+)"
}
