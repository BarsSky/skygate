// Package auth is the feature module for authentication surfaces:
// /login, /logout, /my/account (self-service password change),
// /my/tokens (personal API tokens).
//
// Note: this is the *feature* layer; the existing internal/auth/ package
// holds the JWT primitives (signing, parsing, validation) and Bearer-token
// helpers. The feature layer owns the HTTP handlers and forms.
//
// Refactor status: Phase A (2026-07-29) — feature-module scaffolding only.
// See docs/plans/refactor-v0.30.md.
package auth
