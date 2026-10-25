package admin

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// B417 (2026-10-24) — a relay skygate registered must not sit there advertising
// `0.0.0.0/0` with the route unapproved: that is the state the operator met after a
// provisioning run lost its last step to a proxy timeout, and the page could only
// answer with a red banner and a button («Нажмите Tag as exit-node»). Turning a VPS
// into a working exit node is skygate's job, so skygate finishes it.
//
// SCOPE, decided by the operator on 2026-10-24: only the BASE routes
// (0.0.0.0/0 and ::/0), and only for nodes that have a row in exit_servers — i.e.
// the relays skygate itself was told to register. Subnet routes and any node skygate
// did not register are NEVER approved here; those stay an explicit operator action.

// BaseRouteReconcileInterval is how often the pass runs. Two minutes: long enough
// that a just-registered node has advertised its routes, short enough that the
// operator does not reach for the button.
const BaseRouteReconcileInterval = 2 * time.Minute

// baseRoutes are the ONLY routes this pass will ever approve.
var baseRoutes = []string{"0.0.0.0/0", "::/0"}

// baseRouteRepairDecision is the pure decision: repair only when the node belongs to
// an exit_servers row (skygate-registered), is online, advertises a base route, and
// headscale has not approved it. Any other combination is left alone.
func baseRouteRepairDecision(rowHost string, n headscale.NodeView) bool {
	if strings.TrimSpace(rowHost) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(n.Hostname), strings.TrimSpace(rowHost)) {
		return false
	}
	if !n.Online {
		return false
	}
	if !containsRoute(n.AvailableRoutes, "0.0.0.0/0") && !containsRoute(n.AvailableRoutes, "::/0") {
		return false
	}
	return !containsRoute(n.ApprovedRoutes, "0.0.0.0/0") || !containsRoute(n.ApprovedRoutes, "::/0")
}

func containsRoute(routes []string, want string) bool {
	for _, r := range routes {
		if strings.TrimSpace(r) == want {
			return true
		}
	}
	return false
}

// baseRouteRepairState dedups the failure log so a permanently broken relay does not
// write a line every two minutes (the success path notifies once, through the sink).
type baseRouteRepairState struct {
	mu        sync.Mutex
	lastError map[string]time.Time
}

const baseRouteRepairErrorInterval = 30 * time.Minute

func (st *baseRouteRepairState) shouldLog(host string, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.lastError == nil {
		st.lastError = map[string]time.Time{}
	}
	if t, ok := st.lastError[host]; ok && now.Sub(t) < baseRouteRepairErrorInterval {
		return false
	}
	st.lastError[host] = now
	return true
}

var baseRouteState baseRouteRepairState

// RunBaseRouteReconcile performs ONE pass: it approves the base routes of every
// skygate-registered relay that is online, advertising and unapproved. Returns how
// many relays were inspected and repaired (the counts are what the page/log say).
func (s *Service) RunBaseRouteReconcile() (inspected, repaired int) {
	hs := s.HSGlobalFn()
	if hs == nil {
		return 0, 0
	}
	rows, err := db.ListExitServers(s.dbc())
	if err != nil {
		if baseRouteState.shouldLog("(list)", time.Now()) {
			log.Printf("[b417] base-route reconcile: ListExitServers: %v", err)
		}
		return 0, 0
	}
	if len(rows) == 0 {
		return 0, 0
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		if baseRouteState.shouldLog("(headscale)", time.Now()) {
			log.Printf("[b417] base-route reconcile: ListAllNodes: %v", err)
		}
		return 0, 0
	}
	inspected = len(rows)
	for _, row := range rows {
		for _, n := range nodes {
			if !baseRouteRepairDecision(row.Hostname, n) {
				continue
			}
			id, perr := strconv.ParseInt(strings.TrimSpace(n.ID), 10, 64)
			if perr != nil {
				continue
			}
			approved, aerr := hs.ApproveRoutesForNodeID(id, baseRoutes)
			if aerr != nil {
				if baseRouteState.shouldLog(row.Hostname, time.Now()) {
					log.Printf("[b417] base-route approve %s (id=%d) failed: %v", row.Hostname, id, aerr)
					s.Backend.Audit(0, "skygate", "exit_node_base_route_autorepair",
						"host="+row.Hostname+" id="+n.ID+" err="+aerr.Error())
				}
				continue
			}
			repaired++
			hs.InvalidateCache()
			log.Printf("[b417] base routes auto-approved for %s (id=%d, approved=%d)", row.Hostname, id, approved)
			s.Backend.Audit(0, "skygate", "exit_node_base_route_autorepair",
				"host="+row.Hostname+" id="+n.ID+" approved="+strconv.Itoa(approved))
			s.notifyBaseRouteRepair(row.Hostname, approved)
			break
		}
	}
	return inspected, repaired
}

// notifyBaseRouteRepair tells the operator that skygate finished the job on its own.
func (s *Service) notifyBaseRouteRepair(host string, approved int) {
	if s.Notifier == nil {
		return
	}
	msg := "🩹 exit-node " + host + ": skygate сам одобрил базовые маршруты 0.0.0.0/0 и ::/0 в headscale " +
		"(было не одобрено — клиенты не получали выход). Одобрено маршрутов: " + strconv.Itoa(approved) + "."
	go s.Notifier.SendAlert(msg)
}

// StartBaseRouteReconciler runs the pass in the background until ctx is cancelled.
// It returns the stop function so main can shut it down with the process.
func (s *Service) StartBaseRouteReconciler(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(BaseRouteReconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// B416.1: a run interrupted by a restart claims to be running forever.
				// Checked on the same tick, before the base-route pass, because a resumed
				// run is what registers the exit_servers row that pass needs.
				s.ResumeInterruptedProvisionJob(time.Now())
				s.RunBaseRouteReconcile()
				// B417.1: approval is not egress — probe the relays themselves, slowly.
				s.runHealthCheckIfDue(ctx)
			}
		}
	}()
	return cancel
}
