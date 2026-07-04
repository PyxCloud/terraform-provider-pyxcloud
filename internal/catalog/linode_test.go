package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Wave-2 Linode (Akamai) unit tests (pd-TF-W2-LINODE). They mirror the wave-1
// per-component tests: catalog-driven resolution (region/SKU never invented),
// per-component HCL shaping with the secure-by-default invariants, and the
// hard plan-time errors for components Linode has no native primitive for.
//
// All resolution goes through the SAME EmbeddedCatalog path as wave-1; the
// Linode rows come from the multiplexed linode_catalog.csv snapshot folded into
// the vm / managed_database / os tables at load time.

const linodeRegion = "Frankfurt" // pyx region_name -> linode csp_region "eu-central"

// ── resolution ────────────────────────────────────────────────────────────────

func TestLinodeProviderToCSP(t *testing.T) {
	t.Parallel()
	got, ok := ProviderToCSP(ProviderLinode)
	if !ok || got != "linode" {
		t.Fatalf("ProviderToCSP(linode) = (%q,%v), want (\"linode\",true)", got, ok)
	}
}

func TestLinodeResolveRegion(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	ctx := context.Background()
	cases := []struct {
		region, wantCSPRegion string
	}{
		{"Frankfurt", "eu-central"},
		{"London", "gb-lon"},
		{"Paris", "fr-par"},
		{"Amsterdam", "nl-ams"},
		{"Tokyo", "ap-northeast"},
	}
	for _, c := range cases {
		row, err := cat.ResolveRegion(ctx, c.region, ProviderLinode)
		if err != nil {
			t.Errorf("ResolveRegion(%q, linode) error: %v", c.region, err)
			continue
		}
		if row.CSPRegion != c.wantCSPRegion || row.CSP != "linode" {
			t.Errorf("ResolveRegion(%q, linode) = csp_region=%q csp=%q, want %q/linode",
				c.region, row.CSPRegion, row.CSP, c.wantCSPRegion)
		}
	}
}

func TestLinodeResolveRegionMissing(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// A pyx region with no Linode csp_region must be a hard error, never a fallback.
	if _, err := cat.ResolveRegion(context.Background(), "Atlantis", ProviderLinode); err == nil {
		t.Fatal("expected error for unknown region Atlantis on linode")
	}
}

// TestLinodeResolveVMSKU asserts a vCPU/RAM request resolves to a concrete Linode
// plan id from the catalog snapshot (never invented).
func TestLinodeResolveVMSKU(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	row, err := cat.ResolveSKU(context.Background(), "linode", "eu-central", "x86_64", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(row.Name, "g6-") {
		t.Errorf("resolved Linode SKU %q is not a g6-* plan id", row.Name)
	}
	if row.CSP != "linode" {
		t.Errorf("resolved SKU csp = %q, want linode", row.CSP)
	}
}

// ── network (linode_vpc + linode_vpc_subnet) ──────────────────────────────────

func TestLinodeNetwork(t *testing.T) {
	t.Parallel()
	plan, err := TranslateNetwork(context.Background(), MustEmbedded(), NetworkSpec{
		Name: "production", Region: linodeRegion, Provider: ProviderLinode,
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_vpc" {
		t.Errorf("network ResourceType = %q, want linode_vpc", plan.ResourceType)
	}
	// Linode VPCs are region-scoped: subnets carry no availability zone.
	for _, s := range plan.Subnets {
		if s.Zone != "" {
			t.Errorf("linode subnet %q has zone %q, want none (region-scoped)", s.Name, s.Zone)
		}
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_vpc" "production"`,
		`region = "eu-central"`,
		`resource "linode_vpc_subnet" "production_1"`,
		`vpc_id = linode_vpc.production.id`,
		`ipv4   = "10.0.1.0/24"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode network HCL missing %q:\n%s", want, hcl)
		}
	}
}

// ── security-group (linode_firewall) ──────────────────────────────────────────

func TestLinodeSecurityGroup(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "web-sg", Network: "production", Region: linodeRegion, Provider: ProviderLinode,
		Expose: []int{80, 443},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_firewall" {
		t.Errorf("sg ResourceType = %q, want linode_firewall", plan.ResourceType)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_firewall" "web-sg"`,
		`action   = "ACCEPT"`,
		`protocol = "TCP"`,
		// SECURE BY DEFAULT: default-drop both directions.
		`inbound_policy  = "DROP"`,
		`outbound_policy = "DROP"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode firewall HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestLinodeSecurityGroupAllProtoRejected asserts the "all" protocol is a hard
// plan-time error on Linode (its firewall has only tcp/udp/icmp), like DO.
func TestLinodeSecurityGroupAllProtoRejected(t *testing.T) {
	t.Parallel()
	_, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "bad", Region: linodeRegion, Provider: ProviderLinode,
		Rules: []SecurityRule{{Direction: DirIngress, Protocol: ProtoAll, FromPort: 0, ToPort: 0, CIDRs: []string{"0.0.0.0/0"}}},
	})
	if err == nil {
		t.Fatal("expected error for `all` protocol on linode firewall, got nil")
	}
	if !strings.Contains(err.Error(), ProtoAll) {
		t.Errorf("error should mention the unsupported %q protocol: %v", ProtoAll, err)
	}
}

// TestLinodeSecurityGroupRuleLimit asserts the 25-rules-per-direction Linode
// firewall limit is enforced at plan time.
func TestLinodeSecurityGroupRuleLimit(t *testing.T) {
	t.Parallel()
	rules := make([]SecurityRule, 0, linodeRulesPerDirectionMax+1)
	for i := 0; i < linodeRulesPerDirectionMax+1; i++ {
		rules = append(rules, SecurityRule{
			Direction: DirIngress, Protocol: ProtoTCP, FromPort: 1000 + i, ToPort: 1000 + i,
			CIDRs: []string{"0.0.0.0/0"},
		})
	}
	_, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "toomany", Region: linodeRegion, Provider: ProviderLinode, Rules: rules,
	})
	if err == nil {
		t.Fatalf("expected error exceeding the Linode %d-rule limit, got nil", linodeRulesPerDirectionMax)
	}
}

// ── virtual-machine (linode_instance) ─────────────────────────────────────────

func TestLinodeVirtualMachine(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "web", Region: linodeRegion, Provider: ProviderLinode,
		Architecture: "x86_64", CPU: 2, RAM: 4, OS: "ubuntu", Count: 2,
		Network: "production", Subnet: "production-subnet-1", SecurityGroup: "web-sg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_instance" {
		t.Errorf("vm ResourceType = %q, want linode_instance", plan.ResourceType)
	}
	if !strings.HasPrefix(plan.InstanceType, "g6-") {
		t.Errorf("vm InstanceType = %q, want a g6-* plan id", plan.InstanceType)
	}
	if len(plan.Instances) != 2 {
		t.Errorf("count=2 should yield 2 instances, got %d", len(plan.Instances))
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_instance" "web-1"`,
		`resource "linode_instance" "web-2"`,
		`region = "eu-central"`,
		`type   = "` + plan.InstanceType + `"`,
		`image  = "` + plan.Image + `"`,
		`purpose   = "vpc"`,
		`subnet_id = linode_vpc_subnet.production_1.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode instance HCL missing %q:\n%s", want, hcl)
		}
	}
	// Root password is never committed — it comes from a variable.
	if strings.Contains(hcl, "root_pass = \"") {
		t.Errorf("linode instance HCL must not inline a root password:\n%s", hcl)
	}
}

// ── scale-group (UNSUPPORTED -> clean plan-time error) ────────────────────────

func TestLinodeScaleGroupUnsupported(t *testing.T) {
	t.Parallel()
	_, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "web", Region: linodeRegion, Provider: ProviderLinode,
		Architecture: "x86_64", CPU: 2, RAM: 4, Min: 1, Max: 3, Desired: 2,
	})
	if err == nil {
		t.Fatal("expected ErrAutoscaleUnsupported for scale-group on linode, got nil")
	}
	var au ErrAutoscaleUnsupported
	if !errors.As(err, &au) {
		t.Fatalf("expected ErrAutoscaleUnsupported, got %T: %v", err, err)
	}
	// The error must point the user at LKE node pools (the supported alternative).
	if !strings.Contains(strings.ToLower(err.Error()), "lke") {
		t.Errorf("scale-group error should point to LKE node pools: %v", err)
	}
	// And the renderer must refuse a hand-built Linode scale-group plan too.
	if _, rerr := RenderScaleGroupHCL(ScaleGroupPlan{Provider: ProviderLinode}); rerr == nil {
		t.Error("RenderScaleGroupHCL(linode) should return an unsupported error")
	}
}

// ── load-balancer (linode_nodebalancer + _config + _node) ─────────────────────

func TestLinodeLoadBalancer(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: linodeRegion, Provider: ProviderLinode,
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}},
		Stickiness: true,
		TargetKind: LBTargetVM, TargetName: "web",
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_nodebalancer" {
		t.Errorf("lb ResourceType = %q, want linode_nodebalancer", plan.ResourceType)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_nodebalancer" "web-lb"`,
		`region = "eu-central"`,
		`resource "linode_nodebalancer_config" "web-lb_config_80"`,
		`nodebalancer_id = linode_nodebalancer.web-lb.id`,
		`protocol        = "http"`,
		`stickiness      = "http_cookie"`,
		`resource "linode_nodebalancer_node" "web-lb_node_80"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode nodebalancer HCL missing %q:\n%s", want, hcl)
		}
	}
}

// ── managed-database (linode_database_postgresql, data-safety guard) ──────────

func TestLinodeManagedDatabase(t *testing.T) {
	t.Parallel()
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: linodeRegion, Provider: ProviderLinode,
		Engine: "postgres", Version: "16", CPU: 2, RAM: 4, StorageGB: 20, HA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_database_postgresql_v2" {
		t.Errorf("mdb ResourceType = %q, want linode_database_postgresql_v2", plan.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_database_postgresql_v2" "app-db"`,
		`engine_id    = "postgresql/16"`,
		`region       = "eu-central"`,
		// HA -> 3-node cluster.
		`cluster_size = 3`,
		// SECURE BY DEFAULT: encrypted at rest + TLS are always-on in the v2
		// resource (provider-computed, read-only) — documented, never set.
		`encrypted / ssl_connection are always-on`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode postgres HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestLinodeManagedDatabaseDataSafetyGuard asserts the replacement-forcing
// data-safety guard trips for Linode the same way it does for the wave-1
// providers — an encryption flip on an existing DB is blocked at plan time
// (it would force replacement and destroy the data).
func TestLinodeManagedDatabaseDataSafetyGuard(t *testing.T) {
	t.Parallel()
	prior := &ManagedDatabasePlan{Provider: ProviderLinode, DBName: "app-db", Engine: DBEnginePostgres, Encrypted: true}
	next := &ManagedDatabasePlan{Provider: ProviderLinode, DBName: "app-db", Engine: DBEnginePostgres, Encrypted: false}
	err := CheckManagedDatabaseDataSafety(prior, next)
	if err == nil {
		t.Fatal("expected a data-safety error for an encryption flip (forces replacement), got nil")
	}
	var dsf ErrDataSafetyForceReplace
	if !errors.As(err, &dsf) {
		t.Fatalf("expected ErrDataSafetyForceReplace, got %T: %v", err, err)
	}
	// A fresh create (prior == nil) must NOT trip the guard.
	if err := CheckManagedDatabaseDataSafety(nil, next); err != nil {
		t.Errorf("fresh create should not trip the data-safety guard: %v", err)
	}
}

// TestLinodeManagedDatabaseMySQLNote asserts a non-PostgreSQL engine surfaces an
// explicit note (Linode renders the PostgreSQL resource), never a silent mismatch.
func TestLinodeManagedDatabaseMySQLNote(t *testing.T) {
	t.Parallel()
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: linodeRegion, Provider: ProviderLinode,
		Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Force a non-postgres engine on the resolved plan to exercise the render note.
	plan.Engine = DBEngineMySQL
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, "# NOTE") {
		t.Errorf("a non-PostgreSQL engine should emit an explicit note:\n%s", hcl)
	}
}

// ── object-storage (linode_object_storage_bucket, private by default) ─────────

func TestLinodeObjectStoragePrivateDefault(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "app-assets", Region: linodeRegion, Provider: ProviderLinode, Versioning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_object_storage_bucket" {
		t.Errorf("object-storage ResourceType = %q, want linode_object_storage_bucket", plan.ResourceType)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_object_storage_bucket" "app-assets"`,
		`region = "eu-central"`,
		// PRIVATE BY DEFAULT.
		`acl    = "private"`,
		`versioning = true`,
		`cors_enabled = false`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode object-storage HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestLinodeObjectStoragePublicOptIn(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "app-assets", Region: linodeRegion, Provider: ProviderLinode, Public: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, _ := RenderObjectStorageHCL(plan)
	if !strings.Contains(hcl, `acl    = "public-read"`) {
		t.Errorf("public Linode bucket should use public-read acl:\n%s", hcl)
	}
}

// ── dns-zone (linode_domain) ──────────────────────────────────────────────────

func TestLinodeDNSZone(t *testing.T) {
	t.Parallel()
	plan, err := TranslateDNSZone(context.Background(), MustEmbedded(), DNSZoneSpec{
		Name: "zone", Region: linodeRegion, Provider: ProviderLinode, Domain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_domain" {
		t.Errorf("dns ResourceType = %q, want linode_domain", plan.ResourceType)
	}
	hcl, err := RenderDNSZoneHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_domain" "zone"`,
		`domain    = "example.com"`,
		`type      = "master"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode domain HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestLinodeDNSZonePrivateUnsupported asserts a PRIVATE zone is a clean error
// (Linode DNS is public-only).
func TestLinodeDNSZonePrivateUnsupported(t *testing.T) {
	t.Parallel()
	_, err := TranslateDNSZone(context.Background(), MustEmbedded(), DNSZoneSpec{
		Name: "zone", Region: linodeRegion, Provider: ProviderLinode, Domain: "internal.example.com",
		Private: true, Network: "production",
	})
	if err == nil {
		t.Fatal("expected unsupported error for a private Linode zone, got nil")
	}
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("expected ErrComponentUnsupported, got %T: %v", err, err)
	}
}

// ── managed-kubernetes (linode_lke_cluster) ───────────────────────────────────

func TestLinodeKubernetes(t *testing.T) {
	t.Parallel()
	plan, err := TranslateKubernetes(context.Background(), MustEmbedded(), K8sSpec{
		Name: "app-k8s", Region: linodeRegion, Provider: ProviderLinode, Version: "1.30",
		Architecture: "x86_64", NodeCPU: 2, NodeRAM: 4, MinNodes: 1, MaxNodes: 3, DesiredNodes: 2,
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "linode_lke_cluster" {
		t.Errorf("k8s ResourceType = %q, want linode_lke_cluster", plan.ResourceType)
	}
	hcl, err := RenderKubernetesHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "linode_lke_cluster" "app-k8s"`,
		`region      = "eu-central"`,
		`k8s_version = "1.30"`,
		// LKE node-pool autoscaling — the supported answer to scale-group.
		`autoscaler {`,
		`min = 1`,
		`max = 3`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("linode LKE HCL missing %q:\n%s", want, hcl)
		}
	}
}

// ── unsupported macro components (clean plan-time errors) ─────────────────────

// TestLinodeUnsupportedComponents asserts every component Linode has no native
// primitive for fails with a clear ErrComponentUnsupported at plan time, never a
// silent fallback or an invented resource (SPEC §1, §4).
func TestLinodeUnsupportedComponents(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	ctx := context.Background()

	assertUnsupported := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s on linode: expected unsupported error, got nil", name)
			return
		}
		var unsup ErrComponentUnsupported
		if !errors.As(err, &unsup) {
			t.Errorf("%s on linode: expected ErrComponentUnsupported, got %T: %v", name, err, err)
		}
	}

	_, err := TranslateCache(ctx, cat, CacheSpec{Name: "c", Region: linodeRegion, Provider: ProviderLinode, MemoryGB: 1})
	assertUnsupported("cache", err)

	_, err = TranslateQueue(ctx, cat, QueueSpec{Name: "q", Region: linodeRegion, Provider: ProviderLinode})
	assertUnsupported("managed-queue", err)

	_, err = TranslateStream(ctx, cat, StreamSpec{Name: "s", Region: linodeRegion, Provider: ProviderLinode})
	assertUnsupported("event-streaming", err)

	_, err = TranslateCDN(ctx, cat, CDNSpec{Name: "cdn", Region: linodeRegion, Provider: ProviderLinode, OriginKind: CDNOriginObjectStorage, OriginName: "app-assets"})
	assertUnsupported("cdn-service", err)

	// waf-service on Linode now resolves to Cloudflare WAF (pd-MIG-B2-WAF-CLOUDFLARE),
	// not an unsupported error. Verify the Cloudflare path is taken.
	wafPlan, err := TranslateWAF(ctx, cat, WAFSpec{Name: "waf", Region: linodeRegion, Provider: ProviderLinode, Scope: "regional"})
	if err != nil {
		t.Errorf("waf-service on linode: expected Cloudflare WAF plan, got error: %v", err)
	} else if wafPlan.ResourceType != "cloudflare_ruleset" || !wafPlan.ViaCloudflare {
		t.Errorf("waf-service on linode: want cloudflare_ruleset via Cloudflare, got resource_type=%q ViaCloudflare=%v",
			wafPlan.ResourceType, wafPlan.ViaCloudflare)
	}

	_, err = TranslateSecrets(ctx, cat, SecretsSpec{Name: "sec", Region: linodeRegion, Provider: ProviderLinode})
	assertUnsupported("secrets-manager", err)

	_, err = TranslateServerless(ctx, cat, ServerlessSpec{Name: "fn", Region: linodeRegion, Provider: ProviderLinode, Runtime: "nodejs"})
	assertUnsupported("serverless-function", err)
}
