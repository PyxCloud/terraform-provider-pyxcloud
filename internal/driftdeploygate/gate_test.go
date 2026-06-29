package driftdeploygate

import (
	"strings"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/tfplanparser"
)

func TestEvaluateDriftPolicy(t *testing.T) {
	tests := []struct {
		name          string
		summary       *tfplanparser.PlanSummary
		expectAllowed bool
		expectAction  string
	}{
		{
			name:          "No drift",
			summary:       &tfplanparser.PlanSummary{ResourcesChanged: 0},
			expectAllowed: true,
			expectAction:  "approve",
		},
		{
			name: "Safe additive drift",
			summary: &tfplanparser.PlanSummary{
				ResourcesChanged: 1,
				Added:            1,
				DriftDetails: []tfplanparser.DriftDetail{
					{Address: "aws_instance.new_worker", Type: "aws_instance", ChangeAction: "create"},
				},
			},
			expectAllowed: true,
			expectAction:  "open_pr",
		},
		{
			name: "Risky drift (security-sensitive type)",
			summary: &tfplanparser.PlanSummary{
				ResourcesChanged: 1,
				Added:            1,
				DriftDetails: []tfplanparser.DriftDetail{
					{Address: "aws_security_group.new_rule", Type: "aws_security_group", ChangeAction: "create"},
				},
			},
			expectAllowed: false,
			expectAction:  "block",
		},
		{
			name: "Destructive drift (removed resource)",
			summary: &tfplanparser.PlanSummary{
				ResourcesChanged: 1,
				Removed:          1,
				DriftDetails: []tfplanparser.DriftDetail{
					{Address: "aws_instance.old_worker", Type: "aws_instance", ChangeAction: "delete"},
				},
			},
			expectAllowed: false,
			expectAction:  "block",
		},
		{
			name: "Modifying drift (updated resource)",
			summary: &tfplanparser.PlanSummary{
				ResourcesChanged: 1,
				Changed:          1,
				DriftDetails: []tfplanparser.DriftDetail{
					{Address: "aws_instance.web", Type: "aws_instance", ChangeAction: "update"},
				},
			},
			expectAllowed: false,
			expectAction:  "block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict := EvaluateDriftPolicy(tt.summary)
			if verdict.Allowed != tt.expectAllowed {
				t.Errorf("expected allowed to be %v, got %v (reason: %q)", tt.expectAllowed, verdict.Allowed, verdict.Reason)
			}
			if verdict.Action != tt.expectAction {
				t.Errorf("expected action to be %q, got %q", tt.expectAction, verdict.Action)
			}
		})
	}
}

// ── Cost advisory wiring tests (pd-ONTO-CAP-ARCH-INFRA-COST-04) ──────────────

const testPlanJSONWithCost = `{
	"resource_changes": [
		{
			"address": "aws_instance.expensive",
			"type": "aws_instance",
			"change": {
				"actions": ["create"],
				"after": {
					"instance_type": "p4d.24xlarge",
					"monthly_cost": 9000.0,
					"currency": "USD"
				}
			}
		}
	]
}`

func TestEvaluateDriftPolicyWithCost_OverProvisionAdvisory(t *testing.T) {
	summary := &tfplanparser.PlanSummary{
		ResourcesChanged: 1, Added: 1,
		DriftDetails: []tfplanparser.DriftDetail{
			{Address: "aws_instance.expensive", Type: "aws_instance", ChangeAction: "create"},
		},
	}
	verdict := EvaluateDriftPolicyWithCost(summary, []byte(testPlanJSONWithCost), 500.0, 0)
	if !verdict.Allowed {
		t.Errorf("cost advisory must not block deployment, got allowed=false")
	}
	if verdict.Action != "open_pr" {
		t.Errorf("expected action open_pr, got %q", verdict.Action)
	}
	if len(verdict.CostAdvisories) == 0 {
		t.Error("expected at least one cost advisory for over-priced resource")
	}
	for _, a := range verdict.CostAdvisories {
		if strings.Contains(a, "over-provisioning") {
			return
		}
	}
	t.Errorf("expected over-provisioning advisory, got %v", verdict.CostAdvisories)
}

func TestEvaluateDriftPolicyWithCost_BudgetAdvisory(t *testing.T) {
	summary := &tfplanparser.PlanSummary{
		ResourcesChanged: 1, Added: 1,
		DriftDetails: []tfplanparser.DriftDetail{
			{Address: "aws_instance.expensive", Type: "aws_instance", ChangeAction: "create"},
		},
	}
	verdict := EvaluateDriftPolicyWithCost(summary, []byte(testPlanJSONWithCost), 0, 100.0)
	if !verdict.Allowed {
		t.Errorf("cost advisory must not block deployment, got allowed=false")
	}
	if len(verdict.CostAdvisories) == 0 {
		t.Error("expected at least one cost advisory for budget exceeded")
	}
	for _, a := range verdict.CostAdvisories {
		if strings.Contains(a, "budget") {
			return
		}
	}
	t.Errorf("expected budget advisory, got %v", verdict.CostAdvisories)
}

func TestEvaluateDriftPolicyWithCost_NilPlanJSON_NoAdvisory(t *testing.T) {
	summary := &tfplanparser.PlanSummary{ResourcesChanged: 0}
	verdict := EvaluateDriftPolicyWithCost(summary, nil, 100.0, 100.0)
	if len(verdict.CostAdvisories) != 0 {
		t.Errorf("expected no advisories for nil plan JSON, got %v", verdict.CostAdvisories)
	}
}

func TestEvaluateDriftPolicyWithCost_BlockedDriftNotUnblocked(t *testing.T) {
	// A risky (security-sensitive) change must still be blocked even with cost args.
	summary := &tfplanparser.PlanSummary{
		ResourcesChanged: 1, Added: 1,
		DriftDetails: []tfplanparser.DriftDetail{
			{Address: "aws_security_group.new", Type: "aws_security_group", ChangeAction: "create"},
		},
	}
	verdict := EvaluateDriftPolicyWithCost(summary, []byte(testPlanJSONWithCost), 0, 0)
	if verdict.Allowed {
		t.Errorf("risky drift must still be blocked even with cost evaluation, got allowed=true")
	}
	if verdict.Action != "block" {
		t.Errorf("expected action block, got %q", verdict.Action)
	}
}

func TestEvaluateDriftPolicyWithCost_WithinBudget_NoAdvisory(t *testing.T) {
	planJSON := []byte(`{
		"resource_changes": [
			{
				"address": "aws_instance.small",
				"type": "aws_instance",
				"change": {
					"actions": ["create"],
					"after": {"monthly_cost": 20.0}
				}
			}
		]
	}`)
	summary := &tfplanparser.PlanSummary{
		ResourcesChanged: 1, Added: 1,
		DriftDetails: []tfplanparser.DriftDetail{
			{Address: "aws_instance.small", Type: "aws_instance", ChangeAction: "create"},
		},
	}
	verdict := EvaluateDriftPolicyWithCost(summary, planJSON, 100.0, 500.0)
	if len(verdict.CostAdvisories) != 0 {
		t.Errorf("expected no advisories within budget, got %v", verdict.CostAdvisories)
	}
}
