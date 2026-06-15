package provider

import (
	"context"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestResourceTranslateNetwork exercises the resource's network translation
// wiring end-to-end through the embedded catalog.
func TestResourceTranslateNetwork(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network: &networkModel{
			CIDR:    types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{types.StringValue("10.0.1.0/24"), types.StringValue("10.0.2.0/24")},
		},
	}

	plan, err := r.translateNetwork(context.Background(), m)
	if err != nil {
		t.Fatalf("translateNetwork: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a network plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_vpc" {
		t.Errorf("resource_type = %q, want aws_vpc", plan.ResourceType.ValueString())
	}
	if len(plan.Subnets) != 2 {
		t.Fatalf("want 2 subnets, got %d", len(plan.Subnets))
	}
	if plan.Subnets[0].Zone.ValueString() != "eu-west-1a" {
		t.Errorf("subnet 0 zone = %q, want eu-west-1a", plan.Subnets[0].Zone.ValueString())
	}
}

// TestResourceTranslateNetworkNil returns no plan when no network is declared.
func TestResourceTranslateNetworkNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateNetwork(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateNetwork: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no network declared, got %+v", plan)
	}
}

// TestResourceTranslateNetworkMissingCatalogRegion surfaces a hard error.
func TestResourceTranslateNetworkMissingCatalogRegion(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	_, err := r.translateNetwork(context.Background(), topologyModel{
		Name:     types.StringValue("x"),
		Provider: types.StringValue("digitalocean"),
		Region:   types.StringValue("Dublin"), // no DO region for Dublin
		Network:  &networkModel{CIDR: types.StringValue("10.0.0.0/16")},
	})
	if err == nil {
		t.Fatal("expected hard error for Dublin/digitalocean, got nil")
	}
}
