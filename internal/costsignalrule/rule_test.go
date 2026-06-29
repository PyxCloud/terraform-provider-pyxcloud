package costsignalrule

import (
	"testing"
	"time"
)

func TestDefaultBudgetOverrunRule(t *testing.T) {
	rule := DefaultBudgetOverrunRule
	// Test below threshold
	sig := rule.Evaluate(map[string]float64{"compute": 5000}, nil)
	if sig != nil {
		t.Errorf("expected nil signal for cost below threshold, got %+v", sig)
	}
	// Test above threshold
	sig = rule.Evaluate(map[string]float64{"compute": 15000}, nil)
	if sig == nil {
		t.Fatal("expected signal for cost above threshold")
	}
	if sig.RuleID != "budget-overrun-monthly" {
		t.Errorf("expected rule ID budget-overrun-monthly, got %s", sig.RuleID)
	}
	if sig.CurrentCost != 15000 {
		t.Errorf("expected current cost 15000, got %f", sig.CurrentCost)
	}
}

func TestDefaultCostSpikeRule(t *testing.T) {
	rule := DefaultCostSpikeRule
	// No spike
	sig := rule.Evaluate(
		map[string]float64{"compute": 100},
		map[string]float64{"compute": 100},
	)
	if sig != nil {
		t.Errorf("expected nil signal for no spike, got %+v", sig)
	}
	// Spike >50%
	sig = rule.Evaluate(
		map[string]float64{"compute": 200},
		map[string]float64{"compute": 100},
	)
	if sig == nil {
		t.Fatal("expected signal for spike >50%")
	}
	if sig.RuleType != CostSpike {
		t.Errorf("expected rule type CostSpike, got %s", sig.RuleType)
	}
}

func TestDefaultResourceCostAnomalyRule(t *testing.T) {
	rule := DefaultResourceCostAnomalyRule
	// No anomaly
	sig := rule.Evaluate(
		map[string]float64{"compute": 100},
		map[string]float64{"compute": 100},
	)
	if sig != nil {
		t.Errorf("expected nil signal for no anomaly, got %+v", sig)
	}
	// Anomaly >30%
	sig = rule.Evaluate(
		map[string]float64{"compute": 200},
		map[string]float64{"compute": 100},
	)
	if sig == nil {
		t.Fatal("expected signal for anomaly >30%")
	}
	if sig.RuleType != ResourceCostAnomaly {
		t.Errorf("expected rule type ResourceCostAnomaly, got %s", sig.RuleType)
	}
}

func TestDisabledRule(t *testing.T) {
	rule := DefaultBudgetOverrunRule
	rule.Enabled = false
	sig := rule.Evaluate(map[string]float64{"compute": 999999}, nil)
	if sig != nil {
		t.Errorf("expected nil signal for disabled rule, got %+v", sig)
	}
}

func TestRuleWindow(t *testing.T) {
	rule := DefaultBudgetOverrunRule
	if rule.Window != 30*24*time.Hour {
		t.Errorf("expected window 720h, got %v", rule.Window)
	}
}