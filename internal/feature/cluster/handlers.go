// Package cluster — handlers.go owns the HTTP API
// for the cluster join / heartbeat flow.
//
// v1.5.0+ / B201 — Phase 2.3 of
// docs/internal/cluster-management.md.
//
// Two machine-to-machine POST endpoints (no admin /
// user session — the sgn1 token IS the auth):
//
//   POST /api/cluster/join
//     Request  : JSON {token, hostname, tailscale_ip,
//                      skygate_version, roles}
//     Response 200: JSON {cluster_id, node_id, hostname,
//                         dsn_template, dbname, db_username,
//                         heartbeat_seconds}
//     Errors: 400 (bad JSON), 401 (bad token), 403
//     (hostname mismatch), 409 (invite used),
//     410 (invite expired or revoked), 500 (db).
//
//   POST /api/cluster/heartbeat
//     Request  : JSON {node_id, token}
//     Response 200: JSON {node_id, state, last_seen_unix,
//                         next_heartbeat_seconds,
//                         heartbeats_until_stale}
//     Errors: 400 (bad JSON), 401 (bad token or
//     token/node mismatch), 404 (node not found),
//     500 (db).
//
// Both endpoints are JSON in / JSON out (NOT HTML).
// The error response shape is always
// `{"error": "<message>"}` so the new node can log
// the message verbatim.
//
// Why no auth middleware: the sgn1 token is the
// authentication. The token's HMAC signature proves
// it was issued by this skygate; the payload's
// target_hostname + the used_by_node_id on the
// cluster_invite row are the authorization. A future
// improvement would be to also rate-limit per token
// (a stolen token can spam heartbeats).

package cluster

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"skygate/internal/cluster"
)

// Service is the API service. It holds the same
// *sql.DB as the rest of skygate, plus the invite
// signing secret (the same SKYGATE_SECRET_KEY used
// for JWT signing).
type Service struct {
	DB              *sql.DB
	InviteSecret    string
}

// NewService returns a Service. Both fields are
// required — DB for the cluster_* queries, InviteSecret
// for the HMAC key.
func NewService(d *sql.DB, inviteSecret string) *Service {
	return &Service{DB: d, InviteSecret: inviteSecret}
}

// writeJSON writes a JSON response with the given
// status code. Always sets Content-Type.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON reads + decodes a JSON request body.
// Returns a 400 on parse failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "empty request body")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// PostAPIClusterJoin handles POST /api/cluster/join.
// See package doc for the request/response shape.
func (s *Service) PostAPIClusterJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req cluster.JoinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := cluster.Join(s.DB, s.InviteSecret, &req)
	if err != nil {
		writeJoinError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeJoinError maps the cluster.Join errors to HTTP
// status codes. The message in the body is the
// unwrapped error message (machine-readable).
func writeJoinError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cluster.ErrHostnameMismatch):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, cluster.ErrInviteAlreadyUsed):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, cluster.ErrInviteExpired):
		writeError(w, http.StatusGone, err.Error())
	case errors.Is(err, cluster.ErrInviteRevoked):
		writeError(w, http.StatusGone, err.Error())
	case errors.Is(err, cluster.ErrInviteNotPending):
		writeError(w, http.StatusGone, err.Error())
	case errors.Is(err, cluster.ErrInviteNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		// 401 covers signature mismatch + the
		// "malformed token" cases (all of
		// VerifyToken's errors).
		writeError(w, http.StatusUnauthorized, err.Error())
	}
}

// PostAPIClusterHeartbeat handles POST /api/cluster/heartbeat.
// See package doc for the request/response shape.
func (s *Service) PostAPIClusterHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req cluster.HeartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := cluster.Heartbeat(s.DB, s.InviteSecret, &req)
	if err != nil {
		if errors.Is(err, cluster.ErrHeartbeatNodeNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		// 401 covers signature mismatch + the
		// "token bound to a different node" case.
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
