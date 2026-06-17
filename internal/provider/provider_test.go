package provider

import (
	"context"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
)

// TestProviderSchema is a smoke test: the provider's schema must validate clean
// under the framework's schema diagnostics.
func TestProviderSchema(t *testing.T) {
	t.Parallel()
	p := New("test")()

	resp := &fwprovider.SchemaResponse{}
	p.Schema(context.Background(), fwprovider.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("provider schema diagnostics: %+v", resp.Diagnostics)
	}
	if _, ok := resp.Schema.Attributes["endpoint"]; !ok {
		t.Error("expected 'endpoint' provider attribute")
	}
	if _, ok := resp.Schema.Attributes["token"]; !ok {
		t.Error("expected 'token' provider attribute")
	}
}

// TestResourceSchema validates the pyxcloud_topology resource schema.
func TestResourceSchema(t *testing.T) {
	t.Parallel()
	r := NewTopologyResource()

	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("resource schema diagnostics: %+v", resp.Diagnostics)
	}
	for _, attr := range []string{"id", "name", "provider", "region", "components", "network", "network_plan"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected '%s' attribute on pyxcloud_topology", attr)
		}
	}

	// Metadata type name.
	mresp := &fwresource.MetadataResponse{}
	r.Metadata(context.Background(), fwresource.MetadataRequest{ProviderTypeName: "pyxcloud"}, mresp)
	if mresp.TypeName != "pyxcloud_topology" {
		t.Errorf("expected type name pyxcloud_topology, got %s", mresp.TypeName)
	}
}

// TestDataSourceSchema validates the pyxcloud_compare data source schema.
func TestDataSourceSchema(t *testing.T) {
	t.Parallel()
	ds := NewCompareDataSource()

	resp := &fwdatasource.SchemaResponse{}
	ds.Schema(context.Background(), fwdatasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("data source schema diagnostics: %+v", resp.Diagnostics)
	}
	for _, attr := range []string{"components", "candidates", "results", "cheapest"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected '%s' attribute on pyxcloud_compare", attr)
		}
	}

	mresp := &fwdatasource.MetadataResponse{}
	ds.Metadata(context.Background(), fwdatasource.MetadataRequest{ProviderTypeName: "pyxcloud"}, mresp)
	if mresp.TypeName != "pyxcloud_compare" {
		t.Errorf("expected type name pyxcloud_compare, got %s", mresp.TypeName)
	}
}

// TestProviderInterfaces asserts the factories return the expected framework types.
func TestProviderInterfaces(t *testing.T) {
	t.Parallel()
	p := New("test")()
	if got := len(p.Resources(context.Background())); got != 3 {
		t.Errorf("expected 3 resources (topology, migration, environment), got %d", got)
	}
	if got := len(p.DataSources(context.Background())); got != 1 {
		t.Errorf("expected 1 data source, got %d", got)
	}

	var _ fwresource.Resource = NewTopologyResource()
	var _ fwresource.Resource = NewMigrationResource()
	var _ fwdatasource.DataSource = NewCompareDataSource()
}

// TestStubCompareRanksCheapestFirst exercises the stub client's pricing path
// end-to-end: a topology priced across candidates must come back cheapest-first.
func TestStubCompareRanksCheapestFirst(t *testing.T) {
	t.Parallel()
	c := client.NewStub(client.Config{})
	topo := client.Topology{
		Name: "web",
		Components: []client.Component{
			{Name: "app", Type: "virtual-machine", Count: 2,
				VM: &client.VMType{Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu"}},
			{Name: "db", Type: "managed-database", Count: 1},
		},
	}
	candidates := []client.Candidate{
		{Provider: "aws", Region: "EU West"},
		{Provider: "gcp", Region: "EU West"},
		{Provider: "digitalocean", Region: "EU West"},
	}
	costs, err := c.Compare(context.Background(), topo, candidates)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if len(costs) != 3 {
		t.Fatalf("expected 3 results, got %d", len(costs))
	}
	for i := 1; i < len(costs); i++ {
		if costs[i].HourlyUSD < costs[i-1].HourlyUSD {
			t.Fatalf("results not cheapest-first: %+v", costs)
		}
	}
	for _, c := range costs {
		if c.MonthlyUSD <= 0 || c.HourlyUSD <= 0 {
			t.Errorf("expected positive cost, got %+v", c)
		}
	}
}
