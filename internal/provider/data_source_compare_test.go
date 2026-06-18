package provider

import (
	"context"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

func TestCompareComponentsSchemaIsFlat(t *testing.T) {
	t.Parallel()
	d := NewCompareDataSource()
	resp := &fwdatasource.SchemaResponse{}
	d.Schema(context.Background(), fwdatasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("compare schema diagnostics: %+v", resp.Diagnostics)
	}

	components, ok := resp.Schema.Attributes["components"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("components schema = %T, want schema.ListNestedAttribute", resp.Schema.Attributes["components"])
	}
	attrs := components.NestedObject.Attributes
	for _, name := range []string{"path", "name", "type", "count", "architecture", "cpu", "ram", "os_name", "min", "max", "desired", "health"} {
		if _, ok := attrs[name]; !ok {
			t.Errorf("expected flat component attribute %q", name)
		}
	}
	if _, ok := attrs["vm"]; ok {
		t.Error("component schema must not expose nested vm block")
	}
	if _, ok := attrs["scale_group"]; ok {
		t.Error("component schema must not expose nested scale_group block")
	}
}
