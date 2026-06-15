package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stackitCtx is a tiny ctx helper local to the StackIt tests.
func stackitCtx() context.Context { return context.Background() }

// ── catalog: provider map + region / SKU / DB-class resolution ───────────────

func TestStackItProviderToCSP(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"stackit", "StackIt", "  stackit  "} {
		csp, ok := ProviderToCSP(in)
		if !ok || csp != "stackit" {
			t.Errorf("ProviderToCSP(%q) = (%q,%v), want (stackit,true)", in, csp, ok)
		}
	}
}

func TestStackItResolveRegion(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	row, err := cat.ResolveRegion(stackitCtx(), "Frankfurt", "stackit")
	if err != nil {
		t.Fatalf("ResolveRegion(Frankfurt,stackit): %v", err)
	}
	if row.CSP != "stackit" || row.CSPRegion != "eu01" {
		t.Fatalf("got csp=%q csp_region=%q, want stackit/eu01", row.CSP, row.CSPRegion)
	}
	// Unknown region for stackit -> hard error, never a fallback.
	if _, err := cat.ResolveRegion(stackitCtx(), "Atlantis", "stackit"); err == nil {
		t.Fatal("expected ErrRegionNotFound for Atlantis/stackit")
	} else {
		var nf ErrRegionNotFound
		if !errors.As(err, &nf) {
			t.Fatalf("expected ErrRegionNotFound, got %T", err)
		}
	}
}

func TestStackItResolveSKUAndImage(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	sku, err := cat.ResolveSKU(stackitCtx(), "stackit", "eu01", ArchX8664, 2, 8)
	if err != nil {
		t.Fatalf("ResolveSKU 2/8: %v", err)
	}
	if sku.Name != "g1.2" {
		t.Fatalf("got flavor %q, want g1.2", sku.Name)
	}
	// Missing size is a hard error listing nearest sizes (never a silent fallback).
	if _, err := cat.ResolveSKU(stackitCtx(), "stackit", "eu01", ArchX8664, 99, 99); err == nil {
		t.Fatal("expected ErrSKUNotFound for an unavailable size")
	}
	img, err := cat.ResolveImage(stackitCtx(), "stackit", "eu01", OSUbuntu, "24.04", ArchX8664)
	if err != nil {
		t.Fatalf("ResolveImage ubuntu 24.04: %v", err)
	}
	if img.Image != "ubuntu-24.04" {
		t.Fatalf("got image %q, want ubuntu-24.04", img.Image)
	}
}

func TestStackItResolveDBClass(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	row, err := cat.ResolveDBClass(stackitCtx(), "stackit", "eu01", DBEnginePostgres, 2, 8)
	if err != nil {
		t.Fatalf("ResolveDBClass pg 2/8: %v", err)
	}
	if row.Name != "g1.2" {
		t.Fatalf("got class %q, want g1.2", row.Name)
	}
	if _, err := cat.ResolveDBClass(stackitCtx(), "stackit", "eu01", DBEnginePostgres, 64, 256); err == nil {
		t.Fatal("expected ErrDBClassNotFound for an unavailable size")
	}
}

// ── supported renderers (translate -> render round-trip) ─────────────────────

func TestStackItRenderNetwork(t *testing.T) {
	t.Parallel()
	plan, err := TranslateNetwork(stackitCtx(), MustEmbedded(), NetworkSpec{
		Name: "production", Region: "Frankfurt", Provider: "stackit",
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "stackit_network" || plan.CSPRegion != "eu01" {
		t.Fatalf("plan = %+v", plan)
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "stackit_network" "production"`,
		`project_id  = var.stackit_project_id`,
		`region      = "eu01"`,
		`ipv4_prefix = "10.0.1.0/24"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("network HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestStackItRenderSG(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(stackitCtx(), MustEmbedded(), SecurityGroupSpec{
		Name: "web", Region: "Frankfurt", Provider: "stackit", Description: "web tier",
		Expose: []int{443},
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 8080, ToPort: 8080, SourceSG: "lb"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "stackit_security_group" "web"`,
		`resource "stackit_security_group_rule" "web_rule_1"`,
		`protocol = { name = "tcp" }`,
		`port_range = { min = 443, max = 443 }`,
		`remote_security_group_id = stackit_security_group.lb.security_group_id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("SG HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestStackItRenderVM(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(stackitCtx(), MustEmbedded(), VMSpec{
		Name: "web", Region: "Frankfurt", Provider: "stackit",
		CPU: 2, RAM: 8, OS: "ubuntu", Count: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "stackit_server" || plan.InstanceType != "g1.2" {
		t.Fatalf("plan = %+v", plan)
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "stackit_server" "web-1"`,
		`resource "stackit_server" "web-2"`,
		`machine_type = "g1.2"`,
		`source_id   = "ubuntu-24.04"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("VM HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestStackItRenderMDBPostgresAndMariaDB(t *testing.T) {
	t.Parallel()
	pg, err := TranslateManagedDatabase(stackitCtx(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: "stackit",
		Engine: "postgres", CPU: 2, RAM: 8, StorageGB: 20, HA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pg.ResourceType != "stackit_postgresflex_instance" {
		t.Fatalf("postgres -> %q, want stackit_postgresflex_instance", pg.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(pg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "stackit_postgresflex_instance" "app-db"`,
		`replicas        = 3`, // HA -> replication
		"cpu = 2",
		"ram = 8",
		`size  = 20`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("MDB pg HCL missing %q\n%s", want, hcl)
		}
	}

	my, err := TranslateManagedDatabase(stackitCtx(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: "stackit",
		Engine: "mysql", CPU: 2, RAM: 8, StorageGB: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if my.ResourceType != "stackit_mariadb_instance" {
		t.Fatalf("mysql -> %q, want stackit_mariadb_instance", my.ResourceType)
	}
}

func TestStackItRenderObjectStoragePrivate(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(stackitCtx(), MustEmbedded(), ObjectStorageSpec{
		Name: "assets", Region: "Frankfurt", Provider: "stackit",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, `resource "stackit_objectstorage_bucket"`) {
		t.Errorf("object-storage HCL missing bucket resource\n%s", hcl)
	}
	// A public bucket is rejected at translate time (no public ACL on StackIt).
	if _, err := TranslateObjectStorage(stackitCtx(), MustEmbedded(), ObjectStorageSpec{
		Name: "assets", Region: "Frankfurt", Provider: "stackit", Public: true,
	}); err == nil {
		t.Fatal("expected error for a public StackIt bucket")
	}
}

func TestStackItRenderKubernetes(t *testing.T) {
	t.Parallel()
	plan, err := TranslateKubernetes(stackitCtx(), MustEmbedded(), K8sSpec{
		Name: "cluster", Region: "Frankfurt", Provider: "stackit",
		Version: "1.30", NodeCPU: 2, NodeRAM: 8, MinNodes: 1, MaxNodes: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "stackit_ske_cluster" || plan.NodeType != "g1.2" {
		t.Fatalf("plan = %+v", plan)
	}
	hcl, err := RenderKubernetesHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "stackit_ske_cluster" "cluster"`,
		`kubernetes_version_min = "1.30"`,
		`machine_type       = "g1.2"`,
		`minimum            = 1`,
		`maximum            = 3`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("SKE HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestStackItRenderDNSZone(t *testing.T) {
	t.Parallel()
	plan, err := TranslateDNSZone(stackitCtx(), MustEmbedded(), DNSZoneSpec{
		Name: "zone", Region: "Frankfurt", Provider: "stackit", Domain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderDNSZoneHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "stackit_dns_zone" "zone"`,
		`dns_name   = "example.com"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DNS HCL missing %q\n%s", want, hcl)
		}
	}
	// A private zone is unsupported on StackIt.
	if _, err := TranslateDNSZone(stackitCtx(), MustEmbedded(), DNSZoneSpec{
		Name: "z", Region: "Frankfurt", Provider: "stackit", Domain: "x.com", Private: true,
	}); err == nil {
		t.Fatal("expected error for a private StackIt DNS zone")
	}
}

func TestStackItRenderSecrets(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecrets(stackitCtx(), MustEmbedded(), SecretsSpec{
		Name: "app-secrets", Region: "Frankfurt", Provider: "stackit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "stackit_secretsmanager_instance" {
		t.Fatalf("plan = %+v", plan)
	}
	hcl, err := RenderSecretsHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, `resource "stackit_secretsmanager_instance" "app-secrets"`) {
		t.Errorf("secrets HCL missing instance\n%s", hcl)
	}
}

func TestStackItRenderLoadBalancer(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(stackitCtx(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Frankfurt", Provider: "stackit",
		Listeners:  []LBListenerSpec{{Port: 443, Protocol: "tcp"}},
		TargetKind: "vm", TargetName: "web", Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "stackit_loadbalancer" {
		t.Fatalf("plan = %+v", plan)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "stackit_loadbalancer" "web-lb"`,
		`port        = 443`,
		`protocol    = "PROTOCOL_TCP"`,
		`network_id = stackit_network.production.network_id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("LB HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── unsupported components -> clean plan-time errors (never invented) ─────────

func TestStackItUnsupportedComponents(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()

	// scale-group: no native VM autoscaling primitive.
	if _, err := TranslateScaleGroup(stackitCtx(), cat, ScaleGroupSpec{
		Region: "Frankfurt", Provider: "stackit", CPU: 2, RAM: 8, Min: 1, Max: 3,
	}); err == nil {
		t.Error("scale-group: expected unsupported error on stackit")
	} else {
		var au ErrAutoscaleUnsupported
		if !errors.As(err, &au) {
			t.Errorf("scale-group: expected ErrAutoscaleUnsupported, got %T", err)
		}
		if !strings.Contains(err.Error(), "StackIt") {
			t.Errorf("scale-group error should mention StackIt: %v", err)
		}
	}

	// serverless: no FaaS primitive.
	if _, err := TranslateServerless(stackitCtx(), cat, ServerlessSpec{
		Region: "Frankfurt", Provider: "stackit",
	}); err == nil {
		t.Error("serverless: expected unsupported error on stackit")
	} else {
		var cu ErrComponentUnsupported
		if !errors.As(err, &cu) {
			t.Errorf("serverless: expected ErrComponentUnsupported, got %T", err)
		}
	}

	// cache / managed-queue / event-streaming / cdn / waf: clean unsupported errors.
	if _, err := TranslateCache(stackitCtx(), cat, CacheSpec{Region: "Frankfurt", Provider: "stackit", MemoryGB: 1}); err == nil {
		t.Error("cache: expected unsupported error on stackit")
	}
	if _, err := TranslateQueue(stackitCtx(), cat, QueueSpec{Region: "Frankfurt", Provider: "stackit"}); err == nil {
		t.Error("managed-queue: expected unsupported error on stackit")
	}
	if _, err := TranslateStream(stackitCtx(), cat, StreamSpec{Region: "Frankfurt", Provider: "stackit"}); err == nil {
		t.Error("event-streaming: expected unsupported error on stackit")
	}
	if _, err := TranslateCDN(stackitCtx(), cat, CDNSpec{Region: "Frankfurt", Provider: "stackit"}); err == nil {
		t.Error("cdn-service: expected unsupported error on stackit")
	}
	if _, err := TranslateWAF(stackitCtx(), cat, WAFSpec{Region: "Frankfurt", Provider: "stackit"}); err == nil {
		t.Error("waf-service: expected unsupported error on stackit")
	}
}

// ── MDB data-safety guard applies to StackIt too (provider-agnostic) ─────────

func TestStackItMDBDataSafetyGuard(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	base := ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: "stackit",
		Engine: "postgres", CPU: 2, RAM: 8, StorageGB: 20,
	}
	prior, err := TranslateManagedDatabase(stackitCtx(), cat, base)
	if err != nil {
		t.Fatal(err)
	}

	// A pure size change is in-place and must PASS.
	bigger := base
	bigger.RAM, bigger.CPU = 16, 4
	next, err := TranslateManagedDatabase(stackitCtx(), cat, bigger)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckManagedDatabaseDataSafety(&prior, &next); err != nil {
		t.Errorf("size change should be in-place safe on stackit, got: %v", err)
	}

	// An engine change FORCES replacement and must be BLOCKED.
	myspec := base
	myspec.Engine = "mysql"
	myNext, err := TranslateManagedDatabase(stackitCtx(), cat, myspec)
	if err != nil {
		t.Fatal(err)
	}
	err = CheckManagedDatabaseDataSafety(&prior, &myNext)
	if err == nil {
		t.Fatal("engine change should be blocked by the data-safety guard")
	}
	var ds ErrDataSafetyForceReplace
	if !errors.As(err, &ds) {
		t.Fatalf("expected ErrDataSafetyForceReplace, got %T: %v", err, err)
	}

	// Fresh create (prior nil) is always safe.
	if err := CheckManagedDatabaseDataSafety(nil, &prior); err != nil {
		t.Errorf("fresh create should be safe, got: %v", err)
	}
}

// ── security-group: StackIt rejects the "all" protocol cleanly ───────────────

func TestStackItSGAllProtocolRejected(t *testing.T) {
	t.Parallel()
	_, err := TranslateSecurityGroup(stackitCtx(), MustEmbedded(), SecurityGroupSpec{
		Name: "fw", Region: "Frankfurt", Provider: "stackit",
		Rules: []SecurityRule{{Direction: "egress", Protocol: "all", CIDRs: []string{"0.0.0.0/0"}}},
	})
	if err == nil {
		t.Fatal("expected error for the 'all' protocol on stackit")
	}
	if !strings.Contains(err.Error(), "StackIt") {
		t.Errorf("error should mention StackIt: %v", err)
	}
}
