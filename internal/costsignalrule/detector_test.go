package costsignalrule

import (
	"testing"
	"time"
)

func TestNewDetectorDefaultRules(t *testing.T) {
	det := NewDetector(DefaultRules())
	if det.RuleCount() != 3 {
		t.Errorf("expected 3 default rules, got %d", det.RuleCount())
	}
}

func TestDetectorEvaluateAll_NoSignals(t *testing.T) {
	det := NewDetector(DefaultRules())
	// All costs well within thresholds
	signals := det.EvaluateAll(
		map[string]float64{"compute": 1000, "storage": 500},
		map[string]float64{"compute": 1000, "storage": 500},
	)
	if len(signals) != 0 {
		t.Errorf("expected 0 signals, got %d", len(signals))
	}
}

func TestDetectorEvaluateAll_BudgetOverrun(t *testing.T) {
	det := NewDetector([]CostBlowoutRule{DefaultBudgetOverrunRule})
	signals := det.EvaluateAll(
		map[string]float64{"compute": 8000, "storage": 5000}, // total 13000 > 10000
		nil,
	)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].RuleType != BudgetOverrun {
		t.Errorf("expected BudgetOverrun, got %s", signals[0].RuleType)
	}
}

func TestDetectorEvaluateAll_MultipleRulesFire(t *testing.T) {
	det := NewDetector(DefaultRules())
	// Trigger both BudgetOverrun and CostSpike simultaneously
	signals := det.EvaluateAll(
		map[string]float64{"compute": 12000},
		map[string]float64{"compute": 1000}, // 1100% spike
	)
	// At least BudgetOverrun and CostSpike should fire
	if len(signals) < 2 {
		t.Errorf("expected >=2 signals, got %d", len(signals))
	}
}

func TestDetectorAddRule(t *testing.T) {
	det := NewDetector(nil)
	if det.RuleCount() != 0 {
		t.Fatalf("expected 0 rules initially")
	}
	custom := CostBlowoutRule{
		ID:          "custom-rule",
		Type:        BudgetOverrun,
		Threshold:   500.0,
		Window:      24 * time.Hour,
		Enabled:     true,
		Description: "Custom low-budget gate",
	}
	det.AddRule(custom)
	if det.RuleCount() != 1 {
		t.Errorf("expected 1 rule after AddRule, got %d", det.RuleCount())
	}
	signals := det.EvaluateAll(map[string]float64{"compute": 600}, nil)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal from custom rule, got %d", len(signals))
	}
	if signals[0].RuleID != "custom-rule" {
		t.Errorf("expected rule ID custom-rule, got %s", signals[0].RuleID)
	}
}

func TestDetectorEvaluateAll_DisabledRulesSkipped(t *testing.T) {
	rules := DefaultRules()
	for i := range rules {
		rules[i].Enabled = false
	}
	det := NewDetector(rules)
	signals := det.EvaluateAll(
		map[string]float64{"compute": 999999},
		map[string]float64{"compute": 1},
	)
	if len(signals) != 0 {
		t.Errorf("expected 0 signals with all rules disabled, got %d", len(signals))
	}
}

func TestDetectorEvaluateAll_ResourceCostAnomaly(t *testing.T) {
	det := NewDetector([]CostBlowoutRule{DefaultResourceCostAnomalyRule})
	signals := det.EvaluateAll(
		map[string]float64{"network": 200}, // 100% above baseline
		map[string]float64{"network": 100},
	)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].RuleType != ResourceCostAnomaly {
		t.Errorf("expected ResourceCostAnomaly, got %s", signals[0].RuleType)
	}
}

func TestDetectorEvaluateAll_NegativeAnomaly(t *testing.T) {
	det := NewDetector([]CostBlowoutRule{DefaultResourceCostAnomalyRule})
	// Cost dropped to 50% of baseline → -50% deviation still beyond ±30%
	signals := det.EvaluateAll(
		map[string]float64{"storage": 50},
		map[string]float64{"storage": 100},
	)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal for negative anomaly, got %d", len(signals))
	}
}
