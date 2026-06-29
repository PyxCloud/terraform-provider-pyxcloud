package catalog

import (
	"fmt"
	"strings"
)

// architecture_detect.go — pd-ONTO-CAP-JR-COPYARCH
//
// DetectArchitectureMismatches scans an AssembleInput for premature-microservice
// splits and cargo-cult architecture choices before the plan is rendered to HCL.
// Findings are advisory: callers decide whether to block or warn.
//
// Detects:
//   1. Cargo-cult operator: an operator-pattern component (tracing, vault-ha, …)
//      declared with no Kubernetes cluster in the same topology — the operator
//      has nowhere to run (IsCargoCultOperator).
//   2. Premature microservices split: more than PrematureSplitVMThreshold
//      virtual-machine components that could be combined, each with <= 2 vCPUs,
//      suggesting cargo-cult splitting of a monolith into micro-VMs.
//   3. Managed Kubernetes for a single-VM workload: a `managed-kubernetes` /
//      `container-service` node present with only one virtual-machine component
//      and no scale-group — Kubernetes overhead is unwarranted.

// PrematureSplitVMThreshold is the minimum number of tiny (<=2 vCPU) VM
// components that triggers the "premature microservices split" finding.
const PrematureSplitVMThreshold = 4

// ArchitectureFinding is one architecture-mismatch advisory.
type ArchitectureFinding struct {
	// RuleID is a stable identifier for the rule that fired (e.g. "CARGO-CULT-OPERATOR").
	RuleID string
	// Severity is "WARN" (advisory) or "INFO".
	Severity string
	// ComponentName is the primary component involved ("" when topology-wide).
	ComponentName string
	// ComponentType is the canonical type of the offending component.
	ComponentType string
	// Description is a human-readable summary of the mismatch.
	Description string
	// Remediation is a concrete suggestion to fix the mismatch.
	Remediation string
}

// DetectArchitectureMismatches runs all architecture-mismatch rules against in
// and returns a (possibly empty) slice of advisory findings. It never errors —
// a malformed input simply produces no findings for rules that cannot evaluate.
func DetectArchitectureMismatches(in AssembleInput) []ArchitectureFinding {
	var findings []ArchitectureFinding

	// Collect all component types for topology-level checks.
	allTypes := make([]string, 0, len(in.Components))
	for _, c := range in.Components {
		allTypes = append(allTypes, c.Type)
	}

	// ── Rule 1: Cargo-cult operator (operator component, no cluster) ──────────
	for _, c := range in.Components {
		if IsCargoCultOperator(c.Type, allTypes) {
			findings = append(findings, ArchitectureFinding{
				RuleID:        "CARGO-CULT-OPERATOR",
				Severity:      "WARN",
				ComponentName: c.Name,
				ComponentType: c.Type,
				Description: fmt.Sprintf(
					"component %q (type %q) uses the Kubernetes operator pattern "+
						"but no managed-kubernetes or container-service node is present in the topology; "+
						"the operator has nowhere to run",
					c.Name, c.Type,
				),
				Remediation: "Add a managed-kubernetes or container-service component to host the operator, " +
					"or replace this component with its managed-cloud or VM-mitigation equivalent.",
			})
		}
	}

	// ── Rule 2: Premature microservices split (many tiny VMs) ─────────────────
	var tinyVMs []string
	for _, c := range in.Components {
		if c.Type != "virtual-machine" || c.VM == nil {
			continue
		}
		cpu := atoiOrZero(c.VM.CPU)
		if cpu > 0 && cpu <= 2 {
			tinyVMs = append(tinyVMs, c.Name)
		}
	}
	if len(tinyVMs) >= PrematureSplitVMThreshold {
		findings = append(findings, ArchitectureFinding{
			RuleID:        "PREMATURE-MICROSERVICES-SPLIT",
			Severity:      "WARN",
			ComponentName: "",
			ComponentType: "virtual-machine",
			Description: fmt.Sprintf(
				"%d virtual-machine components with <=2 vCPUs detected (%s); "+
					"this pattern suggests a premature microservices split of a monolith into micro-VMs, "+
					"which adds operational overhead without scalability benefit",
				len(tinyVMs), strings.Join(tinyVMs, ", "),
			),
			Remediation: "Consider consolidating services onto fewer, appropriately-sized VMs " +
				"or migrating to a virtual-machine-scale-group / container-service if horizontal scaling is needed.",
		})
	}

	// ── Rule 3: Managed Kubernetes for a single-VM workload ───────────────────
	hasK8s := false
	vmCount := 0
	scaleGroupCount := 0
	for _, c := range in.Components {
		switch c.Type {
		case "managed-kubernetes", "container-service":
			hasK8s = true
		case "virtual-machine":
			vmCount++
		case "virtual-machine-scale-group":
			scaleGroupCount++
		}
	}
	if hasK8s && vmCount == 1 && scaleGroupCount == 0 {
		findings = append(findings, ArchitectureFinding{
			RuleID:        "KUBERNETES-FOR-SINGLE-VM",
			Severity:      "WARN",
			ComponentName: "",
			ComponentType: "managed-kubernetes",
			Description: "a managed-kubernetes / container-service cluster is declared alongside " +
				"only a single virtual-machine and no scale-group; Kubernetes control-plane overhead " +
				"is disproportionate for a single-instance workload",
			Remediation: "Remove the managed-kubernetes component and run the workload directly on the " +
				"virtual-machine, or replace the VM with a virtual-machine-scale-group if horizontal " +
				"scaling is the goal.",
		})
	}

	return findings
}
