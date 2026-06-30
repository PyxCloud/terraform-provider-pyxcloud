package driftdeploygate

import (
	"fmt"
	"strings"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/iacsecscan"
	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/tfplanparser"
)

// PolicyVerdict represents the outcome of a drift policy evaluation.
type PolicyVerdict struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	Action  string `json:"action"` // "approve", "open_pr", "block"

	// CostAdvisories is a list of non-blocking cost anomaly messages attached
	// to this verdict. They are emitted when cost signals extracted from the
	// Terraform plan exceed configured thresholds. Non-empty advisories do NOT
	// change Allowed — they are informational and logged alongside the verdict.
	CostAdvisories []string `json:"cost_advisories,omitempty"`
}

// EvaluateDriftPolicy evaluates whether drift is allowed to proceed to deployment.
// - Safe drift (only additive changes, i.e. no updates/deletions and no security-sensitive resources) is allowed, with action "open_pr" (to codify).
// - Risky drift (involves updates/deletions, or security/IAM/network resources) is blocked.
func EvaluateDriftPolicy(summary *tfplanparser.PlanSummary) PolicyVerdict {
	if summary == nil || summary.ResourcesChanged == 0 {
		return PolicyVerdict{
			Allowed: true,
			Reason:  "No drift detected",
			Action:  "approve",
		}
	}

	// Check for risky resources or destructive/modifying actions
	var riskyResources []string
	hasDestructiveChanges := summary.Removed > 0 || summary.Changed > 0

	for _, d := range summary.DriftDetails {
		if isSecuritySensitive(d.Type) {
			riskyResources = append(riskyResources, fmt.Sprintf("%s (%s)", d.Address, d.Type))
		}
	}

	if len(riskyResources) > 0 {
		return PolicyVerdict{
			Allowed: false,
			Reason:  fmt.Sprintf("Risky drift detected in security-sensitive resources: %s", strings.Join(riskyResources, ", ")),
			Action:  "block",
		}
	}

	if hasDestructiveChanges {
		return PolicyVerdict{
			Allowed: false,
			Reason:  fmt.Sprintf("Destructive or modifying drift detected (Changed: %d, Removed: %d)", summary.Changed, summary.Removed),
			Action:  "block",
		}
	}

	// Only additive changes (Added > 0, Changed == 0, Removed == 0) and not security-sensitive
	return PolicyVerdict{
		Allowed: true,
		Reason:  "Safe (additive) drift detected; policy permits deployment but recommends opening a PR to codify changes",
		Action:  "open_pr",
	}
}

// EvaluateDriftPolicyWithCost is like EvaluateDriftPolicy but also evaluates
// cost anomaly signals extracted from the plan. Cost anomalies are ADVISORY and
// non-blocking: they are attached to the verdict's CostAdvisories slice and do
// not change the Allowed field. This implements pd-ONTO-CAP-ARCH-INFRA-COST-04.
//
// planJSON is the raw terraform plan JSON (from `terraform show -json`). If nil
// or empty, cost advisory is skipped. overProvisionThreshold is the per-resource
// monthly USD ceiling (zero = disabled). budgetMonthly is the total plan budget
// ceiling (zero = disabled).
func EvaluateDriftPolicyWithCost(
	summary *tfplanparser.PlanSummary,
	planJSON []byte,
	overProvisionThreshold float64,
	budgetMonthly float64,
) PolicyVerdict {
	verdict := EvaluateDriftPolicy(summary)

	if len(planJSON) == 0 {
		return verdict
	}

	signals, err := tfplanparser.ExtractCostSignals(planJSON)
	if err != nil || len(signals) == 0 {
		return verdict
	}

	// Over-provisioning rule (per-resource cost).
	opRule := tfplanparser.OverProvisioningRule{MaxMonthlyCostPerUnit: overProvisionThreshold}
	for _, msg := range opRule.EvaluateOverProvisioning(signals) {
		verdict.CostAdvisories = append(verdict.CostAdvisories, "[cost-advisory] over-provisioning: "+msg)
	}

	// Budget rule (total plan cost).
	budgetRule := tfplanparser.CostTrendRule{MonthlyBudget: budgetMonthly}
	for _, msg := range budgetRule.EvaluateCostTrend(signals) {
		verdict.CostAdvisories = append(verdict.CostAdvisories, "[cost-advisory] budget: "+msg)
	}

	return verdict
}

// EvaluateDriftPolicyWithSecScan is like EvaluateDriftPolicyWithCost but also
// runs the IaC security scanner over the supplied resources. HIGH-severity
// findings block the deploy (Allowed = false). MEDIUM/LOW findings are
// appended as non-blocking SecAdvisories. This implements pd-ONTO-CAP-OPS-IACSECSCAN.
//
// resources is the list of Terraform resources to scan. An empty slice skips
// the security scan entirely.
func EvaluateDriftPolicyWithSecScan(
	summary *tfplanparser.PlanSummary,
	planJSON []byte,
	overProvisionThreshold float64,
	budgetMonthly float64,
	resources []iacsecscan.Resource,
) PolicyVerdict {
	verdict := EvaluateDriftPolicyWithCost(summary, planJSON, overProvisionThreshold, budgetMonthly)

	if len(resources) == 0 {
		return verdict
	}

	result := iacsecscan.ScanWithResult(resources)
	for _, f := range result.Findings {
		msg := fmt.Sprintf("[sec-%s] rule=%s resource=%s/%s: %s — remediation: %s",
			strings.ToLower(f.Severity), f.RuleID, f.ResourceType, f.ResourceName, f.Description, f.Remediation)
		if f.Severity == "HIGH" {
			// HIGH findings block the deploy regardless of drift verdict.
			verdict.Allowed = false
			verdict.Action = "block"
			verdict.Reason = fmt.Sprintf("IaC security gate blocked: %s", msg)
			// Keep going to collect all findings as advisories too.
		}
		verdict.CostAdvisories = append(verdict.CostAdvisories, msg)
	}

	return verdict
}

func isSecuritySensitive(resourceType string) bool {
	norm := strings.ToLower(resourceType)
	for _, k := range []string{"sg", "security_group", "iam", "role", "policy", "vpc", "subnet", "nacl", "firewall", "network"} {
		if norm == k || strings.Contains(norm, k) {
			return true
		}
	}
	return false
}
