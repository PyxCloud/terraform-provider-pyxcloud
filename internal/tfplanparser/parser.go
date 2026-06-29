package tfplanparser

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
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
				Actions []string `json:"actions"`
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
package tfplanparser

import (
	"encoding/json"
	"fmt"
	"reflect"

	tfjson "github.com/hashicorp/terraform-json"
)

type DriftDetail struct {
	Address      string   `json:"address"`
	Type         string   `json:"type"`
	ChangeAction string   `json:"change_action"`
	ChangedAttrs []string `json:"changed_attributes,omitempty"`
}

type PlanSummary struct {
	Added            int           `json:"added"`
	Changed          int           `json:"changed"`
	Removed          int           `json:"removed"`
	ResourcesChanged int           `json:"resources_changed"`
	DriftDetails     []DriftDetail `json:"drift_details,omitempty"`
}

// ParsePlan parses a tfjson.Plan to summarize resource count changes and attribute diffs.
func ParsePlan(plan *tfjson.Plan) *PlanSummary {
	if plan == nil {
		return &PlanSummary{}
	}

	summary := &PlanSummary{}

	for _, rc := range plan.ResourceChanges {
		if rc == nil || rc.Change == nil {
			continue
		}

		actions := rc.Change.Actions
		if len(actions) == 0 || (len(actions) == 1 && (actions[0] == "no-op" || actions[0] == "read")) {
			continue
		}

		isCreate := false
		isDelete := false
		isUpdate := false

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

		detail := DriftDetail{
			Address:      rc.Address,
			Type:         rc.Type,
			ChangeAction: actionStr,
		}

		// Extract attribute diffs if it is an update or replacement
		if actionStr == "update" || actionStr == "replace" {
			detail.ChangedAttrs = diffAttributes(rc.Change.Before, rc.Change.After)
		}

		summary.DriftDetails = append(summary.DriftDetails, detail)
	}

	summary.ResourcesChanged = summary.Added + summary.Changed + summary.Removed
	return summary
}

// ParsePlanJSON parses the JSON string representation of a Terraform plan.
func ParsePlanJSON(planJSON []byte) (*PlanSummary, error) {
	var plan tfjson.Plan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan JSON: %w", err)
	}
	return ParsePlan(&plan), nil
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
