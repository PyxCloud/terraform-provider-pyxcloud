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
	for _, attr := range []string{"id", "name", "cloud", "region", "components", "account_binding", "work_dir", "outputs"} {
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

func TestEnvironmentComponentsSchemaIsFlat(t *testing.T) {
	t.Parallel()
	r := NewEnvironmentResource()
	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("environment schema diagnostics: %+v", resp.Diagnostics)
	}

	components, ok := resp.Schema.Attributes["components"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("components schema = %T, want schema.ListNestedAttribute", resp.Schema.Attributes["components"])
	}
	attrs := components.NestedObject.Attributes
	for _, name := range []string{
		"path", "name", "type", "count", "architecture", "cpu", "ram", "os_name",
		"min", "max", "desired", "health", "user_data", "instance_profile", "root_disk_gb",
		"engine", "version", "storage_gb", "encrypted", "alb_listener_arn", "host_header",
		"scale_group", "assume_service", "managed_policy_arns", "inline_policies",
		"zone_id", "records", "listeners", "target_kind", "target_name",
	} {
		if _, ok := attrs[name]; !ok {
			t.Errorf("expected flat component attribute %q", name)
		}
	}
	for _, name := range []string{"vm", "managed_database", "attach_to_existing_alb", "iam", "dns", "load_balancer"} {
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
		Components: []envComponentModel{{
			Path:                types.StringValue("/0/Europe/0/Web-Net/0/app"),
			Name:                types.StringValue("app"),
			Type:                types.StringValue("virtual-machine-scale-group"),
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
