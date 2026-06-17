package catalog

import (
	"context"
	"fmt"
)

// AssembleHCL is the LOCAL translation entry point: it turns a canonical
// environment (provider + abstract region + components) into the concrete
// per-provider terraform documents, by dispatching each component to the same
// catalog Translate*/Render*HCL pair the round-trip harness uses. This is what
// pyxcloud_environment (Mode A) runs locally with the ambient provider env
// credentials — no backend round-trip, no token, fully testable in Go.
//
// Placement is synthesised: one network (VPC + subnet) and one security group
// per environment, with every VM wired into them. Component config beyond VM
// sizing (rich SG rules, LB listeners, …) will be threaded as the environment
// schema grows; today the env resource feeds VM-centric topologies.
//
// A component type with no catalog translation is a HARD error (never a silent
// drop) — coverage is added component by component, AWS first.

// AssembleVM is the canonical VM sizing for a component (strings mirror the
// resource schema; parsed to ints here).
type AssembleVM struct {
	Architecture    string
	CPU             string
	RAM             string
	OS              string
	OSVersion       string
	UserData        string
	InstanceProfile string
}

// AssembleComponent is one canonical component in the environment.
type AssembleComponent struct {
	Name  string
	Type  string
	Count int
	VM    *AssembleVM
}

// AssembleInput is the catalog-native environment description (no client import,
// so the catalog stays dependency-free).
type AssembleInput struct {
	Name     string
	Provider string
	Region   string
	CIDR     string   // optional; defaults to 10.0.0.0/16
	Subnets  []string // optional; defaults to a single 10.0.1.0/24
	Expose   []int    // optional security-group TCP expose ports
	Components []AssembleComponent
}

// AssembleHCL translates the environment to concrete terraform documents.
func AssembleHCL(ctx context.Context, cat Catalog, in AssembleInput) ([]string, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("environment: name is required")
	}
	netName := in.Name + "-net"
	sgName := in.Name + "-sg"
	cidr := in.CIDR
	if cidr == "" {
		cidr = "10.0.0.0/16"
	}
	subnets := in.Subnets
	if len(subnets) == 0 {
		subnets = []string{"10.0.1.0/24"}
	}

	var docs []string

	// 1. Network (VPC + subnets).
	netPlan, err := TranslateNetwork(ctx, cat, NetworkSpec{
		Name: netName, Region: in.Region, Provider: in.Provider, CIDR: cidr, Subnets: subnets,
	})
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	netHCL, err := RenderHCL(netPlan)
	if err != nil {
		return nil, fmt.Errorf("network render: %w", err)
	}
	docs = append(docs, netHCL)
	if len(netPlan.Subnets) == 0 {
		return nil, fmt.Errorf("network produced no subnets")
	}
	subnetName := netPlan.Subnets[0].Name

	// 2. Security group — only when the environment exposes ports. A SG with no
	//    rule is rejected by the translator, so with no expose we skip it and the
	//    VMs fall back to the VPC default SG. vmSG is the name to wire onto VMs ("" = none).
	vmSG := ""
	if len(in.Expose) > 0 {
		sgPlan, err := TranslateSecurityGroup(ctx, cat, SecurityGroupSpec{
			Name: sgName, Network: netName, Region: in.Region, Provider: in.Provider,
			Description: in.Name + " environment", Expose: in.Expose,
		})
		if err != nil {
			return nil, fmt.Errorf("security-group: %w", err)
		}
		sgHCL, err := RenderSGHCL(sgPlan)
		if err != nil {
			return nil, fmt.Errorf("security-group render: %w", err)
		}
		docs = append(docs, sgHCL)
		vmSG = sgName
	}

	// 3. Components.
	for _, c := range in.Components {
		switch c.Type {
		case "virtual-machine", "virtual-machine-scale-group":
			if c.VM == nil {
				return nil, fmt.Errorf("component %q (%s): vm sizing is required", c.Name, c.Type)
			}
			vmPlan, err := TranslateVM(ctx, cat, VMSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				Architecture: c.VM.Architecture, CPU: atoiOrZero(c.VM.CPU), RAM: atoiOrZero(c.VM.RAM),
				OS: c.VM.OS, OSVersion: c.VM.OSVersion, Count: c.Count,
				Network: netName, Subnet: subnetName, SecurityGroup: vmSG,
				// UserData/InstanceProfile wired once PR #27 (VMSpec user_data) lands.
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			vmHCL, err := RenderVMHCL(vmPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, vmHCL)
		default:
			return nil, fmt.Errorf("component %q: type %q is not yet supported by local assembly "+
				"(coverage is added component by component, AWS first)", c.Name, c.Type)
		}
	}
	return docs, nil
}
