// Package admin — derp_metrics.go
//
// B315 — the metrics endpoint: where /admin/derp reads derper's rich metrics
// from, why it needs an operator-visible knob, and the save/test/clear form.
//
// ---------------------------------------------------------------------------
// THE PROBLEM, MEASURED
// ---------------------------------------------------------------------------
//
// derper exposes `/debug/vars` (accepts, bytes, packets, clients, current
// connections, and the STUN counters) and `/debug/` (uptime, version, machine).
// Upstream `tsweb.AllowDebugAccess` admits **loopback and tailnet sources only**
// and answers everything else `403 debug access denied` — the verdict is made on
// the request's SOURCE address, so no amount of re-dialling from the container
// changes it (the `derp.probe_host` knob of B296 solves a different problem: it
// makes the TCP/TLS probes REACH the relay, it cannot make them ADMITTED).
//
// On the agent VM the two paths differ exactly as the code above predicts:
//
//	host loopback  GET https://127.0.0.1:443/debug/vars  → 200, ~6.3 KB JSON
//	container      GET https://<relay>:443/debug/vars    → 403 debug access denied
//
// The page's pre-B315 reaction was to hang one warning banner next to twelve
// zeros. Both halves of that were wrong: the zeros look like measurements, and
// the banner named no cause and no fix — the operator's own words were «висит
// предупреждение о падаваемых метриках что никак не управляется и не
// объясняется для администратора».
//
// ---------------------------------------------------------------------------
// THE FIX
// ---------------------------------------------------------------------------
//
// A metrics ENDPOINT: an opt-in base URL that the container can reach and that
// fetches on its behalf from the host's loopback. skygate ships the bridge
// itself (`skygate derp-metrics-proxy`, see internal/derpmetricsproxy), and this
// file owns the operator-facing half:
//
//	resolution  derpcfg: DB override (the form below) > SKYGATE_DERP_DEBUG_URL >
//	            none — re-read on every render, so saving takes effect on the
//	            next refresh with no restart and no container recreate (B296 rule);
//	verdict     MetricsAvailable / MetricsErrCode / MetricsErrDetail, so the tiles
//	            can render "—" plus a named reason instead of zeros;
//	action      POST /admin/derp/metrics-endpoint with action=save|clear|test.
package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/derpcfg"
)

// metricsEndpoint is the resolved metrics source for one page render.
type metricsEndpoint struct {
	// Base is the effective base URL (normalised, no trailing slash), "" when the
	// operator has configured none — then the relay itself is used and will, on a
	// hardened deployment, answer 403.
	Base string
	// Source is where Base came from: db | env | default.
	Source derpcfg.Source
	// Env is SKYGATE_DERP_DEBUG_URL, shown next to the form so the operator can
	// see whether editing .env would change anything at all.
	Env string
	// Relay is the fallback base (the relay's own URL) for callers that want to
	// say what would be used instead.
	Relay string
}

// resolveMetricsEndpoint reads the operator's metrics endpoint for one render.
func resolveMetricsEndpoint(db *sql.DB, relayBase string) metricsEndpoint {
	res := derpcfg.ResolveDebug(db)
	return metricsEndpoint{
		Base:   res.Host,
		Source: res.Source,
		Env:    derpcfg.EnvDebugURL(),
		Relay:  relayBase,
	}
}

// MetricsCheck is the outcome of one `/debug/vars` read, shared by the page's own
// scrape and by the "Проверить" button so the two can never disagree.
type MetricsCheck struct {
	OK     bool
	Status int
	Bytes  int
	Denied bool
	// Code is a stable, translatable reason: ok | denied | unreachable |
	// badstatus | badbody.
	Code   string
	Detail string
	// The few counters the check reads out, for the flash message.
	Accepts     int
	STUNOK      int
	STUNNotSTUN int
}

// checkMetricsEndpoint performs one `/debug/vars` round trip against `base`.
//
// `dialAddr` pins the TCP connection while the URL keeps its hostname as SNI
// (B289.1) and is empty for a metrics endpoint (which is dialled as written).
func checkMetricsEndpoint(base, dialAddr string, timeout time.Duration) MetricsCheck {
	var chk MetricsCheck
	body, status, err := httpGetViaStatus(strings.TrimSuffix(base, "/")+"/debug/vars", dialAddr, timeout)
	chk.Status = status
	chk.Bytes = len(body)
	switch {
	case err != nil:
		chk.Code = "unreachable"
		chk.Detail = trimMetricsDetail(err.Error())
	case isDebugAccessDenied(body):
		chk.Denied = true
		chk.Code = "denied"
		chk.Detail = "403 debug access denied"
	case status != http.StatusOK:
		chk.Code = "badstatus"
		chk.Detail = metricsErrorDetail(status, body, base)
	default:
		var st DerpStatus
		if parseDerperVars(&st, body) {
			chk.OK = true
			chk.Code = "ok"
			chk.Accepts = st.Accepts
			chk.STUNOK = st.STUNRequests
			chk.STUNNotSTUN = st.STUNNotSTUN
			chk.Detail = fmt.Sprintf("%d bytes", len(body))
		} else {
			chk.Code = "badbody"
			chk.Detail = metricsErrorDetail(status, body, base)
		}
	}
	return chk
}

// metricsErrorDetail renders "who answered what" for the page, with a short,
// whitespace-collapsed excerpt of an error body so a proxy's own explanation
// survives the trip.
func metricsErrorDetail(status int, body []byte, base string) string {
	where := "the relay"
	if strings.TrimSpace(base) != "" {
		where = "the metrics endpoint " + strings.TrimSpace(base)
	}
	ex := strings.Join(strings.Fields(string(body)), " ")
	if len(ex) > 240 {
		ex = ex[:240] + "…"
	}
	if ex == "" {
		ex = "(empty body)"
	}
	return fmt.Sprintf("%s answered HTTP %d: %s", where, status, ex)
}

// trimMetricsDetail shortens a transport error for the page (the useful part is
// the leading cause, not the whole dial chain).
func trimMetricsDetail(s string) string {
	return trimSTUNErr(s)
}

// ---------- POST /admin/derp/metrics-endpoint ----------

// PostAdminDerpMetricsEndpoint handles the metrics-endpoint card on /admin/derp.
//
// Three actions, one form:
//
//	save   validate + store the typed value (empty clears the row);
//	clear  drop the DB row so the env/default layer takes over again;
//	test   probe the TYPED value WITHOUT saving it — the operator gets to see
//	       whether an address works before committing it. This is the half that
//	       was missing pre-B315: the page could state a problem but offered no
//	       way to check a candidate fix.
//
// Every outcome is a redirect flash on /admin/derp (the page that shows the
// warning), never a raw error page — the operator is already looking at the
// thing that is wrong.
func (s *Service) PostAdminDerpMetricsEndpoint(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp?merr=form", http.StatusFound)
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	raw := r.FormValue("metrics_endpoint")

	// test never writes: validate the candidate, probe it, report.
	if action == "test" {
		candidate, verr := derpcfg.ValidateDebugURL(raw)
		if verr != nil {
			redirectMetricsValidationError(w, r, c.Username, verr)
			return
		}
		if candidate == "" {
			http.Redirect(w, r, "/admin/derp?mt=empty", http.StatusFound)
			return
		}
		chk := checkMetricsEndpoint(candidate, "", 5*time.Second)
		s.Backend.Audit(c.UserID, c.Username, "derp.metrics_endpoint",
			fmt.Sprintf("action=test value=%q result=%s", candidate, chk.Code))
		q := url.Values{}
		q.Set("mt", chk.Code)
		if chk.Detail != "" {
			q.Set("mtd", truncateForQuery(chk.Detail))
		}
		if chk.Status != 0 {
			q.Set("mts", fmt.Sprint(chk.Status))
		}
		if chk.OK {
			q.Set("mtb", fmt.Sprint(chk.Bytes))
			q.Set("mta", fmt.Sprint(chk.Accepts))
			q.Set("mtu", fmt.Sprint(chk.STUNOK))
			q.Set("mtn", fmt.Sprint(chk.STUNNotSTUN))
		}
		http.Redirect(w, r, "/admin/derp?"+q.Encode(), http.StatusFound)
		return
	}

	clear := action == "clear"
	if clear {
		raw = ""
	}
	if err := derpcfg.SaveDebug(s.dbc(), raw); err != nil {
		redirectMetricsValidationError(w, r, c.Username, err)
		return
	}
	res := derpcfg.ResolveDebug(s.dbc())
	verb := "save"
	if clear {
		verb = "clear"
	}
	s.Backend.Audit(c.UserID, c.Username, "derp.metrics_endpoint",
		fmt.Sprintf("action=%s value=%q effective=%q source=%s", verb, raw, res.Host, res.Source))
	if clear {
		http.Redirect(w, r, "/admin/derp?mok=cleared", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/derp?mok=saved", http.StatusFound)
}

// redirectMetricsValidationError turns a derpcfg validation failure into the
// ?merr=<code> flash the template translates, and audits the refusal (a
// rejected value must be visible in the journal too, not only on the page).
func redirectMetricsValidationError(w http.ResponseWriter, r *http.Request, username string, err error) {
	code := "invalid"
	var ve *derpcfg.ValidationError
	if errors.As(err, &ve) {
		code = ve.Code
	}
	log.Printf("derp.metrics_endpoint: refusing value (admin=%s): %v", username, err)
	http.Redirect(w, r, "/admin/derp?merr="+url.QueryEscape(code), http.StatusFound)
}

// truncateForQuery keeps a flash detail inside a sane URL length.
func truncateForQuery(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
