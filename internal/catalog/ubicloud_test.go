package catalog

import (
	"context"
	"strings"
	"testing"
)

// These tests cover the wave-2 Ubicloud surface: the catalog snapshot loads and
// resolves, the FEW genuinely-supported components (network/private-subnet,
// security-group/firewall, virtual-machine, managed Postgres) translate + render
// to real ubicloud_* resources, and EVERY unsupported component returns a clean,
// plan-time error naming the alternative (never a silent fallback, never an
// invented resource). Ubicloud has THIN Terraform support — the unsupported set
// is large and that is the correct, verified outcome.

// ── catalog snapshot ─────────────────────────────────────────────────────────

func TestUbicloudCatalogResolvesRegion(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	for _, tc := range []struct {
		region, wantLoc string
	}{
		{"Frankfurt", "eu-central-h1"},
		{"Helsinki", "eu-north-h1"},
		{"North Virginia", "us-east-a2"},
	} {
		row, err := cat.ResolveRegion(context.Background(), tc.region, ProviderUbicloud)
		if err != nil {
			t.Fatalf("ResolveRegion(%q, ubicloud): %v", tc.region, err)
		}
		if row.CSPRegion != tc.wantLoc {
			t.Errorf("region %q: got location %q, want %q", tc.region, row.CSPRegion, tc.wantLoc)
		}
		if row.CSP != cspUbicloud {
			t.Errorf("region %q: got csp %q, want ubicloud", tc.region, row.CSP)
		}
	}
}

func TestUbicloudCatalogResolvesSKUAndImage(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// standard-4 = 4 vCPU / 16 GiB in the snapshot.
	sku, err := cat.ResolveSKU(context.Background(), cspUbicloud, "eu-central-h1", ArchX8664, 4, 16)
	if err != nil {
		t.Fatalf("ResolveSKU: %v", err)
	}
	if sku.Name != "standard-4" {
		t.Errorf("got SKU %q, want standard-4", sku.Name)
	}
	img, err := cat.ResolveImage(context.Background(), cspUbicloud, "eu-central-h1", OSUbuntu, "24.04", ArchX8664)
	if err != nil {
		t.Fatalf("ResolveImage: %v", err)
	}
	if img.Image != "ubuntu-noble" {
		t.Errorf("got boot_image %q, want ubuntu-noble", img.Image)
	}
}

func TestUbicloudProviderRegistered(t *testing.T) {
	t.Parallel()
	csp, ok := ProviderToCSP(ProviderUbicloud)
	if !ok || csp != cspUbicloud {
		t.Fatalf("ProviderToCSP(ubicloud) = (%q,%v), want (ubicloud,true)", csp, ok)
	}
}

// ── SUPPORTED: network / private-subnet ──────────────────────────────────────

func TestUbicloudNetworkRender(t *testing.T) {
	t.Parallel()
	plan, err := TranslateNetwork(ctx(), MustEmbedded(), NetworkSpec{
		Name: "production", Region: "Frankfurt", Provider: ProviderUbicloud,
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24"},
	})
	if err != nil {
		t.Fatalf("TranslateNetwork: %v", err)
	}
	if plan.ResourceType != "ubicloud_private_subnet" {
		t.Errorf("got resource type %q, want ubicloud_private_subnet", plan.ResourceType)
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatalf("RenderHCL: %v", err)
	}
	for _, want := range []string{
		`resource "ubicloud_private_subnet" "production"`,
		`location   = "eu-central-h1"`,
		ubicloudProjectVar,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("network HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── SUPPORTED: security-group / firewall ─────────────────────────────────────

func TestUbicloudSecurityGroupRender(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(ctx(), MustEmbedded(), SecurityGroupSpec{
		Name: "web", Network: "production", Region: "Frankfurt", Provider: ProviderUbicloud,
		Description: "web tier", Expose: []int{80, 443},
	})
	if err != nil {
		t.Fatalf("TranslateSecurityGroup: %v", err)
	}
	if plan.ResourceType != "ubicloud_firewall" {
		t.Errorf("got resource type %q, want ubicloud_firewall", plan.ResourceType)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatalf("RenderSGHCL: %v", err)
	}
	for _, want := range []string{
		`resource "ubicloud_firewall" "web"`,
		`resource "ubicloud_firewall_rule"`,
		`firewall_name = ubicloud_firewall.web.name`,
		`port_range    = "80..80"`,
		`port_range    = "443..443"`,
		`cidr          = "0.0.0.0/0"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("firewall HCL missing %q\n%s", want, hcl)
		}
	}
}

// ── SUPPORTED: virtual-machine ───────────────────────────────────────────────

func TestUbicloudVMRender(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(ctx(), MustEmbedded(), VMSpec{
		Name: "web", Region: "Frankfurt", Provider: ProviderUbicloud,
		Architecture: ArchX8664, CPU: 4, RAM: 16, OS: OSUbuntu, Count: 2,
		Network: "production",
	})
	if err != nil {
		t.Fatalf("TranslateVM: %v", err)
	}
	if plan.ResourceType != "ubicloud_vm" {
		t.Errorf("got resource type %q, want ubicloud_vm", plan.ResourceType)
	}
	if plan.InstanceType != "standard-4" {
		t.Errorf("got size %q, want standard-4", plan.InstanceType)
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatalf("RenderVMHCL: %v", err)
	}
	for _, want := range []string{
		`resource "ubicloud_vm" "web-1"`,
		`resource "ubicloud_vm" "web-2"`,
		`size       = "standard-4"`,
		`boot_image = "ubuntu-noble"`,
		`location   = "eu-central-h1"`,
		`private_subnet_id = ubicloud_private_subnet.production.id`,
		ubicloudSSHKeyVar,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("vm HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestUbicloudVMUnknownSizeIsCleanError(t *testing.T) {
	t.Parallel()
	// No standard-3 (3 vCPU) in the catalog -> hard error listing nearest sizes.
	_, err := TranslateVM(ctx(), MustEmbedded(), VMSpec{
		Name: "web", Region: "Frankfurt", Provider: ProviderUbicloud,
		Architecture: ArchX8664, CPU: 3, RAM: 9, OS: OSUbuntu,
	})
	if err == nil {
		t.Fatal("expected ErrSKUNotFound for an unavailable size")
	}
	if !strings.Contains(err.Error(), "no virtual_machine SKU") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── SUPPORTED: managed-database (Postgres only) ──────────────────────────────

func TestUbicloudManagedPostgresRender(t *testing.T) {
	t.Parallel()
	plan, err := TranslateManagedDatabase(ctx(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: ProviderUbicloud,
		Engine: "postgres", Version: "16", CPU: 2, RAM: 8, StorageGB: 64, HA: true,
	})
	if err != nil {
		t.Fatalf("TranslateManagedDatabase: %v", err)
	}
	if plan.ResourceType != "ubicloud_postgres" {
		t.Errorf("got resource type %q, want ubicloud_postgres", plan.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatalf("RenderManagedDatabaseHCL: %v", err)
	}
	for _, want := range []string{
		`resource "ubicloud_postgres" "app-db"`,
		`size       = "standard-2"`,
		`version    = "16"`,
		`storage_size = 64`,
		`ha_type    = "async"`,
		`prevent_destroy = true`, // deletion protection defaults on
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("postgres HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestUbicloudManagedDatabaseMySQLUnsupported(t *testing.T) {
	t.Parallel()
	// Translate sets ResourceType=ubicloud_postgres regardless; the RENDER step is
	// the data-safe guard that rejects a non-postgres engine with a clean error.
	plan := ManagedDatabasePlan{Provider: ProviderUbicloud, Engine: DBEngineMySQL, DBName: "app-db"}
	_, err := RenderManagedDatabaseHCL(plan)
	if err == nil {
		t.Fatal("expected unsupported error for mysql on ubicloud")
	}
	if !strings.Contains(err.Error(), "unsupported on ubicloud") || !strings.Contains(err.Error(), "PostgreSQL-only") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── UNSUPPORTED: every component Ubicloud's Terraform provider cannot express ──
//
// Each must return a clean plan-time error that (a) names ubicloud, (b) says
// "unsupported", and (c) is never a silent success. We drive these through the
// render dispatch with a minimal ubicloud plan (defence-in-depth: even a
// hand-built plan can never emit an invented resource).

func TestUbicloudUnsupportedComponents(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func() (string, error)
	}{
		{"scale-group", func() (string, error) {
			return RenderScaleGroupHCL(ScaleGroupPlan{Provider: ProviderUbicloud})
		}},
		{"load-balancer", func() (string, error) {
			return RenderLoadBalancerHCL(LoadBalancerPlan{Provider: ProviderUbicloud})
		}},
		{"object-storage", func() (string, error) {
			return RenderObjectStorageHCL(ObjectStoragePlan{Provider: ProviderUbicloud})
		}},
		{"cache", func() (string, error) {
			return RenderCacheHCL(CachePlan{Provider: ProviderUbicloud})
		}},
		{"managed-queue", func() (string, error) {
			return RenderMessagingHCL(MessagingPlan{Provider: ProviderUbicloud, Kind: KindQueue})
		}},
		{"event-streaming", func() (string, error) {
			return RenderMessagingHCL(MessagingPlan{Provider: ProviderUbicloud, Kind: KindStream})
		}},
		{"dns-zone", func() (string, error) {
			return RenderDNSZoneHCL(DNSZonePlan{Provider: ProviderUbicloud})
		}},
		{"cdn-service", func() (string, error) {
			return RenderCDNHCL(CDNPlan{Provider: ProviderUbicloud})
		}},
		{"waf-service", func() (string, error) {
			return RenderWAFHCL(WAFPlan{Provider: ProviderUbicloud})
		}},
		{"managed-kubernetes", func() (string, error) {
			return RenderKubernetesHCL(K8sPlan{Provider: ProviderUbicloud})
		}},
		{"secrets-manager", func() (string, error) {
			return RenderSecretsHCL(SecretsPlan{Provider: ProviderUbicloud})
		}},
		{"serverless-function", func() (string, error) {
			return RenderServerlessHCL(ServerlessPlan{Provider: ProviderUbicloud})
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := tc.run()
			if err == nil {
				t.Fatalf("%s: expected unsupported error, got nil (out=%q)", tc.name, out)
			}
			msg := err.Error()
			if !strings.Contains(msg, "ubicloud") || !strings.Contains(msg, "unsupported") {
				t.Errorf("%s: error should name ubicloud + unsupported, got: %v", tc.name, err)
			}
		})
	}
}
