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

// RenderVMHCL renders a resolved VMPlan into concrete cloud-provider Terraform
// HCL (aws_instance, google_compute_instance, or digitalocean_droplet). Mirrors
// RenderHCL / RenderSGHCL: translation returns a structured plan, rendering to
// .tf happens here and drives the per-provider round-trip tests (SPEC 6).
// `count` becomes N concrete instances, wired to the subnet + security-group of
// the sibling components.
func RenderVMHCL(plan VMPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderVMAWS(plan), nil
	case ProviderGCP:
		return renderVMGCP(plan), nil
	case ProviderDigitalOcean:
		return renderVMDO(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q", plan.Provider)
	}
}

// subnetResourceLabel maps a canonical subnet name to the Terraform resource
// LABEL the network component emits for that subnet, so the VM references the
// real resource (not the human subnet name). The network renderer names AWS
// subnets / GCP subnetworks "<tfName(network)>_<n>" where the subnet plan name
// is "<network>-subnet-<n>"; we recover <n> from the plan name's suffix. When
// the suffix is missing (a custom subnet name), we fall back to tfName(subnet).
func subnetResourceLabel(networkName, subnetName string) string {
	const sep = "-subnet-"
	if networkName != "" {
		if idx := strings.LastIndex(subnetName, sep); idx >= 0 {
			if suffix := subnetName[idx+len(sep):]; suffix != "" {
				return tfName(networkName) + "_" + suffix
			}
		}
	}
	return tfName(subnetName)
}

func renderVMAWS(p VMPlan) string {
	var b strings.Builder
	subnetLabel := subnetResourceLabel(p.NetworkName, p.SubnetName)
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		fmt.Fprintf(&b, "resource \"aws_instance\" %q {\n", rn)
		fmt.Fprintf(&b, "  ami           = %q\n", p.Image)
		fmt.Fprintf(&b, "  instance_type = %q\n", p.InstanceType)
		if p.SubnetName != "" {
			fmt.Fprintf(&b, "  subnet_id     = aws_subnet.%s.id\n", subnetLabel)
		}
		if p.SecurityGroup != "" {
			fmt.Fprintf(&b, "  vpc_security_group_ids = [aws_security_group.%s.id]\n", tfName(p.SecurityGroup))
		}
		fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", inst.Name)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderVMGCP(p VMPlan) string {
	var b strings.Builder
	// GCP instances are zonal; derive a deterministic zone from the csp_region
	// (region + "-a"), matching the network component's zone derivation.
	zone := p.CSPRegion + "-a"
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		fmt.Fprintf(&b, "resource \"google_compute_instance\" %q {\n", rn)
		fmt.Fprintf(&b, "  name         = %q\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  machine_type = %q\n", p.InstanceType)
		fmt.Fprintf(&b, "  zone         = %q\n", zone)
		b.WriteString("  boot_disk {\n")
		b.WriteString("    initialize_params {\n")
		fmt.Fprintf(&b, "      image = %q\n", p.Image)
		b.WriteString("    }\n")
		b.WriteString("  }\n")
		b.WriteString("  network_interface {\n")
		if p.NetworkName != "" {
			fmt.Fprintf(&b, "    network    = google_compute_network.%s.id\n", tfName(p.NetworkName))
		}
		if p.SubnetName != "" {
			fmt.Fprintf(&b, "    subnetwork = google_compute_subnetwork.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetName))
		}
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderVMDO(p VMPlan) string {
	var b strings.Builder
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		fmt.Fprintf(&b, "resource \"digitalocean_droplet\" %q {\n", rn)
		fmt.Fprintf(&b, "  name   = %q\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  image  = %q\n", p.Image)
		fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  size   = %q\n", p.InstanceType)
		if p.NetworkName != "" {
			fmt.Fprintf(&b, "  vpc_uuid = digitalocean_vpc.%s.id\n", tfName(p.NetworkName))
		}
		fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
		b.WriteString("}\n\n")
	}
	// DO firewalls attach to droplets by droplet_ids; if a security-group is
	// declared, the firewall (rendered separately) references these droplets.
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RenderScaleGroupHCL renders a resolved ScaleGroupPlan into concrete
// cloud-provider Terraform HCL. Mirrors RenderVMHCL: translation returns a
// structured plan, rendering to .tf happens here and drives the per-provider
// round-trip tests (SPEC 6).
//
//   - AWS: aws_launch_template + aws_autoscaling_group across the region's
//     subnets (vpc_zone_identifier), health_check_type from the plan,
//     min/max/desired_capacity, and a rolling instance_refresh — the proven
//     production ASG pattern (multi-AZ, health-check-based, rolling refresh).
//   - GCP: google_compute_instance_template +
//     google_compute_region_instance_group_manager +
//     google_compute_region_autoscaler (min/max replicas, health check).
//
// DigitalOcean never reaches here: TranslateScaleGroup rejects it with
// ErrAutoscaleUnsupported (no native VM ASG primitive).
func RenderScaleGroupHCL(plan ScaleGroupPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderASGAWS(plan), nil
	case ProviderGCP:
		return renderASGGCP(plan), nil
	case ProviderDigitalOcean:
		return "", fmt.Errorf(
			"render: virtual-machine-scale-group is unsupported on digitalocean " +
				"(no native VM autoscaling primitive; use managed-kubernetes)")
	default:
		return "", fmt.Errorf("render: unsupported provider %q", plan.Provider)
	}
}

// awsHealthCheckType maps the canonical health kind to the AWS ASG
// health_check_type ("EC2" or "ELB").
func awsHealthCheckType(health string) string {
	if health == HealthELB {
		return "ELB"
	}
	return "EC2"
}

func renderASGAWS(p ScaleGroupPlan) string {
	ltName := tfName(p.GroupName) + "_lt"
	asgName := tfName(p.GroupName) + "_asg"
	var b strings.Builder

	// Launch template: instance type + image come from the catalog (reused VM SKU
	// resolution), security-group wired from the sibling component.
	fmt.Fprintf(&b, "resource \"aws_launch_template\" %q {\n", ltName)
	fmt.Fprintf(&b, "  name_prefix   = \"%s-\"\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  image_id      = %q\n", p.Image)
	fmt.Fprintf(&b, "  instance_type = %q\n", p.InstanceType)
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  vpc_security_group_ids = [aws_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	b.WriteString("  tag_specifications {\n")
	b.WriteString("    resource_type = \"instance\"\n")
	fmt.Fprintf(&b, "    tags = { Name = %q, pyxcloud = \"true\" }\n", p.GroupName)
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// Autoscaling group: multi-AZ across the region's subnets, min/max/desired,
	// health-check-based, with a rolling instance refresh.
	fmt.Fprintf(&b, "resource \"aws_autoscaling_group\" %q {\n", asgName)
	fmt.Fprintf(&b, "  name                = %q\n", p.GroupName)
	fmt.Fprintf(&b, "  min_size            = %d\n", p.Min)
	fmt.Fprintf(&b, "  max_size            = %d\n", p.Max)
	fmt.Fprintf(&b, "  desired_capacity    = %d\n", p.Desired)
	fmt.Fprintf(&b, "  health_check_type   = %q\n", awsHealthCheckType(p.Health))
	b.WriteString("  health_check_grace_period = 300\n")
	if len(p.SubnetNames) > 0 {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("aws_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  vpc_zone_identifier = [%s]\n", strings.Join(labels, ", "))
	}
	b.WriteString("  launch_template {\n")
	fmt.Fprintf(&b, "    id      = aws_launch_template.%s.id\n", ltName)
	b.WriteString("    version = \"$Latest\"\n")
	b.WriteString("  }\n")
	// Rolling instance refresh — the production ASG pattern.
	b.WriteString("  instance_refresh {\n")
	b.WriteString("    strategy = \"Rolling\"\n")
	b.WriteString("    preferences {\n")
	b.WriteString("      min_healthy_percentage = 90\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("  tag {\n")
	b.WriteString("    key                 = \"pyxcloud\"\n")
	b.WriteString("    value               = \"true\"\n")
	b.WriteString("    propagate_at_launch = true\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderASGGCP(p ScaleGroupPlan) string {
	tmplName := tfName(p.GroupName) + "_tmpl"
	mgrName := tfName(p.GroupName) + "_mig"
	asName := tfName(p.GroupName) + "_as"
	hcName := tfName(p.GroupName) + "_hc"
	var b strings.Builder

	// Instance template: machine type + image from the catalog.
	fmt.Fprintf(&b, "resource \"google_compute_instance_template\" %q {\n", tmplName)
	fmt.Fprintf(&b, "  name_prefix  = \"%s-\"\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  machine_type = %q\n", p.InstanceType)
	b.WriteString("  disk {\n")
	fmt.Fprintf(&b, "    source_image = %q\n", p.Image)
	b.WriteString("    auto_delete  = true\n")
	b.WriteString("    boot         = true\n")
	b.WriteString("  }\n")
	b.WriteString("  network_interface {\n")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "    network    = google_compute_network.%s.id\n", tfName(p.NetworkName))
	}
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "    subnetwork = google_compute_subnetwork.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("  lifecycle {\n")
	b.WriteString("    create_before_destroy = true\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// Health check (used for autohealing when health = elb / lb).
	fmt.Fprintf(&b, "resource \"google_compute_health_check\" %q {\n", hcName)
	fmt.Fprintf(&b, "  name = \"%s-hc\"\n", tfName(p.GroupName))
	b.WriteString("  tcp_health_check {\n")
	b.WriteString("    port = 80\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// Regional instance group manager: regional = multi-zone spread.
	fmt.Fprintf(&b, "resource \"google_compute_region_instance_group_manager\" %q {\n", mgrName)
	fmt.Fprintf(&b, "  name                      = %q\n", p.GroupName)
	fmt.Fprintf(&b, "  region                    = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  base_instance_name        = %q\n", tfName(p.GroupName))
	b.WriteString("  version {\n")
	fmt.Fprintf(&b, "    instance_template = google_compute_instance_template.%s.id\n", tmplName)
	b.WriteString("  }\n")
	if p.Health == HealthELB {
		b.WriteString("  auto_healing_policies {\n")
		fmt.Fprintf(&b, "    health_check      = google_compute_health_check.%s.id\n", hcName)
		b.WriteString("    initial_delay_sec = 300\n")
		b.WriteString("  }\n")
	}
	// Rolling update — the GCP analogue of the AWS instance refresh.
	b.WriteString("  update_policy {\n")
	b.WriteString("    type                  = \"PROACTIVE\"\n")
	b.WriteString("    minimal_action        = \"REPLACE\"\n")
	b.WriteString("    max_surge_fixed       = 3\n")
	b.WriteString("    max_unavailable_fixed = 0\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// Regional autoscaler: min/max replicas.
	fmt.Fprintf(&b, "resource \"google_compute_region_autoscaler\" %q {\n", asName)
	fmt.Fprintf(&b, "  name   = \"%s-as\"\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  target = google_compute_region_instance_group_manager.%s.id\n", mgrName)
	b.WriteString("  autoscaling_policy {\n")
	fmt.Fprintf(&b, "    min_replicas = %d\n", p.Min)
	fmt.Fprintf(&b, "    max_replicas = %d\n", p.Max)
	b.WriteString("    cpu_utilization {\n")
	b.WriteString("      target = 0.6\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
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
