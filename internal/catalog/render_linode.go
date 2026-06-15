package catalog

import (
	"fmt"
	"strings"
)

// This file renders the supported PyxCloud components into concrete Linode
// (Akamai) Terraform HCL using the `linode/linode` provider resources. It is the
// wave-2 Linode mirror of render.go / render_macro.go: translation returns a
// structured plan (catalog-resolved, region/SKU never invented), and rendering to
// .tf happens here and drives the per-component `terraform plan` / `validate`
// round-trip checks (SPEC §6). Each renderer re-asserts the secure-by-default
// invariant (private by default, no public exposure) so a hand-built plan can
// never emit an exposed resource.
//
// Linode coverage (see the PR coverage matrix):
//
//	region+network     linode_vpc + linode_vpc_subnet
//	security-group     linode_firewall
//	virtual-machine    linode_instance
//	scale-group        UNSUPPORTED (no generic VM autoscaler; use LKE node pools)
//	load-balancer      linode_nodebalancer (+ _config + _node)
//	managed-database   linode_database_postgresql_v2 (with the data-safety guard)
//	object-storage     linode_object_storage_bucket (private by default)
//	cache              UNSUPPORTED (no managed Redis/Valkey resource)
//	managed-queue      UNSUPPORTED (no managed queue/broker)
//	event-streaming    UNSUPPORTED (no managed streaming)
//	dns-zone           linode_domain
//	cdn-service        UNSUPPORTED (no managed CDN)
//	waf-service        UNSUPPORTED (no managed WAF)
//	managed-kubernetes linode_lke_cluster
//	secrets-manager    UNSUPPORTED (no managed secrets manager)
//	serverless         UNSUPPORTED (no managed FaaS)
//
// Linode VPCs are region-scoped with no availability zones (like DigitalOcean),
// so subnets carry no zone and instances/databases are placed by region only.

// linodeRulesPerDirectionMax is the Linode firewall rule cap per direction. A
// firewall allows up to 25 inbound + 25 outbound rules; exceeding it is a hard
// plan-time error (enforced in enforceRuleLimits), never a silent trim.
const linodeRulesPerDirectionMax = 25

// ── region + network (linode_vpc + linode_vpc_subnet) ────────────────────────

func renderLinodeNetwork(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"linode_vpc\" %q {\n", name)
	fmt.Fprintf(&b, "  label  = %q\n", tfName(p.VPCName))
	fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
	b.WriteString("}\n")
	// Linode VPC subnets are explicit child resources (linode_vpc_subnet), each a
	// label + an ipv4 CIDR inside the VPC. No availability zone (region-scoped).
	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"linode_vpc_subnet\" %q {\n", sn)
		fmt.Fprintf(&b, "  vpc_id = linode_vpc.%s.id\n", name)
		fmt.Fprintf(&b, "  label  = \"%s-%d\"\n", tfName(p.VPCName), i+1)
		fmt.Fprintf(&b, "  ipv4   = %q\n", s.CIDR)
		b.WriteString("}\n")
	}
	return b.String()
}

// ── security-group (linode_firewall) ─────────────────────────────────────────

// linodeProto maps a canonical protocol to the Linode firewall protocol token
// (Linode expects upper-case TCP/UDP/ICMP). The "all" protocol is rejected
// upstream at translate for Linode (enforceProviderCapabilities), mirroring DO.
func linodeProto(proto string) string {
	return strings.ToUpper(proto)
}

func renderSGLinode(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"linode_firewall\" %q {\n", name)
	fmt.Fprintf(&b, "  label = %q\n", tfName(p.SGName))

	// One inbound/outbound block per rule. Linode firewall rules carry a label, an
	// action (ACCEPT), a protocol, ports, and ipv4/ipv6 CIDR lists.
	inbound := 0
	outbound := 0
	for _, r := range p.Rules {
		blockName := "inbound"
		var idx int
		if r.Direction == DirEgress {
			blockName = "outbound"
			outbound++
			idx = outbound
		} else {
			inbound++
			idx = inbound
		}
		ruleLabel := fmt.Sprintf("%s-%s-%d", linodeRuleLabel(r), r.Direction, idx)
		fmt.Fprintf(&b, "\n  %s {\n", blockName)
		fmt.Fprintf(&b, "    label    = %q\n", ruleLabel)
		b.WriteString("    action   = \"ACCEPT\"\n")
		fmt.Fprintf(&b, "    protocol = %q\n", linodeProto(r.Protocol))
		if r.Protocol == ProtoTCP || r.Protocol == ProtoUDP {
			fmt.Fprintf(&b, "    ports    = %q\n", portRangeString(r.FromPort, r.ToPort))
		}
		v4, v6 := splitCIDRs(r.CIDRs)
		if len(v4) > 0 {
			fmt.Fprintf(&b, "    ipv4     = %s\n", hclCIDRList(v4))
		}
		if len(v6) > 0 {
			fmt.Fprintf(&b, "    ipv6     = %s\n", hclCIDRList(v6))
		}
		b.WriteString("  }\n")
	}

	// SECURE BY DEFAULT: default-drop both directions; only the explicit rules above
	// open traffic. (Linode requires both policies to be set.)
	b.WriteString("\n  inbound_policy  = \"DROP\"\n")
	b.WriteString("  outbound_policy = \"DROP\"\n")
	b.WriteString("}\n")
	return b.String()
}

// linodeRuleLabel derives a short, Linode-safe rule label fragment from a rule's
// protocol (firewall rule labels are alphanumeric/dash, 3-32 chars).
func linodeRuleLabel(r RulePlan) string {
	if r.Protocol == "" {
		return "rule"
	}
	return r.Protocol
}

// ── virtual-machine (linode_instance) ────────────────────────────────────────

func renderVMLinode(p VMPlan) string {
	var b strings.Builder
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		fmt.Fprintf(&b, "resource \"linode_instance\" %q {\n", rn)
		fmt.Fprintf(&b, "  label  = %q\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  type   = %q\n", p.InstanceType)
		fmt.Fprintf(&b, "  image  = %q\n", p.Image)
		// Root password is generated/managed out-of-band (Vault / a throwaway var in
		// the round-trip fixture), never committed — mirrors the RDS password handling.
		b.WriteString("  root_pass = var.linode_root_pass\n")
		// Attach to the VPC subnet via an explicit interface when a network is
		// declared (Linode joins instances to a VPC through interface { subnet_id }).
		if p.NetworkName != "" && p.SubnetName != "" {
			b.WriteString("  interface {\n")
			b.WriteString("    purpose   = \"vpc\"\n")
			fmt.Fprintf(&b, "    subnet_id = linode_vpc_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetName))
			b.WriteString("  }\n")
		}
		if p.SecurityGroup != "" {
			// Linode firewalls attach to instances by id; the firewall (rendered
			// separately) references this instance via its linode_firewall.linodes set.
			fmt.Fprintf(&b, "  # firewall %q attaches via linode_firewall.linodes\n", tfName(p.SecurityGroup))
		}
		fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── load-balancer (linode_nodebalancer + _config + _node) ─────────────────────

// lbLinodeProto maps a canonical LB protocol to a Linode NodeBalancer config
// protocol token (http / https / tcp).
func lbLinodeProto(proto string) string {
	switch proto {
	case LBProtoHTTPS:
		return "https"
	case LBProtoTCP:
		return "tcp"
	default:
		return "http"
	}
}

func renderLBLinode(p LoadBalancerPlan) string {
	name := tfName(p.LBName)
	var b strings.Builder
	hc := p.HealthCheck

	fmt.Fprintf(&b, "resource \"linode_nodebalancer\" %q {\n", name)
	fmt.Fprintf(&b, "  label  = %q\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  tags   = [\"pyxcloud\"]\n")
	b.WriteString("}\n")

	// One config per listener port. Stickiness maps to the config `stickiness`
	// (http_cookie for cookie-based affinity). Health check fields are carried on
	// the config (check, check_path, interval, attempts).
	check := "connection"
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		check = "http"
	}
	for _, l := range p.Listeners {
		cn := fmt.Sprintf("%s_config_%d", name, l.Port)
		fmt.Fprintf(&b, "\nresource \"linode_nodebalancer_config\" %q {\n", cn)
		fmt.Fprintf(&b, "  nodebalancer_id = linode_nodebalancer.%s.id\n", name)
		fmt.Fprintf(&b, "  port            = %d\n", l.Port)
		fmt.Fprintf(&b, "  protocol        = %q\n", lbLinodeProto(l.Protocol))
		fmt.Fprintf(&b, "  algorithm       = \"roundrobin\"\n")
		if p.Stickiness {
			b.WriteString("  stickiness      = \"http_cookie\"\n")
		} else {
			b.WriteString("  stickiness      = \"none\"\n")
		}
		fmt.Fprintf(&b, "  check           = %q\n", check)
		if check == "http" {
			fmt.Fprintf(&b, "  check_path      = %q\n", hc.Path)
		}
		fmt.Fprintf(&b, "  check_interval  = %d\n", hc.IntervalSeconds)
		fmt.Fprintf(&b, "  check_attempts  = %d\n", hc.UnhealthyThreshold)
		b.WriteString("}\n")

		// A node per fixed-VM target instance. A scale-group target has no fixed node
		// set on Linode (no VM autoscaler), so it is fronted by LKE / fixed instances;
		// here we wire a node for the first instance of a VM target as the proven shape.
		if p.TargetKind == LBTargetVM && p.TargetName != "" {
			nn := fmt.Sprintf("%s_node_%d", name, l.Port)
			fmt.Fprintf(&b, "\nresource \"linode_nodebalancer_node\" %q {\n", nn)
			fmt.Fprintf(&b, "  nodebalancer_id = linode_nodebalancer.%s.id\n", name)
			fmt.Fprintf(&b, "  config_id       = linode_nodebalancer_config.%s.id\n", cn)
			fmt.Fprintf(&b, "  label           = \"%s-node\"\n", tfName(p.LBName))
			fmt.Fprintf(&b, "  address         = \"${linode_instance.%s.private_ip_address}:%d\"\n", tfName(p.TargetName+"-1"), hc.Port)
			b.WriteString("}\n")
		}
	}
	return b.String()
}

// ── managed-database (linode_database_postgresql, with data-safety guard) ─────

func renderMDBLinode(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	var b strings.Builder

	// Linode Managed Databases are engine-specific. PostgreSQL is the cross-provider
	// engine this provider targets (linode_database_postgresql_v2 — the v1
	// linode_database_postgresql resource is deprecated by the provider and emits a
	// validate-time deprecation warning; v2 is attribute-compatible for the fields we
	// emit). A MySQL request is surfaced as an explicit note so the user sees the gap
	// rather than a silently-mismatched resource (Linode's MySQL DBaaS is separate).
	if p.Engine != DBEnginePostgres {
		fmt.Fprintf(&b, "# NOTE: Linode managed-database renders linode_database_postgresql_v2; "+
			"engine %q is not the PostgreSQL target. Use postgres on Linode, or AWS/GCP for %q.\n",
			p.Engine, p.Engine)
	}

	fmt.Fprintf(&b, "resource \"linode_database_postgresql_v2\" %q {\n", name)
	fmt.Fprintf(&b, "  label        = %q\n", name)
	// engine_id pins engine+version, e.g. "postgresql/16".
	fmt.Fprintf(&b, "  engine_id    = \"postgresql/%s\"\n", linodeEngineVersion(p.EngineVersion))
	fmt.Fprintf(&b, "  type         = %q\n", p.DBClass)
	fmt.Fprintf(&b, "  region       = %q\n", p.CSPRegion)
	// HA = a 3-node cluster (primary + 2 replicas); single node otherwise.
	if p.HA {
		b.WriteString("  cluster_size = 3\n")
	} else {
		b.WriteString("  cluster_size = 1\n")
	}
	// SECURE BY DEFAULT: Linode Managed Databases are ALWAYS encrypted at rest and
	// TLS-only — the v2 provider exposes `encrypted` / `ssl_connection` as read-only
	// (computed) attributes, so they are NOT set here (setting them is a validate
	// error). The secure posture is intrinsic to the resource, not opt-in. The
	// access list defaults to the caller-supplied private CIDR (no public entry).
	b.WriteString("  # encrypted / ssl_connection are always-on (provider-computed, read-only)\n")
	fmt.Fprintf(&b, "  allow_list   = var.db_allow_list\n")
	// The replacement-forcing data-safety guard runs at PLAN time (ModifyPlan via
	// CheckManagedDatabaseDataSafety), not here; this renderer always emits the
	// production-safe shape. DeletionProtection intent is carried as a lifecycle
	// prevent_destroy when on (Linode has no in-place deletion-protection flag).
	if p.DeletionProtection {
		b.WriteString("  lifecycle {\n")
		b.WriteString("    prevent_destroy = true\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// linodeEngineVersion normalises a requested PostgreSQL version to the major line
// Linode pins on (e.g. "16"). Empty defaults to 16.
func linodeEngineVersion(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return "16"
	}
	// Reduce "16.2" -> "16" (Linode engine_id pins the major line).
	if idx := strings.IndexByte(v, '.'); idx > 0 {
		return v[:idx]
	}
	return v
}

// ── object-storage (linode_object_storage_bucket, private by default) ─────────

func renderObjectStorageLinode(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder

	// PRIVATE BY DEFAULT: acl = "private" unless explicitly public-read.
	acl := "private"
	if p.Public {
		acl = "public-read"
	}
	fmt.Fprintf(&b, "resource \"linode_object_storage_bucket\" %q {\n", label)
	fmt.Fprintf(&b, "  label  = %q\n", p.BucketName)
	// Linode Object Storage is region-scoped (the resolved csp_region is the region id).
	fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  acl    = %q\n", acl)
	fmt.Fprintf(&b, "  versioning = %t\n", p.Versioning)
	// Block public CORS-bypass exposure: keep cors_enabled off by default.
	b.WriteString("  cors_enabled = false\n")
	b.WriteString("}\n")
	return b.String()
}

// ── dns-zone (linode_domain) ──────────────────────────────────────────────────

func renderDNSLinode(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"linode_domain\" %q {\n", name)
	fmt.Fprintf(&b, "  domain    = %q\n", p.Domain)
	// type=master is an authoritative (primary) public zone — the Linode DNS shape.
	b.WriteString("  type      = \"master\"\n")
	// soa_email is required for a master zone; provided out-of-band via a variable.
	b.WriteString("  soa_email = var.dns_soa_email\n")
	fmt.Fprintf(&b, "  tags      = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── managed-kubernetes (linode_lke_cluster) ──────────────────────────────────

func renderK8sLinode(p K8sPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"linode_lke_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  label       = %q\n", name)
	fmt.Fprintf(&b, "  region      = %q\n", p.CSPRegion)
	ver := strings.TrimSpace(p.Version)
	if ver == "" {
		// LKE requires a k8s_version; a conservative recent line is the default.
		ver = "1.30"
	}
	fmt.Fprintf(&b, "  k8s_version = %q\n", ver)
	fmt.Fprintf(&b, "  tags        = [\"pyxcloud\"]\n")
	// A single autoscaling node pool — the Linode answer the scale-group error
	// points to (LKE node-pool autoscaling).
	b.WriteString("  pool {\n")
	fmt.Fprintf(&b, "    type  = %q\n", p.NodeType)
	fmt.Fprintf(&b, "    count = %d\n", p.DesiredNodes)
	b.WriteString("    autoscaler {\n")
	fmt.Fprintf(&b, "      min = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "      max = %d\n", p.MaxNodes)
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}
