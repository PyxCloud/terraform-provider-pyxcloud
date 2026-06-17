package provider

import (
	"context"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
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
	for _, attr := range []string{"id", "name", "provider", "region", "components", "account_binding", "work_dir", "outputs"} {
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
