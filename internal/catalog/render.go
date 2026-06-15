package catalog

import (
	"fmt"
	"strings"
)

// RenderHCL renders a resolved NetworkPlan into concrete cloud-provider
// Terraform HCL (aws_vpc/aws_subnet, google_compute_network/_subnetwork, or
// digitalocean_vpc). This is the "provider owns rendering" half of the
// structured-plan decision (§8): translation returns a structured NetworkPlan,
// and rendering to .tf happens here. The same renderer drives the per-provider
// `terraform plan` / real apply round-trip tests (SPEC §6).
//
// Identifiers are sanitised to valid Terraform resource names.
func RenderHCL(plan NetworkPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderAWS(plan), nil
	case ProviderGCP:
		return renderGCP(plan), nil
	case ProviderDigitalOcean:
		return renderDO(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q", plan.Provider)
	}
}

func tfName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "n_" + out
	}
	return out
}

func renderAWS(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_vpc\" %q {\n", name)
	fmt.Fprintf(&b, "  cidr_block = %q\n", p.CIDR)
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.VPCName)
	b.WriteString("}\n")
	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"aws_subnet\" %q {\n", sn)
		fmt.Fprintf(&b, "  vpc_id            = aws_vpc.%s.id\n", name)
		fmt.Fprintf(&b, "  cidr_block        = %q\n", s.CIDR)
		fmt.Fprintf(&b, "  availability_zone = %q\n", s.Zone)
		fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", s.Name)
		b.WriteString("}\n")
	}
	return b.String()
}

func renderGCP(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_compute_network\" %q {\n", name)
	fmt.Fprintf(&b, "  name                    = %q\n", tfName(p.VPCName))
	b.WriteString("  auto_create_subnetworks = false\n")
	b.WriteString("}\n")
	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"google_compute_subnetwork\" %q {\n", sn)
		fmt.Fprintf(&b, "  name          = \"%s-%d\"\n", tfName(p.VPCName), i+1)
		fmt.Fprintf(&b, "  ip_cidr_range = %q\n", s.CIDR)
		fmt.Fprintf(&b, "  region        = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  network       = google_compute_network.%s.id\n", name)
		b.WriteString("}\n")
	}
	return b.String()
}

func renderDO(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	// DO VPCs are region-scoped; subnets are not separate resources. The first
	// declared subnet CIDR (or the VPC CIDR) is the VPC ip_range.
	ipRange := p.CIDR
	if len(p.Subnets) > 0 {
		ipRange = p.Subnets[0].CIDR
	}
	fmt.Fprintf(&b, "resource \"digitalocean_vpc\" %q {\n", name)
	fmt.Fprintf(&b, "  name     = %q\n", tfName(p.VPCName))
	fmt.Fprintf(&b, "  region   = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  ip_range = %q\n", ipRange)
	b.WriteString("}\n")
	return b.String()
}
