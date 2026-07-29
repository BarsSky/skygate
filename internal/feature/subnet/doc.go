// Package subnet is the feature module for per-user subnet management:
// /admin/users/{id}/subnet, /admin/subnets, /my/devices subnet card, and the
// /mysubnet bot command.
//
// Note: this is the *feature* layer; the existing internal/subnet/ package
// holds the underlying allocator, manager, and shares logic. The feature
// layer will own the HTTP handlers, templates, and per-feature i18n keys
// and import internal/subnet/ for data access.
//
// Refactor status: Phase A (2026-07-29) — feature-module scaffolding only.
// See docs/plans/refactor-v0.30.md.
package subnet
