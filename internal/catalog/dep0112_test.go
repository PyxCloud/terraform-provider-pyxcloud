package catalog

import (
	"context"
	"testing"
)

// DEP-01.12: the V1 DigitalOcean size set must resolve for every V1 region
// (nyc3, fra1, sgp1) — both droplet sizes (TranslateVM, end to end including
// the OS image resolution) and managed Postgres classes
// (TranslateManagedDatabase).
func TestDOV1SizesTranslateEveryRegion(t *testing.T) {
	t.Setenv("PYXCLOUD_BYPASS_JIT_CHECK", "true")
	ctx := context.Background()

	// abstract region_name (region_catalog.csv) -> csp_region (V1)
	regions := []struct {
		regionName string
		cspRegion  string
	}{
		{"New York", "nyc3"},
		{"Frankfurt", "fra1"},
		{"Singapore", "sgp1"},
	}

	vmSizes := []struct {
		cpu, ram int
		sku      string
	}{
		{1, 0, "s-1vcpu-512mb-10gb"},
		{1, 1, "s-1vcpu-1gb"},
		{1, 2, "s-1vcpu-2gb"},
		{2, 2, "s-2vcpu-2gb"},
		{2, 4, "s-2vcpu-4gb"},
		{4, 8, "s-4vcpu-8gb"},
	}
	mdbSizes := []struct {
		cpu, ram int
		class    string
	}{
		{1, 1, "db-s-1vcpu-1gb"},
		{1, 2, "db-s-1vcpu-2gb"},
		{2, 4, "db-s-2vcpu-4gb"},
		{4, 8, "db-s-4vcpu-8gb"},
	}

	cat := MustEmbedded()

	for _, s := range vmSizes {
		t.Run("resolveSKU/"+s.sku, func(t *testing.T) {
			row, err := cat.ResolveSKU(ctx, "do", "nyc3", ArchX8664, s.cpu, s.ram)
			if err != nil {
				t.Fatalf("ResolveSKU(%s): %v", s.sku, err)
			}
			if row.Name != s.sku {
				t.Errorf("sku = %q, want %q", row.Name, s.sku)
			}
		})
	}
	for _, r := range regions {
		for _, s := range vmSizes {
			if s.cpu == 1 && s.ram == 0 {
				// s-1vcpu-512mb-10gb is 0.5 GiB: TranslateVM's spec validation
				// floors RAM at 1 GiB, so it is exercised via ResolveSKU above.
				continue
			}
			t.Run("vm/"+r.cspRegion+"/"+s.sku, func(t *testing.T) {
				plan, err := TranslateVM(ctx, cat, VMSpec{
					Name: "web", Region: r.regionName, Provider: "digitalocean",
					Architecture: ArchX8664, OS: "ubuntu",
					CPU: s.cpu, RAM: s.ram,
					Network: "production", Subnet: "production", SecurityGroup: "production-fw",
				})
				if err != nil {
					t.Fatalf("TranslateVM(%s, %dvCPU/%dGiB): %v", r.cspRegion, s.cpu, s.ram, err)
				}
				if plan.CSPRegion != r.cspRegion {
					t.Errorf("csp_region = %q, want %q", plan.CSPRegion, r.cspRegion)
				}
				if plan.InstanceType != s.sku {
					t.Errorf("instance_type = %q, want %q", plan.InstanceType, s.sku)
				}
				if plan.Image == "" {
					t.Error("image is empty")
				}
			})
		}
		for _, s := range mdbSizes {
			t.Run("mdb/"+r.cspRegion+"/"+s.class, func(t *testing.T) {
				plan, err := TranslateManagedDatabase(ctx, cat, ManagedDatabaseSpec{
					Name: "app-db", Region: r.regionName, Provider: "digitalocean",
					Engine: "postgres", CPU: s.cpu, RAM: s.ram, StorageGB: 30,
					Network: "production",
				})
				if err != nil {
					t.Fatalf("TranslateManagedDatabase(%s, %dvCPU/%dGiB): %v", r.cspRegion, s.cpu, s.ram, err)
				}
				if plan.CSPRegion != r.cspRegion {
					t.Errorf("csp_region = %q, want %q", plan.CSPRegion, r.cspRegion)
				}
				if plan.DBClass != s.class {
					t.Errorf("db_class = %q, want %q", plan.DBClass, s.class)
				}
			})
		}
	}
}
