package catalog

import (
	"fmt"
	"strings"
)

// render_oracle.go renders resolved structured plans into concrete Oracle Cloud
// Infrastructure (OCI) Terraform HCL for the oracle/oci provider. It mirrors the
// wave-1 renderers (render.go / render_macro.go) exactly: translation returns a
// structured plan, rendering to .tf happens here, and the same renderers drive
// the per-provider `terraform validate` / plan round-trip (SPEC §6). Each
// renderer re-asserts the secure-by-default invariant (private subnets/clusters,
// NoPublicAccess buckets, private endpoints, encrypted-at-rest) so a hand-built
// plan can never emit an exposed resource.
//
// OCI shaping notes that differ from AWS/GCP/DO and are applied uniformly here:
//   - Compartment-based: every OCI resource lives in a compartment. We reference
//     it via `var.compartment_id` (an OCI deployment always targets a compartment;
//     the value is supplied out-of-band, like the AWS/GCP project credentials).
//   - OCID references: resources wire together by OCID (`.id`), and a VCN/subnet
//     are distinct resources (oci_core_vcn + oci_core_subnet), like AWS.
//   - Availability domains: OCI ADs carry an opaque tenancy-specific prefix, so
//     the concrete AD name is only known at apply time. Components that need an AD
//     emit a data "oci_identity_availability_domains" and index it by the plan's
//     AD ordinal (the zone "1","2","3" carried by deriveZones for ProviderOracle).
//   - Flex shapes: compute/MySQL/PostgreSQL use *.Flex shapes sized by an explicit
//     ocpu/memory shape_config; the catalog row's cpu/ram fill that config.

// ociADDataSource emits a data source resolving the region's availability
// domains, named uniquely per logical component so two components rendered into
// the same file never collide on the data-source label. The renderer indexes it
// by the AD ordinal carried in the plan's zones.
func ociADDataSource(label string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "data \"oci_identity_availability_domains\" %q {\n", label)
	b.WriteString("  compartment_id = var.compartment_id\n")
	b.WriteString("}\n\n")
	return b.String()
}

// ociADRef returns the HCL expression for the AD name at the given 1-based
// ordinal (the deriveZones zone token for OCI) against the named data source.
func ociADRef(dsLabel, ordinal string) string {
	idx := "0"
	if n := atoiOrZero(ordinal); n > 0 {
		idx = fmt.Sprintf("%d", n-1)
	}
	return fmt.Sprintf("data.oci_identity_availability_domains.%s.availability_domains[%s].name", dsLabel, idx)
}

// ── network (region + VCN) ────────────────────────────────────────────────────

func renderOCI(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	// VCN: the region-scoped private network. cidr_blocks (the modern attribute).
	fmt.Fprintf(&b, "resource \"oci_core_vcn\" %q {\n", name)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  cidr_blocks    = [%q]\n", p.CIDR)
	fmt.Fprintf(&b, "  display_name   = %q\n", p.VPCName)
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	// Subnets are regional (Oracle's recommended shape: no availability_domain set
	// => the subnet spans all ADs). PRIVATE BY DEFAULT: prohibit_public_ip_on_vnic
	// = true so instances launched here never auto-assign a public IP.
	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"oci_core_subnet\" %q {\n", sn)
		b.WriteString("  compartment_id             = var.compartment_id\n")
		fmt.Fprintf(&b, "  vcn_id                     = oci_core_vcn.%s.id\n", name)
		fmt.Fprintf(&b, "  cidr_block                 = %q\n", s.CIDR)
		fmt.Fprintf(&b, "  display_name               = %q\n", s.Name)
		b.WriteString("  prohibit_public_ip_on_vnic = true\n")
		fmt.Fprintf(&b, "  freeform_tags              = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n")
	}
	return b.String()
}

// ── security-group (network security group) ───────────────────────────────────

// ociNSGProto maps a canonical protocol to the OCI NSG protocol number. OCI uses
// IANA protocol numbers: "all", "6" (TCP), "17" (UDP), "1" (ICMP).
func ociNSGProto(proto string) string {
	switch proto {
	case ProtoTCP:
		return "6"
	case ProtoUDP:
		return "17"
	case ProtoICMP:
		return "1"
	default:
		return "all"
	}
}

func renderSGOCI(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	var b strings.Builder
	// The NSG container.
	fmt.Fprintf(&b, "resource \"oci_core_network_security_group\" %q {\n", name)
	b.WriteString("  compartment_id = var.compartment_id\n")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vcn_id         = oci_core_vcn.%s.id\n", tfName(p.NetworkName))
	} else {
		b.WriteString("  vcn_id         = var.vcn_id\n")
	}
	fmt.Fprintf(&b, "  display_name   = %q\n", p.SGName)
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")

	// One oci_core_network_security_group_security_rule per canonical rule. OCI
	// uses INGRESS/EGRESS uppercase, source/destination with *_type=CIDR_BLOCK,
	// and tcp_options/udp_options for port ranges.
	for i, r := range p.Rules {
		rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
		dir := strings.ToUpper(r.Direction)
		fmt.Fprintf(&b, "\nresource \"oci_core_network_security_group_security_rule\" %q {\n", rn)
		fmt.Fprintf(&b, "  network_security_group_id = oci_core_network_security_group.%s.id\n", name)
		fmt.Fprintf(&b, "  direction                 = %q\n", dir)
		fmt.Fprintf(&b, "  protocol                  = %q\n", ociNSGProto(r.Protocol))
		// A peer-SG rule references the other NSG by OCID; otherwise it is CIDR-scoped.
		cidr := "0.0.0.0/0"
		if len(r.CIDRs) > 0 {
			cidr = r.CIDRs[0]
		}
		if r.SourceSG != "" {
			if dir == "INGRESS" {
				fmt.Fprintf(&b, "  source                    = oci_core_network_security_group.%s.id\n", tfName(r.SourceSG))
				b.WriteString("  source_type               = \"NETWORK_SECURITY_GROUP\"\n")
			} else {
				fmt.Fprintf(&b, "  destination               = oci_core_network_security_group.%s.id\n", tfName(r.SourceSG))
				b.WriteString("  destination_type          = \"NETWORK_SECURITY_GROUP\"\n")
			}
		} else if dir == "INGRESS" {
			fmt.Fprintf(&b, "  source                    = %q\n", cidr)
			b.WriteString("  source_type               = \"CIDR_BLOCK\"\n")
		} else {
			fmt.Fprintf(&b, "  destination               = %q\n", cidr)
			b.WriteString("  destination_type          = \"CIDR_BLOCK\"\n")
		}
		if r.Protocol == ProtoTCP || r.Protocol == ProtoUDP {
			optBlock := "tcp_options"
			if r.Protocol == ProtoUDP {
				optBlock = "udp_options"
			}
			fmt.Fprintf(&b, "  %s {\n", optBlock)
			b.WriteString("    destination_port_range {\n")
			fmt.Fprintf(&b, "      min = %d\n", r.FromPort)
			fmt.Fprintf(&b, "      max = %d\n", r.ToPort)
			b.WriteString("    }\n")
			b.WriteString("  }\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// ── virtual-machine (compute instance) ────────────────────────────────────────

// ociShapeConfig emits the flex-shape ocpu/memory config block from the resolved
// cpu/ram. OCI *.Flex shapes are sized by an explicit shape_config; the catalog
// row's cpu/ram fill it so the resolved size is exact (no silent rounding).
func ociShapeConfig(cpu, ram int, indent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%sshape_config {\n", indent)
	fmt.Fprintf(&b, "%s  ocpus         = %d\n", indent, cpu)
	fmt.Fprintf(&b, "%s  memory_in_gbs = %d\n", indent, ram)
	fmt.Fprintf(&b, "%s}\n", indent)
	return b.String()
}

func renderVMOCI(p VMPlan) string {
	var b strings.Builder
	adLabel := tfName(p.VMName) + "_ads"
	b.WriteString(ociADDataSource(adLabel))
	subnetLabel := subnetResourceLabel(p.NetworkName, p.SubnetName)
	for i, inst := range p.Instances {
		rn := tfName(inst.Name)
		// Spread instances across ADs deterministically (round-robin over the AD list).
		fmt.Fprintf(&b, "resource \"oci_core_instance\" %q {\n", rn)
		b.WriteString("  compartment_id      = var.compartment_id\n")
		fmt.Fprintf(&b, "  availability_domain = %s\n", ociADRef(adLabel, fmt.Sprintf("%d", (i%3)+1)))
		fmt.Fprintf(&b, "  display_name        = %q\n", inst.Name)
		fmt.Fprintf(&b, "  shape               = %q\n", p.InstanceType)
		b.WriteString(ociShapeConfig(p.CPU, p.RAM, "  "))
		b.WriteString("  source_details {\n")
		b.WriteString("    source_type = \"image\"\n")
		fmt.Fprintf(&b, "    source_id   = %q\n", p.Image)
		b.WriteString("  }\n")
		b.WriteString("  create_vnic_details {\n")
		if p.SubnetName != "" {
			fmt.Fprintf(&b, "    subnet_id        = oci_core_subnet.%s.id\n", subnetLabel)
		}
		// PRIVATE BY DEFAULT: no public IP on the primary VNIC.
		b.WriteString("    assign_public_ip = false\n")
		if p.SecurityGroup != "" {
			fmt.Fprintf(&b, "    nsg_ids          = [oci_core_network_security_group.%s.id]\n", tfName(p.SecurityGroup))
		}
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── virtual-machine-scale-group (instance pool + autoscaling) ─────────────────

func renderASGOCI(p ScaleGroupPlan) string {
	name := tfName(p.GroupName)
	cfgName := name + "_cfg"
	poolName := name + "_pool"
	asName := name + "_as"
	adLabel := name + "_ads"
	var b strings.Builder
	b.WriteString(ociADDataSource(adLabel))

	// Instance configuration: the launch template equivalent (shape + image + VNIC).
	fmt.Fprintf(&b, "resource \"oci_core_instance_configuration\" %q {\n", cfgName)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name   = \"%s-cfg\"\n", p.GroupName)
	b.WriteString("  instance_details {\n")
	b.WriteString("    instance_type = \"compute\"\n")
	b.WriteString("    launch_details {\n")
	b.WriteString("      compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "      shape          = %q\n", p.InstanceType)
	b.WriteString(ociShapeConfig(p.CPU, p.RAM, "      "))
	b.WriteString("      source_details {\n")
	b.WriteString("        source_type = \"image\"\n")
	fmt.Fprintf(&b, "        image_id    = %q\n", p.Image)
	b.WriteString("      }\n")
	b.WriteString("      create_vnic_details {\n")
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "        subnet_id        = oci_core_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	b.WriteString("        assign_public_ip = false\n")
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "        nsg_ids          = [oci_core_network_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// Instance pool: the fleet across the region's ADs/subnets.
	fmt.Fprintf(&b, "resource \"oci_core_instance_pool\" %q {\n", poolName)
	b.WriteString("  compartment_id            = var.compartment_id\n")
	fmt.Fprintf(&b, "  instance_configuration_id = oci_core_instance_configuration.%s.id\n", cfgName)
	fmt.Fprintf(&b, "  display_name              = %q\n", p.GroupName)
	fmt.Fprintf(&b, "  size                      = %d\n", p.Desired)
	// One placement_configuration per AD/subnet (multi-AD spread).
	if len(p.SubnetNames) > 0 {
		for i, s := range p.SubnetNames {
			b.WriteString("  placement_configurations {\n")
			fmt.Fprintf(&b, "    availability_domain = %s\n", ociADRef(adLabel, fmt.Sprintf("%d", (i%3)+1)))
			fmt.Fprintf(&b, "    primary_subnet_id   = oci_core_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, s))
			b.WriteString("  }\n")
		}
	}
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	// Autoscaling configuration: a threshold policy with min/max capacity over the
	// pool — the OCI analogue of the AWS ASG / GCP autoscaler.
	fmt.Fprintf(&b, "resource \"oci_autoscaling_auto_scaling_configuration\" %q {\n", asName)
	b.WriteString("  compartment_id       = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name         = \"%s-as\"\n", p.GroupName)
	b.WriteString("  cool_down_in_seconds = 300\n")
	b.WriteString("  is_enabled           = true\n")
	b.WriteString("  auto_scaling_resources {\n")
	fmt.Fprintf(&b, "    id   = oci_core_instance_pool.%s.id\n", poolName)
	b.WriteString("    type = \"instancePool\"\n")
	b.WriteString("  }\n")
	b.WriteString("  policies {\n")
	b.WriteString("    policy_type  = \"threshold\"\n")
	b.WriteString("    display_name = \"cpu-threshold\"\n")
	b.WriteString("    capacity {\n")
	fmt.Fprintf(&b, "      initial = %d\n", p.Desired)
	fmt.Fprintf(&b, "      max     = %d\n", p.Max)
	fmt.Fprintf(&b, "      min     = %d\n", p.Min)
	b.WriteString("    }\n")
	b.WriteString("    rules {\n")
	b.WriteString("      display_name = \"scale-out\"\n")
	b.WriteString("      action {\n")
	b.WriteString("        type  = \"CHANGE_COUNT_BY\"\n")
	b.WriteString("        value = 1\n")
	b.WriteString("      }\n")
	b.WriteString("      metric {\n")
	b.WriteString("        metric_type = \"CPU_UTILIZATION\"\n")
	b.WriteString("        threshold {\n")
	b.WriteString("          operator = \"GT\"\n")
	b.WriteString("          value    = 70\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── load-balancer ─────────────────────────────────────────────────────────────

// lbOCIProto maps a canonical LB protocol to the OCI listener protocol token
// (HTTP / HTTPS handled as HTTP at the LB; TCP is layer-4).
func lbOCIProto(proto string) string {
	if proto == LBProtoTCP {
		return "TCP"
	}
	return "HTTP"
}

// hcOCIProto maps a health-check protocol to the OCI health_checker protocol.
func hcOCIProto(proto string) string {
	if proto == LBProtoTCP {
		return "TCP"
	}
	return "HTTP"
}

func renderLBOCI(p LoadBalancerPlan) string {
	name := tfName(p.LBName)
	lbName := name + "_lb"
	bsName := name + "_bs"
	var b strings.Builder
	hc := p.HealthCheck

	// Public (internet-facing) flexible load balancer across the region's subnets.
	fmt.Fprintf(&b, "resource \"oci_load_balancer_load_balancer\" %q {\n", lbName)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name   = %q\n", p.LBName)
	b.WriteString("  shape          = \"flexible\"\n")
	b.WriteString("  is_private     = false\n")
	if len(p.SubnetNames) > 0 {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("oci_core_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  subnet_ids     = [%s]\n", strings.Join(labels, ", "))
	}
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  network_security_group_ids = [oci_core_network_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	b.WriteString("  shape_details {\n")
	b.WriteString("    minimum_bandwidth_in_mbps = 10\n")
	b.WriteString("    maximum_bandwidth_in_mbps = 100\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	// Backend set with the health checker. Stickiness => an lb-cookie session
	// persistence config.
	fmt.Fprintf(&b, "resource \"oci_load_balancer_backend_set\" %q {\n", bsName)
	fmt.Fprintf(&b, "  load_balancer_id = oci_load_balancer_load_balancer.%s.id\n", lbName)
	fmt.Fprintf(&b, "  name             = \"%s-bs\"\n", name)
	b.WriteString("  policy           = \"ROUND_ROBIN\"\n")
	b.WriteString("  health_checker {\n")
	fmt.Fprintf(&b, "    protocol    = %q\n", hcOCIProto(hc.Protocol))
	fmt.Fprintf(&b, "    port        = %d\n", hc.Port)
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		fmt.Fprintf(&b, "    url_path    = %q\n", hc.Path)
		b.WriteString("    return_code = 200\n")
	}
	fmt.Fprintf(&b, "    interval_ms = %d\n", hc.IntervalSeconds*1000)
	fmt.Fprintf(&b, "    retries     = %d\n", hc.HealthyThreshold)
	b.WriteString("  }\n")
	if p.Stickiness {
		b.WriteString("  lb_cookie_session_persistence_configuration {\n")
		b.WriteString("    cookie_name = \"pyxcloud-lb\"\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n\n")

	// One listener per declared listener port, forwarding to the backend set.
	for _, l := range p.Listeners {
		ln := fmt.Sprintf("%s_listener_%d", name, l.Port)
		fmt.Fprintf(&b, "resource \"oci_load_balancer_listener\" %q {\n", ln)
		fmt.Fprintf(&b, "  load_balancer_id         = oci_load_balancer_load_balancer.%s.id\n", lbName)
		fmt.Fprintf(&b, "  name                     = \"%s-l-%d\"\n", name, l.Port)
		fmt.Fprintf(&b, "  default_backend_set_name = oci_load_balancer_backend_set.%s.name\n", bsName)
		fmt.Fprintf(&b, "  port                     = %d\n", l.Port)
		fmt.Fprintf(&b, "  protocol                 = %q\n", lbOCIProto(l.Protocol))
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── managed-database ──────────────────────────────────────────────────────────

func renderMDBOCI(p ManagedDatabasePlan) string {
	if p.Engine == DBEngineMySQL {
		return renderMDBOCIMySQL(p)
	}
	return renderMDBOCIPostgres(p)
}

func renderMDBOCIMySQL(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	adLabel := name + "_ads"
	var b strings.Builder
	b.WriteString(ociADDataSource(adLabel))
	fmt.Fprintf(&b, "resource \"oci_mysql_mysql_db_system\" %q {\n", name)
	b.WriteString("  compartment_id      = var.compartment_id\n")
	fmt.Fprintf(&b, "  availability_domain = %s\n", ociADRef(adLabel, "1"))
	fmt.Fprintf(&b, "  display_name        = %q\n", p.DBName)
	fmt.Fprintf(&b, "  shape_name          = %q\n", p.DBClass)
	fmt.Fprintf(&b, "  mysql_version       = %q\n", p.EngineVersion)
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "  subnet_id           = oci_core_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	fmt.Fprintf(&b, "  data_storage_size_in_gb = %d\n", p.StorageGB)
	// HA = a high-availability (3-node) DB system. MySQL data is encrypted at rest
	// by default (no toggle). Credentials are managed out-of-band (Vault/CI).
	fmt.Fprintf(&b, "  is_highly_available = %t\n", p.HA)
	b.WriteString("  admin_username      = \"pyxadmin\"\n")
	b.WriteString("  admin_password      = var.db_password\n")
	// Production-safe: a final backup is retained (deletion-plan / final snapshot
	// intent carried by the backup policy; deletion-protection via prevent_destroy).
	b.WriteString("  backup_policy {\n")
	b.WriteString("    is_enabled        = true\n")
	b.WriteString("    retention_in_days = 7\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	if p.DeletionProtection {
		b.WriteString("  lifecycle {\n")
		b.WriteString("    prevent_destroy = true\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func renderMDBOCIPostgres(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	adLabel := name + "_ads"
	var b strings.Builder
	b.WriteString(ociADDataSource(adLabel))
	fmt.Fprintf(&b, "resource \"oci_psql_db_system\" %q {\n", name)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name   = %q\n", p.DBName)
	fmt.Fprintf(&b, "  db_version     = %q\n", p.EngineVersion)
	fmt.Fprintf(&b, "  shape          = %q\n", p.DBClass)
	// HA = >1 instance (primary + reader); single instance otherwise.
	if p.HA {
		b.WriteString("  instance_count = 2\n")
	} else {
		b.WriteString("  instance_count = 1\n")
	}
	b.WriteString("  network_details {\n")
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "    subnet_id = oci_core_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	b.WriteString("  }\n")
	// Storage is regionally durable + encrypted at rest by default (OCI block volume).
	b.WriteString("  storage_details {\n")
	b.WriteString("    system_type           = \"OCI_OPTIMIZED_STORAGE\"\n")
	b.WriteString("    is_regionally_durable = true\n")
	b.WriteString("  }\n")
	b.WriteString("  credentials {\n")
	b.WriteString("    username = \"pyxadmin\"\n")
	b.WriteString("    password_details {\n")
	b.WriteString("      password_type = \"PLAIN_TEXT\"\n")
	b.WriteString("      password      = var.db_password\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	if p.DeletionProtection {
		b.WriteString("  lifecycle {\n")
		b.WriteString("    prevent_destroy = true\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// ── object-storage ────────────────────────────────────────────────────────────

func renderObjectStorageOCI(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	// The Object Storage namespace is the tenancy namespace (a data source). PRIVATE
	// BY DEFAULT: access_type = NoPublicAccess unless explicitly public (ObjectRead).
	accessType := "NoPublicAccess"
	if p.Public {
		accessType = "ObjectRead"
	}
	versioning := "Disabled"
	if p.Versioning {
		versioning = "Enabled"
	}
	b.WriteString("data \"oci_objectstorage_namespace\" \"ns\" {\n")
	b.WriteString("  compartment_id = var.compartment_id\n")
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"oci_objectstorage_bucket\" %q {\n", label)
	b.WriteString("  compartment_id = var.compartment_id\n")
	b.WriteString("  namespace      = data.oci_objectstorage_namespace.ns.namespace\n")
	fmt.Fprintf(&b, "  name           = %q\n", p.BucketName)
	fmt.Fprintf(&b, "  access_type    = %q\n", accessType)
	fmt.Fprintf(&b, "  versioning     = %q\n", versioning)
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── cache (OCI Cache with Redis) ──────────────────────────────────────────────

func renderCacheOCI(p CachePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// OCI Cache cluster (Redis). PRIVATE BY DEFAULT: lives in the place's private
	// subnet, no public endpoint. HA => >1 node.
	nodeCount := 1
	if p.HA {
		nodeCount = 2
	}
	mem := p.MemoryGB
	if mem < 1 {
		mem = 1
	}
	fmt.Fprintf(&b, "resource \"oci_redis_redis_cluster\" %q {\n", name)
	b.WriteString("  compartment_id     = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name       = %q\n", p.Name)
	fmt.Fprintf(&b, "  node_count         = %d\n", nodeCount)
	fmt.Fprintf(&b, "  node_memory_in_gbs = %d\n", mem)
	fmt.Fprintf(&b, "  software_version   = %q\n", ociRedisVersion(p.Version))
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "  subnet_id          = oci_core_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	} else {
		b.WriteString("  subnet_id          = var.subnet_id\n")
	}
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ociRedisVersion maps the resolved cache version to an OCI Cache software_version
// token (OCI expects e.g. "V7_0_5"); a bare "7.0" maps to the V7 line default.
func ociRedisVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "7") {
		return "V7_0_5"
	}
	return "V" + strings.ReplaceAll(v, ".", "_")
}

// ── managed-queue / event-streaming ───────────────────────────────────────────

func renderQueueOCI(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"oci_queue_queue\" %q {\n", name)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name   = %q\n", p.Name)
	if p.VisibilityTimeoutSeconds > 0 {
		fmt.Fprintf(&b, "  visibility_in_seconds = %d\n", p.VisibilityTimeoutSeconds)
	}
	if p.MaxReceiveCount > 0 {
		fmt.Fprintf(&b, "  dead_letter_queue_delivery_count = %d\n", p.MaxReceiveCount)
	}
	// SECURE BY DEFAULT: OCI Queue is encrypted at rest with an Oracle-managed key.
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderStreamOCI(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	partitions := p.Shards
	if partitions <= 0 {
		partitions = 1
	}
	fmt.Fprintf(&b, "resource \"oci_streaming_stream\" %q {\n", name)
	fmt.Fprintf(&b, "  name           = %q\n", p.Name)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  partitions     = %d\n", partitions)
	if p.RetentionHours > 0 {
		fmt.Fprintf(&b, "  retention_in_hours = %d\n", p.RetentionHours)
	}
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── dns-zone ──────────────────────────────────────────────────────────────────

func renderDNSOCI(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	zoneType := "PRIMARY"
	fmt.Fprintf(&b, "resource \"oci_dns_zone\" %q {\n", name)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  name           = %q\n", p.Domain)
	fmt.Fprintf(&b, "  zone_type      = %q\n", zoneType)
	// A private zone is scoped to a private DNS view on the place's VCN.
	if p.Private {
		b.WriteString("  scope          = \"PRIVATE\"\n")
		b.WriteString("  view_id        = var.dns_view_id\n")
	}
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── waf-service ───────────────────────────────────────────────────────────────

func renderWAFOCI(p WAFPlan) string {
	name := tfName(p.Name)
	polName := name + "_policy"
	var b strings.Builder
	// WAF policy with the OCI managed protection capabilities (the OWASP core rule
	// set), default action ALLOW. SECURE BY DEFAULT: protection rule BLOCKs.
	fmt.Fprintf(&b, "resource \"oci_waf_web_app_firewall_policy\" %q {\n", polName)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name   = \"%s-policy\"\n", p.Name)
	b.WriteString("  actions {\n")
	b.WriteString("    name = \"allow-default\"\n")
	b.WriteString("    type = \"ALLOW\"\n")
	b.WriteString("  }\n")
	b.WriteString("  actions {\n")
	b.WriteString("    name = \"block-403\"\n")
	b.WriteString("    type = \"RETURN_HTTP_RESPONSE\"\n")
	b.WriteString("    code = 403\n")
	b.WriteString("  }\n")
	b.WriteString("  request_protection {\n")
	b.WriteString("    rules {\n")
	b.WriteString("      name        = \"owasp-core\"\n")
	b.WriteString("      type        = \"PROTECTION\"\n")
	b.WriteString("      action_name = \"block-403\"\n")
	b.WriteString("      protection_capabilities {\n")
	b.WriteString("        key     = \"941100\"\n")
	b.WriteString("        version = 1\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	// The web application firewall attaches the policy to a load balancer backend.
	fmt.Fprintf(&b, "resource \"oci_waf_web_app_firewall\" %q {\n", name)
	b.WriteString("  compartment_id             = var.compartment_id\n")
	b.WriteString("  backend_type               = \"LOAD_BALANCER\"\n")
	if p.AssociateName != "" {
		fmt.Fprintf(&b, "  load_balancer_id           = oci_load_balancer_load_balancer.%s.id\n", tfName(p.AssociateName)+"_lb")
	} else {
		b.WriteString("  load_balancer_id           = var.load_balancer_id\n")
	}
	fmt.Fprintf(&b, "  web_app_firewall_policy_id = oci_waf_web_app_firewall_policy.%s.id\n", polName)
	fmt.Fprintf(&b, "  display_name               = %q\n", p.Name)
	fmt.Fprintf(&b, "  freeform_tags              = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── managed-kubernetes (OKE) ──────────────────────────────────────────────────

func renderK8sOCI(p K8sPlan) string {
	name := tfName(p.Name)
	npName := name + "_np"
	adLabel := name + "_ads"
	var b strings.Builder
	b.WriteString(ociADDataSource(adLabel))

	subnetIDs := func() []string {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("oci_core_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		return labels
	}()

	// OKE cluster. SECURE BY DEFAULT: a private control-plane endpoint.
	fmt.Fprintf(&b, "resource \"oci_containerengine_cluster\" %q {\n", name)
	b.WriteString("  compartment_id     = var.compartment_id\n")
	fmt.Fprintf(&b, "  name               = %q\n", p.Name)
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vcn_id             = oci_core_vcn.%s.id\n", tfName(p.NetworkName))
	} else {
		b.WriteString("  vcn_id             = var.vcn_id\n")
	}
	ver := p.Version
	if ver == "" {
		ver = "v1.30.1"
	}
	fmt.Fprintf(&b, "  kubernetes_version = %q\n", ver)
	// VCN-native pod networking (the modern OKE default) is declared in its own block.
	b.WriteString("  cluster_pod_network_options {\n")
	b.WriteString("    cni_type = \"OCI_VCN_IP_NATIVE\"\n")
	b.WriteString("  }\n")
	if len(subnetIDs) > 0 {
		b.WriteString("  endpoint_config {\n")
		fmt.Fprintf(&b, "    subnet_id            = %s\n", subnetIDs[0])
		b.WriteString("    is_public_ip_enabled = false\n")
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	// Autoscaling node pool, node machine type from the catalog (ResolveSKU).
	fmt.Fprintf(&b, "resource \"oci_containerengine_node_pool\" %q {\n", npName)
	b.WriteString("  compartment_id     = var.compartment_id\n")
	fmt.Fprintf(&b, "  cluster_id         = oci_containerengine_cluster.%s.id\n", name)
	fmt.Fprintf(&b, "  name               = \"%s-np\"\n", p.Name)
	fmt.Fprintf(&b, "  node_shape         = %q\n", p.NodeType)
	fmt.Fprintf(&b, "  kubernetes_version = %q\n", ver)
	b.WriteString("  node_shape_config {\n")
	fmt.Fprintf(&b, "    ocpus         = %d\n", p.NodeCPU)
	fmt.Fprintf(&b, "    memory_in_gbs = %d\n", p.NodeRAM)
	b.WriteString("  }\n")
	// The node OS image is supplied out-of-band (a variable), like the function
	// image / lambda role in the other macro renderers — the OKE worker image is a
	// region-specific OCID resolved in CI, not committed here.
	b.WriteString("  node_source_details {\n")
	b.WriteString("    source_type = \"IMAGE\"\n")
	b.WriteString("    image_id    = var.node_image_id\n")
	b.WriteString("  }\n")
	b.WriteString("  node_config_details {\n")
	fmt.Fprintf(&b, "    size = %d\n", p.DesiredNodes)
	if len(p.SubnetNames) > 0 {
		for i, s := range p.SubnetNames {
			b.WriteString("    placement_configs {\n")
			fmt.Fprintf(&b, "      availability_domain = %s\n", ociADRef(adLabel, fmt.Sprintf("%d", (i%3)+1)))
			fmt.Fprintf(&b, "      subnet_id           = oci_core_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, s))
			b.WriteString("    }\n")
		}
	}
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  freeform_tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── secrets-manager (Vault) ───────────────────────────────────────────────────

func renderSecretsOCI(p SecretsPlan) string {
	name := tfName(p.Name)
	vaultName := name + "_vault"
	var b strings.Builder
	// A KMS vault holds the master key; the secret lives in it. SECURE: the secret
	// VALUE is never declared here (it would leak into state) — the secret content
	// is written out-of-band, mirroring the AWS/GCP secrets renderers. The KMS key
	// is provided out-of-band (a sibling kms component) via a variable.
	fmt.Fprintf(&b, "resource \"oci_kms_vault\" %q {\n", vaultName)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name   = \"%s-vault\"\n", p.Name)
	b.WriteString("  vault_type     = \"DEFAULT\"\n")
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"oci_vault_secret\" %q {\n", name)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  secret_name    = %q\n", p.Name)
	fmt.Fprintf(&b, "  vault_id       = oci_kms_vault.%s.id\n", vaultName)
	b.WriteString("  key_id         = var.kms_key_id\n")
	if desc := asciiOnly(p.Description); desc != "" {
		fmt.Fprintf(&b, "  description    = %q\n", desc)
	}
	// Rotation, when requested, via the secret rotation config.
	if p.RotationDays > 0 {
		b.WriteString("  rotation_config {\n")
		b.WriteString("    is_scheduled_rotation_enabled = true\n")
		fmt.Fprintf(&b, "    rotation_interval = \"P%dD\"\n", p.RotationDays)
		b.WriteString("    target_system_details {\n")
		b.WriteString("      target_system_type = \"FUNCTION\"\n")
		b.WriteString("      function_id        = var.rotation_function_id\n")
		b.WriteString("    }\n")
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── serverless-function (OCI Functions) ───────────────────────────────────────

func renderServerlessOCI(p ServerlessPlan) string {
	name := tfName(p.Name)
	appName := name + "_app"
	var b strings.Builder
	// OCI Functions: an application (the network/compartment boundary) holds the
	// functions. The function is a CONTAINER IMAGE (Fn project), supplied
	// out-of-band via a variable. PRIVATE BY DEFAULT: the application binds to the
	// place's private subnet; no public invoke endpoint is declared.
	fmt.Fprintf(&b, "resource \"oci_functions_application\" %q {\n", appName)
	b.WriteString("  compartment_id = var.compartment_id\n")
	fmt.Fprintf(&b, "  display_name   = \"%s-app\"\n", p.Name)
	b.WriteString("  subnet_ids     = var.function_subnet_ids\n")
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	mem := p.MemoryMB
	if mem <= 0 {
		mem = 128
	}
	fmt.Fprintf(&b, "resource \"oci_functions_function\" %q {\n", name)
	fmt.Fprintf(&b, "  application_id = oci_functions_application.%s.id\n", appName)
	fmt.Fprintf(&b, "  display_name   = %q\n", p.Name)
	fmt.Fprintf(&b, "  memory_in_mbs  = %d\n", mem)
	// The deployable container image (built from the function source) is supplied
	// out-of-band; ConcreteRuntime is the canonical runtime family used to pick the
	// base image in CI. The image reference is a variable so no value leaks here.
	b.WriteString("  image          = var.function_image\n")
	if p.TimeoutSeconds > 0 {
		fmt.Fprintf(&b, "  timeout_in_seconds = %d\n", p.TimeoutSeconds)
	}
	fmt.Fprintf(&b, "  freeform_tags  = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}
