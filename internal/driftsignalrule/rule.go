// Package driftsignalrule provides drift-signal detection rules and event firing.
package driftsignalrule

import (
	"context"
	"strings"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"
	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/tfplanparser"
)

// DriftEvent represents an event fired when unapproved infrastructure drift is detected.
type DriftEvent struct {
	Type    string                 `json:"type"`
	Payload map[string]interface{} `json:"payload"`
}

// EvaluateAndSignal checks a plan summary for drift against an approved list of PR SHAs.
// If drift is found and the change SHA is not approved, it fires a drift_detected event via the client.
func EvaluateAndSignal(ctx context.Context, c client.Client, summary *tfplanparser.PlanSummary, approvedSHAs []string, changeSHA string) (*DriftEvent, error) {
	if summary == nil || summary.ResourcesChanged == 0 {
		return nil, nil
	}

	// Check if the change is approved (i.e. the observed change SHA is in the approved list)
	isApproved := false
	if changeSHA != "" {
		for _, sha := range approvedSHAs {
			if sha == changeSHA {
				isApproved = true
				break
			}
		}
	}

	if isApproved {
		return nil, nil
	}

	// Unapproved drift detected!
	payload := map[string]interface{}{
		"resources_changed": summary.ResourcesChanged,
		"added":             summary.Added,
		"changed":           summary.Changed,
		"removed":           summary.Removed,
		"change_sha":        changeSHA,
		"approved_shas":     approvedSHAs,
	}

	// Collect security-sensitive resources and verify if the drift is risky
	var secSensitive []string
	for _, d := range summary.DriftDetails {
		if isSecuritySensitive(d.Type) {
			secSensitive = append(secSensitive, d.Address)
		}
	}

	if len(secSensitive) > 0 {
		payload["security_sensitive_resources"] = secSensitive
		payload["risky"] = true
	} else {
		payload["risky"] = false
	}

	event := &DriftEvent{
		Type:    "drift_detected",
		Payload: payload,
	}

	// Fire event via client if supported/implemented
	if err := c.FireEvent(ctx, event.Type, event.Payload); err != nil {
		return event, err
	}

	return event, nil
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
