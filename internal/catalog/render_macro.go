package catalog

import (
	"fmt"
	"strings"
)

// This file renders the remaining wave-1 macro components (pd-TF-REST-LAMBDA)
// into concrete cloud-provider Terraform HCL. Mirrors render.go: translation
// returns a structured plan, rendering to .tf happens here and drives the
// per-provider `terraform plan` / real apply round-trip tests (SPEC §6). Each
// renderer re-asserts the secure-by-default invariant (private, encrypted, no
// public exposure) so a hand-built plan can never emit an exposed resource.

// ── cache ────────────────────────────────────────────────────────────────────

// RenderCacheHCL renders a CachePlan into Redis HCL.
//   - AWS: aws_elasticache_subnet_group + aws_elasticache_replication_group,
//     encrypted in transit + at rest, multi-AZ when HA. NOT publicly reachable.
//   - GCP: google_redis_instance (Memorystore), STANDARD_HA tier when HA, private
//     to the place's network (authorized_network), no public IP.
//   - DO: digitalocean_database_cluster engine=redis, private VPC, node_count 2 when HA.
func RenderCacheHCL(plan CachePlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderCacheAWS(plan), nil
	case ProviderGCP:
		return renderCacheGCP(plan), nil
	case ProviderDigitalOcean:
		return renderCacheDO(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"cache",
			"The Ubicloud Terraform provider exposes no managed cache/Redis resource; use managed "+
				"Redis on aws / gcp / digitalocean.")
	default:
		return "", fmt.Errorf("render: unsupported provider %q for cache", plan.Provider)
	}
}

func renderCacheAWS(p CachePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "resource \"aws_elasticache_subnet_group\" %q {\n", name)
		fmt.Fprintf(&b, "  name       = \"%s-cache\"\n", name)
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("aws_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		fmt.Fprintf(&b, "  subnet_ids = [%s]\n", strings.Join(labels, ", "))
		b.WriteString("}\n\n")
	}
	fmt.Fprintf(&b, "resource \"aws_elasticache_replication_group\" %q {\n", name)
	fmt.Fprintf(&b, "  replication_group_id       = %q\n", name)
	fmt.Fprintf(&b, "  description                = \"pyxcloud cache %s\"\n", name)
	fmt.Fprintf(&b, "  engine                     = %q\n", p.Engine)
	fmt.Fprintf(&b, "  engine_version             = %q\n", p.Version)
	fmt.Fprintf(&b, "  node_type                  = %q\n", p.NodeClass)
	if p.HA {
		b.WriteString("  num_cache_clusters         = 2\n")
		b.WriteString("  automatic_failover_enabled = true\n")
		b.WriteString("  multi_az_enabled           = true\n")
	} else {
		b.WriteString("  num_cache_clusters         = 1\n")
	}
	// SECURE BY DEFAULT: encryption in transit + at rest.
	b.WriteString("  transit_encryption_enabled = true\n")
	b.WriteString("  at_rest_encryption_enabled = true\n")
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "  subnet_group_name          = aws_elasticache_subnet_group.%s.name\n", name)
	}
	if p.SecurityGroup != "" {
		fmt.Fprintf(&b, "  security_group_ids         = [aws_security_group.%s.id]\n", tfName(p.SecurityGroup))
	}
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n")
	return b.String()
}

func renderCacheGCP(p CachePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	tier := "BASIC"
	if p.HA {
		tier = "STANDARD_HA"
	}
	fmt.Fprintf(&b, "resource \"google_redis_instance\" %q {\n", name)
	fmt.Fprintf(&b, "  name               = %q\n", name)
	fmt.Fprintf(&b, "  tier               = %q\n", tier)
	fmt.Fprintf(&b, "  memory_size_gb     = %d\n", p.MemoryGB)
	fmt.Fprintf(&b, "  region             = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  redis_version      = %q\n", p.Version)
	// SECURE BY DEFAULT: private to the place's network, TLS in transit.
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  authorized_network = google_compute_network.%s.id\n", tfName(p.NetworkName))
	}
	b.WriteString("  connect_mode       = \"PRIVATE_SERVICE_ACCESS\"\n")
	b.WriteString("  transit_encryption_mode = \"SERVER_AUTHENTICATION\"\n")
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderCacheDO(p CachePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"digitalocean_database_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name       = %q\n", name)
	b.WriteString("  engine     = \"redis\"\n")
	fmt.Fprintf(&b, "  version    = %q\n", p.Version)
	fmt.Fprintf(&b, "  size       = %q\n", p.NodeClass)
	fmt.Fprintf(&b, "  region     = %q\n", p.CSPRegion)
	if p.HA {
		b.WriteString("  node_count = 2\n")
	} else {
		b.WriteString("  node_count = 1\n")
	}
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  private_network_uuid = digitalocean_vpc.%s.id\n", tfName(p.NetworkName))
	}
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── managed-queue / event-streaming ──────────────────────────────────────────

// RenderMessagingHCL renders a MessagingPlan (queue or stream) into HCL. DO never
// reaches here (TranslateQueue/TranslateStream reject it with a clean error).
func RenderMessagingHCL(plan MessagingPlan) (string, error) {
	switch plan.Kind {
	case KindQueue:
		switch plan.Provider {
		case ProviderAWS:
			return renderQueueAWS(plan), nil
		case ProviderGCP:
			return renderQueueGCP(plan), nil
		case ProviderUbicloud:
			return "", errUbicloudUnsupported(
				"managed-queue",
				"The Ubicloud Terraform provider exposes no managed queue resource; use SQS (aws) or "+
					"Pub/Sub (gcp).")
		}
	case KindStream:
		switch plan.Provider {
		case ProviderAWS:
			return renderStreamAWS(plan), nil
		case ProviderGCP:
			return renderStreamGCP(plan), nil
		case ProviderUbicloud:
			return "", errUbicloudUnsupported(
				"event-streaming",
				"The Ubicloud Terraform provider exposes no event-streaming resource; use Kinesis (aws) "+
					"or Pub/Sub (gcp).")
		}
	}
	return "", fmt.Errorf("render: unsupported provider %q for messaging kind %q", plan.Provider, plan.Kind)
}

func renderQueueAWS(p MessagingPlan) string {
	name := tfName(p.Name)
	queueName := p.Name
	if p.FIFO {
		queueName += ".fifo"
	}
	var b strings.Builder
	// Optional dead-letter queue when a redrive policy is requested.
	if p.MaxReceiveCount > 0 {
		dlqName := name + "_dlq"
		dlq := p.Name + "-dlq"
		if p.FIFO {
			dlq += ".fifo"
		}
		fmt.Fprintf(&b, "resource \"aws_sqs_queue\" %q {\n", dlqName)
		fmt.Fprintf(&b, "  name                      = %q\n", dlq)
		if p.FIFO {
			b.WriteString("  fifo_queue                = true\n")
		}
		b.WriteString("  sqs_managed_sse_enabled   = true\n")
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	fmt.Fprintf(&b, "resource \"aws_sqs_queue\" %q {\n", name)
	fmt.Fprintf(&b, "  name                      = %q\n", queueName)
	if p.FIFO {
		b.WriteString("  fifo_queue                = true\n")
		b.WriteString("  content_based_deduplication = true\n")
	}
	if p.VisibilityTimeoutSeconds > 0 {
		fmt.Fprintf(&b, "  visibility_timeout_seconds = %d\n", p.VisibilityTimeoutSeconds)
	}
	// SECURE BY DEFAULT: server-side encryption (SSE-SQS).
	b.WriteString("  sqs_managed_sse_enabled   = true\n")
	if p.MaxReceiveCount > 0 {
		fmt.Fprintf(&b, "  redrive_policy = jsonencode({ deadLetterTargetArn = aws_sqs_queue.%s_dlq.arn, maxReceiveCount = %d })\n", name, p.MaxReceiveCount)
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderQueueGCP(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// A queue on GCP is a topic + a pull subscription (the durable backlog).
	fmt.Fprintf(&b, "resource \"google_pubsub_topic\" %q {\n", name)
	fmt.Fprintf(&b, "  name = \"%s-topic\"\n", name)
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"google_pubsub_subscription\" %q {\n", name)
	fmt.Fprintf(&b, "  name  = %q\n", name)
	fmt.Fprintf(&b, "  topic = google_pubsub_topic.%s.id\n", name)
	ack := p.VisibilityTimeoutSeconds
	if ack <= 0 {
		ack = 10
	}
	fmt.Fprintf(&b, "  ack_deadline_seconds = %d\n", ack)
	if p.FIFO {
		b.WriteString("  enable_message_ordering = true\n")
	}
	if p.MaxReceiveCount > 0 {
		b.WriteString("  dead_letter_policy {\n")
		fmt.Fprintf(&b, "    dead_letter_topic     = google_pubsub_topic.%s.id\n", name)
		fmt.Fprintf(&b, "    max_delivery_attempts = %d\n", p.MaxReceiveCount)
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderStreamAWS(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_kinesis_stream\" %q {\n", name)
	fmt.Fprintf(&b, "  name             = %q\n", p.Name)
	if p.RetentionHours > 0 {
		fmt.Fprintf(&b, "  retention_period = %d\n", p.RetentionHours)
	}
	// SECURE BY DEFAULT: KMS encryption at rest.
	b.WriteString("  encryption_type  = \"KMS\"\n")
	b.WriteString("  kms_key_id       = \"alias/aws/kinesis\"\n")
	if p.Shards > 0 {
		fmt.Fprintf(&b, "  shard_count      = %d\n", p.Shards)
	} else {
		// On-demand: no shard count, stream_mode_details ON_DEMAND.
		b.WriteString("  stream_mode_details {\n")
		b.WriteString("    stream_mode = \"ON_DEMAND\"\n")
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderStreamGCP(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// Pub/Sub is GCP's stream + bus; one topic is the stream (consumers attach
	// their own subscriptions). Google-managed encryption at rest by default.
	fmt.Fprintf(&b, "resource \"google_pubsub_topic\" %q {\n", name)
	fmt.Fprintf(&b, "  name = %q\n", p.Name)
	if p.RetentionHours > 0 {
		fmt.Fprintf(&b, "  message_retention_duration = \"%ds\"\n", p.RetentionHours*3600)
	}
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── dns-zone ─────────────────────────────────────────────────────────────────

// RenderDNSZoneHCL renders a DNSZonePlan into HCL.
func RenderDNSZoneHCL(plan DNSZonePlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderDNSAWS(plan), nil
	case ProviderGCP:
		return renderDNSGCP(plan), nil
	case ProviderDigitalOcean:
		return renderDNSDO(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"dns-zone",
			"The Ubicloud Terraform provider exposes no DNS-zone resource; manage DNS on Route53 (aws), "+
				"Cloud DNS (gcp), or DigitalOcean domains.")
	default:
		return "", fmt.Errorf("render: unsupported provider %q for dns-zone", plan.Provider)
	}
}

func renderDNSAWS(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_route53_zone\" %q {\n", name)
	fmt.Fprintf(&b, "  name = %q\n", p.Domain)
	if p.Private && p.NetworkName != "" {
		b.WriteString("  vpc {\n")
		fmt.Fprintf(&b, "    vpc_id = aws_vpc.%s.id\n", tfName(p.NetworkName))
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderDNSGCP(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_dns_managed_zone\" %q {\n", name)
	fmt.Fprintf(&b, "  name     = %q\n", name)
	// Cloud DNS requires a trailing dot on the dns_name.
	dns := p.Domain
	if !strings.HasSuffix(dns, ".") {
		dns += "."
	}
	fmt.Fprintf(&b, "  dns_name = %q\n", dns)
	if p.Private {
		b.WriteString("  visibility = \"private\"\n")
		if p.NetworkName != "" {
			b.WriteString("  private_visibility_config {\n")
			b.WriteString("    networks {\n")
			fmt.Fprintf(&b, "      network_url = google_compute_network.%s.id\n", tfName(p.NetworkName))
			b.WriteString("    }\n")
			b.WriteString("  }\n")
		}
	}
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderDNSDO(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"digitalocean_domain\" %q {\n", name)
	fmt.Fprintf(&b, "  name = %q\n", p.Domain)
	b.WriteString("}\n")
	return b.String()
}

// ── cdn-service ──────────────────────────────────────────────────────────────

// RenderCDNHCL renders a CDNPlan into HCL.
func RenderCDNHCL(plan CDNPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderCDNAWS(plan), nil
	case ProviderGCP:
		return renderCDNGCP(plan), nil
	case ProviderDigitalOcean:
		return renderCDNDO(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"cdn-service",
			"The Ubicloud Terraform provider exposes no CDN resource; use CloudFront (aws), Cloud CDN "+
				"(gcp), or the DigitalOcean CDN.")
	default:
		return "", fmt.Errorf("render: unsupported provider %q for cdn-service", plan.Provider)
	}
}

func renderCDNAWS(p CDNPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	originID := "pyxcloud-origin"
	fmt.Fprintf(&b, "resource \"aws_cloudfront_distribution\" %q {\n", name)
	b.WriteString("  enabled = true\n")
	b.WriteString("  origin {\n")
	fmt.Fprintf(&b, "    origin_id   = %q\n", originID)
	if p.OriginKind == CDNOriginObjectStorage && p.OriginName != "" {
		fmt.Fprintf(&b, "    domain_name = aws_s3_bucket.%s.bucket_regional_domain_name\n", tfName(p.OriginName))
		b.WriteString("    s3_origin_config {\n")
		b.WriteString("      origin_access_identity = \"\"\n")
		b.WriteString("    }\n")
	} else if p.OriginName != "" {
		fmt.Fprintf(&b, "    domain_name = aws_lb.%s.dns_name\n", tfName(p.OriginName)+"_lb")
		b.WriteString("    custom_origin_config {\n")
		b.WriteString("      http_port              = 80\n")
		b.WriteString("      https_port             = 443\n")
		b.WriteString("      origin_protocol_policy = \"https-only\"\n")
		b.WriteString("      origin_ssl_protocols   = [\"TLSv1.2\"]\n")
		b.WriteString("    }\n")
	}
	b.WriteString("  }\n")
	b.WriteString("  default_cache_behavior {\n")
	fmt.Fprintf(&b, "    target_origin_id       = %q\n", originID)
	// SECURE BY DEFAULT: redirect viewers to HTTPS.
	b.WriteString("    viewer_protocol_policy = \"redirect-to-https\"\n")
	b.WriteString("    allowed_methods        = [\"GET\", \"HEAD\"]\n")
	b.WriteString("    cached_methods         = [\"GET\", \"HEAD\"]\n")
	b.WriteString("    forwarded_values {\n")
	b.WriteString("      query_string = false\n")
	b.WriteString("      cookies { forward = \"none\" }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("  restrictions {\n")
	b.WriteString("    geo_restriction { restriction_type = \"none\" }\n")
	b.WriteString("  }\n")
	b.WriteString("  viewer_certificate {\n")
	b.WriteString("    cloudfront_default_certificate = true\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderCDNGCP(p CDNPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	if p.OriginKind == CDNOriginObjectStorage {
		fmt.Fprintf(&b, "resource \"google_compute_backend_bucket\" %q {\n", name)
		fmt.Fprintf(&b, "  name        = %q\n", name)
		if p.OriginName != "" {
			fmt.Fprintf(&b, "  bucket_name = google_storage_bucket.%s.name\n", tfName(p.OriginName))
		}
		b.WriteString("  enable_cdn  = true\n")
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(&b, "resource \"google_compute_backend_service\" %q {\n", name)
		fmt.Fprintf(&b, "  name        = %q\n", name)
		b.WriteString("  enable_cdn  = true\n")
		b.WriteString("  protocol    = \"HTTPS\"\n")
		b.WriteString("}\n")
	}
	return b.String()
}

func renderCDNDO(p CDNPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"digitalocean_cdn\" %q {\n", name)
	if p.OriginName != "" {
		fmt.Fprintf(&b, "  origin = digitalocean_spaces_bucket.%s.bucket_domain_name\n", tfName(p.OriginName))
	}
	b.WriteString("}\n")
	return b.String()
}

// ── waf-service ──────────────────────────────────────────────────────────────

// RenderWAFHCL renders a WAFPlan into HCL. DO never reaches here.
func RenderWAFHCL(plan WAFPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderWAFAWS(plan), nil
	case ProviderGCP:
		return renderWAFGCP(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"waf-service",
			"The Ubicloud Terraform provider exposes no WAF resource; use AWS WAFv2 or a GCP Cloud "+
				"Armor security policy.")
	default:
		return "", fmt.Errorf("render: unsupported provider %q for waf-service", plan.Provider)
	}
}

func renderWAFAWS(p WAFPlan) string {
	name := tfName(p.Name)
	scope := "REGIONAL"
	if p.Scope == WAFScopeCloudFront {
		scope = "CLOUDFRONT"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_wafv2_web_acl\" %q {\n", name)
	fmt.Fprintf(&b, "  name  = %q\n", p.Name)
	fmt.Fprintf(&b, "  scope = %q\n", scope)
	// SECURE BY DEFAULT: default allow, the AWS managed common rule set BLOCKs.
	b.WriteString("  default_action {\n")
	b.WriteString("    allow {}\n")
	b.WriteString("  }\n")
	b.WriteString("  rule {\n")
	b.WriteString("    name     = \"AWSManagedCommonRules\"\n")
	b.WriteString("    priority = 1\n")
	b.WriteString("    override_action {\n")
	b.WriteString("      none {}\n")
	b.WriteString("    }\n")
	b.WriteString("    statement {\n")
	b.WriteString("      managed_rule_group_statement {\n")
	b.WriteString("        name        = \"AWSManagedRulesCommonRuleSet\"\n")
	b.WriteString("        vendor_name = \"AWS\"\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("    visibility_config {\n")
	b.WriteString("      cloudwatch_metrics_enabled = true\n")
	b.WriteString("      metric_name                = \"common-rules\"\n")
	b.WriteString("      sampled_requests_enabled   = true\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("  visibility_config {\n")
	b.WriteString("    cloudwatch_metrics_enabled = true\n")
	fmt.Fprintf(&b, "    metric_name                = \"%s-waf\"\n", name)
	b.WriteString("    sampled_requests_enabled   = true\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderWAFGCP(p WAFPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_compute_security_policy\" %q {\n", name)
	fmt.Fprintf(&b, "  name = %q\n", name)
	// A preconfigured WAF rule (block known SQLi) + a default allow rule.
	b.WriteString("  rule {\n")
	b.WriteString("    action   = \"deny(403)\"\n")
	b.WriteString("    priority = 1000\n")
	b.WriteString("    match {\n")
	b.WriteString("      expr {\n")
	b.WriteString("        expression = \"evaluatePreconfiguredExpr('sqli-v33-stable')\"\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("  rule {\n")
	b.WriteString("    action   = \"allow\"\n")
	b.WriteString("    priority = 2147483647\n")
	b.WriteString("    match {\n")
	b.WriteString("      versioned_expr = \"SRC_IPS_V1\"\n")
	b.WriteString("      config {\n")
	b.WriteString("        src_ip_ranges = [\"*\"]\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("    description = \"default allow\"\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── managed-kubernetes ───────────────────────────────────────────────────────

// RenderKubernetesHCL renders a K8sPlan into HCL.
func RenderKubernetesHCL(plan K8sPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderK8sAWS(plan), nil
	case ProviderGCP:
		return renderK8sGCP(plan), nil
	case ProviderDigitalOcean:
		return renderK8sDO(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"managed-kubernetes",
			"The Ubicloud Terraform provider exposes no managed-Kubernetes resource; use EKS (aws), "+
				"GKE (gcp), or DOKS (digitalocean).")
	default:
		return "", fmt.Errorf("render: unsupported provider %q for managed-kubernetes", plan.Provider)
	}
}

func renderK8sAWS(p K8sPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	subnetList := func() string {
		labels := make([]string, 0, len(p.SubnetNames))
		for _, s := range p.SubnetNames {
			labels = append(labels, fmt.Sprintf("aws_subnet.%s.id", subnetResourceLabel(p.NetworkName, s)))
		}
		return strings.Join(labels, ", ")
	}()
	// The EKS cluster IAM role is provided out-of-band (a sibling iam component);
	// the macro component references it by a stable expected name.
	fmt.Fprintf(&b, "resource \"aws_eks_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name     = %q\n", p.Name)
	fmt.Fprintf(&b, "  role_arn = var.eks_cluster_role_arn\n")
	if p.Version != "" {
		fmt.Fprintf(&b, "  version  = %q\n", p.Version)
	}
	b.WriteString("  vpc_config {\n")
	if subnetList != "" {
		fmt.Fprintf(&b, "    subnet_ids              = [%s]\n", subnetList)
	}
	// SECURE BY DEFAULT: private endpoint access on, public access off.
	b.WriteString("    endpoint_private_access = true\n")
	b.WriteString("    endpoint_public_access  = false\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"aws_eks_node_group\" %q {\n", name+"_ng")
	fmt.Fprintf(&b, "  cluster_name    = aws_eks_cluster.%s.name\n", name)
	fmt.Fprintf(&b, "  node_group_name = \"%s-ng\"\n", p.Name)
	b.WriteString("  node_role_arn   = var.eks_node_role_arn\n")
	if subnetList != "" {
		fmt.Fprintf(&b, "  subnet_ids      = [%s]\n", subnetList)
	}
	fmt.Fprintf(&b, "  instance_types  = [%q]\n", p.NodeType)
	b.WriteString("  scaling_config {\n")
	fmt.Fprintf(&b, "    desired_size = %d\n", p.DesiredNodes)
	fmt.Fprintf(&b, "    min_size     = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "    max_size     = %d\n", p.MaxNodes)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderK8sGCP(p K8sPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_container_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name                     = %q\n", name)
	fmt.Fprintf(&b, "  location                 = %q\n", p.CSPRegion)
	if p.Version != "" {
		fmt.Fprintf(&b, "  min_master_version       = %q\n", p.Version)
	}
	// Separate node pool, so remove the default one.
	b.WriteString("  remove_default_node_pool = true\n")
	b.WriteString("  initial_node_count       = 1\n")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  network                  = google_compute_network.%s.id\n", tfName(p.NetworkName))
	}
	// SECURE BY DEFAULT: private nodes.
	b.WriteString("  private_cluster_config {\n")
	b.WriteString("    enable_private_nodes = true\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"google_container_node_pool\" %q {\n", name+"_np")
	fmt.Fprintf(&b, "  name     = \"%s-np\"\n", name)
	fmt.Fprintf(&b, "  cluster  = google_container_cluster.%s.id\n", name)
	fmt.Fprintf(&b, "  location = %q\n", p.CSPRegion)
	b.WriteString("  autoscaling {\n")
	fmt.Fprintf(&b, "    min_node_count = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "    max_node_count = %d\n", p.MaxNodes)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  initial_node_count = %d\n", p.DesiredNodes)
	b.WriteString("  node_config {\n")
	fmt.Fprintf(&b, "    machine_type = %q\n", p.NodeType)
	fmt.Fprintf(&b, "    labels = { pyxcloud = \"true\" }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderK8sDO(p K8sPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"digitalocean_kubernetes_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name    = %q\n", name)
	fmt.Fprintf(&b, "  region  = %q\n", p.CSPRegion)
	ver := p.Version
	if ver == "" {
		ver = "latest"
	}
	fmt.Fprintf(&b, "  version = %q\n", ver)
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_uuid = digitalocean_vpc.%s.id\n", tfName(p.NetworkName))
	}
	b.WriteString("  node_pool {\n")
	fmt.Fprintf(&b, "    name       = \"%s-pool\"\n", name)
	fmt.Fprintf(&b, "    size       = %q\n", p.NodeType)
	b.WriteString("    auto_scale = true\n")
	fmt.Fprintf(&b, "    min_nodes  = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "    max_nodes  = %d\n", p.MaxNodes)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// ── secrets-manager ──────────────────────────────────────────────────────────

// RenderSecretsHCL renders a SecretsPlan into HCL. DO never reaches here.
func RenderSecretsHCL(plan SecretsPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderSecretsAWS(plan), nil
	case ProviderGCP:
		return renderSecretsGCP(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"secrets-manager",
			"The Ubicloud Terraform provider exposes no secrets-manager resource; use AWS Secrets "+
				"Manager or GCP Secret Manager.")
	default:
		return "", fmt.Errorf("render: unsupported provider %q for secrets-manager", plan.Provider)
	}
}

func renderSecretsAWS(p SecretsPlan) string {
	name := tfName(p.Name)
	desc := asciiOnly(p.Description) // ASCII guard (AWS rejects non-ASCII).
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_secretsmanager_secret\" %q {\n", name)
	fmt.Fprintf(&b, "  name        = %q\n", p.Name)
	if desc != "" {
		fmt.Fprintf(&b, "  description = %q\n", desc)
	}
	// TEST-ONLY: recovery_window_in_days = 0 force-deletes the secret on destroy so
	// a just-created secret tears down cleanly (no 30-day soft-delete window that
	// would block re-creating the same name). Production keeps the default window.
	if p.ForceDestroy {
		b.WriteString("  recovery_window_in_days = 0\n")
	}
	// SECURE: KMS-encrypted with the AWS-managed key by default (no key declared).
	// The secret VALUE is intentionally NOT declared here (it would leak into state).
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	if p.RotationDays > 0 {
		fmt.Fprintf(&b, "\nresource \"aws_secretsmanager_secret_rotation\" %q {\n", name+"_rot")
		fmt.Fprintf(&b, "  secret_id           = aws_secretsmanager_secret.%s.id\n", name)
		b.WriteString("  rotation_lambda_arn = var.rotation_lambda_arn\n")
		b.WriteString("  rotation_rules {\n")
		fmt.Fprintf(&b, "    automatically_after_days = %d\n", p.RotationDays)
		b.WriteString("  }\n")
		b.WriteString("}\n")
	}
	return b.String()
}

func renderSecretsGCP(p SecretsPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_secret_manager_secret\" %q {\n", name)
	fmt.Fprintf(&b, "  secret_id = %q\n", p.Name)
	b.WriteString("  replication {\n")
	b.WriteString("    auto {}\n")
	b.WriteString("  }\n")
	if p.RotationDays > 0 {
		b.WriteString("  rotation {\n")
		fmt.Fprintf(&b, "    rotation_period = \"%ds\"\n", p.RotationDays*86400)
		// next_rotation_time is required with rotation_period; provided out-of-band.
		b.WriteString("    next_rotation_time = var.next_rotation_time\n")
		b.WriteString("  }\n")
		b.WriteString("  topics {\n")
		b.WriteString("    name = var.rotation_topic\n")
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── serverless-function ──────────────────────────────────────────────────────

// RenderServerlessHCL renders a ServerlessPlan into HCL.
func RenderServerlessHCL(plan ServerlessPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderServerlessAWS(plan), nil
	case ProviderGCP:
		return renderServerlessGCP(plan), nil
	case ProviderDigitalOcean:
		return renderServerlessDO(plan), nil
	case ProviderUbicloud:
		return "", errUbicloudUnsupported(
			"serverless-function",
			"The Ubicloud Terraform provider exposes no serverless-function resource; use Lambda (aws), "+
				"Cloud Functions (gcp), or DigitalOcean Functions.")
	default:
		return "", fmt.Errorf("render: unsupported provider %q for serverless-function", plan.Provider)
	}
}

func renderServerlessAWS(p ServerlessPlan) string {
	name := tfName(p.Name)
	artifact := p.SourceArtifact
	if artifact == "" {
		artifact = "function.zip"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_lambda_function\" %q {\n", name)
	fmt.Fprintf(&b, "  function_name = %q\n", p.Name)
	fmt.Fprintf(&b, "  runtime       = %q\n", p.ConcreteRuntime)
	fmt.Fprintf(&b, "  handler       = %q\n", p.Handler)
	fmt.Fprintf(&b, "  filename      = %q\n", artifact)
	fmt.Fprintf(&b, "  memory_size   = %d\n", p.MemoryMB)
	fmt.Fprintf(&b, "  timeout       = %d\n", p.TimeoutSeconds)
	// Execution role provided out-of-band (a sibling iam component).
	b.WriteString("  role          = var.lambda_role_arn\n")
	// PRIVATE BY DEFAULT: no function URL is declared (public invoke is opt-in).
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderServerlessGCP(p ServerlessPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"google_cloudfunctions2_function\" %q {\n", name)
	fmt.Fprintf(&b, "  name     = %q\n", name)
	fmt.Fprintf(&b, "  location = %q\n", p.CSPRegion)
	b.WriteString("  build_config {\n")
	fmt.Fprintf(&b, "    runtime     = %q\n", p.ConcreteRuntime)
	fmt.Fprintf(&b, "    entry_point = %q\n", p.Handler)
	b.WriteString("    source {\n")
	b.WriteString("      storage_source {\n")
	b.WriteString("        bucket = var.source_bucket\n")
	if p.SourceArtifact != "" {
		fmt.Fprintf(&b, "        object = %q\n", p.SourceArtifact)
	} else {
		b.WriteString("        object = var.source_object\n")
	}
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("  service_config {\n")
	fmt.Fprintf(&b, "    available_memory   = \"%dM\"\n", p.MemoryMB)
	fmt.Fprintf(&b, "    timeout_seconds    = %d\n", p.TimeoutSeconds)
	// PRIVATE BY DEFAULT: ingress restricted to internal traffic, no allUsers invoker.
	b.WriteString("    ingress_settings   = \"ALLOW_INTERNAL_ONLY\"\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  labels = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderServerlessDO(p ServerlessPlan) string {
	name := tfName(p.Name)
	src := p.SourceArtifact
	if src == "" {
		src = "/"
	}
	var b strings.Builder
	// DO App Platform Functions component. The source repo is provided out-of-band
	// via a variable; the function component carries the runtime + source dir.
	fmt.Fprintf(&b, "resource \"digitalocean_app\" %q {\n", name)
	b.WriteString("  spec {\n")
	fmt.Fprintf(&b, "    name   = %q\n", name)
	fmt.Fprintf(&b, "    region = %q\n", p.CSPRegion)
	b.WriteString("    function {\n")
	fmt.Fprintf(&b, "      name       = %q\n", name)
	fmt.Fprintf(&b, "      source_dir = %q\n", src)
	b.WriteString("      git {\n")
	b.WriteString("        repo_clone_url = var.function_repo_url\n")
	b.WriteString("        branch         = var.function_branch\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}
