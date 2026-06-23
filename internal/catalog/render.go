package catalog

import (
	"fmt"
	"sort"
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
	case ProviderAzure:
		return renderNetworkAzure(plan), nil
	case ProviderLinode:
		return renderLinodeNetwork(plan), nil
	case ProviderUbicloud:
		return renderNetworkUbicloud(plan), nil
	case ProviderOracle:
		return renderOCI(plan), nil
	case ProviderIBM:
		return renderNetworkIBM(plan), nil
	case ProviderAlibaba:
		return renderAlibaba(plan), nil
	case ProviderOVH:
		return renderNetworkOVH(plan)
	case ProviderStackIt:
		return renderStackItNetwork(plan), nil
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
	b.WriteString("data \"aws_vpc\" \"default\" { default = true }\n")
	b.WriteString("data \"aws_subnets\" \"default\" {\n  filter {\n    name   = \"vpc-id\"\n    values = [data.aws_vpc.default.id]\n  }\n}\n")
	for i, _ := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\ndata \"aws_subnet\" %q {\n", sn)
		fmt.Fprintf(&b, "  id = tolist(data.aws_subnets.default.ids)[%d]\n", i)
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
	case ProviderAzure:
		return renderSGAzure(plan), nil
	case ProviderLinode:
		return renderSGLinode(plan), nil
	case ProviderUbicloud:
		return renderSGUbicloud(plan), nil
	case ProviderOracle:
		return renderSGOCI(plan), nil
	case ProviderIBM:
		return renderSGIBM(plan), nil
	case ProviderAlibaba:
		return renderSGAlibaba(plan), nil
	case ProviderStackIt:
		return renderStackItSG(plan), nil
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
	fmt.Fprintf(&b, "  vpc_id      = data.aws_vpc.default.id\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.SGName)
	b.WriteString("}\n")

	for i, r := range p.Rules {
		if r.ExternalSourceSGID != "" {
			// Scope to an external, out-of-plan SG by its concrete id (e.g. a shared
			// ALB SG from remote-state). Rendered as a literal, not a resource ref.
			rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
			writeAWSSecurityGroupRule(&b, rn, name, r, fmt.Sprintf("%q", r.ExternalSourceSGID), "source_security_group_id")
			continue
		}
		if r.SourceSG != "" {
			rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
			writeAWSSecurityGroupRule(&b, rn, name, r, fmt.Sprintf("aws_security_group.%s.id", tfName(r.SourceSG)), "source_security_group_id")
			continue
		}
		if r.SourcePrefixList != "" {
			// Reference the managed prefix list emitted by the prefix-list component.
			// AWS rules carry prefix_list_ids as a list of managed-prefix-list ids.
			rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
			writeAWSSecurityGroupRule(&b, rn, name, r,
				fmt.Sprintf("[aws_ec2_managed_prefix_list.%s.id]", tfName(r.SourcePrefixList)), "prefix_list_ids")
			continue
		}
		v4, v6 := splitCIDRs(r.CIDRs)
		for j, cidr := range v4 {
			rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
			if j > 0 || len(v6) > 0 {
				rn = fmt.Sprintf("%s_%s_%d_ipv4_%d", name, r.Direction, i, j)
			}
			writeAWSSecurityGroupRule(&b, rn, name, r, hclCIDRList([]string{cidr}), "cidr_blocks")
		}
		for j, cidr := range v6 {
			rn := fmt.Sprintf("%s_%s_%d_ipv6_%d", name, r.Direction, i, j)
			writeAWSSecurityGroupRule(&b, rn, name, r, hclCIDRList([]string{cidr}), "ipv6_cidr_blocks")
		}
	}
	return b.String()
}

func writeAWSSecurityGroupRule(b *strings.Builder, resourceName, sgName string, r RulePlan, value, attr string) {
	fmt.Fprintf(b, "\nresource \"aws_security_group_rule\" %q {\n", resourceName)
	fmt.Fprintf(b, "  type              = %q\n", r.Direction)
	fmt.Fprintf(b, "  security_group_id = aws_security_group.%s.id\n", sgName)
	fmt.Fprintf(b, "  protocol          = %q\n", awsProto(r.Protocol))
	fmt.Fprintf(b, "  from_port         = %d\n", r.FromPort)
	fmt.Fprintf(b, "  to_port           = %d\n", r.ToPort)
	fmt.Fprintf(b, "  %-17s = %s\n", attr, value)
	b.WriteString("}\n")
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
		tagKey := "source_tags"
		if r.Direction == DirEgress {
			blockName = "outbound_rule"
			cidrKey = "destination_addresses"
			tagKey = "destination_tags"
		}
		fmt.Fprintf(&b, "\n  %s {\n", blockName)
		fmt.Fprintf(&b, "    protocol   = %q\n", doProto(r.Protocol))
		if r.Protocol == ProtoTCP || r.Protocol == ProtoUDP {
			fmt.Fprintf(&b, "    port_range = %q\n", portRangeString(r.FromPort, r.ToPort))
		}
		switch {
		case r.SourceSG != "":
			// DigitalOcean firewalls have no SG-references-SG primitive: a peer
			// security-group is migrated to a DO TAG. The referenced SG's droplets
			// carry that tag, so source_tags/destination_tags reproduce the AWS
			// "allow from this security group" semantics.
			fmt.Fprintf(&b, "    %s = [%q]\n", tagKey, tfName(r.SourceSG))
		case r.SourcePrefixList != "":
			// DO has no managed-prefix-list primitive: inline the resolved CIDRs the
			// prefix-list expands to (translate populated ResolvedPrefixCIDRs).
			fmt.Fprintf(&b, "    %s = %s\n", cidrKey, hclCIDRList(r.ResolvedPrefixCIDRs))
		case r.CIDRs != nil:
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
	case ProviderAzure:
		return renderVMAzure(plan), nil
	case ProviderLinode:
		return renderVMLinode(plan), nil
	case ProviderUbicloud:
		return renderVMUbicloud(plan), nil
	case ProviderOracle:
		return renderVMOCI(plan), nil
	case ProviderIBM:
		return renderVMIBM(plan), nil
	case ProviderAlibaba:
		return renderVMAlibaba(plan), nil
	case ProviderStackIt:
		return renderStackItVM(plan), nil
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
			fmt.Fprintf(&b, "  subnet_id     = data.aws_subnet.%s.id\n", subnetLabel)
		}
		if p.SecurityGroup != "" {
			fmt.Fprintf(&b, "  vpc_security_group_ids = [aws_security_group.%s.id]\n", tfName(p.SecurityGroup))
		}
		if p.InstanceProfile != "" {
			if p.InstanceProfileManaged {
				fmt.Fprintf(&b, "  iam_instance_profile = aws_iam_instance_profile.%s.name\n", tfName(p.InstanceProfile))
			} else {
				fmt.Fprintf(&b, "  iam_instance_profile = %q\n", p.InstanceProfile)
			}
		}
		if p.UserData != "" {
			fmt.Fprintf(&b, "  user_data = base64encode(%s)\n", vmHeredoc(p.UserData))
		}
		fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", inst.Name)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// vmHeredoc renders s as an HCL indented heredoc for VM user_data (no escaping).
func vmHeredoc(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return "<<-PYXUSERDATA\n" + s + "PYXUSERDATA\n  "
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
		if p.UserData != "" {
			fmt.Fprintf(&b, "  user_data = %s\n", vmHeredoc(p.UserData))
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
//   - DigitalOcean: digitalocean_kubernetes_cluster + an auto-scaling node_pool
//     (DO has no native VM ASG primitive, so the scale-group maps to a DOKS node
//     pool — the canonical DO autoscaling answer; min_nodes>=1 self-heal).
//
// Linode and StackIt still never reach here for a scale-group: TranslateScaleGroup
// rejects them with ErrAutoscaleUnsupported (no native VM ASG primitive and no
// node-pool mapping wired).
func RenderScaleGroupHCL(plan ScaleGroupPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderASGAWS(plan), nil
	case ProviderGCP:
		return renderASGGCP(plan), nil
	case ProviderAzure:
		return renderScaleGroupAzure(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"virtual-machine-scale-group",
			"Ubicloud has no managed VM autoscaling primitive in Terraform; place this tier on a "+
				"provider with an autoscaling group (aws / gcp), or model fixed-count virtual-machine "+
				"resources on ubicloud instead.")
	case ProviderOracle:
		return renderASGOCI(plan), nil
	case ProviderIBM:
		return renderASGIBM(plan), nil
	case ProviderAlibaba:
		return renderASGAlibaba(plan), nil
	case ProviderDigitalOcean:
		// DO has no native VM ASG primitive; the scale-group is mapped to a DOKS
		// cluster with an auto-scaling node pool (the canonical DO autoscaling
		// answer). Self-heal: min_nodes >= 1 (DOKS keeps >=1 healthy node and
		// replaces failed ones — the ASG-of-1 pattern).
		return renderScaleGroupDO(plan), nil
	case ProviderLinode:
		return "", fmt.Errorf(
			"render: virtual-machine-scale-group is unsupported on linode " +
				"(no native VM autoscaling primitive; use managed-kubernetes / LKE node-pool autoscaling)")
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
	if p.InstanceProfile != "" {
		b.WriteString("  iam_instance_profile {\n")
		if p.InstanceProfileManaged {
			fmt.Fprintf(&b, "    name = aws_iam_instance_profile.%s.name\n", tfName(p.InstanceProfile))
		} else {
			fmt.Fprintf(&b, "    name = %q\n", p.InstanceProfile)
		}
		b.WriteString("  }\n")
	}
	if p.RootDiskGB > 0 {
		b.WriteString("  block_device_mappings {\n")
		b.WriteString("    device_name = \"/dev/sda1\"\n")
		b.WriteString("    ebs {\n")
		fmt.Fprintf(&b, "      volume_size           = %d\n", p.RootDiskGB)
		b.WriteString("      volume_type           = \"gp3\"\n")
		b.WriteString("      delete_on_termination = true\n")
		b.WriteString("    }\n")
		b.WriteString("  }\n")
	}
	if p.UserData != "" {
		fmt.Fprintf(&b, "  user_data = base64encode(%s)\n", vmHeredoc(p.UserData))
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
			labels = append(labels, fmt.Sprintf("data.aws_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
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

// renderScaleGroupDO maps an abstract scale-group onto DigitalOcean. DO has no
// native VM autoscaling primitive, so the group renders to a
// digitalocean_kubernetes_cluster with an auto-scaling node_pool (DOKS) — the
// canonical DO autoscaling answer the old ErrAutoscaleUnsupported pointed to.
//
// Self-heal semantics are preserved: auto_scale=true with min_nodes>=1 keeps at
// least one healthy node and lets DOKS replace failed ones — the production
// ASG-of-1 pattern. The node SIZE is the catalog-resolved droplet SKU (the same
// virtual_machine SKU resolution the VM/ASG components use), and the pool joins
// the place's VPC (vpc_uuid). DO clusters are region-scoped (no sub-zones), which
// is why ScaleGroupPlan.Zones is empty for DO.
func renderScaleGroupDO(p ScaleGroupPlan) string {
	name := tfName(p.GroupName)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"digitalocean_kubernetes_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name    = %q\n", name)
	fmt.Fprintf(&b, "  region  = %q\n", p.CSPRegion)
	ver := p.KubernetesVersion
	if ver == "" {
		ver = "latest"
	}
	fmt.Fprintf(&b, "  version = %q\n", ver)
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_uuid = digitalocean_vpc.%s.id\n", tfName(p.NetworkName))
	}
	// Auto-scaling node pool: the scale-group's compute. auto_scale + min/max map
	// the abstract bounds; desired seeds node_count within [min, max]. min>=1 is
	// the self-healing floor (enforced in TranslateScaleGroup for DO).
	b.WriteString("  node_pool {\n")
	fmt.Fprintf(&b, "    name       = \"%s-pool\"\n", name)
	fmt.Fprintf(&b, "    size       = %q\n", p.InstanceType)
	b.WriteString("    auto_scale = true\n")
	fmt.Fprintf(&b, "    min_nodes  = %d\n", p.Min)
	fmt.Fprintf(&b, "    max_nodes  = %d\n", p.Max)
	fmt.Fprintf(&b, "    node_count = %d\n", p.Desired)
	b.WriteString("    tags = [\"pyxcloud\"]\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
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

// RenderLoadBalancerHCL renders a resolved LoadBalancerPlan into concrete
// cloud-provider Terraform HCL. Mirrors RenderScaleGroupHCL: translation returns
// a structured plan, rendering to .tf happens here and drives the per-provider
// `terraform plan` / real apply round-trip tests (SPEC §6).
//
//   - AWS: aws_lb (application LB, internet-facing, multi-subnet) +
//     aws_lb_target_group + aws_lb_listener per listener (+ lb_cookie stickiness
//     when requested). A scale-group target is wired by attaching the target
//     group ARN to the ASG (target_group_arns); a fixed-VM target is wired with
//     aws_lb_target_group_attachment per instance. ALB listener rules respect the
//     <= 5 condition-value quota (enforced at translate); descriptions are ASCII.
//   - GCP: google_compute_health_check (regional) + google_compute_region_backend_service
//   - google_compute_forwarding_rule per listener. The MIG (scale-group) is the
//     backend (backend { group = <MIG instance_group> }).
//   - DigitalOcean: digitalocean_loadbalancer with forwarding_rule per listener,
//     a healthcheck, and sticky_sessions when requested, targeting droplets by tag.
func RenderLoadBalancerHCL(plan LoadBalancerPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderLBAWS(plan), nil
	case ProviderGCP:
		return renderLBGCP(plan), nil
	case ProviderDigitalOcean:
		return renderLBDO(plan), nil
	case ProviderAzure:
		return renderLBAzure(plan), nil
	case ProviderLinode:
		return renderLBLinode(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"load-balancer",
			"The Ubicloud Terraform provider exposes no load-balancer resource; front this tier with a "+
				"load-balancer on aws / gcp / digitalocean, or terminate traffic at the application.")
	case ProviderOracle:
		return renderLBOCI(plan), nil
	case ProviderIBM:
		return renderLBIBM(plan), nil
	case ProviderAlibaba:
		return renderLBAlibaba(plan), nil
	case ProviderStackIt:
		return renderStackItLoadBalancer(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q", plan.Provider)
	}
}

// lbAWSProto maps a canonical LB protocol to an AWS ALB listener protocol token.
func lbAWSProto(proto string) string {
	switch proto {
	case LBProtoHTTPS:
		return "HTTPS"
	case LBProtoTCP:
		return "TCP"
	default:
		return "HTTP"
	}
}

// lbAWSTargetGroupProto maps the listener protocol to an aws_lb_target_group
// protocol. ALB target groups speak HTTP/HTTPS; a TCP listener fronts an HTTP
// target group (the instances serve HTTP behind the LB) — the standard ALB shape.
func lbAWSTargetGroupProto(proto string) string {
	if proto == LBProtoHTTPS {
		return "HTTPS"
	}
	return "HTTP"
}

// asgResourceLabel returns the Terraform resource LABEL the scale-group renderer
// emits for the autoscaling group ("<tfName(group)>_asg"), so the LB can wire the
// target group ARN onto the ASG (target_group_arns).
func asgResourceLabel(groupName string) string {
	return tfName(groupName) + "_asg"
}

func renderLBAWS(p LoadBalancerPlan) string {
	lbName := tfName(p.LBName) + "_lb"
	tgName := tfName(p.LBName) + "_tg"
	var b strings.Builder

	// Internet egress wiring for an internet-facing ALB. The network component
	// (pd-TF-REGION-VPC) renders only the VPC + subnets; an internet-facing load
	// balancer additionally needs an internet gateway and a public route, so the
	// LB component owns and emits that wiring (analogous to how the SG component
	// owns its rules). One IGW + one public route table + a route to 0.0.0.0/0,
	// associated with each subnet the LB occupies.
	if p.NetworkName != "" {
		igwName := tfName(p.LBName) + "_igw"
		rtName := tfName(p.LBName) + "_rt"
		fmt.Fprintf(&b, "resource \"aws_internet_gateway\" %q {\n", igwName)
		fmt.Fprintf(&b, "  vpc_id = data.aws_vpc.default.id\n")
		fmt.Fprintf(&b, "  tags = { Name = \"%s-igw\", pyxcloud = \"true\" }\n", tfName(p.LBName))
		b.WriteString("}\n\n")

		fmt.Fprintf(&b, "resource \"aws_route_table\" %q {\n", rtName)
		fmt.Fprintf(&b, "  vpc_id = data.aws_vpc.default.id\n")
		b.WriteString("  route {\n")
		b.WriteString("    cidr_block = \"0.0.0.0/0\"\n")
		fmt.Fprintf(&b, "    gateway_id = aws_internet_gateway.%s.id\n", igwName)
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  tags = { Name = \"%s-rt\", pyxcloud = \"true\" }\n", tfName(p.LBName))
		b.WriteString("}\n\n")

		for i, s := range p.SubnetNames {
			assocName := fmt.Sprintf("%s_rta_%d", tfName(p.LBName), i+1)
			fmt.Fprintf(&b, "resource \"aws_route_table_association\" %q {\n", assocName)
			fmt.Fprintf(&b, "  subnet_id      = data.aws_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, s))
			fmt.Fprintf(&b, "  route_table_id = aws_route_table.%s.id\n", rtName)
			b.WriteString("}\n\n")
		}
	}

	// Application load balancer: internet-facing, multi-subnet (multi-AZ from the
	// region), security-group attached.
	fmt.Fprintf(&b, "resource \"aws_lb\" %q {\n", lbName)
	fmt.Fprintf(&b, "  name               = %q\n", tfName(p.LBName))
	b.WriteString("  internal           = false\n")
	b.WriteString("  load_balancer_type = \"application\"\n")
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  security_groups    = [aws_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	if len(p.SubnetNames) > 0 {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("data.aws_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  subnets            = [%s]\n", strings.Join(labels, ", "))
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.LBName)
	if p.NetworkName != "" {
		// The ALB must not be created before its internet gateway is attached
		// (CreateLoadBalancer fails with "VPC has no internet gateway" otherwise);
		// the ALB does not reference the IGW directly, so order it explicitly.
		fmt.Fprintf(&b, "  depends_on = [aws_internet_gateway.%s]\n", tfName(p.LBName)+"_igw")
	}
	b.WriteString("}\n\n")

	// Target group: the targets the listeners forward to. instance target type so
	// the ASG / fixed instances register here.
	hc := p.HealthCheck
	fmt.Fprintf(&b, "resource \"aws_lb_target_group\" %q {\n", tgName)
	fmt.Fprintf(&b, "  name        = \"%s-tg\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  port        = %d\n", hc.Port)
	fmt.Fprintf(&b, "  protocol    = %q\n", lbAWSTargetGroupProto(hc.Protocol))
	b.WriteString("  target_type = \"instance\"\n")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_id      = data.aws_vpc.default.id\n")
	}
	b.WriteString("  health_check {\n")
	fmt.Fprintf(&b, "    protocol            = %q\n", lbAWSProto(hc.Protocol))
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		fmt.Fprintf(&b, "    path                = %q\n", hc.Path)
	}
	fmt.Fprintf(&b, "    interval            = %d\n", hc.IntervalSeconds)
	fmt.Fprintf(&b, "    healthy_threshold   = %d\n", hc.HealthyThreshold)
	fmt.Fprintf(&b, "    unhealthy_threshold = %d\n", hc.UnhealthyThreshold)
	b.WriteString("  }\n")
	if p.Stickiness {
		b.WriteString("  stickiness {\n")
		b.WriteString("    type            = \"lb_cookie\"\n")
		b.WriteString("    cookie_duration = 86400\n")
		b.WriteString("    enabled         = true\n")
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.LBName)
	b.WriteString("}\n\n")

	// One listener per declared listener port. The default action forwards to the
	// target group; explicit layer-7 routing rules (path/host/priority/admin-VPN
	// gate) render as aws_lb_listener_rule resources attached to the listener.
	for _, l := range p.Listeners {
		ln := fmt.Sprintf("%s_listener_%d", tfName(p.LBName), l.Port)
		fmt.Fprintf(&b, "resource \"aws_lb_listener\" %q {\n", ln)
		fmt.Fprintf(&b, "  load_balancer_arn = aws_lb.%s.arn\n", lbName)
		fmt.Fprintf(&b, "  port              = %d\n", l.Port)
		fmt.Fprintf(&b, "  protocol          = %q\n", lbAWSProto(l.Protocol))
		b.WriteString("  default_action {\n")
		b.WriteString("    type             = \"forward\"\n")
		fmt.Fprintf(&b, "    target_group_arn = aws_lb_target_group.%s.arn\n", tgName)
		b.WriteString("  }\n")
		b.WriteString("}\n\n")

		// Layer-7 routing rules (pd-MIG-LB-L7-ROUTING). Each renders an
		// aws_lb_listener_rule with the resolved priority, the host/path conditions,
		// and the admin-VPN source_ip gate when CIDRs are present. Rules are
		// pre-sorted by ascending priority at translate time.
		renderLBAWSListenerRules(&b, p, l, ln, tgName)
	}

	// Target wiring: a scale-group target gets the target-group ARN attached to
	// the ASG (target_group_arns); a fixed-VM target gets one attachment per
	// instance. The sibling component renders the ASG / instances; here we emit
	// only the wiring resource.
	switch p.TargetKind {
	case LBTargetScaleGroup:
		if p.TargetName != "" {
			attachName := fmt.Sprintf("%s_attach", tfName(p.LBName))
			fmt.Fprintf(&b, "resource \"aws_autoscaling_attachment\" %q {\n", attachName)
			fmt.Fprintf(&b, "  autoscaling_group_name = aws_autoscaling_group.%s.name\n", asgResourceLabel(p.TargetName))
			fmt.Fprintf(&b, "  lb_target_group_arn    = aws_lb_target_group.%s.arn\n", tgName)
			b.WriteString("}\n\n")
		}
	case LBTargetVM:
		if p.TargetName != "" {
			attachName := fmt.Sprintf("%s_attach", tfName(p.LBName))
			fmt.Fprintf(&b, "resource \"aws_lb_target_group_attachment\" %q {\n", attachName)
			fmt.Fprintf(&b, "  target_group_arn = aws_lb_target_group.%s.arn\n", tgName)
			fmt.Fprintf(&b, "  target_id        = aws_instance.%s.id\n", tfName(p.TargetName+"-1"))
			fmt.Fprintf(&b, "  port             = %d\n", hc.Port)
			b.WriteString("}\n\n")
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderLBAWSListenerRules emits one aws_lb_listener_rule per resolved layer-7
// routing rule on a listener. A rule carries an explicit priority, host_header
// and/or path_pattern conditions, and — when AdminVPNCIDRs is set — a source_ip
// condition implementing the admin/VPN gate (only those CIDRs may match the rule).
// A rule may forward to its own target group (TargetName) or, by default, to the
// LB's default target group. The resolved rules are pre-sorted by priority.
func renderLBAWSListenerRules(b *strings.Builder, p LoadBalancerPlan, l LBListenerPlan, listenerLabel, defaultTG string) {
	for _, r := range l.Rules {
		ruleName := fmt.Sprintf("%s_listener_%d_rule_%d", tfName(p.LBName), l.Port, r.Priority)
		// A per-rule target group is referenced by convention "<target>_tg"; when no
		// override is given the rule forwards to the LB's default target group.
		tg := defaultTG
		if r.TargetName != "" {
			tg = tfName(r.TargetName) + "_tg"
		}
		fmt.Fprintf(b, "resource \"aws_lb_listener_rule\" %q {\n", ruleName)
		fmt.Fprintf(b, "  listener_arn = aws_lb_listener.%s.arn\n", listenerLabel)
		fmt.Fprintf(b, "  priority     = %d\n", r.Priority)
		b.WriteString("  action {\n")
		b.WriteString("    type             = \"forward\"\n")
		fmt.Fprintf(b, "    target_group_arn = aws_lb_target_group.%s.arn\n", tg)
		b.WriteString("  }\n")
		if len(r.HostHeaders) > 0 {
			b.WriteString("  condition {\n    host_header {\n")
			fmt.Fprintf(b, "      values = [%s]\n", quoteList(r.HostHeaders))
			b.WriteString("    }\n  }\n")
		}
		if len(r.PathPatterns) > 0 {
			b.WriteString("  condition {\n    path_pattern {\n")
			fmt.Fprintf(b, "      values = [%s]\n", quoteList(r.PathPatterns))
			b.WriteString("    }\n  }\n")
		}
		// Admin-VPN gate: a source_ip condition restricts the rule to the admin/VPN
		// allow-list CIDRs — the same semantics the AWS topology enforces today.
		if len(r.AdminVPNCIDRs) > 0 {
			b.WriteString("  condition {\n    source_ip {\n")
			fmt.Fprintf(b, "      values = [%s]\n", quoteList(r.AdminVPNCIDRs))
			b.WriteString("    }\n  }\n")
		}
		b.WriteString("}\n\n")
	}
}

// lbGCPProto maps a canonical LB protocol to the GCP forwarding-rule /
// backend-service protocol token.
func lbGCPProto(proto string) string {
	switch proto {
	case LBProtoHTTPS:
		return "HTTPS"
	case LBProtoTCP:
		return "TCP"
	default:
		return "HTTP"
	}
}

func renderLBGCP(p LoadBalancerPlan) string {
	hcName := tfName(p.LBName) + "_hc"
	beName := tfName(p.LBName) + "_be"
	var b strings.Builder
	hc := p.HealthCheck

	// Regional health check.
	fmt.Fprintf(&b, "resource \"google_compute_health_check\" %q {\n", hcName)
	fmt.Fprintf(&b, "  name = \"%s-hc\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  check_interval_sec  = %d\n", hc.IntervalSeconds)
	fmt.Fprintf(&b, "  healthy_threshold   = %d\n", hc.HealthyThreshold)
	fmt.Fprintf(&b, "  unhealthy_threshold = %d\n", hc.UnhealthyThreshold)
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		b.WriteString("  http_health_check {\n")
		fmt.Fprintf(&b, "    port         = %d\n", hc.Port)
		fmt.Fprintf(&b, "    request_path = %q\n", hc.Path)
		b.WriteString("  }\n")
	} else {
		b.WriteString("  tcp_health_check {\n")
		fmt.Fprintf(&b, "    port = %d\n", hc.Port)
		b.WriteString("  }\n")
	}
	b.WriteString("}\n\n")

	// Regional backend service: the MIG (scale-group) is the backend. Session
	// affinity = generated cookie when stickiness is requested.
	fmt.Fprintf(&b, "resource \"google_compute_region_backend_service\" %q {\n", beName)
	fmt.Fprintf(&b, "  name                  = \"%s-be\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  region                = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  protocol              = %q\n", lbGCPProto(p.Listeners[0].Protocol))
	b.WriteString("  load_balancing_scheme = \"EXTERNAL\"\n")
	fmt.Fprintf(&b, "  health_checks         = [google_compute_health_check.%s.id]\n", hcName)
	if p.Stickiness {
		b.WriteString("  session_affinity      = \"GENERATED_COOKIE\"\n")
	}
	if p.TargetKind == LBTargetScaleGroup && p.TargetName != "" {
		b.WriteString("  backend {\n")
		fmt.Fprintf(&b, "    group = google_compute_region_instance_group_manager.%s.instance_group\n", tfName(p.TargetName)+"_mig")
		b.WriteString("    balancing_mode = \"UTILIZATION\"\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n\n")

	// One forwarding rule per listener port, fronting the backend service.
	for _, l := range p.Listeners {
		fn := fmt.Sprintf("%s_fr_%d", tfName(p.LBName), l.Port)
		fmt.Fprintf(&b, "resource \"google_compute_forwarding_rule\" %q {\n", fn)
		fmt.Fprintf(&b, "  name                  = \"%s-fr-%d\"\n", tfName(p.LBName), l.Port)
		fmt.Fprintf(&b, "  region                = %q\n", p.CSPRegion)
		b.WriteString("  load_balancing_scheme = \"EXTERNAL\"\n")
		fmt.Fprintf(&b, "  port_range            = %q\n", fmt.Sprintf("%d", l.Port))
		fmt.Fprintf(&b, "  backend_service       = google_compute_region_backend_service.%s.id\n", beName)
		b.WriteString("}\n\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// lbDOProto maps a canonical LB protocol to a DO loadbalancer forwarding-rule
// entry protocol (http / https / tcp).
func lbDOProto(proto string) string {
	switch proto {
	case LBProtoHTTPS:
		return "https"
	case LBProtoTCP:
		return "tcp"
	default:
		return "http"
	}
}

func renderLBDO(p LoadBalancerPlan) string {
	name := tfName(p.LBName)
	var b strings.Builder
	hc := p.HealthCheck

	fmt.Fprintf(&b, "resource \"digitalocean_loadbalancer\" %q {\n", name)
	fmt.Fprintf(&b, "  name   = %q\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_uuid = digitalocean_vpc.%s.id\n", tfName(p.NetworkName))
	}

	// One forwarding rule per listener. The LB terminates entry_protocol on
	// entry_port and forwards to the same target_protocol/port on the droplets.
	for _, l := range p.Listeners {
		b.WriteString("\n  forwarding_rule {\n")
		fmt.Fprintf(&b, "    entry_protocol  = %q\n", lbDOProto(l.Protocol))
		fmt.Fprintf(&b, "    entry_port      = %d\n", l.Port)
		fmt.Fprintf(&b, "    target_protocol = %q\n", lbDOProto(l.Protocol))
		fmt.Fprintf(&b, "    target_port     = %d\n", l.Port)
		b.WriteString("  }\n")
	}

	// Health check against the droplets.
	b.WriteString("\n  healthcheck {\n")
	fmt.Fprintf(&b, "    protocol                 = %q\n", lbDOProto(hc.Protocol))
	fmt.Fprintf(&b, "    port                     = %d\n", hc.Port)
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		fmt.Fprintf(&b, "    path                     = %q\n", hc.Path)
	}
	fmt.Fprintf(&b, "    check_interval_seconds   = %d\n", hc.IntervalSeconds)
	fmt.Fprintf(&b, "    healthy_threshold        = %d\n", hc.HealthyThreshold)
	fmt.Fprintf(&b, "    unhealthy_threshold      = %d\n", hc.UnhealthyThreshold)
	b.WriteString("  }\n")

	if p.Stickiness {
		b.WriteString("\n  sticky_sessions {\n")
		b.WriteString("    type               = \"cookies\"\n")
		b.WriteString("    cookie_name        = \"pyxcloud-lb\"\n")
		b.WriteString("    cookie_ttl_seconds = 86400\n")
		b.WriteString("  }\n")
	}

	// DO has no native VM autoscaling primitive, so a scale-group target on DO is
	// fronted by a droplet tag (the fixed/managed droplets carry the "pyxcloud"
	// tag, the same tag the virtual-machine renderer applies). A vm target uses
	// the same tag selection.
	if p.TargetName != "" {
		b.WriteString("\n  droplet_tag = \"pyxcloud\"\n")
	}

	b.WriteString("}\n")

	// Layer-7 routing rules (pd-MIG-LB-L7-ROUTING). A digitalocean_loadbalancer
	// forwarding_rule is pure port-to-port mapping: it has NO host/path matching
	// and NO per-rule source-IP gate, so it cannot express ALB listener-rule
	// parity. The canonical, plan-time-expressible DO equivalent is a DOKS Ingress
	// (the same kubernetes_manifest convention the cert-manager / scheduled-trigger
	// paths use): host + path rules map to ingress rules, and the admin-VPN gate is
	// preserved as a documented constraint via the ingress-nginx whitelist-source-range
	// annotation (an in-cluster source-IP allow-list, the DO analogue of the ALB
	// source_ip condition). This is appended only when L7 rules are declared.
	if hasLBRoutingRules(p.Listeners) {
		b.WriteString("\n")
		b.WriteString(renderLBDOIngress(p))
	}
	return b.String()
}

// hasLBRoutingRules reports whether any listener carries layer-7 routing rules.
func hasLBRoutingRules(listeners []LBListenerPlan) bool {
	for _, l := range listeners {
		if len(l.Rules) > 0 {
			return true
		}
	}
	return false
}

// renderLBDOIngress renders the DOKS Ingress that carries the layer-7 routing
// rules a DO load-balancer forwarding_rule cannot express (host/path match +
// admin-VPN source-IP gate). Host/path rules become ingress rules; the union of
// all admin-VPN CIDRs across the rules becomes the ingress-nginx
// whitelist-source-range annotation (a DOCUMENTED constraint: DO enforces the
// admin/VPN gate at the in-cluster ingress, not on the managed LB).
func renderLBDOIngress(p LoadBalancerPlan) string {
	name := tfName(p.LBName) + "_ingress"
	var b strings.Builder

	// Collect the deduplicated, deterministic admin-VPN CIDR union for the gate.
	seen := map[string]bool{}
	var vpnCIDRs []string
	for _, l := range p.Listeners {
		for _, r := range l.Rules {
			for _, c := range r.AdminVPNCIDRs {
				if c != "" && !seen[c] {
					seen[c] = true
					vpnCIDRs = append(vpnCIDRs, c)
				}
			}
		}
	}
	sort.Strings(vpnCIDRs)

	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", name)
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"networking.k8s.io/v1\"\n")
	b.WriteString("    kind       = \"Ingress\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", tfName(p.LBName))
	b.WriteString("      namespace = \"default\"\n")
	b.WriteString("      annotations = {\n")
	b.WriteString("        \"kubernetes.io/ingress.class\" = \"nginx\"\n")
	if len(vpnCIDRs) > 0 {
		// Admin-VPN gate: the ingress-nginx source-range whitelist preserves the ALB
		// source_ip admin/VPN allow-list semantics in-cluster (documented constraint).
		fmt.Fprintf(&b, "        \"nginx.ingress.kubernetes.io/whitelist-source-range\" = %q\n", strings.Join(vpnCIDRs, ","))
	}
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      rules = [\n")
	for _, l := range p.Listeners {
		for _, r := range l.Rules {
			// One ingress rule per host (or a host-less rule). Each path pattern
			// becomes an HTTP path on that host, backed by the rule's service.
			svc := tfName(p.LBName) + "-svc"
			if r.TargetName != "" {
				svc = tfName(r.TargetName) + "-svc"
			}
			paths := r.PathPatterns
			if len(paths) == 0 {
				paths = []string{"/"}
			}
			hosts := r.HostHeaders
			if len(hosts) == 0 {
				hosts = []string{""}
			}
			for _, h := range hosts {
				b.WriteString("        {\n")
				if h != "" {
					fmt.Fprintf(&b, "          host = %q\n", h)
				}
				b.WriteString("          http = {\n            paths = [\n")
				for _, pat := range paths {
					b.WriteString("              {\n")
					fmt.Fprintf(&b, "                path     = %q\n", ingressPath(pat))
					b.WriteString("                pathType = \"Prefix\"\n")
					b.WriteString("                backend = {\n                  service = {\n")
					fmt.Fprintf(&b, "                    name = %q\n", svc)
					fmt.Fprintf(&b, "                    port = { number = %d }\n", l.Port)
					b.WriteString("                  }\n                }\n")
					b.WriteString("              },\n")
				}
				b.WriteString("            ]\n          }\n")
				b.WriteString("        },\n")
			}
		}
	}
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// ingressPath maps an ALB-style path pattern ("/admin/*") to a Kubernetes Ingress
// Prefix path ("/admin"), trimming the trailing glob the ingress matches by prefix.
func ingressPath(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	pattern = strings.TrimSuffix(pattern, "*")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return "/"
	}
	return pattern
}

// RenderManagedDatabaseHCL renders a resolved ManagedDatabasePlan into concrete
// cloud-provider Terraform HCL. Mirrors RenderLoadBalancerHCL: translation returns
// a structured plan, rendering to .tf happens here and drives the per-provider
// `terraform plan` / real apply round-trip tests (SPEC §6).
//
//   - AWS: aws_db_subnet_group (multi-AZ across the region's subnets) +
//     aws_db_instance (RDS). storage_encrypted, multi_az (HA),
//     deletion_protection, and a final snapshot (skip_final_snapshot=false +
//     final_snapshot_identifier) — the production-safe defaults. The instance
//     class comes from the catalog.
//   - GCP: google_sql_database_instance with settings { tier, disk_size,
//     availability_type REGIONAL when HA }, disk encryption, and
//     deletion_protection.
//   - DigitalOcean: digitalocean_database_cluster (size from the catalog, node
//     count 2 when HA, region + private VPC). DO clusters are encrypted at rest
//     by default and have no in-place deletion-protection flag, so that intent is
//     carried as a lifecycle prevent_destroy when deletion_protection is on.
//
// The replacement-forcing data-safety guard runs at PLAN time (ModifyPlan), not
// here; this renderer always emits the production-safe shape.
func RenderManagedDatabaseHCL(plan ManagedDatabasePlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderMDBAWS(plan), nil
	case ProviderGCP:
		return renderMDBGCP(plan), nil
	case ProviderDigitalOcean:
		return renderMDBDO(plan), nil
	case ProviderAzure:
		return renderMDBAzure(plan), nil
	case ProviderLinode:
		return renderMDBLinode(plan), nil
	case ProviderUbicloud:
		return renderMDBUbicloud(plan)
	case ProviderOracle:
		return renderMDBOCI(plan), nil
	case ProviderIBM:
		return renderMDBIBM(plan), nil
	case ProviderAlibaba:
		return renderMDBAlibaba(plan), nil
	case ProviderOVH:
		return renderManagedDatabaseOVH(plan)
	case ProviderStackIt:
		return renderStackItMDB(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q", plan.Provider)
	}
}

// mdbAWSEngine maps the canonical engine to the AWS RDS engine token.
func mdbAWSEngine(engine string) string {
	if engine == DBEngineMySQL {
		return "mysql"
	}
	return "postgres"
}

func renderMDBAWS(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	sgName := name + "_subnet_group"
	var b strings.Builder

	// DB subnet group: multi-AZ across the region's subnets (RDS requires >= 2
	// subnets in distinct AZs). The network component renders the subnets.
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "resource \"aws_db_subnet_group\" %q {\n", sgName)
		fmt.Fprintf(&b, "  name       = \"%s-subnets\"\n", name)
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("data.aws_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  subnet_ids = [%s]\n", strings.Join(labels, ", "))
		fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.DBName)
		b.WriteString("}\n\n")
	}

	fmt.Fprintf(&b, "resource \"aws_db_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  identifier              = %q\n", name)
	fmt.Fprintf(&b, "  engine                  = %q\n", mdbAWSEngine(p.Engine))
	fmt.Fprintf(&b, "  engine_version          = %q\n", p.EngineVersion)
	fmt.Fprintf(&b, "  instance_class          = %q\n", p.DBClass)
	fmt.Fprintf(&b, "  allocated_storage       = %d\n", p.StorageGB)
	fmt.Fprintf(&b, "  storage_encrypted       = %t\n", p.Encrypted)
	fmt.Fprintf(&b, "  multi_az                = %t\n", p.HA)
	// Credentials are managed out-of-band (Secrets Manager / Vault); the username
	// is fixed and the password is generated/rotated, not committed. The round-trip
	// fixture provides a throwaway password via a variable.
	b.WriteString("  username                = \"pyxadmin\"\n")
	b.WriteString("  password                = var.db_password\n")
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "  db_subnet_group_name    = aws_db_subnet_group.%s.name\n", sgName)
	}
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  vpc_security_group_ids  = [aws_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	// Production-safe defaults: deletion protection + a final snapshot on destroy.
	fmt.Fprintf(&b, "  deletion_protection     = %t\n", p.DeletionProtection)
	fmt.Fprintf(&b, "  skip_final_snapshot     = %t\n", p.SkipFinalSnapshot)
	if !p.SkipFinalSnapshot {
		fmt.Fprintf(&b, "  final_snapshot_identifier = \"%s-final\"\n", name)
	}
	fmt.Fprintf(&b, "  apply_immediately       = false\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.DBName)
	b.WriteString("}\n")
	return b.String()
}

// mdbGCPEngine maps the canonical engine to a GCP Cloud SQL database_version
// token. Cloud SQL pins a major version (e.g. POSTGRES_16 / MYSQL_8_0); we map
// the resolved engine + version to that form.
func mdbGCPEngine(engine, version string) string {
	v := strings.ReplaceAll(strings.TrimSpace(version), ".", "_")
	if engine == DBEngineMySQL {
		if v == "" {
			v = "8_0"
		}
		return "MYSQL_" + v
	}
	if v == "" {
		v = "16"
	}
	return "POSTGRES_" + v
}

func renderMDBGCP(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"google_sql_database_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = %q\n", name)
	fmt.Fprintf(&b, "  region              = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  database_version    = %q\n", mdbGCPEngine(p.Engine, p.EngineVersion))
	// Production-safe default: deletion protection on the instance.
	fmt.Fprintf(&b, "  deletion_protection = %t\n", p.DeletionProtection)
	b.WriteString("  settings {\n")
	fmt.Fprintf(&b, "    tier              = %q\n", p.DBClass)
	fmt.Fprintf(&b, "    disk_size         = %d\n", p.StorageGB)
	b.WriteString("    disk_type         = \"PD_SSD\"\n")
	// Regional availability = HA (a standby in another zone); ZONAL otherwise.
	if p.HA {
		b.WriteString("    availability_type = \"REGIONAL\"\n")
	} else {
		b.WriteString("    availability_type = \"ZONAL\"\n")
	}
	b.WriteString("    ip_configuration {\n")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "      private_network = google_compute_network.%s.id\n", tfName(p.NetworkName))
	}
	b.WriteString("      ipv4_enabled    = false\n")
	b.WriteString("    }\n")
	b.WriteString("    backup_configuration {\n")
	b.WriteString("      enabled = true\n")
	b.WriteString("    }\n")
	b.WriteString("    user_labels = { pyxcloud = \"true\" }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// mdbDOEngine maps the canonical engine to the DO managed-cluster engine token.
func mdbDOEngine(engine string) string {
	if engine == DBEngineMySQL {
		return "mysql"
	}
	return "pg"
}

func renderMDBDO(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"digitalocean_database_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name       = %q\n", name)
	fmt.Fprintf(&b, "  engine     = %q\n", mdbDOEngine(p.Engine))
	fmt.Fprintf(&b, "  version    = %q\n", p.EngineVersion)
	fmt.Fprintf(&b, "  size       = %q\n", p.DBClass)
	fmt.Fprintf(&b, "  region     = %q\n", p.CSPRegion)
	// HA = a 2-node cluster (primary + standby); single node otherwise. DO managed
	// clusters are encrypted at rest by default (no toggle).
	if p.HA {
		b.WriteString("  node_count = 2\n")
	} else {
		b.WriteString("  node_count = 1\n")
	}
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  private_network_uuid = digitalocean_vpc.%s.id\n", tfName(p.NetworkName))
	}
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	// DO has no in-place deletion-protection flag; carry the production intent as a
	// lifecycle prevent_destroy guard when deletion_protection is on.
	if p.DeletionProtection {
		b.WriteString("  lifecycle {\n")
		b.WriteString("    prevent_destroy = true\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// RenderObjectStorageHCL renders a resolved ObjectStoragePlan into concrete
// cloud-provider Terraform HCL. Mirrors the other renderers (§8): translation
// returns a structured plan, rendering to .tf happens here and drives the
// per-provider `terraform plan` / real apply round-trip tests (SPEC §6).
//
//   - AWS: aws_s3_bucket + aws_s3_bucket_versioning + aws_s3_bucket_public_access_block.
//     PRIVATE BY DEFAULT (SPEC §5.7): when the plan is not public, ALL FOUR
//     public-access-block flags are true (block_public_acls / block_public_policy
//     / ignore_public_acls / restrict_public_buckets) so the bucket can never be
//     made world-readable by an errant ACL/policy. force_destroy follows the plan
//     (false in production; the TEST fixture sets it true for clean teardown).
//   - GCP: google_storage_bucket with uniform_bucket_level_access = true (no
//     per-object ACLs — the GCP private-by-default analogue) and versioning. The
//     location is the catalog-resolved csp_region.
//   - DigitalOcean: digitalocean_spaces_bucket with acl = "private" by default
//     (acl = "public-read" only when public), region-mapped, versioning via the
//     versioning block.
//
// The renderer re-asserts the private-by-default invariant from the plan: a plan
// with Public=false ALWAYS emits the full access-block / private ACL, even for a
// hand-built plan.
func RenderObjectStorageHCL(plan ObjectStoragePlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderObjectStorageAWS(plan), nil
	case ProviderGCP:
		return renderObjectStorageGCP(plan), nil
	case ProviderDigitalOcean:
		return renderObjectStorageDO(plan), nil
	case ProviderAzure:
		return renderObjectStorageAzure(plan), nil
	case ProviderLinode:
		return renderObjectStorageLinode(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"object-storage",
			"Ubicloud offers an S3-compatible object API but the Terraform provider exposes no bucket "+
				"resource; provision object-storage on aws (aws_s3_bucket) / gcp / digitalocean, or "+
				"manage the Ubicloud bucket out-of-band via its S3-compatible API.")
	case ProviderOracle:
		return renderObjectStorageOCI(plan), nil
	case ProviderIBM:
		return renderObjectStorageIBM(plan), nil
	case ProviderAlibaba:
		return renderObjectStorageAlibaba(plan), nil
	case ProviderOVH:
		return renderObjectStorageOVH(plan)
	case ProviderStackIt:
		return renderStackItObjectStorage(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q", plan.Provider)
	}
}

func renderObjectStorageAWS(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"aws_s3_bucket\" %q {\n", label)
	fmt.Fprintf(&b, "  bucket        = %q\n", p.BucketName)
	fmt.Fprintf(&b, "  force_destroy = %t\n", p.ForceDestroy)
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.BucketName)
	b.WriteString("}\n\n")

	// Versioning is a separate resource on the AWS provider v4+. Emit it always so
	// the desired state (enabled OR suspended) is explicit and idempotent.
	versioningStatus := "Suspended"
	if p.Versioning {
		versioningStatus = "Enabled"
	}
	fmt.Fprintf(&b, "resource \"aws_s3_bucket_versioning\" %q {\n", label)
	fmt.Fprintf(&b, "  bucket = aws_s3_bucket.%s.id\n", label)
	b.WriteString("  versioning_configuration {\n")
	fmt.Fprintf(&b, "    status = %q\n", versioningStatus)
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// PRIVATE BY DEFAULT (SPEC §5.7): unless explicitly public, block ALL public
	// access at the bucket level so an errant ACL/policy can never expose it.
	blockPublic := !p.Public
	fmt.Fprintf(&b, "resource \"aws_s3_bucket_public_access_block\" %q {\n", label)
	fmt.Fprintf(&b, "  bucket                  = aws_s3_bucket.%s.id\n", label)
	fmt.Fprintf(&b, "  block_public_acls       = %t\n", blockPublic)
	fmt.Fprintf(&b, "  block_public_policy     = %t\n", blockPublic)
	fmt.Fprintf(&b, "  ignore_public_acls      = %t\n", blockPublic)
	fmt.Fprintf(&b, "  restrict_public_buckets = %t\n", blockPublic)
	b.WriteString("}\n")

	// pd-MIG-OBJSTORE-PARITY: SSE, lifecycle, bucket-policy, access-logs as the
	// separate AWS v4+ sub-resources (the AWS peer of the DO Spaces parity).
	if p.SSE != nil {
		fmt.Fprintf(&b, "\nresource \"aws_s3_bucket_server_side_encryption_configuration\" %q {\n", label)
		fmt.Fprintf(&b, "  bucket = aws_s3_bucket.%s.id\n", label)
		b.WriteString("  rule {\n")
		b.WriteString("    apply_server_side_encryption_by_default {\n")
		fmt.Fprintf(&b, "      sse_algorithm     = %q\n", p.SSE.Algorithm)
		if p.SSE.Algorithm == SSEAlgoKMS && p.SSE.KMSKeyID != "" {
			fmt.Fprintf(&b, "      kms_master_key_id = %q\n", p.SSE.KMSKeyID)
		}
		b.WriteString("    }\n")
		b.WriteString("  }\n")
		b.WriteString("}\n")
	}
	if len(p.Lifecycle) > 0 {
		fmt.Fprintf(&b, "\nresource \"aws_s3_bucket_lifecycle_configuration\" %q {\n", label)
		fmt.Fprintf(&b, "  bucket = aws_s3_bucket.%s.id\n", label)
		for _, r := range p.Lifecycle {
			b.WriteString("  rule {\n")
			fmt.Fprintf(&b, "    id     = %q\n", r.ID)
			status := "Disabled"
			if r.Enabled {
				status = "Enabled"
			}
			fmt.Fprintf(&b, "    status = %q\n", status)
			if r.Prefix != "" {
				b.WriteString("    filter {\n")
				fmt.Fprintf(&b, "      prefix = %q\n", r.Prefix)
				b.WriteString("    }\n")
			}
			if r.ExpireDays > 0 {
				b.WriteString("    expiration {\n")
				fmt.Fprintf(&b, "      days = %d\n", r.ExpireDays)
				b.WriteString("    }\n")
			}
			if r.NoncurrentVersionExpireDays > 0 {
				b.WriteString("    noncurrent_version_expiration {\n")
				fmt.Fprintf(&b, "      noncurrent_days = %d\n", r.NoncurrentVersionExpireDays)
				b.WriteString("    }\n")
			}
			if r.AbortIncompleteMultipartDays > 0 {
				b.WriteString("    abort_incomplete_multipart_upload {\n")
				fmt.Fprintf(&b, "      days_after_initiation = %d\n", r.AbortIncompleteMultipartDays)
				b.WriteString("    }\n")
			}
			b.WriteString("  }\n")
		}
		b.WriteString("}\n")
	}
	if p.BucketPolicy != "" {
		fmt.Fprintf(&b, "\nresource \"aws_s3_bucket_policy\" %q {\n", label)
		fmt.Fprintf(&b, "  bucket = aws_s3_bucket.%s.id\n", label)
		fmt.Fprintf(&b, "  policy = %s\n", iamHeredoc(p.BucketPolicy))
		b.WriteString("}\n")
	}
	if p.AccessLogs != nil {
		fmt.Fprintf(&b, "\nresource \"aws_s3_bucket_logging\" %q {\n", label)
		fmt.Fprintf(&b, "  bucket        = aws_s3_bucket.%s.id\n", label)
		fmt.Fprintf(&b, "  target_bucket = %q\n", p.AccessLogs.TargetBucket)
		fmt.Fprintf(&b, "  target_prefix = %q\n", p.AccessLogs.TargetPrefix)
		b.WriteString("}\n")
	}
	return b.String()
}

func renderObjectStorageGCP(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"google_storage_bucket\" %q {\n", label)
	fmt.Fprintf(&b, "  name          = %q\n", p.BucketName)
	fmt.Fprintf(&b, "  location      = %q\n", strings.ToUpper(p.CSPRegion))
	fmt.Fprintf(&b, "  force_destroy = %t\n", p.ForceDestroy)
	// PRIVATE BY DEFAULT: uniform bucket-level access disables per-object ACLs so
	// the bucket is governed solely by IAM (no accidental public-read object ACL).
	// Public access, when opted in, is granted out-of-band via an IAM binding; the
	// bucket resource itself stays uniform/private.
	b.WriteString("  uniform_bucket_level_access = true\n")
	if !p.Public {
		b.WriteString("  public_access_prevention    = \"enforced\"\n")
	}
	b.WriteString("  versioning {\n")
	fmt.Fprintf(&b, "    enabled = %t\n", p.Versioning)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderObjectStorageDO(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder

	// PRIVATE BY DEFAULT: acl = "private" unless explicitly public-read.
	acl := "private"
	if p.Public {
		acl = "public-read"
	}
	fmt.Fprintf(&b, "resource \"digitalocean_spaces_bucket\" %q {\n", label)
	fmt.Fprintf(&b, "  name          = %q\n", p.BucketName)
	fmt.Fprintf(&b, "  region        = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  acl           = %q\n", acl)
	fmt.Fprintf(&b, "  force_destroy = %t\n", p.ForceDestroy)
	b.WriteString("  versioning {\n")
	fmt.Fprintf(&b, "    enabled = %t\n", p.Versioning)
	b.WriteString("  }\n")

	// pd-MIG-OBJSTORE-PARITY: DO Spaces is S3-compatible. Lifecycle rules render as
	// inline `lifecycle_rule` blocks on the bucket resource (the Spaces analogue of
	// the AWS lifecycle sub-resource). SSE-at-rest is ALWAYS ON for Spaces (no
	// resource toggle) — an explicit AES256 request is honoured by the platform, so
	// we record it as an inline comment for migration traceability rather than a
	// no-op attribute. KMS was already rejected at translate time for DO.
	if p.SSE != nil {
		fmt.Fprintf(&b, "  # server-side encryption (%s) is enabled at rest by default on DO Spaces\n", p.SSE.Algorithm)
	}
	for _, r := range p.Lifecycle {
		b.WriteString("  lifecycle_rule {\n")
		fmt.Fprintf(&b, "    id      = %q\n", r.ID)
		fmt.Fprintf(&b, "    enabled = %t\n", r.Enabled)
		if r.Prefix != "" {
			fmt.Fprintf(&b, "    prefix  = %q\n", r.Prefix)
		}
		if r.ExpireDays > 0 {
			b.WriteString("    expiration {\n")
			fmt.Fprintf(&b, "      days = %d\n", r.ExpireDays)
			b.WriteString("    }\n")
		}
		if r.NoncurrentVersionExpireDays > 0 {
			b.WriteString("    noncurrent_version_expiration {\n")
			fmt.Fprintf(&b, "      days = %d\n", r.NoncurrentVersionExpireDays)
			b.WriteString("    }\n")
		}
		if r.AbortIncompleteMultipartDays > 0 {
			b.WriteString("    abort_incomplete_multipart_upload_days = ")
			fmt.Fprintf(&b, "%d\n", r.AbortIncompleteMultipartDays)
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")

	// Bucket-policy is a separate S3-compatible resource on DO.
	if p.BucketPolicy != "" {
		fmt.Fprintf(&b, "\nresource \"digitalocean_spaces_bucket_policy\" %q {\n", label)
		fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  bucket = digitalocean_spaces_bucket.%s.name\n", label)
		fmt.Fprintf(&b, "  policy = %s\n", iamHeredoc(p.BucketPolicy))
		b.WriteString("}\n")
	}

	// Access-logs: DO Spaces exposes no server-access-logging resource. The
	// translate step does not reject it (logs are advisory, not data-protection),
	// so we record the migration gap as a comment rather than silently swallowing
	// the intent.
	if p.AccessLogs != nil {
		fmt.Fprintf(&b, "\n# NOTE: server access logging (target %q) has no DO Spaces equivalent; "+
			"front the bucket with a CDN/edge log pipeline if access logs are required.\n", p.AccessLogs.TargetBucket)
	}
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

// RenderIAMHCL renders an IAMPlan into concrete provider HCL. DO never reaches
// here (TranslateIAM rejects it). AWS emits the role + inline policies + managed
// attachments + optional instance profile; GCP emits a service account.
func RenderIAMHCL(p IAMPlan) (string, error) {
	switch p.Provider {
	case ProviderAWS:
		return renderIAMAWS(p), nil
	case ProviderGCP:
		return renderIAMGCP(p), nil
	default:
		return "", fmt.Errorf("iam: render unsupported for provider %q", p.Provider)
	}
}

func RenderAccessPolicyHCL(p AccessPolicyPlan) (string, error) {
	switch p.Provider {
	case ProviderAWS:
		return renderAccessPolicyAWS(p), nil
	default:
		return renderAccessPolicyPortable(p), nil
	}
}

// iamHeredoc wraps a raw policy JSON document as an HCL indented heredoc so the
// JSON (quotes, $, braces) needs no escaping.
func iamHeredoc(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return "<<-PYXIAMPOLICY\n" + s + "PYXIAMPOLICY\n  "
}

func renderIAMAWS(p IAMPlan) string {
	var b strings.Builder
	role := tfName(p.Name)
	fmt.Fprintf(&b, "resource \"aws_iam_role\" %q {\n", role)
	fmt.Fprintf(&b, "  name = %q\n", p.Name)
	fmt.Fprintf(&b, "  assume_role_policy = jsonencode({\n")
	b.WriteString("    Version = \"2012-10-17\"\n")
	b.WriteString("    Statement = [{\n")
	b.WriteString("      Action    = \"sts:AssumeRole\"\n")
	b.WriteString("      Effect    = \"Allow\"\n")
	fmt.Fprintf(&b, "      Principal = { Service = %q }\n", p.AssumeService)
	b.WriteString("    }]\n")
	b.WriteString("  })\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	for _, pol := range p.InlinePolicies {
		pn := tfName(p.Name + "-" + pol.Name)
		fmt.Fprintf(&b, "resource \"aws_iam_role_policy\" %q {\n", pn)
		fmt.Fprintf(&b, "  name   = %q\n", pol.Name)
		fmt.Fprintf(&b, "  role   = aws_iam_role.%s.id\n", role)
		fmt.Fprintf(&b, "  policy = %s\n", iamHeredoc(pol.Document))
		b.WriteString("}\n\n")
	}

	for i, arn := range p.ManagedPolicyARNs {
		an := tfName(fmt.Sprintf("%s-managed-%d", p.Name, i+1))
		fmt.Fprintf(&b, "resource \"aws_iam_role_policy_attachment\" %q {\n", an)
		fmt.Fprintf(&b, "  role       = aws_iam_role.%s.name\n", role)
		fmt.Fprintf(&b, "  policy_arn = %q\n", arn)
		b.WriteString("}\n\n")
	}

	if p.InstanceProfile {
		fmt.Fprintf(&b, "resource \"aws_iam_instance_profile\" %q {\n", role)
		fmt.Fprintf(&b, "  name = %q\n", p.Name)
		fmt.Fprintf(&b, "  role = aws_iam_role.%s.name\n", role)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderAccessPolicyAWS(p AccessPolicyPlan) string {
	var b strings.Builder
	for _, pol := range p.InlinePolicies {
		pn := tfName(p.Name + "-" + pol.Name)
		fmt.Fprintf(&b, "resource \"aws_iam_policy\" %q {\n", pn)
		fmt.Fprintf(&b, "  name   = %q\n", p.Name+"-"+pol.Name)
		fmt.Fprintf(&b, "  policy = %s\n", iamHeredoc(pol.Document))
		b.WriteString("  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	for i, arn := range p.ManagedPolicyARNs {
		rn := tfName(fmt.Sprintf("%s-managed-%d", p.Name, i+1))
		fmt.Fprintf(&b, "resource \"terraform_data\" %q {\n", rn)
		fmt.Fprintf(&b, "  input = { policy_arn = %q }\n", arn)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderAccessPolicyPortable(p AccessPolicyPlan) string {
	var b strings.Builder
	for _, pol := range p.InlinePolicies {
		pn := tfName(p.Name + "-" + pol.Name)
		fmt.Fprintf(&b, "resource \"terraform_data\" %q {\n", pn)
		b.WriteString("  input = {\n")
		fmt.Fprintf(&b, "    provider = %q\n", p.Provider)
		fmt.Fprintf(&b, "    name     = %q\n", pol.Name)
		fmt.Fprintf(&b, "    document = %s\n", iamHeredoc(pol.Document))
		b.WriteString("  }\n")
		b.WriteString("}\n\n")
	}
	for i, arn := range p.ManagedPolicyARNs {
		rn := tfName(fmt.Sprintf("%s-managed-%d", p.Name, i+1))
		fmt.Fprintf(&b, "resource \"terraform_data\" %q {\n", rn)
		fmt.Fprintf(&b, "  input = { provider = %q, policy_arn = %q }\n", p.Provider, arn)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderIAMGCP(p IAMPlan) string {
	var b strings.Builder
	sa := tfName(p.Name)
	fmt.Fprintf(&b, "resource \"google_service_account\" %q {\n", sa)
	// GCP account_id: <=30 chars, [a-z][-a-z0-9]*; derive a safe id from the name.
	fmt.Fprintf(&b, "  account_id   = %q\n", gcpAccountID(p.Name))
	fmt.Fprintf(&b, "  display_name = %q\n", p.Name)
	b.WriteString("}\n")
	return b.String()
}

// gcpAccountID reduces a name to the GCP service-account id charset and length.
func gcpAccountID(name string) string {
	s := strings.ToLower(name)
	var out strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			out.WriteRune(r)
		} else {
			out.WriteRune('-')
		}
	}
	id := strings.Trim(out.String(), "-")
	if id == "" {
		id = "pyx-sa"
	}
	if len(id) > 30 {
		id = strings.Trim(id[:30], "-")
	}
	return id
}

// RenderMonitoringHCL renders a MonitoringPlan into provider HCL.
//
//   - AWS: aws_cloudwatch_log_group + aws_cloudwatch_metric_alarm (the CloudWatch+SNS
//     peer being migrated away from).
//   - GCP: google_logging_project_bucket_config.
//   - DigitalOcean: the LGTM operator-pattern stack — kube-prometheus-stack + Loki
//     as upstream helm_release CORE plus our ServiceMonitor/PrometheusRule/Grafana
//     datasource custom resources (EXTRA). See render_monitoring_lgtm.go.
func RenderMonitoringHCL(p MonitoringPlan) (string, error) {
	switch p.Provider {
	case ProviderAWS:
		return renderMonitoringAWS(p), nil
	case ProviderGCP:
		return renderMonitoringGCP(p), nil
	case ProviderDigitalOcean:
		return renderMonitoringDO(p), nil
	default:
		return "", fmt.Errorf("monitoring: render unsupported for provider %q", p.Provider)
	}
}

func renderMonitoringAWS(p MonitoringPlan) string {
	var b strings.Builder
	for _, lg := range p.LogGroups {
		rn := tfName(lg.Name)
		fmt.Fprintf(&b, "resource \"aws_cloudwatch_log_group\" %q {\n", rn)
		fmt.Fprintf(&b, "  name = %q\n", lg.Name)
		if lg.RetentionDays > 0 {
			fmt.Fprintf(&b, "  retention_in_days = %d\n", lg.RetentionDays)
		}
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	for _, a := range p.Alarms {
		rn := tfName(a.Name)
		fmt.Fprintf(&b, "resource \"aws_cloudwatch_metric_alarm\" %q {\n", rn)
		fmt.Fprintf(&b, "  alarm_name          = %q\n", a.Name)
		fmt.Fprintf(&b, "  namespace           = %q\n", a.Namespace)
		fmt.Fprintf(&b, "  metric_name         = %q\n", a.MetricName)
		fmt.Fprintf(&b, "  comparison_operator = %q\n", a.ComparisonOperator)
		fmt.Fprintf(&b, "  threshold           = %g\n", a.Threshold)
		ep := a.EvaluationPeriods
		if ep <= 0 {
			ep = 1
		}
		fmt.Fprintf(&b, "  evaluation_periods  = %d\n", ep)
		per := a.PeriodSeconds
		if per <= 0 {
			per = 300
		}
		fmt.Fprintf(&b, "  period              = %d\n", per)
		stat := a.Statistic
		if stat == "" {
			stat = "Average"
		}
		fmt.Fprintf(&b, "  statistic           = %q\n", stat)
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderMonitoringGCP(p MonitoringPlan) string {
	var b strings.Builder
	for _, lg := range p.LogGroups {
		rn := tfName(lg.Name)
		fmt.Fprintf(&b, "resource \"google_logging_project_bucket_config\" %q {\n", rn)
		fmt.Fprintf(&b, "  bucket_id      = %q\n", lg.Name)
		fmt.Fprintf(&b, "  location       = \"global\"\n")
		if lg.RetentionDays > 0 {
			fmt.Fprintf(&b, "  retention_days = %d\n", lg.RetentionDays)
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RenderContainerRegistryHCL renders a resolved ContainerRegistryPlan into
// concrete cloud-provider Terraform HCL. Mirrors RenderObjectStorageHCL:
// translation returns a structured plan, rendering to .tf happens here and drives
// the per-provider round-trip tests (SPEC §6).
//
//   - AWS:          aws_ecr_repository (the ECR being migrated FROM); image tag
//     mutability honours ImmutableTags.
//   - GCP:          google_artifact_registry_repository (DOCKER format).
//   - DigitalOcean: digitalocean_container_registry with subscription_tier_slug
//     and, when GarbageCollection is requested, a digitalocean_container_registry
//     docker-credentials-free GC is not a TF resource — DO runs GC server-side —
//     so we surface the request as a documented note (the registry resource has
//     no gc block); the tier IS a first-class attribute.
//
// container-registry is a wave-1 (aws/gcp/do) component: any other provider is a
// hard render-time error (no silent fallback).
func RenderContainerRegistryHCL(plan ContainerRegistryPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderContainerRegistryAWS(plan), nil
	case ProviderGCP:
		return renderContainerRegistryGCP(plan), nil
	case ProviderDigitalOcean:
		return renderContainerRegistryDO(plan), nil
	default:
		return "", fmt.Errorf(
			"render: container-registry is unsupported on %q; PyxCloud maps it to "+
				"aws_ecr_repository / google_artifact_registry_repository / "+
				"digitalocean_container_registry only (this is a hard plan-time error, "+
				"never a silent fallback)", plan.Provider)
	}
}

func renderContainerRegistryAWS(p ContainerRegistryPlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	mutability := "MUTABLE"
	if p.ImmutableTags {
		mutability = "IMMUTABLE"
	}
	fmt.Fprintf(&b, "resource \"aws_ecr_repository\" %q {\n", label)
	fmt.Fprintf(&b, "  name                 = %q\n", p.RegistryName)
	fmt.Fprintf(&b, "  image_tag_mutability = %q\n", mutability)
	b.WriteString("  image_scanning_configuration {\n")
	b.WriteString("    scan_on_push = true\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.RegistryName)
	b.WriteString("}\n")
	return b.String()
}

func renderContainerRegistryGCP(p ContainerRegistryPlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_artifact_registry_repository\" %q {\n", label)
	fmt.Fprintf(&b, "  repository_id = %q\n", p.RegistryName)
	fmt.Fprintf(&b, "  location      = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  format        = \"DOCKER\"\n")
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderContainerRegistryDO(p ContainerRegistryPlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	// DO runs garbage collection server-side (POST /registry/{name}/garbage-collection
	// / the doctl/API path), not via a Terraform sub-resource. When the user opts
	// in we record it as a comment on the resource so the intent is visible in the
	// plan; the registry resource itself carries name + region + tier.
	if p.GarbageCollection {
		b.WriteString("# garbage_collection = true (DO runs GC server-side via the registry API;\n")
		b.WriteString("# there is no Terraform sub-resource — trigger it out-of-band post-apply).\n")
	}
	fmt.Fprintf(&b, "resource \"digitalocean_container_registry\" %q {\n", label)
	fmt.Fprintf(&b, "  name                   = %q\n", p.RegistryName)
	fmt.Fprintf(&b, "  subscription_tier_slug = %q\n", p.Tier)
	fmt.Fprintf(&b, "  region                 = %q\n", p.CSPRegion)
	b.WriteString("}\n")
	return b.String()
}

// RenderReservedIPHCL renders a resolved ReservedIPPlan into concrete
// cloud-provider Terraform HCL. Mirrors RenderContainerRegistryHCL.
//
//   - AWS:          aws_eip (the EIP being migrated FROM); when AttachTo is set,
//     wires instance = aws_instance.<target>.id.
//   - GCP:          google_compute_address (a regional static external IP).
//   - DigitalOcean: digitalocean_reserved_ip; when AttachTo is set, binds
//     droplet_id = digitalocean_droplet.<target>.id (the stable VPN endpoint).
//
// reserved-ip is a wave-1 (aws/gcp/do) component: any other provider is a hard
// render-time error (no silent fallback).
func RenderReservedIPHCL(plan ReservedIPPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderReservedIPAWS(plan), nil
	case ProviderGCP:
		return renderReservedIPGCP(plan), nil
	case ProviderDigitalOcean:
		return renderReservedIPDO(plan), nil
	default:
		return "", fmt.Errorf(
			"render: reserved-ip is unsupported on %q; PyxCloud maps it to "+
				"aws_eip / google_compute_address / digitalocean_reserved_ip only "+
				"(this is a hard plan-time error, never a silent fallback)", plan.Provider)
	}
}

func renderReservedIPAWS(p ReservedIPPlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_eip\" %q {\n", label)
	b.WriteString("  domain = \"vpc\"\n")
	if p.AttachTo != "" {
		fmt.Fprintf(&b, "  instance = aws_instance.%s.id\n", tfName(p.AttachTo))
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.LogicalName)
	b.WriteString("}\n")
	return b.String()
}

func renderReservedIPGCP(p ReservedIPPlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_compute_address\" %q {\n", label)
	fmt.Fprintf(&b, "  name         = %q\n", tfName(p.LogicalName))
	fmt.Fprintf(&b, "  region       = %q\n", p.CSPRegion)
	b.WriteString("  address_type = \"EXTERNAL\"\n")
	b.WriteString("}\n")
	return b.String()
}

func renderReservedIPDO(p ReservedIPPlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"digitalocean_reserved_ip\" %q {\n", label)
	fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
	if p.AttachTo != "" {
		fmt.Fprintf(&b, "  droplet_id = digitalocean_droplet.%s.id\n", tfName(p.AttachTo))
	}
	b.WriteString("}\n")
	return b.String()
}
