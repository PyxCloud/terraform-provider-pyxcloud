package driftdeploygate

import (
	"fmt"
	"strings"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/tfplanparser"
)

// PolicyVerdict represents the outcome of a drift policy evaluation.
type PolicyVerdict struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	Action  string `json:"action"` // "approve", "open_pr", "block"
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

func isSecuritySensitive(resourceType string) bool {
	norm := strings.ToLower(resourceType)
	for _, k := range []string{"sg", "security_group", "iam", "role", "policy", "vpc", "subnet", "nacl", "firewall", "network"} {
		if norm == k || strings.Contains(norm, k) {
			return true
		}
	}
	return false
}
