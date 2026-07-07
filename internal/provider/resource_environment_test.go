package provider

import (
	"context"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestEnvironmentSchemaHasDualModeSelector(t *testing.T) {
	t.Parallel()
	r := NewEnvironmentResource()
	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("environment schema diagnostics: %+v", resp.Diagnostics)
	}
	for _, attr := range []string{"id", "name", "cloud", "region", "expose", "pyx_virtual_machine", "account_binding", "work_dir", "outputs"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected '%s' attribute on pyxcloud_environment", attr)
		}
	}
	mresp := &fwresource.MetadataResponse{}
	r.Metadata(context.Background(), fwresource.MetadataRequest{ProviderTypeName: "pyxcloud"}, mresp)
	if mresp.TypeName != "pyxcloud_environment" {
		t.Errorf("type name = %s want pyxcloud_environment", mresp.TypeName)
	}
}

func TestEnvironmentSchemaHasVaultHABlock(t *testing.T) {
	t.Parallel()
	r := NewEnvironmentResource()
	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("environment schema diagnostics: %+v", resp.Diagnostics)
	}
	attr, ok := resp.Schema.Attributes["vault_ha"]
	if !ok {
		t.Fatal("expected 'vault_ha' attribute on pyxcloud_environment")
	}
	nested, ok := attr.(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("vault_ha schema = %T, want schema.SingleNestedAttribute", attr)
	}
	for _, field := range []string{"name", "seal", "transit_addr", "transit_token", "node_count", "reserved_ips"} {
		if _, ok := nested.Attributes[field]; !ok {
			t.Errorf("expected vault_ha.%s attribute", field)
		}
	}
}

func TestEnvironmentSchemaUsesPyxTypedComponentBlocks(t *testing.T) {
	t.Parallel()
	r := NewEnvironmentResource()
	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("environment schema diagnostics: %+v", resp.Diagnostics)
	}

	if _, ok := resp.Schema.Attributes["components"]; ok {
		t.Fatal("schema must not expose generic components block")
	}
	for _, componentType := range pyxComponentTypes {
		blockName := componentType.BlockName
		block, ok := resp.Schema.Attributes[blockName].(schema.ListNestedAttribute)
		if !ok {
			t.Fatalf("%s schema = %T, want schema.ListNestedAttribute", blockName, resp.Schema.Attributes[blockName])
		}
		attrs := block.NestedObject.Attributes
		for _, name := range []string{
			"path", "name", "count", "architecture", "cpu", "ram", "os_name",
			"min", "max", "desired", "health", "user_data", "instance_profile", "root_disk_gb",
			"engine", "version", "storage_gb", "encrypted", "alb_listener_arn", "host_header",
			"scale_group", "assume_service", "managed_policy_arns", "inline_policies",
			"zone_id", "records", "listeners", "target_kind", "target_name",
		} {
			if _, ok := attrs[name]; !ok {
				t.Errorf("%s missing flat component attribute %q", blockName, name)
			}
		}
		if _, ok := attrs["type"]; ok {
			t.Errorf("%s must not expose redundant type attribute", blockName)
		}
	}
	block := resp.Schema.Attributes["pyx_virtual_machine"].(schema.ListNestedAttribute)
	for _, name := range []string{"vm", "managed_database", "attach_to_existing_alb", "iam", "dns", "load_balancer"} {
		attrs := block.NestedObject.Attributes
		if _, ok := attrs[name]; ok {
			t.Errorf("component schema must not expose nested %s block", name)
		}
	}
}

func TestEnvironmentAssembleInputUsesFlatComponentFields(t *testing.T) {
	t.Parallel()
	r := &environmentResource{}
	in := r.assembleInputFromModel(environmentModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Expose:   []types.Int64{types.Int64Value(8080)},
		PyxAutoscaleVirtualMachineGroup: []envComponentModel{{
			Path:                types.StringValue("/0/Europe/0/Web-Net/0/app"),
			Name:                types.StringValue("app"),
			Count:               types.Int64Value(3),
			Architecture:        types.StringValue("x86_64"),
			CPU:                 types.StringValue("2"),
			RAM:                 types.StringValue("4"),
			OSName:              types.StringValue("ubuntu"),
			Min:                 types.Int64Value(1),
			Max:                 types.Int64Value(5),
			Desired:             types.Int64Value(3),
			Health:              types.StringValue("elb"),
			UserData:            types.StringValue("#!/bin/sh"),
			InstanceProfileName: types.StringValue("app-profile"),
			RootDiskGB:          types.Int64Value(30),
			Engine:              types.StringValue("postgres"),
			Version:             types.StringValue("16"),
			StorageGB:           types.Int64Value(100),
			Encrypted:           types.BoolValue(true),
		}},
	})
	if len(in.Components) != 1 {
		t.Fatalf("components = %d, want 1", len(in.Components))
	}
	if len(in.Expose) != 1 || in.Expose[0] != 8080 {
		t.Fatalf("expose = %+v, want [8080]", in.Expose)
	}
	comp := in.Components[0]
	if comp.Path != "/0/Europe/0/Web-Net/0/app" {
		t.Errorf("path = %q", comp.Path)
	}
	if comp.ScaleGroup == nil {
		t.Fatal("expected flat scale-group fields to populate catalog scale group")
	}
	if comp.ScaleGroup.Min != 1 || comp.ScaleGroup.Max != 5 || comp.ScaleGroup.Desired != 3 || comp.ScaleGroup.RootDiskGB != 30 {
		t.Errorf("scale_group = %+v", comp.ScaleGroup)
	}
	if comp.MDB == nil {
		t.Fatal("expected flat database fields to populate catalog managed database")
	}
	if comp.MDB.Engine != "postgres" || comp.MDB.Version != "16" || comp.MDB.StorageGB != 100 || !comp.MDB.Encrypted {
		t.Errorf("managed database = %+v", comp.MDB)
	}
}

func TestEnvironmentAssembleInputMapsVaultHABlock(t *testing.T) {
	t.Parallel()
	r := &environmentResource{}
	in := r.assembleInputFromModel(environmentModel{
		Name:     types.StringValue("prod"),
		Provider: types.StringValue("digitalocean"),
		Region:   types.StringValue("Frankfurt"),
		VaultHA: &envVaultHAModel{
			Seal:      types.StringValue("shamir"),
			NodeCount: types.Int64Value(3),
		},
	})
	if in.VaultHADroplet == nil {
		t.Fatal("expected vault_ha block to populate catalog.AssembleVaultHADroplet")
	}
	if in.VaultHADroplet.Seal != "shamir" {
		t.Errorf("seal = %q, want shamir", in.VaultHADroplet.Seal)
	}
	if in.VaultHADroplet.NodeCount != 3 {
		t.Errorf("node_count = %d, want 3", in.VaultHADroplet.NodeCount)
	}
}

func TestEnvironmentAssembleInputOmitsVaultHAWhenUnset(t *testing.T) {
	t.Parallel()
	r := &environmentResource{}
	in := r.assembleInputFromModel(environmentModel{
		Name:     types.StringValue("prod"),
		Provider: types.StringValue("digitalocean"),
		Region:   types.StringValue("Frankfurt"),
	})
	if in.VaultHADroplet != nil {
		t.Errorf("expected nil VaultHADroplet when vault_ha is unset, got %+v", in.VaultHADroplet)
	}
}

func TestEnvironmentAccessPolicyCanRequestInstanceProfile(t *testing.T) {
	t.Parallel()
	r := &environmentResource{}
	in := r.assembleInputFromModel(environmentModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		PyxAccessPolicy: []envComponentModel{{
			Name:                types.StringValue("api-role"),
			AssumeService:       types.StringValue("ec2.amazonaws.com"),
			InstanceProfileName: types.StringValue("true"),
		}},
	})

	if len(in.Components) != 1 {
		t.Fatalf("components = %d, want 1", len(in.Components))
	}
	if in.Components[0].IAM == nil {
		t.Fatal("expected access-policy to populate IAM")
	}
	if !in.Components[0].IAM.InstanceProfile {
		t.Fatal("expected instance_profile=\"true\" to request an IAM instance profile")
	}
}

func TestNormalizeEnvironmentComputedValuesFillsUnknownCounts(t *testing.T) {
	m := environmentModel{
		PyxAccessPolicy: []envComponentModel{{
			Name:  types.StringValue("role"),
			Count: types.Int64Unknown(),
		}},
		PyxMonitoring: []envComponentModel{{
			Name:  types.StringValue("obs"),
			Count: types.Int64Null(),
		}},
		PyxDNS: []envComponentModel{{
			Name:  types.StringValue("dns"),
			Count: types.Int64Value(2),
		}},
	}

	normalizeEnvironmentComputedValues(&m)

	if got := m.PyxAccessPolicy[0].Count.ValueInt64(); got != 1 {
		t.Fatalf("unknown access-policy count = %d, want 1", got)
	}
	if got := m.PyxMonitoring[0].Count.ValueInt64(); got != 1 {
		t.Fatalf("null monitoring count = %d, want 1", got)
	}
	if got := m.PyxDNS[0].Count.ValueInt64(); got != 2 {
		t.Fatalf("explicit dns count = %d, want 2", got)
	}
}

func TestEnvironmentModeSelector(t *testing.T) {
	t.Parallel()
	// Mode A: no account_binding.
	a := environmentModel{AccountBinding: types.StringNull()}
	if a.modeB() {
		t.Error("null account_binding must be Mode A")
	}
	if (environmentModel{AccountBinding: types.StringValue("")}).modeB() {
		t.Error("empty account_binding must be Mode A")
	}
	// Mode B: account_binding set.
	b := environmentModel{AccountBinding: types.StringValue("ab-123")}
	if !b.modeB() {
		t.Error("set account_binding must be Mode B")
	}
}
