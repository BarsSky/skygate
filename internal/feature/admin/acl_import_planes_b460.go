package admin

// acl_import_planes_b460.go — the ACL import must say what it is pushing TO.
//
// # WHY (B460, 2026-10-29)
//
// /admin/acls/import renders a dry-run that compares the imported policy with the
// current one and then reports "identical" or "different". That is the whole
// story it told — and it was not the story an operator needs, for two reasons.
//
//  1. The apply pushes the SAME policy to EVERY distinct control plane
//     (acl.SetACLForAllPlanes → db.ListControlPlanes). headscale 0.29.x is not
//     uniform across releases: approve_routes left REST at 0.29.1, the REST expire
//     path broke at 0.29.2, grants[] replaced acls[] at 0.29.0-beta.4, and 0.29.2
//     is the release that rejects wildcards in tagOwners
//     (internal/headscale/version_b297.go:30-37). The two live hosts in this
//     project already ran different 0.29.x releases at the same time. A dry-run
//     that does not name the planes, let alone their versions, therefore answers
//     "will the text be interpreted the same way everywhere?" with silence.
//
//  2. A failure answered with the FIRST error: PostAdminACLsImportApply looped
//     over the results and did `http.Error(...); return` on the first non-nil
//     Err — even though the planes BEFORE it had already been changed by
//     SetPolicy and were never rolled back. The operator saw one plane's error and
//     no idea that the run was half-applied.
//
// # WHAT THIS FILE IS NOT
//
// It is NOT a version gate. Every capability difference above is handled by a
// capability LADDER in the client (that is the project's established pattern, and
// B297 states it outright), so refusing an import because a plane reports an
// "unexpected" version would block operations that would have succeeded. The
// plane table INFORMS: it names each plane, its version, and — when the version
// could not be determined — the reason. The decision stays with the operator.
//
// The version itself is sampled by the existing headscale updater
// (internal/headscale_version.Monitor, wired through Service.PlaneVersionFn), so
// rendering this page performs NO network call of its own.

import (
	"fmt"
	"sort"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/db"
)

// planePrimaryLabel stands in for a result that carries no plane URL — the
// single-plane callers that predate B460 leave ApplyResult.PlaneURL empty.
const planePrimaryLabel = "primary plane"

// PlaneVersion is one control plane's answer for the dry-run table.
type PlaneVersion struct {
	// URL is the plane as the apply pipeline will call it ("" = the global one).
	URL string
	// Version is the version the daemon answered (or the declared pin). Empty
	// when nothing is known — and then Reason says why.
	Version string
	// Via names what produced Version (a probe rung, or the pin).
	Via string
	// Reason is non-empty exactly when Version is empty.
	Reason string
}

// Known reports whether this row carries a version.
func (p PlaneVersion) Known() bool { return strings.TrimSpace(p.Version) != "" }

// PlaneVersionAnswer is the PURE decision for one plane, so main.go and the tests
// agree on the precedence: what the daemon answered first, then the operator's
// declared pin, and always a reason when neither exists.
//
// A pin is a declaration a human typed once (internal/config says so), which is
// why it is second and is labelled as such rather than being passed off as a
// detection.
func PlaneVersionAnswer(detected, via, detectErr, declaredPin string) (version, source, reason string) {
	if v := strings.TrimSpace(detected); v != "" {
		return v, firstNonEmptyStr(strings.TrimSpace(via), "version probe"), ""
	}
	if p := strings.TrimSpace(declaredPin); p != "" {
		return p, "SKYGATE_HEADSCALE_VERSION_PIN (declared, not detected)", ""
	}
	if e := strings.TrimSpace(detectErr); e != "" {
		return "", "", "headscale version undetected (" + e + ") and SKYGATE_HEADSCALE_VERSION_PIN is empty"
	}
	return "", "", "headscale version undetected and SKYGATE_HEADSCALE_VERSION_PIN is empty"
}

// planeVersionRows builds the dry-run table in the SAME order the apply walks, so
// the operator reads the sequence that will actually happen.
//
// fn == nil means this build has no source of truth at all; that is stated per
// row instead of showing an empty version, because an empty version that looks
// fine is the defect this page exists to remove.
func planeVersionRows(planes []db.ControlPlaneUserCount, fn func(string) (string, string, error)) []PlaneVersion {
	rows := make([]PlaneVersion, 0, len(planes))
	for _, p := range planes {
		row := PlaneVersion{URL: p.URL}
		if fn == nil {
			row.Reason = "no version source is wired in this build (the headscale update monitor is nil)"
			rows = append(rows, row)
			continue
		}
		version, via, err := fn(p.URL)
		row.Version = strings.TrimSpace(version)
		row.Via = strings.TrimSpace(via)
		if row.Version == "" {
			if err != nil {
				row.Reason = err.Error()
			} else {
				row.Reason = "the version probe returned nothing and no reason"
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// versionMismatch describes the disagreement between planes, or "" when there is
// nothing to warn about.
//
// Deterministic by construction: versions are sorted, and the planes under each
// are sorted too, so the same set of planes always renders the same sentence.
func versionMismatch(rows []PlaneVersion) string {
	byVersion := map[string][]string{}
	for _, r := range rows {
		if !r.Known() {
			continue
		}
		label := r.URL
		if label == "" {
			label = planePrimaryLabel
		}
		byVersion[r.Version] = append(byVersion[r.Version], label)
	}
	if len(byVersion) < 2 {
		return ""
	}
	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	parts := make([]string, 0, len(versions))
	for _, v := range versions {
		urls := byVersion[v]
		sort.Strings(urls)
		parts = append(parts, v+" ("+strings.Join(urls, ", ")+")")
	}
	return strings.Join(parts, "; ")
}

// unknownPlanes counts the rows whose version could not be determined.
func unknownPlanes(rows []PlaneVersion) int {
	n := 0
	for _, r := range rows {
		if !r.Known() {
			n++
		}
	}
	return n
}

// ApplyReportRow is one plane's line in the post-apply report.
type ApplyReportRow struct {
	URL     string
	OK      bool
	Version int
	Detail  string
}

// applyReportRows turns the pipeline's results into the report the page renders.
//
// It keeps EVERY plane — that is the point: pre-B460 the handler returned on the
// first error, so the operator never learned which planes had already changed.
func applyReportRows(results []acl.ApplyResult) []ApplyReportRow {
	rows := make([]ApplyReportRow, 0, len(results))
	for _, r := range results {
		url := r.PlaneURL
		if url == "" {
			url = planePrimaryLabel
		}
		row := ApplyReportRow{URL: url, OK: r.Err == nil && r.Applied, Version: r.Version}
		if r.Err != nil {
			row.Detail = r.Err.Error()
		} else if !r.Applied {
			// No error but not applied: say so rather than showing a blank cell.
			row.Detail = fmt.Sprintf("not applied (snapshot %d)", r.Version)
		}
		rows = append(rows, row)
	}
	return rows
}

// applyReportCounts returns (succeeded, failed) for the report banner.
func applyReportCounts(rows []ApplyReportRow) (okCount, failCount int) {
	for _, r := range rows {
		if r.OK {
			okCount++
		} else {
			failCount++
		}
	}
	return okCount, failCount
}
