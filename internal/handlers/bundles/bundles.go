// Package bundles holds static assets that are embedded
// into the skygate binary and exposed via HTTP endpoints.
//
// The setup.sh and README.md files here are the source of
// truth that /admin/users/<id>/subnet → "Download bundle"
// returns to the user. They are copies of the files in
// deploy/subnet-router/ (which the user can fetch
// directly from GitHub raw URLs).
//
// **Sync requirement**: this directory is a copy. Whenever
// deploy/subnet-router/setup.sh or README.md changes, run
// `make sync-bundles` (or just `cp
// deploy/subnet-router/setup.sh internal/handlers/bundles/`
// and the same for README.md) before commit. CI runs a
// `make check-bundles` target that fails the build if the
// copies drift.
package bundles

import _ "embed"

//go:embed setup.sh
var SetupScript string

//go:embed README.md
var BundleReadme string
