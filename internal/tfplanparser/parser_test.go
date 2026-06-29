package tfplanparser

import (
	"strings"
	"testing"
)

func TestParsePlan(t *testing.T) {
	planJSON := `{
		"format_version": "0.1",
		"resource_changes": [
			{
				"address": "aws_security_group.allow_tls",
				"type": "aws_security_group",
				"name": "allow_tls",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["create"],
					"before": null,
					"after": {
						"name": "allow_tls",
						"description": "Allow TLS inbound traffic"
					}
				}
			},
			{
				"address": "aws_instance.web",
				"type": "aws_instance",
				"name": "web",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["update"],
					"before": {
						"ami": "ami-0c55b159cbfafe1f0",
						"tags": {
							"Environment": "dev"
						}
					},
					"after": {
						"ami": "ami-0c55b159cbfafe1f0",
						"tags": {
							"Environment": "production"
						}
					}
				}
			},
			{
				"address": "aws_subnet.private",
				"type": "aws_subnet",
				"name": "private",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["delete"],
					"before": {
						"cidr_block": "10.0.1.0/24"
					},
					"after": null
				}
			},
			{
				"address": "aws_vpc.main",
				"type": "aws_vpc",
				"name": "main",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["no-op"],
					"before": {
						"cidr_block": "10.0.0.0/16"
					},
					"after": {
						"cidr_block": "10.0.0.0/16"
					}
				}
			}
		]
	}`

	summary, err := ParsePlanJSON([]byte(planJSON))
	if err != nil {
		t.Fatalf("failed to parse plan JSON: %v", err)
	}

	if summary.Added != 1 {
		t.Errorf("expected 1 added resource, got %d", summary.Added)
	}
	if summary.Changed != 1 {
		t.Errorf("expected 1 changed resource, got %d", summary.Changed)
	}
	if summary.Removed != 1 {
		t.Errorf("expected 1 removed resource, got %d", summary.Removed)
	}
	if summary.ResourcesChanged != 3 {
		t.Errorf("expected 3 resources changed in total, got %d", summary.ResourcesChanged)
	}

	if len(summary.DriftDetails) != 3 {
		t.Fatalf("expected 3 drift details, got %d", len(summary.DriftDetails))
	}

	// Verify update details and changed attributes
	var updateDetail *DriftDetail
	for _, d := range summary.DriftDetails {
		if d.Address == "aws_instance.web" {
			updateDetail = &d
		}
	}

	if updateDetail == nil {
		t.Fatal("expected update detail for aws_instance.web")
	}

	if updateDetail.ChangeAction != "update" {
		t.Errorf("expected change action to be 'update', got %q", updateDetail.ChangeAction)
	}

	if len(updateDetail.ChangedAttrs) != 1 || updateDetail.ChangedAttrs[0] != "tags.Environment" {
		t.Errorf("expected changed attributes to contain 'tags.Environment', got %v", updateDetail.ChangedAttrs)
	}
}

// ── CostSignal extraction tests (pd-ONTO-CAP-ARCH-INFRA-COST-05) ─────────────

func TestExtractCostSignals_BasicAttributes(t *testing.T) {
	planJSON := []byte(`{
		"resource_changes": [
			{
				"address": "aws_instance.web",
				"type": "aws_instance",
				"change": {
					"actions": ["create"],
					"after": {
						"instance_type": "t3.medium",
						"region": "us-east-1",
						"monthly_cost": 30.24,
						"hourly_cost": 0.0416,
						"currency": "USD"
					}
				}
			}
		]
	}`)

	signals, err := ExtractCostSignals(planJSON)
	if err != nil {
		t.Fatalf("ExtractCostSignals failed: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	s := signals[0]
	if s.ResourceAddr != "aws_instance.web" {
		t.Errorf("expected addr aws_instance.web, got %q", s.ResourceAddr)
	}
	if s.ResourceType != "aws_instance" {
		t.Errorf("expected type aws_instance, got %q", s.ResourceType)
	}
	if s.Action != "create" {
		t.Errorf("expected action create, got %q", s.Action)
	}
	if s.MonthlyCost != 30.24 {
		t.Errorf("expected monthly_cost 30.24, got %f", s.MonthlyCost)
	}
	if s.InstanceType != "t3.medium" {
		t.Errorf("expected instance_type t3.medium, got %q", s.InstanceType)
	}
	if s.Currency != "USD" {
		t.Errorf("expected currency USD, got %q", s.Currency)
	}
}

func TestExtractCostSignals_SkipsNilAfter(t *testing.T) {
	planJSON := []byte(`{
		"resource_changes": [
			{
				"address": "aws_instance.deleted",
				"type": "aws_instance",
				"change": {
					"actions": ["delete"],
					"after": null
				}
			}
		]
	}`)
	signals, err := ExtractCostSignals(planJSON)
	if err != nil {
		t.Fatalf("ExtractCostSignals failed: %v", err)
	}
	if len(signals) != 0 {
		t.Errorf("expected 0 signals for delete with null after, got %d", len(signals))
	}
}

func TestExtractCostSignals_MultipleResources(t *testing.T) {
	planJSON := []byte(`{
		"resource_changes": [
			{
				"address": "aws_instance.a",
				"type": "aws_instance",
				"change": {"actions": ["create"], "after": {"monthly_cost": 100.0}}
			},
			{
				"address": "aws_db_instance.db",
				"type": "aws_db_instance",
				"change": {"actions": ["create"], "after": {"monthly_cost": 200.0}}
			}
		]
	}`)
	signals, err := ExtractCostSignals(planJSON)
	if err != nil {
		t.Fatalf("ExtractCostSignals failed: %v", err)
	}
	if len(signals) != 2 {
		t.Fatalf("expected 2 signals, got %d", len(signals))
	}
}

func TestExtractCostSignals_InvalidJSON(t *testing.T) {
	_, err := ExtractCostSignals([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// ── OverProvisioningRule tests ────────────────────────────────────────────────

func TestOverProvisioningRule_AboveThreshold(t *testing.T) {
	rule := OverProvisioningRule{MaxMonthlyCostPerUnit: 50.0}
	signals := []CostSignal{
		{ResourceAddr: "aws_instance.expensive", ResourceType: "aws_instance", MonthlyCost: 120.0},
	}
	advisories := rule.EvaluateOverProvisioning(signals)
	if len(advisories) != 1 {
		t.Fatalf("expected 1 advisory, got %d: %v", len(advisories), advisories)
	}
	if !strings.Contains(advisories[0], "aws_instance.expensive") {
		t.Errorf("advisory should mention resource address, got %q", advisories[0])
	}
}

func TestOverProvisioningRule_BelowThreshold(t *testing.T) {
	rule := OverProvisioningRule{MaxMonthlyCostPerUnit: 50.0}
	signals := []CostSignal{
		{ResourceAddr: "aws_instance.small", ResourceType: "aws_instance", MonthlyCost: 20.0},
	}
	advisories := rule.EvaluateOverProvisioning(signals)
	if len(advisories) != 0 {
		t.Errorf("expected 0 advisories below threshold, got %v", advisories)
	}
}

func TestOverProvisioningRule_ZeroThresholdDisabled(t *testing.T) {
	rule := OverProvisioningRule{MaxMonthlyCostPerUnit: 0}
	signals := []CostSignal{
		{ResourceAddr: "aws_instance.any", MonthlyCost: 9999.0},
	}
	advisories := rule.EvaluateOverProvisioning(signals)
	if len(advisories) != 0 {
		t.Errorf("expected rule disabled when threshold=0, got %v", advisories)
	}
}

// ── CostTrendRule tests ───────────────────────────────────────────────────────

func TestCostTrendRule_BudgetExceeded(t *testing.T) {
	rule := CostTrendRule{MonthlyBudget: 100.0}
	signals := []CostSignal{
		{MonthlyCost: 60.0},
		{MonthlyCost: 80.0},
	}
	advisories := rule.EvaluateCostTrend(signals)
	if len(advisories) != 1 {
		t.Fatalf("expected 1 advisory for budget exceeded, got %d: %v", len(advisories), advisories)
	}
	if !strings.Contains(advisories[0], "140.00") {
		t.Errorf("advisory should mention total cost 140.00, got %q", advisories[0])
	}
}

func TestCostTrendRule_WithinBudget(t *testing.T) {
	rule := CostTrendRule{MonthlyBudget: 500.0}
	signals := []CostSignal{
		{MonthlyCost: 100.0},
		{MonthlyCost: 50.0},
	}
	advisories := rule.EvaluateCostTrend(signals)
	if len(advisories) != 0 {
		t.Errorf("expected 0 advisories within budget, got %v", advisories)
	}
}

func TestCostTrendRule_ZeroBudgetDisabled(t *testing.T) {
	rule := CostTrendRule{MonthlyBudget: 0}
	signals := []CostSignal{{MonthlyCost: 999999.0}}
	advisories := rule.EvaluateCostTrend(signals)
	if len(advisories) != 0 {
		t.Errorf("expected rule disabled when budget=0, got %v", advisories)
	}
}
