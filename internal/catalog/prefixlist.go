package catalog

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// PrefixList is the abstract `prefix-list` component: a named, reusable CIDR set
// referenced by security-group rules — the canonical form of the per-provider
// scripts' aws_ec2_managed_prefix_list glue.
//
//   - AWS: aws_ec2_managed_prefix_list.
//   - GCP / DigitalOcean: UNSUPPORTED (no managed-prefix-list primitive). Clean
//     plan-time error — inline the CIDRs into firewall rules instead.

// PrefixEntry is one CIDR (+ description) in the list.
type PrefixEntry struct {
	CIDR        string
	Description string
}

// PrefixListSpec is the abstract prefix-list description.
type PrefixListSpec struct {
	Name     string
	Region   string
	Provider string
	Entries  []PrefixEntry
}

// PrefixListPlan is the resolved concrete plan.
type PrefixListPlan struct {
	Provider      string        `json:"provider"`
	CSP           string        `json:"csp"`
	RegionName    string        `json:"region_name"`
	CSPRegion     string        `json:"csp_region"`
	Name          string        `json:"name"`
	AddressFamily string        `json:"address_family"`
	Entries       []PrefixEntry `json:"entries"`
	ResourceType  string        `json:"resource_type"`
}

// TranslatePrefixList resolves a PrefixListSpec (AWS only).
func TranslatePrefixList(ctx context.Context, cat RegionCatalog, spec PrefixListSpec) (PrefixListPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: name is required")
	}
	if len(spec.Entries) == 0 {
		return PrefixListPlan{}, fmt.Errorf("prefix-list %q: at least one entry is required", spec.Name)
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: unknown provider %q", spec.Provider)
	}
	if !strings.EqualFold(spec.Provider, ProviderAWS) {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: only AWS has a managed prefix list; on %q inline the "+
			"CIDRs into firewall rules instead (hard plan-time error)", spec.Provider)
	}
	family := "IPv4"
	for _, e := range spec.Entries {
		ip, _, err := net.ParseCIDR(strings.TrimSpace(e.CIDR))
		if err != nil {
			return PrefixListPlan{}, fmt.Errorf("prefix-list %q: invalid CIDR %q", spec.Name, e.CIDR)
		}
		if ip.To4() == nil {
			family = "IPv6"
		}
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return PrefixListPlan{}, err
	}
	return PrefixListPlan{
		Provider: ProviderAWS, CSP: row.CSP, RegionName: row.RegionName, CSPRegion: row.CSPRegion,
		Name: spec.Name, AddressFamily: family, Entries: spec.Entries, ResourceType: "aws_ec2_managed_prefix_list",
	}, nil
}

// RenderPrefixListHCL renders a PrefixListPlan (AWS).
func RenderPrefixListHCL(p PrefixListPlan) (string, error) {
	if p.Provider != ProviderAWS {
		return "", fmt.Errorf("prefix-list: render unsupported for provider %q", p.Provider)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_ec2_managed_prefix_list\" %q {\n", tfName(p.Name))
	fmt.Fprintf(&b, "  name           = %q\n", p.Name)
	fmt.Fprintf(&b, "  address_family = %q\n", p.AddressFamily)
	fmt.Fprintf(&b, "  max_entries    = %d\n", len(p.Entries))
	for _, e := range p.Entries {
		b.WriteString("  entry {\n")
		fmt.Fprintf(&b, "    cidr        = %q\n", e.CIDR)
		if e.Description != "" {
			fmt.Fprintf(&b, "    description = %q\n", asciiOnly(e.Description))
		}
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String(), nil
}
