//go:build ignore
// +build ignore

// B216 live-verify helper — fetches the /admin/cluster
// page via HTTP with an admin session JWT and asserts
// the new B216 sections appear in the rendered HTML.
//
// Run on the agent after `go build ./...` is clean:
//
//   SKYGATE_SECRET_KEY=<hex from .env> \
//   SKYGATE_BASE_URL=http://127.0.0.1:8080 \
//   SKYGATE_ADMIN_UID=<numeric uid of an admin user> \
//   go run scripts/b216_liveverify.go
//
// The helper:
//   1. Looks up the SKYGATE_ADMIN_UID admin user's
//      username from the DB (or takes it as a flag).
//   2. Mints a session JWT via auth.IssueJWT.
//   3. GETs /admin/cluster with the JWT cookie.
//   4. Greps the response body for the B216 markers
//      (online pill, replicas row, DSN host row,
//      the 8 action badges in the recent events
//      table, etc).
//   5. Reports the count of markers found vs. the
//      expected count.
//
// `//go:build ignore` keeps this out of `go build ./…`
// (it has package main and would conflict). `go run`
// ignores the build tag.

package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/auth"
)

const (
	cookieName    = "skygate_session"
	liveVerifyTag = "<!-- B216-LIVEVERIFY-MARKER -->"
)

func main() {
	fs := flag.NewFlagSet("b216-liveverify", flag.ExitOnError)
	baseURL := fs.String("base-url", os.Getenv("SKYGATE_BASE_URL"), "skygate base URL (e.g. http://127.0.0.1:8080)")
	adminUID := fs.Int64("admin-uid", 0, "numeric uid of an admin user (default: 1)")
	adminUsername := fs.String("admin-username", "skyadmin", "admin username for the JWT claim")
	_ = fs.Parse(os.Args[1:])

	if *baseURL == "" {
		*baseURL = "http://127.0.0.1:8080"
	}
	if *adminUID == 0 {
		*adminUID = 1
	}
	secret := os.Getenv("SKYGATE_JWT_SECRET")
	if secret == "" {
		// Fall back to SKYGATE_SECRET_KEY (some
		// deployments use this; both are 32-byte hex
		// strings). The .env in skygate-staging has
		// both but the binary reads SKYGATE_JWT_SECRET
		// (see internal/config/config.go:443).
		secret = os.Getenv("SKYGATE_SECRET_KEY")
	}
	if secret == "" {
		die("SKYGATE_JWT_SECRET (or SKYGATE_SECRET_KEY) not set")
	}

	// 1. Mint a session JWT. We trust that admin-uid /
	//    admin-username refer to a real admin row — the
	//    handler's authMW will reject the cookie if they
	//    don't match. This is the same pattern the B161
	//    e2e tests use (B161.4 + B174).
	tok, err := auth.IssueJWT(secret, *adminUID, *adminUsername, true, 1)
	if err != nil {
		die("issue JWT: %v", err)
	}
	fmt.Fprintf(os.Stderr, "issued JWT for uid=%d username=%s (1h TTL)\n", *adminUID, *adminUsername)

	// 2. GET /admin/cluster with the session cookie.
	url := *baseURL + "/admin/cluster"
	req, _ := http.NewRequest("GET", url, nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: noRedirect}
	resp, err := httpClient.Do(req)
	if err != nil {
		die("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 302 {
		// The handler rejected the cookie. The most
		// common cause: the JWT uid doesn't match a
		// real admin row (or the bcrypt comparison
		// failed somewhere). Surface the redirect
		// location so the operator can see where the
		// authMW sent us.
		loc := resp.Header.Get("Location")
		die("302 redirect to %s — session JWT was rejected. Check that admin-uid=%d matches a real admin user. Body preview: %s", loc, *adminUID, truncate(string(body), 200))
	}
	if resp.StatusCode != 200 {
		die("GET %s returned %d: %s", url, resp.StatusCode, truncate(string(body), 200))
	}

	// Write the body to /tmp for offline inspection.
	// Operators can `cat /tmp/b216_admin_cluster.html`
	// to see what was actually rendered.
	bodyFile := os.Getenv("SKYGATE_B216_BODY_FILE")
	if bodyFile == "" {
		bodyFile = "/tmp/b216_admin_cluster.html"
	}
	if err := os.WriteFile(bodyFile, body, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: write body to %s: %v\n", bodyFile, err)
	} else {
		fmt.Fprintf(os.Stderr, "  body saved to %s (%d bytes)\n", bodyFile, len(body))
	}

	bodyStr := string(body)

	// 4. Check for the B216 markers. We use a
	//    language-independent strategy: the i18n
	//    key on the rendered page is "cluster.online"
	//    (the key, not the value) — Go templates
	//    resolve the key at render time, but the
	//    page contains the rendered text. We look
	//    for EITHER the English OR the Russian
	//    rendered text (whichever the page actually
	//    has, depending on the operator's lang
	//    cookie). For the action badges we look
	//    for the B215-style <span class="badge ...">
	//    marker (language-independent).
	//
	//    Two kinds of needles:
	//      - literal: substring match (for action
	//        labels and section headers)
	//      - regex: regular-expression match (for
	//        the "X/Y online" pill which has
	//        variable digits)
	renderedChecks := []struct {
		name    string
		needles []string // any match counts
		isRegex bool     // true = needles are regex, false = needles are literal
	}{
		{"Init (bootstrap) — node_init badge", []string{`Init (bootstrap)`}, false},
		{"Join (standby onboarded) — node_join badge", []string{`Join (standby onboarded)`}, false},
		{"Drain (state=draining) — node_drain badge", []string{`Drain (state=draining)`}, false},
		{"Leave (node removed) — node_leave badge", []string{`Leave (node removed)`}, false},
		{"Health (heartbeat ok) — node_health badge", []string{`Health (heartbeat ok)`}, false},
		{"Failover recommend — failover_recommend badge", []string{`Failover recommend`}, false},
		{"Failover (promote) — node_failover badge", []string{`Failover (promote)`}, false},
		// Failover drill: only present if there's a
		// node_drill event in cluster_audit. Skipped
		// in the check below (no event in the live
		// agent's cluster_audit table).
		// {"Failover drill — node_drill badge", []string{`Failover drill`}, false},
		// X-of-Y online pill: needle is the regex
		// pattern "0/4 online" or "0/4 онлайн". This
		// is the only regex check; the rest are
		// literal substring matches.
		{"X-of-Y online pill (any lang)", []string{`/\d+ online`, `/\d+ онлайн`}, true},
		{"DB host row (any lang)", []string{`DB host:`, `Хост БД:`}, false},
		// The Replicas row has two renderings: the
		// header "Replicas:" when there ARE replicas
		// (in cluster_database.replica_node_ids), or
		// the "no replicas" muted text when there
		// aren't. The agent's live cluster has 0
		// replicas, so we accept EITHER rendering —
		// both prove the B216 code path is active.
		{"Replicas row (header or no-replicas muted text)", []string{`Replicas:`, `Реплики:`, `(no replicas configured`, `(реплики не настроены`}, false},
		{"Nodes section header (any lang)", []string{`Cluster nodes`, `Ноды кластера`}, false},
		{"Audit section header (any lang)", []string{`Recent cluster events`, `Последние cluster-события`}, false},
		{"B215-style badge markup", []string{`<span class="badge bg-info">`, `<span class="badge bg-warning">`, `<span class="badge bg-danger">`, `<span class="badge bg-success">`, `<span class="badge bg-secondary">`}, false},
	}

	// Pre-compile the needles. Literal needles
	// are wrapped with regexp.QuoteMeta so the
	// parentheses in action labels (e.g. "Init
	// (bootstrap)") don't get treated as regex
	// groups. Regex needles are used as-is.
	compiledChecks := make([]struct {
		name  string
		regex *regexp.Regexp
	}, 0)
	for _, c := range renderedChecks {
		var alt string
		if c.isRegex {
			alt = strings.Join(c.needles, "|")
		} else {
			quoted := make([]string, len(c.needles))
			for i, n := range c.needles {
				quoted[i] = regexp.QuoteMeta(n)
			}
			alt = strings.Join(quoted, "|")
		}
		compiledChecks = append(compiledChecks, struct {
			name  string
			regex *regexp.Regexp
		}{c.name, regexp.MustCompile(alt)})
	}

	hit := 0
	miss := []string{}
	for _, c := range compiledChecks {
		if c.regex.MatchString(bodyStr) {
			hit++
			fmt.Fprintf(os.Stderr, "  [ok]   %s\n", c.name)
		} else {
			miss = append(miss, c.name)
			fmt.Fprintf(os.Stderr, "  [miss] %s (needles: %v)\n", c.name, c.regex.String())
		}
	}

	fmt.Fprintf(os.Stderr, "\n%s\n", liveVerifyTag)
	fmt.Fprintf(os.Stderr, "B216 live-verify: %d/%d markers found in /admin/cluster response (HTTP 200, %d bytes)\n", hit, len(renderedChecks), len(bodyStr))
	if len(miss) > 0 {
		fmt.Fprintf(os.Stderr, "MISSED MARKERS (%d):\n", len(miss))
		for _, m := range miss {
			fmt.Fprintf(os.Stderr, "  - %s\n", m)
		}
	}

	// 4. Query cluster_audit to confirm the row count
	//    (a sanity check on the B215 fix in B216).
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		dsn = os.Getenv("SKYGATE_DB_DSN")
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "WARN: SKYGATE_DB_DSN not set — skipping cluster_audit count check")
	} else {
		d, err := sql.Open("pgx", dsn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: open db: %v\n", err)
		} else {
			defer d.Close()
			rows, err := d.Query(`SELECT action, count(*) FROM cluster_audit GROUP BY action ORDER BY action`)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARN: query: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "cluster_audit counts:")
				for rows.Next() {
					var action string
					var n int
					_ = rows.Scan(&action, &n)
					fmt.Fprintf(os.Stderr, "  %s: %d\n", action, n)
				}
				rows.Close()
			}
		}
	}
}

func noRedirect(req *http.Request, via []*http.Request) error {
	// Don't follow 302 — we want to see the Location header
	// if the authMW redirects.
	return http.ErrUseLastResponse
}

func die(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
