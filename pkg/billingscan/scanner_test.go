package billingscan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/costsignalrule"
)

// ── fixture helpers ───────────────────────────────────────────────────────────

// okInput is a billing snapshot that should NOT trigger any built-in rule.
var okInput = Input{
	Current:  map[string]float64{"compute": 5000.0, "storage": 200.0},
	Baseline: map[string]float64{"compute": 5000.0, "storage": 200.0},
}

// budgetBlowoutInput exceeds the $10,000 monthly budget.
var budgetBlowoutInput = Input{
	Current:  map[string]float64{"compute": 8000.0, "storage": 4000.0},
	Baseline: map[string]float64{"compute": 7000.0, "storage": 3500.0},
}

// spikeInput triggers the >50 % cost-spike rule.
var spikeInput = Input{
	Current:  map[string]float64{"compute": 9000.0},
	Baseline: map[string]float64{"compute": 4000.0},
}

// anomalyInput triggers the >30 % per-resource anomaly rule.
var anomalyInput = Input{
	Current:  map[string]float64{"storage": 1000.0},
	Baseline: map[string]float64{"storage": 600.0},
}

// ── Default scanner ───────────────────────────────────────────────────────────

func TestDefaultScanner_OK(t *testing.T) {
	res := Default().Scan(okInput)
	if !res.OK {
		t.Errorf("expected OK=true for clean input, got signals: %+v", res.Signals)
	}
	if len(res.Signals) != 0 {
		t.Errorf("expected 0 signals, got %d", len(res.Signals))
	}
}

func TestDefaultScanner_BudgetBlowout(t *testing.T) {
	res := Default().Scan(budgetBlowoutInput)
	if res.OK {
		t.Fatal("expected OK=false for budget blowout fixture")
	}
	found := false
	for _, s := range res.Signals {
		if s.RuleType == costsignalrule.BudgetOverrun {
			found = true
		}
	}
	if !found {
		t.Errorf("expected BudgetOverrun signal, got %+v", res.Signals)
	}
}

func TestDefaultScanner_CostSpike(t *testing.T) {
	res := Default().Scan(spikeInput)
	if res.OK {
		t.Fatal("expected OK=false for spike fixture")
	}
	found := false
	for _, s := range res.Signals {
		if s.RuleType == costsignalrule.CostSpike {
			found = true
		}
	}
	if !found {
		t.Errorf("expected CostSpike signal, got %+v", res.Signals)
	}
}

func TestDefaultScanner_ResourceAnomaly(t *testing.T) {
	res := Default().Scan(anomalyInput)
	if res.OK {
		t.Fatal("expected OK=false for anomaly fixture")
	}
	found := false
	for _, s := range res.Signals {
		if s.RuleType == costsignalrule.ResourceCostAnomaly {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ResourceCostAnomaly signal, got %+v", res.Signals)
	}
}

// ── Custom scanner (single disabled rule) ─────────────────────────────────────

func TestCustomScanner_DisabledRule_NoSignal(t *testing.T) {
	rule := costsignalrule.DefaultBudgetOverrunRule
	rule.Enabled = false
	s := New([]costsignalrule.CostBlowoutRule{rule})
	res := s.Scan(budgetBlowoutInput)
	if !res.OK {
		t.Errorf("disabled rule must not fire, got signals: %+v", res.Signals)
	}
}

// ── ScanReader ────────────────────────────────────────────────────────────────

func TestScanReader_OK(t *testing.T) {
	raw, _ := json.Marshal(okInput)
	res, err := ScanReader(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("ScanReader error: %v", err)
	}
	if !res.OK {
		t.Errorf("expected OK=true, got signals: %+v", res.Signals)
	}
}

func TestScanReader_BudgetBlowout(t *testing.T) {
	raw, _ := json.Marshal(budgetBlowoutInput)
	res, err := ScanReader(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("ScanReader error: %v", err)
	}
	if res.OK {
		t.Fatal("expected OK=false for budget blowout")
	}
}

func TestScanReader_InvalidJSON(t *testing.T) {
	_, err := ScanReader(strings.NewReader("{bad json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
}

// ── Result fields ─────────────────────────────────────────────────────────────

func TestResult_ScannedAt_IsRecent(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	res := Default().Scan(okInput)
	after := time.Now().UTC().Add(time.Second)
	if res.ScannedAt.Before(before) || res.ScannedAt.After(after) {
		t.Errorf("ScannedAt %v not in expected range [%v, %v]", res.ScannedAt, before, after)
	}
}

func TestResult_SignalFields(t *testing.T) {
	res := Default().Scan(budgetBlowoutInput)
	if res.OK {
		t.Skip("no signals to verify")
	}
	for _, sig := range res.Signals {
		if sig.RuleID == "" {
			t.Error("signal RuleID must not be empty")
		}
		if sig.Threshold <= 0 {
			t.Errorf("signal Threshold must be >0, got %f", sig.Threshold)
		}
		if sig.Message == "" {
			t.Error("signal Message must not be empty")
		}
	}
}

// ── Multiple rules firing in one scan ─────────────────────────────────────────

func TestDefaultScanner_MultipleSignals(t *testing.T) {
	// This input exceeds budget AND is a spike AND has anomaly.
	multiBlowout := Input{
		Current:  map[string]float64{"compute": 12000.0, "storage": 3000.0},
		Baseline: map[string]float64{"compute": 4000.0, "storage": 500.0},
	}
	res := Default().Scan(multiBlowout)
	if res.OK {
		t.Fatal("expected OK=false for multi-blowout input")
	}
	if len(res.Signals) < 2 {
		t.Errorf("expected at least 2 signals, got %d: %+v", len(res.Signals), res.Signals)
	}
}
