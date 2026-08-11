// Package feature is the namespace for top-level feature modules in skygate.
// Each subdirectory of this package is a single user-facing feature, owning
// its HTTP handlers, business logic, DB queries, templates, i18n keys, and
// tests. Cross-feature dependencies go through other features' service
// layers (not handlers or templates).
//
// See docs/plans/refactor-v0.30.md for the full plan.
package feature
