// Command pyxnet-render renders a canonical PyxCloud network or security-group
// fixture into concrete cloud-provider Terraform HCL via the catalog. It is the
// bridge used by the per-provider `terraform plan` / real apply round-trip tests
// (SPEC §6): generate the provider config from a canonical fixture, then
// plan/apply it.
//
// Usage:
//
//	pyxnet-render -fixture place.json -provider aws                 > aws_vpc.tf
//	pyxnet-render -fixture place.json -provider gcp                 > gcp_vpc.tf
//	pyxnet-render -fixture place.json -provider digitalocean        > do_vpc.tf
//	pyxnet-render -fixture sg.json -component security-group -provider aws > aws_sg.tf
//
// The fixture is the abstract, provider-neutral place; -provider selects which
// concrete provider to descend it to, and -component selects which component to
// render (default `network`, the region+VPC component).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
)

// fixture is the canonical, provider-neutral place description. It carries both
// the network (region+VPC) and an optional security-group, so a single fixture
// can drive either component.
type fixture struct {
	Name    string   `json:"name"`
	Region  string   `json:"region"`
	CIDR    string   `json:"cidr"`
	Subnets []string `json:"subnets"`
	// SecurityGroup is the optional canonical security-group for this place.
	SecurityGroup *sgFixture `json:"security_group,omitempty"`
	// VirtualMachine is the optional canonical virtual-machine for this place.
	VirtualMachine *vmFixture `json:"virtual_machine,omitempty"`
	// ScaleGroup is the optional canonical virtual-machine-scale-group.
	ScaleGroup *sgScaleFixture `json:"scale_group,omitempty"`
}

// sgScaleFixture is the canonical virtual-machine-scale-group description.
type sgScaleFixture struct {
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	CPU          int    `json:"cpu"`
	RAM          int    `json:"ram"`
	OS           string `json:"os"`
	OSVersion    string `json:"os_version"`
	Min          int    `json:"min"`
	Max          int    `json:"max"`
	Desired      int    `json:"desired"`
	Health       string `json:"health"`
	// SecurityGroup is the canonical SG name to attach; defaults to the fixture SG.
	SecurityGroup string `json:"security_group"`
}

// vmFixture is the canonical virtual-machine description embedded in a fixture.
type vmFixture struct {
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	CPU          int    `json:"cpu"`
	RAM          int    `json:"ram"`
	OS           string `json:"os"`
	OSVersion    string `json:"os_version"`
	Count        int    `json:"count"`
	// Subnet / SecurityGroup are the canonical names of the sibling components
	// this VM wires into; default to the first subnet and the fixture's SG.
	Subnet        string `json:"subnet"`
	SecurityGroup string `json:"security_group"`
}

// sgFixture is the canonical security-group description embedded in a fixture.
type sgFixture struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Expose      []int         `json:"expose"`
	Rules       []ruleFixture `json:"rules"`
}

type ruleFixture struct {
	Direction string   `json:"direction"`
	Protocol  string   `json:"protocol"`
	FromPort  int      `json:"from_port"`
	ToPort    int      `json:"to_port"`
	CIDRs     []string `json:"cidrs"`
	SourceSG  string   `json:"source_sg"`
}

func main() {
	fixturePath := flag.String("fixture", "", "path to canonical fixture JSON")
	provider := flag.String("provider", "", "target provider: aws | gcp | digitalocean")
	component := flag.String("component", "network", "component to render: network | security-group")
	flag.Parse()

	if *fixturePath == "" || *provider == "" {
		fmt.Fprintln(os.Stderr, "usage: pyxnet-render -fixture f.json -provider aws|gcp|digitalocean [-component network|security-group]")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*fixturePath)
	if err != nil {
		fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		fatal(fmt.Errorf("parse fixture: %w", err))
	}

	cat := catalog.MustEmbedded()
	switch *component {
	case "network":
		renderNetwork(cat, f, *provider)
	case "security-group", "sg":
		renderSecurityGroup(cat, f, *provider)
	case "virtual-machine", "vm":
		renderVM(cat, f, *provider)
	case "scale-group", "virtual-machine-scale-group", "asg":
		renderScaleGroup(cat, f, *provider)
	default:
		fatal(fmt.Errorf("unknown component %q (network | security-group | virtual-machine | scale-group)", *component))
	}
}

func renderScaleGroup(cat catalog.VMCatalog, f fixture, provider string) {
	if f.ScaleGroup == nil {
		fatal(fmt.Errorf("fixture has no scale_group block"))
	}
	sg := f.ScaleGroup
	name := sg.Name
	if name == "" {
		name = f.Name
	}
	// Spread the group across all the network's subnets (multi-AZ).
	subnets := make([]string, 0, len(f.Subnets))
	for i := range f.Subnets {
		subnets = append(subnets, fmt.Sprintf("%s-subnet-%d", f.Name, i+1))
	}
	secGroup := sg.SecurityGroup
	if secGroup == "" && f.SecurityGroup != nil {
		secGroup = f.SecurityGroup.Name
	}
	plan, err := catalog.TranslateScaleGroup(context.Background(), cat, catalog.ScaleGroupSpec{
		Name:          name,
		Region:        f.Region,
		Provider:      provider,
		Architecture:  sg.Architecture,
		CPU:           sg.CPU,
		RAM:           sg.RAM,
		OS:            sg.OS,
		OSVersion:     sg.OSVersion,
		Min:           sg.Min,
		Max:           sg.Max,
		Desired:       sg.Desired,
		Health:        sg.Health,
		Network:       f.Name,
		Subnets:       subnets,
		SecurityGroup: secGroup,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderScaleGroupHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderVM(cat catalog.VMCatalog, f fixture, provider string) {
	if f.VirtualMachine == nil {
		fatal(fmt.Errorf("fixture has no virtual_machine block"))
	}
	vm := f.VirtualMachine
	name := vm.Name
	if name == "" {
		name = f.Name
	}
	// Default the subnet to the first network subnet (production-subnet-1) and
	// the SG to the fixture's security-group, so a VM in a VPC+SG wires up.
	subnet := vm.Subnet
	if subnet == "" && len(f.Subnets) > 0 {
		subnet = fmt.Sprintf("%s-subnet-1", f.Name)
	}
	sg := vm.SecurityGroup
	if sg == "" && f.SecurityGroup != nil {
		sg = f.SecurityGroup.Name
	}
	plan, err := catalog.TranslateVM(context.Background(), cat, catalog.VMSpec{
		Name:          name,
		Region:        f.Region,
		Provider:      provider,
		Architecture:  vm.Architecture,
		CPU:           vm.CPU,
		RAM:           vm.RAM,
		OS:            vm.OS,
		OSVersion:     vm.OSVersion,
		Count:         vm.Count,
		Network:       f.Name,
		Subnet:        subnet,
		SecurityGroup: sg,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderVMHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderNetwork(cat catalog.RegionCatalog, f fixture, provider string) {
	plan, err := catalog.TranslateNetwork(context.Background(), cat, catalog.NetworkSpec{
		Name:     f.Name,
		Region:   f.Region,
		Provider: provider,
		CIDR:     f.CIDR,
		Subnets:  f.Subnets,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderSecurityGroup(cat catalog.RegionCatalog, f fixture, provider string) {
	if f.SecurityGroup == nil {
		fatal(fmt.Errorf("fixture has no security_group block"))
	}
	sg := f.SecurityGroup
	rules := make([]catalog.SecurityRule, 0, len(sg.Rules))
	for _, r := range sg.Rules {
		rules = append(rules, catalog.SecurityRule{
			Direction: r.Direction,
			Protocol:  r.Protocol,
			FromPort:  r.FromPort,
			ToPort:    r.ToPort,
			CIDRs:     r.CIDRs,
			SourceSG:  r.SourceSG,
		})
	}
	name := sg.Name
	if name == "" {
		name = f.Name
	}
	plan, err := catalog.TranslateSecurityGroup(context.Background(), cat, catalog.SecurityGroupSpec{
		Name:        name,
		Network:     f.Name,
		Region:      f.Region,
		Provider:    provider,
		Description: sg.Description,
		Expose:      sg.Expose,
		Rules:       rules,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderSGHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "pyxnet-render:", err)
	os.Exit(1)
}
