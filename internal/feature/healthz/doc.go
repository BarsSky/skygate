// Package healthz is the feature module for liveness/readiness probes:
// GET /healthz (process alive) and GET /readyz (DB + headscale reachable).
// Also exposes the build label (version + commit) for deploy verification
// (R3 in the guarantee catalog).
//
// Refactor status: Phase A (2026-07-29) — feature-module scaffolding only.
// Will be the FIRST feature moved in Phase B step 1 (smallest, no deps).
// See docs/plans/refactor-v0.30.md.
package healthz
