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
	default:
		fatal(fmt.Errorf("unknown component %q (network | security-group)", *component))
	}
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
