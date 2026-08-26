// File: cmd/skygate/derp_probe.go
// B189 (v1.5.2) — `skygate derp-probe` CLI subcommand.
//
// Runs one synchronous DERP probe cycle and prints the
// results as a table to stdout. Used for ad-hoc latency
// investigation from the operator's laptop; the cron +
// /admin/derp/dashboard cover the usual case.
//
// Usage:
//   skygate derp-probe
//   skygate derp-probe -own-only   # only probe the operator's own DERP
//   skygate derp-probe -public-only

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/derphealth"
)

func runDerpProbe(args []string) error {
	fs := flag.NewFlagSet("derp-probe", flag.ContinueOnError)
	ownOnly := fs.Bool("own-only", false, "only probe the operator's own DERP (skygate DB)")
	publicOnly := fs.Bool("public-only", false, "only probe Tailscale's public DERP map")
	noFetchPublic := fs.Bool("no-fetch-public", false, "skip the Tailscale map fetch (faster, but only probes own)")
	timeoutSec := fs.Int("timeout", 30, "per-probe timeout in seconds (default 30)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(*timeoutSec)*time.Second)
	defer cancel()

	// Fetch both lists, then filter per the flags.
	httpClient := &http.Client{Timeout: 10 * time.Second}
	var derps []derphealth.DERPInfo
	if !*publicOnly {
		own, err := derphealth.FetchOwnDERPs(ctx, d)
		if err != nil {
			log.Printf("derp-probe: fetch own: %v", err)
		}
		derps = append(derps, own...)
	}
	if !*ownOnly && !*noFetchPublic {
		pub, err := derphealth.FetchPublicDERPs(ctx, httpClient)
		if err != nil {
			log.Printf("derp-probe: fetch public: %v", err)
		}
		derps = append(derps, pub...)
	}
	if len(derps) == 0 {
		return fmt.Errorf("no DERPs to probe (check flags / DB / network)")
	}

	persist := derphealth.PersistToDB(d)
	results := derphealth.ProbeAll(ctx, derps, httpClient, persist)

	// Pretty print as a table.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Info.IsOwn != results[j].Info.IsOwn {
			return results[i].Info.IsOwn
		}
		return results[i].LatencyMs < results[j].LatencyMs
	})
	fmt.Fprintf(os.Stdout, "%-5s %-7s %-8s %-30s %-8s %s\n",
		"ID", "type", "region", "host", "latency", "status")
	for _, r := range results {
		typ := "public"
		if r.Info.IsOwn {
			typ = "own"
		}
		lat := "—"
		if r.Healthy && r.LatencyMs > 0 {
			lat = fmt.Sprintf("%dms", r.LatencyMs)
		}
		status := "ok"
		if !r.Healthy {
			status = "FAIL"
		}
		fmt.Fprintf(os.Stdout, "%-5d %-7s %-8s %-30s %-8s %s\n",
			r.Info.RegionID, typ, r.Info.RegionCode, r.Info.Host, lat, status)
	}
	return nil
}
