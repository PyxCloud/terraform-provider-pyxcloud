package catalog

// adapter_test.go — end-to-end coverage of the tier-2 adapter providers
// (roadmap P5/P6): translate resolves through the SHARED catalog indexes (no
// second engine), render dispatches through the manifest templates, and
// unsupported component kinds fall through to the standard self-host mitigation
// or surface the honest hard error.

import (
	"strings"
	"testing"
)

func TestAdapterTranslateAndRenderVM(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		provider, region, wantRegion, wantType, wantResource string
	}{
		{"tencent", "Guangzhou", "ap-guangzhou", "S5.MEDIUM4", "tencentcloud_instance"},
		{"hetzner", "Falkenstein", "fsn1", "cx22", "hcloud_server"},
		{"vultr", "New Jersey", "ewr", "vc2-2c-4gb", "vultr_instance"},
		{"scaleway", "Paris", "fr-par-1", "DEV1-S", "scaleway_instance_server"},
		{"rackspace", "London", "LON", "performance1-4", "openstack_compute_instance_v2"},
	}
	for _, tc := range cases {
		plan, err := TranslateVM(ctx(), cat, VMSpec{
			Name: "web", Region: tc.region, Provider: tc.provider,
			CPU: 2, RAM: 4, OS: "ubuntu", OSVersion: "22.04",
			Network: "production", Subnet: "production-subnet-1",
			SecurityGroup: "app-sg", Count: 1,
		})
		if err != nil {
			t.Errorf("%s: TranslateVM: %v", tc.provider, err)
			continue
		}
		if plan.CSPRegion != tc.wantRegion {
			t.Errorf("%s: region = %q, want %q", tc.provider, plan.CSPRegion, tc.wantRegion)
		}
		if plan.InstanceType != tc.wantType {
			t.Errorf("%s: SKU = %q, want %q", tc.provider, plan.InstanceType, tc.wantType)
		}
		hcl, err := RenderVMHCL(plan)
		if err != nil {
			t.Errorf("%s: RenderVMHCL: %v", tc.provider, err)
			continue
		}
		if !strings.Contains(hcl, tc.wantResource) {
			t.Errorf("%s: rendered HCL missing %s:\n%s", tc.provider, tc.wantResource, hcl)
		}
		if !strings.Contains(hcl, "pyxcloud adapter render") {
			t.Errorf("%s: rendered HCL missing provenance header:\n%s", tc.provider, hcl)
		}
	}
}

func TestAdapterNetworkAndSGRender(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	net, err := TranslateNetwork(ctx(), cat, NetworkSpec{
		Name: "production", Region: "Amsterdam", Provider: "vultr", CIDR: "10.0.0.0/16",
		Subnets: []string{"10.0.1.0/24"},
	})
	if err != nil {
		t.Fatalf("TranslateNetwork(vultr): %v", err)
	}
	if net.CSPRegion != "ams" || net.CSP != cspVultr {
		t.Errorf("vultr network plan: %+v", net)
	}
	hcl, err := RenderHCL(net)
	if err != nil || !strings.Contains(hcl, "vultr_vpc2") {
		t.Errorf("RenderHCL(vultr net): %v\n%s", err, hcl)
	}

	sg, err := TranslateSecurityGroup(ctx(), cat, SecurityGroupSpec{
		Name: "app-sg", Region: "Amsterdam", Provider: "vultr", Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 443, ToPort: 443, CIDRs: []string{"0.0.0.0/0"}},
		},
	})
	if err != nil {
		t.Fatalf("TranslateSecurityGroup(vultr): %v", err)
	}
	sgHcl, err := RenderSGHCL(sg)
	if err != nil || !strings.Contains(sgHcl, "vultr_firewall_group") {
		t.Errorf("RenderSGHCL(vultr): %v\n%s", err, sgHcl)
	}
}

func TestAdapterManagedDatabase(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateManagedDatabase(ctx(), cat, ManagedDatabaseSpec{
		Name: "db1", Region: "Guangzhou", Provider: "tencent", Engine: "postgres",
		Version: "16", CPU: 2, RAM: 4, StorageGB: 20, Network: "production",
		SecurityGroup: "app-sg",
	})
	if err != nil {
		t.Fatalf("TranslateMDB(tencent): %v", err)
	}
	hcl, err := RenderManagedDatabaseHCL(plan)
	if err != nil || !strings.Contains(hcl, "tencentcloud_postgresql") {
		t.Errorf("RenderManagedDatabaseHCL(tencent): %v\n%s", err, hcl)
	}

	// hetzner has NO native managed DB: translation must surface the clean
	// native error, never silently substitute a different database.
	if _, err := TranslateManagedDatabase(ctx(), cat, ManagedDatabaseSpec{
		Name: "db1", Region: "Falkenstein", Provider: "hetzner", Engine: "postgres",
		Version: "16", CPU: 2, RAM: 4, StorageGB: 20, Network: "production",
		SecurityGroup: "app-sg",
	}); err == nil {
		t.Error("TranslateMDB(hetzner) should error (no native managed DB in catalog)")
	}
}

func TestAdapterMitigationFallbackForCache(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Tier-2 adapters have no native cache template: the canonical cache MUST
	// mitigate (self-host on a VM) using the provider's own VM renderer.
	if !Mitigatable("cache") || NativelySupported("cache", "hetzner") {
		t.Fatal("cache on hetzner must take the self-host mitigation path")
	}
	// The assemble-level mitigation renders through the standard VM path, which
	// dispatches to the adapter template (no second engine).
	docs, err := mitigateComponent(ctx(), cat, "hetzner", "Falkenstein",
		AssembleComponent{Name: "cache1", Type: "cache"},
		"production", "production-subnet-1", "app-sg")
	if err != nil {
		t.Fatalf("mitigateComponent(hetzner cache): %v", err)
	}
	if len(docs) != 1 || !strings.Contains(docs[0], "hcloud_server") {
		t.Errorf("hetzner cache mitigation should render an hcloud_server VM: %v", docs)
	}
}
