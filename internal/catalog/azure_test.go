package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// azureCtx is a tiny helper mirroring the wave-1 test ergonomics.
func azureCtx() context.Context { return context.Background() }

// ─────────────────────────────────────────────────────────────────────────────
// Catalog: region / SKU / image / DB-class resolution for Azure
// ─────────────────────────────────────────────────────────────────────────────

// TestAzureProviderEnabled asserts the provider-name -> csp mapping now resolves
// azure (wave-2). This is the one shared-map edit the wave-2 PR makes.
func TestAzureProviderEnabled(t *testing.T) {
	t.Parallel()
	got, ok := ProviderToCSP("azure")
	if !ok || got != "azure" {
		t.Fatalf("ProviderToCSP(azure) = (%q,%v), want (azure,true)", got, ok)
	}
	// Case-insensitive, like the wave-1 providers.
	if got, ok := ProviderToCSP("  Azure "); !ok || got != "azure" {
		t.Fatalf("ProviderToCSP(\"  Azure \") = (%q,%v), want (azure,true)", got, ok)
	}
}

// TestAzureResolveRegion checks that region_name -> azure location resolution is
// catalog-driven (values come straight from azure_catalog.csv, not invented).
func TestAzureResolveRegion(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		region, wantLocation string
	}{
		{"Dublin", "northeurope"},
		{"Amsterdam", "westeurope"},
		{"Frankfurt", "germanywestcentral"},
		{"London", "uksouth"},
		{"North Virginia", "eastus"},
		{"Tokyo", "japaneast"},
		{"frankfurt", "germanywestcentral"}, // case-insensitive region_name
	}
	for _, c := range cases {
		row, err := cat.ResolveRegion(azureCtx(), c.region, "azure")
		if err != nil {
			t.Errorf("ResolveRegion(%q, azure) error: %v", c.region, err)
			continue
		}
		if row.CSPRegion != c.wantLocation || row.CSP != "azure" {
			t.Errorf("ResolveRegion(%q, azure) = csp_region=%q csp=%q, want %q/azure",
				c.region, row.CSPRegion, row.CSP, c.wantLocation)
		}
	}
}

// TestAzureResolveRegionMissing asserts a region with no Azure row is a hard,
// catalog-driven error — never a silent fallback.
func TestAzureResolveRegionMissing(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// "Belgium" exists for GCP in wave-1 but has no Azure row in the snapshot.
	_, err := cat.ResolveRegion(azureCtx(), "Belgium", "azure")
	if err == nil {
		t.Fatal("expected ErrRegionNotFound for Belgium/azure, got nil")
	}
	var notFound ErrRegionNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

// TestAzureResolveSKU checks (cpu,ram,arch) -> azure VM size resolution against the
// embedded snapshot, and that a missing size is a hard error.
func TestAzureResolveSKU(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		region, arch  string
		cpu, ram      int
		wantSizeMatch string
	}{
		{"northeurope", "x86_64", 2, 8, "Standard_D2s_v5"},
		{"northeurope", "x86_64", 8, 32, "Standard_D8s_v5"},
		{"northeurope", "x86_64", 1, 2, "Standard_B1ms"},
		{"northeurope", "arm64", 2, 8, "Standard_D2ps_v5"},
	}
	for _, c := range cases {
		row, err := cat.ResolveSKU(azureCtx(), "azure", c.region, c.arch, c.cpu, c.ram)
		if err != nil {
			t.Errorf("ResolveSKU(azure,%s,%s,%d,%d) error: %v", c.region, c.arch, c.cpu, c.ram, err)
			continue
		}
		if row.Name != c.wantSizeMatch {
			t.Errorf("ResolveSKU(azure,%s,%s,%d,%d) = %q, want %q",
				c.region, c.arch, c.cpu, c.ram, row.Name, c.wantSizeMatch)
		}
	}

	// No exact (cpu,ram) match -> hard ErrSKUNotFound, never a silent nearest pick.
	_, err := cat.ResolveSKU(azureCtx(), "azure", "northeurope", "x86_64", 64, 256)
	if err == nil {
		t.Fatal("expected ErrSKUNotFound for an unavailable Azure size")
	}
	var noSKU ErrSKUNotFound
	if !errors.As(err, &noSKU) {
		t.Fatalf("expected ErrSKUNotFound, got %T: %v", err, err)
	}
}

// TestAzureResolveImage checks the OS image (URN) resolution from the catalog.
func TestAzureResolveImage(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	row, err := cat.ResolveImage(azureCtx(), "azure", "northeurope", "ubuntu", "24.04", "x86_64")
	if err != nil {
		t.Fatalf("ResolveImage(azure ubuntu 24.04 x86_64) error: %v", err)
	}
	if !strings.HasPrefix(row.Image, "Canonical:") || strings.Count(row.Image, ":") != 3 {
		t.Errorf("expected a marketplace URN publisher:offer:sku:version, got %q", row.Image)
	}
}

// TestAzureResolveDBClass checks the managed_database class resolution.
func TestAzureResolveDBClass(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	row, err := cat.ResolveDBClass(azureCtx(), "azure", "northeurope", "postgres", 2, 8)
	if err != nil {
		t.Fatalf("ResolveDBClass(azure postgres 2/8) error: %v", err)
	}
	if row.Name != "Standard_D2ds_v5" || row.Family != "GeneralPurpose" {
		t.Errorf("ResolveDBClass(azure postgres 2/8) = %q/%q, want Standard_D2ds_v5/GeneralPurpose",
			row.Name, row.Family)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-component rendering — assert the concrete azurerm_* shapes
// ─────────────────────────────────────────────────────────────────────────────

func TestAzureRenderNetwork(t *testing.T) {
	t.Parallel()
	plan, err := TranslateNetwork(azureCtx(), MustEmbedded(), NetworkSpec{
		Name: "production", Region: "Dublin", Provider: "azure",
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_virtual_network" {
		t.Errorf("network ResourceType = %q, want azurerm_virtual_network", plan.ResourceType)
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_resource_group"`,
		`resource "azurerm_virtual_network"`,
		`location = "northeurope"`,
		`address_space       = ["10.0.0.0/16"]`,
		`resource "azurerm_subnet"`,
		`address_prefixes     = ["10.0.1.0/24"]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure network HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderSecurityGroup(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(azureCtx(), MustEmbedded(), SecurityGroupSpec{
		Name: "web", Network: "production", Region: "Dublin", Provider: "azure",
		Description: "web tier", Expose: []int{80, 443},
		Rules: []SecurityRule{
			{Direction: "egress", Protocol: "all", CIDRs: []string{"0.0.0.0/0"}},
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
		`resource "azurerm_network_security_group"`,
		`resource "azurerm_network_security_rule"`,
		`direction                   = "Inbound"`,
		`direction                   = "Outbound"`,
		`access                      = "Allow"`,
		`priority                    = 100`,
		`destination_port_range      = "80"`,
		`destination_port_range      = "443"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure NSG HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestAzureSGDescriptionASCIIGuard asserts the renderer re-asserts the ASCII guard
// on the (tag-surfaced) description, mirroring every other renderer.
func TestAzureSGDescriptionASCIIGuard(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(azureCtx(), MustEmbedded(), SecurityGroupSpec{
		Name: "web", Network: "production", Region: "Dublin", Provider: "azure",
		Description: "café — naïve", Expose: []int{443},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range "café—naïve" {
		if r > 127 && strings.ContainsRune(hcl, r) {
			t.Errorf("non-ASCII rune %q leaked into Azure NSG HCL:\n%s", r, hcl)
		}
	}
}

func TestAzureRenderVM(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(azureCtx(), MustEmbedded(), VMSpec{
		Name: "web", Region: "Dublin", Provider: "azure",
		CPU: 2, RAM: 8, OS: "ubuntu", Count: 1, Network: "production", Subnet: "production-subnet-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_linux_virtual_machine" {
		t.Errorf("vm ResourceType = %q, want azurerm_linux_virtual_machine", plan.ResourceType)
	}
	if plan.InstanceType != "Standard_D2s_v5" {
		t.Errorf("vm InstanceType = %q, want Standard_D2s_v5 (catalog-resolved)", plan.InstanceType)
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_network_interface"`,
		`resource "azurerm_linux_virtual_machine"`,
		`size                  = "Standard_D2s_v5"`,
		`source_image_reference {`,
		`publisher = "Canonical"`,
		`public_key = var.ssh_public_key`, // SSH key out-of-band, never inline
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure VM HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderScaleGroup(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScaleGroup(azureCtx(), MustEmbedded(), ScaleGroupSpec{
		Name: "web", Region: "Dublin", Provider: "azure",
		CPU: 2, RAM: 8, OS: "ubuntu", Min: 2, Max: 6, Desired: 3, Health: HealthELB,
		Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_linux_virtual_machine_scale_set" {
		t.Errorf("scale-group ResourceType = %q, want azurerm_linux_virtual_machine_scale_set", plan.ResourceType)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_linux_virtual_machine_scale_set"`,
		`sku                 = "Standard_D2s_v5"`,
		`instances           = 3`,
		`resource "azurerm_monitor_autoscale_setting"`,
		`minimum = 2`,
		`maximum = 6`,
		`default = 3`,
		`health_probe_id = var.lb_health_probe_id`, // elb health wiring
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure VMSS HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderLoadBalancer(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(azureCtx(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Dublin", Provider: "azure",
		Listeners:   []LBListenerSpec{{Port: 80, Protocol: LBProtoHTTP}},
		HealthCheck: LBHealthCheckSpec{Protocol: LBProtoHTTP, Port: 80, Path: "/healthz"},
		Stickiness:  true,
		TargetKind:  "scale-group", TargetName: "web", Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_lb" {
		t.Errorf("lb ResourceType = %q, want azurerm_lb", plan.ResourceType)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_public_ip"`,
		`resource "azurerm_lb"`,
		`sku                 = "Standard"`,
		`resource "azurerm_lb_backend_address_pool"`,
		`resource "azurerm_lb_probe"`,
		`resource "azurerm_lb_rule"`,
		`frontend_port                  = 80`,
		`load_distribution              = "SourceIP"`, // stickiness
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure LB HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderManagedDatabase(t *testing.T) {
	t.Parallel()
	plan, err := TranslateManagedDatabase(azureCtx(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: "Dublin", Provider: "azure",
		Engine: "postgres", CPU: 2, RAM: 8, StorageGB: 64, HA: true,
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_postgresql_flexible_server" {
		t.Errorf("mdb ResourceType = %q, want azurerm_postgresql_flexible_server", plan.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_postgresql_flexible_server"`,
		`sku_name               = "GP_Standard_D2ds_v5"`, // tier_<class> from catalog family
		`storage_mb             = 65536`,                 // 64 GiB -> MB
		`high_availability {`,
		`mode = "ZoneRedundant"`,                   // HA
		`administrator_password = var.db_password`, // credentials out-of-band
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure MDB HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderManagedDatabaseMySQL(t *testing.T) {
	t.Parallel()
	plan, err := TranslateManagedDatabase(azureCtx(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: "Dublin", Provider: "azure",
		Engine: "mysql", CPU: 2, RAM: 8, StorageGB: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_mysql_flexible_server" {
		t.Errorf("mysql ResourceType = %q, want azurerm_mysql_flexible_server", plan.ResourceType)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, `resource "azurerm_mysql_flexible_server"`) {
		t.Errorf("Azure MySQL HCL missing the flexible-server resource\n%s", hcl)
	}
}

// TestAzureMDBDataSafetyGuard is the wave-1 data-safety force-replace guard,
// asserted for Azure plans. The guard is provider-agnostic (it runs at plan time
// in ModifyPlan), so it must block destructive replacements on Azure exactly as on
// wave-1 providers. This is the explicit "MDB guard for Azure" the task requires.
func TestAzureMDBDataSafetyGuard(t *testing.T) {
	t.Parallel()
	base := func() *ManagedDatabasePlan {
		return &ManagedDatabasePlan{
			Provider: "azure", DBName: "app-db", Engine: "postgres",
			Family: "GeneralPurpose", Encrypted: true,
		}
	}

	// Fresh create (prior == nil) is always safe.
	if err := CheckManagedDatabaseDataSafety(nil, base()); err != nil {
		t.Fatalf("fresh create should be safe, got %v", err)
	}

	// Encryption flip on an existing Azure DB must be blocked.
	prior, next := base(), base()
	next.Encrypted = false
	err := CheckManagedDatabaseDataSafety(prior, next)
	if err == nil {
		t.Fatal("expected ErrDataSafetyForceReplace for an Azure encryption flip")
	}
	var forced ErrDataSafetyForceReplace
	if !errors.As(err, &forced) {
		t.Fatalf("expected ErrDataSafetyForceReplace, got %T: %v", err, err)
	}
	if forced.Provider != "azure" {
		t.Errorf("guard error provider = %q, want azure", forced.Provider)
	}

	// Engine change (cross-engine) must be blocked.
	prior, next = base(), base()
	next.Engine = "mysql"
	if err := CheckManagedDatabaseDataSafety(prior, next); err == nil {
		t.Fatal("expected force-replace error for an Azure engine change")
	}

	// Class-family change (storage/class lineage) must be blocked.
	prior, next = base(), base()
	next.Family = "Burstable"
	if err := CheckManagedDatabaseDataSafety(prior, next); err == nil {
		t.Fatal("expected force-replace error for an Azure class-family change")
	}

	// An in-place safe change (same family, bigger storage) must pass.
	prior, next = base(), base()
	prior.StorageGB, next.StorageGB = 32, 64
	if err := CheckManagedDatabaseDataSafety(prior, next); err != nil {
		t.Errorf("a same-family storage increase should be in-place safe, got %v", err)
	}
}

// TestAzureRenderObjectStoragePrivateByDefault asserts the storage account +
// container is PRIVATE by default (SPEC §5.7 default-secure).
func TestAzureRenderObjectStoragePrivateByDefault(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(azureCtx(), MustEmbedded(), ObjectStorageSpec{
		Name: "app-assets", Region: "Dublin", Provider: "azure", Versioning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_storage_account" {
		t.Errorf("object-storage ResourceType = %q, want azurerm_storage_account", plan.ResourceType)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_storage_account"`,
		`resource "azurerm_storage_container"`,
		`allow_nested_items_to_be_public = false`, // private by default
		`container_access_type = "private"`,
		`versioning_enabled = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure object-storage HCL missing %q\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, `container_access_type = "blob"`) {
		t.Errorf("private bucket must NOT expose anonymous blob access\n%s", hcl)
	}
}

// TestAzureRenderObjectStoragePublicOptIn asserts public is strictly opt-in.
func TestAzureRenderObjectStoragePublicOptIn(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(azureCtx(), MustEmbedded(), ObjectStorageSpec{
		Name: "public-assets", Region: "Dublin", Provider: "azure", Public: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`allow_nested_items_to_be_public = true`,
		`container_access_type = "blob"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure public object-storage HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderCache(t *testing.T) {
	t.Parallel()
	plan, err := TranslateCache(azureCtx(), MustEmbedded(), CacheSpec{
		Name: "sessions", Region: "Dublin", Provider: "azure", MemoryGB: 4, HA: true,
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_redis_cache" {
		t.Errorf("cache ResourceType = %q, want azurerm_redis_cache", plan.ResourceType)
	}
	hcl, err := RenderCacheHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_redis_cache"`,
		`non_ssl_port_enabled = false`, // TLS only (secure default)
		`minimum_tls_version  = "1.2"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure cache HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderQueue(t *testing.T) {
	t.Parallel()
	plan, err := TranslateQueue(azureCtx(), MustEmbedded(), QueueSpec{
		Name: "jobs", Region: "Dublin", Provider: "azure", FIFO: true, MaxReceiveCount: 5,
		VisibilityTimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_servicebus_queue" {
		t.Errorf("queue ResourceType = %q, want azurerm_servicebus_queue", plan.ResourceType)
	}
	hcl, err := RenderMessagingHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_servicebus_namespace"`,
		`resource "azurerm_servicebus_queue"`,
		`requires_session = true`, // FIFO -> sessions
		`max_delivery_count                   = 5`,
		`lock_duration = "PT60S"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure queue HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderStream(t *testing.T) {
	t.Parallel()
	plan, err := TranslateStream(azureCtx(), MustEmbedded(), StreamSpec{
		Name: "events", Region: "Dublin", Provider: "azure", Shards: 4, RetentionHours: 48,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_eventhub" {
		t.Errorf("stream ResourceType = %q, want azurerm_eventhub", plan.ResourceType)
	}
	hcl, err := RenderMessagingHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "azurerm_eventhub_namespace"`,
		`resource "azurerm_eventhub"`,
		`partition_count     = 4`,
		`message_retention   = 2`, // 48h -> 2 days
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure event-hub HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderDNSZone(t *testing.T) {
	t.Parallel()
	// Public zone.
	pub, err := TranslateDNSZone(azureCtx(), MustEmbedded(), DNSZoneSpec{
		Name: "z", Region: "Dublin", Provider: "azure", Domain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pub.ResourceType != "azurerm_dns_zone" {
		t.Errorf("public dns ResourceType = %q, want azurerm_dns_zone", pub.ResourceType)
	}
	if hcl, _ := RenderDNSZoneHCL(pub); !strings.Contains(hcl, `resource "azurerm_dns_zone"`) {
		t.Errorf("Azure public DNS HCL missing the zone resource\n%s", hcl)
	}

	// Private zone (linked to the vnet).
	priv, err := TranslateDNSZone(azureCtx(), MustEmbedded(), DNSZoneSpec{
		Name: "zp", Region: "Dublin", Provider: "azure", Domain: "internal.example.com",
		Private: true, Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if priv.ResourceType != "azurerm_private_dns_zone" {
		t.Errorf("private dns ResourceType = %q, want azurerm_private_dns_zone", priv.ResourceType)
	}
	hcl, _ := RenderDNSZoneHCL(priv)
	for _, want := range []string{
		`resource "azurerm_private_dns_zone"`,
		`resource "azurerm_private_dns_zone_virtual_network_link"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure private DNS HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderCDN(t *testing.T) {
	t.Parallel()
	plan, err := TranslateCDN(azureCtx(), MustEmbedded(), CDNSpec{
		Name: "edge", Region: "Dublin", Provider: "azure",
		OriginKind: CDNOriginObjectStorage, OriginName: "assets",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_cdn_frontdoor_profile" {
		t.Errorf("cdn ResourceType = %q, want azurerm_cdn_frontdoor_profile", plan.ResourceType)
	}
	hcl, _ := RenderCDNHCL(plan)
	for _, want := range []string{
		`resource "azurerm_cdn_frontdoor_profile"`,
		`resource "azurerm_cdn_frontdoor_endpoint"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure CDN HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderWAF(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWAF(azureCtx(), MustEmbedded(), WAFSpec{
		Name: "fw", Region: "Dublin", Provider: "azure",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_cdn_frontdoor_firewall_policy" {
		t.Errorf("waf ResourceType = %q, want azurerm_cdn_frontdoor_firewall_policy", plan.ResourceType)
	}
	hcl, _ := RenderWAFHCL(plan)
	for _, want := range []string{
		`resource "azurerm_cdn_frontdoor_firewall_policy"`,
		`mode                = "Prevention"`, // block, not just detect
		`Microsoft_DefaultRuleSet`,
		`action  = "Block"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure WAF HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderKubernetes(t *testing.T) {
	t.Parallel()
	plan, err := TranslateKubernetes(azureCtx(), MustEmbedded(), K8sSpec{
		Name: "cluster", Region: "Dublin", Provider: "azure",
		NodeCPU: 2, NodeRAM: 8, MinNodes: 1, MaxNodes: 4, DesiredNodes: 2,
		Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_kubernetes_cluster" {
		t.Errorf("k8s ResourceType = %q, want azurerm_kubernetes_cluster", plan.ResourceType)
	}
	if plan.NodeType != "Standard_D2s_v5" {
		t.Errorf("k8s node type = %q, want Standard_D2s_v5 (catalog-resolved)", plan.NodeType)
	}
	hcl, _ := RenderKubernetesHCL(plan)
	for _, want := range []string{
		`resource "azurerm_kubernetes_cluster"`,
		`vm_size             = "Standard_D2s_v5"`,
		`auto_scaling_enabled = true`,
		`min_count           = 1`,
		`max_count           = 4`,
		`type = "SystemAssigned"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure AKS HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderSecrets(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecrets(azureCtx(), MustEmbedded(), SecretsSpec{
		Name: "db-pw", Region: "Dublin", Provider: "azure", Description: "db password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_key_vault" {
		t.Errorf("secrets ResourceType = %q, want azurerm_key_vault", plan.ResourceType)
	}
	hcl, _ := RenderSecretsHCL(plan)
	for _, want := range []string{
		`resource "azurerm_key_vault"`,
		`purge_protection_enabled   = true`, // production-safe default
		`soft_delete_retention_days = 7`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure Key Vault HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestAzureRenderServerless(t *testing.T) {
	t.Parallel()
	plan, err := TranslateServerless(azureCtx(), MustEmbedded(), ServerlessSpec{
		Name: "api", Region: "Dublin", Provider: "azure", Runtime: "python",
		RuntimeVersion: "3.12", Handler: "main.handler",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "azurerm_linux_function_app" {
		t.Errorf("serverless ResourceType = %q, want azurerm_linux_function_app", plan.ResourceType)
	}
	hcl, _ := RenderServerlessHCL(plan)
	for _, want := range []string{
		`resource "azurerm_service_plan"`,
		`resource "azurerm_linux_function_app"`,
		`sku_name            = "Y1"`, // consumption / serverless plan
		`python_version`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("Azure Function App HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestAzureDeterministicRender asserts the render is deterministic (same plan ->
// byte-identical HCL), the catalog-driven invariant the whole provider relies on.
func TestAzureDeterministicRender(t *testing.T) {
	t.Parallel()
	spec := NetworkSpec{
		Name: "production", Region: "Dublin", Provider: "azure",
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24"},
	}
	plan, err := TranslateNetwork(azureCtx(), MustEmbedded(), spec)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := RenderHCL(plan)
	b, _ := RenderHCL(plan)
	if a != b {
		t.Errorf("Azure render is not deterministic:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}
