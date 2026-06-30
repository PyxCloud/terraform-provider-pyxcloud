// Package costsignalrule implements cost-blowout detection patterns for the
// PyxCloud billing signal gate.
//
// # Overview
//
// The package provides rule definitions and an evaluator (Detector) that
// checks real-time cost data against a set of CostBlowoutRules.  When a rule
// fires the Detector emits a CostBlowoutSignal that callers can use to halt
// deployment, open a board incident, or page on-call.
//
// # Detection Patterns
//
// Three built-in pattern types are supported:
//
//   - BudgetOverrun – cumulative cost in the evaluation window exceeds a fixed
//     monetary threshold (e.g., monthly spend > $10,000).
//
//   - CostSpike – cost increases by more than N% relative to the previous
//     window baseline (e.g., >50% in 24 h).  A zero-baseline window is
//     skipped to avoid false-positives on first-run data.
//
//   - ResourceCostAnomaly – the cost of a specific resource type deviates more
//     than N% from its 7-day rolling baseline in either direction.
//
// # Built-in Rules
//
// The package ships three ready-to-use rules:
//
//   - DefaultBudgetOverrunRule  – monthly budget gate at $10,000.
//   - DefaultCostSpikeRule      – 24-hour spike gate at 50%.
//   - DefaultResourceCostAnomalyRule – per-resource anomaly gate at 30%.
//
// All built-in rules can be overridden or disabled individually.
//
// # Billing Signal Gate Usage
//
//	det := costsignalrule.NewDetector(costsignalrule.DefaultRules())
//	signals := det.EvaluateAll(currentCosts, baselineCosts)
//	if len(signals) > 0 {
//	    // block deployment, emit board event, page on-call …
//	}
//
// # Remediation
//
// When a BudgetOverrun fires: audit the most expensive resource type in
// costData, raise a board pd-BILLING-REVIEW task, and consider enabling
// reserved-instance pricing.
//
// When a CostSpike fires: compare the current window to the previous window
// breakdown; look for newly provisioned resource types not present in the
// baseline.
//
// When a ResourceCostAnomaly fires: inspect the flagged resource type for
// unexpected scaling events (auto-scaling runaway, misconfigured spot
// interruption handling).
package costsignalrule
