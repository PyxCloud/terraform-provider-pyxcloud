package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// These tests cover the remaining wave-1 macro components (pd-TF-REST-LAMBDA):
// schema/translate shaping per provider, the clean unsupported-provider
// plan-time errors, and the rendered-HCL secure-by-default invariants. They
// mirror the existing component tests (objectstorage_test.go et al).

func ctx() context.Context { return context.Background() }

// ── cache ────────────────────────────────────────────────────────────────────

func TestTranslateCacheAllProviders(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		provider, wantRegion, wantType string
	}{
		{"aws", "eu-central-1", "aws_elasticache_replication_group"},
		{"gcp", "europe-west3", "google_redis_instance"},
		{"digitalocean", "fra1", "digitalocean_database_cluster"},
	}
	for _, c := range cases {
		plan, err := TranslateCache(ctx(), cat, CacheSpec{
			Name: "sessions", Region: "Frankfurt", Provider: c.provider, MemoryGB: 2, HA: true,
		})
		if err != nil {
			t.Fatalf("%s: %v", c.provider, err)
		}
		if plan.CSPRegion != c.wantRegion {
			t.Errorf("%s: csp_region = %q, want %q", c.provider, plan.CSPRegion, c.wantRegion)
		}
		if plan.ResourceType != c.wantType {
			t.Errorf("%s: resource_type = %q, want %q", c.provider, plan.ResourceType, c.wantType)
		}
		if plan.Engine != CacheEngineRedis {
			t.Errorf("%s: engine = %q, want redis", c.provider, plan.Engine)
		}
		if plan.NodeClass == "" {
			t.Errorf("%s: node class should be resolved", c.provider)
		}
	}
}

func TestCacheRejectsMemcached(t *testing.T) {
	t.Parallel()
	_, err := TranslateCache(ctx(), MustEmbedded(), CacheSpec{
		Region: "Frankfurt", Provider: "aws", Engine: "memcached", MemoryGB: 1,
	})
	if err == nil {
		t.Fatal("memcached (AWS-only exotic) should be rejected")
	}
}

func TestRenderCacheSecureByDefault(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// AWS: encryption in transit + at rest, multi-AZ when HA.
	plan, _ := TranslateCache(ctx(), cat, CacheSpec{Name: "sessions", Region: "Frankfurt", Provider: "aws", MemoryGB: 2, HA: true, Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"}, SecurityGroup: "app"})
	hcl, err := RenderCacheHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_elasticache_replication_group" "sessions"`,
		`transit_encryption_enabled = true`,
		`at_rest_encryption_enabled = true`,
		`multi_az_enabled           = true`,
		`aws_security_group.app.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws cache HCL missing %q:\n%s", want, hcl)
		}
	}
	// GCP private + TLS.
	gplan, _ := TranslateCache(ctx(), cat, CacheSpec{Name: "sessions", Region: "Frankfurt", Provider: "gcp", MemoryGB: 4, HA: true, Network: "production"})
	ghcl, _ := RenderCacheHCL(gplan)
	for _, want := range []string{`tier               = "STANDARD_HA"`, `connect_mode       = "PRIVATE_SERVICE_ACCESS"`, `transit_encryption_mode = "SERVER_AUTHENTICATION"`} {
		if !strings.Contains(ghcl, want) {
			t.Errorf("gcp cache HCL missing %q:\n%s", want, ghcl)
		}
	}
}

// ── managed-queue ────────────────────────────────────────────────────────────

func TestTranslateQueueAWSGCPandDOUnsupported(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	aws, err := TranslateQueue(ctx(), cat, QueueSpec{Name: "jobs", Region: "Frankfurt", Provider: "aws", FIFO: true, MaxReceiveCount: 5})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ResourceType != "aws_sqs_queue" {
		t.Errorf("aws queue type = %q", aws.ResourceType)
	}
	gcp, err := TranslateQueue(ctx(), cat, QueueSpec{Name: "jobs", Region: "Frankfurt", Provider: "gcp"})
	if err != nil {
		t.Fatal(err)
	}
	if gcp.ResourceType != "google_pubsub_subscription" {
		t.Errorf("gcp queue type = %q", gcp.ResourceType)
	}
	_, err = TranslateQueue(ctx(), cat, QueueSpec{Name: "jobs", Region: "Frankfurt", Provider: "digitalocean"})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("DO queue should be ErrComponentUnsupported, got %T: %v", err, err)
	}
	if unsup.Component != TypeManagedQueue || unsup.CSPRegion != "fra1" {
		t.Errorf("unsupported error mis-shaped: %+v", unsup)
	}
}

func TestRenderQueueAWSFIFOandDLQ(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateQueue(ctx(), MustEmbedded(), QueueSpec{Name: "jobs", Region: "Frankfurt", Provider: "aws", FIFO: true, MaxReceiveCount: 3, VisibilityTimeoutSeconds: 60})
	hcl, err := RenderMessagingHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name                      = "jobs.fifo"`,
		`fifo_queue                = true`,
		`sqs_managed_sse_enabled   = true`, // secure by default: SSE
		`visibility_timeout_seconds = 60`,
		`redrive_policy`,
		`resource "aws_sqs_queue" "jobs_dlq"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws queue HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestRenderQueueGCPTopicSubscription(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateQueue(ctx(), MustEmbedded(), QueueSpec{Name: "jobs", Region: "Frankfurt", Provider: "gcp", FIFO: true})
	hcl, _ := RenderMessagingHCL(plan)
	for _, want := range []string{`resource "google_pubsub_topic" "jobs"`, `resource "google_pubsub_subscription" "jobs"`, `enable_message_ordering = true`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("gcp queue HCL missing %q:\n%s", want, hcl)
		}
	}
}

// ── event-streaming ──────────────────────────────────────────────────────────

func TestTranslateStreamAWSGCPandDOUnsupported(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	aws, err := TranslateStream(ctx(), cat, StreamSpec{Name: "events", Region: "Frankfurt", Provider: "aws"})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ResourceType != "aws_kinesis_stream" {
		t.Errorf("aws stream type = %q", aws.ResourceType)
	}
	gcp, err := TranslateStream(ctx(), cat, StreamSpec{Name: "events", Region: "Frankfurt", Provider: "gcp"})
	if err != nil {
		t.Fatal(err)
	}
	if gcp.ResourceType != "google_pubsub_topic" {
		t.Errorf("gcp stream type = %q", gcp.ResourceType)
	}
	_, err = TranslateStream(ctx(), cat, StreamSpec{Name: "events", Region: "Frankfurt", Provider: "digitalocean"})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("DO stream should be ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestRenderStreamAWSOnDemandEncrypted(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateStream(ctx(), MustEmbedded(), StreamSpec{Name: "events", Region: "Frankfurt", Provider: "aws"})
	hcl, _ := RenderMessagingHCL(plan)
	for _, want := range []string{`resource "aws_kinesis_stream" "events"`, `encryption_type  = "KMS"`, `stream_mode = "ON_DEMAND"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws stream HCL missing %q:\n%s", want, hcl)
		}
	}
	// Provisioned mode when shards set.
	p2, _ := TranslateStream(ctx(), MustEmbedded(), StreamSpec{Name: "events", Region: "Frankfurt", Provider: "aws", Shards: 4})
	hcl2, _ := RenderMessagingHCL(p2)
	if !strings.Contains(hcl2, `shard_count      = 4`) {
		t.Errorf("provisioned stream should set shard_count:\n%s", hcl2)
	}
}

// ── dns-zone ─────────────────────────────────────────────────────────────────

func TestTranslateDNSZoneAllProviders(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct{ provider, wantType string }{
		{"aws", "aws_route53_zone"},
		{"gcp", "google_dns_managed_zone"},
		{"digitalocean", "digitalocean_domain"},
	}
	for _, c := range cases {
		plan, err := TranslateDNSZone(ctx(), cat, DNSZoneSpec{Name: "z", Region: "Frankfurt", Provider: c.provider, Domain: "example.com"})
		if err != nil {
			t.Fatalf("%s: %v", c.provider, err)
		}
		if plan.ResourceType != c.wantType {
			t.Errorf("%s: type = %q, want %q", c.provider, plan.ResourceType, c.wantType)
		}
	}
}

func TestDNSZonePrivateUnsupportedOnDO(t *testing.T) {
	t.Parallel()
	_, err := TranslateDNSZone(ctx(), MustEmbedded(), DNSZoneSpec{Name: "z", Region: "Frankfurt", Provider: "digitalocean", Domain: "internal.example.com", Private: true})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("DO private zone should be ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestDNSZoneRequiresDomain(t *testing.T) {
	t.Parallel()
	if _, err := TranslateDNSZone(ctx(), MustEmbedded(), DNSZoneSpec{Name: "z", Region: "Frankfurt", Provider: "aws"}); err == nil {
		t.Fatal("dns-zone without domain should error")
	}
}

func TestRenderDNSGCPTrailingDot(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateDNSZone(ctx(), MustEmbedded(), DNSZoneSpec{Name: "z", Region: "Frankfurt", Provider: "gcp", Domain: "example.com"})
	hcl, _ := RenderDNSZoneHCL(plan)
	if !strings.Contains(hcl, `dns_name = "example.com."`) {
		t.Errorf("Cloud DNS dns_name needs a trailing dot:\n%s", hcl)
	}
}

// ── cdn-service ──────────────────────────────────────────────────────────────

func TestTranslateCDNAllProviders(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	aws, _ := TranslateCDN(ctx(), cat, CDNSpec{Name: "edge", Region: "Frankfurt", Provider: "aws", OriginKind: "object-storage", OriginName: "assets"})
	if aws.ResourceType != "aws_cloudfront_distribution" {
		t.Errorf("aws cdn type = %q", aws.ResourceType)
	}
	gcp, _ := TranslateCDN(ctx(), cat, CDNSpec{Name: "edge", Region: "Frankfurt", Provider: "gcp", OriginKind: "object-storage", OriginName: "assets"})
	if gcp.ResourceType != "google_compute_backend_bucket" {
		t.Errorf("gcp cdn type = %q", gcp.ResourceType)
	}
	do, _ := TranslateCDN(ctx(), cat, CDNSpec{Name: "edge", Region: "Frankfurt", Provider: "digitalocean", OriginKind: "object-storage", OriginName: "assets"})
	if do.ResourceType != "digitalocean_cdn" {
		t.Errorf("do cdn type = %q", do.ResourceType)
	}
}

func TestCDNDOLoadBalancerOriginUnsupported(t *testing.T) {
	t.Parallel()
	_, err := TranslateCDN(ctx(), MustEmbedded(), CDNSpec{Name: "edge", Region: "Frankfurt", Provider: "digitalocean", OriginKind: "load-balancer", OriginName: "web"})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("DO CDN over an LB origin should be ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestRenderCDNAWSHTTPSRedirect(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateCDN(ctx(), MustEmbedded(), CDNSpec{Name: "edge", Region: "Frankfurt", Provider: "aws", OriginKind: "object-storage", OriginName: "assets"})
	hcl, _ := RenderCDNHCL(plan)
	if !strings.Contains(hcl, `viewer_protocol_policy = "redirect-to-https"`) {
		t.Errorf("CloudFront must redirect to HTTPS (secure default):\n%s", hcl)
	}
}

// ── waf-service ──────────────────────────────────────────────────────────────

func TestTranslateWAFAWSGCPandDOCloudflare(t *testing.T) {
	// pd-MIG-B2-WAF-CLOUDFLARE: DO waf now resolves to Cloudflare WAF (not an
	// error), superseding the degraded single-VM ModSecurity mitigation.
	t.Parallel()
	cat := MustEmbedded()
	aws, _ := TranslateWAF(ctx(), cat, WAFSpec{Name: "fw", Region: "Frankfurt", Provider: "aws"})
	if aws.ResourceType != "aws_wafv2_web_acl" {
		t.Errorf("aws waf type = %q", aws.ResourceType)
	}
	gcp, _ := TranslateWAF(ctx(), cat, WAFSpec{Name: "fw", Region: "Frankfurt", Provider: "gcp"})
	if gcp.ResourceType != "google_compute_security_policy" {
		t.Errorf("gcp waf type = %q", gcp.ResourceType)
	}
	// DigitalOcean has no managed WAF; resolves to Cloudflare WAF.
	do, err := TranslateWAF(ctx(), cat, WAFSpec{Name: "fw", Region: "Frankfurt", Provider: "digitalocean"})
	if err != nil {
		t.Fatalf("DO waf should succeed (Cloudflare WAF path), got: %v", err)
	}
	if do.ResourceType != "cloudflare_ruleset" {
		t.Errorf("DO waf resource_type = %q, want cloudflare_ruleset", do.ResourceType)
	}
	if !do.ViaCloudflare {
		t.Errorf("DO waf plan.ViaCloudflare = false, want true")
	}
}

func TestWAFCloudFrontScopeUnsupportedOnGCP(t *testing.T) {
	t.Parallel()
	_, err := TranslateWAF(ctx(), MustEmbedded(), WAFSpec{Name: "fw", Region: "Frankfurt", Provider: "gcp", Scope: "cloudfront"})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("GCP cloudfront-scope WAF should be unsupported, got %T: %v", err, err)
	}
}

func TestRenderWAFAWSManagedRules(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateWAF(ctx(), MustEmbedded(), WAFSpec{Name: "fw", Region: "Frankfurt", Provider: "aws"})
	hcl, _ := RenderWAFHCL(plan)
	for _, want := range []string{`resource "aws_wafv2_web_acl" "fw"`, `scope = "REGIONAL"`, `AWSManagedRulesCommonRuleSet`, `allow {}`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws waf HCL missing %q:\n%s", want, hcl)
		}
	}
}

// ── waf-service: Cloudflare WAF path (pd-MIG-B2-WAF-CLOUDFLARE) ─────────────

func TestTranslateWAFCloudflare_DOandLinode(t *testing.T) {
	// Both DigitalOcean and Linode have no managed WAF; they must resolve to the
	// Cloudflare WAF path (cloudflare_ruleset), not a single-VM ModSecurity
	// mitigation and not an ErrComponentUnsupported.
	t.Parallel()
	cat := MustEmbedded()
	for _, tc := range []struct{ provider, region string }{
		{"digitalocean", "Frankfurt"},
		{"linode", "Frankfurt"},
	} {
		plan, err := TranslateWAF(ctx(), cat, WAFSpec{Name: "edge-waf", Region: tc.region, Provider: tc.provider})
		if err != nil {
			t.Errorf("provider=%s: want Cloudflare WAF plan, got error: %v", tc.provider, err)
			continue
		}
		if plan.ResourceType != "cloudflare_ruleset" {
			t.Errorf("provider=%s: resource_type=%q, want cloudflare_ruleset", tc.provider, plan.ResourceType)
		}
		if !plan.ViaCloudflare {
			t.Errorf("provider=%s: ViaCloudflare=false, want true", tc.provider)
		}
	}
}

func TestRenderWAFCloudflareHCL(t *testing.T) {
	// The rendered HCL for the Cloudflare WAF path must be a cloudflare_ruleset
	// resource with the OWASP managed ruleset enabled.
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateWAF(ctx(), cat, WAFSpec{Name: "edge-waf", Region: "Frankfurt", Provider: "digitalocean"})
	if err != nil {
		t.Fatalf("TranslateWAF DO: %v", err)
	}
	hcl, err := RenderWAFHCL(plan)
	if err != nil {
		t.Fatalf("RenderWAFHCL: %v", err)
	}
	for _, want := range []string{
		`resource "cloudflare_ruleset" "edge-waf"`,
		`kind    = "zone"`,
		`phase   = "http_request_firewall_managed"`,
		`zone_id = var.cloudflare_zone_id`,
		`action = "execute"`,
		`# Cloudflare OWASP Core Ruleset`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Cloudflare WAF HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestWAFDOViaCloudflare_NativelySupported(t *testing.T) {
	// NativelySupported("waf-service", "digitalocean") must be true so assemble.go
	// does NOT take the single-VM ModSecurity mitigation path for DO WAF.
	t.Parallel()
	if !NativelySupported("waf-service", "digitalocean") {
		t.Error("waf-service should be NativelySupported on digitalocean (Cloudflare WAF path)")
	}
	if !NativelySupported("waf", "digitalocean") {
		t.Error("waf should be NativelySupported on digitalocean (Cloudflare WAF path)")
	}
	if !NativelySupported("waf-service", "linode") {
		t.Error("waf-service should be NativelySupported on linode (Cloudflare WAF path)")
	}
}

func TestAssembleWAFDOPinsCloudflareProvider(t *testing.T) {
	// An AssembleHCL call with a waf-service component on digitalocean must
	// produce a cloudflare_ruleset resource AND pin the cloudflare/cloudflare
	// provider source in the terraform required_providers block.
	t.Parallel()
	cat := MustEmbedded()
	docs, err := AssembleHCL(ctx(), cat, AssembleInput{
		Name: "prod", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "edge-waf", Type: "waf-service", WAF: &AssembleWAF{Scope: "regional"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL waf DO: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, `resource "cloudflare_ruleset"`) {
		t.Errorf("assembled HCL should contain cloudflare_ruleset:\n%s", all)
	}
	if !strings.Contains(all, `cloudflare/cloudflare`) {
		t.Errorf("assembled HCL should pin cloudflare/cloudflare provider:\n%s", all)
	}
}

// ── managed-kubernetes ───────────────────────────────────────────────────────

func TestTranslateKubernetesAllProviders(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// AWS virtual_machine catalog rows live in Dublin (eu-west-1); GCP/DO in
	// Frankfurt. The k8s node type reuses ResolveSKU, so use the region each
	// provider has SKU rows for.
	cases := []struct{ provider, region, wantType string }{
		{"aws", "Dublin", "aws_eks_cluster"},
		{"gcp", "Frankfurt", "google_container_cluster"},
		{"digitalocean", "Frankfurt", "digitalocean_kubernetes_cluster"},
	}
	for _, c := range cases {
		plan, err := TranslateKubernetes(ctx(), cat, K8sSpec{
			Name: "cluster", Region: c.region, Provider: c.provider, NodeCPU: 2, NodeRAM: 4, MinNodes: 1, MaxNodes: 3, DesiredNodes: 2,
		})
		if err != nil {
			t.Fatalf("%s: %v", c.provider, err)
		}
		if plan.ResourceType != c.wantType {
			t.Errorf("%s: type = %q, want %q", c.provider, plan.ResourceType, c.wantType)
		}
		if plan.NodeType == "" {
			t.Errorf("%s: node machine type should be catalog-resolved", c.provider)
		}
		if plan.MaxNodes != 3 || plan.DesiredNodes != 2 {
			t.Errorf("%s: bounds mis-resolved: %+v", c.provider, plan)
		}
	}
}

func TestRenderKubernetesSecureByDefault(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// AWS: private endpoint, public off, autoscaling node group. (Dublin = the
	// region the AWS virtual_machine catalog has SKU rows for.)
	plan, _ := TranslateKubernetes(ctx(), cat, K8sSpec{Name: "cluster", Region: "Dublin", Provider: "aws", NodeCPU: 2, NodeRAM: 4, MinNodes: 1, MaxNodes: 4, DesiredNodes: 2, Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"}})
	hcl, _ := RenderKubernetesHCL(plan)
	for _, want := range []string{`endpoint_private_access = true`, `endpoint_public_access  = false`, `resource "aws_eks_node_group"`, `max_size     = 4`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws eks HCL missing %q:\n%s", want, hcl)
		}
	}
	// DO: DOKS with auto_scale node pool.
	do, _ := TranslateKubernetes(ctx(), cat, K8sSpec{Name: "cluster", Region: "Frankfurt", Provider: "digitalocean", NodeCPU: 2, NodeRAM: 4, MinNodes: 1, MaxNodes: 5, DesiredNodes: 2, Network: "production"})
	dohcl, _ := RenderKubernetesHCL(do)
	for _, want := range []string{`resource "digitalocean_kubernetes_cluster"`, `auto_scale = true`, `max_nodes  = 5`} {
		if !strings.Contains(dohcl, want) {
			t.Errorf("doks HCL missing %q:\n%s", want, dohcl)
		}
	}
}

// ── secrets-manager ──────────────────────────────────────────────────────────

func TestTranslateSecretsAWSGCPandDOUnsupported(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	aws, _ := TranslateSecrets(ctx(), cat, SecretsSpec{Name: "db-pw", Region: "Frankfurt", Provider: "aws"})
	if aws.ResourceType != "aws_secretsmanager_secret" {
		t.Errorf("aws secrets type = %q", aws.ResourceType)
	}
	gcp, _ := TranslateSecrets(ctx(), cat, SecretsSpec{Name: "db-pw", Region: "Frankfurt", Provider: "gcp"})
	if gcp.ResourceType != "google_secret_manager_secret" {
		t.Errorf("gcp secrets type = %q", gcp.ResourceType)
	}
	_, err := TranslateSecrets(ctx(), cat, SecretsSpec{Name: "db-pw", Region: "Frankfurt", Provider: "digitalocean"})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("DO secrets should be ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestSecretsDescriptionASCIIGuard(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateSecrets(ctx(), MustEmbedded(), SecretsSpec{Name: "db-pw", Region: "Frankfurt", Provider: "aws", Description: "café — naïve"})
	if !IsASCII(plan.Description) {
		t.Errorf("secret description must be ASCII-sanitised, got %q", plan.Description)
	}
	hcl, _ := RenderSecretsHCL(plan)
	if !IsASCII(hcl) {
		t.Errorf("rendered secrets HCL must be ASCII:\n%s", hcl)
	}
	// The secret value must NOT appear anywhere in the rendered config.
	if strings.Contains(hcl, "secret_string") {
		t.Errorf("secret VALUE must never be rendered into state:\n%s", hcl)
	}
}

func TestRenderSecretsAWSForceDestroyOverride(t *testing.T) {
	t.Parallel()
	fd := true
	// Default (production): no recovery_window override.
	def, _ := TranslateSecrets(ctx(), MustEmbedded(), SecretsSpec{Name: "s", Region: "Frankfurt", Provider: "aws"})
	defHCL, _ := RenderSecretsHCL(def)
	if strings.Contains(defHCL, "recovery_window_in_days") {
		t.Errorf("production secret should keep the default recovery window:\n%s", defHCL)
	}
	// Test override: force-delete (recovery_window_in_days = 0) so teardown is clean.
	plan, _ := TranslateSecrets(ctx(), MustEmbedded(), SecretsSpec{Name: "s", Region: "Frankfurt", Provider: "aws", ForceDestroy: &fd})
	hcl, _ := RenderSecretsHCL(plan)
	if !strings.Contains(hcl, "recovery_window_in_days = 0") {
		t.Errorf("force-destroy secret should emit recovery_window_in_days = 0:\n%s", hcl)
	}
}

func TestRenderSecretsAWSRotation(t *testing.T) {
	t.Parallel()
	plan, _ := TranslateSecrets(ctx(), MustEmbedded(), SecretsSpec{Name: "db-pw", Region: "Frankfurt", Provider: "aws", RotationDays: 30})
	hcl, _ := RenderSecretsHCL(plan)
	for _, want := range []string{`resource "aws_secretsmanager_secret_rotation"`, `automatically_after_days = 30`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws secrets rotation HCL missing %q:\n%s", want, hcl)
		}
	}
}

// ── serverless-function ──────────────────────────────────────────────────────

func TestTranslateServerlessAllProviders(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		provider, wantType, wantRuntime string
	}{
		{"aws", "aws_lambda_function", "nodejs20.x"},
		{"gcp", "google_cloudfunctions2_function", "nodejs20"},
		{"digitalocean", "digitalocean_app", "node:20"},
	}
	for _, c := range cases {
		plan, err := TranslateServerless(ctx(), cat, ServerlessSpec{
			Name: "api", Region: "Frankfurt", Provider: c.provider, Runtime: "nodejs", RuntimeVersion: "20",
		})
		if err != nil {
			t.Fatalf("%s: %v", c.provider, err)
		}
		if plan.ResourceType != c.wantType {
			t.Errorf("%s: type = %q, want %q", c.provider, plan.ResourceType, c.wantType)
		}
		if plan.ConcreteRuntime != c.wantRuntime {
			t.Errorf("%s: concrete runtime = %q, want %q", c.provider, plan.ConcreteRuntime, c.wantRuntime)
		}
	}
}

func TestServerlessRejectsUnknownRuntime(t *testing.T) {
	t.Parallel()
	if _, err := TranslateServerless(ctx(), MustEmbedded(), ServerlessSpec{Name: "api", Region: "Frankfurt", Provider: "aws", Runtime: "cobol"}); err == nil {
		t.Fatal("unknown runtime should be rejected")
	}
}

func TestRenderServerlessPrivateByDefault(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// AWS: no function URL declared.
	aws, _ := TranslateServerless(ctx(), cat, ServerlessSpec{Name: "api", Region: "Frankfurt", Provider: "aws", Runtime: "python", RuntimeVersion: "3.12", MemoryMB: 256, TimeoutSeconds: 15})
	hcl, _ := RenderServerlessHCL(aws)
	for _, want := range []string{`resource "aws_lambda_function" "api"`, `runtime       = "python3.12"`, `memory_size   = 256`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws lambda HCL missing %q:\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, "aws_lambda_function_url") {
		t.Errorf("lambda must be private by default (no function URL):\n%s", hcl)
	}
	// GCP: internal-only ingress.
	gcp, _ := TranslateServerless(ctx(), cat, ServerlessSpec{Name: "api", Region: "Frankfurt", Provider: "gcp", Runtime: "go", RuntimeVersion: "1.22"})
	ghcl, _ := RenderServerlessHCL(gcp)
	if !strings.Contains(ghcl, `ingress_settings   = "ALLOW_INTERNAL_ONLY"`) {
		t.Errorf("cloud function must be internal-only by default:\n%s", ghcl)
	}
	// DO: app platform function component.
	do, _ := TranslateServerless(ctx(), cat, ServerlessSpec{Name: "api", Region: "Frankfurt", Provider: "digitalocean", Runtime: "nodejs"})
	dohcl, _ := RenderServerlessHCL(do)
	if !strings.Contains(dohcl, `resource "digitalocean_app" "api"`) || !strings.Contains(dohcl, "function {") {
		t.Errorf("DO serverless must use an app function component:\n%s", dohcl)
	}
}

// ── canonical type aliasing ──────────────────────────────────────────────────

func TestCanonicalMacroTypes(t *testing.T) {
	t.Parallel()
	checks := []struct {
		fn   func(string) (string, bool)
		in   string
		want string
	}{
		{CanonicalCacheType, "cache", TypeCache},
		{CanonicalQueueType, "message-queue", TypeManagedQueue},
		{CanonicalStreamType, "event-bus", TypeEventStreaming},
		{CanonicalDNSZoneType, "dns-zone", TypeDNSZone},
		{CanonicalCDNType, "cdn-service", TypeCDNService},
		{CanonicalWAFType, "waf-service", TypeWAFService},
		{CanonicalKubernetesType, "container-service", TypeManagedKubernetes},
		{CanonicalSecretsType, "secrets-manager", TypeSecretsManager},
		{CanonicalServerlessType, "lambda", TypeServerlessFunction},
	}
	for _, c := range checks {
		got, ok := c.fn(c.in)
		if !ok || got != c.want {
			t.Errorf("Canonical(%q) = (%q, %v), want (%q, true)", c.in, got, ok, c.want)
		}
	}
}

// TestMacroUnknownProviderRejected asserts every translate rejects an unknown
// provider with a clear error (defence in depth).
func TestMacroUnknownProviderRejected(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	if _, err := TranslateCache(ctx(), cat, CacheSpec{Region: "Frankfurt", Provider: "vultr", MemoryGB: 1}); err == nil {
		t.Error("cache: unknown provider should error")
	}
	if _, err := TranslateServerless(ctx(), cat, ServerlessSpec{Region: "Frankfurt", Provider: "vultr"}); err == nil {
		t.Error("serverless: unknown provider should error")
	}
	if _, err := TranslateKubernetes(ctx(), cat, K8sSpec{Region: "Frankfurt", Provider: "vultr", NodeCPU: 2, NodeRAM: 4}); err == nil {
		t.Error("kubernetes: unknown provider should error")
	}
}

// TestMacroRegionNotFound asserts an unresolvable region is a hard error across
// the family.
func TestMacroRegionNotFound(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	if _, err := TranslateDNSZone(ctx(), cat, DNSZoneSpec{Region: "Atlantis", Provider: "aws", Domain: "x.com"}); err == nil {
		t.Error("dns-zone: bad region should error")
	}
	if _, err := TranslateSecrets(ctx(), cat, SecretsSpec{Region: "Atlantis", Provider: "aws", Name: "x"}); err == nil {
		t.Error("secrets: bad region should error")
	}
}
