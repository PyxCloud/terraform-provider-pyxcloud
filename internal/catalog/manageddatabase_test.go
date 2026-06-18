package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// boolPtr is a test helper for the *bool override fields.
func boolPtr(b bool) *bool { return &b }

// TestTranslateManagedDatabaseAWS asserts the resolved structured plan for AWS:
// catalog-resolved csp_region + DB class, production-safe defaults
// (deletion_protection true, skip_final_snapshot false), multi-AZ zones, and the
// aws_db_instance resource type.
func TestTranslateManagedDatabaseAWS(t *testing.T) {
	t.Parallel()
	// Frankfurt -> eu-central-1 (AWS); postgres 2vCPU/4GiB -> db.t3.medium.
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Name:          "app-db",
		Region:        "Frankfurt",
		Provider:      "aws",
		Engine:        "postgres",
		CPU:           2,
		RAM:           4,
		StorageGB:     50,
		HA:            true,
		Encrypted:     true,
		Network:       "production",
		Subnets:       []string{"production-subnet-1", "production-subnet-2"},
		SecurityGroup: "production-db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.DBClass != "db.t3.medium" {
		t.Errorf("db_class = %q, want db.t3.medium", plan.DBClass)
	}
	if plan.ResourceType != "aws_db_instance" {
		t.Errorf("resource_type = %q, want aws_db_instance", plan.ResourceType)
	}
	if plan.EngineVersion != "16" {
		t.Errorf("engine_version = %q, want default 16", plan.EngineVersion)
	}
	if plan.StorageGB != 50 {
		t.Errorf("storage_gb = %d, want 50", plan.StorageGB)
	}
	if !plan.HA || !plan.Encrypted {
		t.Errorf("ha/encrypted not carried: %+v", plan)
	}
	// Production-safe defaults.
	if !plan.DeletionProtection {
		t.Error("deletion_protection should default to true")
	}
	if plan.SkipFinalSnapshot {
		t.Error("skip_final_snapshot should default to false (a final snapshot is taken)")
	}
	wantZones := []string{"eu-central-1a", "eu-central-1b"}
	if len(plan.Zones) != 2 {
		t.Fatalf("want 2 zones, got %v", plan.Zones)
	}
	for i, z := range wantZones {
		if plan.Zones[i] != z {
			t.Errorf("zone[%d] = %q, want %q", i, plan.Zones[i], z)
		}
	}
}

func TestTranslateManagedDatabaseGCP(t *testing.T) {
	t.Parallel()
	// Frankfurt -> europe-west3 (GCP); postgres 2vCPU/4GiB -> db-custom-2-4096.
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: "gcp",
		Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 30,
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "europe-west3" {
		t.Errorf("csp_region = %q, want europe-west3", plan.CSPRegion)
	}
	if plan.DBClass != "db-custom-2-4096" {
		t.Errorf("db_class = %q, want db-custom-2-4096", plan.DBClass)
	}
	if plan.ResourceType != "google_sql_database_instance" {
		t.Errorf("resource_type = %q, want google_sql_database_instance", plan.ResourceType)
	}
}

func TestTranslateManagedDatabaseDO(t *testing.T) {
	t.Parallel()
	// Frankfurt -> fra1 (DO); postgres 2vCPU/4GiB -> db-s-2vcpu-4gb.
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Name: "app-db", Region: "Frankfurt", Provider: "digitalocean",
		Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 30,
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	if plan.DBClass != "db-s-2vcpu-4gb" {
		t.Errorf("db_class = %q, want db-s-2vcpu-4gb", plan.DBClass)
	}
	if plan.ResourceType != "digitalocean_database_cluster" {
		t.Errorf("resource_type = %q, want digitalocean_database_cluster", plan.ResourceType)
	}
	// DO is region-scoped: no zones.
	if len(plan.Zones) != 0 {
		t.Errorf("DO should have no zones, got %v", plan.Zones)
	}
}

// TestManagedDatabaseStorageFloor asserts a too-small storage is clamped to the
// RDS minimum rather than rejected.
func TestManagedDatabaseStorageFloor(t *testing.T) {
	t.Parallel()
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Region: "Frankfurt", Provider: "aws", Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.StorageGB != MinStorageGB {
		t.Errorf("storage clamp = %d, want %d", plan.StorageGB, MinStorageGB)
	}
}

// TestManagedDatabaseEngineDefaultsAndAliases asserts engine defaulting/aliasing.
func TestManagedDatabaseEngineDefaultsAndAliases(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct{ in, want string }{
		{"", DBEnginePostgres},
		{"postgres", DBEnginePostgres},
		{"postgresql", DBEnginePostgres},
		{"pg", DBEnginePostgres},
		{"mysql", DBEngineMySQL},
		{"mariadb", DBEngineMySQL},
	}
	for _, c := range cases {
		plan, err := TranslateManagedDatabase(context.Background(), cat, ManagedDatabaseSpec{
			Region: "Frankfurt", Provider: "aws", Engine: c.in, CPU: 2, RAM: 4,
		})
		if err != nil {
			t.Fatalf("engine %q: %v", c.in, err)
		}
		if plan.Engine != c.want {
			t.Errorf("engine %q -> %q, want %q", c.in, plan.Engine, c.want)
		}
	}
}

// TestManagedDatabaseTestOverride asserts the test-only override flips the
// production-safe defaults (deletion_protection off + skip_final_snapshot on) and
// that it is explicit (pointer-set), not the silent default.
func TestManagedDatabaseTestOverride(t *testing.T) {
	t.Parallel()
	plan, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Region: "Frankfurt", Provider: "aws", Engine: "postgres", CPU: 2, RAM: 4,
		DeletionProtection: boolPtr(false),
		SkipFinalSnapshot:  boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeletionProtection {
		t.Error("test override should disable deletion_protection")
	}
	if !plan.SkipFinalSnapshot {
		t.Error("test override should enable skip_final_snapshot")
	}
}

// TestManagedDatabaseClassNotFound asserts an unavailable sizing is a hard error.
func TestManagedDatabaseClassNotFound(t *testing.T) {
	t.Parallel()
	_, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Region: "Frankfurt", Provider: "aws", Engine: "postgres", CPU: 128, RAM: 999,
	})
	if err == nil {
		t.Fatal("expected DB-class-not-found error, got nil")
	}
	var nf ErrDBClassNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrDBClassNotFound, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "Nearest available sizes") {
		t.Errorf("error should list nearest sizes, got %v", err)
	}
}

// TestManagedDatabaseRegionNotFound asserts an unresolvable region is a hard error.
func TestManagedDatabaseRegionNotFound(t *testing.T) {
	t.Parallel()
	_, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), ManagedDatabaseSpec{
		Region: "Atlantis", Provider: "aws", Engine: "postgres", CPU: 2, RAM: 4,
	})
	if err == nil {
		t.Fatal("expected region-not-found error, got nil")
	}
	var nf ErrRegionNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

func TestManagedDatabaseValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec ManagedDatabaseSpec
	}{
		{"missing region", ManagedDatabaseSpec{Provider: "aws", Engine: "postgres", CPU: 2, RAM: 4}},
		{"missing provider", ManagedDatabaseSpec{Region: "Frankfurt", Engine: "postgres", CPU: 2, RAM: 4}},
		{"unknown provider", ManagedDatabaseSpec{Region: "Frankfurt", Provider: "vultr", CPU: 2, RAM: 4}},
		{"bad engine", ManagedDatabaseSpec{Region: "Frankfurt", Provider: "aws", Engine: "oracle", CPU: 2, RAM: 4}},
		{"bad cpu", ManagedDatabaseSpec{Region: "Frankfurt", Provider: "aws", Engine: "postgres", CPU: 0, RAM: 4}},
		{"bad ram", ManagedDatabaseSpec{Region: "Frankfurt", Provider: "aws", Engine: "postgres", CPU: 2, RAM: 0}},
		{"bad storage", ManagedDatabaseSpec{Region: "Frankfurt", Provider: "aws", Engine: "postgres", CPU: 2, RAM: 4, StorageGB: -1}},
	}
	for _, c := range cases {
		if _, err := TranslateManagedDatabase(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}

// ── DATA-SAFETY GUARD TESTS (SPEC §5.6) ──────────────────────────────────────

// mkPlan builds a baseline resolved plan for the guard tests.
func mkPlan(t *testing.T, spec ManagedDatabaseSpec) *ManagedDatabasePlan {
	t.Helper()
	if spec.Region == "" {
		spec.Region = "Frankfurt"
	}
	if spec.Provider == "" {
		spec.Provider = "aws"
	}
	if spec.Engine == "" {
		spec.Engine = "postgres"
	}
	if spec.CPU == 0 {
		spec.CPU = 2
	}
	if spec.RAM == 0 {
		spec.RAM = 4
	}
	if spec.Name == "" {
		spec.Name = "app-db"
	}
	p, err := TranslateManagedDatabase(context.Background(), MustEmbedded(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

// TestDataSafetyEncryptionFlipBlocked asserts that toggling encryption on an
// EXISTING DB is blocked at plan time (the 2026-06-15 incident class).
func TestDataSafetyEncryptionFlipBlocked(t *testing.T) {
	t.Parallel()
	prior := mkPlan(t, ManagedDatabaseSpec{Encrypted: false})
	next := mkPlan(t, ManagedDatabaseSpec{Encrypted: true})
	err := CheckManagedDatabaseDataSafety(prior, next)
	if err == nil {
		t.Fatal("expected data-safety force-replace error for encryption flip, got nil")
	}
	var ds ErrDataSafetyForceReplace
	if !errors.As(err, &ds) {
		t.Fatalf("expected ErrDataSafetyForceReplace, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error should name the encrypted attribute, got %v", err)
	}
	if !strings.Contains(err.Error(), "snapshot-restore") {
		t.Errorf("error should direct to snapshot-restore migration, got %v", err)
	}
	if !strings.Contains(err.Error(), "2026-06-15") {
		t.Errorf("error should reference the 2026-06-15 incident, got %v", err)
	}
}

// TestDataSafetyEngineChangeBlocked asserts an engine change (downgrade /
// cross-engine) is blocked.
func TestDataSafetyEngineChangeBlocked(t *testing.T) {
	t.Parallel()
	prior := mkPlan(t, ManagedDatabaseSpec{Engine: "postgres"})
	next := mkPlan(t, ManagedDatabaseSpec{Engine: "mysql"})
	if err := CheckManagedDatabaseDataSafety(prior, next); err == nil {
		t.Fatal("expected force-replace error for engine change, got nil")
	} else if !strings.Contains(err.Error(), "engine") {
		t.Errorf("error should name the engine change, got %v", err)
	}
}

// TestDataSafetyIdentifierChangeBlocked asserts a DB identifier/name change is
// blocked.
func TestDataSafetyIdentifierChangeBlocked(t *testing.T) {
	t.Parallel()
	prior := mkPlan(t, ManagedDatabaseSpec{Name: "app-db"})
	next := mkPlan(t, ManagedDatabaseSpec{Name: "app-db-v2"})
	if err := CheckManagedDatabaseDataSafety(prior, next); err == nil {
		t.Fatal("expected force-replace error for identifier change, got nil")
	} else if !strings.Contains(err.Error(), "identifier") {
		t.Errorf("error should name the identifier change, got %v", err)
	}
}

// TestDataSafetyStorageTypeChangeBlocked asserts a class-family (storage-type)
// change is blocked. db.t3.medium (db.t3) -> db.m5.large (db.m5) is a family
// change at the same cpu... use a sizing that crosses families.
func TestDataSafetyStorageTypeChangeBlocked(t *testing.T) {
	t.Parallel()
	// 2vCPU/8GiB on AWS postgres resolves to db.t3.large (family db.t3);
	// 2vCPU/8GiB also has db.m5.large (family db.m5) — but ResolveDBClass picks
	// the lexicographically smallest (db.m5.large < db.t3.large). To force a
	// family delta we compare db.t3.medium (2/4) vs db.m5.xlarge (4/16).
	prior := mkPlan(t, ManagedDatabaseSpec{CPU: 2, RAM: 4}) // db.t3.medium / db.t3
	next := mkPlan(t, ManagedDatabaseSpec{CPU: 4, RAM: 16}) // db.m5.xlarge / db.m5
	if prior.Family == next.Family {
		t.Fatalf("test precondition: families should differ, got %q == %q", prior.Family, next.Family)
	}
	if err := CheckManagedDatabaseDataSafety(prior, next); err == nil {
		t.Fatal("expected force-replace error for class-family/storage-type change, got nil")
	} else if !strings.Contains(err.Error(), "storage_type/class_family") {
		t.Errorf("error should name the storage_type/class_family change, got %v", err)
	}
}

// TestDataSafetyFreshCreateSafe asserts a fresh create (no prior state) never
// triggers the guard.
func TestDataSafetyFreshCreateSafe(t *testing.T) {
	t.Parallel()
	next := mkPlan(t, ManagedDatabaseSpec{Encrypted: true})
	if err := CheckManagedDatabaseDataSafety(nil, next); err != nil {
		t.Errorf("fresh create should be safe, got %v", err)
	}
}

// TestDataSafetyInPlaceSizeChangeAllowed asserts an in-place resize within the
// SAME family + a storage increase + an HA toggle are allowed (no replacement).
func TestDataSafetyInPlaceSizeChangeAllowed(t *testing.T) {
	t.Parallel()
	// db.t3.micro (2/1) and db.t3.small (2/2) are both family db.t3 — same-family
	// resize, in-place, allowed.
	prior := mkPlan(t, ManagedDatabaseSpec{CPU: 2, RAM: 1, StorageGB: 20, HA: false})
	next := mkPlan(t, ManagedDatabaseSpec{CPU: 2, RAM: 2, StorageGB: 100, HA: true})
	if prior.Family != next.Family {
		t.Fatalf("test precondition: same family expected, got %q vs %q", prior.Family, next.Family)
	}
	if err := CheckManagedDatabaseDataSafety(prior, next); err != nil {
		t.Errorf("same-family resize + storage increase + HA toggle should be in-place, got %v", err)
	}
}

// TestDataSafetyMultipleViolationsListed asserts the guard reports every
// offending change at once.
func TestDataSafetyMultipleViolationsListed(t *testing.T) {
	t.Parallel()
	prior := mkPlan(t, ManagedDatabaseSpec{Name: "app-db", Engine: "postgres", Encrypted: false})
	next := mkPlan(t, ManagedDatabaseSpec{Name: "app-db-2", Engine: "mysql", Encrypted: true})
	err := CheckManagedDatabaseDataSafety(prior, next)
	if err == nil {
		t.Fatal("expected force-replace error, got nil")
	}
	var ds ErrDataSafetyForceReplace
	if !errors.As(err, &ds) {
		t.Fatalf("expected ErrDataSafetyForceReplace, got %T", err)
	}
	if len(ds.Violations) < 3 {
		t.Errorf("expected >= 3 violations (encrypted, engine, identifier), got %d: %+v", len(ds.Violations), ds.Violations)
	}
}

// ── RENDER TESTS ─────────────────────────────────────────────────────────────

// TestRenderManagedDatabaseAWS asserts the RDS shaping: db_subnet_group +
// aws_db_instance with the production-safe defaults (deletion_protection,
// final snapshot), the catalog instance class, encryption + multi_az, the SG
// wiring, and ASCII output.
func TestRenderManagedDatabaseAWS(t *testing.T) {
	t.Parallel()
	plan := mkPlan(t, ManagedDatabaseSpec{
		Name: "app-db", Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 50,
		HA: true, Encrypted: true,
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
		SecurityGroup: "production-db",
	})
	hcl, err := RenderManagedDatabaseHCL(*plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_db_subnet_group" "app-db_subnet_group"`,
		`subnet_ids = [data.aws_subnet.production_1.id, data.aws_subnet.production_2.id]`,
		`resource "aws_db_instance" "app-db"`,
		`identifier              = "app-db"`,
		`engine                  = "postgres"`,
		`engine_version          = "16"`,
		`instance_class          = "db.t3.medium"`,
		`allocated_storage       = 50`,
		`storage_encrypted       = true`,
		`multi_az                = true`,
		`db_subnet_group_name    = aws_db_subnet_group.app-db_subnet_group.name`,
		`vpc_security_group_ids  = [aws_security_group.production-db.id]`,
		`deletion_protection     = true`,
		`skip_final_snapshot     = false`,
		`final_snapshot_identifier = "app-db-final"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws MDB HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL is not ASCII:\n%s", hcl)
	}
}

// TestRenderManagedDatabaseAWSTestOverride asserts the test-only override emits
// deletion_protection=false + skip_final_snapshot=true and NO final snapshot id.
func TestRenderManagedDatabaseAWSTestOverride(t *testing.T) {
	t.Parallel()
	plan := mkPlan(t, ManagedDatabaseSpec{
		Name: "app-db", Engine: "postgres", CPU: 2, RAM: 1, StorageGB: 20,
		DeletionProtection: boolPtr(false), SkipFinalSnapshot: boolPtr(true),
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	})
	hcl, _ := RenderManagedDatabaseHCL(*plan)
	if !strings.Contains(hcl, `deletion_protection     = false`) {
		t.Errorf("test override should emit deletion_protection=false:\n%s", hcl)
	}
	if !strings.Contains(hcl, `skip_final_snapshot     = true`) {
		t.Errorf("test override should emit skip_final_snapshot=true:\n%s", hcl)
	}
	if strings.Contains(hcl, "final_snapshot_identifier") {
		t.Errorf("test override (skip_final_snapshot) should emit NO final_snapshot_identifier:\n%s", hcl)
	}
}

func TestRenderManagedDatabaseGCP(t *testing.T) {
	t.Parallel()
	plan := mkPlan(t, ManagedDatabaseSpec{
		Name: "app-db", Provider: "gcp", Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 30,
		HA: true, Network: "production",
	})
	hcl, err := RenderManagedDatabaseHCL(*plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "google_sql_database_instance" "app-db"`,
		`region              = "europe-west3"`,
		`database_version    = "POSTGRES_16"`,
		`deletion_protection = true`,
		`tier              = "db-custom-2-4096"`,
		`availability_type = "REGIONAL"`,
		`private_network = google_compute_network.production.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("gcp MDB HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestRenderManagedDatabaseDO(t *testing.T) {
	t.Parallel()
	plan := mkPlan(t, ManagedDatabaseSpec{
		Name: "app-db", Provider: "digitalocean", Engine: "postgres", CPU: 2, RAM: 4, StorageGB: 30,
		HA: true, Network: "production",
	})
	hcl, err := RenderManagedDatabaseHCL(*plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_database_cluster" "app-db"`,
		`engine     = "pg"`,
		`size       = "db-s-2vcpu-4gb"`,
		`region     = "fra1"`,
		`node_count = 2`,
		`private_network_uuid = digitalocean_vpc.production.id`,
		`prevent_destroy = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do MDB HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestRenderManagedDatabaseUnsupportedProvider asserts the renderer rejects an
// unknown provider (defence in depth for a hand-built plan).
func TestRenderManagedDatabaseUnsupportedProvider(t *testing.T) {
	t.Parallel()
	if _, err := RenderManagedDatabaseHCL(ManagedDatabasePlan{Provider: "vultr"}); err == nil {
		t.Fatal("expected render error for unsupported provider, got nil")
	}
}
