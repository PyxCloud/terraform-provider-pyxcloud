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

// AssembleIAM is the canonical IAM config for an `iam` component.
type AssembleIAM struct {
	AssumeService     string
	InlinePolicies    []IAMPolicy
	ManagedPolicyARNs []string
	InstanceProfile   bool
}

// AssembleComponent is one canonical component in the environment.
type AssembleComponent struct {
	Name       string
	Type       string
	Count      int
	VM         *AssembleVM
	IAM        *AssembleIAM
	Monitoring *AssembleMonitoring
	DNS        *AssembleDNS
}

// AssembleMonitoring is the canonical monitoring config for a `monitoring` component.
type AssembleMonitoring struct {
	LogGroups []LogGroup
	Alarms    []MetricAlarm
}

// AssembleDNS is the canonical Cloudflare DNS config for a `dns` component.
type AssembleDNS struct {
	ZoneID  string
	Records []DNSRecord
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
	cidr := in.CIDR
	if cidr == "" {
		cidr = "10.0.0.0/16"
	}
	subnets := in.Subnets
	if len(subnets) == 0 {
		subnets = []string{"10.0.1.0/24"}
	}

	var docs []string
	needsCloudflare := false

	// A network (VPC + subnet) is only needed when the environment places VMs.
	// A DNS-only / IAM-only env must NOT synthesise a VPC.
	hasVM := false
	for _, c := range in.Components {
		if c.Type == "virtual-machine" || c.Type == "virtual-machine-scale-group" {
			hasVM = true
			break
		}
	}

	netName := in.Name + "-net"
	sgName := in.Name + "-sg"
	subnetName := ""
	vmSG := ""

	// 1. Network (VPC + subnets) — only when VMs are present.
	if hasVM {
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
		subnetName = netPlan.Subnets[0].Name
	}

	// 2. Security group — only when VMs are present AND ports are exposed. A SG with
	//    no rule is rejected by the translator, so with no expose we skip it and the
	//    VMs fall back to the VPC default SG. vmSG is the name to wire onto VMs ("" = none).
	if hasVM && len(in.Expose) > 0 {
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
		case "iam":
			iamSpec := IAMSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.IAM != nil {
				iamSpec.AssumeService = c.IAM.AssumeService
				iamSpec.InlinePolicies = c.IAM.InlinePolicies
				iamSpec.ManagedPolicyARNs = c.IAM.ManagedPolicyARNs
				iamSpec.InstanceProfile = c.IAM.InstanceProfile
			}
			iamPlan, err := TranslateIAM(ctx, cat, iamSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			iamHCL, err := RenderIAMHCL(iamPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, iamHCL)
		case "monitoring":
			if c.Monitoring == nil {
				return nil, fmt.Errorf("component %q (monitoring): config is required", c.Name)
			}
			monPlan, err := TranslateMonitoring(ctx, cat, MonitoringSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				LogGroups: c.Monitoring.LogGroups, Alarms: c.Monitoring.Alarms,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			monHCL, err := RenderMonitoringHCL(monPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, monHCL)
		case "dns":
			if c.DNS == nil {
				return nil, fmt.Errorf("component %q (dns): config is required", c.Name)
			}
			dnsPlan, err := TranslateCloudflareDNS(ctx, CloudflareDNSSpec{
				Name: c.Name, ZoneID: c.DNS.ZoneID, Records: c.DNS.Records,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			dnsHCL, err := RenderCloudflareDNSHCL(dnsPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			needsCloudflare = true
			docs = append(docs, dnsHCL)
		default:
			return nil, fmt.Errorf("component %q: type %q is not yet supported by local assembly "+
				"(coverage is added component by component, AWS first)", c.Name, c.Type)
		}
	}
	// Pin the Cloudflare provider source when any Cloudflare component is present
	// (terraform would otherwise assume the non-existent hashicorp/cloudflare).
	if needsCloudflare {
		docs = append([]string{cloudflareRequiredProviders}, docs...)
	}
	return docs, nil
}
