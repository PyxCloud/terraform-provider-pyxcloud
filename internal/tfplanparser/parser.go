package tfplanparser

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// CostSignal represents a single cost-related signal extracted from a Terraform plan.
type CostSignal struct {
	ResourceAddr string  `json:"resource_addr"`
	ResourceType string  `json:"resource_type"`
	Action       string  `json:"action"` // "create", "delete", "update", "noop", "read"
	MonthlyCost  float64 `json:"monthly_cost,omitempty"`
	HourlyCost   float64 `json:"hourly_cost,omitempty"`
	Currency     string  `json:"currency,omitempty"`
	Provider     string  `json:"provider,omitempty"`
	Region       string  `json:"region,omitempty"`
	InstanceType string  `json:"instance_type,omitempty"`
	StorageGB    float64 `json:"storage_gb,omitempty"`
	Count        int     `json:"count,omitempty"`
}

// ExtractCostSignals parses a Terraform plan JSON and returns a slice of CostSignal.
// It extracts cost-related attributes from resource changes.
func ExtractCostSignals(planJSON []byte) ([]CostSignal, error) {
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string               `json:"actions"`
				After   map[string]interface{} `json:"after"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan JSON: %w", err)
	}

	var signals []CostSignal
	for _, rc := range plan.ResourceChanges {
		if len(rc.Change.Actions) == 0 {
			continue
		}
		action := rc.Change.Actions[0]
		after := rc.Change.After
		if after == nil {
			continue
		}

		signal := CostSignal{
			ResourceAddr: rc.Address,
			ResourceType: rc.Type,
			Action:       action,
		}

		// Extract monthly cost
		if v, ok := after["monthly_cost"]; ok {
			signal.MonthlyCost = toFloat64(v)
		}
		// Extract hourly cost
		if v, ok := after["hourly_cost"]; ok {
			signal.HourlyCost = toFloat64(v)
		}
		// Extract currency
		if v, ok := after["currency"]; ok {
			signal.Currency = toString(v)
		}
		// Extract provider
		if v, ok := after["provider"]; ok {
			signal.Provider = toString(v)
		}
		// Extract region
		if v, ok := after["region"]; ok {
			signal.Region = toString(v)
		}
		// Extract instance_type
		if v, ok := after["instance_type"]; ok {
			signal.InstanceType = toString(v)
		}
		// Extract storage_gb
		if v, ok := after["storage_gb"]; ok {
			signal.StorageGB = toFloat64(v)
		}
		// Extract count (from count or for_each)
		if v, ok := after["count"]; ok {
			signal.Count = toInt(v)
		} else if v, ok := after["for_each"]; ok {
			// for_each is a map; count is the number of keys
			if m, ok := v.(map[string]interface{}); ok {
				signal.Count = len(m)
			}
		} else {
			signal.Count = 1
		}

		signals = append(signals, signal)
	}
	return signals, nil
}

// DriftDetail describes one resource change in a Terraform plan.
type DriftDetail struct {
	Address      string   `json:"address"`
	Type         string   `json:"type"`
	ChangeAction string   `json:"change_action"`
	ChangedAttrs []string `json:"changed_attributes,omitempty"`
}

// PlanSummary is a high-level summary of resource changes in a Terraform plan.
type PlanSummary struct {
	Added            int           `json:"added"`
	Changed          int           `json:"changed"`
	Removed          int           `json:"removed"`
	ResourcesChanged int           `json:"resources_changed"`
	DriftDetails     []DriftDetail `json:"drift_details,omitempty"`
}

// ParsePlan parses a raw plan JSON map (as produced by terraform show -json) to summarize
// resource count changes and attribute diffs.
func ParsePlan(plan map[string]interface{}) *PlanSummary {
	if plan == nil {
		return &PlanSummary{}
	}

	summary := &PlanSummary{}

	rcList, _ := plan["resource_changes"].([]interface{})
	for _, item := range rcList {
		rc, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		change, ok := rc["change"].(map[string]interface{})
		if !ok {
			continue
		}
		actionsRaw, _ := change["actions"].([]interface{})
		if len(actionsRaw) == 0 {
			continue
		}
		actions := make([]string, 0, len(actionsRaw))
		for _, a := range actionsRaw {
			if s, ok := a.(string); ok {
				actions = append(actions, s)
			}
		}
		if len(actions) == 1 && (actions[0] == "no-op" || actions[0] == "read") {
			continue
		}

		isCreate, isDelete, isUpdate := false, false, false
		for _, action := range actions {
			switch action {
			case "create":
				isCreate = true
			case "delete":
				isDelete = true
			case "update":
				isUpdate = true
			}
		}

		var actionStr string
		if isCreate && isDelete {
			actionStr = "replace"
			summary.Changed++
		} else if isCreate {
			actionStr = "create"
			summary.Added++
		} else if isDelete {
			actionStr = "delete"
			summary.Removed++
		} else if isUpdate {
			actionStr = "update"
			summary.Changed++
		} else {
			continue
		}

		addr, _ := rc["address"].(string)
		rType, _ := rc["type"].(string)

		detail := DriftDetail{
			Address:      addr,
			Type:         rType,
			ChangeAction: actionStr,
		}

		if actionStr == "update" || actionStr == "replace" {
			before := change["before"]
			after := change["after"]
			detail.ChangedAttrs = diffAttributes(before, after)
		}

		summary.DriftDetails = append(summary.DriftDetails, detail)
	}

	summary.ResourcesChanged = summary.Added + summary.Changed + summary.Removed
	return summary
}

// ParsePlanJSON parses the JSON string representation of a Terraform plan.
func ParsePlanJSON(planJSON []byte) (*PlanSummary, error) {
	var plan map[string]interface{}
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan JSON: %w", err)
	}
	return ParsePlan(plan), nil
}

func diffAttributes(before, after interface{}) []string {
	return recursiveDiff(before, after, "")
}

func recursiveDiff(before, after interface{}, prefix string) []string {
	var diffs []string
	beforeMap, okBefore := before.(map[string]interface{})
	afterMap, okAfter := after.(map[string]interface{})

	if !okBefore || !okAfter {
		if !reflect.DeepEqual(before, after) {
			if prefix != "" {
				return []string{prefix}
			}
			return []string{"value"}
		}
		return nil
	}

	// Compare keys
	allKeys := make(map[string]bool)
	for k := range beforeMap {
		allKeys[k] = true
	}
	for k := range afterMap {
		allKeys[k] = true
	}

	for k := range allKeys {
		valBefore, okB := beforeMap[k]
		valAfter, okA := afterMap[k]

		keyPath := k
		if prefix != "" {
			keyPath = prefix + "." + k
		}

		if okB != okA {
			diffs = append(diffs, keyPath)
		} else {
			_, bIsMap := valBefore.(map[string]interface{})
			_, aIsMap := valAfter.(map[string]interface{})
			if bIsMap && aIsMap {
				diffs = append(diffs, recursiveDiff(valBefore, valAfter, keyPath)...)
			} else if !reflect.DeepEqual(valBefore, valAfter) {
				diffs = append(diffs, keyPath)
			}
		}
	}

	return diffs
}

// OverProvisioningRule detects resources that are over-provisioned based on estimated cost signals.
// It flags resource types that appear large relative to a cost-per-unit baseline.
type OverProvisioningRule struct {
	// MaxMonthlyCostPerUnit is the threshold above which a single resource unit is
	// considered over-provisioned (in USD). Zero disables per-unit checks.
	MaxMonthlyCostPerUnit float64
}

// CostTrendRule detects when the total estimated cost of new resources in a plan
// exceeds a configured budget threshold.
type CostTrendRule struct {
	// MonthlyBudget is the total monthly cost ceiling for all resources in the plan.
	MonthlyBudget float64
}

// EvaluateOverProvisioning checks cost signals for over-provisioned resources.
// Returns a list of advisory messages (non-empty = anomaly detected).
func (r *OverProvisioningRule) EvaluateOverProvisioning(signals []CostSignal) []string {
	if r.MaxMonthlyCostPerUnit <= 0 {
		return nil
	}
	var advisories []string
	for _, s := range signals {
		if s.MonthlyCost > r.MaxMonthlyCostPerUnit {
			advisories = append(advisories, fmt.Sprintf(
				"resource %s (%s) monthly cost %.2f exceeds per-unit threshold %.2f",
				s.ResourceAddr, s.ResourceType, s.MonthlyCost, r.MaxMonthlyCostPerUnit,
			))
		}
	}
	return advisories
}

// EvaluateCostTrend checks cost signals against a total monthly budget.
// Returns a list of advisory messages (non-empty = budget exceeded).
func (r *CostTrendRule) EvaluateCostTrend(signals []CostSignal) []string {
	if r.MonthlyBudget <= 0 {
		return nil
	}
	total := 0.0
	for _, s := range signals {
		total += s.MonthlyCost
	}
	if total > r.MonthlyBudget {
		return []string{fmt.Sprintf(
			"total estimated monthly cost %.2f exceeds budget %.2f",
			total, r.MonthlyBudget,
		)}
	}
	return nil
}

// toFloat64 converts an interface{} to float64, handling numbers and strings.
func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case json.Number:
		f, _ := val.Float64()
		return f
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err == nil {
			return f
		}
		return 0
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}

// toString converts an interface{} to string.
func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// toInt converts an interface{} to int.
func toInt(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(math.Round(val))
	case json.Number:
		i, _ := val.Int64()
		return int(i)
	case string:
		i, err := strconv.Atoi(val)
		if err == nil {
			return i
		}
		return 0
	case int:
		return val
	case int64:
		return int(val)
	default:
		return 0
	}
}

// isSecuritySensitive reports whether the resource type is security-sensitive
// (security group, IAM, VPC, subnet, firewall, network, etc.).
func isSecuritySensitive(resourceType string) bool {
	norm := strings.ToLower(resourceType)
	for _, k := range []string{"sg", "security_group", "iam", "role", "policy", "vpc", "subnet", "nacl", "firewall", "network"} {
		if norm == k || strings.Contains(norm, k) {
			return true
		}
	}
	return false
}
