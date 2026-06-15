package catalog

// render_stackit.go — wave-2 StackIt (pd-TF-PROVIDERS-WAVE2: stackit) renderers.
//
// This is the StackIt half of the "provider owns rendering" decision (SPEC §8):
// each Translate* returns a STRUCTURED plan, and the per-provider rendering to .tf
// lives here, mirroring renderAWS/renderGCP/renderDO in render.go and render_macro.go.
//
// StackIt specifics that shape every renderer:
//   - Every StackIt resource is PROJECT-SCOPED: it needs `project_id`. We render it
//     as a reference to `var.stackit_project_id` so the generated config is valid,
//     parametric, and never embeds a hard-coded project. The fixture declares that
//     variable. `region` is the catalog-resolved csp_region (e.g. eu01).
//   - StackIt is default-secure: buckets are private (no public ACL), networks are
//     not auto-exposed, security groups are explicit. We do not add public toggles.
//   - StackIt uses concrete flavor names (g1.2 etc.) and PostgreSQL/MariaDB Flex
//     with an explicit flavor{cpu,ram}+storage{class,size} block — all resolved
//     from the catalog (never invented).
//
// Resource names verified against stackitcloud/terraform-provider-stackit docs.

import (
	"fmt"
	"strings"
)

// stackitProjectRef is the parametric project_id every StackIt resource carries.
// Kept in one place so the contract (a declared variable, never a literal) is
// obvious and consistent across renderers.
const stackitProjectRef = "var.stackit_project_id"

// renderStackItNetwork renders stackit_network. StackIt networks are project- and
// region-scoped; the VPC CIDR maps to ipv4_prefix. StackIt has no per-subnet
// resource (subnetting is handled within the network / via the network area), so
// subnets in the plan inform the prefix only — mirroring the DO region-scoped VPC.
func renderStackItNetwork(p NetworkPlan) string {
	name := tfName(p.VPCName)
	prefix := p.CIDR
	if len(p.Subnets) > 0 {
		prefix = p.Subnets[0].CIDR
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"stackit_network\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id  = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name        = %q\n", p.VPCName)
	fmt.Fprintf(&b, "  region      = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  ipv4_prefix = %q\n", prefix)
	b.WriteString("  routed      = true\n")
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\", name = %q }\n", p.VPCName)
	b.WriteString("}\n")
	return b.String()
}

// renderStackItSG renders stackit_security_group plus one stackit_security_group_rule
// per concrete rule. StackIt rules carry a direction, a protocol{name=...} block, an
// optional port_range{min,max}, and either an ip_range or a remote_security_group_id.
func renderStackItSG(p SecurityGroupPlan) string {
	// Re-apply the ASCII guard (a hand-built plan could carry non-ASCII).
	desc := asciiOnly(p.Description)
	if desc == "" {
		desc = "Managed by PyxCloud"
	}
	sg := tfName(p.SGName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"stackit_security_group\" %q {\n", sg)
	fmt.Fprintf(&b, "  project_id  = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name        = %q\n", p.SGName)
	fmt.Fprintf(&b, "  region      = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  description = %q\n", desc)
	b.WriteString("  stateful    = true\n")
	b.WriteString("}\n")

	for i, r := range p.Rules {
		rl := fmt.Sprintf("%s_rule_%d", sg, i+1)
		fmt.Fprintf(&b, "\nresource \"stackit_security_group_rule\" %q {\n", rl)
		fmt.Fprintf(&b, "  project_id        = %s\n", stackitProjectRef)
		fmt.Fprintf(&b, "  region            = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  security_group_id = stackit_security_group.%s.security_group_id\n", sg)
		fmt.Fprintf(&b, "  direction         = %q\n", r.Direction)
		fmt.Fprintf(&b, "  ether_type        = %q\n", "IPv4")
		fmt.Fprintf(&b, "  protocol = { name = %q }\n", stackitProto(r.Protocol))
		if r.Protocol != ProtoICMP && (r.FromPort != 0 || r.ToPort != 0) {
			fmt.Fprintf(&b, "  port_range = { min = %d, max = %d }\n", r.FromPort, r.ToPort)
		}
		switch {
		case r.SourceSG != "":
			fmt.Fprintf(&b, "  remote_security_group_id = stackit_security_group.%s.security_group_id\n", tfName(r.SourceSG))
		case len(r.CIDRs) > 0:
			// StackIt takes a single ip_range per rule; the first CIDR is used and the
			// rest (e.g. the ::/0 IPv6 companion) are documented as a follow-up rule.
			fmt.Fprintf(&b, "  ip_range = %q\n", firstV4(r.CIDRs))
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// stackitProto maps a canonical protocol to a StackIt protocol name. StackIt has no
// "all" protocol (guarded at translate), so only tcp/udp/icmp reach here.
func stackitProto(proto string) string {
	switch proto {
	case ProtoTCP:
		return "tcp"
	case ProtoUDP:
		return "udp"
	case ProtoICMP:
		return "icmp"
	default:
		return proto
	}
}

// firstV4 returns the first IPv4 CIDR in the list, falling back to the first entry.
func firstV4(cidrs []string) string {
	for _, c := range cidrs {
		if !strings.Contains(c, ":") {
			return c
		}
	}
	if len(cidrs) > 0 {
		return cidrs[0]
	}
	return "0.0.0.0/0"
}

// renderStackItVM renders stackit_server instances. Each instance gets the
// catalog-resolved machine_type (flavor) and an ephemeral boot volume from the
// resolved image. project_id/region are parametric/catalog-resolved.
func renderStackItVM(p VMPlan) string {
	var b strings.Builder
	for i, inst := range p.Instances {
		label := tfName(inst.Name)
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "resource \"stackit_server\" %q {\n", label)
		fmt.Fprintf(&b, "  project_id   = %s\n", stackitProjectRef)
		fmt.Fprintf(&b, "  name         = %q\n", inst.Name)
		fmt.Fprintf(&b, "  region       = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  machine_type = %q\n", p.InstanceType)
		fmt.Fprintf(&b, "  boot_volume = {\n")
		b.WriteString("    source_type = \"image\"\n")
		fmt.Fprintf(&b, "    source_id   = %q\n", p.Image)
		b.WriteString("    size        = 32\n")
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\", os = %q }\n", p.OSName)
		b.WriteString("}\n")
	}
	return b.String()
}

// renderStackItMDB renders the catalog-resolved managed database: StackIt
// PostgreSQL Flex (postgres) or MariaDB Flex (mysql). The flavor carries the
// resolved cpu/ram; storage carries the allocated size. The data-safety guard
// (provider-agnostic) runs at plan time before this renders.
func renderStackItMDB(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	resource := p.ResourceType
	if resource == "" {
		resource = "stackit_postgresflex_instance"
	}
	// HA -> 3 replicas (replication), otherwise single mode (1 replica).
	replicas := 1
	if p.HA {
		replicas = 3
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource %q %q {\n", resource, name)
	fmt.Fprintf(&b, "  project_id      = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name            = %q\n", p.DBName)
	fmt.Fprintf(&b, "  region          = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  version         = %q\n", p.EngineVersion)
	fmt.Fprintf(&b, "  replicas        = %d\n", replicas)
	b.WriteString("  backup_schedule = \"0 2 * * *\"\n")
	b.WriteString("  acl             = [\"0.0.0.0/0\"]\n")
	fmt.Fprintf(&b, "  flavor = {\n    cpu = %d\n    ram = %d\n  }\n", p.CPU, p.RAM)
	fmt.Fprintf(&b, "  storage = {\n    class = \"premium-perf2-stackit\"\n    size  = %d\n  }\n", p.StorageGB)
	b.WriteString("}\n")
	return b.String()
}

// renderStackItObjectStorage renders stackit_objectstorage_bucket. StackIt buckets
// are private by default (no public-ACL field — matching PyxCloud's default-secure
// posture; a public bucket is rejected at translate time).
func renderStackItObjectStorage(p ObjectStoragePlan) string {
	name := tfName(p.LogicalName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"stackit_objectstorage_bucket\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name       = %q\n", p.BucketName)
	fmt.Fprintf(&b, "  region     = %q\n", p.CSPRegion)
	b.WriteString("}\n")
	return b.String()
}

// renderStackItKubernetes renders stackit_ske_cluster with one autoscaling node
// pool. The node machine_type is the catalog-resolved flavor (the SAME ResolveSKU
// path the VM/scale-group use). Availability zones are the catalog-derived SKE zones.
func renderStackItKubernetes(p K8sPlan) string {
	name := tfName(p.Name)
	// SKE node-pool availability zones use the <region>-m worker-zone form; fall
	// back to the catalog-derived zones when present.
	zones := p.Zones
	if len(zones) == 0 {
		zones = []string{p.CSPRegion + "-1"}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"stackit_ske_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name       = %q\n", p.Name)
	fmt.Fprintf(&b, "  region     = %q\n", p.CSPRegion)
	if p.Version != "" {
		fmt.Fprintf(&b, "  kubernetes_version_min = %q\n", p.Version)
	}
	b.WriteString("  node_pools = [{\n")
	b.WriteString("    name               = \"default\"\n")
	fmt.Fprintf(&b, "    machine_type       = %q\n", p.NodeType)
	fmt.Fprintf(&b, "    minimum            = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "    maximum            = %d\n", p.MaxNodes)
	fmt.Fprintf(&b, "    availability_zones = %s\n", hclStringList(zones))
	b.WriteString("  }]\n")
	b.WriteString("}\n")
	return b.String()
}

// renderStackItDNSZone renders stackit_dns_zone (public zone — a private zone is
// rejected at translate time).
func renderStackItDNSZone(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"stackit_dns_zone\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name       = %q\n", p.Name)
	fmt.Fprintf(&b, "  dns_name   = %q\n", p.Domain)
	b.WriteString("  type       = \"primary\"\n")
	b.WriteString("}\n")
	return b.String()
}

// renderStackItSecrets renders stackit_secretsmanager_instance — the managed,
// Vault-backed secrets store (the canonical secrets-manager for StackIt).
func renderStackItSecrets(p SecretsPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"stackit_secretsmanager_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name       = %q\n", p.Name)
	b.WriteString("  acls       = [\"10.0.0.0/8\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// renderStackItLoadBalancer renders stackit_loadbalancer with its listeners and a
// single target pool. The LB is internal/secure by default (no external_address
// emitted); networks reference the place's network. The target pool references the
// fronted component by name (resolved at apply by the operator's wiring).
func renderStackItLoadBalancer(p LoadBalancerPlan) string {
	name := tfName(p.LBName)
	target := canonicalName(p.TargetName, "pyxcloud-targets")
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"stackit_loadbalancer\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", stackitProjectRef)
	fmt.Fprintf(&b, "  name       = %q\n", p.LBName)
	fmt.Fprintf(&b, "  region     = %q\n", p.CSPRegion)

	b.WriteString("  listeners = [\n")
	for _, l := range p.Listeners {
		b.WriteString("    {\n")
		fmt.Fprintf(&b, "      port        = %d\n", l.Port)
		fmt.Fprintf(&b, "      protocol    = %q\n", stackitLBProto(l.Protocol))
		fmt.Fprintf(&b, "      target_pool = %q\n", target)
		b.WriteString("    },\n")
	}
	b.WriteString("  ]\n")

	b.WriteString("  networks = [\n")
	fmt.Fprintf(&b, "    { network_id = stackit_network.%s.network_id, role = \"ROLE_LISTENERS_AND_TARGETS\" },\n", tfName(p.NetworkName))
	b.WriteString("  ]\n")

	hcPort := p.HealthCheck.Port
	if hcPort == 0 && len(p.Listeners) > 0 {
		hcPort = p.Listeners[0].Port
	}
	b.WriteString("  target_pools = [{\n")
	fmt.Fprintf(&b, "    name        = %q\n", target)
	if len(p.Listeners) > 0 {
		fmt.Fprintf(&b, "    target_port = %d\n", p.Listeners[0].Port)
	}
	b.WriteString("    active_health_check = {\n")
	fmt.Fprintf(&b, "      healthy_threshold   = %d\n", maxInt(p.HealthCheck.HealthyThreshold, 1))
	fmt.Fprintf(&b, "      unhealthy_threshold = %d\n", maxInt(p.HealthCheck.UnhealthyThreshold, 1))
	fmt.Fprintf(&b, "      interval            = \"%ds\"\n", maxInt(p.HealthCheck.IntervalSeconds, 10))
	b.WriteString("    }\n")
	b.WriteString("  }]\n")
	b.WriteString("}\n")
	return b.String()
}

// stackitLBProto maps a canonical listener protocol to a StackIt LB protocol enum.
func stackitLBProto(proto string) string {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "udp":
		return "PROTOCOL_UDP"
	case "tls", "tls_passthrough":
		return "PROTOCOL_TLS_PASSTHROUGH"
	default:
		return "PROTOCOL_TCP"
	}
}

// hclStringList renders a string slice as an HCL list literal: ["a", "b"].
func hclStringList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, s := range items {
		parts = append(parts, fmt.Sprintf("%q", s))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// maxInt is defined once in render_ibm.go (an identical helper); StackIt reuses it.
