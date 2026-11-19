package startup

// boot_page_b503.go — the panel answers on its real port BEFORE it is ready to
// serve a page (B269 moved the socket bind to the top of main so the updater can
// verify the build of a process that is still booting). Until this block, EVERY
// request in that window — including a browser navigation to /admin/update — got
// the provisional /healthz JSON:
//
//	{"build":"v1.5.246","phase":"headscale-client","phase_ms":121,"ready":false,
//	 "stage":"boot","startup_stage":"boot","status":"ok","uptime_ms":155}
//
// That is what the operator saw right after applying an update («получил при
// обновлении следующую страницу»). The JSON is correct and transient, but it
// answers a question nobody asked: a person wants to know that the panel is
// restarting, which step it is on, and when to refresh — and a long boot
// (a migration, a slow headscale) otherwise looks like a broken page.
//
// So the provisional handler now CONNEGOTIATES:
//
//   - a browser navigation (GET/HEAD, Accept contains text/html, and the path is
//     not a machine endpoint) gets a small self-refreshing HTML page naming the
//     phase, the elapsed time and the build;
//   - everything else — /healthz, /readyz, /metrics, /api/*, any client that does
//     not ask for HTML (curl sends */*), and every non-GET method — keeps the
//     JSON byte-identical to before.
//
// A machine endpoint is never allowed to render HTML, whatever its Accept header
// says: the updater polls /healthz to decide whether to roll back, and a "looks
// like a page" response would be unparseable JSON.

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

// Machine paths keep the JSON answer even if a browser (or a curious human with a
// text/html Accept header) opens them directly.
var bootPageMachinePaths = []string{"/healthz", "/readyz", "/metrics", "/api/", "/oidc/", "/.well-known/"}

// WantsBootPage reports whether this request is a BROWSER NAVIGATION and should get
// the human page instead of the provisional JSON. Pure function of the request, so
// the negotiation is testable without a server.
func WantsBootPage(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
		return false
	}
	p := r.URL.Path
	if p == "" {
		p = "/"
	}
	for _, m := range bootPageMachinePaths {
		if strings.HasPrefix(p, m) {
			return false
		}
	}
	return true
}

// BootPage renders the self-refreshing page. `fatal` is the recorded startup error
// (empty while the boot is still progressing): a fatal boot keeps answering 503, and
// the page then says so with the reason instead of refreshing forever.
func BootPage() string {
	st.mu.RLock()
	build := st.build
	phase := st.phase
	fatal := st.fatal
	uptime := time.Since(st.started).Round(time.Millisecond)
	phaseFor := time.Since(st.phaseAt).Round(time.Millisecond)
	st.mu.RUnlock()

	refresh := `<meta http-equiv="refresh" content="2">`
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"ru\"><head><meta charset=\"utf-8\">")
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	if fatal == "" {
		b.WriteString(refresh)
	}
	b.WriteString("<title>Skygate — ")
	if fatal != "" {
		b.WriteString("ошибка запуска")
	} else {
		b.WriteString("панель запускается")
	}
	b.WriteString("</title><style>")
	b.WriteString(`body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;`)
	b.WriteString(`font:16px/1.5 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:#0f1720;color:#e6edf3}`)
	b.WriteString(`.card{max-width:40rem;padding:2rem;border-radius:12px;background:#16212b;border:1px solid #24313d}`)
	b.WriteString(`h1{font-size:1.15rem;margin:0 0 .75rem}code{background:#0b1218;padding:.15rem .35rem;border-radius:4px}`)
	b.WriteString(`.muted{color:#93a4b5;font-size:.9rem;margin-top:1rem}.err{color:#ff9b9b}`)
	b.WriteString(`</style></head><body><div class="card">`)

	if fatal != "" {
		b.WriteString(`<h1 class="err">Панель не запустилась</h1>`)
		b.WriteString(`<p class="muted">Startup failed — the reason below is from skygate's own journal.</p>`)
		fmt.Fprintf(&b, "<p><code>%s</code></p>", html.EscapeString(fatal))
		fmt.Fprintf(&b, `<p class="muted">build <code>%s</code> · шаг <code>%s</code> · %s</p>`,
			html.EscapeString(build), html.EscapeString(phase), html.EscapeString(uptime.String()))
		b.WriteString(`<p class="muted">Проверьте журнал: <code>journalctl -u skygate -n 100</code>. `)
		b.WriteString(`Страница не обновляется сама, пока ошибка не устранена.</p>`)
	} else {
		b.WriteString("<h1>Панель перезапускается</h1>")
		b.WriteString(`<p>The panel is starting up — this page refreshes itself every 2 seconds.</p>`)
		fmt.Fprintf(&b, `<p>Текущий шаг: <code>%s</code> (%s)<br>Всего: %s · build <code>%s</code></p>`,
			html.EscapeString(phase), html.EscapeString(phaseFor.String()),
			html.EscapeString(uptime.String()), html.EscapeString(build))
		b.WriteString(`<p class="muted">Машиночитаемое состояние — <code>/healthz</code> (JSON, отдаётся всегда).</p>`)
	}
	b.WriteString("</div></body></html>")
	return b.String()
}

// serveBootPage writes the human page. Status follows StatusCode() so a fatal boot
// is still a 503 for anything that only looks at the code.
func serveBootPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(StatusCode())
	if r != nil && r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(BootPage()))
}
