// B464 (2026-11-11) — the heartbeat body grows; the endpoint must not punish that.
//
// The heartbeat is the only machine endpoint whose request body gains fields over
// time: B402 added `version`, B464 adds `readiness`. It is also the only one where
// rejecting a body costs a NODE ITS STATE — three rejected beats and the elector
// flips a healthy host to `failed`. So an older hub must ignore a field it does not
// know, while /join (a one-shot, operator-driven call) keeps the strict decoder that
// catches a genuine contract mismatch.
package cluster

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

// TestB464_HeartbeatToleratesUnknownFields — a NEWER agent talking to an older hub.
// The unknown field must be ignored, not turned into `400 invalid JSON`, because a
// node that cannot beat is a node the elector declares failed.
func TestB464_HeartbeatToleratesUnknownFields(t *testing.T) {
	svc := &Service{DB: skygatedb.FixedDBSource{DB: nil}, InviteSecret: ""}
	body := `{"node_id":"node-disc-x","token":"sgn1.e30.x","version":"v1.5.210+dd2f623b",` +
		`"readiness":{"serving":true,"ready":true},"a_field_from_the_future":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/heartbeat", strings.NewReader(body))
	rec := httptest.NewRecorder()
	svc.PostAPIClusterHeartbeat(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatalf("heartbeat answered 400 to an unknown field (%s) — an older hub would mark a newer agent failed", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("strict decoder still in use on the heartbeat: %s", rec.Body.String())
	}
}

// TestB464_HeartbeatDecodesReadiness — the new block must actually arrive in the
// request struct; a field that is accepted but never decoded is worse than one that
// is rejected, because everything downstream looks like "the agent sent nothing".
func TestB464_HeartbeatDecodesReadiness(t *testing.T) {
	var req struct {
		Readiness *struct {
			Serving   bool   `json:"serving"`
			Ready     bool   `json:"ready"`
			HealthURL string `json:"health_url"`
		} `json:"readiness"`
	}
	body := `{"readiness":{"serving":true,"ready":true,"health_url":"http://127.0.0.1:8080/healthz"}}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Readiness == nil || !req.Readiness.Serving || req.Readiness.HealthURL == "" {
		t.Fatalf("readiness block lost in decoding: %+v", req.Readiness)
	}
}

// TestB464_JoinStaysStrict — the counter-contract. Loosening the heartbeat must not
// loosen /join: a caller sending a field the join contract does not have is telling
// us its request is wrong, and a 400 there is the correct answer.
func TestB464_JoinStaysStrict(t *testing.T) {
	svc := &Service{DB: skygatedb.FixedDBSource{DB: nil}, InviteSecret: ""}
	body := `{"token":"sgn1.e30.x","hostname":"h","tailscale_ip":"100.64.0.2","skygate_version":"v1",` +
		`"roles":"skygate-standby","a_field_from_the_future":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/join", strings.NewReader(body))
	rec := httptest.NewRecorder()
	svc.PostAPIClusterJoin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("join answered %d to an unknown field, want 400 (the join contract stays strict)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("join body = %s, want the strict decoder's complaint", rec.Body.String())
	}
}
