// internal/deployrun/handlers.go — HTTP handlers for
// /admin/deploys (list + single + SSE stream + new).
//
// Routes (Go 1.22+ ServeMux method+path patterns):
//   GET  /admin/deploys                 — list of recent runs
//   GET  /admin/deploys/{id}            — single run + live UI
//   GET  /admin/deploys/{id}/stream     — SSE event stream
//   POST /admin/deploys                 — start a new run (form action)
//
// Handlers follow the existing admin pattern: a
// service struct that the framework instantiates at
// boot. The framework registers the routes in
// cmd/skygate/main.go.

package deployrun

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"skygate/internal/headscale"
)

// Service is the deployrun feature module. One Service
// is created at boot by cmd/skygate/main.go.
//
// All fields are read-only after construction.
type Service struct {
	DB        DBExec
	HSFactory HSClientFactory
	S3Factory S3ClientFactory
	Cfg       *Config
	Catalog   Translator
	// brokers tracks active SSEBroker instances by run
	// ID. Created when a run starts, closed + removed
	// when the run finishes. The SSE handler looks up
	// the broker by run ID and subscribes.
	brokersMu sync.RWMutex
	brokers   map[int64]*SSEBroker
}

// DBExec is the minimal DB interface the handlers need
// for redirects / session lookups. The real impl is
// the *sql.DB wrapper. *sql.DB satisfies this
// interface via the buildDBWrapper in main.go.
type DBExec interface{}

// Translator is the minimal i18n interface the
// handlers need. Real impl is the i18n catalog.
type Translator interface {
	T(lang, key string, args ...interface{}) string
}

// NewService constructs a Service. Called once at boot.
func NewService(db DBExec, hsFactory HSClientFactory, s3Factory S3ClientFactory, cfg *Config, catalog Translator) *Service {
	return &Service{
		DB:        db,
		HSFactory: hsFactory,
		S3Factory: s3Factory,
		Cfg:       cfg,
		Catalog:   catalog,
		brokers:   map[int64]*SSEBroker{},
	}
}

// setBroker stores the broker for a run ID. Called by
// PostAdminDeploys after the run starts.
func (s *Service) setBroker(runID int64, broker *SSEBroker) {
	s.brokersMu.Lock()
	defer s.brokersMu.Unlock()
	s.brokers[runID] = broker
}

// clearBroker removes the broker for a run ID. Called
// after the run finishes (close + remove).
func (s *Service) clearBroker(runID int64) {
	s.brokersMu.Lock()
	defer s.brokersMu.Unlock()
	if b, ok := s.brokers[runID]; ok {
		b.Close()
		delete(s.brokers, runID)
	}
}

// getBroker fetches the broker for a run ID.
func (s *Service) getBroker(runID int64) *SSEBroker {
	s.brokersMu.RLock()
	defer s.brokersMu.RUnlock()
	return s.brokers[runID]
}

// GetAdminDeploys — list page (HTML).
func (s *Service) GetAdminDeploys(w http.ResponseWriter, r *http.Request) {
	fw := s.buildFramework()
	ctx := r.Context()
	runs, err := fw.LoadRecentRuns(ctx, 50)
	if err != nil {
		http.Error(w, "load runs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Auto-Deploy History</title></head><body>`)
	fmt.Fprintf(w, `<h1>Auto-Deploy History</h1><p>%d run(s)</p>`, len(runs))
	fmt.Fprintf(w, `<table border="1" cellpadding="4"><tr><th>#</th><th>Type</th><th>Hostname</th><th>Status</th><th>Started</th><th>Finished</th><th>Error</th></tr>`)
	for _, run := range runs {
		finished := "—"
		if !run.FinishedAt.IsZero() {
			finished = run.FinishedAt.Format("2006-01-02 15:04:05")
		}
		errCell := ""
		if run.Error != "" {
			errCell = fmt.Sprintf(`<span style="color:red">%s</span>`, run.Error)
		}
		fmt.Fprintf(w, `<tr><td><a href="/admin/deploys/%d">%d</a></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			run.ID, run.ID, run.Type, run.Hostname, run.Status,
			run.StartedAt.Format("2006-01-02 15:04:05"), finished, errCell)
	}
	fmt.Fprintf(w, `</table>`)
	fmt.Fprintf(w, `</body></html>`)
}

// extractID parses the {id} path segment from the URL.
// ServeMux in Go 1.22+ doesn't auto-extract path
// parameters; the path looks like
// /admin/deploys/42 or /admin/deploys/42/stream.
// We strip the known prefix/suffix to get the integer.
func extractID(path string) (int64, string) {
	const prefix = "/admin/deploys/"
	if !strings.HasPrefix(path, prefix) {
		return 0, ""
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return 0, ""
	}
	id, err := parseInt(parts[0])
	if err != nil {
		return 0, ""
	}
	if len(parts) == 1 {
		return id, ""
	}
	return id, parts[1]
}

func parseInt(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %s", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// GetAdminDeployRun — single run page. Handles both
// /admin/deploys/{id} (page) and /admin/deploys/{id}/stream (SSE).
func (s *Service) GetAdminDeployRun(w http.ResponseWriter, r *http.Request) {
	id, suffix := extractID(r.URL.Path)
	if id == 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if suffix == "stream" {
		s.streamRun(w, r, id)
		return
	}
	fw := s.buildFramework()
	ctx := r.Context()
	run, steps, err := fw.LoadRun(ctx, id)
	if err != nil {
		http.Error(w, "load run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Deploy Run #%d</title></head><body>`, run.ID)
	fmt.Fprintf(w, `<h1>Deploy Run #%d</h1>`, run.ID)
	fmt.Fprintf(w, `<p>Status: <b>%s</b> Type: %s Hostname: <code>%s</code> Operator: %s Started: %s</p>`,
		run.Status, run.Type, run.Hostname, run.Operator,
		run.StartedAt.Format("2006-01-02 15:04:05"))
	if run.Error != "" {
		fmt.Fprintf(w, `<p style="color:red"><b>Error:</b> %s</p>`, run.Error)
	}
	fmt.Fprintf(w, `<h2>Steps</h2>`)
	fmt.Fprintf(w, `<div id="steps">`)
	for _, st := range steps {
		statusColor := "black"
		switch st.Status {
		case "success":
			statusColor = "green"
		case "failed", "rolled_back":
			statusColor = "red"
		case "skipped":
			statusColor = "orange"
		}
		fmt.Fprintf(w, `<div class="step"><b>[%s]</b> <code>%s</code> (%dms)<br>`,
			st.Status, st.StepName, st.DurationMs)
		fmt.Fprintf(w, `<pre style="color:%s">%s</pre>`, statusColor, escapeHTML(joinLogs(st.Logs)))
		if st.Error != "" {
			fmt.Fprintf(w, `<p style="color:red">Error: %s</p>`, escapeHTML(st.Error))
		}
		fmt.Fprintf(w, `</div>`)
	}
	fmt.Fprintf(w, `</div>`)
	fmt.Fprintf(w, `<p><a href="/admin/deploys">← Back to history</a> | <a href="/admin/deploys/%d/stream">SSE stream</a></p>`, run.ID)
	fmt.Fprintf(w, `</body></html>`)
}

// streamRun is the SSE endpoint. It subscribes to the
// run's broker (if still active) and streams events to
// the browser. If the run is already complete, it
// emits a single "broker_closed" event so the browser
// can refresh the page.
func (s *Service) streamRun(w http.ResponseWriter, r *http.Request, id int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	broker := s.getBroker(id)
	if broker == nil {
		// Run is complete (broker already cleared)
		// or never had one (e.g. legacy run from
		// before B194.1). Emit a closed event and exit.
		fmt.Fprintf(w, "event: closed\ndata: {\"run_id\":%d,\"timestamp\":\"%s\"}\n\n",
			id, time.Now().UTC().Format(time.RFC3339))
		flusher.Flush()
		return
	}

	subID, subCh := broker.Subscribe()
	defer broker.Unsubscribe(subID)

	// Send an initial "connected" event so the browser
	// can confirm the SSE pipe is live.
	fmt.Fprintf(w, "event: connected\ndata: {\"run_id\":%d,\"subscriber_id\":%d}\n\n",
		id, subID)
	flusher.Flush()

	// Heartbeat: send a "ping" every 15s so the
	// connection doesn't time out at proxies.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case evt, ok := <-subCh:
			if !ok {
				// Broker closed (run finished).
				fmt.Fprintf(w, "event: closed\ndata: {\"run_id\":%d}\n\n", id)
				flusher.Flush()
				return
			}
			payload, err := MarshalEvent(evt)
			if err != nil {
				continue
			}
			fmt.Fprint(w, payload)
			flusher.Flush()
		}
	}
}

// PostAdminDeploys — start a new run. The form fields
// come from /admin/ha (or /admin/deploys/new). On
// success, the handler:
//   1. Inserts the DeployRun row (status=pending).
//   2. Creates a fresh SSEBroker, stores it in the
//      service's broker map.
//   3. Launches the framework's Run() in a goroutine.
//   4. Redirects to /admin/deploys/{id} for the live UI.
//
// On failure, clears the broker and redirects with
// an error.
func (s *Service) PostAdminDeploys(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	typ := r.FormValue("type")
	if typ == "" {
		typ = "standby"
	}
	stepList := PlanForType(typ)
	if len(stepList) == 0 {
		http.Error(w, "unknown type: "+typ, http.StatusBadRequest)
		return
	}
	hostname := r.FormValue("hostname")
	if hostname == "" {
		http.Error(w, "hostname is required", http.StatusBadRequest)
		return
	}
	run := &DeployRun{
		Type:     typ,
		Status:   RunPending,
		Operator: "admin", // TODO: read from session when authMW is wired
		FormData: encodeFormData(r.Form),
		Hostname: hostname,
	}
	fw := s.buildFramework()
	ctx := r.Context()
	if err := fw.InsertRun(ctx, run); err != nil {
		http.Error(w, "insert run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	broker := NewSSEBroker()
	s.setBroker(run.ID, broker)
	fw.Broker = broker

	// Run the framework in a goroutine. When it
	// returns, clear the broker so the SSE stream
	// gets the "closed" event.
	go func() {
		defer s.clearBroker(run.ID)
		_ = fw.Run(ctx, run, stepList)
	}()

	http.Redirect(w, r, fmt.Sprintf("/admin/deploys/%d", run.ID), http.StatusSeeOther)
}

// GetAdminDeploysNew — render the new-deploy form.
// The form posts to /admin/deploys (PostAdminDeploys).
func (s *Service) GetAdminDeploysNew(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>New Auto-Deploy</title></head><body>`)
	fmt.Fprintf(w, `<h1>Add + auto-deploy standby</h1>`)
	fmt.Fprintf(w, `<p>This runs all 6 B194 steps (validate → preauth → chain → S3 → tag → audit) and shows live progress via SSE.</p>`)
	fmt.Fprintf(w, `<p>Manual SSH bootstrap is still required after the run completes (Phase 2 will automate SSH).</p>`)
	fmt.Fprintf(w, `<form method="POST" action="/admin/deploys" style="display:grid;grid-template-columns:1fr 2fr;gap:8px;max-width:600px">`)
	fmt.Fprintf(w, `<label>Type: <select name="type"><option value="standby" selected>standby</option></select></label>`)
	fmt.Fprintf(w, `<label>Hostname: <input type="text" name="hostname" placeholder="standby-2" required></label>`)
	fmt.Fprintf(w, `<label>Public IP: <input type="text" name="public_ip" placeholder="203.0.113.10" required></label>`)
	fmt.Fprintf(w, `<label>Tailscale IP: <input type="text" name="tailscale_ip" placeholder="100.64.0.X" ></label>`)
	fmt.Fprintf(w, `<label>Priority: <input type="number" name="priority" value="2" min="1" max="100" required></label>`)
	fmt.Fprintf(w, `<label>&nbsp;</label><button type="submit" class="btn btn-success">Start auto-deploy</button>`)
	fmt.Fprintf(w, `</form>`)
	fmt.Fprintf(w, `<p><a href="/admin/deploys">← Back to history</a></p>`)
	fmt.Fprintf(w, `</body></html>`)
}

// buildFramework constructs a fresh framework instance
// from the Service's dependencies. Each call gets a
// new framework (the framework holds per-run state
// only; the Service is reusable).
func (s *Service) buildFramework() *Framework {
	return &Framework{
		Cfg:       s.Cfg,
		HSFactory: s.HSFactory,
		S3Factory: s.S3Factory,
	}
}

// encodeFormData is a placeholder for the JSON
// encoder. In a real impl we'd json.Marshal the
// url.Values.
func encodeFormData(v map[string][]string) string {
	var sb strings.Builder
	sb.WriteString("{")
	first := true
	for k, vs := range v {
		if !first {
			sb.WriteString(",")
		}
		first = false
		sb.WriteString(`"`)
		sb.WriteString(k)
		sb.WriteString(`":[`)
		for i, vv := range vs {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(`"`)
			sb.WriteString(vv)
			sb.WriteString(`"`)
		}
		sb.WriteString(`]`)
	}
	sb.WriteString("}")
	return sb.String()
}

// joinLogs is a small helper for the inline HTML.
func joinLogs(logs []string) string {
	if len(logs) == 0 {
		return "(no logs)"
	}
	var sb strings.Builder
	for _, l := range logs {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}

// escapeHTML escapes the standard HTML entities so
// user-provided strings (hostnames, error messages)
// don't break the rendered page.
func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		`'`, "&#39;",
	)
	return r.Replace(s)
}

// Compile-time guard: headscale.Client fields are
// reachable. The real wiring is in main.go via
// HSFactoryFromFunc(s.Backend.HSGlobalFn()).
var _ = (*headscale.Client)(nil)
