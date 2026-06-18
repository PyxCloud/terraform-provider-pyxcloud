package provider

import (
	"context"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

func TestCompareSchemaUsesPyxTypedComponentBlocks(t *testing.T) {
	t.Parallel()
	d := NewCompareDataSource()
	resp := &fwdatasource.SchemaResponse{}
	d.Schema(context.Background(), fwdatasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("compare schema diagnostics: %+v", resp.Diagnostics)
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
		for _, name := range []string{"path", "name", "count", "architecture", "cpu", "ram", "os_name", "min", "max", "desired", "health"} {
			if _, ok := attrs[name]; !ok {
				t.Errorf("%s missing flat component attribute %q", blockName, name)
			}
		}
		if _, ok := attrs["type"]; ok {
			t.Errorf("%s must not expose redundant type attribute", blockName)
		}
	}
}
