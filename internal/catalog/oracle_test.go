package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// oracle_test.go is the wave-2 Oracle Cloud (OCI) test suite. It mirrors the
// wave-1 component tests: region/SKU/image/DB-class resolution against the
// embedded OCI snapshot, per-component shaping (Translate* -> Render*OCI), the
// secure-by-default invariants, the clean plan-time errors for unsupported
// components, and the managed-database data-safety guard on OCI. No OCI creds are
// needed: these are pure resolution + rendering unit tests (the fixture-level
// `terraform validate` round-trip lives in examples/*/oracle, plan-only).

const ociFrankfurt = "eu-frankfurt-1"

// ── catalog resolution ─────────────────────────────────────────────────────────

func TestOracleRegionResolution(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct{ region, want string }{
		{"Frankfurt", "eu-frankfurt-1"},
		{"Amsterdam", "eu-amsterdam-1"},
		{"London", "uk-london-1"},
		{"North Virginia", "us-ashburn-1"},
		{"Singapore", "ap-singapore-1"},
	}
	for _, c := range cases {
		row, err := cat.ResolveRegion(context.Background(), c.region, "oracle")
		if err != nil {
			t.Fatalf("%s: %v", c.region, err)
		}
		if row.CSPRegion != c.want {
			t.Errorf("%s: csp_region = %q, want %q", c.region, row.CSPRegion, c.want)
		}
		if row.CSP != "oci" {
			t.Errorf("%s: csp = %q, want oci", c.region, row.CSP)
		}
	}
}

func TestOracleProviderToCSP(t *testing.T) {
	t.Parallel()
	csp, ok := ProviderToCSP("oracle")
	if !ok || csp != "oci" {
		t.Fatalf("ProviderToCSP(oracle) = %q,%v; want oci,true", csp, ok)
	}
}

func TestOracleRegionMissingIsHardError(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Dublin has no OCI row in the snapshot -> hard plan-time error, never a fallback.
	_, err := cat.ResolveRegion(context.Background(), "Dublin", "oracle")
	if err == nil {
		t.Fatal("expected ErrRegionNotFound for Dublin/oracle, got nil")
	}
	var notFound ErrRegionNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %T, want ErrRegionNotFound", err)
	}
}

func TestOracleSKUResolution(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Exact x86_64 2/4 match in Frankfurt -> a VM.Standard.E4.Flex shape.
	row, err := cat.ResolveSKU(context.Background(), "oci", ociFrankfurt, "x86_64", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(row.Name, "VM.Standard.E4.Flex") {
		t.Errorf("x86_64 shape = %q, want VM.Standard.E4.Flex*", row.Name)
	}
	// arm64 selects an A1.Flex shape (Ampere).
	arm, err := cat.ResolveSKU(context.Background(), "oci", ociFrankfurt, "arm64", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(arm.Name, "VM.Standard.A1.Flex") {
		t.Errorf("arm64 shape = %q, want VM.Standard.A1.Flex*", arm.Name)
	}
}

func TestOracleSKUMissingIsHardError(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// No 99/99 shape -> hard error listing nearest, never a silent fallback.
	_, err := cat.ResolveSKU(context.Background(), "oci", ociFrankfurt, "x86_64", 99, 99)
	if err == nil {
		t.Fatal("expected ErrSKUNotFound for 99/99, got nil")
	}
	var nf ErrSKUNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("error = %T, want ErrSKUNotFound", err)
	}
}

func TestOracleImageResolution(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	img, err := cat.ResolveImage(context.Background(), "oci", ociFrankfurt, "ubuntu", "24.04", "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(img.Image, "ocid1.image.") {
		t.Errorf("image = %q, want an ocid1.image.* OCID", img.Image)
	}
}

// ── network ────────────────────────────────────────────────────────────────────

func TestOracleTranslateNetwork(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateNetwork(context.Background(), cat, NetworkSpec{
		Name: "production", Region: "Frankfurt", Provider: "oracle",
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24", "10.0.3.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != ociFrankfurt {
		t.Errorf("csp_region = %q, want %s", plan.CSPRegion, ociFrankfurt)
	}
	if plan.ResourceType != "oci_core_vcn" {
		t.Errorf("resource_type = %q, want oci_core_vcn", plan.ResourceType)
	}
	// OCI ADs are opaque: deriveZones carries the AD ORDINAL ("1","2","3").
	wantZones := []string{"1", "2", "3"}
	for i, s := range plan.Subnets {
		if s.Zone != wantZones[i] {
			t.Errorf("subnet %d zone = %q, want %q", i, s.Zone, wantZones[i])
		}
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_core_vcn"`)
	mustContain(t, hcl, `resource "oci_core_subnet"`)
	mustContain(t, hcl, "compartment_id = var.compartment_id")
	// PRIVATE BY DEFAULT: subnets prohibit auto-assigned public IPs.
	mustContain(t, hcl, "prohibit_public_ip_on_vnic = true")
}

// ── security-group ──────────────────────────────────────────────────────────────

func TestOracleTranslateSecurityGroup(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateSecurityGroup(context.Background(), cat, SecurityGroupSpec{
		Name: "production-web", Network: "production", Region: "Frankfurt", Provider: "oracle",
		Description: "web tier", Expose: []int{80, 443},
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, CIDRs: []string{"10.0.0.0/16"}},
			{Direction: "egress", Protocol: "all", CIDRs: []string{"0.0.0.0/0"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_core_network_security_group" {
		t.Errorf("resource_type = %q, want oci_core_network_security_group", plan.ResourceType)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_core_network_security_group"`)
	mustContain(t, hcl, `resource "oci_core_network_security_group_security_rule"`)
	// OCI uses IANA protocol numbers (6 = TCP) and uppercase direction.
	mustContain(t, hcl, `protocol                  = "6"`)
	mustContain(t, hcl, `direction                 = "INGRESS"`)
	mustContain(t, hcl, `direction                 = "EGRESS"`)
}

// ── virtual-machine ─────────────────────────────────────────────────────────────

func TestOracleTranslateVM(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateVM(context.Background(), cat, VMSpec{
		Name: "web", Region: "Frankfurt", Provider: "oracle",
		Architecture: "x86_64", CPU: 2, RAM: 4, OS: "ubuntu", Count: 1,
		Network: "production", Subnet: "production-subnet-1", SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_core_instance" {
		t.Errorf("resource_type = %q, want oci_core_instance", plan.ResourceType)
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_core_instance"`)
	mustContain(t, hcl, `data "oci_identity_availability_domains"`)
	// Flex shape sized by an explicit ocpu/memory shape_config from the catalog row.
	mustContain(t, hcl, "shape_config {")
	mustContain(t, hcl, "ocpus         = 2")
	mustContain(t, hcl, "memory_in_gbs = 4")
	// PRIVATE BY DEFAULT: no public IP on the VNIC.
	mustContain(t, hcl, "assign_public_ip = false")
}

// ── scale-group (instance pool + autoscaling) ────────────────────────────────────

func TestOracleTranslateScaleGroup(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateScaleGroup(context.Background(), cat, ScaleGroupSpec{
		Name: "web", Region: "Frankfurt", Provider: "oracle",
		Architecture: "x86_64", CPU: 2, RAM: 4, OS: "ubuntu",
		Min: 2, Max: 6, Desired: 3, Health: "elb",
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
		SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_core_instance_pool" {
		t.Errorf("resource_type = %q, want oci_core_instance_pool", plan.ResourceType)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_core_instance_configuration"`)
	mustContain(t, hcl, `resource "oci_core_instance_pool"`)
	mustContain(t, hcl, `resource "oci_autoscaling_auto_scaling_configuration"`)
	mustContain(t, hcl, "size                      = 3") // desired
	mustContain(t, hcl, "max     = 6")
	mustContain(t, hcl, "min     = 2")
}

// ── load-balancer ─────────────────────────────────────────────────────────────

func TestOracleTranslateLoadBalancer(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateLoadBalancer(context.Background(), cat, LoadBalancerSpec{
		Name: "web-lb", Region: "Frankfurt", Provider: "oracle",
		Listeners:   []LBListenerSpec{{Port: 80, Protocol: "http"}, {Port: 443, Protocol: "https"}},
		HealthCheck: LBHealthCheckSpec{Protocol: "http", Port: 80, Path: "/"},
		Stickiness:  true, TargetKind: "scale-group", TargetName: "web",
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
		SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_load_balancer_load_balancer" {
		t.Errorf("resource_type = %q, want oci_load_balancer_load_balancer", plan.ResourceType)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_load_balancer_load_balancer"`)
	mustContain(t, hcl, `resource "oci_load_balancer_backend_set"`)
	mustContain(t, hcl, `resource "oci_load_balancer_listener"`)
	// stickiness => an lb-cookie session persistence config.
	mustContain(t, hcl, "lb_cookie_session_persistence_configuration {")
	// one listener per declared port.
	mustContain(t, hcl, "port                     = 80")
	mustContain(t, hcl, "port                     = 443")
}

// ── managed-database (data-safety guard) ─────────────────────────────────────────

func TestOracleTranslateManagedDatabasePostgres(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan := oracleMDB(t, cat, "postgres", "15")
	if plan.ResourceType != "oci_psql_db_system" {
		t.Errorf("resource_type = %q, want oci_psql_db_system", plan.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_psql_db_system"`)
	mustContain(t, hcl, "is_regionally_durable = true")
	// HA -> >1 instance.
	mustContain(t, hcl, "instance_count = 2")
}

func TestOracleTranslateManagedDatabaseMySQL(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan := oracleMDB(t, cat, "mysql", "8.0")
	if plan.ResourceType != "oci_mysql_mysql_db_system" {
		t.Errorf("resource_type = %q, want oci_mysql_mysql_db_system", plan.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_mysql_mysql_db_system"`)
	mustContain(t, hcl, "is_highly_available = true")
}

// TestOracleMDBDataSafetyGuard proves the data-safety guard fires for OCI exactly
// as it does for the wave-1 providers: an encryption/engine/identifier/family flip
// on a LIVE OCI database is blocked at plan time (the post-incident interlock),
// while a fresh create and an in-place storage resize are allowed.
func TestOracleMDBDataSafetyGuard(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	base := oracleMDB(t, cat, "postgres", "15")

	// Fresh create (prior == nil): always safe.
	if err := CheckManagedDatabaseDataSafety(nil, &base); err != nil {
		t.Fatalf("fresh create should be safe, got %v", err)
	}

	// Encryption flip on a live OCI DB -> blocked.
	enc := base
	enc.Encrypted = !base.Encrypted
	if err := CheckManagedDatabaseDataSafety(&base, &enc); err == nil {
		t.Fatal("expected data-safety error on encryption flip, got nil")
	} else {
		var dse ErrDataSafetyForceReplace
		if !errors.As(err, &dse) {
			t.Fatalf("error = %T, want ErrDataSafetyForceReplace", err)
		}
		if dse.Provider != "oracle" {
			t.Errorf("violation provider = %q, want oracle", dse.Provider)
		}
	}

	// Engine change (postgres -> mysql) on a live DB -> blocked.
	eng := base
	eng.Engine = "mysql"
	if err := CheckManagedDatabaseDataSafety(&base, &eng); err == nil {
		t.Fatal("expected data-safety error on engine change, got nil")
	}

	// In-place storage resize -> allowed (not replacement-forcing).
	grow := base
	grow.StorageGB = base.StorageGB + 50
	if err := CheckManagedDatabaseDataSafety(&base, &grow); err != nil {
		t.Errorf("storage resize should be safe, got %v", err)
	}
}

func oracleMDB(t *testing.T, cat Catalog, engine, version string) ManagedDatabasePlan {
	t.Helper()
	plan, err := TranslateManagedDatabase(context.Background(), cat, ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: "oracle",
		Engine: engine, Version: version, CPU: 2, RAM: 16, StorageGB: 50,
		HA: true, Encrypted: true,
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	})
	if err != nil {
		t.Fatalf("%s mdb: %v", engine, err)
	}
	return plan
}

// ── object-storage (private by default) ──────────────────────────────────────────

func TestOracleTranslateObjectStorage(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateObjectStorage(context.Background(), cat, ObjectStorageSpec{
		Name: "app-assets", Region: "Frankfurt", Provider: "oracle", Versioning: true, Public: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_objectstorage_bucket" {
		t.Errorf("resource_type = %q, want oci_objectstorage_bucket", plan.ResourceType)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_objectstorage_bucket"`)
	// PRIVATE BY DEFAULT: NoPublicAccess unless explicitly public.
	mustContain(t, hcl, `access_type    = "NoPublicAccess"`)
	mustContain(t, hcl, `versioning     = "Enabled"`)
}

func TestOracleObjectStoragePublicIsOptIn(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateObjectStorage(context.Background(), cat, ObjectStorageSpec{
		Name: "public-assets", Region: "Frankfurt", Provider: "oracle", Public: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `access_type    = "ObjectRead"`)
}

// ── macro components ─────────────────────────────────────────────────────────────

func TestOracleTranslateCache(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateCache(context.Background(), cat, CacheSpec{
		Name: "sessions", Region: "Frankfurt", Provider: "oracle", MemoryGB: 2, HA: true,
		Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_redis_redis_cluster" {
		t.Errorf("resource_type = %q, want oci_redis_redis_cluster", plan.ResourceType)
	}
	hcl, err := RenderCacheHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_redis_redis_cluster"`)
	mustContain(t, hcl, "node_count         = 2") // HA
}

func TestOracleTranslateQueueAndStream(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	q, err := TranslateQueue(context.Background(), cat, QueueSpec{
		Name: "jobs", Region: "Frankfurt", Provider: "oracle", MaxReceiveCount: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.ResourceType != "oci_queue_queue" {
		t.Errorf("queue resource_type = %q, want oci_queue_queue", q.ResourceType)
	}
	qh, err := RenderMessagingHCL(q)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, qh, `resource "oci_queue_queue"`)
	mustContain(t, qh, "dead_letter_queue_delivery_count = 5")

	s, err := TranslateStream(context.Background(), cat, StreamSpec{
		Name: "events", Region: "Frankfurt", Provider: "oracle", RetentionHours: 48,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ResourceType != "oci_streaming_stream" {
		t.Errorf("stream resource_type = %q, want oci_streaming_stream", s.ResourceType)
	}
	sh, err := RenderMessagingHCL(s)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, sh, `resource "oci_streaming_stream"`)
	mustContain(t, sh, "retention_in_hours = 48")
}

func TestOracleTranslateDNSZone(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateDNSZone(context.Background(), cat, DNSZoneSpec{
		Name: "zone", Region: "Frankfurt", Provider: "oracle", Domain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_dns_zone" {
		t.Errorf("resource_type = %q, want oci_dns_zone", plan.ResourceType)
	}
	hcl, err := RenderDNSZoneHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_dns_zone"`)
}

func TestOracleTranslateWAF(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateWAF(context.Background(), cat, WAFSpec{
		Name: "appwaf", Region: "Frankfurt", Provider: "oracle", Scope: "regional",
		AssociateName: "web-lb",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_waf_web_app_firewall" {
		t.Errorf("resource_type = %q, want oci_waf_web_app_firewall", plan.ResourceType)
	}
	hcl, err := RenderWAFHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_waf_web_app_firewall_policy"`)
	mustContain(t, hcl, `resource "oci_waf_web_app_firewall"`)
}

func TestOracleTranslateKubernetes(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateKubernetes(context.Background(), cat, K8sSpec{
		Name: "cluster", Region: "Frankfurt", Provider: "oracle", Version: "1.30",
		Architecture: "x86_64", NodeCPU: 2, NodeRAM: 4,
		MinNodes: 1, MaxNodes: 4, DesiredNodes: 2,
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_containerengine_cluster" {
		t.Errorf("resource_type = %q, want oci_containerengine_cluster", plan.ResourceType)
	}
	hcl, err := RenderKubernetesHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_containerengine_cluster"`)
	mustContain(t, hcl, `resource "oci_containerengine_node_pool"`)
	// SECURE BY DEFAULT: private control-plane endpoint.
	mustContain(t, hcl, "is_public_ip_enabled = false")
	// CNI declared in its own block (matches the oci provider schema).
	mustContain(t, hcl, "cluster_pod_network_options {")
}

func TestOracleTranslateSecrets(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateSecrets(context.Background(), cat, SecretsSpec{
		Name: "db-password", Region: "Frankfurt", Provider: "oracle", Description: "app db pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_vault_secret" {
		t.Errorf("resource_type = %q, want oci_vault_secret", plan.ResourceType)
	}
	hcl, err := RenderSecretsHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_kms_vault"`)
	mustContain(t, hcl, `resource "oci_vault_secret"`)
	// The secret VALUE must never be rendered into state.
	if strings.Contains(hcl, "secret_content") {
		t.Error("secret content must not be rendered into HCL/state")
	}
}

func TestOracleTranslateServerless(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateServerless(context.Background(), cat, ServerlessSpec{
		Name: "api", Region: "Frankfurt", Provider: "oracle",
		Runtime: "python", RuntimeVersion: "3.12", Handler: "main.handler",
		MemoryMB: 256, TimeoutSeconds: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "oci_functions_function" {
		t.Errorf("resource_type = %q, want oci_functions_function", plan.ResourceType)
	}
	hcl, err := RenderServerlessHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, hcl, `resource "oci_functions_application"`)
	mustContain(t, hcl, `resource "oci_functions_function"`)
	mustContain(t, hcl, "memory_in_mbs  = 256")
}

// ── unsupported components (clean plan-time errors) ──────────────────────────────

func TestOracleCDNUnsupported(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	_, err := TranslateCDN(context.Background(), cat, CDNSpec{
		Name: "edge", Region: "Frankfurt", Provider: "oracle",
		OriginKind: "object-storage", OriginName: "assets",
	})
	if err == nil {
		t.Fatal("expected unsupported error for oracle cdn-service, got nil")
	}
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("error = %T, want ErrComponentUnsupported", err)
	}
	if unsup.Provider != "oracle" {
		t.Errorf("unsupported provider = %q, want oracle", unsup.Provider)
	}
}

func TestOracleWAFCloudFrontScopeUnsupported(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// OCI WAF attaches to a load balancer; a CloudFront-global scope is meaningless.
	_, err := TranslateWAF(context.Background(), cat, WAFSpec{
		Name: "g", Region: "Frankfurt", Provider: "oracle", Scope: "cloudfront",
	})
	if err == nil {
		t.Fatal("expected unsupported error for oracle waf cloudfront scope, got nil")
	}
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("error = %T, want ErrComponentUnsupported", err)
	}
}

// ── render dispatch rejects an unknown provider ──────────────────────────────────

func TestOracleRenderDispatchUnknownProvider(t *testing.T) {
	t.Parallel()
	// A plan with a bogus provider must error in every render dispatcher, never
	// silently emit nothing.
	if _, err := RenderHCL(NetworkPlan{Provider: "nope"}); err == nil {
		t.Error("RenderHCL: expected error for unknown provider")
	}
	if _, err := RenderVMHCL(VMPlan{Provider: "nope"}); err == nil {
		t.Error("RenderVMHCL: expected error for unknown provider")
	}
}

// mustContain fails the test if haystack does not contain needle, printing a
// trimmed view of the rendered HCL for debugging.
func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("rendered HCL missing %q\n--- HCL ---\n%s", needle, haystack)
	}
}
