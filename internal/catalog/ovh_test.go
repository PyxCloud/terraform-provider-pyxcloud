package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ── catalog resolution ────────────────────────────────────────────────────────

func TestOVHCatalogWellFormed(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	if len(cat.Regions()) == 0 {
		t.Fatal("ovh snapshot has no region rows")
	}
	for _, r := range cat.Regions() {
		if r.RegionName == "" || r.CSPRegion == "" {
			t.Errorf("malformed ovh region row: %+v", r)
		}
		if r.CSP != cspOVH {
			t.Errorf("ovh region row has csp %q, want %q (row %+v)", r.CSP, cspOVH, r)
		}
	}
}

func TestOVHResolveRegion(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	cases := []struct{ region, wantCSPRegion string }{
		{"Gravelines", "GRA"},
		{"Frankfurt", "DE"},
		{"London", "UK"},
		{"Beauharnois", "BHS"},
		{"frankfurt", "DE"}, // case-insensitive
	}
	for _, c := range cases {
		row, err := cat.ResolveRegion(c.region)
		if err != nil {
			t.Errorf("ResolveRegion(%q) error: %v", c.region, err)
			continue
		}
		if row.CSPRegion != c.wantCSPRegion {
			t.Errorf("ResolveRegion(%q) = %q, want %q", c.region, row.CSPRegion, c.wantCSPRegion)
		}
	}
}

func TestOVHResolveRegionMissing(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	// A region OVH has no datacenter for -> hard error, no fallback.
	_, err := cat.ResolveRegion("Atlantis")
	if err == nil {
		t.Fatal("expected ErrRegionNotFound for Atlantis, got nil")
	}
	var notFound ErrRegionNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

func TestOVHResolveDBFlavor(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	f, err := cat.ResolveDBFlavor(2, 7)
	if err != nil {
		t.Fatalf("ResolveDBFlavor(2,7) error: %v", err)
	}
	if f.Flavor != "db1-7" || f.Plan != "essential" {
		t.Errorf("ResolveDBFlavor(2,7) = %q/%q, want db1-7/essential", f.Flavor, f.Plan)
	}
	// No flavor at this sizing -> hard error listing nearest.
	if _, err := cat.ResolveDBFlavor(99, 999); err == nil {
		t.Fatal("expected hard error for db flavor 99/999, got nil")
	} else if !strings.Contains(err.Error(), "Nearest available") {
		t.Errorf("expected nearest-sizes hint, got: %v", err)
	}
}

func TestOVHResolveNodeFlavor(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	f, err := cat.ResolveNodeFlavor(2, 8)
	if err != nil {
		t.Fatalf("ResolveNodeFlavor(2,8) error: %v", err)
	}
	if f.Flavor != "b3-8" {
		t.Errorf("ResolveNodeFlavor(2,8) = %q, want b3-8", f.Flavor)
	}
	if _, err := cat.ResolveNodeFlavor(3, 3); err == nil {
		t.Fatal("expected hard error for node flavor 3/3, got nil")
	}
}

// ── supported renderers ───────────────────────────────────────────────────────

func TestOVHRenderNetwork(t *testing.T) {
	t.Parallel()
	hcl, err := RenderOVHComponent(context.Background(), MustOVHCatalog(), "network", NetworkSpec{
		Name: "production", Region: "Gravelines", Provider: ProviderOVH,
		CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24", "10.0.2.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ovh_cloud_project_network_private" "production"`,
		`regions      = ["GRA"]`,
		`service_name = var.ovh_service_name`,
		`resource "ovh_cloud_project_network_private_subnet" "production_1"`,
		`network      = "10.0.1.0/24"`,
		`start        = "10.0.1.2"`,
		`end          = "10.0.1.254"`,
		`no_gateway   = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("OVH network HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestOVHRenderManagedDatabase(t *testing.T) {
	t.Parallel()
	skip := true
	hcl, err := RenderOVHComponent(context.Background(), MustOVHCatalog(), "managed-database", ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: ProviderOVH,
		Engine: "postgres", CPU: 2, RAM: 7, StorageGB: 40, HA: true,
		Network:            "production",
		DeletionProtection: &skip, // true here; test that prevent_destroy is emitted
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ovh_cloud_project_database" "app-db"`,
		`engine       = "postgresql"`,
		`version      = "16"`,
		`plan         = "essential"`,
		`flavor       = "db1-7"`,
		`disk_size    = 40`,
		`region     = "DE"`,
		`prevent_destroy = true`,
		`network_id = ovh_cloud_project_network_private.production`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("OVH MDB HCL missing %q\n%s", want, hcl)
		}
	}
	// HA = 3 nodes: count the nodes {} blocks.
	if n := strings.Count(hcl, "nodes {"); n != 3 {
		t.Errorf("HA OVH MDB: want 3 nodes blocks, got %d\n%s", n, hcl)
	}
}

func TestOVHRenderManagedDatabaseSingleNode(t *testing.T) {
	t.Parallel()
	noProtect := false
	hcl, err := RenderOVHComponent(context.Background(), MustOVHCatalog(), "managed-database", ManagedDatabaseSpec{
		Name: "small", Region: "Gravelines", Provider: ProviderOVH,
		Engine: "mysql", CPU: 2, RAM: 4, DeletionProtection: &noProtect,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(hcl, "nodes {"); n != 1 {
		t.Errorf("non-HA OVH MDB: want 1 nodes block, got %d", n)
	}
	if !strings.Contains(hcl, `engine       = "mysql"`) {
		t.Errorf("expected mysql engine token\n%s", hcl)
	}
	// storage below the floor clamps to MinStorageGB.
	if !strings.Contains(hcl, "disk_size    = 20") {
		t.Errorf("expected disk_size clamped to %d\n%s", MinStorageGB, hcl)
	}
	// deletion_protection off -> no prevent_destroy lifecycle.
	if strings.Contains(hcl, "prevent_destroy") {
		t.Errorf("did not expect prevent_destroy when deletion_protection is off\n%s", hcl)
	}
}

func TestOVHRenderKubernetes(t *testing.T) {
	t.Parallel()
	hcl, err := RenderOVHComponent(context.Background(), MustOVHCatalog(), "managed-kubernetes", K8sSpec{
		Name: "app-kube", Region: "Gravelines", Provider: ProviderOVH,
		Version: "1.30", NodeCPU: 2, NodeRAM: 8,
		MinNodes: 1, MaxNodes: 5, DesiredNodes: 3,
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ovh_cloud_project_kube" "app-kube"`,
		`region       = "GRA11"`, // kube uses the regional variant
		`version      = "1.30"`,
		`private_network_id = ovh_cloud_project_network_private.production`,
		`resource "ovh_cloud_project_kube_nodepool" "app-kube_np"`,
		`flavor_name   = "b3-8"`,
		`autoscale     = true`,
		`desired_nodes = 3`,
		`min_nodes     = 1`,
		`max_nodes     = 5`,
		`name          = "app-kube-np"`, // no "_" in nodepool name
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("OVH kube HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestOVHRenderObjectStorage(t *testing.T) {
	t.Parallel()
	hcl, err := RenderOVHComponent(context.Background(), MustOVHCatalog(), "object-storage", ObjectStorageSpec{
		Name: "app-assets", Region: "Frankfurt", Provider: ProviderOVH, Versioning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "ovh_cloud_project_storage" "app-assets"`,
		`region_name  = "DE"`,
		`name         = "app-assets"`,
		`status = "enabled"`,
		`sse_algorithm = "AES256"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("OVH object-storage HCL missing %q\n%s", want, hcl)
		}
	}
	// PRIVATE BY DEFAULT: no public-read acl emitted.
	if strings.Contains(strings.ToLower(hcl), "public-read") {
		t.Errorf("object-storage must be private by default\n%s", hcl)
	}
}

// ── unsupported components (clean plan-time errors, never invented) ───────────

func TestOVHUnsupportedComponents(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	for _, component := range []string{
		"security-group", "virtual-machine", "virtual-machine-scale-group",
		"load-balancer", "cache", "managed-queue", "message-queue",
		"event-streaming", "event-bus", "dns-zone", "cdn-service",
		"waf-service", "secrets-manager", "serverless-function",
	} {
		_, err := RenderOVHComponent(context.Background(), cat, component, nil)
		if err == nil {
			t.Errorf("%s: expected ErrComponentUnsupported, got nil", component)
			continue
		}
		var unsupported ErrComponentUnsupported
		if !errors.As(err, &unsupported) {
			t.Errorf("%s: expected ErrComponentUnsupported, got %T: %v", component, err, err)
			continue
		}
		if unsupported.Provider != ProviderOVH || unsupported.CSP != cspOVH {
			t.Errorf("%s: error provider/csp = %q/%q, want ovh/ovh", component, unsupported.Provider, unsupported.CSP)
		}
		if unsupported.Alternative == "" {
			t.Errorf("%s: unsupported error must name an alternative", component)
		}
	}
}

func TestOVHUnsupportedNamesAlternative(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	// The load-balancer error must point at the verified alternative, not invent
	// ovh_iploadbalancing as a place-scoped LB.
	_, err := RenderOVHComponent(context.Background(), cat, "load-balancer", nil)
	if err == nil || !strings.Contains(err.Error(), "ovh_iploadbalancing") {
		t.Fatalf("load-balancer error should explain ovh_iploadbalancing does not fit, got: %v", err)
	}
	// scale-group points at the kube nodepool autoscaler.
	_, err = RenderOVHComponent(context.Background(), cat, "virtual-machine-scale-group", nil)
	if err == nil || !strings.Contains(err.Error(), "nodepool") {
		t.Fatalf("scale-group error should point at the kube nodepool, got: %v", err)
	}
}

// ── data-safety guard reuse (SPEC §5.6) ───────────────────────────────────────

func TestOVHManagedDatabaseDataSafetyGuard(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	mk := func(engine string, cpu, ram int) ManagedDatabasePlan {
		p, err := TranslateOVHManagedDatabase(cat, ManagedDatabaseSpec{
			Name: "guarded", Region: "Frankfurt", Provider: ProviderOVH,
			Engine: engine, CPU: cpu, RAM: ram,
		})
		if err != nil {
			t.Fatal(err)
		}
		return p.ToCommon()
	}

	// Fresh create (prior nil) is always safe.
	next := mk("postgres", 2, 7)
	if err := CheckManagedDatabaseDataSafety(nil, &next); err != nil {
		t.Errorf("fresh OVH create should be safe, got: %v", err)
	}

	// Engine change FORCE-REPLACES -> blocked by the shared guard.
	prior := mk("postgres", 2, 7)
	changed := mk("mysql", 2, 7)
	if err := CheckManagedDatabaseDataSafety(&prior, &changed); err == nil {
		t.Error("expected data-safety guard to block an OVH engine change")
	} else {
		var dsf ErrDataSafetyForceReplace
		if !errors.As(err, &dsf) {
			t.Errorf("expected ErrDataSafetyForceReplace, got %T: %v", err, err)
		}
	}

	// Cross-family flavor change (db1 -> db2) FORCE-REPLACES.
	p1 := mk("postgres", 4, 15) // db1-15
	p2 := mk("postgres", 8, 30) // db1-30 (same db1 family) -> SAFE resize
	if err := CheckManagedDatabaseDataSafety(&p1, &p2); err != nil {
		t.Errorf("same-family OVH resize should be in-place safe, got: %v", err)
	}
	// db2-15 is a different family from db1-15 at the same sizing? db2-15 is 4/15
	// which collides with db1-15 (4/15); the resolver picks db1-15 (lexicographically
	// smaller), so build a db2 plan explicitly to exercise the family guard.
	p3 := p1
	p3.Family = "db2"
	p3.DBClass = "db2-15"
	if err := CheckManagedDatabaseDataSafety(&p1, &p3); err == nil {
		t.Error("expected data-safety guard to block a cross-family OVH flavor change")
	}
}

// ── go-string fixture validity (no creds: render-only, asserts shape) ─────────

func TestOVHFixtureRenders(t *testing.T) {
	t.Parallel()
	cat := MustOVHCatalog()
	// The supported four compose into one place graph; assert each renders non-empty
	// and references the out-of-band project var (the only credential surface).
	specs := []struct {
		component string
		spec      interface{}
	}{
		{"network", NetworkSpec{Name: "prod", Region: "Gravelines", Provider: ProviderOVH, CIDR: "10.0.0.0/16", Subnets: []string{"10.0.1.0/24"}}},
		{"managed-database", ManagedDatabaseSpec{Name: "db", Region: "Gravelines", Provider: ProviderOVH, Engine: "postgres", CPU: 2, RAM: 4}},
		{"managed-kubernetes", K8sSpec{Name: "kube", Region: "Gravelines", Provider: ProviderOVH, NodeCPU: 2, NodeRAM: 8, MaxNodes: 3, DesiredNodes: 1}},
		{"object-storage", ObjectStorageSpec{Name: "assets", Region: "Gravelines", Provider: ProviderOVH}},
	}
	for _, s := range specs {
		hcl, err := RenderOVHComponent(context.Background(), cat, s.component, s.spec)
		if err != nil {
			t.Errorf("%s: render error: %v", s.component, err)
			continue
		}
		if strings.TrimSpace(hcl) == "" {
			t.Errorf("%s: rendered empty HCL", s.component)
		}
		if !strings.Contains(hcl, "var.ovh_service_name") {
			t.Errorf("%s: must reference var.ovh_service_name (out-of-band project id)\n%s", s.component, hcl)
		}
	}
}
