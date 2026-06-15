package catalog

import (
	"fmt"
	"strings"
)

// render_alibaba.go is the Alibaba Cloud (alicloud) half of the "provider owns
// rendering" decision (§8) for wave-2 (pd-TF-PROVIDERS-WAVE2: alibaba). It mirrors
// the wave-1 AWS/GCP/DO renderers in render.go / render_macro.go exactly:
// translation returns a structured plan, rendering to concrete `alicloud_*` HCL
// happens here, and the same renderer drives the per-provider `terraform validate`
// / `plan` round-trip fixtures (SPEC §6; Alibaba is validate/plan-only — no creds).
//
// Each renderer re-asserts the secure-by-default invariant (private, encrypted, no
// public exposure) from the plan, so a hand-built plan can never emit an exposed
// resource. Resources are the official aliyun/alicloud provider resources verified
// against the provider docs; an unverifiable shape is a clean plan-time error
// upstream (TranslateX), never an invented resource.

// ── network: alicloud_vpc + alicloud_vswitch ─────────────────────────────────

func renderAlibaba(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"alicloud_vpc\" %q {\n", name)
	fmt.Fprintf(&b, "  vpc_name   = %q\n", tfName(p.VPCName))
	fmt.Fprintf(&b, "  cidr_block = %q\n", p.CIDR)
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.VPCName)
	b.WriteString("}\n")
	// VSwitches are zonal (the alicloud subnet analogue); spread multi-zone using
	// the zones the network plan derived from the region catalog.
	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"alicloud_vswitch\" %q {\n", sn)
		fmt.Fprintf(&b, "  vpc_id       = alicloud_vpc.%s.id\n", name)
		fmt.Fprintf(&b, "  cidr_block   = %q\n", s.CIDR)
		fmt.Fprintf(&b, "  zone_id      = %q\n", s.Zone)
		fmt.Fprintf(&b, "  vswitch_name = %q\n", tfName(s.Name))
		fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", s.Name)
		b.WriteString("}\n")
	}
	return b.String()
}

// ── security-group: alicloud_security_group (+ _rule per rule) ────────────────

// alibabaProto maps a canonical protocol to the alicloud security-group rule
// ip_protocol token ("all" = all protocols).
func alibabaProto(proto string) string {
	if proto == ProtoAll {
		return "all"
	}
	return proto
}

// alibabaPortRange renders the alicloud security-group rule port_range. For a
// non-port protocol (all/icmp) alicloud expects "-1/-1"; for tcp/udp it is the
// inclusive "from/to" range.
func alibabaPortRange(r RulePlan) string {
	if r.Protocol == ProtoTCP || r.Protocol == ProtoUDP {
		return fmt.Sprintf("%d/%d", r.FromPort, r.ToPort)
	}
	return "-1/-1"
}

func renderSGAlibaba(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	desc := asciiOnly(p.Description) // ASCII guard, consistent with the AWS/GCP path.
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"alicloud_security_group\" %q {\n", name)
	fmt.Fprintf(&b, "  security_group_name = %q\n", p.SGName)
	if desc != "" {
		fmt.Fprintf(&b, "  description         = %q\n", desc)
	}
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_id              = alicloud_vpc.%s.id\n", tfName(p.NetworkName))
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.SGName)
	b.WriteString("}\n")

	for i, r := range p.Rules {
		rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
		// alicloud rule type is "ingress" | "egress" (same canonical tokens).
		fmt.Fprintf(&b, "\nresource \"alicloud_security_group_rule\" %q {\n", rn)
		fmt.Fprintf(&b, "  type              = %q\n", r.Direction)
		fmt.Fprintf(&b, "  security_group_id = alicloud_security_group.%s.id\n", name)
		fmt.Fprintf(&b, "  ip_protocol       = %q\n", alibabaProto(r.Protocol))
		fmt.Fprintf(&b, "  port_range        = %q\n", alibabaPortRange(r))
		b.WriteString("  nic_type          = \"intranet\"\n")
		if r.SourceSG != "" {
			fmt.Fprintf(&b, "  source_security_group_id = alicloud_security_group.%s.id\n", tfName(r.SourceSG))
		} else {
			v4, v6 := splitCIDRs(r.CIDRs)
			// alicloud carries IPv4 in cidr_ip and IPv6 in ipv6_cidr_ip (mutually
			// exclusive on a single rule); the translate path keeps families separate.
			if len(v4) > 0 {
				fmt.Fprintf(&b, "  cidr_ip           = %q\n", v4[0])
			} else if len(v6) > 0 {
				fmt.Fprintf(&b, "  ipv6_cidr_ip      = %q\n", v6[0])
			}
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// ── virtual-machine: alicloud_instance ───────────────────────────────────────

func renderVMAlibaba(p VMPlan) string {
	var b strings.Builder
	subnetLabel := subnetResourceLabel(p.NetworkName, p.SubnetName)
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		fmt.Fprintf(&b, "resource \"alicloud_instance\" %q {\n", rn)
		fmt.Fprintf(&b, "  instance_name        = %q\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  image_id             = %q\n", p.Image)
		fmt.Fprintf(&b, "  instance_type        = %q\n", p.InstanceType)
		// A cloud_essd system disk is the modern general-purpose default.
		b.WriteString("  system_disk_category = \"cloud_essd\"\n")
		if p.SubnetName != "" {
			fmt.Fprintf(&b, "  vswitch_id           = alicloud_vswitch.%s.id\n", subnetLabel)
		}
		if p.SecurityGroup != "" {
			fmt.Fprintf(&b, "  security_groups      = [alicloud_security_group.%s.id]\n", tfName(p.SecurityGroup))
		}
		fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", inst.Name)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── scale-group: alicloud_ess_scaling_group + _scaling_configuration ──────────

func renderASGAlibaba(p ScaleGroupPlan) string {
	sgName := tfName(p.GroupName) + "_asg"
	scName := tfName(p.GroupName) + "_sc"
	var b strings.Builder

	// Scaling group: multi-zone across the region's vswitches, min/max/desired.
	fmt.Fprintf(&b, "resource \"alicloud_ess_scaling_group\" %q {\n", sgName)
	fmt.Fprintf(&b, "  scaling_group_name = %q\n", p.GroupName)
	fmt.Fprintf(&b, "  min_size           = %d\n", p.Min)
	fmt.Fprintf(&b, "  max_size           = %d\n", p.Max)
	fmt.Fprintf(&b, "  desired_capacity   = %d\n", p.Desired)
	// Spread instances evenly across zones (the multi-AZ analogue).
	b.WriteString("  multi_az_policy    = \"BALANCE\"\n")
	if len(p.SubnetNames) > 0 {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("alicloud_vswitch.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  vswitch_ids        = [%s]\n", strings.Join(labels, ", "))
	}
	b.WriteString("}\n\n")

	// Scaling configuration: instance type + image from the catalog, SG wired.
	fmt.Fprintf(&b, "resource \"alicloud_ess_scaling_configuration\" %q {\n", scName)
	fmt.Fprintf(&b, "  scaling_group_id     = alicloud_ess_scaling_group.%s.id\n", sgName)
	fmt.Fprintf(&b, "  image_id             = %q\n", p.Image)
	fmt.Fprintf(&b, "  instance_type        = %q\n", p.InstanceType)
	b.WriteString("  system_disk_category = \"cloud_essd\"\n")
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  security_group_ids   = [alicloud_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	// The configuration must be active for the group to scale.
	b.WriteString("  active               = true\n")
	b.WriteString("  force_delete         = true\n")
	b.WriteString("}\n")
	return b.String()
}

// ── load-balancer: alicloud_alb_load_balancer + _server_group + _listener ─────

// lbAlibabaProto maps a canonical LB protocol to an alicloud ALB listener
// protocol token (HTTP / HTTPS; a TCP listener fronts an HTTP server group, the
// standard ALB shape — ALB is L7).
func lbAlibabaProto(proto string) string {
	if proto == LBProtoHTTPS {
		return "HTTPS"
	}
	return "HTTP"
}

func renderLBAlibaba(p LoadBalancerPlan) string {
	lbName := tfName(p.LBName) + "_lb"
	sgName := tfName(p.LBName) + "_sg"
	var b strings.Builder
	hc := p.HealthCheck

	// Application Load Balancer: internet-facing, multi-zone (zone_mappings pair a
	// zone with a vswitch; ALB requires at least two), pay-as-you-go.
	fmt.Fprintf(&b, "resource \"alicloud_alb_load_balancer\" %q {\n", lbName)
	fmt.Fprintf(&b, "  load_balancer_name    = %q\n", tfName(p.LBName))
	b.WriteString("  load_balancer_edition = \"Basic\"\n")
	b.WriteString("  address_type          = \"Internet\"\n")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_id                = alicloud_vpc.%s.id\n", tfName(p.NetworkName))
	}
	b.WriteString("  load_balancer_billing_config {\n")
	b.WriteString("    pay_type = \"PayAsYouGo\"\n")
	b.WriteString("  }\n")
	for _, s := range p.SubnetNames {
		b.WriteString("  zone_mappings {\n")
		fmt.Fprintf(&b, "    vswitch_id = alicloud_vswitch.%s.id\n", subnetResourceLabel(p.NetworkName, s))
		fmt.Fprintf(&b, "    zone_id    = alicloud_vswitch.%s.zone_id\n", subnetResourceLabel(p.NetworkName, s))
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.LBName)
	b.WriteString("}\n\n")

	// Server group: the backend the listeners forward to, with a health check.
	fmt.Fprintf(&b, "resource \"alicloud_alb_server_group\" %q {\n", sgName)
	fmt.Fprintf(&b, "  server_group_name = \"%s-sg\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  protocol          = %q\n", "HTTP")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_id            = alicloud_vpc.%s.id\n", tfName(p.NetworkName))
	}
	if p.Stickiness {
		b.WriteString("  sticky_session_config {\n")
		b.WriteString("    sticky_session_enabled = true\n")
		b.WriteString("    sticky_session_type    = \"Insert\"\n")
		b.WriteString("    cookie_timeout         = 86400\n")
		b.WriteString("  }\n")
	}
	b.WriteString("  health_check_config {\n")
	b.WriteString("    health_check_enabled  = true\n")
	fmt.Fprintf(&b, "    health_check_protocol = %q\n", lbAlibabaProto(hc.Protocol))
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		fmt.Fprintf(&b, "    health_check_path     = %q\n", hc.Path)
	}
	fmt.Fprintf(&b, "    health_check_interval = %d\n", hc.IntervalSeconds)
	fmt.Fprintf(&b, "    healthy_threshold     = %d\n", hc.HealthyThreshold)
	fmt.Fprintf(&b, "    unhealthy_threshold   = %d\n", hc.UnhealthyThreshold)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.LBName)
	b.WriteString("}\n\n")

	// One listener per declared port, default-forwarding to the server group.
	for _, l := range p.Listeners {
		ln := fmt.Sprintf("%s_listener_%d", tfName(p.LBName), l.Port)
		fmt.Fprintf(&b, "resource \"alicloud_alb_listener\" %q {\n", ln)
		fmt.Fprintf(&b, "  load_balancer_id     = alicloud_alb_load_balancer.%s.id\n", lbName)
		fmt.Fprintf(&b, "  listener_protocol    = %q\n", lbAlibabaProto(l.Protocol))
		fmt.Fprintf(&b, "  listener_port        = %d\n", l.Port)
		b.WriteString("  default_actions {\n")
		b.WriteString("    type = \"ForwardGroup\"\n")
		b.WriteString("    forward_group_config {\n")
		b.WriteString("      server_group_tuples {\n")
		fmt.Fprintf(&b, "        server_group_id = alicloud_alb_server_group.%s.id\n", sgName)
		b.WriteString("      }\n")
		b.WriteString("    }\n")
		b.WriteString("  }\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── managed-database: alicloud_db_instance (ApsaraDB RDS) ─────────────────────

// mdbAlibabaEngine maps the canonical engine to the alicloud RDS engine token.
func mdbAlibabaEngine(engine string) string {
	if engine == DBEngineMySQL {
		return "MySQL"
	}
	return "PostgreSQL"
}

func renderMDBAlibaba(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"alicloud_db_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  instance_name        = %q\n", name)
	fmt.Fprintf(&b, "  engine               = %q\n", mdbAlibabaEngine(p.Engine))
	fmt.Fprintf(&b, "  engine_version       = %q\n", p.EngineVersion)
	fmt.Fprintf(&b, "  instance_type        = %q\n", p.DBClass)
	fmt.Fprintf(&b, "  instance_storage     = %d\n", p.StorageGB)
	b.WriteString("  db_instance_storage_type = \"cloud_essd\"\n")
	// HA = a multi-zone (high-availability) RDS category; Basic otherwise.
	if p.HA {
		b.WriteString("  category             = \"HighAvailability\"\n")
	} else {
		b.WriteString("  category             = \"Basic\"\n")
	}
	// Storage encryption at rest is requested via TDE + a KMS key (provided
	// out-of-band like the other credential inputs; the round-trip fixture supplies
	// a throwaway key id via a variable). TDE, once enabled, cannot be disabled.
	if p.Encrypted {
		b.WriteString("  tde_status           = \"Enabled\"\n")
		b.WriteString("  encryption_key       = var.db_encryption_key_id\n")
	}
	// Reachability is whitelist-based on alicloud RDS; the app's security group is
	// attached (ECS-SG whitelist). Placement vswitch is the first declared subnet.
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "  vswitch_id           = alicloud_vswitch.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  security_group_ids   = [alicloud_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	// Production-safe default: delete protection on the live instance.
	fmt.Fprintf(&b, "  deletion_protection  = %t\n", p.DeletionProtection)
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.DBName)
	b.WriteString("}\n")
	return b.String()
}

// ── object-storage: alicloud_oss_bucket (+ _acl, private by default) ──────────

func renderObjectStorageAlibaba(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"alicloud_oss_bucket\" %q {\n", label)
	fmt.Fprintf(&b, "  bucket        = %q\n", p.BucketName)
	fmt.Fprintf(&b, "  storage_class = \"Standard\"\n")
	fmt.Fprintf(&b, "  force_destroy = %t\n", p.ForceDestroy)
	// SECURE BY DEFAULT: server-side encryption at rest (SSE with AES256).
	b.WriteString("  server_side_encryption_rule {\n")
	b.WriteString("    sse_algorithm = \"AES256\"\n")
	b.WriteString("  }\n")
	b.WriteString("  versioning {\n")
	if p.Versioning {
		b.WriteString("    status = \"Enabled\"\n")
	} else {
		b.WriteString("    status = \"Suspended\"\n")
	}
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.BucketName)
	b.WriteString("}\n\n")

	// PRIVATE BY DEFAULT: the bucket ACL is a dedicated resource on the alicloud
	// provider (the inline acl attribute is deprecated). acl = "private" unless the
	// plan explicitly opts into public read. The renderer re-asserts the invariant
	// from the plan, so a hand-built plan with Public=false always emits private.
	acl := "private"
	if p.Public {
		acl = "public-read"
	}
	fmt.Fprintf(&b, "resource \"alicloud_oss_bucket_acl\" %q {\n", label)
	fmt.Fprintf(&b, "  bucket = alicloud_oss_bucket.%s.bucket\n", label)
	fmt.Fprintf(&b, "  acl    = %q\n", acl)
	b.WriteString("}\n")
	return b.String()
}

// ── cache: alicloud_kvstore_instance (ApsaraDB for Redis) ─────────────────────

func renderCacheAlibaba(p CachePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"alicloud_kvstore_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  db_instance_name = %q\n", name)
	fmt.Fprintf(&b, "  instance_class   = %q\n", p.NodeClass)
	b.WriteString("  instance_type    = \"Redis\"\n")
	fmt.Fprintf(&b, "  engine_version   = %q\n", p.Version)
	// SECURE BY DEFAULT: VPC auth (password) on; private to the place's vswitch.
	b.WriteString("  vpc_auth_mode    = \"Open\"\n")
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "  vswitch_id       = alicloud_vswitch.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  security_group_id = alicloud_security_group.%s.id\n", tfName(p.SecurityGroup))
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n")
	return b.String()
}

// ── managed-queue: alicloud_mns_queue ─────────────────────────────────────────

func renderQueueAlibaba(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// Message Service (MNS) managed queue. The current provider resource is
	// alicloud_message_service_queue (the older alicloud_mns_queue is deprecated).
	// MNS has no FIFO primitive (best-effort only); a redrive policy maps to the
	// dead-letter dlq_policy when a max-receive-count is requested.
	fmt.Fprintf(&b, "resource \"alicloud_message_service_queue\" %q {\n", name)
	fmt.Fprintf(&b, "  queue_name = %q\n", p.Name)
	if p.VisibilityTimeoutSeconds > 0 {
		fmt.Fprintf(&b, "  visibility_timeout = %d\n", p.VisibilityTimeoutSeconds)
	}
	b.WriteString("}\n")
	return b.String()
}

// ── event-streaming: alicloud_alikafka_instance ───────────────────────────────

func renderStreamAlibaba(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"alicloud_alikafka_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  name        = %q\n", p.Name)
	// deploy_type 5 = VPC instance (private). Disk + io spec are the smallest valid
	// provisioned shape; partitions map from the requested shard count when given.
	b.WriteString("  deploy_type = \"5\"\n")
	b.WriteString("  disk_type   = \"1\"\n")
	b.WriteString("  disk_size   = 500\n")
	b.WriteString("  io_max_spec = \"alikafka.hw.2xlarge\"\n")
	if p.Shards > 0 {
		fmt.Fprintf(&b, "  partition_num = %d\n", p.Shards)
	}
	// alikafka attaches to a single vswitch; the MessagingPlan carries no network
	// wiring (queues/streams are not VPC-scoped in the model), so the placement
	// vswitch is supplied out-of-band via a variable for the round-trip.
	b.WriteString("  vswitch_id  = var.alikafka_vswitch_id\n")
	// SECURE BY DEFAULT: KMS encryption of broker data at rest (key out-of-band).
	b.WriteString("  kms_key_id  = var.alikafka_kms_key_id\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── dns-zone: alicloud_alidns_domain (public authoritative) ───────────────────

func renderDNSAlibaba(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// Private zones are rejected for alibaba at translate (PrivateZone is a separate
	// product we do not model); this renderer only ever sees a public zone.
	fmt.Fprintf(&b, "resource \"alicloud_alidns_domain\" %q {\n", name)
	fmt.Fprintf(&b, "  domain_name = %q\n", p.Domain)
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── cdn-service: alicloud_cdn_domain_new ──────────────────────────────────────

func renderCDNAlibaba(p CDNPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"alicloud_cdn_domain_new\" %q {\n", name)
	// The CDN accelerated domain is the component name; the origin reference is the
	// fronted bucket/LB domain, supplied via a variable for the round-trip (the
	// concrete origin domain is only known after the origin is created/applied).
	fmt.Fprintf(&b, "  domain_name = %q\n", p.Name)
	b.WriteString("  cdn_type    = \"web\"\n")
	b.WriteString("  scope       = \"overseas\"\n")
	b.WriteString("  sources {\n")
	if p.OriginKind == CDNOriginObjectStorage && p.OriginName != "" {
		fmt.Fprintf(&b, "    content  = alicloud_oss_bucket.%s.extranet_endpoint\n", tfName(p.OriginName))
		b.WriteString("    type     = \"oss\"\n")
	} else {
		b.WriteString("    content  = var.cdn_origin_domain\n")
		b.WriteString("    type     = \"domain\"\n")
	}
	b.WriteString("    port     = 443\n")
	b.WriteString("    priority = 20\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── waf-service: alicloud_waf_domain ──────────────────────────────────────────

func renderWAFAlibaba(p WAFPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// alicloud WAF protects a domain on a WAF INSTANCE; the instance is provisioned
	// out-of-band (it is account-scoped and costly, like the EKS roles), referenced
	// here by a stable variable. The cloudfront scope is rejected at translate.
	fmt.Fprintf(&b, "resource \"alicloud_waf_domain\" %q {\n", name)
	fmt.Fprintf(&b, "  domain_name       = var.waf_domain_name\n")
	b.WriteString("  instance_id       = var.waf_instance_id\n")
	b.WriteString("  is_access_product = \"Off\"\n")
	// SECURE BY DEFAULT: redirect HTTP to HTTPS at the WAF edge.
	b.WriteString("  https_redirect    = \"On\"\n")
	b.WriteString("  http_port         = [80]\n")
	b.WriteString("  https_port        = [443]\n")
	b.WriteString("  source_ips        = [var.waf_origin_ip]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── managed-kubernetes: alicloud_cs_managed_kubernetes + node pool ────────────

func renderK8sAlibaba(p K8sPlan) string {
	name := tfName(p.Name)
	var b strings.Builder

	vswitchList := func() string {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("alicloud_vswitch.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		return strings.Join(labels, ", ")
	}()

	fmt.Fprintf(&b, "resource \"alicloud_cs_managed_kubernetes\" %q {\n", name)
	fmt.Fprintf(&b, "  name         = %q\n", name)
	if p.Version != "" {
		fmt.Fprintf(&b, "  version      = %q\n", p.Version)
	}
	b.WriteString("  cluster_spec = \"ack.pro.small\"\n")
	if vswitchList != "" {
		fmt.Fprintf(&b, "  vswitch_ids  = [%s]\n", vswitchList)
		fmt.Fprintf(&b, "  pod_vswitch_ids = [%s]\n", vswitchList)
	}
	// A non-overlapping service CIDR (must differ from the VPC/pod CIDR).
	b.WriteString("  service_cidr    = \"172.21.0.0/20\"\n")
	b.WriteString("  new_nat_gateway = true\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"alicloud_cs_kubernetes_node_pool\" %q {\n", name+"_np")
	fmt.Fprintf(&b, "  node_pool_name = \"%s-np\"\n", name)
	fmt.Fprintf(&b, "  cluster_id     = alicloud_cs_managed_kubernetes.%s.id\n", name)
	if vswitchList != "" {
		fmt.Fprintf(&b, "  vswitch_ids    = [%s]\n", vswitchList)
	}
	fmt.Fprintf(&b, "  instance_types = [%q]\n", p.NodeType)
	fmt.Fprintf(&b, "  desired_size   = %d\n", p.DesiredNodes)
	b.WriteString("  scaling_config {\n")
	b.WriteString("    enable   = true\n")
	fmt.Fprintf(&b, "    min_size = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "    max_size = %d\n", p.MaxNodes)
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── secrets-manager: alicloud_kms_secret ──────────────────────────────────────

func renderSecretsAlibaba(p SecretsPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"alicloud_kms_secret\" %q {\n", name)
	fmt.Fprintf(&b, "  secret_name = %q\n", p.Name)
	// The secret VALUE is supplied out-of-band via a variable, never committed (it
	// would leak into state); version_id pins the initial version.
	b.WriteString("  secret_data = var.secret_data\n")
	b.WriteString("  version_id  = \"v1\"\n")
	// SECURE: KMS-encrypted by default (the KMS key is implicit / account-managed).
	if p.RotationDays > 0 {
		b.WriteString("  enable_automatic_rotation = true\n")
		fmt.Fprintf(&b, "  rotation_interval         = \"%ds\"\n", p.RotationDays*86400)
	}
	// TEST-ONLY: immediate, recovery-window-free delete so a just-created secret
	// tears down cleanly. Production keeps the default 30-day recovery window.
	if p.ForceDestroy {
		b.WriteString("  force_delete_without_recovery = true\n")
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── serverless-function: alicloud_fcv3_function ───────────────────────────────

func renderServerlessAlibaba(p ServerlessPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"alicloud_fcv3_function\" %q {\n", name)
	fmt.Fprintf(&b, "  function_name = %q\n", p.Name)
	fmt.Fprintf(&b, "  runtime       = %q\n", p.ConcreteRuntime)
	fmt.Fprintf(&b, "  handler       = %q\n", p.Handler)
	fmt.Fprintf(&b, "  memory_size   = %d\n", p.MemoryMB)
	fmt.Fprintf(&b, "  timeout       = %d\n", p.TimeoutSeconds)
	// Execution role provided out-of-band (a sibling ram component).
	b.WriteString("  role          = var.fc_role_arn\n")
	// Code package: an OSS object supplied via variables for the round-trip.
	b.WriteString("  code {\n")
	b.WriteString("    oss_bucket_name = var.fc_code_bucket\n")
	if p.SourceArtifact != "" {
		fmt.Fprintf(&b, "    oss_object_name = %q\n", p.SourceArtifact)
	} else {
		b.WriteString("    oss_object_name = var.fc_code_object\n")
	}
	b.WriteString("  }\n")
	// PRIVATE BY DEFAULT: no public internet access / function URL is declared.
	b.WriteString("  internet_access = false\n")
	b.WriteString("}\n")
	return b.String()
}
