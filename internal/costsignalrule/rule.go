// Package costsignalrule defines cost-blowout detection rule types and built-in rules.
package costsignalrule

import (
	"fmt"
	"time"
)

// CostBlowoutRuleType enumerates the supported cost-blowout detection patterns.
type CostBlowoutRuleType string

const (
	// BudgetOverrun detects when cumulative cost exceeds a fixed budget.
	BudgetOverrun CostBlowoutRuleType = "budget_overrun"
	// CostSpike detects a sudden increase in cost over a short window.
	CostSpike CostBlowoutRuleType = "cost_spike"
	// ResourceCostAnomaly detects cost deviation for a specific resource type.
	ResourceCostAnomaly CostBlowoutRuleType = "resource_cost_anomaly"
)

// CostBlowoutRule defines a single cost-blowout detection rule.
type CostBlowoutRule struct {
	// ID is a unique identifier for the rule.
	ID string
	// Type is the detection pattern.
	Type CostBlowoutRuleType
	// Threshold is the numeric trigger value (e.g., budget amount, spike percentage).
	Threshold float64
	// Window is the evaluation time window (e.g., 24h, 7d).
	Window time.Duration
	// Enabled controls whether the rule is active.
	Enabled bool
	// Description provides human-readable context.
	Description string
}

// CostBlowoutSignal is emitted when a rule triggers.
type CostBlowoutSignal struct {
	RuleID      string
	RuleType    CostBlowoutRuleType
	CurrentCost float64
	Threshold   float64
	Window      time.Duration
	Timestamp   time.Time
	Message     string
}

// Built-in cost-blowout rules.
var (
	// DefaultBudgetOverrunRule alerts when monthly cost exceeds $10,000.
	DefaultBudgetOverrunRule = CostBlowoutRule{
		ID:          "budget-overrun-monthly",
		Type:        BudgetOverrun,
		Threshold:   10000.0,
		Window:      30 * 24 * time.Hour,
		Enabled:     true,
		Description: "Monthly budget overrun alert at $10,000",
	}

	// DefaultCostSpikeRule alerts when cost increases >50% in a 24-hour window.
	DefaultCostSpikeRule = CostBlowoutRule{
		ID:          "cost-spike-24h",
		Type:        CostSpike,
		Threshold:   50.0, // percentage
		Window:      24 * time.Hour,
		Enabled:     true,
		Description: "Cost spike >50% in 24 hours",
	}

	// DefaultResourceCostAnomalyRule alerts when any resource type cost deviates >30% from baseline.
	DefaultResourceCostAnomalyRule = CostBlowoutRule{
		ID:          "resource-cost-anomaly",
		Type:        ResourceCostAnomaly,
		Threshold:   30.0, // percentage deviation
		Window:      7 * 24 * time.Hour,
		Enabled:     true,
		Description: "Resource cost anomaly >30% deviation from 7-day baseline",
	}
)

// Evaluate checks if the given cost data triggers the rule.
// costData is a map of resource type -> current cost.
// baseline is a map of resource type -> expected cost (for anomaly detection).
// Returns a signal if triggered, nil otherwise.
func (r *CostBlowoutRule) Evaluate(costData map[string]float64, baseline map[string]float64) *CostBlowoutSignal {
	if !r.Enabled {
		return nil
	}

	switch r.Type {
	case BudgetOverrun:
		total := 0.0
		for _, v := range costData {
			total += v
		}
		if total > r.Threshold {
			return &CostBlowoutSignal{
				RuleID:      r.ID,
				RuleType:    r.Type,
				CurrentCost: total,
				Threshold:   r.Threshold,
				Window:      r.Window,
				Timestamp:   time.Now(),
				Message:     fmt.Sprintf("Total cost %.2f exceeds budget %.2f", total, r.Threshold),
			}
		}

	case CostSpike:
		total := 0.0
		for _, v := range costData {
			total += v
		}
		// baseline total is expected cost from previous window
		baselineTotal := 0.0
		for _, v := range baseline {
			baselineTotal += v
		}
		if baselineTotal > 0 {
			pctIncrease := ((total - baselineTotal) / baselineTotal) * 100
			if pctIncrease > r.Threshold {
				return &CostBlowoutSignal{
					RuleID:      r.ID,
					RuleType:    r.Type,
					CurrentCost: total,
					Threshold:   r.Threshold,
					Window:      r.Window,
					Timestamp:   time.Now(),
					Message:     fmt.Sprintf("Cost spike: %.2f%% increase (current=%.2f, baseline=%.2f)", pctIncrease, total, baselineTotal),
				}
			}
		}

	case ResourceCostAnomaly:
		for resource, current := range costData {
			expected, ok := baseline[resource]
			if !ok {
				continue
			}
			if expected > 0 {
				deviation := ((current - expected) / expected) * 100
				if deviation > r.Threshold || deviation < -r.Threshold {
					return &CostBlowoutSignal{
						RuleID:      r.ID,
						RuleType:    r.Type,
						CurrentCost: current,
						Threshold:   r.Threshold,
						Window:      r.Window,
						Timestamp:   time.Now(),
						Message:     fmt.Sprintf("Resource %s cost anomaly: %.2f%% deviation (current=%.2f, expected=%.2f)", resource, deviation, current, expected),
					}
				}
			}
		}
	}

	return nil
}