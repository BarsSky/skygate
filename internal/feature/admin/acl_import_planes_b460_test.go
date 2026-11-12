package admin

// acl_import_planes_b460_test.go — B460: the ACL import must name the planes it
// pushes to, and must report EVERY plane after an apply instead of only the first
// failure.
//
// Everything here is a PURE function, so no DB, no headscale and no HTTP are
// needed: the handler decides nothing the tests cannot reach.

import (
	"errors"
	"strings"
	"testing"

	"skygate/internal/acl"
	"skygate/internal/db"
)

func TestPlaneVersionAnswer_Precedence_B460(t *testing.T) {
	cases := []struct {
		name       string
		detected   string
		via        string
		detectErr  string
		pin        string
		wantVer    string
		wantSrcHas string
		wantReason bool
	}{
		{
			name: "detected wins over the declared pin",
			// The pin is a declaration a human typed once; the daemon is the fact.
			detected: "0.29.3", via: "GET /api/v1/version", pin: "0.29.0",
			wantVer: "0.29.3", wantSrcHas: "GET /api/v1/version",
		},
		{
			name:    "pin is the fallback when nothing was detected",
			pin:     "0.29.2",
			wantVer: "0.29.2", wantSrcHas: "SKYGATE_HEADSCALE_VERSION_PIN",
		},
		{
			name:     "detected without a via still names a source",
			detected: "0.29.1",
			wantVer:  "0.29.1", wantSrcHas: "version probe",
		},
		{
			name:       "nothing detected, a failed probe and no pin gives the probe reason",
			detectErr:  "GET /api/v1/version: 401; headscale version: not found",
			wantReason: true,
		},
		{
			name:       "nothing at all",
			wantReason: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotVer, gotSrc, gotReason := PlaneVersionAnswer(c.detected, c.via, c.detectErr, c.pin)
			if gotVer != c.wantVer {
				t.Errorf("version = %q, want %q", gotVer, c.wantVer)
			}
			if c.wantSrcHas != "" && !strings.Contains(gotSrc, c.wantSrcHas) {
				t.Errorf("source = %q, want it to mention %q", gotSrc, c.wantSrcHas)
			}
			// The invariant that makes the page honest: a reason exactly when there
			// is no version, so the template can never render an empty cell that
			// looks healthy.
			if c.wantReason && strings.TrimSpace(gotReason) == "" {
				t.Error("expected a reason when no version could be determined")
			}
			if !c.wantReason && gotReason != "" {
				t.Errorf("unexpected reason: %q", gotReason)
			}
			if c.detectErr != "" && c.wantReason && !strings.Contains(gotReason, "401") {
				t.Errorf("the reason must quote the probe failure, got: %q", gotReason)
			}
		})
	}
}

func TestPlaneVersionRows_NilSourceIsNamed_B460(t *testing.T) {
	rows := planeVersionRows([]db.ControlPlaneUserCount{{URL: "http://a"}, {URL: "http://b"}}, nil)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Known() {
			t.Errorf("%s: must not claim a version when no source is wired", r.URL)
		}
		if !strings.Contains(r.Reason, "no version source") {
			t.Errorf("%s: reason must name the missing source, got %q", r.URL, r.Reason)
		}
	}
}

func TestPlaneVersionRows_PerPlaneAnswers_B460(t *testing.T) {
	planes := []db.ControlPlaneUserCount{
		{URL: "http://primary:50444"},
		{URL: "http://second:50444"},
		{URL: "http://silent:50444"},
	}
	fn := func(url string) (string, string, error) {
		switch url {
		case "http://primary:50444":
			return "0.29.2", "GET /api/v1/version", nil
		case "http://second:50444":
			return "", "", errors.New("the version monitor samples only the primary headscale")
		default:
			return "", "", nil // a source that answers nothing and says nothing
		}
	}
	rows := planeVersionRows(planes, fn)
	if !rows[0].Known() || rows[0].Version != "0.29.2" {
		t.Errorf("row 0 = %+v, want a known 0.29.2", rows[0])
	}
	if rows[1].Known() || !strings.Contains(rows[1].Reason, "samples only the primary") {
		t.Errorf("row 1 = %+v, want the provider's reason", rows[1])
	}
	// A provider that returns nothing and no error must still produce a reason,
	// otherwise the page renders a blank version cell.
	if rows[2].Known() || strings.TrimSpace(rows[2].Reason) == "" {
		t.Errorf("row 2 = %+v, want a non-empty reason", rows[2])
	}
}

func TestVersionMismatch_OnlyWhenTwoKnownVersions_B460(t *testing.T) {
	same := []PlaneVersion{
		{URL: "http://a", Version: "0.29.2"},
		{URL: "http://b", Version: "0.29.2"},
	}
	if got := versionMismatch(same); got != "" {
		t.Errorf("identical versions must not warn, got %q", got)
	}

	oneKnown := []PlaneVersion{
		{URL: "http://a", Version: "0.29.2"},
		{URL: "http://b", Reason: "unknown"},
	}
	if got := versionMismatch(oneKnown); got != "" {
		t.Errorf("a single known version must not warn, got %q", got)
	}

	differ := []PlaneVersion{
		{URL: "http://b", Version: "0.29.2"},
		{URL: "http://a", Version: "0.29.0"},
		{URL: "http://c", Reason: "unknown"},
	}
	got := versionMismatch(differ)
	for _, want := range []string{"0.29.0", "0.29.2", "http://a", "http://b"} {
		if !strings.Contains(got, want) {
			t.Errorf("mismatch %q must mention %q", got, want)
		}
	}
	if strings.Contains(got, "http://c") {
		t.Errorf("an unknown plane must not be listed as a version: %q", got)
	}
	// Deterministic: the same set must render the same sentence every time, or a
	// page refresh looks like a change.
	for i := 0; i < 20; i++ {
		if again := versionMismatch(differ); again != got {
			t.Fatalf("message is not deterministic:\n first: %q\n now:   %q", got, again)
		}
	}
	if !strings.HasPrefix(got, "0.29.0") {
		t.Errorf("versions must be sorted, got %q", got)
	}

	if got := versionMismatch(nil); got != "" {
		t.Errorf("no rows must not warn, got %q", got)
	}
}

func TestApplyReportRows_EveryPlaneKept_B460(t *testing.T) {
	boom := errors.New(`headscale: 400 {"message":"src=tag not found"}`)
	results := []acl.ApplyResult{
		{Version: 11, Applied: true, PlaneURL: "http://a:50444"},
		{Version: 12, Applied: false, PlaneURL: "http://b:50444", Err: boom},
		{Version: 13, Applied: true, PlaneURL: ""},
		{Version: 14, Applied: false, PlaneURL: "http://d:50444"},
	}
	rows := applyReportRows(results)
	if len(rows) != len(results) {
		t.Fatalf("every plane must be reported: got %d rows for %d results", len(rows), len(results))
	}
	if !rows[0].OK || rows[0].URL != "http://a:50444" || rows[0].Detail != "" {
		t.Errorf("row 0 = %+v, want a clean success for a", rows[0])
	}
	if rows[1].OK || !strings.Contains(rows[1].Detail, "src=tag not found") {
		t.Errorf("row 1 = %+v, want the failure detail", rows[1])
	}
	if rows[2].URL != planePrimaryLabel {
		t.Errorf("an empty plane URL must be labelled %q, got %q", planePrimaryLabel, rows[2].URL)
	}
	// No error and not applied: say so instead of rendering an empty cell.
	if rows[3].OK || strings.TrimSpace(rows[3].Detail) == "" {
		t.Errorf("row 3 = %+v, want a named non-applied state", rows[3])
	}

	okCount, failCount := applyReportCounts(rows)
	if okCount != 2 || failCount != 2 {
		t.Errorf("counts = %d/%d, want 2/2", okCount, failCount)
	}
}

func TestUnknownPlanes_B460(t *testing.T) {
	rows := []PlaneVersion{
		{URL: "a", Version: "0.29.2"},
		{URL: "b", Reason: "unknown"},
		{URL: "c"},
	}
	if got := unknownPlanes(rows); got != 2 {
		t.Errorf("unknownPlanes = %d, want 2", got)
	}
	if got := unknownPlanes(nil); got != 0 {
		t.Errorf("unknownPlanes(nil) = %d, want 0", got)
	}
}
