package driftsignalrule

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/passobuild/pyxcloud/internal/client"
)

// Rule defines a drift signal rule.
type Rule interface {
	Name() string
	Evaluate(ctx context.Context, state *client.State) (*Signal, error)
}

// Signal represents a drift signal.
type Signal struct {
	Name        string
	Severity    string // "low", "medium", "high", "critical"
	Description string
	Detail      string
}

// OverProvisioningRule detects resources that are over-provisioned based on utilization.
type OverProvisioningRule struct {
	CPUThreshold    float64 // e.g., 0.20 means 20% CPU utilization threshold
	MemoryThreshold float64
	StorageThreshold float64
}

func (r *OverProvisioningRule) Name() string {
	return "over-provisioning"
}

func (r *OverProvisioningRule) Evaluate(ctx context.Context, state *client.State) (*Signal, error) {
	if state == nil {
		return nil, fmt.Errorf("state is nil")
	}
	overProvisioned := []string{}
	for _, res := range state.Resources {
		if res.Type == "aws_instance" || res.Type == "aws_ecs_service" {
			cpuUtil := res.Metrics.CPUUtilization
			memUtil := res.Metrics.MemoryUtilization
			if cpuUtil < r.CPUThreshold && memUtil < r.MemoryThreshold {
				overProvisioned = append(overProvisioned, res.ID)
			}
		}
		if res.Type == "aws_ebs_volume" || res.Type == "aws_rds_cluster_instance" {
			storageUtil := res.Metrics.StorageUtilization
			if storageUtil < r.StorageThreshold {
				overProvisioned = append(overProvisioned, res.ID)
			}
		}
	}
	if len(overProvisioned) > 0 {
		return &Signal{
			Name:        r.Name(),
			Severity:    "medium",
			Description: fmt.Sprintf("Over-provisioned resources detected: %v", overProvisioned),
			Detail:      fmt.Sprintf("CPU threshold: %.0f%%, Memory threshold: %.0f%%, Storage threshold: %.0f%%", r.CPUThreshold*100, r.MemoryThreshold*100, r.StorageThreshold*100),
		}, nil
	}
	return nil, nil
}

// CostTrendRule detects cost trends that indicate potential overspend.
type CostTrendRule struct {
	DailyCostIncreaseThreshold float64 // e.g., 0.10 means 10% daily increase
	WeeklyCostIncreaseThreshold float64
}

func (r *CostTrendRule) Name() string {
	return "cost-trend"
}

func (r *CostTrendRule) Evaluate(ctx context.Context, state *client.State) (*Signal, error) {
	if state == nil {
		return nil, fmt.Errorf("state is nil")
	}
	if len(state.CostHistory) < 2 {
		return nil, nil
	}
	// Calculate daily trend
	latest := state.CostHistory[len(state.CostHistory)-1]
	previous := state.CostHistory[len(state.CostHistory)-2]
	dailyChange := (latest.DailyCost - previous.DailyCost) / previous.DailyCost
	if math.IsNaN(dailyChange) || math.IsInf(dailyChange, 0) {
		dailyChange = 0
	}
	// Calculate weekly trend (compare last 7 days average to previous 7 days average)
	var lastWeekAvg, prevWeekAvg float64
	if len(state.CostHistory) >= 14 {
		for i := 0; i < 7; i++ {
			lastWeekAvg += state.CostHistory[len(state.CostHistory)-1-i].DailyCost
			prevWeekAvg += state.CostHistory[len(state.CostHistory)-8-i].DailyCost
		}
		lastWeekAvg /= 7
		prevWeekAvg /= 7
	}
	weeklyChange := (lastWeekAvg - prevWeekAvg) / prevWeekAvg
	if math.IsNaN(weeklyChange) || math.IsInf(weeklyChange, 0) {
		weeklyChange = 0
	}

	if dailyChange > r.DailyCostIncreaseThreshold || weeklyChange > r.WeeklyCostIncreaseThreshold {
		severity := "low"
		if dailyChange > r.DailyCostIncreaseThreshold*2 || weeklyChange > r.WeeklyCostIncreaseThreshold*2 {
			severity = "medium"
		}
		return &Signal{
			Name:        r.Name(),
			Severity:    severity,
			Description: fmt.Sprintf("Cost trend indicates potential overspend (daily change: %.2f%%, weekly change: %.2f%%)", dailyChange*100, weeklyChange*100),
			Detail:      fmt.Sprintf("Daily threshold: %.0f%%, Weekly threshold: %.0f%%", r.DailyCostIncreaseThreshold*100, r.WeeklyCostIncreaseThreshold*100),
		}, nil
	}
	return nil, nil
}

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
