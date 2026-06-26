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
