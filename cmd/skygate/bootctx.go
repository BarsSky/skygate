// bootctx.go — B526 (2026-11-27): the objects every late boot phase needs.
//
// main() was one 2 272-line script, and the phases inside it shared a dozen locals, so
// lifting a block out meant threading six parameters through it (the headscale monitor
// already needed ctx, cfg, hs, app, d, rn). This struct is that thread, once: main()
// builds it as soon as its members exist, and every phase becomes a method that takes
// nothing. Add a field when a phase genuinely needs a new object — never a parameter list.
package main

import (
	"context"

	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/telegram"
)

// bootCtx is the boot sequence's working set. Fields are the objects main() constructs;
// a phase reads them and must not replace them.
type bootCtx struct {
	ctx context.Context
	cfg *config.Config
	db  *db.ResettableDB
	hs  *headscale.Client
	app *handlers.App
	rn  *telegram.RealNotifier
}
