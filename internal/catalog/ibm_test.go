package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ibm_test.go covers the wave-2 IBM Cloud (IBM-Cloud/ibm) provider: region
// resolution, per-component abstract→concrete shaping + rendering, the IBM
// managed-database data-safety guard, and the clean plan-time errors for the
// components IBM has no clean primitive for. Mirrors the wave-1 *_test.go style:
// catalog-driven resolution, structured plan assertions, render-string assertions
// for the key IBM resource names, and unsupported sentinels.

const ibmRegion = "Frankfurt" // -> csp_region eu-de

// ── region resolution ─────────────────────────────────────────────────────────

func TestIBMRegionResolution(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := map[string]string{
		"Frankfurt":     "eu-de",
		"London":        "eu-gb",
		"Dallas":        "us-south",
		"Tokyo":         "jp-tok",
		"Sydney":        "au-syd",
		"Washington DC": "us-east",
		"São Paulo":     "br-sao",
	}
	for region, want := range cases {
		row, err := cat.ResolveRegion(context.Background(), region, "ibm")
		if err != nil {
			t.Fatalf("%s/ibm: %v", region, err)
		}
		if row.CSP != "ibm" {
			t.Errorf("%s: csp = %q, want ibm", region, row.CSP)
		}
		if row.CSPRegion != want {
			t.Errorf("%s: csp_region = %q, want %q", region, row.CSPRegion, want)
		}
	}
}

func TestIBMRegionMissingIsHardError(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Atlantis is not a real region -> hard error, never a fallback.
	if _, err := cat.ResolveRegion(context.Background(), "Atlantis", "ibm"); err == nil {
		t.Fatal("expected hard error for Atlantis/ibm")
	}
	// A region that exists for AWS but not IBM (e.g. Dublin) is also a hard error.
	if _, err := cat.ResolveRegion(context.Background(), "Dublin", "ibm"); err == nil {
		t.Fatal("expected hard error for Dublin/ibm (no IBM region)")
	}
}

func TestProviderToCSPIncludesIBM(t *testing.T) {
	t.Parallel()
	csp, ok := ProviderToCSP("ibm")
	if !ok || csp != "ibm" {
		t.Fatalf("ProviderToCSP(ibm) = %q,%v; want ibm,true", csp, ok)
	}
}

// ── network ────────────────────────────────────────────────────────────────────

func TestIBMNetwork(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateNetwork(context.Background(), cat, NetworkSpec{
		Name: "prod", Region: ibmRegion, Provider: "ibm",
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24", "10.0.3.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_is_vpc" {
		t.Errorf("resource_type = %q, want ibm_is_vpc", plan.ResourceType)
	}
	// IBM VPC zones are <region>-<1|2|3>.
	wantZones := []string{"eu-de-1", "eu-de-2", "eu-de-3"}
	for i, s := range plan.Subnets {
		if s.Zone != wantZones[i] {
			t.Errorf("subnet %d zone = %q, want %q", i, s.Zone, wantZones[i])
		}
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource "ibm_is_vpc"`, `resource "ibm_is_subnet"`, `zone            = "eu-de-1"`, `ipv4_cidr_block = "10.0.1.0/24"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("network HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── security-group ─────────────────────────────────────────────────────────────

func TestIBMSecurityGroup(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateSecurityGroup(context.Background(), cat, SecurityGroupSpec{
		Name: "web-sg", Network: "prod", Region: ibmRegion, Provider: "ibm",
		Expose: []int{80, 443},
		Rules: []SecurityRule{
			{Direction: "egress", Protocol: "all", CIDRs: []string{"0.0.0.0/0"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_is_security_group" {
		t.Errorf("resource_type = %q, want ibm_is_security_group", plan.ResourceType)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ibm_is_security_group"`,
		`resource "ibm_is_security_group_rule"`,
		`direction = "inbound"`,
		`direction = "outbound"`,
		`protocol  = "tcp"`,
		"port_min  = 80",
		`remote    = "0.0.0.0/0"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("SG HCL missing %q\n%s", want, hcl)
		}
	}
	// IBM SGs DO support the "all" protocol (unlike DigitalOcean): the egress-all
	// rule must translate cleanly (no capability rejection).
}

// ── virtual-machine ──────────────────────────────────────────────────────────

func TestIBMVirtualMachine(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateVM(context.Background(), cat, VMSpec{
		Name: "web", Region: ibmRegion, Provider: "ibm",
		CPU: 2, RAM: 8, Count: 2,
		Network: "prod", Subnet: "prod-subnet-1", SecurityGroup: "web-sg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_is_instance" {
		t.Errorf("resource_type = %q, want ibm_is_instance", plan.ResourceType)
	}
	// 2 vCPU / 8 GiB resolves to the balanced bx2 profile (preferred family).
	if plan.InstanceType != "bx2-2x8" {
		t.Errorf("instance_type = %q, want bx2-2x8", plan.InstanceType)
	}
	if !strings.HasPrefix(plan.Image, "ibm-ubuntu-") {
		t.Errorf("image = %q, want an ibm- stock image", plan.Image)
	}
	if len(plan.Instances) != 2 {
		t.Fatalf("want 2 instances, got %d", len(plan.Instances))
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource "ibm_is_instance"`, `profile        = "bx2-2x8"`, "primary_network_interface {", `keys           = [var.ibm_ssh_key_id]`, `security_groups = [ibm_is_security_group.web-sg.id]`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("VM HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestIBMVMUnknownSizeIsHardError(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// 99 vCPU has no IBM profile -> hard error listing nearest, never a fallback.
	_, err := TranslateVM(context.Background(), cat, VMSpec{
		Region: ibmRegion, Provider: "ibm", CPU: 99, RAM: 999,
	})
	if err == nil {
		t.Fatal("expected ErrSKUNotFound for an unavailable IBM size")
	}
	var skuErr ErrSKUNotFound
	if !errors.As(err, &skuErr) {
		t.Errorf("want ErrSKUNotFound, got %T: %v", err, err)
	}
}

// ── scale-group (IBM IS supported, unlike DigitalOcean) ──────────────────────

func TestIBMScaleGroup(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateScaleGroup(context.Background(), cat, ScaleGroupSpec{
		Name: "web", Region: ibmRegion, Provider: "ibm",
		CPU: 2, RAM: 8, Min: 2, Max: 6, Desired: 3, Health: "elb",
		Network: "prod", Subnets: []string{"prod-subnet-1", "prod-subnet-2"}, SecurityGroup: "web-sg",
	})
	if err != nil {
		t.Fatalf("IBM scale-group must be supported (ibm_is_instance_group): %v", err)
	}
	if plan.ResourceType != "ibm_is_instance_group" {
		t.Errorf("resource_type = %q, want ibm_is_instance_group", plan.ResourceType)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ibm_is_instance_template"`,
		`resource "ibm_is_instance_group"`,
		`resource "ibm_is_instance_group_manager"`,
		`instance_count    = 3`,
		`max_membership_count = 6`,
		`min_membership_count = 2`,
		`manager_type         = "autoscale"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("ASG HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── load-balancer ────────────────────────────────────────────────────────────

func TestIBMLoadBalancer(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateLoadBalancer(context.Background(), cat, LoadBalancerSpec{
		Name: "web-lb", Region: ibmRegion, Provider: "ibm",
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}, {Port: 443, Protocol: "https"}},
		Stickiness: true, TargetKind: "scale-group", TargetName: "web",
		Network: "prod", Subnets: []string{"prod-subnet-1", "prod-subnet-2"}, SecurityGroup: "web-sg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_is_lb" {
		t.Errorf("resource_type = %q, want ibm_is_lb", plan.ResourceType)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ibm_is_lb"`,
		`resource "ibm_is_lb_pool"`,
		`resource "ibm_is_lb_listener"`,
		`type           = "public"`,
		`session_persistence_type = "source_ip"`,
		`port         = 80`,
		`port         = 443`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("LB HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── managed-database + data-safety guard ─────────────────────────────────────
// (boolPtr is defined in manageddatabase_test.go and reused here.)

func TestIBMManagedDatabase(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateManagedDatabase(context.Background(), cat, ManagedDatabaseSpec{
		Name: "app-db", Region: ibmRegion, Provider: "ibm",
		Engine: "postgres", CPU: 2, RAM: 8, StorageGB: 50, HA: true, Encrypted: true,
		Network: "prod", Subnets: []string{"prod-subnet-1", "prod-subnet-2"}, SecurityGroup: "web-sg",
		DeletionProtection: boolPtr(false), // test override for clean teardown
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_database" {
		t.Errorf("resource_type = %q, want ibm_database", plan.ResourceType)
	}
	if plan.DBClass != "icd-2x8" {
		t.Errorf("db_class = %q, want icd-2x8", plan.DBClass)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ibm_database"`,
		`service           = "databases-for-postgresql"`,
		`service_endpoints = "private"`,
		"allocation_mb = 8192",  // 8 GiB memory
		"allocation_mb = 51200", // 50 GiB disk
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("MDB HCL missing %q\n%s", want, hcl)
		}
	}
	// deletion_protection=false (test override) -> no prevent_destroy lifecycle.
	if strings.Contains(hcl, "prevent_destroy") {
		t.Errorf("MDB HCL should not have prevent_destroy when deletion_protection=false\n%s", hcl)
	}
}

func TestIBMManagedDatabaseMySQLService(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateManagedDatabase(context.Background(), cat, ManagedDatabaseSpec{
		Name: "app-db", Region: ibmRegion, Provider: "ibm",
		Engine: "mysql", CPU: 2, RAM: 4, StorageGB: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, _ := RenderManagedDatabaseHCL(plan)
	if !strings.Contains(hcl, `service           = "databases-for-mysql"`) {
		t.Errorf("mysql MDB HCL missing databases-for-mysql\n%s", hcl)
	}
}

func TestIBMManagedDatabaseDataSafetyGuard(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	base := ManagedDatabaseSpec{
		Name: "app-db", Region: ibmRegion, Provider: "ibm",
		Engine: "postgres", CPU: 2, RAM: 8, StorageGB: 50, Encrypted: true,
	}
	prior, err := TranslateManagedDatabase(context.Background(), cat, base)
	if err != nil {
		t.Fatal(err)
	}

	// In-place safe change (HA toggle) must pass the guard.
	safe := base
	safe.HA = true
	safePlan, _ := TranslateManagedDatabase(context.Background(), cat, safe)
	if err := CheckManagedDatabaseDataSafety(&prior, &safePlan); err != nil {
		t.Errorf("HA toggle should be in-place safe on IBM, guard blocked it: %v", err)
	}

	// Encryption flip on an EXISTING IBM DB forces replacement -> guard blocks it.
	enc := base
	enc.Encrypted = false
	encPlan, _ := TranslateManagedDatabase(context.Background(), cat, enc)
	err = CheckManagedDatabaseDataSafety(&prior, &encPlan)
	if err == nil {
		t.Fatal("encryption flip on an existing IBM DB must be blocked by the data-safety guard")
	}
	var dsErr ErrDataSafetyForceReplace
	if !errors.As(err, &dsErr) {
		t.Errorf("want ErrDataSafetyForceReplace, got %T: %v", err, err)
	}

	// Engine change also forces replacement -> blocked.
	eng := base
	eng.Engine = "mysql"
	engPlan, _ := TranslateManagedDatabase(context.Background(), cat, eng)
	if err := CheckManagedDatabaseDataSafety(&prior, &engPlan); err == nil {
		t.Fatal("engine change on an existing IBM DB must be blocked")
	}

	// Fresh create (prior nil) is always safe.
	if err := CheckManagedDatabaseDataSafety(nil, &prior); err != nil {
		t.Errorf("fresh IBM DB create should be safe, got %v", err)
	}
}

// ── object-storage (private by default) ──────────────────────────────────────

func TestIBMObjectStorage(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateObjectStorage(context.Background(), cat, ObjectStorageSpec{
		Name: "app-assets", Region: ibmRegion, Provider: "ibm", Versioning: true,
		ForceDestroy: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_cos_bucket" {
		t.Errorf("resource_type = %q, want ibm_cos_bucket", plan.ResourceType)
	}
	if plan.Public {
		t.Error("object-storage must be private by default")
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ibm_resource_instance"`,
		`service           = "cloud-object-storage"`,
		`resource "ibm_cos_bucket"`,
		`region_location      = "eu-de"`,
		"object_versioning {",
		"force_delete = true",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("object-storage HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── cache (Databases for Redis) ──────────────────────────────────────────────

func TestIBMCache(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateCache(context.Background(), cat, CacheSpec{
		Name: "sessions", Region: ibmRegion, Provider: "ibm", MemoryGB: 4, HA: true,
		Network: "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_database" {
		t.Errorf("resource_type = %q, want ibm_database", plan.ResourceType)
	}
	hcl, err := RenderCacheHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource "ibm_database"`, `service           = "databases-for-redis"`, `service_endpoints = "private"`, "allocation_mb = 4096"} {
		if !strings.Contains(hcl, want) {
			t.Errorf("cache HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── managed-kubernetes ───────────────────────────────────────────────────────

func TestIBMKubernetes(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateKubernetes(context.Background(), cat, K8sSpec{
		Name: "app-cluster", Region: ibmRegion, Provider: "ibm",
		Version: "1.30", NodeCPU: 4, NodeRAM: 16, MinNodes: 2, MaxNodes: 5, DesiredNodes: 3,
		Network: "prod", Subnets: []string{"prod-subnet-1", "prod-subnet-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_container_vpc_cluster" {
		t.Errorf("resource_type = %q, want ibm_container_vpc_cluster", plan.ResourceType)
	}
	hcl, err := RenderKubernetesHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ibm_container_vpc_cluster"`,
		`flavor            = "bx2-4x16"`,
		`kube_version      = "1.30"`,
		"zones {",
		`name      = "eu-de-1"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("k8s HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── dns-zone (private -> DNS Services, public -> CIS) ────────────────────────

func TestIBMDNSZonePrivateAndPublic(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	priv, err := TranslateDNSZone(context.Background(), cat, DNSZoneSpec{
		Name: "internal", Region: ibmRegion, Provider: "ibm", Domain: "internal.example.com",
		Private: true, Network: "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if priv.ResourceType != "ibm_dns_zone" {
		t.Errorf("private dns resource_type = %q, want ibm_dns_zone", priv.ResourceType)
	}
	hclPriv, _ := RenderDNSZoneHCL(priv)
	if !strings.Contains(hclPriv, `resource "ibm_dns_zone"`) || !strings.Contains(hclPriv, "instance_id = var.ibm_dns_instance_id") {
		t.Errorf("private dns HCL wrong\n%s", hclPriv)
	}

	pub, err := TranslateDNSZone(context.Background(), cat, DNSZoneSpec{
		Name: "public", Region: ibmRegion, Provider: "ibm", Domain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pub.ResourceType != "ibm_cis_domain" {
		t.Errorf("public dns resource_type = %q, want ibm_cis_domain", pub.ResourceType)
	}
	hclPub, _ := RenderDNSZoneHCL(pub)
	if !strings.Contains(hclPub, `resource "ibm_cis_domain"`) || !strings.Contains(hclPub, "cis_id = var.ibm_cis_id") {
		t.Errorf("public dns HCL wrong\n%s", hclPub)
	}
}

// ── waf (CIS, regional supported; cloudfront scope unsupported) ──────────────

func TestIBMWAFRegionalAndCloudfront(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateWAF(context.Background(), cat, WAFSpec{
		Name: "edge-waf", Region: ibmRegion, Provider: "ibm", Scope: "regional",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_cis_waf_group" {
		t.Errorf("waf resource_type = %q, want ibm_cis_waf_group", plan.ResourceType)
	}
	hcl, _ := RenderWAFHCL(plan)
	if !strings.Contains(hcl, `resource "ibm_cis_waf_group"`) || !strings.Contains(hcl, `mode       = "on"`) {
		t.Errorf("waf HCL wrong\n%s", hcl)
	}

	// cloudfront scope is AWS-specific -> clean unsupported error on IBM.
	_, err = TranslateWAF(context.Background(), cat, WAFSpec{
		Name: "edge-waf", Region: ibmRegion, Provider: "ibm", Scope: "cloudfront",
	})
	if err == nil {
		t.Fatal("expected unsupported error for IBM waf cloudfront scope")
	}
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Errorf("want ErrComponentUnsupported, got %T", err)
	}
}

// ── secrets-manager ──────────────────────────────────────────────────────────

func TestIBMSecrets(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateSecrets(context.Background(), cat, SecretsSpec{
		Name: "api-key", Region: ibmRegion, Provider: "ibm", Description: "API key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_sm_arbitrary_secret" {
		t.Errorf("resource_type = %q, want ibm_sm_arbitrary_secret", plan.ResourceType)
	}
	hcl, _ := RenderSecretsHCL(plan)
	for _, want := range []string{`resource "ibm_sm_arbitrary_secret"`, "instance_id = var.ibm_sm_instance_id", `region      = "eu-de"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("secrets HCL missing %q\n%s", want, hcl)
		}
	}
	// The secret VALUE must never be inlined into HCL (state-leak guard).
	if strings.Contains(hcl, "API key") && strings.Contains(hcl, "payload     = \"API key\"") {
		t.Error("secret value must not be inlined")
	}
}

// ── serverless (Code Engine) ─────────────────────────────────────────────────

func TestIBMServerless(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateServerless(context.Background(), cat, ServerlessSpec{
		Name: "api", Region: ibmRegion, Provider: "ibm", Runtime: "nodejs", MemoryMB: 256, TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_code_engine_app" {
		t.Errorf("resource_type = %q, want ibm_code_engine_app", plan.ResourceType)
	}
	hcl, _ := RenderServerlessHCL(plan)
	for _, want := range []string{`resource "ibm_code_engine_project"`, `resource "ibm_code_engine_app"`, "image_reference", "scale_memory_limit = \"256M\""} {
		if !strings.Contains(hcl, want) {
			t.Errorf("serverless HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── event-streaming (Event Streams) vs managed-queue (unsupported) ───────────

func TestIBMEventStreaming(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateStream(context.Background(), cat, StreamSpec{
		Name: "events", Region: ibmRegion, Provider: "ibm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "ibm_resource_instance" {
		t.Errorf("resource_type = %q, want ibm_resource_instance", plan.ResourceType)
	}
	hcl, _ := RenderMessagingHCL(plan)
	if !strings.Contains(hcl, `service           = "messagehub"`) {
		t.Errorf("stream HCL missing messagehub (Event Streams)\n%s", hcl)
	}
}

// ── unsupported components: managed-queue, cdn-service ───────────────────────

func TestIBMUnsupportedComponents(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()

	// managed-queue: IBM has no clean managed work-queue primitive -> clean error.
	if _, err := TranslateQueue(context.Background(), cat, QueueSpec{
		Name: "jobs", Region: ibmRegion, Provider: "ibm",
	}); err == nil {
		t.Error("IBM managed-queue should be a clean unsupported error")
	} else {
		var unsup ErrComponentUnsupported
		if !errors.As(err, &unsup) {
			t.Errorf("managed-queue: want ErrComponentUnsupported, got %T", err)
		}
		if !strings.Contains(err.Error(), "Event Streams") {
			t.Errorf("managed-queue error should point to Event Streams: %v", err)
		}
	}

	// cdn-service: IBM has no origin-fronting CDN distribution -> clean error.
	if _, err := TranslateCDN(context.Background(), cat, CDNSpec{
		Name: "cdn", Region: ibmRegion, Provider: "ibm", OriginKind: "object-storage", OriginName: "app-assets",
	}); err == nil {
		t.Error("IBM cdn-service should be a clean unsupported error")
	} else {
		var unsup ErrComponentUnsupported
		if !errors.As(err, &unsup) {
			t.Errorf("cdn-service: want ErrComponentUnsupported, got %T", err)
		}
	}
}
