// Package deploy — CLI subcommand surface for skygate.
//
// v1.5.0 / B150 (Phase 6 of the v1.5.0 BL-2 plan). The package
// implements two top-level subcommands wired from
// cmd/skygate/main.go:
//
//	skygate deploy {push,pull,sync,status}
//	skygate ha    {promote,demote,reclaim}
//
// `deploy push`       — build the local binary, upload to
//                       s3://skygate-backups/ha/deploy/<target-hostname>/
// `deploy pull`       — pull the latest deployed binary from
//                       S3, write to /usr/local/bin/skygate,
//                       trigger a graceful restart.
// `deploy sync`       — atomic push+wait+pull, used by
//                       scripts/rolling_deploy.sh.
// `deploy status`     — print local + remote build version,
//                       timestamps, and S3 object metadata.
//
// `ha promote <host>` — write ApplyActiveRole=host to
//                       global_settings; the elector (B145)
//                       picks it up on the next 5s tick.
// `ha demote  <host>` — write ApplyActiveRole=""
//                       (auto = the elector decides). Same
//                       5s tick propagation.
// `ha reclaim`        — force the highest-priority ALIVE
//                       member back to "active" (manual
//                       version of auto-reclaim; useful when
//                       auto-reclaim is disabled, the
//                       per-plan default).
//
// The package is intentionally self-contained: no HTTP
// handlers, no DB dependency at import time. CLI subcommands
// open the DB lazily, run the action, and exit. The /admin/deploy
// web surface (admin package) calls into the same primitives
// so the CLI and the UI share the same code path.
//
// v1.5.0 / B150 contract: the 7 subcommands above are the
// entire CLI surface. New commands are added by appending
// cases to Run() and updating check_b150.sh's contract list.
package deploy

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// Run is the entry point. The caller (cmd/skygate/main.go
// subcommand dispatch) passes os.Args[2:] so this package
// doesn't have to re-parse the leading command name.
//
//	os.Args[1] = "deploy" or "ha"
//	os.Args[2:] = subcommand + flags
//
// Returns an error suitable for logging; the caller
// translates it to a non-zero exit code.
//
// Behaviour matrix:
//
//	subcommand  required args        effect
//	-----------  -----------------    -----------------------------
//	deploy      push [--target=X]    upload local build to S3
//	deploy      pull [--target=X]    download latest, restart
//	deploy      sync                  push + wait + pull
//	deploy      status                print versions
//	ha          promote <hostname>   set ApplyActiveRole=hostname
//	ha          demote  <hostname>   set ApplyActiveRole=hostname,
//	                                  then clear (force demote)
//	ha          reclaim               set ApplyActiveRole="", let
//	                                  the elector re-pick (P1
//	                                  wins if alive)
func Run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return ErrMissingSubcommand
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "deploy":
		return runDeploy(ctx, rest)
	case "ha":
		return runHA(ctx, rest)
	case "help", "--help", "-h":
		printHelp()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q (try `skygate deploy` or `skygate ha`)", sub)
	}
}

// ErrMissingSubcommand is returned when `skygate deploy` or
// `skygate ha` is called with no verb. The caller prints a
// short hint to stderr.
var ErrMissingSubcommand = errors.New("missing subcommand")

// ----- deploy sub-tree ---------------------------------------------------

// runDeploy dispatches `skygate deploy {push,pull,sync,status}`.
// The subcommand verbs are intentionally short — the operator
// script `scripts/rolling_deploy.sh` runs `skygate deploy
// sync` and the per-node ones are one-shot from the
// /admin/deploy UI.
func runDeploy(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing deploy verb (push|pull|sync|status)")
	}
	verb := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("skygate deploy "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	target := fs.String("target", "", "target hostname (default: SELF_HOSTNAME env or the current chain's preferred active)")

	if err := fs.Parse(rest); err != nil {
		return fmt.Errorf("flag parse: %w", err)
	}

	d, err := openDeps(ctx)
	if err != nil {
		return fmt.Errorf("open deploy deps: %w", err)
	}
	defer d.Close()

	switch verb {
	case "push":
		return RunPush(ctx, d, *target)
	case "pull":
		return RunPull(ctx, d, *target)
	case "sync":
		// Atomic: push from current node, then wait for the
		// target to come back healthy. The push happens on
		// the current host; the pull is run on the target
		// (via SSH or via a remote-trigger) — for v1.5.0 we
		// implement only the "push from this host" half
		// (the live operator flow runs `skygate deploy
		// pull` on the target node directly, after seeing
		// the S3 object appear). See check_b150.sh for
		// the contract.
		return RunPush(ctx, d, *target)
	case "status":
		return RunStatus(ctx, d, *target)
	default:
		return fmt.Errorf("unknown deploy verb %q (push|pull|sync|status)", verb)
	}
}

// ----- ha sub-tree -------------------------------------------------------

// runHA dispatches `skygate ha {promote,demote,reclaim}`.
// The ha verbs write the desired ApplyActiveRole to
// global_settings; the elector (B145) picks up the new value
// on its next 5s tick and either confirms (Patroni agrees)
// or overwrites (Patroni disagrees). This is the same
// mechanism the /admin/ha "Force actions" buttons use, so
// the CLI and the UI share the propagation path.
func runHA(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing ha verb (promote|demote|reclaim)")
	}
	verb := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("skygate ha "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	host := fs.String("host", "", "target hostname (required for promote / demote)")

	if err := fs.Parse(rest); err != nil {
		return fmt.Errorf("flag parse: %w", err)
	}

	d, err := openDeps(ctx)
	if err != nil {
		return fmt.Errorf("open deploy deps: %w", err)
	}
	defer d.Close()

	switch verb {
	case "promote":
		if *host == "" {
			return fmt.Errorf("ha promote requires --host=<hostname>")
		}
		return HAPromote(ctx, d, *host)
	case "demote":
		if *host == "" {
			return fmt.Errorf("ha demote requires --host=<hostname>")
		}
		return HADemote(ctx, d, *host)
	case "reclaim":
		return HAReclaim(ctx, d)
	default:
		return fmt.Errorf("unknown ha verb %q (promote|demote|reclaim)", verb)
	}
}

// ----- shared deps -------------------------------------------------------

// Deps groups the open resources the deploy subcommands
// share: a *sql.DB (for chain reads + ApplyActiveRole
// writes), a build locator (so push/pull can identify the
// current binary), and an S3 client (for the deploy bucket).
//
// openDeps is the only constructor; it reads from the same
// env vars the rest of skygate uses (SKYGATE_DB_DSN,
// SKYGATE_HA_DEPLOY_S3_BUCKET, SKYGATE_HOST_REPO_PATH) so
// the operator can drop the same .env on every node.
//
// Lives in subcommand.go (not push.go/pull.go) so both
// verbs see the same fields without circular imports.
type Deps struct {
	DB         *sql.DB
	Bucket     string
	BinPath    string
	BuildInfo  BuildInfo
	SelfHost   string
}

// BuildInfo is the read-only metadata the local binary
// exposes to push/status. Sourced from the same -ldflags
// variables the web /healthz shows (version, commit, time).
// PushedAt is set by callers that want to track when the
// build was uploaded (e.g. the /admin/deploy page renders
// the "last push" timestamp).
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
	PushedAt  time.Time
}

// Close releases the DB connection. Other fields are
// pure values and don't need to be released.
func (d *Deps) Close() error {
	if d == nil || d.DB == nil {
		return nil
	}
	return d.DB.Close()
}

// openDeps reads env, opens the DB, and returns Deps. On
// failure returns a descriptive error so the caller can
// print to stderr without losing the env-var context.
//
// B150 contract: SKYGATE_HA_DEPLOY_S3_BUCKET and
// SKYGATE_HOST_REPO_PATH are optional — if unset,
// push/pull/status return ErrNoS3Config (the operator
// hasn't set up the deploy bucket yet). The ha verbs
// (promote/demote/reclaim) don't need S3, so they work
// without the env vars.
func openDeps(ctx context.Context) (*Deps, error) {
	dsn := os.Getenv("SKYGATE_DB_DSN")
	if dsn == "" {
		return nil, errors.New("SKYGATE_DB_DSN is not set (skygate deploy/ha requires the runtime DB connection)")
	}
	return OpenDepsFromEnv(
		ctx, dsn,
		os.Getenv("SKYGATE_HA_DEPLOY_S3_BUCKET"),
		os.Getenv("SKYGATE_TS_HOSTNAME"),
		os.Getenv("SKYGATE_HOST_REPO_PATH"),
		BuildInfo{
			Version:   os.Getenv("SKYGATE_BUILD_VERSION"),
			Commit:    os.Getenv("SKYGATE_BUILD_COMMIT"),
			BuildTime: os.Getenv("SKYGATE_BUILD_TIME"),
		},
	)
}

// OpenDepsFromEnv is the public constructor used by both
// the CLI subcommand dispatch (openDeps) and the
// /admin/deploy HTTP handlers. Splitting it out keeps the
// admin package from reading os.Getenv itself (the admin
// handlers should pull from config, not env directly, so
// the tests can construct Deps without a process env).
//
// The dsn / bucket / selfHost / binPath / buildInfo fields
// are passed explicitly so the caller controls the
// source — the subcommand reads them from os.Getenv, the
// HTTP handler reads them from os.Getenv too, and tests
// construct Deps by hand.
func OpenDepsFromEnv(ctx context.Context, dsn, bucket, selfHost, binPath string, buildInfo BuildInfo) (*Deps, error) {
	if dsn == "" {
		return nil, errors.New("OpenDepsFromEnv: dsn is empty")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db.Ping (DSN=%s): %w", redactDSN(dsn), err)
	}

	if binPath == "" {
		binPath = "/home/operator/skygate"
	}

	return &Deps{
		DB:        db,
		Bucket:    bucket,
		BinPath:   binPath,
		SelfHost:  selfHost,
		BuildInfo: buildInfo,
	}, nil
}

// ErrNoS3Config is returned by push/pull/status when
// SKYGATE_HA_DEPLOY_S3_BUCKET is not set. The ha verbs
// (which don't need S3) ignore this error.
var ErrNoS3Config = errors.New("SKYGATE_HA_DEPLOY_S3_BUCKET is not set (skygate deploy push/pull/status require the S3 deploy bucket)")

// redactDSN strips the password from a postgres:// DSN
// for safe error messages. Same algorithm as
// cmd/skygate/main.go:extractPGPassword — duplicated here
// to avoid an import cycle (main depends on deploy, not
// vice versa).
func redactDSN(dsn string) string {
	const prefix = "://"
	prefixIdx := strings.Index(dsn, prefix)
	if prefixIdx < 0 {
		return dsn
	}
	rest := dsn[prefixIdx+len(prefix):]
	atIdx := strings.Index(rest, "@")
	if atIdx < 0 {
		return dsn
	}
	creds := rest[:atIdx]
	colonIdx := strings.Index(creds, ":")
	if colonIdx < 0 {
		return dsn
	}
	return dsn[:prefixIdx+len(prefix)] + creds[:colonIdx+1] + "***" + dsn[prefixIdx+len(prefix)+atIdx:]
}

// ----- help --------------------------------------------------------------

// printHelp renders the top-level help text for
// `skygate deploy help`. Kept terse on purpose — the B150
// plan locks the subcommand surface; adding rows here
// without a matching contract is a regression.
func printHelp() {
	fmt.Println("skygate deploy {push|pull|sync|status} [--target=<host>]")
	fmt.Println("skygate ha    {promote|demote|reclaim} [--host=<host>]")
	fmt.Println()
	fmt.Println("deploy verbs:")
	fmt.Println("  push     upload the local build to the S3 deploy bucket")
	fmt.Println("  pull     download the latest build from S3 and restart")
	fmt.Println("  sync     push only (the live operator flow runs `pull` on the target)")
	fmt.Println("  status   print local + remote build version")
	fmt.Println()
	fmt.Println("ha verbs:")
	fmt.Println("  promote  <host>   mark <host> as the desired active (elector picks it up)")
	fmt.Println("  demote   <host>   mark <host> as the desired active, then clear (force demote)")
	fmt.Println("  reclaim           clear ApplyActiveRole so the elector re-picks P1")
}
