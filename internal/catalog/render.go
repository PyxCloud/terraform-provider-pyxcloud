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

// RenderSGHCL renders a resolved SecurityGroupPlan into concrete cloud-provider
// Terraform HCL (aws_security_group(_rule), google_compute_firewall, or
// digitalocean_firewall). Mirrors RenderHCL: translation returns a structured
// plan, rendering to .tf happens here and drives the round-trip tests (SPEC §6).
//
// The plan's Description is already ASCII-sanitised by TranslateSecurityGroup;
// RenderSGHCL re-applies the guard so a hand-built plan can never emit a
// non-ASCII description (AWS rejects those — this caused a real incident).
func RenderSGHCL(plan SecurityGroupPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderSGAWS(plan), nil
	case ProviderGCP:
		return renderSGGCP(plan), nil
	case ProviderDigitalOcean:
		return renderSGDO(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q", plan.Provider)
	}
}

// hclCIDRList renders a string slice as an HCL list literal: ["a", "b"].
func hclCIDRList(cidrs []string) string {
	parts := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		parts = append(parts, fmt.Sprintf("%q", c))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// awsProto maps a canonical protocol to the AWS protocol token ("-1" = all).
func awsProto(proto string) string {
	if proto == ProtoAll {
		return "-1"
	}
	return proto
}

func renderSGAWS(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	desc := asciiOnly(p.Description) // ASCII guard: AWS rejects non-ASCII descriptions.
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_security_group\" %q {\n", name)
	fmt.Fprintf(&b, "  name        = %q\n", p.SGName)
	fmt.Fprintf(&b, "  description = %q\n", desc)
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_id      = aws_vpc.%s.id\n", tfName(p.NetworkName))
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.SGName)
	b.WriteString("}\n")

	for i, r := range p.Rules {
		rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
		fmt.Fprintf(&b, "\nresource \"aws_security_group_rule\" %q {\n", rn)
		fmt.Fprintf(&b, "  type              = %q\n", r.Direction)
		fmt.Fprintf(&b, "  security_group_id = aws_security_group.%s.id\n", name)
		fmt.Fprintf(&b, "  protocol          = %q\n", awsProto(r.Protocol))
		fmt.Fprintf(&b, "  from_port         = %d\n", r.FromPort)
		fmt.Fprintf(&b, "  to_port           = %d\n", r.ToPort)
		if r.SourceSG != "" {
			fmt.Fprintf(&b, "  source_security_group_id = aws_security_group.%s.id\n", tfName(r.SourceSG))
		} else {
			v4, v6 := splitCIDRs(r.CIDRs)
			if len(v4) > 0 {
				fmt.Fprintf(&b, "  cidr_blocks       = %s\n", hclCIDRList(v4))
			}
			if len(v6) > 0 {
				fmt.Fprintf(&b, "  ipv6_cidr_blocks  = %s\n", hclCIDRList(v6))
			}
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func renderSGGCP(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	desc := asciiOnly(p.Description)
	var b strings.Builder
	// GCP firewalls are direction-scoped (a single firewall is either INGRESS or
	// EGRESS). Emit one google_compute_firewall per direction that has rules,
	// each carrying its allow blocks.
	for _, dir := range []string{DirIngress, DirEgress} {
		var dirRules []RulePlan
		for _, r := range p.Rules {
			if r.Direction == dir {
				dirRules = append(dirRules, r)
			}
		}
		if len(dirRules) == 0 {
			continue
		}
		gcpDir := strings.ToUpper(dir)
		rn := fmt.Sprintf("%s_%s", name, dir)
		fmt.Fprintf(&b, "resource \"google_compute_firewall\" %q {\n", rn)
		fmt.Fprintf(&b, "  name        = \"%s-%s\"\n", tfName(p.SGName), dir)
		fmt.Fprintf(&b, "  description = %q\n", desc)
		if p.NetworkName != "" {
			fmt.Fprintf(&b, "  network     = google_compute_network.%s.id\n", tfName(p.NetworkName))
		}
		fmt.Fprintf(&b, "  direction   = %q\n", gcpDir)
		// Collect cidr scopes for this direction (GCP: source_ranges on ingress,
		// destination_ranges on egress).
		cidrs := dedupeCIDRs(dirRules)
		if len(cidrs) > 0 {
			if dir == DirIngress {
				fmt.Fprintf(&b, "  source_ranges = %s\n", hclCIDRList(cidrs))
			} else {
				fmt.Fprintf(&b, "  destination_ranges = %s\n", hclCIDRList(cidrs))
			}
		}
		for _, r := range dirRules {
			fmt.Fprintf(&b, "  allow {\n")
			fmt.Fprintf(&b, "    protocol = %q\n", r.Protocol)
			if r.Protocol == ProtoTCP || r.Protocol == ProtoUDP {
				fmt.Fprintf(&b, "    ports    = [%q]\n", portRangeString(r.FromPort, r.ToPort))
			}
			b.WriteString("  }\n")
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderSGDO(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"digitalocean_firewall\" %q {\n", name)
	fmt.Fprintf(&b, "  name = %q\n", p.SGName)
	// DO firewalls attach to droplets/tags, not VPCs; the network association is
	// carried via the droplets that join later. We expose it as a tag for intent.
	for _, r := range p.Rules {
		blockName := "inbound_rule"
		cidrKey := "source_addresses"
		if r.Direction == DirEgress {
			blockName = "outbound_rule"
			cidrKey = "destination_addresses"
		}
		fmt.Fprintf(&b, "\n  %s {\n", blockName)
		fmt.Fprintf(&b, "    protocol   = %q\n", doProto(r.Protocol))
		if r.Protocol == ProtoTCP || r.Protocol == ProtoUDP {
			fmt.Fprintf(&b, "    port_range = %q\n", portRangeString(r.FromPort, r.ToPort))
		}
		if r.CIDRs != nil {
			fmt.Fprintf(&b, "    %s = %s\n", cidrKey, hclCIDRList(r.CIDRs))
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// doProto maps a canonical protocol to a DO firewall protocol. DigitalOcean
// firewalls support tcp/udp/icmp; "all" is not a DO protocol, so it is rejected
// upstream at translate for DO via the limit/validation path — here we pass the
// canonical token through for tcp/udp/icmp.
func doProto(proto string) string {
	return proto
}

// splitCIDRs partitions CIDRs into IPv4 and IPv6 (AWS uses distinct attributes).
func splitCIDRs(cidrs []string) (v4, v6 []string) {
	for _, c := range cidrs {
		if strings.Contains(c, ":") {
			v6 = append(v6, c)
		} else {
			v4 = append(v4, c)
		}
	}
	return v4, v6
}

// dedupeCIDRs collects the unique, order-preserving CIDR set across rules.
func dedupeCIDRs(rules []RulePlan) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rules {
		for _, c := range r.CIDRs {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}
