package catalog

import (
	"fmt"
	"strings"
)

// PrefixListSpec is a managed prefix list — a named, reusable set of CIDRs
// referenced by security-group rules (e.g. an allow-list of office/CF egress
// ranges). AWS-complete (aws_ec2_managed_prefix_list); others hard-unsupported.
type PrefixListSpec struct {
	Name       string
	Provider   string
	AddressFam string   // IPv4 (default) | IPv6
	Entries    []string // CIDRs
	MaxEntries int      // capacity; defaults to len(Entries) when 0
}

// PrefixListPlan is the deterministic concrete translation.
type PrefixListPlan struct {
	Provider     string   `json:"provider"`
	CSP          string   `json:"csp"`
	Name         string   `json:"name"`
	AddressFam   string   `json:"address_family"`
	Entries      []string `json:"entries"`
	MaxEntries   int      `json:"max_entries"`
	ResourceType string   `json:"resource_type"`
}

// TranslatePrefixList resolves a PrefixListSpec into a concrete plan.
func TranslatePrefixList(spec PrefixListSpec) (PrefixListPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: unknown provider %q", spec.Provider)
	}
	if len(spec.Entries) == 0 {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: declare at least one CIDR entry")
	}
	fam := strings.TrimSpace(spec.AddressFam)
	if fam == "" {
		fam = "IPv4"
	}
	if fam != "IPv4" && fam != "IPv6" {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: address_family must be IPv4 or IPv6, got %q", fam)
	}
	max := spec.MaxEntries
	if max < len(spec.Entries) {
		max = len(spec.Entries)
	}
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	if provider != ProviderAWS {
		return PrefixListPlan{}, fmt.Errorf("prefix-list: unsupported on provider %q (supported: aws). "+
			"Hard plan-time error, never an invented resource", provider)
	}
	return PrefixListPlan{
		Provider: provider, CSP: csp, Name: spec.Name, AddressFam: fam,
		Entries: spec.Entries, MaxEntries: max, ResourceType: "aws_ec2_managed_prefix_list",
	}, nil
}

// RenderPrefixListHCL renders a resolved plan.
func RenderPrefixListHCL(plan PrefixListPlan) (string, error) {
	if plan.Provider != ProviderAWS {
		return "", fmt.Errorf("prefix-list: no renderer for provider %q", plan.Provider)
	}
	var b strings.Builder
	rn := tfName(plan.Name)
	fmt.Fprintf(&b, "resource \"aws_ec2_managed_prefix_list\" %q {\n", rn)
	fmt.Fprintf(&b, "  name           = %q\n", plan.Name)
	fmt.Fprintf(&b, "  address_family = %q\n", plan.AddressFam)
	fmt.Fprintf(&b, "  max_entries    = %d\n", plan.MaxEntries)
	for _, cidr := range plan.Entries {
		b.WriteString("  entry {\n")
		fmt.Fprintf(&b, "    cidr = %q\n", cidr)
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String(), nil
}
