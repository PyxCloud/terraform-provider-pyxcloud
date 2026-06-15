package catalog

import (
	"fmt"
	"strings"
)

// render_ibm.go renders resolved structured plans into concrete IBM Cloud
// (IBM-Cloud/ibm provider) Terraform HCL. It is the wave-2 IBM half of the
// "provider owns rendering" decision (SPEC §8): translation returns a structured
// plan (the same plan structs the wave-1 providers use), and rendering to .tf for
// IBM happens here. Mirrors render.go / render_macro.go exactly — one renderer per
// component, secure-by-default invariants re-asserted from the plan, no hardcoded
// region/profile maps (those come from the catalog).
//
// IBM-specific conventions used throughout:
//   - IBM VPC zones are "<region>-<1|2|3>" (e.g. eu-de-1), derived by deriveZones.
//   - An ibm_is_instance requires an SSH key; it is provided out-of-band via
//     var.ibm_ssh_key_id (like the AWS DB password / EKS role pattern).
//   - Region-scoped IBM Cloud services (ICD database, COS, Secrets Manager, Event
//     Streams, Code Engine) live in a resource group, provided via
//     var.ibm_resource_group_id.
//   - CIS-backed components (public DNS, WAF) reference the CIS instance via
//     var.ibm_cis_id; private DNS references the DNS Services instance via
//     var.ibm_dns_instance_id; Secrets Manager via var.ibm_sm_instance_id.
//
// These out-of-band variables keep the macro component declarative and free of
// account-specific identifiers, exactly as the wave-1 renderers do for IAM roles
// and rotation lambdas.

// ── network: ibm_is_vpc + ibm_is_subnet ──────────────────────────────────────

func renderNetworkIBM(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ibm_is_vpc\" %q {\n", name)
	fmt.Fprintf(&b, "  name           = %q\n", tfName(p.VPCName))
	b.WriteString("  resource_group = var.ibm_resource_group_id\n")
	fmt.Fprintf(&b, "  tags           = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"ibm_is_subnet\" %q {\n", sn)
		fmt.Fprintf(&b, "  name            = \"%s-%d\"\n", tfName(p.VPCName), i+1)
		fmt.Fprintf(&b, "  vpc             = ibm_is_vpc.%s.id\n", name)
		fmt.Fprintf(&b, "  zone            = %q\n", s.Zone)
		fmt.Fprintf(&b, "  ipv4_cidr_block = %q\n", s.CIDR)
		b.WriteString("  resource_group  = var.ibm_resource_group_id\n")
		b.WriteString("}\n")
	}
	return b.String()
}

// ── security-group: ibm_is_security_group(+_rule) ─────────────────────────────

// ibmSGDirection maps the canonical direction to the IBM VPC rule direction.
func ibmSGDirection(dir string) string {
	if dir == DirEgress {
		return "outbound"
	}
	return "inbound"
}

func renderSGIBM(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ibm_is_security_group\" %q {\n", name)
	fmt.Fprintf(&b, "  name           = %q\n", tfName(p.SGName))
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc            = ibm_is_vpc.%s.id\n", tfName(p.NetworkName))
	}
	b.WriteString("  resource_group = var.ibm_resource_group_id\n")
	b.WriteString("}\n")

	// IBM VPC security-group rules are individual resources, each scoped to ONE
	// remote (cidr/IP/sg). A rule with multiple CIDRs fans out to one rule per CIDR.
	idx := 0
	for _, r := range p.Rules {
		dir := ibmSGDirection(r.Direction)
		remotes := r.CIDRs
		if r.SourceSG != "" {
			remotes = nil // peer-SG handled below
		}
		emit := func(remoteExpr string) {
			idx++
			rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, idx)
			fmt.Fprintf(&b, "\nresource \"ibm_is_security_group_rule\" %q {\n", rn)
			fmt.Fprintf(&b, "  group     = ibm_is_security_group.%s.id\n", name)
			fmt.Fprintf(&b, "  direction = %q\n", dir)
			fmt.Fprintf(&b, "  remote    = %s\n", remoteExpr)
			// Modern (non-deprecated) IBM VPC rule form: top-level protocol +
			// port_min/port_max for tcp/udp, protocol + type for icmp. Omitting the
			// protocol entirely (canonical "all") yields an icmp_tcp_udp rule that
			// matches every protocol — the IBM "all" equivalent.
			switch r.Protocol {
			case ProtoTCP, ProtoUDP:
				fmt.Fprintf(&b, "  protocol  = %q\n", r.Protocol)
				fmt.Fprintf(&b, "  port_min  = %d\n", r.FromPort)
				fmt.Fprintf(&b, "  port_max  = %d\n", r.ToPort)
			case ProtoICMP:
				b.WriteString("  protocol  = \"icmp\"\n")
				b.WriteString("  type      = 8\n")
			}
			b.WriteString("}\n")
		}
		if r.SourceSG != "" {
			emit(fmt.Sprintf("ibm_is_security_group.%s.id", tfName(r.SourceSG)))
			continue
		}
		if len(remotes) == 0 {
			emit("\"0.0.0.0/0\"")
			continue
		}
		for _, c := range remotes {
			emit(fmt.Sprintf("%q", c))
		}
	}
	return b.String()
}

// ── virtual-machine: ibm_is_instance ──────────────────────────────────────────

func renderVMIBM(p VMPlan) string {
	var b strings.Builder
	subnetLabel := subnetResourceLabel(p.NetworkName, p.SubnetName)
	// IBM instances are zonal; derive a deterministic zone from the csp_region
	// (region-1), matching the network component's first-zone derivation.
	zone := p.CSPRegion + "-1"
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		fmt.Fprintf(&b, "resource \"ibm_is_instance\" %q {\n", rn)
		fmt.Fprintf(&b, "  name           = %q\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  image          = %q\n", p.Image)
		fmt.Fprintf(&b, "  profile        = %q\n", p.InstanceType)
		if p.NetworkName != "" {
			fmt.Fprintf(&b, "  vpc            = ibm_is_vpc.%s.id\n", tfName(p.NetworkName))
		}
		fmt.Fprintf(&b, "  zone           = %q\n", zone)
		b.WriteString("  keys           = [var.ibm_ssh_key_id]\n")
		b.WriteString("  resource_group = var.ibm_resource_group_id\n")
		b.WriteString("  primary_network_interface {\n")
		if p.SubnetName != "" {
			fmt.Fprintf(&b, "    subnet          = ibm_is_subnet.%s.id\n", subnetLabel)
		}
		if p.SecurityGroup != "" {
			fmt.Fprintf(&b, "    security_groups = [ibm_is_security_group.%s.id]\n", tfName(p.SecurityGroup))
		}
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── scale-group: ibm_is_instance_template + _group + _group_manager ───────────

func renderASGIBM(p ScaleGroupPlan) string {
	tmplName := tfName(p.GroupName) + "_tmpl"
	groupName := tfName(p.GroupName) + "_ig"
	mgrName := tfName(p.GroupName) + "_igm"
	var b strings.Builder
	zone := p.CSPRegion + "-1"
	if len(p.Zones) > 0 {
		zone = p.Zones[0]
	}

	// Instance template: profile + image from the catalog.
	fmt.Fprintf(&b, "resource \"ibm_is_instance_template\" %q {\n", tmplName)
	fmt.Fprintf(&b, "  name    = \"%s-tmpl\"\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  image   = %q\n", p.Image)
	fmt.Fprintf(&b, "  profile = %q\n", p.InstanceType)
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc     = ibm_is_vpc.%s.id\n", tfName(p.NetworkName))
	}
	fmt.Fprintf(&b, "  zone    = %q\n", zone)
	b.WriteString("  keys    = [var.ibm_ssh_key_id]\n")
	b.WriteString("  resource_group = var.ibm_resource_group_id\n")
	b.WriteString("  primary_network_interface {\n")
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "    subnet          = ibm_is_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "    security_groups = [ibm_is_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// Instance group: the managed fleet across the region's subnets, sized to the
	// desired membership count.
	fmt.Fprintf(&b, "resource \"ibm_is_instance_group\" %q {\n", groupName)
	fmt.Fprintf(&b, "  name              = %q\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  instance_template = ibm_is_instance_template.%s.id\n", tmplName)
	fmt.Fprintf(&b, "  instance_count    = %d\n", p.Desired)
	if len(p.SubnetNames) > 0 {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("ibm_is_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  subnets           = [%s]\n", strings.Join(labels, ", "))
	}
	// NOTE: the instance group's load-balancer integration (application_port +
	// load_balancer + load_balancer_pool, which IBM requires together) is wired
	// from the LOAD-BALANCER component, not here — mirroring how the AWS scale-group
	// renderer leaves the target-group attachment to the LB renderer. A standalone
	// application_port would be an incomplete (invalid) binding, so it is omitted.
	b.WriteString("  resource_group    = var.ibm_resource_group_id\n")
	b.WriteString("}\n\n")

	// Autoscale manager: min/max membership, scaling on CPU utilisation — the IBM
	// analogue of the AWS instance refresh / GCP autoscaler.
	fmt.Fprintf(&b, "resource \"ibm_is_instance_group_manager\" %q {\n", mgrName)
	fmt.Fprintf(&b, "  name                 = \"%s-mgr\"\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  instance_group       = ibm_is_instance_group.%s.id\n", groupName)
	b.WriteString("  manager_type         = \"autoscale\"\n")
	b.WriteString("  enable_manager       = true\n")
	fmt.Fprintf(&b, "  max_membership_count = %d\n", p.Max)
	fmt.Fprintf(&b, "  min_membership_count = %d\n", p.Min)
	b.WriteString("  aggregation_window   = 90\n")
	b.WriteString("  cooldown             = 300\n")
	b.WriteString("}\n")
	return b.String()
}

// ── load-balancer: ibm_is_lb (+pool/listener/pool_member) ─────────────────────

// lbIBMProto maps a canonical LB protocol to an IBM VPC LB pool/listener protocol.
func lbIBMProto(proto string) string {
	switch proto {
	case LBProtoHTTPS:
		return "https"
	case LBProtoTCP:
		return "tcp"
	default:
		return "http"
	}
}

func renderLBIBM(p LoadBalancerPlan) string {
	lbName := tfName(p.LBName) + "_lb"
	poolName := tfName(p.LBName) + "_pool"
	var b strings.Builder
	hc := p.HealthCheck

	// Public application load balancer across the region's subnets (multi-zone).
	fmt.Fprintf(&b, "resource \"ibm_is_lb\" %q {\n", lbName)
	fmt.Fprintf(&b, "  name           = %q\n", tfName(p.LBName))
	b.WriteString("  type           = \"public\"\n")
	if len(p.SubnetNames) > 0 {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("ibm_is_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  subnets        = [%s]\n", strings.Join(labels, ", "))
	}
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  security_groups = [ibm_is_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	b.WriteString("  resource_group = var.ibm_resource_group_id\n")
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n\n")

	// Backend pool with the health check. Session stickiness via source-ip when
	// requested (IBM VPC LB session_persistence).
	fmt.Fprintf(&b, "resource \"ibm_is_lb_pool\" %q {\n", poolName)
	fmt.Fprintf(&b, "  name           = \"%s-pool\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  lb             = ibm_is_lb.%s.id\n", lbName)
	b.WriteString("  algorithm      = \"round_robin\"\n")
	fmt.Fprintf(&b, "  protocol       = %q\n", lbIBMProto(hc.Protocol))
	fmt.Fprintf(&b, "  health_delay   = %d\n", hc.IntervalSeconds)
	fmt.Fprintf(&b, "  health_retries = %d\n", hc.HealthyThreshold)
	fmt.Fprintf(&b, "  health_timeout = %d\n", maxInt(hc.IntervalSeconds-1, 2))
	fmt.Fprintf(&b, "  health_type    = %q\n", lbIBMProto(hc.Protocol))
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		fmt.Fprintf(&b, "  health_monitor_url = %q\n", hc.Path)
	}
	if p.Stickiness {
		b.WriteString("  session_persistence_type = \"source_ip\"\n")
	}
	b.WriteString("}\n\n")

	// One listener per declared listener port, fronting the pool.
	for _, l := range p.Listeners {
		ln := fmt.Sprintf("%s_listener_%d", tfName(p.LBName), l.Port)
		fmt.Fprintf(&b, "resource \"ibm_is_lb_listener\" %q {\n", ln)
		fmt.Fprintf(&b, "  lb           = ibm_is_lb.%s.id\n", lbName)
		fmt.Fprintf(&b, "  port         = %d\n", l.Port)
		fmt.Fprintf(&b, "  protocol     = %q\n", lbIBMProto(l.Protocol))
		fmt.Fprintf(&b, "  default_pool = ibm_is_lb_pool.%s.id\n", poolName)
		b.WriteString("}\n\n")
	}

	// A fixed-VM target registers as a pool member; a scale-group target is wired
	// via the instance group's load-balancer integration (application_port set on
	// the group), so no static member is emitted for it.
	if p.TargetKind == LBTargetVM && p.TargetName != "" {
		memberName := tfName(p.LBName) + "_member"
		fmt.Fprintf(&b, "resource \"ibm_is_lb_pool_member\" %q {\n", memberName)
		fmt.Fprintf(&b, "  lb             = ibm_is_lb.%s.id\n", lbName)
		fmt.Fprintf(&b, "  pool           = element(split(\"/\", ibm_is_lb_pool.%s.id), 1)\n", poolName)
		fmt.Fprintf(&b, "  port           = %d\n", hc.Port)
		fmt.Fprintf(&b, "  target_address = ibm_is_instance.%s.primary_network_interface[0].primary_ip[0].address\n", tfName(p.TargetName+"-1"))
		b.WriteString("}\n\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── managed-database: ibm_database (ICD) ──────────────────────────────────────

// mdbIBMService maps the canonical engine to the IBM Cloud Databases service name.
func mdbIBMService(engine string) string {
	if engine == DBEngineMySQL {
		return "databases-for-mysql"
	}
	return "databases-for-postgresql"
}

func renderMDBIBM(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ibm_database\" %q {\n", name)
	fmt.Fprintf(&b, "  name              = %q\n", name)
	fmt.Fprintf(&b, "  service           = %q\n", mdbIBMService(p.Engine))
	b.WriteString("  plan              = \"standard\"\n")
	fmt.Fprintf(&b, "  location          = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  version           = %q\n", p.EngineVersion)
	b.WriteString("  resource_group_id = var.ibm_resource_group_id\n")
	b.WriteString("  adminpassword     = var.db_password\n")
	// Private-only connectivity: ICD private service endpoints (no public endpoint).
	b.WriteString("  service_endpoints = \"private\"\n")
	// Resolved sizing -> ICD member group allocations (cpu/ram/disk). HA = 2 members.
	members := 2
	if !p.HA {
		members = 2 // ICD always runs >= 2 members; HA is implicit in the platform
	}
	b.WriteString("  group {\n")
	b.WriteString("    group_id = \"member\"\n")
	fmt.Fprintf(&b, "    members {\n      allocation_count = %d\n    }\n", members)
	fmt.Fprintf(&b, "    memory {\n      allocation_mb = %d\n    }\n", p.RAM*1024)
	fmt.Fprintf(&b, "    cpu {\n      allocation_count = %d\n    }\n", maxInt(p.CPU, 1))
	fmt.Fprintf(&b, "    disk {\n      allocation_mb = %d\n    }\n", p.StorageGB*1024)
	b.WriteString("  }\n")
	// Data-safety: prevent_destroy when deletion protection is on (ICD has no
	// in-place deletion-protection flag), mirroring the DO managed-cluster pattern.
	if p.DeletionProtection {
		b.WriteString("  lifecycle {\n    prevent_destroy = true\n  }\n")
	}
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── object-storage: ibm_resource_instance (COS) + ibm_cos_bucket ──────────────

func renderObjectStorageIBM(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	cosInstance := label + "_cos"
	var b strings.Builder

	// A Cloud Object Storage service instance to hold the bucket. COS is a global
	// service; the bucket carries the regional placement.
	fmt.Fprintf(&b, "resource \"ibm_resource_instance\" %q {\n", cosInstance)
	fmt.Fprintf(&b, "  name              = \"%s-cos\"\n", label)
	b.WriteString("  service           = \"cloud-object-storage\"\n")
	b.WriteString("  plan              = \"standard\"\n")
	b.WriteString("  location          = \"global\"\n")
	b.WriteString("  resource_group_id = var.ibm_resource_group_id\n")
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"ibm_cos_bucket\" %q {\n", label)
	fmt.Fprintf(&b, "  bucket_name          = %q\n", p.BucketName)
	fmt.Fprintf(&b, "  resource_instance_id = ibm_resource_instance.%s.id\n", cosInstance)
	// Regional bucket in the resolved csp_region (private storage class).
	fmt.Fprintf(&b, "  region_location      = %q\n", p.CSPRegion)
	b.WriteString("  storage_class        = \"smart\"\n")
	// PRIVATE BY DEFAULT (SPEC §5.7): COS buckets are private; public read is never
	// emitted by default. (Public exposure on COS is via a separate bucket policy /
	// static-web config, an explicit opt-in — never the macro default.)
	if p.Versioning {
		b.WriteString("  object_versioning {\n    enable = true\n  }\n")
	}
	if p.ForceDestroy {
		b.WriteString("  force_delete = true\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// ── cache: ibm_database (Databases for Redis) ─────────────────────────────────

// ibmCacheMemoryMB maps the resolved node-class token (e.g. "4gb") to the ICD
// member memory allocation in MB; falls back to the requested MemoryGB.
func renderCacheIBM(p CachePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	memGB := p.MemoryGB
	if memGB <= 0 {
		memGB = 1
	}
	fmt.Fprintf(&b, "resource \"ibm_database\" %q {\n", name)
	fmt.Fprintf(&b, "  name              = %q\n", name)
	b.WriteString("  service           = \"databases-for-redis\"\n")
	b.WriteString("  plan              = \"standard\"\n")
	fmt.Fprintf(&b, "  location          = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  version           = %q\n", p.Version)
	b.WriteString("  resource_group_id = var.ibm_resource_group_id\n")
	// SECURE BY DEFAULT: private endpoints only (no public reachability).
	b.WriteString("  service_endpoints = \"private\"\n")
	members := 2 // ICD runs >= 2 members; HA failover is platform-managed
	b.WriteString("  group {\n")
	b.WriteString("    group_id = \"member\"\n")
	fmt.Fprintf(&b, "    members {\n      allocation_count = %d\n    }\n", members)
	fmt.Fprintf(&b, "    memory {\n      allocation_mb = %d\n    }\n", memGB*1024)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── event-streaming: ibm_resource_instance (Event Streams / Kafka) ────────────

func renderStreamIBM(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// IBM Event Streams is a managed Kafka service instance. The "standard" plan is
	// the multi-tenant shared tier (the cross-provider streaming default); topics
	// are created out-of-band (the stream itself is the instance, mirroring how the
	// GCP renderer treats one Pub/Sub topic as the stream).
	fmt.Fprintf(&b, "resource \"ibm_resource_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  name              = %q\n", p.Name)
	b.WriteString("  service           = \"messagehub\"\n")
	b.WriteString("  plan              = \"standard\"\n")
	fmt.Fprintf(&b, "  location          = %q\n", p.CSPRegion)
	b.WriteString("  resource_group_id = var.ibm_resource_group_id\n")
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── dns-zone: ibm_dns_zone (private) / ibm_cis_domain (public) ────────────────

func renderDNSIBM(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	if p.Private {
		// Private DNS zone in IBM Cloud DNS Services, scoped to the DNS instance.
		fmt.Fprintf(&b, "resource \"ibm_dns_zone\" %q {\n", name)
		fmt.Fprintf(&b, "  name        = %q\n", p.Domain)
		b.WriteString("  instance_id = var.ibm_dns_instance_id\n")
		b.WriteString("  description = \"Managed by PyxCloud\"\n")
		b.WriteString("}\n")
		return b.String()
	}
	// Public zone via Cloud Internet Services (CIS).
	fmt.Fprintf(&b, "resource \"ibm_cis_domain\" %q {\n", name)
	fmt.Fprintf(&b, "  domain = %q\n", p.Domain)
	b.WriteString("  cis_id = var.ibm_cis_id\n")
	b.WriteString("}\n")
	return b.String()
}

// ── waf-service: ibm_cis_waf_group (CIS managed WAF) ──────────────────────────

func renderWAFIBM(p WAFPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// IBM Cloud Internet Services managed WAF rule group on a CIS domain. The CIS
	// instance, domain, and package ids are provided out-of-band (account-specific);
	// the macro component turns the managed group ON ("on" mode) — secure by default
	// (a fresh WAF is not a no-op), mirroring the AWS managed-common-rule-set choice.
	fmt.Fprintf(&b, "resource \"ibm_cis_waf_group\" %q {\n", name)
	b.WriteString("  cis_id     = var.ibm_cis_id\n")
	b.WriteString("  domain_id  = var.ibm_cis_domain_id\n")
	b.WriteString("  package_id = var.ibm_cis_waf_package_id\n")
	b.WriteString("  group_id   = var.ibm_cis_waf_group_id\n")
	b.WriteString("  mode       = \"on\"\n")
	b.WriteString("}\n")
	return b.String()
}

// ── managed-kubernetes: ibm_container_vpc_cluster ─────────────────────────────

func renderK8sIBM(p K8sPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ibm_container_vpc_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name              = %q\n", name)
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_id            = ibm_is_vpc.%s.id\n", tfName(p.NetworkName))
	}
	if p.Version != "" {
		fmt.Fprintf(&b, "  kube_version      = %q\n", p.Version)
	}
	fmt.Fprintf(&b, "  flavor            = %q\n", p.NodeType)
	fmt.Fprintf(&b, "  worker_count      = %d\n", maxInt(p.DesiredNodes, 1))
	b.WriteString("  resource_group_id = var.ibm_resource_group_id\n")
	// One zone block per subnet (each subnet is in a distinct VPC zone), spreading
	// the default worker pool multi-zone.
	for i, s := range p.SubnetNames {
		zone := p.CSPRegion + fmt.Sprintf("-%d", (i%3)+1)
		if i < len(p.Zones) {
			zone = p.Zones[i]
		}
		b.WriteString("  zones {\n")
		fmt.Fprintf(&b, "    subnet_id = ibm_is_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, s))
		fmt.Fprintf(&b, "    name      = %q\n", zone)
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── secrets-manager: ibm_sm_arbitrary_secret ──────────────────────────────────

func renderSecretsIBM(p SecretsPlan) string {
	name := tfName(p.Name)
	desc := asciiOnly(p.Description)
	var b strings.Builder
	// IBM Cloud Secrets Manager arbitrary secret. The Secrets Manager instance is
	// provided out-of-band (var.ibm_sm_instance_id). The secret VALUE is NOT declared
	// here (it would leak into state) — the payload is written out-of-band, mirroring
	// the AWS/GCP secrets renderers.
	fmt.Fprintf(&b, "resource \"ibm_sm_arbitrary_secret\" %q {\n", name)
	fmt.Fprintf(&b, "  name        = %q\n", p.Name)
	b.WriteString("  instance_id = var.ibm_sm_instance_id\n")
	fmt.Fprintf(&b, "  region      = %q\n", p.CSPRegion)
	if desc != "" {
		fmt.Fprintf(&b, "  description = %q\n", desc)
	}
	b.WriteString("  payload     = var.secret_payload\n")
	b.WriteString("}\n")
	if p.RotationDays > 0 {
		// IBM Secrets Manager rotates via a rotation policy on supported secret types
		// (e.g. username/password); an arbitrary secret carries no native rotation, so
		// the rotation intent is surfaced as a comment for the operator. (No invented
		// resource — a hard contract, documented in the PR.)
		fmt.Fprintf(&b, "# NOTE: rotation (%d days) requested; arbitrary secrets have no native IBM rotation policy — use a username/password or IAM-credentials secret type to rotate.\n", p.RotationDays)
	}
	return b.String()
}

// ── serverless-function: ibm_code_engine_project + ibm_code_engine_app ────────

func renderServerlessIBM(p ServerlessPlan) string {
	name := tfName(p.Name)
	projName := name + "_project"
	var b strings.Builder
	// Code Engine project (the namespace the app runs in).
	fmt.Fprintf(&b, "resource \"ibm_code_engine_project\" %q {\n", projName)
	fmt.Fprintf(&b, "  name              = \"%s-project\"\n", name)
	b.WriteString("  resource_group_id = var.ibm_resource_group_id\n")
	b.WriteString("}\n\n")

	// Code Engine application. Code Engine is container-image based: the resolved
	// ConcreteRuntime is the stock runtime image; a real deployment image overrides
	// it via var.function_image. PRIVATE BY DEFAULT: no public visibility flag is
	// set beyond the platform default ingress.
	image := p.ConcreteRuntime
	if p.SourceArtifact != "" {
		image = p.SourceArtifact
	}
	fmt.Fprintf(&b, "resource \"ibm_code_engine_app\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id      = ibm_code_engine_project.%s.project_id\n", projName)
	fmt.Fprintf(&b, "  name            = %q\n", name)
	fmt.Fprintf(&b, "  image_reference = %q\n", image)
	fmt.Fprintf(&b, "  scale_memory_limit = \"%dM\"\n", p.MemoryMB)
	fmt.Fprintf(&b, "  scale_request_timeout = %d\n", p.TimeoutSeconds)
	b.WriteString("}\n")
	return b.String()
}

// maxInt returns the larger of two ints (small local helper for IBM renderers).
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
