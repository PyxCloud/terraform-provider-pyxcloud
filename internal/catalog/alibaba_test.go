package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// alibaba_test.go is the wave-2 Alibaba Cloud (alicloud) test suite
// (pd-TF-PROVIDERS-WAVE2: alibaba). It exercises, per component:
//   - resolution against the embedded alicloud catalog snapshot (region/vm/os/mdb),
//   - per-component HCL shaping (the alicloud_* resources + secure-by-default),
//   - the documented catalog GAPS surfacing as clean errors (no Dublin region),
//   - the unsupported-on-alibaba errors (private dns-zone, cloudfront WAF), and
//   - the managed-database data-safety guard on alicloud (engine/encryption flips).
//
// No Alibaba creds are available, so these are pure unit tests on the structured
// plan + rendered HCL; the round-trip against `terraform validate`/`plan` lives in
// examples/*/alibaba (validate/plan-only, documented in those provider.tf files).

const aliProvider = ProviderAlibaba // "alicloud"

// ── catalog resolution + the documented region gap ───────────────────────────

func TestAlibabaResolveRegion(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	ctx := context.Background()

	// Frankfurt resolves to the authored eu-central-1 row.
	row, err := cat.ResolveRegion(ctx, "Frankfurt", aliProvider)
	if err != nil {
		t.Fatalf("Frankfurt/alicloud should resolve: %v", err)
	}
	if row.CSP != "alicloud" || row.CSPRegion != "eu-central-1" {
		t.Errorf("Frankfurt resolved to %q/%q, want alicloud/eu-central-1", row.CSP, row.CSPRegion)
	}

	// Documented GAP: Alibaba has no Ireland region, so Dublin must NOT resolve.
	if _, err := cat.ResolveRegion(ctx, "Dublin", aliProvider); err == nil {
		t.Fatal("Dublin/alicloud should be ErrRegionNotFound (documented gap), got nil")
	} else {
		var notFound ErrRegionNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("Dublin/alicloud: want ErrRegionNotFound, got %T: %v", err, err)
		}
	}
}

func TestAlibabaProvenanceEmbedded(t *testing.T) {
	t.Parallel()
	// The authored Alibaba snapshot / gap record must be embedded (build-time
	// invariant); it documents the ETL gap the loader CSV rows fill in.
	prov := AlibabaCatalogProvenance()
	if strings.TrimSpace(prov) == "" {
		t.Fatal("embedded Alibaba provenance snapshot is empty")
	}
	for _, want := range []string{"eu-central-1", "ap-southeast-1", "NO live PyxCloud ETL"} {
		if !strings.Contains(prov, want) {
			t.Errorf("provenance snapshot missing %q", want)
		}
	}
}

// ── network: alicloud_vpc + alicloud_vswitch (multi-zone) ─────────────────────

func TestAlibabaNetwork(t *testing.T) {
	t.Parallel()
	plan, err := TranslateNetwork(context.Background(), MustEmbedded(), NetworkSpec{
		Name: "production", Region: "Frankfurt", Provider: aliProvider,
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_vpc" {
		t.Errorf("resource_type = %q, want alicloud_vpc", plan.ResourceType)
	}
	// alicloud zones share the AWS shape: <region><a|b...> e.g. eu-central-1a.
	if len(plan.Subnets) != 2 || plan.Subnets[0].Zone != "eu-central-1a" || plan.Subnets[1].Zone != "eu-central-1b" {
		t.Fatalf("zones not derived multi-zone: %+v", plan.Subnets)
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_vpc" "production"`,
		`cidr_block = "10.0.0.0/16"`,
		`resource "alicloud_vswitch"`,
		`zone_id      = "eu-central-1a"`,
		`zone_id      = "eu-central-1b"`,
		`vpc_id       = alicloud_vpc.production.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("network HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── security-group: alicloud_security_group(+_rule) ──────────────────────────

func TestAlibabaSecurityGroup(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "web", Network: "production", Region: "Frankfurt", Provider: aliProvider,
		Description: "web tier", Expose: []int{80, 443},
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 8080, ToPort: 8080, SourceSG: "lb"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_security_group" {
		t.Errorf("resource_type = %q, want alicloud_security_group", plan.ResourceType)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_security_group" "web"`,
		`vpc_id              = alicloud_vpc.production.id`,
		`resource "alicloud_security_group_rule"`,
		`ip_protocol       = "tcp"`,
		`port_range        = "80/80"`,
		`source_security_group_id = alicloud_security_group.lb.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("SG HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAlibabaSecurityGroupRuleLimit(t *testing.T) {
	t.Parallel()
	// Over the 200-rules-per-direction alicloud cap -> hard plan-time error.
	rules := make([]SecurityRule, 0, alibabaRulesPerDirectionMax+1)
	for i := 0; i <= alibabaRulesPerDirectionMax; i++ {
		rules = append(rules, SecurityRule{Direction: "ingress", Protocol: "tcp", FromPort: 80, ToPort: 80, CIDRs: []string{"10.0.0.0/8"}})
	}
	_, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "web", Network: "production", Region: "Frankfurt", Provider: aliProvider, Rules: rules,
	})
	if err == nil || !strings.Contains(err.Error(), "Alibaba") {
		t.Fatalf("want Alibaba rule-limit error, got %v", err)
	}
}

// ── virtual-machine: alicloud_instance ───────────────────────────────────────

func TestAlibabaVM(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "web", Region: "Frankfurt", Provider: aliProvider,
		CPU: 2, RAM: 8, OS: "ubuntu", OSVersion: "24.04", Count: 2,
		Network: "production", Subnet: "production-subnet-1", SecurityGroup: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_instance" || plan.InstanceType == "" || plan.Image == "" {
		t.Fatalf("VM resolution wrong: type=%q sku=%q image=%q", plan.ResourceType, plan.InstanceType, plan.Image)
	}
	if len(plan.Instances) != 2 {
		t.Fatalf("want 2 instances, got %d", len(plan.Instances))
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_instance"`,
		`system_disk_category = "cloud_essd"`,
		`vswitch_id           = alicloud_vswitch.production_1.id`,
		`security_groups      = [alicloud_security_group.web.id]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("VM HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── scale-group: alicloud_ess_scaling_group + _scaling_configuration ──────────

func TestAlibabaScaleGroup(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "web", Region: "Frankfurt", Provider: aliProvider,
		CPU: 2, RAM: 8, OS: "ubuntu", OSVersion: "24.04",
		Min: 2, Max: 6, Desired: 3, Health: "elb",
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"}, SecurityGroup: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_ess_scaling_group" {
		t.Errorf("resource_type = %q, want alicloud_ess_scaling_group", plan.ResourceType)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_ess_scaling_group" "web_asg"`,
		`min_size           = 2`,
		`max_size           = 6`,
		`desired_capacity   = 3`,
		`vswitch_ids        = [alicloud_vswitch.production_1.id, alicloud_vswitch.production_2.id]`,
		`resource "alicloud_ess_scaling_configuration" "web_sc"`,
		`security_group_ids   = [alicloud_security_group.web.id]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("ASG HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── load-balancer: alicloud_alb_load_balancer + server_group + listener ───────

func TestAlibabaLoadBalancer(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Frankfurt", Provider: aliProvider,
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}, {Port: 443, Protocol: "https"}},
		Stickiness: true, TargetKind: "scale-group", TargetName: "web",
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"}, SecurityGroup: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_alb_load_balancer" {
		t.Errorf("resource_type = %q, want alicloud_alb_load_balancer", plan.ResourceType)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_alb_load_balancer" "web-lb_lb"`,
		`load_balancer_edition = "Basic"`,
		`address_type          = "Internet"`,
		`zone_mappings {`,
		`resource "alicloud_alb_server_group" "web-lb_sg"`,
		`sticky_session_enabled = true`,
		`resource "alicloud_alb_listener" "web-lb_listener_80"`,
		`resource "alicloud_alb_listener" "web-lb_listener_443"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("LB HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── managed-database: alicloud_db_instance + the data-safety guard ────────────

func translateAlibabaMDB(t *testing.T, spec ManagedDatabaseSpec) ManagedDatabasePlan {
	t.Helper()
	spec.Region = "Frankfurt"
	spec.Provider = aliProvider
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestAlibabaManagedDatabase(t *testing.T) {
	t.Parallel()
	plan := translateAlibabaMDB(t, ManagedDatabaseSpec{
		Name: "app-db", Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 50,
		HA: true, Encrypted: true,
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"}, SecurityGroup: "db",
	})
	if plan.ResourceType != "alicloud_db_instance" {
		t.Errorf("resource_type = %q, want alicloud_db_instance", plan.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_db_instance" "app-db"`,
		`engine               = "PostgreSQL"`,
		`category             = "HighAvailability"`,
		`tde_status           = "Enabled"`,
		`vswitch_id           = alicloud_vswitch.production_1.id`,
		`security_group_ids   = [alicloud_security_group.db.id]`,
		`deletion_protection  = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("MDB HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAlibabaManagedDatabaseDataSafetyGuard(t *testing.T) {
	t.Parallel()
	base := ManagedDatabaseSpec{
		Name: "app-db", Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 50,
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	}
	prior := translateAlibabaMDB(t, base)

	// Fresh create (prior nil) is always safe.
	if err := CheckManagedDatabaseDataSafety(nil, &prior); err != nil {
		t.Fatalf("fresh create should be safe, got %v", err)
	}

	// Flipping encryption on an EXISTING alicloud DB forces replacement -> blocked.
	encChanged := base
	encChanged.Encrypted = true
	next := translateAlibabaMDB(t, encChanged)
	err := CheckManagedDatabaseDataSafety(&prior, &next)
	if err == nil {
		t.Fatal("encryption flip on existing alicloud DB must be blocked by the data-safety guard")
	}
	var safety ErrDataSafetyForceReplace
	if !errors.As(err, &safety) {
		t.Fatalf("want ErrDataSafetyForceReplace, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("guard error should cite the encrypted attribute: %v", err)
	}

	// Cross-engine change also force-replaces.
	engChanged := base
	engChanged.Engine = "mysql"
	mysqlNext := translateAlibabaMDB(t, engChanged)
	if err := CheckManagedDatabaseDataSafety(&prior, &mysqlNext); err == nil {
		t.Fatal("engine change on existing alicloud DB must be blocked")
	}

	// An in-place storage increase is safe (passes the guard).
	grow := base
	grow.StorageGB = 100
	growNext := translateAlibabaMDB(t, grow)
	if err := CheckManagedDatabaseDataSafety(&prior, &growNext); err != nil {
		t.Errorf("storage increase should be safe, got %v", err)
	}
}

// ── object-storage: alicloud_oss_bucket + _acl (private by default) ───────────

func TestAlibabaObjectStoragePrivateByDefault(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "assets", Region: "Frankfurt", Provider: aliProvider, Versioning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_oss_bucket" || plan.Public {
		t.Fatalf("object-storage resolution wrong: type=%q public=%v", plan.ResourceType, plan.Public)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_oss_bucket" "assets"`,
		`sse_algorithm = "AES256"`,
		`status = "Enabled"`,
		`resource "alicloud_oss_bucket_acl" "assets"`,
		`acl    = "private"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("OSS HCL missing %q\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, `acl    = "public-read"`) {
		t.Errorf("private bucket must not emit public-read ACL\n%s", hcl)
	}
}

// ── macro components: cache / stream / queue / dns / cdn / waf / k8s / secrets / serverless ──

func TestAlibabaCache(t *testing.T) {
	t.Parallel()
	plan, err := TranslateCache(context.Background(), MustEmbedded(), CacheSpec{
		Name: "sessions", Region: "Frankfurt", Provider: aliProvider, MemoryGB: 4, HA: true,
		Network: "production", Subnets: []string{"production-subnet-1"}, SecurityGroup: "cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_kvstore_instance" {
		t.Errorf("resource_type = %q, want alicloud_kvstore_instance", plan.ResourceType)
	}
	hcl, err := RenderCacheHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_kvstore_instance" "sessions"`,
		`instance_type    = "Redis"`,
		`instance_class   = "redis.basic.large.default"`,
		`vswitch_id       = alicloud_vswitch.production_1.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("cache HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAlibabaStreamAndQueue(t *testing.T) {
	t.Parallel()
	stream, err := TranslateStream(context.Background(), MustEmbedded(), StreamSpec{
		Name: "events", Region: "Frankfurt", Provider: aliProvider, Shards: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stream.ResourceType != "alicloud_alikafka_instance" {
		t.Errorf("stream resource_type = %q, want alicloud_alikafka_instance", stream.ResourceType)
	}
	shcl, err := RenderMessagingHCL(stream)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource "alicloud_alikafka_instance" "events"`, `partition_num = 6`, `kms_key_id  = var.alikafka_kms_key_id`} {
		if !strings.Contains(shcl, want) {
			t.Errorf("stream HCL missing %q\n%s", want, shcl)
		}
	}

	queue, err := TranslateQueue(context.Background(), MustEmbedded(), QueueSpec{
		Name: "jobs", Region: "Frankfurt", Provider: aliProvider, VisibilityTimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queue.ResourceType != "alicloud_message_service_queue" {
		t.Errorf("queue resource_type = %q, want alicloud_message_service_queue", queue.ResourceType)
	}
	qhcl, err := RenderMessagingHCL(queue)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource "alicloud_message_service_queue" "jobs"`, `queue_name = "jobs"`, `visibility_timeout = 60`} {
		if !strings.Contains(qhcl, want) {
			t.Errorf("queue HCL missing %q\n%s", want, qhcl)
		}
	}
}

func TestAlibabaDNSZonePublicAndPrivateUnsupported(t *testing.T) {
	t.Parallel()
	// Public zone resolves + renders.
	plan, err := TranslateDNSZone(context.Background(), MustEmbedded(), DNSZoneSpec{
		Name: "z", Region: "Frankfurt", Provider: aliProvider, Domain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_alidns_domain" {
		t.Errorf("resource_type = %q, want alicloud_alidns_domain", plan.ResourceType)
	}
	hcl, err := RenderDNSZoneHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, `resource "alicloud_alidns_domain" "z"`) || !strings.Contains(hcl, `domain_name = "example.com"`) {
		t.Errorf("dns HCL wrong\n%s", hcl)
	}

	// PRIVATE zone is unsupported on alibaba -> clean ErrComponentUnsupported.
	_, err = TranslateDNSZone(context.Background(), MustEmbedded(), DNSZoneSpec{
		Name: "z", Region: "Frankfurt", Provider: aliProvider, Domain: "internal.example", Private: true, Network: "production",
	})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("private dns-zone on alibaba: want ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestAlibabaCDN(t *testing.T) {
	t.Parallel()
	plan, err := TranslateCDN(context.Background(), MustEmbedded(), CDNSpec{
		Name: "cdn", Region: "Frankfurt", Provider: aliProvider, OriginKind: "object-storage", OriginName: "assets",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_cdn_domain_new" {
		t.Errorf("resource_type = %q, want alicloud_cdn_domain_new", plan.ResourceType)
	}
	hcl, err := RenderCDNHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource "alicloud_cdn_domain_new" "cdn"`, `cdn_type    = "web"`, `type     = "oss"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("cdn HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAlibabaWAFRegionalAndCloudFrontUnsupported(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWAF(context.Background(), MustEmbedded(), WAFSpec{
		Name: "waf", Region: "Frankfurt", Provider: aliProvider, Scope: "regional",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_waf_domain" {
		t.Errorf("resource_type = %q, want alicloud_waf_domain", plan.ResourceType)
	}
	hcl, err := RenderWAFHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource "alicloud_waf_domain" "waf"`, `instance_id       = var.waf_instance_id`, `https_redirect    = "On"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("waf HCL missing %q\n%s", want, hcl)
		}
	}

	// cloudfront scope is AWS-specific -> unsupported on alibaba.
	_, err = TranslateWAF(context.Background(), MustEmbedded(), WAFSpec{
		Name: "waf", Region: "Frankfurt", Provider: aliProvider, Scope: "cloudfront",
	})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("cloudfront WAF on alibaba: want ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestAlibabaKubernetes(t *testing.T) {
	t.Parallel()
	plan, err := TranslateKubernetes(context.Background(), MustEmbedded(), K8sSpec{
		Name: "cluster", Region: "Frankfurt", Provider: aliProvider, Version: "1.30",
		NodeCPU: 2, NodeRAM: 8, MinNodes: 2, MaxNodes: 6, DesiredNodes: 3,
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_cs_managed_kubernetes" {
		t.Errorf("resource_type = %q, want alicloud_cs_managed_kubernetes", plan.ResourceType)
	}
	hcl, err := RenderKubernetesHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_cs_managed_kubernetes" "cluster"`,
		`vswitch_ids  = [alicloud_vswitch.production_1.id, alicloud_vswitch.production_2.id]`,
		`resource "alicloud_cs_kubernetes_node_pool" "cluster_np"`,
		`min_size = 2`,
		`max_size = 6`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("k8s HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAlibabaSecrets(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecrets(context.Background(), MustEmbedded(), SecretsSpec{
		Name: "api-key", Region: "Frankfurt", Provider: aliProvider, Description: "api key", RotationDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_kms_secret" {
		t.Errorf("resource_type = %q, want alicloud_kms_secret", plan.ResourceType)
	}
	hcl, err := RenderSecretsHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_kms_secret" "api-key"`,
		`secret_data = var.secret_data`,
		`enable_automatic_rotation = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("secrets HCL missing %q\n%s", want, hcl)
		}
	}
	// The secret VALUE must never be inlined (state-leak guard).
	if strings.Contains(hcl, `secret_data = "`) {
		t.Errorf("secret value must not be inlined\n%s", hcl)
	}
}

func TestAlibabaServerless(t *testing.T) {
	t.Parallel()
	plan, err := TranslateServerless(context.Background(), MustEmbedded(), ServerlessSpec{
		Name: "fn", Region: "Frankfurt", Provider: aliProvider, Runtime: "nodejs", RuntimeVersion: "20",
		Handler: "index.handler", MemoryMB: 256, TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "alicloud_fcv3_function" {
		t.Errorf("resource_type = %q, want alicloud_fcv3_function", plan.ResourceType)
	}
	hcl, err := RenderServerlessHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "alicloud_fcv3_function" "fn"`,
		`runtime       = "nodejs20"`,
		`handler       = "index.handler"`,
		`memory_size   = 256`,
		`internet_access = false`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("serverless HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── unsupported provider on a renderer with no Alibaba dispatch path ──────────

func TestAlibabaUnsupportedProviderRender(t *testing.T) {
	t.Parallel()
	// A plan with a bogus provider must produce a clean unsupported error, never a
	// panic or invented resource.
	if _, err := RenderHCL(NetworkPlan{Provider: "fakecloud"}); err == nil {
		t.Fatal("want unsupported-provider error for fakecloud")
	}
}
