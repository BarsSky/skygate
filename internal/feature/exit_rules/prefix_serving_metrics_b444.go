// prefix_serving_metrics_b444.go — B444. The PERIODIC half of the prefix-serving diagnosis.
//
// The dashboard question the operator's complaint really asks is «какие адреса не попали в
// маршрутизацию» — and a metric is the only place that question gets answered continuously.
// Two gauges, both set at the end of a sync pass:
//
//	skygate_exit_rules_prefixes_owned          — how many prefixes the assignment table owns
//	skygate_exit_rules_prefixes_not_applied    — how many of them were NOT applied by their
//	                                             owner relay in the last pass
//
// They are deliberately NOT called "unserved": a sync pass knows what it ASKED each relay to
// advertise and how that attempt ended, not what headscale later approved. The live truth is
// the `exit_rules.prefix_serving` system test, which reads headscale's approved routes. Two
// different questions, two different answers, and no metric pretending to be the other.
package exit_rules

import "skygate/internal/metrics"

var (
	prefixesOwnedGauge = metrics.NewGauge("skygate_exit_rules_prefixes_owned",
		"Prefixes the assignment table assigns to a relay (last sync pass)")

	// The help is the contract for what the number MEANS: this pass's apply result, not the
	// live set of approved routes. A dashboard that mixes the two starts lying.
	prefixesNotAppliedGauge = metrics.NewGauge("skygate_exit_rules_prefixes_not_applied",
		"Owned prefixes NOT applied by their relay in the last sync pass (0 = every owned prefix was accepted; the live approved-route truth is the exit_rules.prefix_serving system test)")
)

// publishPrefixServingMetrics stores one pass's numbers. Best effort: a metrics failure must
// never make a sync fail.
func publishPrefixServingMetrics(owners map[string]string, resultByRelay map[string]string) {
	defer func() { _ = recover() }()
	prefixesOwnedGauge.Set(float64(len(owners)))
	prefixesNotAppliedGauge.Set(float64(CountOwnedPrefixesNotApplied(owners, resultByRelay)))
}

// keep the file's purpose explicit: the gauges are the periodic half, the system test is the
// live half.
