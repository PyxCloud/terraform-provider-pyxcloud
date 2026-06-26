package driftdeploygate

import (
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
