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

// TestResourceTranslateSecurityGroup exercises the SG translation wiring end-to-
// end through the embedded catalog (pd-TF-SG).
func TestResourceTranslateSecurityGroup(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network:  &networkModel{CIDR: types.StringValue("10.0.0.0/16")},
		SecurityGroup: &securityGroupModel{
			Description: types.StringValue("web édge"), // non-ASCII -> sanitised
			Expose:      []types.Int64{types.Int64Value(80), types.Int64Value(443)},
			Rules: []securityRuleModel{{
				Direction: types.StringValue("ingress"),
				Protocol:  types.StringValue("tcp"),
				FromPort:  types.Int64Value(22),
				ToPort:    types.Int64Value(22),
				CIDRs:     []types.String{types.StringValue("10.0.0.0/16")},
				SourceSG:  types.StringValue(""),
			}},
		},
	}

	plan, err := r.translateSecurityGroup(context.Background(), m)
	if err != nil {
		t.Fatalf("translateSecurityGroup: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a security-group plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_security_group" {
		t.Errorf("resource_type = %q, want aws_security_group", plan.ResourceType.ValueString())
	}
	if !catalog.IsASCII(plan.Description.ValueString()) {
		t.Errorf("description not ASCII-sanitised: %q", plan.Description.ValueString())
	}
	if plan.NetworkName.ValueString() != "production" {
		t.Errorf("network_name = %q, want production", plan.NetworkName.ValueString())
	}
	// 2 expose + 1 explicit = 3 rules.
	if len(plan.Rules) != 3 {
		t.Fatalf("want 3 rules, got %d", len(plan.Rules))
	}
}

// TestResourceTranslateSecurityGroupNil returns no plan when none declared.
func TestResourceTranslateSecurityGroupNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateSecurityGroup(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateSecurityGroup: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no security_group declared, got %+v", plan)
	}
}

// TestResourceTranslateVM exercises the VM translation wiring end-to-end through
// the embedded catalog (pd-TF-EC2-VM): instance type from the virtual_machine
// catalog, image from the OS catalog, placement wired to the sibling subnet/SG.
func TestResourceTranslateVM(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network: &networkModel{
			CIDR:    types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{types.StringValue("10.0.1.0/24")},
		},
		SecurityGroup: &securityGroupModel{
			Name:   types.StringValue("production-web"),
			Expose: []types.Int64{types.Int64Value(80)},
		},
		VirtualMachine: &virtualMachineModel{
			Architecture: types.StringValue("x86_64"),
			CPU:          types.Int64Value(2),
			RAM:          types.Int64Value(4),
			OS:           types.StringValue("ubuntu"),
			Count:        types.Int64Value(2),
		},
	}

	plan, err := r.translateVM(context.Background(), m)
	if err != nil {
		t.Fatalf("translateVM: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a VM plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.InstanceType.ValueString() != "t3.medium" {
		t.Errorf("instance_type = %q, want t3.medium", plan.InstanceType.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_instance" {
		t.Errorf("resource_type = %q, want aws_instance", plan.ResourceType.ValueString())
	}
	if plan.SubnetName.ValueString() != "production-subnet-1" {
		t.Errorf("subnet_name = %q, want production-subnet-1", plan.SubnetName.ValueString())
	}
	if plan.SecurityGroup.ValueString() != "production-web" {
		t.Errorf("security_group = %q, want production-web", plan.SecurityGroup.ValueString())
	}
	if len(plan.Instances) != 2 {
		t.Fatalf("want 2 instances for count=2, got %d", len(plan.Instances))
	}
}

// TestResourceTranslateVMNil returns no plan when none declared.
func TestResourceTranslateVMNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateVM(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateVM: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no virtual_machine declared, got %+v", plan)
	}
}

// TestResourceTranslateVMSKUNoMatch surfaces a hard plan-time error.
func TestResourceTranslateVMSKUNoMatch(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	_, err := r.translateVM(context.Background(), topologyModel{
		Name:     types.StringValue("x"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		VirtualMachine: &virtualMachineModel{
			CPU: types.Int64Value(999), RAM: types.Int64Value(9999),
		},
	})
	if err == nil {
		t.Fatal("expected hard SKU no-match error, got nil")
	}
}

// TestResourceTranslateScaleGroup exercises the scale-group translation wiring
// end-to-end through the embedded catalog (pd-TF-ASG): instance type from the
// virtual_machine catalog (reused VM SKU), multi-AZ spread across the network's
// subnets, min/max/desired + health wired into the plan.
func TestResourceTranslateScaleGroup(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network: &networkModel{
			CIDR: types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{
				types.StringValue("10.0.1.0/24"),
				types.StringValue("10.0.2.0/24"),
				types.StringValue("10.0.3.0/24"),
			},
		},
		SecurityGroup: &securityGroupModel{
			Name:   types.StringValue("production-web"),
			Expose: []types.Int64{types.Int64Value(80)},
		},
		ScaleGroup: &scaleGroupModel{
			Architecture: types.StringValue("x86_64"),
			CPU:          types.Int64Value(2),
			RAM:          types.Int64Value(4),
			OS:           types.StringValue("ubuntu"),
			Min:          types.Int64Value(2),
			Max:          types.Int64Value(6),
			Desired:      types.Int64Value(3),
			Health:       types.StringValue("elb"),
		},
	}

	plan, err := r.translateScaleGroup(context.Background(), m)
	if err != nil {
		t.Fatalf("translateScaleGroup: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a scale-group plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.InstanceType.ValueString() != "t3.medium" {
		t.Errorf("instance_type = %q, want t3.medium (reused VM SKU)", plan.InstanceType.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_autoscaling_group" {
		t.Errorf("resource_type = %q, want aws_autoscaling_group", plan.ResourceType.ValueString())
	}
	if plan.Min.ValueInt64() != 2 || plan.Max.ValueInt64() != 6 || plan.Desired.ValueInt64() != 3 {
		t.Errorf("bounds = %d/%d/%d, want 2/6/3", plan.Min.ValueInt64(), plan.Max.ValueInt64(), plan.Desired.ValueInt64())
	}
	if plan.Health.ValueString() != "elb" {
		t.Errorf("health = %q, want elb", plan.Health.ValueString())
	}
	if len(plan.SubnetNames) != 3 || len(plan.Zones) != 3 {
		t.Errorf("want 3-way multi-AZ spread, got %d subnets / %d zones", len(plan.SubnetNames), len(plan.Zones))
	}
	if plan.SecurityGroup.ValueString() != "production-web" {
		t.Errorf("security_group = %q, want production-web", plan.SecurityGroup.ValueString())
	}
}

// TestResourceTranslateScaleGroupNil returns no plan when none declared.
func TestResourceTranslateScaleGroupNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateScaleGroup(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateScaleGroup: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no scale_group declared, got %+v", plan)
	}
}

// TestResourceTranslateScaleGroupDOUnsupported surfaces the DO hard error.
func TestResourceTranslateScaleGroupDOUnsupported(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	_, err := r.translateScaleGroup(context.Background(), topologyModel{
		Name:     types.StringValue("x"),
		Provider: types.StringValue("digitalocean"),
		Region:   types.StringValue("Frankfurt"),
		ScaleGroup: &scaleGroupModel{
			CPU: types.Int64Value(2), RAM: types.Int64Value(4),
			Min: types.Int64Value(1), Max: types.Int64Value(3),
		},
	})
	if err == nil {
		t.Fatal("expected DO unsupported error, got nil")
	}
}
