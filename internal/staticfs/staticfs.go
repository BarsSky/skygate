// Package staticfs embeds the project's static/ directory into the binary
// so that the prebuilt Docker image (ghcr.io/barssky/skygate:*) can serve
// /static/* and /favicon.svg without needing static/ on disk at runtime.
//
// 2026-09-11 (B-mod-static-embed): Pre-change, internal/handlers/static.go
// used http.ServeFile(w, r, "./static/"+clean) — required the ./static/
// directory at the binary's working directory. The prebuilt image (built
// via Dockerfile.prebuilt) did NOT COPY static/, so every /static/*
// request returned 404 — broken UI for operators who deployed via the
// recommended prebuilt path.
//
// This package lives at the repo root (next to static/) because Go's
// //go:embed directive requires the embedded files to be in the same
// directory as the source file. The internal/handlers package imports
// staticfs.FS and passes it to http.ServeFileFS.
//
// The dev path (./:/app bind-mount) still works because the embed.FS
// is built from the same source tree — the binary always has the
// embedded copy regardless of whether the host also has static/.
package staticfs

import "embed"

// FS is the embedded static/ directory tree. Mount at /static/ in the
// HTTP mux; the handler strips the /static/ prefix and serves files
// relative to the FS root (which is "static/" inside the FS, since the
// embed preserves the directory name).
//
//go:embed static
var FS embed.FS
